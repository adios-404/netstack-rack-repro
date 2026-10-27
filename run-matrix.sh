#!/usr/bin/env bash
# Runs the same scenario against every sender variant and keeps each output in
# results/. Extra arguments are passed to every run.
set -euo pipefail
cd "$(dirname "$0")"
out="results/$(date -u +%Y-%m-%d)"
mkdir -p "$out"
run() {
  local name="$1"; shift
  echo "== $name"
  "$@" | tee "$out/$name.txt" | grep -E '^(sender|trial .* measured|measured fetches)'
}
run gvisor-pin-rack-on      bin/repro-pin "$@"
run gvisor-pin-rack-off     bin/repro-pin -rack=false "$@"
run gvisor-pr15432-rack-on  bin/repro-pr15432 "$@"
run gvisor-guard-rack-on    bin/repro-guard "$@"
run gvisor-fix-rack-on      bin/repro-fix "$@"
run linux-kernel            ./run-linux-sender.sh "$@"
