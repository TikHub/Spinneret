package hotstate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// redisFirst simulates a Redis-first lifecycle change written by apply.lua
// (fields plus the change time "sct").
func (f *fixture) redisFirst(hashKey string, at time.Time, fields ...string) {
	f.t.Helper()
	cmd := f.rdb.B().Hset().Key(hashKey).FieldValue().FieldValue("sct", ms(at))
	for i := 0; i+1 < len(fields); i += 2 {
		cmd = cmd.FieldValue(fields[i], fields[i+1])
	}
	f.do(cmd.Build())
}

func TestSyncIdentitiesKeepsNewerRedisFirstLifecycle(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	changed := f.now.Add(-time.Hour)

	t.Run("automatic ban not yet persisted survives an attribute sync", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{})
		f.exec(`UPDATE identities SET state_changed_at = $1 WHERE id = $2`, changed, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))

		// apply.lua bans permanently; the StateWriter has not written PostgreSQL yet.
		f.redisFirst(f.keys.Identity(site.Key, hk), f.now.Add(-time.Second), "st", "banned", "bu", "-1", "qu", "0")
		f.do(f.rdb.B().Zrem().Key(f.keys.Ready(site.Key, g.Key)).Member(key(hk)).Build())

		// An operator edits the tags (PostgreSQL row unchanged in its lifecycle).
		f.exec(`UPDATE identities SET tags = '{vip}', region = 'hk' WHERE id = $1`, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		h := f.hgetall(f.keys.Identity(site.Key, hk))
		require.Equal(t, "banned", h["st"])
		require.Equal(t, "-1", h["bu"])
		require.Equal(t, "hk", h["rg"], "non-lifecycle fields still come from PostgreSQL")
		_, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
		require.False(t, ok, "the banned identity must not re-enter the ready queue")
		require.Equal(t, ms(f.now.Add(-time.Second)), h["sct"])

		// A later PostgreSQL-first transition (operator unban) wins.
		f.exec(`UPDATE identities SET state = 'pending', state_changed_at = $1 WHERE id = $2`, f.now, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		h = f.hgetall(f.keys.Identity(site.Key, hk))
		require.Equal(t, "pending", h["st"])
		require.Equal(t, "0", h["bu"])
		require.Equal(t, ms(f.now), h["sct"])
		_, ok = f.zscore(f.keys.Ready(site.Key, g.Key), hk)
		require.True(t, ok)
	})

	t.Run("older PostgreSQL-first change never wins", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{})
		f.exec(`UPDATE identities SET state_changed_at = $1 WHERE id = $2`, changed, id)
		until := f.now.Add(time.Hour)
		f.redisFirst(f.keys.Identity(site.Key, hk), f.now, "st", "quarantined", "qu", ms(until), "bu", "0")
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		h := f.hgetall(f.keys.Identity(site.Key, hk))
		require.Equal(t, "quarantined", h["st"])
		require.Equal(t, ms(until), h["qu"])
	})

	t.Run("an ended Redis-first ban does not outlive the PostgreSQL state", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{})
		f.exec(`UPDATE identities SET state_changed_at = $1 WHERE id = $2`, changed, id)
		f.redisFirst(f.keys.Identity(site.Key, hk), f.now.Add(-time.Minute),
			"st", "banned", "bu", ms(f.now.Add(-time.Second)), "qu", "0")
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		h := f.hgetall(f.keys.Identity(site.Key, hk))
		require.Equal(t, "active", h["st"])
		require.Equal(t, "0", h["bu"])
		_, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
		require.True(t, ok)
	})
}

func TestSyncProxiesKeepsRedisFirstState(t *testing.T) {
	f := newFixture(t)
	s1 := f.addSite(f.ns, "one", "web")
	s2 := f.addSite(f.ns, "two", "web")
	changed := f.now.Add(-time.Hour)

	t.Run("automatic proxy-global cooldown survives", func(t *testing.T) {
		pid, pk := f.addProxy(f.ns, proxySpec{})
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		gcd := f.now.Add(30 * time.Minute).UnixMilli()
		for _, s := range []int64{s1.Key, s2.Key} {
			// apply.lua {op: cd, sc: pg}: Redis only, never persisted.
			f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(s, pk)).FieldValue().FieldValue("gcd", key(gcd)).Build())
			f.do(f.rdb.B().Zadd().Key(f.keys.ProxyReady(s)).Xx().Gt().ScoreMember().ScoreMember(float64(gcd), key(pk)).Build())
		}
		f.exec(`UPDATE proxies SET tags = '{fast}' WHERE id = $1`, pid)
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		for _, s := range []int64{s1.Key, s2.Key} {
			require.Equal(t, key(gcd), f.hgetall(f.keys.ProxySite(s, pk))["gcd"])
			score, ok := f.zscore(f.keys.ProxyReady(s), pk)
			require.True(t, ok)
			require.Equal(t, gcd, score, "the proxy stays cooling down on every site")
		}

		// A longer PostgreSQL cooldown still extends it.
		longer := f.now.Add(time.Hour)
		f.exec(`UPDATE proxies SET cooldown_until = $1 WHERE id = $2`, longer, pid)
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		require.Equal(t, ms(longer), f.hgetall(f.keys.ProxySite(s1.Key, pk))["gcd"])
	})

	t.Run("automatic proxy ban not yet persisted survives", func(t *testing.T) {
		pid, pk := f.addProxy(f.ns, proxySpec{})
		f.exec(`UPDATE proxies SET state_changed_at = $1 WHERE id = $2`, changed, pid)
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		until := f.now.Add(time.Hour)
		for _, s := range []int64{s1.Key, s2.Key} {
			f.redisFirst(f.keys.ProxySite(s, pk), f.now, "st", "banned", "bu", ms(until))
			f.do(f.rdb.B().Zrem().Key(f.keys.ProxyReady(s)).Member(key(pk)).Build())
		}
		f.exec(`UPDATE proxies SET region = 'us' WHERE id = $1`, pid)
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		for _, s := range []int64{s1.Key, s2.Key} {
			h := f.hgetall(f.keys.ProxySite(s, pk))
			require.Equal(t, "banned", h["st"])
			require.Equal(t, ms(until), h["bu"])
			require.Equal(t, "us", h["rg"])
			_, ok := f.zscore(f.keys.ProxyReady(s), pk)
			require.False(t, ok)
		}

		// A newer PostgreSQL-first transition (manual unban) wins.
		f.exec(`UPDATE proxies SET state = 'active', ban_until = NULL, state_changed_at = $1 WHERE id = $2`,
			f.now.Add(time.Second), pid)
		require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
		h := f.hgetall(f.keys.ProxySite(s1.Key, pk))
		require.Equal(t, "active", h["st"])
		require.Equal(t, "0", h["bu"])
		_, ok := f.zscore(f.keys.ProxyReady(s1.Key), pk)
		require.True(t, ok)
	})
}

func TestSyncAccountsKeepsNewerRedisFirstBan(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	accID, accKey := f.addAccount(site, "user-a", "active", nil)
	f.addIdentity(site, typ, identitySpec{accountID: accID})
	created := f.now.Add(-time.Hour)
	f.exec(`UPDATE accounts SET created_at = $1, updated_at = $1 WHERE id = $2`, created, accID)
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))

	// apply.lua bans the account; the StateWriter has not persisted it yet.
	f.redisFirst(f.keys.Account(site.Key, accKey), f.now.Add(-time.Second), "st", "banned", "bu", "-1")
	// An unrelated account edit bumps updated_at and re-synchronizes membership.
	f.exec(`UPDATE accounts SET notes = 'vip', updated_at = $1 WHERE id = $2`, f.now, accID)
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))
	h := f.hgetall(f.keys.Account(site.Key, accKey))
	require.Equal(t, "banned", h["st"])
	require.Equal(t, "-1", h["bu"])
	require.Equal(t, ms(f.now.Add(-time.Second)), h["sct"])

	// A later PostgreSQL-first transition records a state event and wins.
	f.exec(`INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
	                                  from_state, to_state, action, actor)
	        VALUES ($1, $2, $3, $4, $5, 'account', $6, 'banned', 'active', 'unban', 'user:u1')`,
		idgen.New(idgen.StateEvent), f.now, f.ns.TenantID, f.ns.ID, site.ID, accID)
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))
	h = f.hgetall(f.keys.Account(site.Key, accKey))
	require.Equal(t, "active", h["st"])
	require.Equal(t, "0", h["bu"])
	require.Equal(t, ms(f.now), h["sct"])

	// Cooldown and shadow events are not lifecycle changes.
	f.redisFirst(f.keys.Account(site.Key, accKey), f.now.Add(time.Second), "st", "banned", "bu", "-1")
	for _, ev := range []struct{ action, shadow string }{{"cooldown", "false"}, {"ban", "true"}} {
		f.exec(`INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id,
		                                  action, actor, shadow)
		        VALUES ($1, $2, $3, $4, $5, 'account', $6, $7, 'system', $8)`,
			idgen.New(idgen.StateEvent), f.now.Add(2*time.Second), f.ns.TenantID, f.ns.ID, site.ID, accID, ev.action, ev.shadow == "true")
	}
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))
	require.Equal(t, "banned", f.hgetall(f.keys.Account(site.Key, accKey))["st"])
}

// A rebuild (authoritative for identities and accounts) keeps Redis-first
// lifecycle changes that PostgreSQL has not caught up with.
func TestRebuildKeepsNewerRedisFirstLifecycle(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	accID, accKey := f.addAccount(site, "user-a", "active", nil)
	id, hk := f.addIdentity(site, typ, identitySpec{accountID: accID})
	old := f.now.Add(-time.Hour)
	f.exec(`UPDATE accounts SET created_at = $1 WHERE id = $2`, old, accID)
	f.exec(`UPDATE identities SET state_changed_at = $1 WHERE id = $2`, old, id)
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))

	f.redisFirst(f.keys.Account(site.Key, accKey), f.now, "st", "banned", "bu", "-1")
	f.redisFirst(f.keys.Identity(site.Key, hk), f.now, "st", "banned", "bu", "-1", "qu", "0")
	f.do(f.rdb.B().Zrem().Key(f.keys.Ready(site.Key, g.Key)).Member(key(hk)).Build())
	require.NoError(t, f.syncer.RebuildAll(f.ctx))
	require.Equal(t, "banned", f.hgetall(f.keys.Account(site.Key, accKey))["st"])
	require.Equal(t, "banned", f.hgetall(f.keys.Identity(site.Key, hk))["st"])
	_, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
	require.False(t, ok)
}
