// Command mocktarget is a scriptable fake crawl target plus an authenticating HTTP forward proxy,
// used by Spinneret end-to-end and load tests.
//
// The target site (SPINNERET_MOCK_ADDR, default :9090) serves JSON under /site/... whose behavior
// (ok, rate_limit, captcha, login_redirect, server_error, empty, business_error, slow, flaky) is
// scripted per path prefix, identity and proxy through the admin API under /_admin/. The forward
// proxy (SPINNERET_MOCK_PROXY_ADDR, default :9091) accepts absolute-form HTTP requests and CONNECT
// tunnels authenticated with Basic credentials "<proxyId>:<password>" and tags forwarded requests
// with the X-Mock-Proxy-Id header. See README.md for the full admin API.
//
// Usage:
//
//	mocktarget               serve both listeners until SIGINT/SIGTERM
//	mocktarget serve         same as above
//	mocktarget healthcheck   exit 0 when the local target site answers /healthz
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// healthcheckTimeout bounds the healthcheck subcommand.
const healthcheckTimeout = 3 * time.Second

// main is the process entry point.
func main() {
	os.Exit(realMain(os.Args[1:], os.LookupEnv, os.Stderr))
}

// realMain runs the command and returns the process exit code.
func realMain(args []string, lookup func(string) (string, bool), stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	if len(args) > 1 || (cmd != "serve" && cmd != "healthcheck") {
		_, _ = fmt.Fprintln(stderr, "usage: mocktarget [serve|healthcheck]")
		return 2
	}
	cfg, err := loadConfig(lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mocktarget: %v\n", err)
		return 2
	}
	if cmd == "healthcheck" {
		return healthcheck(cfg, stderr)
	}
	logger := newLogger(cfg, stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Once the first signal starts a graceful shutdown, restore default signal handling so a second
	// SIGINT/SIGTERM terminates the process immediately.
	context.AfterFunc(ctx, stop)
	if err := run(ctx, cfg, logger, nil); err != nil {
		logger.Error("mocktarget stopped with error", "error", err)
		return 1
	}
	logger.Info("mocktarget stopped")
	return 0
}

// healthcheck probes the local target site; it lets distroless containers define a HEALTHCHECK.
func healthcheck(cfg config, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), healthcheckTimeout)
	defer cancel()
	url := "http://" + loopbackAddr(cfg.SiteAddr) + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mocktarget healthcheck: build request: %v\n", err)
		return 1
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mocktarget healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(stderr, "mocktarget healthcheck: unexpected status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// loopbackAddr turns a listen address into a dialable local address.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip.To4() == nil {
			host = "::1"
		}
	}
	return net.JoinHostPort(host, port)
}
