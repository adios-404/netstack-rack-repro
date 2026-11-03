# gVisor's TCP tests on newt's pin

`go test -p 2 ./pkg/tcpip/transport/tcp/...` in a tree from
`gvisor-go-test-overlay.sh` (go branch `39ed1f5ac2` plus master `5c4b4f51c`'s
test files), on a Linux arm64 virtual machine:

- `gvisor-base.txt`: the pin as is. All 10 packages pass.
- `gvisor-all-four.txt`: with `patches/wgpin-1` to `wgpin-4`.
  `TestRACKWithWindowFull` fails.

`TestRACKWithWindowFull`, 10 runs each:

| patches | failing runs |
|---|---|
| wgpin-1 (#15432) | 10 of 10 |
| wgpin-1 without its `resendSegment` hunk | 10 of 10 |
| wgpin-1's `resendSegment` hunk alone | 0 of 10 |
| wgpin-2 (#16081), wgpin-3 (#16659), wgpin-4 (#16660), each alone | 0 of 10 |
| wgpin-2 + wgpin-3 + wgpin-4 | 0 of 10 |
| wgpin-1 without its `resendSegment` hunk + wgpin-2 + 3 + 4 | 10 of 10 |

With wgpin-2, 3 and 4 together, the whole suite passes (all 10 packages).
