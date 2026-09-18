package server_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/appconfig"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/server"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

const (
	e2eAdminUser     = "admin"
	e2eAdminPassword = "Correct-Horse-Battery-9"
	e2eTenant        = "acme"
	e2eNamespace     = "default"
)

// logBuffer collects server logs; they are printed only when the test fails.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() > 8<<20 {
		b.buf.Reset()
		b.buf.WriteString("... (earlier logs truncated)\n")
	}
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// e2eEnv is a running in-process server (role all) on real test databases.
type e2eEnv struct {
	baseURL  string
	pool     *pgxpool.Pool
	chURL    string
	tenantID string

	// stop shuts the server down once and reports how long it took.
	stop     func() (time.Duration, error)
	stopTook time.Duration
	stopErr  error
}

// startE2E creates fresh PostgreSQL/Redis/ClickHouse state, bootstraps the
// platform administrator and runs the server until the test ends.
func startE2E(t *testing.T) *e2eEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}
	redisURL := strings.TrimSpace(os.Getenv(testutil.RedisURLEnv))
	if redisURL == "" {
		t.Skipf("end-to-end test needs %s", testutil.RedisURLEnv)
	}
	dbURL := testutil.PostgresURL(t)
	chURL := testutil.ClickHouseURL(t)
	_, keys := testutil.Redis(t) // unique prefix, keys deleted at cleanup
	kek, err := vault.GenerateKEK()
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgstore.Open(ctx, dbURL, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = auth.BootstrapPlatformAdmin(ctx, pool, e2eAdminUser, e2eAdminPassword, e2eTenant, e2eNamespace,
		policysvc.NewService(pool, nil, nil, nil, nil))
	require.NoError(t, err)

	env := map[string]string{
		"SPINNERET_HTTP_ADDR":            "127.0.0.1:0",
		"SPINNERET_ROLE":                 "all",
		"SPINNERET_INSTANCE_ID":          "e2e-" + keys.Prefix,
		"SPINNERET_DATABASE_URL":         dbURL,
		"SPINNERET_DATABASE_MAX_CONNS":   "24",
		"SPINNERET_REDIS_URL":            redisURL,
		"SPINNERET_REDIS_PREFIX":         keys.Prefix,
		"SPINNERET_CLICKHOUSE_URL":       chURL,
		"SPINNERET_KEKS":                 "k1:" + kek,
		"SPINNERET_REPORT_SHARDS":        "4",
		"SPINNERET_LOG_LEVEL":            "info",
		"SPINNERET_LOG_FORMAT":           "text",
		"SPINNERET_SHUTDOWN_TIMEOUT":     "8s",
		"SPINNERET_PROXY_CHECK_INTERVAL": "1h",
	}
	cfg, err := appconfig.LoadFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	require.NoError(t, err)

	logs := &logBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := server.New(ctx, cfg, server.Options{Logger: logger, Version: "e2e"})
	require.NoError(t, err)

	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(runCtx) }()

	e := &e2eEnv{baseURL: "http://" + srv.Addr().String(), pool: pool, chURL: chURL}
	var once sync.Once
	e.stop = func() (time.Duration, error) {
		once.Do(func() {
			start := time.Now()
			cancelRun()
			select {
			case e.stopErr = <-done:
			case <-time.After(30 * time.Second):
				e.stopErr = errors.New("server did not shut down within 30s")
			}
			e.stopTook = time.Since(start)
		})
		return e.stopTook, e.stopErr
	}
	t.Cleanup(func() {
		_, err := e.stop()
		require.NoError(t, err)
		if t.Failed() || os.Getenv("SPINNERET_E2E_LOGS") != "" {
			t.Logf("server logs:\n%s", tail(logs.String(), 2000))
		}
	})
	waitReady(t, e.baseURL)
	return e
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func waitReady(t *testing.T, baseURL string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status, body := httpGet(t, baseURL+"/readyz")
		if status == http.StatusOK {
			return
		}
		last = fmt.Sprintf("%d %s", status, body)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server not ready within 60s: %s", last)
}

// httpGet performs a GET request and returns the status and body.
func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// headerInterceptor adds fixed headers to every client request.
type headerInterceptor struct {
	mu      sync.Mutex
	headers http.Header
}

func (h *headerInterceptor) set(key, value string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.headers.Set(key, value)
}

func (h *headerInterceptor) apply(dst http.Header) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, vs := range h.headers {
		for _, v := range vs {
			dst.Set(k, v)
		}
	}
}

func (h *headerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		h.apply(req.Header())
		return next(ctx, req)
	}
}

func (h *headerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (h *headerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// consoleClient is a cookie-authenticated console user.
type consoleClient struct {
	http    *http.Client
	headers *headerInterceptor
	baseURL string

	auth     spinneretv1connect.AuthServiceClient
	tenants  spinneretv1connect.TenantAdminServiceClient
	access   spinneretv1connect.AccessAdminServiceClient
	sites    spinneretv1connect.SiteAdminServiceClient
	ids      spinneretv1connect.IdentityAdminServiceClient
	policies spinneretv1connect.PolicyAdminServiceClient
	breakers spinneretv1connect.BreakerAdminServiceClient
	configs  spinneretv1connect.ConfigAdminServiceClient
	secrets  spinneretv1connect.SecretAdminServiceClient
	proxies  spinneretv1connect.ProxyAdminServiceClient
	notify   spinneretv1connect.NotificationAdminServiceClient
	dash     spinneretv1connect.DashboardServiceClient
}

func newConsoleClient(t *testing.T, baseURL string) *consoleClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: jar, Timeout: 90 * time.Second}
	hi := &headerInterceptor{headers: http.Header{"X-Spinneret-Csrf": []string{"1"}}}
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(hi)}
	return &consoleClient{
		http: hc, headers: hi, baseURL: baseURL,
		auth:     spinneretv1connect.NewAuthServiceClient(hc, baseURL, opts...),
		tenants:  spinneretv1connect.NewTenantAdminServiceClient(hc, baseURL, opts...),
		access:   spinneretv1connect.NewAccessAdminServiceClient(hc, baseURL, opts...),
		sites:    spinneretv1connect.NewSiteAdminServiceClient(hc, baseURL, opts...),
		ids:      spinneretv1connect.NewIdentityAdminServiceClient(hc, baseURL, opts...),
		policies: spinneretv1connect.NewPolicyAdminServiceClient(hc, baseURL, opts...),
		breakers: spinneretv1connect.NewBreakerAdminServiceClient(hc, baseURL, opts...),
		configs:  spinneretv1connect.NewConfigAdminServiceClient(hc, baseURL, opts...),
		secrets:  spinneretv1connect.NewSecretAdminServiceClient(hc, baseURL, opts...),
		proxies:  spinneretv1connect.NewProxyAdminServiceClient(hc, baseURL, opts...),
		notify:   spinneretv1connect.NewNotificationAdminServiceClient(hc, baseURL, opts...),
		dash:     spinneretv1connect.NewDashboardServiceClient(hc, baseURL, opts...),
	}
}

// nodeClient is a bearer-token authenticated crawler node.
type nodeClient struct {
	leases  spinneretv1connect.LeaseServiceClient
	reports spinneretv1connect.ReportServiceClient
	configs spinneretv1connect.ConfigServiceClient
	secrets spinneretv1connect.SecretServiceClient
}

func newNodeClient(baseURL, token string) *nodeClient {
	hc := &http.Client{Timeout: 90 * time.Second}
	hi := &headerInterceptor{headers: http.Header{}}
	hi.set("Authorization", "Bearer "+token)
	hi.set("X-Spinneret-Node", "e2e-node")
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(hi)}
	return &nodeClient{
		leases:  spinneretv1connect.NewLeaseServiceClient(hc, baseURL, opts...),
		reports: spinneretv1connect.NewReportServiceClient(hc, baseURL, opts...),
		configs: spinneretv1connect.NewConfigServiceClient(hc, baseURL, opts...),
		secrets: spinneretv1connect.NewSecretServiceClient(hc, baseURL, opts...),
	}
}

// requireConnectError asserts the Connect code and Spinneret-Reason of err and
// returns the error for further checks.
func requireConnectError(t *testing.T, err error, code connect.Code, reason apperr.Reason) *connect.Error {
	t.Helper()
	require.Error(t, err)
	var ce *connect.Error
	require.Truef(t, errors.As(err, &ce), "expected a connect error, got %T: %v", err, err)
	require.Equalf(t, code, ce.Code(), "unexpected code: %v", err)
	if reason != "" {
		require.Equalf(t, string(reason), ce.Meta().Get(apperr.HeaderReason), "unexpected reason: %v", err)
	}
	return ce
}

// sseStream reads Server-Sent Events of one connection in the background.
type sseStream struct {
	events chan string // event types
	cancel context.CancelFunc
	done   chan struct{}
}

// openEventStream connects to the SSE endpoint with the console client's cookies.
func openEventStream(t *testing.T, c *consoleClient, tenantID, namespace string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := fmt.Sprintf("%s%s?tenant=%s&namespace=%s", c.baseURL, server.EventStreamPath, tenantID, namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: c.http.Jar} // no client timeout: the stream is long-lived
	resp, err := hc.Do(req)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("event stream status %d: %s", resp.StatusCode, b)
	}
	s := &sseStream{events: make(chan string, 256), cancel: cancel, done: make(chan struct{})}
	connected := make(chan struct{})
	go func() {
		defer close(s.done)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		once := sync.Once{}
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, ": connected") {
				once.Do(func() { close(connected) })
			}
			if typ, ok := strings.CutPrefix(line, "event: "); ok {
				select {
				case s.events <- typ:
				default:
				}
			}
		}
	}()
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		s.close()
		t.Fatal("event stream did not confirm the connection")
	}
	t.Cleanup(s.close)
	return s
}

func (s *sseStream) close() {
	s.cancel()
	<-s.done
}

// waitEvent waits for an event of the given type.
func (s *sseStream) waitEvent(t *testing.T, typ string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-s.events:
			if got == typ {
				return
			}
		case <-deadline:
			t.Fatalf("no %s event within %s", typ, timeout)
		}
	}
}
