package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/observability"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Environment variables read by the relaxed loaders.
const (
	envDatabaseURL      = "SPINNERET_DATABASE_URL"
	envDatabaseMaxConns = "SPINNERET_DATABASE_MAX_CONNS"
	envRedisURL         = "SPINNERET_REDIS_URL"
	envRedisAddrs       = "SPINNERET_REDIS_ADDRS"
	envRedisPrefix      = "SPINNERET_REDIS_PREFIX"

	// cliDatabaseMaxConns is the pool size of database-only commands.
	cliDatabaseMaxConns int32 = 4
)

// app carries the process environment of one CLI invocation so commands can
// be tested without touching the real process state.
type app struct {
	lookup   func(string) (string, bool)
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
	logLevel string
}

// env returns the trimmed value of an environment variable.
func (a *app) env(key string) string {
	v, _ := a.lookup(key)
	return strings.TrimSpace(v)
}

// logger returns a text logger on stderr; command output stays on stdout.
func (a *app) logger() *slog.Logger {
	l, err := observability.NewLogger(a.logLevel, "text", a.stderr)
	if err != nil {
		return slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	return l
}

// fullConfig loads and validates the complete server configuration.
func (a *app) fullConfig() (appconfig.Config, error) {
	cfg, err := appconfig.LoadFrom(a.lookup)
	if err != nil {
		return appconfig.Config{}, fmt.Errorf("invalid configuration:\n%w", err)
	}
	return cfg, nil
}

// openDatabase connects with only SPINNERET_DATABASE_URL (and optionally
// SPINNERET_DATABASE_MAX_CONNS) set; Redis and KEK settings are not required.
func (a *app) openDatabase(ctx context.Context) (*pgxpool.Pool, error) {
	url := a.env(envDatabaseURL)
	if url == "" {
		return nil, errors.New(envDatabaseURL + " is required")
	}
	maxConns := cliDatabaseMaxConns
	if v := a.env(envDatabaseMaxConns); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 2 {
			return nil, fmt.Errorf("%s must be an integer >= 2 (got %q)", envDatabaseMaxConns, v)
		}
		maxConns = int32(n)
	}
	pool, err := pgstore.Open(ctx, url, maxConns)
	if err != nil {
		return nil, fmt.Errorf("connect postgresql: %w", err)
	}
	return pool, nil
}

// optionalRedis connects to Redis when SPINNERET_REDIS_URL or
// SPINNERET_REDIS_ADDRS is set. It returns a nil client (and no error) when
// Redis is not configured, so database-only commands work without it.
func (a *app) optionalRedis(ctx context.Context) (rueidis.Client, redis.Keys, error) {
	url, addrs := a.env(envRedisURL), splitList(a.env(envRedisAddrs))
	prefix := a.env(envRedisPrefix)
	if prefix == "" {
		prefix = redis.DefaultPrefix
	}
	keys := redis.NewKeys(prefix)
	if url == "" && len(addrs) == 0 {
		return nil, keys, nil
	}
	client, err := redis.Open(ctx, url, addrs)
	if err != nil {
		return nil, keys, fmt.Errorf("connect redis: %w", err)
	}
	return client, keys, nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// lookupNamespace resolves tenant and namespace names to their IDs.
func lookupNamespace(ctx context.Context, pool *pgxpool.Pool, tenant, namespace string) (tenantID, namespaceID string, err error) {
	err = pool.QueryRow(ctx, `
SELECT t.id, n.id
FROM namespaces n
JOIN tenants t ON t.id = n.tenant_id
WHERE t.name = $1 AND n.name = $2`, tenant, namespace).Scan(&tenantID, &namespaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("namespace %q of tenant %q not found", namespace, tenant)
		}
		return "", "", fmt.Errorf("look up namespace %s/%s: %w", tenant, namespace, err)
	}
	return tenantID, namespaceID, nil
}
