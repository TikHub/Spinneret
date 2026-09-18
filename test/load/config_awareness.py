#!/usr/bin/env python3
"""Measure config-change awareness: publish → WatchConfig wake-up latency.

Design target §18.4: a published configuration change must reach the nodes in under
one second. Publisher and watchers live in this one process, so both timestamps come
from the same clock and no container/host clock offset can distort the result.

    python3 test/load/config_awareness.py --watchers 200 --publishes 10 --interval 5

Each round publishes a new version of the watched item whose content carries
`published_at_ms` (test/load/watch_config.js reads it, so k6 watchers report the same
latency), then waits until every watcher has been woken. Two latencies are reported per
watcher: from just before the publish RPC (`total`, the conservative figure) and from
the moment the publish RPC returned (`from_commit`, the pure fan-out latency).

Stdlib only. Admin credentials come from deploy/compose/.env; the node token from
--token or $LOADTEST_TOKEN.
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO / "scripts" / "lib"))

from spinneret_admin import Admin, ApiError, read_env_file  # noqa: E402


class Watcher(threading.Thread):
    """One node-token WatchConfig long poll loop."""

    def __init__(self, base_url: str, token: str, group: str, key: str, version: int,
                 timeout_ms: int, stop: threading.Event, index: int) -> None:
        super().__init__(daemon=True, name=f"watcher-{index}")
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.group = group
        self.key = key
        self.version = version
        self.timeout_ms = timeout_ms
        self.stop = stop
        self.index = index
        # (version, monotonic receive time) of every change this watcher observed.
        self.received: list[tuple[int, float]] = []
        self.errors: list[str] = []
        self.polls = 0

    def run(self) -> None:
        url = f"{self.base_url}/spinneret.v1.ConfigService/WatchConfig"
        headers = {
            "Content-Type": "application/json",
            "Connect-Protocol-Version": "1",
            "Authorization": f"Bearer {self.token}",
            "X-Spinneret-Node": f"awareness-{self.index}",
        }
        while not self.stop.is_set():
            body = json.dumps({
                "items": [{"group": self.group, "key": self.key, "version": self.version}],
                "timeout_ms": self.timeout_ms,
            }).encode()
            req = urllib.request.Request(url, data=body, headers=headers, method="POST")
            try:
                with urllib.request.urlopen(req, timeout=self.timeout_ms / 1000 + 20) as resp:
                    payload = json.loads(resp.read() or b"{}")
            except (urllib.error.URLError, OSError, ValueError) as err:
                if self.stop.is_set():
                    return
                self.errors.append(str(err)[:160])
                time.sleep(0.2)
                continue
            at = time.monotonic()
            self.polls += 1
            for item in payload.get("items", []):
                version = int(item.get("version", 0))
                if version > self.version:
                    self.version = version
                    self.received.append((version, at))


def percentile(values: list[float], q: float) -> float:
    if not values:
        return float("nan")
    ordered = sorted(values)
    idx = min(len(ordered) - 1, max(0, round(q * (len(ordered) - 1))))
    return ordered[idx]


def summarize(values: list[float]) -> dict:
    if not values:
        return {"n": 0}
    return {
        "n": len(values),
        "min_ms": round(min(values) * 1000, 1),
        "avg_ms": round(statistics.fmean(values) * 1000, 1),
        "p50_ms": round(percentile(values, 0.5) * 1000, 1),
        "p95_ms": round(percentile(values, 0.95) * 1000, 1),
        "p99_ms": round(percentile(values, 0.99) * 1000, 1),
        "max_ms": round(max(values) * 1000, 1),
    }


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default=os.environ.get("SPINNERET_URL", "http://localhost:8080"))
    ap.add_argument("--token", default=os.environ.get("LOADTEST_TOKEN", ""), help="node token (config:read scope)")
    ap.add_argument("--env-file", default=str(REPO / "deploy" / "compose" / ".env"))
    ap.add_argument("--namespace", default="default")
    ap.add_argument("--group", default="crawler")
    ap.add_argument("--key", default="loadtest.json")
    ap.add_argument("--watchers", type=int, default=50, help="long polls held by this process")
    ap.add_argument("--publishes", type=int, default=10, help="config versions to publish")
    ap.add_argument("--interval", type=float, default=5.0, help="seconds between publishes")
    ap.add_argument("--timeout-ms", type=int, default=60000, help="long-poll timeout")
    ap.add_argument("--settle", type=float, default=3.0, help="seconds to wait for the last wake-ups")
    ap.add_argument("-o", "--out", help="write the JSON result to this file as well")
    args = ap.parse_args(argv)

    if not args.token:
        ap.error("no node token: pass --token or set LOADTEST_TOKEN")
    env = read_env_file(args.env_file)
    username = env.get("SPINNERET_ADMIN_USERNAME", "admin")
    password = env.get("SPINNERET_ADMIN_PASSWORD", "")
    if not password:
        ap.error(f"SPINNERET_ADMIN_PASSWORD is not set in {args.env_file}")

    admin = Admin(args.url)
    admin.login(username, password)
    try:
        item = admin.call("ConfigAdminService", "GetConfigItem",
                          {"locator": {"namespace": args.namespace, "group": args.group, "key": args.key}})["item"]
    except ApiError as err:
        print(f"config item {args.group}/{args.key} is not available: {err}", file=sys.stderr)
        return 1
    item_id = item["id"]
    version = int(item.get("current_version") or 0)
    if version == 0:
        print(f"config item {args.group}/{args.key} has no published version", file=sys.stderr)
        return 1

    stop = threading.Event()
    watchers = [Watcher(args.url, args.token, args.group, args.key, version, args.timeout_ms, stop, i)
                for i in range(args.watchers)]
    for w in watchers:
        w.start()
    # Give every watcher time to block on the server before the first publish.
    time.sleep(min(5.0, 0.5 + args.watchers / 200))

    rounds = []
    for n in range(args.publishes):
        content = json.dumps({
            "site": "loadtest", "round": n + 1,
            "published_at_ms": int(time.time() * 1000),
            "note": "test/load/config_awareness.py",
        })
        expected = version + 1
        before = time.monotonic()
        admin.call("ConfigAdminService", "SaveConfigDraft", {"id": item_id, "content": content})
        t0 = time.monotonic()
        admin.call("ConfigAdminService", "PublishConfig", {"id": item_id, "comment": "awareness probe"})
        t1 = time.monotonic()
        version = expected

        deadline = t1 + max(args.settle, 2.0)
        while time.monotonic() < deadline:
            if all(any(v >= expected for v, _ in w.received) for w in watchers):
                break
            time.sleep(0.005)

        total, from_commit = [], []
        woken = 0
        for w in watchers:
            hits = [at for v, at in w.received if v >= expected]
            if not hits:
                continue
            woken += 1
            at = min(hits)
            total.append(at - t0)
            from_commit.append(max(0.0, at - t1))
        rounds.append({
            "round": n + 1,
            "version": expected,
            "publish_rpc_ms": round((t1 - t0) * 1000, 1),
            "draft_rpc_ms": round((t0 - before) * 1000, 1),
            "woken": woken,
            "watchers": len(watchers),
            "total": summarize(total),
            "from_commit": summarize(from_commit),
        })
        print(f"round {n + 1}/{args.publishes}: version {expected}, woken {woken}/{len(watchers)}, "
              f"total p99 {rounds[-1]['total'].get('p99_ms', 'n/a')} ms, "
              f"publish rpc {rounds[-1]['publish_rpc_ms']} ms", file=sys.stderr)
        if n + 1 < args.publishes:
            time.sleep(args.interval)

    stop.set()
    # Long polls return on the next timeout; do not block the run on them.
    all_total = [v / 1000 for r in rounds for v in [r["total"]["avg_ms"]] if r["total"]["n"]]
    result = {
        "url": args.url,
        "watchers": args.watchers,
        "publishes": args.publishes,
        "rounds": rounds,
        "aggregate": {
            "woken_ratio": round(sum(r["woken"] for r in rounds) / max(1, sum(r["watchers"] for r in rounds)), 4),
            "total_p99_ms_max": max((r["total"].get("p99_ms", 0) for r in rounds if r["total"]["n"]), default=None),
            "total_max_ms": max((r["total"].get("max_ms", 0) for r in rounds if r["total"]["n"]), default=None),
            "from_commit_p99_ms_max": max((r["from_commit"].get("p99_ms", 0) for r in rounds if r["from_commit"]["n"]), default=None),
            "avg_of_round_avgs_ms": round(statistics.fmean(all_total) * 1000, 1) if all_total else None,
            "poll_errors": sum(len(w.errors) for w in watchers),
        },
    }
    text = json.dumps(result, indent=2)
    print(text)
    if args.out:
        Path(args.out).write_text(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
