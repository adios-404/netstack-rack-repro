# Reordering on gVisor master, 2026-11-05

A rerun of the headline cells of [2026-10-31](../../2026-10-31/reorder/)
for the rebased copy of
[#14458](https://github.com/google/gvisor/pull/14458), on a newer master.
Three builds of the reproduction, each on gVisor master `e3496657de` plus
the non-test code of the named branches of
[adios-404/gvisor](https://github.com/adios-404/gvisor):

| build | gVisor |
|---|---|
| `master` | master |
| `carry` | + #14458 rebased (`carry-14458-spurious-recovery`, `963e83a958`) |
| `step8` | + #14458 rebased + RFC 4015 step (8) (`spurious-rto-resume-unsent`, `4cef606678`) |

The gVisor "go" branch had not yet exported `e3496657de`, so every build
starts from its export of `94c03f66c` (`47b82d2938`) and adds the non-test
code of the two newer master commits. One of them, `48e209b560`, removes a
field that the exported save/restore code still names; the builds replace
it with a throwaway variable, since the reproduction never saves or
restores. Every `sender:` line in `raw/` names
`v0.0.0-20260915211658-a6f909f08a72`, the version `go.mod` requires; each
build replaces it with the tree above.

## How it ran

As on 2026-10-31: the sender uses the harness defaults, which are
Tailscale's netstack settings (CUBIC, SACK, host GSO, MTU 1280); the client
is gVisor; every fetch is 20 MB on a new connection, after one warm-up
fetch. Run i of every build uses `-seed i`, and within a cell the builds
ran back to back, one process at a time, in an order that rotates with the
seed. 16 runs per build and cell, on a 1 ms, 1 Gbit/s path:

| cell | reordering | RACK | timestamps |
|---|---|---|---|
| `lan-1pct-200us-rackoff-tson` | 1% of data packets held 200 µs | off | on |
| `lan-1pct-200us-rackoff-tsoff` | same | off | off |
| `lan-1pct-200us-rackon-tson` | same | on | on |
| `lan-noreorder-rackoff-tson` | none | off | on |

```sh
bin/repro-<build> -loss none -rtt 1ms -rate-mbps 1000 -size 20000000 \
  -warmup 1 -fetches 1 -trials 1 -interval 100ms \
  -reorder-pct 1 -reorder-hold 200us -rack=false -client-timestamps=true -seed 1
```

The machine was not idle: `load.txt` shows a load average between 7.7
and 12.6 during the run. The builds alternated within each seed, so they
shared the same conditions.

## Results

Median time of a measured fetch, in seconds. benchstat compares each build
with `master`: every difference shown has p < 0.001 (n = 16); `~` means no
significant difference.

| cell | RACK | timestamps | master | carry | step8 |
|---|---|---|---|---|---|
| 1% held 200 µs | off | on | 1.33 | 0.268 | 0.266 |
| 1% held 200 µs | off | off | 1.33 | 1.32 ~ | 1.33 ~ |
| 1% held 200 µs | on | on | 1.18 | 0.196 | 0.192 |
| none | off | on | 0.169 | 0.169 ~ | 0.169 ~ |

With RACK off and timestamps on, the median number of retransmitted
packets per fetch rose from 95.5 on `master` to 156.5 with `carry` (156
with `step8`). No run had a queue drop or a retransmission timeout, so
step (8), which acts only after a spurious timeout, never ran; `step8`
matches `carry`. The numbers agree with 2026-10-31 (1.30 s and 0.258 s on master
`aa767e4c4`).

Files: `raw/<cell>/<build>.txt` (harness output), `bench/<build>.txt`
(Go benchmark format: sec/op, retx/op, rtos/op, spurious-rto/op),
`benchstat-*.txt`, `load.txt`.
