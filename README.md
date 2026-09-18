<h1 align="center">Spinneret</h1>

<p align="center"><em>A control plane for fleets that share scarce, rate-limited credentials and egress.</em></p>

<div align="center">

[English](./README.md) | [简体中文](./README.zh-CN.md)

Your workers ask for a credential and an exit before each request, use them, and report what happened.
Spinneret turns those reports into cooldowns, bans, health scores and circuit breaking within seconds —
and distributes versioned configuration and secrets to the same fleet.

One Go binary, PostgreSQL and Valkey — plus ClickHouse if you want the request explorer, which the
default stack starts for you. No agent and no sidecar on your workers: a server URL and a token are the
whole node configuration, and the SDKs are ordinary libraries — plain HTTP works just as well.

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

## 🧭 What Spinneret is

Some resources are neither fungible nor stateless. A pool of API keys, accounts, sessions, device
identities or egress addresses is limited in number, limited in rate, and — the part that breaks ordinary
tooling — changes its own usability as a function of how it was just used. Use one twice in a second and
it is throttled. Use it on the wrong endpoint and it is challenged. A connection pool has none of these
properties, and a health check cannot discover them, because the only probe that tells you whether a
credential is still good is a real request made with it.

Spinneret is the arbiter for resources of that shape. Workers hold no list. They ask per request and get
back one credential already rendered for use, optionally one egress route, and a lease: a TTL-bounded,
renewable, reclaimable claim that tells the scheduler how many workers hold that credential right now.

```text
Acquire(site, client, uri)   →  identity + credential + proxy + lease
   ... the worker sends its own request to its own target ...
Report(lease_id, status, latency, markers)
                             →  the server classifies, assigns blame, cools down,
                                bans, scores, trips breakers — fleet-wide, in seconds
```

A worker reports facts, never verdicts: status code, business code, transport error kind, up to 32
markers it recognised in the response, latency, size, timestamps. The server classifies the report,
decides who pays — the credential, the route, both or neither — and applies at most one action per
subject at a chosen blast radius. All of it is policy written as versioned YAML: published, diffable,
rollback-able, and in effect seconds later without touching a worker.

Spinneret is never in the data path. It never sits between your workers and the systems they call,
proxies no byte, signs nothing, and contains no login flows or captcha handling. Its only outbound
traffic of its own is the periodic reachability check it makes through each egress route, to a URL you
configure. It manages the state around your requests; your worker still makes them.

---

## 🎯 Who it is for

Spinneret does not know what your workers do. It manages the things they must not waste or misuse: a pool
of credentials, a pool of exits, and the configuration and secrets that tell a worker how to behave.
Anywhere a fleet shares an identity pool that is scarce, rate-limited or stateful, the same machinery
applies.

| Application | The features that serve it |
| --- | --- |
| **Fleets calling metered third-party APIs** — a pool of keys with per-key quotas and concurrency ceilings | identity field kinds `string` / `secret_ref` so the key lives in the vault, delivery into `headers` or `query`, several sliding `quota` windows per key per endpoint group, `reuse_interval`, `max_concurrent_leases`, health scores, per-endpoint-group breakers |
| **Egress governance** — which exit each request leaves through, and retiring one that goes bad | a namespace proxy pool with kinds, regions, providers and tags; modes `none / pool / bind_identity / region_match`; per-route concurrency; periodic health checks; blame separation between route and credential |
| **Distributed data collection** — many workers, one pool of sessions, targets that push back | identity leasing, cooldown ladders, account-scoped bans, markers as the extension point for a new failure mode, the cooldown heatmap, the request explorer |
| **Account-bound automation** — each account is a scarce, stateful identity | the accounts entity, ban and cooldown at account scope, sticky sessions, `bind_identity` egress affinity, the seven-state identity machine |
| **Test and CI fleets sharing a few real accounts** | `max_concurrent_leases: 1` as a distributed mutex with a TTL, the lease reaper that reclaims what a killed job held, `max_lease_lifetime`, `wait_ms` queueing up to 5 s, `AcquireBatch` for up to 50 distinct identities in one round trip |
| **Fleet configuration and secret distribution** — change behaviour without a redeploy | versioned config items with drafts, publish, rollback and diff; long-poll `WatchConfig`; `${secret:path}` references; an envelope-encrypted vault with KEK rotation; `config:read` and `secret:read` token scopes |
| **Shared-pool governance across teams** — one pool, several teams, an audit trail | tenants → namespaces → sites, roles `viewer / operator / admin / owner` with per-namespace and per-site bindings, namespace-scoped node tokens, every secret read audited |

### What Spinneret is not

- **Not in the data path.** No interception, no sidecar, no TLS termination. If you need something that
  sits in the path, you need a forward proxy or a mesh.
- **Not a job queue.** It has no work queue, no task distribution, no deduplication of your business work
  and no storage of responses. Keep your queue; Spinneret answers *who to go as*, not *what to do*.
- **It does not obtain or refresh credentials.** No login flows, no signing algorithms, no session
  harvesting, no target-specific code of any kind. The `expire` action marks a credential stale so that
  your own refresh job replaces it.
- **There is no fleet-wide request-rate ceiling per target.** Limits are per identity (`quota`,
  `reuse_interval`, `max_concurrent_leases`) and per API token (requests per second, per instance). The
  circuit breaker is a failure-driven brake, not a rate governor. Distributed global rate limiting is a
  v0.2 candidate.
- **A proxy is assigned as part of a lease.** There is no credential-free, egress-only lease.
- **The report vocabulary is HTTP-shaped.** `uri` is required, `error_kind` is a closed enum of transport
  failures. Another protocol can be expressed through `business_code` and markers, but the nouns will
  fight you.
- **It is overkill below a threshold.** One process, a handful of credentials that never get throttled: a
  local semaphore beats this. The crossover is more than one machine, *and* more credentials than a
  person can track by hand, *and* usability that changes with use.

### Vocabulary

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
turns reports back into state — over PostgreSQL for truth, Valkey for the hot path, and ClickHouse for
raw history. Three views of the same process follow.

**The request path.** Everything a worker calls, answered with one Redis round trip and no PostgreSQL in
the way.

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
X
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


**Hot path.** `Acquire` runs one Lua script against Redis: it checks the breaker, samples candidates,
filters on availability (cooldown, reuse interval, quota, concurrency) and writes the lease — no
PostgreSQL round trip. `Report` validates, de-duplicates and appends to a Redis stream shard derived from
the lease ID, then returns.

**State layers.** PostgreSQL is the source of truth: catalog, identities, policies, secrets, audit. Redis
holds the derived hot state and can be rebuilt from PostgreSQL at any time with `spnr rebuild`.
ClickHouse is optional and stores raw request events for the request explorer.

**Roles.** One binary, `SPINNERET_ROLE=all|api|worker`. `all` is the default and what Compose runs; split
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

Split the roles when reports and request serving should scale independently. The instances are stateless
and replicate freely, the three stores are shared, and the acquire ceiling stays with the single Valkey.

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

21 routes covering every module, English and Chinese, light and dark, live updates over Server-Sent
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
| **[Web console](documents/en/05-console-overview.md)** | 21 routes covering every module, English and Chinese, light and dark, live updates over SSE |
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
| Python (SDK) | 3.9, 3.12, 3.13 |
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

Spinneret is pre-1.0 and feature-complete for v0.1: the four milestones — core path, risk-control loop,
infrastructure, console and release — are implemented, and the stack ships with Go end-to-end scenarios,
a replica failover drill, a Playwright console suite and k6 load scenarios.

Measured on the Compose stack, one site with 100,000 identities across 50 endpoint groups, one server
instance and one Valkey instance. Only the first row was measured with acquire admission control turned
on; the other three predate the acquire gate, as the performance page states. Full tables, method and
caveats are in [Performance and tuning](documents/en/17-performance.md).

| Measurement | Result |
| --- | --- |
| Full acquire→report cycle, exclusive leases, **admission control on** | **4,499/s per instance** at 4,500 offered, acquire p99 **1.97 ms**, 0.4 sheds/s |
| `Acquire` alone | **4,993/s per instance** at server-side p99 **4.32 ms**, peak 7,792/s |
| Report ingest | **44,437/s** accepted per instance with zero rejections; about 20,000/s applied to hot state inside the 200 ms budget |
| Config change to fleet awareness | p99 **46.1 ms** to wake every watcher, measured with 200 watchers; separately, about 10,000 concurrent long polls held per instance at 0.05 cores |

**Two replicas share one Redis, so extra replicas add server capacity, not acquire throughput.** Two
replicas serve about 4,000 cycles/s where one serves 4,499/s. Redis capacity is what raises the acquire
ceiling; replicas raise long-poll capacity, report processing and availability. Past that ceiling you
partition into separate deployments rather than scaling out.

Each instance bounds its own concurrency at Redis with **acquire admission control**
(`SPINNERET_ACQUIRE_FLEET_INFLIGHT`, 64 across the fleet by default, divided by the live instances each
one sees; `0` turns it off). Excess acquires are shed inside the server, before any Redis command is
issued, as `unavailable`/`overloaded` with a jittered retry hint. It costs a single replica nothing
(4,499 of 4,500 offered, 0.4 sheds/s); at capacity two replicas behave much the same gated or ungated,
for some tail (acquire p99 2.99 ms against 1.66 ms); and past capacity it converts overload into shedding
rather than timeouts. **Its behaviour well past the knee was not measured exhaustively** — the
performance page states what was deliberately left unmeasured and gives an A/B recipe for your own
hardware.

Candidates for v0.2, explicitly out of scope for v0.1: distributed global rate limiting, external
validator and refresher webhooks, proxy provider adapters, NATS JetStream, OIDC and TOTP, mTLS, staged
config rollouts, fingerprint distribution, browser pools.

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

Three things stay your responsibility in every deployment: the KEK that wraps the vault's data keys must
live outside the repository and outside the image; the console and the node API must sit behind TLS; and
node tokens must be scoped to one namespace, the sites they need and nothing else.
[Security hardening](documents/en/19-security.md) is the checklist to work through before anyone else can
reach the installation.

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
Python 3.9 or newer for the SDK.

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
builds and tests the console, tests the Python SDK on 3.9, 3.12 and 3.13, and builds the image.

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
