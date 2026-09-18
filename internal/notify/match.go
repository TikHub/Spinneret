package notify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/notify/notifydb"
)

// Channel cache limits.
const (
	// maxChannelsPerTenant bounds the enabled channels considered for one alert.
	maxChannelsPerTenant = 1000
	// maxCachedTenants bounds the channel cache; it is reset when exceeded.
	maxCachedTenants = 4096
)

// target is an enabled channel prepared for alert matching and delivery.
type target struct {
	id          string
	name        string
	kind        string
	namespaceID string
	eventTypes  []string
	siteIDs     []string
	minSeverity string
	cfg         channelConfig
	// cfgErr is set when the settings could not be decrypted; deliveries to
	// the channel then fail immediately.
	cfgErr error
}

// matches reports whether an alert must be delivered through the channel:
// tenant-wide channels receive alerts of every namespace; namespace channels
// receive the alerts of their namespace and tenant-level alerts (which concern
// every namespace, e.g. report_backlog) and, when restricted to sites, only
// alerts of those sites or alerts that concern no site at all.
func (t target) matches(a Alert) bool {
	if t.namespaceID != "" && a.NamespaceID != "" && t.namespaceID != a.NamespaceID {
		return false
	}
	if len(t.eventTypes) > 0 && !slices.Contains(t.eventTypes, a.Kind) {
		return false
	}
	if t.namespaceID != "" && len(t.siteIDs) > 0 && a.SiteID != "" && !slices.Contains(t.siteIDs, a.SiteID) {
		return false
	}
	return severityRank(a.Severity) >= severityRank(t.minSeverity)
}

type cacheEntry struct {
	loadedAt time.Time
	targets  []target
}

// channelCache caches the enabled channels (with decrypted settings) of
// tenants for a short TTL. Local channel mutations invalidate it immediately;
// changes made on other instances become visible after the TTL.
type channelCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]cacheEntry
	// generation increases on every invalidation so that a load that started
	// before a local mutation cannot store its stale result afterwards.
	generation uint64
}

func newChannelCache(ttl time.Duration) *channelCache {
	return &channelCache{ttl: ttl, entries: map[string]cacheEntry{}}
}

// get returns the cached targets of a tenant, or false together with the
// cache generation to pass to put after loading them.
func (c *channelCache) get(tenantID string, now time.Time) ([]target, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tenantID]
	if !ok {
		return nil, c.generation, false
	}
	if now.Sub(e.loadedAt) >= c.ttl || now.Before(e.loadedAt) {
		// Expired entries hold decrypted settings: drop them.
		delete(c.entries, tenantID)
		return nil, c.generation, false
	}
	return e.targets, c.generation, true
}

// put stores loaded targets unless the cache was invalidated since generation.
func (c *channelCache) put(tenantID string, targets []target, now time.Time, generation uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation {
		return
	}
	if len(c.entries) >= maxCachedTenants {
		c.entries = map[string]cacheEntry{}
	}
	c.entries[tenantID] = cacheEntry{loadedAt: now, targets: targets}
}

func (c *channelCache) invalidate(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	delete(c.entries, tenantID)
}

// enabledTargets returns the enabled channels of a tenant (cached).
func (s *Service) enabledTargets(ctx context.Context, tenantID string) ([]target, error) {
	now := s.now()
	targets, generation, ok := s.channels.get(tenantID, now)
	if ok {
		return targets, nil
	}
	rows, err := s.q.NotifyChannelListEnabled(ctx, notifydb.NotifyChannelListEnabledParams{
		TenantID: tenantID, LimitRows: maxChannelsPerTenant,
	})
	if err != nil {
		return nil, fmt.Errorf("load channels of tenant %s: %w", tenantID, err)
	}
	if len(rows) == maxChannelsPerTenant {
		s.logger.Warn("tenant has too many enabled channels; only the first are matched",
			slog.String("tenant_id", tenantID), slog.Int("limit", maxChannelsPerTenant))
	}
	targets = make([]target, 0, len(rows))
	for _, row := range rows {
		targets = append(targets, s.targetOf(row))
	}
	s.channels.put(tenantID, targets, now, generation)
	return targets, nil
}

// targetOf prepares a channel row for delivery.
func (s *Service) targetOf(row notifydb.NotificationChannel) target {
	t := target{
		id:          row.ID,
		name:        row.Name,
		kind:        row.Kind,
		namespaceID: deref(row.NamespaceID),
		eventTypes:  row.EventTypes,
		siteIDs:     row.SiteIds,
		minSeverity: row.MinSeverity,
	}
	cfg, err := s.openConfig(row)
	if err != nil {
		s.logger.Error("open channel config failed", slog.String("channel_id", row.ID), slog.Any("error", err))
		t.cfgErr = failure(true, "channel settings cannot be decrypted")
		return t
	}
	t.cfg = cfg
	return t
}

// matchTargets returns the channels an alert must be delivered through.
func (s *Service) matchTargets(ctx context.Context, a Alert) ([]target, error) {
	all, err := s.enabledTargets(ctx, a.TenantID)
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(all))
	for _, t := range all {
		if t.matches(a) {
			out = append(out, t)
		}
	}
	return out, nil
}
