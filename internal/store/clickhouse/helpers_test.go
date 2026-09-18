package clickhouse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
)

const testClickHouseImage = "clickhouse/clickhouse-server:25.8-alpine"

var (
	containerOnce sync.Once
	containerURL  string
	containerErr  error
)

// testClickHouseURL returns the base ClickHouse URL, skipping in short mode.
func testClickHouseURL(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires ClickHouse; skipped in -short mode")
	}
	if u := os.Getenv("SPINNERET_TEST_CLICKHOUSE_URL"); u != "" {
		return u
	}
	containerOnce.Do(func() {
		ctx := context.Background()
		c, err := tcclickhouse.Run(ctx, testClickHouseImage)
		if err != nil {
			containerErr = err
			return
		}
		containerURL, containerErr = c.ConnectionString(ctx)
	})
	require.NoError(t, containerErr, "start clickhouse container")
	return containerURL
}

// testDatabase creates a unique database and returns a URL pointing at it
// plus an admin connection; the database is dropped on cleanup.
func testDatabase(t testing.TB) (string, chdriver.Conn) {
	t.Helper()
	base := testClickHouseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := Open(ctx, base)
	require.NoError(t, err)

	var b [6]byte
	_, err = rand.Read(b[:])
	require.NoError(t, err)
	name := "t_" + hex.EncodeToString(b[:])
	require.NoError(t, admin.Exec(ctx, "CREATE DATABASE "+name))
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := admin.Exec(cctx, "DROP DATABASE IF EXISTS "+name+" SYNC"); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
		_ = admin.Close()
	})

	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String(), admin
}

// testConn returns a connection to a fresh, empty test database.
func testConn(t testing.TB) chdriver.Conn {
	t.Helper()
	dsn, _ := testDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Open(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
