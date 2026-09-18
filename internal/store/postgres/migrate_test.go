package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// specColumns lists every table and column required by section 4 of the
// implementation spec.
var specColumns = map[string][]string{
	"tenants":    {"id", "name", "display_name", "description", "created_at", "updated_at"},
	"namespaces": {"id", "tenant_id", "name", "display_name", "description", "created_at", "updated_at"},
	"users": {"id", "username", "display_name", "email", "password_hash", "is_platform_admin", "disabled", "locale",
		"last_login_at", "last_login_ip", "password_changed_at", "created_at", "updated_at"},
	"role_bindings": {"id", "user_id", "tenant_id", "role", "namespace_id", "site_ids", "extra_permissions",
		"created_by", "created_at"},
	"api_tokens": {"id", "tenant_id", "namespace_id", "name", "description", "token_prefix", "token_hash", "scopes",
		"ip_allowlist", "rate_limit_rps", "expires_at", "revoked_at", "last_used_at", "last_used_ip", "created_by",
		"created_at"},
	"sites": {"id", "hkey", "namespace_id", "name", "display_name", "description", "clients", "paused",
		"paused_reason", "paused_at", "paused_by", "created_at", "updated_at"},
	"endpoint_groups": {"id", "hkey", "site_id", "client", "name", "description", "low_watermark", "created_at",
		"updated_at"},
	"uri_rules": {"id", "endpoint_group_id", "kind", "pattern", "position", "created_at", "updated_at"},
	"identity_types": {"id", "site_id", "client", "name", "description", "spec", "spec_yaml", "json_schema",
		"version", "created_at", "updated_at"},
	"accounts": {"id", "hkey", "site_id", "external_ref", "region", "tags", "state", "ban_until", "cooldown_until",
		"notes", "created_at", "updated_at"},
	"identities": {"id", "hkey", "site_id", "client", "type_id", "account_id", "state", "state_reason",
		"state_changed_at", "ban_until", "quarantine_until", "region", "tags", "labels", "unique_hash",
		"payload_hash", "payload_version", "activated_at", "last_used_at", "created_by", "created_at", "updated_at"},
	"identity_payloads": {"identity_id", "version", "ciphertext", "wrapped_dek", "kek_id", "created_by",
		"created_at"},
	"proxies": {"id", "hkey", "namespace_id", "scheme", "host", "port", "username_hint", "display_url", "url_hash",
		"url_ciphertext", "url_wrapped_dek", "url_kek_id", "url_version", "kind", "region", "city", "provider",
		"max_concurrency", "tags", "session_template", "state", "state_reason", "state_changed_at", "ban_until",
		"cooldown_until", "consecutive_check_failures", "last_check_at", "last_check_ok", "last_latency_ms",
		"exit_ip", "next_check_at", "created_at", "updated_at"},
	"proxy_bindings": {"identity_id", "proxy_id", "bound_at", "rebind_day", "rebinds_today"},
	"policies": {"id", "namespace_id", "kind", "name", "description", "current_version", "draft_yaml",
		"draft_updated_by", "draft_updated_at", "created_by", "created_at", "updated_at"},
	"policy_versions": {"policy_id", "version", "spec", "spec_yaml", "comment", "created_by", "created_at"},
	"policy_bindings": {"id", "policy_id", "kind", "namespace_id", "site_id", "client", "endpoint_group_id",
		"created_by", "created_at"},
	"hot_state_snapshots": {"site_id", "subject", "subject_id", "endpoint_group_id", "score", "samples",
		"consecutive_failures", "last_failure_at", "cooldown_until", "reuse_until", "last_used_at", "updated_at"},
	"state_events": {"id", "created_at", "tenant_id", "namespace_id", "site_id", "subject_kind", "subject_id",
		"endpoint_group_id", "from_state", "to_state", "action", "scope", "until", "permanent", "outcome",
		"policy_id", "policy_version", "rule", "report_id", "lease_id", "actor", "reason", "shadow", "details"},
	"risk_events": {"id", "created_at", "tenant_id", "namespace_id", "site_id", "endpoint_group_id",
		"identity_id", "proxy_id", "lease_id", "report_id", "node", "token_id", "uri", "method", "http_status",
		"business_code", "error_kind", "markers", "outcome", "blame", "rule", "latency_ms", "response_bytes",
		"started_at", "finished_at"},
	"outcome_stats_minutely": {"bucket", "namespace_id", "site_id", "endpoint_group_id", "proxy_id", "outcome",
		"count", "latency_ms_sum", "response_bytes_sum"},
	"identity_stats_hourly":   {"bucket", "site_id", "identity_id", "endpoint_group_id", "outcome", "count"},
	"acquire_stats_minutely":  {"bucket", "namespace_id", "site_id", "endpoint_group_id", "result", "count", "duration_us_sum"},
	"node_stats_minutely":     {"bucket", "namespace_id", "node", "acquires", "reports", "abandoned", "rejected"},
	"payload_access_minutely": {"bucket", "namespace_id", "token_id", "identity_type_id", "count"},
	"breaker_events": {"id", "created_at", "tenant_id", "namespace_id", "site_id", "endpoint_group_id",
		"from_state", "to_state", "trigger", "reason", "open_until", "metrics", "actor"},
	"config_items": {"id", "namespace_id", "group_name", "key", "format", "schema", "description",
		"current_version", "draft_content", "draft_updated_by", "draft_updated_at", "created_by", "created_at",
		"updated_at"},
	"config_versions": {"item_id", "version", "content", "comment", "source_version", "published_by",
		"published_at"},
	"secrets": {"id", "namespace_id", "path", "description", "tags", "current_version", "expires_at",
		"last_accessed_at", "created_by", "created_at", "updated_at"},
	"secret_versions": {"secret_id", "version", "ciphertext", "wrapped_dek", "kek_id", "created_by", "created_at"},
	"audit_logs": {"id", "created_at", "tenant_id", "namespace_id", "actor_kind", "actor_id", "actor_name",
		"action", "resource_kind", "resource_id", "resource_name", "result", "ip", "user_agent", "details"},
	"notification_channels": {"id", "tenant_id", "namespace_id", "name", "kind", "config_ciphertext",
		"config_wrapped_dek", "config_kek_id", "event_types", "site_ids", "min_severity", "enabled",
		"last_delivery_at", "last_delivery_status", "created_by", "created_at", "updated_at"},
	"alert_events": {"id", "created_at", "tenant_id", "namespace_id", "site_id", "kind", "severity", "title",
		"message", "details", "dedup_key", "deliveries"},
	"system_keys":     {"name", "ciphertext", "wrapped_dek", "kek_id", "created_at", "updated_at"},
	"system_settings": {"key", "value", "updated_at"},
}

var partitionFunctions = []string{"spinneret_ensure_partitions", "spinneret_drop_partitions_before"}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func openBlank(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dsn := testutil.PostgresBlankURL(t)
	pool, err := postgres.Open(integrationContext(t), dsn, maxConns)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func existingTables(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND NOT c.relispartition`)
	require.NoError(t, err)
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func functionCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM pg_proc WHERE proname = ANY($1)", partitionFunctions).Scan(&n))
	return n
}

func TestLatestMigrationVersion(t *testing.T) {
	v, err := postgres.LatestMigrationVersion()
	require.NoError(t, err)
	// 00002_partitioned.sql is the last migration of the initial schema; later
	// migrations only add versions.
	require.GreaterOrEqual(t, v, int64(2))
}

func TestMigrateRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 1) // a single-connection pool must not deadlock with the lock connection
	latest, err := postgres.LatestMigrationVersion()
	require.NoError(t, err)

	v, err := postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Zero(t, v)
	require.NotContains(t, existingTables(ctx, t, pool), "goose_db_version", "MigrationVersion must not create the version table")

	require.NoError(t, postgres.Migrate(ctx, pool))
	v, err = postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, latest, v)
	tables := existingTables(ctx, t, pool)
	for table := range specColumns {
		require.True(t, tables[table], "table %s missing after migrate", table)
	}
	require.Equal(t, len(partitionFunctions), functionCount(ctx, t, pool))

	// Migrate is idempotent.
	require.NoError(t, postgres.Migrate(ctx, pool))

	// Partial rollback removes only the partitioned tables.
	require.NoError(t, postgres.MigrateDownTo(ctx, pool, 1))
	v, err = postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, int64(1), v)
	tables = existingTables(ctx, t, pool)
	require.True(t, tables["identities"])
	require.False(t, tables["state_events"])
	require.Zero(t, functionCount(ctx, t, pool))

	// Full rollback leaves only the goose version table.
	require.NoError(t, postgres.MigrateDownTo(ctx, pool, 0))
	v, err = postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Zero(t, v)
	tables = existingTables(ctx, t, pool)
	delete(tables, "goose_db_version")
	require.Empty(t, tables)

	// And up again.
	require.NoError(t, postgres.Migrate(ctx, pool))
	v, err = postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, latest, v)
	require.Equal(t, len(partitionFunctions), functionCount(ctx, t, pool))
}

func TestMigrateDownToInvalidVersion(t *testing.T) {
	err := postgres.MigrateDownTo(context.Background(), nil, -1)
	require.ErrorContains(t, err, "invalid target version")
}

func TestMigrateNilPool(t *testing.T) {
	require.ErrorContains(t, postgres.Migrate(context.Background(), nil), "pool is nil")
	_, err := postgres.MigrationVersion(context.Background(), nil)
	require.ErrorContains(t, err, "pool is nil")
}

func TestMigrateConcurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	dsn := testutil.PostgresBlankURL(t)
	latest, err := postgres.LatestMigrationVersion()
	require.NoError(t, err)

	const instances = 4
	var wg sync.WaitGroup
	errs := make([]error, instances)
	for i := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool, err := postgres.Open(ctx, dsn, 2)
			if err != nil {
				errs[i] = err
				return
			}
			defer pool.Close()
			errs[i] = postgres.Migrate(ctx, pool)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "instance %d", i)
	}

	pool, err := postgres.Open(ctx, dsn, 1)
	require.NoError(t, err)
	defer pool.Close()
	v, err := postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Equal(t, latest, v)
}

func TestMigrateWaitsForAdvisoryLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2)

	holder, err := pool.Acquire(ctx)
	require.NoError(t, err)
	_, err = holder.Exec(ctx, "SELECT pg_advisory_lock($1)", postgres.MigrationLockID)
	require.NoError(t, err)

	shortCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err = postgres.Migrate(shortCtx, pool)
	require.Error(t, err)
	require.ErrorContains(t, err, "acquire advisory lock")

	_, err = holder.Exec(ctx, "SELECT pg_advisory_unlock($1)", postgres.MigrationLockID)
	require.NoError(t, err)
	holder.Release()

	require.NoError(t, postgres.Migrate(ctx, pool))
}

func TestMigrateFailureIsReported(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2)
	// A conflicting pre-existing object makes the first migration fail.
	_, err := pool.Exec(ctx, "CREATE TABLE tenants (id int)")
	require.NoError(t, err)

	err = postgres.Migrate(ctx, pool)
	require.ErrorContains(t, err, "migrate up")
	v, err := postgres.MigrationVersion(ctx, pool)
	require.NoError(t, err)
	require.Zero(t, v, "the failed migration must be rolled back")

	// The advisory lock was released: a later rollback attempt can proceed.
	require.NoError(t, postgres.MigrateDownTo(ctx, pool, 0))
}

func TestMigrateLockConnectionFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := postgres.Migrate(canceled, pool)
	require.ErrorContains(t, err, "open lock connection")
}

func TestSchemaColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := testutil.Postgres(t)

	rows, err := pool.Query(ctx, `
		SELECT table_name::text, column_name::text
		FROM information_schema.columns
		WHERE table_schema = current_schema()`)
	require.NoError(t, err)
	type col struct{ Table, Column string }
	cols, err := pgx.CollectRows(rows, pgx.RowToStructByPos[col])
	require.NoError(t, err)
	actual := map[string]map[string]bool{}
	for _, c := range cols {
		if actual[c.Table] == nil {
			actual[c.Table] = map[string]bool{}
		}
		actual[c.Table][c.Column] = true
	}

	for table, columns := range specColumns {
		t.Run(table, func(t *testing.T) {
			require.NotNil(t, actual[table], "table missing")
			for _, c := range columns {
				require.True(t, actual[table][c], "column %s.%s missing", table, c)
			}
		})
	}
}

// TestAcquireResultOverloadedConstraint pins migration 00006: without it the
// first shed acquire would break statistics ingestion, because every flush
// batch containing an 'overloaded' row would violate the result check.
func TestAcquireResultOverloadedConstraint(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := integrationContext(t)
	pool := openBlank(t, 2)
	require.NoError(t, postgres.Migrate(ctx, pool))

	bucket := time.Now().UTC().Truncate(time.Minute)
	// Each insert uses its own endpoint group, because (bucket, namespace, site,
	// group, result) is the primary key.
	var group int
	insert := func(result string) error {
		group++
		_, err := pool.Exec(ctx, `INSERT INTO acquire_stats_minutely
			(bucket, namespace_id, site_id, endpoint_group_id, result, count, duration_us_sum)
			VALUES ($1, 'ns_1', 'sit_1', $2, $3, 1, 100)`, bucket, fmt.Sprintf("eg_%d", group), result)
		return err
	}
	require.NoError(t, insert("overloaded"))
	require.NoError(t, insert("exhausted"))

	// Rolling back to 00005 restores the narrower check and drops the rows the
	// widened one allowed.
	require.NoError(t, postgres.MigrateDownTo(ctx, pool, 5))
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM acquire_stats_minutely WHERE result = 'overloaded'`).Scan(&n))
	require.Zero(t, n)
	require.ErrorContains(t, insert("overloaded"), "acquire_stats_minutely_result_check")
	require.NoError(t, insert("exhausted"))
}
