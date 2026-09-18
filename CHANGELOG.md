# Changelog

All notable changes to Spinneret are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

## [Unreleased]

### Added

- **Per-instance admission control on the Acquire path**, which turns the two-replica collapse into a
  plateau. `acquire.lua` concurrency at Redis is now bounded: `SPINNERET_ACQUIRE_FLEET_INFLIGHT`
  (default `64`, range 0–65536) is the budget for the *whole fleet*, and each API instance admits that
  number divided by the live API instances it sees in a Redis heartbeat set, clamped to `[4, 4096]`.
  `SPINNERET_ACQUIRE_MAX_INFLIGHT` (default `0`, max 4096) pins one instance's limit instead and stops
  its heartbeat — pin it on every API instance or on none. `SPINNERET_ACQUIRE_FLEET_INFLIGHT=0` turns
  admission control off and reproduces the pre-gate behaviour exactly, which is the A/B lever for
  measuring it on one build. Release, renew, reap and ingest are deliberately left ungated: that
  asymmetry is the mechanism, because it bounds the queue a `release.lua` waits behind.
- An attempt waits up to 50 ms — never longer than its own remaining `wait_ms` — for a permit, and the
  time it spent waiting is credited against its retry rung, so a given `wait_ms` still buys the same
  number of attempts it did before. What cannot be admitted is shed with the new reason
  **`overloaded`** (`unavailable`, HTTP 503) and a retry hint jittered into 100–200 ms. A shed issues
  **zero Redis commands**, which is why retrying it is always safe and why it cannot deepen the queue.
  `AcquireBatch` takes `count` permits, so a 50-lease batch is charged for the Lua work it really does.
- New statistics result `overloaded`, distinct from `exhausted`: `exhausted` now means the identity
  pool is empty (add identities, widen the rotation policy) and `overloaded` means the instance is at
  its acquire concurrency limit (add capacity, or offer less load). Migration
  `00006_acquire_result_overloaded.sql` widens the `acquire_stats_minutely` result constraint;
  ClickHouse needs no change. The console's acquire failure ratio counts the new result.
- New metrics: `spinneret_acquire_script_seconds` (the `acquire.lua` round trip alone, sharing buckets
  with `spinneret_acquire_duration_seconds`), `spinneret_acquire_admission_total{result}`
  (`immediate`, `queued`, `shed_no_wait`, `shed_queue_full`, `shed_timeout`, `shed_canceled`),
  `spinneret_acquire_admission_wait_seconds`, and the gauges `spinneret_acquire_inflight`,
  `_queued`, `_inflight_limit`, `_peers`, `_peer_beat_age_seconds` and `_peer_beat_failures_total`.
  The gauges exist only while admission control is on; `_peers` is also absent when the limit is
  pinned. `spinneret_acquire_total{result="exhausted"}` is **no longer the collapse detector** — alert
  on `overloaded` for overload and `exhausted` for pool exhaustion, and exclude `overloaded` from
  acquire-failure alerts, since shedding is a healthy response to overload.

- **The project is open source**, maintained by [TikHub](https://github.com/TikHub) under the
  [Apache License 2.0](LICENSE). `LICENSE`, `SECURITY.md` (private vulnerability reporting),
  `CONTRIBUTING.md`, GitHub issue and pull-request templates, and a `release.yml` workflow that builds
  a multi-architecture image for `linux/amd64` and `linux/arm64` and pushes it to
  `ghcr.io/tikhub/spinneret` on a `v*` tag are all new.
- **A guided one-command installer**, `install/install.sh` with an identical Chinese twin
  `install/install.zh.sh`. Docker only: it detects the host, offers to install Docker, clones the
  repository, generates every password and the key-encryption key on the machine, writes the Compose
  overrides for this host, pulls the published image or falls back to building from source, runs the
  migrations, waits for `/readyz`, creates the first administrator, and writes a `spnrctl` control
  script. Re-running it opens a management menu: status, upgrade, accounts, tokens, hot-state rebuild,
  configuration, health, logs, restart, backup, restore, disk and uninstall. `install/README.md`
  documents every question, flag and file it writes.
- **A complete bilingual manual in `documents/`**: 21 pages in English and Simplified Chinese, from a
  quick start to a performance and tuning guide, with an index in `documents/README.md`. Every page was
  verified against the source. `docs/` is now local working notes and is no longer tracked.
- `make help` lists the developer tasks, and `make web-test` runs the console gates that CI runs.

### Changed

- **The Go module path is now `github.com/TikHub/Spinneret`** (was `github.com/Evil0ctal/Spinneret`),
  matching the repository the project is published from. Import paths in the SDKs change with it.
- **Redis hot path rewritten; every v0.1 performance target is now met on one server instance and one
  Redis instance.** One acquire→report cycle costs **168.3 µs of Redis CPU instead of 271.2 µs**, i.e.
  **5,940 cycles/s per Redis thread instead of 3,690**. Per script, measured with `SLOWLOG` at
  1,000 cycles/s on the 100k × 50 dataset: `acquire.lua` 104.2 → 68.3 µs (−34 %), `observe.lua`
  71.7 → 43.5 (−39 %), `release.lua` 67.0 → 34.0 (−49 %), `ingest.lua` 16.3 → 12.2 (−25 %),
  `lease_retain.lua` 12.0 → 10.3 (−14 %). The scheduler-path changes are in `internal/scheduler/lua/**`
  and `internal/store/redis/lua/common.lua`; the worker-path changes in `internal/worker/lua/observe.lua`,
  `internal/action/lua/apply.lua`, `internal/signal/lua/*.lua` and `internal/breaker/lua/breaker_eval.lua`.
  One encoding change came with it: `xg` now names the endpoint groups that pushed an identity's
  ready-queue score forward, instead of `"1"` meaning all of them.
- End-to-end effect, one instance, 100k identities × 50 endpoint groups
  (`documents/en/17-performance.md`, before → after):
  Acquire alone **4,436/s at p99 > 250 ms → 4,993/s at p99 4.32 ms** (peak 4,614 → 7,792/s);
  full acquire→report cycle **2,000/s → 4,500/s** with exclusive leases and **3,000/s → 5,500/s** with
  `max_concurrent_leases: 4`; report ingest **29,584/s → 44,437/s** accepted; reports applied to the hot
  state within p99 200 ms **~9,846/s → 19,761/s**; report lag p99 at ~10,000 reports/s **21.5 → 9.5 ms**.
- **Valkey tuning in `deploy/compose`**, each change measured: `io-threads 4`
  (`VALKEY_IO_THREADS` to override) — +3 % throughput but acquire p99 18.9 → 4.1 ms and report lag p99
  4.66 s → 41 ms at 6,000 cycles/s, for one extra core; RDB `save` points disabled, since the AOF is the
  durability mechanism and the two forked for the same data; `auto-aof-rewrite-percentage 300` and
  `auto-aof-rewrite-min-size 1gb`, because a hot state of short-lived keys triggered an AOF rewrite every
  52 s at 4,000 cycles/s and each rewrite fork was enough to tip the system into congestion collapse.
- ClickHouse in `deploy/compose` gets `config/clickhouse-limits.xml`: its default 5 GiB mark cache is sized
  for a dedicated analytics host, and unconstrained it was OOM-killed during a load run, freezing the whole
  Docker VM. `max_server_memory_usage` is a 1.5 GiB safety net, deliberately above the image's idle RSS —
  set below it, every INSERT fails and the report worker stalls.
- `SPINNERET_REPORT_DEDUP_TTL` is now settable from `deploy/compose/.env`. It is the dominant
  traffic-proportional term of Redis memory: at 4,500 cycles/s the default 1 h costs about 5.4 GiB.
- `test/load/run.sh` takes `MID_STATS=0`, which drops the `docker stats` part of the mid-run snapshot.
  That call walks every container of the machine and, on Docker Desktop, stalled the system under test for
  ~5 s at 4,000 cycles/s — enough to turn a 1.9 ms p99 into 11.9 ms. Use it for any run near the knee.

### Notes

- **The remaining bottleneck is not Redis CPU.** Two replicas behind the load balancer reach 3,000
  cycles/s in aggregate, *less* than one replica alone, because each instance drives its own unbounded
  concurrency at the shared Redis and a failing acquire costs about five times a succeeding one (220 Redis
  commands versus 49). The first lever, per-instance admission control on the Acquire path, has landed
  (see Added above) and bounds that loop; the remaining levers, in order, are making a rejected
  candidate cheap and then Redis Cluster.
- **A freshly seeded or rebuilt site must be warmed before it is benchmarked.** Seeding writes the same
  ready-queue score for every identity in every endpoint group, so all groups contend on the same head;
  a cold site collapsed at 2,500 cycles/s where the warmed one carried 4,500/s. About a million acquires
  of ordinary traffic removes it. See `documents/en/17-performance.md`.
- **Never unlink `sp:{<site>}:ls:*` by hand** (nor trim a stream holding unprocessed `release: true`
  reports): the lease hash is what decrements the identity's active-lease counter, so deleting live ones
  makes those identities permanently unavailable. `spnr rebuild --site` does not reset runtime counters by
  design; the repair is to unlink the whole site prefix first and then rebuild.

## [0.1.0] — 2026-09-17

First release. Spinneret is a control plane for multi-node crawlers and API nodes: it leases identities and
proxies to nodes, turns the request results they report into cooldowns, bans, health scores and circuit
breaking, and distributes versioned configuration and secrets. Nodes need only a server URL and an API
token.

### Added

**Core path — identity scheduling and reporting**

- `LeaseService` (`Acquire`, `AcquireBatch`, `Renew`, `Release`): candidate sampling, availability filtering
  and lease writing in a single atomic Redis Lua script, with no PostgreSQL round trip on the hot path.
- Rotation policies: strategies `weighted_random`, `least_recently_used`, `round_robin`, `best_health`;
  lease TTL and lifetime cap, concurrency limit, reuse interval with `acquired`/`released` anchor and
  endpoint-group/site scope, per-window quotas, sticky sessions, warm-up and probe weighting.
- Identity types with typed payload fields (`string`, `number`, `bool`, `cookie_map`, `json`, `secret_ref`),
  JSON-Schema-validated imports (JSON Lines and CSV, up to 50 000 rows per call, dry run), deduplication by
  `unique_by`, payload versioning and a six-segment delivery rendering that hides type details from nodes.
- `ReportService/Report`: batches of up to 500 reports, per-report validation, idempotency by `report_id`,
  asynchronous ingest into sharded Redis streams.

**Risk-control loop**

- Signal policies: configurable rules over HTTP status, business code, error kind, markers, URI, method,
  latency and size, producing twelve outcomes; optional trust of node-proposed outcomes.
- Action policies: cooldown, expire, quarantine and ban at identity-endpoint, identity-site, identity,
  account, proxy-site and proxy scope; exponential backoff with caps, escalation ladders, failure-streak
  resets and a shadow mode that records without enforcing.
- Health scoring: EWMA with time decay towards a baseline, per-endpoint low-score cooldowns and automatic
  quarantine; identity lifecycle `pending → active → quarantined / banned / expired / disabled / retired`.
- Cross attribution between identities and proxies, so a bad proxy is not charged to the identities that
  used it.
- Circuit breakers per endpoint group: sliding window with buckets, three states with probe leases, manual
  open and close, and optional revert of the cooldowns applied in the tripping window.
- `RevertActions` for bulk rollback of automatic actions by time range, site, policy, rule and action kind,
  with dry run and optional health/failure resets.

**Infrastructure**

- Config center: versioned items with drafts, publish, rollback and diffs; `WatchConfig` long polling;
  `${secret:path}` references resolved at delivery; reserved read-only `_runtime` group exposing breakers
  and site switches.
- Vault: AES-256-GCM envelope encryption (KEK → DEK → data) with file and environment KEK providers,
  online KEK rotation and a resumable rewrap job, secret versions, expiry alerts and audited reads.
- Proxy pool: kinds, regions, providers, tags, concurrency limits and session templates; assignment modes
  `none`, `pool`, `bind_identity` and `region_match`; periodic health checks with exit-IP and GeoIP
  enrichment; imports from URL lines, JSON Lines and CSV.
- Tenancy and access control: tenants → namespaces → sites, console roles `viewer`/`operator`/`admin`/
  `owner` with per-namespace and per-site bindings and extra permissions; node API tokens with scoped
  grants, IP allow-lists, rate limits and expiry; Argon2id passwords, login throttling, sessions and CSRF
  protection.
- Hot-state rebuild from PostgreSQL (`spnr rebuild`), full or per site, with periodic snapshots of health
  state and automatic rebuild when Redis has lost its data.

**Console and delivery**

- Embedded React console covering every module across 21 routes: overview, heatmap, identities and
  identity types, accounts, proxies, sites, policies, breakers, config, secrets, request explorer, risk
  events, notifications, tokens, users, audit log and tenants. Bilingual (English and Chinese), light and
  dark, live updates over SSE.
- Notifications: webhook (HMAC-SHA256 signed), Feishu, DingTalk, WeCom and Telegram channels; eleven alert
  kinds with de-duplication, per-site routing, test deliveries and delivery history.
- Observability: `/healthz`, `/readyz` (PostgreSQL, Redis, hot state, catalog, draining), Prometheus
  `/metrics`, optional OTLP tracing and an optional ClickHouse-backed request explorer.
- Docker Compose deployment (`deploy/compose`) with PostgreSQL, Valkey, ClickHouse, migrations, two server
  replicas and a Caddy load balancer with readiness-based health checks, plus the profiles `init`,
  `observability`, `test`, `loadtest` and `example`.
- `spnr` CLI: `migrate`, `admin init`, `token create`, `rebuild`, `kek generate|status|rewrap`, `seed`,
  `config check`, `healthcheck`, `version`.
- Python SDK (sync and async, httpx + pydantic v2) and Go SDK (Connect JSON or gRPC), both with lease
  helpers, batching reporters, config watchers with local snapshots and typed errors.
- FastAPI example crawler (`examples/fastapi-crawler`) with a one-command quickstart, and a mock target
  site and proxy for testing without touching a real platform.
- Test suites: Go unit and integration tests, end-to-end scenarios inside the Compose network
  (`test/e2e`), a replica failover drill, a Playwright console suite (`web/e2e`) and k6 load scenarios
  (`test/load`).
- Documentation: bilingual README, deployment guide, operations runbook, API reference and benchmark
  report (`documents/en/17-performance.md`), plus SDK and example documentation.
- Optional profiling listener: `SPINNERET_PPROF_ADDR` serves `net/http/pprof` on its own address. It is
  off by default and is never mounted on the API or metrics listener.
- Report stream retention: each shard owner trims its Redis stream to the consumer group position
  (`XTRIM … MINID ~`, floored by the oldest pending entry), so a drained stream no longer keeps every
  processed entry until `SPINNERET_STREAM_MAXLEN` evicts it.
- API token names are unique among usable tokens only, so revoking a token frees its name for the
  replacement (migration `00005`). `spnr seed` names the node token after `--site` by default instead of
  revoking another site's token.

### Notes

- **Licensing:** Apache License 2.0, Copyright 2026 TikHub — see `LICENSE` in the repository root.
- **Encryption keys:** the KEK is the only thing that makes encrypted data recoverable. Back up
  `deploy/compose/secrets/kek.key` separately from database dumps before running anything in production.
- **Fixed at deploy time:** `SPINNERET_REPORT_SHARDS` cannot be changed without stranding in-flight leases;
  pick it before going live (see the deployment guide).
- **Measured performance** (`documents/en/17-performance.md`, 100k identities × 50 endpoint groups on a laptop-sized
  Docker VM): Acquire p99 0.5–1.7 ms, report → state update p99 6.8 ms, report ingest 29,584/s per
  instance, 10,001 concurrent config watchers per instance, config change awareness p99 176 ms. Acquire
  throughput is bounded by the single-threaded Redis at ~3,000 acquire→report cycles/s in total, below the
  5,000/s per-instance target; scale Redis (the key space is hash-tagged per site and per report shard) to
  go beyond it.
- **Redis memory scales with traffic, not with the dataset:** report dedup markers and ended lease hashes
  live for `SPINNERET_REPORT_DEDUP_TTL`. The report streams are trimmed to the consumer group position by
  their shard owners, so they hold the real backlog; `SPINNERET_STREAM_MAXLEN` is the cap of a backlog the
  workers cannot drain. Size both before running at a high report rate — see the benchmark report.
- Out of scope for 0.1 and candidates for 0.2: distributed global rate limiting, external validator and
  refresher webhooks, proxy provider adapters, NATS JetStream, OIDC and TOTP, mTLS, staged config rollouts,
  fingerprint distribution and browser pools.

[0.1.0]: https://github.com/TikHub/Spinneret/releases/tag/v0.1.0
[Unreleased]: https://github.com/TikHub/Spinneret/compare/v0.1.0...HEAD
