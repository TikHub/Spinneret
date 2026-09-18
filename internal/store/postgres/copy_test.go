package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/testutil"
)

type nodeStat struct {
	Bucket   time.Time
	Node     string
	Acquires int64
}

var nodeStatColumns = []string{"bucket", "namespace_id", "node", "acquires"}

func nodeStatValues(s nodeStat) []any { return []any{s.Bucket, "ns_copy", s.Node, s.Acquires} }

type failingCopier struct{ err error }

func (f failingCopier) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, src pgx.CopyFromSource) (int64, error) {
	for src.Next() {
		if _, err := src.Values(); err != nil {
			return 0, err
		}
	}
	return 0, f.err
}

func TestCopyRowsValidation(t *testing.T) {
	ctx := context.Background()
	rows := []nodeStat{{Node: "a"}}
	tests := []struct {
		name    string
		c       postgres.CopyFromer
		table   string
		columns []string
		values  func(nodeStat) []any
		wantErr string
	}{
		{name: "nil destination", c: nil, table: "t", columns: nodeStatColumns, values: nodeStatValues, wantErr: "destination is nil"},
		{name: "missing table", c: failingCopier{}, table: "", columns: nodeStatColumns, values: nodeStatValues, wantErr: "table and columns are required"},
		{name: "missing columns", c: failingCopier{}, table: "t", columns: nil, values: nodeStatValues, wantErr: "table and columns are required"},
		{name: "nil values", c: failingCopier{}, table: "t", columns: nodeStatColumns, values: nil, wantErr: "values function is nil"},
		{
			name: "value count mismatch", c: failingCopier{}, table: "t", columns: nodeStatColumns,
			values: func(nodeStat) []any { return []any{1} }, wantErr: "row 0 has 1 values, want 4",
		},
		{
			name: "copy failure", c: failingCopier{err: errors.New("broken pipe")}, table: "t", columns: nodeStatColumns,
			values: nodeStatValues, wantErr: "copy rows into t: broken pipe",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := postgres.CopyRows(ctx, tc.c, tc.table, tc.columns, rows, tc.values)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	n, err := postgres.CopyRows(ctx, failingCopier{err: errors.New("unused")}, "t", nodeStatColumns, []nodeStat{}, nodeStatValues)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestCopyRows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	bucket := time.Now().UTC().Truncate(time.Minute)

	stats := []nodeStat{
		{Bucket: bucket, Node: "node-a", Acquires: 3},
		{Bucket: bucket, Node: "node-b", Acquires: 5},
	}
	n, err := postgres.CopyRows(ctx, pool, "public.node_stats_minutely", nodeStatColumns, stats, nodeStatValues)
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	var total int64
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT sum(acquires) FROM node_stats_minutely WHERE namespace_id = 'ns_copy'").Scan(&total))
	require.Equal(t, int64(8), total)

	// Duplicate primary keys fail the whole COPY.
	_, err = postgres.CopyRows(ctx, pool, "node_stats_minutely", nodeStatColumns, stats[:1], nodeStatValues)
	require.Error(t, err)
}
