package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// ReadinessCheck reports whether one dependency is ready.
type ReadinessCheck struct {
	Name  string
	Check func(ctx context.Context) error
}

// healthHandlers serves liveness and readiness endpoints.
type healthHandlers struct {
	checks  []ReadinessCheck
	timeout time.Duration

	mu       sync.Mutex
	draining bool
}

func newHealthHandlers(checks []ReadinessCheck) *healthHandlers {
	return &healthHandlers{checks: checks, timeout: 2 * time.Second}
}

// setDraining marks the instance as shutting down so load balancers stop
// routing new requests while in-flight requests finish.
func (h *healthHandlers) setDraining() {
	h.mu.Lock()
	h.draining = true
	h.mu.Unlock()
}

func (h *healthHandlers) isDraining() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.draining
}

// liveness always succeeds while the process can serve HTTP.
func (h *healthHandlers) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// readiness runs all checks concurrently.
func (h *healthHandlers) readiness(w http.ResponseWriter, r *http.Request) {
	if h.isDraining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "draining"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(h.checks))
	for _, c := range h.checks {
		go func(c ReadinessCheck) {
			results <- result{name: c.Name, err: c.Check(ctx)}
		}(c)
	}
	status := http.StatusOK
	details := make(map[string]string, len(h.checks))
	for range h.checks {
		res := <-results
		if res.err != nil {
			status = http.StatusServiceUnavailable
			details[res.name] = res.err.Error()
			continue
		}
		details[res.name] = "ok"
	}
	state := "ok"
	if status != http.StatusOK {
		state = "unavailable"
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": details})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
