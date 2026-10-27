# netstack-rack-repro

A reproduction of
[tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946).
After a burst loss, TCP that gVisor's netstack terminates with RACK on
retransmits about two segments per retransmission timeout (RTO). A 2.8 MB
transfer then takes seconds, sometimes tens of seconds, on a 3 ms path. With
gVisor as Tailscale v1.104.2 ships it, every trial of the burst scenarios
below stalls. The same harness can run Linux kernel TCP as the sender, and
gVisor with the open upstream fixes.

Checked on 2026-10-27 against:

- Tailscale v1.104.2, which pins `gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72`;
- gVisor master `4777ee78` (2026-10-27) and its `go` branch export `a68437cf`;
- the open gVisor PRs, at their heads on that day:
  [#15432](https://github.com/google/gvisor/pull/15432) (`00aaa82b`),
  [#16081](https://github.com/google/gvisor/pull/16081) (`7d4f369c`),
  [#16495](https://github.com/google/gvisor/pull/16495) (`b7d9ced5`) and
  [#16419](https://github.com/google/gvisor/pull/16419) (`8c03c2e9`).

## What it does

One Go program builds two TCP endpoints and joins them with an emulated path.

- **Server (sender).** Either a gVisor stack set up the way
  `wgengine/netstack` sets it up in v1.104.2, or Linux kernel TCP
  (`-sender=linux`).
  - gVisor: CUBIC, with SACK and RACK at gVisor's defaults (on), 8 MiB / 6 MiB
    buffer limits, host GSO up to 64 KiB, MTU 1280 and RX checksum offload.
    GSO packets are split the way tstun splits them, with wireguard-go's
    `tun.GSOSplit`.
  - Linux: TCP sends over a TUN device in a new network namespace, through
    the same emulated path. CUBIC is set on the listening socket, so
    accepted connections never run BBR's init, which would turn on pacing.
- **Client (receiver).** A gVisor stack with gVisor's defaults, except an
  8 MiB receive buffer limit, so the receive window never limits the sender.
  It offers TCP timestamps, unless `-client-timestamps=false` strips them
  from its SYN, as for a Windows client with default settings.
- **Path.** 3 ms RTT plus up to 2 × 200 µs of seeded jitter that never
  reorders. 80 Mbit/s toward the client, no queue limit, and a scripted loss
  burst. A loss-free 1000-byte request and response takes a median 3.3 ms
  without jitter, and 3.5 ms with it.
- **Workload.** Like `curl --rate 20/m` in the A/B test of #21946: on one
  connection, an 8-byte request and a 2.8 MB response, with 3 s of idle time
  before each fetch. Each trial uses a new connection and two loss-free
  warm-up fetches.

The burst counts packets, not time, until it starts: in each measured fetch,
the first 1000 data packets are delivered, and then the scenario's drop rule
applies to whatever the sender sends, so a sender with a larger window at
that point loses more packets (see Results).

Each fetch reports:

- the time to completion;
- data packets on the wire, and how many were retransmissions (packets below
  the highest sequence number sent on the connection; gVisor's own counter
  counts segments, which with GSO can span several packets);
- RTOs (gVisor: the stack's `Timeouts` counter, with one connection at a time;
  Linux: `tcpi_total_rto`);
- the retransmissions between successive RTOs.

With `-trace` it also prints a per-100 ms table in the format of the excerpt
in #21946, and the gVisor sender's congestion state after each ACK, from
gVisor's TCP probe.

## Run it

```sh
./build-variants.sh                  # bin/repro-*, see below
bin/repro-pin -trace                 # gVisor as Tailscale 1.104.x ships it
bin/repro-pin -rack=false -trace     # the same with RACK off
./run-linux-sender.sh -trace         # Linux kernel TCP (needs root or user namespaces)
./run-matrix.sh default              # one scenario, every variant, saved under results/
```

The emulator spins for the last 2 ms before each delivery, so give each run
two CPUs of its own (`taskset -c 0,1`) and nothing else to do.
`run-matrix.sh` lists the scenarios and their flags. Go 1.27.3 is used
(`GOTOOLCHAIN=go1.27.3` downloads it).

`build-variants.sh` patches the pinned gVisor module, and gVisor master, with
the files in `patches/`. The patches carry only the PRs' non-test code, and
their code lines are the PRs' own.

| binary | gVisor |
|---|---|
| `repro-pin` | the module Tailscale v1.104.2 pins |
| `repro-pr15432` | pin + [#15432](https://github.com/google/gvisor/pull/15432): `SetPipe` skips RACK-lost segments, `DoRecovery` recomputes the pipe, `clone` keeps the marks |
| `repro-pr16081` | pin + [#16081](https://github.com/google/gvisor/pull/16081): RACK does not start fast recovery during RTO recovery |
| `repro-pr15432-16081` | pin + both |
| `repro-pr16081-notsecr`, `repro-pr15432-16081-notsecr` | + no TSecr test in `rackControl.update` (an experiment) |
| `repro-pr16081-tsecr`, `repro-pr15432-16081-tsecr` | + the TSecr fix in `patches/pin-4-tsecr-fix-code.patch` |
| `repro-master` | gVisor master `4777ee78` (`go` branch `a68437cf`) |
| `repro-master-pr15432-16081` | master + #15432 + #16081 |
| `repro-master-pr15432-16081-tsecr` | master + both + the TSecr fix |
| `repro-master-pr16495` | master + [#16495](https://github.com/google/gvisor/pull/16495) |
| `repro-master-pr15432-16081-16495-16419` | master + #15432, #16081, #16495 and [#16419](https://github.com/google/gvisor/pull/16419), stacked |

`gvisor-go-test-overlay.sh` builds a tree in which gVisor's own TCP test
suites run under `go test`, without Bazel: the `go` branch export of a master
commit, plus that commit's test files.

## Results (2026-10-27)

Eight trials per cell, each on a new connection, except long-lived: one
connection, five measured fetches. Each cell gives the fetch times (min–max)
and the RTOs per fetch. Raw outputs, with per-100 ms traces, are in
`results/2026-10-27/`, and `run-matrix.sh` has each scenario's flags. The
binaries for these tables were built from the Go sources of commit `e82f942`;
later commits change no behavior with these flags.

| sender | default | tail-tlp-survives | mid-flight-hole |
|---|---|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | 3.54–6.03 s (15–27 RTOs) | 3.52–5.82 s (15–26 RTOs) | 0.30–0.32 s (0 RTOs) |
| gVisor pin, RACK on, no GSO | 3.76–11.20 s (16–52 RTOs) | 3.54–51.28 s (15–245 RTOs) | 0.30–0.33 s (0 RTOs) |
| gVisor pin, RACK off (`TS_NETSTACK_TCP_RACK=false`) | 0.52–0.55 s (1 RTO) | 0.32–0.56 s (0–1 RTO) | 0.30–0.32 s (0 RTOs) |
| pin + #15432 | 0.64–0.66 s (1 RTO) | 0.32–0.65 s (0–1 RTO) | 0.30–0.33 s (0 RTOs) |
| pin + #16081 | 0.54–0.57 s (1 RTO) | 0.53–0.55 s (1 RTO) | 0.30–0.33 s (0 RTOs) |
| pin + #15432 + #16081 | 0.53–0.57 s (1 RTO) | 0.33–0.58 s (0–1 RTO) | 0.30–0.33 s (0 RTOs) |
| pin + #16081 + no TSecr test (experiment) | 0.54–0.57 s (1 RTO) | 0.55–0.57 s (1 RTO) | 0.30–0.33 s (0 RTOs) |
| pin + #15432 + #16081 + no TSecr test (experiment) | 0.53–0.55 s (1 RTO) | 0.32–0.55 s (0–1 RTO) | 0.30–0.33 s (0 RTOs) |
| gVisor master `4777ee78` | 5.18–10.55 s (23–49 RTOs) | 0.30–11.58 s (0–54 RTOs) | 0.30–0.32 s (0 RTOs) |
| master + #15432 + #16081 | 0.54–0.56 s (1 RTO) | 0.33–0.55 s (0–1 RTO) | 0.31–0.34 s (0 RTOs) |
| Linux 6.18 kernel TCP, CUBIC | 0.60–0.65 s (1 RTO) | 0.30–0.39 s (0 RTOs) | 0.30–0.33 s (0 RTOs) |

On one long-lived connection:

| sender | fetch 1–5 |
|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | 3.53, 42.12, 47.30, 48.65, 50.41 s |
| gVisor pin, RACK on, no GSO | 4.16, 39.21, 39.42, 45.85, 47.51 s |
| gVisor pin, RACK off (`TS_NETSTACK_TCP_RACK=false`) | 0.54, 0.55, 0.55, 0.56, 0.56 s |
| pin + #15432 | 0.67, 0.91, 0.90, 0.89, 0.89 s |
| pin + #16081 | 0.54, 0.54, 0.55, 0.56, 0.56 s |
| pin + #15432 + #16081 | 0.55, 0.55, 0.58, 0.56, 0.56 s |
| pin + #16081 + no TSecr test (experiment) | 0.57, 0.57, 0.60, 0.56, 0.58 s |
| pin + #15432 + #16081 + no TSecr test (experiment) | 0.54, 0.56, 0.57, 0.56, 0.56 s |
| gVisor master `4777ee78` | 10.35, 4.76, 4.76, 4.77, 4.77 s |
| master + #15432 + #16081 | 0.54, 0.55, 0.56, 0.55, 0.55 s |
| Linux 6.18 kernel TCP, CUBIC | 0.62, 0.61, 0.61, 0.61, 0.62 s |

- **The stall.** With RACK on, as Tailscale 1.104.x ships it, every trial of
  both tail-loss scenarios stalls for 3.5–6.0 s, at a median of two
  retransmissions per RTO, whether or not the loss probe gets through. GSO
  makes no difference. On one connection the stalls compound: 3.5 s, then
  42–50 s per fetch.
- **How long it lasts** depends on the window at the burst. In one trial
  without GSO, a spurious recovery in a warm-up fetch left ssthresh at 340.
  After the 3 s of idle time, the pinned CUBIC took cwnd from 10 to 494
  within 100 ms, about 490 packets were lost, and recovery took 245 RTOs:
  51.3 s. gVisor master's CUBIC (google/gvisor#15604) stops that growth: on
  one connection, master takes 4.8 s per fetch instead of 42–50 s, but it
  still stalls.
- **The fixes.** #16081, alone or with #15432, recovers with one RTO in every
  scenario here, as RACK off and Linux do. #15432 alone ends the crawl but
  leaves ssthresh at 2, so later fetches on a connection take about 0.9 s
  instead of 0.55 s.
- **Holes in mid-flight**, with SACKed data above them, take about 0.3 s with
  every sender: the stall needs a tail loss.
- **A delivered loss probe.** Linux recovers in tail-tlp-survives without an
  RTO in 8 of 8 trials (0.30–0.39 s), gVisor with both fixes in 1 of 8. RACK
  discards the probe's SACK because of its TSecr test; without that test
  (the experiment), gVisor recovers without an RTO in 7 of 8 trials.
- **Loss volume.** The drop rule takes whatever the sender sends, so a sender
  with a larger window at the burst loses more. In the default scenario,
  Linux retransmitted 202–272 packets per fetch, and gVisor as shipped
  35–58.

## Limits

- The loss is scripted and the path is simple: no Wi-Fi aggregation, no
  reordering, no ACK loss. It shows the recovery behavior, not how often a
  real network produces such a burst.
- Trials are not identical. The drop rule is fixed, but timing, and with it
  the window at the burst, varies from trial to trial.
- Before commit `dc2830b`, the path delivered packets up to 1.5 ms late (a
  loss-free 1000-byte fetch took a median 4.9 ms), and the Linux sender was
  paced by BBR's init. Results from that harness are in the history, in
  commit `398ebf4`.
