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

// seedSite creates tenant ten_s, namespace ns_s, site sit_s with endpoint group
// eg_s, identity type ity_s, account acc_s and identity idt_s bound to proxy pxy_s.
func seedSite(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	stmts := []string{
		`INSERT INTO tenants (id, name) VALUES ('ten_s', 'acme')`,
		`INSERT INTO namespaces (id, tenant_id, name) VALUES ('ns_s', 'ten_s', 'prod')`,
		`INSERT INTO sites (id, namespace_id, name) VALUES ('sit_s', 'ns_s', 'shop')`,
		`INSERT INTO endpoint_groups (id, site_id, client, name) VALUES ('eg_s', 'sit_s', 'web', '_default')`,
		`INSERT INTO uri_rules (id, endpoint_group_id, kind, pattern) VALUES ('uri_s', 'eg_s', 'prefix', '/api')`,
		`INSERT INTO identity_types (id, site_id, client, name) VALUES ('ity_s', 'sit_s', 'web', 'cookie')`,
		`INSERT INTO accounts (id, site_id, external_ref) VALUES ('acc_s', 'sit_s', 'user-1')`,
		`INSERT INTO identities (id, site_id, client, type_id, account_id, unique_hash, payload_hash)
		 VALUES ('idt_s', 'sit_s', 'web', 'ity_s', 'acc_s', '\x01', '\x02')`,
		`INSERT INTO identity_payloads (identity_id, version, ciphertext, wrapped_dek, kek_id)
		 VALUES ('idt_s', 1, '\x01', '\x02', 'k1')`,
		`INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext, url_wrapped_dek, url_kek_id)
		 VALUES ('pxy_s', 'ns_s', 'http', 'proxy.local', 8080, 'http://proxy.local:8080', '\x03', '\x04', '\x05', 'k1')`,
		`INSERT INTO proxy_bindings (identity_id, proxy_id) VALUES ('idt_s', 'pxy_s')`,
		`INSERT INTO policies (id, namespace_id, kind, name) VALUES ('pol_s', 'ns_s', 'rotation', 'default-rotation')`,
		`INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id, client, endpoint_group_id)
		 VALUES ('pbd_s', 'pol_s', 'rotation', 'ns_s', 'sit_s', 'web', 'eg_s')`,
		`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, endpoint_group_id) VALUES ('sit_s', 'ie', 'idt_s', 'eg_s')`,
	}
	for _, s := range stmts {
		_, err := pool.Exec(ctx, s)
		require.NoError(t, err, s)
	}
}

func countRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, query, args...).Scan(&n))
	return n
}

func TestSchemaConstraints(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)
	seedSite(ctx, t, pool)

	var siteKey, identityKey int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT hkey FROM sites WHERE id = 'sit_s'").Scan(&siteKey))
	require.NoError(t, pool.QueryRow(ctx, "SELECT hkey FROM identities WHERE id = 'idt_s'").Scan(&identityKey))
	require.Positive(t, siteKey)
	require.Positive(t, identityKey)

	tests := []struct {
		name       string
		sql        string
		wantReason apperr.Reason
	}{
		{"hkey cannot be assigned", `INSERT INTO sites (id, hkey, namespace_id, name) VALUES ('sit_x', 99, 'ns_s', 'x')`, ""},
		{"duplicate site name", `INSERT INTO sites (id, namespace_id, name) VALUES ('sit_dup', 'ns_s', 'shop')`, apperr.ReasonAlreadyExists},
		{"unknown namespace", `INSERT INTO sites (id, namespace_id, name) VALUES ('sit_x', 'ns_missing', 'x')`, apperr.ReasonFailedPrecondition},
		{"invalid identity state", `UPDATE identities SET state = 'zombie' WHERE id = 'idt_s'`, apperr.ReasonFailedPrecondition},
		{"invalid proxy port", `UPDATE proxies SET port = 70000 WHERE id = 'pxy_s'`, apperr.ReasonFailedPrecondition},
		{"invalid role", `INSERT INTO users (id, username, password_hash) VALUES ('usr_1', 'bob', 'x');
			INSERT INTO role_bindings (id, user_id, tenant_id, role) VALUES ('rb_1', 'usr_1', 'ten_s', 'god')`, apperr.ReasonFailedPrecondition},
		{"upper-case username", `INSERT INTO users (id, username, password_hash) VALUES ('usr_2', 'Alice', 'x')`, apperr.ReasonFailedPrecondition},
		{"missing required value", `INSERT INTO tenants (id) VALUES ('ten_x')`, apperr.ReasonFailedPrecondition},
		{"identity type still referenced", `DELETE FROM identity_types WHERE id = 'ity_s'`, apperr.ReasonFailedPrecondition},
		{"invalid secret path", `INSERT INTO secrets (id, namespace_id, path) VALUES ('sec_1', 'ns_s', '/abs')`, apperr.ReasonFailedPrecondition},
		{"duplicate namespace binding", `INSERT INTO policy_bindings (id, policy_id, kind, namespace_id) VALUES ('pbd_1', 'pol_s', 'rotation', 'ns_s');
			INSERT INTO policy_bindings (id, policy_id, kind, namespace_id) VALUES ('pbd_2', 'pol_s', 'rotation', 'ns_s')`, apperr.ReasonAlreadyExists},
		{"endpoint group binding without site", `INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, endpoint_group_id)
			VALUES ('pbd_3', 'pol_s', 'rotation', 'ns_s', 'eg_s')`, apperr.ReasonFailedPrecondition},
		{"invalid acquire result", `INSERT INTO acquire_stats_minutely (bucket, namespace_id, site_id, result) VALUES (now(), 'ns_s', 'sit_s', 'maybe')`, apperr.ReasonFailedPrecondition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, tc.sql)
			require.Error(t, err)
			if tc.wantReason == "" {
				return
			}
			require.Equal(t, tc.wantReason, apperr.ReasonOf(postgres.MapError(err, "row")), err.Error())
		})
	}

	t.Run("deleting an account detaches identities", func(t *testing.T) {
		_, err := pool.Exec(ctx, "DELETE FROM accounts WHERE id = 'acc_s'")
		require.NoError(t, err)
		require.Equal(t, 1, countRows(ctx, t, pool, "SELECT count(*) FROM identities WHERE id = 'idt_s' AND account_id IS NULL"))
	})

	t.Run("deleting a site cascades to its children", func(t *testing.T) {
		_, err := pool.Exec(ctx, "DELETE FROM sites WHERE id = 'sit_s'")
		require.NoError(t, err)
		for _, table := range []string{"endpoint_groups", "uri_rules", "identity_types", "identities",
			"identity_payloads", "proxy_bindings", "policy_bindings", "hot_state_snapshots"} {
			require.Zero(t, countRows(ctx, t, pool, "SELECT count(*) FROM "+table), table)
		}
		require.Equal(t, 1, countRows(ctx, t, pool, "SELECT count(*) FROM proxies"), "proxies belong to the namespace")
	})

	t.Run("sites block namespace deletion, policies cascade", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO sites (id, namespace_id, name) VALUES ('sit_t', 'ns_s', 'market')`)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM namespaces WHERE id = 'ns_s'")
		require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(postgres.MapError(err, "namespace")))

		_, err = pool.Exec(ctx, "DELETE FROM sites WHERE id = 'sit_t'")
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM proxies")
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM namespaces WHERE id = 'ns_s'")
		require.NoError(t, err)
		require.Zero(t, countRows(ctx, t, pool, "SELECT count(*) FROM policies"))
	})
}
