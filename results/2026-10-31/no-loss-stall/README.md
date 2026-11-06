# Stalls without scripted loss, 2026-10-31

In `../cubic-idle/`, both warm-up fetches of `pin-rackon-30s-t1` crawled
(15.3 s and 29.0 s), although warm-up fetches have no scripted loss. These
runs measure how often a single fetch on a new connection crawls when the
only loss is a drop-tail queue overflowing, with gVisor as Tailscale
v1.104.0 to v1.104.4 run it, and which of the gVisor fixes in flight stops it.

They ran in two batches. The main batch (`raw/`, `runs.csv`) compares the
pin with gVisor master, each with RACK on and off. The fix test
(`fix-test/`) runs the pin with each fix.

## Builds

Each binary is this repository's harness, built with Go 1.27.3. Each
run's `sender:` line names its gVisor.

| build | gVisor | RACK |
|---|---|---|
| `pin` | `v0.0.0-20260915211658-a6f909f08a72`, which Tailscale v1.104.x and main pin | on, as v1.104.0 to v1.104.4 run it; off, as v1.104.5 and main run it ([tailscale/tailscale#22092](https://github.com/tailscale/tailscale/pull/22092)) |
| `master` | `v0.0.0-20261031110703-16342efb9a78`, the go branch of master `aa767e4c4`, which has [google/gvisor#15579](https://github.com/google/gvisor/pull/15579) and [google/gvisor#15604](https://github.com/google/gvisor/pull/15604) | on and off |
| `pin-16659` | pin + [google/gvisor#16659](https://github.com/google/gvisor/pull/16659) at `581d0bc94` (`fix-test/patches/pin-16659-revised.patch`) | on |
| `pin-16659orig` | pin + the first version of the same fix (`patches/pin-4-tsecr-fix-code.patch` in this repository) | on |
| `pin-16660` | pin + [google/gvisor#16660](https://github.com/google/gvisor/pull/16660) at `f739c6928` (`fix-test/patches/pin-16660-revised.patch`) | on |
| `pin-16659-16660` | pin + #16659, then #16660 | on |
| `pin-16081` | pin + [google/gvisor#16081](https://github.com/google/gvisor/pull/16081) at `7d4f369c` (`patches/pin-2-pr-16081-code.patch`) | on |
| `pin-15432-16081` | pin + [google/gvisor#15432](https://github.com/google/gvisor/pull/15432) at `00aaa82b` (`patches/pin-1-pr-15432-code.patch`), then #16081 | on |

The fix builds run only in the fix test, `master` only in the main batch.
The patches for the four PRs carry only their non-test code, at their
heads on 2026-10-31. The two in `fix-test/patches/` change the same
lines as this repository's `patches/master-tsecr-fix-revised-code.patch`
and `patches/master-tlp-new-data-revised-code.patch`, with the pin's
context.
The first version of the TSecr fix skips RACK's TSecr test for every
segment that the ACK does not cumulatively acknowledge; #16659 skips it
only for a segment that is SACKed in full.

The fix builds follow `build-variants.sh`: a copy of the pinned module,
`patch -p1` with every hunk applying without fuzz, then
`go build -mod=mod -modfile=...` with a `replace` of `gvisor.dev/gvisor`.
The main batch's binaries were built from this repository at `e14785e`
(the code of `main` on 2026-10-31), the fix test's at `94a018b`, which
only added results, so the two `pin` binaries differ only in that stamp.

## How it ran

The sender uses the harness defaults, which are Tailscale's netstack
settings (CUBIC, SACK, host GSO, MTU 1280). The client is gVisor with an
8 MiB receive buffer and offers TCP timestamps unless noted. Toward the
client, the path has a drop-tail queue of 50, 100 or 256 packets, plus the
default jitter (up to 2 × 200 µs), which never reorders.

Each run is one process: a new connection, 3 s of idle time (the default
`-interval`), then one fetch, with no warm-up and no scripted loss:

```sh
bin/repro-pin -rack=true -loss none -warmup 0 -fetches 1 -trials 1 \
  -timeout 30s -interval 3s -seed 1 -rtt 3ms -rate-mbps 80 -queue 100
```

| cell | path | queue (packets) | fetch | seeds | builds |
|---|---|---|---|---|---|
| `3ms-80M-q50` | 3 ms, 80 Mbit/s | 50 | 2.8 MB | 1–12 | `pin` and `master`, RACK on and off |
| `3ms-80M-q100` | 3 ms, 80 Mbit/s | 100 | 2.8 MB | 1–12 | the same |
| `3ms-80M-q256` | 3 ms, 80 Mbit/s | 256 | 2.8 MB | 1–12 | the same |
| `3ms-80M-q100-20MB` | 3 ms, 80 Mbit/s | 100 | 20 MB (`-size 20000000`) | 1–8 | the same |
| `3ms-80M-q100-tsoff` | 3 ms, 80 Mbit/s | 100 | 2.8 MB, client without timestamps (`-client-timestamps=false`) | 1–8 | `pin`, RACK on |
| `20ms-100M-q100` | 20 ms, 100 Mbit/s (`-rtt 20ms -rate-mbps 100`) | 100 | 2.8 MB | 1–8 | `pin` and `master`, RACK on and off |
| `3ms-80M-q100-pass2` | 3 ms, 80 Mbit/s | 100 | 2.8 MB | 13–16 | the same |
| `3ms-80M-q256-pass2` | 3 ms, 80 Mbit/s | 256 | 2.8 MB | 13–16 | the same |
| `20ms-100M-q100-pass2` | 20 ms, 100 Mbit/s | 100 | 2.8 MB | 9–16 | the same |
| `fix-test/raw/3ms-80M-q100` | 3 ms, 80 Mbit/s | 100 | 2.8 MB | 1–10 | `pin` with RACK on and off, and the six fix builds |
| `fix-test/raw/3ms-80M-q256` | 3 ms, 80 Mbit/s | 256 | 2.8 MB | 1–10 | `pin`, `pin-16659`, `pin-16659-16660` |

Run i of every build uses `-seed i`, which sets only the path's jitter, so
a seed does not repeat an outcome. Within a seed, the builds ran back to
back, one process at a time, in an order that rotates with the seed. Each
timestamps-off run followed the four builds of the same seed in
`3ms-80M-q100`. The main batch's 280 runs ran on 2026-10-31 from 17:14 to
17:50 UTC; its `-pass2` cells were added after the others, once the load
had dropped, and are shown separately. The fix test's 110 runs ran from
18:01:52 to 18:16:35 UTC. Every process exited 0, and every run's `loss:`
line reads `none scripted`, with 0 scripted drops.

From 17:31:55 UTC, the main batch paused between runs whenever the
1-minute load was above 16, until it fell below 10 or 120 s had passed
(604 s in all). A run already in progress was not affected, but its
`wall=` value then includes the pause (five runs). The fix test checked
the load the same way before every run and never had to pause.

A **crawl** is a fetch with at least 10 RTOs and a median of at most 3
retransmissions between successive RTOs, the signature of
[tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946).
**Over 1 s** counts fetches that took longer than 1 s or failed. A
**timeout** is a fetch cut at 30 s, and it counts as 30 s. Every crawl
here had 15 to 144 RTOs and a median of 2 retransmissions per RTO
interval. Every other fetch had at most 3 RTOs in the main batch, and at
most 7 in the fix test.

## Results

### Main batch: pin and master, RACK on and off

Crawls, then the median (and largest) time of a fetch in seconds. Without
loss, a fetch takes about 0.30 s on the 3 ms path (2.1 s for 20 MB) and
0.35 s on the 20 ms path.

| cell | runs per build | pin, RACK on (v1.104.0–v1.104.4) | pin, RACK off (v1.104.5, main) | master, RACK on | master, RACK off |
|---|---|---|---|---|---|
| 3 ms, queue 50 | 12 | **10 crawls** (4 timeouts); 19.5 (30.0) | 0; 0.87 (1.03) | 0; 0.30 (0.33) | 0; 0.30 (0.50) |
| 3 ms, queue 100 | 12 | **6 crawls**; 8.24 (29.6) | 0; 0.70 (0.90) | 0; 0.30 (0.56) | 0; 0.30 (1.02) |
| 3 ms, queue 100, pass 2 | 4 | **4 crawls**; 7.29 (15.3) | 0; 0.69 (0.92) | 0; 0.30 (0.30) | 0; 0.30 (0.31) |
| 3 ms, queue 256 | 12 | **1 crawl**; 0.31 (12.6) | 0; 0.51 (1.61) | 0; 0.34 (1.29) | 0; 0.38 (1.05) |
| 3 ms, queue 256, pass 2 | 4 | **1 crawl**; 0.31 (13.9) | 0; 0.51 (0.54) | 0; 0.30 (0.30) | 0; 0.30 (0.30) |
| 3 ms, queue 100, 20 MB | 8 | **3 crawls** (1 timeout); 2.30 (30.0) | 0; 2.52 (2.90) | 0; 2.11 (2.30) | 0; 2.10 (2.11) |
| 3 ms, queue 100, no client timestamps | 8 | 0; 0.31 (0.35) | | | |
| 20 ms, queue 100 | 8 | 0; 0.38 (1.98) | 0; 0.52 (0.91) | 0; 0.43 (1.82) | 0; 0.39 (0.92) |
| 20 ms, queue 100, pass 2 | 8 | 0; 0.35 (0.58) | 0; 0.35 (0.80) | 0; 0.35 (0.57) | 0; 0.35 (0.35) |

### Fix test: the pin with each fix

3 ms, 80 Mbit/s, client timestamps on, 10 runs per build. Times in
seconds.

| queue | build | RACK | over 1 s | crawls | timeouts | median (largest) time | runs with an RTO | RTOs in all | median queue drops |
|---|---|---|---|---|---|---|---|---|---|
| 100 | `pin` (v1.104.0–v1.104.4) | on | **6** | 6 | 0 | 13.0 (30.0) | 8 | 629 | 1371 |
| 100 | `pin` (v1.104.5, main) | off | 0 | 0 | 0 | 0.69 (0.73) | 10 | 20 | 848.5 |
| 100 | `pin-16659` | on | **5** | 4 | 0 | 1.10 (29.2) | 5 | 219 | 2244 |
| 100 | `pin-16659orig` | on | **4** | 4 | 2 | 0.33 (30.0) | 4 | 341 | 2925.5 |
| 100 | `pin-16660` | on | **8** | 7 | 1 | 17.4 (30.0) | 10 | 703 | 696.5 |
| 100 | `pin-16659-16660` | on | **5** | 5 | 1 | 3.13 (30.0) | 5 | 375 | 2872.5 |
| 100 | `pin-16081` | on | 0 | 0 | 0 | 0.74 (0.95) | 10 | 19 | 1259.5 |
| 100 | `pin-15432-16081` | on | 0 | 0 | 0 | 0.54 (0.97) | 8 | 13 | 1579 |
| 256 | `pin` (v1.104.0–v1.104.4) | on | **2** | 2 | 0 | 0.30 (13.0) | 2 | 119 | 312 |
| 256 | `pin-16659` | on | 0 | 0 | 0 | 0.30 (0.31) | 0 | 0 | 420 |
| 256 | `pin-16659-16660` | on | 0 | 0 | 0 | 0.30 (0.31) | 0 | 0 | 396 |

Runs over 1 s against the pin with RACK on at the same queue, Fisher's
exact test, two-sided:

- RACK off, `pin-16081` and `pin-15432-16081`, 0 of 10 each against 6 of
  10: p = 0.011 each. The two #16081 builds together, 0 of 20:
  p = 0.0004.
- `pin-16659` 5 of 10: p = 1.0. `pin-16659orig` 4 of 10: p = 0.66.
  `pin-16660` 8 of 10: p = 0.63. `pin-16659-16660` 5 of 10: p = 1.0.
- Queue 256: `pin-16659` and `pin-16659-16660`, 0 of 10 each against 2
  of 10: p = 0.47.

## What the runs show

Main batch:

- With gVisor as v1.104.0 to v1.104.4 run it, fetches crawled without any scripted
  loss. On the 3 ms path: 10 of 12 with a 50-packet queue (4 reached the
  30 s timeout), 10 of 16 with 100 packets and 2 of 16 with 256 packets
  (both passes), and 3 of 8 20 MB fetches (1 timeout). The crawls took
  3.8 s up to the timeout.
- Every crawl started the same way: the congestion window reached 381 to
  511 packets, the queue dropped 295 to 2,820 packets, and the first RTO
  came 0.23 to 0.51 s into the fetch. After that, the sender
  retransmitted a median of 2 packets per RTO, 15 to 144 times. Not every
  fetch jumped: on the 3 ms path, 12 RACK-on pin fetches with client
  timestamps peaked at 120 to 329 packets, all at a 1-minute load of 22
  or more.
- With RACK off, the pin overflowed the queue in a similar way. With
  2.8 MB fetches on the 3 ms path, its largest window was 102 to 576
  packets and the queue dropped a median of 162 to 754 packets per
  fetch, depending on the queue (835 for 20 MB). It recovered with at
  most 3 RTOs: no crawl in 68 runs, at most 1.61 s for 2.8 MB and 2.90 s
  for 20 MB. With 2.8 MB on the 3 ms path, it still took a median of
  0.51 to 0.87 s, against 0.30 to 0.38 s for master: the cost that
  [tailscale/tailscale#22104](https://github.com/tailscale/tailscale/issues/22104)
  describes.
- gVisor master's window stayed far smaller: on the 3 ms path, its
  largest window was 44 to 227 packets, and the queue dropped at most 106
  packets in a fetch. It did not crawl in any of its 136 runs. That
  removes the trigger, not the crawl: master still lacks #15432 and
  #16081, and master `4777ee78`, which lacked them too, crawled after a
  scripted burst in `../../2026-10-27/`. Master `aa767e4c4` was not tested
  with a scripted burst here.
- On the 20 ms, 100 Mbit/s path, no build crawled. The RACK-on pin's
  queue drops were mostly small (median 10 per fetch), and its two fetches
  that lost 660 and 952 packets took 0.58 and 0.95 s.
- With a client that does not offer timestamps, the RACK-on pin's largest
  window was 253 to 254 packets and the queue still dropped 445 to 1,386
  packets, but no fetch had an RTO and none crawled (0 of 8). These runs
  do not show why.
- Eight 2.8 MB fetches that did not crawl took 1.02 to 1.98 s, with at
  most 3 RTOs: six RACK-off or master fetches (1.02 to 1.82 s) and two
  RACK-on pin fetches (1.05 s on the 3 ms path with a 100-packet queue,
  1.98 s on the 20 ms path). Seven of them started at a 1-minute load of
  25 or more.

Fix test:

- Of the fixes tested, only #16081 removed the stall. With it, alone or
  with #15432, 0 of 20 fetches took over 1 s (at most 0.97 s), against 6
  of 10 for the pin (p = 0.0004). RACK off did the same (0 of 10, at most
  0.73 s).
- Every fetch over 1 s had an RTO. At queue 100, 28 of the 32 runs with
  an RTO on the RACK-on builds without #16081 took over 1 s (`pin` 6 of
  8, `pin-16659` 5 of 5, `pin-16659orig` 4 of 4, `pin-16660` 8 of 10,
  `pin-16659-16660` 5 of 5). With #16081, none of 18 did, and with RACK
  off, none of 10. Every crawl in the fix test had a median of 2
  retransmissions per RTO interval, as in the main batch. The two slow
  fetches that did not crawl took 1.88 s (`pin-16659`, 7 RTOs) and
  1.20 s (`pin-16660`, 1 RTO).
- #16081 does not stop the overflow or the RTO. Its builds still had an
  RTO in 8 to 10 of 10 runs and a median of 1,260 to 1,579 queue drops,
  and their fetches with an RTO took 0.52 to 0.97 s, close to RACK off
  (0.64 to 0.73 s) and slower than a fetch without loss (about 0.30 s).
- #16659 (revised or first version), #16660 and the two together did not
  remove the stall: 4 to 8 of 10 runs took over 1 s, some up to the 30 s
  timeout, which cannot be told apart from the pin (p = 0.63 to 1.0). The
  #16659 builds had an RTO in fewer runs (4 or 5 of 10, against 8 of 10
  for the pin), but that is not significant (p = 0.17 to 0.35).
- The 20 runs without an RTO at queue 100 took 0.31 to 0.50 s, although
  the queue dropped 1,658 to 4,157 packets in them. The #16659 builds had
  more of these runs (5 or 6 of 10, against 2 of 10 for the pin), which
  raises their median queue drops.
- At queue 256, the pin crawled in 2 of 10 runs (12.4 s and 13.0 s). The
  two #16659 builds crawled in none of theirs and had no RTO at all. With
  p = 0.47, this is suggestive only.

## Why the queue overflows

In the pin, HyStart ends slow start by setting ssthresh to the window,
inside the `Update` call in `pkg/tcpip/transport/tcp/cubic.go` that then
takes CUBIC's congestion-avoidance path, `getCwnd`, for the same ACK.
HyStart's exit does not start a new CUBIC epoch, so on a connection that
has not lost a packet yet, `getCwnd`'s Reno-friendly estimate is
0.53 × t / SRTT packets, where t is the time since the connection was set
up, idle time included, and `getCwnd` sets the window to that estimate at
once. With t a little over 3 s and an SRTT near 3.5 ms, that is 400 to
500 packets, while the 3 ms path holds about 25 packets plus the queue.
When a loss ends slow start instead, CUBIC starts a new epoch and there
is no jump. On master,
[google/gvisor#15579](https://github.com/google/gvisor/pull/15579) removed
the instant jump, and
[google/gvisor#15604](https://github.com/google/gvisor/pull/15604)
replaced the time-based estimate and starts the epoch when congestion
avoidance begins. The pin has neither. On the 20 ms path, the same
estimate is about 80 packets, less than the window slow start had
reached there, so it did not raise the window.

## Limits

- The machine was not quiet:
  - Main batch: the 1-minute load at the start of a run was 5.4 to 46.9
    (`runs.csv`). Another job's Linux virtual machine ran builds and
    tests on the same machine from before the batch began until 17:44
    UTC; the `-pass2` cells ran after it had stopped. Another job built
    and smoke-tested the fix-test binaries from 17:31 to 17:34 UTC. The
    emulator runs on timers in one process, and crawls were rarer at
    high load:

    | pin, RACK on, 3 ms, 2.8 MB | load below 16 | load 16 or more |
    |---|---|---|
    | queue 50 | 8 of 8 crawled | 2 of 4 |
    | queue 100, both passes | 9 of 11 | 1 of 5 |
    | queue 256, both passes | 2 of 9 | 0 of 7 |

    The first passes of the queue-256, 20 ms and 20 MB cells ran mostly
    at high load (median 22, 38 and 26), so they may undercount crawls.
  - Fix test: the load was 2.9 to 7.4 (median 4.6 at queue 100 and 5.5
    at queue 256). The Linux virtual machine was stopped when the fix
    test began, but ran from 18:08:51 to 18:17:06 UTC, so seeds 6 to 10
    at queue 100 and the whole queue-256 cell ran with it up. It used up
    to about 5 cores for about 20 s around 18:12:20 UTC, near the end of
    seed 8, and at most about 1 core otherwise. Both halves show the
    same result: with seeds 1 to 5, the pin took over 1 s in 4 of 5
    runs, and with seeds 6 to 10 in 2 of 5. The #16081 builds took over
    1 s in none of their 10 runs in either half, and every other RACK-on
    build took over 1 s in at least 2 runs of each half.
- The samples are small. With 10 runs per build, the fix test cannot
  tell #16659, its first version, #16660 or the two together apart from
  the pin. 0 of 16 on the 20 ms path and 0 of 8 without client
  timestamps do not show that those cases are safe; after a scripted
  burst, the stall of a client without timestamps can last minutes (see
  the top-level README).
- Every fetch started 3 s after its connection was set up. The jump grows
  with that time and shrinks as the RTT grows. Going by the code above, a
  response sent right after the handshake should not get it (not
  measured). On a connection that has recovered from a loss, t counts
  from the end of that recovery instead (`../cubic-idle/`).
- The path is emulated, with a drop-tail queue. AQM such as CoDel or PIE
  drops packets differently (not measured). The client is gVisor with an
  8 MiB receive buffer, so its window never limits the sender. A real
  client's window can: the capture in tailscale/tailscale#21946 shows
  about 460 KB, about 375 packets, which is still more than this path and
  its queue hold.
- The fix test ran only on the 3 ms path, with client timestamps on. It
  did not test #15432 alone, other open fixes such as
  [google/gvisor#16495](https://github.com/google/gvisor/pull/16495), or
  master with any fix.
- No run used `-trace`, so the jump and the first RTO are seen only in
  the counters each fetch reports.
- These runs have no Linux sender reference.

## Files

- `raw/<cell>/<build>-rack<on|off>.txt`: the harness output of every run
  in the main batch (33 files). Each run starts with a `=== run` line
  (seed, UTC start time, 1-minute load, command) and ends with an
  `=== end` line (exit status and wall-clock seconds).
- `runs.csv`: one row per main-batch run (280 rows), parsed from `raw/`:
  start time, load, time, status (`ok` or `timeout`), RTOs, median
  retransmissions between RTOs, time of the first RTO, retransmitted and
  data packets, queue drops, the most data packets sent in 1 ms, the
  largest congestion window, SACK recoveries, client timestamps, exit
  status and wall-clock seconds.
- `fix-test/raw/<cell>/<build>.txt`: the same for the fix test (11
  files). A patched build's `sender:` line shows
  `=> fix-test/trees/gvisor-<build>`, the patched copy of the module that
  the build used, with the local part of its path cut.
- `fix-test/runs.csv`: one row per fix-test run (110 rows), with the
  columns above plus RACK, the run's place in its seed's order (`pos`),
  fast retransmits, TLP recoveries, spurious RTO recoveries and the
  binary.
- `fix-test/patches/`: #16659 and #16660 for the pin.
- `table.md` and `fix-test/table.md`: per-build summaries of the two
  `runs.csv` files, with the per-seed times of the fix test.
- `fix-test/build.sh`, `fix-test/measure.sh`: the scripts that built the
  fix-test binaries and ran the fix test, as they ran next to a checkout
  of this repository at `94a018b` (only their comments edited since).
- `fix-test/summarize.py`: writes `fix-test/runs.csv` and
  `fix-test/table.md` from `fix-test/raw/`
  (`python3 -I summarize.py <fix-test folder>`).
