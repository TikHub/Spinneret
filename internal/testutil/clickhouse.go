package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"

	chstore "github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
)

// ClickHouseURLEnv names the environment variable holding the base ClickHouse
// URL used by integration tests. The user must be allowed to create databases.
const ClickHouseURLEnv = "SPINNERET_TEST_CLICKHOUSE_URL"

const (
	clickhouseImage          = "clickhouse/clickhouse-server:25.8-alpine"
	clickhouseTestDBPrefix   = "t_"
	clickhouseTTLDays        = 90
	clickhouseSetupTimeout   = 3 * time.Minute
	clickhouseCleanupTimeout = 30 * time.Second
)

// clickhouseFixture lazily starts one ClickHouse container per test binary when no URL is configured.
type clickhouseFixture struct {
	containerOnce sync.Once
	containerURL  string
	containerErr  error
}

var sharedClickHouse = &clickhouseFixture{}

// ClickHouse returns a connection to a fresh database ("t_" + 16 hex
// characters) migrated with store/clickhouse.Migrate (TTL 90 days). The
// connection is closed and the database dropped when the test ends. Skips in
// -short mode.
func ClickHouse(t testing.TB) chdriver.Conn {
	t.Helper()
	dsn := ClickHouseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseSetupTimeout)
	defer cancel()
	conn, err := chstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: connect test clickhouse database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// ClickHouseURL is like ClickHouse but returns the URL of the migrated test
// database, for code under test that opens its own connection.
func ClickHouseURL(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires ClickHouse; skipped in -short mode")
	}
	return sharedClickHouse.newDatabase(t)
}

func (f *clickhouseFixture) newDatabase(t testing.TB) string {
	t.Helper()
	base, err := f.baseURL()
	if err != nil {
		t.Fatalf("testutil: start clickhouse container (set %s to use a running server): %v", ClickHouseURLEnv, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseSetupTimeout)
	defer cancel()

	name := randomClickHouseDatabaseName()
	dsn, err := clickhouseDatabaseURL(base, name)
	if err != nil {
		t.Fatalf("testutil: %v", err)
	}
	if err := execClickHouse(ctx, base, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("testutil: create test clickhouse database: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), clickhouseCleanupTimeout)
		defer ccancel()
		if err := execClickHouse(cctx, base, "DROP DATABASE IF EXISTS "+name+" SYNC"); err != nil {
			t.Errorf("testutil: drop test clickhouse database %s: %v", name, err)
		}
	})

	conn, err := chstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("testutil: connect test clickhouse database: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := chstore.Migrate(ctx, conn, clickhouseTTLDays); err != nil {
		t.Fatalf("testutil: migrate test clickhouse database: %v", err)
	}
	return dsn
}

// baseURL returns the configured URL or starts the shared container once.
func (f *clickhouseFixture) baseURL() (string, error) {
	if v := strings.TrimSpace(os.Getenv(ClickHouseURLEnv)); v != "" {
		return v, nil
	}
	f.containerOnce.Do(func() {
		f.containerURL, f.containerErr = startClickHouseContainer()
	})
	return f.containerURL, f.containerErr
}

// startClickHouseContainer starts a ClickHouse container; Ryuk reaps it when the test binary exits.
func startClickHouseContainer() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clickhouseSetupTimeout)
	defer cancel()
	c, err := tcclickhouse.Run(ctx, clickhouseImage)
	if err != nil {
		return "", fmt.Errorf("run %s: %w", clickhouseImage, err)
	}
	dsn, err := c.ConnectionString(ctx)
	if err != nil {
		return "", fmt.Errorf("clickhouse container connection string: %w", err)
	}
	return dsn, nil
}

// execClickHouse runs one statement on a short-lived connection to base.
func execClickHouse(ctx context.Context, base, stmt string) error {
	conn, err := chstore.Open(ctx, base)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Exec(ctx, stmt)
}

// clickhouseDatabaseURL returns base with its database replaced by name.
func clickhouseDatabaseURL(base, name string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		// url.Parse errors echo the input, which may contain a password.
		return "", errors.New("base ClickHouse URL is not a valid URL")
	}
	if u.Scheme != "clickhouse" && u.Scheme != "tcp" {
		return "", fmt.Errorf("base ClickHouse URL has scheme %q, want clickhouse:// or tcp://", u.Scheme)
	}
	u.Path = "/" + name
	u.RawPath = ""
	return u.String(), nil
}

func randomClickHouseDatabaseName() string {
	var b [8]byte
	// crypto/rand.Read never returns an error (it aborts the process instead).
	_, _ = rand.Read(b[:])
	return clickhouseTestDBPrefix + hex.EncodeToString(b[:])
}
