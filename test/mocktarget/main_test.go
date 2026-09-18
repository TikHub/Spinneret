package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// envLookup builds a lookup function from a map.
func envLookup(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr []string
		check   func(t *testing.T, cfg config)
	}{
		{
			name: "defaults",
			env:  map[string]string{envSiteAddr: "  ", envProxyPassword: ""},
			check: func(t *testing.T, cfg config) {
				require.Equal(t, config{
					SiteAddr: defaultSiteAddr, ProxyAddr: defaultProxyAddr, ProxyPassword: defaultPassword,
					LogLevel: slog.LevelInfo, LogFormat: "json", StatsMaxKeys: defaultStatsKeys,
					ShutdownTimeout: defaultShutdown, DialTimeout: defaultDialTimeout, TunnelIdleTimeout: defaultTunnelIdle,
				}, cfg)
			},
		},
		{
			name: "overrides",
			env: map[string]string{
				envSiteAddr: "127.0.0.1:1", envProxyAddr: "127.0.0.1:2", envProxyPassword: " p w ",
				envLogLevel: "DEBUG", envLogFormat: "Text", envSeed: "99", envStatsMaxKeys: "10",
				envShutdownTimeout: "1s", envDialTimeout: "2s", envTunnelIdleTimeout: "3m",
			},
			check: func(t *testing.T, cfg config) {
				require.Equal(t, config{
					SiteAddr: "127.0.0.1:1", ProxyAddr: "127.0.0.1:2", ProxyPassword: " p w ",
					LogLevel: slog.LevelDebug, LogFormat: "text", Seed: 99, StatsMaxKeys: 10,
					ShutdownTimeout: time.Second, DialTimeout: 2 * time.Second, TunnelIdleTimeout: 3 * time.Minute,
				}, cfg)
			},
		},
		{
			name: "all invalid",
			env: map[string]string{
				envLogLevel: "loud", envLogFormat: "xml", envSeed: "-1", envStatsMaxKeys: "many",
				envShutdownTimeout: "soon", envDialTimeout: "0s", envTunnelIdleTimeout: "-1m",
			},
			wantErr: []string{envLogLevel, envLogFormat, envSeed, envStatsMaxKeys, envShutdownTimeout, envDialTimeout, envTunnelIdleTimeout},
		},
		{name: "stats keys out of range", env: map[string]string{envStatsMaxKeys: "0"}, wantErr: []string{"must be in"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := loadConfig(envLookup(tt.env))
			if len(tt.wantErr) > 0 {
				require.Error(t, err)
				for _, want := range tt.wantErr {
					require.ErrorContains(t, err, want)
				}
				return
			}
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	newLogger(config{LogFormat: "json", LogLevel: slog.LevelInfo}, &buf).Info("hello", "k", "v")
	require.Contains(t, buf.String(), `"msg":"hello"`)
	buf.Reset()
	newLogger(config{LogFormat: "text", LogLevel: slog.LevelWarn}, &buf).Info("hidden")
	require.Empty(t, buf.String())
	newLogger(config{LogFormat: "text", LogLevel: slog.LevelWarn}, &buf).Warn("shown")
	require.Contains(t, buf.String(), "msg=shown")
}

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		":9090":          "127.0.0.1:9090",
		"0.0.0.0:9090":   "127.0.0.1:9090",
		"[::]:9090":      "[::1]:9090",
		"10.0.0.5:9090":  "10.0.0.5:9090",
		"localhost:9090": "localhost:9090",
		"garbage":        "garbage",
	}
	for in, want := range tests {
		require.Equal(t, want, loopbackAddr(in), in)
	}
}

// startRun starts run on loopback ports and returns the bound addresses and a stop function.
func startRun(t *testing.T, cfg config) (site, proxy string, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan [2]string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, cfg, discardLogger(), func(s, p net.Addr) { addrs <- [2]string{s.String(), p.String()} })
	}()
	select {
	case a := <-addrs:
		var once sync.Once
		var stopErr error
		stop = func() error {
			once.Do(func() {
				cancel()
				stopErr = <-errCh
			})
			return stopErr
		}
		t.Cleanup(func() { _ = stop() })
		return a[0], a[1], stop
	case err := <-errCh:
		cancel()
		t.Fatalf("run failed early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("run did not become ready")
	}
	return "", "", nil
}

// testConfig returns a config listening on ephemeral loopback ports.
func testConfig() config {
	return config{
		SiteAddr: "127.0.0.1:0", ProxyAddr: "127.0.0.1:0", ProxyPassword: testPassword,
		LogLevel: slog.LevelError, LogFormat: "json", Seed: 1, StatsMaxKeys: 1000,
		ShutdownTimeout: 2 * time.Second, DialTimeout: time.Second, TunnelIdleTimeout: time.Minute,
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	t.Parallel()
	site, proxy, stop := startRun(t, testConfig())
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 5 * time.Second}

	resp, err := client.Get("http://" + site + "/healthz")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, err = client.Get("http://" + proxy + "/healthz")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Zero(t, healthcheck(config{SiteAddr: site}, io.Discard))

	// An open tunnel must not block shutdown.
	conn, err := net.Dial("tcp", proxy)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "CONNECT "+site+" HTTP/1.1\r\nHost: "+site+"\r\nProxy-Authorization: "+basicAuth("p", testPassword)+"\r\n\r\n")
	require.NoError(t, err)
	buf := make([]byte, len(connectEstablish))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	require.Equal(t, connectEstablish, string(buf))

	require.NoError(t, stop())
	resp, err = client.Get("http://" + site + "/healthz")
	closeIfOK(resp, err)
	require.Error(t, err)
}

func TestRunShutdownTimeoutForcesClose(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.ShutdownTimeout = 50 * time.Millisecond
	site, _, stop := startRun(t, cfg)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	req, err := http.NewRequest(http.MethodPut, "http://"+site+"/_admin/rules",
		strings.NewReader(`[{"prefix":"/site/slow","mode":"slow","latency_ms":60000}]`))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	slowDone := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + site + "/site/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
		slowDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	require.NoError(t, stop())
	require.Less(t, time.Since(start), 10*time.Second)
	select {
	case err := <-slowDone:
		require.Error(t, err, "in-flight slow request is aborted by the forced close")
	case <-time.After(10 * time.Second):
		t.Fatal("slow request was not aborted")
	}
}

func TestRunListenErrors(t *testing.T) {
	t.Parallel()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })

	cfg := testConfig()
	cfg.SiteAddr = busy.Addr().String()
	require.ErrorContains(t, run(context.Background(), cfg, discardLogger(), nil), "listen site")

	cfg = testConfig()
	cfg.ProxyAddr = busy.Addr().String()
	require.ErrorContains(t, run(context.Background(), cfg, discardLogger(), nil), "listen proxy")
}

func TestServeListenerFailure(t *testing.T) {
	t.Parallel()
	siteLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	require.NoError(t, siteLn.Close()) // Serve fails immediately.

	a := newApp(testConfig(), discardLogger())
	errCh := make(chan error, 1)
	go func() { errCh <- a.serve(context.Background(), siteLn, proxyLn) }()
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "serve site")
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after a listener failure")
	}
}

func TestHealthcheckFailures(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	require.Equal(t, 1, healthcheck(config{SiteAddr: closedPort(t)}, &stderr))
	require.Contains(t, stderr.String(), "mocktarget healthcheck")

	stderr.Reset()
	require.Equal(t, 1, healthcheck(config{SiteAddr: "bad host:1"}, &stderr))
	require.NotEmpty(t, stderr.String())
}

func TestHealthcheckUnexpectedStatus(t *testing.T) {
	t.Parallel()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}), ReadHeaderTimeout: time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	var stderr bytes.Buffer
	require.Equal(t, 1, healthcheck(config{SiteAddr: ln.Addr().String()}, &stderr))
	require.Contains(t, stderr.String(), "unexpected status 503")
}

func TestRealMainArgumentsAndConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		wantCode int
		wantOut  string
	}{
		{name: "unknown command", args: []string{"explode"}, wantCode: 2, wantOut: "usage"},
		{name: "too many args", args: []string{"serve", "extra"}, wantCode: 2, wantOut: "usage"},
		{name: "bad config", args: []string{"serve"}, env: map[string]string{envLogFormat: "xml"}, wantCode: 2, wantOut: envLogFormat},
		{name: "healthcheck down", args: []string{"healthcheck"}, env: map[string]string{envSiteAddr: "127.0.0.1:1"}, wantCode: 1, wantOut: "healthcheck"},
		{name: "listen failure", args: nil, env: map[string]string{envSiteAddr: "256.0.0.1:1"}, wantCode: 1, wantOut: "listen site"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.Equal(t, tt.wantCode, realMain(tt.args, envLookup(tt.env), &out))
			require.Contains(t, out.String(), tt.wantOut)
		})
	}
}

// TestRealMainServeUntilSIGTERM exercises the signal-driven lifecycle of the serve command.
// It is not parallel: it delivers SIGTERM to the test process.
func TestRealMainServeUntilSIGTERM(t *testing.T) {
	ports := freeAddrs(t, 2)
	sitePort, proxyPort := ports[0], ports[1]
	env := map[string]string{envSiteAddr: sitePort, envProxyAddr: proxyPort, envLogLevel: "error"}
	var out bytes.Buffer
	var mu sync.Mutex
	codeCh := make(chan int, 1)
	go func() {
		w := &lockedWriter{mu: &mu, w: &out}
		codeCh <- realMain([]string{"serve"}, envLookup(env), w)
	}()
	require.Eventually(t, func() bool {
		return healthcheck(config{SiteAddr: sitePort}, io.Discard) == 0
	}, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	select {
	case code := <-codeCh:
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, 0, code, out.String())
	case <-time.After(10 * time.Second):
		t.Fatal("realMain did not stop on SIGTERM")
	}
}

// lockedWriter serializes writes to w.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

// Write implements io.Writer.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// freeAddrs returns n distinct loopback addresses nothing listens on. All listeners stay open until
// every address is chosen, so the operating system cannot hand out the same port twice.
func freeAddrs(t *testing.T, n int) []string {
	t.Helper()
	addrs := make([]string, 0, n)
	listeners := make([]net.Listener, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listeners = append(listeners, ln)
		addrs = append(addrs, ln.Addr().String())
	}
	for _, ln := range listeners {
		require.NoError(t, ln.Close())
	}
	return addrs
}
