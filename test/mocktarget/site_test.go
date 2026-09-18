package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSiteModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rule        Rule
		wantStatus  int
		wantMode    Mode
		wantMarker  string
		wantBizCode string
		check       func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name: "no rule", wantStatus: 200, wantMode: ModeOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				body := decodeBody(t, rec.Body.Bytes())
				require.Equal(t, true, body["ok"])
				require.Equal(t, "/site/search", body["path"])
				require.Len(t, body["items"], defaultItemCount)
				require.Empty(t, rec.Header().Get(HeaderRule))
			},
		},
		{
			name: "ok with item count", rule: Rule{Prefix: "/site", Mode: ModeOK, ItemCount: ptr(5)}, wantStatus: 200, wantMode: ModeOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				body := decodeBody(t, rec.Body.Bytes())
				items := body["items"].([]any)
				require.Len(t, items, 5)
				require.Equal(t, map[string]any{"id": "/site/search#1", "title": "item 1"}, items[0])
				require.Equal(t, "/site", rec.Header().Get(HeaderRule))
				require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
			},
		},
		{
			name: "rate limit default", rule: Rule{Prefix: "/site/search", Mode: ModeRateLimit}, wantStatus: 429, wantMode: ModeRateLimit,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Equal(t, map[string]any{"ok": false, "error": "rate_limited"}, decodeBody(t, rec.Body.Bytes()))
			},
		},
		{name: "rate limit custom status", rule: Rule{Prefix: "/site", Mode: ModeRateLimit, Status: 403}, wantStatus: 403, wantMode: ModeRateLimit},
		{
			name: "captcha", rule: Rule{Prefix: "/site", Mode: ModeCaptcha}, wantStatus: 200, wantMode: ModeCaptcha, wantMarker: MarkerCaptchaPage,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Contains(t, rec.Body.String(), "captcha-page")
				require.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"))
			},
		},
		{
			name: "login redirect", rule: Rule{Prefix: "/site", Mode: ModeLoginRedirect}, wantStatus: 302, wantMode: ModeLoginRedirect,
			wantMarker: MarkerLoginRedirect,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Equal(t, "/login", rec.Header().Get("Location"))
			},
		},
		{
			name: "server error", rule: Rule{Prefix: "/site", Mode: ModeServerError}, wantStatus: 503, wantMode: ModeServerError,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Equal(t, "server_error", decodeBody(t, rec.Body.Bytes())["error"])
			},
		},
		{
			name: "empty", rule: Rule{Prefix: "/site", Mode: ModeEmpty}, wantStatus: 200, wantMode: ModeEmpty, wantMarker: MarkerEmptyList,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				body := decodeBody(t, rec.Body.Bytes())
				require.Equal(t, true, body["ok"])
				require.Equal(t, []any{}, body["items"])
				require.Contains(t, rec.Body.String(), `"items":[]`)
			},
		},
		{
			name: "business error numeric", rule: Rule{Prefix: "/site", Mode: ModeBusinessError, BusinessCode: "10001"},
			wantStatus: 200, wantMode: ModeBusinessError, wantBizCode: "10001",
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Contains(t, rec.Body.String(), `"ok":false,"code":10001`)
			},
		},
		{
			name: "business error string", rule: Rule{Prefix: "/site", Mode: ModeBusinessError, BusinessCode: "E_BLOCKED"},
			wantStatus: 200, wantMode: ModeBusinessError, wantBizCode: "E_BLOCKED",
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Contains(t, rec.Body.String(), `"code":"E_BLOCKED"`)
			},
		},
		{
			name: "business error leading zero stays string", rule: Rule{Prefix: "/site", Mode: ModeBusinessError, BusinessCode: "007"},
			wantStatus: 200, wantMode: ModeBusinessError, wantBizCode: "007",
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Contains(t, rec.Body.String(), `"code":"007"`)
			},
		},
		{
			name: "slow", rule: Rule{Prefix: "/site", Mode: ModeSlow, LatencyMs: 30}, wantStatus: 200, wantMode: ModeSlow,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				require.Len(t, decodeBody(t, rec.Body.Bytes())["items"], defaultItemCount)
			},
		},
		{name: "flaky always", rule: Rule{Prefix: "/site", Mode: ModeFlaky, Probability: ptr(1.0)}, wantStatus: 429, wantMode: ModeRateLimit},
		{name: "flaky never", rule: Rule{Prefix: "/site", Mode: ModeFlaky, Probability: ptr(0.0)}, wantStatus: 200, wantMode: ModeOK},
		{name: "flaky custom status", rule: Rule{Prefix: "/site", Mode: ModeFlaky, Probability: ptr(1.0), Status: 418}, wantStatus: 418, wantMode: ModeRateLimit},
		{name: "probability zero serves ok", rule: Rule{Prefix: "/site", Mode: ModeCaptcha, Probability: ptr(0.0)}, wantStatus: 200, wantMode: ModeOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t)
			if tt.rule.Prefix != "" {
				env.putRules(t, []Rule{tt.rule})
			}
			start := time.Now()
			rec := env.do(t, http.MethodGet, "/site/search?device_id=dev-1", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, string(tt.wantMode), rec.Header().Get(HeaderMode))
			require.Equal(t, tt.wantMarker, rec.Header().Get(HeaderMarker))
			require.Equal(t, tt.wantBizCode, rec.Header().Get(HeaderBusinessCode))
			require.Equal(t, "dev-1", rec.Header().Get(HeaderIdentity))
			require.Equal(t, directProxy, rec.Header().Get(HeaderProxy))
			if tt.rule.LatencyMs > 0 {
				require.GreaterOrEqual(t, time.Since(start), time.Duration(tt.rule.LatencyMs)*time.Millisecond)
			}
			if tt.check != nil {
				tt.check(t, rec)
			}
			rep := env.statsReport(t, "")
			require.Equal(t, int64(1), rep.Total)
			require.Equal(t, int64(1), rep.ByMode[string(tt.wantMode)])
			require.Equal(t, int64(1), rep.ByIdentity["dev-1"])
		})
	}
}

func TestSiteIdentityAndProxyAttribution(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{
		{Prefix: "/site/", Mode: ModeCaptcha, Identities: []string{"sess-a"}},
		{Prefix: "/site/", Mode: ModeRateLimit, Proxies: []string{"p1"}},
	})
	tests := []struct {
		name         string
		target       string
		cookie       string
		proxyHeader  string
		wantIdentity string
		wantProxy    string
		wantStatus   int
	}{
		{name: "anonymous direct", target: "/site/a", wantIdentity: anonymousIdentity, wantProxy: directProxy, wantStatus: 200},
		{name: "cookie identity", target: "/site/a", cookie: "sess-a", wantIdentity: "sess-a", wantProxy: directProxy, wantStatus: 200},
		{name: "cookie wins over query", target: "/site/a?device_id=d1", cookie: "sess-b", wantIdentity: "sess-b", wantProxy: directProxy, wantStatus: 200},
		{name: "query identity", target: "/site/a?device_id=d1", wantIdentity: "d1", wantProxy: directProxy, wantStatus: 200},
		{name: "proxy header", target: "/site/a", proxyHeader: "p1", wantIdentity: anonymousIdentity, wantProxy: "p1", wantStatus: 429},
		{name: "identity rule beats proxy rule", target: "/site/a", cookie: "sess-a", proxyHeader: "p1", wantIdentity: "sess-a", wantProxy: "p1", wantStatus: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: identityCookie, Value: tt.cookie})
			}
			if tt.proxyHeader != "" {
				req.Header.Set(HeaderProxyID, tt.proxyHeader)
			}
			rec := httptest.NewRecorder()
			env.site.ServeHTTP(rec, req)
			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, tt.wantIdentity, rec.Header().Get(HeaderIdentity))
			require.Equal(t, tt.wantProxy, rec.Header().Get(HeaderProxy))
		})
	}
}

func TestSiteMatchesLongLabelsInFull(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	longIdentity := strings.Repeat("s", maxLabelLen+44)
	longProxy := strings.Repeat("p", maxLabelLen+10)
	env.putRules(t, []Rule{
		{Prefix: "/site/id", Mode: ModeRateLimit, Identities: []string{longIdentity}},
		{Prefix: "/site/px", Mode: ModeCaptcha, Proxies: []string{longProxy}},
	})
	tests := []struct {
		name       string
		target     string
		cookie     string
		proxy      string
		wantStatus int
		wantMode   Mode
	}{
		{name: "long identity matches", target: "/site/id", cookie: longIdentity, wantStatus: 429, wantMode: ModeRateLimit},
		{name: "same label prefix does not match", target: "/site/id", cookie: longIdentity[:maxLabelLen] + "other", wantStatus: 200, wantMode: ModeOK},
		{name: "long proxy matches", target: "/site/px", proxy: longProxy, wantStatus: 200, wantMode: ModeCaptcha},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: identityCookie, Value: tt.cookie})
			}
			if tt.proxy != "" {
				req.Header.Set(HeaderProxyID, tt.proxy)
			}
			rec := httptest.NewRecorder()
			env.site.ServeHTTP(rec, req)
			require.Equal(t, tt.wantStatus, rec.Code)
			require.Equal(t, string(tt.wantMode), rec.Header().Get(HeaderMode))
			require.LessOrEqual(t, len(rec.Header().Get(HeaderIdentity)), maxLabelLen, "echoed labels stay bounded")
			require.LessOrEqual(t, len(rec.Header().Get(HeaderProxy)), maxLabelLen)
		})
	}
}

func TestSiteDebugLogging(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var mu sync.Mutex
	logger := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := newState(100, 1)
	site := newSiteHandler(st, logger)
	req := httptest.NewRequest(http.MethodGet, "/site/logged?device_id=dev-log", nil)
	rec := httptest.NewRecorder()
	site.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	mu.Lock()
	defer mu.Unlock()
	line := decodeBody(t, bytes.TrimSpace(buf.Bytes()))
	require.Equal(t, "site request", line["msg"])
	require.Equal(t, "/site/logged", line["path"])
	require.Equal(t, "dev-log", line["identity"])
	require.Equal(t, directProxy, line["proxy"])
	require.EqualValues(t, http.StatusOK, line["status"])
}

func TestSiteTunnelRegistryAttribution(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/site/a", nil)
	req.RemoteAddr = "10.1.2.3:4567"
	req.Header.Set(HeaderProxyID, "spoofed")
	unregister := env.state.tunnels.register(req.RemoteAddr, "tunnel-proxy")
	defer unregister()
	rec := httptest.NewRecorder()
	env.site.ServeHTTP(rec, req)
	require.Equal(t, "tunnel-proxy", rec.Header().Get(HeaderProxy))
}

func TestSiteSlowCanceledRecords499(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site", Mode: ModeSlow, LatencyMs: 60_000}})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/site/slow", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.site.ServeHTTP(rec, req)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("slow handler did not observe cancellation")
	}
	rep := env.statsReport(t, "")
	require.Equal(t, int64(1), rep.ByStatus["499"])
}

func TestSitePostLoginAndHealth(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/submit", Mode: ModeEmpty}})

	rec := env.do(t, http.MethodPost, "/site/submit?device_id=d9", `{"q":"x"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, MarkerEmptyList, rec.Header().Get(HeaderMarker))

	rec = env.do(t, http.MethodGet, "/login", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "login-page")

	rec = env.do(t, http.MethodGet, "/healthz", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())

	rec = env.do(t, http.MethodDelete, "/site/submit", nil)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	rec = env.do(t, http.MethodGet, "/nope", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSiteFlakyDistributionOverHTTP(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/flaky", Mode: ModeFlaky, Probability: ptr(0.3)}})
	const n = 2000
	limited := 0
	for range n {
		rec := env.do(t, http.MethodGet, "/site/flaky", nil)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		} else {
			require.Equal(t, http.StatusOK, rec.Code)
		}
	}
	require.InDelta(t, 0.3, float64(limited)/n, 0.05)
	rep := env.statsReport(t, "?path_prefix=/site/flaky")
	require.Equal(t, int64(limited), rep.ByStatus["429"])
	require.Equal(t, int64(n-limited), rep.ByStatus["200"])
}
