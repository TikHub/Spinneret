package configcenter

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// fakeVersions is an in-memory versionLoader.
type fakeVersions struct {
	mu       sync.Mutex
	versions map[itemKey]itemVersion
	err      error
	calls    atomic.Int64
	// block, when set, is closed by the test to release a pending load.
	block chan struct{}
}

func newFakeVersions() *fakeVersions {
	return &fakeVersions{versions: make(map[itemKey]itemVersion)}
}

func (f *fakeVersions) set(k itemKey, v int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v == 0 {
		delete(f.versions, k)
		return
	}
	f.versions[k] = itemVersion{id: "cfg_" + k.key, version: v}
}

func (f *fakeVersions) load(ctx context.Context, keys []itemKey) (map[itemKey]itemVersion, error) {
	f.calls.Add(1)
	f.mu.Lock()
	block, err := f.block, f.err
	out := make(map[itemKey]itemVersion, len(keys))
	for _, k := range keys {
		if v, ok := f.versions[k]; ok {
			out[k] = v
		}
	}
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return out, err
}

func testHub(t *testing.T, cfg hubConfig, f *fakeVersions) *hub {
	t.Helper()
	if cfg.maxWatchers == 0 {
		cfg.maxWatchers = 100
	}
	if cfg.maxEntries == 0 {
		cfg.maxEntries = 100
	}
	if cfg.ttl == 0 {
		cfg.ttl = time.Hour
	}
	return newHub(cfg, f.load)
}

func runHub(t *testing.T, h *hub) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.run(ctx, time.Second, slog.New(slog.DiscardHandler))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

func awaitWake(t *testing.T, w *waiter, within time.Duration) {
	t.Helper()
	select {
	case <-w.wake:
	case <-time.After(within):
		t.Fatalf("waiter was not woken within %s", within)
	}
}

func requireNoWake(t *testing.T, w *waiter, within time.Duration) {
	t.Helper()
	select {
	case <-w.wake:
		t.Fatal("waiter was woken unexpectedly")
	case <-time.After(within):
	}
}

func TestHubRegisterAndWake(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	a := itemKey{ns: "ns1", group: "g", key: "a"}
	b := itemKey{ns: "ns1", group: "g", key: "b"}
	f.set(a, 1)
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "w"})
	h := testHub(t, hubConfig{gauge: gauge}, f)
	runHub(t, h)
	ctx := context.Background()

	wa := newWaiter([]itemKey{a}, []int32{1})
	wb := newWaiter([]itemKey{b}, []int32{0})
	for _, w := range []*waiter{wa, wb} {
		snap, seq, err := h.snapshot(ctx, w.keys)
		require.NoError(t, err)
		changes, err := h.register(w, snap, seq, false)
		require.NoError(t, err)
		require.Empty(t, changes)
		require.True(t, w.registered)
	}
	require.InDelta(t, 2, gaugeValue(t, gauge), 0)

	// A change of a only wakes the waiter of a.
	f.set(a, 2)
	h.invalidate(a)
	awaitWake(t, wa, time.Second)
	requireNoWake(t, wb, 50*time.Millisecond)
	snap, seq, err := h.snapshot(ctx, wa.keys)
	require.NoError(t, err)
	changes, err := h.register(wa, snap, seq, false)
	require.NoError(t, err)
	require.Equal(t, []change{{index: 0, cur: itemVersion{id: "cfg_a", version: 2}}}, changes)

	// Publishing b (held version 0) wakes b.
	f.set(b, 1)
	h.invalidate(b)
	awaitWake(t, wb, time.Second)

	// Deleting a does not count as a change.
	f.set(a, 0)
	h.invalidate(a)
	awaitWake(t, wa, time.Second)
	snap, seq, err = h.snapshot(ctx, wa.keys)
	require.NoError(t, err)
	changes, err = h.register(wa, snap, seq, false)
	require.NoError(t, err)
	require.Empty(t, changes)

	h.unregister(wa)
	h.unregister(wa) // idempotent
	h.unregister(wb)
	entries, watchers := h.stats()
	require.Equal(t, 2, entries)
	require.Zero(t, watchers)
	require.InDelta(t, 0, gaugeValue(t, gauge), 0)
}

func TestHubImmediateChangeDoesNotRegister(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	k := itemKey{ns: "ns", group: "g", key: "k"}
	f.set(k, 3)
	h := testHub(t, hubConfig{}, f)
	w := newWaiter([]itemKey{k}, []int32{2})
	snap, seq, err := h.snapshot(context.Background(), w.keys)
	require.NoError(t, err)
	changes, err := h.register(w, snap, seq, false)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.False(t, w.registered)

	// force registers despite the change.
	changes, err = h.register(w, snap, seq, true)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.True(t, w.registered)
	h.unregister(w)
}

func TestHubMaxWatchers(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	h := testHub(t, hubConfig{maxWatchers: 1}, f)
	k := itemKey{ns: "ns", group: "g", key: "k"}
	w1 := newWaiter([]itemKey{k}, []int32{0})
	w2 := newWaiter([]itemKey{k}, []int32{0})
	snap, seq, err := h.snapshot(context.Background(), []itemKey{k})
	require.NoError(t, err)
	_, err = h.register(w1, snap, seq, false)
	require.NoError(t, err)
	_, err = h.register(w2, snap, seq, false)
	require.Error(t, err)
	require.Equal(t, apperr.ReasonRateLimited, apperr.ReasonOf(err))
	h.unregister(w1)
	_, err = h.register(w2, snap, seq, false)
	require.NoError(t, err)
	h.unregister(w2)
}

func TestHubSnapshotCachesAndExpires(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	k := itemKey{ns: "ns", group: "g", key: "k"}
	f.set(k, 1)
	h := testHub(t, hubConfig{ttl: time.Minute}, f)
	now := time.Unix(1_000, 0)
	h.now = func() time.Time { return now }
	ctx := context.Background()

	_, _, err := h.snapshot(ctx, []itemKey{k})
	require.NoError(t, err)
	_, _, err = h.snapshot(ctx, []itemKey{k})
	require.NoError(t, err)
	require.EqualValues(t, 1, f.calls.Load())

	now = now.Add(2 * time.Minute)
	f.set(k, 2)
	snap, _, err := h.snapshot(ctx, []itemKey{k})
	require.NoError(t, err)
	require.EqualValues(t, 2, f.calls.Load())
	require.Equal(t, int32(2), snap[0].version)

	f.err = errors.New("boom")
	now = now.Add(2 * time.Minute)
	_, _, err = h.snapshot(ctx, []itemKey{k})
	require.ErrorContains(t, err, "boom")
}

func TestHubSnapshotRaceMarksDirty(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	k := itemKey{ns: "ns", group: "g", key: "k"}
	f.set(k, 1)
	h := testHub(t, hubConfig{}, f)
	f.block = make(chan struct{})

	type result struct {
		snap []itemVersion
		seq  uint64
		err  error
	}
	res := make(chan result, 1)
	go func() {
		snap, seq, err := h.snapshot(context.Background(), []itemKey{k})
		res <- result{snap, seq, err}
	}()
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, time.Second, time.Millisecond)
	h.invalidate(k) // an event arrives while the load is in flight
	f.mu.Lock()
	close(f.block)
	f.block = nil
	f.mu.Unlock()
	r := <-res
	require.NoError(t, r.err)

	h.mu.Lock()
	_, dirty := h.dirty[k]
	h.mu.Unlock()
	require.True(t, dirty, "a load racing with an invalidation must be re-verified")

	// register with a stale sequence marks keys dirty as well.
	w := newWaiter([]itemKey{k}, []int32{1})
	h.mu.Lock()
	clear(h.dirty)
	h.mu.Unlock()
	_, err := h.register(w, r.snap, r.seq-1, false)
	require.NoError(t, err)
	h.mu.Lock()
	_, dirty = h.dirty[k]
	h.mu.Unlock()
	require.True(t, dirty)
	h.unregister(w)
}

func TestHubRefreshRetriesAfterFailure(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	k := itemKey{ns: "ns", group: "g", key: "k"}
	f.set(k, 1)
	h := testHub(t, hubConfig{}, f)
	w := newWaiter([]itemKey{k}, []int32{1})
	snap, seq, err := h.snapshot(context.Background(), w.keys)
	require.NoError(t, err)
	_, err = h.register(w, snap, seq, false)
	require.NoError(t, err)

	f.mu.Lock()
	f.err = errors.New("database down")
	f.mu.Unlock()
	f.set(k, 2)
	runHub(t, h)
	h.invalidate(k)
	require.Eventually(t, func() bool { return f.calls.Load() >= 2 }, time.Second, time.Millisecond)
	requireNoWake(t, w, 50*time.Millisecond)

	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	awaitWake(t, w, 3*time.Second)
	h.unregister(w)
}

func TestHubResyncAndEviction(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	watched := itemKey{ns: "ns", group: "g", key: "watched"}
	idle := itemKey{ns: "ns", group: "g", key: "idle"}
	f.set(watched, 1)
	f.set(idle, 1)
	h := testHub(t, hubConfig{ttl: 30 * time.Millisecond}, f)
	ctx := context.Background()
	_, _, err := h.snapshot(ctx, []itemKey{idle})
	require.NoError(t, err)
	w := newWaiter([]itemKey{watched}, []int32{1})
	snap, seq, err := h.snapshot(ctx, w.keys)
	require.NoError(t, err)
	_, err = h.register(w, snap, seq, false)
	require.NoError(t, err)

	// A lost event: the version changes without invalidation. The periodic
	// resync of watched keys still wakes the waiter.
	f.set(watched, 2)
	runHub(t, h)
	awaitWake(t, w, time.Second)
	require.Eventually(t, func() bool {
		entries, _ := h.stats()
		return entries == 1
	}, time.Second, 5*time.Millisecond, "idle entries expire")
	h.unregister(w)
}

func TestHubForcedEvictionWhenFull(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	h := testHub(t, hubConfig{maxEntries: 10, ttl: time.Hour}, f)
	ctx := context.Background()
	pinned := itemKey{ns: "ns", group: "g", key: "pinned"}
	w := newWaiter([]itemKey{pinned}, []int32{0})
	snap, seq, err := h.snapshot(ctx, w.keys)
	require.NoError(t, err)
	_, err = h.register(w, snap, seq, false)
	require.NoError(t, err)
	for i := range 9 {
		_, _, err := h.snapshot(ctx, []itemKey{{ns: "ns", group: "g", key: string(rune('a' + i))}})
		require.NoError(t, err)
	}
	entries, _ := h.stats()
	require.Equal(t, 10, entries)

	// The next insert forces an eviction down to 90% keeping pinned entries.
	_, _, err = h.snapshot(ctx, []itemKey{{ns: "ns", group: "g", key: "new"}})
	require.NoError(t, err)
	entries, _ = h.stats()
	require.LessOrEqual(t, entries, 10)
	h.mu.Lock()
	_, ok := h.entries[pinned]
	h.mu.Unlock()
	require.True(t, ok)

	// Within the rate limit a full cache simply does not cache new keys.
	for i := range 20 {
		_, _, err := h.snapshot(ctx, []itemKey{{ns: "ns2", group: "g", key: string(rune('a' + i))}})
		require.NoError(t, err)
	}
	entries, _ = h.stats()
	require.LessOrEqual(t, entries, 10)

	// markDirty only affects cached keys.
	h.markDirty([]itemKey{pinned, {ns: "nope", group: "g", key: "k"}})
	h.mu.Lock()
	_, dirtyPinned := h.dirty[pinned]
	dirtyCount := len(h.dirty)
	h.mu.Unlock()
	require.True(t, dirtyPinned)
	require.Equal(t, 1, dirtyCount)
	h.unregister(w)
}

func TestHubIgnoresStaleLowerVersionOfSameItem(t *testing.T) {
	t.Parallel()
	f := newFakeVersions()
	k := itemKey{ns: "ns", group: "g", key: "k"}
	f.set(k, 3)
	h := testHub(t, hubConfig{ttl: time.Hour}, f)
	ctx := context.Background()
	w := newWaiter([]itemKey{k}, []int32{3})
	snap, seq, err := h.snapshot(ctx, w.keys)
	require.NoError(t, err)
	_, err = h.register(w, snap, seq, false)
	require.NoError(t, err)

	// A read that started before the publish of v3 finishes late with v2.
	h.mu.Lock()
	h.setLocked(h.entries[k], itemVersion{id: "cfg_k", version: 2}, h.now())
	cur := h.entries[k].cur
	h.mu.Unlock()
	require.Equal(t, int32(3), cur.version)
	requireNoWake(t, w, 20*time.Millisecond)

	// A different item (deleted and created again) replaces the entry.
	h.mu.Lock()
	h.setLocked(h.entries[k], itemVersion{id: "cfg_other", version: 1}, h.now())
	cur = h.entries[k].cur
	h.mu.Unlock()
	require.Equal(t, itemVersion{id: "cfg_other", version: 1}, cur)
	awaitWake(t, w, time.Second)

	// reload bypasses the cache and returns what the entry holds.
	f.set(k, 4)
	got, err := h.reload(ctx, []itemKey{k})
	require.NoError(t, err)
	require.Equal(t, []itemVersion{{id: "cfg_k", version: 4}}, got)
	f.err = errors.New("boom")
	_, err = h.reload(ctx, []itemKey{k})
	require.ErrorContains(t, err, "boom")
	h.unregister(w)
}
