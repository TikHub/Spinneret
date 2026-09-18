#!/usr/bin/env python3
"""Server-side metric snapshots for the Spinneret load tests.

The server replicas do not publish a host port, so /metrics is scraped from
inside the compose network through the load balancer container (busybox wget).
Snapshots are plain JSON; `diff` turns two snapshots into throughput, histogram
quantiles (interpolated inside the Prometheus buckets) and container CPU.

    python3 test/load/metrics.py snapshot -o before.json
    ... run k6 ...
    python3 test/load/metrics.py snapshot -o after.json
    python3 test/load/metrics.py diff before.json after.json

Stdlib only; every container access goes through `docker compose`.
"""

from __future__ import annotations

import argparse
import json
import math
import subprocess
import sys
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
COMPOSE_FILE = REPO / "deploy" / "compose" / "docker-compose.yml"

# Histograms whose bucket deltas are turned into quantiles by `diff`.
HISTOGRAMS = (
    "spinneret_acquire_duration_seconds",
    "spinneret_report_lag_seconds",
    "spinneret_report_process_duration_seconds",
    "spinneret_http_request_duration_seconds",
    # Acquire admission control: the Lua round trip alone and the time an acquire waited
    # for a permit. The gap between spinneret_acquire_duration_seconds and
    # spinneret_acquire_script_seconds is the wait ladder plus rendering.
    "spinneret_acquire_script_seconds",
    "spinneret_acquire_admission_wait_seconds",
)

# Counters and gauges reported verbatim (sum over all label sets).
COUNTERS = (
    "spinneret_acquire_total",
    "spinneret_acquire_admission_total",
    "spinneret_report_ingest_total",
    "spinneret_report_total",
    "spinneret_http_requests_total",
    "spinneret_lease_reaped_total",
    "spinneret_breaker_transitions_total",
    "process_cpu_seconds_total",
)
GAUGES = (
    "spinneret_config_watchers",
    "spinneret_stream_pending",
    "spinneret_stream_owned_shards",
    "spinneret_identities_available",
    # Per-instance state of the acquire admission gate.
    "spinneret_acquire_inflight",
    "spinneret_acquire_queued",
    "spinneret_acquire_inflight_limit",
    "spinneret_acquire_peers",
    "go_goroutines",
    "go_memstats_heap_inuse_bytes",
    "process_resident_memory_bytes",
)


def run(cmd: list[str], timeout: float = 60.0, attempts: int = 3) -> str:
    """Run a command and return stdout, raising on a non-zero exit.

    A snapshot taken while the machine is saturated by the load generator can fail
    in the Docker CLI itself (the daemon does not answer in time), which must not
    end the run; such calls are retried.
    """
    last = ""
    for attempt in range(attempts):
        try:
            res = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            last = f"timed out after {timeout}s"
        else:
            if res.returncode == 0:
                return res.stdout
            last = f"exit {res.returncode}: {res.stderr.strip()[:300]}"
        if attempt + 1 < attempts:
            time.sleep(1.0 + attempt)
    raise RuntimeError(f"{' '.join(cmd)} failed ({last})")


def compose(*args: str, timeout: float = 60.0) -> str:
    return run(["docker", "compose", "-f", str(COMPOSE_FILE), *args], timeout=timeout)


def replicas() -> dict[str, str]:
    """Return {container name: IP} of the running spinneret replicas."""
    names = [n for n in compose("ps", "-q", "spinneret").split() if n]
    out: dict[str, str] = {}
    for cid in names:
        info = json.loads(run(["docker", "inspect", cid]))[0]
        name = info["Name"].lstrip("/")
        nets = info["NetworkSettings"]["Networks"]
        ip = next(iter(nets.values()))["IPAddress"]
        out[name] = ip
    return dict(sorted(out.items()))


def scrape(ip: str) -> str:
    """Fetch /metrics of one replica from inside the compose network."""
    return compose("exec", "-T", "lb", "wget", "-q", "-O", "-", f"http://{ip}:8080/metrics")


def parse_prom(text: str) -> dict:
    """Parse the exposition format into {name: [(labels, value)]}."""
    series: dict[str, list] = {}
    for line in text.splitlines():
        if not line or line[0] == "#":
            continue
        try:
            head, value = line.rsplit(" ", 1)
            val = float(value)
        except ValueError:
            continue
        if "{" in head:
            name, rest = head.split("{", 1)
            labels = {}
            for part in rest.rstrip("}").split('",'):
                if "=" not in part:
                    continue
                k, v = part.split("=", 1)
                labels[k.strip()] = v.strip().strip('"')
        else:
            name, labels = head, {}
        series.setdefault(name, []).append((labels, val))
    return series


def select(series: dict, wanted: tuple[str, ...]) -> dict:
    """Keep the interesting families in a compact JSON-friendly shape."""
    out: dict[str, list] = {}
    for name in wanted:
        for suffix in ("", "_bucket", "_sum", "_count"):
            key = name + suffix
            if key in series:
                out[key] = [[lbl, val] for lbl, val in series[key]]
    return out


def valkey_commandstats() -> dict:
    """Return {command: (calls, usec)} so `diff` can attribute the Valkey CPU.

    Calls issued from inside a Lua script are counted too, which is what makes
    the per-command breakdown of an Acquire or Report useful.
    """
    txt = compose("exec", "-T", "valkey", "valkey-cli", "info", "commandstats")
    out: dict[str, list[float]] = {}
    for line in txt.splitlines():
        if not line.startswith("cmdstat_") or ":" not in line:
            continue
        name, rest = line[len("cmdstat_"):].split(":", 1)
        fields = dict(part.split("=", 1) for part in rest.strip().split(",") if "=" in part)
        try:
            out[name] = [float(fields.get("calls", 0)), float(fields.get("usec", 0))]
        except ValueError:
            continue
    return out


def valkey_info() -> dict:
    """Return the Valkey memory/clients/ops counters used in the report."""
    txt = compose("exec", "-T", "valkey", "valkey-cli", "info")
    keep = {
        "used_memory", "used_memory_rss", "used_memory_peak", "maxmemory",
        "connected_clients", "blocked_clients", "total_commands_processed",
        "instantaneous_ops_per_sec", "total_net_input_bytes", "total_net_output_bytes",
        "used_cpu_sys", "used_cpu_user", "evicted_keys", "rejected_connections",
    }
    out = {}
    for line in txt.splitlines():
        if ":" in line:
            k, v = line.split(":", 1)
            if k in keep:
                out[k] = v.strip()
    return out


def docker_stats() -> dict:
    """One-shot `docker stats` of the compose project containers."""
    txt = run(["docker", "stats", "--no-stream", "--format",
               "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}"], timeout=60)
    out = {}
    for line in txt.splitlines():
        parts = line.split("\t")
        if len(parts) == 3 and parts[0].startswith("spinneret"):
            out[parts[0]] = {"cpu": parts[1], "mem": parts[2]}
    return out


def snapshot(with_stats: bool) -> dict:
    snap = {"at": time.time(), "instances": {}}
    for name, ip in replicas().items():
        series = parse_prom(scrape(ip))
        snap["instances"][name] = select(series, HISTOGRAMS + COUNTERS + GAUGES)
    snap["valkey"] = valkey_info()
    snap["valkey_commands"] = valkey_commandstats()
    if with_stats:
        snap["stats"] = docker_stats()
    return snap


def total(entries: list) -> float:
    return sum(v for _, v in entries)


def buckets(entries: list) -> list[tuple[float, float]]:
    """Return sorted (le, cumulative count) from *_bucket series."""
    acc: dict[float, float] = {}
    for labels, val in entries:
        le = labels.get("le", "+Inf")
        edge = math.inf if le in ("+Inf", "Inf") else float(le)
        acc[edge] = acc.get(edge, 0.0) + val
    return sorted(acc.items())


def quantile(bkts: list[tuple[float, float]], q: float) -> float | None:
    """Interpolate a quantile from cumulative bucket counts (Prometheus style)."""
    if not bkts:
        return None
    total_count = bkts[-1][1]
    if total_count <= 0:
        return None
    rank = q * total_count
    prev_edge, prev_count = 0.0, 0.0
    for edge, count in bkts:
        if count >= rank:
            if math.isinf(edge):
                return float("inf")
            if count == prev_count:
                return edge
            frac = (rank - prev_count) / (count - prev_count)
            return prev_edge + frac * (edge - prev_edge)
        prev_edge, prev_count = edge, count
    return float("inf")


def hist_delta(before: dict, after: dict, name: str) -> dict | None:
    b_b = buckets(before.get(name + "_bucket", []))
    a_b = buckets(after.get(name + "_bucket", []))
    if not a_b:
        return None
    base = dict(b_b)
    delta = [(edge, count - base.get(edge, 0.0)) for edge, count in a_b]
    count = delta[-1][1] if delta else 0.0
    if count <= 0:
        return {"count": 0}
    sum_d = total(after.get(name + "_sum", [])) - total(before.get(name + "_sum", []))
    res = {
        "count": round(count),
        "avg_ms": round(sum_d / count * 1000, 3),
        "p50_ms": None,
        "p90_ms": None,
        "p99_ms": None,
    }
    for q, key in ((0.5, "p50_ms"), (0.9, "p90_ms"), (0.99, "p99_ms")):
        v = quantile(delta, q)
        res[key] = None if v is None else (float("inf") if math.isinf(v) else round(v * 1000, 3))
    return res


def by_label(before: dict, after: dict, name: str, label: str) -> dict:
    """Counter delta grouped by one label."""
    def agg(src: dict) -> dict:
        out: dict[str, float] = {}
        for labels, val in src.get(name, []):
            out[labels.get(label, "_")] = out.get(labels.get(label, "_"), 0.0) + val
        return out
    b, a = agg(before), agg(after)
    return {k: round(v - b.get(k, 0.0)) for k, v in a.items() if v - b.get(k, 0.0) != 0}


def diff(before: dict, after: dict) -> dict:
    elapsed = after["at"] - before["at"]
    out: dict = {"elapsed_s": round(elapsed, 2), "instances": {}, "totals": {}}
    acquire_total = report_total = 0.0
    for name, a_inst in after["instances"].items():
        b_inst = before["instances"].get(name, {})
        inst: dict = {}
        for h in HISTOGRAMS:
            d = hist_delta(b_inst, a_inst, h)
            if d:
                inst[h] = d
        inst["acquire_by_result"] = by_label(b_inst, a_inst, "spinneret_acquire_total", "result")
        inst["admission_by_result"] = by_label(b_inst, a_inst, "spinneret_acquire_admission_total", "result")
        inst["report_ingest_by_result"] = by_label(b_inst, a_inst, "spinneret_report_ingest_total", "result")
        inst["report_by_outcome"] = by_label(b_inst, a_inst, "spinneret_report_total", "outcome")
        inst["http_by_code"] = by_label(b_inst, a_inst, "spinneret_http_requests_total", "code")
        cpu = total(a_inst.get("process_cpu_seconds_total", [])) - total(b_inst.get("process_cpu_seconds_total", []))
        inst["cpu_seconds"] = round(cpu, 2)
        inst["cpu_cores"] = round(cpu / elapsed, 2) if elapsed > 0 else None
        for g in ("spinneret_config_watchers", "go_goroutines", "spinneret_stream_owned_shards",
                  "process_resident_memory_bytes", "go_memstats_heap_inuse_bytes",
                  "spinneret_acquire_inflight_limit", "spinneret_acquire_peers"):
            if g in a_inst:
                inst[g] = round(total(a_inst[g]), 0)
        pending = total(a_inst.get("spinneret_stream_pending", []))
        inst["stream_pending"] = round(pending)
        acq = sum(inst["acquire_by_result"].values())
        rep = inst["report_ingest_by_result"].get("accepted", 0)
        acquire_total += acq
        report_total += rep
        inst["acquire_per_s"] = round(acq / elapsed, 1) if elapsed > 0 else None
        inst["reports_accepted_per_s"] = round(rep / elapsed, 1) if elapsed > 0 else None
        out["instances"][name] = inst
    n = max(1, len(after["instances"]))
    out["totals"] = {
        "acquire": round(acquire_total),
        "acquire_per_s": round(acquire_total / elapsed, 1) if elapsed > 0 else None,
        "acquire_per_s_per_instance": round(acquire_total / elapsed / n, 1) if elapsed > 0 else None,
        "reports_accepted": round(report_total),
        "reports_per_s": round(report_total / elapsed, 1) if elapsed > 0 else None,
        "reports_per_s_per_instance": round(report_total / elapsed / n, 1) if elapsed > 0 else None,
        "instances": n,
    }
    out["valkey"] = {
        "used_memory_after": after.get("valkey", {}).get("used_memory"),
        "used_memory_peak": after.get("valkey", {}).get("used_memory_peak"),
        "connected_clients": after.get("valkey", {}).get("connected_clients"),
        "ops_per_sec_sample": after.get("valkey", {}).get("instantaneous_ops_per_sec"),
        "commands_delta": _int(after, "total_commands_processed") - _int(before, "total_commands_processed"),
        "cpu_delta_s": round(
            _float(after, "used_cpu_sys") + _float(after, "used_cpu_user")
            - _float(before, "used_cpu_sys") - _float(before, "used_cpu_user"), 2),
    }
    if elapsed > 0:
        out["valkey"]["commands_per_s"] = round(out["valkey"]["commands_delta"] / elapsed, 1)
        out["valkey"]["cpu_cores"] = round(out["valkey"]["cpu_delta_s"] / elapsed, 2)
    out["valkey_commands"] = command_delta(before, after, elapsed)
    if "stats" in after:
        out["stats"] = after["stats"]
    return out


def gauges(snap: dict) -> dict:
    """Gauge values of one snapshot (used for the mid-run sample)."""
    out: dict = {"at": snap.get("at"), "instances": {}}
    wanted = ("spinneret_config_watchers", "spinneret_stream_pending", "spinneret_stream_owned_shards",
              "spinneret_acquire_inflight", "spinneret_acquire_queued",
              "spinneret_acquire_inflight_limit", "spinneret_acquire_peers",
              "go_goroutines", "go_memstats_heap_inuse_bytes", "process_resident_memory_bytes")
    watchers = 0.0
    for name, inst in snap["instances"].items():
        row: dict = {}
        for g in wanted:
            if g in inst:
                row[g] = round(total(inst[g]), 0)
        row["stream_pending_max_shard"] = max((v for _, v in inst.get("spinneret_stream_pending", [])), default=0)
        watchers += row.get("spinneret_config_watchers", 0)
        out["instances"][name] = row
    out["config_watchers_total"] = round(watchers)
    out["valkey"] = snap.get("valkey", {})
    if "stats" in snap:
        out["stats"] = snap["stats"]
    return out


def command_delta(before: dict, after: dict, elapsed: float) -> dict:
    """Per-command call and CPU deltas, most CPU first (top 15)."""
    b = before.get("valkey_commands", {})
    a = after.get("valkey_commands", {})
    rows = []
    for name, (calls, usec) in a.items():
        b_calls, b_usec = b.get(name, [0.0, 0.0])
        d_calls, d_usec = calls - b_calls, usec - b_usec
        if d_calls <= 0:
            continue
        rows.append({
            "cmd": name,
            "calls": round(d_calls),
            "calls_per_s": round(d_calls / elapsed, 1) if elapsed > 0 else None,
            "cpu_s": round(d_usec / 1e6, 2),
            "usec_per_call": round(d_usec / d_calls, 2),
        })
    rows.sort(key=lambda r: r["cpu_s"], reverse=True)
    total_cpu = round(sum(r["cpu_s"] for r in rows), 2)
    return {"total_cpu_s": total_cpu, "total_cpu_cores": round(total_cpu / elapsed, 2) if elapsed > 0 else None,
            "top": rows[:15]}


def _int(snap: dict, key: str) -> int:
    try:
        return int(snap.get("valkey", {}).get(key, 0))
    except (TypeError, ValueError):
        return 0


def _float(snap: dict, key: str) -> float:
    try:
        return float(snap.get("valkey", {}).get(key, 0))
    except (TypeError, ValueError):
        return 0.0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("snapshot", help="write a metric snapshot as JSON")
    s.add_argument("-o", "--out", help="output file (default: stdout)")
    s.add_argument("--stats", action="store_true", help="also collect `docker stats` (takes ~2 s)")
    d = sub.add_parser("diff", help="compare two snapshots")
    d.add_argument("before")
    d.add_argument("after")
    d.add_argument("-o", "--out", help="output file (default: stdout)")
    g = sub.add_parser("gauges", help="print the gauge values of one snapshot")
    g.add_argument("snapshot")
    g.add_argument("-o", "--out", help="output file (default: stdout)")
    args = ap.parse_args()

    if args.cmd == "snapshot":
        data = snapshot(args.stats)
    elif args.cmd == "gauges":
        data = gauges(json.loads(Path(args.snapshot).read_text()))
    else:
        data = diff(json.loads(Path(args.before).read_text()), json.loads(Path(args.after).read_text()))
    text = json.dumps(data, indent=2)
    if args.out:
        Path(args.out).write_text(text)
    else:
        print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
