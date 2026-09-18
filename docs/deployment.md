# Deployment

[中文文档](deployment.zh-CN.md) · [Operations runbook](operations.md) · [API reference](api.md)

Spinneret ships as a single stateless binary (`spinneret-server`) plus an admin CLI (`spnr`), both in one
distroless image. This guide covers the supported deployment — Docker Compose — and everything around it:
configuration, keys, TLS, scaling, upgrades, backups, hardening and troubleshooting.

- [Requirements](#requirements)
- [Compose deployment](#compose-deployment)
- [Configuration reference](#configuration-reference)
- [KEK management](#kek-management)
- [TLS and reverse proxies](#tls-and-reverse-proxies)
- [Scaling](#scaling)
- [Upgrades and migrations](#upgrades-and-migrations)
- [Backups](#backups)
- [Security hardening checklist](#security-hardening-checklist)
- [Troubleshooting](#troubleshooting)

---

## Requirements

| Component | Version | Notes |
| --- | --- | --- |
| Docker Engine | 24+ with the Compose plugin | `docker compose version` must work |
| PostgreSQL | 17 (16 works) | Source of truth; Compose runs `postgres:17-alpine` |
| Redis or Valkey | Valkey 8 / Redis 7+ | Hot state, leases, report streams, sessions. Must be persistent (`appendonly yes`) |
| ClickHouse | 25.x, optional | Raw request events for the request explorer; leave `SPINNERET_CLICKHOUSE_URL` empty to disable |

Sizing for a mid-size deployment (one site, 100 000 identities, 50 endpoint groups, a few thousand
acquires/second): 2 server replicas at 4 vCPU / 8 GB, PostgreSQL 4 vCPU / 8 GB with fast disks, Valkey
2 vCPU / 4 GB. Server instances are stateless — scale them horizontally. The `deploy/compose` defaults
(2 replicas, `max_connections=300`, `shared_buffers=512MB`) run comfortably on a 8 GB developer machine.

Network ports published by the Compose stack:

| Port | Service | Variable |
| --- | --- | --- |
| 8080 | Console and API through the Caddy load balancer | `SPINNERET_PORT` |
| 9090 | Prometheus (profile `observability`) | `PROMETHEUS_PORT` |
| 19090 / 19091 | Mock target site / mock proxy (profiles `test`, `loadtest`, `example`) | `MOCK_TARGET_PORT`, `MOCK_PROXY_PORT` |
| 18000 | Example crawler (profile `example`) | `EXAMPLE_PORT` |

PostgreSQL, Valkey and ClickHouse are **not** published to the host; they are reachable only on the
Compose network.

---

## Compose deployment

Everything lives in [`deploy/compose/docker-compose.yml`](../deploy/compose/docker-compose.yml), project
name `spinneret`.

### 1. Initialize secrets

```bash
./scripts/compose-init.sh
```

Creates, if missing:

- `deploy/compose/.env` from `.env.example` with random `PG_PASSWORD`, `CLICKHOUSE_PASSWORD` and
  `SPINNERET_ADMIN_PASSWORD`,
- `deploy/compose/secrets/kek.key` with a fresh 32-byte key-encryption key (`k1:<base64>`).

The script is safe to re-run; existing files are kept. **Back up `kek.key` before going further** — data
encrypted with it cannot be recovered without it. Both files are git-ignored and excluded from the Docker
build context.

### 2. Start the stack

```bash
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

Services, in start order:

| Service | Role |
| --- | --- |
| `postgres` | PostgreSQL 17 with a named volume `pgdata` |
| `valkey` | Valkey 8, AOF persistence (`appendfsync everysec`), `maxmemory-policy noeviction` |
| `clickhouse` | ClickHouse 25.8 with a named volume `chdata` |
| `migrate` | One-shot `spnr migrate up`; the server waits for it to complete successfully |
| `spinneret` | The server, 2 replicas (`SPINNERET_REPLICAS`), `stop_grace_period: 40s` |
| `lb` | Caddy reverse proxy on host port 8080, readiness-based health checks |

`--wait` returns when every container is healthy. Migrations are serialized with a PostgreSQL advisory
lock, so it is safe for several instances to start at once.

### 3. Bootstrap the first administrator

```bash
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

Creates the platform administrator (`SPINNERET_ADMIN_USERNAME`, default `admin`), the tenant `default`, the
namespace `default` and its four default policies. The command is idempotent — on a second run it prints
`already initialized` and exits 0.

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env     # the password
open http://localhost:8080
```

### 4. Verify

```bash
curl -s localhost:8080/healthz    # {"status":"ok"}
curl -s localhost:8080/readyz     # {"checks":{"catalog":"ok","hotstate":"ok","postgres":"ok","redis":"ok"},"status":"ok"}
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr migrate status
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr config check   # env, secrets redacted
```

### Optional profiles

| Profile | Services | Command |
| --- | --- | --- |
| `init` | `init-admin` | `--profile init run --rm init-admin` |
| `observability` | `prometheus` scraping both replicas | `--profile observability up -d` |
| `test` | `mocktarget` (mock site + authenticating proxy) | used by `make e2e` |
| `loadtest` | `mocktarget` + `k6` | `make load`, see [`test/load/README.md`](../test/load/README.md) |
| `example` | `mocktarget` + `example-crawler` | `./scripts/example-quickstart.sh` |

Profiles do not change the core stack; they only add containers.

### Stopping and removing

```bash
docker compose -f deploy/compose/docker-compose.yml down       # stop, keep volumes
docker compose -f deploy/compose/docker-compose.yml down -v    # also delete pgdata, valkeydata, chdata
```

---

## Configuration reference

The server and `spnr` read the same `SPINNERET_*` environment variables. Values are trimmed; an empty value
is treated as unset. Durations accept `ms`, `s`, `m`, `h`, `d` suffixes (`30s`, `7d`, `1h30m`). Invalid
values abort startup with a message naming the variable — validate an environment before deploying it with
`spnr config check`.

### Core

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_HTTP_ADDR` | `:8080` | Listen address of the API, console and (by default) `/metrics` |
| `SPINNERET_ROLE` | `all` | `all`, `api` (serve requests only) or `worker` (background pipeline only) |
| `SPINNERET_INSTANCE_ID` | hostname + 6 random hex | Identifies the instance in logs, job leases and stream consumer groups |
| `SPINNERET_SHUTDOWN_TIMEOUT` | `30s` | Total graceful shutdown budget. The first quarter (max 5 s) is a drain window in which `/readyz` answers `draining` before the listener closes |

### Storage

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_DATABASE_URL` | **required** | PostgreSQL URL or libpq DSN |
| `SPINNERET_DATABASE_MAX_CONNS` | `32` | Pool size per instance (minimum 2). Leader jobs each hold one connection while they own their advisory lock; startup refuses a pool that they would exhaust |
| `SPINNERET_REDIS_URL` | **required** unless `_ADDRS` is set | `redis://` or `rediss://` URL |
| `SPINNERET_REDIS_ADDRS` | empty | Comma-separated `host:port` list for Redis Cluster |
| `SPINNERET_REDIS_PREFIX` | `sp` | Key prefix; must not contain `{`, `}`, `:` or spaces. Two deployments sharing one Redis need different prefixes |
| `SPINNERET_CLICKHOUSE_URL` | empty | `clickhouse://user:pass@host:9000/db`; empty disables raw request events and the request explorer |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | TTL of the raw event table (1–3650) |

### Keys

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_KEK_FILE` | — | File with KEK lines `id:base64` (one per line), or a single base64 key (id `k1`) |
| `SPINNERET_KEKS` | — | KEKs inline: `k1:base64,k2:base64` |
| `SPINNERET_KEK_CURRENT` | last key listed | ID of the KEK used to wrap **new** data keys |

At least one of `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` is required; keys are exactly 32 bytes. See
[KEK management](#kek-management).

### Report pipeline

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_REPORT_SHARDS` | `16` | Number of Redis stream shards (1–255). **Fixed for the life of a deployment** — lease IDs encode their shard, see [Changing the shard count](#changing-the-shard-count) |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | Window in which a repeated `report_id` is counted as `duplicated` (minimum 1m) |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | Reports arriving later than this are recorded but no longer change identity state |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | Approximate cap of the *backlog* per stream shard, minimum 1000. Workers trim their shard to the consumer position, so a drained stream stays small; entries beyond this cap are dropped when the workers cannot keep up |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | `true` | Write a state event for every cooldown. Set to `false` at very high volume if state-event storage dominates |

### Caches

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_PAYLOAD_CACHE` | `true` | Cache rendered credentials in process. Disable to keep decrypted payloads out of process memory at the cost of a decrypt per acquire |
| `SPINNERET_PAYLOAD_CACHE_SIZE` | `200000` | LRU entries. Entries containing resolved secret references expire after 60 s regardless |
| `SPINNERET_DEK_CACHE_SIZE` | `100000` | LRU of unwrapped data keys |
| `SPINNERET_DEK_CACHE_TTL` | `10m` | TTL of that cache |
| `SPINNERET_TOKEN_CACHE_TTL` | `30s` | How long a token verification result is cached. Revocation publishes an event that drops it immediately; this bounds the worst case |

### Console, sessions and transport security

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_SESSION_TTL` | `12h` | Console session lifetime, refreshed when less than half remains (minimum 1m) |
| `SPINNERET_COOKIE_SECURE` | `auto` | `auto` sets `Secure` when the request arrived over TLS or with `X-Forwarded-Proto: https`; `true` always; `false` never (plain-HTTP intranet only) |
| `SPINNERET_TLS_CERT_FILE` / `SPINNERET_TLS_KEY_FILE` | empty | Serve HTTPS directly. Both or neither |
| `SPINNERET_TRUSTED_PROXIES` | empty | CIDRs whose `X-Forwarded-For` / `X-Forwarded-Proto` are trusted. **Required** behind a reverse proxy for correct client IPs (token IP allowlists, login throttling, audit) |
| `SPINNERET_UI_ENABLED` | `true` | Serve the embedded console. `false` leaves only the APIs |
| `SPINNERET_ALLOWED_ORIGINS` | empty | CORS allow-list, for running `pnpm dev` against this server. Leave empty in production |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | `67108864` (64 MiB) | Body limit of admin RPCs (identity and proxy imports); minimum 1 MiB |
| `SPINNERET_MAX_WATCHERS` | `20000` | Concurrent `WatchConfig` long polls per instance; beyond it callers get `resource_exhausted` |

### Proxy health checks

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` | URL fetched *through* each proxy. On a host without internet access point it at something reachable, e.g. `http://mocktarget:9090/healthz` |
| `SPINNERET_PROXY_CHECK_INTERVAL` | `60s` | Check period (minimum 1s) |
| `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s` | Per-proxy timeout (minimum 100ms) |
| `SPINNERET_PROXY_EXIT_IP_URL` | empty | URL that returns the caller's IP; used to record each proxy's exit IP |
| `SPINNERET_GEOIP_DB` | empty | Path to a MaxMind DB used to fill empty proxy regions from the exit IP |

### Retention

Retention applies to the partitioned PostgreSQL tables; partitions older than the period are dropped by the
hourly `partition_manager` job. Each value must be at least `24h`.

| Variable | Default |
| --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h` (30 d) |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h` (30 d) |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h` (180 d) |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h` (365 d) |
| `SPINNERET_RETENTION_AUDIT` | `8760h` (365 d) |

### Observability

| Variable | Default | Meaning |
| --- | --- | --- |
| `SPINNERET_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SPINNERET_LOG_FORMAT` | `json` | `json` or `text` |
| `SPINNERET_METRICS_ADDR` | empty | Empty serves `/metrics` on the main listener. Set e.g. `:9091` to serve it on a separate, non-public listener |
| `SPINNERET_PPROF_ADDR` | empty | Empty disables profiling. Set e.g. `127.0.0.1:6060` to serve `/debug/pprof/` on its own listener for a profiling session. The endpoints are **unauthenticated** and expose heap contents and goroutine stacks: never publish the port (see [benchmarks.md](benchmarks.md)) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Enables OTLP tracing when set |

### Compose-only variables (`deploy/compose/.env`)

| Variable | Default | Meaning |
| --- | --- | --- |
| `PG_PASSWORD`, `CLICKHOUSE_PASSWORD` | generated | Database passwords; referenced by the service URLs |
| `SPINNERET_PORT` | `8080` | Host port of the load balancer |
| `SPINNERET_REPLICAS` | `2` | Number of `spinneret` replicas |
| `SPINNERET_ADMIN_USERNAME` / `SPINNERET_ADMIN_PASSWORD` | `admin` / generated | Used once by `init-admin` |
| `PROMETHEUS_PORT`, `MOCK_TARGET_PORT`, `MOCK_PROXY_PORT`, `EXAMPLE_PORT` | `9090`, `19090`, `19091`, `18000` | Host ports of the profile services |
| `EXAMPLE_TOKEN`, `LOADTEST_TOKEN` | empty | Node tokens for the example crawler and k6; written by `scripts/example-quickstart.sh` / printed by `spnr seed` |

---

## KEK management

Spinneret encrypts identity payloads, proxy URLs, secret versions and notification channel credentials with
envelope encryption: a random 32-byte **DEK** per record encrypts the data (AES-256-GCM), and the DEK is
wrapped with a **KEK** you provide. Only wrapped DEKs and ciphertext are stored; the KEK never touches the
database.

### Providing keys

```bash
# generate a key line
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek generate --id k1
# k1:kZ7s...base64...=
```

Give the server either a file:

```
SPINNERET_KEK_FILE=/run/secrets/kek        # one "id:base64" per line
```

or the keys inline (`SPINNERET_KEKS=k1:...,k2:...`). `SPINNERET_KEK_CURRENT` picks the key used to wrap new
DEKs; without it the last key listed is current. Every key listed can still *unwrap*, which is what makes
rotation online.

The Compose stack mounts `deploy/compose/secrets/kek.key` as the Docker secret `kek` at `/run/secrets/kek`.

### Rotating

1. Append a new key and make it current:

   ```bash
   docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek generate --id k2 \
     >> deploy/compose/secrets/kek.key
   ```

   (or set `SPINNERET_KEK_CURRENT=k2` explicitly; the last line is current by default).

2. Restart the instances so they load the new key list:

   ```bash
   docker compose -f deploy/compose/docker-compose.yml up -d --wait spinneret
   ```

3. Re-wrap every stored DEK with the current KEK. The job is resumable and runs on the leader instance;
   the command follows its progress and exits non-zero on errors:

   ```bash
   docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr kek rewrap
   ```

   The console does the same from **Secrets → KEK** (`SecretAdminService/StartKEKRewrap`, platform admins only).

4. When `spnr kek status` reports no records wrapped with the old key, remove that line from the file and
   restart.

### Backing up

`kek.key` is the single most important file in the deployment. Store it in a secret manager or offline
backup, separate from the database dumps. **A database backup without the KEK is unrecoverable** for every
encrypted field. Keep old keys until `spnr kek status` shows zero records wrapped with them, and keep the
backup of a key at least as long as any backup of data encrypted with it.

---

## TLS and reverse proxies

The server can terminate TLS itself (`SPINNERET_TLS_CERT_FILE` + `SPINNERET_TLS_KEY_FILE`), but the usual
setup is a reverse proxy. The Compose stack ships Caddy as the in-network load balancer
([`deploy/compose/config/Caddyfile`](../deploy/compose/config/Caddyfile)); put your own edge proxy in front
of it, or replace it.

Whatever proxy you use, it must:

- **Pass `X-Forwarded-For` and `X-Forwarded-Proto`**, and the server must trust it:
  `SPINNERET_TRUSTED_PROXIES=10.0.0.0/8,172.16.0.0/12` (the Compose stack sets this). Without it every
  client IP is the proxy's, which breaks per-token IP allowlists, login throttling and audit records.
- **Not buffer long polls or SSE.** `WatchConfig` blocks up to 60 s and `/api/v1/events/stream` is an
  endless stream. In Caddy: `flush_interval -1`; in nginx: `proxy_buffering off` with
  `proxy_read_timeout 120s`.
- **Use readiness, not liveness, for health checks**: probe `/readyz` every ~2 s. A draining instance
  answers `503 {"status":"draining"}` for the first quarter of `SPINNERET_SHUTDOWN_TIMEOUT` (max 5 s)
  *before* it closes its listener, which is what lets a rolling restart happen without dropped requests.
  `/healthz` only says the process is alive.
- **Buffer request bodies if you want POST retries.** All RPCs are POSTs; a proxy can only replay them onto
  another upstream if it has the body. Caddy's `request_buffers 128KiB` covers every node and console call;
  larger bodies (identity/proxy imports) stream and are not retried.
- **Speak HTTP/2 end to end if clients use gRPC.** Connect JSON works over HTTP/1.1; the Go SDK's `UseGRPC`
  mode needs h2c (plain) or HTTP/2 over TLS.

Minimal nginx location block:

```nginx
location / {
    proxy_pass         http://spinneret_upstream;
    proxy_http_version 1.1;
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
    proxy_buffering    off;            # long polls and SSE
    proxy_read_timeout 120s;
}
```

With TLS terminated at the edge, set `SPINNERET_COOKIE_SECURE=auto` (the default) so session cookies are
marked `Secure` based on `X-Forwarded-Proto`, or `true` to force it.

---

## Scaling

**Server replicas.** Instances are stateless; scale with `SPINNERET_REPLICAS` (Compose) or your
orchestrator. Everything coordinates through Redis and PostgreSQL: stream shard ownership, job leader
election (PostgreSQL advisory locks) and catalog/config invalidation (Redis Pub/Sub).

```bash
SPINNERET_REPLICAS=4 docker compose -f deploy/compose/docker-compose.yml up -d --wait
```

**Roles.** `SPINNERET_ROLE=api` serves requests and runs no background jobs; `SPINNERET_ROLE=worker` runs
the report pipeline, breaker evaluation, lease reaping, proxy health checks, alert evaluation, hot-state
snapshots and partition maintenance, and serves no traffic. Split them when report processing and request
serving have different scaling curves; keep **at least two workers** so a worker failure does not stall the
pipeline. With `all` (default) every instance does both.

**Report shards.** `SPINNERET_REPORT_SHARDS` (default 16) bounds how many workers can process reports in
parallel — one shard has exactly one owner. As a rule of thumb keep at least 2–4 shards per worker
instance. Watch `spinneret_stream_pending{shard}` and `spinneret_report_lag_seconds`: steadily growing
pending entries mean the workers, not the shards, are the bottleneck; evenly spread pending entries with
idle CPU mean too few shards.

### Changing the shard count

Lease IDs encode the shard a report must go to, so changing the count **invalidates in-flight leases**:

1. Stop the node traffic, or accept that leases issued before the change report into a shard nobody owns.
2. Wait for the longest lease TTL (default 2 minutes, `max_lease_lifetime` at most 30 minutes) so all
   outstanding leases expired, and let the stream drain (`spinneret_stream_pending` at zero).
3. Change `SPINNERET_REPORT_SHARDS` and restart every instance at once — do not run a mixed fleet.

**Database.** PostgreSQL is not on the acquire/report hot path; it takes the aggregate writes, the catalog
and admin traffic. Raise `SPINNERET_DATABASE_MAX_CONNS` with the replica count, keeping
`max_connections` on the server above `replicas × max_conns + headroom`.

**Redis.** One instance is enough for the design targets; it is the bottleneck for acquire throughput.
`SPINNERET_REDIS_ADDRS` enables Redis Cluster mode (keys are hash-tagged per namespace). Never enable key
eviction: the Compose stack sets `maxmemory-policy noeviction`, and losing hot-state keys silently changes
scheduling decisions.

**Design targets** (v0.1, measured with the [k6 scenarios](../test/load/README.md)): acquire p99 < 5 ms
server-side and ≥ 5 000 acquires/s per instance, ≥ 20 000 reports/s per instance, report → state update
p99 < 200 ms, config change visible in < 1 s, ≥ 10 000 concurrent long polls per instance.

---

## Upgrades and migrations

Migrations are embedded in the binary and applied by `spnr migrate up`, serialized with an advisory lock.
The Compose stack runs them in the one-shot `migrate` service that the server depends on, so the ordinary
upgrade is:

```bash
git pull
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait
```

Compose rebuilds the image, re-runs `migrate` and restarts the replicas one after another. Each replica
drains first (`/readyz` → `draining`, keep-alives closed, in-flight requests finished) while the load
balancer's 2-second readiness check moves traffic to the other replica.

Checklist for a production upgrade:

1. **Back up first** — at minimum a PostgreSQL dump (see [Backups](#backups)).
2. `docker compose ... exec spinneret spnr migrate status` before and after; it prints the applied and the
   embedded schema version.
3. Watch `/readyz` on each replica and `spinneret_stream_pending` while the workers restart.
4. Roll back a migration only if you must: `spnr migrate down --to N` **drops tables and data**. Restoring a
   backup is usually the safer path.

Zero-downtime notes: with two or more replicas and a readiness-checking proxy, node clients see no failed
requests apart from in-flight POSTs on a replica being stopped, which SDK clients retry (`unavailable`).
Node API and SDK wire formats are forward compatible — unknown JSON fields are ignored in both directions —
so nodes do not have to be upgraded in lockstep with the server.

---

## Backups

| What | How | Why |
| --- | --- | --- |
| **KEK** (`deploy/compose/secrets/kek.key`) | Copy to a secret manager / offline storage before first use, and after every rotation | Without it, encrypted payloads, proxy URLs and secrets are unrecoverable |
| **PostgreSQL** | `pg_dump -Fc` nightly, or streaming replication / PITR | The source of truth: catalog, identities, policies, secrets, users, audit |
| **Valkey/Redis** | AOF (`appendonly yes`, `appendfsync everysec`, already set) plus a periodic copy of the volume | Speeds up recovery; not strictly required — see below |
| **ClickHouse** | Only if you need historical raw events beyond `SPINNERET_CLICKHOUSE_TTL_DAYS` | Analytics only, derived from reports |

```bash
# PostgreSQL dump from the Compose stack
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  pg_dump -U spinneret -Fc spinneret > spinneret-$(date -u +%Y%m%d).dump

# restore into an empty database
docker compose -f deploy/compose/docker-compose.yml exec -T postgres \
  pg_restore -U spinneret -d spinneret --clean --if-exists < spinneret-20260917.dump
```

**Redis is rebuildable.** The hot state is derived from PostgreSQL; if you lose it, rebuild instead of
restoring:

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret spnr rebuild
```

This deletes the hot-state epoch and re-materializes every site from PostgreSQL (identities, proxies,
health scores restored from the periodic `hot_state_snapshots`). API instances report not-ready
(`rebuilding`) until it finishes. `spnr rebuild --tenant t --namespace n --site s` re-materializes a single
site instead, without the fleet-wide downtime.

What a Redis loss does destroy and no rebuild brings back: in-flight leases (nodes get `lease_unknown` for
their reports and simply acquire again), reports still queued in the stream shards, the current breaker
windows, and console sessions — everyone signs in again.

Test your restore path: a PostgreSQL dump plus the matching KEK, restored into an empty stack, followed by
`spnr rebuild`, must bring the deployment back.

---

## Security hardening checklist

**Network**

- [ ] Do not publish PostgreSQL, Valkey or ClickHouse ports to the host or the internet (the Compose file
      does not).
- [ ] Terminate TLS at the edge, redirect HTTP to HTTPS, and set `SPINNERET_TRUSTED_PROXIES` to the proxy's
      CIDRs so client IPs are real.
- [ ] Serve `/metrics` on a separate listener (`SPINNERET_METRICS_ADDR=:9091`) reachable only from your
      monitoring network — on the main listener it is unauthenticated.
- [ ] Keep `SPINNERET_ALLOWED_ORIGINS` empty in production (it exists for local console development).

**Tokens and accounts**

- [ ] Give node tokens the narrowest scopes that work: `lease:acquire:<site>`, `report:write:<site>`,
      `config:read:<group>`, and only the `secret:read:<ns>/<path glob>` a node truly needs. Avoid `admin`.
- [ ] Set an expiry (`--expires 720h`) and rotate tokens; restrict them to source CIDRs and a rate limit
      where the console allows it.
- [ ] Console users get the least role that works (`viewer` < `operator` < `admin` < `owner`) and bindings
      restricted to a namespace, or to individual sites.
- [ ] Platform administrators (`is_platform_admin`) bypass every permission check — create as few as
      possible.
- [ ] Change the bootstrap `admin` password after the first login; a password change or reset invalidates
      every other session of that user.

**Secrets**

- [ ] Keep the KEK out of the repository, out of the image (the build context excludes it) and out of
      database backups; rotate it on a schedule and after any suspected exposure.
- [ ] Reference secrets from configs (`${secret:path}`) and from identity payloads (`secret_ref` fields)
      instead of pasting values; every read is audited.
- [ ] `secret:reveal` (console) and `secret:read` (tokens) are separate permissions — grant reveal to
      humans only.
- [ ] Never log lease credentials or proxy URLs; the SDKs keep them out of `repr`/`String()` and the server
      never logs payload fields.

**Browser**

- [ ] The console is served with `Content-Security-Policy: default-src 'self'` (hashed inline bootstrap
      script only), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and
      `Referrer-Policy: same-origin`. Keep them if you put your own proxy in front — do not add permissive
      CSP or CORS headers there.
- [ ] Session cookies are `HttpOnly`, `SameSite=Strict`, and `Secure` under TLS; unsafe cookie-authenticated
      requests additionally require `X-Spinneret-CSRF: 1`.

**Operations**

- [ ] Keep the audit log (`SPINNERET_RETENTION_AUDIT`, default 365 days) and review `secret.read` entries.
- [ ] Run the image as the non-root user it ships with (distroless `nonroot`), read-only filesystem where
      your platform supports it.
- [ ] Alert on the signals in [Monitoring](operations.md#monitoring), especially breakers opening and
      report backlog.

---

## Troubleshooting

**`up` fails with `PG_PASSWORD: run scripts/compose-init.sh`** — `deploy/compose/.env` is missing. Run
`./scripts/compose-init.sh`.

**`/readyz` returns `503` with a failing check**

| Check | Meaning |
| --- | --- |
| `postgres` | The pool cannot reach PostgreSQL — check credentials, `max_connections`, network |
| `redis` | Redis/Valkey unreachable, or the prefix points at a database that was flushed |
| `hotstate` | A rebuild is in progress (`rebuilding`), or the hot-state epoch is missing — run `spnr rebuild` |
| `catalog` | The namespace snapshot could not be loaded from PostgreSQL |

**`503 {"status":"draining"}`** — expected during a restart, for up to a quarter of
`SPINNERET_SHUTDOWN_TIMEOUT` (max 5 s). Your proxy should already have removed the instance.

**Nodes get `429 no_identity_available`** — no identity passes the availability rules right now:
all leased (`max_concurrent_leases`), in reuse interval, cooling down, out of quota, or banned. The
`Spinneret-Retry-After-Ms` header says how long the server thinks it will take. Check the identity states
on the Identities page and the heatmap for the endpoint group.

**Nodes get `429 no_proxy_available`** — the rotation policy requires a proxy and none is usable. The most
common cause is the health checker marking proxies `dead` because
`SPINNERET_PROXY_CHECK_URL` (`http://example.com/` by default) is not reachable from the
server. On an isolated host point it somewhere reachable, e.g. `http://mocktarget:9090/healthz`, and
restart the `spinneret` service.

**Nodes get `503 circuit_open` / `503 site_paused`** — the endpoint group breaker is open (it closes after
`open_duration` if probes succeed) or the site switch is off. Both are visible and reversible on the
Breakers page.

**Nodes get `503 rebuilding`** — the hot state is being rebuilt; wait for it, or check whether an
unintended `spnr rebuild` is running.

**Reports pile up** — `spinneret_stream_pending{shard}` grows and `spinneret_report_lag_seconds` rises.
Check that worker-capable instances are running (`SPINNERET_ROLE`), that `spinneret_stream_owned_shards`
across instances sums to `SPINNERET_REPORT_SHARDS`, and that PostgreSQL is not saturated.

**Login fails with `login_throttled`** — 5 failures per username or 20 per IP in 15 minutes. Wait it out;
if every user is throttled from one IP, `SPINNERET_TRUSTED_PROXIES` is probably unset and all requests
appear to come from the proxy.

**Console shows CSRF or session errors** — the request lacked `X-Spinneret-CSRF: 1` (a proxy stripping
headers) or the cookie was not `Secure` on an HTTPS origin. Check `SPINNERET_COOKIE_SECURE` and that
`X-Forwarded-Proto` reaches the server.

**gRPC clients fail, JSON works** — the proxy is not doing HTTP/2 end to end. Enable h2c or HTTP/2 to the
upstream, or use the Connect JSON protocol.

Logs: `docker compose -f deploy/compose/docker-compose.yml logs -f spinneret`. Raise detail with
`SPINNERET_LOG_LEVEL=debug`; secrets, payloads, proxy credentials and tokens are never logged.
