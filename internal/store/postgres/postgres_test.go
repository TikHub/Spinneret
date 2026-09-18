package postgres_test

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func TestOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	dsn := testutil.PostgresURL(t)

	t.Run("applies pool settings", func(t *testing.T) {
		pool, err := postgres.Open(ctx, dsn, 7)
		require.NoError(t, err)
		defer pool.Close()

		cfg := pool.Config()
		require.Equal(t, int32(7), cfg.MaxConns)
		require.Equal(t, int32(2), cfg.MinConns)
		require.Equal(t, 5*time.Minute, cfg.MaxConnIdleTime)
		require.Equal(t, 30*time.Second, cfg.HealthCheckPeriod)

		var appName, statementTimeout string
		require.NoError(t, pool.QueryRow(ctx,
			"SELECT current_setting('application_name'), current_setting('statement_timeout')").Scan(&appName, &statementTimeout))
		require.Equal(t, postgres.ApplicationName, appName)
		require.Equal(t, "0", statementTimeout, "no global statement timeout")
	})

	t.Run("min conns never exceed max conns", func(t *testing.T) {
		pool, err := postgres.Open(ctx, dsn, 1)
		require.NoError(t, err)
		defer pool.Close()
		require.Equal(t, int32(1), pool.Config().MaxConns)
		require.Equal(t, int32(1), pool.Config().MinConns)
	})

	t.Run("non-positive max conns keeps the default", func(t *testing.T) {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		q := u.Query()
		q.Set("pool_max_conns", "3")
		q.Set("application_name", "custom-app")
		u.RawQuery = q.Encode()

		pool, err := postgres.Open(ctx, u.String(), 0)
		require.NoError(t, err)
		defer pool.Close()
		require.Equal(t, int32(3), pool.Config().MaxConns)

		var appName string
		require.NoError(t, pool.QueryRow(ctx, "SELECT current_setting('application_name')").Scan(&appName))
		require.Equal(t, "custom-app", appName, "an explicit application_name is preserved")
	})
}

func TestOpenErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Reserve a local port and close it so nothing listens there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "empty", url: "", wantErr: "database url is empty"},
		{name: "malformed", url: "postgres://user:secret@localhost:notaport/db", wantErr: "parse database url"},
		{name: "unreachable", url: "postgres://user:secret@" + addr + "/db?sslmode=disable&connect_timeout=2", wantErr: "ping"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := postgres.Open(ctx, tc.url, 2)
			require.Nil(t, pool)
			require.ErrorContains(t, err, tc.wantErr)
			require.NotContains(t, err.Error(), "secret", "password must not leak")
		})
	}
}
