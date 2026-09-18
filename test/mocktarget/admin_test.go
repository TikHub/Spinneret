package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminRulesLifecycle(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/_admin/rules", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String())

	rec = env.do(t, http.MethodPut, "/_admin/rules",
		`[{"prefix":"/site/search","mode":"rate_limit","identities":["a"]},{"prefix":"/site","mode":"business_error","business_code":42}]`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var put []Rule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &put))
	require.Len(t, put, 2)
	require.Equal(t, 429, put[0].Status)
	require.Equal(t, BusinessCode("42"), put[1].BusinessCode)

	rec = env.do(t, http.MethodGet, "/_admin/rules", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []Rule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, put, got)

	rec = env.do(t, http.MethodGet, "/site/search?device_id=a", nil)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	rec = env.do(t, http.MethodDelete, "/_admin/rules", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String())

	rec = env.do(t, http.MethodGet, "/site/search?device_id=a", nil)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestAdminRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site", Mode: ModeCaptcha}})
	env.putProxies(t, []ProxyRule{{ProxyID: "p1", Mode: ProxyModeRefuse}})

	tests := []struct {
		name       string
		method     string
		target     string
		body       string
		wantStatus int
		wantErr    string
	}{
		{name: "rules bad json", method: http.MethodPut, target: "/_admin/rules", body: `[{`, wantStatus: 400, wantErr: "decode rules"},
		{name: "rules invalid rule", method: http.MethodPut, target: "/_admin/rules", body: `[{"prefix":"/x","mode":"nope"}]`, wantStatus: 400, wantErr: "rule 0: unknown mode"},
		{name: "rules empty body", method: http.MethodPut, target: "/_admin/rules", body: ``, wantStatus: 400, wantErr: "decode rules"},
		{name: "rules too large", method: http.MethodPut, target: "/_admin/rules", body: `[` + strings.Repeat(" ", maxAdminBodyBytes) + `]`, wantStatus: 413, wantErr: "exceeds"},
		{name: "proxies invalid", method: http.MethodPut, target: "/_admin/proxies", body: `[{"proxy_id":"","mode":"ok"}]`, wantStatus: 400, wantErr: "proxy_id is required"},
		{name: "proxies too large", method: http.MethodPut, target: "/_admin/proxies", body: strings.Repeat("x", maxAdminBodyBytes+1), wantStatus: 413, wantErr: "exceeds"},
		{name: "stats bad entries", method: http.MethodGet, target: "/_admin/stats?entries=maybe", wantStatus: 400, wantErr: "invalid entries"},
		{name: "rules wrong method", method: http.MethodPost, target: "/_admin/rules", wantStatus: 405},
		{name: "reset wrong method", method: http.MethodGet, target: "/_admin/reset", wantStatus: 405},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body any
			if tt.body != "" {
				body = tt.body
			}
			rec := env.do(t, tt.method, tt.target, body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			if tt.wantErr != "" {
				require.Contains(t, decodeBody(t, rec.Body.Bytes())["error"], tt.wantErr)
			}
		})
	}

	// Failed updates keep the previous rules.
	require.Equal(t, http.StatusOK, env.do(t, http.MethodGet, "/site/x", nil).Code)
	require.Equal(t, MarkerCaptchaPage, env.do(t, http.MethodGet, "/site/x", nil).Header().Get(HeaderMarker))
	rec := env.do(t, http.MethodGet, "/_admin/proxies", nil)
	require.JSONEq(t, `[{"proxy_id":"p1","mode":"refuse"}]`, rec.Body.String())
}

func TestAdminProxiesLifecycle(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	rec := env.do(t, http.MethodPut, "/_admin/proxies", `[{"proxy_id":"p1","mode":"slow"},{"proxy_id":"*","mode":"auth_fail"}]`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `[{"proxy_id":"p1","mode":"slow","latency_ms":1000},{"proxy_id":"*","mode":"auth_fail"}]`, rec.Body.String())

	rec = env.do(t, http.MethodGet, "/_admin/proxies", nil)
	require.JSONEq(t, `[{"proxy_id":"p1","mode":"slow","latency_ms":1000},{"proxy_id":"*","mode":"auth_fail"}]`, rec.Body.String())

	rec = env.do(t, http.MethodDelete, "/_admin/proxies", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String())
	require.Equal(t, defaultProxyRule, env.state.proxyRules.Load().lookup("p1"))
}

func TestAdminStatsAndReset(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/search", Mode: ModeRateLimit, Identities: []string{"a"}}})
	for _, target := range []string{"/site/search?device_id=a", "/site/search?device_id=a", "/site/search?device_id=b", "/site/feed?device_id=a"} {
		env.do(t, http.MethodGet, target, nil)
	}
	env.state.stats.proxy.inc(proxyStatKey{ProxyID: "p1", Result: proxyResultForwarded})

	rep := env.statsReport(t, "")
	require.Equal(t, int64(4), rep.Total)
	require.Equal(t, int64(2), rep.ByMode["rate_limit"])
	require.Equal(t, int64(3), rep.ByPath["/site/search"])
	require.Equal(t, int64(3), rep.ByIdentity["a"])
	require.Equal(t, int64(4), rep.ByProxy[directProxy])
	require.Len(t, rep.Entries, 3)
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultForwarded])
	for _, e := range rep.Entries {
		if e.Mode == ModeRateLimit {
			require.Equal(t, "/site/search", e.Rule)
			require.Equal(t, 429, e.Status)
			require.Equal(t, int64(2), e.Count)
		}
	}

	rep = env.statsReport(t, "?identity=a&mode=rate_limit&entries=false")
	require.Equal(t, int64(2), rep.Total)
	require.Nil(t, rep.Entries)

	rep = env.statsReport(t, "?path_prefix=/site/feed&proxy=direct")
	require.Equal(t, int64(1), rep.Total)
	require.Zero(t, rep.ProxyListener.Total)

	rec := env.do(t, http.MethodPost, "/_admin/reset", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())
	rep = env.statsReport(t, "")
	require.Zero(t, rep.Total)
	require.Zero(t, rep.ProxyListener.Total)

	// Reset clears statistics only; rules stay.
	require.Equal(t, http.StatusTooManyRequests, env.do(t, http.MethodGet, "/site/search?device_id=a", nil).Code)
}

// failingReader returns err on every read.
type failingReader struct{ err error }

// Read implements io.Reader.
func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadAdminBodyReadError(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	for _, target := range []string{"/_admin/rules", "/_admin/proxies"} {
		t.Run(target, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, target, failingReader{err: errors.New("connection reset")})
			rec := httptest.NewRecorder()
			env.site.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, decodeBody(t, rec.Body.Bytes())["error"], "read request body: connection reset")
		})
	}
}
