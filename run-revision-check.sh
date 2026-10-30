#!/usr/bin/env bash
# Compares the first and revised versions of the two proposed fixes (the
# revised ones after review on google/gvisor#16659 and #16660; builds ending
# in 2). As run-paired-benchmarks.sh does, it runs the four benchmark scenarios
# on gVisor master + #15432 + #16081, one trial per process, trial i of every
# build with jitter seed i, the builds taking turns trial by trial; then
# end-of-transfer on master with each version of the TSecr fix. Last, as
# run-rack-decisions.sh does, it logs RACK's decisions for both versions of
# the TSecr fix with the tail lost and the probe delivered, the two builds side
# by side on CPUs 0-1 and 2-3.
#
# Results go to results/<UTC date>/paired/ and
# results/<UTC date>/rack-decisions/tail-tlp-survives-24-trials.log. Build the
# variants first with ./build-variants.sh, and run this with two CPUs of its
# own (taskset -c 0,1 ./run-revision-check.sh); the decision logging also
# needs CPUs 2-3. TRIALS sets the number of paired trials (default 24).
set -euo pipefail
cd "$(dirname "$0")"
declare -A FLAGS=(
  [tail-tlp-survives-24-trials]="-burst-drop 100000 -burst-window 0 -burst-until-gap 3ms"
  [end-of-transfer]="-size 14400 -burst-keep-first 8 -burst-drop 4 -burst-window 0"
  [default-24-trials]=""
  [mid-flight-hole-24-trials]="-burst-drop 16 -burst-window 0"
)
trials="${TRIALS:-24}"
day="results/$(date -u +%Y-%m-%d)"
out="$day/paired"
mkdir -p "$out"

run_set() { # <scenario> <build>...
  local scenario=$1; shift
  local builds=("$@")
  for b in "${builds[@]}"; do : > "$out/$scenario-gvisor-$b-rack-on.txt"; done
  for ((seed = 1; seed <= trials; seed++)); do
    # Why: rotating the order each trial keeps one build from always running
    # first or last in a round.
    for ((k = 0; k < ${#builds[@]}; k++)); do
      b="${builds[$(((seed + k) % ${#builds[@]}))]}"
      # shellcheck disable=SC2086
      "bin/repro-$b" -trials 1 -seed "$seed" ${FLAGS[$scenario]} -trace >> "$out/$scenario-gvisor-$b-rack-on.txt"
    done
  done
  echo "== $scenario: $trials trials per build in $out"
}
stack=(master-pr15432-16081 master-pr15432-16081-tsecr master-pr15432-16081-tsecr2
  master-pr15432-16081-tlpnew master-pr15432-16081-tlpnew2
  master-pr15432-16081-tsecr-tlpnew master-pr15432-16081-tsecr2-tlpnew2)
for scenario in tail-tlp-survives-24-trials end-of-transfer default-24-trials mid-flight-hole-24-trials; do
  run_set "$scenario" "${stack[@]}"
done
run_set end-of-transfer master master-tsecr master-tsecr2

# RACK's decisions, from builds with patches/diagnostic-log-rack-decisions*.patch.
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.3}"
master=v0.0.0-20261027095827-a68437cfc713
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
go mod download gvisor.dev/gvisor@"$master"
src="$(go env GOMODCACHE)/gvisor.dev/gvisor@$master"
build() { # name, patches...
  local name="$1"; shift
  local tree="$work/gvisor-$name"
  cp -r "$src" "$tree"
  # Why: files copied out of the module cache are read-only.
  chmod -R u+w "$tree"
  for p in "$@"; do
    patch -s -d "$tree" -p1 < "$p"
  done
  cp go.mod "$work/go.$name.mod"
  cp go.sum "$work/go.$name.sum"
  echo "replace gvisor.dev/gvisor => $tree" >> "$work/go.$name.mod"
  go build -mod=mod -modfile="$work/go.$name.mod" -o "$work/repro-$name" .
}
build upstream-tsecr patches/master-pr-15432-16081-code.patch patches/master-tsecr-fix-code.patch \
  patches/diagnostic-log-rack-decisions.patch
build upstream-tsecr2 patches/master-pr-15432-16081-code.patch patches/master-tsecr-fix-revised-code.patch \
  patches/diagnostic-log-rack-decisions-revised.patch
mkdir -p "$day/rack-decisions"
keep() { grep -E '^trial .* measured|RACKDBG (minrtt-reject|tsecr-reject|rexmit-accept)|gVisor stats|^measured fetches'; }
decisions() { # name cpus
  for seed in 1 2; do
    echo "=== $1 seed $seed tail-tlp-survives-24-trials"
    taskset -c "$2" "$work/repro-$1" -seed "$seed" -trials 24 -burst-drop 100000 -burst-window 0 \
      -burst-until-gap 3ms 2>&1 | keep
  done > "$work/$1.log"
}
decisions upstream-tsecr 0,1 &
decisions upstream-tsecr2 2,3 &
wait
cat "$work/upstream-tsecr.log" "$work/upstream-tsecr2.log" > "$day/rack-decisions/tail-tlp-survives-24-trials.log"
echo "== rack decisions in $day/rack-decisions; summarize with ./parse-rack-decisions.py"
