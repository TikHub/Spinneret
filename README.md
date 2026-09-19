<h1 align="center">Spinneret</h1>

<p align="center"><em>One server that hands out credentials, exits and configuration to a whole fleet of nodes — written for a large distributed crawler, and it works out which credential just got burned.</em></p>

<div align="center">

[English](./README.md) | [简体中文](./README.zh-CN.md)

Spinneret shares the things a distributed fleet never has enough of — accounts, API keys, sessions,
cookie jars, exit addresses — and pushes configuration down to the same nodes. Your nodes ask for a
credential and an exit before each request, use them, and report the status they got back. Seconds later
the burnt ones are out of rotation fleet-wide, and the next node to ask gets something else. Nobody edits
a config file, nobody restarts a worker.

It was built for a large distributed crawler: one pool of accounts, sessions or keys, a few hundred worker
processes, and no honest answer to "which of these still works". Nothing in it knows what a crawler is, so
any fleet queueing for the same short list of accounts, keys or exits uses it the same way.

One Go binary, PostgreSQL and Valkey — plus ClickHouse if you want the request explorer. No agent and no
sidecar on your machines: a node's entire configuration is a server URL and an API token, and the SDKs are
ordinary libraries, so plain HTTP works just as well. Measured on one instance: **4,499 acquire→report
cycles per second** at acquire p99 **1.97 ms**, against a pool of 100,000 identities.

[![License](https://img.shields.io/github/license/TikHub/Spinneret?style=flat-square)](LICENSE)
[![Release](https://img.shields.io/github/v/release/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/releases/latest)
[![Stars](https://img.shields.io/github/stars/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/stargazers)
[![Forks](https://img.shields.io/github/forks/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/forks)
[![Issues](https://img.shields.io/github/issues/TikHub/Spinneret?style=flat-square)](https://github.com/TikHub/Spinneret/issues)
<br>
[![CI](https://img.shields.io/github/actions/workflow/status/TikHub/Spinneret/ci.yml?branch=main&style=flat-square&label=CI)](https://github.com/TikHub/Spinneret/actions/workflows/ci.yml)
[![Release build](https://img.shields.io/github/actions/workflow/status/TikHub/Spinneret/release.yml?style=flat-square&label=release%20build)](https://github.com/TikHub/Spinneret/actions/workflows/release.yml)
[![Last commit](https://img.shields.io/github/last-commit/TikHub/Spinneret?style=flat-square&label=last%20commit)](https://github.com/TikHub/Spinneret/commits/main)
<br>
[![Go](https://img.shields.io/github/go-mod/go-version/TikHub/Spinneret?style=flat-square&logo=go&logoColor=white&label=go)](go.mod)
[![Container image](https://img.shields.io/badge/ghcr.io-tikhub%2Fspinneret-2496ed?style=flat-square&logo=docker&logoColor=white)](https://github.com/TikHub/Spinneret/pkgs/container/spinneret)
[![Docs](https://img.shields.io/badge/docs-21%20pages%20%C2%B7%20EN%20%2F%20%E4%B8%AD%E6%96%87-2f6feb?style=flat-square&logo=readthedocs&logoColor=white)](documents/README.md)

</div>

<div align="center">
  <img src="documents/images/overview.png" width="900" alt="The Spinneret console: per-site health, throughput, outcome mix and breakers"/>
</div>

---

## 🧭 The two calls

Two calls carry the hot path.

**`Acquire(site, client, uri)`** returns an identity, its credential already rendered the way you send
it — a cookie map, a ready `Cookie` header, headers, query parameters, a JSON fragment, or free-form
typed values — optionally a proxy, and a lease. The lease has a TTL, can be renewed, and is reclaimed
within one TTL by the reaper if the node dies holding it. `wait_ms` queues for up to 5 s instead of failing;
`AcquireBatch` takes up to 50 distinct identities in one round trip.

**Your node sends its own request.** Spinneret is never in the data path. It proxies no bytes and signs
nothing, and there is no login flow and no captcha handling anywhere in it. The only requests it makes on
its own account are the reachability check it runs through each egress route and the alerts it delivers —
both to URLs you configure.

**`Report(lease_id, …)`** carries facts, never verdicts: status code, business code, transport error kind,
up to 32 markers your node recognised in the response, latency, size. The server classifies it, decides
who pays — the credential, the route, both or neither — and applies at most one action per subject.

`GetConfig`, `WatchConfig` and `GetSecret` sit on the same server behind the same token. One URL and one
token is still the whole node configuration.

All of the behaviour below is YAML policy: versioned, with drafts, diff, publish and rollback, resolved
endpoint group > client > site > namespace > built-in. Publish, and it is live across the fleet in
seconds. No redeploy, no worker restart.

---

## 🕷 The main case: a large distributed crawler

Forty machines. One pool of a hundred thousand sessions. Nobody holds a list.

Monday morning. Throughput halved over the weekend and nothing crashed. Forty workers are pulling cookies
out of the same Redis set; four of those accounts were banned on Saturday night and are still being handed
out. You cannot tell me how many requests that costs, and neither can anyone else on the team. One proxy
subnet started answering with challenge pages at 02:00 and you cannot tell whether the proxies went bad or
the accounts did, because the only place that knows is a log line on whichever worker happened to draw that
pair. Someone has already pushed a `sleep(2)` to the hot loop and called it a fix, and the only way to
change the rotation interval is to redeploy 40 containers.

The pool is shared. What anyone knows about it is not.

With Spinneret every machine is ignorant on purpose: it asks per request, gets one credential and one
exit, and tells the server what happened. The pool has one state, and it lives in one place.

### Resource management

You have a pool, and the pool has rules that a semaphore cannot express.

Identities live in the server, not in your workers. Each one is a row with an encrypted payload, a type
that says how to render it, an optional account it belongs to, and a lifecycle state of its own —
`pending`, `active`, `quarantined`, `banned`, `expired`, `disabled`, `retired`. Proxies are a separate
namespace pool with kinds (`datacenter`, `residential`, `mobile`, `tunnel`), regions, providers and tags.

The limits are the point, and they are evaluated in one Redis script at acquire time, so they hold across
every worker on every machine instead of per process:

| You want | The knob |
| --- | --- |
| never two workers on the same account at once | `max_concurrent_leases: 1` — a distributed mutex with a TTL. The reaper sweeps every second, so a killed worker's lease comes back once its TTL is up, not once someone notices |
| at least 90 s between two uses of one identity | `reuse_interval`, anchored on `acquired` or `released`, scoped to the endpoint group or to the whole site |
| 60 requests an hour on the expensive endpoint group, and 2,000 a day on the same group | a list of sliding `quota` windows, per identity per endpoint group |
| the credential itself never sitting in a config file on forty machines | payload fields typed `secret_ref`, resolved out of the envelope-encrypted vault at acquire time, every read audited |
| a new account eased in instead of run at full rate on day one | `warmup: {duration, quota_factor}` scales its quotas while it is young |
| a freshly imported account proved before you trust it | `pending` identities get a probe trickle — weight factor `0.1`, at most 2 leases — and their first clean report activates them |
| every request from one account leaving through the same exit | proxy mode `bind_identity`, with `rebind_tolerance` and `max_rebinds_per_day` so a route failure does not turn into an account that appears in three countries before lunch |
| 20 accounts for one batch job in one round trip | `AcquireBatch`, up to 50 distinct identities |
| a caller to wait for a free identity rather than fail | `wait_ms`, up to 5,000 |

When the pool has nothing left you get `no_identity_available` with a reason, not a random pick that was
going to fail. So when you go and ask for more accounts you bring the shortfall count with you.

### Risk-control monitoring

One account gets banned. Thirty-nine machines do not know yet — that is the failure that costs you the
rest of the pool, because they keep hitting it and the target keeps learning.

**A 200 with an empty list is not a success, and your worker should not be the one deciding that.** It
reports the markers it recognised — `captcha_page`, `login_redirect`, `empty_list`, whatever names you
invent — and the signal policy maps status, business code, error kind, markers, URI, method, latency and
size onto 12 outcomes. A new failure mode is a new marker and a new rule, not a new release.

**Punishment has a blast radius.** Open it too wide and you stop forty working accounts to punish one. An
action lands at one of six scopes:

| Scope | What it takes out |
| --- | --- |
| `identity_endpoint` | that account on that endpoint group only — the usual answer to a rate limit |
| `identity_site` | that account everywhere on that site |
| `identity` | that account everywhere |
| `account` | every identity belonging to the same account, for when one session getting challenged means the account is flagged |
| `proxy_site` / `proxy` | the exit route, on this site or everywhere |

The actions are cooldown, quarantine, ban and expire. A cooldown takes a `multiplier` that backs it off
over a failure streak, capped (24 h by default) and reset after an hour of quiet. Repeated bans climb an
escalation ladder keyed on how many bans that identity collected inside a window. `expire` marks a
credential stale so your own refresh job replaces it — Spinneret will not mint one for you.

**A bad route looks exactly like a bad credential.** Charge the credential for the route's failure and you
retire good accounts one at a time, in the wrong order, for weeks. Cross-attribution watches the other
axis: inside a 10-minute window, one exit failing across three or more distinct identities is the exit's
fault, and one identity failing across three or more distinct exits is the identity's. The report is
re-blamed before anything is charged.

Health scores are an EWMA per identity per endpoint group, decaying back toward a baseline over hours, so
an account that failed twice at 3 a.m. is not still being punished at noon. The default strategy samples
proportional to the square of the score, so a degrading identity fades out of the rotation before anything
has to ban it.

Each endpoint group has its own breaker over a sliding window: closed, open, half-open with probe leases.
Open, and every `Acquire` for that group returns `circuit_open` with a retry hint, so your workers back off
instead of queueing. That is there because one endpoint group breaking otherwise means the whole fleet
spends an hour on it. Opening a breaker also reverts the cooldowns it charged during the window that
tripped it — those identities were never the problem. It is on by default
(`revert_recent_cooldowns: endpoint`).

You also have to be able to look at it. The heatmap is identity × endpoint group availability on one
screen, which is where "one group is on fire" and "the site is gone" look completely different. The request
explorer, on ClickHouse, is every report with its outcome and who was blamed, filterable by identity,
proxy, outcome, status or endpoint group, with the markers on every row — it is also how you find out that
your new marker rule has been quietly banning healthy sessions. 11 automatic alert kinds go out over
HMAC-signed webhooks or four chat platforms, de-duplicated so a flapping breaker does not produce a hundred
messages. The console updates live over SSE, and Prometheus `/metrics`, `/healthz` and `/readyz` are there
for everything else.

`unavailable`/`overloaded` and `no_identity_available` are separate reasons on purpose: one means ask again
later, the other means back off hard. Your client needs to tell them apart.

If you are not sure a policy is right, publish it in `mode: shadow` — it classifies, plans and records,
and applies nothing.

### Scheduling

`Acquire` runs a single Lua script in Redis. It checks the breaker, samples up to 32 candidates from the
ready queue, drops what is cooling down, leased, out of quota or inside its reuse interval, weights the
rest by the square of their health score, assigns an exit and writes the lease — no PostgreSQL in the path.

Four rotation strategies — `weighted_random`, `least_recently_used`, `round_robin`, `best_health` — set
per endpoint group. A cheap detail endpoint and an expensive listing endpoint almost never want the same
one, and the rest of rotation resolves per endpoint group too: the expensive ones can run exclusive leases
with a 90-second reuse interval while the cheap one runs four concurrent leases and no interval.

A multi-step flow that must stay on one session passes a `session_key`. It gets the same identity for
every step while that identity stays usable, bypassing the reuse interval, because a paginated crawl that
switches accounts halfway through is a crawl that gets flagged. `Renew` extends the lease,
`max_lease_lifetime` stops it being held forever, and the reaper cleans up after the process that dies
holding it.

What Spinneret does not do here: there is no fleet-wide request-rate ceiling per target. Limits are per
identity and per token, and the breaker is a failure-driven brake, not a rate governor. Keep your queue —
Spinneret answers **who to go as**, not **what to fetch**.

---

## 🧰 It is not only for crawlers

| What you actually run | What Spinneret is doing |
| --- | --- |
| a pool of metered third-party API keys | the key sits in the vault as a `secret_ref` field and is delivered into `headers` or `query` at acquire time, so it never lands in a node's config file; sliding quotas keep each key inside its budget, a rate-limit answer cools that one key down instead of the whole integration, and a rejected auth marks it expired for your refresh job |
| a CI or test fleet sharing a few real accounts | `max_concurrent_leases: 1` is a distributed mutex with a TTL, and the reaper cleans up after the job that died holding it |
| egress you want governed in one place | one namespace proxy pool for every workload, with per-route concurrency, health checks and blame separation; retiring a bad route is a state change on the route, not an edit to every workload using it |
| configuration and secrets, nothing else | versioned items with drafts, publish, diff and rollback; long-poll `WatchConfig`; `${secret:path}` resolved server-side; AES-256-GCM envelope encryption with online KEK rotation; `config:read` and `secret:read` token scopes; every secret read in the audit log |
| one pool, several teams | tenants → namespaces → sites, roles `viewer / operator / admin / owner` bindable per namespace and per site, node tokens scoped to exactly the sites they need, and an audit trail that says who changed the policy and which report caused the ban |

Plenty of people will want only the config server and the vault, and none of the rest.

---

## 📈 Performance and scaling

Measured with the k6 scenarios in `test/load/` against the Compose stack, on one site with 100,000
identities across 50 endpoint groups. Everything, the load generator included, ran inside one Docker VM on
a laptop, so read the shapes as the product and the rates as a floor.

| | Per instance |
| --- | --- |
| Full `Acquire` → `Report` cycle, exclusive leases, admission control on | **4,499/s** of 4,500 offered, acquire p99 **1.97 ms** |
| `Acquire` alone, no report, `max_concurrent_leases: 4` | **4,993/s** at server-side p99 **4.32 ms** |
| Report reception | **44,437/s** accepted, zero rejections |
| A published config change reaching a watcher | p99 **46.1 ms**, with 200 watchers |
| Concurrent long polls held | about **10,000**, at 0.05 cores |

API instances are stateless, so add as many as you want — just know what you are buying. **Replicas add
server capacity, not acquire throughput**: they share one Redis and the acquire ceiling lives there, so
what you buy is long-poll capacity, report processing and availability. Raising the ceiling means raising
Redis, and past that you partition into separate deployments. Report streams are sharded
(`SPINNERET_REPORT_SHARDS`, 16 by default) and rebalance across workers as you add them.

Past capacity it sheds instead of falling over. `SPINNERET_ACQUIRE_FLEET_INFLIGHT` (64 across the fleet by
default, divided by the live instances each one sees, never below 4 per instance) caps in-flight acquire
scripts and refuses the excess **in the server, before any Redis command**, as `unavailable`/`overloaded`
with a jittered retry hint both SDKs honour. At 4,500 offered on two replicas it served 2,418 cycles/s,
shed 1,881/s, and held acquire p99 at 89 ms: throughput fell, and nothing timed out.

[Status and performance](#-status-and-performance) has the two-replica history, the rest of the numbers and
the one thing the harness would not let me measure. [Performance and tuning](documents/en/17-performance.md)
has the method and the traps.

---

## 🚧 What Spinneret is not

- **Not in the data path.** No interception, no sidecar, no TLS termination. If you need something that
  sits in the path, you want a forward proxy or a mesh.
- **Not a job queue.** No work queue, no task distribution, no deduplication of your business work, no
  storage of responses. Keep your queue.
- **It does not obtain or refresh credentials.** No login flows, no signing algorithms, no session
  harvesting, no target-specific code of any kind. The `expire` action marks a credential stale so that
  your own refresh job replaces it.
- **No fleet-wide request-rate ceiling per target.** Limits are per identity (`quota`, `reuse_interval`,
  `max_concurrent_leases`) and per API token (requests per second, per instance). Distributed global rate
  limiting is a v0.2 candidate.
- **A proxy is assigned as part of a lease.** There is no credential-free, egress-only lease.
- **The report vocabulary is HTTP-shaped.** `uri` is required and `error_kind` is a closed enum of
  transport failures. Another protocol fits through `business_code` and markers, but the nouns will fight
  you.
- **It is overkill below a threshold.** One process and a handful of credentials that never get throttled:
  a local semaphore beats this. The crossover is more than one machine, more credentials than a person can
  track by hand, and usability that changes with use.

---

## 🔤 Vocabulary

The API and the console use six nouns throughout.

| Term | Meaning |
| --- | --- |
| **site** | one upstream target system inside a namespace — an API, a service, a platform |
| **client** | a flavour of access to it (`web`, `mobile`, `partner`); identities are not interchangeable across them |
| **endpoint group** | a set of endpoints that behave alike — the unit of quotas, cooldowns, health scores and breakers |
| **identity** | one usable credential or persona: a key, a token, a session, a cookie jar, a device fingerprint |
| **node** | a worker process in your fleet |
| **proxy** | an egress route |

---

## 🧱 Architecture

### The system

One binary in two roles: the request path that hands out credentials and egress, and the pipeline that
turns reports back into state — over PostgreSQL for truth, Valkey for the hot path, and ClickHouse for raw
history. Three views of the same process, drawn separately because they are the three things that go wrong
separately.

**The request path.** Everything a node calls, answered out of Redis — scheduling never touches
PostgreSQL, and the credential comes from the payload cache (a miss reads its encrypted payload once).

```mermaid
flowchart LR
    subgraph FLEET["Your fleet"]
        direction TB
        SDKGO["Go SDK"]
        SDKPY["Python SDK"]
        RAW["Connect · gRPC<br/>HTTP+JSON, no SDK"]
    end

    LB["Caddy<br/>least_conn<br/>/readyz ejection<br/>retry elsewhere"]

    subgraph APIROLE["SPINNERET_ROLE=api or all"]
        direction TB
        NODEAPI["Node API<br/>Acquire · AcquireBatch<br/>Renew · Release · Report<br/>GetConfig · WatchConfig<br/>GetSecret"]
        SCHED["Scheduler<br/>acquire.lua<br/>one round trip"]
        CRED["Payload cache + vault<br/>envelope decrypt<br/>secret_ref · render"]
        INGEST["Report ingestor<br/>validate · authorize<br/>dedup · shard"]
        CFGC["Config center<br/>long-poll watchers"]
    end

    RD[("Valkey<br/>hot state")]
    PG[("PostgreSQL<br/>source of truth")]

    SDKGO --> LB
    SDKPY --> LB
    RAW --> LB
    LB --> NODEAPI
    NODEAPI --> SCHED
    NODEAPI --> INGEST
    NODEAPI --> CFGC
    SCHED -->|"gate, pick,<br/>assign, lease"| RD
    SCHED --> CRED
    INGEST -->|"append to<br/>the shard"| RD
    CRED -->|"encrypted<br/>payloads"| PG
    CFGC -->|"published<br/>versions"| PG
```

**The report pipeline.** What a report becomes: a classification, a blame decision, one atomic hot-state
update, and at most one action per subject.

```mermaid
flowchart TB
    RDIN[("Valkey<br/>report stream shards")]

    subgraph WORKERROLE["SPINNERET_ROLE=worker or all — report pipeline and jobs"]
        direction TB
        REG["Shard registry<br/>heartbeat<br/>ownership rebalance"]
        CONS["One consumer per owned shard<br/>XREADGROUP · XAUTOCLAIM"]
        CLASS["Classify<br/>signal policy → outcome + blame"]
        OBS["observe.lua<br/>checkpoint · breaker window · quota<br/>health EWMA · streaks · counters"]
        PLAN["Action policy → plan<br/>most severe action per subject"]
        EXEC["Executor<br/>cooldown · quarantine<br/>ban · expire"]
        JOBS["Periodic jobs<br/>lease reaper 1s · breaker sweep 5s<br/>proxy health checks<br/>leader-only: snapshot · ban expiry<br/>alerts · partitions"]
        NOTIFY["Alert evaluation<br/>and delivery"]
    end

    RDOUT[("Valkey<br/>hot state")]
    PG[("PostgreSQL<br/>rows and state events")]
    HOOK["Webhook and<br/>chat channels"]

    RDIN -->|"stream entries"| CONS
    REG <--> RDIN
    CONS --> CLASS
    CLASS --> OBS
    CLASS --> PLAN
    PLAN --> EXEC
    OBS -->|"one atomic update"| RDOUT
    EXEC -->|"availability changes,<br/>immediately"| RDOUT
    EXEC -->|"asynchronously"| PG
    JOBS --> RDOUT
    JOBS -->|"advisory locks<br/>elect the leader"| PG
    JOBS -->|"leader only"| NOTIFY
    NOTIFY -->|"signed delivery"| HOOK
```

**Shared state, the console and observability.** Every instance carries the same in-process caches and
the same event bus; the three stores are shared by all of them.

```mermaid
flowchart LR
    subgraph OPS["Operators"]
        direction TB
        UI["React console<br/>served by the API instances"]
        PROM["Prometheus"]
    end

    ADMINAPI["Admin API<br/>sites · identities · proxies<br/>policies · breakers · config<br/>secrets · tenants · tokens"]

    subgraph EVERY["On every instance"]
        direction TB
        SSE["Server-Sent Events<br/>/api/v1/events/stream"]
        BUS["Event bus<br/>Redis pub/sub<br/>one channel per namespace"]
        CAT["Catalog<br/>in-process<br/>namespace snapshot"]
        STATS["Stats aggregator<br/>rollups and raw events"]
    end

    subgraph STATE["State"]
        direction TB
        PG[("PostgreSQL — source of truth<br/>tenants · sites · identities and<br/>encrypted payload versions · accounts<br/>proxies · policies · config · secrets<br/>state events · audit · rollups")]
        RD[("Valkey / Redis — hot state<br/>ready queues · identity, account and<br/>proxy hashes · leases and expiry sets<br/>breaker windows · quotas · sticky sessions<br/>report streams · dedup · console sessions")]
        CH[("ClickHouse — optional<br/>report_events · lease_events")]
    end

    UI --> ADMINAPI
    PROM -.->|"scrape<br/>/metrics"| EVERY
    ADMINAPI --> SSE
    ADMINAPI --> PG
    SSE -.->|"breaker, identity, proxy,<br/>alert and publish events"| UI
    BUS <--> RD
    BUS -.->|"invalidate"| CAT
    BUS -.->|"fan out"| SSE
    CAT -->|"reload on change"| PG
    STATS -->|"aggregates"| PG
    STATS -->|"raw events"| CH
    CH -->|"request explorer"| ADMINAPI
```

**Hot path.** `Acquire` is one Lua script against Redis: breaker, candidate sample, availability filter
(cooldown, reuse interval, quota, concurrency), lease written — and no PostgreSQL round trip. `Report`
validates, de-duplicates, appends to the Redis stream shard derived from the lease ID, and returns. The
full cycle is five Lua scripts, not one: the acquire, the report ingest, `observe`, the action apply and
the lease end.

**State layers.** PostgreSQL is the source of truth: catalog, identities, policies, secrets, audit. Redis
holds the derived hot state, and `spnr rebuild` rebuilds all of it from PostgreSQL whenever you need it.
ClickHouse is optional and stores raw request events for the request explorer.

**Roles.** One binary, `SPINNERET_ROLE=all|api|worker`. `all` is the default and what Compose runs. Split
the roles when a burst of reports must not slow `Acquire` down.

### One request, end to end

```mermaid
sequenceDiagram
    autonumber
    participant A as Worker A
    participant API as Spinneret API
    participant R as Valkey hot state
    participant T as Target system
    participant W as Spinneret report pipeline
    participant PG as PostgreSQL
    participant B as Worker B

    A->>API: Acquire(site, client, uri, wait_ms)
    Note over API: authenticate the token, resolve site, client<br/>and endpoint group, resolve the rotation policy
    API->>R: acquire.lua — one round trip
    Note over R: breaker gate, sticky reuse, sample the ready queue,<br/>drop what is cooling down, leased, out of quota or<br/>inside its reuse interval, weight by health score²,<br/>assign a proxy, write the lease
    R-->>API: identity + proxy + lease id
    API->>PG: read the encrypted payload version (cached)
    API-->>A: lease, credential, proxy, hints
    A->>T: the request, with that credential through that proxy
    T-->>A: response the worker recognises as a challenge
    A->>API: Report(report_id, lease_id, status, markers, latency, release)
    API->>R: dedup, then append to the lease's stream shard
    API-->>A: accepted
    W->>R: read the shard entry
    Note over W: signal policy classifies it: outcome captcha, blame identity
    W->>R: observe.lua — checkpoint, breaker window, quota,<br/>health scores, failure streak, counters
    Note over W: action policy plans a cooldown, or a ban if the count<br/>condition matches — the most severe action per subject wins
    W->>R: apply the action and end the lease
    W->>PG: state row and state event, asynchronously
    B->>API: Acquire, same endpoint group
    API->>R: acquire.lua
    R-->>API: a different identity — the penalised one is not offered
    API-->>B: lease, credential, proxy
    Note over W,R: within 5s the breaker sweep re-reads the window. If the<br/>whole group is failing it opens, and every Acquire gets<br/>circuit_open with a retry hint until a probe succeeds
```

### Deployment shapes

The default stack is one Compose file: two instances in the combined role behind Caddy, over PostgreSQL,
Valkey and ClickHouse. The server itself runs without ClickHouse — only the request explorer needs it —
but the shipped Compose file starts it and waits for it, so dropping it means editing
`deploy/compose/docker-compose.yml`.

```mermaid
flowchart TB
    F["Your fleet"] --> CADDY["Caddy — the published entry point"]

    subgraph HOST["One host · Docker Compose"]
        direction TB
        CADDY
        subgraph APPS["spinneret · SPINNERET_ROLE=all · 2 replicas by default"]
            direction LR
            I1["instance"]
            I2["instance"]
        end
        PG[("PostgreSQL")]
        VK[("Valkey")]
        CH[("ClickHouse — optional to the server<br/>started by this stack")]
        MIG["migrate — one-shot, runs before the instances"]
        PROM["Prometheus — optional profile"]
    end

    CADDY --> APPS
    APPS --> PG
    APPS --> VK
    APPS --> CH
    MIG --> PG
    PROM -.->|"scrape"| APPS
    KEK["KEK, mounted as a file secret"] -.-> APPS
```

Split the roles when reports and request serving have to scale independently. The instances are stateless
and replicate freely, the three stores are shared, and the acquire ceiling stays where it was: in Valkey.

```mermaid
flowchart LR
    F["Fleet — N application processes"] --> LB["Load balancer"]

    subgraph REPL["Replicated — stateless, add as many as you need"]
        direction TB
        subgraph APIS["SPINNERET_ROLE=api — serves traffic, runs no jobs"]
            direction LR
            A1["api"]
            A2["api"]
            A3["api"]
        end
        subgraph WKS["SPINNERET_ROLE=worker — pipeline and jobs, no public API"]
            direction LR
            W1["worker"]
            W2["worker"]
        end
    end

    subgraph SHARED["Shared by every instance"]
        direction TB
        PG[("PostgreSQL<br/>also elects the leader jobs<br/>through advisory locks")]
        VK[("Valkey<br/>hot state and report streams<br/>— the acquire ceiling lives here")]
        CH[("ClickHouse — optional")]
    end

    LB --> APIS
    APIS -->|"append reports to the shards"| VK
    APIS --> PG
    WKS -->|"own and drain the shards"| VK
    WKS --> PG
    APIS --> CH
    WKS --> CH
    OPS["Console and Prometheus"] --> LB
```

### Scenario topologies

The same control loop, drawn once per application. Only the identity and the target change.

**Pooled keys for rate-limited upstream APIs.** A pool of vendor keys, each with its own budget, handed
out one call at a time and withdrawn the moment the upstream says no.

```mermaid
flowchart LR
    APP["Application workers"] -->|"Acquire, rotation mode: no proxy"| S["Spinneret"]
    K[("Key pool — one identity per key<br/>the key itself is a secret_ref into the vault")] --- S
    S -->|"one key, inside its budget"| APP
    APP -->|"call"| U["Rate-limited upstream API"]
    APP -->|"Report the status it got"| S
    S --> Q["per-key sliding quota, reuse interval,<br/>one in-flight lease per key"]
    S --> L["rate limited → cool the key down<br/>auth rejected → mark it expired for refresh"]
    S --> BR["sustained failure → the breaker stops<br/>that endpoint group, not the whole integration"]
```

**Shared egress governance.** One egress pool for every workload in the namespace, with blame that tells
a bad credential apart from a bad route before either is punished.

```mermaid
flowchart LR
    N["Any outbound workload"] -->|"Acquire"| S["Spinneret"]
    P[("Namespace egress pool<br/>kind, region, provider, tags, max concurrency")] --- S
    S -->|"pool, region-matched, or pinned per identity"| N
    N -->|"Report: status, error kind, latency"| S
    S --> BL{"blame"}
    BL -->|"identity"| I["the credential pays"]
    BL -->|"proxy"| X["the route pays: cool it for this target<br/>or everywhere, and score it down"]
    BL -->|"cross attribution"| CA["one route failing across many credentials<br/>is the route's fault, and is re-blamed"]
    HC["Background health checker<br/>reachability, exit IP, region"] --> S
```

**Distributed collection.** Collectors report what the response looked like; the server decides what it
cost, and when to stop sending altogether.

```mermaid
flowchart LR
    C["Collector processes"] -->|"Acquire"| S["Spinneret"]
    S -->|"identity + egress + lease"| C
    C -->|"request"| T["Target system"]
    T -->|"response"| C
    C -->|"Report with the markers it recognised"| S
    S --> D{"signal policy"}
    D -->|"success"| OK["score up, straight back into the queue"]
    D -->|"challenge, rate limit, login lost"| CD["cool down this identity here<br/>ban it after N in a window"]
    D -->|"the whole group is failing"| BR["open the breaker, stop the traffic,<br/>give back the cooldowns it caused"]
```

**Long-lived sessions and multi-step workflows.** A workflow that must stay on one identity for every
step, with a lease that can be renewed, a cap that stops it being held forever, and a reaper that cleans
up after a process that dies.

```mermaid
flowchart LR
    W["Multi-step workflow"] -->|"Acquire with a session_key"| S["Spinneret"]
    S -->|"the same identity for every step<br/>while it stays usable"| W
    W -->|"Renew when the hint says so"| S
    S -->|"lease TTL, capped by max_lease_lifetime"| W
    W -->|"final Report with release"| S
    S --> ACC["Identities grouped into accounts"]
    ACC -->|"a report says the account is finished"| OUT["every identity of that account<br/>leaves scheduling at once"]
    S --> RE["the process dies holding the lease →<br/>the reaper ends it within one TTL<br/>and gives the identity back"]
```

**Runtime configuration and secrets.** Versioned configuration and envelope-encrypted secrets reach the
whole fleet in seconds, without a deployment and without secrets in images.

```mermaid
flowchart LR
    OP["Operator or CI"] -->|"publish a new version"| S["Spinneret config center"]
    S --- V[("Versioned items: json, yaml, text<br/>rollback to any published version")]
    F["Every process in the fleet"] -->|"WatchConfig long poll"| S
    S -->|"only what changed, within seconds"| F
    S -->|"resolves ${secret:...} server-side<br/>every read is audited"| SEC[("Vault<br/>envelope-encrypted secrets")]
    S -->|"reserved _runtime group:<br/>current breaker states and paused targets"| F
```

**Multi-team governance.** One installation serving several teams: hard isolation per tenant, a namespace
per team, tokens that cannot reach anything else, and an audit trail for every change.

```mermaid
flowchart TB
    PA["Platform team"] --> T["Tenant — hard isolation boundary"]
    T --> NSA["Namespace: team A<br/>own credentials, egress, policies, config"]
    T --> NSB["Namespace: team B<br/>own credentials, egress, policies, config"]
    NSA --> TOKA["API token, scoped to this namespace only"]
    NSB --> TOKB["API token, scoped to this namespace only"]
    TOKA --> WA["Team A processes"]
    TOKB --> WB["Team B processes"]
    PA --> RB["Role bindings: viewer, operator, admin, owner<br/>optionally narrowed to one namespace or a list of targets"]
    PA --> SH["Shadow mode: a new policy plans everything,<br/>applies nothing, and records what it would have done"]
    PA --> AU[("Audit log and state events<br/>who changed what, and which report caused it")]
```

---

## 🖥 The console

| | |
| --- | --- |
| ![Overview](documents/images/overview.png) | ![Identities](documents/images/identities.png) |
| Overview: per-site health, throughput, outcome mix, breakers | Identities: state, health score, cooldowns, filters |
| ![Heatmap](documents/images/heatmap.png) | ![Policies](documents/images/policies.png) |
| Heatmap: identity × endpoint group availability | Policies: YAML editor, versions, diff, publish |
| ![Breakers](documents/images/breakers.png) | ![Requests](documents/images/requests.png) |
| Breakers: state, windows, manual open and close, site switches | Request explorer: every report with its outcome and blame |

22 routes covering every module, English and Chinese, light and dark, live updates over Server-Sent
Events. The same overview in Chinese: [`documents/images/overview-zh.png`](documents/images/overview-zh.png).

---

## 🧩 What is in v0.1

Every module below is implemented.

| Module | What it gives you |
| --- | --- |
| **[Identity scheduling](documents/en/06-identities.md)** | Identity types with typed payload fields and delivery templates; rotation policies (`weighted_random`, `least_recently_used`, `round_robin`, `best_health`), lease TTL and lifetime, reuse interval and anchor, quotas, sticky sessions, warm-up, probe weighting |
| **[Egress distribution](documents/en/07-proxies.md)** | Proxy pool with kinds, regions, providers and tags; assignment modes `none / pool / bind_identity / region_match`; session templates; periodic health checks; per-site proxy cooldowns |
| **[Reporting and signal detection](documents/en/08-policies.md)** | Batched, idempotent reports; configurable signal rules over status, business code, error kind, markers, URI, method, latency and size; 12 outcomes; cross attribution between identity and proxy |
| **[Cooldowns and bans](documents/en/08-policies.md)** | Cooldown at identity-endpoint, identity-site, identity, account, proxy-site and proxy scope; ban at identity, account and proxy; quarantine at identity and proxy; expire at identity; exponential backoff with caps; escalation ladders; shadow mode; manual operations and bulk rollback (`RevertActions`) |
| **[Health and lifecycle](documents/en/06-identities.md)** | EWMA health score with time decay, per-endpoint low-score cooldowns, automatic quarantine, identity state machine (`pending → active → quarantined / banned / expired / disabled / retired`) |
| **[Circuit breaking](documents/en/08-policies.md)** | Per endpoint group sliding-window statistics, three-state breaker (closed / open / half-open) with probe leases, manual open and close, optional revert of cooldowns applied in the tripping window |
| **[Config center](documents/en/09-config-center.md)** | Versioned config items with drafts, publish, rollback and diffs; long-poll `WatchConfig`; local snapshots in the SDKs; `${secret:path}` references; read-only `_runtime` group exposing breakers and site switches |
| **[Vault](documents/en/10-secrets.md)** | AES-256-GCM envelope encryption (KEK → DEK → data), file or env KEK providers, online KEK rotation and rewrap, secret versions and expiry, every read audited |
| **[Auth and tenancy](documents/en/11-access-control.md)** | Tenants → namespaces → sites; console users with roles `viewer / operator / admin / owner`, per-namespace and per-site bindings; node API tokens with fine-grained scopes; Argon2id passwords, login throttle, sessions, CSRF |
| **[Web console](documents/en/05-console-overview.md)** | 22 routes covering every module, English and Chinese, light and dark, live updates over SSE |
| **[Notifications](documents/en/12-observability.md)** | Webhook with HMAC-signed delivery plus four chat-platform senders; 11 automatic alert kinds and a test alert, with de-duplication and per-site routing |
| **[Observability](documents/en/12-observability.md)** | `/healthz`, `/readyz`, Prometheus `/metrics`, optional OTLP tracing, ClickHouse-backed request explorer |

---

## ⚡️ Quick start

Requirements: Docker Engine with the Compose plugin, Compose 2.24 or newer, about 4 GB of free RAM, one
free host port (8080 by default).

### One command

```bash
curl -fsSL https://raw.githubusercontent.com/TikHub/Spinneret/main/install/install.sh -o install.sh
less install.sh          # read it first; you are about to run it
bash install.sh
```

The guided installer detects the host, offers to install Docker if it is missing, clones the repository,
generates the passwords and the vault key **on the machine**, writes the Compose overrides for this host,
pulls or builds, migrates, starts, waits for `/readyz` and creates the first administrator — asking seven
questions, each with a default. `--yes` takes every default, `--check` changes nothing and prints what
would happen, and `--manage` re-opens it as a management menu: status, upgrade, accounts, tokens, backup,
restore, health, disk, uninstall.

A Chinese version with identical behaviour is [`install/install.zh.sh`](install/install.zh.sh);
[`install/README.md`](install/README.md) documents every question, flag and file it writes.

### Or by hand

```bash
git clone https://github.com/TikHub/Spinneret.git
cd Spinneret

# 1. Generate deploy/compose/.env (random passwords) and deploy/compose/secrets/kek.key
./scripts/compose-init.sh

# 2. Build the image and start Postgres, Valkey, ClickHouse, migrations, 2 replicas and the LB
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait

# 3. Create the first administrator, tenant and namespace (idempotent)
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

Open <http://localhost:8080> and sign in as `admin`; the password is in `deploy/compose/.env`:

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

### Run the example node

A complete node that leases, requests and reports against a built-in mock target:

```bash
./scripts/example-quickstart.sh        # or: make example
```

The script is idempotent. It seeds the site `example` (2 endpoint groups, 20 identities, 2 mock proxies,
published rotation and signal policies, a config item), creates a node token, starts the example service
on <http://localhost:18000> and calls it:

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

Now make the target misbehave and watch Spinneret react in the console — Identities, Breakers, Requests:

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

See [`examples/fastapi-crawler/README.md`](examples/fastapi-crawler/README.md), and
`./scripts/example-quickstart.sh --reset` to undo it.

### Where to look next

| If you want to | Read |
| --- | --- |
| Understand the model before configuring anything | [Concepts](documents/en/04-concepts.md) |
| Go from nothing to a node in about ten minutes | [Quick start](documents/en/01-quickstart.md) |
| Deploy behind TLS, on several hosts, or upgrade one | [Installation and deployment](documents/en/02-installation.md) |
| Know what every `SPINNERET_*` variable does | [Configuration](documents/en/03-configuration.md) |

---

## 🔌 Integrating a node

Every RPC is `POST /spinneret.v1.<Service>/<Method>` with `Content-Type: application/json` — Connect,
gRPC and gRPC-Web work too. Field names are snake_case, timestamps are RFC 3339 UTC.

### The API in two calls

First a token. `spnr` lives in the image and needs only PostgreSQL, so run it through the one-shot
`migrate` service rather than through a replica that may not be healthy. Scopes are per site and per
config group:

```bash
docker compose -f deploy/compose/docker-compose.yml run --rm -T \
  --entrypoint /usr/local/bin/spnr migrate \
  token create --tenant default --namespace default --name node-hk \
    --scope lease:acquire:example --scope report:write:example \
    --scope config:read:crawler --expires 720h
# spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
```

**Acquire** an identity and an egress route for the URI you are about to call:

```bash
curl -s http://localhost:8080/spinneret.v1.LeaseService/Acquire \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: node-hk-03" \
  -H 'Content-Type: application/json' \
  -d '{"site":"example","client":"web","uri":"/site/search?q=shoes","wait_ms":2000}'
```

```json
{
  "lease": {
    "lease_id": "lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
    "identity_id": "idt_01a0b0a4dc4774759bba124ee0e6be8f",
    "identity_type": "example_web_cookie",
    "endpoint_group": "search",
    "expires_at": "2026-09-17T20:12:54.398Z",
    "sticky": false,
    "probe": false
  },
  "credential": {
    "cookies": { "csrftoken": "csrf-17", "sessionid": "example-17" },
    "cookie_header": "",
    "headers": { "User-Agent": "ExampleClient/17" },
    "query": {},
    "json": null,
    "values": {}
  },
  "proxy": {
    "proxy_id": "pxy_01a0b0a4dc4b7c5984d858b85e92f447",
    "url": "http://expx1:secret@mocktarget:9091",
    "kind": "datacenter",
    "region": ""
  },
  "hints": { "renew_before_ms": 15000 }
}
```

**Report** what happened — facts only, the server decides the outcome. `release: true` ends the lease:

```bash
curl -s http://localhost:8080/spinneret.v1.ReportService/Report \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"reports":[{
        "report_id":"9f1c4c40-2f6a-4d6e-9c63-8f1b1d8f0a11",
        "lease_id":"lse_01a0b0ff449e78a49c2d8cc240515316_i_05",
        "uri":"/site/search","method":"GET","http_status":200,
        "latency_ms":143,"response_bytes":48213,"markers":[],
        "started_at":"2026-09-17T20:11:54.100Z",
        "finished_at":"2026-09-17T20:11:54.243Z",
        "release":true}]}'
```

```json
{ "accepted": 1, "duplicated": 0, "rejected": [] }
```

Failures carry a machine-readable reason and a retry hint in response headers, so a client can back off
without parsing prose:

```http
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

The full reference — every node service, error reason and retry rule — is in the
[Node API reference](documents/en/13-node-api.md).

### SDKs

| SDK | Package | Highlights |
| --- | --- | --- |
| **Python** — [`sdk/python`](sdk/python/README.md) | `sdk/python`, importable as `spinneret` | Sync `Client` and asyncio `AsyncClient` on httpx, pydantic v2 models, `with client.lease(...)` merging credential and proxy into httpx arguments, background batching reporter, config watcher with local snapshots |
| **Go** — [`sdk/go`](sdk/go/README.md) | `github.com/TikHub/Spinneret/sdk/go/spinneret` | Connect JSON or gRPC, typed errors (`IsCircuitOpen`, `IsNoIdentity`, …), `Lease.Apply(req)` and `Lease.Transport(base)`, batching `Reporter`, `ConfigWatcher` with snapshots |

```python
import httpx, spinneret

with spinneret.Client() as client:                       # SPINNERET_URL / SPINNERET_TOKEN
    with client.lease(site="example", client="web", uri="/site/search") as lease:
        with httpx.Client(**lease.httpx_kwargs()) as http:
            r = http.get("http://target/site/search", params={"q": "shoes"})
        lease.report_response(r, markers=["empty_list"] if not r.json() else [])
```

```go
client, _ := spinneret.New(spinneret.Options{})          // SPINNERET_URL / SPINNERET_TOKEN
lease, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: "example", Client: "web", Uri: target})
if err != nil {
	return err                                           // spinneret.IsNoIdentity(err), IsCircuitOpen(err), ...
}
defer lease.Close(ctx)                                   // the last report releases the lease

transport, _ := lease.Transport(nil)                     // http.DefaultTransport clone + leased proxy
req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
lease.Apply(req)                                         // cookies, headers, query of the credential
resp, err := (&http.Client{Transport: transport}).Do(req)
if err != nil {
	return lease.ReportError(err, spinneret.ReportInput{})
}
return lease.ReportResponse(resp, spinneret.ReportInput{Markers: detectMarkers(resp)})
```

No SDK for your language? Plain HTTP and JSON is a first-class client — see the
[Node API reference](documents/en/13-node-api.md) and [`proto/README.md`](proto/README.md).

---

## ⚗️ Built with

| Layer | Stack |
| --- | --- |
| Server | Go 1.27, one binary, `SPINNERET_ROLE=all\|api\|worker` |
| Transport | Connect, gRPC, gRPC-Web and HTTP+JSON from one set of Protobuf definitions |
| Data | PostgreSQL (source of truth), Valkey or Redis (hot state, leases, report streams, sessions), ClickHouse (raw request events, optional) |
| Hot path | Lua scripts executed server-side in Redis: one round trip per `Acquire` |
| Console | React 18 and TypeScript, embedded into the binary with `go:embed` |
| Deployment | Docker Compose behind Caddy; images at `ghcr.io/tikhub/spinneret` for linux/amd64 and linux/arm64 |
| Observability | Prometheus metrics, optional OTLP tracing |
| Tooling | buf, sqlc, golangci-lint, k6, Playwright, Vitest, pytest |

Compatibility, as tested in CI and pinned in Compose:

| Component | Version |
| --- | --- |
| Go | 1.27 or newer |
| Node and pnpm | Node 22, pnpm 10 |
| Python (SDK) | 3.10, 3.12, 3.13 |
| PostgreSQL | 17 in Compose |
| Valkey or Redis | Valkey 8 in Compose |
| ClickHouse | 25.8 in Compose, optional |
| Docker Compose | 2.24 or newer |

---

## 🗂 Project layout

```text
cmd/                spinneret-server, spnr (the admin CLI)
proto/              Protobuf definitions; the wire contract
internal/
  scheduler/        Acquire, AcquireBatch, Renew, Release, the lease reaper, acquire.lua
  worker/           report pipeline: shard ownership, classify, observe.lua
  policy/           rotation, signal, action and breaker policies as versioned YAML
  action/           cooldown, quarantine, ban, expire, revert
  breaker/          sliding windows, three-state breaker, the _runtime group
  identity/         identity types, payload rendering, masking, import
  identitysvc/      identity and account services, the state machine
  proxy/            pool, assignment modes, health checks, sealed URLs
  configcenter/     versioned items, drafts, publish, rollback, WatchConfig
  vault/            envelope encryption, KEK providers, rewrap
  auth/             users, roles, node tokens, sessions, password hashing
  tenancy/ authz/ audit/
                    tenants and namespaces, scopes and permissions, the audit trail
  server/           HTTP and Connect mounts, SSE, component wiring
  store/            PostgreSQL queries, Redis keys, ClickHouse schema
web/                React console, embedded into the binary
sdk/go, sdk/python  client libraries
examples/           a complete example node against a mock target
install/            the guided installer, English and Chinese
deploy/compose/     the Compose stack, Caddy, Prometheus
documents/          the manual, 21 pages in English and Chinese
test/               e2e, load (k6) and hot-path benchmarks
```

---

## 📊 Status and performance

Spinneret is pre-1.0 and feature-complete for v0.1. The four milestones — core path, risk-control loop,
infrastructure, console and release — are implemented, and the stack ships with Go end-to-end scenarios, a
replica failover drill, a Playwright console suite and k6 load scenarios.

The numbers, in full. Measured on the Compose stack, one site with 100,000 identities across 50 endpoint
groups, one server instance and one Valkey instance. Only the first row was measured with acquire
admission control on; the other three predate the gate, which the performance page says plainly. Full
tables, method and caveats are in [Performance and tuning](documents/en/17-performance.md).

| Measurement | Result |
| --- | --- |
| Full acquire→report cycle, exclusive leases, **admission control on** | **4,499/s per instance** at 4,500 offered, acquire p99 **1.97 ms**, 0.4 sheds/s |
| `Acquire` alone, no report, `max_concurrent_leases: 4` | **4,993/s per instance** at server-side p99 **4.32 ms**, peak 7,792/s |
| Report ingest | **44,437/s** accepted per instance with zero rejections; about 20,000/s applied to hot state inside the 200 ms budget |
| Config change to fleet awareness | p99 **46.1 ms** to wake every watcher, measured with 200 watchers; separately, about 10,000 concurrent long polls held per instance at 0.05 cores |

**Replicas add server capacity, not acquire throughput**, because they share one Redis and the acquire
ceiling lives there. Two replicas over one Valkey served 4,000 cycles/s at 4,000 offered, gated or ungated
— the highest rate at which two replicas kept up, not a ceiling anyone found. An earlier round of this
measurement had two replicas doing *less* than one; it did not reproduce from a rebuilt and warmed pool,
and it would have been easy to publish the admission gate as its cure. What replicas do buy is long-poll
capacity, report processing and availability. Raising the acquire ceiling means raising Redis, and past
that you partition into separate deployments.

Each instance bounds its own concurrency at Redis with **acquire admission control**
(`SPINNERET_ACQUIRE_FLEET_INFLIGHT`, 64 across the fleet by default, divided by the live instances each one
sees and never below 4 per instance; `0` builds no gate at all). Excess acquires are shed inside the
server, before any Redis command is issued, as `unavailable`/`overloaded` with a jittered retry hint.
Three things were measured: it costs a single replica nothing (4,499 of 4,500 offered, 0.4 sheds/s); at
capacity two replicas behave much the same gated or ungated, for some tail (acquire p99 2.99 ms against
1.66 ms); and past capacity it converts overload into shedding rather than timeouts (2,418/s served,
1,881/s shed, acquire p99 89 ms).
**What the same overload does with the gate off was never cleanly measured** — a collapsed run poisons the
next one and a Valkey restart looks identical to congestion collapse, so every attempt was invalidated. The
performance page marks which claims are mechanism and which are measurement, and gives an A/B recipe for
your own hardware.

Out of scope for v0.1 and candidates for v0.2: distributed global rate limiting, external validator and
refresher webhooks, proxy provider adapters, NATS JetStream, OIDC and TOTP, mTLS, staged config rollouts,
fingerprint distribution, browser pools.

---

## 🔢 Versioning and compatibility

Spinneret follows SemVer with pre-1.0 semantics. Before 1.0, the node API wire format
(`spinneret.v1.*`), the `SPINNERET_*` variables and the policy YAML may change in a minor release. Every
change lands in [`CHANGELOG.md`](CHANGELOG.md), which follows Keep a Changelog.

Schema migrations are applied with `spnr migrate up`, inspected with `spnr migrate status` and rolled
back with `spnr migrate down --to <version>`; back up before upgrading, as
[Operations](documents/en/16-operations.md) describes. Container images are published per release tag on
`ghcr.io/tikhub/spinneret` for linux/amd64 and linux/arm64 by the `release` workflow. Security fixes go
to the latest release and `main`, per [`SECURITY.md`](SECURITY.md).

---

## 🔐 Security

Report a vulnerability privately at
<https://github.com/TikHub/Spinneret/security/advisories/new>. Never open a public issue for a security
problem. [`SECURITY.md`](SECURITY.md) describes the process.

Three things stay yours in every deployment. The KEK that wraps the vault's data keys lives outside the
repository and outside the image. The console and the node API sit behind TLS. Node tokens are scoped to
one namespace, the sites they need and nothing else. [Security hardening](documents/en/19-security.md) is
the checklist to work through before anyone else can reach the installation.

---

## 📖 Documentation

**Start here** — [Quick start](documents/en/01-quickstart.md) ·
[Concepts](documents/en/04-concepts.md) · [Console overview](documents/en/05-console-overview.md)

**Deploy and operate** — [Installation](documents/en/02-installation.md) ·
[Configuration](documents/en/03-configuration.md) · [Operations](documents/en/16-operations.md) ·
[Performance and tuning](documents/en/17-performance.md) ·
[Troubleshooting](documents/en/18-troubleshooting.md) ·
[Security hardening](documents/en/19-security.md)

**Configure the loop** — [Identities and accounts](documents/en/06-identities.md) ·
[Proxies](documents/en/07-proxies.md) · [Policies](documents/en/08-policies.md) ·
[Config center](documents/en/09-config-center.md) · [Secret vault](documents/en/10-secrets.md) ·
[Access control](documents/en/11-access-control.md) ·
[Observability and alerting](documents/en/12-observability.md)

**Integrate** — [Node API reference](documents/en/13-node-api.md) · [SDKs](documents/en/14-sdks.md) ·
[CLI reference](documents/en/15-cli.md) · [`proto/README.md`](proto/README.md) ·
[`sdk/python/README.md`](sdk/python/README.md) · [`sdk/go/README.md`](sdk/go/README.md) ·
[`examples/fastapi-crawler/README.md`](examples/fastapi-crawler/README.md)

**Reference** — [FAQ and glossary](documents/en/21-faq.md) ·
[Contributing guide](documents/en/20-contributing.md) · [`install/README.md`](install/README.md) ·
[`web/README.md`](web/README.md) · [`test/load/README.md`](test/load/README.md) ·
[`test/perf/README.md`](test/perf/README.md)

The complete index, in both languages, is [`documents/README.md`](documents/README.md) ·
[中文](documents/README.zh-CN.md).

---

## 🛠 Development

Requirements: Go 1.27 or newer, Node 22 with pnpm 10, Docker for the integration test infrastructure, and
Python 3.10 or newer for the SDK.

```bash
export PATH="$(go env GOPATH)/bin:$PATH"

make infra-up          # PostgreSQL, Valkey and ClickHouse for tests on ports 45432 / 46379 / 49000
make test              # go test ./...            (integration tests use the infra above)
make test-race         # go test -race ./...
make lint vet fmt      # golangci-lint, go vet, gofmt
make generate          # buf generate + sqlc generate (commit the result)

make web-install web   # pnpm install + pnpm build (the console is embedded via go:embed)
make build             # bin/spinneret-server and bin/spnr
make docker up down    # build the image, start and stop the Compose stack
```

| Command | What it runs |
| --- | --- |
| `make test` | Go unit and integration tests |
| `make e2e` | Go end-to-end scenarios inside the Compose network (`test/e2e`, build tag `e2e`) |
| `make e2e-failover` | Replica failover drill under acquire and report load |
| `make e2e-web` | Playwright suite driving the real console (`web/e2e`) |
| `cd web && pnpm test` | Console unit tests (Vitest) |
| `make python-test` | Python SDK tests |
| `make example-test` | Example node tests |
| `make load` | k6 load scenarios (profile `loadtest`) |

`.github/workflows/ci.yml` runs the Go suite with `-race`, checks that generated code is up to date,
builds and tests the console, tests the Python SDK on 3.10, 3.12 and 3.13, and builds the image.

The `spnr` CLI administers a deployment and reads the same `SPINNERET_*` environment as the server:

```bash
spnr migrate up|down|status      spnr admin init --username admin --password-stdin
spnr token create --name ...     spnr rebuild [--site ...]
spnr kek generate|status|rewrap  spnr seed --site loadtest --identities 100000
spnr config check                spnr healthcheck --url http://127.0.0.1:8080/readyz
```

---

## 🤝 Contributing and support

| | |
| --- | --- |
| Found a bug, or want a feature? | Open an [issue](https://github.com/TikHub/Spinneret/issues) — [CONTRIBUTING.md](CONTRIBUTING.md) explains what makes a good one, and [documents/en/20-contributing.md](documents/en/20-contributing.md) is the development guide |
| Not sure how something works? | Start with the [FAQ and glossary](documents/en/21-faq.md), then [Troubleshooting](documents/en/18-troubleshooting.md), then [Discussions](https://github.com/TikHub/Spinneret/discussions) |
| Found a security problem? | Do **not** open a public issue. [SECURITY.md](SECURITY.md) explains private reporting |
| Anything else | <support@tikhub.io> |

Community support runs on issues and discussions, with no service-level agreement. Commercial support is
available from TikHub.

---

## 📄 Licence

Spinneret is released under the [Apache License 2.0](LICENSE). In short: you may use, modify and
redistribute it, including commercially; you must preserve the licence and attribution notices and state
significant changes; and the licence grants patent rights while terminating them if you bring a patent
claim over the software.

Spinneret is developed, maintained and open-sourced by [TikHub](https://github.com/TikHub), which builds
on the same code.

You are responsible for what you point it at. Spinneret arbitrates credentials and egress that you supply
and targets that you choose; obtaining those credentials lawfully, honouring the terms that govern the
systems you call, and complying with the law where you operate are yours to get right.
