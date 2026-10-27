#!/usr/bin/env python3
"""Summarizes a results/*/rack-decisions/*.log file from run-rack-decisions.sh.

For each run (build and jitter seed), it counts the measured fetches with and
without an RTO, and what RACK did with the loss probe's sample: the first
sample of a SACKed (not cumulatively acknowledged) retransmitted segment in
the fetch. Kinds: rexmit-accept (RACK used it), minrtt-reject (its RTT was
below the connection's minimum RTT), tsecr-reject (the ACK's TSecr was older
than the retransmission), or none (no such sample: the burst ended mid-flight
and later data was SACKed instead).

Usage: ./parse-rack-decisions.py results/2026-10-27/rack-decisions/end-of-transfer.log
"""
import re
import sys
from collections import Counter, defaultdict


def ms(x):
    # Go prints durations like 3.3ms, 950µs, 1.2s or 1m2.5s.
    units = {'h': 3.6e6, 'm': 6e4, 'ns': 1e-6, 'µs': 1e-3, 'us': 1e-3, 'ms': 1, 's': 1e3}
    sign = -1 if x.startswith('-') else 1
    return sign * sum(float(v) * units[u] for v, u in re.findall(r'([\d.]+)(h|ns|µs|us|ms|m|s)', x))


runs = defaultdict(list)
run, events = None, []
for line in open(sys.argv[1], encoding='utf-8'):
    if line.startswith('=== '):
        run, events = line[4:].strip(), []
        continue
    m = re.match(r'RACKDBG (\S+) rtt=(\S+) min=(\S+) xmits=(\d+) cumulative=(\w+)', line)
    if m:
        events.append((m.group(1), ms(m.group(2)), ms(m.group(3)), m.group(5) == 'true'))
        continue
    m = re.match(r'trial (\d+) fetch \d+ measured +([\d.]+) s .*RTOs +(\d+)', line)
    if m:
        runs[run].append((int(m.group(3)), events))
        events = []

for run, trials in runs.items():
    kinds = Counter()
    gaps = defaultdict(list)
    for rtos, ev in trials:
        # Why: the client stack logs too, but only the server retransmits, so
        # the first SACKed retransmission sample is the server's loss probe.
        probe = next((e for e in ev if e[0] != 'new-minrtt' and not e[3]), None)
        kind = probe[0] if probe else 'none'
        kinds[(kind, 'RTO' if rtos else 'no RTO')] += 1
        if probe:
            gaps[kind].append(probe[1] - probe[2])
    print(f'== {run}: {len(trials)} trials, {sum(1 for r, _ in trials if not r)} without an RTO')
    for (kind, rto), n in sorted(kinds.items()):
        print(f'   {n:3d}  probe sample {kind:14s} fetch {rto}')
    for kind, g in sorted(gaps.items()):
        print(f'   {kind}: probe RTT minus minimum RTT {min(g):+.3f} to {max(g):+.3f} ms')
