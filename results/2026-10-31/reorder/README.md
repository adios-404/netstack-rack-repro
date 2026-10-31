# Reordering on gVisor master, 2026-10-31

Four builds of the reproduction, each on gVisor master `aa767e4c4` (go
branch `16342efb9`) plus the non-test code of the named pull requests:

| build | gVisor |
|---|---|
| `master` | master |
| `pr14101` | + [#14101](https://github.com/google/gvisor/pull/14101) (head `df000e6d7`) |
| `pr14458` | + [#14458](https://github.com/google/gvisor/pull/14458) (head `1af4ae880`) |
| `both` | + both |

The non-test code of each pull request applies to master as it is.
Together they conflict only in adjacent `sender` fields, and both were
kept. Every `sender:` line in `raw/` names
`v0.0.0-20260915211658-a6f909f08a72`, the version `go.mod` requires; each
build replaces it with the tree above.

## How it ran

The sender uses the harness defaults, which are Tailscale's netstack
settings (CUBIC, SACK, host GSO, MTU 1280); the client is gVisor. Every
fetch is 20 MB, on a new connection.

| cell | path | reordering (see [Reordering](../../../README.md#reordering)) | runs per build |
|---|---|---|---|
| `lan-1pct-200us` | 1 ms, 1 Gbit/s | 1% of data packets held 200 µs | 16 |
| `lan-25pct-15us` | 1 ms, 1 Gbit/s | 25% held 15 µs | 16 |
| `lan-noreorder` | 1 ms, 1 Gbit/s | none | 16 |
| `wan-1pct-1ms` | 30 ms, 200 Mbit/s | 1% held 1 ms | 4, without `pr14101` |
| `wan-noreorder` | 30 ms, 200 Mbit/s | none | 4, `master` and `both` only |

Each cell runs with RACK off and on (`-rack`). The two LAN reordering cells
also run with and without client timestamps (`-client-timestamps`); the
others have them on. LAN runs make one warm-up and one measured fetch;
WAN runs make one measured fetch. Run i of every build uses `-seed i`.
Within a cell the builds ran back to back, one process at a time, in an
order that rotates with the seed. The headline cell, for example:

```sh
bin/repro-<build> -loss none -rtt 1ms -rate-mbps 1000 -size 20000000 \
  -warmup 1 -fetches 1 -trials 1 -interval 100ms \
  -reorder-pct 1 -reorder-hold 200us -rack=false -client-timestamps=true -seed 1
```

WAN runs use `-rtt 30ms -rate-mbps 200 -warmup 0 -timeout 180s` instead.

## Results

Median time of a measured fetch, in seconds. benchstat compares each build
with `master`: every difference shown has p < 0.001 (n = 16), except the
WAN rows (n = 4, p = 0.029); `~` means no significant difference.

| cell | RACK | timestamps | master | pr14101 | pr14458 | both |
|---|---|---|---|---|---|---|
| 1% held 200 µs | off | on | 1.30 | 1.31 ~ | 0.258 | 0.254 |
| 1% held 200 µs | off | off | 1.33 | 1.30 ~ | 1.32 ~ | 1.28 ~ |
| 1% held 200 µs | on | on | 1.14 | 0.955 | 0.195 | 0.186 |
| 1% held 200 µs | on | off | 1.15 | 0.956 | 1.15 ~ | 0.935 |
| 25% held 15 µs | off | on | 2.82 | 1.89 | 0.575 | 0.560 |
| 25% held 15 µs | off | off | 2.85 | 1.94 | 2.85 ~ | 1.91 |
| 25% held 15 µs | on | on | 2.47 | 0.254 | 0.770 | 0.169 |
| 25% held 15 µs | on | off | 2.35 | 0.321 | 2.35 ~ | 0.314 |
| none | off, on | on | 0.169 | 0.169 ~ | 0.168–0.169 ~ | 0.168–0.169 ~ |
| WAN, 1% held 1 ms | off | on | 26.5 | | 4.43 | 4.43 |
| WAN, 1% held 1 ms | on | on | 16.1 | | 11.8 | 2.97 |
| WAN, none | off, on | on | 1.02 | | | 1.02 ~ |

#14458 undoes a recovery only after gVisor finds it was spurious, which
takes timestamps (RFC 3522), so it changes nothing when the client offers
none. With RACK
off, where it helps it also retransmits more, because it sends faster: 98
to 156 retransmissions per fetch in the first row, 163 to 767 in the
fifth. With RACK on, the count does not change significantly. No fetch in
any cell had an RTO.

The machine was not idle: the 1-minute load average was 3.6 to 7.3 during
the run (`load.txt`). benchstat's ± shows each cell's spread. This run has
no Linux sender reference.

## Files

- `raw/<cell>/<build>.txt`: the harness output of every run.
- `bench/<build>.txt`: one Go benchmark line per measured fetch (sec/op,
  retx/op, rtos/op, spurious-rto/op), parsed from `raw/`; RTOs are gVisor's
  `Timeouts` counter.
- `benchstat-all.txt`: the four builds side by side;
  `benchstat-master-vs-<build>.txt` and `benchstat-pr14458-vs-both.txt`:
  the pairs.
- `load.txt`: load averages before the run, after each seed, and after.
