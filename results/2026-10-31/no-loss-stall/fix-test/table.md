# Fix test: which fix removes the plain-download stall on Tailscale's gVisor pin

| Path | Build | RACK | Runs | Over 1 s | Crawls | 30 s timeouts | Median s | Max s | Runs with an RTO | Total RTOs | Median queue drops | Median retransmitted |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 3 ms, 80 Mbit/s, queue 100 | pin (v1.104.0–v1.104.4) | on | 10 | 6 | 6 | 0 | 13.0 | 30.0 | 8 | 629 | 1371 | 1449.5 |
| 3 ms, 80 Mbit/s, queue 100 | pin (v1.104.5, main) | off | 10 | 0 | 0 | 0 | 0.69 | 0.73 | 10 | 20 | 848.5 | 975 |
| 3 ms, 80 Mbit/s, queue 100 | pin + #16659 (TSecr fix, revised) | on | 10 | 5 | 4 | 0 | 1.10 | 29.2 | 5 | 219 | 2244 | 2259 |
| 3 ms, 80 Mbit/s, queue 100 | pin + TSecr fix, first version | on | 10 | 4 | 4 | 2 | 0.33 | 30.0 | 4 | 341 | 2925.5 | 2967 |
| 3 ms, 80 Mbit/s, queue 100 | pin + #16660 (loss-probe fix) | on | 10 | 8 | 7 | 1 | 17.4 | 30.0 | 10 | 703 | 696.5 | 750.5 |
| 3 ms, 80 Mbit/s, queue 100 | pin + #16659 + #16660 | on | 10 | 5 | 5 | 1 | 3.13 | 30.0 | 5 | 375 | 2872.5 | 2893 |
| 3 ms, 80 Mbit/s, queue 100 | pin + #16081 | on | 10 | 0 | 0 | 0 | 0.74 | 0.95 | 10 | 19 | 1259.5 | 1404 |
| 3 ms, 80 Mbit/s, queue 100 | pin + #15432 + #16081 | on | 10 | 0 | 0 | 0 | 0.54 | 0.97 | 8 | 13 | 1579 | 1631 |
| 3 ms, 80 Mbit/s, queue 256 | pin (v1.104.0–v1.104.4) | on | 10 | 2 | 2 | 0 | 0.30 | 13.0 | 2 | 119 | 312 | 317.5 |
| 3 ms, 80 Mbit/s, queue 256 | pin + #16659 (TSecr fix, revised) | on | 10 | 0 | 0 | 0 | 0.30 | 0.31 | 0 | 0 | 420 | 428 |
| 3 ms, 80 Mbit/s, queue 256 | pin + #16659 + #16660 | on | 10 | 0 | 0 | 0 | 0.30 | 0.31 | 0 | 0 | 396 | 406 |

Over 1 s: the fetch took more than 1 s or failed. Crawl: at least 10 RTOs with a median of at most 3 retransmissions per RTO interval (the tailscale/tailscale#21946 signature). 30 s timeouts: the fetch hit the harness's 30 s limit. All runs: client timestamps on, no scripted loss, 2.8 MB fetch on a fresh connection, 3 s idle first.

Over-1-s runs against the pin (RACK on) in the same cell, Fisher exact test, two-sided:

- 3 ms, 80 Mbit/s, queue 100, pin (v1.104.5, main): 0/10 against the pin's 6/10, p = 0.011
- 3 ms, 80 Mbit/s, queue 100, pin + #16659 (TSecr fix, revised): 5/10 against the pin's 6/10, p = 1.000
- 3 ms, 80 Mbit/s, queue 100, pin + TSecr fix, first version: 4/10 against the pin's 6/10, p = 0.656
- 3 ms, 80 Mbit/s, queue 100, pin + #16660 (loss-probe fix): 8/10 against the pin's 6/10, p = 0.628
- 3 ms, 80 Mbit/s, queue 100, pin + #16659 + #16660: 5/10 against the pin's 6/10, p = 1.000
- 3 ms, 80 Mbit/s, queue 100, pin + #16081: 0/10 against the pin's 6/10, p = 0.011
- 3 ms, 80 Mbit/s, queue 100, pin + #15432 + #16081: 0/10 against the pin's 6/10, p = 0.011
- 3 ms, 80 Mbit/s, queue 256, pin + #16659 (TSecr fix, revised): 0/10 against the pin's 2/10, p = 0.474
- 3 ms, 80 Mbit/s, queue 256, pin + #16659 + #16660: 0/10 against the pin's 2/10, p = 0.474

How measured (2026-10-31, 18:01:52-18:16:35 UTC, 883 s in all): one harness process per run, one process at a time, flags `-rack=true|false -loss none -warmup 0 -fetches 1 -trials 1 -timeout 30s -interval 3s -seed i -rtt 3ms -rate-mbps 80 -queue 100|256` (the main test's flags). N = 10 seeds per build; within each seed every build ran once, the order rotated by seed. Binaries: fix-test/bin, built by fix-test/build.sh (pin a6f909f08a72 plus the patches named in the build column; the pin binary differs from the main test's only in the harness commit stamp, 94a018b against e14785e, which changed results files only). A load gate (pause above load 16) never triggered: the 1-minute load was 2.9-7.4 at run start. The Linux VM was stopped when the batch began, but the VM manager's log shows it running from 18:08:51 to 18:17:06 UTC, so queue-100 seeds 6-10 and the whole queue-256 cell ran with it up. It used up to about 5 cores for about 20 s around 18:12:20 UTC (near the end of seed 8) and up to 1 core otherwise. Seeds 1-5 (VM stopped) and 6-10 (VM up) show the same split: no #16081 run over 1 s in either half, and every other RACK-on build has slow runs in both halves.

Fetch time in s per seed, 3 ms, 80 Mbit/s, queue 100 (T = 30 s timeout):

| Seed | Load at seed start | pin | pin-rackoff | pin-16659 | pin-16659orig | pin-16660 | pin-16659-16660 | pin-16081 | pin-15432-16081 |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 3.7 | 0.62 | 0.64 | 0.32 | 5.56 | 16.9 | 0.32 | 0.73 | 0.53 |
| 2 | 3.3 | 10.0 | 0.65 | 29.2 | 0.32 | 28.9 | 9.76 | 0.53 | 0.32 |
| 3 | 4.6 | 16.0 | 0.72 | 1.88 | 6.61 | 17.8 | T | 0.76 | 0.52 |
| 4 | 4.3 | 22.8 | 0.65 | 6.18 | 0.32 | 23.0 | 0.31 | 0.74 | 0.74 |
| 5 | 3.4 | 24.6 | 0.73 | 0.31 | 0.32 | T | 5.77 | 0.75 | 0.74 |
| 6 | 6.0 | 30.0 | 0.66 | 0.31 | T | 27.1 | 9.59 | 0.75 | 0.97 |
| 7 | 5.0 | 0.32 | 0.65 | 5.56 | 0.31 | 3.69 | 24.8 | 0.74 | 0.74 |
| 8 | 4.0 | 0.61 | 0.72 | 4.58 | 0.34 | 0.62 | 0.50 | 0.52 | 0.53 |
| 9 | 6.0 | 0.32 | 0.71 | 0.31 | 0.31 | 1.20 | 0.42 | 0.75 | 0.55 |
| 10 | 6.1 | 28.7 | 0.72 | 0.31 | T | 0.61 | 0.32 | 0.95 | 0.33 |

1-minute load at run start: min 2.9, median 4.6, max 6.3 (80 runs).

Fetch time in s per seed, 3 ms, 80 Mbit/s, queue 256 (T = 30 s timeout):

| Seed | Load at seed start | pin | pin-16659 | pin-16659-16660 |
|---:|---:|---:|---:|---:|
| 1 | 6.2 | 0.30 | 0.31 | 0.30 |
| 2 | 7.0 | 0.30 | 0.31 | 0.31 |
| 3 | 6.3 | 0.30 | 0.31 | 0.30 |
| 4 | 5.8 | 0.30 | 0.30 | 0.30 |
| 5 | 5.4 | 0.30 | 0.30 | 0.31 |
| 6 | 5.3 | 0.30 | 0.31 | 0.31 |
| 7 | 5.5 | 0.30 | 0.30 | 0.30 |
| 8 | 6.6 | 12.4 | 0.30 | 0.31 |
| 9 | 5.2 | 13.0 | 0.30 | 0.30 |
| 10 | 4.9 | 0.30 | 0.30 | 0.31 |

1-minute load at run start: min 4.7, median 5.5, max 7.4 (30 runs).
