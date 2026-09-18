package catalog

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// countingMarks counts the marks read by loads (one Get per load).
type countingMarks struct {
	ChangeMarks
	gets atomic.Int64
}

func (c *countingMarks) Get(ctx context.Context, namespaceID string) (int64, error) {
	c.gets.Add(1)
	return c.ChangeMarks.Get(ctx, namespaceID)
}

// failingMarks is a change mark store whose backend is down.
type failingMarks struct{}

var errMarksDown = errors.New("marks backend down")

func (failingMarks) Bump(context.Context, string) error            { return errMarksDown }
func (failingMarks) Get(context.Context, string) (int64, error)    { return 0, errMarksDown }
func (failingMarks) All(context.Context) (map[string]int64, error) { return nil, errMarksDown }
func (failingMarks) Delete(context.Context, ...string) error       { return errMarksDown }

// Regression: two instances behind a load balancer. A write acknowledged by
// instance A must be visible to an admin request on instance B right away,
// even when B has not received (or not yet applied) the Pub/Sub invalidation.
// Before change marks, B answered "namespace not found" for ~100 ms after
// CreateNamespace on A.
func TestSyncGivesReadYourWritesAcrossInstances(t *testing.T) {
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	sd := newSeeder(t, pool)
	ctx := context.Background()

	marksB := &countingMarks{ChangeMarks: NewRedisChangeMarks(rdb, keys)}
	// No event bus: peers never receive invalidations, only marks connect them.
	a := NewStore(pool, nil, nil)
	a.SetChangeMarks(NewRedisChangeMarks(rdb, keys))
	b := NewStore(pool, nil, nil)
	b.SetChangeMarks(marksB)
	require.NoError(t, a.ReloadAll(ctx))
	require.NoError(t, b.ReloadAll(ctx))

	// A namespace created through instance A.
	tenantID := sd.tenant("acme")
	nsID := sd.namespace(tenantID, "prod")
	require.NoError(t, a.Invalidate(ctx, nsID))
	_, ok := a.NamespaceByName(tenantID, "prod")
	require.True(t, ok, "the writing instance sees its change")
	_, ok = b.NamespaceByName(tenantID, "prod")
	require.False(t, ok, "without sync the peer still has the old catalog")
	require.NoError(t, b.Sync(ctx))
	nsB, ok := b.NamespaceByName(tenantID, "prod")
	require.True(t, ok, "sync reloads namespaces changed on other instances")

	// Nothing changed: sync performs no load and keeps the snapshot.
	loads := marksB.gets.Load()
	require.NoError(t, b.Sync(ctx))
	require.Equal(t, loads, marksB.gets.Load())
	same, _ := b.Namespace(nsID)
	require.Same(t, nsB, same)

	// Changes inside the namespace (a new site and endpoint group).
	siteID, _ := sd.site(nsID, "shop", "web")
	sd.group(siteID, "web", site.DefaultGroup)
	require.NoError(t, a.Invalidate(ctx, nsID))
	require.NoError(t, b.Sync(ctx))
	nsB, _ = b.Namespace(nsID)
	require.Contains(t, nsB.Sites, "shop")

	// An invalidation without content change reloads once; the unchanged
	// fingerprint still records the mark, so the next sync is a no-op.
	require.NoError(t, a.Invalidate(ctx, nsID))
	loads = marksB.gets.Load()
	require.NoError(t, b.Sync(ctx))
	require.Equal(t, loads+1, marksB.gets.Load())
	require.NoError(t, b.Sync(ctx))
	require.Equal(t, loads+1, marksB.gets.Load())

	// A deleted namespace disappears from the peer as well.
	sd.deleteNamespace(nsID)
	require.NoError(t, a.Invalidate(ctx, nsID))
	require.NoError(t, b.Sync(ctx))
	_, ok = b.Namespace(nsID)
	require.False(t, ok)
	require.NoError(t, b.Sync(ctx))

	// A fresh instance catches up on marks of deleted namespaces once.
	c := NewStore(pool, nil, nil)
	marksC := &countingMarks{ChangeMarks: NewRedisChangeMarks(rdb, keys)}
	c.SetChangeMarks(marksC)
	require.NoError(t, c.Sync(ctx))
	loads = marksC.gets.Load()
	require.NoError(t, c.Sync(ctx))
	require.Equal(t, loads, marksC.gets.Load())

	// Marks of deleted namespaces are pruned after two full reloads.
	all, err := NewRedisChangeMarks(rdb, keys).All(ctx)
	require.NoError(t, err)
	require.Contains(t, all, nsID)
	require.NoError(t, b.ReloadAll(ctx))
	all, err = NewRedisChangeMarks(rdb, keys).All(ctx)
	require.NoError(t, err)
	require.Contains(t, all, nsID, "the first full reload only records the candidate")
	require.NoError(t, b.ReloadAll(ctx))
	all, err = NewRedisChangeMarks(rdb, keys).All(ctx)
	require.NoError(t, err)
	require.NotContains(t, all, nsID)
}

func TestSyncWithoutOrWithBrokenMarks(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	ctx := context.Background()
	nsID := sd.namespace(sd.tenant("acme"), "prod")

	plain := NewStore(pool, nil, nil)
	require.NoError(t, plain.Sync(ctx), "sync is a no-op without change marks")
	require.NoError(t, plain.Invalidate(ctx, nsID))

	broken := NewStore(pool, nil, nil)
	broken.SetChangeMarks(failingMarks{})
	require.NoError(t, broken.ReloadAll(ctx), "loads do not depend on the marks backend")
	_, ok := broken.Namespace(nsID)
	require.True(t, ok)
	require.NoError(t, broken.Invalidate(ctx, nsID), "a failed bump is logged, not returned")
	err := broken.Sync(ctx)
	require.ErrorIs(t, err, errMarksDown)
	_, ok = broken.Namespace(nsID)
	require.True(t, ok, "a failed sync keeps the snapshots")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	rdb, keys := testutil.Redis(t)
	marks := NewRedisChangeMarks(rdb, keys)
	require.NoError(t, marks.Bump(ctx, nsID))
	s := NewStore(pool, nil, nil)
	s.SetChangeMarks(marks)
	require.Error(t, s.Sync(canceled))
	require.NoError(t, marks.Delete(ctx))
}
