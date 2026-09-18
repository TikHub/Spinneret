# Spinneret

[中文文档](README.zh-CN.md)

**Spinneret is a control plane for multi-node crawlers and API nodes.** It centrally manages identities
(cookies, device parameters, accounts), proxies, configuration and secrets, and turns the request results
your nodes report into cooldowns, bans, health scores and circuit breaking — automatically, in seconds,
without redeploying a single node.

A node needs exactly two things to start: a server URL and an API token. Everything else — which cookie to
use, through which proxy, whether an endpoint is currently circuit-broken, what the crawler's configuration
is — comes from Spinneret at request time.

```
Acquire(site, client, uri)  →  identity + credential + proxy + lease
  ... node sends the request to the target site ...
Report(lease_id, status, latency, markers)  →  server classifies, cools down, bans, trips breakers
```

Spinneret is **site-agnostic**: it contains no signing algorithms, no login flows and no captcha solving.
It manages the *state* around your requests; your node still makes them.

---

## Table of contents

- [Why](#why)
- [Features](#features)
- [Architecture](#architecture)
- [Screenshots](#screenshots)
- [Quickstart](#quickstart)
- [Node API at a glance](#node-api-at-a-glance)
- [SDKs](#sdks)
- [Documentation](#documentation)
- [Development](#development)
- [Project status](#project-status)

---

## Why

| Problem | What Spinneret does |
| --- | --- |
| Cookies, proxies and keys hardcoded in images and `.env` files | Images carry no configuration; nodes fetch credentials per request and configuration by long poll |
| Every node picks a cookie at random, the same identity is used concurrently | Server-side leases with reuse intervals, quotas and concurrency limits |
| No feedback loop — burnt identities keep being used | Reports drive cooldowns, bans, expiry and health scores, all evaluated server-side |
| No global view of what is failing | A console with per-site dashboards, an identity × endpoint-group heatmap, a request explorer and risk events |

---

## Features

The v0.1 scope follows the [design document](docs/design/0_first_doc.md); every module below is implemented.

| Module | What it gives you |
| --- | --- |
| **Identity scheduling** (§5, §6) | Identity types with typed payload fields and delivery templates; rotation policies (`weighted_random`, `least_recently_used`, `round_robin`, `best_health`), lease TTL and lifetime, reuse interval and anchor, quotas, sticky sessions, warm-up, probe weighting |
| **Proxy distribution** (§11) | Proxy pool with kinds/regions/providers/tags, assignment modes `none / pool / bind_identity / region_match`, session templates, periodic health checks, per-site proxy cooldowns |
| **Reporting & signal detection** (§7) | Batched, idempotent reports; configurable signal rules over status, business code, error kind, markers, URI, method, latency and size; 12 outcomes; cross attribution between identity and proxy |
| **Cooldowns & bans** (§8) | Cooldown / expire / quarantine / ban at identity-endpoint, identity-site, identity, account, proxy-site and proxy scope; exponential backoff with caps; escalation ladders; shadow mode; manual operations and bulk rollback (`RevertActions`) |
| **Health & lifecycle** (§9) | EWMA health score with time decay, per-endpoint low-score cooldowns, automatic quarantine, identity state machine (`pending → active → quarantined / banned / expired / disabled / retired`) |
| **Circuit breaking** (§10) | Per endpoint group sliding-window statistics, three-state breaker (closed / open / half-open) with probe leases, manual open/close, optional revert of cooldowns applied in the tripping window |
| **Config center** (§12) | Versioned config items with drafts, publish, rollback and diffs; long-poll `WatchConfig`; local snapshots in the SDKs; `${secret:path}` references; read-only `_runtime` group exposing breakers and site switches |
| **Vault** (§13) | AES-256-GCM envelope encryption (KEK → DEK → data), file or env KEK providers, online KEK rotation and rewrap, secret versions and expiry, every read audited |
| **Auth & tenancy** (§14) | Tenants → namespaces → sites; console users with roles `viewer / operator / admin / owner`, per-namespace and per-site bindings; node API tokens with fine-grained scopes; Argon2id passwords, login throttle, sessions, CSRF |
| **Web console** (§17) | 21 routes covering every module, English and Chinese, light and dark, live updates over SSE |
| **Notifications** (§18.3) | Webhook (HMAC-signed), Feishu, DingTalk, WeCom, Telegram; 11 alert kinds with de-duplication and per-site routing |
| **Observability** (§18.3) | `/healthz`, `/readyz`, Prometheus `/metrics`, optional OTLP tracing, ClickHouse-backed request explorer |

---

## Architecture

```mermaid
flowchart LR
    subgraph nodes["Crawler nodes"]
        PY["Python SDK"]
        GO["Go SDK"]
        HTTP["plain HTTP + JSON"]
    end

    subgraph plane["Spinneret (stateless, N replicas)"]
        API["API role<br/>LeaseService · ReportService<br/>ConfigService · SecretService<br/>Admin services · Console"]
        WRK["Worker role<br/>report pipeline · breakers<br/>lease reaper · jobs"]
    end

    subgraph data["Data layer"]
        RD[("Redis / Valkey<br/>hot state, leases,<br/>report streams, sessions")]
        PG[("PostgreSQL<br/>source of truth<br/>+ aggregates, audit")]
        CH[("ClickHouse<br/>raw request events<br/>optional")]
    end

    PY & GO & HTTP -->|Connect / gRPC / JSON| LB["Load balancer"]
    LB --> API
    API -->|"Lua: filter, select, lease"| RD
    API -->|"enqueue report"| RD
    RD -->|"stream shards"| WRK
    WRK -->|"classify → act → observe"| RD
    WRK --> PG
    WRK --> CH
    API <--> PG
    API -.->|"pub/sub: catalog, config, runtime"| RD
    WRK -->|alerts| NOTIFY["Webhook · Feishu<br/>DingTalk · WeCom · Telegram"]
    BROWSER["Operator browser"] --> LB
```

**Hot path.** `Acquire` runs one Lua script against Redis: it checks the breaker, samples candidates,
filters on availability (cooldown, reuse interval, quota, concurrency) and writes the lease — no
PostgreSQL round trip. `Report` validates, de-duplicates and appends to a Redis stream shard derived from
the lease ID, then returns. Workers own stream shards, classify each report with the signal policy, apply
the action policy, update the hot state and persist aggregates, state events and risk events.

**State layers.** PostgreSQL is the source of truth (catalog, identities, policies, secrets, audit).
Redis holds the derived hot state and can be rebuilt from PostgreSQL at any time (`spnr rebuild`).
ClickHouse is optional and stores raw request events for the request explorer.

**Roles.** One binary, `SPINNERET_ROLE=all|api|worker`. `all` is the default and what Compose runs;
split the roles when you want to scale request serving and report processing independently.

---

## Screenshots

| | |
| --- | --- |
| ![Overview](docs/images/overview.png) | ![Identities](docs/images/identities.png) |
| Overview: per-site health, QPS, outcome mix, breakers | Identities: state, health score, cooldowns, filters |
| ![Heatmap](docs/images/heatmap.png) | ![Policies](docs/images/policies.png) |
| Heatmap: identity × endpoint group availability | Policies: YAML editor, versions, diff, publish |
| ![Breakers](docs/images/breakers.png) | ![Requests](docs/images/requests.png) |
| Breakers: state, windows, manual open/close, site switches | Request explorer: every report with outcome and blame |

The console is bilingual — the same overview in Chinese: [`docs/images/overview-zh.png`](docs/images/overview-zh.png).

---

## Quickstart

Requirements: Docker 24+ with the Compose plugin, ~4 GB free RAM, ports 8080 (console/API) free.

```bash
git clone https://github.com/Evil0ctal/Spinneret.git
cd Spinneret

# 1. Generate deploy/compose/.env (random passwords) and deploy/compose/secrets/kek.key
./scripts/compose-init.sh

# 2. Build the image and start Postgres, Valkey, ClickHouse, migrations, 2 server replicas and the LB
docker compose -f deploy/compose/docker-compose.yml up -d --build --wait

# 3. Create the first administrator, tenant and namespace (idempotent)
docker compose -f deploy/compose/docker-compose.yml --profile init run --rm init-admin
```

Open <http://localhost:8080> and sign in as `admin`; the password is in `deploy/compose/.env`:

```bash
grep SPINNERET_ADMIN_PASSWORD deploy/compose/.env
```

### Run the example crawler

A complete FastAPI node that leases, requests and reports against a built-in mock target site:

```bash
./scripts/example-quickstart.sh        # or: make example
```

The script is idempotent. It seeds the site `example` (2 endpoint groups, 20 identities, 2 mock proxies,
published rotation/signal policies, a config item), creates a node token, starts the `example-crawler`
service on <http://localhost:18000> and calls it:

```bash
curl 'http://localhost:18000/crawl/search?q=shoes'
curl  http://localhost:18000/crawl/item/42
curl  http://localhost:18000/config
```

Now make the target misbehave and watch Spinneret react in the console (Identities, Breakers, Requests):

```bash
curl -X PUT localhost:19090/_admin/rules -d '[{"prefix":"/site/search","mode":"rate_limit"}]'
for i in $(seq 1 60); do curl -s -o /dev/null 'http://localhost:18000/crawl/search?q=x'; done
curl -X DELETE localhost:19090/_admin/rules
```

See [`examples/fastapi-crawler/README.md`](examples/fastapi-crawler/README.md) and
[`scripts/example-quickstart.sh --reset`](scripts/example-quickstart.sh) to undo it.

---

## Node API at a glance

Every RPC is `POST /spinneret.v1.<Service>/<Method>` with `Content-Type: application/json` — Connect, gRPC
and gRPC-Web work too. Field names are snake_case, timestamps are RFC 3339 UTC.

Create a token (scopes are per site and per config group):

```bash
docker compose -f deploy/compose/docker-compose.yml exec spinneret \
  spnr token create --tenant default --namespace default --name crawler-hk \
    --scope lease:acquire:example --scope report:write:example \
    --scope config:read:crawler --expires 720h
# spn_EXAMPLEtokenEXAMPLEtokenEXAMPLEtoken1234567
```

**Acquire** an identity and a proxy for the URI you are about to fetch:

```bash
curl -s http://localhost:8080/spinneret.v1.LeaseService/Acquire \
  -H "Authorization: Bearer $SPINNERET_TOKEN" \
  -H "X-Spinneret-Node: crawler-hk-03" \
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
    "headers": { "User-Agent": "ExampleCrawler/17" },
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

Errors carry a machine-readable reason and a retry hint in response headers:

```http
HTTP/1.1 429 Too Many Requests
Spinneret-Reason: no_identity_available
Spinneret-Retry-After-Ms: 59367

{"code":"resource_exhausted","message":"no identity available for example/web/search"}
```

The full reference — every node service, error reason and retry rule — is in [`docs/api.md`](docs/api.md).

---

## SDKs

| SDK | Package | Highlights |
| --- | --- | --- |
| **Python** — [`sdk/python`](sdk/python/README.md) | `pip install spinneret` | Sync `Client` and asyncio `AsyncClient` on httpx, pydantic v2 models, `with client.lease(...)` merging credentials and proxy into `httpx` arguments, background batching reporter, config watcher with encrypted local snapshots |
| **Go** — [`sdk/go`](sdk/go/README.md) | `github.com/Evil0ctal/Spinneret/sdk/go/spinneret` | Connect JSON or gRPC, typed errors (`IsCircuitOpen`, `IsNoIdentity`, …), `Lease.Apply(req)` / `Lease.Transport(base)`, batching `Reporter`, `ConfigWatcher` with snapshots |

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

No SDK for your language? Plain HTTP + JSON is a first-class client — see [`docs/api.md`](docs/api.md).

---

## Documentation

| Document | Contents |
| --- | --- |
| [`docs/deployment.md`](docs/deployment.md) · [中文](docs/deployment.zh-CN.md) | Requirements, Compose deployment, every `SPINNERET_*` variable, KEK management, TLS and reverse proxies, scaling, upgrades, backups, hardening, troubleshooting |
| [`docs/operations.md`](docs/operations.md) · [中文](docs/operations.zh-CN.md) | Day-2 runbook: tenants, users and roles, tokens, sites and endpoint groups, identity types and imports, policy YAML for all four kinds, breakers, rollback, proxies, config and secrets, notifications, monitoring, hot-state rebuild, KEK rotation, retention |
| [`docs/api.md`](docs/api.md) · [中文](docs/api.zh-CN.md) | Node API reference with request/response JSON, error reason table, retry guidance, admin API overview |
| [`docs/benchmarks.md`](docs/benchmarks.md) · [中文](docs/benchmarks.zh-CN.md) | Measured performance against the v0.1 targets: method, results per scenario, where the time goes, Redis sizing, tuning recommendations |
| [`proto/README.md`](proto/README.md) | Wire conventions, JSON mapping, headers, service list |
| [`sdk/python/README.md`](sdk/python/README.md) · [中文](sdk/python/README.zh-CN.md) | Python SDK reference |
| [`sdk/go/README.md`](sdk/go/README.md) · [中文](sdk/go/README.zh-CN.md) | Go SDK reference |
| [`examples/fastapi-crawler/README.md`](examples/fastapi-crawler/README.md) · [中文](examples/fastapi-crawler/README.zh-CN.md) | Example node, endpoint by endpoint |
| [`web/README.md`](web/README.md) | Console development |
| [`test/load/README.md`](test/load/README.md) | k6 load scenarios, the metric snapshot tooling and their targets |
| [`docs/design/`](docs/design/) | Design document (Chinese) and the engineering specifications it was built from |
| [`CHANGELOG.md`](CHANGELOG.md) | Release notes |

---

## Development

Requirements: Go 1.27+, Node 22 + pnpm 10, Docker (for the integration test infrastructure), Python 3.9+ for
the SDK.

```bash
export PATH="$(go env GOPATH)/bin:$PATH"

make infra-up          # PostgreSQL, Valkey and ClickHouse for tests on ports 45432 / 46379 / 49000
make test              # go test ./...            (integration tests use the infra above)
make test-race         # go test -race ./...
make lint vet fmt      # golangci-lint, go vet, gofmt
make generate          # buf generate + sqlc generate (commit the result)

make web-install web   # pnpm install + pnpm build (the console is embedded via go:embed)
make build             # bin/spinneret-server and bin/spnr
make docker up down    # build the image, start / stop the Compose stack
```

Test suites:

| Command | What it runs |
| --- | --- |
| `make test` | Go unit and integration tests |
| `make e2e` | Go end-to-end scenarios inside the Compose network (`test/e2e`, build tag `e2e`) |
| `make e2e-failover` | Replica failover drill under acquire/report load |
| `make e2e-web` | Playwright suite driving the real console (`web/e2e`) |
| `cd web && pnpm test` | Console unit tests (Vitest) |
| `make python-test` | Python SDK tests |
| `make example-test` | Example crawler tests |
| `make load` | k6 load scenarios (profile `loadtest`) |

`.github/workflows/ci.yml` runs the Go suite with `-race`, checks that generated code is up to date, builds
and tests the console, tests the Python SDK on 3.9 / 3.12 / 3.13 and builds the image.

The `spnr` CLI administers a deployment (migrations, first administrator, tokens, hot-state rebuild, KEK,
load-test seed data) and reads the same `SPINNERET_*` environment as the server:

```bash
spnr migrate up|down|status      spnr admin init --username admin --password-stdin
spnr token create --name ...     spnr rebuild [--site ...]
spnr kek generate|status|rewrap  spnr seed --site loadtest --identities 100000
spnr config check                spnr healthcheck --url http://127.0.0.1:8080/readyz
```

---

## Project status

Spinneret v0.1 is feature-complete against its design document: the four milestones (core path, risk-control
loop, infrastructure, console and release) are implemented, and the stack ships with Go end-to-end scenarios,
a failover drill, a Playwright console suite and k6 load scenarios.

**Every §18.4 performance target is met on one server instance and one Redis instance.** Measured on the
design's own test condition (one site, 100,000 identities, 50 endpoint groups) after the Lua hot-path
optimizations:

* Acquire alone: **4,993/s per instance at server-side p99 4.32 ms** (peak 7,792/s), against a target of
  5,000/s at p99 < 5 ms.
* The full acquire→report cycle, which is five Lua scripts: **5,500/s per instance** with
  `max_concurrent_leases: 4`, or **4,500/s** with the exclusive leases the load-test seed configures — up
  from 3,000/s and 2,000/s.
* Report ingest **44,437/s** accepted per instance with zero rejections (target 20,000/s), of which
  **19,761/s** are applied to the hot state within p99 30.7 ms (target p99 < 200 ms).
* One acquire→report cycle costs **168.3 µs of Redis CPU**, down from 271.2 µs: **5,940 cycles/s per Redis
  thread** instead of 3,690.

The remaining bottleneck is not Redis CPU. Two replicas behind the load balancer reach 3,000 cycles/s in
aggregate — *less* than one replica alone — because each instance drives its own unbounded concurrency at
the shared Redis, and a failing acquire costs about five times a succeeding one, so contention amplifies
into congestion collapse instead of degrading gracefully. The next levers are per-instance admission
control on the Acquire path, making a rejected candidate cheap, and then Redis Cluster.
[`docs/benchmarks.md`](docs/benchmarks.md) has the full before/after tables, the per-script costs and the
Valkey settings the numbers depend on.

Candidates for v0.2 (explicitly out of scope for v0.1): distributed global rate limiting, external validator
and refresher webhooks, proxy provider adapters, NATS JetStream, OIDC and TOTP, mTLS, staged config rollouts,
fingerprint distribution, browser pools.

**License:** none yet — no license file is published with this repository, so all rights are reserved by the
authors until one is added. Ask before redistributing.
