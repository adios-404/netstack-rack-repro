#!/usr/bin/env bash
# Runs one loss scenario against every sender variant and keeps each output in
# results/<UTC date>/<scenario>-<variant>.txt. Build the variants first with
# ./build-variants.sh. Extra arguments are passed to every run.
#
# Usage: ./run-matrix.sh <scenario> [flags]
#
# All scenarios: 3 ms RTT plus up to 2 x 200 us of jitter, 80 Mbit/s toward the
# client, MTU 1280, 2.8 MB fetches, 3 s idle before each fetch. Each trial is a
# new connection with 2 loss-free warm-up fetches; then, in each measured
# fetch, the first 1000 data packets are delivered and then:
#   default            every data packet in the next 150 ms is dropped. The
#                      tail and the loss probe are lost, so recovery starts
#                      with an RTO, as in the issue's capture. 8 trials.
#   long-lived         the same, on each of 5 fetches on one connection, so
#                      effects carry over from one fetch to the next. 1 trial.
#   tail-tlp-survives  every data packet is dropped until the sender pauses for
#                      3 ms; the loss probe and everything after it arrive, so
#                      recovery can start from the probe. 8 trials.
#   mid-flight-hole    16 data packets are dropped and later ones arrive, so
#                      SACKs drive recovery without a timer. 8 trials.
set -euo pipefail
cd "$(dirname "$0")"
scenario="${1:?scenario: default, long-lived, tail-tlp-survives or mid-flight-hole}"
shift
case "$scenario" in
default) flags=(-trials 8) ;;
long-lived) flags=(-trials 1 -fetches 5) ;;
tail-tlp-survives) flags=(-trials 8 -burst-drop 100000 -burst-window 0 -burst-until-gap 3ms) ;;
mid-flight-hole) flags=(-trials 8 -burst-drop 16 -burst-window 0) ;;
*)
  echo "unknown scenario: $scenario" >&2
  exit 2
  ;;
esac
flags+=(-trace "$@")
out="results/$(date -u +%Y-%m-%d)"
mkdir -p "$out"
run() {
  local name="$1"; shift
  echo "== $scenario $name"
  "$@" "${flags[@]}" | tee "$out/$scenario-$name.txt" | grep -E '^(sender|trial .* measured|measured fetches)'
}
run gvisor-pin-rack-on                   bin/repro-pin
run gvisor-pin-rack-on-nogso             bin/repro-pin -gso=false
run gvisor-pin-rack-off                  bin/repro-pin -rack=false
run gvisor-pr15432-rack-on               bin/repro-pr15432
run gvisor-pr16081-rack-on               bin/repro-pr16081
run gvisor-pr15432-16081-rack-on         bin/repro-pr15432-16081
run gvisor-pr16081-notsecr-rack-on       bin/repro-pr16081-notsecr
run gvisor-pr15432-16081-notsecr-rack-on bin/repro-pr15432-16081-notsecr
run gvisor-master-rack-on                bin/repro-master
run gvisor-master-pr15432-16081-rack-on  bin/repro-master-pr15432-16081
run gvisor-master-pr16495-rack-on        bin/repro-master-pr16495
run gvisor-master-pr15432-16081-16495-16419-rack-on bin/repro-master-pr15432-16081-16495-16419
run linux-kernel-cubic                   ./run-linux-sender.sh
