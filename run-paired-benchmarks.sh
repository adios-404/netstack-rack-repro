#!/usr/bin/env bash
# Runs the four benchmark scenarios on gVisor master + #15432 + #16081, with
# and without each proposed fix, one trial per process. Trial i of every build
# uses jitter seed i, and the builds take turns trial by trial, so drift in
# the machine's timing during a run affects every build alike. Build the
# variants first with ./build-variants.sh.
#
# Results go to results/<UTC date>/paired/<scenario>-<variant>.txt. Compare
# two builds with results-to-benchfmt.py and benchstat, e.g.:
#   ./results-to-benchfmt.py results/2026-10-28/paired/*-gvisor-master-pr15432-16081-rack-on.txt > base.txt
#   ./results-to-benchfmt.py results/2026-10-28/paired/*-gvisor-master-pr15432-16081-tsecr-rack-on.txt > new.txt
#   benchstat base=base.txt new=new.txt
#
# Usage: ./run-paired-benchmarks.sh [scenario ...]
# Scenarios: tail-tlp-survives-24-trials, end-of-transfer, default-24-trials,
# mid-flight-hole-24-trials (run-matrix.sh describes them). TRIALS sets the
# number of trials (default 24).
set -euo pipefail
cd "$(dirname "$0")"
declare -A FLAGS=(
  [tail-tlp-survives-24-trials]="-burst-drop 100000 -burst-window 0 -burst-until-gap 3ms"
  [end-of-transfer]="-size 14400 -burst-keep-first 8 -burst-drop 4 -burst-window 0"
  [default-24-trials]=""
  [mid-flight-hole-24-trials]="-burst-drop 16 -burst-window 0"
)
variants=(master-pr15432-16081 master-pr15432-16081-tsecr master-pr15432-16081-tlpnew master-pr15432-16081-tsecr-tlpnew)
trials="${TRIALS:-24}"
out="results/$(date -u +%Y-%m-%d)/paired"
mkdir -p "$out"
if [[ $# -eq 0 ]]; then
  set -- tail-tlp-survives-24-trials end-of-transfer default-24-trials mid-flight-hole-24-trials
fi
for scenario in "$@"; do
  [[ -v "FLAGS[$scenario]" ]] || { echo "unknown scenario: $scenario" >&2; exit 2; }
  for v in "${variants[@]}"; do
    : > "$out/$scenario-gvisor-$v-rack-on.txt"
  done
  for ((seed = 1; seed <= trials; seed++)); do
    # Why: rotating the order each trial keeps one build from always running
    # first or last in a round.
    for ((k = 0; k < ${#variants[@]}; k++)); do
      v="${variants[$(((seed + k) % ${#variants[@]}))]}"
      # shellcheck disable=SC2086
      "bin/repro-$v" -trials 1 -seed "$seed" ${FLAGS[$scenario]} -trace >> "$out/$scenario-gvisor-$v-rack-on.txt"
    done
  done
  echo "== $scenario: $trials trials per build in $out"
done
