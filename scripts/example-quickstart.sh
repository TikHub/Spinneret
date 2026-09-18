#!/usr/bin/env bash
# Example quickstart: prepares the "example" site on a running compose stack and proves the chain
# node → Spinneret → proxy → target works with the FastAPI example crawler.
#
#   scripts/example-quickstart.sh            # idempotent: reuses what exists (token included)
#   scripts/example-quickstart.sh --reset    # delete the example site, token, proxies, policies, config first
#
# Steps: start the stack (without recreating running containers) and the mock target, log in as the
# administrator through the API, create site "example" (endpoint groups search/detail), the identity
# type, 20 identities, 2 mock proxies, rotation + signal policies, config crawler/example.json and a
# node token (written to deploy/compose/.env as EXAMPLE_TOKEN), start example-crawler and call it.
#
# Environment: EXAMPLE_PORT (18000), SPINNERET_PORT (from .env, 8080), EXAMPLE_IDENTITIES (20).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$ROOT/deploy/compose/.env"
COMPOSE=(docker compose --progress quiet -f "$ROOT/deploy/compose/docker-compose.yml")
ADMIN=(python3 "$ROOT/scripts/lib/spinneret_admin.py")

RESET=0
for arg in "$@"; do
  case "$arg" in
    --reset) RESET=1 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

now() { python3 -c 'import time; print(f"{time.time():.3f}")'; }
elapsed() { python3 -c "import sys; print(f'{float(sys.argv[2]) - float(sys.argv[1]):.1f}s')" "$1" "$(now)"; }
log() { printf '[example-quickstart] %s\n' "$*"; }
die() { printf '[example-quickstart] ERROR: %s\n' "$*" >&2; exit 1; }
env_value() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1; }

command -v docker >/dev/null || die "docker is required"
command -v python3 >/dev/null || die "python3 is required"
command -v curl >/dev/null || die "curl is required"
[[ -f "$ENV_FILE" ]] || die "$ENV_FILE is missing: run scripts/compose-init.sh first"

SPINNERET_PORT="$(env_value SPINNERET_PORT)"
SPINNERET_PORT="${SPINNERET_PORT:-8080}"
EXAMPLE_PORT="${EXAMPLE_PORT:-$(env_value EXAMPLE_PORT)}"
EXAMPLE_PORT="${EXAMPLE_PORT:-18000}"
BASE="http://localhost:$SPINNERET_PORT"
EXAMPLE="http://localhost:$EXAMPLE_PORT"
START="$(now)"

# 1. Stack and mock target. --no-recreate keeps running containers (e.g. replicas started with the
#    e2e overlay) untouched; missing ones are created.
t="$(now)"
"${COMPOSE[@]}" --profile example up -d --wait --no-recreate lb mocktarget >/dev/null
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$BASE/readyz" && break
  sleep 1
done
curl -fsS -o /dev/null "$BASE/readyz" || die "Spinneret is not ready at $BASE/readyz"
log "stack ready ($(elapsed "$t"))"

# 2. Admin API: optional reset, then idempotent seeding.
if [[ "$RESET" == 1 ]]; then
  t="$(now)"
  removed="$("${ADMIN[@]}" example-clean --env-file "$ENV_FILE" --url "$BASE")"
  log "reset: $removed ($(elapsed "$t"))"
fi
t="$(now)"
seed="$("${ADMIN[@]}" example --env-file "$ENV_FILE" --url "$BASE" --write-env --identities "${EXAMPLE_IDENTITIES:-20}")"
summary="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); d.pop("token"); print(json.dumps(d))' "$seed")"
log "seeded: $summary ($(elapsed "$t"))"

# 3. Example crawler (rebuilt when its sources changed, recreated when EXAMPLE_TOKEN changed).
t="$(now)"
"${COMPOSE[@]}" --profile example up -d --build --wait --no-deps example-crawler >/dev/null
log "example-crawler running on $EXAMPLE ($(elapsed "$t"))"

# 4. Prove the chain: a search and an item page through leases, then the watched config.
t="$(now)"
check() {
  # $1 path, $2 python assertion over the JSON body `d`
  local path="$1" assertion="$2" body status
  for attempt in 1 2 3 4 5; do
    body="$(curl -sS -w '\n%{http_code}' "$EXAMPLE$path" || true)"
    status="${body##*$'\n'}"
    body="${body%$'\n'*}"
    if [[ "$status" == 200 ]] && python3 -c "import json,sys; d=json.loads(sys.argv[1]); assert $assertion, d" "$body" 2>/dev/null; then
      log "GET $path -> $body"
      return 0
    fi
    sleep "$attempt"
  done
  die "GET $path failed: HTTP $status $body"
}
check "/healthz" 'd["ok"]'
check "/crawl/search?q=quickstart" 'd["ok"] and d["status"] == 200 and d["identity_id"].startswith("idt_") and d["endpoint_group"] == "search"'
check "/crawl/item/42" 'd["ok"] and d["endpoint_group"] == "detail"'
check "/config" 'd["version"] >= 1 and d["content"]["search_page_size"] == 10'
log "chain verified ($(elapsed "$t"))"
log "done in $(elapsed "$START"): console $BASE (site example), crawler $EXAMPLE"
