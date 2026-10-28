#!/usr/bin/env python3
"""Converts results/*/<scenario>-<variant>.txt files to Go benchmark format.

Each measured fetch becomes one benchmark line, named after its scenario, with
the fetch time as sec/op and the fetch's RTO count as rtos/op, so that
golang.org/x/perf/cmd/benchstat can compare two variants:

  ./results-to-benchfmt.py results/2026-10-28/paired/*-gvisor-master-pr15432-16081-rack-on.txt > base.txt
  ./results-to-benchfmt.py results/2026-10-28/paired/*-gvisor-master-pr15432-16081-tlpnew-rack-on.txt > new.txt
  benchstat base=base.txt new=new.txt

Compare builds that ran side by side, such as the paired runs of
run-paired-benchmarks.sh: builds that ran at different times also differ
by drift in the machine's timing.

Only the 24-trial scenarios in NAMES are converted. The long-lived scenario
is left out because its fetches share one connection, so they are not
independent samples.
"""
import os
import re
import sys

NAMES = {
    'tail-tlp-survives-24-trials': 'TailLostProbeArrives',
    'end-of-transfer': 'EndOfTransferTailLost',
    'default-24-trials': 'TailAndProbeLost',
    'mid-flight-hole-24-trials': 'HoleMidFlight',
}

print('goos: linux')
print('pkg: netstack-rack-repro')
for path in sys.argv[1:]:
    base = os.path.basename(path)
    # Why: scenario names share prefixes (end-of-transfer and
    # end-of-transfer-no-ts), so match the longest name followed by the
    # variant's "-gvisor-" or "-linux-".
    scenario = next((s for s in sorted(NAMES, key=len, reverse=True)
                     if re.match(re.escape(s) + r'-(gvisor|linux)-', base)), None)
    if scenario is None:
        print(f'skipping {path}: not a 24-trial scenario', file=sys.stderr)
        continue
    fetch = None
    for line in open(path, encoding='utf-8'):
        m = re.match(r'trial \d+ fetch \d+ (\S+) +([\d.]+) s .*RTOs +(\d+)', line)
        if m:
            if fetch:
                print(fetch % fetch_rtos)
            fetch = None
            if m.group(1) == 'measured':
                ns = round(float(m.group(2)) * 1e9)
                fetch, fetch_rtos = f'Benchmark{NAMES[scenario]} 1 {ns} ns/op %d rtos/op', int(m.group(3))
            continue
        # Why: for a gVisor sender, count RTOs with gVisor's own Timeouts
        # counter, from the fetch's stats line. The RTOs field polls that
        # counter every millisecond and can miss an RTO that fires a few
        # milliseconds before the fetch ends.
        g = re.search(r'gVisor stats: .*timeouts (\d+)', line)
        if g and fetch:
            fetch_rtos = int(g.group(1))
    if fetch:
        print(fetch % fetch_rtos)
