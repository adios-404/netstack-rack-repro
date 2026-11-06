#!/usr/bin/env python3
"""Parse fix-test raw harness output into per-run rows and a summary table.

Usage: python3 -I summarize.py <fix-test dir>   (writes runs.csv, table.md)
NOTES below describes the 2026-10-31 18:01-18:16 UTC batch; edit it before summarizing a new batch.
Parsing follows the main batch's parser so the numbers compare.
"""
import csv
from decimal import ROUND_HALF_UP, Decimal
import re
import statistics
import sys
from pathlib import Path

CELL_ORDER = ["3ms-80M-q100", "3ms-80M-q256"]
CELL_DESC = {
    "3ms-80M-q100": "3 ms, 80 Mbit/s, queue 100",
    "3ms-80M-q256": "3 ms, 80 Mbit/s, queue 256",
}
BUILD_ORDER = [
    "pin", "pin-rackoff", "pin-16659", "pin-16659orig", "pin-16660",
    "pin-16659-16660", "pin-16081", "pin-15432-16081",
]
# label -> (description, RACK)
BUILD_DESC = {
    "pin": ("pin (v1.104.0–v1.104.4)", "on"),
    "pin-rackoff": ("pin (v1.104.5, main)", "off"),
    "pin-16659": ("pin + #16659 (TSecr fix, revised)", "on"),
    "pin-16659orig": ("pin + TSecr fix, first version", "on"),
    "pin-16660": ("pin + #16660 (loss-probe fix)", "on"),
    "pin-16659-16660": ("pin + #16659 + #16660", "on"),
    "pin-16081": ("pin + #16081", "on"),
    "pin-15432-16081": ("pin + #15432 + #16081", "on"),
}
NOTES = (
    "How measured (2026-10-31, 18:01:52-18:16:35 UTC, 883 s in all): one harness process per run, one process"
    " at a time, flags `-rack=true|false -loss none -warmup 0 -fetches 1 -trials 1 -timeout 30s -interval 3s"
    " -seed i -rtt 3ms -rate-mbps 80 -queue 100|256` (the main test's flags). N = 10 seeds per build; within"
    " each seed every build ran once, the order rotated by seed. Binaries: fix-test/bin, built by"
    " fix-test/build.sh (pin a6f909f08a72 plus the patches named in the build column; the pin binary differs"
    " from the main test's only in the harness commit stamp, 94a018b against e14785e, which changed results"
    " files only). A load gate (pause above load 16) never triggered: the 1-minute load was 2.9-7.4 at run"
    " start. The Linux VM was stopped when the batch began, but the VM manager's log shows it running from"
    " 18:08:51 to 18:17:06 UTC, so queue-100 seeds 6-10 and the whole queue-256 cell ran with it up. It used"
    " up to about 5 cores for about 20 s around 18:12:20 UTC (near the end of seed 8) and up to 1 core otherwise. Seeds"
    " 1-5 (VM stopped) and 6-10 (VM up) show the same split: no #16081 run over 1 s in either half, and every"
    " other RACK-on build has slow runs in both halves."
)
STALL_S = 1.0
CRAWL_MIN_RTOS = 10
CRAWL_MAX_PER_RTO = 3

RUN_RE = re.compile(r"^=== run seed=(\d+) start=(\S+) load1=(\S+) cmd: (\S+) (.*)$")
END_RE = re.compile(r"^=== end seed=(\d+) exit=(\d+) wall=(\d+)s")
FETCH_RE = re.compile(
    r"^trial 1 fetch 1 measured\s+([\d.]+) s \|\s+(\d+) data pkts,\s+(\d+) retransmitted"
    r" \| RTOs\s+(\d+)(?: \(first at \+([\d.]+)s\))?(.*)$"
)
AFTER_RE = re.compile(
    r"after \S+ idle \| peak (\d+) data pkts in 1 ms \| (\d+) queue drops"
    r"(?: \| cwnd first (\d+), max (\d+), last (\d+))?"
)
STATS_RE = re.compile(
    r"gVisor stats: retransmits (\d+), timeouts (\d+), fast retransmits (\d+),"
    r" SACK recoveries (\d+), TLP recoveries (\d+), spurious RTO recoveries (\d+)"
)
TS_RE = re.compile(r"\(gVisor connection: TCP timestamps (on|off)\)")
SENDER_RE = re.compile(r"^sender:\s+gVisor (\S+)(?: => (\S+))? \(RACK (on|off), (\w+)")


def parse_file(path: Path):
    cell = path.parent.name
    build = path.stem
    runs, cur = [], None
    for line in path.read_text().splitlines():
        m = RUN_RE.match(line)
        if m:
            flags = m.group(5)
            rk = re.search(r"-rack=(true|false)", flags)
            cur = {
                "cell": cell, "build": build, "seed": int(m.group(1)),
                "start": m.group(2), "load1": float(m.group(3)),
                "elapsed": None, "data": None, "retx": None, "rtos": None,
                "first_rto": None, "status": "missing", "peak": None,
                "qdrops": None, "cwnd_max": None, "sack_rec": None,
                "ts": None, "exit": None, "wall": None, "per_rto_med": None,
                "rack": {"true": "on", "false": "off"}[rk.group(1)] if rk else None,
                "binary": m.group(4), "pos": None, "fast_retx": None,
                "tlp_rec": None, "spurious_rto": None, "sender_rack": None,
                "tree": None,
            }
            continue
        if cur is None:
            continue
        m = SENDER_RE.match(line)
        if m:
            cur["tree"] = m.group(2) or "-"
            cur["sender_rack"] = m.group(3)
            continue
        m = FETCH_RE.match(line)
        if m:
            tail = m.group(6)
            cur.update(
                elapsed=float(m.group(1)), data=int(m.group(2)), retx=int(m.group(3)),
                rtos=int(m.group(4)),
                first_rto=float(m.group(5)) if m.group(5) else None,
            )
            pm = re.search(r"per RTO interval: median (\d+), max (\d+)", tail)
            cur["per_rto_med"] = int(pm.group(1)) if pm else None
            # Why: gonet's deadline error is not os.ErrDeadlineExceeded, so the
            # harness prints a 30 s timeout as "ERROR: ... i/o timeout".
            if "TIMED OUT" in tail or "i/o timeout" in tail:
                cur["status"] = "timeout"
            elif "ERROR" in tail:
                cur["status"] = "error"
            else:
                cur["status"] = "ok"
            continue
        m = AFTER_RE.search(line)
        if m and cur["peak"] is None:
            cur["peak"], cur["qdrops"] = int(m.group(1)), int(m.group(2))
            if m.group(4):
                cur["cwnd_max"] = int(m.group(4))
            continue
        m = STATS_RE.search(line)
        if m:
            cur["fast_retx"] = int(m.group(3))
            cur["sack_rec"] = int(m.group(4))
            cur["tlp_rec"] = int(m.group(5))
            cur["spurious_rto"] = int(m.group(6))
            continue
        m = TS_RE.search(line)
        if m:
            cur["ts"] = m.group(1)
            continue
        m = END_RE.match(line)
        if m:
            cur["exit"], cur["wall"] = int(m.group(2)), int(m.group(3))
            runs.append(cur)
            cur = None
    if cur is not None:  # run still in progress or cut off
        runs.append(cur)
    return runs


def is_slow(r):
    return r["status"] != "ok" or (r["elapsed"] is not None and r["elapsed"] > STALL_S)


def is_crawl(r):
    """The tailscale/tailscale#21946 signature: many RTOs, each followed by
    only a couple of retransmissions."""
    return (r["rtos"] or 0) >= CRAWL_MIN_RTOS and (r["per_rto_med"] or 99) <= CRAWL_MAX_PER_RTO


def fmt_s(x):
    if x is None:
        return "-"
    # Why: round half up on the decimal value the harness printed (0.725 s ->
    # 0.73), as the README does; f"{x:.2f}" rounds the binary float, which
    # sits just below 0.725, down to 0.72.
    q = Decimal("0.01") if x < 10 else Decimal("0.1")
    return str(Decimal(repr(x)).quantize(q, rounding=ROUND_HALF_UP))


def med(xs):
    xs = [x for x in xs if x is not None]
    return statistics.median(xs) if xs else None


def fmt_n(x):
    return "-" if x is None else f"{x:g}"


def fisher_two_sided(a_hit, a_n, b_hit, b_n):
    """Two-sided Fisher exact test for a 2x2 table (hits/misses in two groups)."""
    from math import comb
    k, n = a_hit + b_hit, a_n + b_n
    def p(x):  # P(group b has x hits | margins)
        return comb(k, x) * comb(n - k, b_n - x) / comb(n, b_n)
    lo, hi = max(0, b_n - (n - k)), min(k, b_n)
    p_obs = p(b_hit)
    return min(1.0, sum(p(x) for x in range(lo, hi + 1) if p(x) <= p_obs * (1 + 1e-9)))


def aggregate(runs):
    done = [r for r in runs if r["exit"] is not None]
    times = [r["elapsed"] for r in done if r["elapsed"] is not None]
    return {
        "runs": len(done),
        "slow": sum(1 for r in done if is_slow(r)),
        "crawls": sum(1 for r in done if is_crawl(r)),
        "timeouts": sum(1 for r in done if r["status"] in ("timeout", "missing")),
        "median": med(times),
        "max": max(times) if times else None,
        "with_rto": sum(1 for r in done if (r["rtos"] or 0) > 0),
        "rtos": sum(r["rtos"] or 0 for r in done),
        "qdrops_med": med([r["qdrops"] for r in done]),
        "retx_med": med([r["retx"] for r in done]),
        "data_med": med([r["data"] for r in done]),
    }


def main():
    base = Path(sys.argv[1])
    raw = base / "raw"
    all_runs = []
    for f in sorted(raw.glob("*/*.txt")):
        all_runs.extend(parse_file(f))

    # Position of each run within its seed (1 = first), from start times.
    by_seed = {}
    for r in all_runs:
        by_seed.setdefault((r["cell"], r["seed"]), []).append(r)
    for rs in by_seed.values():
        for i, r in enumerate(sorted(rs, key=lambda r: r["start"]), 1):
            r["pos"] = i

    cols = ["cell", "build", "seed", "start", "load1", "elapsed", "status", "rtos",
            "per_rto_med", "first_rto", "retx", "data", "qdrops", "peak", "cwnd_max",
            "sack_rec", "ts", "exit", "wall", "rack", "pos", "fast_retx", "tlp_rec",
            "spurious_rto", "binary"]
    border = {b: i for i, b in enumerate(BUILD_ORDER)}
    with open(base / "runs.csv", "w", newline="") as fh:
        w = csv.DictWriter(fh, fieldnames=cols, extrasaction="ignore")
        w.writeheader()
        for r in sorted(all_runs, key=lambda r: (CELL_ORDER.index(r["cell"]) if r["cell"] in CELL_ORDER else 99,
                                                  border.get(r["build"], 99), r["seed"])):
            w.writerow(r)

    head = ("| Path | Build | RACK | Runs | Over 1 s | Crawls | 30 s timeouts | Median s | Max s |"
            " Runs with an RTO | Total RTOs | Median queue drops | Median retransmitted |")
    lines = [head, "|---|---|---|" + "---:|" * 10]
    cells = [c for c in CELL_ORDER if (raw / c).is_dir()]
    for cell in cells:
        for b in BUILD_ORDER:
            rs = [r for r in all_runs if r["cell"] == cell and r["build"] == b]
            if not rs:
                continue
            a = aggregate(rs)
            desc, rack = BUILD_DESC[b]
            lines.append(
                f"| {CELL_DESC[cell]} | {desc} | {rack} | {a['runs']} | {a['slow']} | {a['crawls']} |"
                f" {a['timeouts']} | {fmt_s(a['median'])} | {fmt_s(a['max'])} | {a['with_rto']} |"
                f" {a['rtos']} | {fmt_n(a['qdrops_med'])} | {fmt_n(a['retx_med'])} |"
            )
    table = "\n".join(lines)

    # Per-seed grid of fetch times, for pairing builds within a seed.
    grids = []
    for cell in cells:
        builds = [b for b in BUILD_ORDER if any(r["cell"] == cell and r["build"] == b for r in all_runs)]
        seeds = sorted({r["seed"] for r in all_runs if r["cell"] == cell})
        g = [f"Fetch time in s per seed, {CELL_DESC[cell]} (T = 30 s timeout):", "",
             "| Seed | Load at seed start | " + " | ".join(builds) + " |",
             "|---:|---:|" + "---:|" * len(builds)]
        for s in seeds:
            rs = {r["build"]: r for r in all_runs if r["cell"] == cell and r["seed"] == s}
            first = min(rs.values(), key=lambda r: r["start"])
            cells_s = []
            for b in builds:
                r = rs.get(b)
                if r is None or r["exit"] is None:
                    cells_s.append("-")
                elif r["status"] == "timeout":
                    cells_s.append("T")
                else:
                    cells_s.append(fmt_s(r["elapsed"]))
            g.append(f"| {s} | {first['load1']:.1f} | " + " | ".join(cells_s) + " |")
        loads = [r["load1"] for r in all_runs if r["cell"] == cell]
        g += ["", f"1-minute load at run start: min {min(loads):.1f}, median {statistics.median(loads):.1f},"
                  f" max {max(loads):.1f} ({len(loads)} runs)."]
        grids.append("\n".join(g))

    fisher = []
    for cell in cells:
        base_rs = [r for r in all_runs if r["cell"] == cell and r["build"] == "pin" and r["exit"] is not None]
        if not base_rs:
            continue
        bs, bn = sum(1 for r in base_rs if is_slow(r)), len(base_rs)
        for b in BUILD_ORDER[1:]:
            rs = [r for r in all_runs if r["cell"] == cell and r["build"] == b and r["exit"] is not None]
            if rs:
                s, n = sum(1 for r in rs if is_slow(r)), len(rs)
                fisher.append(f"- {CELL_DESC[cell]}, {BUILD_DESC[b][0]}: {s}/{n} against the pin's {bs}/{bn},"
                              f" p = {fisher_two_sided(bs, bn, s, n):.3f}")

    doc = [
        "# Fix test: which fix removes the plain-download stall on Tailscale's gVisor pin",
        "",
        table,
        "",
        "Over 1 s: the fetch took more than 1 s or failed. Crawl: at least 10 RTOs with a median of at most 3"
        " retransmissions per RTO interval (the tailscale/tailscale#21946 signature). 30 s timeouts: the fetch hit"
        " the harness's 30 s limit. All runs: client timestamps on, no scripted loss, 2.8 MB fetch on a fresh"
        " connection, 3 s idle first.",
        "",
        "Over-1-s runs against the pin (RACK on) in the same cell, Fisher exact test, two-sided:",
        "",
        *fisher,
        "",
        NOTES,
        "",
        "\n\n".join(grids),
    ]
    (base / "table.md").write_text("\n".join(doc) + "\n")
    print(table)
    print()
    print("\n\n".join(grids))

    print("\nslow runs (over 1 s or not ok):")
    for r in sorted(all_runs, key=lambda r: (r["cell"], border.get(r["build"], 99), r["seed"])):
        if r["exit"] is not None and is_slow(r):
            print(f"  {r['cell']:13s} {r['build']:16s} seed {r['seed']:2d} pos {r['pos']} {fmt_s(r['elapsed']):>6s} s"
                  f" {r['status']:8s} {'CRAWL' if is_crawl(r) else 'slow '} RTOs {r['rtos']} per-RTO med {r['per_rto_med']}"
                  f" first RTO +{r['first_rto']}s qdrops {r['qdrops']} retx {r['retx']} peak/ms {r['peak']}"
                  f" cwnd max {r['cwnd_max']} load1 {r['load1']}")

    print("\nchecks:")
    for r in all_runs:
        if r["exit"] not in (0, None):
            print(f"  nonzero exit: {r['cell']} {r['build']} seed {r['seed']} exit {r['exit']}")
        if r["ts"] != "on":
            print(f"  timestamps not on: {r['cell']} {r['build']} seed {r['seed']} ts {r['ts']}")
        if r["rack"] != r["sender_rack"] or r["rack"] != BUILD_DESC[r["build"]][1]:
            print(f"  RACK mismatch: {r['cell']} {r['build']} seed {r['seed']} flag {r['rack']} sender {r['sender_rack']}")
        want_tree = "-" if r["build"] in ("pin", "pin-rackoff") else f"fix-test/trees/gvisor-{r['build']}"
        if r["tree"] != want_tree:
            print(f"  tree mismatch: {r['cell']} {r['build']} seed {r['seed']} tree {r['tree']} want {want_tree}")
    for cell in cells:
        for b in BUILD_ORDER:
            n = sum(1 for r in all_runs if r["cell"] == cell and r["build"] == b and r["exit"] is not None)
            if n:
                print(f"  {cell} {b}: {n} complete runs")

    print("\nover-1-s runs vs the pin (RACK on) in the same cell, Fisher exact test (two-sided p):")
    for cell in cells:
        base_rs = [r for r in all_runs if r["cell"] == cell and r["build"] == "pin" and r["exit"] is not None]
        if not base_rs:
            continue
        bs, bn = sum(1 for r in base_rs if is_slow(r)), len(base_rs)
        for b in BUILD_ORDER[1:]:
            rs = [r for r in all_runs if r["cell"] == cell and r["build"] == b and r["exit"] is not None]
            if not rs:
                continue
            s, n = sum(1 for r in rs if is_slow(r)), len(rs)
            print(f"  {cell:13s} {b:16s} {s}/{n} vs pin {bs}/{bn}: p = {fisher_two_sided(bs, bn, s, n):.4f}")

    print("\nload at start, slow runs vs fast runs (median), per cell and build with both kinds:")
    for cell in cells:
        for b in BUILD_ORDER:
            rs = [r for r in all_runs if r["cell"] == cell and r["build"] == b and r["exit"] is not None]
            slow = [r["load1"] for r in rs if is_slow(r)]
            fast = [r["load1"] for r in rs if not is_slow(r)]
            if slow and fast:
                print(f"  {cell:13s} {b:16s} slow {len(slow)} runs, median load {statistics.median(slow):.2f};"
                      f" fast {len(fast)} runs, median load {statistics.median(fast):.2f}")

    inprog = [r for r in all_runs if r["exit"] is None]
    if inprog:
        print(f"\nruns without an end line (in progress or cut off): {len(inprog)}")


if __name__ == "__main__":
    main()
