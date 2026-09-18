#!/usr/bin/env bash
# Serializes concurrent buf runs and regenerates Go code for all protos.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCK="${TMPDIR:-/tmp}/spinneret-buf.lock"
export PATH="$HOME/go/bin:$PATH"
for _ in $(seq 1 600); do
  if mkdir "$LOCK" 2>/dev/null; then
    trap 'rmdir "$LOCK"' EXIT
    cd "$ROOT"
    buf lint
    buf generate
    exit 0
  fi
  sleep 0.2
done
echo "buf-generate: timed out waiting for lock $LOCK" >&2
exit 1
