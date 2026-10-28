#!/usr/bin/env bash
# End-to-end check of the netstack stall with real tailscaled. Two nodes run
# in network namespaces of their own on a private Headscale tailnet, joined
# by tsbridge, which carries their WireGuard packets over the emulated path
# of netstack-rack-repro (3 ms RTT, 80 Mbit/s toward the client, scripted
# burst loss). The server node fronts a 2.8 MB file with `tailscale serve`,
# so the response comes from netstack's TCP, as in tailscale/tailscale#21946;
# the client node runs in TUN mode, so the client is kernel TCP.
#
# Usage: run-e2e.sh up | run VARIANT SCENARIO TRIALS | down   (as root; build.sh first)
# Variants of the server node: shipped (v1.104.2 plus the knob, unset, so
# RACK is on as shipped), rack-off (the same with TS_NETSTACK_TCP_RACK=false),
# pr15432-16081 (+ google/gvisor#15432 and #16081) and
# pr15432-16081-tsecr-tlpnew (+ the TSecr fix and the probe fix). Scenarios: default (tail and
# probe lost: 150 ms burst after the first 1000 packets), tail-tlp-survives
# (the burst ends when the sender resumes after a 3 ms pause: its probe).
set -euo pipefail
E="$(cd "$(dirname "$0")" && pwd)"
OUT="$E/out"
RESULTS="$E/results/$(date -u +%Y-%m-%d)"
RUN="$E/run"
HS="$E/hs"
hs() { "$E/bin/headscale" -c "$HS/config.yaml" "$@"; }
tsc() { local side=$1; shift; ip netns exec "$side" "$OUT/shipped/tailscale" --socket="$RUN/$side.sock" "$@"; }

# Why: PIDs are reused quickly here, so a stale pid file could name an
# unrelated process; each is remembered with its start time and checked.
started() { awk '{print $22}' "/proc/$1/stat" 2>/dev/null; }
remember() { echo "$1 $(started "$1")" >"$RUN/$2.pid"; }
pid_of() { local p _; read -r p _ <"$RUN/$1.pid" && echo "$p"; }
stop() {
  local p st
  if read -r p st <"$RUN/$1.pid" 2>/dev/null && [[ -n "$p" && "$(started "$p")" == "$st" ]]; then
    kill "$p"
    for _ in $(seq 50); do [[ "$(started "$p")" == "$st" ]] || break; sleep 0.1; done
  fi
  rm -f "$RUN/$1.pid"
}
wait_for() { # wait_for SECONDS DESCRIPTION COMMAND...
  local secs=$1 what=$2; shift 2
  for _ in $(seq $((secs * 10))); do "$@" >/dev/null 2>&1 && return 0; sleep 0.1; done
  echo "timed out waiting for $what" >&2
  return 1
}

down() {
  for p in driver srv-tailscaled cli-tailscaled backend bridge derper headscale; do stop "$p"; done
  # Why: deleting a namespace frees its devices asynchronously, so a quick
  # rerun could find the old veths; deleting them directly is synchronous.
  ip link del hs-srv 2>/dev/null || true
  ip link del hs-cli 2>/dev/null || true
  ip netns del srv 2>/dev/null || true
  ip netns del cli 2>/dev/null || true
  ip addr del 10.99.0.1/32 dev lo 2>/dev/null || true
}

start_server() { # start_server FLAVOR [VAR=value ...]
  local flavor=$1; shift
  stop srv-tailscaled
  # Why: tailscaled must run in the srv namespace, and env sets the knob for
  # this process only. TS_DEBUG_USE_DERP_HTTP lets it reach the local derper
  # without TLS.
  ip netns exec srv env TS_DEBUG_USE_DERP_HTTP=1 "$@" "$OUT/$flavor/tailscaled" --tun=userspace-networking --port=41641 \
    --statedir="$RUN/srv-state" --socket="$RUN/srv.sock" >>"$RUN/srv-tailscaled.log" 2>&1 &
  remember $! srv-tailscaled
  wait_for 30 "the server node's socket" test -S "$RUN/srv.sock"
  # Why: before its first `tailscale up`, the node waits for a login instead.
  [[ -f "$RUN/srv-joined" ]] || return 0
  wait_for 60 "the server node to run" sh -c "'$OUT/shipped/tailscale' --socket='$RUN/srv.sock' status --json | grep -q '\"BackendState\": \"Running\"'"
}

up() {
  down
  mkdir -p "$RUN" "$HS/lib" "$HS/run" "$E/www"
  rm -rf "$RUN"/*.log "$RUN"/srv-state "$RUN"/cli-state "$RUN/srv-joined" "$E/reports.jsonl"
  # The two namespaces, each with a veth to this namespace for the
  # control plane only; the tailnet traffic between them goes through tsbridge.
  ip addr add 10.99.0.1/32 dev lo
  for side in srv cli; do
    n=$([[ $side == srv ]] && echo 2 || echo 3)
    ip netns add "$side"
    ip -n "$side" link set lo up
    ip link add "hs-$side" type veth peer name ctl netns "$side"
    ip addr add "10.99.$n.1/24" dev "hs-$side"
    ip link set "hs-$side" up
    ip -n "$side" addr add "10.99.$n.2/24" dev ctl
    ip -n "$side" link set ctl up
    ip -n "$side" route add 10.99.0.1/32 via "10.99.$n.1"
  done

  cat >"$HS/config.yaml" <<EOF
server_url: http://10.99.0.1:8080
listen_addr: 10.99.0.1:8080
metrics_listen_addr: 127.0.0.1:19090
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false
noise:
  private_key_path: $HS/lib/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential
derp:
  server:
    enabled: false
  urls: []
  paths:
    - $HS/derp.yaml
  auto_update_enabled: false
  update_frequency: 24h
disable_check_updates: true
database:
  type: sqlite
  sqlite:
    path: $HS/lib/db.sqlite
log:
  level: info
policy:
  mode: file
  path: ""
dns:
  magic_dns: false
  base_domain: tailnet.test
  override_local_dns: false
  nameservers:
    global: []
unix_socket: $HS/run/headscale.sock
unix_socket_permission: "0770"
logtail:
  enabled: false
taildrop:
  enabled: false
EOF
  # Why: headscale needs a DERP region, and without a working DERP and STUN
  # server tailscaled's netcheck fails and it rebinds its sockets every few
  # seconds, which disrupts the path. The local derper runs plain HTTP; the
  # nodes still talk directly, over tsbridge, and reach it over their veths.
  cat >"$HS/derp.yaml" <<EOF
regions:
  900:
    regionid: 900
    regioncode: none
    regionname: Local
    nodes:
      - name: 900a
        regionid: 900
        hostname: 10.99.0.1
        ipv4: 10.99.0.1
        stunport: 3478
        derpport: 3340
EOF
  "$E/bin/derper" -a 10.99.0.1:3340 -http-port -1 -stun-port 3478 -hostname 10.99.0.1 >"$RUN/derper.log" 2>&1 &
  remember $! derper
  rm -f "$HS/lib/db.sqlite"*
  "$E/bin/headscale" -c "$HS/config.yaml" serve >"$RUN/headscale.log" 2>&1 &
  remember $! headscale
  wait_for 30 "headscale" test -S "$HS/run/headscale.sock"
  hs users create e2e >/dev/null
  uid="$(hs users list -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')"
  key="$(hs preauthkeys create --user "$uid" --reusable --expiration 24h -o json | python3 -c 'import json,sys; print(json.load(sys.stdin)["key"])')"

  "$E/bin/tsbridge" -ns-a /var/run/netns/srv -ns-b /var/run/netns/cli -idle-gap 100ms \
    -arm-file "$E/arm.json" -report-file "$E/reports.jsonl" >"$RUN/bridge.log" 2>&1 &
  remember $! bridge
  wait_for 10 "the bridge devices" ip -n cli link show tsb
  for side in srv cli; do
    dev=$([[ $side == srv ]] && echo tsa || echo tsb)
    addr=$([[ $side == srv ]] && echo 10.99.1.1 || echo 10.99.1.2)
    ip -n "$side" addr add "$addr/24" dev "$dev"
    # Why: tailscaled sends bursts of thousands of packets; a short device
    # queue would drop some before tsbridge reads them, which would be loss
    # the experiment did not script.
    ip -n "$side" link set "$dev" mtu 1500 txqueuelen 20000 up
  done

  head -c 2800000 /dev/urandom >"$E/www/file.bin"
  ip netns exec srv python3 -m http.server 8000 --bind 127.0.0.1 --directory "$E/www" >"$RUN/backend.log" 2>&1 &
  remember $! backend

  ip netns exec cli env TS_DEBUG_USE_DERP_HTTP=1 "$OUT/shipped/tailscaled" --tun=tailscale0 --port=41642 \
    --statedir="$RUN/cli-state" --socket="$RUN/cli.sock" >"$RUN/cli-tailscaled.log" 2>&1 &
  remember $! cli-tailscaled
  wait_for 30 "the client node's socket" test -S "$RUN/cli.sock"
  tsc cli up --login-server=http://10.99.0.1:8080 --authkey="$key" --hostname=cli --accept-dns=false --netfilter-mode=off

  start_server shipped
  tsc srv up --login-server=http://10.99.0.1:8080 --authkey="$key" --hostname=srv --accept-dns=false
  touch "$RUN/srv-joined"
  tsc srv serve --bg --http=80 http://127.0.0.1:8000
  echo "server tailnet IP: $(tsc srv ip -4)"
  tsc cli ping -c 10 --until-direct "$(tsc srv ip -4)"
}

run() { # run VARIANT SCENARIO TRIALS
  local variant=$1 scenario=$2 trials=$3 params
  case "$variant" in
    shipped) start_server shipped ;;
    rack-off) start_server shipped TS_NETSTACK_TCP_RACK=false ;;
    pr15432-16081 | pr15432-16081-tsecr-tlpnew) start_server "$variant" ;;
    *) echo "unknown variant $variant" >&2; return 2 ;;
  esac
  case "$scenario" in
    default) params='{"keep_first":1000,"drop_count":1400,"window_ns":150000000,"until_gap_ns":0}' ;;
    tail-tlp-survives) params='{"keep_first":1000,"drop_count":100000,"window_ns":0,"until_gap_ns":3000000}' ;;
    *) echo "unknown scenario $scenario" >&2; return 2 ;;
  esac
  local ip
  mkdir -p "$RESULTS"
  ip="$(tsc srv ip -4)"
  # Why: a restarted server node has a new disco key, which reaches the
  # client through the control server, so the direct path takes a few seconds.
  wait_for 90 "a direct path to the server node" tsc cli ping -c 3 --until-direct --timeout 1s "$ip"
  {
    echo "variant $variant, scenario $scenario: $params"
    echo "server: $(tsc srv version | head -1)"
    # Why: serve matches requests by the node's name, so the Host header
    # carries it while the connection goes to the tailnet IP.
    ip netns exec cli python3 "$E/fetch-trials.py" "http://$ip:80/file.bin" "$trials" "$(pid_of bridge)" \
      "$E/arm.json" "$E/reports.jsonl" "$params" srv.tailnet.test
    echo "tsa drops: $(ip -n srv -s link show tsa | awk '/TX:/{getline; print "tx dropped " $4}')"
  } | tee "$RESULTS/$scenario-$variant.txt"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  run) shift; run "$@" ;;
  *) sed -n '2,16p' "$0"; exit 2 ;;
esac
