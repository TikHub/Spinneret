package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// MigrationLockID is the pg_advisory_lock key that serializes schema
// migrations across processes (ASCII "spnr_mig").
const MigrationLockID int64 = 0x73706e725f6d6967

// migrationTable is the goose version table.
const migrationTable = goose.DefaultTablename

// migrationsDir is the directory of the embedded migrations inside Migrations.
const migrationsDir = "migrations"

// unlockTimeout bounds the advisory unlock performed after a migration, which
// runs even when the caller's context has been canceled.
const unlockTimeout = 10 * time.Second

// Migrate applies every pending migration. Concurrent callers (for example
// several server instances starting at once) are serialized with a
// session-level advisory lock held on a dedicated connection, so the pool may
// be as small as one connection.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return withMigrationProvider(ctx, pool, func(p *goose.Provider) error {
		if _, err := p.Up(ctx); err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		return nil
	})
}

// MigrateDownTo rolls migrations back until the schema version equals
// version (0 removes every migration). It is serialized with Migrate and is
// intended for tooling and tests.
func MigrateDownTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	if version < 0 {
		return fmt.Errorf("migrate down: invalid target version %d", version)
	}
	return withMigrationProvider(ctx, pool, func(p *goose.Provider) error {
		if _, err := p.DownTo(ctx, version); err != nil {
			return fmt.Errorf("migrate down to %d: %w", version, err)
		}
		return nil
	})
}

// MigrationVersion returns the highest applied migration version, or 0 when
// the database has never been migrated. It never creates the version table.
func MigrationVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	if pool == nil {
		return 0, errors.New("migration version: pool is nil")
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", migrationTable).Scan(&exists); err != nil {
		return 0, fmt.Errorf("migration version: look up %s: %w", migrationTable, err)
	}
	if !exists {
		return 0, nil
	}
	var version int64
	query := "SELECT coalesce(max(version_id), 0) FROM " + pgx.Identifier{migrationTable}.Sanitize()
	if err := pool.QueryRow(ctx, query).Scan(&version); err != nil {
		return 0, fmt.Errorf("migration version: query %s: %w", migrationTable, err)
	}
	return version, nil
}

// LatestMigrationVersion returns the highest migration version embedded in
// the binary.
func LatestMigrationVersion() (int64, error) {
	entries, err := fs.ReadDir(Migrations, migrationsDir)
	if err != nil {
		return 0, fmt.Errorf("read embedded migrations: %w", err)
	}
	var latest int64
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		v, err := migrationFileVersion(e.Name())
		if err != nil {
			return 0, err
		}
		latest = max(latest, v)
	}
	if latest == 0 {
		return 0, errors.New("read embedded migrations: no migrations found")
	}
	return latest, nil
}

// migrationFileVersion parses the numeric version prefix of a goose file name
// such as "00001_core.sql".
func migrationFileVersion(name string) (int64, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q: missing version prefix", name)
	}
	v, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("migration %q: invalid version prefix", name)
	}
	return v, nil
}

// withMigrationProvider runs fn with a goose provider bound to pool while
// holding the migration advisory lock.
func withMigrationProvider(ctx context.Context, pool *pgxpool.Pool, fn func(p *goose.Provider) error) (err error) {
	if pool == nil {
		return errors.New("migrate: pool is nil")
	}
	release, err := acquireMigrationLock(ctx, pool)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, release())
	}()

	fsys, err := fs.Sub(Migrations, migrationsDir)
	if err != nil {
		return fmt.Errorf("migrate: open embedded migrations: %w", err)
	}
	// Closing the *sql.DB does not close the underlying pgx pool.
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithDisableGlobalRegistry(true),
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("migrate: create goose provider: %w", err)
	}
	defer func() {
		if cerr := provider.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("migrate: close goose provider: %w", cerr))
		}
	}()
	return fn(provider)
}

// acquireMigrationLock opens a dedicated connection (outside the pool, so a
// single-connection pool cannot deadlock) and blocks on pg_advisory_lock until
// the lock is granted or ctx is done. The returned function unlocks and closes
// the connection.
func acquireMigrationLock(ctx context.Context, pool *pgxpool.Pool) (func() error, error) {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		return nil, fmt.Errorf("migrate: open lock connection: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", MigrationLockID); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()
		_ = conn.Close(closeCtx)
		return nil, fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}
	return func() error {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()
		var unlockErr error
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", MigrationLockID); err != nil {
			// Closing the session releases the lock anyway.
			unlockErr = fmt.Errorf("migrate: release advisory lock: %w", err)
		}
		if err := conn.Close(unlockCtx); err != nil {
			unlockErr = errors.Join(unlockErr, fmt.Errorf("migrate: close lock connection: %w", err))
		}
		return unlockErr
	}, nil
}
