# Spinneret v0.1 — Implementation Specification

Status: authoritative engineering contract for v0.1. It refines `0_first_doc.md` (the product design, in Chinese)
and resolves every open question in it. When the two documents disagree, **this document wins**. Every package,
script, table and API must follow the contracts below so independently written components fit together.

Module path: `github.com/Evil0ctal/Spinneret`. Go 1.27. All code comments are English.

---

## 0. Decisions on the open questions of the design doc

| Question | Decision |
| --- | --- |
| Multi-tenancy | **Real multi-tenancy.** `tenant → namespace → site`. A tenant is a hard isolation boundary (team/org): users, tokens, namespaces, sites, proxies, configs, secrets, channels never cross tenants. Inside a tenant, role bindings can be narrowed to a namespace and to a set of sites, so "the platform-A team only sees platform-A sites, endpoint groups, identity types, identities, policies and breakers". See §3. |
| Per-URI cooldown granularity | Endpoint group stays the minimum unit. A single URI gets its own endpoint group with an `exact` rule (exact has the highest match priority). |
| Plaintext payload cache | Enabled by default (`SPINNERET_PAYLOAD_CACHE=true`), can be disabled globally; entries keyed by `identity_id + payload_version + type_version`. |
| Revert cooldowns when a breaker opens | `revert_recent_cooldowns` is an enum `none | endpoint | all`, default **`endpoint`** (YAML `true` = `endpoint`, `false` = `none`). Rationale (crawler-node view): cooldowns applied during the window that tripped the breaker were caused by an endpoint-wide tightening, not by those identities; keeping them would starve the endpoint for up to 30 min *after* the breaker closes and would inflate failure streaks so the next ordinary 429 escalates straight to the max. Identity×endpoint cooldowns (and their failure-streak increments) are therefore reverted. Identity×site cooldowns (typically captcha — a cookie flagged with a captcha usually stays flagged) are kept unless `all`. Bans, expiry and quarantine are never reverted automatically. |
| Success request details | Every report (success included) is written to **ClickHouse** `report_events` (90-day TTL, configurable). PostgreSQL keeps non-success `risk_events` (30 d) and aggregates. ClickHouse is optional: when `SPINNERET_CLICKHOUSE_URL` is empty the system runs without raw events. |
| License | None for now. |
| PyPI name | Package `spinneret` reserved in `sdk/python/pyproject.toml` (distribution name `spinneret`). |

Other resolved ambiguities are marked **[decision]** throughout this document.

---

## 1. Repository layout and ownership

```
proto/spinneret/v1/            *.proto — API source of truth (buf)
gen/go/spinneret/v1/            generated Go messages (package spinneretv1)
gen/go/spinneret/v1/spinneretv1connect/  generated Connect handlers/clients
cmd/spinneret-server/           server entrypoint (flags: --role all|api|worker)
cmd/spnr/                       admin CLI (cobra): migrate, admin init, token, rebuild, kek, seed, version
internal/
  appconfig/        env configuration (SPINNERET_*)
  apperr/           typed application errors + reasons → Connect errors & headers
  authz/            Principal, roles, permissions, scopes, permission checks (pure, no I/O)
  auth/             token authentication, users, passwords (Argon2id), sessions, login throttle, Connect interceptor, AuthService/AccessAdminService handlers live in internal/api
  tenancy/          tenants, namespaces (CRUD, default policies bootstrap)
  pkg/idgen/        prefixed UUIDv7 IDs, lease-id encoding
  pkg/durationx/    duration parsing ("30s", "7d", "permanent")
  pkg/glob/         glob matching for scopes
  pkg/netx/         client IP extraction, CIDR allowlists
  observability/    slog logger, Prometheus metrics (all metric vectors defined here), OTel
  store/postgres/   pgx pool, goose migrations (embedded), tx helper, partition manager; queries/*.sql + db/ (sqlc)
  store/redis/      rueidis client factory, key builder (Keys), Lua script loader with common prelude
  store/clickhouse/ ClickHouse client + schema migration + batch writer
  vault/            envelope encryption, KEK providers, DEK cache, secrets domain service, rewrap job
  events/           Redis Pub/Sub bus + in-process fan-out (catalog invalidation, config changes, SSE)
  audit/            async batched audit log writer
  catalog/          in-memory, per-namespace immutable snapshot of sites/EGs/URI matchers/identity types/resolved policies
  site/             sites, endpoint groups, URI rules, URI matcher
  identity/         identity types (spec, JSON Schema, compile, deliver rendering), identities, payloads, import, accounts, payload cache
  proxy/            proxies, import, bindings, URL rendering (session templates), health checker
  policy/           policy specs (rotation/signal/action/breaker), YAML/JSON codecs, validation, defaults, compile, resolution, signal classifier, action rule evaluator
  hotstate/         Redis hot-state schema helpers (Go side of common.lua), sync/materialize/rebuild, snapshots
  scheduler/        LeaseService logic: acquire/acquireBatch/renew/release + Lua (acquire, renew, release, reap)
  signal/           ReportService ingest (validate, dedup, enqueue) + stream event codec
  worker/           stream shard ownership, consumers, report processing pipeline, observe Lua, stats aggregation, risk-event & ClickHouse writers
  action/           action executor (apply Lua), lifecycle state writer, manual/bulk operations, rollback, ban/quarantine expiry
  breaker/          breaker window evaluation Lua, state machine, revert cooldowns, manual ops, runtime config (_runtime group)
  configcenter/     config items, drafts, versions, publish/rollback, watch hub, secret reference resolution
  notify/           notification channels (webhook, feishu, dingtalk, wecom, telegram), alert evaluator, delivery
  analytics/        dashboard queries (PG aggregates, Redis live counts, ClickHouse explorer)
  jobs/             background job runner, PG advisory-lock leader election, worker registry
  api/              Connect handlers for every service, JSON codec, interceptors (auth, errors, metrics, rate limit)
  server/           HTTP server assembly: Connect mux, /healthz, /readyz, /metrics, SSE, embedded web UI
  testutil/         shared test fixtures (Postgres, Redis, ClickHouse via env or testcontainers)
web/                React console (embedded via web/embed.go)
sdk/python/         Python SDK (httpx + pydantic v2), sync & async
sdk/go/             Go SDK (package spinneret) using generated Connect clients
examples/fastapi-crawler/   FastAPI example node
deploy/compose/     docker-compose.yml (+ test overlay), prometheus, .env.example
deploy/docker/      Dockerfile
test/e2e/           Go end-to-end scenarios against a running stack (build tag e2e)
test/load/          k6 scripts + seed profiles
test/mocktarget/    mock target site + mock HTTP proxy used by e2e/load tests
docs/               design, deployment (en + zh), SDK, operations
```

Rules for every package:
- No package-level mutable globals except metrics registration and embedded files.
- Every exported function takes `context.Context` first when it does I/O.
- Errors are wrapped with `%w` and context (`fmt.Errorf("load site %s: %w", id, err)`); domain errors use `apperr`.
- Never log secrets, payload fields, proxy credentials or tokens. Use `slog` with structured attributes.
- Constructors take explicit dependencies (no service locator). Prefer small interfaces declared by the consumer.
- Tests: table-driven, `testify/require`, integration tests use `internal/testutil`.

---

## 2. Identifiers

- All entity IDs: `<prefix>_<32 lowercase hex of a UUIDv7>` (`idgen.New(prefix)`), stored as `text`.
- Prefixes: `ten` tenant, `ns` namespace, `sit` site, `eg` endpoint group, `uri` URI rule, `ity` identity type,
  `idt` identity, `acc` account, `pxy` proxy, `pol` policy, `pbd` policy binding, `cfg` config item, `sec` secret,
  `tok` API token, `usr` user, `rb` role binding, `evt` state event, `aud` audit log, `brk` breaker event,
  `nch` notification channel, `alt` alert event, `rsk` risk event, `lse` lease.
- Hot-state keys: `sites`, `endpoint_groups`, `identities`, `accounts`, `proxies` also have `hkey bigint GENERATED ALWAYS AS IDENTITY`
  (compact numeric key used in Redis). In Redis they are written as base-10 strings.
- **Lease ID** [decision]: `lse_<32hex uuidv7>_<siteHkey base36>_<shard 2 lowercase hex>`, e.g.
  `lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f`. The Go side generates the prefix `lse_<hex>_<site36>_` and the
  acquire Lua script appends the shard (`identityHkey % report_shards`, formatted `%02x`). `idgen.ParseLeaseID`
  returns `(siteKey int64, shard int, err)`. This lets Report route to the stream shard and the site slot without I/O.
- Report IDs are client supplied, 1–64 chars `[A-Za-z0-9_.:-]` (UUID recommended).

---

## 3. Tenancy, authentication and authorization

### 3.1 Model
- `tenants` — isolation boundary. `namespaces` belong to one tenant (`UNIQUE(tenant_id, name)`).
- `sites`, `proxies`, `config_items`, `secrets`, `api_tokens`, `policies` belong to a namespace.
  `endpoint_groups`, `identity_types`, `identities`, `accounts` belong to a site.
  `notification_channels`, `role_bindings` belong to a tenant.
- `users` are global accounts. `is_platform_admin` users (created by `spnr admin init`) manage tenants,
  KEK rewrap and can act in any tenant.
- `role_bindings(user_id, tenant_id, role, namespace_id NULL=all namespaces, site_ids text[] empty=all sites, extra_permissions text[])`.
- Console requests carry the active tenant in header `X-Spinneret-Tenant: <tenant id>`; handlers take the namespace
  **name** in request messages (`namespace` field) and resolve it inside the active tenant.
- API tokens are bound to exactly one tenant + namespace; node-facing requests never need a namespace field
  (WatchConfig's optional `namespace` must equal the token namespace name).

### 3.2 Permissions (`internal/authz`)
Permission strings:
```
tenant:manage kek:manage                                  (platform admin only)
namespace:read namespace:write
site:read site:write
identity:read identity:write identity:operate identity:reveal
proxy:read proxy:write proxy:operate
policy:read policy:write policy:publish
breaker:read breaker:operate
config:read config:write config:publish
secret:list secret:write secret:reveal
token:read token:write
user:read user:write
audit:read
notify:read notify:write
dashboard:read
lease:acquire report:write secret:read                    (node only)
```
Roles:
- `viewer`: all `*:read`, `secret:list`, `dashboard:read`, `audit:read`, `notify:read`.
- `operator`: viewer + `identity:write identity:operate proxy:write proxy:operate policy:write config:write breaker:operate`.
- `admin`: operator + `site:write policy:publish config:publish secret:write secret:reveal identity:reveal token:read token:write notify:write namespace:read`.
- `owner`: admin + `namespace:write user:read user:write`.
- `extra_permissions` may add `config:publish`, `secret:reveal`, `identity:reveal`, `policy:publish` to any role.

Resource scoping: `authz.Resource{TenantID, NamespaceID, SiteID, SiteName, ConfigGroup, SecretPath}`.
- A binding matches when tenant matches, `namespace_id` is NULL or equal, and (`site_ids` empty or contains `SiteID`).
- **Namespace-level resources** (proxies, configs, secrets, tokens, channels, namespace itself) have `SiteID==""` and
  require a binding **without** site restriction — except `proxy:read`, which site-restricted bindings also get.
- List endpoints filter results to accessible sites (`Principal.SiteFilter(nsID, perm) (all bool, siteIDs []string)`).
- Platform admins pass every check.

Token scopes (strings stored in `api_tokens.scopes`):
| Scope | Grants |
| --- | --- |
| `lease:acquire[:<site name>]` | `lease:acquire` |
| `report:write[:<site name>]` | `report:write` |
| `config:read[:<group glob>]` | `config:read` (node ConfigService + ConfigAdminService reads) |
| `config:publish[:<group glob>]` | `config:read config:write config:publish` |
| `secret:read:<glob over "<namespace name>/<path>">` | `secret:read` |
| `identity:write[:<site name>]` | `identity:read identity:write identity:operate` |
| `proxy:write` | `proxy:read proxy:write proxy:operate` |
| `admin` | role `admin` within the token namespace |

### 3.3 Authentication
- Token: `spn_` + base62(32 random bytes). Stored as `sha256` (bytea) + `token_prefix` (first 12 chars incl. `spn_`).
  Verification result cached in-process 30 s (`SPINNERET_TOKEN_CACHE_TTL`); revocation publishes `tokens` event → caches drop entry.
  Checks: not revoked, not expired, IP allowlist (CIDRs, client IP from `SPINNERET_TRUSTED_PROXIES`-aware extraction),
  optional per-token `rate_limit_rps` (in-process token bucket per instance). `last_used_at/ip` flushed every 30 s.
- Users: Argon2id (`m=64MiB,t=3,p=2`, 16-byte salt, 32-byte key, PHC string). Login throttle: 5 failures / 15 min per
  username and 20 / 15 min per IP (Redis counters) → `resource_exhausted` reason `login_throttled`.
- Sessions: 32 random bytes → base64url session id; Redis hash `sp:sess:<sha256(id) hex>` with TTL 12 h
  (`SPINNERET_SESSION_TTL`), sliding refresh when < half remaining. Cookie `spinneret_session`, HttpOnly,
  SameSite=Strict, Path=/, Secure when TLS / `X-Forwarded-Proto=https` / `SPINNERET_COOKIE_SECURE=true`.
  A session stores the user's password generation (`users.password_changed_at` in Unix microseconds, `0` when unset)
  and is rejected once it differs from the user's, so a password change or reset ends every other session even when a
  login racing with it stored its session after the revocation; login also re-reads the user after storing the
  session and deletes it when the password or the disabled flag changed meanwhile. Password changes and resets
  publish `user.changed`. Login trims surrounding white space, then refuses usernames with control characters or invalid UTF-8 before the throttle.
- CSRF: cookie-authenticated requests with unsafe methods (everything except GET/HEAD/OPTIONS) must send header
  `X-Spinneret-CSRF: 1` (the console always does); combined with SameSite=Strict and JSON content type this blocks
  cross-site requests. The SSE endpoint is GET-only and takes `?tenant=<tenant id>&namespace=<namespace name>`.
- Precedence: `Authorization: Bearer spn_...` → token; else session cookie → user; else unauthenticated
  (only `AuthService/Login`, health, metrics and static UI are public).

---

## 4. PostgreSQL schema (goose migrations in `internal/store/postgres/migrations`)

Conventions: `text` IDs; `timestamptz`; `created_at/updated_at DEFAULT now()`; `jsonb` specs; arrays `text[] NOT NULL DEFAULT '{}'`.
Partitioned tables use `PARTITION BY RANGE`, partitions named `<table>_pYYYYMMDD` (daily) or `<table>_pYYYYMM` (monthly),
created by SQL function `spinneret_ensure_partitions(table text, granularity text, from_ts timestamptz, to_ts timestamptz)`
and dropped by `spinneret_drop_partitions_before(table text, granularity text, before_ts timestamptz)`.
Migrations create partitions for [now − 2 periods, now + 7 days / 3 months]; the `partition_manager` job keeps them rolling.

Tables (column lists are normative; migrations may add indexes):

- `tenants(id, name UNIQUE, display_name, description, created_at, updated_at)`
- `namespaces(id, tenant_id→tenants, name, display_name, description, created_at, updated_at, UNIQUE(tenant_id,name))`
- `users(id, username UNIQUE (lower-case), display_name, email, password_hash, is_platform_admin bool, disabled bool, locale, last_login_at, last_login_ip, password_changed_at, created_at, updated_at)`
- `role_bindings(id, user_id→users CASCADE, tenant_id→tenants CASCADE, role CHECK(owner|admin|operator|viewer), namespace_id→namespaces CASCADE NULL, site_ids text[], extra_permissions text[], created_by, created_at)`
- `api_tokens(id, tenant_id, namespace_id→namespaces CASCADE, name, description, token_prefix, token_hash bytea UNIQUE, scopes text[], ip_allowlist text[], rate_limit_rps int DEFAULT 0, expires_at, revoked_at, last_used_at, last_used_ip, created_by, created_at, UNIQUE(namespace_id,name) WHERE revoked_at IS NULL)` — the name is unique among *usable* tokens only, so rotating a token (create the new one, revoke the old one; there is no delete RPC) frees the name again
- `sites(id, hkey identity UNIQUE, namespace_id→namespaces, name, display_name, description, clients text[] DEFAULT '{web}', paused bool, paused_reason, paused_at, paused_by, created_at, updated_at, UNIQUE(namespace_id,name))`
- `endpoint_groups(id, hkey identity UNIQUE, site_id→sites CASCADE, client, name, description, low_watermark int DEFAULT 0, created_at, updated_at, UNIQUE(site_id,client,name))` — `_default` is auto-created per site+client.
- `uri_rules(id, endpoint_group_id→endpoint_groups CASCADE, kind CHECK(exact|template|prefix|regex), pattern, position int, created_at, updated_at)`
- `identity_types(id, site_id→sites CASCADE, client, name, description, spec jsonb, spec_yaml text, json_schema jsonb, version int DEFAULT 1, created_at, updated_at, UNIQUE(site_id,name))`
- `accounts(id, hkey identity UNIQUE, site_id→sites CASCADE, external_ref, region, tags, state CHECK(active|banned|disabled), ban_until, cooldown_until, notes, created_at, updated_at, UNIQUE(site_id,external_ref))`
- `identities(id, hkey identity UNIQUE, site_id→sites CASCADE, client, type_id→identity_types, account_id→accounts SET NULL, state CHECK(pending|active|expired|banned|quarantined|disabled|retired), state_reason, state_changed_at, ban_until (NULL+banned = permanent), quarantine_until, region, tags, labels jsonb, unique_hash bytea, payload_hash bytea, payload_version int DEFAULT 1, activated_at, last_used_at, created_by, created_at, updated_at, UNIQUE(type_id, unique_hash))`
- `identity_payloads(identity_id→identities CASCADE, version, ciphertext bytea, wrapped_dek bytea, kek_id, created_by, created_at, PRIMARY KEY(identity_id,version))`
- `proxies(id, hkey identity UNIQUE, namespace_id→namespaces, scheme CHECK(http|https|socks5), host, port, username_hint (first 4 chars + ***), display_url (no credentials), url_hash bytea, url_ciphertext bytea, url_wrapped_dek bytea, url_kek_id, url_version int, kind CHECK(datacenter|residential|mobile|tunnel), region, city, provider, max_concurrency int DEFAULT 1, tags, session_template, state CHECK(active|disabled|dead|banned|quarantined|retired), state_reason, state_changed_at, ban_until, cooldown_until, consecutive_check_failures int, last_check_at, last_check_ok bool, last_latency_ms int, exit_ip, next_check_at, created_at, updated_at, UNIQUE(namespace_id,url_hash))`
- `proxy_bindings(identity_id PK→identities CASCADE, proxy_id→proxies CASCADE, bound_at, rebind_day date, rebinds_today int)`
- `policies(id, namespace_id, kind CHECK(rotation|signal|action|breaker), name, description, current_version int DEFAULT 0, draft_yaml text, draft_updated_by, draft_updated_at, created_by, created_at, updated_at, UNIQUE(namespace_id,kind,name))`
- `policy_versions(policy_id→policies CASCADE, version, spec jsonb, spec_yaml, comment, created_by, created_at, PRIMARY KEY(policy_id,version))`
- `policy_bindings(id, policy_id→policies CASCADE, kind, namespace_id, site_id→sites CASCADE NULL, client NULL, endpoint_group_id→endpoint_groups CASCADE NULL, created_by, created_at)` + unique index on `(namespace_id, kind, coalesce(site_id,''), coalesce(client,''), coalesce(endpoint_group_id,''))`
- `hot_state_snapshots(site_id, subject CHECK(ie|ig|ps), subject_id, endpoint_group_id DEFAULT '', score float8, samples int, consecutive_failures int, last_failure_at, cooldown_until, reuse_until, last_used_at, updated_at, PRIMARY KEY(site_id,subject,subject_id,endpoint_group_id))`
- `state_events` (monthly partitions, PK(id,created_at)): `id, created_at, tenant_id, namespace_id, site_id, subject_kind CHECK(identity|account|proxy|endpoint_group|site), subject_id, endpoint_group_id, from_state, to_state, action, scope, until, permanent bool, outcome, policy_id, policy_version, rule, report_id, lease_id, actor, reason, shadow bool, details jsonb`
- `risk_events` (daily, PK(id,created_at)): `id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, identity_id, proxy_id, lease_id, report_id, node, token_id, uri, method, http_status int, business_code, error_kind, markers text[], outcome, blame, rule, latency_ms int, response_bytes bigint, started_at, finished_at`
- `outcome_stats_minutely` (daily, PK(bucket,namespace_id,site_id,endpoint_group_id,proxy_id,outcome)): `bucket, namespace_id, site_id, endpoint_group_id, proxy_id DEFAULT '', outcome, count bigint, latency_ms_sum bigint, response_bytes_sum bigint`
- `identity_stats_hourly` (monthly, PK(bucket,identity_id,endpoint_group_id,outcome)): `bucket, site_id, identity_id, endpoint_group_id, outcome, count bigint`
- `acquire_stats_minutely` (daily, PK(bucket,namespace_id,site_id,endpoint_group_id,result)): `result CHECK(ok|exhausted|circuit_open|site_paused|no_proxy|error)`, `count bigint, duration_us_sum bigint`
- `node_stats_minutely` (daily, PK(bucket,namespace_id,node)): `acquires, reports, abandoned, rejected bigint`
- `payload_access_minutely` (daily, PK(bucket,namespace_id,token_id,identity_type_id)): `count bigint`
- `breaker_events(id PK, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, from_state, to_state, trigger CHECK(auto|manual|probe|site_switch), reason, open_until, metrics jsonb, actor)`
- `config_items(id, namespace_id, group_name, key, format CHECK(json|yaml|text), schema jsonb, description, current_version int DEFAULT 0, draft_content text, draft_updated_by, draft_updated_at, created_by, created_at, updated_at, UNIQUE(namespace_id,group_name,key))` — group names starting with `_` are reserved.
- `config_versions(item_id→config_items CASCADE, version, content text, comment, source_version int, published_by, published_at, PRIMARY KEY(item_id,version))`
- `config_version_floors(namespace_id→namespaces CASCADE, group_name, key, last_version int CHECK(>=0), updated_at, PRIMARY KEY(namespace_id,group_name,key))` (migration 00004)
  — deleting a config item records its last version; the next version published under the same namespace/group/key is
  `max(current_version, last_version) + 1`, so a deleted and re-created item never reuses a version a node may hold
  (nodes identify content by group, key and version only).
- `secrets(id, namespace_id, path, description, tags, current_version int, expires_at, last_accessed_at, created_by, created_at, updated_at, UNIQUE(namespace_id,path))`
- `secret_versions(secret_id→secrets CASCADE, version, ciphertext, wrapped_dek, kek_id, created_by, created_at, PRIMARY KEY(secret_id,version))`
- `audit_logs` (monthly, PK(id,created_at)): `id, created_at, tenant_id, namespace_id, actor_kind CHECK(user|token|system), actor_id, actor_name, action, resource_kind, resource_id, resource_name, result CHECK(ok|denied|error), ip, user_agent, details jsonb`
- `notification_channels(id, tenant_id, namespace_id NULL, name, kind CHECK(webhook|feishu|dingtalk|wecom|telegram), config_ciphertext, config_wrapped_dek, config_kek_id, event_types text[], site_ids text[], min_severity CHECK(info|warning|critical), enabled bool, last_delivery_at, last_delivery_status, created_by, created_at, updated_at, UNIQUE(tenant_id,name))`
- `alert_events(id PK, created_at, tenant_id, namespace_id, site_id, kind, severity, title, message, details jsonb, dedup_key, deliveries jsonb)`
- `system_keys(name PK, ciphertext, wrapped_dek, kek_id, created_at, updated_at)` — e.g. `dedupe_pepper` (32 random bytes).
- `system_settings(key PK, value jsonb, updated_at)`.

sqlc: one config `sqlc.yaml`, engine postgresql, schema = migrations dir, queries = `internal/store/postgres/queries/*.sql`
(one file per domain: `tenancy.sql`, `auth.sql`, `site.sql`, `identity.sql`, `proxy.sql`, `policy.sql`, `config.sql`,
`secret.sql`, `events.sql`, `stats.sql`, `notify.sql`, `audit.sql`, `hotstate.sql`), output package
`internal/store/postgres/db` (`sql_package: pgx/v5`, `emit_json_tags`, `emit_empty_slices`,
`emit_pointers_for_null_types`). **Always regenerate with `scripts/sqlc-generate.sh`** (serializes concurrent runs).
Dynamic filter queries that sqlc cannot express may use pgx directly inside the owning package.

---

## 5. Redis hot state (`internal/store/redis`, `internal/hotstate`)

Prefix `P` = `SPINNERET_REDIS_PREFIX` (default `sp`). Site tag `T` = `{s<siteHkey>}`. Report shard tag `R` = `{r<shard>}`.
All keys of one site share the tag so Lua scripts are atomic under Redis Cluster; scripts receive `KEYS[1] = P:T:meta`
and derive the site base `P:T:` by stripping the trailing `meta`. Numeric fields are base-10 strings; timestamps are Unix ms.

| Key | Type | Content |
| --- | --- | --- |
| `P:T:meta` | HASH | `ns` namespace id, `site` site id, `built` epoch id, `paused` 0/1 |
| `P:T:rdy:<eg>` | ZSET | identity hkey → available-at ms (only identities in `active`/`pending` and of an allowed type) |
| `P:T:hs:<eg>` | HASH | identity hkey → packed `score|sts|samples|nfail|lastfail|cd|ru|lu` (see 5.1) |
| `P:T:id:<i>` | HASH | identity state (5.2) |
| `P:T:acc:<a>` | HASH | `st` active/banned/disabled, `bu` ban until (−1 permanent), `cd` cooldown until, `sct` change time of `st`/`bu` (5.8) |
| `P:T:accm:<a>` | SET | identity hkeys of the account |
| `P:T:ls:<leaseId>` | HASH | lease (5.3) |
| `P:T:lsexp` | ZSET | leaseId → expires ms (active leases only) |
| `P:T:stk:<eg>:<session>` | STRING+PX | identity hkey; `<session>` = raw key if ≤64 chars of `[A-Za-z0-9_.:-]`, else hex sha256[:32] computed in Go |
| `P:T:q:<eg>:<i>` | HASH+PX | quota counters: `<windowMs>:c` window index, `<windowMs>:n` current count, `<windowMs>:p` previous count; PX = 2×max window |
| `P:T:px:<p>` | HASH | proxy per site (5.4) |
| `P:T:pxrdy` | ZSET | proxy hkey → available-at ms (only `active` proxies) |
| `P:T:win:<eg>:<bucket>` | HASH+PX | `t` total, `s` success, `r` risk; bucket = floor(ms / bucketMs); PX = 2×window, set when `observe.lua` creates the bucket (a bucket is summed for at most one window after its start, so refreshing it per report is wasted work) |
| `P:T:winh:<eg>:<bucket>` | HLL+PX | identity hkeys that produced `captcha` |
| `P:T:brk:<eg>` | HASH | breaker (5.5) |
| `P:T:cnt:<subj>:<outcome>:<windowMs>` | HASH+PX | bucket index → count; bucketMs = max(windowMs/60, 1000); `<subj>` = `i<hkey>`, `a<hkey>`, `p<hkey>` |
| `P:T:xa:p:<p>` | ZSET+PX | identity hkey → last risk ms (cross attribution) |
| `P:T:xa:i:<i>` | ZSET+PX | proxy hkey → last risk ms |
| `P:T:bans:<i>` | ZSET | ban timestamps (member = ms string), trimmed to 30 d |
| `P:T:rcd:<eg>` | ZSET | member `<i>|<prevCd>|<prevNfail>` → applied ms; trimmed to breaker window×2 (written by `apply.lua` for automatic identity×endpoint cooldowns) |
| `P:T:rcds` | ZSET | member `<i>|<prevScd>` → applied ms; trimmed to 10 min (automatic identity×site cooldowns; used by revert mode `all`) |
| `P:T:axr:<token>` | STRING+PX | recorded `apply.lua` results of one executor call (JSON), PX 15 min; token = `r:<leaseId>:<reportId>:<digest of now and operations>` (6.6) |
| `P:T:brko` | SET | eg hkeys whose breaker is not `closed` (maintained by breaker scripts) |
| `P:T:ckpt` | HASH | shard → last applied stream id `ms-seq` (worker idempotency) |
| `P:T:dirty` | SET | `e<eg>:<i>`, `g<i>`, `p<p>` entries changed since last snapshot |
| `P:T:rr:<eg>` | STRING | round-robin cursor (identity hkey) |
| `P:T:aeg` | ZSET | eg hkey → last activity ms (breaker evaluation candidates) |
| `P:T:lock:<name>` | STRING+PX | per-site job locks (`reap`, `brk:<eg>`) |
| `P:R:stream` | STREAM | report events (6.2) |
| `P:R:dd:<reportId>` | STRING+PX | report dedup, PX = `SPINNERET_REPORT_DEDUP_TTL` (1 h) |
| `P:R:owner` | STRING+PX | owning instance id |
| `P:meta:epoch` | STRING | hot-state epoch id; missing ⇒ full rebuild required |
| `P:workers` | ZSET | instance id → heartbeat ms |
| `P:rtv:<nsId>` | HASH | runtime config versions: `breakers`, `site_switches` |
| `P:catv` | HASH | catalog change marks: namespace id → counter (§11) |
| `P:sess:<hash>` | HASH+PX | console session |
| `P:rl:<kind>:<key>` | STRING+PX | rate-limit counters |
| `P:alert:<dedupKey>` | STRING+PX | alert de-duplication |
| `P:lock:<name>` | STRING+PX | global locks |
| `P:ch:<channel>` | Pub/Sub | event bus channels (§11) |

### 5.1 Identity × endpoint-group packed state (`hs`)
`score|sts|samples|nfail|lastfail|cd|ru|lu` — `score` float (2 decimals), `sts` ms of last score update,
`samples` int, `nfail` consecutive failures, `lastfail` ms, `cd` cooldown until ms, `ru` reuse until ms, `lu` last used ms.
Missing entry ⇒ `score=baseline, sts=now, samples=0, nfail=0, lastfail=0, cd=0, ru=0, lu=0`.

### 5.2 Identity hash `P:T:id:<i>`
`iid` identity id · `st` state · `ty` identity type name · `tv` type version · `pv` payload version · `acc` account hkey or "" ·
`rg` region · `bu` ban until (−1 permanent) · `qu` quarantine until · `scd` site cooldown until · `sru` site reuse until ·
`al` active leases · `xl` exclusive-lease until (max expiry of active leases whose `mc`=1) · `act` activated-at ms ·
`xg` the endpoint groups that pushed the identity's `rdy` score because of `xl`, written by the acquire that pushed it as
`,<eg>,` and extended to `,<eg>,<eg>,`; `release.lua`/`reap.lua` restore only those groups (intersected with the groups of
the lease's client) and clear `xg`, which keeps a lease end O(groups that really pushed) instead of
O(endpoint groups of the client) — measured 60 µs instead of 96 µs of Redis CPU per lease end on a client with 50 groups.
An absent marker means nothing pushed; the literal `1` written by releases before this encoding means "every group of the
client" and is still honoured. A marker lost with the hash only delays a restore by one lease TTL ·
`px` bound proxy hkey or "" · `rbd` rebind day `YYYYMMDD` · `rbn` rebinds that day ·
`gs` global score · `gts` global score ts · `gn` global samples · `lu` last used ms ·
`sct` change time (ms) of the lifecycle fields `st`/`bu`/`qu`/`act` (5.8).

### 5.3 Lease hash `P:T:ls:<leaseId>`
`i` identity hkey · `iid` identity id · `e` eg hkey · `p` proxy hkey or "" · `pid` proxy id · `n` node · `tk` token id ·
`ns` namespace id · `sk` session key · `a` acquired ms · `x` expires ms · `cap` lifetime cap ms · `ttl` lease ttl ms ·
`st` `active|released|expired` · `end` ended ms · `pr` probe 0/1 · `mc` max concurrent · `ri` reuse interval ms ·
`ra` `a|r` reuse anchor · `rs` `e|s` reuse scope · `rc` report count (processed by the worker) ·
`pru` reuse value before an `acquired` anchor raised it (only then) · `rv` reports ingested ·
`kx` keep-until ms (ingest time of the latest timely report + `SPINNERET_REPORT_DEDUP_TTL`).
While active the key has no TTL (the reaper resolves the identity and proxy of an overdue lease only from it);
when ended it is set to `PEXPIRE max(late_window, kx − now)` (`SPINNERET_LATE_REPORT_WINDOW`, default 10 m), so a
lease with reports still queued for the worker stays readable after the late window.

### 5.4 Proxy-per-site hash `P:T:px:<p>`
`pid` proxy id · `st` state · `kd` kind · `rg` region · `pv` provider · `tg` `,tag1,tag2,` · `mc` max concurrency ·
`al` active leases · `cd` site cooldown until · `gcd` global cooldown until · `sc` score · `sts` score ts · `sn` samples ·
`nf` consecutive failures · `lf` last failure ms · `uv` url version · `bu`/`qu` ban / quarantine end (mirror
`proxies.ban_until`) · `sct` change time of `st`/`bu`/`qu` (5.8).
Global proxy changes (state, attributes, global cooldown) are propagated by Go to every site of the namespace.
Synchronizations keep `gcd = max(Redis, PostgreSQL)`: automatic proxy-global cooldowns exist only in Redis and are never
shortened by a sync (manual proxy operations overwrite `gcd` with `cooldown.lua` after syncing).

### 5.5 Breaker hash `P:T:brk:<eg>`
`st` `closed|open|half_open` · `ou` open until ms (0 with `man=1` = indefinite) · `oc` consecutive opens ·
`lo` last opened ms · `lc` last closed ms · `man` 0/1 manual · `hw` half-open window start ms · `hc` probes issued in window ·
`ps` probe samples · `pk` probe successes · `rsn` reason · `v` version (incremented on every transition).
Missing hash ⇒ closed.

### 5.6 Availability rule (implemented once in `common.lua` as `avail(base, eg, i, now)`)
```
avail = max(hs.cd, hs.ru, id.scd, id.sru, (id.al > 0 ? id.xl : 0), acc.cd (if account))
```
`common.lua` exposes the rule as `sp_avail` and, for callers that only need the two `hs` fields of it,
`sp_hs_cd_ru(packed)`, which reads `cd` and `ru` without decoding the other six (0.6 µs against 2.3 µs).
A caller that already holds the identity hash and the `hs` entry — `apply.lua` applying a cooldown — computes the
same maximum from those values instead of reading them again.
Bans/expiry/quarantine/disabled remove the identity from every `rdy` ZSET instead of pushing its score.
Identity-level pushes use `ZADD XX GT` on every `rdy:<eg>` of the identity's site+client (Go passes the eg hkey list).
Cold rebuild adds uniform jitter 0–60 s to scores that would otherwise be ≤ now.

### 5.7 Lua scripts
All scripts are stored under the owning package (`<pkg>/lua/*.lua`), embedded, and registered through
`store/redis.NewScript(name, body)`, which prepends the helpers of `internal/store/redis/lua/common.lua` that the body
uses. Owners and contracts:

| Script | Owner | Purpose |
| --- | --- | --- |
| `acquire.lua` | scheduler | breaker gate, sticky, sample/filter/select, proxy selection, write lease(s) |
| `renew.lua` | scheduler | extend lease with lifetime cap |
| `release.lua` | scheduler | end lease, reuse anchor (or abort an undelivered lease), recompute availability |
| `reap.lua` | scheduler | expire overdue leases in batches, report abandoned (`rv = 0` and `rc = 0`) |
| `lease_retain.lua` | signal | per **site** of a request: for each of its leases, namespace lookup, `rv += n`, `kx`, retain an ended lease hash |
| `ingest.lua` | signal | per shard: dedup `SET NX PX` + `XADD MAXLEN ~` for N reports |
| `observe.lua` | worker | per report: idempotency ckpt, lease report count/quota, window buckets, probe stats, health EWMA, streaks, counters, cross attribution, ban counts |
| `apply.lua` | action | apply cooldown/ban/expire/quarantine/activate/unban for identity/account/proxy scopes (+optional release) |
| `revert.lua` | breaker | revert recent endpoint cooldowns |
| `breaker_eval.lua` | breaker | sum window, trip / half-open close / reopen transitions |
| `breaker_set.lua` | breaker | manual open/close |
| `sync_*.lua` | hotstate | identity/proxy/eg materialization helpers |

Script ARGV/return shapes are defined by the owning package; the **key schema and field encodings above are frozen**.

`NewScript` does not prepend the whole of `common.lua`: the library is split into `--@sp <name>` sections and only the
helpers a body names, plus their transitive dependencies, are emitted. Redis recreates every chunk-level closure on every
call (about 0.14 µs each on Valkey 8.1), so a script that uses two helpers must not pay for eighteen; the emitted text is a
pure function of the body, and a helper named only in a comment is not pulled in. Helper names and semantics are part of
the contract and are unchanged.

Three conventions of the hot path are load-bearing for its cost and are relied on by the scheduler scripts:

* **Integral `redis.call` arguments travel as Lua numbers, not through `sp_int_str`.** The server renders a number
  argument with the shortest representation that round-trips, which for an integer of magnitude ≤ 2^53 is exactly its
  digits (verified on Valkey 8.1 for 1758011411962, 9007199254740992 and −0 → `"0"`). This applies to `redis.call`
  arguments only: a number placed in a reply table comes back as a RESP integer, and a number concatenated into a string
  still goes through `"%.14g"`.
* **Numerals on the wire stay short.** Lua 5.1 parses a numeral of ten digits or more about eight times slower than a
  short one (0.58 µs against 0.07 µs), so `sp_dnum` splits a 10–15 digit numeral into two halves of at most nine digits
  and recombines them, which is exact. Fractions are *not* split that way — it rounds twice and disagrees with `tonumber`
  in the last bit — so `acquire.lua` receives its fractions as parts-per-million integers instead (§6.1).
* **`release.lua` and `reap.lua` receive the endpoint group layout packed and parse it lazily.** The layout is the number
  of clients followed by one ARGV entry per client holding that client's `hkey,baseline` pairs, comma-separated; expanding
  it to two entries per group would put 101 bulk strings on the wire per release on a site with 50 endpoint groups, and
  Valkey spends about 0.1 µs per ARGV entry just receiving them. The scripts parse an entry only when a lease end has to
  walk the other groups of the client (`xg`, §5.2) or when the packed state of the identity has no usable score, which
  never happens for an entry `acquire.lua` wrote. Go caches the layout per catalog snapshot.
* **Constants of a compiled policy travel packed, and what Go can compute Go computes.** `observe.lua` receives the four
  health constants as one `alpha,baseline,tau,failure_reset_after` entry and the four cross-attribution constants as one
  `enabled,window,proxy_distinct_identities,identity_distinct_proxies` entry, which it decodes only for a risk outcome
  that has both subjects and is not late; it also receives the breaker window bucket (`<eg>:<floor(now/bucket)>`) and the
  bucket key TTL rather than the bucket length, so that no key is built from a Lua number. Arguments are read from `ARGV`
  by index — an accessor closure called once per argument costs more than the arguments themselves.
* **A field a script does not change is spliced back, never re-encoded.** `sp_hs_raw(packed)` returns the eight fields of
  an `hs` entry as the strings they are on the wire (or `nil` when the value is not in that form, which sends the caller
  to `sp_hs_unpack`). `observe.lua` therefore parses `score`, `sts`, `samples` and `nfail`, never touches `cd`, `ru` and
  `lu`, and splices `ARGV[4]` itself in where it writes `now` into `sts` or `lastfail`; `apply.lua` rewrites only `cd`.
  The result is byte-identical to `sp_hs_pack` for every entry this system writes.
* **A closure is created on every call, so a script creates only the ones it can reach.** A chunk-level closure with
  upvalues costs about 0.38 µs per call on Valkey 8.1. `apply.lua` decodes its operations first and then defines only the
  handlers those operations name (and, for anything but a cooldown, five shared helpers a cooldown cannot reach);
  `breaker_eval.lua` creates its four transition closures only in mode `eval`. Dispatch tables are replaced by an
  `if`/`elseif` chain and constant sets by a delimited string, because a table constructor is an allocation per call.

### 5.8 Lifecycle change time (`sct`) and PostgreSQL-first synchronization
Lifecycle fields are written from two directions: automatic changes Redis-first (`apply.lua`, persisted later by the
StateWriter) and admin operations, reverts and expiry PostgreSQL-first (then pushed with `apply.lua` "set" and
`hotstate.Syncer`). To keep both directions from reverting each other, every lifecycle write records its change time
`sct` (ms) on `id`, `acc` and `px` hashes: `apply.lua` writes `now`, the sync scripts write the PostgreSQL change time
(`identities.state_changed_at`, `proxies.state_changed_at`; for accounts, which have no such column, the time of the
latest non-shadow, non-cooldown account `state_events` row, falling back to `created_at`). Rules:
- Authoritative syncs (`SyncIdentities`, `SyncAccounts`, `SyncProxies`, rebuild) apply the PostgreSQL lifecycle fields
  unless Redis holds a newer change (`sct` > PostgreSQL change time) that is still in force; a Redis-first temporary ban
  or quarantine whose end has passed never outlives an older PostgreSQL state. Merge syncs (`SyncSite`) keep the Redis
  lifecycle fields whenever present. Non-lifecycle fields (type, region, tags, bindings, …) always come from PostgreSQL.
- PostgreSQL-first writers set the change time on every lifecycle change (including a re-quarantine that only moves the
  end) and never on attribute edits, so tag/region/payload-attribute edits cannot revert an automatic ban the
  StateWriter has not persisted yet.
- `apply.lua` "set" (the push of a committed PostgreSQL-first change) runs at the commit time and is skipped with reason
  `newer_state` when the subject holds a later Redis-first change; the StateWriter then persists that later change.
- Health resets of a sync (`ResetHealth`) reset each existing `hs` entry to the group baseline (score, sts, samples,
  nfail, lastfail) and keep `cd`, `ru` and `lu`, like `apply.lua`; `ResetHealth` and `ResetFailures` also clear the
  identity's global failure streak `gnf`/`glf`.

---

## 6. Hot paths

### 6.1 Acquire (`scheduler`)
Go:
1. Resolve namespace snapshot from principal; `site` by name (`site_unknown`), `client ∈ site.clients` (`client_unknown`).
2. `principal.Require(lease:acquire, site)` (`scope_missing`). Site paused (catalog) → `unavailable/site_paused`.
3. Endpoint group: explicit `endpoint_group` (`endpoint_group_unknown`) or URI match on the path part
   (must start with `/`, ≤ 2048 bytes, else `uri_invalid`); fallback `_default`.
4. Run `acquire.lua` with rotation params. `wait_ms` (≤ 5000): retry with delays 50,100,150,200,200… ms until deadline.
5. On success render credential (`identity.PayloadCache`) and proxy (`proxy.Resolver`); on render failure (or when the
   request context ends before the response) release the lease in abort mode and return `internal`: an undelivered lease
   applies no `released` reuse anchor, restores a reuse value raised by the `acquired` anchor (`pru`, unless a later lease
   raised it again; for the site scope the scores other groups pushed up to the raised value meanwhile are pulled back)
   and rolls back its quota count and half-open probe slot (`hc`). Response
   `hints.renew_before_ms = lease_ttl/4`.
6. Record metrics, `acquire_stats_minutely`, `payload_access_minutely`, optional ClickHouse `lease_events`.

`acquire.lua` ARGV is not frozen and is shaped for the cost of parsing it in Lua (§5.7): the fractions
(`probe.weight_factor`, `warmup.quota_factor` and the random values) travel as parts-per-million integers and the script
divides by 10⁶, the proxy filter lists and quota windows are only materialized when the policy has any, and the identity
`HMGET` of the filter step leaves out the five fields that only the quota windows and the proxy modes read
(`act`, `px`, `rbd`, `rbn`, `rg`) when neither is configured. None of this changes which identity is chosen: the
parts-per-million values are the same numbers the decimal form carried, rounded the same way.

`acquire.lua` algorithm (single call, `count` 1–50 for AcquireBatch):
1. Breaker gate on `brk:<eg>`: `open` & manual & (ou==0 or now<ou) → `BREAKER_OPEN retry`;
   `open` & now<ou → `BREAKER_OPEN retry=ou-now`; `open` & now≥ou → transition to `half_open` (hw=now, hc=0, ps=0, pk=0, v++)
   and return transition flag. `half_open`: reset window if now−hw ≥ 10000; if hc ≥ probe limit → `BREAKER_OPEN retry=hw+10000−now`;
   otherwise leases issued are probes (`pr=1`, hc++) and selection uses `best_health`. Probes issue at most `count=1`.
2. Sticky (count==1 & session key): candidate from `stk` is accepted when state ∈ {active,pending}, not in cooldown
   (`hs.cd`, `id.scd`, `acc.cd`), concurrency and quota OK — **reuse interval is bypassed for sticky reuse** [decision].
3. Sampling loop (≤ 3 rounds): `ZRANGEBYSCORE rdy -inf now LIMIT 0 want+taken` with `want = max(K, count − picked)`
   (`taken` = identities already picked or tried by this call, which are skipped); a round's picks never end the loop,
   only a filled batch, 3 proxy failures or a round returning fewer members than its limit (the due range is exhausted).
   For each candidate filter in order, pushing the ZSET score when filtered:
   state not active/pending → `ZREM`; ban/cooldown/reuse/account constraints → push to `avail()`;
   `al ≥ mc` (pending: `al ≥ min(mc, probe.max_leases)`) → push to `now + min(ttl, 5000)`;
   quota: for each window `est = p·(1 − elapsed/w) + n`, limit × (warmup factor if `now − act < warmup`) → push to window end;
   proxy constraints for `bind_identity` (bound proxy cooling ≤ tolerance → push to proxy avail; dead/banned/longer → rebind candidate,
   rebind limit per day exceeded → push 10 min).
4. Selection among survivors: `weighted_random` (w = max(decayed score,5)² × probe factor for pending; rejection sampling
   with Go-supplied random values, then a roulette over every weight), `least_recently_used` (min `hs.lu`),
   `round_robin` (smallest hkey > `rr` cursor, wrap), `best_health` (max score).
   The packed `hs` of a sampled candidate is read **on demand**, one `HGET` per candidate the selection really looks at:
   `weighted_random` accepts one of its first two or three proposals, so reading the whole sample up front costs more than
   the individual reads (`HMGET` of 32 fields is 3.1 µs against 0.39 µs for one `HGET`). `least_recently_used`,
   `best_health` and the exact roulette rank the whole sample and read what is still missing in one `HMGET`. The candidate
   set and the chosen candidate are exactly those of a read-everything implementation.
5. Proxy (modes `none|pool|bind_identity|region_match`): pool selection samples `ZRANGEBYSCORE pxrdy -inf now LIMIT 0 16`,
   filters `st=active`, `al<mc`, kinds/providers/regions ∈ lists, all tags present, region match; weighted by max(score,5)²;
   none available → `NO_PROXY retry` (no lease written).
6. Write lease(s): lease hash, `lsexp`, `id.al` (set to the value the filter step read plus one, in the same `HSET` as
   `id.lu` — the hash was read by this script, so no `HINCRBY` round trip is needed), `hs.lu`, `xl` when mc=1, reuse anchor
   `acquired` sets `ru`/`sru`, quota `n++`, proxy `al++`, sticky `SET PX`, `rr` cursor, `aeg`. New score for `rdy:<eg>` =
   `avail()` (≥ lease expiry when mc=1). Returns identity/proxy ids, payload/type versions, expiry,
   probe/sticky/rebound flags.
7. No survivors after 3 rounds → `EXHAUSTED retry = clamp(firstScore − now, 50, 60000)` (60000 when the ZSET is empty).

### 6.2 Report ingest (`signal`)
1. Token needs `report:write` for the lease site (`site name` from catalog by lease site key). Max 500 reports.
2. Validate each report (ids, uri, times, enums); invalid → `rejected{report_id, reason}` with reasons
   `invalid_argument`, `lease_unknown`, `scope_missing`.
3. `lease_retain.lua` once per **site** of the request (pipelined, at most 100 lease keys per call): all lease keys of a
   site share its hash tag, so the batch is single-slot under Redis Cluster, and a 100-report request stops paying 100
   EVALSHA dispatches and preludes (measured 9.2 µs per lease before, 2.0 µs after). Missing or other namespace →
   `lease_unknown`; otherwise `rv += reports of the lease in the request`; when the reports are timely (lease active, or
   `now − end ≤ late_window`, the worker's lateness rule) also `kx = max(kx, now + SPINNERET_REPORT_DEDUP_TTL)` and an
   ended lease has its TTL raised to the dedup TTL with `PEXPIRE … GT`, so a worker backlog does not turn timely reports
   into statistics-only ones. An active lease hash carries no TTL (§5.3) and is not given one. Late reports never extend
   the retention: an ended lease hash lives at most `late_window + dedup TTL`.
4. Group by shard; `ingest.lua` per shard: `SET dd NX PX` then `XADD P:R:stream MAXLEN ~ <SPINNERET_STREAM_MAXLEN> * v 1 d <json>`.
5. Response `{accepted, duplicated, rejected[]}`.

Stream event JSON (`d` field), produced only by `signal.EncodeEvent`:
```json
{"rid":"…","lid":"…","ns":"ns_…","tn":"ten_…","tok":"tok_…","node":"crawler-hk-03","rcv":1758011411962,
 "uri":"/a/b","m":"GET","hs":200,"bc":"0","ek":"","mk":["empty_list"],"oh":"","lat":842,"rb":48213,
 "sa":1758011411120,"fa":1758011411962,"rel":true}
```

### 6.3 Worker (`worker`)
- Instance registry `P:workers` heartbeat every 2 s; live = heartbeat within 10 s.
- Shard ownership: `SET P:R:owner <instance> NX PX 10000`, renewed every 3 s by compare-and-pexpire;
  target share = ceil(shards / live instances); release shards above share; acquire free shards below share.
- Per owned shard: consumer group `workers` (`XGROUP CREATE … MKSTREAM`), on takeover `XAUTOCLAIM` everything pending,
  then `XREADGROUP BLOCK 2000 COUNT 100 >`; process sequentially; `XACK` after each batch.
- Stream retention: at most every 5 s an owner trims its shard with `XTRIM … MINID ~ <floor>`, where the floor is the
  oldest entry the group still has pending (`XPENDING` summary) or, when nothing is pending, the last entry this
  consumer acknowledged. `SPINNERET_STREAM_MAXLEN` stays the hard cap of a *backlog*; without the trim a drained
  stream keeps every processed entry until the cap evicts it (16 shards × 1M × ≈440 B ≈ 7 GiB of Redis for the life
  of the deployment).
- Per event: load lease (`HMGET`), classify (`policy.CompiledSignal.Classify`), `observe.lua`, evaluate actions
  (`policy.CompiledAction.Evaluate`), execute (`action.Executor`), release when `rel`, breaker notify for risk outcomes,
  aggregate stats, queue risk event (non-success) and ClickHouse row.
- Execute is retried after a failure only when the executor declares itself idempotent per report
  (`worker.IdempotentActionExecutor`: a repeated call with the same lease id, report id, `Now` and plan returns the
  results of the call that applied the actions); otherwise a failed call, whose outcome is unknown, is logged and not
  repeated.
- Suppression: while the breaker is `open` (non-probe lease) or the report is later than `late_window` after lease end,
  health/actions are skipped and only statistics are recorded.
- Lease missing (expired beyond window) → statistics only at site level with `endpoint_group_id=''`.
- Site missing from this instance's catalog snapshot (a site created moments ago; snapshots propagate
  asynchronously) → reload that namespace synchronously (at most once every 2 s per namespace) and retry the
  lookup; only a report whose site is still unknown afterwards is dropped.

### 6.4 Health score (`observe.lua`)
Observation values (overridable in action policy `health.observations`): `success 100, empty 60, network_error 50
(only when blamed on identity), rate_limited 30, forbidden 10, captcha 0`; other outcomes don't change identity scores.
Decay on read: `score = baseline + (score − baseline)·exp(−Δt/τ)`; update: `score = α·v + (1−α)·score`.
Streak: failure outcomes (`empty rate_limited captcha auth_invalid forbidden banned`, plus `network_error` when blamed on
identity) → `nfail = (now − lastfail > failure_reset_after ? 0 : nfail) + 1`, `lastfail = now`; `success` → `nfail = floor(nfail/2)`.
Proxy×site score uses the same math with outcomes blamed on the proxy plus `success` and health-check observations
(`proxy_error 0`, `network_error 40`, `rate_limited 30`, `success 100`).
Identity global score `gs` is an EWMA over all identity-affecting observations (request-weighted by construction).

### 6.5 Blame and cross attribution
Default blame: `empty identity`, `rate_limited both`, `captcha identity`, `auth_invalid identity`, `forbidden identity`,
`banned identity`, `proxy_error proxy`, `network_error proxy`, others none. Signal rules may override via `blame`.
For risk outcomes (`rate_limited captcha forbidden banned`) with a proxy: record `xa:p:<p>` and `xa:i:<i>` (window 10 m);
if distinct identities on the proxy ≥ 3 and distinct proxies on the identity < 3 → blame `proxy`; if distinct proxies on the
identity ≥ 3 → blame includes `identity`. Window and thresholds are in `action.cross_attribution`.

### 6.6 Actions
Severity: permanent ban (5) > temporary ban (4) > expire (3) > quarantine (2) > cooldown (1); ties → longer duration.
One action per subject per report. Cooldown duration `min(base·multiplier^min(n−1, max_exponent), max)·U(0.8,1.2)`,
where `n` is the streak of the scope's subject **after** this report (identity×eg streak for `identity_endpoint`,
identity global streak approximated by the max eg streak for `identity_site`/`account`, proxy streak for proxy scopes).
Escalation applies when a temporary ban is chosen: the matching step with the highest threshold replaces the duration
(ban counts from `bans:<i>` include the ban being applied).
Health thresholds (from action policy): eg score < 15 with ≥ 10 samples → cooldown identity_endpoint 6 h;
global score < 20 with ≥ 10 samples → quarantine (default 24 h, then pending).
`pending` identity + `success` → `activate` (→ active, `act=now`).
`mode: shadow` → state events with `shadow=true`, nothing applied.
Lifecycle state writes are Redis-first (in `apply.lua`) then PostgreSQL via `action.StateWriter` (batched every
200 ms / 500 rows, retried) including `state_events`. Admin operations are PostgreSQL-first then `hotstate.Syncer`
(ordering between both directions: 5.8).

Executor idempotency: the `apply.lua` calls of one report carry the token `r:<leaseId>:<reportId>:<digest>` (digest of
`now` and the operations). The first call records its results in `P:T:axr:<token>` (15 min); a retry of the same call
(e.g. after a client-side timeout of a call that did run) returns the recorded results instead of `already_banned`, so
the retried `Execute` still queues the state changes and events. State events of a report get deterministic IDs
(`evt_` + change ms + plan/member position + digest) and the StateWriter inserts events with `ON CONFLICT DO NOTHING`,
so a replayed report stores its events once.

StateWriter failure handling (never drops lifecycle changes on transient PostgreSQL failures):
- PostgreSQL unavailable (connection/network errors, timeouts, SQLSTATE classes 08, 53, 57 except 57014, 58): the batch
  is kept and retried with an exponential backoff (200 ms doubling to 5 s, reset on success); queued changes stay queued.
- Data errors (SQLSTATE 22xxx, 23xxx, 54000): the batch is split until the rejected changes are isolated; only those are
  dropped. A missing partition is retried 5 times first. Other errors are retried 5 times, then the batch is dropped.
- Queue: at most 100 000 changes. When full, the oldest non-lifecycle changes (cooldown and shadow events) are spilled
  first; enqueueing a lifecycle change waits for space (backpressure on the worker, at most 1 min for all changes
  of one report). Metrics
  `spinneret_state_writer_pending_changes`, `spinneret_state_writer_dropped_changes_total`,
  `spinneret_state_writer_spilled_changes_total`.
- The account row guard compares with the account's latest lifecycle `state_events` row (accounts have no
  `state_changed_at`; `updated_at` is bumped by attribute edits and cooldowns). Revert candidates of automatic account
  bans use the same rule.

Ban/quarantine expiry (PostgreSQL-first): a pass releases nothing while Redis does not answer `PING`. Subjects whose
push fails after the commit are re-synchronized from PostgreSQL at the start of every following pass until that
succeeds (in memory, at most 100 000 subjects). The first pass of an instance (a new leader), a pass after missed passes
and a pass after the retry set overflowed re-synchronize the expiry releases recorded in `state_events` during the last
10 minutes.

### 6.7 Breaker
Window 60 s / 12 buckets; evaluated every 5 s for eg with activity in the last 2 min or non-closed state, and at most once
per second per eg when the worker observes a risk outcome. Trip when samples ≥ `min_requests` and any of:
risk ratio ≥ 0.4, distinct captcha identities ≥ 10, success ratio ≤ 0.2. Open duration `open_duration·2^(oc−1)` capped by
`max_open_duration`; `oc` resets when closed for ≥ 30 min. Half-open: ≤ 5 probe leases per 10 s; close when probe samples ≥ 5 and
success ratio ≥ 0.8; reopen (oc+1) when samples ≥ 5 and ratio < 0.8. Every transition: `breaker_events` row,
`P:rtv:<ns> breakers` HINCRBY, bus event, alert. Manual open (duration or indefinite) / close and site switch are admin operations.

### 6.8 Background jobs (`jobs` + owners)
| Job | Period | Coordination | Owner |
| --- | --- | --- | --- |
| report consumers | continuous | shard ownership | worker |
| lease reaper | 1 s | per-site Redis lock `lock:reap` | scheduler |
| breaker evaluation | 5 s | per-eg Redis lock | breaker |
| ban / quarantine expiry | 10 s | leader | action |
| stats flush (PG aggregates) | 10 s | each instance | worker |
| state writer flush | 200 ms | each instance | action |
| proxy health checks | 60 s | proxy hkey % live workers == my index | proxy |
| hot-state snapshot | 60 s | leader | hotstate |
| partition manager + retention | 1 h (drops daily) | leader | store/postgres |
| alert evaluation (incl. identity_expired recovery from `state_events`) | 30 s | leader | notify |
| token last-used flush | 30 s | each instance | auth |
| KEK rewrap | on demand | leader | vault |
Leader election: `pg_try_advisory_lock(hashtext('spinneret:<job>'))` on a dedicated connection (`jobs.Leader`).

---

## 7. Policies (`internal/policy`)

Specs are YAML (stored as `spec_yaml`) + canonical JSON (`spec`). `Duration` accepts `ms|s|m|h|d` suffixes and
`permanent` (−1). Unknown fields are rejected. Each kind has `name`, optional `description`, optional `bind`
(`{site, client, endpoint_group}`, converted to a binding on create). Resolution per endpoint group:
binding on (site, client, eg) > (site, client) > (site) > namespace default (site NULL) > built-in default.
`signal` and `action` support `extends: <policy name>` (parent rules first, child rules appended; cycles rejected; depth ≤ 5).
Policies have drafts: `draft_yaml` is edited (`policy:write`), publishing validates and creates a version (`policy:publish`);
rollback publishes an old version's YAML as a new version. Publishing invalidates catalog snapshots of the namespace.

Rotation (defaults in brackets): `identity_types [] (all types of the site+client)`, `rotation.strategy [weighted_random]`,
`candidate_sample [32] (1..256)`, `lease_ttl [120s] (5s..30m)`, `max_lease_lifetime [30m]`, `max_concurrent_leases [1] (1..10000)`,
`reuse_interval [0]`, `reuse_anchor [released] (acquired|released)`, `reuse_scope [endpoint_group] (endpoint_group|site)`,
`quota [] ({limit, window})`, `sticky {enabled [false], ttl [10m]}`, `warmup {duration [0], quota_factor [1]}`,
`probe {weight_factor [0.1], max_leases [2]}`, `proxy {mode [none], kinds [], tags [], providers [], regions [],
region_match [false], rebind_tolerance [5m], max_rebinds_per_day [3]}`.

Signal: `trust_outcome_hint [false]`, `rules[] {name, when, outcome, blame}`; `when` keys: `http_status` (list or
`{gte,gt,lte,lt}`; `0` means "no status"), `business_code` (list, numbers normalized to strings), `error_kind` (list),
`markers` (any-of list), `uri` (`{prefix}` or `{regex}`, RE2), `method` (list), `latency_ms` / `response_bytes` (range).
First match wins; no match → `unknown` (or the hint when trusted and valid).
Outcomes: `success empty rate_limited captcha auth_invalid forbidden banned proxy_error network_error target_error client_error unknown`.

Action: `mode [enforce] (enforce|shadow)`, `rules[] {name, when {outcome (scalar or list), count {gte, within}},
action (cooldown|expire|quarantine|ban), scope (identity_endpoint|identity_site|identity|account|proxy_site|proxy),
base, multiplier [1], max [24h], max_exponent [10], duration (ban/quarantine; "permanent" allowed for ban),
failure_reset_after [1h]}`, `escalation[] {when {bans {gte, within}}, duration}`,
`health {alpha [0.1], baseline [70], tau [6h], observations {}, endpoint_low_score [15], endpoint_low_min_samples [10],
endpoint_low_cooldown [6h], quarantine_score [20], quarantine_min_samples [10], quarantine_duration [24h]}`,
`ban_expiry_state [pending] (pending|active)`, `cross_attribution {enabled [true], window [10m], proxy_distinct_identities [3], identity_distinct_proxies [3]}`.
Valid scope/action pairs: cooldown → identity_endpoint, identity_site (identity ≡ identity_site), account, proxy_site, proxy;
ban → identity, account, proxy; expire → identity; quarantine → identity, proxy.

Breaker: `enabled [true]`, `window [60s]`, `buckets [12]`, `min_requests [50]`, `trip {risk_ratio_gte [0.4],
distinct_captcha_identities_gte [10], success_ratio_lte [0.2]}` (0 disables a condition), `open_duration [2m]`,
`max_open_duration [1h]`, `reset_open_count_after [30m]`, `half_open {probe_leases_per_10s [5], close_min_samples [5],
close_success_ratio_gte [0.8]}`, `revert_recent_cooldowns [endpoint]`.

Built-in defaults (used when nothing is bound): rotation defaults above; signal rules = the example in the design doc
§7.3 without the business-code rule (plus `http_status [401] → auth_invalid`, `[403] → forbidden`); action rules =
design doc §8.4 example rules; breaker = defaults above. Every new namespace gets these four as published policies named
`default-rotation`, `default-signal`, `default-action`, `default-breaker` bound at namespace level.

---

## 8. Identity types and delivery (`internal/identity`)

Spec: `fields{name: {type: string|number|bool|cookie_map|json|secret_ref, required, sensitive, description}}`,
`unique_by [field paths]` (default: all required fields), `activation (probe|immediate) [probe]`,
`deliver {cookies, cookie_header, headers, query, json, values}`.
Templates: string values; a value that is exactly `{{ path }}` yields the typed value; otherwise placeholders are
interpolated as strings; no expressions. `cookie_map` into `cookie_header` renders `k1=v1; k2=v2` (keys sorted for
determinism). Missing optional values render as empty string / omitted map entry / JSON null. `secret_ref` values are
secret paths resolved at render time (namespace-relative: canonical vault paths without empty, `.` or `..` segments;
rendering refuses any other stored value). Because a reference is resolved into every lease credential without further
checks, writing a payload (import, payload update) requires, for every referenced path that the same field of the
identity's stored payload does not already reference (moving a reference to another field is a new reference), read access to that secret — `secret:read` with a scope matching `<namespace>/<path>` for
API tokens, `secret:reveal` for users — and that the secret exists in the namespace; otherwise the import row is
rejected (or the update fails with permission_denied / invalid_argument). A JSON Schema (draft 2020-12) is generated from the fields
for import validation. `cookie_map` import accepts an object, a `Cookie` header string, or a browser-export array of
`{name, value}` objects.

Import (JSON Lines or CSV, ≤ 50 000 rows / 32 MiB per call): row → `{payload, account, region, tags, labels}`
(JSONL: if no `payload` key the whole object is the payload minus reserved keys `_account _region _tags _labels`;
CSV: header = field names, reserved columns `_account _region _tags` (`;`-separated), cookie_map cells are header strings,
json cells are JSON). Dedupe by `unique_hash = HMAC-SHA256(pepper, type_id || canonical(unique_by values))`.
Existing identity with changed payload → new payload version (keep last 5), state: `active|pending|quarantined|expired`
→ `pending` (probe) or `active` (immediate); `banned|disabled|retired` unchanged; health reset to baseline.
Unchanged payload → `unchanged`. `dry_run` validates only.

Payload cache: LRU (`SPINNERET_PAYLOAD_CACHE_SIZE`, default 200 000) of rendered `Credential` keyed
`identityID:payloadVersion:typeVersion`; entries containing secret refs expire after 60 s.
Console shows payloads with sensitive fields masked (`••••` + last 4) unless the caller has `identity:reveal`.

---

## 9. Vault (`internal/vault`)

- KEKs: `SPINNERET_KEK_FILE` (file lines `id:base64` or a single base64 key ⇒ id `k1`) and/or `SPINNERET_KEKS`
  (`id:base64,id2:base64`); current = `SPINNERET_KEK_CURRENT` (default: last key listed). 32-byte keys only.
- Envelope: random 32-byte DEK; data = AES-256-GCM(DEK, nonce 12, plaintext, AAD); stored `nonce||ciphertext`;
  DEK wrapped = AES-256-GCM(KEK, nonce 12, DEK, AAD="spinneret-dek:"+kekID) stored `nonce||ciphertext`.
- AAD for data = `recordID + "\x00" + field` (e.g. `idt_…\x00payload:v3`, `pxy_…\x00url`, `sec_…\x00v2`).
- DEK cache: LRU 100 000 entries, TTL 10 min keyed by sha256(wrapped DEK).
- Rewrap job: re-wrap every DEK whose `kek_id != current` across `identity_payloads`, `secret_versions`, `proxies`,
  `notification_channels`, `system_keys`; progress exposed via `SecretAdminService/GetKEKStatus`.
- Secrets: path `[a-z0-9][a-z0-9_.-/]*` (≤ 256) relative to namespace; versions; `expires_at` (alert 7 days before);
  every read writes audit (`secret.read`); console reveal needs `secret:reveal` and the UI asks for confirmation.
- Config secret references `${secret:<path>}` or `${secret:<path>#<version>}`.

---

## 10. API conventions (`proto/spinneret/v1`, `internal/api`)

- Package `spinneret.v1`, `option go_package = "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1;spinneretv1"`.
- JSON codec registered on every handler: protojson `UseProtoNames: true, EmitUnpopulated: true`, unmarshal
  `DiscardUnknown: true`. Durations in node APIs are `int32` milliseconds (`*_ms`); admin APIs use duration strings
  (`"30m"`, `"permanent"`). Counts are `int32`. Timestamps are `google.protobuf.Timestamp`.
- Enum-like fields are `string` validated with protovalidate `string.in`.
- Errors: `apperr.Error{Code, Reason, Message, RetryAfterMs}` → Connect error whose metadata sets
  `Spinneret-Reason` and `Spinneret-Retry-After-Ms` (unary errors put metadata in HTTP headers).
- Reasons: `token_invalid token_expired token_revoked ip_not_allowed session_invalid csrf_missing login_throttled
  scope_missing permission_denied site_unknown client_unknown endpoint_group_unknown uri_invalid invalid_argument
  no_identity_available no_proxy_available circuit_open site_paused rebuilding lease_unknown lease_released lease_expired
  lease_lifetime_exceeded rate_limited not_found already_exists failed_precondition conflict internal`.
- Node header `X-Spinneret-Node` (≤ 128 chars, sanitized). Console tenant header `X-Spinneret-Tenant`.
- Pagination: `page_size` (default 50, max 500 unless stated), `page_token` (opaque base64 cursor), response
  `next_page_token`, `total` (int32, may be approximate for large tables).
- Every mutating admin RPC writes an audit log entry. The audit writer stores text with NUL characters and invalid
  UTF-8 replaced by U+FFFD (also inside `details`); a batch the database rejects is split until the rejected entries
  are isolated, and connection failures are retried, so one bad entry never loses the others.

Services (see proto files for full messages):
`LeaseService{Acquire, AcquireBatch, Renew, Release}`, `ReportService{Report}`,
`ConfigService{GetConfig, BatchGetConfig, WatchConfig}`, `SecretService{GetSecret}`,
`AuthService{Login, Logout, GetMe, ChangePassword}`,
`TenantAdminService{ListTenants, CreateTenant, UpdateTenant, DeleteTenant, ListNamespaces, CreateNamespace, UpdateNamespace, DeleteNamespace}`,
`AccessAdminService{ListTokens, CreateToken, RevokeToken, ListUsers, CreateUser, UpdateUser, ResetPassword, ListRoleBindings, CreateRoleBinding, DeleteRoleBinding, ListAuditLogs}`,
`SiteAdminService{ListSites, GetSite, CreateSite, UpdateSite, DeleteSite, ListEndpointGroups, CreateEndpointGroup, UpdateEndpointGroup, DeleteEndpointGroup, ListURIRules, ReplaceURIRules, TestURI}`,
`IdentityAdminService{ListIdentityTypes, GetIdentityType, CreateIdentityType, UpdateIdentityType, DeleteIdentityType, PreviewDelivery, ListIdentities, GetIdentity, ImportIdentities, UpdateIdentityPayload, UpdateIdentity, OperateIdentities, BulkOperateIdentities, RevertActions, ListStateEvents, GetIdentityHotState, ListAccounts, UpsertAccount, OperateAccount}`,
`ProxyAdminService{ListProxies, GetProxy, ImportProxies, UpdateProxy, OperateProxies, DeleteProxies, CheckProxy, GetProviderStats}`,
`PolicyAdminService{ListPolicies, GetPolicy, CreatePolicy, SaveDraft, PublishPolicy, RollbackPolicy, DeletePolicy, ListPolicyVersions, DiffPolicyVersions, ListBindings, SetBinding, DeleteBinding, ResolvePolicies, DebugReport, ValidatePolicy}`,
`BreakerAdminService{ListBreakers, GetBreaker, OpenBreaker, CloseBreaker, ListBreakerEvents, SetSitePaused}`,
`ConfigAdminService{ListConfigItems, GetConfigItem, CreateConfigItem, SaveConfigDraft, PublishConfig, RollbackConfig, DeleteConfigItem, ListConfigVersions, DiffConfigVersions}`,
`SecretAdminService{ListSecrets, GetSecret, CreateSecret, UpdateSecret, DeleteSecret, RevealSecret, ListSecretVersions, ListSecretAccessLogs, GetKEKStatus, StartKEKRewrap}`,
`NotificationAdminService{ListChannels, CreateChannel, UpdateChannel, DeleteChannel, TestChannel, ListAlertEvents}`,
`DashboardService{GetOverview, GetTimeSeries, GetHeatmap, ListRiskEvents, QueryRequestEvents, GetNodeStats}`.

Non-Connect HTTP endpoints: `GET /healthz`, `GET /readyz`, `GET /metrics`, `GET /api/v1/events/stream?tenant=<id>&namespace=<name>` (SSE,
session auth, events filtered by permissions), `GET /*` embedded console with SPA fallback.

---

## 11. Event bus (`internal/events`)
Admin requests are read-your-writes across instances: `catalog.Invalidate` bumps the namespace's change
mark (`HINCRBY P:catv <nsId>`) after the commit, every catalog load records the mark it read before querying
PostgreSQL, and a Connect interceptor on the console/admin services calls `catalog.Store.Sync` first, which
reloads every namespace whose shared mark is ahead of the loaded one (one `HGETALL` per admin request, a
reload only when an instance is behind). Node-facing services do not sync: their hot paths tolerate the
Pub/Sub propagation delay. Marks of deleted namespaces are pruned by the periodic full reload.

Redis Pub/Sub channels `P:ch:<name>`: `catalog` (namespace snapshot invalidation, payload `{"ns":"…"}`),
`tokens` (`{"token_id":"…"}`), `config` (`{"ns":"…","group":"…","key":"…","version":N}`),
`runtime` (`{"ns":"…","kind":"breakers|site_switches","version":N}`), `ns:<nsId>` (console events: `breaker.transition`,
`identity.state`, `proxy.state`, `alert`, `config.published`, `policy.published`). Instances apply their own events
locally as well (publish is best effort; catalog also reloads every 60 s as a safety net).

---

## 12. Configuration (`internal/appconfig`)
| Variable | Default |
| --- | --- |
| `SPINNERET_HTTP_ADDR` | `:8080` |
| `SPINNERET_ROLE` | `all` (`api`, `worker`) |
| `SPINNERET_INSTANCE_ID` | hostname + 6 random hex |
| `SPINNERET_DATABASE_URL` | required |
| `SPINNERET_DATABASE_MAX_CONNS` | `32` |
| `SPINNERET_REDIS_URL` | required (`redis://`, `rediss://`; comma-separated `SPINNERET_REDIS_ADDRS` for cluster) |
| `SPINNERET_REDIS_PREFIX` | `sp` |
| `SPINNERET_CLICKHOUSE_URL` | empty = disabled |
| `SPINNERET_CLICKHOUSE_TTL_DAYS` | `90` |
| `SPINNERET_KEK_FILE`, `SPINNERET_KEKS`, `SPINNERET_KEK_CURRENT` | one of file/keys required |
| `SPINNERET_REPORT_SHARDS` | `16` |
| `SPINNERET_REPORT_DEDUP_TTL` | `1h` |
| `SPINNERET_LATE_REPORT_WINDOW` | `10m` |
| `SPINNERET_STREAM_MAXLEN` | `1000000` |
| `SPINNERET_PAYLOAD_CACHE`, `SPINNERET_PAYLOAD_CACHE_SIZE` | `true`, `200000` |
| `SPINNERET_DEK_CACHE_SIZE`, `SPINNERET_DEK_CACHE_TTL` | `100000`, `10m` |
| `SPINNERET_TOKEN_CACHE_TTL` | `30s` |
| `SPINNERET_SESSION_TTL` | `12h` |
| `SPINNERET_COOKIE_SECURE` | `auto` |
| `SPINNERET_TLS_CERT_FILE`, `SPINNERET_TLS_KEY_FILE` | empty |
| `SPINNERET_TRUSTED_PROXIES` | empty (CIDRs) |
| `SPINNERET_METRICS_ADDR` | empty (serve on main listener) |
| `SPINNERET_PPROF_ADDR` | empty (net/http/pprof off; own listener, never the API or metrics address) |
| `SPINNERET_LOG_LEVEL`, `SPINNERET_LOG_FORMAT` | `info`, `json` |
| `SPINNERET_PROXY_CHECK_URL` | `http://example.com/` |
| `SPINNERET_PROXY_CHECK_INTERVAL`, `SPINNERET_PROXY_CHECK_TIMEOUT` | `60s`, `10s` |
| `SPINNERET_PROXY_EXIT_IP_URL`, `SPINNERET_GEOIP_DB` | empty |
| `SPINNERET_RETENTION_RISK_EVENTS` / `_MINUTE_STATS` / `_HOUR_STATS` / `_STATE_EVENTS` / `_AUDIT` | `720h` / `720h` / `4320h` / `8760h` / `8760h` |
| `SPINNERET_RECORD_COOLDOWN_EVENTS` | `true` |
| `SPINNERET_MAX_WATCHERS` | `20000` |
| `SPINNERET_UI_ENABLED` | `true` |
| `SPINNERET_ALLOWED_ORIGINS` | empty (CORS for local UI dev) |
| `SPINNERET_SHUTDOWN_TIMEOUT` | `30s` |
| `SPINNERET_ADMIN_MAX_REQUEST_BYTES` | `67108864` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty = tracing disabled |

---

## 13. Observability (`internal/observability`)
All metrics are defined in one `Metrics` struct (namespace `spinneret`):
`acquire_total{site,group,result}`, `acquire_duration_seconds{site}` (buckets 0.0005…0.1),
`report_ingest_total{result}` (accepted|duplicated|rejected), `report_total{site,group,outcome}`,
`report_lag_seconds` (histogram), `report_process_duration_seconds`, `identities{site,type,state}`,
`identities_available{site,group}`, `actions_total{site,action,scope,mode}`, `breaker_state{site,group}` (0 closed,1 half,2 open),
`breaker_transitions_total{site,group,to}`, `proxies{site,state}`, `stream_pending{shard}`, `stream_owned_shards`,
`config_watchers`, `lease_reaped_total{site,kind}` (expired|abandoned), `http_requests_total{procedure,code}`,
`http_request_duration_seconds{procedure}`, `notify_deliveries_total{kind,result}`, `db_write_batches_total{writer,result}`.
Label values for site/group are names; unknown/empty → `_`.

---

## 14. Testing conventions
- Unit tests next to code. Integration tests use `testutil.Postgres(t)` (fresh database per test package cloned from a
  migrated template), `testutil.Redis(t)` (unique key prefix per test, `FLUSH` avoided), `testutil.ClickHouse(t)`.
  Connection strings come from `SPINNERET_TEST_DATABASE_URL`, `SPINNERET_TEST_REDIS_URL`, `SPINNERET_TEST_CLICKHOUSE_URL`
  (the test compose stack exports them); when unset, testcontainers starts `postgres:17-alpine`, `valkey/valkey:8-alpine`,
  `clickhouse/clickhouse-server:25.8-alpine` once per package.
- `go test ./...` must pass with the test stack running; `-short` skips integration tests.
- Lua scripts are tested against real Redis/Valkey (never miniredis).
- E2E (`test/e2e`, build tag `e2e`) runs against `deploy/compose` with the test overlay.
- Load (`test/load`) uses k6 in the compose `loadtest` profile.
- Coverage target ≥ 80% for domain packages (policy, identity, site, vault, authz, apperr, idgen, scheduler, worker, action, breaker).

---

## 15. Deviations from the design doc
- Report late-window: the lease record is kept for `late_window` after lease end (longer while timely accepted reports may
  still be queued, at most `late_window + dedup TTL`, see §5.3 `kx`); reports later than that are rejected with `lease_unknown` (still counted in
  `node_stats_minutely.rejected`) because the lease → identity mapping is gone.
- Breaker notification loop (`breaker.Run`) runs on every role: acquire on `api` instances reports lazy open → half_open
  transitions through `scheduler.Config.OnBreakerHalfOpen`.
- Quota counts: the first request of a lease is counted at acquire; further reports on the same lease are counted by the worker.
- Probe `max_leases` is the maximum concurrent leases of one `pending` identity.
- `revert_recent_cooldowns` is an enum (see §0).
- Policies have drafts and publish permissions (mirrors the config center) so operators cannot change live policy alone.
- `cooldown` actions also write `state_events` (configurable) so rule rollbacks and timelines include them.
- Extra console pages beyond the 12 listed: tenants & namespaces, notifications, request explorer (ClickHouse).
