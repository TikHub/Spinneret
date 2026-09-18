#!/usr/bin/env python3
"""Acquire → report load generator for scripts/e2e-failover.sh (standard library only).

    python3 scripts/lib/failover_load.py run --url http://localhost:8080 --token spn_... --site failover \\
        --workers 16 --duration 75 --out /tmp/load.json
    python3 scripts/lib/failover_load.py analyze --in /tmp/load.json --stop-at 1758011411.2 --window 30

Every worker keeps one keep-alive connection to the load balancer and loops: LeaseService/Acquire
(wait_ms 2000) then ReportService/Report with release=true. Results are bucketed per second; errors
are classified as http_<status>:<reason> or transport:<exception>.
"""

from __future__ import annotations

import argparse
import http.client
import json
import sys
import threading
import time
import urllib.parse
import uuid
from collections import Counter, defaultdict
from datetime import datetime, timezone
from typing import Any

RESOURCE_EXHAUSTED = "resource_exhausted"


class Recorder:
    """Thread-safe per-second counters."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        # second -> op -> Counter(result)
        self.buckets: dict[int, dict[str, Counter[str]]] = defaultdict(lambda: defaultdict(Counter))
        self.samples: list[dict[str, Any]] = []
        self.latencies: dict[str, list[float]] = defaultdict(list)

    def record(self, op: str, result: str, started: float, latency: float, detail: str = "") -> None:
        with self._lock:
            self.buckets[int(started)][op][result] += 1
            self.latencies[op].append(latency)
            if result != "ok" and len(self.samples) < 200:
                self.samples.append({"at": round(started, 3), "op": op, "result": result, "detail": detail[:200]})


def _post(
    conn: http.client.HTTPConnection, path: str, token: str, body: dict[str, Any]
) -> tuple[int, dict[str, Any], str]:
    payload = json.dumps(body).encode()
    conn.request(
        "POST",
        path,
        body=payload,
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {token}",
            "X-Spinneret-Node": "failover-drill",
        },
    )
    resp = conn.getresponse()
    raw = resp.read()
    reason = resp.getheader("Spinneret-Reason", "") or ""
    try:
        data = json.loads(raw or b"{}")
    except ValueError:
        data = {}
    return resp.status, data, reason


def _now_rfc3339() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def worker(args: argparse.Namespace, rec: Recorder, deadline: float) -> None:
    url = urllib.parse.urlsplit(args.url)
    host, port = url.hostname or "localhost", url.port or 80
    conn: http.client.HTTPConnection | None = None
    while time.time() < deadline:
        if conn is None:
            conn = http.client.HTTPConnection(host, port, timeout=15)
        # Acquire.
        started = time.time()
        try:
            status, data, reason = _post(
                conn,
                "/spinneret.v1.LeaseService/Acquire",
                args.token,
                {"site": args.site, "client": "web", "uri": "/api/items", "wait_ms": 2000},
            )
        except (OSError, http.client.HTTPException) as exc:
            rec.record("acquire", f"transport:{type(exc).__name__}", started, time.time() - started, str(exc))
            conn.close()
            conn = None
            time.sleep(0.05)
            continue
        if status != 200:
            code = data.get("code", "")
            result = f"http_{status}:{reason or code or 'no_reason'}"
            rec.record("acquire", result, started, time.time() - started, data.get("message", ""))
            time.sleep(0.2 if code == RESOURCE_EXHAUSTED else 0.05)
            continue
        rec.record("acquire", "ok", started, time.time() - started)
        lease_id = data["lease"]["lease_id"]
        # Report (releases the lease).
        started = time.time()
        # One wall-clock reading for both timestamps: two readings can straddle an NTP
        # correction, and the server rejects a report whose finished_at precedes started_at.
        at = _now_rfc3339()
        report = {
            "report_id": str(uuid.uuid4()),
            "lease_id": lease_id,
            "uri": "/api/items",
            "method": "GET",
            "http_status": 200,
            "latency_ms": 3,
            "response_bytes": 512,
            "started_at": at,
            "finished_at": at,
            "release": True,
        }
        try:
            status, data, reason = _post(conn, "/spinneret.v1.ReportService/Report", args.token, {"reports": [report]})
        except (OSError, http.client.HTTPException) as exc:
            rec.record("report", f"transport:{type(exc).__name__}", started, time.time() - started, str(exc))
            conn.close()
            conn = None
            continue
        if status != 200 or data.get("accepted") != 1:
            rejected = (data.get("rejected") or [{}])[0].get("reason", "")
            rec.record(
                "report",
                f"http_{status}:{reason or rejected or data.get('code', 'not_accepted')}",
                started,
                time.time() - started,
                json.dumps(data)[:200],
            )
            continue
        rec.record("report", "ok", started, time.time() - started)
    if conn is not None:
        conn.close()


def percentile(values: list[float], q: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(q * len(ordered)))]


def cmd_run(args: argparse.Namespace) -> int:
    rec = Recorder()
    start = time.time()
    deadline = start + args.duration
    threads = [threading.Thread(target=worker, args=(args, rec, deadline), daemon=True) for _ in range(args.workers)]
    for th in threads:
        th.start()
    for th in threads:
        th.join(args.duration + 60)
    result = {
        "started_at": start,
        "finished_at": time.time(),
        "workers": args.workers,
        "timeline": [
            {"second": sec, **{op: dict(counts) for op, counts in ops.items()}}
            for sec, ops in sorted(rec.buckets.items())
        ],
        "latency_ms": {
            op: {
                "p50": round(percentile(v, 0.5) * 1000, 1),
                "p99": round(percentile(v, 0.99) * 1000, 1),
                "max": round(max(v) * 1000, 1),
            }
            for op, v in rec.latencies.items()
            if v
        },
        "error_samples": rec.samples,
    }
    with open(args.out, "w", encoding="utf-8") as fh:
        json.dump(result, fh)
    return 0


def _totals(timeline: list[dict[str, Any]], lo: float, hi: float) -> dict[str, Counter[str]]:
    totals: dict[str, Counter[str]] = defaultdict(Counter)
    for bucket in timeline:
        if lo <= bucket["second"] < hi:
            for op in ("acquire", "report"):
                totals[op].update(bucket.get(op, {}))
    return totals


def _rate(counts: Counter[str], exclude_exhausted: bool = True) -> tuple[int, int, float]:
    total = sum(counts.values())
    errors = sum(n for r, n in counts.items() if r != "ok" and not (exclude_exhausted and "no_identity_available" in r))
    return total, errors, (errors / total if total else 0.0)


def cmd_analyze(args: argparse.Namespace) -> int:
    with open(args.inp, encoding="utf-8") as fh:
        data = json.load(fh)
    timeline = data["timeline"]
    stop = args.stop_at
    windows = {
        "whole_run": (0, float("inf")),
        "before_stop": (0, stop),
        f"stop_to_stop+{args.window:g}s": (int(stop), stop + args.window),
        "after_window": (stop + args.window, float("inf")),
    }
    out: dict[str, Any] = {"latency_ms": data.get("latency_ms", {}), "windows": {}}
    worst = 0.0
    for name, (lo, hi) in windows.items():
        totals = _totals(timeline, lo, hi)
        entry = {}
        for op in ("acquire", "report"):
            total, errors, rate = _rate(totals[op])
            entry[op] = {
                "total": total,
                "errors": errors,
                "error_rate": round(rate, 5),
                "results": {k: v for k, v in totals[op].items() if k != "ok"},
            }
            if name != "before_stop":
                worst = max(worst, rate)
        out["windows"][name] = entry
    # Throughput per second around the stop, to show the dip (if any).
    out["acquire_ok_per_second_around_stop"] = [
        {
            "t": b["second"] - int(stop),
            "ok": b.get("acquire", {}).get("ok", 0),
            "errors": sum(n for r, n in b.get("acquire", {}).items() if r != "ok"),
        }
        for b in timeline
        if -5 <= b["second"] - int(stop) <= 15
    ]
    out["error_samples"] = data.get("error_samples", [])[:10]
    out["max_error_rate"] = round(worst, 5)
    json.dump(out, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0 if worst <= args.max_error_rate else 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run")
    run.add_argument("--url", required=True)
    run.add_argument("--token", required=True)
    run.add_argument("--site", required=True)
    run.add_argument("--workers", type=int, default=16)
    run.add_argument("--duration", type=float, default=75)
    run.add_argument("--out", required=True)
    analyze = sub.add_parser("analyze")
    analyze.add_argument("--in", dest="inp", required=True)
    analyze.add_argument("--stop-at", type=float, required=True)
    analyze.add_argument("--window", type=float, default=30)
    analyze.add_argument("--max-error-rate", type=float, default=0.01)
    args = parser.parse_args(argv)
    return cmd_run(args) if args.command == "run" else cmd_analyze(args)


if __name__ == "__main__":
    sys.exit(main())
