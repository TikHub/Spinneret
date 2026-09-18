# Troubleshooting

**Symptom → cause → fix for the failures that actually happen in a Spinneret deployment, plus the complete error-reason table, how to read the server logs, and how to collect a diagnostic bundle for a bug report.**

[中文](../zh/18-troubleshooting.md)

---

## Contents

- [Before you start](#before-you-start)
- [The stack will not start](#the-stack-will-not-start)
- [The console will not load or will not sign in](#the-console-will-not-load-or-will-not-sign-in)
- [A node gets no_identity_available](#a-node-gets-no_identity_available)
- [A node gets no_proxy_available](#a-node-gets-no_proxy_available)
- [A node gets circuit_open](#a-node-gets-circuit_open)
- [A node gets scope_missing or permission_denied](#a-node-gets-scope_missing-or-permission_denied)
- [Reports are accepted but nothing changes](#reports-are-accepted-but-nothing-changes)
- [The pool drains and never recovers](#the-pool-drains-and-never-recovers)
- [Identities are banned that should not be](#identities-are-banned-that-should-not-be)
- [The request explorer returns an error or is empty](#the-request-explorer-returns-an-error-or-is-empty)
- [Config changes do not reach nodes](#config-changes-do-not-reach-nodes)
- [A secret cannot be read](#a-secret-cannot-be-read)
- [Latency is high or throughput collapses](#latency-is-high-or-throughput-collapses)
- [Redis, PostgreSQL or ClickHouse is out of memory or disk](#redis-postgresql-or-clickhouse-is-out-of-memory-or-disk)
- [After an upgrade](#after-an-upgrade)
- [After a restore](#after-a-restore)
- [The complete error-reason table](#the-complete-error-reason-table)
- [Reading the server logs](#reading-the-server-logs)
- [Turning on debug logging safely](#turning-on-debug-logging-safely)
- [Collecting a diagnostic bundle](#collecting-a-diagnostic-bundle)

---

## Before you start

Three commands answer most questions before you read any further. Every example on this page
assumes you are in the repository root and that `COMPOSE` is set:

```bash
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"
```

```bash
# 1. Which containers are up, and which are healthy?
$COMPOSE ps

# 2. Which dependency is a server instance unhappy about?
curl -s http://localhost:${SPINNERET_PORT:-8080}/readyz | python3 -m json.tool

# 3. Is the configuration the server actually loaded the one you think it is?
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate config check
```

`/readyz` is the single most useful endpoint in the system. It answers with the state of every
dependency of the instance that served the request:

```json
{
  "status": "ok",
  "checks": {"postgres": "ok", "redis": "ok", "catalog": "ok", "hotstate": "ok"}
}
```

| Check | Fails when | Detail string |
| --- | --- | --- |
| `postgres` | `Ping` on the pool fails | `unreachable` |
| `redis` | `PING` fails | `unreachable` |
| `catalog` | the in-memory catalog has never loaded | `not loaded` |
| `hotstate` | a worker instance has not finished `EnsureBuilt` | `building` |
| `hotstate` | the Redis epoch key is missing | `epoch missing (rebuild pending)` |
| `hotstate` | the epoch lookup itself failed | `unreachable` |

The whole document returns HTTP 503 when any check fails (`"status": "unavailable"`), and
`{"status": "draining"}` with 503 while the instance is shutting down. `/healthz` is liveness
only: it answers `{"status":"ok"}` as long as the process can serve HTTP, and says nothing
about dependencies.

**Note.** With more than one replica, `curl` through the load balancer will not find the bad one.
Caddy polls `/readyz` on every replica every 2 s and parks an unready instance, so the answer you
get back is always from a healthy one. To identify a failing replica, read
`$COMPOSE logs spinneret` for `readiness: … failed` and group the records by the `instance` field
(both are documented below), or use `$COMPOSE ps` plus `docker inspect` to read each container's
own health log.

**What `deploy/compose/.env` can change, and what it cannot.** Only the variables that the
`x-spinneret-env` anchor of `deploy/compose/docker-compose.yml` maps into the container can be set
from `.env`: `SPINNERET_LOG_LEVEL`, `SPINNERET_COOKIE_SECURE`, `SPINNERET_DATABASE_MAX_CONNS`,
`SPINNERET_REPORT_SHARDS`, `SPINNERET_REPORT_DEDUP_TTL` and `SPINNERET_PROXY_CHECK_URL` — plus
`SPINNERET_PORT` and `SPINNERET_REPLICAS`, which Compose itself reads, and the database passwords.
Every other `SPINNERET_*` variable named on this page (`SPINNERET_UI_ENABLED`,
`SPINNERET_ALLOWED_ORIGINS`, `SPINNERET_MAX_WATCHERS`, `SPINNERET_STREAM_MAXLEN`, the
`SPINNERET_RETENTION_*` set, `SPINNERET_CLICKHOUSE_TTL_DAYS`, `SPINNERET_RECORD_COOLDOWN_EVENTS`,
`SPINNERET_ACQUIRE_FLEET_INFLIGHT`, `SPINNERET_PPROF_ADDR`, …) needs a line added to that anchor
first; putting it in `.env` alone has no effect. `SPINNERET_TRUSTED_PROXIES` is a special case: the
anchor sets it to the hardcoded literal `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`, not to a
`${...}` reference, so it cannot be overridden from `.env` at all — widening it means editing
`docker-compose.yml`.

---

## The stack will not start

### `docker compose up` refuses to start at all

```text
error while interpolating services.spinneret.environment...: required variable PG_PASSWORD is missing a value: run scripts/compose-init.sh
```

The shared env anchor marks `PG_PASSWORD` and `CLICKHOUSE_PASSWORD` as required, with exactly the
message above. `SPINNERET_ADMIN_PASSWORD` is also required, but only by the `init-admin` service of
the `init` profile, and it fails with a different message (`set SPINNERET_ADMIN_PASSWORD in .env`)
when you run that profile. Either way the cause is the same: `deploy/compose/.env` does not exist
yet. Run the initializer once — it is safe to re-run and keeps files that already exist:

```bash
./scripts/compose-init.sh
```

It writes `deploy/compose/.env` from `.env.example` with random passwords and
`deploy/compose/secrets/kek.key` with a fresh key-encryption key, mode `0644` (the container
runs as the distroless `nonroot` user and must be able to read the bind-mounted secret).

### `postgres` never becomes healthy

Healthcheck: `pg_isready -U spinneret -d spinneret`, every 5 s, 3 s timeout, 30 retries.

| Cause | How to tell | Fix |
| --- | --- | --- |
| `PG_PASSWORD` was changed after the first start | `password authentication failed for user "spinneret"` in `$COMPOSE logs postgres` | Restore the old password in `.env`, or change it inside the database, or delete the `pgdata` volume and start over (this destroys all data) |
| Disk full | `could not extend file`, `No space left on device` | Free space on the Docker data root, then restart |
| The volume was written by a newer PostgreSQL | `database files are incompatible with server` | Keep the image version that created the volume, or dump and restore |

### `valkey` never becomes healthy

Healthcheck: `valkey-cli ping`, every 5 s, 3 s timeout, 30 retries.

Valkey runs with `--maxmemory-policy noeviction` on purpose: Spinneret's hot state must never be
evicted silently. If the container is being OOM-killed (`$COMPOSE ps` shows it restarting, and
`docker inspect` reports exit code 137), see
[Redis, PostgreSQL or ClickHouse is out of memory or disk](#redis-postgresql-or-clickhouse-is-out-of-memory-or-disk).
An AOF that cannot be loaded (`Bad file format reading the append only file`) also keeps the
container in a restart loop; because the AOF is the durability mechanism, recovering from it is
a restore, not a repair — see [After a restore](#after-a-restore).

### `clickhouse` never becomes healthy

Healthcheck: `wget -qO- http://127.0.0.1:8123/ping`, every 5 s, 3 s timeout, 40 retries — it is
allowed the longest start window of the stack.

The healthcheck only asks `/ping`, and that keeps answering while inserts are failing — so an
unhealthy `clickhouse` means the server is not running at all, not that it is under pressure. The
two causes: the container was OOM-killed by the host (`docker inspect` reports exit code 137), or a
value in `deploy/compose/config/` is refused at startup. Lowering `background_pool_size` to 4, for
example, trips a MergeTree sanity check and the server exits with `BAD_ARGUMENTS` (exit 36). Memory
pressure that leaves the container *healthy* while every INSERT fails is a different failure — see
[Redis, PostgreSQL or ClickHouse is out of memory or disk](#redis-postgresql-or-clickhouse-is-out-of-memory-or-disk).

ClickHouse is optional *to the binary*. With `SPINNERET_CLICKHOUSE_URL` unset the server logs
`clickhouse disabled: raw report events are not stored` and starts normally; everything works
except the request explorer. In the bundled Compose stack the `spinneret` service still declares
`depends_on: clickhouse: service_healthy`, so running without ClickHouse there also means removing
that `depends_on` entry (or the whole service) from `deploy/compose/docker-compose.yml`.

### `migrate` exits non-zero

`migrate` is a one-shot service (`restart: "no"`, healthcheck disabled) that runs
`spnr migrate up` and must complete successfully before `spinneret` starts. If it fails, the
server never starts either.

```bash
$COMPOSE logs migrate
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status
```

`migrate status` prints the applied version and the version embedded in the binary. A failure
here is almost always an unreachable or misconfigured PostgreSQL — `spnr migrate` needs only
`SPINNERET_DATABASE_URL`. Concurrent runs are serialized with a PostgreSQL advisory lock, so a
second `migrate` container is not the problem.

### `spinneret` starts and immediately exits

The binary prints one of two prefixes and exits with status 1:

```text
spinneret-server: invalid configuration:
spinneret-server: startup failed: <what it was doing>
```

| Message fragment | Meaning | Fix |
| --- | --- | --- |
| `invalid configuration:` | one or more `SPINNERET_*` values failed validation; every problem is listed | Fix `.env`; see [Configuration reference](./03-configuration.md) |
| `connect postgresql` | the pool could not be opened | Check `SPINNERET_DATABASE_URL`, the `postgres` container and its password |
| `database schema is at version <n>, binary expects <m>: run spnr migrate up` | migrations are pending and `--migrate` was not given | Let the `migrate` service run, or start the server with `--migrate` |
| `ensure partitions` | the partition maintainer could not create the current partitions | Check PostgreSQL privileges and disk space |
| `connect redis` | Redis/Valkey is unreachable | Check `SPINNERET_REDIS_URL` / `SPINNERET_REDIS_ADDRS` |
| `connect clickhouse` / `migrate clickhouse` | ClickHouse is configured but not usable | Fix ClickHouse, or unset `SPINNERET_CLICKHOUSE_URL` to start without it |
| `load key-encryption keys` | `SPINNERET_KEK_FILE` is unreadable, or the key material is malformed | Check the secret file exists and is mode `0644`; `spnr kek generate` prints a valid line |
| `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | the KEK does not match the one this database was initialized with | Restore the original KEK; see [Secret vault](./10-secrets.md) |
| `listen on SPINNERET_HTTP_ADDR` | the address is taken or not bindable | Change `SPINNERET_HTTP_ADDR`, or free the port |
| `load TLS certificate` | `SPINNERET_TLS_CERT_FILE` / `_KEY_FILE` cannot be parsed | Fix the key pair; both must be set together |
| `SPINNERET_DATABASE_MAX_CONNS=… is too small for a worker instance` | a worker role holds one pooled connection per leader job while it leads | Raise the pool; the message states the minimum |

**Note.** A database schema *newer* than the binary is accepted with the warning
`database schema is newer than this binary` — that is a rolling upgrade in progress, not an
error.

### `spinneret` is up but never healthy

The image healthcheck is `spnr healthcheck --url http://127.0.0.1:8080/readyz`, every 10 s, 3 s
timeout, 20 s start period, 3 retries. A container that is running but unhealthy means
readiness is failing; read it directly:

```bash
$COMPOSE logs --tail 100 spinneret
curl -s http://localhost:${SPINNERET_PORT:-8080}/readyz
```

The most common cause on a first start is `hotstate: epoch missing (rebuild pending)` on an
`api`-role instance while no `worker`-role instance is running to build it. With the default
`SPINNERET_ROLE=all` every instance can build the hot state itself.

### `lb` never becomes healthy, or answers 503

Caddy's own healthcheck is `wget -qO- http://127.0.0.1:8080/healthz`, every 5 s. Caddy will not
start until at least one `spinneret` instance is healthy (`depends_on: service_healthy`), so an
unhealthy `lb` usually means an unhealthy `spinneret`. Fix that first.

Caddy answers `503` with no upstream when every replica is parked. It ejects a replica after a
single failed dial (`max_fails 1`) for `fail_duration 2s`, and it polls `/readyz` every 2 s, so
an instance that reports `draining` or `unavailable` leaves the pool within about two seconds.
If you see a burst of 503s under load, the replicas are failing readiness, not Caddy — check
`/readyz` on each of them.

### `init-admin` does nothing

`init-admin` lives in the `init` profile and only runs when you ask for it:

```bash
$COMPOSE --profile init run --rm init-admin
```

It runs `spnr admin init --username "$SPINNERET_ADMIN_USERNAME" --password-env SPINNERET_ADMIN_PASSWORD`.
The command is idempotent: when a platform administrator already exists it prints
`already initialized` and exits 0. If you have lost the administrator password, that message is
what you will see — creating a second administrator with this command is not possible. The way back
in is another signed-in user who holds `user:write` (the `owner` role) resetting the password on the
`Users` page; a platform administrator can only be managed by another platform administrator, so if
the lost account is the only one, the reset is a manual database operation. See
[Tenants, users and tokens](./11-access-control.md).

---

## The console will not load or will not sign in

| Symptom | Likely cause | Distinguish | Fix |
| --- | --- | --- | --- |
| Browser shows nothing / connection refused | `lb` is not running, or the host port is taken | `$COMPOSE ps lb` | Start the stack; change `SPINNERET_PORT` |
| HTTP 404 on every console route | `SPINNERET_UI_ENABLED=false` — the static console is not mounted | `spnr config check` shows the value | Set it back to `true` and restart |
| HTTP 503 on every route | no healthy replica behind Caddy | `curl /readyz` | See [The stack will not start](#the-stack-will-not-start) |
| Page loads, every API call fails with `unauthenticated` | the session cookie is not being stored | Browser devtools → Application → Cookies | See the cookie note below |
| "Invalid username or password." | wrong credentials, or the account is disabled | Audit log, `Users` page | Reset the password |
| "Too many failed attempts. Try again in …" | login throttle | — | Wait; see below |
| Signed in, but every page says "You don't have permission for this action" | the user's role bindings do not cover the selected scope | `Users` page → role bindings | Grant the role in the right tenant/namespace |
| Calls fail with `csrf_missing` | a proxy strips the `X-Spinneret-CSRF` header | Reproduce against the instance directly | Fix the proxy |

**The cookie.** The session cookie is `spinneret_session`: `HttpOnly`, `SameSite=Strict`,
`Path=/`, `Max-Age = SPINNERET_SESSION_TTL` (default `12h`). Its `Secure` attribute follows
`SPINNERET_COOKIE_SECURE`:

- `auto` (default) — `Secure` when the request arrived over TLS, or through a reverse proxy
  that sent `X-Forwarded-Proto: https` **and** whose address is in `SPINNERET_TRUSTED_PROXIES`.
- `true` — always `Secure`. Sign-in over plain HTTP then silently fails, because the browser
  refuses to store the cookie.
- `false` — never `Secure`. Plain-HTTP intranet only.

If sign-in "succeeds" and the console immediately bounces back to the sign-in page, this is
almost always the cause: either `SPINNERET_COOKIE_SECURE=true` behind plain HTTP, or `auto`
behind a TLS-terminating proxy whose IP is not trusted so the server thinks the request was
plain HTTP. Add the proxy's network to `SPINNERET_TRUSTED_PROXIES` — in the Compose stack that
means editing the literal in the `x-spinneret-env` anchor, not `.env`.

**The CSRF header.** Every cookie-authenticated unsafe request must carry
`X-Spinneret-CSRF: 1`. The console sends it. Anything in front of the server that strips
unknown request headers will produce `csrf_missing` (HTTP 403) on every write.

**The login throttle.** Counted in Redis: **5** failed attempts per username and **20** per
client IP within a **15-minute** window. Both return `login_throttled` with a
`Spinneret-Retry-After-Ms` hint. The username counter is cleared by an administrative password
reset; the IP counter is released on a successful sign-in from that address.

**Cross-origin.** If you serve the console from a different origin than the API, that origin
must be listed in `SPINNERET_ALLOWED_ORIGINS`. Otherwise the browser blocks the call and the
console shows a network error with nothing in the server log.

---

## A node gets `no_identity_available`

Connect code `resource_exhausted`, HTTP 429, with `Spinneret-Retry-After-Ms` clamped to
50 ms – 60 s. It means `acquire.lua` returned `EXHAUSTED`: no candidate in the endpoint group's
ready queue passed every filter within the wait budget (`wait_ms`, at most 5 s).

Causes, most common first:

1. **Every identity is cooling down.** The normal, healthy case: the site is being crawled
   faster than the rotation policy allows. The retry hint is the time until the earliest
   candidate becomes ready.
2. **Identities are leased.** With `max_concurrent_leases: 1` an identity in use is invisible to
   every other acquire. Compare the overview page's **Available** tile and its low watermark card
   with the number of `active` identities on the `Identities` page: available far below active means
   the pool is busy, not missing. Identity counts are not exported as Prometheus metrics — see
   [Observability and alerting](./12-observability.md).
3. **Identities are banned, quarantined, expired or disabled.** A signal rule may be banning far
   more than you intended — see [Identities are banned that should not be](#identities-are-banned-that-should-not-be).
4. **The account behind the identity is banned or cooling down.** Account state is checked on
   every candidate; one banned account removes all of its identities at once.
5. **The endpoint group has no identities of a matching type**, or a filter in the rotation
   policy (tags, quotas, region) excludes all of them.
6. **The site has no identities at all** — a fresh deployment, or an import that failed.
7. **The hot state does not reflect PostgreSQL.** Rare, and the only case where the console and
   the scheduler disagree: the console reads PostgreSQL, the scheduler reads Redis.

How to distinguish:

```bash
# The console answers 1-6 directly:
#   Identities  → filter by site, endpoint group and state
#   Heatmap     → the cooldown heatmap shows whether the pool is cooling or empty
#   Policies    → the rotation policy bound to this site/client/endpoint group
```

```promql
# How often, and against which endpoint group?
rate(spinneret_acquire_total{result="exhausted", site="example-site"}[1m])
# Is it steady starvation or bursts? Compare with the acquires that do succeed.
rate(spinneret_acquire_total{result="ok", site="example-site"}[1m])
```

Whether available identities drop to zero periodically (cooldown) or stay at zero (exhaustion) is a
question for the overview page and the cooldown heatmap, not for Prometheus: the identity gauges are
reserved and export no series.

Fixes, in the order they usually apply: add identities; raise `max_concurrent_leases` (it is
also the biggest throughput lever, see [Performance and tuning](./17-performance.md)); relax the
cooldown in the rotation policy; widen the endpoint group's identity type filter; unban the
identities that were banned wrongly. If the console shows a healthy pool and the scheduler still
says exhausted, re-materialize the site's hot state:

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  rebuild --tenant default --namespace default --site example-site
```

**Note.** Nodes should treat this as backpressure, not as an error: honour
`Spinneret-Retry-After-Ms` and do not retry tighter than the hint. A node that hammers a drained
pool makes the drain worse — a failing acquire costs roughly five times as many Redis commands
as a succeeding one.

**Not the same as `overloaded`.** `overloaded` means admission control shed the call inside the
server before it reached Redis; it says nothing about the pool. The two never mask each other: a shed
only surfaces as `overloaded` when *no* attempt in the wait ladder reached Redis. Once `acquire.lua`
has answered `EXHAUSTED` or `NO_PROXY`, that reason survives and the caller gets
`no_identity_available` or `no_proxy_available`.

---

## A node gets `no_proxy_available`

Connect code `resource_exhausted`, HTTP 429, with a retry hint. `acquire.lua` found an identity
but could not attach a proxy: the site or endpoint group requires one, and none passed the
filters.

| Cause | Distinguish | Fix |
| --- | --- | --- |
| The health checker marked every proxy `dead` | `Proxies` page, or `spinneret_proxies{site,state}` | See below |
| Proxies are cooling down per site | Proxy detail → per-site cooldowns | Relax the action policy, or add proxies |
| The proxy filter (kind, region, provider, tags) matches nothing | The rotation policy's proxy selector | Widen the filter, or tag more proxies |
| The identity is bound to a proxy that is unusable | Identity detail → bound proxy | Unbind, or repair that proxy |
| Every proxy is at `max_concurrency` | `Proxies` page | Raise the limit (`0` means 1; an update accepts 1–100000), or add proxies |

The health-check trap is worth stating plainly. The checker fetches
`SPINNERET_PROXY_CHECK_URL` **through each proxy** every `SPINNERET_PROXY_CHECK_INTERVAL`
(default 60 s) with `SPINNERET_PROXY_CHECK_TIMEOUT` (default 10 s). The default URL is a
public one. On a host with no outbound internet access, every proxy fails its check and is
marked `dead` — including proxies that work perfectly for your actual target. Point the checker
at something reachable:

```bash
# deploy/compose/.env
SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz
```

Proxy states are `active`, `disabled`, `dead`, `banned`, `quarantined`, `retired`. Only `active`
proxies are assignable. See [Proxies](./07-proxies.md).

---

## A node gets `circuit_open`

Connect code `unavailable`, HTTP 503, with a retry hint (at least 1 ms; the breaker reports the
time left of the open window, and 60 s for an open with no deadline). The endpoint group's
circuit breaker is open — deliberately, because the breaker policy decided that requests to this
endpoint group are currently failing.

Work out which of the three it is on the `Breakers` page:

1. **Automatic open.** The breaker policy's thresholds were crossed. The breaker page shows the
   trigger and the window. This is the system doing its job: the fix is to make the underlying
   requests succeed, not to force the breaker closed.
2. **Manual open.** Someone opened it from the console or the API — the transition is recorded
   with trigger `manual` and an actor. A manual open with no duration stays open until it is
   closed manually.
3. **Half-open probing.** State `half_open`: a limited number of probe leases are let through
   and everything else still gets `circuit_open`. This is the recovery path; it resolves itself.

```promql
spinneret_breaker_state{site="example-site"}        # 0 closed, 1 half-open, 2 open
rate(spinneret_breaker_transitions_total[5m])       # label "to" = the target state
```

**`site_paused` is not the same thing.** If the whole site was paused (the site switch on the
`Breakers` page), every acquire fails with reason `site_paused`, code `unavailable`, and a fixed
30-second retry hint. Unpause the site.

See [Policies](./08-policies.md) for breaker policy fields and [Observability and
alerting](./12-observability.md) for alerting on breaker state.

---

## A node gets `scope_missing` or `permission_denied`

Both are Connect code `permission_denied`, HTTP 403. They are different failures:

| Reason | Raised when |
| --- | --- |
| `scope_missing` | An **API token**'s scopes do not grant the permission for the target resource, or the token is not valid for the namespace named in the request |
| `permission_denied` | A **console user**'s role bindings do not grant the permission for the target resource |

A node authenticates with a token, so a node gets `scope_missing`. The scopes a node needs:

| Call | Scope | Optional suffix |
| --- | --- | --- |
| `Acquire`, `AcquireBatch`, `Renew`, `Release` | `lease:acquire` | `:<site>` |
| `Report`, `ReportBatch` | `report:write` | `:<site>` |
| `GetConfig`, `WatchConfig` | `config:read` | `:<group glob>` |
| `GetSecret` | `secret:read:<namespace>/<path glob>` | required |

Check what the token actually has:

```bash
# The console: Access → Tokens → the token → its scopes, namespace and IP allowlist.
# A new token with the right scopes:
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate token create \
  --tenant default --namespace default --name crawler-01 \
  --scope lease:acquire --scope report:write --scope config:read --expires 720h
```

Three traps:

- **The suffix narrows, it does not widen.** `lease:acquire:search` grants nothing for site
  `detail`. A bare `lease:acquire` covers every site in the token's namespace.
- **A token belongs to exactly one namespace.** A request naming another namespace fails with
  `scope_missing` even when the scopes look right.
- **`ReportBatch` rejects per report.** A batch whose token lacks `report:write` for *one*
  report's site returns HTTP 200 with that report in `rejected[]`, reason `scope_missing` — the
  rest of the batch is accepted. A node that only checks the HTTP status will never notice.

`ip_not_allowed` (also `permission_denied`, HTTP 403) is a separate check: the token has an IP
allowlist and the client address is not in it. Behind a reverse proxy, the client address is
only taken from `X-Forwarded-For` when the immediate peer is inside
`SPINNERET_TRUSTED_PROXIES` — otherwise every node appears to come from the proxy's address. In the
Compose stack that variable is a literal in the `x-spinneret-env` anchor; widen it there, not in
`.env`.

See [Tenants, users and tokens](./11-access-control.md).

---

## Reports are accepted but nothing changes

`Report` returns success, the request explorer shows the report, and no identity is cooled down,
banned or scored. Check in this order:

1. **Shadow mode.** An action policy in mode `shadow` plans every action and applies none. State
   events are recorded with `shadow=true` so you can see exactly what *would* have happened. The
   `Policies` page shows the mode; `spinneret_actions_total{mode="shadow"}` counts them.
2. **No worker is running.** Reports are enqueued on a Redis stream by the API and processed by a
   worker. With `SPINNERET_ROLE=api` on every instance, nothing ever consumes the stream. The
   default role is `all`. Confirm with `spinneret_stream_owned_shards` (should sum to
   `SPINNERET_REPORT_SHARDS`, default 16) and a growing `spinneret_stream_pending{shard}`.
3. **The worker is behind.** `spinneret_report_lag_seconds` is the delay between receipt and
   processing. Seconds of lag under load is a capacity problem, not a correctness problem; see
   [Latency is high or throughput collapses](#latency-is-high-or-throughput-collapses).
4. **No signal rule matched.** The signal policy turns status, latency and markers into an
   outcome; if no rule matches, the outcome is the default and the action policy has nothing to
   do. Use the rule debugger on the `Policies` page with the exact report you sent.
5. **The report was a duplicate.** A `report_id` seen within `SPINNERET_REPORT_DEDUP_TTL`
   (default `1h`) is counted as duplicated and dropped. `spinneret_report_ingest_total{result="duplicated"}`
   counts them. Node SDKs generate a fresh `report_id` per attempt; a node that reuses one
   across retries silently discards everything after the first.
6. **The report was rejected inside a successful batch.** `ReportBatch` returns per-report
   results; check `rejected[]`. `spinneret_report_ingest_total{result="rejected"}` counts them.
7. **The report was late.** A report whose lease ended more than `SPINNERET_LATE_REPORT_WINDOW`
   ago (default `10m`) is not applied to the identity.
8. **The action policy bound to this endpoint group does nothing** for that outcome. Check the
   binding hierarchy on the `Policies` page — a binding at a more specific level replaces the
   one you edited, it does not merge with it.

---

## The pool drains and never recovers

The distinguishing symptom: the overview page's **Available** tile falls to zero and stays there,
with no traffic explaining it, and `Identities` in the console shows most of the pool as `active`.
The identities are *leased*, and nothing is ending the leases.

| Cause | Distinguish | Fix |
| --- | --- | --- |
| Nodes acquire and never release or report | `spinneret_lease_reaped_total{kind="abandoned"}` rising while `spinneret_report_ingest_total{result="accepted"}` stays flat — leases are ending through the reaper instead of through `Release`/`Report`. The metric has only the two kinds `expired` and `abandoned`; there is no `released` series on it | Fix the node: always `Release` or `Report` with `release: true`, in a `finally` |
| The report worker is stopped or stuck | `spinneret_stream_pending` growing, `spinneret_report_lag_seconds` growing | Restart the worker instances; see [Operations runbook](./16-operations.md) |
| Lease hashes were deleted from Redis by hand | Nothing else explains it | Rebuild, below |
| Leases are long and the pool is small | `lease_ttl` in the rotation policy (default 120 s, range 5 s–30 m) and `max_lease_lifetime` vs. pool size | Shorten the lease, or grow the pool |

The lease reaper runs every second on every instance and ends leases that expired or were
abandoned, so a pool that stays empty for more than a lease TTL is not a reaper problem.

**Warning.** Never delete live lease keys (`<prefix>:{s<siteKey>}:ls:*`) from Redis. The lease hash is what
decrements the identity's active-lease counter when the lease ends; deleting one leaks that
counter and the identity is *never* available again. This is not theoretical — doing it once
during load testing left 97,520 of 100,000 identities permanently leased and every subsequent
run collapsed into `resource_exhausted`. Trimming a report stream that still holds unprocessed
`release: true` reports has the same effect.

The repair is to clear the whole site prefix and re-materialize it from PostgreSQL. Every key of a
site lives under `<prefix>:{s<siteKey>}:`, where `<prefix>` is `SPINNERET_REDIS_PREFIX` (default
`sp`) and `<siteKey>` is the site's **numeric** key — not its name. The numeric key is the `site_key`
field of the server's log records (see [Reading the server logs](#reading-the-server-logs)). With the
default prefix and site key 12 the pattern is therefore `sp:{s12}:*`:

```bash
# 1. Count the keys first. A count of 0 means the pattern is wrong, not that the site is clean —
#    a mistyped pattern matches nothing and fails silently.
$COMPOSE exec -T valkey sh -c \
  "valkey-cli --scan --pattern 'sp:{s12}:*' --count 5000 > /tmp/k; wc -l < /tmp/k"

# 2. If the count is plausible, remove the site's hot state entirely.
$COMPOSE exec -T valkey sh -c "xargs -a /tmp/k -n 1000 valkey-cli unlink"

# 3. Rebuild it.
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate \
  rebuild --tenant default --namespace default --site example-site
```

`spnr rebuild --site` on its own does **not** reset runtime counters — by design, a rebuild must
not drop live leases. The unlink is what clears them. A rebuilt site is also cold: its ready
scores are spread uniformly over the next 60 s, and the first million or so acquires are less
efficient than a warm site's.

Without `--site`, `spnr rebuild` deletes the hot-state epoch and rebuilds every site; API
instances report not-ready (`epoch missing (rebuild pending)`) until it finishes, and Caddy takes
them out of the pool while it runs. Plan for that.

---

## Identities are banned that should not be

The identity state machine only moves on an action, and every action is recorded. Start from the
record, not from the policy.

1. Open `Identities` → the identity → its **State timeline**. Every transition names the action, the
   report that caused it, the rule that matched and the actor.
2. If the actor is `system`, a signal rule matched. Open `Policies` → the signal policy bound to
   that endpoint group, find the rule, and run the rule debugger against the offending report.
3. If the actor is a user, it was a manual operation.

The usual culprits:

- **A rule matches on an HTTP status that is not actually a ban.** `429` and `503` are rate
  limiting; classifying them as a ban burns the pool during any target-side incident.
- **A marker is too broad.** A marker that the node sets on any non-200 response will match
  transient failures.
- **A threshold counts across too long a window**, so a short outage accumulates enough
  consecutive failures to ban everything.
- **The action's scope is wrong.** An action scoped to the account bans every identity of that
  account; scoped to the proxy it bans everything sharing the proxy.

Fixes:

- **Revert actions.** The `Identities` page has a **Revert actions** dialog. It is not driven by
  ticking identities: you describe the damage and it finds them. A **time range is required** (an
  empty start is rejected, so a revert never silently covers the whole history), and you may narrow
  it further by site, policy ID, rule name and which of `ban`, `quarantine`, `expire`, `cooldown` to
  undo — selecting none undoes all four. Run it as a **dry run** first: it lists the identities that
  would be affected without changing anything. Only events recorded by the system
  (`shadow=false`, actor `system`) are touched. Reverted identities go back to `pending`, not to
  `active`; reverted cooldowns are cleared. `reset_failures` and `reset_health` are separate
  opt-ins that also clear the failure streak and restore the baseline health score.
- **Shadow mode first.** Republish the corrected signal or action policy in mode `shadow`, let
  it run against live traffic, and compare `spinneret_actions_total{mode="shadow"}` with what
  you expect before switching it to `enforce`.
- **Version rollback.** Policies are versioned; the `Policies` page can compare two versions and
  roll back to the previous one in one operation.

See [Policies](./08-policies.md) and [Identities and accounts](./06-identities.md).

---

## The request explorer returns an error or is empty

The `Requests` page reads raw report events from ClickHouse. Everything else in the console reads
PostgreSQL, which is why this one page can fail on its own.

| What you see | Reason | Fix |
| --- | --- | --- |
| "Service unavailable — request events are unavailable: ClickHouse is not configured" | `SPINNERET_CLICKHOUSE_URL` is empty. The server logs `clickhouse disabled: raw report events are not stored` at startup | Configure ClickHouse and restart |
| "Request timed out" (`query_timeout`) | the query exceeded the 30 s per-call ClickHouse budget | Narrow the time range, add a site/outcome filter |
| "Too many requests" (`query_too_large`) | the query needed more than the 512 MiB per-query memory cap, or more rows/bytes than ClickHouse allows | Same: narrow the range, add filters |
| "Invalid request" on a time range | the range exceeds the 7-day maximum for request events | Split the query |
| Empty, no error | no events in the range, or the time range is in the future, or the selected scope has no readable sites | Widen the range; check the scope switcher |
| Empty although reports are being accepted | reports are enqueued but not processed, or the ClickHouse writer is failing | `spinneret_report_lag_seconds`, `spinneret_db_write_batches_total{writer,result="error"}` |

Aggregate pages (overview, per-site cards, heatmap, risk events) come from PostgreSQL and have a
31-day maximum range; they keep working when ClickHouse is down.

**Note.** `request events are unavailable` is returned with Connect code `unavailable` (HTTP
503) but carries the reason `failed_precondition`. Match on the reason, not on the code, if you
are automating against it.

---

## Config changes do not reach nodes

A node reads configuration with `GetConfig` and subscribes with `WatchConfig`, a long poll that
returns immediately when something changed and otherwise parks for up to 60 seconds. Measured
end-to-end wake-up latency after a publish is well under 100 ms, so "the node did not notice" is
never normal.

| Cause | Distinguish | Fix |
| --- | --- | --- |
| The change is still a draft | `Config Center` shows the item as having unpublished changes | Publish it |
| The node watches a different group or item key | Compare the node's watch request with the item's group and key | Fix the node |
| The token's `config:read` scope glob does not cover the group | The call fails with `scope_missing`, not silence | Reissue the token |
| The node is not calling `WatchConfig` at all | `spinneret_config_watchers` on each instance | Fix the node |
| The watcher limit was hit | `WatchConfig` fails with `rate_limited` and a 1 s retry hint | Raise `SPINNERET_MAX_WATCHERS` (default 20,000 per instance), or add replicas |
| A proxy buffers the long poll | The node sees changes only every 60 s, in lockstep | The bundled Caddyfile sets `flush_interval -1`; any other proxy needs the equivalent |
| The node caches the value and never re-reads it | Only the node's own logs show this | Fix the node |

For a **policy** change (rotation, signal, action, breaker) the propagation path is different:
policies live in the catalog, not the config center. An instance reloads a namespace's catalog
on an invalidation event (debounced 100 ms) and does a full reload every 60 s as a safety net;
in addition every admin request syncs the catalog before it runs, so the console always shows
its own writes. If a published policy has not taken effect on a node after a few seconds, look
for `catalog: full reload failed` or `catalog: reload after invalidation failed` in the server
logs — a namespace whose reload keeps failing keeps serving the last snapshot that loaded.

See [Configuration center](./09-config-center.md) and [Node API reference](./13-node-api.md).

---

## A secret cannot be read

| Symptom | Reason | Fix |
| --- | --- | --- |
| Node: HTTP 403, `scope_missing` | the token has no `secret:read:<namespace>/<path glob>` covering the path | Reissue the token with the right glob |
| Console: "You don't have permission for this action" | the user lacks `secret:reveal` in that namespace | Grant a role that has it |
| HTTP 404, `not_found` | no secret at that path, or no such version | Check the path and version on the `Secrets` page |
| HTTP 500, `internal`, server log says decrypt failed | the DEK cannot be unwrapped with any configured KEK | See below |
| Startup fails with `load dedupe_pepper system key (is the KEK the one used to initialize this database?)` | the KEK does not match the database | See below |
| A config item still contains a literal `${secret:...}` | an admin read returns raw content by design; a node read either substitutes or fails | See below |

**The KEK.** Secrets use envelope encryption: each secret is sealed with a data-encryption key
(DEK), and each DEK is wrapped with a key-encryption key (KEK) loaded from
`SPINNERET_KEK_FILE` (the Compose stack mounts `deploy/compose/secrets/kek.key` at
`/run/secrets/kek`) or from `SPINNERET_KEKS`. If a KEK that wrapped existing DEKs is removed
from the configuration, those secrets can no longer be decrypted. Inspect what is wrapped with
what:

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek status
```

It lists the configured KEKs, how many records each one wraps, and rewrap progress. Restore the
missing KEK line, restart, then re-wrap everything with the current KEK:

```bash
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek rewrap
```

**`${secret:...}` in config.** A config item may reference a secret as `${secret:<path>}` or
`${secret:<path>#<version>}`. Resolution happens when the *node* reads the item, with the
node's own principal — so a node whose token lacks `secret:read` for that path gets an error,
not a silently unsubstituted string. A node read either substitutes every reference or fails the
whole request: `permission_denied` when the node may not read a referenced secret,
`failed_precondition` when the secret does not exist.

So a literal `${secret:...}` almost always means you are looking at an **admin** read: admin reads
always return raw content by design, which is exactly what the console shows you in the editor.
References are scanned on every format (`json`, `yaml` and `text` alike), and there is no escape
syntax — every `${secret:` sequence must form a valid reference, and one that does not is rejected
when the draft is saved and again when it is published. Check what the *node* sees with `GetConfig`
using the node's own token.

Every read attempt — allowed, denied or failed — is written to the audit log as `secret.read`
with the purpose, version and client IP. That is the fastest way to see whether a node's call
even arrived. See [Secret vault](./10-secrets.md).

---

## Latency is high or throughput collapses

Spinneret does not degrade gracefully past its knee: it collapses. Recognising the signature
matters more than any individual number.

**The signature.** Acquire p99 jumps by an order of magnitude, `spinneret_report_lag_seconds`
goes from tens of milliseconds to seconds, `spinneret_acquire_total{result="exhausted"}` climbs
sharply, and Redis CPU is high while *useful* throughput is falling. The loop is:

1. Redis saturates, so the report worker falls behind;
2. leases are therefore not released, so identities stay leased;
3. acquires sample those leased identities, reject them, and push their ready-queue score
   forward — one `HGET` plus one `ZADD` each;
4. a failing acquire walks far more candidates than a succeeding one (measured: 220 Redis
   commands instead of 49), which pushes Redis further into saturation.

It is a feedback loop, not a CPU ceiling. Once it starts, offered load has to drop below the
knee for it to unwind.

**Admission control bounds that loop.** Every instance caps how many `acquire.lua` calls it has in
flight at Redis and sheds the excess in the server instead of multiplying concurrency at Redis.
`SPINNERET_ACQUIRE_FLEET_INFLIGHT` (default `64`, range 0–65536) is the budget for the *whole fleet*:
an instance admits that number divided by the live API instances it sees, clamped to
`[4, 4096]`. `SPINNERET_ACQUIRE_MAX_INFLIGHT` (default `0` = derive from the fleet budget, max 4096)
pins one instance's limit instead and stops the heartbeat that counts peers.
`SPINNERET_ACQUIRE_FLEET_INFLIGHT=0` turns admission control off and restores the old unbounded
behaviour. An attempt waits up to 50 ms — never longer than its own remaining `wait_ms` budget — for
a permit; if none arrives it is shed with reason `overloaded` (code `unavailable`, HTTP 503) and a
retry hint jittered into 100–200 ms. A shed issues no Redis command at all, which is the whole point:
it turns the feedback loop into ordinary backpressure. A batch permit is weighted by `count`, so an
`AcquireBatch` is charged for the Lua work it really does.

Two things about `overloaded` are worth knowing before you read the metrics:

- It means *"this instance was at its limit, or had callers queued ahead of you"* — not strictly
  "in flight equalled the limit at that instant". A caller that passes no wait budget is never
  parked, so it is shed while callers that did offer to wait are still queued in front of it. The
  window is one drain cycle (about one Redis round trip) and it is the price of the first-in,
  first-out rule that keeps waiting callers from being starved by fresh arrivals.
- Because every SDK defaults to `wait_ms = 0`, **`spinneret_acquire_queued` stays at zero for
  default traffic** no matter how hard an instance is shedding: nothing is ever parked. It only
  becomes non-zero for callers that pass `wait_ms > 0`. Read `shed_no_wait` for the default client
  and `queued`/`shed_queue_full` for clients that wait.

Both SDKs retry `unavailable` automatically, and the retry re-enters the *ungated* front half of
the request: authentication, the per-token rate limiter, catalog and group resolution. During heavy
shedding the request rate at the front door therefore rises by the retry factor, and a tenant can
start seeing `resource_exhausted/rate_limited` at an offered rate below its configured RPS. Read the
two together: the ratio to watch is the client request rate against
`rate(spinneret_acquire_admission_total{result=~"shed.*"}[1m])`.

| Metric | What it tells you |
| --- | --- |
| `spinneret_acquire_admission_total{result}` | admission decisions, one per *attempt*: `immediate` (a permit was free), `queued` (parked, then admitted), `shed_no_wait` (no permit and no wait budget — the default client), `shed_queue_full` (no permit and the 4×limit wait room was full), `shed_timeout` (parked, budget spent), `shed_canceled` (the caller went away). The shed ratio is `rate(…{result=~"shed.*"}[1m]) / rate(…[1m])` over *all* labels — per attempt, not per request, which is why `spinneret_acquire_total` is the wrong denominator |
| `spinneret_acquire_admission_wait_seconds` | how long attempts waited for a permit |
| `spinneret_acquire_inflight` | script calls in flight on this instance; `sum()` across replicas is the fleet-wide concurrency at Redis |
| `spinneret_acquire_queued` | attempts waiting for a permit on this instance — zero unless callers pass `wait_ms > 0` |
| `spinneret_acquire_inflight_limit` | this instance's current limit. `sum(spinneret_acquire_inflight_limit) > 256` means the per-instance floor of 4 has overtaken the fleet budget: past about 16 replicas the fleet total grows as 4 × replicas again. Shard Redis or lower `SPINNERET_ACQUIRE_FLEET_INFLIGHT` |
| `spinneret_acquire_peers` | live API instances the fleet budget is divided among. Cross-check it against the number of scraped API-role instances (`count(up{job="spinneret"})` in the bundled Compose stack): a mismatch is the only thing that distinguishes "one replica" from "the division is broken" |
| `spinneret_acquire_peer_beat_age_seconds` | age of that count. It grows from process start, so a heartbeat that has never succeeded cannot look fresh |
| `spinneret_acquire_peer_beat_failures_total` | failed heartbeats. While they fail the count is frozen at its last value, which can only narrow the limit, never widen it |
| `spinneret_acquire_script_seconds` | `acquire.lua` round-trip time at Redis; shares its buckets with `spinneret_acquire_duration_seconds`, so the gap between them is the wait ladder plus rendering |
| `spinneret_acquire_total{result="overloaded"}` | acquires that ended as a shed |

| Observation | What it means | What to do |
| --- | --- | --- |
| Rising `exhausted` | The identity pool is empty for that endpoint group — with admission control on, `exhausted` means pool exhaustion only, never overload | Add identities, widen the rotation policy, or shorten `max_concurrent_leases` holds. For overload, read `overloaded` instead |
| `report_lag_seconds` in seconds | The worker cannot drain the streams | Add worker instances (each owns a share of the 16 shards), or reduce load |
| Rising `overloaded` with Redis CPU below its ceiling **and** `spinneret_acquire_script_seconds` p99 normal | Admission control is shedding: the per-instance limit is narrower than what Redis can take | Raise `SPINNERET_ACQUIRE_FLEET_INFLIGHT`, or pin a measured value with `SPINNERET_ACQUIRE_MAX_INFLIGHT` |
| Rising `overloaded` with Redis CPU low **and** `spinneret_acquire_script_seconds` p99 spiked | Redis is stalled, not narrow. A stall pins every permit for up to the 2 s script timeout while Redis CPU sits idle, so the symptoms look identical | Fix Redis (AOF rewrite fork, swap, a slow `SAVE`, a noisy neighbour). **Do not raise the budget** — that re-opens the collapse loop |
| `spinneret_acquire_peers` is 1 on every replica of a multi-replica fleet | Either the heartbeats are failing, or the replicas share one `SPINNERET_INSTANCE_ID` and collapse into a single registry member — each one then admits the *whole* fleet budget | Check `spinneret_acquire_peer_beat_failures_total` and `spinneret_acquire_peer_beat_age_seconds`. If the beats are healthy, the ids are duplicated: leave `SPINNERET_INSTANCE_ID` unset so the hostname-derived default applies |
| The fleet total exceeds the configured budget | Some API instances pin `SPINNERET_ACQUIRE_MAX_INFLIGHT` and some derive. Pinned instances do not register, so the deriving ones divide by too small a count and the pinned allocation is added on top | Pin the limit on *every* API instance or on none |
| Adding replicas made it *worse* | With admission control off (`SPINNERET_ACQUIRE_FLEET_INFLIGHT=0`), two replicas put twice the concurrency on one Redis and collapse earlier than one. With it on, the fleet budget is divided among the live instances, so extra replicas add server capacity without adding Redis concurrency | Scale Redis, not replicas, for acquire throughput; keep admission control on |
| A freshly seeded or rebuilt site collapses early | A cold site's ready scores are correlated | Warm it with traffic before measuring |
| Valkey stalls every ~52 s | AOF rewrite forks | Use the bundled Valkey settings (`auto-aof-rewrite-percentage 300`, `auto-aof-rewrite-min-size 1gb`, `--save ""`) |
| Tail latency bad, throughput fine | Valkey `io-threads` | The bundled default is 4; `VALKEY_IO_THREADS=1` restores single-threaded behaviour |
| Bursts of HTTP 503 from the load balancer | Replicas being parked by passive health checks | See the `lb` notes above |

The levers, in the order they pay, are in [Performance and tuning](./17-performance.md). The two
that matter most here: `max_concurrent_leases` (`1` sustains about 4,500 acquire→report
cycles/s per instance, `4` about 5,500 and degrades far more gracefully) and Redis capacity
(budget roughly one Redis primary per 4,500 cycles/s).

**Profiling.** Set `SPINNERET_PPROF_ADDR` (for example `127.0.0.1:6060`; in the Compose stack it
needs a line in the `x-spinneret-env` anchor) to serve
`/debug/pprof/` on its own listener. It is off by default, is never mounted on the API or
metrics listener, and the server logs a warning when it is enabled. The endpoints are
unauthenticated and expose heap contents and goroutine stacks — never publish the port. The
configuration is rejected outright if the pprof address equals the API or metrics address.

---

## Redis, PostgreSQL or ClickHouse is out of memory or disk

### Redis / Valkey

Valkey runs with `--maxmemory-policy noeviction`: Spinneret's hot state must never be evicted
behind its back. When memory runs out, writes fail loudly instead of corrupting the pool
silently; if the container is OOM-killed by the host you will see exit code 137 and a restart
loop.

What actually fills Redis is not the dataset, it is traffic-proportional state:

```text
working set ≈ dataset
            + reports/s  × SPINNERET_REPORT_DEDUP_TTL × 80 B    (dedup markers)
            + acquires/s × SPINNERET_REPORT_DEDUP_TTL × 290 B   (ended lease hashes)
            + backlog_entries × 440 B                           (report streams)
```

At 4,500 cycles/s with the default 1-hour dedup TTL, that is about 1.2 GiB of dedup markers plus
4.4 GiB of ended lease hashes — far more than a 100,000-identity dataset (about 350 MiB).

The single biggest lever is `SPINNERET_REPORT_DEDUP_TTL`. Nodes retry within seconds, not
hours: 5–15 minutes is usually enough and cuts that term by 4–12×. The minimum accepted value
is `1m`. Second lever: `SPINNERET_STREAM_MAXLEN` (default 1,000,000 per shard) caps a backlog
the workers cannot drain — size it to `rate × seconds_of_backlog / shards`, and remember that
entries past the cap are dropped silently.

### PostgreSQL

Growth comes from analytics and audit rows, all governed by retention settings. Defaults:
`SPINNERET_RETENTION_RISK_EVENTS` and `SPINNERET_RETENTION_MINUTE_STATS` 720 h,
`SPINNERET_RETENTION_HOUR_STATS` 4320 h, `SPINNERET_RETENTION_STATE_EVENTS` and
`SPINNERET_RETENTION_AUDIT` 8760 h. The `partition_manager` job creates and drops partitions
hourly on the leader instance; if it is failing, partitions stop being dropped and the disk
fills. Check `spinneret_job_runs_total{job="partition_manager",result="error"}`.

Set `SPINNERET_RECORD_COOLDOWN_EVENTS=false` if cooldown state events dominate your row count
and you do not need them.

### ClickHouse

Two distinct failures. **Disk**: bounded by `SPINNERET_CLICKHOUSE_TTL_DAYS` (default 90,
range 1–3650), applied to the tables when the server migrates them at startup. **Memory**: the
bundled `deploy/compose/config/clickhouse-limits.xml` caps the caches, which the default configuration sizes
for a dedicated analytics machine. Keep `max_server_memory_usage` above the process's idle
resident size (~1.2 GiB for the alpine image) — below it, every INSERT fails with
`MEMORY_LIMIT_EXCEEDED`, which shows up as `clickhouse batch insert failed` in the server log and as
a stalled report worker under load. The container stays *healthy* throughout, because the healthcheck
only asks `/ping`.

A ClickHouse outage does not stop the control plane: acquire, report, policy and config keep
working, and only the request explorer goes dark.

---

## After an upgrade

| Symptom | Cause | Fix |
| --- | --- | --- |
| The server refuses to start, log says the schema is behind the binary | migrations were not applied | Run the `migrate` service, or start with `--migrate` |
| The log warns `database schema is newer than this binary` | an old replica is still running next to new ones | Expected during a rolling upgrade; it resolves when the last old replica is replaced |
| `invalid configuration:` listing a variable that used to work | a validation rule was tightened, or a variable was renamed | Read the list — every problem is named — and check [Configuration reference](./03-configuration.md) |
| The console is the old version | a cached `index.html` | Hard-reload; the console is served from the binary, so a new image is a new console |
| `SPINNERET_REPORT_SHARDS` was *lowered* | lease IDs encode their shard, and a report whose shard is `>=` the new count is rejected as `lease_unknown`; in-flight leases above the new count are stranded | Change it back. Raising the count re-routes new leases without rejecting in-flight ones, but treat it as fixed for the life of a deployment either way |

Always `$COMPOSE ps` after an upgrade and confirm every `spinneret` replica reaches `healthy`,
not just `running`.

---

## After a restore

The order matters, and one thing must be restored that is easy to forget.

1. **The KEK is not in the database.** `deploy/compose/secrets/kek.key` (or whatever
   `SPINNERET_KEK_FILE` points at) is a separate artefact. Restoring PostgreSQL without the KEK
   that was current when it was written makes every secret and every identity payload
   permanently unreadable. The server will not even start: it fails on
   `load dedupe_pepper system key (is the KEK the one used to initialize this database?)`.
2. **Redis is a cache of PostgreSQL, but it is not rebuilt automatically after a restore of
   PostgreSQL alone.** If the hot state still holds the pre-restore world, rebuild it:

   ```bash
   $COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate rebuild
   ```

   Without `--site` this deletes the epoch and rebuilds every site; API instances answer
   `/readyz` with `hotstate: epoch missing (rebuild pending)` until it finishes, and Caddy takes
   them out of the pool while it runs. Do it in a maintenance window.
3. **Restored sites are cold.** Ready scores are spread uniformly over the next 60 s and the
   first stretch of traffic is less efficient. Expect a period of elevated `exhausted`.
4. **Leases did not survive.** Nodes holding pre-restore leases get `lease_unknown` on
   `Renew`, `Release` and `Report`. They should treat it as "start over" and acquire again.
5. **ClickHouse is restored separately or not at all.** Losing it loses request-level history
   only.

See [Operations runbook](./16-operations.md) for the full backup and restore drill.

---

## The complete error-reason table

Every application error carries a stable, machine-readable reason. Connect and gRPC clients read
it from the error metadata; plain HTTP+JSON clients read it from the **`Spinneret-Reason`**
response header. Retryable errors also set **`Spinneret-Retry-After-Ms`** — wait at least that
long. The reasons are defined in `internal/apperr/apperr.go`; the HTTP status column is the
Connect protocol's mapping of the code.

| Reason | Connect code | HTTP | Meaning | Retry? | What to do |
| --- | --- | ---: | --- | --- | --- |
| `token_invalid` | `unauthenticated` | 401 | The Bearer token is not a known API token | No | Check the token; issue a new one |
| `token_expired` | `unauthenticated` | 401 | The token's expiry has passed | No | Issue a new token |
| `token_revoked` | `unauthenticated` | 401 | The token was revoked | No | Issue a new token |
| `ip_not_allowed` | `permission_denied` | 403 | The client address is not in the token's IP allowlist | No | Fix the allowlist, or `SPINNERET_TRUSTED_PROXIES` |
| `session_invalid` | `unauthenticated` | 401 | No console session, an expired one, or a disabled account. Also returned when a service is called with no authenticated principal | No | Sign in again |
| `csrf_missing` | `permission_denied` | 403 | A cookie-authenticated unsafe request had no `X-Spinneret-CSRF` header | No | Send the header; stop stripping it in a proxy |
| `login_throttled` | `resource_exhausted` | 429 | 5 failed sign-ins for the username, or 20 for the IP, within 15 minutes | After the hint | Wait; an admin password reset clears the username counter |
| `scope_missing` | `permission_denied` | 403 | The API token's scopes do not grant the permission for this resource or namespace | No | Reissue the token with the right scopes |
| `permission_denied` | `permission_denied` | 403 | The user's role bindings do not grant the permission | No | Grant the role in the right scope |
| `site_unknown` | `invalid_argument` | 400 | No such site in the caller's namespace, or `site` was empty | No | Fix the request |
| `client_unknown` | `invalid_argument` | 400 | The client is not declared by the site | No | Declare it, or fix the request |
| `endpoint_group_unknown` | `invalid_argument` | 400 | No such endpoint group for that site and client | No | Fix the request |
| `uri_invalid` | `invalid_argument` | 400 | The URI could not be normalized, or exceeds the site's maximum path length | No | Fix the request |
| `invalid_argument` | `invalid_argument` | 400 | Request validation failed (also the fallback reason for validation errors that carry none) | No | Fix the request |
| `no_identity_available` | `resource_exhausted` | 429 | No identity in the endpoint group passed the filters within the wait budget | Yes | Honour the hint; see the section above |
| `no_proxy_available` | `resource_exhausted` | 429 | An identity was found but no proxy could be attached | Yes | Honour the hint; check the proxy pool |
| `circuit_open` | `unavailable` | 503 | The endpoint group's breaker is open | Yes | Honour the hint; do not hot-loop |
| `site_paused` | `unavailable` | 503 | The site switch is on; retry hint is 30 s | Yes | Unpause the site |
| `overloaded` | `unavailable` | 503 | Acquire admission control shed the call before it reached Redis — this instance is at its acquire concurrency limit. No Redis command was issued, so retrying is always safe | Yes | Honour the hint (100–200 ms, jittered); see [Latency is high or throughput collapses](#latency-is-high-or-throughput-collapses) |
| `rebuilding` | `unavailable` | 503 | The hot state is being rebuilt | Yes | Retry after the hint |
| `lease_unknown` | `not_found` | 404 | No such lease: wrong ID, wrong namespace, or it aged out | No | Acquire again |
| `lease_released` | `failed_precondition` | 400 | The lease was already released | No | Acquire again |
| `lease_expired` | `failed_precondition` | 400 | The lease TTL passed before the call | No | Acquire again; renew earlier |
| `lease_lifetime_exceeded` | `failed_precondition` | 400 | `Renew` would exceed the lease's maximum lifetime | No | Release and acquire a new lease |
| `rate_limited` | `resource_exhausted` | 429 | The token's requests-per-second limit, or too many concurrent config watchers on this instance (1 s hint) | Yes | Honour the hint; raise the limit or add replicas |
| `not_found` | `not_found` | 404 | The addressed resource does not exist | No | Fix the request |
| `already_exists` | `already_exists` | 409 | A uniqueness constraint was violated | No | Use another name |
| `failed_precondition` | `failed_precondition` | 400 | The system is not in a state that allows the operation. Also carried by the `unavailable` (503) answer when the request explorer is queried with ClickHouse unconfigured | No | Satisfy the precondition |
| `conflict` | `aborted` | 409 | The resource changed concurrently | Yes | Reload and retry |
| `query_too_large` | `resource_exhausted` | 429 | An analytics query needed more resources than ClickHouse allows | No | Narrow the time range or add filters |
| `query_timeout` | `deadline_exceeded` | 504 | An analytics query exceeded its time budget | No | Narrow the time range or add filters |
| `internal` | `internal` or `unavailable` | 500 / 503 | An unexpected failure (`internal`), or a dependency is unreachable — report ingest answers `unavailable` with this reason and a retry hint when the report queue cannot be written. On `internal` the client always sees the generic message `internal error`; the cause is only in the server log | Yes when the code is `unavailable` and a hint is present | Retry with backoff on `unavailable`; on `internal` read the server log, then file a bug |

Two protocol details worth knowing:

- Clients never see the cause of an `internal` error. It is logged server-side with the
  procedure and the actor and replaced by `internal error` on the wire, deliberately.
- A canceled or timed-out request becomes `canceled` / `deadline_exceeded`, not `internal`.

---

## Reading the server logs

The server logs to **stdout** as JSON by default (`SPINNERET_LOG_FORMAT=json`; `text` is the
alternative), at `SPINNERET_LOG_LEVEL` (default `info`).

```bash
$COMPOSE logs -f spinneret
$COMPOSE logs --no-color --since 30m spinneret | python3 -c \
  'import json,sys
for line in sys.stdin:
    try: r = json.loads(line)
    except ValueError: continue
    if r.get("level") in ("ERROR", "WARN"):
        print(r["time"], r["level"], r["msg"], {k: v for k, v in r.items() if k not in ("time", "level", "msg")})'
```

Fields you will actually use:

| Field | Meaning |
| --- | --- |
| `time`, `level`, `msg` | slog's built-ins |
| `instance` | `SPINNERET_INSTANCE_ID`, on every record — this is how you tell replicas apart. Unset (as in the bundled stack) it defaults to `<hostname>-<6 hex>`, so in Compose it changes on every container start; set it explicitly if you want stable replica identity across restarts |
| `component` | the subsystem: `rpc`, `http`, `sse`, `jobs`, `catalog_sync`, `report_ingest`, … |
| `procedure` | the Connect procedure of a failed request |
| `actor` | the principal of a failed request, when there was one |
| `error` | the wrapped cause, including everything the client was not shown |
| `site_id`, `site_key`, `namespace_id`, `report_id`, `lease_id` | subject identifiers, where relevant |

Records worth recognising:

| Message | Level | Means |
| --- | --- | --- |
| `starting spinneret` | INFO | the effective configuration follows, with secrets redacted |
| `spinneret started` | INFO | serving; carries `role`, `addr`, `version` |
| `shutdown requested` | INFO | a signal arrived; the drain begins |
| `request failed` | ERROR | an `internal`, `unknown` or `data_loss` result; the cause is in `error` |
| `handler panic` / `stream handler panic` | ERROR | a bug — the stack is in the record. Please report it |
| `background loop failed, restarting` | ERROR | a supervised loop died; `spinneret_loop_restarts_total{loop}` counts it |
| `listener failed, shutting down` | ERROR | the HTTP listener died; the process exits non-zero |
| `readiness: … failed` | WARN | why `/readyz` is red |
| `catalog: full reload failed` | ERROR | the namespace keeps serving its last good snapshot |
| `database schema is newer than this binary` | WARN | rolling upgrade in progress |
| `pprof debug listener enabled…` | WARN | `SPINNERET_PPROF_ADDR` is set |
| `clickhouse disabled: raw report events are not stored` | INFO | the request explorer will be empty |
| `report ingest failed` | ERROR | the report queue could not be written; the caller got `unavailable` with reason `internal` and a retry hint. Check Redis |
| `state change flush failed` / `dropping state change batch` | ERROR | actions were applied but not persisted; check PostgreSQL |
| `proxy binding queue full, dropping bindings` | WARN | write backpressure on proxy bindings |

**Redaction.** Attributes whose key is `password`, `token`, `secret`, `authorization`, `cookie`,
`payload` or `url_credentials` — and anything nested in a group with one of those names — are
written as `[REDACTED]`. The startup configuration dump is redacted the same way. Logs are still
sensitive: they contain site names, identity IDs and actor names.

---

## Turning on debug logging safely

Debug logging is safe to enable in production for a bounded period. It does not log credentials
(the redaction above applies at every level) and it does not log request bodies. It is verbose,
so raise your log volume budget first.

```bash
# In deploy/compose/.env
SPINNERET_LOG_LEVEL=debug
```

```bash
$COMPOSE up -d spinneret     # recreates the replicas with the new level
# ... reproduce the problem, capture the logs ...
# then put it back
sed -i.bak 's/^SPINNERET_LOG_LEVEL=debug/SPINNERET_LOG_LEVEL=info/' deploy/compose/.env
$COMPOSE up -d spinneret
```

The level is read at startup; there is no runtime toggle. Valid values are `debug`, `info`,
`warn`, `error` — anything else fails validation and the server will not start.

To reduce the blast radius on a multi-replica deployment, scale the service to one replica,
capture, then scale back — or run one extra instance with the debug level and send only your own
traffic to it.

**Do not** enable `SPINNERET_PPROF_ADDR` on a reachable address as a "debug setting". The pprof
endpoints are unauthenticated and expose heap contents and goroutine stacks. Bind it to
`127.0.0.1` and reach it through an SSH tunnel.

---

## Collecting a diagnostic bundle

Run this before filing a bug at <https://github.com/TikHub/Spinneret/issues>. It collects only
what the maintainers need, and nothing from `.env` or the KEK file.

```bash
#!/usr/bin/env bash
set -euo pipefail
cd /path/to/Spinneret
COMPOSE="docker compose -f deploy/compose/docker-compose.yml"
OUT="spinneret-diag-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT"

# Versions and topology.
{ docker version; docker compose version; uname -a; } > "$OUT/host.txt" 2>&1
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate version > "$OUT/version.txt" 2>&1
$COMPOSE ps                                    > "$OUT/ps.txt"        2>&1
$COMPOSE config --no-interpolate               > "$OUT/compose.yml"   2>&1

# Effective configuration, secrets redacted by the command itself.
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate config check \
                                               > "$OUT/config-check.txt" 2>&1

# Schema and hot state.
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate migrate status \
                                               > "$OUT/migrate-status.txt" 2>&1
$COMPOSE run --rm -T --entrypoint /usr/local/bin/spnr migrate kek status \
                                               > "$OUT/kek-status.txt" 2>&1

# Health and metrics (one replica, through the load balancer).
BASE="http://localhost:${SPINNERET_PORT:-8080}"
curl -fsS "$BASE/healthz"  > "$OUT/healthz.json" || true
curl -fsS "$BASE/readyz"   > "$OUT/readyz.json"  || true
curl -fsS "$BASE/metrics"  > "$OUT/metrics.txt"  || true

# Logs.
for svc in spinneret migrate lb postgres valkey clickhouse; do
  $COMPOSE logs --no-color --tail 5000 "$svc" > "$OUT/logs-$svc.txt" 2>&1 || true
done

# Datastore vitals.
$COMPOSE exec -T valkey valkey-cli info \
  > "$OUT/valkey-info.txt" 2>&1 || true
# --stat has no sample limit (-c is the boolean cluster-mode flag), so bound it from outside.
timeout 10 $COMPOSE exec -T valkey valkey-cli --stat -i 1 \
  > "$OUT/valkey-stat.txt" 2>&1 || true

tar czf "$OUT.tar.gz" "$OUT" && rm -rf "$OUT"
echo "wrote $OUT.tar.gz"
```

Before you attach it:

- **Check `metrics.txt` and the logs for names you do not want to publish.** Site names, client
  names, endpoint group names, namespace names and node names appear as metric labels and log
  fields. Rename them in the bundle if they are sensitive.
- `config-check.txt` is redacted by `spnr config check` itself, but read it once anyway.
- Never attach `deploy/compose/.env`, `deploy/compose/secrets/kek.key`, a database dump, or a
  heap profile — a heap profile can contain decrypted payloads.

Say in the report: what you did, what you expected, what happened, the exact reason string and
`Spinneret-Reason` header if there was one, and the UTC timestamp of the occurrence so the
maintainers can find it in the logs.

---

## Next

- [Operations runbook](./16-operations.md) — the planned procedures: backups, upgrades, scaling, KEK rotation, draining.
- [Performance and tuning](./17-performance.md) — the measured numbers behind the collapse signature, and the tuning levers in the order they pay.
- [Observability and alerting](./12-observability.md) — the metrics and alert rules that turn these symptoms into pages.
- [Node API reference](./13-node-api.md) — the retry rules a node must implement for each reason.
- [Configuration reference](./03-configuration.md) — every `SPINNERET_*` variable named on this page.
