# FAQ and glossary

**The questions people ask before they deploy Spinneret and while they run it, each answered in a
few lines with a link to the page that has the detail — followed by every term this documentation
uses, in English and Chinese.**

[中文](../zh/21-faq.md)

---

## Contents

- [Before you deploy](#before-you-deploy)
- [About the model](#about-the-model)
- [About scale and cost](#about-scale-and-cost)
- [About security](#about-security)
- [About operating it](#about-operating-it)
- [About the project](#about-the-project)
- [Glossary](#glossary)

---

## Before you deploy

### What does Spinneret actually do?

It is a control plane for a fleet of crawler or API nodes. It stores the identities (cookies,
device parameters, account credentials), the proxies, the configuration and the secrets those
nodes need; it hands out exactly one usable identity plus a proxy per request under a lease; and
it turns the results the nodes report back into cooldowns, bans, health scores and circuit
breaking — server-side, within seconds, without a node redeploy.

A node needs a server URL and an API token. Everything else arrives at request time. See
[Concepts](./04-concepts.md).

### What does Spinneret *not* do?

It never contacts a target. It contains no signing algorithms, no login flows, no captcha
solving, no browser automation and no HTML parsing. It does not fetch anything, schedule your
URLs, or decide what your crawler should do next. Your node still makes every request; Spinneret
manages the state around those requests.

It also does not do distributed global rate limiting, external validator/refresher webhooks,
proxy-provider adapters, OIDC, TOTP or mTLS in v0.1. Those are listed as candidates for a later
release in `CHANGELOG.md`.

### Is it legal or appropriate to use?

Spinneret is infrastructure. It is site-agnostic: it has no knowledge of any particular website,
ships no integration with one, and cannot make a request on its own. What it holds is credential
and scheduling state that you supply, and what it sends is whatever your nodes ask for.

That means the responsibility is entirely yours: the operator is accountable for which targets
their nodes contact, what terms of service and laws apply to those requests, which credentials
they load into the vault and whether they had the right to. Deploying Spinneret does not make an
unlawful crawl lawful, and the software takes no position on what you crawl. See
[Security](./19-security.md) for what the software protects and what stays your responsibility.

### Do I need ClickHouse?

No. ClickHouse is optional. It stores the raw per-request events behind the **Requests** page
(the request explorer). Leave `SPINNERET_CLICKHOUSE_URL` empty and the server logs
`clickhouse disabled: raw report events are not stored` at startup and runs normally — scheduling,
reporting, cooldowns, bans, breakers, the overview dashboards, the cooldown heatmap and risk
events all read PostgreSQL and keep working. Only the request explorer becomes unavailable.

The shipped Compose stack does include ClickHouse and the `spinneret` service waits for it to
become healthy, so dropping it means editing `deploy/compose/docker-compose.yml`. See
[Installation and deployment](./02-installation.md) and
[Configuration reference](./03-configuration.md).

### What *do* I need?

PostgreSQL (the source of truth) and Redis or Valkey (the hot state). Those two are mandatory.
Plus a key-encryption key — `SPINNERET_KEK_FILE` or an inline `SPINNERET_KEKS`; the server refuses
to start with neither. See [Installation and deployment](./02-installation.md).

### Can I run it without Docker?

Yes. Spinneret is two static Go binaries, one of which has the console compiled into it:

```bash
make web          # builds the console into web/dist — do this first
make build        # builds bin/spinneret-server and bin/spnr
```

`make build` on its own does *not* build the console: without a prior `make web` the binary embeds
only a placeholder and serves no UI.

`bin/spinneret-server` takes all of its configuration from `SPINNERET_*` environment variables,
serves the embedded console and every API on one port (`SPINNERET_HTTP_ADDR`, default `:8080`), and
needs no CGO, no sidecar and no external web server. `bin/spnr` runs the migrations, creates the
first administrator and issues tokens; it is a plain CLI and embeds no console. You still have to
provide PostgreSQL and Redis/Valkey yourself. [Installation and deployment](./02-installation.md)
has the systemd-style layout; [CLI reference](./15-cli.md) has both binaries' flags, and
[Contributing](./20-contributing.md) has the console build in detail.

### Do I have to rewrite my crawler?

No. The integration is two calls around the request you already make: `Acquire` before it,
`Report` after it. In the Python and Go SDKs that is a context manager or a `defer`, and the SDK
merges the credential and proxy into your existing HTTP client. See
[SDKs and examples](./14-sdks.md).

### How do I migrate from a home-grown scheduler?

In this order, and you can stop after any step:

1. **Import the state you already have.** `ImportIdentities` takes your cookies and device
   parameters in bulk, `ImportProxies` takes your proxy list. Both are also in the console. See
   [Identities and accounts](./06-identities.md) and [Proxies](./07-proxies.md).
2. **Run one node against Spinneret** while the rest keep using the old scheduler. Identities do
   not have to be exclusive to one system for the trial; set `max_concurrent_leases` high enough
   that Spinneret does not fight your old code for the same cookie.
3. **Put the policies in shadow mode.** `mode: shadow` on the action policy records every action
   it would have taken and applies none of them, so you can compare its judgement against yours
   for a day before it can ban anything. See [Policies](./08-policies.md).
4. **Move the fleet**, then switch the policy to `enforce`.
5. **Move configuration last.** Once nodes read their settings from the configuration center you
   can stop rebuilding images to change a parameter. See
   [Configuration center](./09-config-center.md).

### Can I try it without real credentials?

Yes. `./scripts/example-quickstart.sh` seeds a site, twenty synthetic identities, two mock
proxies, published policies and a node token, then runs a complete example crawler against a
built-in mock target you can make misbehave on demand. Nothing leaves the machine. See
[Quick start](./01-quickstart.md) and [SDKs and examples](./14-sdks.md).

---

## About the model

### What is a namespace versus a tenant, and which one is my "team"?

A **tenant** is the hard isolation boundary — a company, a customer, an organization. Nothing
crosses it. A **namespace** is a partition inside one tenant: an environment (`prod`, `staging`)
or a business line.

Your team is almost always a **namespace**. Use a second tenant only when the other side must
never see anything of yours — a different customer, a different legal entity. Sites, proxies,
policies, config items, secrets and API tokens all belong to a namespace; a token's namespace is
fixed at creation and can never be changed. See
[Tenants, users and tokens](./11-access-control.md).

### What is the difference between a site, a client and an endpoint group?

A **site** is one target system. A **client** is the access surface you use against it (`web`,
`mobile`, `partner`) — different surfaces need different cookies and different headers. An
**endpoint group** is a class of URL inside a site+client that shares limits and policies, for
example `search` versus `detail`. The node sends the URI and Spinneret maps it to an endpoint
group with the site's URI rules. See [Concepts](./04-concepts.md).

### Who decides that an identity is bad — my node or the server?

The server. Nodes report **facts** only: the HTTP status, an optional business code, a transport
error kind, latency, response size and any markers the node recognized (`captcha_page`,
`empty_list`, …). The signal policy turns those facts into one of twelve outcomes plus a blame,
and the action policy turns the outcome into a cooldown, expiry, quarantine or ban. Changing that
judgement is a policy edit, not a node deploy. A node may send an `outcome_hint`, but it is used
only if the signal policy explicitly trusts hints. See [Policies](./08-policies.md).

### Does Spinneret store the content my nodes fetch?

No. A report carries `report_id`, `lease_id`, `uri`, `method`, `http_status`, `business_code`,
`error_kind`, up to 32 short `markers`, an optional `outcome_hint`, `latency_ms`,
`response_bytes`, `started_at`, `finished_at` and a `release` flag — the complete wire message is in
the [Node API reference](./13-node-api.md). There is no field for a response body, a request body or
a response header, and the server has nowhere to put one. The request explorer shows this metadata
and nothing more.

The one thing you control here is `uri`: it is stored as sent, including the query string, so
strip anything sensitive from it before reporting if your query strings carry secrets.

### What is the difference between an identity and an account?

An **identity** is one usable set of credentials — a cookie jar plus device parameters, or an API
key. An **account** is the login several identities may belong to. Banning an account takes every
identity under it out of rotation at once; that is what the `account` action scope is for. See
[Identities and accounts](./06-identities.md).

### Do I have to use proxies?

No. `proxy.mode: none` in the rotation policy is the default and Spinneret then returns no proxy
with the lease. The proxy pool, its health checks and the per-site proxy cooldowns only come into
play when you set a mode. See [Proxies](./07-proxies.md).

### Can two nodes get the same identity at the same time?

Only if you allow it. `rotation.max_concurrent_leases` defaults to `1`, which makes an identity
exclusive within the endpoint group while a lease is held. Raise it to allow parallel use, add a
`reuse_interval` to enforce a gap between uses, and add `quota` windows to cap requests per
identity per period. See [Policies](./08-policies.md).

### What happens if a node dies holding a lease?

Nothing on the node side. Every instance runs a lease reaper once per second that ends overdue
leases, rescores the identity and restores the availability the lease was holding back. A lease the
node did report on ends as `expired`; one that ended without any report ever arriving ends as
**abandoned**. Abandoned leases are not request-explorer rows — the explorer only holds reports, and
there is none. The count is on the **Overview** page's per-node table and in
`spinneret_lease_reaped_total{kind="abandoned"}`. You lose at most one lease TTL of that identity's
capacity. See [Concepts](./04-concepts.md) and
[Observability and alerting](./12-observability.md).

### Why is my identity `active` but never handed out?

Because the lifecycle state and the availability condition are two different things. An `active`
identity can still be unusable right now: a cooldown at endpoint-group or site scope, already at
`max_concurrent_leases`, a spent quota window, a reuse interval that has not elapsed, its account
cooled down or banned, or its bound proxy unavailable. The cooldown heatmap exists to make that
second layer visible. See [Troubleshooting](./18-troubleshooting.md).

---

## About scale and cost

The figures below were measured against the Compose stack with the load harness in `test/load/`,
on one site with 100,000 identities across 50 endpoint groups, in a single
laptop-sized Docker VM that also runs the load generator. Read them as the shape of the curve, not
as a guarantee on your hardware. [Performance and tuning](./17-performance.md) has the method, the
full tables and the caveats.

### How many requests per second can one deployment handle?

Per server instance, against one Redis:

| Workload | Sustained | Latency at that rate |
| --- | ---: | --- |
| Full `Acquire` → `Report` cycle, exclusive leases (`max_concurrent_leases: 1`) | 4,500 cycles/s | acquire p99 1.86 ms, report lag p99 9.3 ms |
| Full cycle, `max_concurrent_leases: 4` | 5,500 cycles/s | acquire p99 4.49 ms, report lag p99 62 ms |
| `Acquire` alone | 4,993/s | p99 4.32 ms (peak 7,792/s with latency unbounded) |
| `Report` ingest, accepted | 44,437 reports/s | zero rejections |
| `Report` applied to the hot state inside the 200 ms target | ~19,761 reports/s | lag p99 30.7 ms |

### How many identities and endpoint groups?

100,000 identities across 50 endpoint groups on one site is the measured dataset and it is not
near a limit — the hot state cost per acquire is set by `rotation.candidate_sample` (default 32),
not by how many identities exist. Redis memory scales with **traffic**, not with the dataset:
report dedup markers and ended lease hashes live for `SPINNERET_REPORT_DEDUP_TTL` (default `1h`)
and the report streams hold the undrained backlog up to `SPINNERET_STREAM_MAXLEN` (default
1,000,000). Size those before running at a high report rate.

### How many nodes?

The binding constraint is not the node count but the number of parked `WatchConfig` long polls,
because every node that watches configuration holds one open request. One instance held ~10,000
concurrent watchers at 0.05 CPU cores and zero failures; `SPINNERET_MAX_WATCHERS` defaults to
20,000 per instance and is the hard cap. Acquire traffic is the other constraint, and that is the
table above.

### Does adding a second replica double the throughput?

No — and this is the single most important operational fact in the performance page. Two replicas
behind the load balancer sustained **3,000 cycles/s in aggregate**, which is *less* than one
replica reaches alone (4,500/s). Each instance drives its own unbounded concurrency against the
shared Redis, a failing acquire costs roughly five times a succeeding one, and past the knee the
system suffers congestion collapse rather than degrading gracefully.

Practical advice: run replicas for **availability**, not for throughput; keep the offered load
under about 80 % of your measured knee; alert on
`spinneret_acquire_total{result="exhausted"}` and `spinneret_report_lag_seconds`. Splitting
`SPINNERET_ROLE` into `api` and `worker` instances lets you scale report processing without adding
Acquire concurrency. See [Performance and tuning](./17-performance.md) and
[Operations runbook](./16-operations.md).

### How much hardware?

The v0.1 sizing assumption — 4 vCPU / 8 GB per service instance and a single Redis instance — held
up: at the sustained rates one server instance used 1.4–1.6 cores and Valkey 1.6–1.9 cores (with
`io-threads 4`). PostgreSQL and ClickHouse sizing depends on your retention settings, not on your
request rate. [Operations runbook](./16-operations.md) has the capacity-planning section.

### What does it cost to run?

Three datastores and one stateless binary. There is no metering, no phone-home, no licence server
and no external service the software calls. The cost is whatever PostgreSQL, Redis/Valkey and
(optionally) ClickHouse cost you, plus the instances.

---

## About security

### Can a node read secrets?

Only the ones you let it read. A node token needs the `secret:read:<glob>` scope, the glob
argument is mandatory, and it is matched against `<namespace name>/<path>`. A token in namespace
`prod` with `secret:read:prod/signing/*` can read `prod/signing/api_key` and nothing else.
Grant the narrowest glob that works. See [Secret vault](./10-secrets.md) and the `GetSecret` call in
the [Node API reference](./13-node-api.md).

### Can a node reach another tenant's data?

No. A token's tenant and namespace are fixed at creation and cannot be changed; every handler
re-checks them. Giving a node access to two namespaces means issuing two tokens. See
[Tenants, users and tokens](./11-access-control.md).

### Where are cookies, passwords and proxy credentials stored?

In PostgreSQL, as AES-256-GCM ciphertext under envelope encryption: a key-encryption key (KEK)
that never leaves the host wraps per-record data-encryption keys (DEKs), and those encrypt the
identity payloads, proxy URLs, secret values, notification channel configurations and the server's
own internal keys (`system_keys`). Someone who steals a database dump without the KEK recovers none
of it. See [Secret vault](./10-secrets.md) and [Security](./19-security.md) for the full table of
encrypted columns.

### What happens if I lose the KEK?

Every encrypted value is unrecoverable — identity payloads, proxy credentials, secrets. There is
no escrow, no recovery key and no support path. Back up the KEK file **separately from the
database dumps**, before you put anything real into the system. See
[Secret vault](./10-secrets.md) and [Security](./19-security.md).

### Is the console safe to expose to the internet?

Only behind TLS, and only after you have read the hardening checklist. Spinneret speaks plain
HTTP unless you give it a certificate (`SPINNERET_TLS_CERT_FILE` / `SPINNERET_TLS_KEY_FILE`) or
put it behind a TLS terminator. `/metrics` is unauthenticated and exposes internal state — bind it
to its own address with `SPINNERET_METRICS_ADDR` and keep it off the public listener. The pprof
listener (`SPINNERET_PPROF_ADDR`) is off by default and must stay unreachable from outside. See
[Security](./19-security.md).

### Who can see a plaintext credential?

A node holding a token with the right scope, a console user holding `identity:reveal` or
`secret:reveal`, anyone who can run `spnr` against the database (that bypasses the API and its
permission checks entirely), and anyone with root on the host.

Every *reveal* operation — `RevealSecret`, `GetIdentity` with `reveal` — and every `GetSecret` is
written to the audit log. Two paths that hand out real credentials are **not** audited:
`Acquire`/`AcquireBatch`, deliberately, because it is the hot path (lease state and the request
explorer are the record instead), and `spnr` against the database. See
[Security](./19-security.md).

### How do I report a vulnerability?

Not in a public issue. See
[Reporting a vulnerability](./19-security.md#reporting-a-vulnerability).

---

## About operating it

### What happens when Spinneret is down? Do my nodes stop?

Partly, and how much depends on what they were doing:

| Node activity | While the control plane is unreachable |
| --- | --- |
| Holding a valid lease | Keeps working until the lease TTL expires; the request itself never touches Spinneret |
| `Acquire` | Fails. There is no client-side fallback pool — a node with no lease cannot start a request |
| `Report` | Buffered. Both SDKs queue reports (default 10,000, oldest dropped beyond that) and retry with jittered exponential backoff |
| `GetConfig` / `WatchConfig` | Both SDKs can fall back to a local snapshot of the last configuration they fetched and keep running with it, flagging `from_snapshot`. The Python SDK snapshots by default; the Go SDK only when `SnapshotDir` is set |
| `GetSecret` | Fails; there is no snapshot of a secret |

Snapshots are plain JSON files on disk. Items that reference a secret are never written at all,
unless the Python SDK is started with `cache_secrets=True`, which encrypts those items with a key
derived from the API token — and the Go SDK ignores any encrypted snapshot it finds. See
[SDKs and examples](./14-sdks.md). Every RPC in the table is specified in the
[Node API reference](./13-node-api.md).

Two things to know about the recovery. Reports are idempotent by `report_id` for
`SPINNERET_REPORT_DEDUP_TTL` (default `1h`), so a buffered retry is safe. But a report that
arrives more than `SPINNERET_LATE_REPORT_WINDOW` (default `10m`) after its lease ended is **late**:
it still counts in the statistics and plans no actions. A long outage therefore costs you risk
signal, not correctness. See [SDKs and examples](./14-sdks.md).

### Is there a single point of failure?

The server is stateless and you can run as many replicas as you like. The single points of
failure are the things underneath it:

| Component | If it is lost |
| --- | --- |
| Redis / Valkey | Acquire stops. The hot state is derived, so rebuild it from PostgreSQL with `spnr rebuild` — nothing is permanently lost |
| PostgreSQL | Everything stops. This is the source of truth; back it up and test the restore |
| The KEK file | Every encrypted value is unrecoverable. Back it up separately from the database |
| ClickHouse | Only the request explorer stops; everything else keeps running |

Redis Cluster is supported — set `SPINNERET_REDIS_ADDRS` to the seed nodes and the client connects
in cluster mode — but every benchmark in this documentation was taken on a single instance, and
the current throughput ceiling is contention, not Redis CPU. See
[Operations runbook](./16-operations.md).

### How do I run it in more than one region?

There is no cross-region replication in Spinneret itself. Two workable shapes:

- **One control plane, nodes in many regions.** Simplest. Nodes pay one WAN round trip per
  `Acquire` and one per report batch; reports are batched and asynchronous, so the latency that
  matters is the acquire. Region-aware proxy selection is a policy concern
  (`proxy.mode: region_match`), not a deployment one.
- **One deployment per region**, each with its own PostgreSQL, Redis and namespace. Identities are
  not shared, which is usually what you want when the identities themselves are region-bound.

Choose the second when the WAN round trip on the acquire path is unacceptable or when a region
must survive the loss of another. See [Operations runbook](./16-operations.md).

### How do I upgrade safely?

1. Back up PostgreSQL and confirm the KEK backup exists.
2. Read `CHANGELOG.md` for the release — migrations and anything fixed at deploy time are called
   out there.
3. Run the migrations: `spnr migrate up` (the Compose stack runs this as its `migrate` service
   before the server starts).
4. Roll the replicas one at a time. A server whose database is *ahead* of the binary — the normal
   state mid-roll — starts with a warning instead of refusing, so a rolling upgrade works; a
   database *behind* the binary is migrated or rejected depending on how you start it.
5. Watch `spinneret_report_lag_seconds` and the acquire error counters while the roll finishes.

**Warning.** `SPINNERET_REPORT_SHARDS` (default `16`) cannot be changed without stranding
in-flight leases. Pick it before going live. See [Operations runbook](./16-operations.md) and
[CLI reference](./15-cli.md).

### How do I back it up?

PostgreSQL with your normal dump or snapshot mechanism, and the KEK file separately — a dump
without the KEK is unreadable, and a KEK without a dump is useless. Redis needs no backup:
`spnr rebuild` reconstructs the hot state from PostgreSQL. ClickHouse holds raw request events
whose retention is `SPINNERET_CLICKHOUSE_TTL_DAYS` (default `90`) and is usually not worth backing
up. [Operations runbook](./16-operations.md) has the restore drill.

### How do I test a policy change without breaking production?

Three tools, in increasing order of commitment: the **rule debugger** replays a synthetic report
against the resolved policies and shows you which rule matched; **shadow mode** (`mode: shadow`)
records every action the policy would have taken and applies none; and **version rollback** puts
the previous published version back in one operation. See [Policies](./08-policies.md).

### What should I monitor?

At minimum: `spinneret_acquire_total{result="exhausted"}` (the pool is empty),
`spinneret_report_lag_seconds` (the workers are falling behind),
`spinneret_acquire_duration_seconds` (the knee), and the `identity_low_watermark`,
`proxy_low_watermark`, `report_backlog` and `breaker_opened` alert kinds routed to a notification
channel. See [Observability and alerting](./12-observability.md).

### Can I just wipe Redis?

Yes, deliberately. The hot state is derived from PostgreSQL and `spnr rebuild` recreates it. Three
caveats: in-flight leases are lost (the affected requests fail and retry); a rebuild of
*everything* — `spnr rebuild` with no `--site` — makes every API instance report not-ready until it
finishes, so a load balancer takes them out of rotation, while `spnr rebuild --site <name>`
re-materializes one site and the rest of the deployment keeps serving; and a freshly rebuilt site
is briefly more expensive to serve than a warm one, because every endpoint group starts with the
same ready-queue ordering and they collide on the same candidates until traffic decorrelates them.
Rebuild during a quiet period if you can. See [Operations runbook](./16-operations.md) and
[CLI reference](./15-cli.md).

---

## About the project

### Who maintains it?

Spinneret is developed and maintained by **TikHub** — <https://github.com/TikHub>. The repository
is <https://github.com/TikHub/Spinneret> and the Go module path is
`github.com/TikHub/Spinneret`.

### What is the licence?

The **Apache License 2.0**, Copyright 2026 TikHub. The full text is in `LICENSE` in the repository
root; read it there rather than trusting this summary before you redistribute anything or build a
product on top.

### What state is the project in?

v0.1.0 is feature-complete: identity scheduling, proxy distribution,
reporting and signal detection, cooldowns and bans, health and lifecycle, circuit breaking, the
configuration center, the secret vault, auth and tenancy, the console, notifications and
observability are all implemented, with Go end-to-end scenarios, a failover drill, a console test
suite and k6 load scenarios. `CHANGELOG.md` is the authoritative record of what changed and what
is explicitly out of scope.

### How do I get support or report a bug?

Open an issue at <https://github.com/TikHub/Spinneret/issues>. A useful report contains the
version (`spnr version`), how the stack is deployed, the exact error — including the
`Spinneret-Reason` header value if a node call failed — and the relevant server log lines.
[Troubleshooting](./18-troubleshooting.md) has a section on collecting a diagnostic bundle, and
going through that page first will often answer the question outright.

Security issues do **not** go in a public issue: see
[Reporting a vulnerability](./19-security.md#reporting-a-vulnerability).

### Can I contribute?

Yes. [Contributing](./20-contributing.md) covers the development environment, the repository
layout, code generation, the test layers and the quality gates a change has to pass.

---

## Glossary

Alphabetical by the English term. Identifiers keep the spelling they have in code.

| English | 中文 | Definition |
| --- | --- | --- |
| account | 账号 | A login that several identities can belong to. Banning or cooling down an account takes every identity under it out of rotation at once. |
| acquire | 租借 | Taking a lease on one identity for one endpoint group. The verb for what a node does before every request; the RPC is `Acquire`. |
| action | 动作 | A disposition the server applies to an identity, account or proxy after it has classified a report: `cooldown`, `expire`, `quarantine`, `ban` or `activate`. |
| action policy | 动作策略 | The policy that decides which action follows which classification result, at which scope, with what backoff and escalation, and what the health score does. |
| admission control | 准入控制 | Bounding how many requests a server instance works on at once so that overload is refused cheaply instead of collapsing throughput. Not implemented in v0.1; the overload behaviour it would fix is described in the performance page. |
| alert rule | 告警规则 | The condition on a metric or an alert kind that decides when a notification channel is fired. The starter set is in the observability page. |
| API token | API 令牌 | The `spn_…` bearer credential a node authenticates with. It is bound to one tenant and one namespace for life and carries a fixed list of scopes. |
| audit log | 审计日志 | The record of who did what through the API: the actor, the action, the target, the time and the source address. Written for administrative operations and for every plaintext reveal. |
| backpressure | 背压 | The signal that flows back from a saturated stage to the one feeding it, telling it to slow down. Here it shows up as growing report lag and as acquires that are refused. |
| ban | 封禁 | Taking an identity, account or proxy out of rotation for a stated duration or permanently. The harshest of the automatic dispositions. |
| blame | 归因 | Whether a classified result is held against the identity, the proxy, both or neither. Rules that punish an identity only fire when the identity is blamed. |
| breaker policy | 熔断策略 | The policy that configures the per-endpoint-group failure detector: window size, thresholds, how long it stays tripped, and how many trial requests it allows while recovering. |
| candidate sample | 候选采样数 | How many identities the scheduler pulls out of the ready set on one `Acquire` before the rotation strategy picks among them. `rotation.candidate_sample`, default `32`. |
| circuit breaker | 熔断器 | A per-endpoint-group switch that refuses acquires when the recent failure or risk ratio crosses a threshold, so a failing endpoint is not hammered. |
| ClickHouse | ClickHouse | The optional analytical database that stores raw per-request events. Without it every other part of the system works and only the request explorer is unavailable. |
| client | 客户端 | The access surface used against a target, for example `web`, `mobile` or `partner`. Different surfaces normally need different credentials and headers. |
| closed | 关闭 | The normal state of a circuit breaker: acquires pass through. It is what a breaker returns to after enough probe requests in `half-open` succeed. |
| config group | 配置分组 | A named folder of configuration keys inside a namespace, for example `crawler`. Token scopes are granted per group. |
| config item | 配置项 | One key inside a config group, holding JSON, YAML or text, versioned with drafts, publishes and rollbacks. |
| configuration center | 配置中心 | The part of Spinneret that stores versioned configuration for nodes and pushes changes to them, so a parameter change does not need an image rebuild. |
| congestion collapse | 拥塞崩溃 | The failure mode where extra load makes total useful work go *down*: contention makes each operation more expensive, which raises contention further. |
| control plane | 控制平面 | The system that owns the state around requests — which credential, through which proxy, whether the endpoint is usable — as opposed to the machines that make the requests. |
| cooldown | 冷却 | A timestamp until which something is unavailable. Applied at endpoint-group, site, identity, account or proxy scope, and lengthened exponentially as failures repeat. |
| cooldown heatmap | 冷却热力图 | The console page that draws identities against endpoint groups and colours each cell by whether that identity can serve that group right now. |
| credential | 凭据 | The concrete cookies, headers, query parameters and body values a node receives with a lease, rendered from the stored fields by the identity type's delivery template. |
| cross attribution | 交叉归因 | Re-assigning risk between an identity and the proxy it went through when the evidence points at one rather than the other. Configured in the action policy. |
| CSRF header | CSRF 请求头 | The `X-Spinneret-CSRF: 1` header the console must send on unsafe methods. Its absence is what stops another website from acting as a logged-in operator. |
| DEK (data-encryption key) | DEK（数据加密密钥） | A per-record key that encrypts one stored value. It is itself stored encrypted, wrapped by the key-encryption key. |
| delivery template | 投递模板 | The part of an identity type that says how stored fields become a request — which field becomes a cookie, which becomes a header, which becomes a query parameter. |
| draft | 草稿 | An edited but unpublished version of a policy or a config item. Nodes never see a draft; publishing is a separate, separately permissioned step. |
| drain | 排空 | Taking a server instance out of service gracefully: stop accepting new work, finish what is in flight, then exit. |
| endpoint group | 端点组 | A class of URL within one site and client that shares limits and policies, for example `search` or `detail`. The URI a node sends is mapped to one by the site's rules. |
| escalation ladder | 升级阶梯 | An ordered list that makes each repeated temporary ban of the same subject longer than the last. |
| EWMA | 指数加权移动平均 | Exponentially weighted moving average: a running average that weights recent samples more heavily. It is how the health score keeps up with change without overreacting to one bad request. |
| expire | 失效 | Marking an identity as no longer usable until its stored fields are refreshed. Typically the response to credentials the target has rejected as invalid. |
| failure streak | 失败连击 | How many consecutive failures a subject has accumulated. It is the exponent of the exponential cooldown backoff, and it resets after a configurable quiet period. |
| half-open | 半开 | The trial state of a failure detector: it has been tripped, the wait has elapsed, and it now allows a small number of probe requests to find out whether the endpoint recovered. |
| health score | 健康分 | A 0–100 running score per identity, updated from every result with exponential weighting and decayed over time. Low scores cool an identity down, take it out of rotation, or lower its selection weight. |
| hot state | 热状态 | The working set in Redis/Valkey: ready queues, leases, cooldowns, counters and report streams. Entirely derived from PostgreSQL and rebuildable from it. |
| idempotency key | 幂等键 | The client-generated `report_id` on every report. A retry with the same value inside the dedup window is counted as a duplicate and has no further effect. |
| identity | 身份 | One usable set of credentials against a target — a cookie jar with device parameters, or an API key. The unit that is leased, cooled down, scored and banned. |
| identity lifecycle state | 身份生命周期状态 | Where an identity is in its life, independent of whether it is usable right now: `pending`, `active`, `expired`, `banned`, `quarantined`, `disabled` or `retired`. |
| identity type | 身份类型 | The schema of an identity within a site: which fields it stores, which of them are required, and how they are delivered to a node. |
| instance | 实例 | One running `spinneret-server` process. Instances are stateless and interchangeable; the state lives in PostgreSQL and Redis. |
| KEK (key-encryption key) | KEK（密钥加密密钥） | The master key, held in a file on the host, that encrypts every data-encryption key. It never enters the database. Losing it makes every encrypted value unrecoverable. |
| late report | 迟到上报 | A report that arrives more than `SPINNERET_LATE_REPORT_WINDOW` (default `10m`) after its lease ended. It still counts in the statistics but plans no dispositions. |
| lease | 租约 | A time-boxed claim on one identity for one endpoint group. It is what lets the server know how many nodes hold that identity right now and enforce a limit on it. |
| lease lifetime | 租约最长存续 | The ceiling that renewals cannot push a lease past, however often it is renewed. `rotation.max_lease_lifetime`, default `30m`. |
| lease reaper | 租约回收器 | The loop each instance runs once a second to end overdue leases, rescore the identity and give back the availability the lease was holding. It is why a crashed node costs at most one lease TTL. |
| lease TTL | 租约有效期 | How long one `Acquire` or `Renew` grants the lease for. `rotation.lease_ttl`, default `2m`. |
| long poll | 长轮询 | A request the server holds open until something changes or a timeout elapses. It is how a node learns about a configuration change within milliseconds without polling in a loop. |
| low watermark | 低水位 | The threshold below which the number of usable identities or proxies is considered dangerous. Crossing it raises an alert. |
| marker | 标记 | A short label a node attaches to a report for a response feature it recognized — `captcha_page`, `login_redirect`, `empty_list`. Up to 32 per report, and the main input to classification beyond the status code. |
| namespace | 命名空间 | A partition inside one tenant — an environment or a business line. Sites, proxies, policies, config items, secrets and tokens all belong to exactly one. It is the usual unit for a team. |
| node | 节点 | A machine of yours that makes the actual requests: a crawler, a worker, an API client. Spinneret never makes a request itself. |
| notification channel | 通知渠道 | A configured destination for alerts. Five kinds are supported, listed with their configuration fields in [Observability and alerting](./12-observability.md); `webhook` is the generic one and is HMAC-signed. |
| open | 熔断 | The tripped state of a circuit breaker: acquires for that endpoint group are refused outright until the wait elapses and it moves to `half-open`. |
| outcome | 判定结果 | The server's classification of one report. There are exactly twelve: `success`, `empty`, `rate_limited`, `captcha`, `auth_invalid`, `forbidden`, `banned`, `proxy_error`, `network_error`, `target_error`, `client_error`, `unknown`. |
| outcome hint | 判定建议 | A classification a node proposes on its own report. It is ignored unless the signal policy is explicitly configured to trust hints. |
| payload | 载荷 | The encrypted stored fields of an identity — the cookies, tokens and device parameters themselves, as opposed to the rendered form a node receives. |
| payload version | 载荷版本 | The revision counter of an identity's stored fields. Refreshing the fields bumps it, which is how a node can tell that the credential it holds is stale. |
| permission | 权限 | A single right in the console's authorization model, written `resource:verb`, for example `identity:reveal` or `policy:publish`. Roles are sets of these. |
| platform administrator | 平台管理员 | A user flagged `is_platform_admin`. They pass every permission check in every tenant, including key management. No configuration restricts them. |
| policy | 策略 | A named, versioned YAML document that governs behaviour. There are four kinds: rotation, signal, action and breaker. |
| policy binding | 策略绑定 | The attachment of a policy to a scope — namespace, site, client or endpoint group. The most specific binding wins, which is how one base policy covers a fleet with narrow exceptions on top. |
| probe lease | 探针租约 | A lease handed out to test the water: either to a recovering endpoint whose failure detector is half-open, or to a newly imported identity that has not proven itself yet. |
| proxy | 代理 | An egress route stored in the pool with its URL, kind, region, provider and tags. Its credentials are encrypted at rest like any other secret. |
| proxy assignment mode | 代理分配模式 | How a lease gets its proxy: `none` (no proxy), `pool` (any matching one), `bind_identity` (the one this identity is bound to) or `region_match` (one whose region matches the identity's). |
| proxy pool | 代理池 | The set of proxies available to a namespace, with their health-check results, per-site cooldowns and identity bindings. |
| quarantine | 隔离 | Taking an identity — or a proxy — out of rotation for a fixed period, after which it must prove itself again rather than resuming as fully trusted. Usually driven by a collapsed health score. |
| quota | 配额 | A cap on how many requests one identity may make in an endpoint group per time window, evaluated as a sliding window. Several windows can apply at once. |
| rebuild | 重建 | Regenerating the Redis working set from PostgreSQL with `spnr rebuild`. Safe to run at any time and it does not drop live leases; a freshly rebuilt site is briefly more expensive to serve. Leases are lost only if you actually wipe Redis before rebuilding. |
| release | 归还 | Ending a lease so the identity becomes available again. A node normally releases by setting `release: true` on its final report rather than making a separate call. |
| renew | 续约 | Extending a lease that is about to expire, up to the maximum lifetime. For requests that take longer than the lease was granted for. |
| replica | 副本 | One of several interchangeable server instances behind a load balancer. Replicas give you availability; they do not multiply throughput, because they contend for the same Redis. |
| report | 上报 | The record a node sends after a request, carrying facts only: status, business code, transport error kind, latency, size, markers and timestamps. Never the response content. |
| report shard | 上报分片 | One of `SPINNERET_REPORT_SHARDS` (default `16`) Redis streams that reports are distributed across, each owned by one worker. The count cannot be changed after go-live without stranding in-flight leases. |
| request explorer | 请求明细 | The console page that lists individual reported requests with their classification and blame. It is the only page that needs ClickHouse. |
| reuse interval | 复用间隔 | The minimum gap between two uses of the same identity, measured either from when the previous lease was acquired or from when it was released. |
| reveal | 明文查看 | Displaying a stored plaintext — an identity payload or a secret value — in the console. It needs its own permission and every reveal is audited. |
| risk event | 风险事件 | A recorded moment of elevated danger: a spike of bans, a failure detector tripping, an identity pool draining. Shown on its own console page and routable to alerts. |
| role | 角色 | A named bundle of permissions. The four are `viewer`, `operator`, `admin` and `owner`, in increasing order of what they can change. |
| role binding | 角色绑定 | The grant that gives a user a role, on a tenant and optionally narrowed to one namespace and some of its sites. A user account on its own grants nothing. |
| rotation policy | 轮换策略 | The policy that decides which identity an `Acquire` gets: which identity types are eligible, the selection strategy, lease durations, concurrency, reuse gaps, quotas, stickiness and proxy mode. |
| rule debugger | 规则调试器 | The console tool that replays a synthetic report against the currently resolved policies and shows which rule matched and what would have happened. |
| `_runtime` group | `_runtime` 分组 | A reserved, read-only config group that exposes live operational state — failure-detector states and site switches — to nodes through the same watch protocol as ordinary configuration. |
| scope (token) | 权限范围 | One entry in an API token's permission list, such as `lease:acquire:example-site` or `secret:read:prod/signing/*`. A token can do only what its scopes allow. |
| secret | 密钥 | A named, versioned, encrypted value at a path such as `signing/api_key`. Readable by nodes with a matching scope, or referenced from a config item. |
| secret reference | 密钥引用 | The `${secret:<path>}` placeholder inside a config item. The server resolves it when a node reads the item, if that node's token is also allowed to read the referenced path. |
| secret vault | 密钥保管库 | The part of Spinneret that stores secrets under envelope encryption, versions them, audits every read and supports online key rotation. |
| seed | 种子数据 | Synthetic demonstration or load-test data created by `spnr seed`: a site, endpoint groups, identities, published policies, a config item and a node token. Re-runnable: existing objects are kept, but the node token is re-created and the previous one revoked. |
| session | 会话 | A signed-in console session, carried by an `HttpOnly`, `SameSite=Strict` cookie whose lifetime is `SPINNERET_SESSION_TTL` (default `12h`). Unrelated to a node's stateless token auth. |
| shadow mode | 影子模式 | Running an action policy with `mode: shadow`, which records every disposition it would have applied and applies none. The way to evaluate a policy change against live traffic without risk. |
| signal policy | 信号策略 | The policy that classifies raw reports into outcomes and assigns blame, using conditions over status, business code, error kind, markers, URI, method, latency and size. |
| signal rule | 信号规则 | One condition-and-result entry inside a signal policy. The first one that matches decides the classification. |
| site | 站点 | One target system as Spinneret models it, holding its endpoint groups, URI rules, identity types and identities. Spinneret has no knowledge of the actual system beyond this record. |
| site switch | 站点开关 | An operator-controlled pause on a whole site. While it is on, acquires for that site are refused with a distinct reason instead of failing individually. |
| sticky session | 会话粘性 | Pinning one identity to a session key for a period, so a multi-step flow keeps the same credentials. Enabled per rotation policy; the key is sent with `Acquire`. |
| tail latency | 尾延迟 | The slow end of the latency distribution — p99 and above. It is where overload shows up first, long before the average moves. |
| tenant | 租户 | The hard isolation boundary: a company, customer or organization. Nothing is shared across tenants except the server process, the database and the operator's keys. |
| throughput | 吞吐 | Useful work completed per second. Measured here as `Acquire`→`Report` cycles per second, not as raw requests per second. |
| Valkey | Valkey | The Redis-compatible server the Compose stack runs for the hot state. Anything speaking the Redis protocol works; the benchmarks and the tuning notes were taken on Valkey. |
| version token | 版本令牌 | The plain `int32` a node sends with a configuration watch to say which version of an item it already holds (`0` for none). The server answers immediately when the published version *differs* from it — which is what makes a rollback reach a node — and parks the request while the two are equal. |
| warm-up | 预热 | A period after an identity is activated during which its quotas are scaled down, so a fresh credential is eased into traffic instead of being used at full rate immediately. |
| watcher | 订阅者 | One parked `WatchConfig` call. A node holds one no matter how many items (up to 200) that call watches, which is why `SPINNERET_MAX_WATCHERS` (default `20000` per instance) bounds fleet size. |

---

## Next

- [Concepts](./04-concepts.md) — the full mental model behind every term above.
- [Quick start](./01-quickstart.md) — an empty host to a working node in about thirty minutes.
- [Troubleshooting](./18-troubleshooting.md) — symptom, cause and fix for the failures that actually happen.
- [Contributing](./20-contributing.md) — how to change the thing you just read about.
