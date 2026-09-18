package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// runCLI executes spnr with the given environment and returns exit code, stdout and stderr.
func runCLI(t *testing.T, env map[string]string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	a := &app{
		lookup: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		stdin:  strings.NewReader(stdin),
		stdout: &stdout,
		stderr: &stderr,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	code := execute(ctx, args, a)
	return code, stdout.String(), stderr.String()
}

// integrationEnv returns a full server environment on fresh test databases.
func integrationEnv(t *testing.T, migrated bool) map[string]string {
	t.Helper()
	redisURL := strings.TrimSpace(os.Getenv(testutil.RedisURLEnv))
	if testing.Short() || redisURL == "" {
		t.Skipf("integration test needs %s and no -short", testutil.RedisURLEnv)
	}
	dbURL := testutil.PostgresBlankURL(t)
	if migrated {
		dbURL = testutil.PostgresURL(t)
	}
	_, keys := testutil.Redis(t)
	kek, err := vault.GenerateKEK()
	require.NoError(t, err)
	return map[string]string{
		"SPINNERET_DATABASE_URL": dbURL,
		"SPINNERET_REDIS_URL":    redisURL,
		"SPINNERET_REDIS_PREFIX": keys.Prefix,
		"SPINNERET_KEKS":         "k1:" + kek,
		"ADMIN_PASSWORD":         "Integration-Passw0rd",
	}
}

func TestIntegrationMigrateAdminTokenSeed(t *testing.T) {
	env := integrationEnv(t, false)
	dbOnly := map[string]string{"SPINNERET_DATABASE_URL": env["SPINNERET_DATABASE_URL"], "ADMIN_PASSWORD": env["ADMIN_PASSWORD"]}
	latest, err := pgstore.LatestMigrationVersion()
	require.NoError(t, err)

	code, out, errOut := runCLI(t, dbOnly, "", "migrate", "status")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "database schema version 0")
	require.Contains(t, out, "pending")

	code, out, errOut = runCLI(t, dbOnly, "", "migrate", "up")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, fmt.Sprintf("database schema is at version %d\n", latest), out)

	code, out, _ = runCLI(t, dbOnly, "", "migrate", "status")
	require.Equal(t, 0, code)
	require.Contains(t, out, "up to date")

	// admin init works with only the database configured and is idempotent.
	code, out, errOut = runCLI(t, dbOnly, "", "admin", "init", "--username", "admin", "--password-env", "ADMIN_PASSWORD")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "created platform administrator admin")
	code, out, errOut = runCLI(t, dbOnly, "Integration-Passw0rd\n", "admin", "init", "--username", "other", "--password-stdin")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, alreadyInitialized+"\n", out)

	code, out, errOut = runCLI(t, dbOnly, "", "token", "create", "--name", "ci-node", "--scope", "lease:acquire",
		"--scope", "report:write", "--description", "created by the integration test")
	require.Equal(t, 0, code, errOut)
	token := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(token, "spn_"), out)
	require.Equal(t, 1, strings.Count(out, "\n"), "only the token is printed")

	pool := openTestPool(t, env["SPINNERET_DATABASE_URL"])
	var description string
	var expiresAt *time.Time
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT description, expires_at FROM api_tokens WHERE name = 'ci-node'`).Scan(&description, &expiresAt))
	require.Equal(t, "created by the integration test", description)
	require.NotNil(t, expiresAt)
	require.WithinDuration(t, time.Now().Add(720*time.Hour), *expiresAt, time.Minute)

	code, _, errOut = runCLI(t, dbOnly, "", "token", "create", "--name", "ci-node", "--scope", "lease:acquire")
	require.Equal(t, 1, code, "duplicate names are rejected")
	require.NotEmpty(t, errOut)

	// seed with small numbers, twice (idempotent, token rotated).
	seedArgs := []string{"seed", "--site", "loadtest", "--groups", "3", "--identities", "25", "--chunk-size", "10",
		"--proxies", "2", "--proxy-url", "http://lt{i}:secret@127.0.0.1:9", "--token-name", "lt"}
	first := runSeed(t, env, seedArgs)
	require.Equal(t, "loadtest", first.Site)
	require.Equal(t, 3, first.Groups)
	require.Equal(t, 25, first.Identities)
	require.Equal(t, 2, first.Proxies)
	second := runSeed(t, env, seedArgs)
	require.Equal(t, 25, second.Identities)
	require.NotEqual(t, first.Token, second.Token)

	ctx := context.Background()
	var groups, revoked, active int
	require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*) FROM endpoint_groups g JOIN sites s ON s.id = g.site_id
WHERE s.name = 'loadtest' AND g.name LIKE 'g%'`).Scan(&groups))
	require.Equal(t, 3, groups)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE revoked_at IS NOT NULL), count(*) FILTER (WHERE revoked_at IS NULL AND name = 'lt') FROM api_tokens WHERE name LIKE 'lt%'`).Scan(&revoked, &active))
	require.Equal(t, 1, revoked)
	require.Equal(t, 1, active)
	var bound int
	require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*) FROM policy_bindings b JOIN policies p ON p.id = b.policy_id
WHERE p.name IN ('loadtest-rotation', 'loadtest-breaker') AND b.site_id IS NOT NULL`).Scan(&bound))
	require.Equal(t, 2, bound)
	var rotationVersion int
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_version FROM policies WHERE name = 'loadtest-rotation'`).Scan(&rotationVersion))
	require.Equal(t, 1, rotationVersion, "re-seeding with the same options publishes nothing new")

	// Changing the proxy count changes the rotation policy (proxy mode none → pool).
	noProxies := runSeed(t, env, []string{"seed", "--site", "loadtest", "--groups", "3", "--identities", "25", "--token-name", "lt"})
	require.Equal(t, 25, noProxies.Identities)
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_version FROM policies WHERE name = 'loadtest-rotation'`).Scan(&rotationVersion))
	require.Equal(t, 2, rotationVersion)
	var published int32
	require.NoError(t, pool.QueryRow(ctx, `SELECT current_version FROM config_items WHERE group_name = 'crawler' AND key = 'loadtest.json'`).Scan(&published))
	require.EqualValues(t, 1, published)

	code, out, errOut = runCLI(t, env, "", "rebuild")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "rebuilt hot state of every site")
	code, out, errOut = runCLI(t, env, "", "rebuild", "--site", "loadtest")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "rebuilt hot state of site default/default/loadtest")

	code, out, errOut = runCLI(t, env, "", "kek", "status")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "current kek: k1")
	code, out, errOut = runCLI(t, env, "", "kek", "rewrap")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "kek rewrap finished")

	code, out, errOut = runCLI(t, env, "", "config", "check")
	require.Equal(t, 0, code, errOut)
	var redacted map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &redacted))
	require.Equal(t, "all", redacted["role"])
	require.NotContains(t, out, env["SPINNERET_KEKS"])

	code, out, errOut = runCLI(t, dbOnly, "", "migrate", "down")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, fmt.Sprintf("database schema is at version %d\n", latest-1), out)
	code, _, errOut = runCLI(t, dbOnly, "", "migrate", "up")
	require.Equal(t, 0, code, errOut)
}

// TestIntegrationSeedLoadProfile seeds the default load-test profile (100k
// identities, 50 groups). It is opt-in because it takes a while:
// SPNR_SEED_PERF=1 go test ./cmd/spnr -run TestIntegrationSeedLoadProfile.
func TestIntegrationSeedLoadProfile(t *testing.T) {
	if os.Getenv("SPNR_SEED_PERF") == "" {
		t.Skip("set SPNR_SEED_PERF=1 to seed the 100k identity load profile")
	}
	env := integrationEnv(t, true)
	start := time.Now()
	res := runSeed(t, env, []string{"seed", "--site", "loadtest", "--groups", "50", "--identities", "100000"})
	elapsed := time.Since(start)
	t.Logf("seeded %d identities and %d groups in %s", res.Identities, res.Groups, elapsed)
	require.Equal(t, 100_000, res.Identities)
	require.Less(t, elapsed, 3*time.Minute)
}

func runSeed(t *testing.T, env map[string]string, args []string) seedResult {
	t.Helper()
	code, out, errOut := runCLI(t, env, "", args...)
	require.Equalf(t, 0, code, "seed failed: %s", errOut)
	var res seedResult
	require.NoError(t, json.Unmarshal([]byte(out), &res), out)
	require.True(t, strings.HasPrefix(res.Token, "spn_"))
	return res
}

func openTestPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgstore.Open(ctx, url, 2)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
