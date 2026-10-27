#!/usr/bin/env bash
# Shows why a trial with the TSecr fix still waits for an RTO: builds gVisor
# master + the TSecr fix, with and without #15432 + #16081, plus a patch that
# logs every RACK decision on a retransmitted segment to stderr
# (patches/diagnostic-log-rack-decisions.patch), and runs them in the
# end-of-transfer and tail-probe scenarios with several jitter seeds. Writes
# results/2026-10-27/rack-decisions/*.log; summarize one with
# ./parse-rack-decisions.py <log>. Diagnostic only: the binaries also print
# the client stack's RACK decisions, and nothing else changes.
#
# Usage: ./run-rack-decisions.sh   (give it two CPUs, as run-matrix.sh says)
set -euo pipefail
cd "$(dirname "$0")"
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
build tsecr patches/master-tsecr-fix-code.patch patches/diagnostic-log-rack-decisions.patch
build upstream-tsecr patches/master-pr-15432-16081-code.patch patches/master-tsecr-fix-code.patch \
  patches/diagnostic-log-rack-decisions.patch

out=results/2026-10-27/rack-decisions
mkdir -p "$out"
eot=(-size 14400 -burst-keep-first 8 -burst-drop 4 -burst-window 0)
# Why: keep only what parse-rack-decisions.py reads; the per-100 ms traces
# are in the matching run-matrix.sh results.
keep() { grep -E '^trial .* measured|RACKDBG|gVisor stats|^measured fetches'; }

# "tsecr" is master + the TSecr fix; "upstream-tsecr" is master + #15432 +
# #16081 + the TSecr fix.
for seed in 1 2 3; do
  for name in tsecr upstream-tsecr; do
    echo "=== $name seed $seed"
    "$work/repro-$name" -seed "$seed" -trials 24 "${eot[@]}" 2>&1 | keep
  done
done > "$out/end-of-transfer.log"
for seed in 1 2 3; do
  for name in tsecr upstream-tsecr; do
    echo "=== $name seed $seed no-ts"
    "$work/repro-$name" -seed "$seed" -trials 12 "${eot[@]}" -client-timestamps=false 2>&1 | keep
  done
done > "$out/end-of-transfer-no-ts.log"
for seed in 1 2; do
  echo "=== upstream-tsecr seed $seed tail-tlp-survives-24-trials"
  "$work/repro-upstream-tsecr" -seed "$seed" -trials 24 -burst-drop 100000 -burst-window 0 -burst-until-gap 3ms 2>&1 |
    grep -E '^trial .* measured|RACKDBG (minrtt-reject|tsecr-reject|rexmit-accept)|gVisor stats|^measured fetches'
done > "$out/tail-tlp-survives-24-trials.log"
ls -l "$out"
