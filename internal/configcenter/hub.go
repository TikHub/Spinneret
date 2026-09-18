package configcenter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

const (
	// refreshBatch bounds the keys reloaded in one refresh round.
	refreshBatch = 5_000
	// refreshRetryDelay is the pause after a failed refresh round.
	refreshRetryDelay = time.Second
	// evictTargetPercent is the share of maxEntries kept after a forced eviction.
	evictTargetPercent = 90
	// forcedEvictInterval bounds how often a full cache is scanned for victims.
	forcedEvictInterval = time.Second
)

// itemKey identifies an item by name (IDs change when an item is deleted and
// created again; nodes address items by name).
type itemKey struct {
	ns    string
	group string
	key   string
}

// itemVersion is the published state of an item: id is empty for runtime
// items and for missing items, version is 0 when the item does not exist or
// was never published.
type itemVersion struct {
	id      string
	version int32
}

// versionLoader reads the current versions of keys from the source of truth.
// Keys absent from the result do not exist.
type versionLoader func(ctx context.Context, keys []itemKey) (map[itemKey]itemVersion, error)

type hubEntry struct {
	cur      itemVersion
	loadedAt time.Time
	waiters  map[*waiter]struct{}
}

// waiter is one blocked WatchConfig call. It is used by a single goroutine;
// registered is guarded by the hub mutex.
type waiter struct {
	keys       []itemKey
	held       []int32
	wake       chan struct{}
	registered bool
}

func newWaiter(keys []itemKey, held []int32) *waiter {
	return &waiter{keys: keys, held: held, wake: make(chan struct{}, 1)}
}

// change is a watched item whose current version differs from the held one.
type change struct {
	index int
	cur   itemVersion
}

type hubConfig struct {
	maxWatchers int
	maxEntries  int
	ttl         time.Duration
	gauge       prometheus.Gauge
}

// hub caches current item versions and wakes blocked watchers of changed
// items. Versions are loaded lazily, reloaded when bus events mark keys dirty
// (the loaded value, not the event payload, is trusted, so out-of-order
// events cannot regress versions) and periodically re-verified for watched
// keys. Each waiter costs O(items) memory; notifications only wake waiters
// registered on the affected keys.
type hub struct {
	cfg  hubConfig
	load versionLoader
	now  func() time.Time

	mu       sync.Mutex
	entries  map[itemKey]*hubEntry
	dirty    map[itemKey]struct{}
	seq      uint64 // incremented by every invalidation
	watchers int
	// lastEvict rate-limits forced evictions (full scans) of a full cache.
	lastEvict time.Time

	signal chan struct{}
}

func newHub(cfg hubConfig, load versionLoader) *hub {
	return &hub{
		cfg:     cfg,
		load:    load,
		now:     time.Now,
		entries: make(map[itemKey]*hubEntry),
		dirty:   make(map[itemKey]struct{}),
		signal:  make(chan struct{}, 1),
	}
}

// snapshot returns the current versions of keys, loading uncached or expired
// entries. The returned sequence number must be passed to register.
func (h *hub) snapshot(ctx context.Context, keys []itemKey) ([]itemVersion, uint64, error) {
	return h.read(ctx, keys, false)
}

// reload reads the current versions of keys from the source of truth,
// bypassing cached entries, and stores them.
func (h *hub) reload(ctx context.Context, keys []itemKey) ([]itemVersion, error) {
	out, _, err := h.read(ctx, keys, true)
	return out, err
}

// read implements snapshot (force false) and reload (force true).
func (h *hub) read(ctx context.Context, keys []itemKey, force bool) ([]itemVersion, uint64, error) {
	out := make([]itemVersion, len(keys))
	var missing []itemKey
	var missingIdx []int
	h.mu.Lock()
	now := h.now()
	for i, k := range keys {
		if e, ok := h.entries[k]; ok && !force && (len(e.waiters) > 0 || now.Sub(e.loadedAt) < h.cfg.ttl) {
			out[i] = e.cur
			continue
		}
		missing = append(missing, k)
		missingIdx = append(missingIdx, i)
	}
	seq := h.seq
	h.mu.Unlock()
	if len(missing) == 0 {
		return out, seq, nil
	}

	loaded, err := h.load(ctx, missing)
	if err != nil {
		return nil, 0, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	raced := h.seq != seq
	now = h.now()
	for j, k := range missing {
		v := loaded[k]
		out[missingIdx[j]] = v
		e, ok := h.entries[k]
		if !ok {
			if len(h.entries) >= h.cfg.maxEntries && now.Sub(h.lastEvict) >= forcedEvictInterval {
				h.lastEvict = now
				h.evictLocked(now, true)
			}
			if len(h.entries) >= h.cfg.maxEntries {
				continue
			}
			e = &hubEntry{cur: v, loadedAt: now}
			h.entries[k] = e
		} else {
			h.setLocked(e, v, now)
			// A concurrent reload may already hold a newer version.
			out[missingIdx[j]] = e.cur
		}
		if raced {
			h.dirty[k] = struct{}{}
		}
	}
	if raced {
		h.notifyLocked()
	}
	return out, h.seq, nil
}

// register compares the waiter's held versions with the current ones (cached
// entries win over the snapshot) and registers the waiter when nothing
// changed or force is set. It fails with resource_exhausted when the watcher
// limit is reached.
func (h *hub) register(w *waiter, snap []itemVersion, seq uint64, force bool) ([]change, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	changes := h.changesLocked(w, snap)
	if w.registered || (len(changes) > 0 && !force) {
		return changes, nil
	}
	if h.watchers >= h.cfg.maxWatchers {
		return changes, apperr.ResourceExhausted(apperr.ReasonRateLimited, watchRetryAfterMs,
			"too many concurrent config watchers on this instance, retry later")
	}
	now := h.now()
	raced := h.seq != seq
	for i, k := range w.keys {
		e, ok := h.entries[k]
		if !ok {
			e = &hubEntry{cur: snap[i], loadedAt: now}
			h.entries[k] = e
		}
		if e.waiters == nil {
			e.waiters = make(map[*waiter]struct{}, 1)
		}
		e.waiters[w] = struct{}{}
		if raced {
			h.dirty[k] = struct{}{}
		}
	}
	if raced {
		h.notifyLocked()
	}
	w.registered = true
	h.watchers++
	h.setGaugeLocked()
	return changes, nil
}

// changesLocked lists items whose current version is published and differs
// from the held version. Deleted or unpublished items never count as changed.
func (h *hub) changesLocked(w *waiter, snap []itemVersion) []change {
	var changes []change
	for i, k := range w.keys {
		cur := snap[i]
		if e, ok := h.entries[k]; ok {
			cur = e.cur
		}
		if cur.version > 0 && cur.version != w.held[i] {
			changes = append(changes, change{index: i, cur: cur})
		}
	}
	return changes
}

// unregister removes a registered waiter. It is idempotent.
func (h *hub) unregister(w *waiter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !w.registered {
		return
	}
	now := h.now()
	for _, k := range w.keys {
		e, ok := h.entries[k]
		if !ok {
			continue
		}
		delete(e.waiters, w)
		if len(e.waiters) == 0 {
			// The entry was kept fresh by events and resyncs while it was
			// watched, so its TTL restarts now. This avoids a reload burst
			// when many watchers of the same item time out together.
			e.waiters = nil
			e.loadedAt = now
		}
	}
	w.registered = false
	h.watchers--
	h.setGaugeLocked()
}

// invalidate marks a key as changed at the source (a bus event). The key is
// reloaded by the refresher when it is cached.
func (h *hub) invalidate(k itemKey) {
	h.mu.Lock()
	h.seq++
	if _, ok := h.entries[k]; ok {
		h.dirty[k] = struct{}{}
	}
	h.mu.Unlock()
	h.notify()
}

// markDirty schedules cached keys for re-verification without counting as a
// source change (used when resolved content disagrees with the cache).
func (h *hub) markDirty(keys []itemKey) {
	h.mu.Lock()
	added := false
	for _, k := range keys {
		if _, ok := h.entries[k]; ok {
			h.dirty[k] = struct{}{}
			added = true
		}
	}
	h.mu.Unlock()
	if added {
		h.notify()
	}
}

// setLocked stores a loaded version and wakes the entry's waiters when it
// changed. Versions of one stored item (same ID) only grow, so a lower
// version of the same item is a stale read that finished after a newer one
// and is ignored.
func (h *hub) setLocked(e *hubEntry, v itemVersion, now time.Time) {
	e.loadedAt = now
	if e.cur == v || (v.id != "" && v.id == e.cur.id && v.version < e.cur.version) {
		return
	}
	e.cur = v
	for w := range e.waiters {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (h *hub) notify() {
	select {
	case h.signal <- struct{}{}:
	default:
	}
}

func (h *hub) notifyLocked() { h.notify() }

func (h *hub) setGaugeLocked() {
	if h.cfg.gauge != nil {
		h.cfg.gauge.Set(float64(h.watchers))
	}
}

// evictLocked drops unwatched expired entries; when force is set and the
// cache is still full it also drops unwatched entries down to the target.
func (h *hub) evictLocked(now time.Time, force bool) {
	for k, e := range h.entries {
		if len(e.waiters) == 0 && now.Sub(e.loadedAt) >= h.cfg.ttl {
			delete(h.entries, k)
			delete(h.dirty, k)
		}
	}
	if !force {
		return
	}
	target := h.cfg.maxEntries * evictTargetPercent / 100
	for k, e := range h.entries {
		if len(h.entries) <= target {
			return
		}
		if len(e.waiters) == 0 {
			delete(h.entries, k)
			delete(h.dirty, k)
		}
	}
}

// resyncWatchedLocked schedules every watched key for re-verification.
func (h *hub) resyncWatchedLocked() bool {
	added := false
	for k, e := range h.entries {
		if len(e.waiters) > 0 {
			h.dirty[k] = struct{}{}
			added = true
		}
	}
	return added
}

// run processes dirty keys and periodic resyncs until ctx is done.
func (h *hub) run(ctx context.Context, opTimeout time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(h.cfg.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.signal:
		case <-ticker.C:
			h.mu.Lock()
			h.evictLocked(h.now(), false)
			h.resyncWatchedLocked()
			h.mu.Unlock()
		}
		for h.refresh(ctx, opTimeout, logger) {
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// refresh reloads one batch of dirty keys. It returns true when more dirty
// keys remain and another round should run immediately.
func (h *hub) refresh(ctx context.Context, opTimeout time.Duration, logger *slog.Logger) bool {
	h.mu.Lock()
	keys := make([]itemKey, 0, min(len(h.dirty), refreshBatch))
	for k := range h.dirty {
		if len(keys) == refreshBatch {
			break
		}
		keys = append(keys, k)
		delete(h.dirty, k)
	}
	more := len(h.dirty) > 0
	h.mu.Unlock()
	if len(keys) == 0 {
		return false
	}

	lctx, cancel := context.WithTimeout(ctx, opTimeout)
	loaded, err := h.load(lctx, keys)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		logger.Warn("reload config versions failed", slog.Int("keys", len(keys)), slog.Any("error", err))
		h.mu.Lock()
		for _, k := range keys {
			if _, ok := h.entries[k]; ok {
				h.dirty[k] = struct{}{}
			}
		}
		h.mu.Unlock()
		timer := time.NewTimer(refreshRetryDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			h.notify()
		}
		return false
	}

	h.mu.Lock()
	now := h.now()
	for _, k := range keys {
		if e, ok := h.entries[k]; ok {
			h.setLocked(e, loaded[k], now)
		}
	}
	h.mu.Unlock()
	return more
}

// stats returns the number of cached entries and registered watchers.
func (h *hub) stats() (entries, watchers int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries), h.watchers
}
