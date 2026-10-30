# netstack-rack-repro

A reproduction of
[tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946).
After a burst loss, TCP that gVisor's netstack terminates with RACK on
retransmits about two segments per retransmission timeout (RTO). A 2.8 MB
transfer then takes seconds, sometimes tens of seconds, on a 3 ms path. With
gVisor as Tailscale v1.104.2 ships it, every trial of the `default` and
`tail-tlp-survives` scenarios below stalls, and with a client that does not
offer TCP timestamps, the stall can last minutes. The same harness can run
Linux kernel TCP as the sender, gVisor with the open upstream fixes, and
gVisor with two further changes proposed here: one to RACK's TSecr test and
one to the loss probe.

Checked on 2026-10-27 against:

- Tailscale v1.104.2, which pins
  `gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72` (v1.104.4, released
  on 2026-10-28, pins the same);
- gVisor master `4777ee78` (2026-10-27) and its `go` branch export
  `a68437cf`;
- the open gVisor PRs, at their heads on that day:
  [#15432](https://github.com/google/gvisor/pull/15432) (`00aaa82b`),
  [#16081](https://github.com/google/gvisor/pull/16081) (`7d4f369c`),
  [#16495](https://github.com/google/gvisor/pull/16495) (`b7d9ced5`) and
  [#16419](https://github.com/google/gvisor/pull/16419) (`8c03c2e9`).

## What it does

One Go program builds two TCP endpoints and joins them with an emulated
path.

- **Server (sender).** Either a gVisor stack set up the way
  `wgengine/netstack` sets it up in v1.104.2 on Linux, or Linux kernel TCP
  (`-sender=linux`).
  - gVisor: CUBIC, with SACK and RACK at gVisor's defaults (on), 8 MiB / 6
    MiB buffer limits, host GSO up to 64 KiB, MTU 1280 and RX checksum
    offload. GSO packets are split the way tstun splits them with
    wireguard-go's `tun.GSOSplit`, reimplemented in `gso-split.go` (FIN and
    PSH on the last segment only, IP IDs incremented, full checksums). Other
    platforms have no host GSO; see the no-GSO rows.
  - Linux: TCP sends over a TUN device in a new network namespace, through
    the same emulated path. CUBIC is set on the listening socket, so
    accepted connections never run BBR's init, which would turn on pacing.
- **Client (receiver).** A gVisor stack with gVisor's defaults, except an
  8 MiB receive buffer limit, so the receive window never limits the sender.
  It offers TCP timestamps, unless `-client-timestamps=false` strips them
  from its SYN, as for a Windows client with default settings.
- **Path.** 3 ms RTT plus up to 2 × 200 µs of seeded jitter that never
  reorders. 80 Mbit/s toward the client, no queue limit, and a scripted loss
  burst. A loss-free 1000-byte response arrives a median 3.3 ms after the
  request without jitter, and 3.5 ms with it (see the message of commit
  `a48eb27`).
- **Workload.** Modeled on an A/B test of `tailscale serve` with
  `curl --rate 20/m` (one fetch every 3 s on one connection) and on the
  ~2.8 MB burst in the capture in #21946: on one connection, an 8-byte
  request and a 2.8 MB response, with 3 s of idle time before each fetch.
  Each trial uses a new connection and two loss-free warm-up fetches.

The burst counts packets, not time, until it starts: in each measured fetch,
the first 1000 data packets are delivered (the first 8 in the
end-of-transfer scenarios), and then the scenario's drop rule
applies to whatever the sender sends, so a sender with a larger window at
that point loses more packets (see Results).

Each fetch reports:

- the time to completion;
- data packets on the wire, and how many were retransmissions (packets below
  the highest sequence number sent on the connection; gVisor's own counter
  counts segments, which with GSO can span several packets);
- RTOs (gVisor: the stack's `Timeouts` counter, with one connection at a
  time; Linux: `tcpi_total_rto`);
- the retransmissions between successive RTOs.

With `-trace` it also prints a per-100 ms table in the format of the excerpt
in #21946, and the gVisor sender's congestion state after the first ACK in
each 100 ms, from gVisor's TCP probe.

## Run it

```sh
./build-variants.sh                  # bin/repro-*, see below
bin/repro-pin -trace                 # gVisor as Tailscale 1.104.x ships it
bin/repro-pin -rack=false -trace     # the same with RACK off
./run-linux-sender.sh -trace         # Linux kernel TCP (needs root or user namespaces)
./run-matrix.sh default              # one scenario, every variant, saved under results/
./run-paired-benchmarks.sh           # benchmark scenarios, builds interleaved, for benchstat
./run-revision-check.sh              # the same, first vs revised fixes after review
e2e/build.sh && e2e/run-e2e.sh up && e2e/run-matrix-e2e.sh   # real tailscaled (root; e2e/README.md)
```

The emulator spins for the last 2 ms before each delivery, so give each run
two CPUs of its own (`taskset -c 0,1`) and nothing else to do.
`run-matrix.sh` lists the scenarios and their flags. Go 1.27.3 is used
(`GOTOOLCHAIN=go1.27.3` downloads it).

`build-variants.sh` patches the pinned gVisor module, and gVisor master,
with the files in `patches/`. The `pin-1`, `pin-2` and `master-pr-*` patches
carry only the upstream PRs' non-test code, in the PRs' own lines. The
master patches leave out #15432's `resendSegment` change, which master
already has (`7c8d7811a`), and the four-PR stack merges one guard line (see
`build-variants.sh`). `pin-3` is an experiment; `pin-4` and
`master-tsecr-fix`, and `pin-5` and `master-tlp-new-data`, are the two fixes
proposed here, not upstream code.

| binary | gVisor |
|---|---|
| `repro-pin` | the module Tailscale v1.104.2 pins |
| `repro-pr15432` | pin + [#15432](https://github.com/google/gvisor/pull/15432): `SetPipe` skips RACK-lost segments, `DoRecovery` recomputes the pipe, `clone` keeps the marks, and `resendSegment` moves `writeNext` past the segment it retransmits |
| `repro-pr16081` | pin + [#16081](https://github.com/google/gvisor/pull/16081): RACK does not start fast recovery during RTO recovery |
| `repro-pr15432-16081` | pin + both |
| `repro-pr16081-notsecr`, `repro-pr15432-16081-notsecr` | + no TSecr test in `rackControl.update` at all (an experiment, not a proposed change) |
| `repro-pr16081-tsecr`, `repro-pr15432-16081-tsecr` | + the TSecr fix proposed here (`patches/pin-4-tsecr-fix-code.patch`) |
| `repro-pr15432-16081-tlpnew`, `repro-pr15432-16081-tsecr-tlpnew` | pin + both + the probe fix proposed here (`patches/pin-5-tlp-new-data-code.patch`), without or with the TSecr fix |
| `repro-master` | gVisor master `4777ee78` (`go` branch `a68437cf`) |
| `repro-master-tsecr` | master + the TSecr fix alone |
| `repro-master-pr15432-16081` | master + #15432 + #16081 |
| `repro-master-pr15432-16081-tsecr`, `repro-master-pr15432-16081-tlpnew`, `repro-master-pr15432-16081-tsecr-tlpnew` | master + both + the TSecr fix, the probe fix, or both (`patches/master-tsecr-fix-code.patch`, `patches/master-tlp-new-data-code.patch`) |
| `repro-master-pr16495` | master + [#16495](https://github.com/google/gvisor/pull/16495) |
| `repro-master-pr15432-16081-16495-16419` | master + #15432, #16081, #16495 and [#16419](https://github.com/google/gvisor/pull/16419), stacked |

The two proposed changes:

- **TSecr fix** ([#16659](https://github.com/google/gvisor/pull/16659)).
  `rackControl.update` skips an ACK of a retransmitted
  segment when the ACK's TSecr is older than the retransmission. A receiver
  echoes the timestamp of the most recent segment that advanced the left
  edge of its window (RCV.NXT; RFC 7323, section 4.3), so an ACK that SACKs
  a retransmission above a hole, such as a tail loss probe, usually fails
  that test, and RACK ignores it. The fix applies the test only when the ACK
  cumulatively acknowledges the segment. This departs from RFC 8985's
  section 6.2, step 2, which applies the TSecr test to SACKed segments too,
  although a SACK's TSecr does not show which copy arrived; the minimum-RTT
  test still applies. Linux makes the same split: its TSecr test
  (`tcp_skb_spurious_retrans`) covers cumulatively acknowledged segments,
  and a SACKed one passes to `tcp_rack_advance`, which checks only the
  minimum RTT.
- **Probe fix** ([#16660](https://github.com/google/gvisor/pull/16660)).
  `probeTimerExpired` sends new data as the loss probe only
  while the congestion window has room, so after a tail loss the probe is a
  retransmission, whose SACK RACK can ignore as ambiguous. The fix sends new
  data whenever there is some and the receive window allows, even with the
  congestion window full, but at most one segment past it, and sends no
  further probe while one is unacknowledged, as RFC 8985 (sections 7.3 and
  9.3) and Linux's `tcp_send_loss_probe` do.

`gvisor-go-test-overlay.sh` builds a tree in which gVisor's own TCP test
suites run under `go test`, without Bazel: the `go` branch export of a
master commit, plus that commit's test files.

## Results (2026-10-27 to 2026-10-30)

Eight trials per cell, each on a new connection, unless noted. Each cell
gives the fetch times (min–max) and the RTOs per fetch; a dash means the
variant did not run in that scenario. Raw outputs, with per-100 ms traces,
are in `results/2026-10-27/` and `results/2026-10-28/`, and `run-matrix.sh`
has each scenario's flags. The binaries were built from the Go sources of
commit `e82f942` for the first runs, and of `39551db`, whose Go sources did
not change until `ff9bfc2`, for the later variants and scenarios, including
the instrumented builds. Between the first two, no packet that the endpoints
send changed; `a7828ed` fixed how the trace counts retransmissions when a
connection reuses an earlier one's client port.

On 2026-10-28 the probe fix changed: it now sends no further probe while one
is unacknowledged, as RFC 8985 (section 7.3) requires. Every row with the
probe fix comes from runs of that version on 2026-10-28, with binaries built
from `39551db`'s Go sources and the patches of `a42662a` (`befad9f` later
added only a comment to them). The 2026-10-27 files of the previous version
(at most one segment past the window, but no limit of one probe at a time)
are kept. For gVisor senders, the tables count RTOs with gVisor's own
counter, from each fetch's `gVisor stats` line. The per-fetch `RTOs` field
polls that counter every millisecond and missed 3 of the 3,721 RTOs in the
2026-10-27 runs, 2 of the 99 in the instrumented runs and 2 of the 179 in
the paired runs, all at the end of a transfer. `ff9bfc2` fixes the poll, but
every binary used here predates it, so the tables,
`parse-rack-decisions.py` and `results-to-benchfmt.py` all count with
gVisor's counter.

RACK off (`-rack=false`) sets `TCPRecovery(0)`, as Tailscale 1.82 through
1.102 did, and as `TS_NETSTACK_ENABLE_RACK=false` does in Tailscale builds
that include
[tailscale/tailscale#22069](https://github.com/tailscale/tailscale/pull/22069).
That PR merged the switch proposed in
[tailscale/tailscale#22045](https://github.com/tailscale/tailscale/pull/22045)
as `TS_NETSTACK_TCP_RACK`, the name the end-to-end runs below use. The
scenarios: `default` loses the tail and the loss probe;
`tail-tlp-survives` loses the tail, and the probe and everything after it
arrive; `tail-tlp-survives-no-ts` is the same with a client that does not
offer TCP timestamps; `mid-flight-hole` loses 16 packets with later ones
arriving; `end-of-transfer` and `end-of-transfer-no-ts` are below.

| sender | tail and probe lost | probe arrives | probe arrives, client without timestamps | hole mid-flight |
|---|---|---|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | 3.54–6.03 s (15–27 RTOs) | 3.52–5.82 s (15–26 RTOs) | 0.30 s, but 4 of 8 unfinished at 120 s (0–9 RTOs) | 0.30–0.32 s (0 RTOs) |
| gVisor pin, RACK on, no GSO | 3.76–11.2 s (16–52 RTOs) | 3.54–51.3 s (15–245 RTOs) | 0.30 s, but 1 of 8 unfinished at 120 s (0–9 RTOs) | 0.30–0.33 s (0 RTOs) |
| gVisor pin, RACK off (TCPRecovery(0), the 1.82–1.102 setting) | 0.52–0.55 s (1 RTO) | 0.32–0.56 s (0–1 RTOs) | 0.30–0.52 s (0–1 RTOs) | 0.30–0.32 s (0 RTOs) |
| pin + #15432 | 0.64–0.66 s (1 RTO) | 0.32–0.65 s (0–1 RTOs) | 0.30–1.22 s (0–1 RTOs) | 0.30–0.33 s (0 RTOs) |
| pin + #16081 | 0.54–0.57 s (1 RTO) | 0.53–0.55 s (1 RTO) | 0.30–0.56 s (0–1 RTOs) | 0.30–0.33 s (0 RTOs) |
| pin + #15432 + #16081 | 0.53–0.57 s (1 RTO) | 0.33–0.58 s (0–1 RTOs) | 0.30–0.56 s (0–1 RTOs) | 0.30–0.33 s (0 RTOs) |
| pin + #16081 + no TSecr test (experiment) | 0.54–0.57 s (1 RTO) | 0.54–0.59 s (1 RTO) | 0.30–0.58 s (0–1 RTOs) | 0.30–0.33 s (0 RTOs) |
| pin + #15432 + #16081 + no TSecr test (experiment) | 0.53–0.55 s (1 RTO) | 0.33–0.55 s (0–1 RTOs) | 0.30–0.55 s (0–1 RTOs) | 0.30–0.33 s (0 RTOs) |
| pin + #16081 + TSecr fix | 0.54–0.63 s (1 RTO) | 0.54–0.58 s (1 RTO) | 0.30–0.57 s (0–1 RTOs) | 0.30–0.32 s (0 RTOs) |
| pin + #15432 + #16081 + TSecr fix | 0.53–0.55 s (1 RTO) | 0.32–0.54 s (0–1 RTOs) | 0.30–0.34 s (0 RTOs) | 0.30–0.32 s (0 RTOs) |
| pin + #15432 + #16081 + probe fix | 0.55–0.58 s (1 RTO) | 0.30–0.35 s (0 RTOs) | – | 0.30–0.35 s (0 RTOs) |
| pin + #15432 + #16081 + TSecr fix + probe fix | 0.55–0.58 s (1 RTO) | 0.30–0.36 s (0 RTOs) | – | 0.30–0.34 s (0 RTOs) |
| gVisor master `4777ee78` | 5.18–10.6 s (23–49 RTOs) | 0.30–11.6 s (0–54 RTOs) | 0.30–0.31 s, but 3 of 8 unfinished at 120 s (0–9 RTOs) | 0.30–0.32 s (0 RTOs) |
| master + #15432 + #16081 | 0.54–0.56 s (1 RTO) | 0.33–0.55 s (0–1 RTOs) | 0.30–0.56 s (0–1 RTOs) | 0.31–0.34 s (0 RTOs) |
| master + #15432 + #16081 + TSecr fix | 0.54–0.56 s (1 RTO) | 0.33–0.34 s (0 RTOs) | 0.30–0.55 s (0–1 RTOs) | 0.30–0.36 s (0 RTOs) |
| master + #15432 + #16081 + probe fix | 0.54–0.56 s (1 RTO) | 0.33–0.35 s (0 RTOs) | – | 0.30–0.32 s (0 RTOs) |
| master + #15432 + #16081 + TSecr fix + probe fix | 0.54–0.56 s (1 RTO) | 0.33–0.35 s (0 RTOs) | – | 0.31–0.33 s (0 RTOs) |
| master + #16495 | 0.79–0.81 s (2 RTOs) | 0.32–0.91 s (0–2 RTOs) | 0.30–1.02 s (0–2 RTOs) | 0.30–0.33 s (0 RTOs) |
| master + #15432 + #16081 + #16495 + #16419 | 0.53–0.55 s (1 RTO) | 0.53–0.55 s (1 RTO) | 0.30–0.55 s (0–1 RTOs) | 0.30–0.31 s (0 RTOs) |
| Linux 6.18 kernel TCP, CUBIC | 0.60–0.65 s (1 RTO) | 0.30–0.39 s (0 RTOs) | 0.30 s (0 RTOs) | 0.30–0.33 s (0 RTOs) |

On one long-lived connection (one trial, five measured fetches, numbered 3
to 7 in the raw output after the two warm-ups):

| sender | measured fetches 1–5 |
|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | 3.53, 42.1, 47.3, 48.7, 50.4 s |
| gVisor pin, RACK on, no GSO | 4.16, 39.2, 39.4, 45.8, 47.5 s |
| gVisor pin, RACK off (TCPRecovery(0), the 1.82–1.102 setting) | 0.54, 0.55, 0.55, 0.56, 0.56 s |
| pin + #15432 | 0.67, 0.91, 0.90, 0.89, 0.89 s |
| pin + #16081 | 0.54, 0.54, 0.55, 0.56, 0.56 s |
| pin + #15432 + #16081 | 0.55, 0.55, 0.58, 0.56, 0.56 s |
| pin + #16081 + no TSecr test (experiment) | 0.57, 0.57, 0.60, 0.56, 0.58 s |
| pin + #15432 + #16081 + no TSecr test (experiment) | 0.54, 0.56, 0.57, 0.56, 0.56 s |
| pin + #16081 + TSecr fix | 0.55, 0.56, 0.56, 0.56, 0.56 s |
| pin + #15432 + #16081 + TSecr fix | 0.54, 0.55, 0.56, 0.57, 0.56 s |
| pin + #15432 + #16081 + probe fix | 0.55, 0.56, 0.57, 0.60, 0.58 s |
| pin + #15432 + #16081 + TSecr fix + probe fix | 0.55, 0.56, 0.59, 0.58, 0.59 s |
| gVisor master `4777ee78` | 10.4, 4.76, 4.76, 4.77, 4.77 s |
| master + #15432 + #16081 | 0.54, 0.55, 0.56, 0.55, 0.55 s |
| master + #15432 + #16081 + TSecr fix | 0.54, 0.55, 0.55, 0.57, 0.56 s |
| master + #15432 + #16081 + probe fix | 0.54, 0.56, 0.55, 0.58, 0.56 s |
| master + #15432 + #16081 + TSecr fix + probe fix | 0.54, 0.55, 0.54, 0.55, 0.56 s |
| master + #16495 | 0.78, 0.82, 0.80, 0.80, 0.81 s |
| master + #15432 + #16081 + #16495 + #16419 | 0.53, 0.53, 0.55, 0.53, 0.54 s |
| Linux 6.18 kernel TCP, CUBIC | 0.62, 0.61, 0.61, 0.61, 0.62 s |

When the loss probe arrives, whether a fetch needs an RTO varies from trial
to trial, so the main variants also ran `tail-tlp-survives` with 24 trials:

| sender | trials | fetch time | trials without an RTO |
|---|---|---|---|
| master + #15432 + #16081 | 24 | 0.31–0.59 s, median 0.55 s | 2 of 24 |
| master + #15432 + #16081 + TSecr fix | 24 | 0.32–0.55 s, median 0.34 s | 22 of 24 |
| master + #15432 + #16081 + probe fix | 24 | 0.33–0.36 s, median 0.34 s | 24 of 24 |
| master + #15432 + #16081 + TSecr fix + probe fix | 24 | 0.30–0.36 s, median 0.34 s | 24 of 24 |
| Linux 6.18 kernel TCP, CUBIC | 24 | 0.30–0.38 s, median 0.30 s | 24 of 24 |

The same scenario with eight trials:

| sender | trials | fetch time | trials without an RTO |
|---|---|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | 8 | 3.52–5.82 s, median 3.94 s | 0 of 8 |
| gVisor pin, RACK on, no GSO | 8 | 3.54–51.3 s, median 3.94 s | 0 of 8 |
| gVisor pin, RACK off (TCPRecovery(0), the 1.82–1.102 setting) | 8 | 0.32–0.56 s, median 0.53 s | 1 of 8 |
| pin + #15432 | 8 | 0.32–0.65 s, median 0.64 s | 1 of 8 |
| pin + #16081 | 8 | 0.53–0.55 s, median 0.54 s | 0 of 8 |
| pin + #15432 + #16081 | 8 | 0.33–0.58 s, median 0.54 s | 1 of 8 |
| pin + #16081 + no TSecr test (experiment) | 8 | 0.54–0.59 s, median 0.57 s | 0 of 8 |
| pin + #15432 + #16081 + no TSecr test (experiment) | 8 | 0.33–0.55 s, median 0.43 s | 4 of 8 |
| pin + #16081 + TSecr fix | 8 | 0.54–0.58 s, median 0.56 s | 0 of 8 |
| pin + #15432 + #16081 + TSecr fix | 8 | 0.32–0.54 s, median 0.33 s | 7 of 8 |
| pin + #15432 + #16081 + probe fix | 8 | 0.30–0.35 s, median 0.34 s | 8 of 8 |
| pin + #15432 + #16081 + TSecr fix + probe fix | 8 | 0.30–0.36 s, median 0.34 s | 8 of 8 |
| gVisor master `4777ee78` | 8 | 0.30–11.6 s, median 5.48 s | 1 of 8 |
| master + #15432 + #16081 | 8 | 0.33–0.55 s, median 0.55 s | 1 of 8 |
| master + #15432 + #16081 + TSecr fix | 8 | 0.33–0.34 s, median 0.34 s | 8 of 8 |
| master + #15432 + #16081 + probe fix | 8 | 0.33–0.35 s, median 0.34 s | 8 of 8 |
| master + #15432 + #16081 + TSecr fix + probe fix | 8 | 0.33–0.35 s, median 0.34 s | 8 of 8 |
| master + #16495 | 8 | 0.32–0.91 s, median 0.80 s | 1 of 8 |
| master + #15432 + #16081 + #16495 + #16419 | 8 | 0.53–0.55 s, median 0.54 s | 0 of 8 |
| Linux 6.18 kernel TCP, CUBIC | 8 | 0.30–0.39 s, median 0.30 s | 8 of 8 |

At the end of a transfer, the loss probe has to be a retransmission, because
there is no new data to send. `end-of-transfer` sends a 14,400-byte response
(12 packets) and loses its last 4 packets; the probe and everything after
it arrive. 24 trials per row, and eight for `end-of-transfer-no-ts`, which
uses a client without timestamps:

| sender | trials | fetch time | trials without an RTO |
|---|---|---|---|
| gVisor master `4777ee78` | 24 | 0.22–0.23 s, median 0.22 s | 0 of 24 |
| master + TSecr fix | 24 | 0.02–0.22 s, median 0.02 s | 14 of 24 |
| master + #15432 + #16081 | 24 | 0.22 s, median 0.22 s | 0 of 24 |
| master + #15432 + #16081 + TSecr fix | 24 | 0.02–0.22 s, median 0.22 s | 5 of 24 |
| master + #15432 + #16081 + probe fix | 24 | 0.22 s, median 0.22 s | 0 of 24 |
| master + #15432 + #16081 + TSecr fix + probe fix | 24 | 0.02–0.22 s, median 0.02 s | 16 of 24 |
| Linux 6.18 kernel TCP, CUBIC | 24 | 0.03–0.24 s, median 0.03 s | 20 of 24 |
| master + #15432 + #16081, client without timestamps | 8 | 0.02–0.22 s, median 0.02 s | 5 of 8 |
| master + #15432 + #16081 + TSecr fix, client without timestamps | 8 | 0.02–0.22 s, median 0.02 s | 6 of 8 |
| Linux 6.18 kernel TCP, CUBIC, client without timestamps | 8 | 0.03–0.24 s, median 0.03 s | 6 of 8 |

To see why a trial with the TSecr fix still waits for the RTO,
`run-rack-decisions.sh` builds master + the TSecr fix, with and without
#15432 and #16081, plus `patches/diagnostic-log-rack-decisions.patch`, which
logs every RACK decision on a retransmitted segment, and every new minimum
RTT, for both stacks. It runs both builds
in the end-of-transfer scenarios (jitter seeds 1–3; 24 trials, or 12 with a
client without timestamps), and master + #15432 + #16081 + the TSecr fix in
the 24-trial tail-probe scenario (seeds 1–2). The logs are in
`results/2026-10-27/rack-decisions/`, and `parse-rack-decisions.py`
summarizes one.

- **The stall.** With RACK on, as Tailscale 1.104.x ships it, every trial of
  both tail-loss scenarios stalls for 3.5–6.0 s, at a median of two
  retransmissions per RTO, whether or not the loss probe gets through.
  Turning netstack's GSO off does not prevent it (median 3.95 and 3.94 s
  without GSO, 4.06 and 3.94 s with it); the longest no-GSO trials had
  larger windows at the burst (next point). On one connection the stalls
  compound: 3.5 s, then 42–50 s per fetch.
- **How long it lasts** depends on the window at the burst. In one trial
  without GSO, a spurious recovery in a warm-up fetch left ssthresh at 340.
  After the 3 s of idle time, the pinned CUBIC took cwnd from 10 to 494
  within 100 ms, about 490 packets were lost, and recovery took 245 RTOs:
  51.3 s. gVisor master's CUBIC (google/gvisor#15604) stops that growth: on
  one connection, master takes 10.4 s and then 4.8 s per fetch instead of
  42–50 s, but it still stalls.
- **Clients without timestamps.** In `tail-tlp-survives-no-ts` the stall
  becomes a hang. Without timestamps, gVisor applies Karn's rule (RFC 6298,
  section 3): a retransmission moves `RTTMeasureSeqNum` to `SndNxt` (every
  retransmission on master, each fast retransmit on the pin), and only an
  ACK beyond it gives an RTT sample. During the crawl nothing new is sent,
  so no ACK gives one, and the backed-off RTO is never recomputed. In the
  trials that lost the whole tail, about 1000 packets, the pinned gVisor and
  master doubled the RTO from 200 ms to 102 s and had retransmitted 20
  packets when the client gave up at 120 s (4 of 8 trials on the pin, 3 of 8
  on master). Trials in which the burst ended mid-flight, so later data was
  SACKed, finished in 0.3 s. Every variant
  with #15432 or #16081 finished every fetch in at most 1.22 s.
- **The upstream fixes.** #16081, alone or with #15432, recovers with at
  most one RTO in every scenario here, as RACK off does. #15432 alone ends
  the crawl but leaves ssthresh at 2: after the idle time the pinned CUBIC
  grows cwnd to about 450 within 100 ms, so each later burst loses about 450
  packets, and a fetch takes about 0.9 s instead of 0.55 s. #16495 alone
  ends the crawl after a second RTO (0.79–0.81 s with the tail and the probe
  lost); stacked with #15432, #16081 and #16419, the results match #15432 +
  #16081.
- **Holes in mid-flight**, with SACKed data above them, take about 0.3 s
  with every sender: the stall needs a tail loss.
- **A delivered loss probe.** gVisor master with #15432 and #16081 recovers
  without an RTO in 2 of 24 trials, and in those two the burst ended
  mid-flight, so later data was SACKed and recovery did not need the probe.
  Linux needed no RTO in any trial, but mostly for the same reason: its
  burst ended mid-flight in 30 of 32 trials (it lost 6–165 packets), and in
  the 2 where it lost the whole tail, it recovered from the probe in 0.38
  and 0.39 s. After a tail loss the congestion window is full, so the probe
  is a retransmission, and RACK discards its SACK: the receiver echoes the
  timestamp of the last in-order segment, which is older than the probe, and
  RACK's TSecr test takes that to mean the ACK was not for the probe. With
  the TSecr fix, 22 of 24 trials recover without an RTO, all 22 from the
  probe's SACK (gVisor's `TLP recoveries` counter is 1 in each). In the
  instrumented reruns of the same build, 19 and 18 of 24 recovered from the
  probe, and 2 and 5 ended mid-flight and did not need it. With
  the probe fix, the probe is new data, whose SACK is not ambiguous: 24 of
  24, and 24 of 24 with both. With a flight this large, the TSecr fix needs
  #15432 on the pinned gVisor (0 of 8 with #16081 alone): RACK marks the
  tail lost, but the pipe still counts it, and nothing goes out until the
  RTO. The experiment, which drops the TSecr test altogether, recovered
  without an RTO in 4 of 8 trials with #15432 and #16081; eight trials are
  too few to tell it apart from the TSecr fix's 7 of 8.
- **The end of a transfer.** With no new data left, the loss probe has to be
  a retransmission, so the probe fix cannot help, and gVisor master waits
  for the RTO with or without #15432 and #16081 (24 of 24 trials). With the
  TSecr fix, about half the trials recover from the probe, and the count
  varies from run to run: 5, 14 and 16 of 24 in the runs above, and 83 of
  144 in the instrumented runs described above (both master builds with the
  fix, three jitter seeds: 12, 18 and 12 of 24, and 11, 15 and 15 of 24). In
  every instrumented trial that waited for the RTO (61 of 144), RACK
  rejected the probe's RTT sample with RFC 8985's other test in step 2: it
  was below the connection's minimum RTT, by at most 0.25 ms. On this path,
  141 of the 144 probe RTTs were within 0.4 ms of the minimum, the range of
  the jitter, so that test is close to a coin toss. Linux applies the same
  comparison (`tcp_rack_advance`), but its minimum (`tcp_min_rtt`) also
  takes the RTT of the handshake, whose small packets cross the 80 Mbit/s
  link about 0.12 ms faster than a full segment; gVisor's RACK minimum comes
  only from RACK's own samples of data segments. That may be why Linux
  recovered without an RTO in 20 of 24. Without client timestamps there is
  no TSecr test, and master + #15432 + #16081 recovered without an RTO in 5
  of 8.
- **The proposed fixes elsewhere.** In the other scenarios (the tail and the
  probe lost, a hole mid-flight, five fetches on one connection), the RTO
  counts with and without the TSecr fix and the probe fix are the same. The
  benchmarks below compare the fetch times.
- **Loss volume.** The drop rule takes whatever the sender sends, so a
  sender with a larger window at the burst loses more. In the default
  scenario, Linux retransmitted 202–272 packets per fetch, and gVisor as
  shipped 35–58.

### Benchmarks (2026-10-28)

gVisor asks for `benchstat` output with changes that can affect netstack
performance. `run-paired-benchmarks.sh` runs four scenarios on master +
#15432 + #16081, with and without each proposed fix: 24 trials per scenario
and build, one trial per process, trial i of every build with jitter seed
i, the builds taking turns trial by trial. `results-to-benchfmt.py` turns
each measured fetch into a benchmark line, with the fetch's RTO count from
gVisor's own counter as `rtos/op`. The outputs are in
`results/2026-10-28/benchstat-tsecr-fix.txt`, `benchstat-probe-fix.txt`
and `benchstat-paired-all.txt` (all four builds side by side).

| scenario | base: trials without an RTO | TSecr fix | probe fix | both |
|---|---|---|---|---|
| `TailLostProbeArrives` (`tail-tlp-survives`) | 3 of 24 | 24 of 24, −38% | 24 of 24, −39% | 24 of 24, −38% |
| `EndOfTransferTailLost` (`end-of-transfer`) | 0 of 24 | 15 of 24, median −90% | 0 of 24, no change | 19 of 24, median −91% |
| `TailAndProbeLost` (`default`) | 0 of 24 | 0 of 24, no change | 0 of 24, −1.5% (p=0.008) | 0 of 24, no change |
| `HoleMidFlight` (`mid-flight-hole`) | 24 of 24 | 24 of 24, no change | 24 of 24, no change | 24 of 24, no change |

Percentages are benchstat's change in median fetch time; "no change" means
benchstat found no significant difference (p > 0.05). These runs are
separate from the tables above: with the tail lost and the probe delivered,
the 2026-10-27 run had 2 of 24 (base) and 22 of 24 (TSecr fix) trials
without an RTO, and these interleaved runs had 3 and 24 of 24. The probe
fix's 1.5% with the tail and the probe lost has no mechanism that I could
find: every trial with and without it recovers through one RTO, with no fast
retransmit, SACK recovery or TLP recovery, and the unpaired runs below found
no difference (p=0.22).

The first benchmark runs, one 24-trial process per build, one build after
another (`results/2026-10-28/default-24-trials-*` and
`mid-flight-hole-24-trials-*`, compared in `benchstat-*-unpaired.txt`),
showed significant differences of 0.8–2.3% in the two scenarios where
neither fix acts, between builds that all behave the same there. The likely
cause is drift in the machine's timing between those runs. Interleaving the
builds trial by trial spreads such drift over all of them, but it cannot
rule out noise such as the probe fix's 1.5% above, and benchstat's U-test
does not use the pairing. (The unpaired files' `TailLostProbeArrives` and
`EndOfTransferTailLost` rows for the probe fix compare 2026-10-27 base runs
with 2026-10-28 probe-fix runs; each file lists its inputs.)

### End-to-end check with real tailscaled (2026-10-28)

`e2e/` runs the `default` and `tail-tlp-survives` scenarios through two real
`tailscaled` v1.104.2 nodes on a private Headscale tailnet. Their WireGuard
packets cross the same emulated path, the response comes from netstack
behind `tailscale serve`, and the client is Linux kernel TCP. Eight trials
per row; `e2e/README.md` has the setup and the full tables.

| server | tail and probe lost | tail lost, probe delivered |
|---|---|---|
| v1.104.2 as shipped (RACK on) | 4.04–6.59 s, all 8 over 1 s | 0.37–4.17 s, 5 of 8 over 1 s |
| v1.104.2, `TS_NETSTACK_TCP_RACK=false` | 0.61–0.69 s | 0.42–0.72 s |
| v1.104.2 + #15432 + #16081 | 0.62–0.72 s | 0.35–0.68 s; 5 of 8 wait for an RTO |
| the same + TSecr fix + probe fix | 0.57–0.67 s | 0.36–0.47 s; none waits for an RTO |

### Revised fixes after review (2026-10-30)

Review on google/gvisor#16659 and #16660 found three edge cases, and both
fixes changed (`patches/master-tsecr-fix-revised-code.patch` and
`master-tlp-new-data-revised-code.patch`): the TSecr fix skips the TSecr
test only for a retransmission that SACK blocks cover entirely, and the
probe fix schedules no probe timer while a probe is out and does not let an
ACK of data not yet sent end a new-data probe. `run-revision-check.sh`
compares the first and revised versions (builds ending in `2`) as
`run-paired-benchmarks.sh` does: 24 trials per scenario and build,
interleaved trial by trial. It ran on a Linux arm64 virtual machine, with
binaries cross-built from the same patches, so its times are not
comparable with the tables above; compare builds within it. Raw output and
benchstat files: `results/2026-10-30/`.

| with #15432 + #16081 | `TailLostProbeArrives`: no RTO, median | `EndOfTransferTailLost`: no RTO, median |
|---|---|---|
| neither fix | 0 of 24, 0.534 s | 0 of 24, 0.221 s |
| TSecr fix, first / revised | 19 / 17 of 24, 0.325 / 0.328 s | 21 / 21 of 24, 0.021 / 0.021 s |
| probe fix, first / revised | 24 / 24 of 24, 0.325 / 0.323 s | 0 / 0 of 24, 0.221 / 0.222 s |
| both, first / revised | 24 / 24 of 24, 0.324 / 0.325 s | 21 / 24 of 24, 0.020 / 0.021 s |

In `TailAndProbeLost` every build waited for one RTO (medians 0.532–0.534
s), and in `HoleMidFlight` none did (0.301–0.303 s). benchstat finds no
significant difference between the first and revised versions in any
scenario, except the probe fix in `TailLostProbeArrives`, 0.77% faster when
revised (p=0.007; `benchstat-revision-*.txt`). The no-RTO counts differ by
up to three trials between versions, in both directions (Fisher's exact
test, p ≥ 0.23). Against #15432 + #16081 alone, the revised TSecr fix cuts
`TailLostProbeArrives` by 38.61% and `EndOfTransferTailLost` by 90.50%, and
the revised probe fix cuts `TailLostProbeArrives` by 39.55%
(`benchstat-revised-*.txt`); the first versions' runs above give 37.98%,
90.27% and 38.98%. On master alone, at the end of a transfer, 0 of 24
fetches avoided an RTO, 21 of 24 with the first TSecr fix and 19 of 24 with
the revised one (medians 0.221, 0.023 and 0.024 s).

With RACK's decisions logged (`rack-decisions/`, 48 fetches per version
with the tail lost and the probe delivered), the first TSecr fix avoided
the RTO in 31 fetches and the revised one in 33. In every fetch that waited
for an RTO, RACK rejected the probe's sample with the minimum RTT test,
0.002–0.239 ms below the minimum, as above. Neither version rejected a
SACKed sample with the TSecr test; all its rejections were of cumulatively
acknowledged segments, which both versions treat alike.

## Limits

- The loss is scripted and the path is simple: no Wi-Fi aggregation, no
  reordering, no ACK loss. It shows the recovery behavior, not how often a
  real network produces such a burst.
- Trials are not identical. The drop rule is fixed, but timing, and with it
  the window at the burst and the size of the loss, varies from trial to
  trial. Eight trials are too few to compare variants whose results differ
  only in how often a fetch needs an RTO.
- The receiver is gVisor's TCP standing in for the client's kernel TCP
  (macOS in #21946), and the server accepts with gonet rather than through
  tailscaled's TCP forwarder. A real client's ACK, SACK and timestamp echo
  can differ. `e2e/` repeats two scenarios with real tailscaled and a Linux
  kernel TCP client (above).
- Before commits `a48eb27` and `113cb76`, the path delivered packets up to
  1.5 ms late, and the Linux sender was paced by BBR's init. Results from
  that harness are in the history, in commit `398ebf4`.
