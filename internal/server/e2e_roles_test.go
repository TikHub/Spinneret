package server_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/server"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault"
)

// runServer starts srv and stops it (waiting for Run to return) at cleanup.
func runServer(t *testing.T, srv *server.Server, logs *logBuffer) string {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Error("server did not shut down within 30s")
		}
		if t.Failed() || os.Getenv("SPINNERET_E2E_LOGS") != "" {
			t.Logf("server logs:\n%s", tail(logs.String(), 400))
		}
	})
	return "http://" + srv.Addr().String()
}

// TestRolesAndStartup runs separate api and worker instances on a blank
// database: the schema check, migrations at startup, role-specific routes,
// readiness gating on the hot-state epoch and a dedicated metrics listener.
func TestRolesAndStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}
	redisURL := strings.TrimSpace(os.Getenv(testutil.RedisURLEnv))
	if redisURL == "" {
		t.Skipf("end-to-end test needs %s", testutil.RedisURLEnv)
	}
	dbURL := testutil.PostgresBlankURL(t)
	_, keys := testutil.Redis(t)
	kek, err := vault.GenerateKEK()
	require.NoError(t, err)
	config := func(role string, extra map[string]string) appconfig.Config {
		env := map[string]string{
			"SPINNERET_HTTP_ADDR":        "127.0.0.1:0",
			"SPINNERET_ROLE":             role,
			"SPINNERET_INSTANCE_ID":      role + "-" + keys.Prefix,
			"SPINNERET_DATABASE_URL":     dbURL,
			"SPINNERET_REDIS_URL":        redisURL,
			"SPINNERET_REDIS_PREFIX":     keys.Prefix,
			"SPINNERET_KEKS":             "k1:" + kek,
			"SPINNERET_REPORT_SHARDS":    "2",
			"SPINNERET_SHUTDOWN_TIMEOUT": "4s",
			"SPINNERET_UI_ENABLED":       "false",
		}
		for k, v := range extra {
			env[k] = v
		}
		cfg, err := appconfig.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
		require.NoError(t, err)
		return cfg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	latest, err := pgstore.LatestMigrationVersion()
	require.NoError(t, err)

	apiLogs := &logBuffer{}
	apiLogger := slog.New(slog.NewTextHandler(apiLogs, nil))
	_, err = server.New(ctx, config("api", nil), server.Options{Logger: apiLogger})
	require.EqualError(t, err, fmt.Sprintf("database schema is at version 0, binary expects %d: run spnr migrate up", latest))

	api, err := server.New(ctx, config("api", map[string]string{
		"SPINNERET_METRICS_ADDR": "127.0.0.1:0",
		"SPINNERET_PPROF_ADDR":   "127.0.0.1:0",
	}), server.Options{Logger: apiLogger, Migrate: true})
	require.NoError(t, err)
	apiURL := runServer(t, api, apiLogs)
	require.NotNil(t, api.MetricsAddr())

	// pprof is opt-in and lives on its own listener; it is never reachable
	// through the API or the metrics listener.
	require.NotNil(t, api.PprofAddr())
	status, body := httpGet(t, "http://"+api.PprofAddr().String()+"/debug/pprof/heap?debug=1")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, body, "heap profile")
	status, _ = httpGet(t, apiURL+"/debug/pprof/")
	require.Equal(t, http.StatusNotFound, status, "pprof is not mounted on the API listener")
	status, _ = httpGet(t, "http://"+api.MetricsAddr().String()+"/debug/pprof/")
	require.Equal(t, http.StatusNotFound, status, "pprof is not mounted on the metrics listener")

	status, _ = httpGet(t, apiURL+"/healthz")
	require.Equal(t, http.StatusOK, status)
	status, body = httpGet(t, apiURL+"/readyz")
	require.Equal(t, http.StatusServiceUnavailable, status, "api instances wait for the hot-state epoch")
	require.Contains(t, body, "epoch missing")
	status, _ = httpGet(t, apiURL+"/metrics")
	require.Equal(t, http.StatusNotFound, status, "metrics are served on the dedicated listener")
	status, body = httpGet(t, "http://"+api.MetricsAddr().String()+"/metrics")
	require.Equal(t, http.StatusOK, status)
	require.True(t, strings.Contains(body, "spinneret_config_watchers"), "metrics listener serves the registry")
	require.Equal(t, http.StatusUnauthorized, postJSON(t, apiURL+"/spinneret.v1.AuthService/GetMe"))

	workerLogs := &logBuffer{}
	worker, err := server.New(ctx, config("worker", nil), server.Options{Logger: slog.New(slog.NewTextHandler(workerLogs, nil))})
	require.NoError(t, err)
	workerURL := runServer(t, worker, workerLogs)
	waitReady(t, workerURL)
	waitReady(t, apiURL)
	status, _ = httpGet(t, workerURL+"/metrics")
	require.Equal(t, http.StatusOK, status)
	require.Eventually(t, func() bool {
		_, body := httpGet(t, workerURL+"/metrics")
		return strings.Contains(body, "spinneret_stream_owned_shards 2")
	}, 15*time.Second, 100*time.Millisecond, "the only worker owns every report shard")
	require.Equal(t, http.StatusNotFound, postJSON(t, workerURL+"/spinneret.v1.AuthService/GetMe"), "worker instances serve no APIs")
}

// postJSON sends an empty Connect JSON request and returns the status code.
func postJSON(t *testing.T, url string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}
