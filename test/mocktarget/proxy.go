package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// Proxy listener constants.
const (
	proxyAuthRealm   = `Basic realm="mocktarget"`
	unknownProxyID   = "_unknown"
	connectEstablish = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

// proxyOptions configures the forward proxy.
type proxyOptions struct {
	Password          string
	DialTimeout       time.Duration
	TunnelIdleTimeout time.Duration
	UpstreamTimeout   time.Duration
}

// proxyHandler is an authenticating HTTP forward proxy supporting absolute-form requests and CONNECT tunnels.
type proxyHandler struct {
	st        *state
	logger    *slog.Logger
	opts      proxyOptions
	password  []byte
	dialer    *net.Dialer
	transport *http.Transport
	forwarder *httputil.ReverseProxy
	conns     *connTracker
}

// forwardStateKey is the context key of the per-request forwardState.
type forwardStateKey struct{}

// forwardState carries the proxy id into the rewrite hook and the outcome back to the handler.
type forwardState struct {
	proxyID string
	result  string
}

// newProxyHandler creates the forward proxy.
func newProxyHandler(st *state, logger *slog.Logger, opts proxyOptions) *proxyHandler {
	p := &proxyHandler{
		st:       st,
		logger:   logger,
		opts:     opts,
		password: []byte(opts.Password),
		dialer:   &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: 30 * time.Second},
		conns:    newConnTracker(),
	}
	p.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dialer.DialContext,
		MaxIdleConns:          4096,
		MaxIdleConnsPerHost:   2048,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   opts.DialTimeout,
		ResponseHeaderTimeout: opts.UpstreamTimeout,
		ExpectContinueTimeout: time.Second,
		// Relay bodies byte-for-byte instead of negotiating gzip upstream and decompressing.
		DisableCompression: true,
	}
	p.forwarder = &httputil.ReverseProxy{
		Rewrite:      p.rewrite,
		Transport:    p.transport,
		ErrorHandler: p.forwardError,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelDebug),
	}
	return p
}

// ServeHTTP implements http.Handler.
func (p *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect && !r.URL.IsAbs() {
		if r.URL.Path == "/healthz" {
			writeJSON(w, http.StatusOK, okBody{OK: true})
			return
		}
		p.record(unknownProxyID, proxyResultBadRequest)
		writeError(w, http.StatusBadRequest, "mocktarget proxy: absolute-form request or CONNECT required")
		return
	}
	user, pass, ok := parseProxyAuthorization(r.Header.Get("Proxy-Authorization"))
	if !ok {
		p.record(unknownProxyID, proxyResultAuthMissing)
		p.requireAuth(w, r, unknownProxyID, "missing or malformed Proxy-Authorization")
		return
	}
	label := truncateLabel(user)
	rule := p.st.proxyRules.Load().lookup(user)
	if err := sleepCtx(r.Context(), millis(rule.LatencyMs)); err != nil {
		p.record(label, proxyResultCanceled)
		return
	}
	switch rule.Mode {
	case ProxyModeRefuse:
		p.record(label, proxyResultRefused)
		p.refuse(w, r, label)
		return
	case ProxyModeAuthFail:
		p.record(label, proxyResultAuthFail)
		p.requireAuth(w, r, label, "auth_fail rule")
		return
	}
	if subtle.ConstantTimeCompare([]byte(pass), p.password) != 1 {
		p.record(label, proxyResultAuthInvalid)
		p.requireAuth(w, r, label, "invalid proxy credentials")
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, r, user, label)
		return
	}
	p.forward(w, r, user, label)
}

// record increments a proxy listener counter.
func (p *proxyHandler) record(proxyID, result string) {
	p.st.stats.proxy.inc(proxyStatKey{ProxyID: proxyID, Result: result})
}

// parseProxyAuthorization extracts Basic credentials; the username must be non-empty.
func parseProxyAuthorization(header string) (user, pass string, ok bool) {
	const prefix = "basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, found := strings.Cut(string(decoded), ":")
	if !found || user == "" {
		return "", "", false
	}
	return user, pass, true
}

// requireAuth answers 407 Proxy Authentication Required.
func (p *proxyHandler) requireAuth(w http.ResponseWriter, r *http.Request, proxyID, reason string) {
	p.logger.LogAttrs(r.Context(), slog.LevelDebug, "proxy authentication rejected",
		slog.String("proxy_id", proxyID), slog.String("reason", reason), slog.String("method", r.Method))
	w.Header().Set("Proxy-Authenticate", proxyAuthRealm)
	writeError(w, http.StatusProxyAuthRequired, "proxy authentication required")
}

// refuse drops the client connection without a response, resetting it when possible.
func (p *proxyHandler) refuse(w http.ResponseWriter, r *http.Request, proxyID string) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		p.logger.LogAttrs(r.Context(), slog.LevelDebug, "proxy refuse without hijack",
			slog.String("proxy_id", proxyID), slog.String("error", err.Error()))
		w.Header().Set("Connection", "close")
		writeError(w, http.StatusBadGateway, "proxy refused")
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

// forward relays an absolute-form request to its origin, injecting the proxy id header.
func (p *proxyHandler) forward(w http.ResponseWriter, r *http.Request, proxyID, label string) {
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		p.record(label, proxyResultBadRequest)
		writeError(w, http.StatusBadRequest, "mocktarget proxy: unsupported scheme")
		return
	}
	fs := &forwardState{proxyID: proxyID, result: proxyResultForwarded}
	defer func() {
		// ReverseProxy aborts with http.ErrAbortHandler when the response body copy fails mid-stream
		// (client or upstream closed). Count the request, then let http.Server drop the connection
		// so the client cannot mistake a truncated body for a complete one.
		if v := recover(); v != nil {
			p.record(label, proxyResultAborted)
			panic(v)
		}
		p.record(label, fs.result)
	}()
	p.forwarder.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), forwardStateKey{}, fs)))
}

// rewrite prepares the outbound request of a forwarded request.
func (p *proxyHandler) rewrite(pr *httputil.ProxyRequest) {
	pr.Out.Header.Del(HeaderProxyID)
	if fs, ok := pr.In.Context().Value(forwardStateKey{}).(*forwardState); ok {
		pr.Out.Header.Set(HeaderProxyID, fs.proxyID)
	}
}

// forwardError handles upstream failures of forwarded requests.
func (p *proxyHandler) forwardError(w http.ResponseWriter, r *http.Request, err error) {
	result := proxyResultUpstreamError
	if r.Context().Err() != nil {
		result = proxyResultCanceled
	}
	if fs, ok := r.Context().Value(forwardStateKey{}).(*forwardState); ok {
		fs.result = result
	}
	p.logger.LogAttrs(r.Context(), slog.LevelDebug, "proxy upstream error",
		slog.String("host", r.URL.Host), slog.String("result", result), slog.String("error", err.Error()))
	if result == proxyResultCanceled {
		return
	}
	writeError(w, http.StatusBadGateway, "mocktarget proxy: upstream unavailable")
}

// tunnel serves a CONNECT request by piping bytes between the client and the target.
func (p *proxyHandler) tunnel(w http.ResponseWriter, r *http.Request, proxyID, label string) {
	target := r.Host
	if _, port, err := net.SplitHostPort(target); err != nil || port == "" {
		p.record(label, proxyResultBadRequest)
		writeError(w, http.StatusBadRequest, "mocktarget proxy: CONNECT target must be host:port")
		return
	}
	upstream, err := p.dialer.DialContext(r.Context(), "tcp", target)
	if err != nil {
		p.record(label, proxyResultUpstreamError)
		p.logger.LogAttrs(r.Context(), slog.LevelDebug, "proxy tunnel dial failed",
			slog.String("proxy_id", label), slog.String("target", target), slog.String("error", err.Error()))
		writeError(w, http.StatusBadGateway, "mocktarget proxy: upstream unavailable")
		return
	}
	// Register before any client byte can reach the target, so the target attributes the first request.
	unregister := p.st.tunnels.register(upstream.LocalAddr().String(), proxyID)
	defer unregister()

	client, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = upstream.Close()
		p.record(label, proxyResultUpstreamError)
		writeError(w, http.StatusInternalServerError, "mocktarget proxy: connection cannot be hijacked")
		return
	}
	if !p.conns.add(client, upstream) {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	defer p.conns.remove(client, upstream)
	// The server armed read/write deadlines on the connection; a tunnel manages its own idleness.
	_ = client.SetDeadline(time.Time{})
	if _, err := io.WriteString(client, connectEstablish); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.record(label, proxyResultTunneled)
	if n := brw.Reader.Buffered(); n > 0 {
		early, _ := brw.Peek(n)
		if _, err := upstream.Write(early); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
	}
	pipe(client, upstream, p.opts.TunnelIdleTimeout)
}
