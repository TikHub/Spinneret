package testutil

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"

	chstore "github.com/TikHub/Spinneret/internal/store/clickhouse"
)

func clickhouseDatabaseExists(ctx context.Context, t *testing.T, conn chdriver.Conn, name string) bool {
	t.Helper()
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&n))
	return n == 1
}

func TestClickHouseFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	base, err := sharedClickHouse.baseURL()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	observer, err := chstore.Open(ctx, base)
	require.NoError(t, err)
	defer func() { _ = observer.Close() }()

	var dbA, dbB string
	t.Run("isolated migrated databases", func(t *testing.T) {
		a := ClickHouse(t)
		dsn := ClickHouseURL(t)

		require.NoError(t, a.QueryRow(ctx, "SELECT currentDatabase()").Scan(&dbA))
		require.Regexp(t, regexp.MustCompile(`^t_[0-9a-f]{16}$`), dbA)
		b, err := chstore.Open(ctx, dsn)
		require.NoError(t, err)
		defer func() { _ = b.Close() }()
		require.NoError(t, b.QueryRow(ctx, "SELECT currentDatabase()").Scan(&dbB))
		require.NotEqual(t, dbA, dbB)

		for _, conn := range []chdriver.Conn{a, b} {
			var tables []string
			rows, err := conn.Query(ctx, "SELECT name FROM system.tables WHERE database = currentDatabase() ORDER BY name")
			require.NoError(t, err)
			for rows.Next() {
				var name string
				require.NoError(t, rows.Scan(&name))
				tables = append(tables, name)
			}
			require.NoError(t, rows.Err())
			_ = rows.Close()
			require.Equal(t, []string{chstore.LeaseEventsTable, chstore.ReportEventsTable}, tables)
		}
		var engine string
		require.NoError(t, a.QueryRow(ctx,
			"SELECT engine_full FROM system.tables WHERE database = currentDatabase() AND name = 'report_events'").Scan(&engine))
		require.Contains(t, engine, "toIntervalDay(90)")

		require.True(t, clickhouseDatabaseExists(ctx, t, observer, dbA))
		require.True(t, clickhouseDatabaseExists(ctx, t, observer, dbB))
	})
	require.NotEmpty(t, dbA)
	require.False(t, clickhouseDatabaseExists(ctx, t, observer, dbA), "database dropped on cleanup")
	require.False(t, clickhouseDatabaseExists(ctx, t, observer, dbB), "database dropped on cleanup")
}

func TestClickHouseDatabaseURL(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		want    string
		wantErr string
	}{
		{name: "replaces database", base: "clickhouse://u:p@localhost:9000/default?dial_timeout=5s", want: "clickhouse://u:p@localhost:9000/t_1?dial_timeout=5s"},
		{name: "adds database", base: "tcp://localhost:9000", want: "tcp://localhost:9000/t_1"},
		{name: "http rejected", base: "http://localhost:8123/default", wantErr: "want clickhouse://"},
		{name: "invalid url hides password", base: "clickhouse://u:secret@local host:9000", wantErr: "not a valid URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := clickhouseDatabaseURL(tt.base, "t_1")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRandomClickHouseDatabaseName(t *testing.T) {
	a, b := randomClickHouseDatabaseName(), randomClickHouseDatabaseName()
	require.Regexp(t, `^t_[0-9a-f]{16}$`, a)
	require.NotEqual(t, a, b)
}

func TestClickHouseFixtureFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	t.Run("container start failure", func(t *testing.T) {
		t.Setenv(ClickHouseURLEnv, "")
		f := &clickhouseFixture{}
		f.containerOnce.Do(func() { f.containerErr = errors.New("docker unavailable") })
		msg := (&fixtureFatalCapture{T: t}).run(func(tb testing.TB) { f.newDatabase(tb) })
		require.Contains(t, msg, "docker unavailable")
		require.Contains(t, msg, ClickHouseURLEnv)
	})

	t.Run("container url is used when the variable is unset", func(t *testing.T) {
		t.Setenv(ClickHouseURLEnv, " ")
		f := &clickhouseFixture{}
		f.containerOnce.Do(func() { f.containerURL = "clickhouse://container:9000/default" })
		got, err := f.baseURL()
		require.NoError(t, err)
		require.Equal(t, "clickhouse://container:9000/default", got)
	})

	t.Run("invalid base url", func(t *testing.T) {
		t.Setenv(ClickHouseURLEnv, "http://secret@localhost:8123/default")
		f := &clickhouseFixture{}
		msg := (&fixtureFatalCapture{T: t}).run(func(tb testing.TB) { f.newDatabase(tb) })
		require.Contains(t, msg, "scheme")
	})

	t.Run("unreachable server", func(t *testing.T) {
		t.Setenv(ClickHouseURLEnv, "clickhouse://u:secret@127.0.0.1:1/default?dial_timeout=500ms")
		f := &clickhouseFixture{}
		msg := (&fixtureFatalCapture{T: t}).run(func(tb testing.TB) { f.newDatabase(tb) })
		require.Contains(t, msg, "create test clickhouse database")
		require.False(t, strings.Contains(msg, "secret"))
	})
}

func TestStartClickHouseContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseSetupTimeout)
	defer cancel()

	// The container is reaped by Ryuk when the test binary exits.
	f := &clickhouseFixture{}
	f.containerOnce.Do(func() { f.containerURL, f.containerErr = startClickHouseContainer() })
	require.NoError(t, f.containerErr)
	t.Setenv(ClickHouseURLEnv, "")
	dsn := f.newDatabase(t)
	conn, err := chstore.Open(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	version, err := conn.ServerVersion()
	require.NoError(t, err)
	require.Equal(t, uint64(25), version.Version.Major)
	var tables uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM system.tables WHERE database = currentDatabase()").Scan(&tables))
	require.Equal(t, uint64(2), tables)
}
