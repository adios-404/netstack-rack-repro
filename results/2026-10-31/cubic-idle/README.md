# CUBIC after idle, 2026-10-31

Two builds of the reproduction, as each run's `sender:` line records:

| build | gVisor |
|---|---|
| `pin` | `v0.0.0-20260915211658-a6f909f08a72`, which Tailscale's main and v1.104.x pin |
| `master` | `v0.0.0-20261031110703-16342efb9a78`, the go branch of master `aa767e4c4`, which has [#15579](https://github.com/google/gvisor/pull/15579) and [#15604](https://github.com/google/gvisor/pull/15604) |

## How it ran

The sender uses the harness defaults, which are Tailscale's netstack
settings (CUBIC, SACK, host GSO, MTU 1280); the client is gVisor and
offers timestamps. The path is 3 ms, 80 Mbit/s toward the client, with a
100-packet drop-tail queue.

Each run is one process and one connection: two warm-up fetches, one
2.8 MB fetch that loses 16 packets in the middle, the idle period, then one
2.8 MB fetch without scripted loss. Its `POST` line is the result:

```sh
bin/repro-<build> -rack=false -burst-drop 16 -burst-window 0 -queue 100 \
  -post-fetches 1 -trials 1 -post-interval 30s
```

The RACK-on runs add `-rack=true -timeout 30s`. The builds alternate, so
that slow changes in the machine's load cannot favor one of them.

## Results

The post-idle fetch: congestion window before the idle period → largest
during the fetch, packets the queue dropped, RTOs, and time. Runs with 1,
5 and 30 s of idle are two per build; 120 s is one. Every RACK-off pin
run's first warm-up overflowed the queue in slow start (2 RTOs); in
`pin-rackoff-5s-t2` and `pin-rackoff-120s-t1` the next two fetches did too
(1 RTO each).

| idle (RACK off) | pin | master |
|---|---|---|
| 1 s | cwnd 74–75 → 152–153, 28 queue drops, 0 RTOs, 0.30 s | cwnd 53–54 → 68–69, 0 queue drops, 0 RTOs, 0.30 s |
| 5 s | cwnd 88–101 → 401–417, 706–726 queue drops, 2 RTOs, 0.62 s | cwnd 54 → 68, 0 queue drops, 0 RTOs, 0.30 s |
| 30 s | cwnd 75 → 1,242–1,249, 1,732–1,885 queue drops, 4 RTOs, 1.22–1.23 s | cwnd 54 → 68, 0 queue drops, 0 RTOs, 0.30 s |
| 120 s | cwnd 103 → 13,983, 3,755 queue drops, 4 RTOs, 1.37 s | cwnd 60 → 69, 0 queue drops, 0 RTOs, 0.30 s |

With RACK on, as v1.104.x ships, the pin stalled before the idle period
mattered ([tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946)):
with 5 s of idle, the fetch with the loss took 29.6 s (141 RTOs) and the
post-idle fetch hit the 30 s timeout after 144 RTOs;
with 30 s of idle, the run ended at an earlier fetch's 30 s timeout. master
took 0.30 s in both.

## Files

- `<build>-<config>-t<n>.txt`: the harness output of each run.
- `summary.txt`: every run's `POST` line, in the order the runs went.
