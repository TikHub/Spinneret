#!/usr/bin/env bash
# Replica failover drill against the running compose stack (two spinneret replicas behind the LB).
#
#   scripts/e2e-failover.sh
#
# 1. Seeds site "failover" (200 identities) and a node token through the admin API.
# 2. Runs an acquire → report(release) load through the load balancer (FAILOVER_WORKERS workers for
#    FAILOVER_DURATION seconds).
# 3. After FAILOVER_STOP_AFTER seconds it stops replica 2 (`docker stop`, graceful: drain + shutdown)
#    and measures the report stream backlog (XLEN, XPENDING and consumer-group lag of every shard) and
#    shard ownership in Valkey until the survivor owns every shard and the backlog is drained.
# 4. When the load ends it starts the replica again and waits until both replicas are healthy workers
#    that share the shards.
# 5. Prints a summary and fails when the acquire/report error rate after the stop exceeds
#    FAILOVER_MAX_ERROR_RATE (default 0.01) or the backlog did not drain within FAILOVER_DRAIN_TIMEOUT.
#
# Environment: FAILOVER_WORKERS (16) FAILOVER_DURATION (75) FAILOVER_STOP_AFTER (20)
#              FAILOVER_DRAIN_TIMEOUT (60) FAILOVER_MAX_ERROR_RATE (0.01) FAILOVER_REPLICA (2)
#              FAILOVER_STEADY_BACKLOG (100: pending + lag considered caught up while the load runs)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$ROOT/deploy/compose/.env"
PROJECT="spinneret"
COMPOSE=(docker compose --progress quiet -f "$ROOT/deploy/compose/docker-compose.yml")
ADMIN=(python3 "$ROOT/scripts/lib/spinneret_admin.py")
LOAD=(python3 "$ROOT/scripts/lib/failover_load.py")

WORKERS="${FAILOVER_WORKERS:-16}"
DURATION="${FAILOVER_DURATION:-75}"
STOP_AFTER="${FAILOVER_STOP_AFTER:-20}"
DRAIN_TIMEOUT="${FAILOVER_DRAIN_TIMEOUT:-60}"
MAX_ERROR_RATE="${FAILOVER_MAX_ERROR_RATE:-0.01}"
REPLICA="${FAILOVER_REPLICA:-2}"
STEADY_BACKLOG="${FAILOVER_STEADY_BACKLOG:-100}"

now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
since() { python3 -c "import sys; print(f'{float(sys.argv[2]) - float(sys.argv[1]):.1f}')" "$1" "$(now)"; }
log() { printf '[failover %s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { printf '[failover] ERROR: %s\n' "$*" >&2; exit 1; }
env_value() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1; }

command -v docker >/dev/null || die "docker is required"
command -v python3 >/dev/null || die "python3 is required"
[[ -f "$ENV_FILE" ]] || die "$ENV_FILE is missing: run scripts/compose-init.sh first"
PORT="$(env_value SPINNERET_PORT)"
BASE="http://localhost:${PORT:-8080}"
SHARDS="$(env_value SPINNERET_REPORT_SHARDS)"
SHARDS="${SHARDS:-16}"
REDIS_PREFIX="$(env_value SPINNERET_REDIS_PREFIX)"
REDIS_PREFIX="${REDIS_PREFIX:-sp}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/spinneret-failover.XXXXXX")"
TOKEN_ID=""
LOAD_PID=""
cleanup() {
  [[ -n "$LOAD_PID" ]] && kill "$LOAD_PID" 2>/dev/null || true
  if [[ -n "${VICTIM:-}" ]] && [[ "$(docker inspect -f '{{.State.Running}}' "$VICTIM" 2>/dev/null)" == "false" ]]; then
    log "restarting $VICTIM after an interrupted drill"
    docker start "$VICTIM" >/dev/null || true
  fi
  [[ -n "$TOKEN_ID" ]] && "${ADMIN[@]}" revoke-token --env-file "$ENV_FILE" --url "$BASE" --token-id "$TOKEN_ID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# valkey runs a Lua snippet in the valkey container and prints its result lines.
valkey_eval() {
  "${COMPOSE[@]}" exec -T valkey valkey-cli --raw EVAL "$1" 0 "$REDIS_PREFIX" "$SHARDS"
}

# Backlog of the report streams: total XLEN, pending entries and lag of the "workers" groups.
BACKLOG_LUA='
local prefix, shards = ARGV[1], tonumber(ARGV[2])
local xlen, pending, lag = 0, 0, 0
for i = 0, shards - 1 do
  local key = prefix .. ":{r" .. i .. "}:stream"
  if redis.call("EXISTS", key) == 1 then
    xlen = xlen + redis.call("XLEN", key)
    for _, g in ipairs(redis.call("XINFO", "GROUPS", key)) do
      local fields = {}
      for j = 1, #g, 2 do fields[g[j]] = g[j + 1] end
      if fields["name"] == "workers" then
        pending = pending + (tonumber(fields["pending"]) or 0)
        lag = lag + (tonumber(fields["lag"]) or 0)
      end
    end
  end
end
return {xlen, pending, lag}'

# Shard owners as "<instance>=<count>" lines, plus the number of live workers (heartbeat within 10 s).
OWNERS_LUA='
local prefix, shards = ARGV[1], tonumber(ARGV[2])
local counts, out = {}, {}
for i = 0, shards - 1 do
  local o = redis.call("GET", prefix .. ":{r" .. i .. "}:owner") or "(none)"
  counts[o] = (counts[o] or 0) + 1
end
for o, n in pairs(counts) do table.insert(out, o .. "=" .. n) end
table.sort(out)
local t = redis.call("TIME")
local nowms = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
table.insert(out, "live_workers=" .. redis.call("ZCOUNT", prefix .. ":workers", nowms - 10000, "+inf"))
return out'

backlog() { valkey_eval "$BACKLOG_LUA" | paste -sd' ' -; }       # "xlen pending lag"
owners() { valkey_eval "$OWNERS_LUA" | paste -sd' ' -; }

# --- preflight ---------------------------------------------------------------------------------------
VICTIM="$(docker ps --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.service=spinneret" \
  --filter "label=com.docker.compose.container-number=$REPLICA" --format '{{.Names}}')"
[[ -n "$VICTIM" ]] || die "replica $REPLICA of service spinneret is not running (start the stack first)"
running="$(docker ps --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.service=spinneret" --format '{{.Names}}' | wc -l | tr -d ' ')"
[[ "$running" -ge 2 ]] || die "the drill needs at least 2 running replicas (found $running)"
curl -fsS -o /dev/null "$BASE/readyz" || die "Spinneret is not ready at $BASE"
log "replicas running: $running, victim: $VICTIM, shards: $SHARDS, owners: $(owners)"

seed="$("${ADMIN[@]}" failover --env-file "$ENV_FILE" --url "$BASE")"
TOKEN="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["token"])' "$seed")"
TOKEN_ID="$(python3 -c 'import json,sys; print(json.loads(sys.argv[1])["token_id"])' "$seed")"
log "seeded site failover: $(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d["identities"])' "$seed")"

# --- load + stop -------------------------------------------------------------------------------------
"${LOAD[@]}" run --url "$BASE" --token "$TOKEN" --site failover --workers "$WORKERS" --duration "$DURATION" \
  --out "$WORK/load.json" &
LOAD_PID=$!
log "load started: $WORKERS workers for ${DURATION}s"
sleep "$STOP_AFTER"
log "before stop: backlog(xlen pending lag)=$(backlog) owners: $(owners)"

STOP_AT="$(now)"
docker stop "$VICTIM" >/dev/null
STOP_TOOK="$(since "$STOP_AT")"
log "stopped $VICTIM in ${STOP_TOOK}s (graceful drain + shutdown)"

# While the load continues the backlog never stays at exactly zero (new reports keep arriving), so
# "caught up" means: the survivor owns every shard and pending + lag is back at a steady level.
CAUGHT_UP_AFTER=""
TAKEOVER_AFTER=""
MAX_BACKLOG=0
deadline=$(( $(date +%s) + DRAIN_TIMEOUT ))
while [[ "$(date +%s)" -le "$deadline" ]]; do
  read -r xlen pending lag <<<"$(backlog)"
  own="$(owners)"
  (( pending + lag > MAX_BACKLOG )) && MAX_BACKLOG=$(( pending + lag ))
  owner_count="$(grep -o "[^ ]*=[0-9]*" <<<"$own" | grep -v '^live_workers=' | wc -l | tr -d ' ')"
  if [[ -z "$TAKEOVER_AFTER" && "$owner_count" == 1 && "$own" != *"(none)"* ]]; then
    TAKEOVER_AFTER="$(since "$STOP_AT")"
    log "survivor owns every shard after ${TAKEOVER_AFTER}s: $own"
  fi
  if [[ -n "$TAKEOVER_AFTER" ]] && (( pending + lag <= STEADY_BACKLOG )); then
    CAUGHT_UP_AFTER="$(since "$STOP_AT")"
    log "backlog caught up after ${CAUGHT_UP_AFTER}s (pending $pending, lag $lag, max pending+lag since stop $MAX_BACKLOG, xlen $xlen)"
    break
  fi
  sleep 1
done
[[ -n "$CAUGHT_UP_AFTER" ]] || log "backlog NOT caught up within ${DRAIN_TIMEOUT}s: pending=$pending lag=$lag owners: $(owners)"

wait "$LOAD_PID" || die "load generator failed"
LOAD_PID=""
LOAD_END="$(now)"
log "load finished"
# Without new reports the backlog must drain completely.
DRAINED_AFTER=""
for _ in $(seq 1 60); do
  read -r xlen pending lag <<<"$(backlog)"
  if [[ "$pending" == 0 && "$lag" == 0 ]]; then
    DRAINED_AFTER="$(since "$LOAD_END")"
    break
  fi
  sleep 1
done
log "backlog after the load: pending $pending, lag $lag (drained after ${DRAINED_AFTER:-never}s)"

# --- restart -----------------------------------------------------------------------------------------
START_AT="$(now)"
docker start "$VICTIM" >/dev/null
for _ in $(seq 1 120); do
  [[ "$(docker inspect -f '{{.State.Health.Status}}' "$VICTIM")" == "healthy" ]] && break
  sleep 1
done
HEALTHY_AFTER="$(since "$START_AT")"
[[ "$(docker inspect -f '{{.State.Health.Status}}' "$VICTIM")" == "healthy" ]] || die "$VICTIM did not become healthy"
REBALANCED_AFTER=""
for _ in $(seq 1 60); do
  own="$(owners)"
  owner_count="$(grep -o "[^ ]*=[0-9]*" <<<"$own" | grep -v '^live_workers=' | wc -l | tr -d ' ')"
  if [[ "$own" == *"live_workers=2"* && "$owner_count" == 2 && "$own" != *"(none)"* ]]; then
    REBALANCED_AFTER="$(since "$START_AT")"
    break
  fi
  sleep 1
done
log "restarted $VICTIM: healthy after ${HEALTHY_AFTER}s, shards rebalanced after ${REBALANCED_AFTER:-never}s: $(owners)"

# --- summary -----------------------------------------------------------------------------------------
set +e
analysis="$("${LOAD[@]}" analyze --in "$WORK/load.json" --stop-at "$STOP_AT" --window 30 --max-error-rate "$MAX_ERROR_RATE")"
analysis_status=$?
set -e
echo "$analysis"
python3 - "$analysis" <<EOF
import json, sys
a = json.loads(sys.argv[1])
w = a["windows"]
def row(name):
    x = w[name]
    return f"{name:<22} acquire {x['acquire']['total']:>6} req {x['acquire']['errors']:>4} err ({x['acquire']['error_rate']:.3%})   report {x['report']['total']:>6} req {x['report']['errors']:>4} err ({x['report']['error_rate']:.3%})"
print("================ failover summary ================")
for name in w:
    print(row(name))
print(f"stop took               ${STOP_TOOK}s")
print(f"survivor owned shards   ${TAKEOVER_AFTER:-never}s after stop")
print(f"backlog caught up       ${CAUGHT_UP_AFTER:-never}s after stop (max pending+lag ${MAX_BACKLOG})")
print(f"backlog fully drained   ${DRAINED_AFTER:-never}s after the load ended")
print(f"replica healthy again   ${HEALTHY_AFTER}s after start, shards rebalanced after ${REBALANCED_AFTER:-never}s")
print(f"latency ms              {a['latency_ms']}")
print(f"max error rate          {a['max_error_rate']:.3%} (limit {float('${MAX_ERROR_RATE}'):.3%})")
EOF
[[ -n "$CAUGHT_UP_AFTER" ]] || die "report backlog did not catch up after the stop"
[[ -n "$DRAINED_AFTER" ]] || die "report backlog did not drain after the load"
[[ -n "$REBALANCED_AFTER" ]] || die "shards were not rebalanced after the restart"
[[ "$analysis_status" == 0 ]] || die "error rate above $MAX_ERROR_RATE"
log "PASS"
