package hotstate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestIdentityBindingIsRedisFirst(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	id, hk := f.addIdentity(site, typ, identitySpec{})
	pA, kA := f.addProxy(f.ns, proxySpec{})
	pB, kB := f.addProxy(f.ns, proxySpec{})
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pA, pB}))
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	idKey := f.keys.Identity(site.Key, hk)
	require.Equal(t, "", f.hgetall(idKey)["px"])

	// acquire.lua binds the identity to proxy B (bind_identity mode); the
	// binding is not (yet) persisted in PostgreSQL.
	f.do(f.rdb.B().Hset().Key(idKey).FieldValue().
		FieldValue("px", key(kB)).FieldValue("rbd", "20260917").FieldValue("rbn", "1").Build())

	tests := []struct {
		name string
		run  func() error
	}{
		{"authoritative identity sync", func() error {
			return f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetHealth: true})
		}},
		{"stale PostgreSQL binding", func() error {
			f.bind(id, pA)
			return f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{})
		}},
		{"site materialization", func() error { return f.syncer.SyncSite(f.ctx, site.ID) }},
		{"rebuild with intact Redis", func() error { return f.syncer.RebuildAll(f.ctx) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.run())
			h := f.hgetall(idKey)
			require.Equal(t, key(kB), h["px"], "the Redis binding wins")
			require.Equal(t, "1", h["rbn"])
		})
	}

	t.Run("binding to a proxy that is not materialized falls back to PostgreSQL", func(t *testing.T) {
		f.do(f.rdb.B().Hset().Key(idKey).FieldValue().FieldValue("px", "987654321").Build())
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		require.Equal(t, key(kA), f.hgetall(idKey)["px"])
	})

	t.Run("an empty Redis binding takes the PostgreSQL binding", func(t *testing.T) {
		f.do(f.rdb.B().Hset().Key(idKey).FieldValue().FieldValue("px", "").Build())
		require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
		require.Equal(t, key(kA), f.hgetall(idKey)["px"])
	})

	t.Run("after Redis data loss the PostgreSQL binding is restored", func(t *testing.T) {
		f.do(f.rdb.B().Del().Key(idKey, f.keys.ProxySite(site.Key, kB)).Build())
		require.NoError(t, f.syncer.RebuildAll(f.ctx))
		require.Equal(t, key(kA), f.hgetall(idKey)["px"])
	})
}

func TestSyncProxiesLifecycleEnds(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	until := f.now.Add(3 * time.Hour)
	stale := f.now.Add(-time.Hour)

	tests := []struct {
		name     string
		state    string
		banUntil *time.Time
		wantBU   string
		wantQU   string
		wantRdy  bool
	}{
		{name: "permanent ban", state: "banned", wantBU: "-1", wantQU: "0"},
		{name: "temporary ban", state: "banned", banUntil: &until, wantBU: ms(until), wantQU: "0"},
		{name: "quarantine", state: "quarantined", banUntil: &until, wantBU: "0", wantQU: ms(until)},
		{name: "quarantine without end", state: "quarantined", wantBU: "0", wantQU: "0"},
		{name: "active with a stale end", state: "active", banUntil: &stale, wantBU: "0", wantQU: "0", wantRdy: true},
		{name: "dead", state: "dead", banUntil: &until, wantBU: "0", wantQU: "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pid, pk := f.addProxy(f.ns, proxySpec{state: tc.state})
			f.exec(`UPDATE proxies SET ban_until = $1 WHERE id = $2`, tc.banUntil, pid)
			require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
			h := f.hgetall(f.keys.ProxySite(site.Key, pk))
			require.Equal(t, tc.state, h["st"])
			require.Equal(t, tc.wantBU, h["bu"])
			require.Equal(t, tc.wantQU, h["qu"])
			_, ok := f.zscore(f.keys.ProxyReady(site.Key), pk)
			require.Equal(t, tc.wantRdy, ok)
		})
	}

	t.Run("merge keeps Redis-first lifecycle ends", func(t *testing.T) {
		pid, pk := f.addProxy(f.ns, proxySpec{})
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		pxKey := f.keys.ProxySite(site.Key, pk)
		// apply.lua quarantined the proxy; the state writer has not persisted it yet.
		f.do(f.rdb.B().Hset().Key(pxKey).FieldValue().FieldValue("st", "quarantined").FieldValue("qu", ms(until)).Build())
		f.do(f.rdb.B().Zrem().Key(f.keys.ProxyReady(site.Key)).Member(key(pk)).Build())
		require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
		h := f.hgetall(pxKey)
		require.Equal(t, "quarantined", h["st"])
		require.Equal(t, ms(until), h["qu"])
		_, ok := f.zscore(f.keys.ProxyReady(site.Key), pk)
		require.False(t, ok)

		// A PostgreSQL-first operation is authoritative.
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		h = f.hgetall(pxKey)
		require.Equal(t, "active", h["st"])
		require.Equal(t, "0", h["qu"])
		_, ok = f.zscore(f.keys.ProxyReady(site.Key), pk)
		require.True(t, ok)
	})
}

func TestSyncSiteUsesPostgresTruth(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")

	// The catalog still says running while the switch was turned on elsewhere.
	f.exec(`UPDATE sites SET paused = true WHERE id = $1`, site.ID)
	require.False(t, site.Paused)
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	require.Equal(t, "1", f.hgetall(f.keys.SiteMeta(site.Key))["paused"])

	// A site deleted after the catalog snapshot was taken is not materialized again.
	gone := f.addSite(f.ns, "gone", "web")
	f.exec(`DELETE FROM endpoint_groups WHERE site_id = $1`, gone.ID)
	f.exec(`DELETE FROM sites WHERE id = $1`, gone.ID)
	err := f.syncer.SyncSite(f.ctx, gone.ID)
	require.True(t, apperr.IsNotFound(err), "got %v", err)
	require.False(t, f.exists(f.keys.SiteMeta(gone.Key)))
}

func TestHotStateNamespaceIsolation(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	id, hk := f.addIdentity(site, typ, identitySpec{})
	other := f.addNamespace("other")
	foreignID, foreignKey := f.addProxy(other, proxySpec{})
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))

	// Corrupt hot state: a binding and a dirty entry referencing a proxy of
	// another namespace, whose hash has no "pid".
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, hk)).FieldValue().FieldValue("px", key(foreignKey)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, foreignKey)).FieldValue().FieldValue("sc", "10.00").FieldValue("sn", "3").Build())
	hot, err := f.syncer.IdentityHotState(f.ctx, site, id)
	require.NoError(t, err)
	require.Empty(t, hot.BoundProxyID, "the proxy ID of another namespace is never revealed")

	f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member("p" + key(foreignKey)).Build())
	require.NoError(t, f.syncer.SnapshotJob().Run(f.ctx))
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM hot_state_snapshots WHERE subject_id = $1`, foreignID).Scan(&n))
	require.Zero(t, n)
}
