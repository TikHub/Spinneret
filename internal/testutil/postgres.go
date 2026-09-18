package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
)

// PostgresURLEnv names the environment variable holding the base PostgreSQL
// URL used by integration tests. The role must be allowed to create databases.
const PostgresURLEnv = "SPINNERET_TEST_DATABASE_URL"

const (
	postgresImage          = "postgres:17-alpine"
	postgresTemplatePrefix = "spinneret_tpl_"
	postgresTestDBPrefix   = "t_"
	// postgresTemplateLockID serializes template creation and cloning across
	// test processes (ASCII "spnr_tst").
	postgresTemplateLockID int64 = 0x73706e725f747374
	postgresSetupTimeout         = 3 * time.Minute
	postgresCleanupTimeout       = 30 * time.Second
	// postgresPoolCloseTimeout bounds how long the cleanup of Postgres waits
	// for connections the test never released.
	postgresPoolCloseTimeout       = 15 * time.Second
	postgresPoolMaxConns     int32 = 10
	// postgresMaxConnections matches the compose test stack, so many parallel
	// tests (each with its own pool) fit into a fallback container as well.
	postgresMaxConnections = "500"
)

// postgresFixture is the per-test-binary PostgreSQL state: the lazily started
// container and the templates already verified by this process.
type postgresFixture struct {
	containerOnce sync.Once
	containerURL  string
	containerErr  error

	mu       sync.Mutex
	verified map[string]bool
}

var sharedPostgres = &postgresFixture{verified: make(map[string]bool)}

// Postgres returns a connection pool to a fresh, fully migrated database
// cloned from the migration template (see PostgresURL). The pool is closed and
// the database dropped when the test finishes. It skips the test in -short mode.
func Postgres(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := PostgresURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), postgresSetupTimeout)
	defer cancel()
	pool, err := pgstore.Open(ctx, dsn, postgresPoolMaxConns)
	if err != nil {
		t.Fatalf("testutil: open test database pool: %v", err)
	}
	// Registered after PostgresURL's cleanup, so it runs before the drop.
	t.Cleanup(func() { closePostgresPool(t, pool, postgresPoolCloseTimeout) })
	return pool
}

// closePostgresPool closes pool but gives up after timeout, reporting a test
// error. pgxpool.Pool.Close blocks until every acquired connection has been
// released, so a test that leaks one (unclosed rows, an unfinished
// transaction, Acquire without Release) would otherwise hang its cleanup
// forever. The database is dropped WITH (FORCE) afterwards, which terminates
// the leaked session; the goroutine closing the pool finishes once the leaked
// connection is released, if ever.
func closePostgresPool(t testing.TB, pool *pgxpool.Pool, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		pool.Close()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Errorf("testutil: closing the test database pool timed out after %s: %d connection(s) still in use "+
			"(unclosed rows, unfinished transaction or Acquire without Release?)", timeout, pool.Stat().AcquiredConns())
	}
}

// PostgresURL creates a fresh database named "t_<random hex>" cloned from a
// migrated template database and returns its URL. The template
// ("spinneret_tpl_<hash of the embedded migrations>") is created and migrated
// on first use and reused afterwards; creation and cloning are serialized
// across processes with an advisory lock. The database is dropped WITH (FORCE)
// when the test finishes. It skips the test in -short mode.
func PostgresURL(t testing.TB) string {
	t.Helper()
	return sharedPostgres.newDatabase(t, true)
}

// PostgresBlankURL creates an empty database (no migrations applied) named
// "t_<random hex>" and returns its URL, for tests that exercise migrations
// themselves. The database is dropped when the test finishes. It skips the
// test in -short mode.
func PostgresBlankURL(t testing.TB) string {
	t.Helper()
	return sharedPostgres.newDatabase(t, false)
}

func (f *postgresFixture) newDatabase(t testing.TB, migrated bool) string {
	t.Helper()
	if testing.Short() {
		t.Skip("testutil: skipping PostgreSQL integration test in -short mode")
	}
	base, err := f.baseURL()
	if err != nil {
		t.Fatalf("testutil: %v (set %s to use an existing server)", err, PostgresURLEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), postgresSetupTimeout)
	defer cancel()

	var name string
	if migrated {
		name, err = f.cloneTemplate(ctx, base)
	} else {
		name, err = createBlankPostgresDatabase(ctx, base)
	}
	if err != nil {
		t.Fatalf("testutil: create test database: %v", err)
	}
	t.Cleanup(func() {
		if err := dropPostgresDatabase(base, name); err != nil {
			t.Errorf("testutil: drop test database %s: %v", name, err)
		}
	})
	dsn, err := postgresDatabaseURL(base, name)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	return dsn
}

// baseURL returns the admin URL from the environment or, when unset, from a
// postgres container started once per test binary. The container is not
// terminated explicitly; the testcontainers Ryuk reaper removes it when the
// process exits.
func (f *postgresFixture) baseURL() (string, error) {
	if v := strings.TrimSpace(os.Getenv(PostgresURLEnv)); v != "" {
		return v, nil
	}
	f.containerOnce.Do(func() {
		f.containerURL, f.containerErr = startPostgresContainer()
	})
	return f.containerURL, f.containerErr
}

func startPostgresContainer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), postgresSetupTimeout)
	defer cancel()
	c, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("spinneret"),
		tcpostgres.WithUsername("spinneret"),
		tcpostgres.WithPassword("spinneret"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithCmdArgs("-c", "max_connections="+postgresMaxConnections),
	)
	if err != nil {
		return "", fmt.Errorf("start %s container: %w", postgresImage, err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", fmt.Errorf("resolve %s container address: %w", postgresImage, err)
	}
	return dsn, nil
}

// cloneTemplate ensures the migration template exists and clones it into a
// new database, all under the template advisory lock: cloning fails while any
// other session is connected to the template, and the lock guarantees that no
// other test process is creating, migrating or refreshing it.
func (f *postgresFixture) cloneTemplate(ctx context.Context, base string) (string, error) {
	tpl, err := PostgresTemplateName()
	if err != nil {
		return "", err
	}
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", fmt.Errorf("connect admin database: %w", err)
	}
	defer closePostgresConn(admin)

	unlock, err := postgresAdvisoryLock(ctx, admin, postgresTemplateLockID)
	if err != nil {
		return "", err
	}
	defer unlock()

	if err := f.ensureTemplate(ctx, admin, base, tpl); err != nil {
		return "", err
	}
	name := randomPostgresDatabaseName()
	stmt := fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s",
		pgx.Identifier{name}.Sanitize(), pgx.Identifier{tpl}.Sanitize())
	if _, err := admin.Exec(ctx, stmt); err != nil {
		return "", fmt.Errorf("clone template %s: %w", tpl, err)
	}
	return name, nil
}

// ensureTemplate makes sure the template database tpl exists and is migrated
// to the latest embedded version. A template left behind half-migrated (for
// example by an interrupted run) is dropped and rebuilt. The caller must hold
// the template advisory lock.
func (f *postgresFixture) ensureTemplate(ctx context.Context, admin *pgx.Conn, base, tpl string) error {
	f.mu.Lock()
	verified := f.verified[tpl]
	f.mu.Unlock()
	if verified {
		return nil
	}

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", tpl).Scan(&exists); err != nil {
		return fmt.Errorf("look up template %s: %w", tpl, err)
	}
	ready := false
	if exists {
		var err error
		if ready, err = refreshPostgresTemplate(ctx, base, tpl); err != nil {
			return err
		}
		if !ready {
			if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{tpl}.Sanitize()+" WITH (FORCE)"); err != nil {
				return fmt.Errorf("drop stale template %s: %w", tpl, err)
			}
		}
	}
	if !ready {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{tpl}.Sanitize()); err != nil {
			return fmt.Errorf("create template %s: %w", tpl, err)
		}
		if err := migratePostgresTemplate(ctx, base, tpl); err != nil {
			dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postgresCleanupTimeout)
			defer cancel()
			_, _ = admin.Exec(dropCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{tpl}.Sanitize()+" WITH (FORCE)")
			return err
		}
	}

	f.mu.Lock()
	f.verified[tpl] = true
	f.mu.Unlock()
	return nil
}

// refreshPostgresTemplate reports whether an existing template is at the
// latest migration version and, if so, rolls its partitions forward so that
// clones accept rows timestamped now even when the template was built days
// ago. The pool is closed before returning so the template can be cloned.
func refreshPostgresTemplate(ctx context.Context, base, tpl string) (bool, error) {
	latest, err := pgstore.LatestMigrationVersion()
	if err != nil {
		return false, err
	}
	pool, err := openPostgresDatabase(ctx, base, tpl)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	version, err := pgstore.MigrationVersion(ctx, pool)
	if err != nil {
		return false, fmt.Errorf("template %s: %w", tpl, err)
	}
	if version != latest {
		return false, nil
	}
	if err := pgstore.EnsurePartitions(ctx, pool, time.Now()); err != nil {
		return false, fmt.Errorf("template %s: %w", tpl, err)
	}
	return true, nil
}

func migratePostgresTemplate(ctx context.Context, base, tpl string) error {
	pool, err := openPostgresDatabase(ctx, base, tpl)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate template %s: %w", tpl, err)
	}
	return nil
}

func openPostgresDatabase(ctx context.Context, base, name string) (*pgxpool.Pool, error) {
	dsn, err := postgresDatabaseURL(base, name)
	if err != nil {
		return nil, err
	}
	pool, err := pgstore.Open(ctx, dsn, 2)
	if err != nil {
		return nil, fmt.Errorf("connect database %s: %w", name, err)
	}
	return pool, nil
}

func createBlankPostgresDatabase(ctx context.Context, base string) (string, error) {
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", fmt.Errorf("connect admin database: %w", err)
	}
	defer closePostgresConn(admin)
	name := randomPostgresDatabaseName()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", fmt.Errorf("create database %s: %w", name, err)
	}
	return name, nil
}

func dropPostgresDatabase(base, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return fmt.Errorf("connect admin database: %w", err)
	}
	defer closePostgresConn(admin)
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		return err
	}
	return nil
}

// postgresAdvisoryLock blocks until the session-level advisory lock is granted
// or ctx is done. The returned function releases it.
func postgresAdvisoryLock(ctx context.Context, conn *pgx.Conn, id int64) (func(), error) {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", id); err != nil {
		return nil, fmt.Errorf("acquire advisory lock %d: %w", id, err)
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
		defer cancel()
		// Closing the session also releases the lock, so a failure here is harmless.
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", id)
	}, nil
}

func closePostgresConn(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), postgresCleanupTimeout)
	defer cancel()
	_ = conn.Close(ctx)
}

// PostgresTemplateName returns the name of the migration template database:
// "spinneret_tpl_" followed by the first 12 hex digits of a SHA-256 over the
// names and contents of all embedded migration files, so any migration change
// yields a new template.
func PostgresTemplateName() (string, error) {
	const dir = "migrations"
	entries, err := fs.ReadDir(pgstore.Migrations, dir)
	if err != nil {
		return "", fmt.Errorf("read embedded migrations: %w", err)
	}
	h := sha256.New()
	for _, e := range entries { // fs.ReadDir sorts by file name
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(pgstore.Migrations, path.Join(dir, e.Name()))
		if err != nil {
			return "", fmt.Errorf("read embedded migration %s: %w", e.Name(), err)
		}
		h.Write([]byte(e.Name()))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return postgresTemplatePrefix + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// postgresDatabaseURL returns base with its database replaced by name.
func postgresDatabaseURL(base, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		// url.Parse errors echo the input, which may contain a password.
		return "", errors.New("base PostgreSQL URL is not a valid URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", fmt.Errorf("base PostgreSQL URL has scheme %q, want postgres:// or postgresql://", u.Scheme)
	}
	u.Path = "/" + name
	u.RawPath = ""
	return u.String(), nil
}

func randomPostgresDatabaseName() string {
	var b [8]byte
	// crypto/rand.Read never returns an error (it aborts the process instead).
	_, _ = rand.Read(b[:])
	return postgresTestDBPrefix + hex.EncodeToString(b[:])
}
