package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

type watchResult struct {
	items   []NodeItem
	err     error
	elapsed time.Duration
}

func watchAsync(svc *Service, p *authz.Principal, ns *catalog.Namespace, refs []WatchRef, timeout time.Duration) <-chan watchResult {
	out := make(chan watchResult, 1)
	go func() {
		start := time.Now()
		items, err := svc.WatchConfig(context.Background(), p, ns, refs, timeout)
		out <- watchResult{items: items, err: err, elapsed: time.Since(start)}
	}()
	return out
}

func waitWatchers(t *testing.T, svc *Service, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, w := svc.hub.stats()
		return w == n
	}, 30*time.Second, 2*time.Millisecond)
}

func TestWatchImmediateReturn(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.create(t, "crawler", "a.json", FormatJSON, `{"v":1}`, true)
	b := e.create(t, "crawler", "b.json", FormatJSON, `{"v":1}`, true)
	e.publishContent(t, b.ID, `{"v":2}`)
	node := e.token(t, e.ns, "config:read")

	items, err := e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{
		{Group: "crawler", Key: "a.json", Version: 1},
		{Group: "crawler", Key: "b.json", Version: 1},
		{Group: "crawler", Key: "missing.json", Version: 3},
	}, 5*time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "b.json", items[0].Key)
	require.Equal(t, int32(2), items[0].Version)
	require.JSONEq(t, `{"v":2}`, items[0].Content)

	// Held version 0 returns the published item; a held version newer than
	// the current one (item recreated) returns it as well.
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "crawler", Key: "a.json"}}, time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "crawler", Key: "a.json", Version: 9}}, time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int32(1), items[0].Version)
}

func TestWatchBlocksUntilPublish(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	it := e.create(t, "crawler", "a.json", FormatJSON, `{"v":1}`, true)
	other := e.create(t, "crawler", "other.json", FormatJSON, `{"v":1}`, true)
	node := e.token(t, e.ns, "config:read")

	res := watchAsync(e.svc, node, e.ns, []WatchRef{
		{Group: "crawler", Key: "a.json", Version: 1},
		{Group: "crawler", Key: "later.json"},
	}, 30*time.Second)
	unrelated := watchAsync(e.svc, node, e.ns, []WatchRef{{Group: "crawler", Key: "other.json", Version: 1}}, 2*time.Second)
	waitWatchers(t, e.svc, 2)
	require.InDelta(t, 2, gaugeValue(t, e.metrics.ConfigWatchers), 0)

	publishedAt := time.Now()
	e.publishContent(t, it.ID, `{"v":2}`)
	select {
	case r := <-res:
		require.NoError(t, r.err)
		require.Less(t, time.Since(publishedAt), time.Second, "watchers must wake within 1s")
		require.Len(t, r.items, 1)
		require.Equal(t, int32(2), r.items[0].Version)
		require.JSONEq(t, `{"v":2}`, r.items[0].Content)
	case <-time.After(5 * time.Second):
		t.Fatal("watcher was not woken by the publish")
	}

	// The watcher of an unrelated item keeps waiting until its timeout.
	r := <-unrelated
	require.NoError(t, r.err)
	require.Empty(t, r.items)
	require.GreaterOrEqual(t, r.elapsed, 1900*time.Millisecond)
	_ = other

	// An item that does not exist yet triggers when it is created and published.
	res = watchAsync(e.svc, node, e.ns, []WatchRef{{Group: "crawler", Key: "later.json"}}, 30*time.Second)
	waitWatchers(t, e.svc, 1)
	e.create(t, "crawler", "later.json", FormatJSON, `{"late":true}`, true)
	r = <-res
	require.NoError(t, r.err)
	require.Len(t, r.items, 1)
	require.Equal(t, "later.json", r.items[0].Key)
	waitWatchers(t, e.svc, 0)
	require.InDelta(t, 0, gaugeValue(t, e.metrics.ConfigWatchers), 0)
}

func TestWatchTimeoutDeleteAndCancel(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{DefaultWatchTimeout: 300 * time.Millisecond, MaxWatchTimeout: 500 * time.Millisecond})
	it := e.create(t, "crawler", "a.json", FormatJSON, `{}`, true)
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: "crawler", Key: "a.json", Version: 1}}

	start := time.Now()
	items, err := e.svc.WatchConfig(context.Background(), node, e.ns, refs, 0)
	require.NoError(t, err)
	require.NotNil(t, items)
	require.Empty(t, items)
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)

	start = time.Now()
	_, err = e.svc.WatchConfig(context.Background(), node, e.ns, refs, time.Hour)
	require.NoError(t, err)
	require.Less(t, time.Since(start), 2*time.Second, "timeout is capped by MaxWatchTimeout")

	// Deleting a watched item does not end the poll.
	res := watchAsync(e.svc, node, e.ns, refs, 400*time.Millisecond)
	waitWatchers(t, e.svc, 1)
	require.NoError(t, e.svc.DeleteItem(context.Background(), e.admin, it.ID))
	r := <-res
	require.NoError(t, r.err)
	require.Empty(t, r.items)

	// Context cancellation and deadlines.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.WatchConfig(ctx, node, e.ns, refs, 400*time.Millisecond)
		done <- err
	}()
	waitWatchers(t, e.svc, 1)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	dctx, dcancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer dcancel()
	start = time.Now()
	items, err = e.svc.WatchConfig(dctx, node, e.ns, refs, 400*time.Millisecond)
	require.NoError(t, err, "a deadline shorter than the timeout ends the poll gracefully")
	require.Empty(t, items)
	require.Less(t, time.Since(start), 300*time.Millisecond)
}

func TestWatchMaxWatchers(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{MaxWatchers: 1})
	e.create(t, "g", "k", FormatText, "x", true)
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: "g", Key: "k", Version: 1}}

	first := watchAsync(e.svc, node, e.ns, refs, time.Second)
	waitWatchers(t, e.svc, 1)
	_, err := e.svc.WatchConfig(context.Background(), node, e.ns, refs, time.Second)
	requireReason(t, err, apperr.ReasonRateLimited)
	ae, ok := apperr.As(err)
	require.True(t, ok)
	require.Positive(t, ae.RetryAfterMs)

	// Immediate answers do not need a watcher slot.
	items, err := e.svc.WatchConfig(context.Background(), node, e.ns, []WatchRef{{Group: "g", Key: "k"}}, time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, (<-first).err)
}

func TestWatchRuntimeItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers, Version: 1}}
	res := watchAsync(e.svc, node, e.ns, refs, 30*time.Second)
	waitWatchers(t, e.svc, 1)

	v := e.runtime.bump(e.ns.ID, RuntimeBreakers)
	data, err := json.Marshal(RuntimeEventData{NS: e.ns.ID, Kind: RuntimeBreakers, Version: v})
	require.NoError(t, err)
	require.NoError(t, e.bus.Publish(context.Background(), events.ChannelRuntime, events.Event{Type: "runtime", Data: data}))
	r := <-res
	require.NoError(t, r.err)
	require.Len(t, r.items, 1)
	require.Equal(t, int32(2), r.items[0].Version)
	require.JSONEq(t, `{"kind":"breakers","version":1}`, r.items[0].Content)

	// The cached runtime content is reused for the same version.
	e.runtime.mu.Lock()
	before := e.runtime.contents
	e.runtime.mu.Unlock()
	items, err := e.svc.WatchConfig(context.Background(), node, e.ns, refs, time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	e.runtime.mu.Lock()
	require.Equal(t, before, e.runtime.contents)
	e.runtime.mu.Unlock()

	// Malformed runtime events are ignored.
	require.NoError(t, e.bus.Publish(context.Background(), events.ChannelRuntime, events.Event{Type: "runtime", Data: json.RawMessage(`{"ns":"x","kind":"nope"}`)}))
	require.NoError(t, e.bus.Publish(context.Background(), events.ChannelConfig, events.Event{Type: "x", Data: json.RawMessage(`{"ns":""}`)}))

	// Provider failures fail the watch.
	e.runtime.setErr(errors.New("redis down"))
	other := []WatchRef{{Group: RuntimeGroup, Key: RuntimeSiteSwitches, Version: 1}}
	_, err = e.svc.WatchConfig(context.Background(), node, e.ns, other, time.Second)
	require.ErrorContains(t, err, "redis down")
}

func TestWatchAcrossInstances(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	it := e.create(t, "g", "k", FormatText, "v1", true)
	node := e.token(t, e.ns, "config:read")

	// A peer instance sharing the database and the bus.
	peer := New(Config{}, e.pool, e.cat, e.bus, e.secrets, e.runtime, e.audit, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- peer.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	res := watchAsync(peer, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 1}}, 30*time.Second)
	waitWatchers(t, peer, 1)
	e.publishContent(t, it.ID, "v2")
	r := <-res
	require.NoError(t, r.err)
	require.Len(t, r.items, 1)
	require.Equal(t, "v2", r.items[0].Content)
}

func TestWatchResyncWithoutEvents(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: 100 * time.Millisecond})
	it := e.create(t, "g", "k", FormatText, "v1", true)
	node := e.token(t, e.ns, "config:read")
	res := watchAsync(e.svc, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 1}}, 30*time.Second)
	waitWatchers(t, e.svc, 1)

	// Publish behind the service's back (as if the bus event was lost).
	e.exec(t, `INSERT INTO config_versions (item_id, version, content) VALUES ($1, 2, 'v2')`, it.ID)
	e.exec(t, `UPDATE config_items SET current_version = 2 WHERE id = $1`, it.ID)
	select {
	case r := <-res:
		require.NoError(t, r.err)
		require.Len(t, r.items, 1)
		require.Equal(t, "v2", r.items[0].Content)
	case <-time.After(5 * time.Second):
		t.Fatal("resync did not wake the watcher")
	}
}

func TestWatchValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	node := e.token(t, e.ns, "config:read:crawler")
	tests := []struct {
		name   string
		p      *authz.Principal
		refs   []WatchRef
		reason apperr.Reason
	}{
		{name: "empty", p: node, refs: nil, reason: apperr.ReasonInvalidArgument},
		{name: "too many", p: node, refs: make([]WatchRef, MaxNodeItems+1), reason: apperr.ReasonInvalidArgument},
		{name: "duplicate", p: node, refs: []WatchRef{{Group: "crawler", Key: "a"}, {Group: "crawler", Key: "a", Version: 2}}, reason: apperr.ReasonInvalidArgument},
		{name: "negative", p: node, refs: []WatchRef{{Group: "crawler", Key: "a", Version: -1}}, reason: apperr.ReasonInvalidArgument},
		{name: "group denied", p: node, refs: []WatchRef{{Group: "crawler", Key: "a"}, {Group: "ci", Key: "a"}}, reason: apperr.ReasonScopeMissing},
		{name: "runtime denied", p: node, refs: []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers}}, reason: apperr.ReasonScopeMissing},
		{name: "nil principal", p: nil, refs: []WatchRef{{Group: "crawler", Key: "a"}}, reason: apperr.ReasonSessionInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.WatchConfig(ctx, tc.p, e.ns, tc.refs, time.Second)
			requireReason(t, err, tc.reason)
		})
	}
}

// TestWatchManyWatchers is a memory and goroutine sanity check for 10k
// concurrent watchers on one instance.
func TestWatchManyWatchers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 10k watcher test in -short mode")
	}
	e := newEnv(t, Config{MaxWatchers: 20_000})
	const (
		watchers = 10_000
		keys     = 10
	)
	items := make([]Item, keys)
	for i := range keys {
		items[i] = e.create(t, "fleet", "item-"+string(rune('a'+i)), FormatText, "v1", true)
	}
	node := e.token(t, e.ns, "config:read")

	// Warm the version cache so that watchers do not all hit the database.
	_, err := e.svc.WatchConfig(context.Background(), node, e.ns, []WatchRef{{Group: "fleet", Key: "item-a"}}, time.Millisecond)
	require.NoError(t, err)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	baseGoroutines := runtime.NumGoroutine()

	results := make(chan watchResult, watchers)
	var wg sync.WaitGroup
	for i := range watchers {
		key := "item-" + string(rune('a'+i%keys))
		wg.Go(func() {
			start := time.Now()
			got, err := e.svc.WatchConfig(context.Background(), node, e.ns, []WatchRef{{Group: "fleet", Key: key, Version: 1}}, 20*time.Second)
			results <- watchResult{items: got, err: err, elapsed: time.Since(start)}
		})
	}
	waitWatchers(t, e.svc, watchers)
	require.InDelta(t, watchers, gaugeValue(t, e.metrics.ConfigWatchers), 0)

	runtime.GC()
	var during runtime.MemStats
	runtime.ReadMemStats(&during)
	heapPerWatcher := (int64(during.HeapAlloc) - int64(before.HeapAlloc)) / watchers
	t.Logf("heap per watcher: %d bytes, goroutines: %d", heapPerWatcher, runtime.NumGoroutine()-baseGoroutines)
	require.Less(t, heapPerWatcher, int64(16<<10), "each watcher must stay small")

	// Publishing one item wakes exactly its watchers, quickly.
	publishedAt := time.Now()
	e.publishContent(t, items[0].ID, "v2")
	woken := 0
	deadline := time.After(10 * time.Second)
	for woken < watchers/keys {
		select {
		case r := <-results:
			require.NoError(t, r.err)
			require.Len(t, r.items, 1)
			require.Equal(t, "item-a", r.items[0].Key)
			woken++
		case <-deadline:
			t.Fatalf("only %d of %d watchers woken", woken, watchers/keys)
		}
	}
	t.Logf("woke %d watchers in %s", woken, time.Since(publishedAt))
	waitWatchers(t, e.svc, watchers-watchers/keys)

	// Publishing the remaining items releases every watcher.
	for _, it := range items[1:] {
		e.publishContent(t, it.ID, "v2")
	}
	wg.Wait()
	close(results)
	for r := range results {
		require.NoError(t, r.err)
		require.Len(t, r.items, 1)
	}
	waitWatchers(t, e.svc, 0)
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseGoroutines+10
	}, 10*time.Second, 50*time.Millisecond, "watcher goroutines must exit")
}

func TestWatchRecoversFromStaleCache(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: time.Hour})
	it := e.create(t, "g", "k", FormatText, "old", true)
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: "g", Key: "k", Version: 1}}
	items, err := e.svc.WatchConfig(context.Background(), node, e.ns, refs, 50*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)

	// Replace the item behind the service's back: same name and version, new ID.
	newID := "cfg_0192a3f4c1d27b8e9a01f2c3d4e5a6b7"
	e.exec(t, `DELETE FROM config_items WHERE id = $1`, it.ID)
	e.exec(t, `INSERT INTO config_items (id, namespace_id, group_name, key, format, current_version) VALUES ($1, $2, 'g', 'k', 'text', 1)`, newID, e.ns.ID)
	e.exec(t, `INSERT INTO config_versions (item_id, version, content) VALUES ($1, 1, 'new')`, newID)

	start := time.Now()
	items, err = e.svc.WatchConfig(context.Background(), node, e.ns, []WatchRef{{Group: "g", Key: "k"}}, 10*time.Second)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "new", items[0].Content)
	require.Less(t, time.Since(start), 5*time.Second)
}
