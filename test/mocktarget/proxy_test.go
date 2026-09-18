package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseProxyAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		header   string
		wantUser string
		wantPass string
		wantOK   bool
	}{
		{name: "valid", header: basicAuth("p1", "secret"), wantUser: "p1", wantPass: "secret", wantOK: true},
		{name: "lowercase scheme", header: "basic " + strings.TrimPrefix(basicAuth("p1", "s"), "Basic "), wantUser: "p1", wantPass: "s", wantOK: true},
		{name: "password with colon", header: basicAuth("p1", "a:b"), wantUser: "p1", wantPass: "a:b", wantOK: true},
		{name: "empty password", header: basicAuth("p1", ""), wantUser: "p1", wantOK: true},
		{name: "missing", header: ""},
		{name: "bearer", header: "Bearer abc"},
		{name: "bad base64", header: "Basic !!!"},
		{name: "no colon", header: "Basic cDE="},
		{name: "empty user", header: basicAuth("", "secret")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			user, pass, ok := parseProxyAuthorization(tt.header)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantUser, user)
			require.Equal(t, tt.wantPass, pass)
		})
	}
}

func TestProxyForwardInjectsProxyID(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/search", Mode: ModeCaptcha, Proxies: []string{"p1"}}})

	req, err := http.NewRequest(http.MethodGet, env.siteSrv.URL+"/site/search?device_id=d1", nil)
	require.NoError(t, err)
	req.Header.Set(HeaderProxyID, "spoofed")
	resp, err := env.proxyClient(t, "p1", testPassword).Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "p1", resp.Header.Get(HeaderProxy))
	require.Equal(t, MarkerCaptchaPage, resp.Header.Get(HeaderMarker))
	require.Contains(t, string(body), "captcha-page")

	resp, err = env.proxyClient(t, "p2", testPassword).Get(env.siteSrv.URL + "/site/search?device_id=d1")
	require.NoError(t, err)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "p2", decodeBody(t, body)["proxy"])

	rep := env.statsReport(t, "")
	require.Equal(t, int64(1), rep.ByProxy["p1"])
	require.Equal(t, int64(1), rep.ByProxy["p2"])
	require.Equal(t, int64(2), rep.ProxyListener.ByResult[proxyResultForwarded])
}

func TestProxyDoesNotForwardProxyAuthorization(t *testing.T) {
	t.Parallel()
	seen := make(chan http.Header, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(origin.Close)
	env := newTestEnv(t)

	client := env.proxyClient(t, "p7", testPassword)
	client.Transport.(*http.Transport).DisableCompression = true
	resp, err := client.Get(origin.URL + "/anything")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	h := <-seen
	require.Empty(t, h.Get("Proxy-Authorization"))
	require.Equal(t, "p7", h.Get(HeaderProxyID))
	require.Empty(t, h.Get("Accept-Encoding"), "the proxy relays bodies as-is and must not negotiate compression")
}

func TestProxyForwardAbortedResponseIsCounted(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Promise a 1000 byte body, send a few bytes, then drop the connection mid-stream.
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial")
		_ = http.NewResponseController(w).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	t.Cleanup(origin.Close)
	env := newTestEnv(t)

	resp, err := env.proxyClient(t, "ab", testPassword).Get(origin.URL + "/stream")
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	require.Error(t, err, "a truncated upstream body must not look complete to the client")
	require.Eventually(t, func() bool {
		return env.state.stats.report(statsFilter{Proxy: "ab"}).ProxyListener.ByResult[proxyResultAborted] == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(1), env.state.stats.report(statsFilter{Proxy: "ab"}).ProxyListener.Total)
}

func TestProxyAuthentication(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putProxies(t, []ProxyRule{{ProxyID: "bad", Mode: ProxyModeAuthFail}})
	target := env.siteSrv.URL + "/site/a"

	tests := []struct {
		name       string
		client     *http.Client
		wantStatus int
		wantResult string
		wantProxy  string
	}{
		{name: "wrong password", client: env.proxyClient(t, "p1", "nope"), wantStatus: 407, wantResult: proxyResultAuthInvalid, wantProxy: "p1"},
		{name: "auth_fail rule", client: env.proxyClient(t, "bad", testPassword), wantStatus: 407, wantResult: proxyResultAuthFail, wantProxy: "bad"},
		{name: "correct", client: env.proxyClient(t, "good", testPassword), wantStatus: 200, wantResult: proxyResultForwarded, wantProxy: "good"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.client.Get(target)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantStatus == http.StatusProxyAuthRequired {
				require.Equal(t, proxyAuthRealm, resp.Header.Get("Proxy-Authenticate"))
			}
			rep := env.statsReport(t, "?proxy="+tt.wantProxy)
			require.Equal(t, int64(1), rep.ProxyListener.ByResult[tt.wantResult])
		})
	}

	// No credentials at all.
	u, err := url.Parse(env.proxySrv.URL)
	require.NoError(t, err)
	anon := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
	resp, err := anon.Get(target)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)
	rep := env.statsReport(t, "")
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultAuthMissing])
}

func TestProxyRefuseClosesConnection(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putProxies(t, []ProxyRule{{ProxyID: "dead", Mode: ProxyModeRefuse}})

	resp, err := env.proxyClient(t, "dead", testPassword).Get(env.siteSrv.URL + "/site/a")
	closeIfOK(resp, err)
	require.Error(t, err)

	conn := dialProxy(t, env)
	writeConnect(t, conn, env.siteSrv.Listener.Addr().String(), "dead", testPassword, "")
	resp, err = http.ReadResponse(bufio.NewReader(conn), nil)
	closeIfOK(resp, err)
	require.Error(t, err, "refused CONNECT must not receive a response")

	rep := env.statsReport(t, "?proxy=dead")
	require.GreaterOrEqual(t, rep.ProxyListener.ByResult[proxyResultRefused], int64(2))
	require.Zero(t, rep.Total, "refused requests never reach the site")
}

func TestProxySlowMode(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putProxies(t, []ProxyRule{{ProxyID: "slow", Mode: ProxyModeSlow, LatencyMs: 80}})
	start := time.Now()
	resp, err := env.proxyClient(t, "slow", testPassword).Get(env.siteSrv.URL + "/site/a")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond)
}

func TestProxySlowModeCanceled(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putProxies(t, []ProxyRule{{ProxyID: "slow", Mode: ProxyModeSlow, LatencyMs: 60_000}})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.siteSrv.URL+"/site/a", nil)
	require.NoError(t, err)
	resp, err := env.proxyClient(t, "slow", testPassword).Do(req)
	closeIfOK(resp, err)
	require.Error(t, err)
	require.Eventually(t, func() bool {
		return env.state.stats.report(statsFilter{Proxy: "slow"}).ProxyListener.ByResult[proxyResultCanceled] == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestProxyConnectTunnel(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/tunnel", Mode: ModeEmpty, Proxies: []string{"tp"}}})
	siteAddr := env.siteSrv.Listener.Addr().String()

	tests := []struct {
		name  string
		early bool
	}{
		{name: "request after established"},
		{name: "request pipelined with CONNECT", early: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := dialProxy(t, env)
			inner := "GET /site/tunnel?device_id=t1 HTTP/1.1\r\nHost: " + siteAddr + "\r\nConnection: close\r\n\r\n"
			br := bufio.NewReader(conn)
			if tt.early {
				writeConnect(t, conn, siteAddr, "tp", testPassword, inner)
				readEstablished(t, br)
			} else {
				writeConnect(t, conn, siteAddr, "tp", testPassword, "")
				readEstablished(t, br)
				_, err := io.WriteString(conn, inner)
				require.NoError(t, err)
			}
			resp, err := http.ReadResponse(br, nil)
			require.NoError(t, err)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Equal(t, MarkerEmptyList, resp.Header.Get(HeaderMarker))
			require.Equal(t, "tp", resp.Header.Get(HeaderProxy), "tunnel traffic is attributed via the registry")
			require.Equal(t, "tp", decodeBody(t, body)["proxy"])
		})
	}
	rep := env.statsReport(t, "?proxy=tp")
	require.Equal(t, int64(2), rep.ProxyListener.ByResult[proxyResultTunneled])
	require.Equal(t, int64(2), rep.ByMode[string(ModeEmpty)])
	require.Eventually(t, func() bool { return env.proxy.conns.len() == 0 }, 5*time.Second, 10*time.Millisecond)
}

func TestProxyConnectErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	closedAddr := closedPort(t)

	tests := []struct {
		name       string
		target     string
		password   string
		wantStatus int
		wantResult string
	}{
		{name: "wrong password", target: env.siteSrv.Listener.Addr().String(), password: "bad", wantStatus: 407, wantResult: proxyResultAuthInvalid},
		{name: "unreachable target", target: closedAddr, password: testPassword, wantStatus: 502, wantResult: proxyResultUpstreamError},
		{name: "target without port", target: "example.invalid", password: testPassword, wantStatus: 400, wantResult: proxyResultBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := dialProxy(t, env)
			writeConnect(t, conn, tt.target, "ce", tt.password, "")
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
	rep := env.statsReport(t, "?proxy=ce")
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultAuthInvalid])
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultUpstreamError])
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultBadRequest])
}

func TestProxyNonProxyRequests(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	direct := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := direct.Get(env.proxySrv.URL + "/healthz")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = direct.Get(env.proxySrv.URL + "/site/a")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Absolute-form request with an unsupported scheme, sent directly.
	req := httptest.NewRequest(http.MethodGet, "ftp://example.com/file", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("px", testPassword))
	rec := httptest.NewRecorder()
	env.proxy.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rep := env.statsReport(t, "")
	require.Equal(t, int64(1), rep.ProxyListener.ByProxy[unknownProxyID])
	require.Equal(t, int64(1), rep.ProxyListener.ByProxy["px"])
}

func TestProxyForwardUpstreamError(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	resp, err := env.proxyClient(t, "p1", testPassword).Get("http://" + closedPort(t) + "/x")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
	var eb errorBody
	require.NoError(t, json.Unmarshal(body, &eb))
	require.Contains(t, eb.Error, "upstream unavailable")
	rep := env.statsReport(t, "?proxy=p1")
	require.Equal(t, int64(1), rep.ProxyListener.ByResult[proxyResultUpstreamError])
}

func TestProxyForwardCanceledUpstream(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putRules(t, []Rule{{Prefix: "/site/slow", Mode: ModeSlow, LatencyMs: 60_000}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.siteSrv.URL+"/site/slow", nil)
	require.NoError(t, err)
	resp, err := env.proxyClient(t, "p1", testPassword).Do(req)
	closeIfOK(resp, err)
	require.Error(t, err)
	require.Eventually(t, func() bool {
		return env.state.stats.report(statsFilter{Proxy: "p1"}).ProxyListener.ByResult[proxyResultCanceled] == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestProxyRefuseWithoutHijacker(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.putProxies(t, []ProxyRule{{ProxyID: "dead", Mode: ProxyModeRefuse}})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("dead", testPassword))
	rec := httptest.NewRecorder()
	env.proxy.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "close", rec.Header().Get("Connection"))

	// CONNECT through a recorder cannot hijack either.
	env2 := newTestEnv(t)
	req = httptest.NewRequest(http.MethodConnect, "http://"+env2.siteSrv.Listener.Addr().String(), nil)
	req.Host = env2.siteSrv.Listener.Addr().String()
	req.Header.Set("Proxy-Authorization", basicAuth("hj", testPassword))
	rec = httptest.NewRecorder()
	env2.proxy.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestProxyConnectRejectedAfterShutdown(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.proxy.conns.closeAll()
	conn := dialProxy(t, env)
	writeConnect(t, conn, env.siteSrv.Listener.Addr().String(), "late", testPassword, "")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	closeIfOK(resp, err)
	require.Error(t, err, "tunnels opened during shutdown are closed immediately")
}

// dialProxy opens a raw TCP connection to the proxy listener.
func dialProxy(t *testing.T, env *testEnv) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", env.proxySrv.Listener.Addr().String(), 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// writeConnect writes a CONNECT request, optionally followed by early tunnel bytes.
func writeConnect(t *testing.T, conn net.Conn, target, user, pass, early string) {
	t.Helper()
	msg := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: " + basicAuth(user, pass) + "\r\n\r\n" + early
	_, err := io.WriteString(conn, msg)
	require.NoError(t, err)
}

// readEstablished consumes a successful CONNECT response.
func readEstablished(t *testing.T, br *bufio.Reader) {
	t.Helper()
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	// The response body is deliberately not read or closed: br continues with the tunneled bytes.
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}
