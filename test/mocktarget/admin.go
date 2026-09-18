package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// maxAdminBodyBytes bounds admin request bodies (large proxy rule lists stay well below it).
const maxAdminBodyBytes = 8 << 20

// registerAdmin registers the admin API routes.
func (h *siteHandler) registerAdmin() {
	h.mux.HandleFunc("GET /_admin/rules", h.handleGetRules)
	h.mux.HandleFunc("PUT /_admin/rules", h.handlePutRules)
	h.mux.HandleFunc("DELETE /_admin/rules", h.handleDeleteRules)
	h.mux.HandleFunc("GET /_admin/proxies", h.handleGetProxies)
	h.mux.HandleFunc("PUT /_admin/proxies", h.handlePutProxies)
	h.mux.HandleFunc("DELETE /_admin/proxies", h.handleDeleteProxies)
	h.mux.HandleFunc("GET /_admin/stats", h.handleStats)
	h.mux.HandleFunc("POST /_admin/reset", h.handleReset)
}

// handleGetRules returns the current target site rules.
func (h *siteHandler) handleGetRules(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.st.rules.Load().rules)
}

// handlePutRules atomically replaces the target site rules.
func (h *siteHandler) handlePutRules(w http.ResponseWriter, r *http.Request) {
	body, ok := readAdminBody(w, r)
	if !ok {
		return
	}
	set, err := parseRules(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.st.rules.Store(set)
	h.logger.InfoContext(r.Context(), "site rules replaced", "count", len(set.rules))
	writeJSON(w, http.StatusOK, set.rules)
}

// handleDeleteRules resets the target site to "everything ok".
func (h *siteHandler) handleDeleteRules(w http.ResponseWriter, r *http.Request) {
	set := emptyRuleSet()
	h.st.rules.Store(set)
	h.logger.InfoContext(r.Context(), "site rules cleared")
	writeJSON(w, http.StatusOK, set.rules)
}

// handleGetProxies returns the current proxy rules.
func (h *siteHandler) handleGetProxies(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.st.proxyRules.Load().rules)
}

// handlePutProxies atomically replaces the proxy rules.
func (h *siteHandler) handlePutProxies(w http.ResponseWriter, r *http.Request) {
	body, ok := readAdminBody(w, r)
	if !ok {
		return
	}
	set, err := parseProxyRules(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.st.proxyRules.Store(set)
	h.logger.InfoContext(r.Context(), "proxy rules replaced", "count", len(set.rules))
	writeJSON(w, http.StatusOK, set.rules)
}

// handleDeleteProxies resets every proxy to ok.
func (h *siteHandler) handleDeleteProxies(w http.ResponseWriter, r *http.Request) {
	set := emptyProxyRuleSet()
	h.st.proxyRules.Store(set)
	h.logger.InfoContext(r.Context(), "proxy rules cleared")
	writeJSON(w, http.StatusOK, set.rules)
}

// handleStats returns request counters of both listeners.
// Query parameters: path_prefix, identity, proxy, mode (filters) and entries=false (omit per-key entries).
func (h *siteHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := statsFilter{
		PathPrefix: q.Get("path_prefix"),
		Identity:   q.Get("identity"),
		Proxy:      q.Get("proxy"),
		Mode:       Mode(q.Get("mode")),
		Entries:    true,
	}
	if v := q.Get("entries"); v != "" {
		entries, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid entries parameter %q", v))
			return
		}
		f.Entries = entries
	}
	writeJSON(w, http.StatusOK, h.st.stats.report(f))
}

// handleReset clears the statistics of both listeners.
func (h *siteHandler) handleReset(w http.ResponseWriter, r *http.Request) {
	h.st.stats.reset()
	h.logger.InfoContext(r.Context(), "statistics reset")
	writeJSON(w, http.StatusOK, okBody{OK: true})
}

// readAdminBody reads a bounded admin request body, writing the error response itself on failure.
func readAdminBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdminBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request body exceeds %d bytes", tooLarge.Limit))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return nil, false
	}
	return body, true
}
