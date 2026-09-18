package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// indexes00003 maps every index created by migration 00003 to its table and
// key definition as reported by pg_get_indexdef.
var indexes00003 = map[string]struct{ table, keys string }{
	"audit_logs_tenant_id_resource_kind_resource_id_created_at_idx": {"audit_logs", "(tenant_id, resource_kind, resource_id, created_at DESC)"},
	"proxies_url_kek_id_idx":                  {"proxies", "(url_kek_id)"},
	"notification_channels_config_kek_id_idx": {"notification_channels", "(config_kek_id)"},
	"identities_site_id_client_id_idx":        {"identities", "(site_id, client, id)"},
	"risk_events_identity_id_created_at_idx":  {"risk_events", "(identity_id, created_at DESC)"},
	"risk_events_proxy_id_created_at_idx":     {"risk_events", "(proxy_id, created_at DESC)"},
	"state_events_action_created_at_idx":      {"state_events", "(action, created_at DESC)"},
	"alert_events_created_at_idx":             {"alert_events", "(created_at)"},
}

// parentIndexDefs returns the definitions of the indexes of indexes00003
// that exist. Partition indexes have generated names and are not returned.
func parentIndexDefs(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	names := make([]string, 0, len(indexes00003))
	for n := range indexes00003 {
		names = append(names, n)
	}
	rows, err := pool.Query(ctx, `
		SELECT i.relname, pg_get_indexdef(i.oid)
		FROM pg_class i
		JOIN pg_namespace n ON n.oid = i.relnamespace
		WHERE n.nspname = current_schema() AND i.relkind IN ('i', 'I') AND i.relname = ANY($1)`, names)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out[name] = def
	}
	require.NoError(t, rows.Err())
	return out
}

func secretPathConstraintExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'secrets_path_segments_check')`).Scan(&exists))
	return exists
}

func TestMigration00003Indexes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	defs := parentIndexDefs(ctx, t, pool)
	require.Len(t, defs, len(indexes00003))
	for name, want := range indexes00003 {
		require.Contains(t, defs[name], " ON ", name)
		require.Contains(t, defs[name], want.table+" ", name)
		require.Contains(t, defs[name], want.keys, name)
	}

	// Partitioned parent indexes are attached to every partition.
	for name, idx := range indexes00003 {
		var partitions, attached int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM pg_inherits WHERE inhparent = $1::regclass),
			       (SELECT count(*) FROM pg_inherits WHERE inhparent = $2::regclass)`,
			idx.table, name).Scan(&partitions, &attached))
		require.Equal(t, partitions, attached, "index %s must be attached to every partition of %s", name, idx.table)
	}
	var riskPartitions int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'risk_events'::regclass").Scan(&riskPartitions))
	require.Positive(t, riskPartitions)

	require.True(t, secretPathConstraintExists(ctx, t, pool))
}

func TestSecretPathSegmentsConstraint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	for _, s := range []string{
		`INSERT INTO tenants (id, name) VALUES ('ten_p', 'acme')`,
		`INSERT INTO namespaces (id, tenant_id, name) VALUES ('ns_p', 'ten_p', 'prod')`,
	} {
		_, err := pool.Exec(ctx, s)
		require.NoError(t, err)
	}

	tests := []struct {
		path string
		ok   bool
	}{
		{"db", true},
		{"db/password", true},
		{"a.b/c..d/.e/f.", true},
		{"a/b-c/d_e/0", true},
		{"", false},
		{".", false},
		{"..", false},
		{"/db", false},
		{"db/", false},
		{"db//password", false},
		{"db/./password", false},
		{"db/../password", false},
		{"db/.", false},
		{"db/..", false},
		{"./db", false},
		{"../db", false},
	}
	for i, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			id := "sec_" + string(rune('a'+i))
			_, err := pool.Exec(ctx, `INSERT INTO secrets (id, namespace_id, path) VALUES ($1, 'ns_p', $2)`, id, tc.path)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(postgres.MapError(err, "secret")), err.Error())
		})
	}
}

func TestMigration00003RollsBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2)

	require.NoError(t, postgres.Migrate(ctx, pool))
	require.Len(t, parentIndexDefs(ctx, t, pool), len(indexes00003))
	require.True(t, secretPathConstraintExists(ctx, t, pool))

	require.NoError(t, postgres.MigrateDownTo(ctx, pool, 2))
	require.Empty(t, parentIndexDefs(ctx, t, pool))
	require.False(t, secretPathConstraintExists(ctx, t, pool))
	tables := existingTables(ctx, t, pool)
	require.True(t, tables["risk_events"], "rolling back 00003 keeps the partitioned tables")

	require.NoError(t, postgres.Migrate(ctx, pool))
	require.Len(t, parentIndexDefs(ctx, t, pool), len(indexes00003))
	require.True(t, secretPathConstraintExists(ctx, t, pool))
}
