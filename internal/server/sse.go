package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

// sseEventPermission maps console event types to the permission needed to receive them.
var sseEventPermission = map[string]authz.Permission{
	"breaker.transition": authz.PermBreakerRead,
	"identity.state":     authz.PermIdentityRead,
	"proxy.state":        authz.PermProxyRead,
	"alert":              authz.PermNotifyRead,
	"config.published":   authz.PermConfigRead,
	"policy.published":   authz.PermPolicyRead,
}

// sseHandler streams namespace events to console users as Server-Sent Events.
type sseHandler struct {
	catalog   catalog.Catalog
	bus       events.Bus
	logger    *slog.Logger
	heartbeat time.Duration
	maxConns  int64
	conns     atomic.Int64
}

func newSSEHandler(cat catalog.Catalog, bus events.Bus, logger *slog.Logger) *sseHandler {
	return &sseHandler{catalog: cat, bus: bus, logger: logger, heartbeat: 15 * time.Second, maxConns: 5000}
}

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p, ok := authz.FromContext(r.Context())
	if !ok {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	nsName := r.URL.Query().Get("namespace")
	if p.TenantID == "" || nsName == "" {
		http.Error(w, "tenant and namespace are required", http.StatusBadRequest)
		return
	}
	ns, found := h.catalog.NamespaceByName(p.TenantID, nsName)
	if !found || !p.Can(authz.PermNamespaceRead, authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}) {
		http.Error(w, "namespace not found", http.StatusNotFound)
		return
	}
	if h.conns.Add(1) > h.maxConns {
		h.conns.Add(-1)
		http.Error(w, "too many event streams", http.StatusServiceUnavailable)
		return
	}
	defer h.conns.Add(-1)

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	// Long-lived stream: lift the server write deadline for this response.
	_ = rc.SetWriteDeadline(time.Time{})

	queue := make(chan events.Event, 256)
	unsubscribe := h.bus.Subscribe(events.NamespaceChannel(ns.ID), func(_ context.Context, _ string, ev events.Event) {
		select {
		case queue <- ev:
		default: // slow consumer: drop rather than block the bus
		}
	})
	defer unsubscribe()

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	if _, err := fmt.Fprintf(w, "retry: 5000\n: connected\n\n"); err != nil {
		return
	}
	_ = rc.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case ev := <-queue:
			if !h.allowed(p, ns, ev) {
				continue
			}
			payload, err := json.Marshal(ev)
			if err != nil {
				h.logger.Warn("encode sse event", slog.Any("error", err))
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, payload); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// allowed reports whether the principal may see the event.
func (h *sseHandler) allowed(p *authz.Principal, ns *catalog.Namespace, ev events.Event) bool {
	perm, ok := sseEventPermission[ev.Type]
	if !ok {
		return false
	}
	res := authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
	if ev.SiteID != "" {
		s, found := ns.SitesByID[ev.SiteID]
		if !found {
			return false
		}
		res.SiteID, res.SiteName = s.ID, s.Name
	}
	return p.Can(perm, res)
}
