# netstack-rack-repro

A deterministic reproduction of
[tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946):
after a burst loss, TCP that gVisor's netstack terminates with RACK on recovers
at about two segments per retransmission timeout (RTO), so a 2.8 MB transfer
takes seconds to tens of seconds on a 3 ms path. It also runs the same scenario
against the Linux kernel's TCP for comparison.

Findings are dated 2026-10-27 and were checked against Tailscale v1.104.2,
which pins `gvisor.dev/gvisor v0.0.0-20260915211658-a6f909f08a72`, and gVisor
master `4777ee78` (2026-10-27).

## What it does

One Go program builds two TCP endpoints and joins them with an emulated path:

- **Server (sender).** Either a gVisor stack set up the way
  `wgengine/netstack` sets it up in v1.104.2: CUBIC, SACK and RACK at gVisor's
  defaults (on), 8 MiB / 6 MiB buffer limits, host GSO up to 64 KiB (the
  program segments GSO packets the way tstun does), MTU 1280, RX checksum
  offload. Or the Linux kernel's TCP (`-sender=linux`), which sends over a TUN
  device in a private network namespace and through the same emulated path.
- **Client (receiver).** A gVisor stack with default settings.
- **Path.** 3 ms RTT plus up to 2 × 200 µs of seeded jitter that never
  reorders, 80 Mbit/s toward the client, no queue limit, and a scripted loss
  burst.
- **Workload.** Like `curl --rate 20/m` in the original A/B test: on one
  connection, a small request and a 2.8 MB response, with 3 s of idle time
  before each fetch. Each trial uses a new connection with two loss-free
  warm-up fetches, so every trial meets the burst in the same sender state.

The scripted burst counts packets, not time, until it starts: it delivers the
first 1000 data packets of a measured fetch, then drops every data packet for
150 ms. The drop phase covers the tail of the flight and the loss probe, so
recovery starts with an RTO, as in the issue's capture. Options select other
patterns (see `-h` and `results/*/scenarios.txt`).

It reports, per fetch, the time to completion, the number of RTOs and the
retransmissions between successive RTOs. With `-trace` it also prints a
per-100 ms table in the format of the excerpt in #21946, plus the gVisor
sender's congestion state after each ACK (from gVisor's TCP probe).

## Run it

```sh
./build-variants.sh                 # bin/repro-{pin,pr15432,guard,fix}
bin/repro-pin -trace                # gVisor as Tailscale 1.104.x ships it
bin/repro-pin -rack=false -trace    # RACK off, as Tailscale <= 1.102
./run-linux-sender.sh -trace        # Linux kernel TCP (needs root or user namespaces)
./run-matrix.sh -trials 8 -trace    # all of the above, saved under results/
```

`build-variants.sh` patches the pinned gVisor module with the files in
`patches/`:

| binary | gVisor |
|---|---|
| `repro-pin` | the module that Tailscale v1.104.2 pins |
| `repro-pr15432` | + [google/gvisor#15432](https://github.com/google/gvisor/pull/15432): `SetPipe` skips RACK-lost segments, `DoRecovery` recomputes pipe, `clone` keeps the lost mark |
| `repro-guard` | + RACK does not start fast recovery during RTO recovery |
| `repro-fix` | + both |

Go 1.27.3 is used (`GOTOOLCHAIN=go1.27.3` downloads it).

`gvisor-go-test-overlay.sh` builds a tree in which gVisor's own TCP test
suites run under `go test` without Bazel: the `go` branch export of a master
commit plus that commit's test files.

## Results (2026-10-27)

Eight trials per row; all fetches 2.8 MB. Full outputs, with per-100 ms traces,
are in `results/2026-10-27/`; `scenarios.txt` there lists the flags of
each scenario.

| sender | 150 ms burst (tail and probe lost) | tail lost, probe delivered | mid-flight hole |
|---|---|---|---|
| gVisor pin, RACK on (Tailscale 1.104.x) | **4.1–6.2 s**, 17–27 RTOs, median **2** retransmissions per RTO | **3.5–6.4 s** | 0.31–0.40 s |
| gVisor pin, RACK on, no GSO | 3.9–7.0 s | | |
| gVisor pin, RACK off (Tailscale ≤ 1.102) | 0.53–0.62 s | 0.53–0.65 s | 0.31–0.39 s |
| + #15432 | 0.68–0.80 s | 0.68–0.77 s | 0.30–0.37 s |
| + RTO guard | 0.55–0.64 s | 0.55–0.64 s | 0.31–0.40 s |
| + #15432 + RTO guard | 0.55–0.65 s | 0.56–0.65 s | 0.30–0.36 s |
| + both + no RACK TSecr test (experiment) | | 0.34–0.58 s | |
| gVisor master 4777ee78, RACK on | 4.6–7.3 s | 5.5–7.6 s | |
| master + #15432 + RTO guard | 0.545–0.553 s | 0.54–0.56 s | |
| Linux 6.18, CUBIC | 0.63–0.66 s | 0.42–0.44 s | 0.31–0.32 s |

Five fetches on one long-lived connection, where the effects compound: the
stall leaves ssthresh at 2, and the pinned CUBIC then inflates cwnd after each
idle period (fixed on master by 0ef892cc5), so later bursts lose more.

| sender | five fetches on one connection |
|---|---|
| gVisor pin, RACK on | 6.0, 36.0, 37.9, 36.2, 36.8 s |
| + #15432 | 0.78, 1.00, 1.00, 1.02, 1.00 s |
| + RTO guard | 0.63, 0.67, 0.67, 0.67, 0.67 s |
| + #15432 + RTO guard | 0.57, 0.62, 0.66, 0.68, 0.67 s |
| gVisor master, RACK on | 5.7, 4.9, 4.9, 4.9, 4.9 s |
| master + #15432 + RTO guard | 0.54, 0.54, 0.55, 0.57, 0.58 s |
| Linux 6.18, CUBIC | 0.64, 0.63, 0.62, 0.62, 0.62 s |

`results/2026-10-27/superseded/` keeps two earlier tail-loss scenarios whose
loss pattern did not do what their names said (see `scenarios.txt`).
