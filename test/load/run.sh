#!/usr/bin/env bash
# Run one k6 scenario with server-side metric snapshots around it.
#
#   LOADTEST_TOKEN=spn_... test/load/run.sh acquire-5000 acquire_report.js ACQUIRE_RATE=5000 DURATION=2m
#
# Writes into ${OUT_DIR:-.loadtest}/<name>/:
#   before.json / mid.json / after.json  metric snapshots (test/load/metrics.py)
#   delta.json                           server-side deltas: throughput, histogram quantiles, CPU
#   k6.txt / k6.json                     k6 output and its machine-readable summary
#   gauges.json                          gauges sampled while the scenario was running
#
# The scenario runs through the compose `loadtest` profile, so k6 shares the Docker VM
# with the server; see docs/benchmarks.md for what that means for the numbers.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE=("docker" "compose" "-f" "${REPO}/deploy/compose/docker-compose.yml")

if [[ $# -lt 2 ]]; then
	echo "usage: $0 <run-name> <k6-script.js> [KEY=VALUE ...]" >&2
	exit 2
fi
NAME="$1"
SCRIPT="$2"
shift 2

: "${LOADTEST_TOKEN:?set LOADTEST_TOKEN (printed by \`spnr seed\`)}"
OUT_DIR="${OUT_DIR:-${REPO}/.loadtest}"
RUN_DIR="${OUT_DIR}/${NAME}"
mkdir -p "${RUN_DIR}"

K6_ENV=()
for kv in "$@"; do
	K6_ENV+=("-e" "${kv}")
done

# MID_AFTER seconds into the run a second snapshot samples the gauges (config
# watchers, stream backlog, goroutines) and `docker stats` while the load is on.
#
# `docker stats` walks every container of the machine and is expensive enough on
# Docker Desktop to perturb the run it is measuring: at 4,000 cycles/s it stalled
# the system under test for ~5 s (19,000 late iterations) and put a 12 ms p99 into
# an otherwise 2 ms run. Set MID_STATS=0 to keep the gauge sample and drop the
# `docker stats` part of it, which is what the throughput rows of
# docs/benchmarks.md are measured with. MID_STATS=0 loses only the per-container
# CPU/memory gauges in gauges.json.
MID_AFTER="${MID_AFTER:-30}"
MID_STATS="${MID_STATS:-1}"
MID_SNAPSHOT_ARGS=()
if [[ "${MID_STATS}" != "0" ]]; then
	MID_SNAPSHOT_ARGS+=("--stats")
fi

echo "== ${NAME}: ${SCRIPT} ${*}"
python3 "${REPO}/test/load/metrics.py" snapshot -o "${RUN_DIR}/before.json"

set +e
LOADTEST_TOKEN="${LOADTEST_TOKEN}" K6_SCRIPT="${SCRIPT}" \
	"${COMPOSE[@]}" --profile loadtest run --rm -T "${K6_ENV[@]}" k6 >"${RUN_DIR}/k6.txt" 2>&1 &
K6_PID=$!
sleep "${MID_AFTER}"
if kill -0 "${K6_PID}" 2>/dev/null; then
	python3 "${REPO}/test/load/metrics.py" snapshot \
		${MID_SNAPSHOT_ARGS[@]+"${MID_SNAPSHOT_ARGS[@]}"} -o "${RUN_DIR}/mid.json"
else
	echo "warning: k6 already finished before the mid-run snapshot (MID_AFTER=${MID_AFTER})" >&2
fi
wait "${K6_PID}"
K6_STATUS=$?
set -e

python3 "${REPO}/test/load/metrics.py" snapshot -o "${RUN_DIR}/after.json"
python3 "${REPO}/test/load/metrics.py" diff "${RUN_DIR}/before.json" "${RUN_DIR}/after.json" -o "${RUN_DIR}/delta.json"
if [[ -f "${RUN_DIR}/mid.json" ]]; then
	python3 "${REPO}/test/load/metrics.py" gauges "${RUN_DIR}/mid.json" -o "${RUN_DIR}/gauges.json"
fi

# k6 prints its machine-readable summary between the markers (see test/load/lib.js).
awk '/^---K6SUMMARY---$/{f=1;next} /^---K6SUMMARYEND---$/{f=0} f' "${RUN_DIR}/k6.txt" >"${RUN_DIR}/k6.json" || true
if [[ ! -s "${RUN_DIR}/k6.json" ]]; then
	rm -f "${RUN_DIR}/k6.json"
fi

echo "-- k6 exit ${K6_STATUS}; results in ${RUN_DIR}"
python3 - "${RUN_DIR}" <<'PY'
import json, sys, pathlib
run = pathlib.Path(sys.argv[1])
delta = json.loads((run / "delta.json").read_text())
t = delta["totals"]
print(f"   window {delta['elapsed_s']}s  acquire {t['acquire']} ({t['acquire_per_s']}/s, "
      f"{t['acquire_per_s_per_instance']}/s per instance)  reports {t['reports_accepted']} "
      f"({t['reports_per_s']}/s, {t['reports_per_s_per_instance']}/s per instance)")
# The snapshot window is a few seconds longer than the scenario, so the rates above
# are conservative; k6 measures the achieved rate over the scenario itself.
k6 = run / "k6.json"
if k6.exists():
    m = json.loads(k6.read_text())["metrics"]
    parts = [f"{n}={m[n]['rate']:.1f}/s" for n in
             ("acquire_ok", "acquire_exhausted", "report_accepted", "reports_accepted",
              "watch_polls", "watch_changes", "dropped_iterations", "iterations")
             if n in m and m[n].get("rate")]
    print("   k6: " + "  ".join(parts))
for name, inst in delta["instances"].items():
    acq = inst.get("spinneret_acquire_duration_seconds") or {}
    lag = inst.get("spinneret_report_lag_seconds") or {}
    print(f"   {name}: acquire p50={acq.get('p50_ms')}ms p99={acq.get('p99_ms')}ms n={acq.get('count')} | "
          f"lag p50={lag.get('p50_ms')}ms p99={lag.get('p99_ms')}ms n={lag.get('count')} | "
          f"cpu={inst.get('cpu_cores')} cores | http={inst.get('http_by_code')}")
v = delta.get("valkey", {})
print(f"   valkey: {v.get('commands_per_s')} cmd/s, cpu {v.get('cpu_cores')} cores, "
      f"used_memory {int(v.get('used_memory_after') or 0) / 2**20:.0f} MiB")
PY
exit "${K6_STATUS}"
