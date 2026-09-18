package server_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Regression: while an instance drains, its answers must end the connection. A load balancer that
// keeps a pooled connection open would otherwise send a request on it in the moment the listener
// closes, and POSTs (Acquire, Report) cannot safely be retried by the proxy — the failover drill
// (scripts/e2e-failover.sh) measured those as 502s at the end of the drain window.
func TestDrainingInstanceClosesKeepAliveConnections(t *testing.T) {
	env := startE2E(t)
	transport := &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: time.Minute}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	readyz := func(t *testing.T) (*http.Response, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.baseURL+"/readyz", nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		return resp, string(body)
	}

	// While serving, connections are reusable.
	resp, body := readyz(t)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, `"status":"ok"`)
	require.False(t, resp.Close, "a serving instance keeps connections alive")

	go func() { _, _ = env.stop() }()

	// The drain delay is min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4) = 2s in this harness; readiness turns
	// "draining" at its start, together with the keep-alive shutdown.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, body = readyz(t)
		if resp.StatusCode == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance did not report draining within the drain delay: %d %s", resp.StatusCode, body)
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.Contains(t, body, `"status":"draining"`)
	require.True(t, resp.Close, "a draining instance answers with Connection: close")

	took, err := env.stop()
	require.NoError(t, err)
	require.Less(t, took, 8*time.Second)
}
