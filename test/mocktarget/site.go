package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// Headers exchanged with the target site.
const (
	// HeaderProxyID carries the proxy id injected by the mock proxy into forwarded requests.
	HeaderProxyID = "X-Mock-Proxy-Id"
	// HeaderMarker names the page marker of captcha, login redirect and empty list responses.
	HeaderMarker = "X-Mock-Marker"
	// HeaderBusinessCode carries the business code of business_error responses.
	HeaderBusinessCode = "X-Mock-Business-Code"
	// HeaderMode is the behavior actually served.
	HeaderMode = "X-Mock-Mode"
	// HeaderRule is the prefix of the matched rule (absent when no rule matched).
	HeaderRule = "X-Mock-Rule"
	// HeaderIdentity is the identity the target site derived from the request.
	HeaderIdentity = "X-Mock-Identity"
	// HeaderProxy is the proxy id the target site attributed the request to.
	HeaderProxy = "X-Mock-Proxy"
)

// Page markers set in HeaderMarker.
const (
	MarkerCaptchaPage   = "captcha_page"
	MarkerLoginRedirect = "login_redirect"
	MarkerEmptyList     = "empty_list"
)

// Request attribution defaults and limits of the target site.
const (
	identityCookie     = "sessionid"
	identityQuery      = "device_id"
	anonymousIdentity  = "anonymous"
	directProxy        = "direct"
	loginPath          = "/login"
	maxSiteBodyBytes   = 1 << 20
	statusClientClosed = 499
)

// captchaHTML is served by captcha mode; it contains the "captcha-page" marker.
const captchaHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Security verification</title></head>
<body><div id="captcha-page" class="captcha-page">Please complete the security check to continue.</div></body></html>
`

// loginHTML is served on the login page targeted by login_redirect.
const loginHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>Login</title></head>
<body><form id="login-page" class="login-page" method="post" action="/login"></form></body></html>
`

// siteHandler serves the scripted target site and the admin API.
type siteHandler struct {
	st     *state
	logger *slog.Logger
	mux    *http.ServeMux
}

// newSiteHandler builds the target site router.
func newSiteHandler(st *state, logger *slog.Logger) *siteHandler {
	h := &siteHandler{st: st, logger: logger, mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", h.handleHealthz)
	h.mux.HandleFunc("GET "+loginPath, h.handleLogin)
	h.mux.HandleFunc("GET /site/", h.handleSite)
	h.mux.HandleFunc("POST /site/", h.handleSite)
	h.registerAdmin()
	return h
}

// ServeHTTP implements http.Handler.
func (h *siteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// handleHealthz reports liveness.
func (h *siteHandler) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, okBody{OK: true})
}

// handleLogin serves the page login_redirect points to.
func (h *siteHandler) handleLogin(w http.ResponseWriter, _ *http.Request) {
	writeBody(w, http.StatusOK, "text/html; charset=utf-8", []byte(loginHTML))
}

// decision is the behavior chosen for one request.
type decision struct {
	mode    Mode
	status  int
	latency time.Duration
	items   int
	rule    *compiledRule
}

// ruleLabel returns the matched rule prefix or "" when no rule matched.
func (d decision) ruleLabel() string {
	if d.rule == nil {
		return ""
	}
	return d.rule.Prefix
}

// decide picks the served behavior for a matched rule (nil = no rule).
func (h *siteHandler) decide(rule *compiledRule) decision {
	if rule == nil {
		return decision{mode: ModeOK, status: http.StatusOK, items: defaultItemCount}
	}
	if !h.st.rng.trigger(rule.probability) {
		return decision{mode: ModeOK, status: http.StatusOK, items: rule.itemCount, rule: rule}
	}
	d := decision{mode: rule.Mode, status: rule.Status, latency: millis(rule.LatencyMs), items: rule.itemCount, rule: rule}
	if rule.Mode == ModeFlaky {
		d.mode = ModeRateLimit
	}
	return d
}

// handleSite serves every path under /site/ according to the scripted rules.
func (h *siteHandler) handleSite(w http.ResponseWriter, r *http.Request) {
	// Drain the request body so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxSiteBodyBytes))

	path := r.URL.Path
	// Rules match the full values; only the labels echoed in headers, bodies and stats are bounded.
	rawIdentity, rawProxy := requestIdentity(r), h.requestProxy(r)
	d := h.decide(h.st.rules.Load().match(path, rawIdentity, rawProxy))
	identity, proxy := truncateLabel(rawIdentity), truncateLabel(rawProxy)

	hdr := w.Header()
	hdr.Set(HeaderMode, string(d.mode))
	hdr.Set(HeaderIdentity, identity)
	hdr.Set(HeaderProxy, proxy)
	if d.rule != nil {
		hdr.Set(HeaderRule, d.rule.Prefix)
	}

	status := d.status
	if err := sleepCtx(r.Context(), d.latency); err != nil {
		status = statusClientClosed
	} else {
		h.writeSiteResponse(w, d, path, identity, proxy)
	}
	h.st.stats.site.inc(siteStatKey{
		Path: truncateLabel(path), Rule: d.ruleLabel(), Mode: d.mode, Identity: identity, Proxy: proxy, Status: status,
	})
	if h.logger.Enabled(r.Context(), slog.LevelDebug) {
		h.logger.LogAttrs(r.Context(), slog.LevelDebug, "site request",
			slog.String("method", r.Method), slog.String("path", path), slog.String("identity", identity),
			slog.String("proxy", proxy), slog.String("mode", string(d.mode)), slog.Int("status", status),
			slog.String("rule", d.ruleLabel()))
	}
}

// requestIdentity derives the identity from the sessionid cookie or the device_id query parameter.
func requestIdentity(r *http.Request) string {
	if c, err := r.Cookie(identityCookie); err == nil && c.Value != "" {
		return c.Value
	}
	if v := r.URL.Query().Get(identityQuery); v != "" {
		return v
	}
	return anonymousIdentity
}

// requestProxy attributes the request to a proxy: tunnel registry, then the injected header, then "direct".
func (h *siteHandler) requestProxy(r *http.Request) string {
	if id, ok := h.st.tunnels.lookup(r.RemoteAddr); ok {
		return id
	}
	if v := r.Header.Get(HeaderProxyID); v != "" {
		return v
	}
	return directProxy
}

// siteItem is one element of an item list response.
type siteItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// itemsBody is the JSON body of ok, slow and empty responses.
type itemsBody struct {
	OK       bool       `json:"ok"`
	Path     string     `json:"path"`
	Identity string     `json:"identity"`
	Proxy    string     `json:"proxy"`
	Items    []siteItem `json:"items"`
}

// businessErrorBody is the JSON body of business_error responses.
type businessErrorBody struct {
	OK      bool   `json:"ok"`
	Code    any    `json:"code"`
	Message string `json:"message"`
}

// writeSiteResponse writes the response of the served mode.
func (h *siteHandler) writeSiteResponse(w http.ResponseWriter, d decision, path, identity, proxy string) {
	hdr := w.Header()
	switch d.mode {
	case ModeRateLimit:
		writeError(w, d.status, "rate_limited")
	case ModeServerError:
		writeError(w, d.status, "server_error")
	case ModeCaptcha:
		hdr.Set(HeaderMarker, MarkerCaptchaPage)
		writeBody(w, d.status, "text/html; charset=utf-8", []byte(captchaHTML))
	case ModeLoginRedirect:
		hdr.Set(HeaderMarker, MarkerLoginRedirect)
		hdr.Set("Location", loginPath)
		writeBody(w, d.status, "text/html; charset=utf-8", []byte(`<a href="/login">login required</a>`+"\n"))
	case ModeEmpty:
		hdr.Set(HeaderMarker, MarkerEmptyList)
		writeJSON(w, d.status, itemsBody{OK: true, Path: path, Identity: identity, Proxy: proxy, Items: []siteItem{}})
	case ModeBusinessError:
		code := string(d.rule.BusinessCode)
		hdr.Set(HeaderBusinessCode, code)
		writeJSON(w, d.status, businessErrorBody{OK: false, Code: businessCodeValue(code), Message: "business error"})
	default: // ok, slow
		writeJSON(w, d.status, itemsBody{OK: true, Path: path, Identity: identity, Proxy: proxy, Items: makeItems(path, d.items)})
	}
}

// makeItems builds a deterministic item list for path.
func makeItems(path string, n int) []siteItem {
	items := make([]siteItem, n)
	for i := range items {
		idx := strconv.Itoa(i + 1)
		items[i] = siteItem{ID: path + "#" + idx, Title: "item " + idx}
	}
	return items
}

// businessCodeValue renders canonical integer codes as JSON numbers and anything else as JSON strings.
func businessCodeValue(code string) any {
	if n, err := strconv.ParseInt(code, 10, 64); err == nil && strconv.FormatInt(n, 10) == code {
		return json.Number(code)
	}
	return code
}
