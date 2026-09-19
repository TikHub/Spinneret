# Contributing

**How to set up a Spinneret development environment, find your way around the repository, regenerate
code, run every test layer, and get a change through the quality gates. Read this before your first
pull request.**

[中文](../zh/20-contributing.md)

---

## Contents

- [Development environment](#development-environment)
- [Repository layout](#repository-layout)
- [The local development loop](#the-local-development-loop)
- [Ports](#ports)
- [Code generation](#code-generation)
- [Database migrations](#database-migrations)
- [Tests](#tests)
- [Makefile targets](#makefile-targets)
- [Quality gates](#quality-gates)
- [Code conventions](#code-conventions)
- [Documentation](#documentation)
- [Commits and pull requests](#commits-and-pull-requests)
- [Proposing a larger change](#proposing-a-larger-change)
- [Next](#next)

---

## Development environment

Spinneret is a Go server with an embedded React console, a Python SDK, a Go SDK and a Docker Compose
stack. You do not need all of it to make a change: a backend-only change needs Go and Docker, a
console-only change needs Node and a running server.

| Tool | Version | Needed for | Install |
| --- | --- | --- | --- |
| Go | 1.27.1 or newer (`go.mod`) | server, CLI, Go SDK, every Go test | <https://go.dev/dl/> |
| Docker + Compose v2 | any current release | test infrastructure, the stack, the end-to-end and load suites | Docker Desktop or Docker Engine |
| Node.js | 22.13 or newer (`web/package.json` → `engines`) | the console | <https://nodejs.org/> or a version manager |
| pnpm | 10.27.0 (`web/package.json` → `packageManager`) | the console | `corepack enable` picks up the pinned version |
| buf | v1.73.0 (pinned by CI) | regenerating protobuf code | `go install github.com/bufbuild/buf/cmd/buf@v1.73.0` |
| protoc-gen-go | latest | generated Go messages | `go install google.golang.org/protobuf/cmd/protoc-gen-go@latest` |
| protoc-gen-connect-go | latest | generated Connect handlers and clients | `go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest` |
| sqlc | v1.31.1 (pinned by CI) | regenerating database query code | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` |
| golangci-lint | v2.13.2 (pinned by CI) | the Go lint gate | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` |
| Python | 3.9 or newer (CI tests 3.9, 3.12 and 3.13) | the Python SDK, the example node, the drill scripts | your platform's Python |
| Chromium for Playwright | matched to `@playwright/test` 1.63.0 | the console journeys | `cd web && pnpm exec playwright install chromium` |
| k6 | — | the load suite | not installed on the host: it runs from the `grafana/k6` image through the Compose `loadtest` profile |

**Note.** There is no goose CLI to install. Migrations are ordinary goose-format SQL files compiled
into the binaries (`github.com/pressly/goose/v3` is used as a library), and `spnr migrate` applies
them. See [Database migrations](#database-migrations).

Everything in one go:

```bash
# Go toolchain
go version                      # must report go1.27.1 or newer

# Code generation and lint tools, at the versions CI pins
go install github.com/bufbuild/buf/cmd/buf@v1.73.0
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
export PATH="$(go env GOPATH)/bin:$PATH"

# Console
corepack enable
cd web && pnpm install --frozen-lockfile && cd ..

# Python SDK
cd sdk/python
python3 -m venv .venv && . .venv/bin/activate
pip install -e '.[dev]'
cd ../..
```

The `Makefile` puts `$(go env GOPATH)/bin` on `PATH` for every target, and `scripts/buf-generate.sh`,
`scripts/sqlc-generate.sh` and `web/scripts/gen.mjs` each prepend `$HOME/go/bin` themselves, so the
`go install`ed tools are found even from a shell that does not export it. Any pip-compatible
installer works for the Python SDK; `pip` is what CI uses.

---

## Repository layout

```text
Spinneret/
├── cmd/
│   ├── spinneret-server/    server entrypoint (flags, signals, role selection)
│   ├── spnr/                admin CLI (cobra): migrate, admin, token, rebuild, kek, seed, healthcheck, version, config
│   └── internal/buildinfo/  version and build metadata shared by both binaries
├── proto/spinneret/v1/      the API source of truth (.proto, package spinneret.v1); the comments are normative
├── gen/go/spinneret/v1/     generated Go messages and Connect handlers/clients — committed, never hand-edited
├── internal/                all server code (see the table below)
├── web/                     React console; built into web/dist and embedded in the server by web/embed.go
├── sdk/go/spinneret/        Go SDK, in the main module
├── sdk/python/              Python SDK (httpx + pydantic v2), its own package with pyproject.toml
├── examples/fastapi-crawler/  runnable example node using the Python SDK
├── deploy/compose/          the Compose stack, its overlays, .env.example and service config
├── deploy/docker/           `Dockerfile` (multi-stage: console → binaries → distroless) and `mocktarget.Dockerfile` for the mock target site and proxy
├── install/                 the one-click installer
├── scripts/                 developer scripts: code generation locks, compose init, drills, example quickstart
├── test/                    everything that is not a package-local test (see below)
├── tools/deps.go            build-tag-gated imports that pin tool dependencies in go.mod
├── documents/               the published documentation: en/, zh/ and images/
├── CONTRIBUTING.md          the short version of this page, bilingual; the repository front page links to it
├── SECURITY.md              the supported versions and how to report a vulnerability privately
├── CHANGELOG.md             Keep a Changelog format; a user-visible change appends to its [Unreleased] section
├── Makefile                 every developer task (`make help` lists them)
├── buf.yaml / buf.gen.yaml  the buf workspace and the Go generation template
├── sqlc.yaml                the root sqlc configuration (there is one per package that has queries)
├── .golangci.yml            the lint configuration
└── .github/                 the CI and release workflows, the pull-request template and the issue templates
```

`CONTRIBUTING.md` at the repository root is the short version of this page — the loop, the house rules
and the commit conventions on one screen — and it links here for the rest. `SECURITY.md` is the
reporting process for a vulnerability. Both hold the English and the Chinese text in a single file.

`docs/` also exists in a working tree. It holds local design notes and is excluded by `.gitignore`;
it is not part of the published project, and nothing in `documents/` may link into it.

### `internal/`

| Package | What lives there |
| --- | --- |
| `appconfig` | loads and validates every `SPINNERET_*` variable and its default |
| `apperr` | typed application errors carrying a Connect code, a machine-readable reason and a retry hint |
| `authz` | permissions, roles, scopes, principals and resource-scoped checks — pure, no I/O |
| `auth` | API token verification, console users (Argon2id), sessions, login throttling, CSRF |
| `tenancy` | tenants and namespaces, deletion constraints, default policy bootstrap |
| `pkg/` | small shared helpers: `admit` (the admission gate: a bounded concurrency limit with a FIFO wait room), `idgen`, `durationx`, `glob`, `netx`, `textdiff` |
| `observability` | `slog` logging, every Prometheus metric vector, OpenTelemetry tracing |
| `store/postgres` | pgx pool, embedded goose migrations, transactions, partition manager, sqlc output |
| `store/redis` | rueidis client factory, the key builder, the Lua script loader and its shared prelude |
| `store/clickhouse` | ClickHouse client, schema migration and the batch writer |
| `vault` | envelope encryption: KEKs, DEKs, the DEK cache, the secrets service, the rewrap job |
| `events` | the cluster event bus: in-process fan-out plus Redis Pub/Sub |
| `audit` | the buffered, batched audit log writer |
| `catalog` | the immutable per-namespace snapshot the hot paths read on every request |
| `site` / `sitesvc` | sites, endpoint groups, URI rules and the URI matcher; their PostgreSQL service |
| `identity` / `identitysvc` | identity types, payloads, delivery rendering, import, accounts; their service |
| `proxy` | the proxy pool: import, bindings, URL rendering, health checking |
| `policy` / `policysvc` | the four policy kinds, their YAML, validation and resolution; drafts, versions, bindings |
| `hotstate` | the Go side of the Redis hot state: materialize, sync, rebuild, snapshots |
| `scheduler` | the lease hot path — Acquire, AcquireBatch, Renew, Release and the lease reaper |
| `peers` | the registry of live API instances, kept by heartbeat in Redis; the acquire admission gate divides the fleet-wide concurrency budget by this count |
| `signal` | report ingestion: validation, authorization, de-duplication, stream append |
| `worker` | stream shard ownership, the report consumers, statistics and event writers |
| `action` | the action executor, the lifecycle state writer, manual and bulk operations |
| `breaker` | circuit breaker window evaluation, the state machine and manual operations |
| `configcenter` | config items, drafts, versions, publish and rollback, the watch hub, secret references |
| `notify` | notification channels, alert evaluation and asynchronous delivery |
| `analytics` | the dashboard queries over PostgreSQL, Redis and ClickHouse |
| `stats` | in-memory lease and report aggregation and its flush to PostgreSQL and ClickHouse |
| `jobs` | the periodic job runner and PostgreSQL advisory-lock leader election |
| `api` | one package per service with the Connect handlers, plus the shared interceptors |
| `server` | assembly: infrastructure clients, services, background loops, the HTTP server, the embedded console |
| `testutil` | shared integration fixtures for PostgreSQL, Redis and ClickHouse |
| `version` | build metadata injected at link time |

### `test/`

| Directory | What it is |
| --- | --- |
| `test/e2e/` | Go end-to-end scenarios against a deployed stack, behind the `e2e` build tag |
| `test/contract/` | wire-contract tests for the node API and admin API validation |
| `test/mocktarget/` | the mock target site and mock HTTP proxy used by the e2e, load and example runs |
| `test/load/` | the k6 scenarios, the metric snapshot tooling and the tuning script |
| `test/perf/` | Redis-side micro-benchmarks of the hot path, behind the `perf` build tag |

---

## The local development loop

### 1. Start the infrastructure

```bash
make infra-up
```

This runs `deploy/compose/docker-compose.infra.yml` as the Compose project `spinneret-infra`:
PostgreSQL, Valkey and ClickHouse only, on non-default ports so they never collide with a full stack
on the same machine. The PostgreSQL container keeps its data in `tmpfs` with `fsync=off` — it is for
development and tests, never for anything you want to keep. `make infra-down` removes it with its
volumes.

### 2. Run the server from source

```bash
export SPINNERET_DATABASE_URL='postgres://spinneret:spinneret@localhost:45432/spinneret?sslmode=disable'
export SPINNERET_REDIS_URL='redis://localhost:46379/0'
export SPINNERET_CLICKHOUSE_URL='clickhouse://spinneret:spinneret@localhost:49000/default'
export SPINNERET_KEKS="k1:$(openssl rand -base64 32)"
export SPINNERET_KEK_CURRENT=k1
export SPINNERET_LOG_FORMAT=text

go run ./cmd/spnr migrate up
printf '%s\n' 'change-me' | go run ./cmd/spnr admin init --username admin --password-stdin
go run ./cmd/spinneret-server
```

The server listens on `SPINNERET_HTTP_ADDR` (default `:8080`) and serves the Connect APIs, `/healthz`,
`/readyz`, the event stream and — if `web/dist` has been built — the console. Every variable is
documented in [Configuration reference](./03-configuration.md); every CLI command in
[CLI reference](./15-cli.md).

**Note.** Without `cd web && pnpm build` the binary embeds only a placeholder, so `http://localhost:8080`
serves no console. That is fine when you work on the backend: use the Vite dev server instead.

### 3. Run the console dev server

```bash
cd web
pnpm install --frozen-lockfile     # first time, or after a dependency change
pnpm dev
```

Vite listens on `http://localhost:5173` and proxies `/spinneret.v1.*`, `/api`, `/healthz` and
`/readyz` to `SPINNERET_API_URL` (default `http://localhost:8080`), so the dev server works against
your local `go run` server or against any deployed stack.

### 4. Or run the whole stack

```bash
./scripts/compose-init.sh    # writes deploy/compose/.env and a KEK file, both idempotent
make up                      # docker compose up -d --build --wait
```

This is the same stack the documentation deploys: two server replicas behind a load balancer,
PostgreSQL, Valkey and ClickHouse. It is what the Go end-to-end suite, the Playwright journeys, the
failover drill and the load scenarios run against. See
[Installation and deployment](./02-installation.md) for the services, the profiles and the volumes.

---

## Ports

| Port | What |
| --- | --- |
| `8080` | the server (`SPINNERET_HTTP_ADDR`, default `:8080`); in the Compose stack the load balancer publishes `SPINNERET_PORT`, default `8080` |
| `5173` | the Vite dev server (`pnpm dev`) |
| `45432` | infra PostgreSQL (`make infra-up`) |
| `46379` | infra Valkey |
| `49000` / `48123` | infra ClickHouse, native protocol / HTTP |
| `9090` | Prometheus in the stack's `observability` profile (`PROMETHEUS_PORT`) |
| `19090` / `19091` | the mock target site / the mock HTTP proxy (`MOCK_TARGET_PORT`, `MOCK_PROXY_PORT`) |
| `18000` | the example crawler (`EXAMPLE_PORT`) |

The infra ports deliberately differ from the stack ports so both can run at once.

---

## Code generation

Three kinds of code in this repository are generated and **committed**. A change that touches an
input must include the regenerated output in the same commit. CI verifies two of the three — the Go
protobuf output and the sqlc output, with `git diff --exit-code -- gen internal`. Nothing in CI runs
`pnpm gen` or looks at `web/src/gen`, so stale console protobuf code merges silently: keeping it in
step with the `.proto` files is the author's responsibility, not a gate's.

### Protobuf → Go

`proto/spinneret/v1/*.proto` is the source of truth of the wire API. After editing a `.proto`:

```bash
make proto          # = ./scripts/buf-generate.sh  (buf lint, then buf generate)
```

The output is `gen/go/spinneret/v1/*.pb.go` (messages) and
`gen/go/spinneret/v1/spinneretv1connect/*.connect.go` (handlers and clients), configured by
`buf.gen.yaml`. Validation rules are written with protovalidate (`buf.validate`) and the dependency
is resolved from `buf.lock`. The script takes a lock directory so two concurrent runs cannot
interleave.

### Protobuf → TypeScript

The console has its own generated client code, which is **not** produced by `make proto`:

```bash
cd web && pnpm gen
```

This runs buf with `web/buf.gen.yaml` and writes protobuf-es code into `web/src/gen`, using the same
lock directory as the Go generator. Regenerate it in the same commit as the `.proto` change.

### SQL → Go

Queries live in `<package>/queries/*.sql` and the schema sqlc reads is
`internal/store/postgres/migrations`. There is one `sqlc.yaml` at the repository root and one in each
package that owns queries (`internal/auth`, `internal/proxy`, `internal/policysvc`, and so on — sixteen
in total), each with its own output directory: the root one writes `internal/store/postgres/db`,
`internal/auth/sqlc.yaml` writes `internal/auth/authdb`, and so on.

**`make sqlc` regenerates only one of the sixteen.** `scripts/sqlc-generate.sh` runs a bare
`sqlc generate` from the repository root, which reads the root `sqlc.yaml` and nothing else, so it
covers `internal/store/postgres/db` alone. CI loops over every config instead. After editing a query
file, or a migration that changes a column a query selects, run the loop CI runs:

```bash
# Only internal/store/postgres/db — the root sqlc.yaml
make sqlc           # = ./scripts/sqlc-generate.sh

# All sixteen — what CI does, and what you want whenever you touched a package's queries
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
```

Each `sqlc.yaml` pins the shape of its output: `pgx/v5`, JSON tags, empty slices instead of nil,
pointers for nullable columns, `timestamptz` as `time.Time`, `jsonb` as `json.RawMessage`. Only the
package name differs — `db` at the root, `<area>db` per package.

`make generate` runs the protobuf step and `make sqlc`, so it inherits the same one-of-sixteen scope;
`make all` runs generation and then builds both binaries.

### Verifying

```bash
make proto
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
(cd web && pnpm gen)
git diff --exit-code -- gen internal web/src/gen
```

CI performs the `gen internal` half of this check itself, with the same loop. Running `make generate`
in its place is not equivalent: fifteen of the sixteen sqlc outputs go unregenerated, so the local
check passes and CI's `git diff --exit-code -- gen internal` fails.

---

## Database migrations

Migrations are goose-format SQL files in `internal/store/postgres/migrations`, embedded into the
binaries with `//go:embed migrations/*.sql`. There is no separate migration tool and no goose CLI:
`spnr migrate` drives them, serialized across processes by a PostgreSQL session advisory lock, so
several server instances may start at once.

| Command | What it does |
| --- | --- |
| `spnr migrate up` | applies every pending migration and prints the resulting version |
| `spnr migrate down` | rolls back the latest applied migration |
| `spnr migrate down --to N` | rolls back until the schema version equals `N` (`0` removes everything) |
| `spnr migrate status` | prints the applied version and the version embedded in the binary |

To add one:

1. Create `internal/store/postgres/migrations/00006_<short_name>.sql`, continuing the five-digit
   sequence. The current head is `00005_api_token_name_unique_active.sql`.
2. Write both directions. Every existing migration has an `-- +goose Up` and an `-- +goose Down`
   section, and `spnr migrate down` and the test fixtures rely on it. Wrap any statement that
   contains semicolons goose cannot split — a function body, a `DO` block — in
   `-- +goose StatementBegin` / `-- +goose StatementEnd`, as `00002_partitioned.sql` does.
3. Regenerate sqlc if the change touches a table a query reads, and commit the regenerated `db` and
   `<area>db` packages. Use the loop, not `make sqlc`:
   `for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done`.
4. Apply it locally against the infra database and run the Go tests: `internal/testutil` clones every
   test database from a freshly migrated template, so a broken migration fails a large part of the
   suite immediately.

**Warning.** Never edit a migration that has been released. It has been applied in deployments and
goose will not reapply it; the change would exist only on new installations. Add a new migration
instead.

---

## Tests

There are nine test layers. The first four rows below are four ways to run the same Go suite and are
what you run while working; the rest are the ones you run before touching the hot path, the console or
the deployment.

| Layer | Command | Requires | Roughly |
| --- | --- | --- | --- |
| Go, unit only | `make test-short` | nothing | 20 s |
| Go, unit + integration | `make infra-up` then `make test` | the infra stack | 30 s |
| Go, race detector | `make test-race` | the infra stack | 45 s |
| Go, coverage | `make cover` | the infra stack | like `make test`, plus the report |
| Console unit | `cd web && pnpm test` | Node and pnpm | 10 s (93 test files) |
| Python SDK | `make python-test` | the SDK venv, activated | 5 s (375 tests) |
| Example node | `make example-test` | `examples/fastapi-crawler/.venv` | seconds |
| Compose end-to-end | `make e2e` | Docker | several minutes, plus the first image build |
| Failover drill | `make e2e-failover` | the running stack | about 2 minutes |
| Console journeys | `make e2e-web` | the running stack, Chromium | on the order of 10 minutes (31 tests, 15 files, serial) |
| k6 load | `make load` or `test/load/run.sh` | the running stack and seeded data | as long as the scenario's `DURATION` |
| Redis micro-benchmarks | `go test -tags perf …` | a reachable Valkey | tens of minutes for a full `-bench .` |

The timings are from a developer laptop with a warm build cache and the infra stack already up.

### Go tests

`internal/testutil` gives every integration test an isolated PostgreSQL database cloned from a
migrated template, a Redis key prefix and a ClickHouse database. Connection strings come from
`SPINNERET_TEST_DATABASE_URL`, `SPINNERET_TEST_REDIS_URL` and `SPINNERET_TEST_CLICKHOUSE_URL`, which
the `Makefile` exports with the infra stack's ports:

```text
postgres://spinneret:spinneret@localhost:45432/spinneret?sslmode=disable
redis://localhost:46379/0
clickhouse://spinneret:spinneret@localhost:49000/default
```

When a variable is unset the fixture starts a throwaway container with testcontainers-go, once per
test binary. Every fixture calls `t.Skip` under `-short`, which is why `make test-short` needs
nothing at all and is the right loop while you write code. Run `make test` or `make test-race` before
you push.

### Compose end-to-end suite

```bash
make e2e
```

builds the images, starts the stack with `deploy/compose/docker-compose.e2e.yml` on top (a 5 s proxy
health-check interval and the mock target) and runs `go test -tags e2e ./test/e2e/...` inside the
Compose network. Nothing is stubbed: leases are taken through the load balancer, requests really
travel through the mock HTTP proxy to the mock site, reports flow through the Redis streams into the
workers, and the assertions read the admin APIs, the Redis hot state and ClickHouse.

Each run creates its own namespace, site, identity types, identities, proxies, policies, token and
notification channel through the admin APIs, so runs never collide and no seed data is involved; a
successful run deletes everything it created. `SPINNERET_E2E_KEEP=1` keeps it, and a failed run always
keeps it. Every environment variable the suite reads is documented in `test/e2e/doc_test.go`; the
scenario list is in `test/e2e/README.md`. The crawler simulation alone runs for
`SPINNERET_E2E_CRAWL_DURATION` (60 s by default).

Two related drills use the same stack: `make e2e-failover` stops one replica under acquire/report load
and fails if the error rate or the report backlog exceeds its thresholds, and `make example` seeds an
example site and drives the FastAPI example node against it.

### Console tests

```bash
cd web
pnpm test                          # Vitest, jsdom, src/**/*.test.{ts,tsx}
pnpm typecheck && pnpm lint        # tsc -b --noEmit, ESLint
```

The Playwright journeys drive the real console against a running deployment:

```bash
make up                            # the stack must be running
make e2e-web                       # installs the matching Chromium, then runs the suite
make e2e-web ARGS='-g "sites"'     # one journey
make e2e-web ARGS='--headed'       # watch it
```

`make e2e-web` reads the administrator credentials from `deploy/compose/.env`; override
`SPINNERET_UI_URL` to point at another deployment, including the Vite dev server on port 5173. The
suite runs serially (`workers: 1`) because its specs share one namespace, and every spec creates
uniquely named resources and deletes them again, so it can run repeatedly against a long-lived
deployment. `web/e2e/README.md` lists what each spec covers. One of them, `screenshots.spec.ts`,
writes the images under `documents/images/`.

### Python SDK

```bash
cd sdk/python
. .venv/bin/activate
pytest -q --cov=spinneret
ruff check . && ruff format --check .
mypy src
```

`make python-test` runs `python3 -m pytest -q` from `sdk/python`, so activate the virtualenv first or
it will use a system interpreter that has no test dependencies. The tests use `respx` and
`httpx.MockTransport` and never touch the network. The SDK must keep working on Python 3.10.

### Load and performance

`test/load/` holds the k6 scenarios for the acquire/report cycle, report ingest and `WatchConfig` long
polls, plus `run.sh`, which brackets a scenario with server-side metric snapshots and writes
everything into `.loadtest/<name>/`. `test/perf/` holds Redis-side micro-benchmarks behind the `perf`
build tag, so `go build ./...` and `go test ./internal/...` never compile them:

```bash
export SPINNERET_TEST_REDIS_URL='redis://localhost:46379/0'
go test -tags perf -timeout 60m ./test/perf/ -run XXX -bench BenchmarkAcquire \
  -benchtime 1x -perf.ops 3000 -perf.slowlog=false
```

Both directories have a README that explains how to get a number that means something — warming the
site, settling between runs, and which instrument is authoritative. The measured results are in
[Performance and tuning](./17-performance.md). A change to the Lua hot path should come with a
before/after table from `test/perf`, measured back to back on an idle machine, because the
run-to-run drift of identical code is about ±8 %.

---

## Makefile targets

| Target | What it runs |
| --- | --- |
| `make help` | the default goal: every task, grouped, one line each |
| `make all` | `generate` then `build` |
| `make generate` | `proto` and `sqlc` |
| `make proto` | `./scripts/buf-generate.sh` — `buf lint` and `buf generate` |
| `make sqlc` | `./scripts/sqlc-generate.sh` — `sqlc generate` from the root, so only `internal/store/postgres/db` |
| `make build` | static `bin/spinneret-server` and `bin/spnr` with the version stamped in |
| `make test` | `go test -count=1 ./...` |
| `make test-short` | `go test -short -count=1 ./...` — integration fixtures skip themselves |
| `make test-race` | `go test -race -count=1 ./...` |
| `make cover` | coverage over `./internal/...` into `coverage.out`, printing the total |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run ./...` |
| `make fmt` | `gofmt -w` over every tracked `.go` file outside `gen/` |
| `make infra-up` / `make infra-down` | the PostgreSQL + Valkey + ClickHouse test stack, up (waiting for health) or down with its volumes |
| `make web-install` | `pnpm install --frozen-lockfile` in `web/` |
| `make web` | `pnpm build` in `web/` — required before the server can embed the console |
| `make web-test` | the console gate: `pnpm typecheck`, `pnpm lint`, `pnpm format:check` and `pnpm test` in `web/` |
| `make docker` | builds the image as `spinneret:$(VERSION)` and `spinneret:local` |
| `make up` / `make down` | the full Compose stack |
| `make e2e` | the Go end-to-end suite inside the Compose network |
| `make e2e-web` | the Playwright console suite (`ARGS=…` passes Playwright flags) |
| `make e2e-failover` | the replica failover drill |
| `make example` | seeds the example site and runs the FastAPI example node against the stack |
| `make example-test` | the example node's unit tests |
| `make load` | one k6 scenario through the `loadtest` profile |
| `make python-test` | the Python SDK tests |
| `make clean` | removes `bin`, `dist` and `coverage.out` |

`VERSION` defaults to `git describe --tags --always --dirty`; `GOBIN` defaults to
`$(go env GOPATH)/bin` and is prepended to `PATH`.

---

## Quality gates

`.github/workflows/ci.yml` runs on every pull request and on pushes to `main`. It has four jobs, and
a change merges only when all of them pass.

### `go`

Runs with PostgreSQL 17, Valkey 8 and ClickHouse 25.8 as service containers, with the
`SPINNERET_TEST_*` variables pointed at them.

| Step | Command |
| --- | --- |
| Generated code is up to date | installs `buf@v1.73.0`, `sqlc@v1.31.1`, `protoc-gen-go@latest`, `protoc-gen-connect-go@latest`, runs `buf lint && buf generate`, runs `sqlc generate` for every `sqlc.yaml` outside `web/`, then `git diff --exit-code -- gen internal` |
| Vet | `go vet ./...` |
| Lint | golangci-lint v2.13.2 with `--build-tags e2e --timeout 10m` |
| Test | `go test -race -count=1 -skip 'TestStart.*Container' ./...` |

The skipped tests are the three that exercise the testcontainers fallback in `internal/testutil`;
CI provides the services directly, so they have nothing to start. Note that the lint step includes
the `e2e` build tag, so `test/e2e` is linted too — run it that way locally.

### `web`

In `web/`, with Node 22 and pnpm: `pnpm install --frozen-lockfile`, then `pnpm typecheck`,
`pnpm lint`, `pnpm test` and `pnpm build`. `pnpm format:check` is not among them — `make web-test`
runs it, and the pull-request template asks you to run `make web-test` when the console changed, so
Prettier drift is caught by you rather than by CI.

### `python-sdk`

In `sdk/python/`, on Python 3.10, 3.12 and 3.13: `pip install -e '.[dev]'`, `pytest -q`,
`ruff check . && ruff format --check .`, and `mypy src` on 3.12 only.

### `image`

After `go` and `web` pass: builds `deploy/docker/Dockerfile` with buildx, tagged `spinneret:ci`,
without pushing.

Publishing is a separate workflow. `.github/workflows/release.yml` triggers on a `v*` tag, builds the
same Dockerfile for amd64 and arm64 and pushes it to `ghcr.io/tikhub/spinneret` and to `tikhubio/spinneret` on Docker Hub; it is
the only thing in the repository that publishes an image. Nothing on a pull request or on `main` pushes
anything.

It is one build pushed to both registries rather than two builds, so the same digest is served
everywhere and the registries cannot drift apart. Docker Hub is skipped, rather than failing the run,
when the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets are absent — which is what a fork sees. The
Docker Hub namespace is that username unless the repository variable `DOCKERHUB_REPOSITORY` overrides
it, for an account that pushes into an organisation namespace.

The workflow also runs from **Actions → release → Run workflow** with an existing tag, which builds and
publishes images for that tag without touching its GitHub release. That is how a registry added after a
release was cut gets the images it missed. Turn off the `latest` input when the tag is not the newest
release, or `:latest` will move backwards.

### What CI does not run

The Compose end-to-end suite, the failover drill, the Playwright journeys, the k6 load scenarios and
the `perf` benchmarks all need a deployed stack and are **not** in CI. Run the ones your change can
break, locally, and say so in the pull request.

### Before you push

```bash
make proto
for f in $(find . -name sqlc.yaml -not -path './web/*'); do sqlc generate -f "$f"; done
(cd web && pnpm gen) && git diff --exit-code -- gen internal web/src/gen
make vet
golangci-lint run --build-tags e2e ./...
make test-race
make web-test && (cd web && pnpm build)
(cd sdk/python && . .venv/bin/activate && ruff check . && ruff format --check . && mypy src && pytest -q)
```

---

## Code conventions

### Reading `spec §N` in a comment

Comments throughout `internal/` cite section numbers — `spec §6.6`, `design doc §8.5`. They refer to
the engineering specification Spinneret was built from, which is **not published**: it lives in the
repository owner's local `docs/` directory, which `.gitignore` excludes. Nothing in the published
tree depends on it, and you never need it to work on the code.

Treat those markers as what they are: a stable shorthand the authors used to keep a hundred Lua and
Go files consistent with one another. When you need the behaviour a marker refers to, the published
[documentation](../README.md) describes it — [Concepts](./04-concepts.md) for the model,
[Policies](./08-policies.md) for the rules, [Node API reference](./13-node-api.md) for the wire.
When you add a comment, prefer describing the invariant over citing a section number, and never add
a link to `docs/`.

### Everywhere

- **Comments, identifiers, commit messages and log messages are English.** The product is bilingual;
  the source is not. The only Chinese in the repository is user-facing text: the `zh-CN` console
  locale files, `documents/zh/`, and the `*.zh-CN.md` READMEs.
- **No brand or platform names.** Spinneret is site-agnostic. Examples use neutral placeholders —
  `example-site`, `search`, `detail`, `partner-api`. This applies to code, fixtures, seed data,
  tests and documentation alike.
- **Never log a secret.** Payload fields, proxy credentials, API tokens, session IDs and KEK material
  never reach a log line, a metric label, an error message or a test fixture dump.

### Go

- No package-level mutable globals, except metric registration and embedded files.
- Every exported function that does I/O takes `context.Context` as its first parameter.
- Wrap errors with `%w` and context: `fmt.Errorf("load site %s: %w", id, err)`. Errors a client will
  see go through `internal/apperr`, which carries the Connect code, the machine-readable reason
  returned in the `Spinneret-Reason` header and the retry hint. Adding a new reason means adding it
  to `apperr` and to the error table in [Troubleshooting](./18-troubleshooting.md) and
  [Node API reference](./13-node-api.md).
- Constructors take their dependencies explicitly. No service locator, no global registry. Interfaces
  are small and declared by the consumer, not by the implementation.
- Prefer immutable values. The catalog snapshot is the model: it is built once, never mutated, and
  replaced wholesale on invalidation, so a reader never needs a lock. Anything reachable from more
  than one goroutine is either immutable or explicitly synchronized.
- Keep files focused. No hand-written Go file in the repository exceeds about 700 lines; when one
  approaches that, split the package by concern instead of growing the file. Generated files
  (`gen/`, `*db/`) are exempt and are never edited.
- Tests are table-driven and use `testify/require`. Integration tests use `internal/testutil` and
  never share state between tests.
- `gofmt` and `goimports` are enforced by golangci-lint's formatters; `make fmt` fixes the whole tree.
  Beyond the standard linter set, `.golangci.yml` enables `bodyclose`, `errorlint`, `gosec`,
  `misspell`, `nilerr`, `noctx`, `rowserrcheck`, `sqlclosecheck`, `unconvert` and `wastedassign`.
  `gen/` and the generated `*db/` packages are excluded, `_test.go` files are exempt from `gosec`,
  `noctx` and `bodyclose`, and `test/` from `gosec` and `noctx`.

### Console

`web/README.md` holds the full conventions; the ones a first change trips over:

- Call the API only through the typed clients in `@/lib/clients`, inside React Query, and pass the
  active namespace name on every namespace-scoped request.
- Build query keys with `useScopedQueryKey()` so events and cache invalidation reach the right rows,
  and use `useScopedPlaceholder()` for paged lists so a scope switch never shows another tenant's
  data.
- Every user-visible string goes through i18next. A new key needs an entry in **both**
  `web/src/i18n/locales/en/<area>.json` and `web/src/i18n/locales/zh-CN/<area>.json`, in the same file
  and under the same path. The seventeen locale files exist in both languages and must stay
  structurally identical.
- Components are located in tests by role, label or accessible name. A control that cannot be reached
  that way is a defect in the component, not a reason for a CSS selector.

### Python SDK

Ruff with line length 100 and `target-version = "py310"`, `ruff format`, and `mypy` in strict mode over
`src` with the pydantic plugin. `pytest` runs with `filterwarnings = ["error"]`, so a new warning
fails the suite. The public surface must work unchanged on Python 3.10 through 3.13.

---

## Documentation

The published documentation is `documents/` plus the root `README.md` and `README.zh-CN.md`.
`docs/` is local working notes, is not tracked by git, and must never be linked from anything that
ships.

**The parity rule: `documents/en/NN-x.md` and `documents/zh/NN-x.md` change in the same commit.** The
two files are the same document in two languages — the same sections in the same order, the same
tables with the same rows, the same commands and the same examples. The Chinese page is not a
machine translation of the English one; command names, paths, environment variables, field names,
code and URLs stay untranslated inside it. The same rule applies to every `*.zh-CN.md` in the
repository: the root README, `sdk/go/README.md`, `sdk/python/README.md` and the example node's README.

A change that alters behaviour updates the documentation in the same pull request. In particular:

| If you change | Also update |
| --- | --- |
| a `SPINNERET_*` variable or its default | [Configuration reference](./03-configuration.md) |
| a `.proto` message, field or RPC | [Node API reference](./13-node-api.md), and both SDKs |
| a `spnr` command or flag | [CLI reference](./15-cli.md) |
| an error reason in `apperr` | [Troubleshooting](./18-troubleshooting.md) and [Node API reference](./13-node-api.md) |
| a Prometheus metric | [Observability and alerting](./12-observability.md) |
| a Compose service, port or profile | [Installation and deployment](./02-installation.md) |
| a console page or its navigation entry | [Console overview](./05-console-overview.md) and the relevant feature page |
| a new concept or term | [Concepts](./04-concepts.md) and the glossary in [FAQ and glossary](./21-faq.md) |

Screenshots under `documents/images/` are produced by `web/e2e/screenshots.spec.ts` at 1440×900, not
taken by hand. Re-run that spec rather than cropping a new image.

---

## Commits and pull requests

Commit subjects follow conventional commits:

```text
<type>: <imperative summary, lower case, no trailing period>

<body: why the change is needed and what it does, wrapped at 72 columns.
List the areas touched. Note migrations, new environment variables and any
behaviour an operator would notice.>
```

Types in use: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`. Keep the subject under
about 72 characters, and keep one commit to one concern — a refactor and the feature it enables are
two commits.

A pull request:

- targets `main` from a topic branch, and covers one subject;
- explains the problem first and the solution second;
- lists the gates you ran, and explicitly names the ones CI does not run if your change could break
  them (`make e2e`, `make e2e-web`, `make e2e-failover`, `make load`, the `perf` benchmarks);
- includes the regenerated `gen/`, `internal/**/db/` and `web/src/gen` output when an input changed;
- includes the matching `documents/en/` and `documents/zh/` updates;
- adds an entry under `## [Unreleased]` in `CHANGELOG.md` whenever an operator or a node can see the
  change — a new variable, a new RPC, a changed default, a fixed bug. The file follows Keep a
  Changelog, so the entry goes under `Added`, `Changed`, `Fixed` or `Removed`;
- calls out a new migration, a new `SPINNERET_*` variable, a new permission or a changed default in
  its own paragraph, because those are the things an operator has to act on;
- attaches before/after screenshots for a visible console change, and a before/after benchmark table
  for a hot-path change.

`.github/pull_request_template.md` fills the description in for you, and its three sections map onto
the bullets above: *What and why* is the problem then the solution, *How it was verified* is the gate
list, and *Checklist* is the bilingual documentation, the changelog entry and the regenerated code.
Leave a box unticked rather than ticking one you did not run — an honest gap is reviewable, a wrong
tick is not.

Contributions are made under the repository's Apache 2.0 licence (`LICENSE`).

---

## Proposing a larger change

Anything that changes the data model, the wire API, the hot path or the security model starts with an
issue, not a pull request. `.github/ISSUE_TEMPLATE/feature_request.yml` is the form to open; it asks
for the same things in the same order. Describe:

1. **The problem**, in terms of what an operator or a node cannot do today.
2. **The model change** — the new entity, policy kind, RPC or permission — and how it fits the
   existing vocabulary in [Concepts](./04-concepts.md).
3. **The wire impact.** `buf.yaml` declares `breaking: use: FILE`: the API is expected to stay
   backwards compatible. Adding a field or an RPC is fine; renaming, renumbering or removing one is
   not, and neither is changing the meaning of an existing value. Nodes in the field run older SDKs.
4. **The schema impact** — the migration, whether it can run online, and what happens to a deployment
   that rolls back.
5. **The hot-path impact.** A change to `internal/scheduler`, `internal/worker` or any `*.lua` script
   is measured, not argued: name the `test/perf` blocks you will report and the load scenario you will
   run. [Performance and tuning](./17-performance.md) sets the baseline.
6. **The security impact** — any new way a credential, a secret or a payload can leave the system.
   See [Security](./19-security.md).

Features that were deliberately left out of the current release are listed at the end of the root
`README.md`; check it before proposing one, and say what changed if you think it should move.

Security vulnerabilities do not go in an issue. Report them the way
[Security](./19-security.md) describes.

---

## Next

- [Concepts](./04-concepts.md) — the vocabulary the code uses, before you read the code
- [CLI reference](./15-cli.md) — every `spnr` command you will use while developing
- [Configuration reference](./03-configuration.md) — every variable the server reads
- [Node API reference](./13-node-api.md) — the wire contract the `.proto` files define
- [Performance and tuning](./17-performance.md) — the baseline any hot-path change is measured against
- [Installation and deployment](./02-installation.md) — the stack your tests run against
