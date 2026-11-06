#!/usr/bin/env bash
# Fix test: which in-flight gVisor fix removes the
# plain-download stall on Tailscale's gVisor pin (a6f909f08a72, RACK on)?
#
# Cell 1: 3 ms, 80 Mbit/s, 100-packet drop-tail queue, client timestamps on
# (harness default), no scripted loss. Eight builds, N=10 seeds.
# Cell 2 (optional, time permitting): the same path with a 256-packet queue,
# three builds, N=10 seeds.
#
# One process = one fresh connection = one measured fetch (-warmup 0
# -fetches 1 -trials 1), 30 s fetch timeout, -seed i for run i, the same flags
# as the main batch. Builds interleave within each seed, order rotated
# by seed. One process at a time.
set -u
FT="$(cd "$(dirname "$0")" && pwd)"
S="$FT/logs/measure"
mkdir -p "$S"

BIN="$FT/bin"
RAW="$FT/raw"
STATE="$FT/machine-state.txt"     # per seed: load averages + top 3 CPU users (basenames only)
RUNSTATE="$S/per-run-state.log"    # private: per run load + top 3
PROGRESS="$S/progress.log"
BUDGET=1800                        # seconds (30 min) for both cells, gate pauses included
Q256_EST=420                       # seconds; cell 2 starts only if it fits in the budget
N=10
COMMON="-loss none -warmup 0 -fetches 1 -trials 1 -timeout 30s -interval 3s"
P3="-rtt 3ms -rate-mbps 80"
# label:binary:rack
B100=(pin:repro-pin:on pin-rackoff:repro-pin:off pin-16659:repro-pin-16659:on
      pin-16659orig:repro-pin-16659orig:on pin-16660:repro-pin-16660:on
      pin-16659-16660:repro-pin-16659-16660:on pin-16081:repro-pin-16081:on
      pin-15432-16081:repro-pin-15432-16081:on)
B256=(pin:repro-pin:on pin-16659:repro-pin-16659:on pin-16659-16660:repro-pin-16659-16660:on)
RUN_Q256=${RUN_Q256:-1}

# Load gate, as in the main batch. Why: the Linux VM's builds push the
# 1-minute load to 20-45 on this machine; the harness's emulated link
# (goroutine timers) gets CPU-starved, and the main test saw the pin's crawl
# far less often above load ~20 than below ~16. Checked before every run.
HI=16          # pause when load1 is above this
LO=10          # resume when load1 drops below this
MAXPAUSE=120   # seconds; resume anyway after this long
MINRUN=45      # seconds of running after a resume before pausing again
TOTALCAP=600   # seconds; stop gating after this much total pause

mkdir -p "$RAW"
# Why: raw files are appended per run; refuse to mix two sessions' runs.
if [ -n "$(ls -A "$RAW")" ]; then echo "raw/ is not empty; move it away first" >&2; exit 1; fi
start=$(date +%s)
elapsed() { echo $(( $(date +%s) - start )); }
load1() { sysctl -n vm.loadavg | awk '{print $2}'; }
gt() { awk -v a="$1" -v b="$2" 'BEGIN{exit !(a > b)}'; }
top3() { ps -Ao pcpu,comm -r | sed -n '2,4p' | sed -E 's#^ *([0-9.]+) .*/#\1 #; s#^ *([0-9.]+) #\1 #'; }
# Why: patched builds print their gVisor replace path, which holds the local
# user name; keep only the part under fix-test/.
sanitize() { sed -E 's#/Users/[^ ]*/fix-test/#fix-test/#g; s#/(Users|private|var)/[^ ]*#<path>#g'; }
log() { echo "$(date -u +%TZ) +$(elapsed)s $*" >> "$PROGRESS"; }

state() { # label
  {
    echo "== $(date -u +%FT%TZ) (+$(elapsed)s) $1"
    echo "load averages: $(uptime | sed 's/.*load averages*: //')"
    echo "top 3 CPU users (%CPU, process):"
    top3 | sed 's/^/  /'
    echo
  } >> "$STATE"
}

paused=0
last_resume=0
gate() {
  local l t0 waited why
  l=$(load1)
  if gt "$l" "$HI" && (( $(date +%s) - last_resume >= MINRUN )) && (( paused < TOTALCAP )); then
    log "GATE pause at load $l (top: $(top3 | head -1))"
    t0=$(date +%s)
    while :; do
      sleep 3
      l=$(load1)
      waited=$(( $(date +%s) - t0 ))
      if ! gt "$l" "$LO"; then why="load $l"; break; fi
      if (( waited >= MAXPAUSE )); then why="max pause, load still $l"; break; fi
      if (( paused + waited >= TOTALCAP )); then why="total cap, load still $l"; break; fi
    done
    paused=$(( paused + waited ))
    last_resume=$(date +%s)
    log "GATE resume after ${waited}s ($why); total paused ${paused}s"
    echo "   gate: paused ${waited}s before this run ($why)" >> "$STATE"
  fi
}

run_one() { # cell label binary rack seed extra-flags...
  local cell=$1 label=$2 bin=$3 rack=$4 seed=$5; shift 5
  local dir="$RAW/$cell" f rackflag t0 rc l
  mkdir -p "$dir"
  f="$dir/$label.txt"
  if [ "$rack" = on ]; then rackflag=-rack=true; else rackflag=-rack=false; fi
  local flags="$rackflag $COMMON -seed $seed $*"
  gate
  l=$(load1)
  echo "$(date -u +%TZ) $cell $label seed=$seed load1=$l top3=[$(top3 | paste -sd ';' -)]" >> "$RUNSTATE"
  t0=$(date +%s)
  {
    echo "=== run seed=$seed start=$(date -u +%TZ) load1=$l cmd: $bin $flags"
    # shellcheck disable=SC2086
    timeout -k 5 75 "$BIN/$bin" $flags 2>&1 | sanitize
    rc=${PIPESTATUS[0]}
    echo "=== end seed=$seed exit=$rc wall=$(( $(date +%s) - t0 ))s"
    echo
  } >> "$f"
  log "$cell $label seed=$seed | $(grep -h '^trial 1 fetch 1' "$f" | tail -1 | cut -c26-140)"
}

cell() { # name est-seconds builds-array-name extra-flags...
  local name=$1 est=$2 arr=$3 seed k n e; shift 3
  local -a builds
  eval "builds=(\"\${${arr}[@]}\")"
  n=${#builds[@]}
  if (( $(elapsed) + est > BUDGET )); then
    log "SKIP cell $name (estimate ${est}s would pass the ${BUDGET}s budget)"
    echo "== $(date -u +%FT%TZ) (+$(elapsed)s) SKIP cell $name: estimate ${est}s would pass the budget" >> "$STATE"
    return
  fi
  log "START cell $name: $n builds x N=$N; flags: $*"
  for ((seed = 1; seed <= N; seed++)); do
    state "cell $name seed $seed (before the seed's $n runs)"
    for ((k = 0; k < n; k++)); do
      e=${builds[$(( (seed - 1 + k) % n ))]}
      IFS=: read -r label bin rack <<< "$e"
      run_one "$name" "$label" "$bin" "$rack" "$seed" "$@"
    done
  done
  log "END cell $name"
}

log "start; binaries: $(cd "$BIN" && shasum -a 256 repro-pin* | awk '{printf "%s %s; ", $2, substr($1,1,16)}')"
echo "# fix-test runs: harness flags $COMMON $P3 -queue Q -seed i (client timestamps on by default)." >> "$STATE"
echo "# Load gate before each run: pause above load $HI, resume below $LO or after ${MAXPAUSE}s, total cap ${TOTALCAP}s." >> "$STATE"
echo >> "$STATE"

cell 3ms-80M-q100 0 B100 $P3 -queue 100
if [ "$RUN_Q256" = 1 ]; then
  cell 3ms-80M-q256 "$Q256_EST" B256 $P3 -queue 256
else
  log "SKIP cell 3ms-80M-q256 (RUN_Q256=$RUN_Q256)"
fi

state "end (total gate pause ${paused}s)"
log "done after $(elapsed)s; total gate pause ${paused}s"
