# Spinneret developer tasks. Tools are expected in $(GOBIN) (go install) and on PATH.
SHELL := /usr/bin/env bash
GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/TikHub/Spinneret/internal/version.Version=$(VERSION)

INFRA_COMPOSE := deploy/compose/docker-compose.infra.yml
STACK_COMPOSE := deploy/compose/docker-compose.yml
E2E_COMPOSE := docker compose -f $(STACK_COMPOSE) -f deploy/compose/docker-compose.e2e.yml --profile test

export SPINNERET_TEST_DATABASE_URL ?= postgres://spinneret:spinneret@localhost:45432/spinneret?sslmode=disable
export SPINNERET_TEST_REDIS_URL ?= redis://localhost:46379/0
export SPINNERET_TEST_CLICKHOUSE_URL ?= clickhouse://spinneret:spinneret@localhost:49000/default

.PHONY: help all generate proto sqlc build test test-short test-race cover lint vet fmt infra-up infra-down \
        web web-install web-test docker up down e2e e2e-web e2e-failover example example-test load python-test clean

# Printed by `make` with no target: the tasks a contributor needs, grouped, with one line each.
.DEFAULT_GOAL := help

help:
	@printf 'Spinneret developer tasks\n\n'
	@printf '  Build and generate\n'
	@printf '    generate       regenerate protobuf (buf) and query (sqlc) code\n'
	@printf '    build          build bin/spinneret-server and bin/spnr\n'
	@printf '    docker         build the server container image\n\n'
	@printf '  Test\n'
	@printf '    infra-up       start PostgreSQL, Valkey and ClickHouse for the test suite\n'
	@printf '    infra-down     stop them and delete their volumes\n'
	@printf '    test           Go tests\n'
	@printf '    test-race      Go tests with the race detector\n'
	@printf '    test-short     Go tests that need no infrastructure\n'
	@printf '    cover          Go coverage of ./internal/...\n'
	@printf '    web-test       console typecheck, lint, format check and unit tests\n'
	@printf '    python-test    Python SDK tests\n'
	@printf '    e2e            end-to-end suite inside the compose network\n'
	@printf '    e2e-web        Playwright console journeys against the running stack\n'
	@printf '    e2e-failover   replica failover drill under load\n'
	@printf '    load           k6 load suite against the running stack\n\n'
	@printf '  Quality\n'
	@printf '    lint           golangci-lint\n'
	@printf '    vet            go vet\n'
	@printf '    fmt            gofmt the tracked Go files\n\n'
	@printf '  Run\n'
	@printf '    up             build and start the full compose stack\n'
	@printf '    down           stop it\n'
	@printf '    example        seed and run the example crawler node\n'
	@printf '    clean          remove build output\n'

all: generate build

generate: proto sqlc

proto:
	./scripts/buf-generate.sh

sqlc:
	./scripts/sqlc-generate.sh

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/spinneret-server ./cmd/spinneret-server
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/spnr ./cmd/spnr

test:
	go test -count=1 ./...

test-short:
	go test -short -count=1 ./...

test-race:
	go test -race -count=1 ./...

cover:
	go test -count=1 -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w $$(git ls-files '*.go' | grep -v '^gen/')

infra-up:
	docker compose -f $(INFRA_COMPOSE) up -d --wait

infra-down:
	docker compose -f $(INFRA_COMPOSE) down -v

web-install:
	cd web && pnpm install --frozen-lockfile

web:
	cd web && pnpm build

# The console gates CI runs: everything but the production build.
web-test:
	cd web && pnpm typecheck
	cd web && pnpm lint
	cd web && pnpm format:check
	cd web && pnpm test

docker:
	docker build -f deploy/docker/Dockerfile -t spinneret:$(VERSION) -t spinneret:local .

up:
	docker compose -f $(STACK_COMPOSE) up -d --build --wait

down:
	docker compose -f $(STACK_COMPOSE) down

# End-to-end suite inside the compose network: rebuilds the images, (re)starts the stack with the e2e
# overlay (short proxy health check interval, mock target) and runs `go test -tags e2e` in the e2e service.
e2e:
	$(E2E_COMPOSE) build
	$(E2E_COMPOSE) up -d --wait spinneret lb mocktarget
	$(E2E_COMPOSE) run --rm --use-aliases e2e

# Playwright end-to-end suite of the web console against the running stack
# (make up). Credentials come from deploy/compose/.env; override SPINNERET_UI_URL
# to test another deployment. Pass arguments with ARGS, e.g.
#   make e2e-web ARGS='--headed -g "sites"'
e2e-web:
	cd web && pnpm exec playwright install chromium
	set -a; [ -f deploy/compose/.env ] && . ./deploy/compose/.env; set +a; \
	cd web && SPINNERET_UI_URL=$${SPINNERET_UI_URL:-http://localhost:8080} \
		SPINNERET_E2E_USER=$${SPINNERET_ADMIN_USERNAME:-admin} \
		SPINNERET_E2E_PASSWORD=$${SPINNERET_ADMIN_PASSWORD} \
		pnpm exec playwright test $(ARGS)

# Replica failover drill: stops one server replica under acquire/report load and checks recovery.
e2e-failover:
	./scripts/e2e-failover.sh

# Example FastAPI crawler: seeds the "example" site and a node token, starts example-crawler and calls it.
example:
	./scripts/example-quickstart.sh

# Example unit tests (respx, no network). Uses examples/fastapi-crawler/.venv when it exists:
#   python3 -m venv examples/fastapi-crawler/.venv
#   examples/fastapi-crawler/.venv/bin/pip install -r examples/fastapi-crawler/requirements-dev.txt -e sdk/python
example-test:
	cd examples/fastapi-crawler && { [ -x .venv/bin/python ] && .venv/bin/python -m pytest -q || python3 -m pytest -q; }

load:
	docker compose -f $(STACK_COMPOSE) --profile loadtest run --rm k6

python-test:
	cd sdk/python && python3 -m pytest -q

clean:
	rm -rf bin dist coverage.out
