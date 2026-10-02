#!/usr/bin/env bash
# Serializes concurrent sqlc runs (several developers/agents may regenerate at once)
# using an atomic mkdir lock, then regenerates the query code from the repository root.
#
# With no arguments it runs `sqlc generate -f` for every sqlc.yaml outside web/ -- the
# root one and one per package that owns queries -- which is what CI does before it
# checks `git diff --exit-code -- gen internal`. Arguments go to a single
# `sqlc generate` instead, e.g. `-f internal/auth/sqlc.yaml` for one package.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCK="${TMPDIR:-/tmp}/spinneret-sqlc.lock"
export PATH="$HOME/go/bin:$PATH"
for _ in $(seq 1 600); do
  if mkdir "$LOCK" 2>/dev/null; then
    trap 'rmdir "$LOCK"' EXIT
    cd "$ROOT"
    if [ "$#" -gt 0 ]; then
      sqlc generate "$@"
    else
      # The same files, in the same way, as the loop in .github/workflows/ci.yml.
      while IFS= read -r config; do
        sqlc generate -f "$config"
      done < <(find . -name sqlc.yaml -not -path './web/*')
    fi
    exit 0
  fi
  sleep 0.2
done
echo "sqlc-generate: timed out waiting for lock $LOCK" >&2
exit 1
