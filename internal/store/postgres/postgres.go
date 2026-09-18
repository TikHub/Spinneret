// Package postgres owns Spinneret's PostgreSQL access layer: the pgx
// connection pool factory, the embedded goose migrations, the transaction
// helper, error mapping to application errors and the partition manager for
// the range-partitioned history and statistics tables.
//
// Generated sqlc queries live in the db sub-package (queries/*.sql).
package postgres

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrations holds the goose SQL migrations (migrations/*.sql). The schema in
// these files is the single source of truth for both goose and sqlc.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// ApplicationName is the application_name reported to PostgreSQL by pools
// created with Open unless the connection URL sets one explicitly.
const ApplicationName = "spinneret"

// Pool tuning applied by Open.
const (
	defaultMinConns          int32 = 2
	defaultMaxConnIdleTime         = 5 * time.Minute
	defaultHealthCheckPeriod       = 30 * time.Second
)

// Open creates a pgx connection pool for url and verifies connectivity with a
// ping. maxConns caps the pool size; values <= 0 keep the pgx default
// (max(4, NumCPU)) or the pool_max_conns URL parameter. The pool keeps at
// least two idle connections (never more than maxConns), closes connections
// idle for five minutes and health-checks every 30 seconds.
// application_name defaults to "spinneret". No global statement timeout is
// configured; callers bound queries with their context.
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, errors.New("open postgres: database url is empty")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx redacts passwords in parse errors.
		return nil, fmt.Errorf("open postgres: parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MinConns = min(defaultMinConns, cfg.MaxConns)
	cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	cfg.HealthCheckPeriod = defaultHealthCheckPeriod
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	if cfg.ConnConfig.RuntimeParams["application_name"] == "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = ApplicationName
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open postgres: ping %s:%d/%s: %w",
			cfg.ConnConfig.Host, cfg.ConnConfig.Port, cfg.ConnConfig.Database, err)
	}
	return pool, nil
}
