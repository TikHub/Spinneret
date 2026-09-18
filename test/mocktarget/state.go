package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// maxLabelLen bounds identity and proxy labels kept in headers and statistics.
const maxLabelLen = 256

// state is the runtime state shared by the target site and the forward proxy.
type state struct {
	rules      atomic.Pointer[ruleSet]
	proxyRules atomic.Pointer[proxyRuleSet]
	stats      *stats
	tunnels    *tunnelRegistry
	rng        *rngSource
}

// newState creates the shared state with "everything ok" rules.
func newState(maxStatKeys int, seed uint64) *state {
	s := &state{stats: newStats(maxStatKeys), tunnels: &tunnelRegistry{}, rng: newRNGSource(seed)}
	s.rules.Store(emptyRuleSet())
	s.proxyRules.Store(emptyProxyRuleSet())
	return s
}

// rngSource hands out independent per-request random generators without a shared lock.
// Each draw seeds a PCG from the process seed and a request counter, so a fixed seed and
// request order reproduce the same decisions.
type rngSource struct {
	seed    uint64
	counter atomic.Uint64
}

// newRNGSource creates a source; seed 0 selects a random seed.
func newRNGSource(seed uint64) *rngSource {
	if seed == 0 {
		seed = rand.Uint64() | 1
	}
	return &rngSource{seed: seed}
}

// float64 returns a uniformly distributed value in [0,1).
func (s *rngSource) float64() float64 {
	var pcg rand.PCG
	pcg.Seed(s.seed, mix64(s.counter.Add(1)))
	return float64(pcg.Uint64()>>11) / (1 << 53)
}

// trigger reports whether an event with probability p happens.
func (s *rngSource) trigger(p float64) bool {
	switch {
	case p >= 1:
		return true
	case p <= 0:
		return false
	default:
		return s.float64() < p
	}
}

// mix64 is the SplitMix64 finalizer; it decorrelates consecutive counters used as seeds.
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// tunnelRegistry maps the local address of proxy-dialed upstream connections to the proxy id,
// so the target site can attribute requests that arrive through a CONNECT tunnel.
type tunnelRegistry struct {
	m sync.Map // local addr string -> proxy id string
}

// register records addr as belonging to proxyID and returns the function removing it.
func (t *tunnelRegistry) register(addr, proxyID string) (unregister func()) {
	t.m.Store(addr, proxyID)
	return func() { t.m.CompareAndDelete(addr, proxyID) }
}

// lookup returns the proxy id of a tunneled connection whose remote address is addr.
func (t *tunnelRegistry) lookup(addr string) (string, bool) {
	v, ok := t.m.Load(addr)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// sleepCtx sleeps for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// millis converts milliseconds to a duration.
func millis(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// truncateLabel bounds a label to maxLabelLen bytes without splitting a UTF-8 sequence.
func truncateLabel(s string) string {
	if len(s) <= maxLabelLen {
		return s
	}
	n := maxLabelLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// errorBody is the JSON body of error responses.
type errorBody struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// okBody is the JSON body of plain success responses.
type okBody struct {
	OK bool `json:"ok"`
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"ok":false,"error":"encode response"}`)
	}
	writeBody(w, status, "application/json; charset=utf-8", data)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{OK: false, Error: msg})
}

// writeBody writes a complete response body. Write errors mean the client went away and are ignored.
func writeBody(w http.ResponseWriter, status int, contentType string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
