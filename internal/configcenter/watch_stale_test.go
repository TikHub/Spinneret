package configcenter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWatchClientAheadOfStaleCache covers a node that already holds a newer
// version than this instance's cached one (for example it read the item from
// a peer instance before this instance received the bus event, or the event
// was lost). The watch must not deliver the older cached version.
func TestWatchClientAheadOfStaleCache(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: time.Hour})
	ctx := context.Background()
	it := e.create(t, "g", "k", FormatText, "v1", true)
	node := e.token(t, e.ns, "config:read")

	// Warm the version cache with v1 (no watcher remains registered).
	items, err := e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 1}}, 20*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)

	// v2 is published elsewhere; this instance never hears about it.
	e.exec(t, `INSERT INTO config_versions (item_id, version, content) VALUES ($1, 2, 'v2')`, it.ID)
	e.exec(t, `UPDATE config_items SET current_version = 2 WHERE id = $1`, it.ID)

	// The node already got v2 from the peer: nothing changed for it.
	start := time.Now()
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 2}}, 300*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items, "an older cached version must never be delivered to a node holding a newer one")
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)

	// A node holding v1 receives v2 at once (the cache was corrected).
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 1}}, 300*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int32(2), items[0].Version)
	require.Equal(t, "v2", items[0].Content)

	// A node holding a version newer than the item's (item recreated) gets
	// the current version immediately.
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "g", Key: "k", Version: 7}}, 300*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int32(2), items[0].Version)
}

// TestWatchRuntimeClientAheadOfStaleCache is the runtime counterpart: without
// verification the watch returned the version the node already holds at once,
// making nodes spin in a hot loop until the cache entry expired.
func TestWatchRuntimeClientAheadOfStaleCache(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: time.Hour})
	ctx := context.Background()
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers, Version: 1}}

	items, err := e.svc.WatchConfig(ctx, node, e.ns, refs, 20*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)

	e.runtime.bump(e.ns.ID, RuntimeBreakers) // no bus event
	got, err := e.svc.GetConfig(ctx, node, e.ns, Ref{Group: RuntimeGroup, Key: RuntimeBreakers})
	require.NoError(t, err)
	require.Equal(t, int32(2), got.Version)

	start := time.Now()
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers, Version: 2}}, 300*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items, "the version the node holds must not be returned as a change")
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)
}

// TestWatchRuntimeResolvesToHeldVersion covers a provider whose version went
// back (for example a Redis reset) while the cache still holds a higher one:
// content that resolves to the held version is not a change.
func TestWatchRuntimeResolvesToHeldVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: time.Hour})
	ctx := context.Background()
	node := e.token(t, e.ns, "config:read")
	e.runtime.set(e.ns.ID, RuntimeBreakers, 2) // API version 3

	items, err := e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers, Version: 3}}, 20*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)

	e.runtime.set(e.ns.ID, RuntimeBreakers, 1) // API version 2, no bus event
	start := time.Now()
	items, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: RuntimeGroup, Key: RuntimeBreakers, Version: 2}}, 300*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)
	require.GreaterOrEqual(t, time.Since(start), 250*time.Millisecond)
}

// TestWatchVerificationFailure checks that a failing source of truth during
// verification fails the watch instead of delivering a stale version.
func TestWatchVerificationFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{ResyncInterval: time.Hour})
	ctx := context.Background()
	node := e.token(t, e.ns, "config:read")
	refs := []WatchRef{{Group: RuntimeGroup, Key: RuntimeSiteSwitches, Version: 1}}
	items, err := e.svc.WatchConfig(ctx, node, e.ns, refs, 20*time.Millisecond)
	require.NoError(t, err)
	require.Empty(t, items)

	e.runtime.setErr(context.DeadlineExceeded)
	_, err = e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: RuntimeGroup, Key: RuntimeSiteSwitches, Version: 5}}, 300*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
