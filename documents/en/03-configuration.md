# Configuration reference

**Every runtime setting of a Spinneret instance: the variable, the default the code really applies, what
it does, and when you would change it. This is the page you come to with a `.env` open in the other
window.**

[中文](../zh/03-configuration.md)

---

## Contents

- [Two configuration layers](#two-configuration-layers)
- [How the environment is read](#how-the-environment-is-read)
- [Validation, and checking before you deploy](#validation-and-checking-before-you-deploy)
- [Process identity](#process-identity)
- [PostgreSQL](#postgresql)
- [Redis / Valkey](#redis--valkey)
- [ClickHouse](#clickhouse)
- [Keys](#keys)
- [Reports and streams](#reports-and-streams)
- [Caches](#caches)
- [Acquire admission control](#acquire-admission-control)
- [Sessions and cookies](#sessions-and-cookies)
- [TLS and trusted proxies](#tls-and-trusted-proxies)
- [Metrics, profiling and tracing](#metrics-profiling-and-tracing)
- [Logging](#logging)
- [Proxy health checking and GeoIP](#proxy-health-checking-and-geoip)
- [Retention](#retention)
- [Request limits](#request-limits)
- [The console](#the-console)
- [What the Compose stack sets for you](#what-the-compose-stack-sets-for-you)
- [Compose-only and installer-only variables](#compose-only-and-installer-only-variables)
- [Across replicas: same, different, tunable](#across-replicas-same-different-tunable)
- [Secrets in the environment](#secrets-in-the-environment)
- [What is not an environment variable](#what-is-not-an-environment-variable)
- [Three worked configurations](#three-worked-configurations)
- [Next](#next)

---

## Two configuration layers

Spinneret has two things that are both called configuration, and mixing them up costs an afternoon. They
have nothing to do with each other.

| | Layer 1 — the process environment | Layer 2 — the configuration center |
| --- | --- | --- |
| Configures | the **Spinneret server process** | **your crawler nodes** |
| Named | `SPINNERET_*` environment variables | namespace / group / key items |
| Stored in | the orchestrator, `.env`, a unit file | PostgreSQL, versioned |
| Changed by | editing and **restarting the instance** | publishing in the console or over the admin API |
| Takes effect | on the next start | within about a second, on every watching node |
| Reference | **this page** | [Configuration center](./09-config-center.md) |

The server never reads its own settings from the configuration center. There is no bootstrap loop, no
chicken-and-egg: `SPINNERET_DATABASE_URL` cannot live in a database. And Spinneret never interprets the
content of a config item — to the server it is a validated blob of JSON, YAML or text that belongs to
your nodes.

So:

- "I want the cooldown after a 403 to be 10 minutes instead of 5" → that is a **policy**, published at
  runtime. [Policies](./08-policies.md). Nothing on this page.
- "I want my nodes to run at 20 requests per second" → that is a **config item**, published at runtime.
  [Configuration center](./09-config-center.md). Nothing on this page.
- "I want this instance to stop serving traffic and only run background jobs" → that is
  `SPINNERET_ROLE=worker`, and it is on this page.

Everything on this page is process environment, everything on this page needs a restart, and nothing on
this page can be changed from the console. That is deliberate: what a container runs with stays visible
in the orchestrator that started it.

There is no configuration file. The server reads no YAML, no TOML, no `spinneret.conf`. There is also no
reload signal — `SIGINT` and `SIGTERM` start a graceful shutdown, and nothing re-reads the environment of
a running process.

---

## How the environment is read

`internal/appconfig/appconfig.go` is the whole of it. Both binaries — `spinneret-server` and `spnr` —
load the same environment through the same function, so `spnr` in a shell on the host validates exactly
what the server would do with that shell's environment.

| Rule | Detail |
| --- | --- |
| Values are trimmed | Leading and trailing whitespace is removed before anything else. |
| Empty means unset | `SPINNERET_METRICS_ADDR=` is the **default**, not the empty string. A variable set to spaces is also unset. This is why the Compose stack can pass `SPINNERET_PROXY_CHECK_URL: ${SPINNERET_PROXY_CHECK_URL:-}` and still get the built-in default. |
| Integers | Plain decimal. `1_000_000` is **not** accepted; write `1000000`. |
| Booleans | `true`/`false`, `1`/`0`, `t`/`f`, `T`/`F`, `TRUE`/`True`, `FALSE`/`False`. |
| Durations | `500ms`, `30s`, `10m`, `24h`, `7d` and combinations such as `1h30m` and `1d12h`. The `d` unit is a Spinneret extension and must be the leading component. The literal `0` is zero; an empty value is the **default**, as in the row above. |
| `permanent` is rejected | Policies accept `permanent` as a duration; environment durations do not. |
| Lists | Comma-separated, each element trimmed, empty elements dropped. Applies to `SPINNERET_REDIS_ADDRS`, `SPINNERET_TRUSTED_PROXIES` and `SPINNERET_ALLOWED_ORIGINS`. |
| Case | `SPINNERET_ROLE`, `SPINNERET_COOKIE_SECURE`, `SPINNERET_LOG_LEVEL` and `SPINNERET_LOG_FORMAT` are lower-cased before validation, so `INFO` and `Info` both work. Nothing else is. |

One variable does not start with `SPINNERET_`: `OTEL_EXPORTER_OTLP_ENDPOINT`, because it is the standard
name and the rest of the OpenTelemetry environment is read by the SDK, not by Spinneret.

---

## Validation, and checking before you deploy

Every value is checked before anything is connected to. Two kinds of problem are reported:

**Parse errors** name the variable and quote what they saw:

```text
SPINNERET_REPORT_SHARDS: invalid integer "sixteen"
SPINNERET_DATABASE_MAX_CONNS: invalid 32-bit integer "9999999999"
SPINNERET_PAYLOAD_CACHE: invalid boolean "on"
SPINNERET_SESSION_TTL: invalid duration "12 hours"
```

**Range and consistency errors** are the complete list below. Every one is collected, so one run tells
you about all of them instead of one per restart:

| Condition | Exact message |
| --- | --- |
| Role not `all`/`api`/`worker` | `SPINNERET_ROLE must be one of all, api, worker (got "leader")` |
| No database | `SPINNERET_DATABASE_URL is required` |
| No Redis | `SPINNERET_REDIS_URL or SPINNERET_REDIS_ADDRS is required` |
| No key | `SPINNERET_KEK_FILE or SPINNERET_KEKS is required` |
| Bad key prefix | `SPINNERET_REDIS_PREFIX must be non-empty and must not contain '{', '}', ':' or spaces` |
| Pool too small | `SPINNERET_DATABASE_MAX_CONNS must be >= 2` |
| Shard count out of range | `SPINNERET_REPORT_SHARDS must be between 1 and 255` |
| Dedup window too short | `SPINNERET_REPORT_DEDUP_TTL must be >= 1m` |
| Negative late window | `SPINNERET_LATE_REPORT_WINDOW must be >= 0` |
| Stream cap too small | `SPINNERET_STREAM_MAXLEN must be >= 1000` |
| Fleet budget out of range | `SPINNERET_ACQUIRE_FLEET_INFLIGHT must be between 0 (admission control off) and 65536` |
| Pinned limit out of range | `SPINNERET_ACQUIRE_MAX_INFLIGHT must be between 0 (derive from the fleet budget) and 4096` |
| Negative payload cache size, or DEK cache size below 1 | `cache sizes must be positive` |
| Payload cache on with size 0 | `SPINNERET_PAYLOAD_CACHE_SIZE must be >= 1 when SPINNERET_PAYLOAD_CACHE is enabled` |
| Session lifetime too short | `SPINNERET_SESSION_TTL must be >= 1m` |
| Unparseable CIDR | `SPINNERET_TRUSTED_PROXIES: <the parse error>` |
| Any retention below a day | `SPINNERET_RETENTION_RISK_EVENTS must be >= 24h` (and the same for the other four) |
| Bad cookie mode | `SPINNERET_COOKIE_SECURE must be auto, true or false` |
| One TLS file without the other | `SPINNERET_TLS_CERT_FILE and SPINNERET_TLS_KEY_FILE must be set together` |
| Bad log level | `SPINNERET_LOG_LEVEL must be debug, info, warn or error` |
| Bad log format | `SPINNERET_LOG_FORMAT must be json or text` |
| Proxy check period below `1s` or timeout below `100ms` | `proxy check interval/timeout too small` |
| ClickHouse TTL out of range | `SPINNERET_CLICKHOUSE_TTL_DAYS must be between 1 and 3650` |
| Watcher cap below 1 | `SPINNERET_MAX_WATCHERS must be >= 1` |
| Admin body limit below 1 MiB | `SPINNERET_ADMIN_MAX_REQUEST_BYTES must be >= 1MiB` |
| pprof on the API address | `SPINNERET_PPROF_ADDR must not be the API address: the profiling endpoints are unauthenticated` |
| pprof on the metrics address | `SPINNERET_PPROF_ADDR must not be the metrics address` |

The server prints them under one header and exits 1 without opening a single connection:

```text
spinneret-server: invalid configuration:
SPINNERET_ROLE must be one of all, api, worker (got "leader")
SPINNERET_REPORT_SHARDS must be between 1 and 255
```

### `spnr config check`

The same validation, on demand, with the resolved configuration printed as JSON and every credential
masked. It connects to nothing, so it is the right first command on a host whose databases are not up
yet.

```bash
# in a hand-made deployment, with the service environment loaded
spnr config check

# in the Compose stack, in a fresh one-shot container
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check

# or inside a replica that is already running (distroless: absolute path, there is
# no shell; --index because the service runs SPINNERET_REPLICAS replicas, 2 by default)
docker compose -f deploy/compose/docker-compose.yml \
  exec --index 1 spinneret /usr/local/bin/spnr config check

# with the installer's wrapper, without a running container
./spnrctl run --rm -T --entrypoint /usr/local/bin/spnr migrate config check
```

Exit 0 means the environment is valid. Exit 1 prints the list above. Passwords inside URLs and DSNs are
replaced with `xxxxx`, and inline key material shows only as `<set>`. The output is a summary of the
settings operators most often get wrong, not all fifty variables — the full sample output and the flag
surface are in [CLI reference → `spnr config`](./15-cli.md#spnr-config).

Two more things worth knowing before the tables. The variables below are grouped by what they configure,
and every variable the server reads appears in exactly one of them. Validation that is *not* in the
table above happens later, at startup, because it needs more than the value: a worker instance whose pool
is too small for its leader jobs is refused with
`SPINNERET_DATABASE_MAX_CONNS=… is too small for a worker instance: … leader jobs each hold a connection
while leading; use at least …`, and a broken TLS key pair or an unreadable key file fails the same way.

---

## Process identity

Who this instance is, what it runs, and where it listens.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_ROLE` | `all` | `all` serves the API, the admin API and the console **and** runs the background workers. `api` serves traffic and runs no background jobs. `worker` runs the pipeline and serves only `/healthz`, `/readyz` and (when no separate metrics address is set) `/metrics`. | When you split request serving from the report pipeline so that a burst of reports cannot slow down `Acquire`. See [Operations → Scaling](./16-operations.md#scaling). |
| `SPINNERET_INSTANCE_ID` | the hostname plus 6 random hex characters, e.g. `node-1-bba039` | Identifies the instance in logs, in job leases, as the Redis stream consumer name, and as the member name of the acquire-budget registry. | Almost never. Leave it unset so the unique default applies. If you must set it, it has to be **distinct per instance** — see [Acquire admission control](#acquire-admission-control) for what happens when it is not. |
| `SPINNERET_HTTP_ADDR` | `:8080` | Listen address of the node API, the admin API, the console, the event stream and — unless `SPINNERET_METRICS_ADDR` is set — `/metrics`. | To bind one interface (`127.0.0.1:8080` behind a proxy on the same host) or to move the port. |
| `SPINNERET_SHUTDOWN_TIMEOUT` | `30s` | Total graceful-shutdown budget. Keep-alives are disabled first, then a drain window of `min(5s, timeout/4)` runs in which `/readyz` already answers `draining` and every response carries `Connection: close` while the listener is still open. Then HTTP shutdown gets half the remaining budget and the background loops get the rest. | Raise it when a slow report backlog needs longer to flush; lower it only with the orchestrator's kill timeout in mind, which must stay above it. The Compose stack gives the service `stop_grace_period: 40s` for exactly this reason. |

`spinneret-server --role api` overrides `SPINNERET_ROLE` on the command line, which is convenient when
one unit file serves both roles. Full flag list: [CLI reference → `spinneret-server`](./15-cli.md#spinneret-server).

The drain window is what makes a rolling restart invisible: a load balancer sees `draining` and stops
routing before the listener closes, so no request is cut off mid-flight. Long-lived requests — the
console event stream and node `WatchConfig` long polls — are cancelled at the start of the drain rather
than being left to hit the deadline, so the client reconnects to a healthy instance immediately.

---

## PostgreSQL

PostgreSQL holds the durable state: tenants, namespaces, identities, proxies, policies, config items,
secrets, the audit log and the partitioned statistics tables. In the warmed steady state the hot path does
not touch it: it sees batched writes and stayed under 3 % CPU in every load scenario. It is not absent from
the hot path, though — a payload-cache miss reads and decrypts the identity payload from PostgreSQL, and
with `SPINNERET_PAYLOAD_CACHE=false` every acquire does. That is why even an `api` instance needs a pool.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_DATABASE_URL` | **required** | A `postgres://` URL or a libpq key/value DSN. Query parameters are passed through, so `?sslmode=verify-full&pool_max_conn_lifetime=1h` works. | Always — there is no default. |
| `SPINNERET_DATABASE_MAX_CONNS` | `32` | Pool size **per instance**, minimum 2. | When you change the replica count or the server's `max_connections`. |

Sizing the pool is one multiplication: `replicas × SPINNERET_DATABASE_MAX_CONNS + headroom` must stay
under PostgreSQL's `max_connections`, which the Compose stack sets to 300. The default pair — two
replicas at 32 — uses 64 of those.

A worker instance has a floor as well as a ceiling. Each leader job holds one connection for as long as
it owns its advisory lock, so a pool that could not give every leader job a connection plus two spare is
refused at startup rather than deadlocking under load. If you shrink the pool on a `worker` or `all`
instance and it refuses to start, the error names the minimum.

`spnr` commands that only touch the database use a pool of 4 and honour
`SPINNERET_DATABASE_MAX_CONNS` if it is set; they do not need Redis or a key configured at all.

---

## Redis / Valkey

Redis (or Valkey — the stack ships Valkey 8 and the protocol is identical) holds the hot state: the
candidate sets `Acquire` picks from, cooldown and ban markers, lease hashes, breaker counters, report
streams and console sessions. This is the hot path.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_REDIS_URL` | **required** unless `SPINNERET_REDIS_ADDRS` is set | `redis://host:port/db` or `rediss://` for TLS. Credentials go in the URL. | Always, for a single primary or a Sentinel-fronted address. |
| `SPINNERET_REDIS_ADDRS` | empty | Comma-separated `host:port` list. A non-empty list selects **Cluster mode** and replaces the seed addresses. At least one of this and the URL is required, and the two **combine**: set both and the URL still supplies the credentials and the TLS settings while this list supplies the nodes. A database index cannot be selected in cluster mode — a URL with a non-zero db then fails with `redis: database N cannot be selected in cluster mode`. | When you run Cluster. |
| `SPINNERET_REDIS_PREFIX` | `sp` | Prefix of every key. Must be non-empty and must not contain `{`, `}`, `:` or a space. | When two independent Spinneret deployments share one Redis. Otherwise leave it. |

Two deployments can share one Redis safely as long as their prefixes differ, because the prefix is the
first path segment of every key. Changing the prefix of a *running* deployment orphans the old keys and
looks exactly like a cold hot state — see [Operations → Rebuilding the hot state](./16-operations.md#rebuilding-the-hot-state).

Keys are hash-tagged so that the Lua scripts stay atomic under Cluster: every key of one site shares the
tag `{s<site key>}` and every key of one report shard shares `{r<shard>}`. You do not configure this;
it is worth knowing when you read a `KEYS` listing or plan slot migration.

**The hot state must never be evicted.** It is derived data and can be rebuilt with `spnr rebuild`, but
silently *losing* a key changes a scheduling decision instead of failing loudly: a cooldown marker that
disappears makes a cooling identity eligible again. This is why the Compose stack runs Valkey with
`maxmemory-policy noeviction` and why you should too. Sizing the instance so it never gets there is in
[Performance → Redis sizing](./17-performance.md#redis-sizing).

---

## ClickHouse

ClickHouse stores raw per-request events and lease events — the rows behind the request explorer. It is
optional. Without it everything else works; you lose per-request drill-down and keep the aggregates,
which live in PostgreSQL.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_CLICKHOUSE_URL` | empty | `clickhouse://user:password@host:9000/database`. Empty disables raw event storage; the server logs `clickhouse disabled: raw report events are not stored` at startup. | Set it whenever you want the request explorer. Leave it empty on a small evaluation host to save a container. |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` | Retention of the `report_events` and `lease_events` tables, 1–3650. | When disk or an audit requirement says something other than 90 days. |

The TTL is applied by the schema migration that runs at startup, and it is applied to existing tables
too: when the configured value differs from what the table has, the server issues
`ALTER TABLE … MODIFY TTL`. So changing this variable and restarting is all it takes — but shortening it
deletes data on the next TTL merge, and that is not reversible.

This variable has nothing to do with the `SPINNERET_RETENTION_*` family, which covers the PostgreSQL
tables. See [Retention](#retention).

---

## Keys

The key-encryption keys (KEKs) that wrap every data-encryption key (DEK) in the deployment. Identity
payloads, proxy URLs, secret versions and notification credentials are each encrypted with their own DEK;
the DEK is wrapped with a KEK; only wrapped keys and ciphertext reach the database. The KEK itself never
does.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_KEK_FILE` | empty | Path to a key file of at most 1 MiB. Its content is either `id:base64` lines or a single bare base64 line, which then gets the id `k1`. Blank lines, `#` comments, surrounding whitespace and a leading byte order mark are handled. | This is the recommended form — see [Secrets in the environment](#secrets-in-the-environment). |
| `SPINNERET_KEKS` | empty | The same keys inline: `k1:base64,k2:base64`. Empty entries are ignored. | Only where a file secret is impossible. |
| `SPINNERET_KEK_CURRENT` | the id of the last key listed (file entries first, then inline entries) | Which key wraps **new** DEKs. Every configured key can still unwrap, which is what makes rotation online. | During a rotation, to move new writes to the new key before re-wrapping the old ones. |

At least one of `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` is required. Both may be set; the key sets are
merged, and listing the same id twice is allowed only with identical key material — otherwise startup
fails with `vault: kek "k1" is configured twice with different keys`. Every key must decode to exactly
32 bytes, from standard or URL-safe base64, padded or not.

```bash
spnr kek generate --id k1   # prints one "k1:<base64 of 32 random bytes>" line
spnr kek status             # how many stored records are wrapped with each key
spnr kek rewrap             # re-wrap every stored DEK with the current KEK
```

**Back the key up before you put data into the deployment, and again after every rotation.** A database
backup without the KEK is unrecoverable for every encrypted field. There is no recovery path, by design.

The Compose stack keeps the key in `deploy/compose/secrets/kek.key`, mounts it as the Docker secret
`kek` at `/run/secrets/kek`, and sets `SPINNERET_KEK_FILE` to that path. Why that file is mode `0644` on
purpose: [Installation → The key-encryption key](./02-installation.md#the-key-encryption-key). How to
rotate: [Operations → Key rotation](./16-operations.md#key-rotation). What the envelope scheme actually
is: [Secret vault](./10-secrets.md).

---

## Reports and streams

A `Report` is written to a Redis stream shard and processed asynchronously. These five variables shape
that pipeline.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_REPORT_SHARDS` | `16` | Number of Redis stream shards, 1–255. One shard has exactly one owning worker, so this bounds report parallelism. | Before the first start, and effectively never again. **Lease ids encode their shard**, so changing it strands in-flight leases — the procedure is [Operations → Changing the shard count](./16-operations.md#changing-the-shard-count). |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` | Window in which a repeated `report_id` is classified `duplicated` instead of being applied twice. Minimum `1m`. | Make it comfortably longer than your nodes' retry horizon. Shorten it when Valkey memory is the constraint: the dedup marker also pins the ended lease hash for the same window, which makes this the dominant traffic-proportional term of Valkey memory. See [Performance → Redis sizing](./17-performance.md#redis-sizing). |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` | A report that arrives later than this is still recorded and still counted in statistics, but no longer changes identity state. `0` disables the cutoff. | Raise it for nodes with long offline buffering. Set `0` only if you genuinely want a report from yesterday to cool an identity today. |
| `SPINNERET_STREAM_MAXLEN` | `1000000` | Approximate cap on the backlog of each shard, minimum 1000. Workers trim their shard to the consumer position, so a keeping-up deployment stays far below it; entries beyond the cap are dropped when workers cannot keep up. | Raise it if a planned worker outage should not lose reports; lower it to bound Valkey memory in the worst case. |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | `true` | Write a state event row for every cooldown, not only for bans, quarantines and expiries. | Turn it off at very high volume when state-event storage dominates. You lose the per-cooldown audit trail, not the cooldown. |

The shard count is the one setting on this page that is genuinely hard to change later. Size it from the
**worker count** you expect, not from the report rate: a shard has exactly one owner, so the rule is
**2–4 shards per planned worker instance**. Fewer shards than workers leaves workers idle; many more than
four per worker buys nothing and costs a consumer group each. Capacity is not the usual reason to go above
16: measured, one worker instance applies about **20,000 reports/s** inside the 200 ms target with the
default 16 shards. So 32 shards is the right choice for a planned 8–16 worker instances, and not free
below that — see [Performance → Tuning, in the order it pays](./17-performance.md#tuning-in-the-order-it-pays)
and [Operations → Report lag is growing](./16-operations.md#4-report-lag-is-growing).

---

## Caches

Three in-process caches trade memory for latency on the hot path. All are per instance; none is shared.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_PAYLOAD_CACHE` | `true` | Cache rendered credentials in process, so a repeat acquire of the same identity does not re-read and re-decrypt the payload. | Set `false` to keep decrypted payloads out of process memory — a hardening choice that costs a decrypt per acquire. |
| `SPINNERET_PAYLOAD_CACHE_SIZE` | `200000` | LRU entry limit, at least 1 while the cache is on. An entry is only served when the payload version, identity type id and type version all still match, so a payload edit or a type change invalidates it by construction. | Raise it when the working set of identities is larger than 200 000; lower it to cap memory. |
| `SPINNERET_DEK_CACHE_SIZE` | `100000` | LRU of unwrapped data keys, at least 1. Without it every payload read costs a KEK unwrap. | Same reasoning, one level down: size it to the number of distinct encrypted records in the working set. |
| `SPINNERET_DEK_CACHE_TTL` | `10m` | Lifetime of an entry in that cache. | Shorten it to reduce how long an unwrapped key lives in memory. |
| `SPINNERET_TOKEN_CACHE_TTL` | `30s` | How long a token verification result is cached. | Rarely. It bounds only the worst case: revoking a token publishes an event that drops the entry immediately on every instance, and this is what happens if that event is missed. |

A credential that resolved a `${secret:...}` reference is cached for at most 60 seconds regardless of the
size setting, so a secret rotation reaches nodes quickly without turning the cache off. That 60 s is not
configurable.

---

## Acquire admission control

An `Acquire` that fails costs roughly five times one that succeeds, because it walks candidates it cannot
use. Past the knee, a fleet that queues every request at Redis therefore degrades by *collapsing* rather
than by slowing down: more concurrency produces less throughput. Admission control bounds what the fleet
has in flight at Redis and sheds the excess with a retry hint instead of queueing it.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `64` | Concurrent acquire scripts the **whole fleet** may have in flight at Redis, 0–65536. Each API instance admits this budget divided by the number of live API instances it sees, clamped to `[4, 4096]`. Beyond the limit an acquire waits at most 50 ms for a permit and is then shed as `unavailable` with reason `overloaded` and a retry hint of 100–200 ms. `0` turns admission control off and restores unbounded behaviour — but only while `SPINNERET_ACQUIRE_MAX_INFLIGHT` is also `0`, because a pinned limit is applied before the fleet budget is ever consulted. | Raise it when you have measured that your Redis can take more. Set it to `0` as an incident lever if shedding is doing more harm than the congestion it prevents — and on a fleet that pins, set **both** to `0`, or the gate stays at the pinned value and the lever silently does nothing. |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `0` (derive) | Pin **this instance's** limit instead of dividing the fleet budget, 0–4096. | When Redis capacity scales with the number of primaries (set the per-primary figure), when you measured your own ceiling, or for a reproducible load test. |

Two failure modes are worth spelling out, because both are silent.

**Duplicate instance ids collapse the divisor.** The registry member name is `SPINNERET_INSTANCE_ID`, so
N instances sharing one id register as a single member. Each then sees one live peer, reports
`spinneret_acquire_peers = 1`, and admits the **whole** fleet budget — N times the budget in total. The
unique default prevents this; setting the variable by hand is what breaks it.

**Pinning on some instances but not others overshoots the budget.** A positive
`SPINNERET_ACQUIRE_MAX_INFLIGHT` also stops the heartbeat that counts live API instances, so a pinned
instance does not register as an acquirer. The instances that still derive then divide by too small a
count, and the fleet total exceeds the budget by the pinned instances' whole allocation. Pin it on
**every** API instance or on none.

Watch `spinneret_acquire_inflight_limit`, `spinneret_acquire_inflight`, `spinneret_acquire_queued` and
`spinneret_acquire_peers`. A limit that changes by itself is logged once per change with the old and new
peer count. The measurements behind the default and how to size it:
[Performance → Admission control](./17-performance.md#admission-control).

---

## Sessions and cookies

These two govern console sign-in. They have no effect on node API tokens, which do not use cookies at
all.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_SESSION_TTL` | `12h` | Console session lifetime, minimum `1m`. Expiry is sliding: an active session is extended, and one that is only extended while it still exists, so a refresh racing a sign-out cannot resurrect it. | Shorten it on a console reachable from an untrusted network; lengthen it for an internal tool people leave open. |
| `SPINNERET_COOKIE_SECURE` | `auto` | `auto` marks the session cookie `Secure` when the request arrived over TLS or with `X-Forwarded-Proto: https` from a trusted proxy; `true` always; `false` never. The cookie is always `HttpOnly` with `SameSite=Strict`. | Set `true` on anything published beyond loopback, and be explicit about it rather than relying on `auto`. `false` belongs only on a plain-HTTP intranet. |

`auto` depends on [`SPINNERET_TRUSTED_PROXIES`](#tls-and-trusted-proxies) being correct. If the proxy is
not trusted, its `X-Forwarded-Proto` is ignored, the request looks like plain HTTP, and the cookie is not
marked `Secure` — on an HTTPS deployment. Setting `SPINNERET_COOKIE_SECURE=true` removes that dependency
for the cookie, though not for the client IPs. The session model in full:
[Security → Console authentication and sessions](./19-security.md#console-authentication-and-sessions).

---

## TLS and trusted proxies

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_TLS_CERT_FILE` | empty | PEM certificate chain. Serving HTTPS directly enables HTTP/2 over TLS with a minimum of TLS 1.2. | When there is no reverse proxy to terminate TLS. |
| `SPINNERET_TLS_KEY_FILE` | empty | The matching private key. Must be set together with the certificate. | Together with the above. |
| `SPINNERET_TRUSTED_PROXIES` | empty | Comma-separated CIDR prefixes (IPv4 and IPv6) whose `X-Forwarded-For` and `X-Forwarded-Proto` headers are honoured. | **Required behind any reverse proxy.** |

The key pair is loaded at startup, so a broken or mismatched certificate fails the start with
`load TLS certificate <path>: …` instead of failing the first request. With no certificate the listener
is plain HTTP with unencrypted HTTP/2 (h2c) enabled, which is what the in-network load balancer speaks.

`SPINNERET_TRUSTED_PROXIES` is the variable most often forgotten and the one with the widest blast
radius. Leave it empty behind a proxy and every request appears to come from the proxy's address, which
means: token IP allowlists match the proxy instead of the node, login throttling counts the whole world
as one client, and every audit record and risk event carries the wrong source IP. Set it to the prefixes
your proxies actually come from — not `0.0.0.0/0`, which trusts a forged header from anyone who can
reach the port.

The Compose stack sets `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16`, which covers Docker's own bridge
networks and the private ranges an edge proxy on the same host would use. What a proxy in front must
also do — no buffering of long polls, no request timeout below the poll window — is in
[Installation → Reverse proxies and TLS](./02-installation.md#behind-a-reverse-proxy-and-tls).

---

## Metrics, profiling and tracing

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_METRICS_ADDR` | empty | Empty serves `/metrics` on the main listener, where it is **unauthenticated**. A value such as `:9091` serves it on a separate listener that carries nothing else. | Set it whenever the main listener is published: then the metrics port can be reachable only from the monitoring network. |
| `SPINNERET_PPROF_ADDR` | empty | Empty disables profiling entirely. A value such as `127.0.0.1:6060` serves `/debug/pprof/` on its own listener with no read timeout, so a CPU profile or execution trace can hold the response open. | Only for a profiling session, and then set it back. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty | Setting it enables OTLP trace export and installs the tracing interceptor on every RPC. The remaining standard `OTEL_*` variables are read by the SDK. | When you have a collector. It costs a little per request, so it is off by default. |

The pprof endpoints are unauthenticated and expose heap contents, goroutine stacks and the command
line. They are never mounted on the API or metrics listener, and configuration refuses an address equal
to either of those. Enabling it logs a warning —
`pprof debug listener enabled; keep this address unreachable from untrusted networks` — every start.
Bind it to loopback and reach it over an SSH tunnel.

Nothing sensitive is logged or exported at any level: payload fields, proxy credentials, secret values
and token material are redacted by the logger itself, not by the call sites. Which metrics to alert on
and what each one means: [Observability and alerting](./12-observability.md).

---

## Logging

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_LOG_LEVEL` | `info` | `debug`, `info`, `warn` (`warning` is accepted as a synonym) or `error`. | `debug` while diagnosing, and back afterwards — it is verbose on the hot path. |
| `SPINNERET_LOG_FORMAT` | `json` | `json` for structured output, `text` for a human reading a terminal. | `text` for a foreground process on a laptop; keep `json` wherever a log collector reads it. |

Both go to stderr. `spnr` always logs as `text` on stderr and keeps command output on stdout, so piping
`spnr` output into `jq` works regardless of this setting. How to read a Spinneret log line, and which
fields to grep for: [Troubleshooting](./18-troubleshooting.md).

---

## Proxy health checking and GeoIP

The health checker fetches one URL **through** every proxy on a period and marks the proxy active or
dead from the result. Concurrency within a run is fixed at 64 checks.

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` | The URL fetched through each proxy. | **On any host without general internet access.** Otherwise every proxy fails its check, is marked dead, and nodes get `no_proxy_available` with a perfectly healthy pool. Point it at something the proxies can actually reach. |
| `SPINNERET_PROXY_CHECK_INTERVAL` | `60s` | Period between checks, minimum `1s`. | Shorten it for volatile residential pools; lengthen it for a large stable pool to cut check traffic. |
| `SPINNERET_PROXY_CHECK_TIMEOUT` | `10s` | Per-proxy timeout, minimum `100ms`. | Lower it when slow proxies should be marked dead faster; raise it for high-latency exits. |
| `SPINNERET_PROXY_EXIT_IP_URL` | empty | A URL that returns the caller's own IP address. When set, each check records the proxy's exit IP. | When you want to see exit IPs in the console, detect two entries sharing one exit, or fill regions from GeoIP. |
| `SPINNERET_GEOIP_DB` | empty | Path to a MaxMind-format database, used to fill a proxy's region when it is empty and an exit IP is known. | When your provider does not tell you the region and you route by it. |

For the `test`, `loadtest` and `example` profiles the check URL should be the bundled mock target,
`http://mocktarget:9090/healthz` — which is what `scripts/example-quickstart.sh` writes into `.env`.
The pool model, assignment modes and what "dead" does to scheduling: [Proxies](./07-proxies.md).

---

## Retention

These five cover the partitioned PostgreSQL tables. The hourly `partition_manager` leader job keeps
future partitions rolling and drops those whose whole range is older than the retention, so a partition
is only dropped when every row in it has expired. Each value must be at least `24h`.

| Variable | Default | Tables it governs | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_RETENTION_RISK_EVENTS` | `720h` (30 days) | `risk_events` — the rows behind the console's risk feed | Lengthen it if you investigate incidents weeks later. |
| `SPINNERET_RETENTION_MINUTE_STATS` | `720h` (30 days) | `outcome_stats_minutely`, `acquire_stats_minutely`, `node_stats_minutely`, `payload_access_minutely` | This is the biggest of the five by row count. Shorten it first when PostgreSQL disk is the constraint. |
| `SPINNERET_RETENTION_HOUR_STATS` | `4320h` (180 days) | `identity_stats_hourly` — the long trends | Lengthen it for year-over-year comparison; it is cheap. |
| `SPINNERET_RETENTION_STATE_EVENTS` | `8760h` (365 days) | `state_events` — every identity and proxy state transition | Shorten it only if you do not need to explain why an identity was banned last quarter. |
| `SPINNERET_RETENTION_AUDIT` | `8760h` (365 days) | `audit_logs` — including every secret read | Usually a compliance decision, not a disk one. |

Raw request events are in ClickHouse and expire on `SPINNERET_CLICKHOUSE_TTL_DAYS` instead, quite
independently of these. Alert events are purged on a fixed 90-day window that is not configurable.

Lengthening a retention takes effect on the next hourly pass and costs nothing immediately — the data has
to accumulate. Shortening one drops partitions on the next pass, and dropped is dropped. Measuring what
each table actually costs per day: [Operations → Retention and disk](./16-operations.md#data-retention-and-reclaiming-disk).

---

## Request limits

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | `67108864` (64 MiB) | Body size limit of the admin RPCs, minimum 1 MiB. This is the limit a large identity or proxy import runs into. | Raise it for a very large one-shot import; lower it to reduce what an authenticated admin client can make the server buffer. |
| `SPINNERET_MAX_WATCHERS` | `20000` | Concurrent `WatchConfig` long polls this instance will hold open. Beyond it a caller gets `resource_exhausted` with reason `rate_limited` and a retry hint, which both SDKs back off from. | Raise it for a fleet larger than 20 000 watching nodes per instance; lower it to bound memory and file descriptors. |

Two limits nearby are **not** configurable and are worth knowing so you do not look for a variable:
node-facing RPC bodies are capped at 8 MiB, and request headers at 1 MiB.

---

## The console

| Variable | Default | What it does | When to change it |
| --- | --- | --- | --- |
| `SPINNERET_UI_ENABLED` | `true` | Serve the embedded console from the main listener. `false` leaves the APIs and the health endpoints only. | `false` on instances that only serve nodes, so the console is reachable on one address you can protect. |
| `SPINNERET_ALLOWED_ORIGINS` | empty | CORS allow-list of origins, e.g. `http://localhost:5173`. Empty installs no CORS middleware at all. `*` is accepted and allows any origin. | Only to run the console's development server against this instance. Leave it empty in production. |

A `worker`-role instance serves no console regardless of `SPINNERET_UI_ENABLED`, because it serves no
API. What the console contains and which document covers which page:
[Console overview](./05-console-overview.md).

---

## What the Compose stack sets for you

`deploy/compose/docker-compose.yml` sets these in the container environment of `migrate`, `spinneret`
and `init-admin`. You do not put them in `.env` — several of them are composed from other values, and
editing the Compose file is not the way to change them.

| Variable | Value in the stack | Note |
| --- | --- | --- |
| `SPINNERET_HTTP_ADDR` | `:8080` | Fixed; the published port is `SPINNERET_PORT` on the load balancer. |
| `SPINNERET_DATABASE_URL` | `postgres://spinneret:${PG_PASSWORD}@postgres:5432/spinneret?sslmode=disable` | `sslmode=disable` is fine on a private Compose network, not between hosts. |
| `SPINNERET_REDIS_URL` | `redis://valkey:6379/0` | |
| `SPINNERET_CLICKHOUSE_URL` | `clickhouse://spinneret:${CLICKHOUSE_PASSWORD}@clickhouse:9000/spinneret` | |
| `SPINNERET_KEK_FILE` | `/run/secrets/kek` | The Docker secret from `deploy/compose/secrets/kek.key`. |
| `SPINNERET_TRUSTED_PROXIES` | `172.16.0.0/12,10.0.0.0/8,192.168.0.0/16` | Covers the Compose bridge networks and a host-local edge proxy. |
| `SPINNERET_DATABASE_MAX_CONNS` | `${SPINNERET_DATABASE_MAX_CONNS:-32}` | Overridable from `.env`. |
| `SPINNERET_LOG_LEVEL` | `${SPINNERET_LOG_LEVEL:-info}` | Overridable from `.env`. |
| `SPINNERET_COOKIE_SECURE` | `${SPINNERET_COOKIE_SECURE:-auto}` | Overridable from `.env`. |
| `SPINNERET_REPORT_SHARDS` | `${SPINNERET_REPORT_SHARDS:-16}` | Overridable from `.env`. |
| `SPINNERET_REPORT_DEDUP_TTL` | `${SPINNERET_REPORT_DEDUP_TTL:-1h}` | Overridable from `.env`. |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | `${SPINNERET_ACQUIRE_FLEET_INFLIGHT:-64}` | Overridable from `.env`. |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | `${SPINNERET_ACQUIRE_MAX_INFLIGHT:-0}` | Overridable from `.env`. |
| `SPINNERET_PROXY_CHECK_URL` | `${SPINNERET_PROXY_CHECK_URL:-}` | Empty passes through as unset, so the built-in default applies. |

Any other variable on this page you want in a Compose deployment goes into a small override file of your
own, passed after the shipped file, rather than into edits of `docker-compose.yml` that an upgrade will
move under you:

```yaml
# deploy/compose/compose.local.yml
services:
  spinneret:
    environment:
      SPINNERET_METRICS_ADDR: ":9091"
      SPINNERET_SESSION_TTL: "4h"
      SPINNERET_RETENTION_MINUTE_STATS: "168h"
```

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/compose.local.yml up -d
```

`SPINNERET_INSTANCE_ID` is deliberately **not** passed through: each replica gets a container hostname,
the default derives a unique id from it, and templating one value across replicas would break the acquire
budget. See [Across replicas](#across-replicas-same-different-tunable).

---

## Compose-only and installer-only variables

These are read by the Compose file or by the installer, never by the server. They live in
`deploy/compose/.env`, which `scripts/compose-init.sh` generates from `.env.example` with random
passwords, and which is git-ignored and excluded from the Docker build context.

| Variable | Default | What it does |
| --- | --- | --- |
| `PG_PASSWORD` | generated | Interpolated into `SPINNERET_DATABASE_URL` and given to the `postgres` container. Honoured only while the data directory is empty: changing it later needs the change inside the database too. |
| `CLICKHOUSE_PASSWORD` | generated | The same for ClickHouse. |
| `SPINNERET_ADMIN_USERNAME` | `admin` | Read once by the one-shot `init-admin` service. |
| `SPINNERET_ADMIN_PASSWORD` | generated | The same. Change it after the first sign-in; until you do, it also sits here in clear. |
| `SPINNERET_PORT` | `8080` | Host port of the load balancer. |
| `SPINNERET_REPLICAS` | `2` | Number of `spinneret` replicas. |
| `VALKEY_IO_THREADS` | `4` | Valkey's `io-threads`. `1` restores single-threaded behaviour — the measurement is in [Performance → Valkey io-threads](./17-performance.md#valkey-io-threads). **Not in `.env.example`**: it exists only as an interpolation in `docker-compose.yml`, so add the line to `.env` yourself to change it. |
| `PROMETHEUS_PORT` | `9090` | Host port of the `observability` profile's Prometheus. |
| `MOCK_TARGET_PORT` / `MOCK_PROXY_PORT` | `19090` / `19091` | Host ports of the mock target site and its authenticating proxy. |
| `EXAMPLE_PORT` | `18000` | Host port of the example crawler. |
| `EXAMPLE_TOKEN` | empty | Node token for the example crawler, written by `scripts/example-quickstart.sh`. |
| `LOADTEST_TOKEN` | empty | Node token for the k6 scenarios; `spnr seed` prints one. |
| `K6_SCRIPT` | `acquire_report.js` | Which scenario the `loadtest` profile runs. **Not in `.env.example`**: like `VALKEY_IO_THREADS` it is only an interpolation in `docker-compose.yml`; add the line to `.env` yourself. |

The guided installer writes up to three of its own into `.env` — `SPINNERET_BIND_HOST` always, plus
`SPINNERET_IMAGE` and `SPINNERET_IMAGE_TAG` when it installs the published image rather than building from
a checkout — and reads the rest only from your shell:

| Variable | Default | What it does |
| --- | --- | --- |
| `SPINNERET_BIND_HOST` | `127.0.0.1` | The address the load balancer publishes on, `127.0.0.1` or `0.0.0.0`. Written to `.env`, read by the generated `compose.host.yml`. **The shipped `docker-compose.yml` does not read it**: it publishes `"${SPINNERET_PORT:-8080}:8080"` with no host part, so on a manual Compose stack this variable does nothing and you need the override in [Three worked configurations](#three-worked-configurations). |
| `SPINNERET_IMAGE` | `ghcr.io/tikhub/spinneret` | Image repository, for a private mirror or a fork. Read by the generated `compose.image.yml`. |
| `SPINNERET_IMAGE_TAG` | `latest` | Image tag. Pin an exact one for production. Read by the same generated `compose.image.yml`. |
| `SPINNERET_PROJECT` | `spinneret` | Compose project name, also how an existing install is found. |
| `SPINNERET_INSTALL_DIR` | `/opt/spinneret` as root, `~/spinneret` otherwise | Where to install. |
| `SPINNERET_ENABLE_OBSERVABILITY` | `0` | `1` adds the Prometheus profile. |
| `SPINNERET_USE_PUBLISHED` | `1` | `1` pulls the published image, `0` builds from the checkout. |
| `NO_COLOR` | unset | Set to anything to turn colour off. |

`.env.example` carries a comment for every variable it defines and is the file `.env` is generated from,
so anything added to it later shows up in the next install. The installer in full:
[Installation → The guided installer](./02-installation.md#the-guided-installer).

---

## Across replicas: same, different, tunable

Instances of one deployment are stateless and interchangeable, but they are not independent. Three
categories:

### Must be identical on every instance

| Variable | Why |
| --- | --- |
| `SPINNERET_DATABASE_URL` | Same database, obviously — but also: two pools pointing at different databases produce two disjoint deployments that look like one. |
| `SPINNERET_REDIS_URL` / `SPINNERET_REDIS_ADDRS` | Same hot state. |
| `SPINNERET_REDIS_PREFIX` | A different prefix is a different hot state under the same Redis. |
| `SPINNERET_KEK_FILE` / `SPINNERET_KEKS` / `SPINNERET_KEK_CURRENT` | An instance that lacks a key cannot unwrap the records another instance wrote with it, and fails those reads. Distribute the whole key set everywhere. |
| `SPINNERET_REPORT_SHARDS` | Lease ids encode their shard. Instances disagreeing about the count write leases the others cannot resolve. |
| `SPINNERET_REPORT_DEDUP_TTL`, `SPINNERET_LATE_REPORT_WINDOW`, `SPINNERET_STREAM_MAXLEN` | Each instance ingests with its own values and each shard has its own owner, so a disagreement makes deduplication, the late cutoff and the backlog cap depend on **which instance happened to handle the report**. That is an unreproducible classification, not a per-instance tuning knob. |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | The worker that owns the shard decides whether the cooldown gets a state-event row. Mixed values make the audit trail depend on which worker ran. |
| `SPINNERET_PROXY_CHECK_URL`, `SPINNERET_PROXY_CHECK_INTERVAL`, `SPINNERET_PROXY_CHECK_TIMEOUT`, `SPINNERET_PROXY_EXIT_IP_URL`, `SPINNERET_GEOIP_DB` | The health checker runs on **every** worker-capable instance, and all of them write the same proxy rows. Different check URLs, periods or timeouts mean instances reach opposite verdicts about the same proxy and flip it between `active` and `dead` — the flapping the [proxy check](#proxy-health-checking-and-geoip) section warns about, with no bad proxy behind it. |
| `SPINNERET_CLICKHOUSE_URL`, `SPINNERET_CLICKHOUSE_TTL_DAYS` | Same table, one TTL. Two values make the startup migration flip the TTL back and forth. |
| `SPINNERET_RETENTION_*` | The partition job is a leader job, so whichever instance holds the lock decides — which makes a disagreement a lottery instead of a setting. |
| `SPINNERET_ACQUIRE_FLEET_INFLIGHT` | It is a fleet-wide budget that each instance divides locally. Different values mean different divisions of a budget that is supposed to be one number. |
| `SPINNERET_ACQUIRE_MAX_INFLIGHT` | Pin it everywhere or nowhere. Mixed, the fleet overshoots its budget — see [Acquire admission control](#acquire-admission-control). |
| `SPINNERET_TRUSTED_PROXIES` | Client IP attribution must not depend on which replica answered. |
| `SPINNERET_COOKIE_SECURE`, `SPINNERET_SESSION_TTL` | A console session survives being load-balanced to a different replica; the cookie must mean the same thing on all of them. |

### Must differ

| Variable | Why |
| --- | --- |
| `SPINNERET_INSTANCE_ID` | It is the acquire-registry member name, the Redis stream consumer name and the job-lease owner. Duplicates collapse the acquire divisor and make two consumers fight over one pending entry list. **Leave it unset** and the default is unique per host; only template it per replica if you must set it at all. |

### Can differ on purpose

| Variable | Why you would |
| --- | --- |
| `SPINNERET_ROLE` | The whole point of a split deployment. |
| `SPINNERET_HTTP_ADDR`, `SPINNERET_METRICS_ADDR`, `SPINNERET_PPROF_ADDR` | Different hosts, different interfaces; pprof enabled on one instance for one session. |
| `SPINNERET_UI_ENABLED` | Console on the instances behind your admin ingress, off on the ones serving nodes. |
| `SPINNERET_TLS_CERT_FILE`, `SPINNERET_TLS_KEY_FILE` | Only the instances that terminate TLS need a certificate. |
| `SPINNERET_LOG_LEVEL`, `SPINNERET_LOG_FORMAT` | `debug` on one instance while you watch it. |
| `SPINNERET_DATABASE_MAX_CONNS` | A `worker` instance needs a bigger pool than an `api` instance; a small `api` box needs less. |
| `SPINNERET_PAYLOAD_CACHE`, `SPINNERET_PAYLOAD_CACHE_SIZE`, `SPINNERET_DEK_CACHE_SIZE`, `SPINNERET_DEK_CACHE_TTL`, `SPINNERET_TOKEN_CACHE_TTL` | Per-instance memory and per-instance staleness. A worker-only instance barely uses the payload cache. |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | Only the instances behind your admin ingress take admin RPCs, and a big import can be pointed at one of them. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Trace one instance, or send different tiers to different collectors. |
| `SPINNERET_SHUTDOWN_TIMEOUT` | Match each instance's orchestrator kill timeout. |
| `SPINNERET_MAX_WATCHERS` | Per-instance capacity; size it to the instance, not the fleet. |
| `SPINNERET_ALLOWED_ORIGINS` | Only the instance a developer points their dev server at needs it. |

A useful habit: keep the "must be identical" set in one file that every instance loads, and the rest in a
per-instance file. Then a divergence is a diff, not a discovery.

---

## Secrets in the environment

Four values in this reference are secrets: the PostgreSQL password, the ClickHouse password, the
key-encryption keys, and — while it is still the generated one — the administrator password.

**Never do these:**

- Type a secret on a command line. `SPINNERET_KEKS=k1:… spinneret-server` lands in your shell history
  and in `/proc/<pid>/cmdline`, and `ps` shows the environment of your own processes on most systems.
- Bake one into an image. `ENV SPINNERET_KEKS=…` or a `COPY` of a key file puts it in a layer that
  anybody who can pull the image can read, forever, including after you "removed" it in a later layer.
- Commit a `.env`. `deploy/compose/.env` and `deploy/compose/secrets/` are git-ignored and excluded from
  the Docker build context for this reason; keep it that way in your own overlays.
- Put a KEK in a CI variable that logs command output. `SPINNERET_KEK_FILE` and a mounted file leave
  nothing to echo.

**Prefer a file over an inline value.** `SPINNERET_KEK_FILE` points at a path; `SPINNERET_KEKS` carries
the key material itself. The file form keeps the key out of the process environment, out of
`docker inspect`, out of an orchestrator's serialized pod spec, and out of any crash dump that
serializes the environment. This is why the Compose stack uses a Docker **file secret**:

```yaml
# deploy/compose/docker-compose.yml
x-spinneret-env: &spinneret-env
  SPINNERET_KEK_FILE: /run/secrets/kek
  # …

services:
  spinneret:
    environment: *spinneret-env
    secrets: [kek]

secrets:
  kek:
    file: ./secrets/kek.key
```

The environment carries a path, the key arrives as a file inside the container, and `docker inspect` on
the service shows `/run/secrets/kek` rather than 32 bytes of base64. `scripts/compose-init.sh` generates
that file with a fresh key on first run, and the mode is deliberately `0644`: Compose bind-mounts this
exact file into a container that runs as the distroless `nonroot` user, which matches no host user and
cannot read a `0600` file. The protection therefore belongs on the directory. The guided installer sets
that (`chmod 0700 deploy/compose/secrets`); `scripts/compose-init.sh` does not, so on a manual Compose
stack do it yourself:

```bash
chmod 0700 deploy/compose/secrets
```

Details: [Installation → The key-encryption key](./02-installation.md#the-key-encryption-key).

**What the software does to help.** Everything that prints configuration redacts it. `spnr config check`
and the startup log line replace the password in any URL or libpq DSN with `xxxxx`, mask the query
parameters `password`, `passwd`, `pass`, `pwd`, `secret`, `token`, `sslpassword`, `access_token`,
`api_key` and `apikey`, and report inline key material only as `<set>`. A KEK parse error carries the
line number, never the line. That is a safety net, not a strategy: a secret that is never in the
environment cannot be leaked by something that forgot to redact.

The threat model this sits inside, and what the operator is responsible for:
[Security](./19-security.md).

---

## What is not an environment variable

A deliberate line. Knowing which side a thing is on saves looking in the wrong place:

| What you want to change | Where it lives |
| --- | --- |
| How identities are chosen, leased, cooled down, banned; what a marker means; when a breaker trips and how it recovers | **Policies** — versioned, published at runtime, roll back in one click. [Policies](./08-policies.md) |
| What your nodes are configured with | The **configuration center** — long-polled by nodes, live in about a second. [Configuration center](./09-config-center.md) |
| Credentials your nodes need, referenced as `${secret:path}` | The **secret vault**. [Secret vault](./10-secrets.md) |
| Which proxies exist, their regions, tags and assignment mode | The **proxy pool**. [Proxies](./07-proxies.md) |
| Who may do what, and what a token may reach | Users, roles, role bindings, token scopes and IP allowlists. [Tenants, users and tokens](./11-access-control.md) |
| Where alerts go and what triggers them | Notification channels and alert rules, per site. [Observability and alerting](./12-observability.md) |
| How long a ban lasts, how long a cooldown lasts | A policy, not `SPINNERET_RETENTION_*`. [Policies](./08-policies.md) |

None of those needs a restart. That separation is the reason a cooldown rule can change in seconds
without redeploying a single node — and the reason this page is as short as it is.

---

## Three worked configurations

Each of these is a real `.env` excerpt showing **only what differs from the defaults**. Everything not
listed is the default, and a default that works is not worth writing down.

### 1. Single-host evaluation

One machine, the Compose stack, console on loopback, reached over an SSH tunnel. The goal is to spend no
thought on tuning.

```bash
# deploy/compose/.env — everything else stays as scripts/compose-init.sh generated it
PG_PASSWORD=<generated>
CLICKHOUSE_PASSWORD=<generated>
SPINNERET_ADMIN_USERNAME=admin
SPINNERET_ADMIN_PASSWORD=<generated>

# One replica is plenty for an evaluation and halves the memory.
SPINNERET_REPLICAS=1
SPINNERET_PORT=8080
```

The shipped `docker-compose.yml` publishes `"${SPINNERET_PORT:-8080}:8080"` with **no host part**, so that
port is on every interface of the machine. `SPINNERET_BIND_HOST` does not fix this on a manual stack —
only the installer-generated `compose.host.yml` reads it. Bind it yourself with a one-service override, and
keep using that file for everything else the stack does not pass through:

```yaml
# deploy/compose/compose.local.yml
services:
  lb:
    # "!override" replaces the published-port list instead of appending to it: a plain
    # merge would leave two mappings for container port 8080 and the second would fail
    # to bind. Needs Compose 2.24 or newer.
    ports: !override
      - "127.0.0.1:${SPINNERET_PORT:-8080}:8080"
```

Then reach the console over a tunnel:

```bash
ssh -L 8080:127.0.0.1:8080 <host>
```

`SPINNERET_COOKIE_SECURE=auto` is correct here because the console is plain HTTP on loopback, the built-in
proxy check URL works if the host has internet access, and the shipped `SPINNERET_TRUSTED_PROXIES` already
covers the Compose network the Caddy load balancer sits on.

If the host has **no** internet access, add one line, or every proxy you import will be marked dead:

```bash
# The bundled mock target is reachable from inside the stack (profiles test/loadtest/example).
SPINNERET_PROXY_CHECK_URL=http://mocktarget:9090/healthz
```

Start it:

```bash
cd deploy/compose
chmod 0700 secrets
docker compose -f docker-compose.yml -f compose.local.yml up -d --build --wait
docker compose -f docker-compose.yml -f compose.local.yml --profile init run --rm init-admin
docker compose -f docker-compose.yml -f compose.local.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate config check
```

**Note.** The guided installer does all of the above for you — it writes `compose.host.yml` from
`SPINNERET_BIND_HOST`, sets the directory mode, and leaves a `spnrctl` wrapper that carries the `-f` flags.
This configuration is the manual path, for a host where you want to see every file.

The step-by-step version of this, ending in a node that acquires and reports:
[Quick start](./01-quickstart.md).

### 2. Two replicas in production behind a load balancer

Compose again, but published through an edge proxy that terminates TLS, with metrics on their own port
and a retention profile that fits the disk.

```bash
# deploy/compose/.env
PG_PASSWORD=<generated>
CLICKHOUSE_PASSWORD=<generated>
SPINNERET_ADMIN_USERNAME=ops
SPINNERET_ADMIN_PASSWORD=<generated, changed after the first sign-in>

SPINNERET_REPLICAS=2
SPINNERET_PORT=8080

# TLS is terminated at the edge; be explicit rather than relying on the
# forwarded-header heuristic.
SPINNERET_COOKIE_SECURE=true

# 2 replicas x 48 = 96 connections, comfortably under max_connections=300.
SPINNERET_DATABASE_MAX_CONNS=48
```

Everything else goes in an override file, because the Compose stack does not pass it through — including
the bind address and the image, which the shipped `docker-compose.yml` does not read from `.env` at all:

```yaml
# deploy/compose/compose.local.yml
services:
  lb:
    # The edge proxy runs on this host and connects over loopback, so the stack must
    # not publish on every interface. "!override" replaces the port list rather than
    # appending to it; needs Compose 2.24 or newer.
    ports: !override
      - "127.0.0.1:${SPINNERET_PORT:-8080}:8080"
  # Run the published image at an exact tag instead of building from the checkout, so
  # you can say which build is running and put it back. "build: !reset null" drops the
  # build section so a compose build cannot quietly rebuild over the pulled image.
  # The guided installer writes exactly this as compose.image.yml from
  # SPINNERET_IMAGE / SPINNERET_IMAGE_TAG.
  migrate:
    image: ghcr.io/tikhub/spinneret:v0.1.0
    build: !reset null
  init-admin:
    image: ghcr.io/tikhub/spinneret:v0.1.0
    build: !reset null
  spinneret:
    image: ghcr.io/tikhub/spinneret:v0.1.0
    build: !reset null
    environment:
      # /metrics off the published listener, reachable only from the monitoring network.
      SPINNERET_METRICS_ADDR: ":9091"
      # A shorter console session on a published console.
      SPINNERET_SESSION_TTL: "4h"
      # Nodes retry within two minutes, so an hour of dedup markers is memory
      # spent on nothing. See documents/en/17-performance.md.
      SPINNERET_REPORT_DEDUP_TTL: "15m"
      # The minute tables dominate PostgreSQL disk; two weeks is enough for the
      # dashboards, and the hourly tables keep the long trends.
      SPINNERET_RETENTION_MINUTE_STATS: "336h"
      # Under the orchestrator's kill timeout; stop_grace_period is 40s.
      SPINNERET_SHUTDOWN_TIMEOUT: "35s"
```

```bash
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/compose.local.yml up -d --wait
```

`SPINNERET_INSTANCE_ID` is absent on purpose: each replica's container hostname produces a distinct
default, which is exactly what the acquire budget needs. `SPINNERET_TRUSTED_PROXIES` is absent because
the shipped value already covers the Compose network; add your edge proxy's prefix to it if the proxy is
on a different network.

### 3. Split api / worker

A hand-made deployment — systemd units, a Kubernetes Deployment pair, whatever schedules processes —
where request serving and the report pipeline are separate so a report backlog cannot slow down
`Acquire`. Three files: one shared, two per-role.

```bash
# /etc/spinneret/common.env — identical on every instance
SPINNERET_DATABASE_URL=postgres://spinneret@db.internal:5432/spinneret?sslmode=verify-full
SPINNERET_REDIS_URL=rediss://:@valkey.internal:6379/0
SPINNERET_CLICKHOUSE_URL=clickhouse://spinneret@clickhouse.internal:9000/spinneret
SPINNERET_KEK_FILE=/etc/spinneret/kek.key

SPINNERET_REPORT_SHARDS=32
SPINNERET_TRUSTED_PROXIES=10.40.0.0/16
SPINNERET_COOKIE_SECURE=true
SPINNERET_ACQUIRE_FLEET_INFLIGHT=96
SPINNERET_RETENTION_MINUTE_STATS=336h
```

```bash
# /etc/spinneret/api.env — loaded after common.env on the 4 API instances
SPINNERET_ROLE=api
SPINNERET_HTTP_ADDR=0.0.0.0:8080
SPINNERET_METRICS_ADDR=127.0.0.1:9091
# api instances serve nodes and the console; they run no leader jobs, so the
# pool only has to carry request concurrency.
SPINNERET_DATABASE_MAX_CONNS=24
# 4 API instances x 24 = 96, plus the workers below, under max_connections.
SPINNERET_MAX_WATCHERS=40000
```

```bash
# /etc/spinneret/worker.env — loaded after common.env on the 2 worker instances
SPINNERET_ROLE=worker
# Health and metrics only; no node API, no console.
SPINNERET_HTTP_ADDR=127.0.0.1:8080
SPINNERET_METRICS_ADDR=127.0.0.1:9091
SPINNERET_UI_ENABLED=false
# Leader jobs each hold a connection while leading, so a worker needs headroom
# an api instance does not. Startup refuses a pool that is too small and names
# the minimum.
SPINNERET_DATABASE_MAX_CONNS=48
# Nothing acquires here, so the payload cache earns little.
SPINNERET_PAYLOAD_CACHE_SIZE=20000
# A report backlog needs longer to flush than an api instance needs to drain.
SPINNERET_SHUTDOWN_TIMEOUT=60s
```

Notes that matter for this shape:

- `SPINNERET_ACQUIRE_FLEET_INFLIGHT=96` is divided by the number of live **API** instances — four here,
  so 24 each. Worker instances do not acquire and do not count. Scale the API tier and the division
  follows within a heartbeat; no configuration change.
- `SPINNERET_INSTANCE_ID` is set nowhere. Every process has a distinct hostname, so every default is
  distinct. If your scheduler gives two processes the same hostname, template the id from the pod or
  unit instance name.
- Load balancer health checks go to `/readyz`, not `/healthz`: `/readyz` is what turns `draining`
  during the shutdown drain window, and it reports PostgreSQL, Redis, catalog and hot-state readiness.
- Only the API instances need TLS material if you terminate at the edge, and only they need
  `SPINNERET_ALLOWED_ORIGINS` if a developer ever points a dev server at one.
- `spnr config check` with each of these environments loaded, before any of it starts:
  `set -a; . /etc/spinneret/common.env; . /etc/spinneret/worker.env; set +a; spnr config check`.

Capacity planning for the tiers and what to watch after the split:
[Operations → Scaling](./16-operations.md#scaling) and [Performance](./17-performance.md).

---

## Next

- [Installation and deployment](./02-installation.md) — where these variables are actually set, service
  by service.
- [Operations runbook](./16-operations.md) — the day-2 procedures that change some of them.
- [Performance and tuning](./17-performance.md) — which of them matter under load, with measurements.
- [Security](./19-security.md) — the settings on this page that are security decisions.
- [CLI reference](./15-cli.md) — `spnr config check`, `spnr kek`, and every other command.
- [Troubleshooting](./18-troubleshooting.md) — when a valid configuration still does not work.
