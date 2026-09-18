#!/usr/bin/env bash
# Prepares deploy/compose for a first start: random passwords in .env and a KEK secret file.
# Safe to re-run: existing files are kept.
set -euo pipefail
DIR="$(cd "$(dirname "$0")/../deploy/compose" && pwd)"
mkdir -p "$DIR/secrets"
rand() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "${1:-32}"; }
if [[ ! -f "$DIR/.env" ]]; then
  sed -e "s/^PG_PASSWORD=.*/PG_PASSWORD=$(rand 32)/" \
      -e "s/^CLICKHOUSE_PASSWORD=.*/CLICKHOUSE_PASSWORD=$(rand 32)/" \
      -e "s/^SPINNERET_ADMIN_PASSWORD=.*/SPINNERET_ADMIN_PASSWORD=$(rand 20)/" \
      "$DIR/.env.example" > "$DIR/.env"
  echo "created $DIR/.env"
fi
if [[ ! -f "$DIR/secrets/kek.key" ]]; then
  printf 'k1:%s\n' "$(openssl rand -base64 32)" > "$DIR/secrets/kek.key"
  # The container runs as the distroless nonroot user and must be able to read the bind-mounted secret.
  chmod 0644 "$DIR/secrets/kek.key"
  echo "created $DIR/secrets/kek.key (back it up: data encrypted with it is unrecoverable without it)"
fi
