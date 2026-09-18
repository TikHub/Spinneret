#!/usr/bin/env bash
# Serializes concurrent sqlc runs (several developers/agents may regenerate at once)
# using an atomic mkdir lock, then runs `sqlc generate` from the repository root.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCK="${TMPDIR:-/tmp}/spinneret-sqlc.lock"
export PATH="$HOME/go/bin:$PATH"
for _ in $(seq 1 600); do
  if mkdir "$LOCK" 2>/dev/null; then
    trap 'rmdir "$LOCK"' EXIT
    cd "$ROOT"
    sqlc generate "$@"
    exit 0
  fi
  sleep 0.2
done
echo "sqlc-generate: timed out waiting for lock $LOCK" >&2
exit 1
