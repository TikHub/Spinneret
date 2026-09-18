//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

// Header names of the Spinneret API.
const (
	headerReason     = "Spinneret-Reason"
	headerRetryAfter = "Spinneret-Retry-After-Ms"
	headerCSRF       = "X-Spinneret-CSRF"
	headerTenant     = "X-Spinneret-Tenant"
	headerNode       = "X-Spinneret-Node"
)

// config is the suite configuration read from SPINNERET_E2E_* variables (see doc_test.go).
type config struct {
	URL             string
	AdminUser       string
	AdminPassword   string
	Tenant          string
	MockURL         string
	TargetURL       string
	ProxyHost       string
	ClientProxyHost string
	ProxyPassword   string
	RedisAddr       string
	RedisPrefix     string
	ReportShards    int
	Replicas        int
	CallbackURL     string
	SinkAddr        string
	CrawlDuration   time.Duration
	Keep            bool
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// loadConfig reads the suite configuration and skips the test when the stack is not configured.
func loadConfig(t *testing.T) config {
	t.Helper()
	c := config{
		URL:             strings.TrimRight(env("SPINNERET_E2E_URL", "http://localhost:8080"), "/"),
		AdminUser:       env("SPINNERET_E2E_ADMIN_USER", "admin"),
		AdminPassword:   os.Getenv("SPINNERET_E2E_ADMIN_PASSWORD"),
		Tenant:          env("SPINNERET_E2E_TENANT", "default"),
		MockURL:         strings.TrimRight(env("SPINNERET_E2E_MOCK_URL", "http://localhost:19090"), "/"),
		ProxyHost:       env("SPINNERET_E2E_PROXY_HOST", "mocktarget:9091"),
		ClientProxyHost: env("SPINNERET_E2E_CLIENT_PROXY_HOST", ""),
		ProxyPassword:   env("SPINNERET_E2E_PROXY_PASSWORD", "secret"),
		RedisAddr:       env("SPINNERET_E2E_REDIS", "localhost:6379"),
		RedisPrefix:     env("SPINNERET_E2E_REDIS_PREFIX", "sp"),
		CallbackURL:     strings.TrimRight(env("SPINNERET_E2E_CALLBACK", "http://e2e:18099"), "/"),
		SinkAddr:        env("SPINNERET_E2E_SINK_ADDR", ":18099"),
		Keep:            os.Getenv("SPINNERET_E2E_KEEP") != "",
	}
	c.TargetURL = strings.TrimRight(env("SPINNERET_E2E_TARGET_URL", c.MockURL), "/")
	if c.AdminPassword == "" {
		t.Skip("SPINNERET_E2E_ADMIN_PASSWORD is not set: run the suite with `make e2e`")
	}
	var err error
	c.ReportShards, err = strconv.Atoi(env("SPINNERET_E2E_REPORT_SHARDS", "16"))
	require.NoError(t, err, "SPINNERET_E2E_REPORT_SHARDS")
	c.Replicas, err = strconv.Atoi(env("SPINNERET_E2E_REPLICAS", "2"))
	require.NoError(t, err, "SPINNERET_E2E_REPLICAS")
	c.CrawlDuration, err = time.ParseDuration(env("SPINNERET_E2E_CRAWL_DURATION", "60s"))
	require.NoError(t, err, "SPINNERET_E2E_CRAWL_DURATION")
	return c
}

// randomHex returns n random bytes as lower-case hex.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// headerInterceptor sets fixed headers on every unary client request.
type headerInterceptor struct {
	mu      sync.RWMutex
	headers http.Header
}

func newHeaderInterceptor() *headerInterceptor {
	return &headerInterceptor{headers: http.Header{}}
}

func (h *headerInterceptor) set(key, value string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.headers.Set(key, value)
}

func (h *headerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		h.mu.RLock()
		for k, vs := range h.headers {
			for _, v := range vs {
				req.Header().Set(k, v)
			}
		}
		h.mu.RUnlock()
		return next(ctx, req)
	}
}

func (h *headerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (h *headerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// console is a cookie-authenticated console session of the platform administrator.
type console struct {
	baseURL  string
	http     *http.Client
	headers  *headerInterceptor
	tenantID string

	auth     spinneretv1connect.AuthServiceClient
	tenants  spinneretv1connect.TenantAdminServiceClient
	access   spinneretv1connect.AccessAdminServiceClient
	sites    spinneretv1connect.SiteAdminServiceClient
	ids      spinneretv1connect.IdentityAdminServiceClient
	proxies  spinneretv1connect.ProxyAdminServiceClient
	policies spinneretv1connect.PolicyAdminServiceClient
	breakers spinneretv1connect.BreakerAdminServiceClient
	configs  spinneretv1connect.ConfigAdminServiceClient
	secrets  spinneretv1connect.SecretAdminServiceClient
	notify   spinneretv1connect.NotificationAdminServiceClient
	dash     spinneretv1connect.DashboardServiceClient
}

// login signs in as the administrator and selects the configured tenant.
func login(ctx context.Context, t *testing.T, cfg config) *console {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: jar, Timeout: 90 * time.Second}
	hi := newHeaderInterceptor()
	// Cookie-authenticated unsafe requests need the CSRF header (the console always sends it).
	hi.set(headerCSRF, "1")
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(hi)}
	c := &console{
		baseURL: cfg.URL, http: hc, headers: hi,
		auth:     spinneretv1connect.NewAuthServiceClient(hc, cfg.URL, opts...),
		tenants:  spinneretv1connect.NewTenantAdminServiceClient(hc, cfg.URL, opts...),
		access:   spinneretv1connect.NewAccessAdminServiceClient(hc, cfg.URL, opts...),
		sites:    spinneretv1connect.NewSiteAdminServiceClient(hc, cfg.URL, opts...),
		ids:      spinneretv1connect.NewIdentityAdminServiceClient(hc, cfg.URL, opts...),
		proxies:  spinneretv1connect.NewProxyAdminServiceClient(hc, cfg.URL, opts...),
		policies: spinneretv1connect.NewPolicyAdminServiceClient(hc, cfg.URL, opts...),
		breakers: spinneretv1connect.NewBreakerAdminServiceClient(hc, cfg.URL, opts...),
		configs:  spinneretv1connect.NewConfigAdminServiceClient(hc, cfg.URL, opts...),
		secrets:  spinneretv1connect.NewSecretAdminServiceClient(hc, cfg.URL, opts...),
		notify:   spinneretv1connect.NewNotificationAdminServiceClient(hc, cfg.URL, opts...),
		dash:     spinneretv1connect.NewDashboardServiceClient(hc, cfg.URL, opts...),
	}
	res, err := c.auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: cfg.AdminUser, Password: cfg.AdminPassword}))
	require.NoError(t, err, "admin login")
	require.True(t, res.Msg.GetUser().GetIsPlatformAdmin(), "the e2e user must be a platform administrator")
	for _, ta := range res.Msg.GetTenants() {
		if ta.GetTenant().GetName() == cfg.Tenant {
			c.tenantID = ta.GetTenant().GetId()
		}
	}
	require.NotEmptyf(t, c.tenantID, "tenant %q not found for user %s", cfg.Tenant, cfg.AdminUser)
	hi.set(headerTenant, c.tenantID)
	// The session cookie authenticates the following calls.
	me, err := c.auth.GetMe(ctx, connect.NewRequest(&spinneretv1.GetMeRequest{}))
	require.NoError(t, err, "GetMe with the session cookie")
	require.Equal(t, cfg.AdminUser, me.Msg.GetUser().GetUsername())
	return c
}

// node is a crawler node authenticated with an API token.
type node struct {
	name    string
	leases  spinneretv1connect.LeaseServiceClient
	reports spinneretv1connect.ReportServiceClient
	configs spinneretv1connect.ConfigServiceClient
	secrets spinneretv1connect.SecretServiceClient
}

func newNode(baseURL, token, name string) *node {
	hc := &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     60 * time.Second,
	}}
	hi := newHeaderInterceptor()
	hi.set("Authorization", "Bearer "+token)
	hi.set(headerNode, name)
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(hi)}
	return &node{
		name:    name,
		leases:  spinneretv1connect.NewLeaseServiceClient(hc, baseURL, opts...),
		reports: spinneretv1connect.NewReportServiceClient(hc, baseURL, opts...),
		configs: spinneretv1connect.NewConfigServiceClient(hc, baseURL, opts...),
		secrets: spinneretv1connect.NewSecretServiceClient(hc, baseURL, opts...),
	}
}

// apiError describes a Connect error of the Spinneret API.
type apiError struct {
	Code       connect.Code
	Reason     string
	RetryAfter time.Duration
}

// asAPIError extracts the code, Spinneret-Reason and retry hint of err.
func asAPIError(err error) (apiError, bool) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return apiError{}, false
	}
	ae := apiError{Code: ce.Code(), Reason: ce.Meta().Get(headerReason)}
	if ms, perr := strconv.Atoi(ce.Meta().Get(headerRetryAfter)); perr == nil && ms > 0 {
		ae.RetryAfter = time.Duration(ms) * time.Millisecond
	}
	return ae, true
}

// requireReason asserts that err is a Connect error with the given code and reason.
func requireReason(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := asAPIError(err)
	require.Truef(t, ok, "expected a Connect error, got %T: %v", err, err)
	require.Equalf(t, code, ae.Code, "unexpected code: %v", err)
	require.Equalf(t, reason, ae.Reason, "unexpected reason: %v", err)
}

// eventually polls cond every interval until it returns true or timeout passes. cond returns a
// description of the last observed state that is included in the failure message.
func eventually(t *testing.T, timeout, interval time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for {
		ok, state := cond()
		if ok {
			return
		}
		last = state
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s (last state: %s)", what, timeout, last)
		}
		time.Sleep(interval)
	}
}

// sleepCtx sleeps for d or until ctx ends; it reports whether the full duration elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// describe formats a value for failure messages.
func describe(format string, args ...any) string { return fmt.Sprintf(format, args...) }
