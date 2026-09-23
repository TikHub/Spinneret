# Changelog

All notable changes to Spinneret are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

**Version spelling.** A release is the git tag `vX.Y.Z` — with the `v`, because the Go module proxy
will not resolve a version without it — and that tag is what the container image, the GitHub release
and the console's update check all report. Everything that carries a version *inside* a file writes it
bare: `0.1.0` in this changelog, in `web/package.json` and in the Python SDK's metadata, as PEP 440 and
npm require. One release, two spellings, decided by where the string lives.

## [Unreleased]

### Fixed

- **The notification SSRF opt-out could not be set on a Compose deployment.** 0.1.3 turns the delivery
  guard on for everyone and names `SPINNERET_NOTIFY_ALLOW_PRIVATE_TARGETS` as the way back in, but the
  variable was missing from the Compose environment block — and Compose delivers only the variables that
  block names, so the documented escape hatch did not exist on the default deployment. Only the Compose
  files changed, so the 0.1.3 image is unaffected: `git pull` in the deployment directory is enough.

- **A restarting Valkey no longer takes the server down with it.** An instance answers `LOADING` to every
  command while it reads its AOF back after a restart, and the server treated that as a fatal startup
  error. It exited, the supervisor restarted it, and that loop did eventually succeed — but the backoff
  grows while the instance needs a fixed time to load, so the larger the dataset the further recovery
  lags behind it. Startup now re-pings for up to two minutes while the reply is `LOADING`, and still
  fails at once on anything else: a wrong password does not become right by waiting.

  The Compose health check hid this. `valkey-cli ping` exits 0 even when the reply is an error, so the
  container reported healthy while it was still refusing work and `depends_on: service_healthy` let the
  server start into it. It now matches `PONG`.

- **Disabling the update check is possible again.** The checker reads an empty URL as "make no outbound
  call" and `.env.example` offered exactly that, but a blank value reads as unset and falls back to the
  built-in feed, so the documented configuration was unreachable. The variable was also missing from the
  Compose environment block, so the check could not be pointed at a mirror either. Both are passed
  through now, and the switch is a separate `SPINNERET_UPDATE_CHECK_ENABLED` — it has to be, because
  Compose delivers an empty string for every variable an operator has not set, so a blank-means-off rule
  would disable the check on every deployment that left it alone.

- **The deployment can say which proxies may set `X-Forwarded-For`.** Caddy overwrites the header for an
  untrusted peer, which is right when the published port is the edge. Behind something that terminates
  the connection first — a Cloudflare Tunnel, an nginx on the host, a cloud load balancer — Caddy's peer
  is the Docker bridge gateway, and it replaced the real client address with `172.18.0.1` in every audit
  entry and risk event. `CADDY_TRUSTED_PROXIES` now carries the trusted networks, defaulting to loopback
  only: no request in this topology comes from loopback, so out of the box nothing is trusted.

## [0.1.3] — 2026-09-22

Two security fixes, both reachable only by an authenticated caller but both
across a trust boundary a multi-tenant deployment relies on. No schema
migration and no configuration to change; upgrading is a pull and a restart.
A deployment that deliberately sends notifications to internal hosts now has
to say so with `SPINNERET_NOTIFY_ALLOW_PRIVATE_TARGETS=true`.

### Security

- **Webhook delivery could reach internal services and cloud instance metadata.** Notification channels
  (webhook, Feishu, DingTalk, WeCom, Telegram) validated only that the URL was `http(s)` and then dialled
  with the default transport. Nothing stopped a channel from targeting `169.254.169.254` or a service on
  the private network, and because `TestChannel` answers synchronously and the chat providers echo part
  of the response body, the answer came back to whoever configured it. Creating a channel needs
  `notify:write`, held by tenant `admin`/`owner` — in a multi-tenant deployment those are customers, not
  infrastructure operators, so this crossed a trust boundary.

  Delivery now refuses non-public targets in the dialer's `Control` hook, which runs *after* DNS
  resolution and therefore also defeats DNS rebinding: a hostname that resolves to a public address when
  the channel is saved and a private one when it is used is still blocked. Redirects were already
  disabled. Set `SPINNERET_NOTIFY_ALLOW_PRIVATE_TARGETS=true` for a trusted single-tenant deployment that
  deliberately notifies internal hosts.

  The blocked set is wider than Go's own classification. `netip.Addr.IsPrivate` is RFC 1918 and RFC 4193
  only, so shared address space would still have gone through — and that is where Alibaba Cloud keeps
  instance metadata, at `100.100.100.200`, rather than in the `169.254.0.0/16` every other provider uses.
  Carrier-grade NAT, `0.0.0.0/8`, `192.0.0.0/24`, `198.18.0.0/15` and `240.0.0.0/4` are refused too, as are
  the IPv6 forms that carry an IPv4 address inside an otherwise ordinary global-unicast address: NAT64
  (`64:ff9b::a00:1`) and 6to4 (`2002:0a00:0001::`) both looked public while delivering to `10.0.0.1`.

  Thanks to [@xuemian168](https://github.com/xuemian168) ([#19](https://github.com/TikHub/Spinneret/pull/19)).

- **One identity-import row could pin a CPU for hours.** A row's `_tags` field was de-duplicated with an
  `O(n²)` linear scan and nothing capped the token count, so only the 32 MiB stream budget bounded the
  work: 50 000 distinct tags already cost 1.6 s, and the growth is quadratic — a full-budget field runs for
  hours on the parsing goroutine while the rest of the fleet shares that CPU. Import needs
  `identity:write` on the target site, which the node and worker tokens carry, making it the most widely
  distributed credential that could trigger it.

  Tags and labels are now capped at 64 per row — the limit `UpdateIdentityRequest` has always enforced, so
  this closes an inconsistency rather than tightening the API — de-duplicated in `O(n)`, and the scan
  stops at the cap instead of materializing every token.

  Thanks to [@xuemian168](https://github.com/xuemian168) ([#18](https://github.com/TikHub/Spinneret/pull/18)).

### Added

- **Adding one identity no longer means hand-writing JSON.** The import dialog takes JSON Lines or CSV, the
  payload editor is a raw JSON editor, and there is no `CreateIdentity` RPC — so a single cookie had to be
  escaped into JSON by hand. The identities page now has a **New** button whose form is built from the
  selected type's fields: a textarea for `cookie_map` and `json`, a switch for `bool`, an input for the
  rest, plus the `_account`, `_region` and `_tags` metadata. It submits one JSONL row through
  `ImportIdentities`, so normalization, deduplication and activation stay the rules a file import already
  follows. A `cookie_map` field takes a pasted `Cookie` header as-is and decodes a browser export array.

### Changed

- The console moved to React 19 and Vite 8. Vite 8 cuts the production build from roughly 7.5 s to under
  a second. No API or configuration change.
- Dependency updates: `@types/node`, `prettier`, `docker/setup-buildx-action`, `prometheus/client_model`,
  `protovalidate`, and the Python SDK's `hatchling`, `respx` and `pytest-asyncio` development pins.

## [0.1.2] — 2026-09-20

Two fixes, both for settings that looked like they worked and did not. No schema
migration, no configuration to change; upgrading is a pull and a restart.

### Fixed

- **The console's ClickHouse retention setting now reaches ClickHouse.** It stored a number and changed
  nothing: the tables carry a TTL of their own, ClickHouse is the only thing that enforces it, and no
  `ALTER TABLE ... MODIFY TTL` was ever issued. A restart did not help either — startup migrated the
  tables with the value from the environment and ignored the stored one. So the page reported 30 days
  over tables that expired at 90, and the disk grew at the old rate until somebody looked.

  Changing it now alters the tables in the same call, and a failed `ALTER` is reported instead of leaving
  a stored number that means nothing. Startup resolves the setting before migrating, so a value set from
  the console survives a restart. Clearing the setting applies whatever it falls back to. With ClickHouse
  disabled the value is recorded for whenever one is connected.

- **Nine documented environment variables could not be set on the Compose deployment at all.** Compose
  delivers only the variables its `environment` block names, and `SPINNERET_CLICKHOUSE_TTL_DAYS`, the five
  `SPINNERET_RETENTION_*`, `SPINNERET_LATE_REPORT_WINDOW`, `SPINNERET_STREAM_MAXLEN` and
  `SPINNERET_MAX_WATCHERS` were not among them. Putting any of them in `.env` did nothing — the server
  kept its built-in default and nothing said otherwise. Found while setting a ClickHouse TTL on a real
  deployment, where 30 days in `.env` left the tables on 90.

  They are passed through with an empty default rather than the documented one, which matters: a variable
  that always arrived with a value would count as explicitly set, pin its setting to the environment, and
  make Settings → System show it read-only on every Compose deployment. `TestABlankValueIsNotSet` holds
  that invariant down.

## [0.1.1] — 2026-09-19

A patch release: no schema migration, no configuration you must change, and every default unchanged.
Upgrading is `spnrctl pull && spnrctl up -d --wait`. Two of the fixes below are things that were wrong
in 0.1.0 without saying so — container logs had no size limit at all, and the installer's default image
was one that only TikHub could pull.

### Added

- **Retention is editable from the console**, at Settings → System, instead of only through environment
  variables and a restart. The five PostgreSQL retentions and the ClickHouse TTL are joined there by the
  alert history window, which was hard-coded at 90 days and had no variable at all. A change applies on
  the next hourly maintenance pass.

  The two sources compose in one direction: an environment variable that is explicitly set **wins and
  pins** the setting, which the console shows read-only next to the name of the variable to remove. That
  keeps the guarantee a deployment managed from a file depends on, and since nothing in the shipped
  `.env` sets a retention variable, every existing deployment starts with all of them at their defaults
  and nothing about its current behaviour changes. Values are stored as one row in `system_settings`, which
  has existed since the first migration and already holds the notification watermark, so there is no new
  migration.

  Reading them needs only a console session — what a deployment keeps its data for is not secret, and an
  operator looking at a filling disk should not need a role binding. Changing them requires a platform
  administrator, because it is deployment-wide and destructive, and every change is audited, accepted or
  refused: retention decides how long evidence is kept, so the change to it is evidence.

  Deliberately not editable here: anything encoded into data (`SPINNERET_REPORT_SHARDS` lives in every
  lease id), anything needed before the database can be reached (its own URL, the key-encryption keys),
  and the acquire and report hot paths. The test for inclusion is not whether someone would like to
  change a value but what happens if it changes between two reads.

- **Every release is published to Docker Hub as well as to GitHub Packages.** It is one build pushed to
  both registries rather than a build each, so both serve the same digest and cannot drift apart, and
  the four tags (`v0.1.0`, `0.1.0`, `0.1`, and `latest` for a non-pre-release) are identical on each.
  The Docker Hub image is `tikhubio/spinneret`, and it is the one to pull from a host that cannot read
  GitHub Packages. Docker Hub needs the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets; without them
  the run publishes to GitHub Packages alone instead of failing, which is what a fork sees. The namespace
  is that username unless the repository variable `DOCKERHUB_REPOSITORY` overrides it.

### Changed

- **The installer gives Valkey one I/O thread on a host with two cores or fewer**, instead of the
  default four. Four buys tail latency on a machine with cores to spare and oversubscribes one that runs
  PostgreSQL, ClickHouse, the server and the load balancer on the same two. Valkey executes every command
  on its main thread whatever this is set to, so the cost is tail latency at high rates, not throughput.
  The installer already scales the per-container memory ceilings to the host; this is the same idea for
  the CPU.
- **The installer now pulls from Docker Hub by default**, `tikhubio/spinneret` instead of
  `ghcr.io/tikhub/spinneret`. GitHub Packages is private for this project, so the old default answered
  401 for everyone outside the organisation and the installer quietly fell back to building from source —
  which is the slow path the published image exists to avoid. The same build is on both registries at the
  same digest, and `SPINNERET_IMAGE` still points at GitHub Packages, a private mirror or a fork.
- The `release` workflow can be run by hand against a tag that already exists, from **Actions → release
  → Run workflow**. It rebuilds from the tag and publishes the images without touching that tag's GitHub
  release, which is how a registry added after a release was cut gets the images it missed. Its `latest`
  input exists so that publishing an older tag does not move `:latest` backwards.

### Fixed

- **Container logs are capped.** Every service in the Compose files now sets `json-file` with
  `max-size=20m` and `max-file=10`. Docker's default keeps container output forever, so this was the one
  store in the stack with no bound of any kind — not governed by a retention variable, not trimmed by a
  job, and not in any of the three volumes an operator is told to watch. It writes to the Docker data
  root, which is the filesystem PostgreSQL and ClickHouse are on. The retention table in the operations
  runbook now lists it alongside the settings, and the disk alert covers the data root and not only the
  volumes.

- **Settings → System no longer tells a build made from source that it is on the latest release.** A
  build with no release number cannot be ordered against one — it may well be ahead of it — so the card
  now says the build came from source and shows the latest release beside it without claiming either is
  newer. `CheckForUpdateResponse` carries the new field `current_is_release` for the distinction;
  `update_available` was already correct and is unchanged. Found by checking the console against the
  real feed immediately after publishing v0.1.0, which is the first time the "unversioned build, a
  release exists" combination could occur.
- **The notification service reported itself ready before it had subscribed to the event bus**, so an
  event published in that gap reached no handler. The bus delivers to the handlers a channel holds at the
  moment of publication and keeps no backlog, so such an event is lost rather than late. `Service.Run`
  sets `running` on its first statement because that flag is the guard against a second `Run`, several
  statements before the subscription exists; readiness is now the separate `Service.Subscribed()`, set
  once the delivery workers and the bus handler are in place. Anything that publishes and then waits for
  the result should wait on that. Two CI builds failed on this, each in whichever notify bus test lost
  the race.
- `TestShedAfterRedisReplyKeepsExhausted` no longer depends on the order the scheduler's tests run in.
  It asserts that only the first acquire attempt reaches Redis by counting commands, and `Script.Exec`
  sends `EVALSHA` and falls back to `EVAL` when the server answers `NOSCRIPT`, so the first attempt of a
  cold run costs two commands instead of one. The test now loads the script before it starts counting.

## [0.1.0] — 2026-09-18

First release, open source under the [Apache License 2.0](LICENSE) and maintained by
[TikHub](https://github.com/TikHub). Spinneret is a control plane for fleets that share scarce,
rate-limited credentials and egress: it leases identities and proxies to nodes, turns the request
results they report into cooldowns, bans, health scores and circuit breaking, and distributes versioned
configuration and secrets. Nodes need only a server URL and an API token.

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

**Admission control on the Acquire path**

- Acquire concurrency at Redis is bounded per instance, so offered load above the knee becomes shedding
  instead of congestion collapse. `SPINNERET_ACQUIRE_FLEET_INFLIGHT` (default `64`, range 0–65536) is the
  budget for the *whole fleet*, and each API instance admits that number divided by the live API instances
  it sees in a Redis heartbeat set, clamped to `[4, 4096]`. `SPINNERET_ACQUIRE_MAX_INFLIGHT` (default `0`,
  max 4096) pins one instance's limit instead and stops its heartbeat — pin it on every API instance or on
  none. `SPINNERET_ACQUIRE_FLEET_INFLIGHT=0` turns admission control off entirely, which is also the A/B
  lever for measuring it on one build. Release, renew, reap and ingest are deliberately left ungated: that
  asymmetry is the mechanism, because it bounds the queue a `release.lua` waits behind.
- An attempt waits up to 50 ms — never longer than its own remaining `wait_ms` — for a permit, and the time
  it spent waiting is credited against its retry rung, so a given `wait_ms` buys the same number of attempts
  it would without the gate. What cannot be admitted is shed with the reason **`overloaded`**
  (`unavailable`, HTTP 503) and a retry hint jittered into 100–200 ms. A shed issues **zero Redis
  commands**, which is why retrying it is always safe and why it cannot deepen the queue. `AcquireBatch`
  takes `count` permits, so a 50-lease batch is charged for the Lua work it really does.
- Two failure modes, two statistics results. `exhausted` means the identity pool is empty — add identities
  or widen the rotation policy; `overloaded` means the instance is at its acquire concurrency limit — add
  capacity or offer less load. Alert on each for its own cause and exclude `overloaded` from
  acquire-failure alerts, since shedding is the healthy response to overload. The console's acquire failure
  ratio counts both.
- Metrics: `spinneret_acquire_script_seconds` (the `acquire.lua` round trip alone, sharing buckets with
  `spinneret_acquire_duration_seconds`), `spinneret_acquire_admission_total{result}` (`immediate`, `queued`,
  `shed_no_wait`, `shed_queue_full`, `shed_timeout`, `shed_canceled`),
  `spinneret_acquire_admission_wait_seconds`, and the gauges `spinneret_acquire_inflight`, `_queued`,
  `_inflight_limit`, `_peers`, `_peer_beat_age_seconds` and `_peer_beat_failures_total`. The gauges exist
  only while admission control is on; `_peers` is also absent when the limit is pinned.

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
  protection. Token names are unique among usable tokens only, so revoking a token frees its name for the
  replacement.
- Hot-state rebuild from PostgreSQL (`spnr rebuild`), full or per site, with periodic snapshots of health
  state and automatic rebuild when Redis has lost its data.
- Report stream retention: each shard owner trims its Redis stream to the consumer group position
  (`XTRIM … MINID ~`, floored by the oldest pending entry), so a drained stream does not keep every
  processed entry until `SPINNERET_STREAM_MAXLEN` evicts it.
- Observability: `/healthz`, `/readyz` (PostgreSQL, Redis, hot state, catalog, draining), Prometheus
  `/metrics`, optional OTLP tracing and an optional ClickHouse-backed request explorer. An optional
  profiling listener, `SPINNERET_PPROF_ADDR`, serves `net/http/pprof` on its own address; it is off by
  default and is never mounted on the API or metrics listener.

**Console and delivery**

- Embedded React console covering every module across 22 routes: overview, heatmap, identities and
  identity types, accounts, proxies, sites, policies, breakers, config, secrets, request explorer, risk
  events, notifications, tokens, users, audit log, tenants and system. Bilingual (English and Chinese),
  light and dark, live updates over SSE.
- Settings → System reports the running build and checks the published releases on demand. Nothing polls:
  the deployment reaches out only when an operator presses the button, the answer is cached for an hour,
  and `SPINNERET_UPDATE_CHECK_URL=""` disables the check entirely. The check runs server-side, so the
  console's `connect-src 'self'` CSP stays as it is and the feature works where the browser has no route
  out but the host does.
- Notifications: webhook (HMAC-SHA256 signed), Feishu, DingTalk, WeCom and Telegram channels; eleven alert
  kinds with de-duplication, per-site routing, test deliveries and delivery history.
- `spnr` CLI: `migrate`, `admin init`, `token create`, `rebuild`, `kek generate|status|rewrap`, `seed`,
  `config check`, `healthcheck`, `version`.
- Python SDK (sync and async, httpx + pydantic v2) and Go SDK (Connect JSON or gRPC,
  `github.com/TikHub/Spinneret/sdk/go`), both with lease helpers, batching reporters, config watchers with
  local snapshots and typed errors.
- FastAPI example crawler (`examples/fastapi-crawler`) with a one-command quickstart, and a mock target
  site and proxy for testing without touching a real platform.

**Install, deployment and documentation**

- **A guided one-command installer**, `install/install.sh` with an identical Chinese twin
  `install/install.zh.sh`. Docker only: it detects the host, offers to install Docker, clones the
  repository, generates every password and the key-encryption key on the machine, writes the Compose
  overrides for this host, pulls the published image or falls back to building from source, runs the
  migrations, waits for `/readyz`, creates the first administrator, and writes a `spnrctl` control script.
  Re-running it opens a management menu: status, upgrade, accounts, tokens, hot-state rebuild,
  configuration, health, logs, restart, backup, restore, disk and uninstall. `install/README.md` documents
  every question, flag and file it writes.
- Docker Compose deployment (`deploy/compose`) with PostgreSQL, Valkey, ClickHouse, migrations, two server
  replicas and a Caddy load balancer with readiness-based health checks, plus the profiles `init`,
  `observability`, `test`, `loadtest` and `example`.
- **A complete bilingual manual in `documents/`**: 21 pages in English and Simplified Chinese, from a quick
  start to a performance and tuning guide, with an index in `documents/README.md`. Every page was verified
  against the source.
- Open-source scaffolding: `LICENSE`, `SECURITY.md` (private vulnerability reporting), `CONTRIBUTING.md`,
  GitHub issue and pull-request templates, and a `release.yml` workflow that builds a multi-architecture
  image for `linux/amd64` and `linux/arm64` and pushes it to `ghcr.io/tikhub/spinneret` on a version tag.
- Test suites: Go unit and integration tests, end-to-end scenarios inside the Compose network
  (`test/e2e`), a replica failover drill, a Playwright console suite (`web/e2e`) and k6 load scenarios
  (`test/load`). `make help` lists the developer tasks and `make web-test` runs the console gates that CI
  runs.

### Performance

Measured on one instance against one Valkey, 100k identities × 50 endpoint groups in a laptop-sized Docker
VM; the method, the hardware and every caveat are in `documents/en/17-performance.md`.

- **Every v0.1 target is met on a single server instance and a single Redis instance.** Acquire **4,993/s**
  sustained at p99 4.32 ms (peak 7,792/s), and p99 **1.86 ms** at 4,500 cycles/s. Full acquire→report cycle
  **4,500/s** with exclusive leases, **5,500/s** with `max_concurrent_leases: 4`. Report ingest **44,437/s**
  accepted with zero rejections; reports applied to the hot state p99 **30.7 ms at 19,761/s**. Config change
  awareness p99 **46.1 ms**, with ~10,000 concurrent long polls held per instance on 0.05 cores.
- One acquire→report cycle costs **168.3 µs of Redis CPU**, i.e. about **5,940 cycles/s per Redis thread**.
  Per script, measured with `SLOWLOG` at 1,000 cycles/s: `acquire.lua` 68.3 µs, `observe.lua` 43.5,
  `release.lua` 34.0, `ingest.lua` 12.2, `lease_retain.lua` 10.3 — between 14 % and 49 % cheaper than the
  first working versions of each. `xg` names the endpoint groups that pushed an identity's ready-queue score
  forward rather than `"1"` meaning all of them, which is where a good part of that came from.
- **Two replicas serve 4,000 cycles/s, gated or ungated.** The gate costs a single replica nothing (4,499 of
  4,500 offered cycles/s admitted, p99 1.97 ms) and costs two replicas some tail (acquire p99 2.99 ms
  against 1.66, report lag 15.8 ms against 8.4). Past capacity it earns that: at 4,500/s offered it served
  2,418 cycles/s, shed 1,881/s as `overloaded`, and kept acquire p99 finite at 89 ms. Throughput fell; it
  did not collapse, and no client saw a timeout.
- **Valkey tuning in `deploy/compose`**, each change measured: `io-threads 4` (`VALKEY_IO_THREADS` to
  override) — +3 % throughput but acquire p99 18.9 → 4.1 ms and report lag p99 4.66 s → 41 ms at
  6,000 cycles/s, for one extra core; RDB `save` points disabled, since the AOF is the durability mechanism
  and the two forked for the same data; `auto-aof-rewrite-percentage 300` and
  `auto-aof-rewrite-min-size 1gb`, because a hot state of short-lived keys triggered an AOF rewrite every
  52 s at 4,000 cycles/s and each rewrite fork was enough to tip the system into congestion collapse.
- ClickHouse in `deploy/compose` gets `config/clickhouse-limits.xml`: its default 5 GiB mark cache is sized
  for a dedicated analytics host, and unconstrained it was OOM-killed during a load run, freezing the whole
  Docker VM. `max_server_memory_usage` is a 1.5 GiB safety net, deliberately above the image's idle RSS —
  set below it, every INSERT fails and the report worker stalls.
- `test/load/run.sh` takes `MID_STATS=0`, which drops the `docker stats` part of the mid-run snapshot. That
  call walks every container of the machine and, on Docker Desktop, stalled the system under test for ~5 s
  at 4,000 cycles/s — enough to turn a 1.9 ms p99 into 11.9 ms. Use it for any run near the knee.

### Notes

- **Licensing:** Apache License 2.0, Copyright 2026 TikHub — see `LICENSE` in the repository root.
- **Encryption keys:** the KEK is the only thing that makes encrypted data recoverable. Back up
  `deploy/compose/secrets/kek.key` separately from database dumps before running anything in production.
- **Fixed at deploy time:** `SPINNERET_REPORT_SHARDS` cannot be changed without stranding in-flight leases;
  pick it before going live (see the deployment guide).
- **Redis memory scales with traffic, not with the dataset:** report dedup markers and ended lease hashes
  live for `SPINNERET_REPORT_DEDUP_TTL`, settable from `deploy/compose/.env` and the dominant
  traffic-proportional term — at 4,500 cycles/s the default 1 h costs about 5.4 GiB. The report streams are
  trimmed to the consumer group position by their shard owners, so they hold the real backlog;
  `SPINNERET_STREAM_MAXLEN` is the cap of a backlog the workers cannot drain. Size both before running at a
  high report rate.
- **A freshly seeded or rebuilt site must be warmed before it is benchmarked.** Seeding writes the same
  ready-queue score for every identity in every endpoint group, so all groups contend on the same head; a
  cold site collapsed at 2,500 cycles/s where the warmed one carried 4,500/s. About a million acquires of
  ordinary traffic removes it.
- **Never unlink `sp:{<site>}:ls:*` by hand** (nor trim a stream holding unprocessed `release: true`
  reports): the lease hash is what decrements the identity's active-lease counter, so deleting live ones
  makes those identities permanently unavailable. `spnr rebuild --site` does not reset runtime counters by
  design; the repair is to unlink the whole site prefix first and then rebuild.
- **The next scaling levers**, in order: make a rejected acquire candidate cheap (a failing acquire costs
  about five times a succeeding one — 220 Redis commands against 49), then Redis Cluster. The key space is
  already hash-tagged per site and per report shard for it.
- Out of scope for 0.1 and candidates for 0.2: distributed global rate limiting, external validator and
  refresher webhooks, proxy provider adapters, NATS JetStream, OIDC and TOTP, mTLS, staged config rollouts,
  fingerprint distribution and browser pools.

[0.1.3]: https://github.com/TikHub/Spinneret/releases/tag/v0.1.3
[0.1.2]: https://github.com/TikHub/Spinneret/releases/tag/v0.1.2
[0.1.1]: https://github.com/TikHub/Spinneret/releases/tag/v0.1.1
[0.1.0]: https://github.com/TikHub/Spinneret/releases/tag/v0.1.0
[Unreleased]: https://github.com/TikHub/Spinneret/compare/v0.1.3...HEAD
