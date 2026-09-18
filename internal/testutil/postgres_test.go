package testutil

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

func requirePostgresBase(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	base, err := sharedPostgres.baseURL()
	require.NoError(t, err)
	return base
}

func postgresDatabaseExists(ctx context.Context, t *testing.T, base, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(ctx, base)
	require.NoError(t, err)
	defer closePostgresConn(conn)
	var exists bool
	require.NoError(t, conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists))
	return exists
}

func TestPostgresParallel(t *testing.T) {
	base := requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	latest, err := pgstore.LatestMigrationVersion()
	require.NoError(t, err)

	var mu sync.Mutex
	names := map[string]bool{}
	t.Run("group", func(t *testing.T) {
		for i := range 6 {
			t.Run(fmt.Sprintf("db%d", i), func(t *testing.T) {
				t.Parallel()
				pool := Postgres(t)

				var name string
				require.NoError(t, pool.QueryRow(ctx, "SELECT current_database()").Scan(&name))
				require.True(t, strings.HasPrefix(name, "t_"), name)

				version, err := pgstore.MigrationVersion(ctx, pool)
				require.NoError(t, err)
				require.Equal(t, latest, version)

				// Every test sees only its own data.
				_, err = pool.Exec(ctx, "INSERT INTO tenants (id, name) VALUES ('ten_shared', 'shared')")
				require.NoError(t, err)

				// Partitions accept rows timestamped now.
				_, err = pool.Exec(ctx, `INSERT INTO risk_events (id, tenant_id, namespace_id, site_id, outcome)
					VALUES ('rsk_1', 't', 'n', 's', 'captcha')`)
				require.NoError(t, err)

				mu.Lock()
				names[name] = true
				mu.Unlock()
			})
		}
	})
	require.Len(t, names, 6, "databases must be distinct")
	for name := range names {
		require.False(t, postgresDatabaseExists(ctx, t, base, name), "database %s must be dropped after the test", name)
	}
}

func TestPostgresURL(t *testing.T) {
	base := requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var (
		name string
		pool *pgxpool.Pool
	)
	t.Run("inner", func(t *testing.T) {
		dsn := PostgresURL(t)
		var err error
		pool, err = pgstore.Open(ctx, dsn, 2)
		require.NoError(t, err)
		// The pool deliberately stays open past the cleanup: the database is
		// dropped WITH (FORCE) regardless of connected sessions.
		require.NoError(t, pool.QueryRow(ctx, "SELECT current_database()").Scan(&name))
		require.True(t, postgresDatabaseExists(ctx, t, base, name))
		tpl, err := PostgresTemplateName()
		require.NoError(t, err)
		require.True(t, postgresDatabaseExists(ctx, t, base, tpl))
	})
	require.NotNil(t, pool)
	defer pool.Close()
	require.NotEmpty(t, name)
	require.False(t, postgresDatabaseExists(ctx, t, base, name))
}

func TestPostgresBlankURL(t *testing.T) {
	requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pool, err := pgstore.Open(ctx, PostgresBlankURL(t), 2)
	require.NoError(t, err)
	defer pool.Close()
	version, err := pgstore.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Zero(t, version)
	var tables int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables))
	require.Zero(t, tables)
}

func TestPostgresTemplateName(t *testing.T) {
	first, err := PostgresTemplateName()
	require.NoError(t, err)
	second, err := PostgresTemplateName()
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Regexp(t, regexp.MustCompile(`^spinneret_tpl_[0-9a-f]{12}$`), first)
}

func TestPostgresDatabaseURL(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		want    string
		wantErr string
	}{
		{
			name: "postgres scheme keeps query",
			base: "postgres://u:p@localhost:5432/spinneret?sslmode=disable",
			want: "postgres://u:p@localhost:5432/t_1?sslmode=disable",
		},
		{name: "postgresql scheme without path", base: "postgresql://localhost", want: "postgresql://localhost/t_1"},
		{name: "invalid url", base: "postgres://u:secret@local host:%zz/db", wantErr: "not a valid URL"},
		{name: "key value dsn", base: "host=localhost dbname=spinneret", wantErr: "want postgres://"},
		{name: "other scheme", base: "mysql://localhost/db", wantErr: `scheme "mysql"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := postgresDatabaseURL(tc.base, "t_1")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestRandomPostgresDatabaseName(t *testing.T) {
	a := randomPostgresDatabaseName()
	b := randomPostgresDatabaseName()
	require.Regexp(t, regexp.MustCompile(`^t_[0-9a-f]{16}$`), a)
	require.NotEqual(t, a, b)
}

func TestEnsureTemplateRebuildsStaleTemplate(t *testing.T) {
	base := requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tpl := "spinneret_tpl_test_" + strings.TrimPrefix(randomPostgresDatabaseName(), "t_")
	t.Cleanup(func() { require.NoError(t, dropPostgresDatabase(base, tpl)) })

	admin, err := pgx.Connect(ctx, base)
	require.NoError(t, err)
	defer closePostgresConn(admin)
	unlock, err := postgresAdvisoryLock(ctx, admin, postgresTemplateLockID)
	require.NoError(t, err)
	defer unlock()

	// A template that exists but was never migrated is rebuilt.
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{tpl}.Sanitize())
	require.NoError(t, err)
	fixture := &postgresFixture{verified: map[string]bool{}}
	require.NoError(t, fixture.ensureTemplate(ctx, admin, base, tpl))
	require.True(t, fixture.verified[tpl])

	pool, err := openPostgresDatabase(ctx, base, tpl)
	require.NoError(t, err)
	latest, err := pgstore.LatestMigrationVersion()
	require.NoError(t, err)
	version, err := pgstore.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, latest, version)
	// Marker proving the next call reuses (does not rebuild) the template.
	_, err = pool.Exec(ctx, "CREATE TABLE reuse_marker (id int)")
	require.NoError(t, err)
	pool.Close()

	fresh := &postgresFixture{verified: map[string]bool{}}
	require.NoError(t, fresh.ensureTemplate(ctx, admin, base, tpl))
	// Already verified: no database access at all.
	require.NoError(t, fresh.ensureTemplate(ctx, nil, base, tpl))

	pool, err = openPostgresDatabase(ctx, base, tpl)
	require.NoError(t, err)
	defer pool.Close()
	var marker bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('reuse_marker') IS NOT NULL").Scan(&marker))
	require.True(t, marker, "a ready template must be reused")
}

func TestPostgresHelpersErrors(t *testing.T) {
	base := requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := refreshPostgresTemplate(ctx, base, "spinneret_missing_database")
	require.ErrorContains(t, err, "connect database spinneret_missing_database")
	err = migratePostgresTemplate(ctx, base, "spinneret_missing_database")
	require.ErrorContains(t, err, "connect database spinneret_missing_database")
	_, err = openPostgresDatabase(ctx, "mysql://localhost/db", "x")
	require.Error(t, err)

	conn, err := pgx.Connect(ctx, base)
	require.NoError(t, err)
	defer closePostgresConn(conn)
	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	_, err = postgresAdvisoryLock(canceled, conn, postgresTemplateLockID)
	require.ErrorContains(t, err, "acquire advisory lock")

	f := &postgresFixture{verified: map[string]bool{}}
	require.ErrorContains(t, f.ensureTemplate(canceled, conn, base, "spinneret_tpl_unused"), "look up template")

	require.NoError(t, dropPostgresDatabase(base, "spinneret_missing_database"), "dropping a missing database is a no-op")
	require.Error(t, dropPostgresDatabase("postgres://127.0.0.1:1/db?connect_timeout=1", "x"))
}

// postgresFatalRecorder captures Fatalf/Skip calls of the fixture without failing the
// enclosing test. Like testing.T it stops the calling goroutine.
type postgresFatalRecorder struct {
	*testing.T
	mu      sync.Mutex
	message string
}

func (r *postgresFatalRecorder) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.message = fmt.Sprintf(format, args...)
	r.mu.Unlock()
	runtime.Goexit()
}

func (r *postgresFatalRecorder) run(fn func(tb testing.TB)) string {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.message
}

func TestNewDatabaseFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Run("container start failure", func(t *testing.T) {
		t.Setenv(PostgresURLEnv, "")
		f := &postgresFixture{verified: map[string]bool{}}
		f.containerOnce.Do(func() { f.containerErr = errors.New("docker unavailable") })
		msg := (&postgresFatalRecorder{T: t}).run(func(tb testing.TB) { f.newDatabase(tb, true) })
		require.Contains(t, msg, "docker unavailable")
		require.Contains(t, msg, PostgresURLEnv)
	})

	t.Run("container url is used when the variable is unset", func(t *testing.T) {
		t.Setenv(PostgresURLEnv, "  ")
		f := &postgresFixture{verified: map[string]bool{}}
		f.containerOnce.Do(func() { f.containerURL = "postgres://container/db" })
		got, err := f.baseURL()
		require.NoError(t, err)
		require.Equal(t, "postgres://container/db", got)
	})

	for _, migrated := range []bool{true, false} {
		t.Run(fmt.Sprintf("unreachable server migrated=%v", migrated), func(t *testing.T) {
			t.Setenv(PostgresURLEnv, "postgres://u:secret@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
			f := &postgresFixture{verified: map[string]bool{}}
			msg := (&postgresFatalRecorder{T: t}).run(func(tb testing.TB) { f.newDatabase(tb, migrated) })
			require.Contains(t, msg, "create test database")
			require.NotContains(t, msg, "secret")
		})
	}
}

// postgresErrorRecorder captures Errorf calls without failing the enclosing test.
type postgresErrorRecorder struct {
	testing.TB
	mu     sync.Mutex
	errors []string
}

func (r *postgresErrorRecorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func TestClosePostgresPool(t *testing.T) {
	requirePostgresBase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	t.Run("released connections close promptly", func(t *testing.T) {
		pool, err := pgstore.Open(ctx, PostgresBlankURL(t), 2)
		require.NoError(t, err)
		require.NoError(t, pool.Ping(ctx))
		rec := &postgresErrorRecorder{TB: t}
		closePostgresPool(rec, pool, 10*time.Second)
		require.Empty(t, rec.errors)
	})

	t.Run("a leaked connection is reported instead of hanging", func(t *testing.T) {
		pool, err := pgstore.Open(ctx, PostgresBlankURL(t), 2)
		require.NoError(t, err)
		leaked, err := pool.Acquire(ctx)
		require.NoError(t, err)

		rec := &postgresErrorRecorder{TB: t}
		start := time.Now()
		closePostgresPool(rec, pool, 200*time.Millisecond)
		require.Less(t, time.Since(start), 5*time.Second)
		require.Len(t, rec.errors, 1)
		require.Contains(t, rec.errors[0], "timed out")
		require.Contains(t, rec.errors[0], "1 connection(s) still in use")

		// Releasing the connection lets the pending Close finish.
		leaked.Release()
	})
}

func TestStartPostgresContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), postgresSetupTimeout)
	defer cancel()

	// The container is reaped by Ryuk when the test binary exits.
	dsn, err := startPostgresContainer()
	require.NoError(t, err)
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer closePostgresConn(conn)
	var version int
	require.NoError(t, conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version))
	require.GreaterOrEqual(t, version, 170000)
	var maxConnections string
	require.NoError(t, conn.QueryRow(ctx, "SELECT current_setting('max_connections')").Scan(&maxConnections))
	require.Equal(t, postgresMaxConnections, maxConnections)
}
