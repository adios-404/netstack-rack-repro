#!/usr/bin/env bash
# Runs fetches through newt's data path (see main.go) on Linux, as root.
#
# Two network namespaces joined by a veth pair: "site" runs the e2e-newt
# site (newt's netstack, wireguard-go and newt's proxy, in front of a local
# HTTP server), and "srv" has a kernel WireGuard interface and fetches with
# curl, so the receiver is Linux TCP (CUBIC, SACK, timestamps), as on a
# Pangolin server. netem on the site's veth shapes its uplink, which carries
# the downloads, with a drop-tail buffer; netem on the server's veth adds the
# whole 30 ms round trip to the return path. Why: netem counts the packets it
# is delaying against its limit, so delaying on the uplink would shrink the
# buffer by the packets in flight.
#
# Usage: sudo ./run.sh <site binary> <mode> [fetches] [bytes per fetch]
#   burst          100 Mbit/s uplink with a 1000-packet buffer;
#                  in each fetch the site delivers the first 1000 data packets,
#                  then drops every data packet for 150 ms (the harness's
#                  "default" burst, applied before encryption).
#   bottleneck-Q   no scripted loss: a 50 Mbit/s uplink whose drop-tail buffer
#                  holds Q packets, as on a home connection's upload (a small
#                  Q acts like a policer).
#   random-P       100 Mbit/s uplink with a 1000-packet buffer, and each
#                  direction loses P percent of packets at random.
# Each fetch is a new connection, after 3 s of idle time. Prints one line per
# fetch (seconds, bytes, Mbit/s) and the site's per-transfer packet counts.
set -euo pipefail
bin="$(realpath "${1:?site binary}")"
mode="${2:?mode: burst, bottleneck-Q or random-P}"
fetches="${3:-5}"
size="${4:-2800000}"
timeout=120

rate=100mbit limit=1000 loss= burst=()
case "$mode" in
burst) burst=(-burst-keep-first 1000 -burst-window 150ms) ;;
bottleneck-*) rate=50mbit limit="${mode#bottleneck-}" ;;
random-*) loss="loss ${mode#random-}%" ;;
*) echo "unknown mode $mode" >&2; exit 2 ;;
esac

log="$(mktemp)"
cleanup() {
  [[ -n "${site_pid:-}" ]] && kill "$site_pid" 2>/dev/null && wait "$site_pid" 2>/dev/null
  ip netns del srv 2>/dev/null || true
  ip netns del site 2>/dev/null || true
}
trap cleanup EXIT
cleanup

ip netns add srv
ip netns add site
ip link add veth-srv netns srv type veth peer name veth-site netns site
ip -n srv addr add 192.168.77.1/24 dev veth-srv
ip -n site addr add 192.168.77.2/24 dev veth-site
for ns in srv site; do ip -n "$ns" link set lo up; done
ip -n srv link set veth-srv up
ip -n site link set veth-site up
# shellcheck disable=SC2086 # $loss is empty or two words.
tc -n site qdisc add dev veth-site root netem limit "$limit" rate "$rate" $loss
# shellcheck disable=SC2086
tc -n srv qdisc add dev veth-srv root netem delay 30ms $loss

read -r srv_key srv_pub < <("$bin" keys)
read -r site_key site_pub < <("$bin" keys)
ip -n srv link add wg0 type wireguard
ip netns exec srv "$bin" wg-server -iface wg0 -key "$srv_key" -port 51820 -peer "$site_pub" -allowed 10.77.0.2/32
ip -n srv addr add 10.77.0.1/24 dev wg0
ip -n srv link set wg0 mtu 1280 up

ip netns exec site "$bin" site -key "$site_key" -peer "$srv_pub" -endpoint 192.168.77.1:51820 -idle-gap 2s "${burst[@]}" >"$log" 2>&1 &
site_pid=$!
for _ in $(seq 50); do
  if ip netns exec srv curl -s -o /dev/null --max-time 1 http://10.77.0.2:8080/bytes/10; then break; fi
  sleep 0.2
done
head -1 "$log"
echo "path: 30 ms RTT, uplink $rate, buffer $limit packets${loss:+, $loss}; $fetches fetches of $size bytes"

for i in $(seq "$fetches"); do
  sleep 3
  out="$(ip netns exec srv curl -s -o /dev/null --max-time "$timeout" -w '%{time_total} %{size_download}' "http://10.77.0.2:8080/bytes/$size" || true)"
  read -r t got <<<"$out"
  awk -v i="$i" -v t="$t" -v got="$got" -v want="$size" 'BEGIN {
    printf "fetch %d: %7.3f s, %d of %d bytes, %6.1f Mbit/s%s\n", i, t, got, want, got * 8 / t / 1e6, (got < want ? "  (incomplete)" : "") }'
done
kill "$site_pid"
wait "$site_pid" 2>/dev/null || true
site_pid=
grep '^.\{16\}transfer ' "$log" | sed 's/^.\{16\}/site: /' | tail -n "$fetches"
rm -f "$log"
