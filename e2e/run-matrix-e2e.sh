#!/usr/bin/env bash
# Runs every variant in both scenarios, as netstack-rack-repro's matrix does.
# TRIALS sets the trials per run (default 8). Run as root after build.sh and
# run-e2e.sh up; results go to results/<UTC date>/<scenario>-<variant>.txt.
set -uo pipefail
E="$(cd "$(dirname "$0")" && pwd)"
for scenario in default tail-tlp-survives; do
  for variant in shipped rack-off pr15432-16081 pr15432-16081-tsecr-tlpnew; do
    "$E/run-e2e.sh" run "$variant" "$scenario" "${TRIALS:-8}" >/dev/null || echo "FAILED $scenario $variant" >&2
  done
done
"$E/summarize-e2e.py" "$E/results/$(date -u +%Y-%m-%d)"
