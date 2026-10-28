#!/usr/bin/env python3
"""Summarizes run-e2e.sh results: one table row per variant and scenario.

Usage: summarize-e2e.py RESULTS_DIR

The burst length runs from the first dropped packet to the first packet
after it that the bridge delivered. A "wait" is a silence of at least 100 ms in the server's data packets after
the scripted burst, as tsbridge saw them; each is an RTO the sender waited
for (the minimum RTO is 200 ms), so a fetch with no waits recovered without
an RTO after the burst ended.
"""
import json
import os
import re
import statistics
import sys

VARIANTS = ("shipped", "rack-off", "pr15432-16081", "pr15432-16081-tsecr-tlpnew")
LINE = re.compile(r"^trial (\d+) fetch 3 measured\s+([\d.]+) s\s+(\d+) bytes\s+HTTP (\d+)\s+bridge (\{.*\})(.*)$")
NAMES = {
    "shipped": "v1.104.2 as shipped (RACK on)",
    "rack-off": "v1.104.2, `TS_NETSTACK_TCP_RACK=false`",
    "pr15432-16081": "v1.104.2 + google/gvisor#15432 + #16081",
    "pr15432-16081-tsecr-tlpnew": "the same + the TSecr fix + the probe fix",
}

rows = {}
for name in sorted(os.listdir(sys.argv[1])):
    m = re.match(r"(default|tail-tlp-survives)-(%s)\.txt$" % "|".join(map(re.escape, VARIANTS)), name)
    if not m:
        continue
    times, waits, bursts, bad = [], [], [], 0
    for line in open(os.path.join(sys.argv[1], name)):
        f = LINE.match(line.rstrip("\n"))
        if not f:
            continue
        report = json.loads(f.group(5))
        times.append(float(f.group(2)))
        waits.append(report["silences_over_100ms_after_drop"])
        bursts.append(report["drop_phase_ms"])
        if f.group(4) != "200" or "INVALID" in f.group(6) or not report["started"] or report["dropped"] == 0:
            bad += 1
    rows[(m.group(1), m.group(2))] = (times, waits, bursts, bad)

for scenario in ("default", "tail-tlp-survives"):
    print(f"\n{scenario}\n")
    print("| server | trials | burst length | fetch time | over 1 s | waits for an RTO after the burst |")
    print("|---|---|---|---|---|---|")
    for variant in VARIANTS:
        if (scenario, variant) not in rows:
            continue
        times, waits, bursts, bad = rows[(scenario, variant)]
        if not times:
            continue
        span = f"{min(times):.2f}–{max(times):.2f} s" if max(times) - min(times) >= 0.005 else f"{times[0]:.2f} s"
        none = sum(1 for w in waits if w == 0)
        note = f" ({bad} invalid)" if bad else ""
        print(f"| {NAMES[variant]} | {len(times)}{note} | {min(bursts):.0f}–{max(bursts):.0f} ms | {span}, median {statistics.median(times):.2f} s | "
              f"{sum(1 for t in times if t > 1)} | {min(waits)}–{max(waits)}; none in {none} of {len(times)} |")
