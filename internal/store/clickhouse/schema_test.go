package clickhouse

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

func TestCreateSQL(t *testing.T) {
	sql := reportEventsSpec().createSQL(90)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS report_events (",
		"event_time DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),",
		"tenant_id LowCardinality(String),",
		"markers Array(LowCardinality(String)),",
		"http_status UInt16,",
		"probe UInt8,",
		"INDEX idx_report_id report_id TYPE bloom_filter(0.01) GRANULARITY 4\n",
		") ENGINE = MergeTree",
		"PARTITION BY toYYYYMMDD(event_time)",
		"ORDER BY (namespace_id, site, endpoint_group, event_time)",
		"TTL toDateTime(event_time) + INTERVAL 90 DAY",
	} {
		require.Contains(t, sql, want)
	}

	spec := tableSpec{name: "x", columns: []column{{"event_time", timestamp}}, orderBy: "event_time"}
	require.Contains(t, spec.createSQL(1), "event_time DateTime64(3, 'UTC')\n) ENGINE")

	require.Equal(t,
		"INSERT INTO lease_events (event_time, tenant_id, namespace_id, site_id, site, endpoint_group, client, identity_id, proxy_id, lease_id, node, token_id, event, result, duration_us, probe, sticky)",
		leaseEventsSpec().insertSQL())
}

func TestSpecsMatchEventStructs(t *testing.T) {
	// The Append calls in writer.go pass one value per column in this order.
	require.Len(t, reportEventsSpec().columns, 31)
	require.Len(t, leaseEventsSpec().columns, 17)
}

func TestParseTTLDays(t *testing.T) {
	tests := []struct {
		name   string
		engine string
		days   int
		ok     bool
	}{
		{"normalized", "MergeTree PARTITION BY toYYYYMMDD(event_time) ORDER BY (namespace_id) TTL toDateTime(event_time) + toIntervalDay(90) SETTINGS index_granularity = 8192", 90, true},
		{"single day", "MergeTree TTL toDateTime(event_time) + toIntervalDay(1)", 1, true},
		{"no ttl", "MergeTree ORDER BY event_time SETTINGS index_granularity = 8192", 0, false},
		{"other interval unit", "MergeTree TTL toDateTime(event_time) + toIntervalMonth(3)", 0, false},
		{"overflow", "MergeTree TTL toDateTime(event_time) + toIntervalDay(99999999999999999999)", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			days, ok := parseTTLDays(tt.engine)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.days, days)
		})
	}
}

type tableInfo struct {
	engineFull   string
	partitionKey string
	sortingKey   string
}

func loadTable(t *testing.T, conn chdriver.Conn, table string) tableInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var info tableInfo
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT engine_full, partition_key, sorting_key FROM system.tables WHERE database = currentDatabase() AND name = ?",
		table).Scan(&info.engineFull, &info.partitionKey, &info.sortingKey))
	return info
}

func loadColumns(t *testing.T, conn chdriver.Conn, table string) []column {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx,
		"SELECT name, type FROM system.columns WHERE database = currentDatabase() AND table = ? ORDER BY position", table)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var cols []column
	for rows.Next() {
		var c column
		require.NoError(t, rows.Scan(&c.name, &c.typ))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	return cols
}

// baseType strips a trailing CODEC clause from a column definition.
func baseType(typ string) string {
	if i := strings.Index(typ, " CODEC("); i >= 0 {
		return typ[:i]
	}
	return typ
}

func TestMigrate(t *testing.T) {
	conn := testConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.NoError(t, Migrate(ctx, conn, 90))
	require.NoError(t, Migrate(ctx, conn, 90), "idempotent")
	// Re-running with the same retention must not start a TTL materialization mutation.
	require.Zero(t, countMutations(t, conn))

	for _, spec := range []tableSpec{reportEventsSpec(), leaseEventsSpec()} {
		info := loadTable(t, conn, spec.name)
		require.Contains(t, info.engineFull, "MergeTree")
		require.Contains(t, info.engineFull, "toIntervalDay(90)")
		require.Contains(t, info.engineFull, "ttl_only_drop_parts = 1")
		require.Equal(t, "toYYYYMMDD(event_time)", info.partitionKey)
		require.Equal(t, "namespace_id, site, endpoint_group, event_time", info.sortingKey)

		cols := loadColumns(t, conn, spec.name)
		require.Len(t, cols, len(spec.columns))
		for i, c := range spec.columns {
			require.Equal(t, c.name, cols[i].name)
			require.Equal(t, baseType(c.typ), cols[i].typ, c.name)
		}
	}

	// Changing the retention rewrites the TTL.
	require.NoError(t, Migrate(ctx, conn, 30))
	require.Contains(t, loadTable(t, conn, ReportEventsTable).engineFull, "toIntervalDay(30)")
	require.Contains(t, loadTable(t, conn, LeaseEventsTable).engineFull, "toIntervalDay(30)")
	require.Positive(t, countMutations(t, conn))

	// The largest accepted retention must not overflow the TTL expression:
	// a fresh row stays visible instead of expiring on insert.
	require.NoError(t, Migrate(ctx, conn, MaxTTLDays))
	require.NoError(t, conn.Exec(ctx, "INSERT INTO lease_events (event_time, lease_id) VALUES (now64(3), 'ttl-max')"))
	var expires time.Time
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT toDateTime(event_time) + INTERVAL "+strconv.Itoa(MaxTTLDays)+" DAY FROM lease_events WHERE lease_id = 'ttl-max'").
		Scan(&expires))
	require.True(t, expires.After(time.Now()), "TTL for %d days must lie in the future, got %s", MaxTTLDays, expires)
	require.Equal(t, uint64(1), countRows(t, conn, LeaseEventsTable))
}

// countMutations returns the number of mutations recorded for the current database.
func countMutations(t *testing.T, conn chdriver.Conn) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM system.mutations WHERE database = currentDatabase()").Scan(&n))
	return n
}

func TestMigrateEvolvesExistingTable(t *testing.T) {
	conn := testConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// An older table: a subset of the columns, no TTL, no skipping indexes.
	require.NoError(t, conn.Exec(ctx, `CREATE TABLE report_events (
		event_time DateTime64(3, 'UTC'),
		namespace_id LowCardinality(String),
		site LowCardinality(String),
		endpoint_group LowCardinality(String),
		outcome LowCardinality(String)
	) ENGINE = MergeTree PARTITION BY toYYYYMMDD(event_time) ORDER BY (namespace_id, site, endpoint_group, event_time)`))

	require.NoError(t, Migrate(ctx, conn, 7))

	cols := loadColumns(t, conn, ReportEventsTable)
	spec := reportEventsSpec()
	require.Len(t, cols, len(spec.columns))
	for i, c := range spec.columns {
		require.Equal(t, c.name, cols[i].name, "column order")
	}
	require.Contains(t, loadTable(t, conn, ReportEventsTable).engineFull, "toIntervalDay(7)")

	var indexes uint64
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count() FROM system.data_skipping_indices WHERE database = currentDatabase() AND table = ?",
		ReportEventsTable).Scan(&indexes))
	require.Equal(t, uint64(len(spec.indexes)), indexes)
}

func TestMigrateErrors(t *testing.T) {
	ctx := context.Background()
	require.ErrorContains(t, Migrate(ctx, nil, 90), "nil connection")

	conn := testConn(t)
	require.ErrorContains(t, Migrate(ctx, conn, 0), "ttl days")
	require.ErrorContains(t, Migrate(ctx, conn, -5), "ttl days")
	require.ErrorContains(t, Migrate(ctx, conn, MaxTTLDays+1), "ttl days")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, Migrate(canceled, conn, 90))

	// A view with the same name cannot be migrated.
	require.NoError(t, conn.Exec(ctx, "CREATE VIEW lease_events AS SELECT 1 AS x"))
	require.NoError(t, conn.Exec(ctx, "CREATE VIEW report_events AS SELECT 1 AS x"))
	require.Error(t, Migrate(ctx, conn, 90))
}
