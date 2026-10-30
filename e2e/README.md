# End-to-end check with real tailscaled

The harness in the parent directory runs gVisor's TCP directly. This
directory runs the same loss scenarios through two real `tailscaled`
v1.104.2 nodes, on a private tailnet, so the response comes from netstack
behind `tailscale serve`, as in
[tailscale/tailscale#21946](https://github.com/tailscale/tailscale/issues/21946),
and the client is Linux kernel TCP.

## How it works

- Two network namespaces, `srv` and `cli`, each run a `tailscaled`. The
  server node runs in userspace-networking mode and fronts a 2.8 MB file
  with `tailscale serve --http=80`, so netstack sends the response. The
  client node runs in TUN mode, so the client is Linux kernel TCP, with
  timestamps on, as macOS and Linux offer them.
- Their WireGuard packets cross `tsbridge`, which creates a TUN device in
  each namespace and applies the parent harness's emulated path
  (`../link-emulator.go`): 3 ms RTT plus up to 2 × 200 µs of seeded jitter
  that never reorders, 80 Mbit/s toward the client with an unlimited queue,
  and the same scripted burst loss. It counts WireGuard data packets (UDP
  payload of 1000 bytes or more) instead of TCP segments; each carries one
  packet of the tunnel. `tailscale ping` shows the nodes talking directly
  over it (`via 10.99.1.1:41641`).
- The control plane is private: Headscale v0.29.6, and Tailscale's `derper`
  in plain-HTTP mode (`TS_DEBUG_USE_DERP_HTTP=1`). The nodes need the
  derper only for netcheck's STUN; without it, `tailscaled` rebinds its
  sockets every few seconds and disrupts the path. No Tailscale account and
  no internet access are needed once everything is built.
- Each trial opens a new connection and makes two loss-free warm-up fetches
  and one measured fetch, each after 3 s of idle time
  (`fetch-trials.py`). Before the measured fetch, the bridge is armed to
  drop one burst in that response, and afterwards it reports what it saw.

Scenarios, as in the parent harness:

- `default`: after the first 1000 data packets of the response, everything
  for 150 ms (the tail and the loss probe are lost).
- `tail-tlp-survives`: after the first 1000, everything until the sender
  resumes after a pause of 3 ms or more, which is its loss probe.

Server variants:

- `shipped`: v1.104.2 plus the `TS_NETSTACK_TCP_RACK` knob, unset, so RACK
  is on, as shipped. This is the knob as proposed in
  tailscale/tailscale#22045; Tailscale merged it as `TS_NETSTACK_ENABLE_RACK`
  in tailscale/tailscale#22069.
- `rack-off`: the same binary with `TS_NETSTACK_TCP_RACK=false`.
- `pr15432-16081`: the same with google/gvisor#15432 and #16081 applied to
  the gVisor that v1.104.2 pins (`../patches/pin-1-*`, `pin-2-*`).
- `pr15432-16081-tsecr-tlpnew`: the same plus the two changes proposed in
  the parent README (`../patches/pin-4-*`, `pin-5-*`).

## Run it

Linux, as root, with Go, git, patch, python3 and iproute2:

```sh
./build.sh                 # tailscaled variants, derper, headscale, tsbridge
./run-e2e.sh up            # namespaces, control plane, bridge, both nodes
./run-matrix-e2e.sh        # every variant in both scenarios (TRIALS=8)
./run-e2e.sh down
```

`run-e2e.sh run VARIANT SCENARIO TRIALS` runs one cell. Results go to
`results/<UTC date>/<scenario>-<variant>.txt`, one line per fetch, with the
bridge's report after each measured fetch; `summarize-e2e.py` makes the
tables below.

## Results

Eight trials per row, run with `run-matrix-e2e.sh` on 2026-10-28 from 04:32
to 04:46 UTC; `results/2026-10-28/` has every fetch and the bridge's report
on each measured one. The burst length runs from the first dropped packet
to the first one delivered after it. A wait is a silence of 100 ms or more
in the server's data packets after the burst: an RTO, whose minimum is
200 ms. No device queue dropped a packet in any run (`tsa drops` in each
file).

The tail and the loss probe lost (`default`):

| server | trials | burst length | fetch time | over 1 s | waits for an RTO after the burst |
|---|---|---|---|---|---|
| v1.104.2 as shipped (RACK on) | 8 | 215–264 ms | 4.04–6.59 s, median 4.65 s | 8 | 15–26; none in 0 of 8 |
| v1.104.2, `TS_NETSTACK_TCP_RACK=false` | 8 | 205–253 ms | 0.61–0.69 s, median 0.66 s | 0 | 0–0; none in 8 of 8 |
| v1.104.2 + google/gvisor#15432 + #16081 | 8 | 217–281 ms | 0.62–0.72 s, median 0.68 s | 0 | 0–0; none in 8 of 8 |
| the same + the TSecr fix + the probe fix | 8 | 215–264 ms | 0.57–0.67 s, median 0.65 s | 0 | 0–0; none in 8 of 8 |

The tail lost, the loss probe delivered (`tail-tlp-survives`):

| server | trials | burst length | fetch time | over 1 s | waits for an RTO after the burst |
|---|---|---|---|---|---|
| v1.104.2 as shipped (RACK on) | 8 | 15–18 ms | 0.37–4.17 s, median 3.88 s | 5 | 0–17; none in 3 of 8 |
| v1.104.2, `TS_NETSTACK_TCP_RACK=false` | 8 | 48–207 ms | 0.42–0.72 s, median 0.65 s | 0 | 0–1; none in 7 of 8 |
| v1.104.2 + google/gvisor#15432 + #16081 | 8 | 4–17 ms | 0.35–0.68 s, median 0.62 s | 0 | 0–1; none in 3 of 8 |
| the same + the TSecr fix + the probe fix | 8 | 15–38 ms | 0.36–0.47 s, median 0.38 s | 0 | 0–0; none in 8 of 8 |

- **The stall happens in real tailscaled.** As shipped, every fetch whose
  tail and loss probe were lost took 4.0–6.6 s, with 15–26 waits for an RTO
  after the burst, as in the parent harness (3.5–6.0 s).
- **The knob and the upstream fixes end it.** With
  `TS_NETSTACK_TCP_RACK=false`, or with google/gvisor#15432 and #16081,
  every fetch took 0.57–0.72 s: the burst ends with the RTO's
  retransmission, about 0.2 s in, and recovery needs no further wait.
- **With the loss probe delivered**, the burst ends 4–38 ms in, when the
  sender resumes after a pause. As shipped, 5 of 8 fetches still stalled
  (3.9–4.2 s); with #15432 and #16081, 5 of 8 waited for one RTO (0.62–0.68
  s); with the two proposed changes as well, all 8 recovered without an RTO
  (0.36–0.47 s, median 0.38 s against 0.62 s). The bridge cannot see which
  segment ended a burst, so these runs do not show why 3 of 8 recovered
  without the changes. With RACK off, 7 of 8 needed no RTO after the burst.

## Limits

- It runs on one machine, so both nodes share its CPUs; the emulated path
  times packets in user space, as the parent harness does.
- The bridge cannot see inside WireGuard, so it counts large packets as
  data; keepalives and disco messages are small and pass. It times RTO
  waits from the gaps in the server's data packets, not from tailscaled's
  counters.
- The client is Linux kernel TCP, not macOS.
