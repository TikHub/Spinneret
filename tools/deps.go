//go:build tools

// Package tools pins module dependencies that are not yet imported by
// production code so that `go mod tidy` keeps them in go.mod.
package tools

import (
	_ "buf.build/go/protovalidate"
	_ "connectrpc.com/connect"
	_ "connectrpc.com/otelconnect"
	_ "connectrpc.com/validate"
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/cespare/xxhash/v2"
	_ "github.com/google/uuid"
	_ "github.com/hashicorp/golang-lru/v2"
	_ "github.com/jackc/pgx/v5"
	_ "github.com/oschwald/maxminddb-golang/v2"
	_ "github.com/pressly/goose/v3"
	_ "github.com/prometheus/client_golang/prometheus"
	_ "github.com/redis/rueidis"
	_ "github.com/santhosh-tekuri/jsonschema/v6"
	_ "github.com/spf13/cobra"
	_ "github.com/stretchr/testify/require"
	_ "github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/clickhouse"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "github.com/testcontainers/testcontainers-go/modules/redis"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	_ "go.opentelemetry.io/otel/sdk"
	_ "go.yaml.in/yaml/v3"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/net/proxy"
	_ "golang.org/x/sync/errgroup"
)
