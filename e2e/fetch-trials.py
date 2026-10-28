#!/usr/bin/env python3
"""Fetches through tailscale serve as netstack-rack-repro's trials do.

Usage: fetch-trials.py URL TRIALS BRIDGE_PID ARM_FILE REPORT_FILE SCENARIO_JSON HOST

Each trial opens a new connection and makes two warm-up fetches and one
measured fetch, each after 3 s of idle time. Before the measured fetch it
writes the scenario's burst parameters to ARM_FILE and signals the bridge
(SIGUSR1) to drop one burst in that response; afterwards it asks the bridge
for its report (SIGUSR2) and prints it with the fetch time.
"""
import http.client
import json
import os
import signal
import sys
import time
import urllib.parse

url, trials, bridge_pid, arm_file, report_file, scenario, host = sys.argv[1:8]
trials, bridge_pid = int(trials), int(bridge_pid)
u = urllib.parse.urlsplit(url)


def fetch(conn):
    t0 = time.monotonic()
    conn.request("GET", u.path, headers={"Host": host, "Connection": "keep-alive"})
    r = conn.getresponse()
    n = len(r.read())
    return time.monotonic() - t0, n, r.status


def reports():
    try:
        with open(report_file) as f:
            return f.read().splitlines()
    except FileNotFoundError:
        return []


with open(arm_file, "w") as f:
    f.write(scenario)
expected = None
for trial in range(1, trials + 1):
    conn = http.client.HTTPConnection(u.hostname, u.port or 80, timeout=600)
    for k in range(1, 4):
        time.sleep(3)
        measured = k == 3
        if measured:
            before = len(reports())
            os.kill(bridge_pid, signal.SIGUSR1)
        secs, size, status = fetch(conn)
        expected = expected or size
        line = f"trial {trial} fetch {k} {'measured' if measured else 'warm-up '} {secs:8.3f} s  {size} bytes  HTTP {status}"
        if measured:
            os.kill(bridge_pid, signal.SIGUSR2)
            deadline = time.monotonic() + 2
            while len(reports()) <= before and time.monotonic() < deadline:
                time.sleep(0.01)
            got = reports()
            line += "  bridge " + (got[-1] if len(got) > before else "(no report)")
        if status != 200 or size != expected:
            line += "  INVALID"
        print(line, flush=True)
    conn.close()
