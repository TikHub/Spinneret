package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testPassword is the proxy password used by the test environment.
const testPassword = "pw-test"

// testEnv is a running target site and forward proxy sharing one state.
type testEnv struct {
	state    *state
	site     *siteHandler
	proxy    *proxyHandler
	siteSrv  *httptest.Server
	proxySrv *httptest.Server
}

// discardLogger returns a logger that drops every record.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// newTestEnv starts a site and proxy on loopback ports.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWith(t, proxyOptions{
		Password:          testPassword,
		DialTimeout:       2 * time.Second,
		TunnelIdleTimeout: 5 * time.Second,
		UpstreamTimeout:   5 * time.Second,
	})
}

// newTestEnvWith starts a site and proxy with explicit proxy options.
func newTestEnvWith(t *testing.T, opts proxyOptions) *testEnv {
	t.Helper()
	st := newState(1000, 42)
	env := &testEnv{state: st, site: newSiteHandler(st, discardLogger()), proxy: newProxyHandler(st, discardLogger(), opts)}
	env.siteSrv = httptest.NewServer(env.site)
	env.proxySrv = httptest.NewServer(env.proxy)
	t.Cleanup(func() {
		env.proxy.conns.closeAll()
		env.proxySrv.Close()
		env.siteSrv.Close()
		env.proxy.transport.CloseIdleConnections()
	})
	return env
}

// proxyClient returns an HTTP client that sends requests through the mock proxy as proxyID.
func (e *testEnv) proxyClient(t *testing.T, proxyID, password string) *http.Client {
	t.Helper()
	u, err := url.Parse(e.proxySrv.URL)
	require.NoError(t, err)
	u.User = url.UserPassword(proxyID, password)
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true},
		Timeout:   10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// basicAuth encodes Basic credentials.
func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// doJSON sends a request to the site handler and returns the recorded response.
func (e *testEnv) do(t *testing.T, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = bytes.NewBufferString(b)
	default:
		data, err := json.Marshal(b)
		require.NoError(t, err)
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	e.site.ServeHTTP(rec, req)
	return rec
}

// putRules replaces the site rules and requires success.
func (e *testEnv) putRules(t *testing.T, rules any) {
	t.Helper()
	rec := e.do(t, http.MethodPut, "/_admin/rules", rules)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// putProxies replaces the proxy rules and requires success.
func (e *testEnv) putProxies(t *testing.T, rules any) {
	t.Helper()
	rec := e.do(t, http.MethodPut, "/_admin/proxies", rules)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// statsReport fetches and decodes the stats report.
func (e *testEnv) statsReport(t *testing.T, query string) StatsReport {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/_admin/stats"+query, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var rep StatsReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	return rep
}

// decodeBody decodes a JSON body into a generic map.
func decodeBody(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m), string(data))
	return m
}

// closeIfOK closes the body of a response that was expected to fail but did not.
func closeIfOK(resp *http.Response, err error) {
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}
