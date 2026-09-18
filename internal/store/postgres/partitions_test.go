package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/appconfig"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func TestPartitionedTables(t *testing.T) {
	tables := postgres.PartitionedTables()
	require.Len(t, tables, 8)
	want := map[string]postgres.Granularity{
		"state_events":            postgres.GranularityMonth,
		"risk_events":             postgres.GranularityDay,
		"outcome_stats_minutely":  postgres.GranularityDay,
		"identity_stats_hourly":   postgres.GranularityMonth,
		"acquire_stats_minutely":  postgres.GranularityDay,
		"node_stats_minutely":     postgres.GranularityDay,
		"payload_access_minutely": postgres.GranularityDay,
		"audit_logs":              postgres.GranularityMonth,
	}
	for _, tbl := range tables {
		require.Equal(t, want[tbl.Name], tbl.Granularity, tbl.Name)
	}
	tables[0].Name = "mutated"
	require.Equal(t, "state_events", postgres.PartitionedTables()[0].Name, "result must be a fresh copy")
}

func TestEnsurePartitionWindow(t *testing.T) {
	cst := time.FixedZone("UTC+8", 8*3600)
	tests := []struct {
		name     string
		g        postgres.Granularity
		now      time.Time
		wantFrom time.Time
		wantTo   time.Time
		wantErr  bool
	}{
		{
			name:     "day",
			g:        postgres.GranularityDay,
			now:      time.Date(2031, 3, 31, 15, 0, 0, 0, cst),
			wantFrom: time.Date(2031, 3, 30, 7, 0, 0, 0, time.UTC),
			wantTo:   time.Date(2031, 4, 7, 7, 0, 0, 0, time.UTC),
		},
		{
			name:     "month at end of a long month",
			g:        postgres.GranularityMonth,
			now:      time.Date(2031, 3, 31, 23, 0, 0, 0, time.UTC),
			wantFrom: time.Date(2031, 2, 1, 0, 0, 0, 0, time.UTC),
			wantTo:   time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "month across a year boundary",
			g:        postgres.GranularityMonth,
			now:      time.Date(2031, 1, 1, 3, 0, 0, 0, cst), // 2030-12-31T19:00Z
			wantFrom: time.Date(2030, 11, 1, 0, 0, 0, 0, time.UTC),
			wantTo:   time.Date(2031, 3, 1, 0, 0, 0, 0, time.UTC),
		},
		{name: "unsupported", g: "week", now: time.Now(), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			from, to, err := postgres.EnsurePartitionWindow(tc.g, tc.now)
			if tc.wantErr {
				require.ErrorContains(t, err, "unsupported partition granularity")
				return
			}
			require.NoError(t, err)
			require.True(t, tc.wantFrom.Equal(from), "from %s", from)
			require.True(t, tc.wantTo.Equal(to), "to %s", to)
		})
	}
}

func TestRetentionFor(t *testing.T) {
	r := appconfig.Retention{
		RiskEvents:  1 * time.Hour,
		MinuteStats: 2 * time.Hour,
		HourStats:   3 * time.Hour,
		StateEvents: 4 * time.Hour,
		Audit:       5 * time.Hour,
	}
	tests := map[string]time.Duration{
		"risk_events":             time.Hour,
		"outcome_stats_minutely":  2 * time.Hour,
		"acquire_stats_minutely":  2 * time.Hour,
		"node_stats_minutely":     2 * time.Hour,
		"payload_access_minutely": 2 * time.Hour,
		"identity_stats_hourly":   3 * time.Hour,
		"state_events":            4 * time.Hour,
		"audit_logs":              5 * time.Hour,
		"tenants":                 0,
	}
	for table, want := range tests {
		require.Equal(t, want, postgres.RetentionFor(table, r), table)
	}
	for _, tbl := range postgres.PartitionedTables() {
		require.NotZero(t, postgres.RetentionFor(tbl.Name, r), "retention mapping missing for %s", tbl.Name)
	}
}

func TestPartitionManagerNilPool(t *testing.T) {
	require.ErrorContains(t, postgres.EnsurePartitions(context.Background(), nil, time.Now()), "pool is nil")
	require.ErrorContains(t,
		postgres.DropExpiredPartitions(context.Background(), nil, appconfig.Retention{}, time.Now()), "pool is nil")
}

// partitionNames returns the partition names of table, sorted.
func partitionNames(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname::text FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = to_regclass($1)
		ORDER BY c.relname`, table)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return names
}

func expectedPartitionNames(t *testing.T, table string, g postgres.Granularity, now time.Time) []string {
	t.Helper()
	from, to, err := postgres.EnsurePartitionWindow(g, now)
	require.NoError(t, err)
	var names []string
	switch g {
	case postgres.GranularityDay:
		for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC); !d.After(to); d = d.AddDate(0, 0, 1) {
			names = append(names, table+"_p"+d.Format("20060102"))
		}
	case postgres.GranularityMonth:
		for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(to); m = m.AddDate(0, 1, 0) {
			names = append(names, table+"_p"+m.Format("200601"))
		}
	}
	return names
}

func TestPartitionsFromMigrationAcceptCurrentRows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	inserts := map[string]string{
		"state_events":            `INSERT INTO state_events (id, created_at, tenant_id, namespace_id, subject_kind, subject_id) VALUES ($1, $2, 'ten_x', 'ns_x', 'identity', 'idt_x')`,
		"risk_events":             `INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, outcome) VALUES ($1, $2, 'ten_x', 'ns_x', 'sit_x', 'captcha')`,
		"outcome_stats_minutely":  `INSERT INTO outcome_stats_minutely (bucket, namespace_id, site_id, outcome) VALUES ($2, $1, 'sit_x', 'success')`,
		"identity_stats_hourly":   `INSERT INTO identity_stats_hourly (bucket, site_id, identity_id, outcome) VALUES ($2, 'sit_x', $1, 'success')`,
		"acquire_stats_minutely":  `INSERT INTO acquire_stats_minutely (bucket, namespace_id, site_id, result) VALUES ($2, $1, 'sit_x', 'ok')`,
		"node_stats_minutely":     `INSERT INTO node_stats_minutely (bucket, namespace_id, node) VALUES ($2, $1, 'node-1')`,
		"payload_access_minutely": `INSERT INTO payload_access_minutely (bucket, namespace_id, token_id, identity_type_id) VALUES ($2, $1, 'tok_x', 'ity_x')`,
		"audit_logs":              `INSERT INTO audit_logs (id, created_at, actor_kind, action) VALUES ($1, $2, 'system', 'test')`,
	}
	require.Len(t, inserts, len(postgres.PartitionedTables()))

	now := time.Now().UTC()
	offsets := []time.Duration{-24 * time.Hour, 0, 6 * 24 * time.Hour}
	for _, tbl := range postgres.PartitionedTables() {
		for i, off := range offsets {
			_, err := pool.Exec(ctx, inserts[tbl.Name], fmt.Sprintf("row_%d", i), now.Add(off))
			require.NoError(t, err, "%s at now%+v", tbl.Name, off)
		}
	}
}

func TestEnsurePartitions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	now := time.Date(2031, 3, 31, 15, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))

	require.NoError(t, postgres.EnsurePartitions(ctx, pool, now))
	counts := map[string]int{}
	for _, tbl := range postgres.PartitionedTables() {
		names := partitionNames(ctx, t, pool, tbl.Name)
		for _, want := range expectedPartitionNames(t, tbl.Name, tbl.Granularity, now) {
			require.Contains(t, names, want)
		}
		counts[tbl.Name] = len(names)
	}

	// Idempotent.
	require.NoError(t, postgres.EnsurePartitions(ctx, pool, now))
	for _, tbl := range postgres.PartitionedTables() {
		require.Len(t, partitionNames(ctx, t, pool, tbl.Name), counts[tbl.Name], tbl.Name)
	}

	// Boundaries are aligned to UTC midnight / first of month.
	const (
		insertRisk  = `INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, outcome) VALUES ($1, $2, 't', 'n', 's', 'captcha') RETURNING tableoid::regclass::text`
		insertState = `INSERT INTO state_events (id, created_at, tenant_id, namespace_id, subject_kind, subject_id) VALUES ($1, $2, 't', 'n', 'identity', 's') RETURNING tableoid::regclass::text`
	)
	routes := []struct {
		sql  string
		at   time.Time
		want string
	}{
		{insertRisk, time.Date(2031, 3, 30, 23, 59, 59, 0, time.UTC), "risk_events_p20310330"},
		{insertRisk, time.Date(2031, 3, 31, 0, 0, 0, 0, time.UTC), "risk_events_p20310331"},
		{insertState, time.Date(2031, 2, 28, 23, 59, 59, 0, time.UTC), "state_events_p203102"},
		{insertState, time.Date(2031, 6, 1, 0, 0, 0, 0, time.UTC), "state_events_p203106"},
	}
	for i, r := range routes {
		var part string
		require.NoError(t, pool.QueryRow(ctx, r.sql, fmt.Sprintf("route_%d", i), r.at).Scan(&part))
		require.Equal(t, r.want, part)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, postgres.EnsurePartitions(canceled, pool, now), context.Canceled)
}

func TestPartitionFunctionsValidateInput(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	from := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"ensure bad granularity", "SELECT spinneret_ensure_partitions($1, $2, $3, $4)", []any{"risk_events", "week", from, from}, "22023"},
		{"ensure inverted range", "SELECT spinneret_ensure_partitions($1, $2, $3, $4)", []any{"risk_events", "day", from.Add(time.Hour), from}, "22023"},
		{"ensure null bound", "SELECT spinneret_ensure_partitions($1, $2, $3, NULL)", []any{"risk_events", "day", from}, "22023"},
		{"ensure regular table", "SELECT spinneret_ensure_partitions($1, $2, $3, $4)", []any{"tenants", "day", from, from}, "42P01"},
		{"ensure missing table", "SELECT spinneret_ensure_partitions($1, $2, $3, $4)", []any{"no_such_table", "day", from, from}, "42P01"},
		{"drop bad granularity", "SELECT spinneret_drop_partitions_before($1, $2, $3)", []any{"risk_events", "year", from}, "22023"},
		{"drop null cutoff", "SELECT spinneret_drop_partitions_before($1, $2, NULL)", []any{"risk_events", "day"}, "22023"},
		{"drop regular table", "SELECT spinneret_drop_partitions_before($1, $2, $3)", []any{"tenants", "month", from}, "42P01"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql, tc.args...)
			var pgErr *pgconn.PgError
			require.True(t, errors.As(err, &pgErr), "want PgError, got %v", err)
			require.Equal(t, tc.code, pgErr.Code, pgErr.Message)
		})
	}
}

func TestDropPartitionsBeforeBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	day := func(d int) time.Time { return time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC) }

	var created int
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_ensure_partitions('risk_events', 'day', $1, $2)", day(1), day(5)).Scan(&created))
	require.Equal(t, 5, created)
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_ensure_partitions('risk_events', 'day', $1, $2)", day(1), day(5)).Scan(&created))
	require.Zero(t, created, "second call must be a no-op")

	// A partition outside the managed naming scheme is never dropped.
	_, err := pool.Exec(ctx, `CREATE TABLE risk_events_archive PARTITION OF risk_events
		FOR VALUES FROM ('2019-01-01 00:00:00+00') TO ('2019-02-01 00:00:00+00')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, outcome)
		VALUES ('old', $1, 't', 'n', 's', 'captcha')`, day(1).Add(12*time.Hour))
	require.NoError(t, err)

	var dropped int
	// The partition for Jan 3 ends at Jan 4 00:00, after the cutoff: kept.
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_drop_partitions_before('risk_events', 'day', $1)",
		day(3).Add(12*time.Hour)).Scan(&dropped))
	require.Equal(t, 2, dropped)

	names := partitionNames(ctx, t, pool, "risk_events")
	require.NotContains(t, names, "risk_events_p20200101")
	require.NotContains(t, names, "risk_events_p20200102")
	require.Contains(t, names, "risk_events_p20200103")
	require.Contains(t, names, "risk_events_archive")

	var rows int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM risk_events WHERE id = 'old'").Scan(&rows))
	require.Zero(t, rows)

	// Exactly at the upper bound: dropped.
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_drop_partitions_before('risk_events', 'day', $1)", day(4)).Scan(&dropped))
	require.Equal(t, 1, dropped)
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_drop_partitions_before('risk_events', 'day', $1)", day(4)).Scan(&dropped))
	require.Zero(t, dropped, "second call must be a no-op")

	// Monthly naming does not match daily partitions and vice versa.
	require.NoError(t, pool.QueryRow(ctx, "SELECT spinneret_drop_partitions_before('risk_events', 'month', $1)", day(5)).Scan(&dropped))
	require.Zero(t, dropped)
}

func TestDropExpiredPartitions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	created := time.Date(2030, 6, 15, 12, 0, 0, 0, time.UTC)
	require.NoError(t, postgres.EnsurePartitions(ctx, pool, created))
	before := map[string][]string{}
	for _, tbl := range postgres.PartitionedTables() {
		before[tbl.Name] = partitionNames(ctx, t, pool, tbl.Name)
	}

	retention := appconfig.Retention{
		RiskEvents:  48 * time.Hour,
		MinuteStats: 24 * time.Hour,
		HourStats:   0, // keep forever
		StateEvents: 60 * 24 * time.Hour,
		Audit:       -time.Hour, // keep forever
	}
	now := time.Date(2030, 6, 20, 12, 0, 0, 0, time.UTC)
	for range 2 { // idempotent
		require.NoError(t, postgres.DropExpiredPartitions(ctx, pool, retention, now))
	}

	firstKept := map[string]string{
		"risk_events":             "risk_events_p20300618",
		"outcome_stats_minutely":  "outcome_stats_minutely_p20300619",
		"acquire_stats_minutely":  "acquire_stats_minutely_p20300619",
		"node_stats_minutely":     "node_stats_minutely_p20300619",
		"payload_access_minutely": "payload_access_minutely_p20300619",
		"state_events":            "state_events_p203005",
	}
	for _, tbl := range postgres.PartitionedTables() {
		after := partitionNames(ctx, t, pool, tbl.Name)
		keep, ok := firstKept[tbl.Name]
		if !ok {
			require.Equal(t, before[tbl.Name], after, "%s must keep all partitions", tbl.Name)
			continue
		}
		require.NotEmpty(t, after, tbl.Name)
		require.Equal(t, keep, after[0], "%s oldest remaining partition", tbl.Name)
		for _, name := range before[tbl.Name] {
			if name >= keep {
				require.True(t, slices.Contains(after, name), "%s must be kept", name)
			}
		}
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, postgres.DropExpiredPartitions(canceled, pool, retention, now), context.Canceled)
}

func TestPartitionDDLErrorsAreJoined(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2) // no schema: every table fails

	err := postgres.EnsurePartitions(ctx, pool, time.Now())
	require.Error(t, err)
	err2 := postgres.DropExpiredPartitions(ctx, pool, appconfig.Retention{
		RiskEvents: time.Hour, MinuteStats: time.Hour, HourStats: time.Hour, StateEvents: time.Hour, Audit: time.Hour,
	}, time.Now())
	require.Error(t, err2)
	for _, tbl := range postgres.PartitionedTables() {
		require.ErrorContains(t, err, "ensure partitions of "+tbl.Name)
		require.ErrorContains(t, err2, "drop expired partitions of "+tbl.Name)
	}
}
