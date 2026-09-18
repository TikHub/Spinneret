// Package events is the cluster-wide event bus: in-process fan-out to local
// subscribers plus Redis Pub/Sub ("P:ch:<channel>") to reach peer instances.
// It carries cache invalidations (catalog, tokens), config and runtime
// version bumps, and namespace-scoped console events (spec §11). Delivery is
// best effort: consumers must tolerate lost events (the catalog also reloads
// periodically as a safety net).
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Well-known channels.
const (
	// ChannelCatalog invalidates namespace catalog snapshots; data {"ns":"…"}.
	ChannelCatalog = "catalog"
	// ChannelTokens invalidates cached token verifications; data {"token_id":"…"}.
	ChannelTokens = "tokens"
	// ChannelConfig announces published config versions; data {"ns","group","key","version"}.
	ChannelConfig = "config"
	// ChannelRuntime announces runtime config version bumps; data {"ns","kind","version"}.
	ChannelRuntime = "runtime"
	// ChannelAll subscribes to every channel. It cannot be published to.
	ChannelAll = "*"
)

// Console event types published on namespace channels.
const (
	TypeBreakerTransition = "breaker.transition"
	TypeIdentityState     = "identity.state"
	TypeProxyState        = "proxy.state"
	TypeAlert             = "alert"
	TypeConfigPublished   = "config.published"
	TypePolicyPublished   = "policy.published"
)

// maxChannelLen bounds channel names so that keys stay reasonable.
const maxChannelLen = 256

// ErrInvalidChannel is returned by Publish for an empty, wildcard or oversized channel name.
var ErrInvalidChannel = errors.New("events: invalid channel")

// ErrInvalidData is returned by Publish when Event.Data is not valid JSON.
var ErrInvalidData = errors.New("events: event data is not valid JSON")

// NamespaceChannel returns the console event channel of a namespace, "ns:<id>".
func NamespaceChannel(namespaceID string) string {
	return "ns:" + namespaceID
}

// Event is the unit carried by the bus.
type Event struct {
	// Type is the event type, e.g. "breaker.transition" or "invalidate".
	Type string `json:"type"`
	// TenantID, NamespaceID and SiteID scope the event (used for permission filtering).
	TenantID    string `json:"tenant_id,omitempty"`
	NamespaceID string `json:"namespace_id,omitempty"`
	SiteID      string `json:"site_id,omitempty"`
	// At is when the event was produced; Publish fills it when zero.
	At time.Time `json:"at"`
	// Data is the channel-specific JSON payload.
	Data json.RawMessage `json:"data,omitempty"`
}

// Handler consumes events. Handlers must be fast and must not block for long:
// local delivery runs synchronously inside Publish and remote delivery runs
// sequentially on the bus goroutine. A panicking handler is recovered and logged.
type Handler func(ctx context.Context, channel string, ev Event)

// Bus publishes events to local subscribers and peer instances.
type Bus interface {
	// Publish delivers ev to local subscribers synchronously, then broadcasts it
	// to peer instances (Redis Pub/Sub). Local delivery happens even when the
	// broadcast fails; the broadcast error is returned.
	Publish(ctx context.Context, channel string, ev Event) error
	// Subscribe registers h for channel ("*" receives every channel) and returns
	// an idempotent unsubscribe function.
	Subscribe(channel string, h Handler) (unsubscribe func())
	// Run receives events from peers until ctx is done (reconnecting on
	// failures) and then returns nil.
	Run(ctx context.Context) error
}

// prepare validates a publish request and fills defaults, returning the event to deliver.
func prepare(channel string, ev Event, now func() time.Time) (Event, error) {
	if channel == "" || channel == ChannelAll || len(channel) > maxChannelLen {
		return Event{}, fmt.Errorf("%w: %q", ErrInvalidChannel, channel)
	}
	if len(ev.Data) > 0 && !json.Valid(ev.Data) {
		return Event{}, ErrInvalidData
	}
	if ev.At.IsZero() {
		ev.At = now().UTC()
	}
	return ev, nil
}
