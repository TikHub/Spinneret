package hotstate

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/jobs"
)

func TestSyncSiteMaterialization(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	other := f.addType(site, "web", "device")
	g := site.Groups[groupKey("web", "_default")]
	extra := f.addGroup(site, "web", "detail")
	extra.IdentityTypeIDs = []string{typ.ID}
	id1, k1 := f.addIdentity(site, typ, identitySpec{})
	_, k2 := f.addIdentity(site, other, identitySpec{state: "pending"})
	id3, k3 := f.addIdentity(site, typ, identitySpec{state: "banned"})
	pid, pk := f.addProxy(f.ns, proxySpec{})

	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	meta := f.hgetall(f.keys.SiteMeta(site.Key))
	require.Equal(t, f.ns.ID, meta["ns"])
	require.Equal(t, site.ID, meta["site"])
	require.Equal(t, "0", meta["paused"])
	require.NotContains(t, meta, "built")
	for _, tc := range []struct {
		group, ident int64
		want         bool
	}{
		{g.Key, k1, true}, {g.Key, k2, true}, {g.Key, k3, false},
		{extra.Key, k1, true}, {extra.Key, k2, false},
	} {
		_, ok := f.zscore(f.keys.Ready(site.Key, tc.group), tc.ident)
		require.Equal(t, tc.want, ok, "group %d identity %d", tc.group, tc.ident)
	}
	require.Equal(t, pid, f.hgetall(f.keys.ProxySite(site.Key, pk))["pid"])

	// Stale members: an unknown key without a hash, a deleted identity whose hash
	// lingers, an identity that became ineligible and a proxy that no longer exists.
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, g.Key)).ScoreMember().ScoreMember(0, "999999").ScoreMember(0, "junk").Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, g.Key)).ScoreMember().ScoreMember(0, key(k3)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, 888888)).FieldValue().
		FieldValue("st", "active").FieldValue("ty", "cookie").FieldValue("iid", "idt_gone").Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, g.Key)).ScoreMember().ScoreMember(0, "888888").Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.ProxyReady(site.Key)).ScoreMember().ScoreMember(0, "777777").Build())
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, 777777)).FieldValue().FieldValue("st", "active").Build())
	extra.IdentityTypeIDs = nil // identity 1 is no longer eligible for "detail"

	// Merge mode: a Redis-first ban written by apply.lua is not reverted by the PostgreSQL state.
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, k1)).FieldValue().FieldValue("st", "banned").FieldValue("bu", "-1").Build())
	f.do(f.rdb.B().Zrem().Key(f.keys.Ready(site.Key, g.Key)).Member(key(k1)).Build())

	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	members, err := f.rdb.Do(f.ctx, f.rdb.B().Zrange().Key(f.keys.Ready(site.Key, g.Key)).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{key(k2)}, members)
	require.False(t, f.exists(f.keys.Identity(site.Key, 888888)), "deleted identities are removed entirely")
	require.Equal(t, "banned", f.hgetall(f.keys.Identity(site.Key, k1))["st"])
	_, ok := f.zscore(f.keys.Ready(site.Key, extra.Key), k1)
	require.False(t, ok)
	proxies, err := f.rdb.Do(f.ctx, f.rdb.B().Zrange().Key(f.keys.ProxyReady(site.Key)).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{key(pk)}, proxies)
	require.False(t, f.exists(f.keys.ProxySite(site.Key, 777777)))

	// A PostgreSQL-first unban goes through SyncIdentities (authoritative).
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id1, id3}, SyncOptions{}))
	_, ok = f.zscore(f.keys.Ready(site.Key, g.Key), k1)
	require.True(t, ok)

	// Deleted endpoint groups lose their keys; paused sites are flagged.
	f.do(f.rdb.B().Hset().Key(f.keys.Breaker(site.Key, extra.Key)).FieldValue().FieldValue("st", "open").Build())
	f.do(f.rdb.B().Sadd().Key(f.keys.OpenBreakers(site.Key)).Member(key(extra.Key)).Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, extra.Key)).ScoreMember().ScoreMember(0, key(k1)).Build())
	f.removeGroup(site, extra)
	// The pause switch comes from PostgreSQL, not from the (possibly stale) catalog.
	f.exec(`UPDATE sites SET paused = true WHERE id = $1`, site.ID)
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	require.False(t, f.exists(f.keys.Ready(site.Key, extra.Key)))
	require.False(t, f.exists(f.keys.Breaker(site.Key, extra.Key)))
	require.Empty(t, f.smembers(f.keys.OpenBreakers(site.Key)))
	meta = f.hgetall(f.keys.SiteMeta(site.Key))
	require.Equal(t, "1", meta["paused"])
	require.Equal(t, key(g.Key), meta[metaGroupsField])
}

func TestSyncSiteCatalogReload(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	f.cat.Remove(f.ns.ID)
	err := f.syncer.SyncSite(f.ctx, site.ID)
	require.Error(t, err, "the in-memory catalog cannot reload the namespace")
	require.Contains(t, f.cat.Calls(), "reload:"+f.ns.ID)
	require.True(t, apperr.IsNotFound(f.syncer.SyncSite(f.ctx, "sit_missing")))
}

func TestRemoveSite(t *testing.T) {
	f := newFixture(t)
	s1 := f.addSite(f.ns, "one", "web")
	s2 := f.addSite(f.ns, "two", "web")
	for _, s := range []int64{s1.Key, s2.Key} {
		for i := 0; i < 2500; i++ {
			f.do(f.rdb.B().Set().Key(fmt.Sprintf("%sq:1:%d", f.keys.SiteBase(s), i)).Value("1").Build())
		}
		f.do(f.rdb.B().Hset().Key(f.keys.SiteMeta(s)).FieldValue().FieldValue("site", "x").Build())
	}
	require.NoError(t, f.syncer.RemoveSite(f.ctx, s1.Key))
	require.False(t, f.exists(f.keys.SiteMeta(s1.Key)))
	require.True(t, f.exists(f.keys.SiteMeta(s2.Key)))
	n, err := f.rdb.Do(f.ctx, f.rdb.B().Exists().Key(f.keys.SiteBase(s2.Key)+"q:1:2499").Build()).AsInt64()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	var left int
	require.NoError(t, f.syncer.scanKeys(f.ctx, escapeGlob(f.keys.SiteBase(s1.Key))+"*", func(keys []string) (bool, error) {
		left += len(keys)
		return false, nil
	}))
	require.Zero(t, left)
}

func TestRebuildAllRestoresSnapshots(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id1, k1 := f.addIdentity(site, typ, identitySpec{})
	id2, k2 := f.addIdentity(site, typ, identitySpec{})
	_, k3 := f.addIdentity(site, typ, identitySpec{state: "expired"})
	pid, pk := f.addProxy(f.ns, proxySpec{})
	cd := f.now.Add(15 * time.Minute)
	updated := f.now.Add(-time.Hour)
	lastFail := f.now.Add(-2 * time.Minute)
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, endpoint_group_id, score, samples,
	            consecutive_failures, last_failure_at, cooldown_until, reuse_until, last_used_at, updated_at)
	        VALUES ($1, 'ie', $2, $3, 55.5, 20, 3, $4, $5, NULL, $6, $7)`, site.ID, id1, g.ID, lastFail, cd, updated, updated)
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, score, samples, cooldown_until, updated_at)
	        VALUES ($1, 'ig', $2, 61.25, 40, $3, $4)`, site.ID, id2, cd, updated)
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, score, samples, consecutive_failures,
	            last_failure_at, cooldown_until, updated_at)
	        VALUES ($1, 'ps', $2, 12.5, 7, 2, $3, $4, $5)`, site.ID, pid, lastFail, cd, updated)
	// Values already in Redis win over snapshots.
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, pk)).FieldValue().FieldValue("sn", "99").Build())

	jitters := []int64{0, 30000, 60000}
	var mu sync.Mutex
	next := 0
	f.syncer.jitter = func() int64 {
		mu.Lock()
		defer mu.Unlock()
		v := jitters[next%len(jitters)]
		next++
		return v
	}
	// Keys of a site that no longer exists in PostgreSQL.
	f.do(f.rdb.B().Hset().Key(f.keys.SiteMeta(424242)).FieldValue().FieldValue("site", "gone").Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(424242, 1)).ScoreMember().ScoreMember(0, "1").Build())
	require.NoError(t, f.syncer.RebuildAll(f.ctx))

	epoch, err := f.rdb.Do(f.ctx, f.rdb.B().Get().Key(f.keys.Epoch()).Build()).ToString()
	require.NoError(t, err)
	require.Len(t, epoch, 32)
	require.Equal(t, epoch, f.hgetall(f.keys.SiteMeta(site.Key))["built"])
	require.False(t, f.exists(f.keys.SiteMeta(424242)), "orphan sites are removed")
	require.False(t, f.exists(f.keys.Ready(424242, 1)))

	require.Equal(t, fmt.Sprintf("55.50|%d|20|3|%d|%d|0|%d", updated.UnixMilli(), lastFail.UnixMilli(), cd.UnixMilli(), updated.UnixMilli()),
		f.hgetall(f.keys.Health(site.Key, g.Key))[key(k1)])
	h2 := f.hgetall(f.keys.Identity(site.Key, k2))
	require.Equal(t, "61.25", h2["gs"])
	require.Equal(t, ms(updated), h2["gts"])
	require.Equal(t, "40", h2["gn"])
	require.Equal(t, ms(cd), h2["scd"])
	require.Equal(t, id2, h2["iid"])
	px := f.hgetall(f.keys.ProxySite(site.Key, pk))
	require.Equal(t, "12.50", px["sc"])
	require.Equal(t, "99", px["sn"])
	require.Equal(t, "2", px["nf"])
	require.Equal(t, ms(cd), px["cd"])
	require.Equal(t, pid, px["pid"])

	s1, ok := f.zscore(f.keys.Ready(site.Key, g.Key), k1)
	require.True(t, ok)
	require.Equal(t, cd.UnixMilli(), s1, "restored cooldowns are kept")
	s2, ok := f.zscore(f.keys.Ready(site.Key, g.Key), k2)
	require.True(t, ok)
	require.Equal(t, cd.UnixMilli(), s2, "restored site cooldowns are kept")
	_, ok = f.zscore(f.keys.Ready(site.Key, g.Key), k3)
	require.False(t, ok)
	pscore, _ := f.zscore(f.keys.ProxyReady(site.Key), pk)
	require.Equal(t, cd.UnixMilli(), pscore)

	// Cold start: identities available now are spread over [now, now+60s].
	var fresh []int64
	for i := 0; i < 6; i++ {
		_, k := f.addIdentity(site, typ, identitySpec{})
		fresh = append(fresh, k)
	}
	require.NoError(t, f.syncer.RebuildAll(f.ctx))
	seen := map[int64]bool{}
	for _, k := range fresh {
		score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), k)
		require.True(t, ok)
		require.GreaterOrEqual(t, score, f.now.UnixMilli())
		require.LessOrEqual(t, score, f.now.Add(coldStartJitter).UnixMilli())
		seen[score-f.now.UnixMilli()] = true
	}
	require.Greater(t, len(seen), 1, "scores are jittered")
	newEpoch, err := f.rdb.Do(f.ctx, f.rdb.B().Get().Key(f.keys.Epoch()).Build()).ToString()
	require.NoError(t, err)
	require.NotEqual(t, epoch, newEpoch)
}

func TestEnsureBuiltConcurrency(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	for i := 0; i < 50; i++ {
		f.addIdentity(site, typ, identitySpec{})
	}

	const workers = 4
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := NewSyncer(f.pool, f.rdb, f.keys, f.cat, nil)
			rebuilt, err := s.EnsureBuilt(f.ctx)
			results <- rebuilt
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	count := 0
	for r := range results {
		if r {
			count++
		}
	}
	require.Equal(t, 1, count, "exactly one caller rebuilds")
	rebuilt, err := f.syncer.EnsureBuilt(f.ctx)
	require.NoError(t, err)
	require.False(t, rebuilt)

	t.Run("lock held elsewhere", func(t *testing.T) {
		conn, err := f.pool.Acquire(f.ctx)
		require.NoError(t, err)
		defer conn.Release()
		_, err = conn.Exec(f.ctx, "SELECT pg_advisory_lock($1)", jobs.LockKey(rebuildLockName))
		require.NoError(t, err)
		defer func() {
			_, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", jobs.LockKey(rebuildLockName))
			require.NoError(t, err)
		}()

		err = f.syncer.RebuildAll(f.ctx)
		require.Equal(t, apperr.ReasonRebuilding, apperr.ReasonOf(err))

		f.do(f.rdb.B().Del().Key(f.keys.Epoch()).Build())
		ctx, cancel := context.WithTimeout(f.ctx, 1200*time.Millisecond)
		defer cancel()
		rebuilt, err := f.syncer.EnsureBuilt(ctx)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, rebuilt)

		// Another instance finishing its rebuild unblocks waiters.
		waitErr := make(chan error, 1)
		go func() {
			_, err := f.syncer.EnsureBuilt(f.ctx)
			waitErr <- err
		}()
		time.Sleep(700 * time.Millisecond)
		f.do(f.rdb.B().Set().Key(f.keys.Epoch()).Value("other").Build())
		require.NoError(t, <-waitErr)
	})
}
