# Pangolin's newt and the wireguard-go netstack, 2026-11-03

Pangolin's site connector, [newt](https://github.com/fosrl/newt), carries
every download from a home server to the Pangolin server through gVisor's
TCP. Its main tunnel runs on wireguard-go's `tun/netstack`, and its client
tunnels on `netstack2`, newt's copy of that file. Both pin
`gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c` (master `5c4b4f51c`,
2025-05-02), the gVisor that wireguard-go itself still pins (master
`b3bc704ef0ee`, 2026-10-24), and both leave RACK at gVisor's default, on.
flyctl (v0.4.123 and later), wireproxy and Sliver use the same netstack and
gVisor.

Two setups: the harness with newt's TCP settings (`-profile wireguard-go`),
and `e2e-newt/`, which runs newt's own proxy and netstack over wireguard-go
against Linux WireGuard and Linux TCP.

## Harness

`build-wgpin-variants.sh`; every run uses `-profile wireguard-go` (Reno,
SACK on, gVisor's default buffers, no GSO, MTU 1280), as each run's
`sender:` line records.

| build | gVisor |
|---|---|
| `wg-pin-rack-on` | the pin, as newt runs it |
| `wg-pin-rack-off` | the pin with `-rack=false` (`TCPRecovery(0)`), as the proposed newt change runs it |
| `wg-pr16659-16660-rack-on` | the pin + [#16659](https://github.com/google/gvisor/pull/16659) (`581d0bc94`) + [#16660](https://github.com/google/gvisor/pull/16660) (`f739c6928`) |
| `wg-pr16081-rack-on` | the pin + [#16081](https://github.com/google/gvisor/pull/16081) |
| `wg-pr15432-16081-rack-on` | the pin + [#15432](https://github.com/google/gvisor/pull/15432) + #16081 |
| `wg-pr15432-16081-16659-16660-rack-on` | the pin + all four |

The `patches/wgpin-*` files carry each PR's non-test code, ported by hand:
only the surrounding lines differ from the PRs. gVisor's TCP tests for this
pin (`go test ./pkg/tcpip/transport/tcp/...` in a tree from
`gvisor-go-test-overlay.sh`, on Linux) pass on the pin as is, and with
#16081, #16659 and #16660 applied together. With the #15432 port,
`TestRACKWithWindowFull` fails in 10 of 10 runs, alone or with the others
(its `resendSegment` hunk alone passes): #15432 targets current master, not
this pin, so the rows with #15432, and `e2e-newt`'s patched build, use a
port that fails that one test.

### Scripted loss (`wgpin/`, `wgpin-wan/`)

`run-matrix.sh` scenarios (see its header), 3 ms RTT and 80 Mbit/s unless
noted; `wgpin-wan/` is the same with `-rtt 30ms -rate-mbps 100`. Each cell:
fetch times, min–max (median), and RTOs per fetch from gVisor's counter.

3 ms RTT, 80 Mbit/s (`wgpin/`; the last two columns are `wgpin-quiet/`, rerun two at a time):

| build | tail and probe lost | one connection, 5 fetches | probe arrives | probe arrives, client without timestamps | hole mid-flight | end of transfer |
|---|---|---|---|---|---|---|
| pin, RACK on (newt now) | 104.51–105.60 s (105.09), 503 RTOs | 9.10–105.75 s (12.56), 42–503 RTOs | 17.74–108.68 s (105.55), 0–503 RTOs | 0.53–120.00 s (1.78), 0–9 RTOs | 0.30–0.51 s (0.36), 0–1 RTOs | 0.22–0.22 s (0.22), 1 RTO |
| pin, RACK off (the newt change) | 0.52–0.56 s (0.55), 1 RTO | 0.52–0.55 s (0.52), 1 RTO | 0.47–0.62 s (0.55), 1 RTO | 0.48–1.15 s (0.51), 1 RTO | 0.34–0.50 s (0.39), 0–1 RTOs | 0.21–0.22 s (0.21), 1 RTO |
| + #16659 + #16660 | 104.80–105.61 s (105.29), 503 RTOs | 12.75–106.08 s (14.03), 60–503 RTOs | 0.65–105.52 s (105.02), 1–503 RTOs | 0.53–120.00 s (29.71), 0–9 RTOs | 0.30–0.46 s (0.30), 0–1 RTOs | 0.02–0.22 s (0.02), 0–1 RTOs |
| + #16081 | 0.54–0.67 s (0.66), 0–1 RTOs | 0.55–0.66 s (0.57), 1 RTO | 0.30–1.37 s (0.66), 0–1 RTOs | 0.56–2.00 s (0.59), 0–2 RTOs | 0.30–0.34 s (0.30), 0 RTOs | 0.22–0.22 s (0.22), 1 RTO |
| + #15432 + #16081 | 0.65–0.72 s (0.66), 1 RTO | 0.54–0.66 s (0.56), 1 RTO | 0.46–1.43 s (0.65), 0–1 RTOs | 0.35–1.69 s (0.62), 0–1 RTOs | 0.36–0.47 s (0.38), 0–1 RTOs | 0.22–0.22 s (0.22), 1 RTO |
| + all four | 0.43–0.59 s (0.46), 0 RTOs | 0.44–0.57 s (0.55), 0–1 RTOs | 0.33–1.31 s (0.51), 0–1 RTOs | 0.30–5.90 s (0.43), 0–2 RTOs | 0.36–0.53 s (0.38), 0–1 RTOs | 0.02–0.22 s (0.02), 0–1 RTOs |

30 ms RTT, 100 Mbit/s (`wgpin-wan/`):

| build | tail and probe lost | probe arrives |
|---|---|---|
| pin, RACK on (newt now) | 13.51–120.00 s (120.00), 0–456 RTOs | 0.45–120.00 s (120.00), 0–457 RTOs |
| pin, RACK off (the newt change) | 0.84–5.54 s (0.96), 0–1 RTOs | 0.53–0.95 s (0.84), 0–1 RTOs |
| + #16659 + #16660 | 120.00–120.02 s (120.00), 450–456 RTOs | 0.48–120.00 s (120.00), 0–455 RTOs |
| + #16081 | 0.96–1.00 s (0.96), 1 RTO | 0.48–0.96 s (0.96), 0–1 RTOs |
| + #15432 + #16081 | 0.96–0.98 s (0.96), 1 RTO | 0.73–0.98 s (0.94), 1 RTO |
| + all four | 0.64–0.82 s (0.71), 0 RTOs | 0.64–0.74 s (0.70), 0–1 RTOs |

A time of 120.00 s is a fetch that had not finished at the 120 s timeout.
On this pin, #16659 and #16660 alone do not end the crawl; #16081 does, and
all four together recover fastest, mostly without an RTO.

### No scripted loss (`wgpin-noloss/`)

One process per run: a new connection, 3 s idle, one 20 MB fetch, no
warm-up, 30 ms RTT, seeds 1–6, three builds interleaved, two runs at a time:

```sh
bin/repro-wgpin -profile wireguard-go -loss none -warmup 0 -fetches 1 \
  -trials 1 -timeout 120s -rtt 30ms -size 20000000 -seed N <cell flags>
```

| 20 MB, 30 ms RTT | RACK on (newt now) | RACK off (the PR) | RACK on + all four fixes |
|---|---|---|---|
| 50 Mbit/s, 256-packet queue | 4.4–9.7 s, median 5.0 (6 runs, RTOs ≤20) | 4.2–5.6 s, median 4.8 (6 runs, RTOs ≤3) | 4.3–5.4 s, median 4.7 (6 runs, RTOs ≤4) |
| 50 Mbit/s, 64-packet queue | 4.1–45.2 s, median 13.8 (6 runs, RTOs ≤143) | 4.1–6.6 s, median 4.6 (6 runs, RTOs ≤4) | 4.3–7.0 s, median 4.6 (6 runs, RTOs ≤1) |
| 50 Mbit/s, 16-packet queue | 14.0–57.6 s, median 27.0 (6 runs, RTOs ≤163) | 12.8–14.4 s, median 13.6 (6 runs, RTOs ≤19) | 11.7–14.5 s, median 13.6 (6 runs, RTOs ≤0) |
| 50 Mbit/s, 8-packet queue | 38.8–62.4 s, median 51.2 (6 runs, RTOs ≤122) | 25.2–34.4 s, median 30.3 (6 runs, RTOs ≤43) | 25.7–34.2 s, median 27.7 (6 runs, RTOs ≤2) |
| 100 Mbit/s, 1% of packets 1 ms late | 1.8–18.5 s, median 8.4 (6 runs, RTOs ≤0) | 15.6–18.7 s, median 15.9 (6 runs, RTOs ≤1) | 1.8–14.6 s, median 12.9 (6 runs, RTOs ≤0) |
| 100 Mbit/s, 25% of packets 15 µs late | 1.8–114.8 s, median 5.8 (6 runs, RTOs ≤414) | 2.0–2.9 s, median 2.6 (6 runs, RTOs ≤0) | 1.8–7.6 s, median 2.3 (6 runs, RTOs ≤2) |
| 100 Mbit/s, unlimited queue | 1.8–79.7 s, median 3.4 (6 runs, RTOs ≤275) | 1.8–2.5 s, median 2.3 (6 runs, RTOs ≤1) | 1.8–3.4 s, median 1.8 (6 runs, RTOs ≤1) |

Only the queue drops packets here (the `reorder` rows drop none), so the
first losses come from slow start overflowing it. With RACK on, three of six
fetches crawled at 64 packets (61–143 RTOs), and RTOs reached the hundreds in
other cells. RACK off never crawled; its one cost is the 1% reordering row,
where it treats late packets as lost and halves the window (median 15.9 s
against 8.4 s).

## End to end through newt (`e2e-newt/`)

`e2e-newt/` compiles a small program inside newt's own module, so it runs
newt's proxy (`proxy.ProxyManager`, as for a public resource) on newt's
main-tunnel netstack over wireguard-go, in front of a local HTTP server. In
another network namespace, a Linux kernel WireGuard interface and `curl`
stand in for Gerbil and Traefik, so the receiver is Linux TCP. netem shapes
the site's uplink, with a drop-tail buffer, and delays the return path by
30 ms (`run.sh`). Each fetch is a new connection after 3 s idle. Three
builds (`build.sh`): newt `dev` `76ff3b1` as is (RACK on), the proposed
change `91177a6` (RACK off, main tunnel on netstack2), and `76ff3b1` again
with its gVisor patched with the four fixes (RACK on). Five fetches per row
(three without loss), on the Linux VM.

| 30 ms RTT, Linux receiver | newt dev (RACK on) | this PR (RACK off) | newt dev + gVisor fixes (RACK on) |
|---|---|---|---|
| 2.8 MB, 100 Mbit/s, everything in flight lost once (`burst`) | 102.7–120.1 s, median 120.0; 4 of 5 unfinished at 120 s | 0.9–1.0 s, median 0.9 | 1.0–1.0 s, median 1.0 |
| 20 MB, 50 Mbit/s uplink, 256-packet buffer | 4.1–19.7 s, median 5.5 | 4.2–4.3 s, median 4.3 | 4.1–4.5 s, median 4.1 |
| 20 MB, 50 Mbit/s uplink, 64-packet buffer | 3.9–5.1 s, median 4.3 | 3.9–7.0 s, median 5.1 | 4.1–5.1 s, median 4.6 |
| 20 MB, 50 Mbit/s uplink, 16-packet buffer | 38.1–39.8 s, median 38.3 | 40.5–43.0 s, median 41.8 | 37.8–41.5 s, median 40.2 |
| 20 MB, 100 Mbit/s, 0.5% random loss each way | 27.4–29.4 s, median 27.9 | 25.1–31.2 s, median 28.4 | 24.6–30.5 s, median 28.2 |
| 20 MB, 100 Mbit/s uplink, 1000-packet buffer, no other loss | 6.4–44.8 s, median 6.6 | 2.5–2.7 s, median 2.7 | 2.7–2.7 s, median 2.7 |

`burst` keeps the first 1000 data packets of each fetch and then drops every
data packet for 150 ms, before encryption, like the harness's `default`
scenario. In the last row nothing is dropped on purpose: every build lost
about 2,000 packets when slow start overflowed the buffer, and with RACK on
the recovery took 6.4 s, or 44.8 s once, against 2.5–2.7 s for the others.
The change costs about 9% with a 16-packet buffer, and nothing measurable
with random loss.

`wireguard-go-patch/` runs a fourth build, `bin/e2e-newt-wgpatch`: newt
`76ff3b1` with only `patches/wireguard-go-disable-rack.patch` applied to the
wireguard-go it pins, so the main tunnel runs wireguard-go's own netstack
with RACK off. Its `site up` lines read like the first build's, because only
wireguard-go differs; the file names tell them apart. Run later the same day
on the same virtual machine, with one more pass of the first build on the
idle link:

| 30 ms RTT, Linux receiver | first build, same day (RACK on) | wireguard-go patched (RACK off) |
|---|---|---|
| 2.8 MB, 100 Mbit/s, everything in flight lost once (`burst`) | see the table above | 0.94–0.99 s |
| 20 MB, 50 Mbit/s uplink, 256-packet buffer | see the table above | 4.25–4.27 s |
| 20 MB, 50 Mbit/s uplink, 16-packet buffer | see the table above | 36.6–39.1 s |
| 20 MB, 100 Mbit/s, 0.5% random loss each way | see the table above | 18.2–31.2 s |
| 20 MB, 100 Mbit/s uplink, 1000-packet buffer, no other loss | 4.1, 48.1, 6.6 s | 2.6–2.7 s |

`first-pass/` is an earlier run of the first two builds with netem's delay
on the uplink. netem counts the packets it is delaying against its limit,
so its 64-packet "buffer" left almost no room and acted like a policer;
there the change was slower (median 22.1 s against 10.4 s). The rows above
replaced it. Its burst rows also re-armed the scripted loss after each
retransmission timeout, which `-idle-gap 2s` now prevents.

## Machine

Harness: macOS with Go 1.27.3, up to five runs at once beside a busy
virtual machine, so the fast cells carry some timing noise; the two last
columns of the 3 ms table were rerun two at a time (`wgpin-quiet/`). End to
end, and gVisor's TCP tests: a Linux arm64 virtual machine.
