package hotstate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

func TestAccountMembershipMove(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	accA, keyA := f.addAccount(site, "user-a", "active", nil)
	accB, keyB := f.addAccount(site, "user-b", "banned", nil)
	id, hk := f.addIdentity(site, typ, identitySpec{accountID: accA})

	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	require.Equal(t, key(keyA), f.hgetall(f.keys.Identity(site.Key, hk))["acc"])
	require.ElementsMatch(t, []string{key(hk)}, f.smembers(f.keys.AccountMembers(site.Key, keyA)))
	require.Equal(t, map[string]string{"st": "active", "bu": "0", "cd": "0"}, withoutSct(f.hgetall(f.keys.Account(site.Key, keyA))))

	f.exec(`UPDATE identities SET account_id = $1 WHERE id = $2`, accB, id)
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	require.Equal(t, key(keyB), f.hgetall(f.keys.Identity(site.Key, hk))["acc"])
	require.Empty(t, f.smembers(f.keys.AccountMembers(site.Key, keyA)))
	require.ElementsMatch(t, []string{key(hk)}, f.smembers(f.keys.AccountMembers(site.Key, keyB)))
	require.Equal(t, map[string]string{"st": "banned", "bu": "-1", "cd": "0"}, withoutSct(f.hgetall(f.keys.Account(site.Key, keyB))))

	f.exec(`UPDATE identities SET account_id = NULL WHERE id = $1`, id)
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	require.Equal(t, "", f.hgetall(f.keys.Identity(site.Key, hk))["acc"])
	require.Empty(t, f.smembers(f.keys.AccountMembers(site.Key, keyB)))

	t.Run("sync accounts", func(t *testing.T) {
		f.exec(`UPDATE identities SET account_id = $1 WHERE id = $2`, accA, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		// A stale member is dropped when the membership is rebuilt from PostgreSQL.
		f.do(f.rdb.B().Sadd().Key(f.keys.AccountMembers(site.Key, keyA)).Member("999999").Build())

		cd := f.now.Add(30 * time.Minute)
		f.exec(`UPDATE accounts SET state = 'banned', ban_until = $1, cooldown_until = $2 WHERE id = $3`,
			f.now.Add(time.Hour), cd, accA)
		require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accA, accA, idgen.New(idgen.Account)}))
		require.Equal(t, map[string]string{"st": "banned", "bu": ms(f.now.Add(time.Hour)), "cd": ms(cd)},
			withoutSct(f.hgetall(f.keys.Account(site.Key, keyA))))
		require.ElementsMatch(t, []string{key(hk)}, f.smembers(f.keys.AccountMembers(site.Key, keyA)))
		score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
		require.True(t, ok)
		require.Equal(t, cd.UnixMilli(), score, "members are pushed to the account cooldown")

		// A longer cooldown already in Redis (automatic action) is never shortened.
		longer := f.now.Add(2 * time.Hour).UnixMilli()
		f.do(f.rdb.B().Hset().Key(f.keys.Account(site.Key, keyA)).FieldValue().FieldValue("cd", key(longer)).Build())
		f.exec(`UPDATE accounts SET state = 'active', ban_until = NULL, cooldown_until = NULL WHERE id = $1`, accA)
		require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accA}))
		require.Equal(t, map[string]string{"st": "active", "bu": "0", "cd": key(longer)}, withoutSct(f.hgetall(f.keys.Account(site.Key, keyA))))

		require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, nil))
	})

	t.Run("large accounts are chunked", func(t *testing.T) {
		accC, keyC := f.addAccount(site, "user-c", "active", nil)
		n := accountMembersPerCall + 5
		for i := 0; i < n; i++ {
			f.addIdentity(site, typ, identitySpec{accountID: accC})
		}
		f.exec(`UPDATE identities SET state = 'retired' WHERE hkey = (SELECT max(hkey) FROM identities WHERE account_id = $1)`, accC)
		f.exec(`UPDATE accounts SET cooldown_until = $1 WHERE id = $2`, f.now.Add(time.Minute), accC)
		require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accC}))
		members := f.smembers(f.keys.AccountMembers(site.Key, keyC))
		require.Len(t, members, n-1)
	})
}

func TestSyncProxiesAcrossSites(t *testing.T) {
	f := newFixture(t)
	s1 := f.addSite(f.ns, "one", "web")
	s2 := f.addSite(f.ns, "two", "web")
	other := f.addNamespace("other")
	s3 := f.addSite(other, "three", "web")
	gcd := f.now.Add(5 * time.Minute)
	active, ak := f.addProxy(f.ns, proxySpec{kind: "residential", region: "hk", provider: "acme", tags: []string{"fast", "hk"}, maxConc: 5})
	cooling, ck := f.addProxy(f.ns, proxySpec{cooldownUntil: &gcd})
	dead, dk := f.addProxy(f.ns, proxySpec{state: "dead"})

	// Fields owned by other scripts on site one must survive.
	siteCD := f.now.Add(10 * time.Minute).UnixMilli()
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(s1.Key, ck)).FieldValue().
		FieldValue("al", "3").FieldValue("sc", "88.00").FieldValue("cd", key(siteCD)).Build())

	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{active, cooling, dead}))
	for _, s := range []int64{s1.Key, s2.Key} {
		h := f.hgetall(f.keys.ProxySite(s, ak))
		require.Equal(t, active, h["pid"])
		require.Equal(t, "active", h["st"])
		require.Equal(t, "residential", h["kd"])
		require.Equal(t, "hk", h["rg"])
		require.Equal(t, "acme", h["pv"])
		require.Equal(t, ",fast,hk,", h["tg"])
		require.Equal(t, "5", h["mc"])
		require.Equal(t, "1", h["uv"])
		require.Equal(t, "0", h["gcd"])
		score, ok := f.zscore(f.keys.ProxyReady(s), ak)
		require.True(t, ok)
		require.Zero(t, score)
		_, ok = f.zscore(f.keys.ProxyReady(s), dk)
		require.False(t, ok)
		require.Equal(t, "dead", f.hgetall(f.keys.ProxySite(s, dk))["st"])
	}
	require.False(t, f.exists(f.keys.ProxySite(s3.Key, ak)), "other namespaces are untouched")

	h := f.hgetall(f.keys.ProxySite(s1.Key, ck))
	require.Equal(t, "3", h["al"])
	require.Equal(t, "88.00", h["sc"])
	require.Equal(t, key(siteCD), h["cd"])
	score, _ := f.zscore(f.keys.ProxyReady(s1.Key), ck)
	require.Equal(t, siteCD, score, "site cooldown beats global cooldown")
	score, _ = f.zscore(f.keys.ProxyReady(s2.Key), ck)
	require.Equal(t, gcd.UnixMilli(), score)

	// State changes remove proxies from the ready queue.
	f.exec(`UPDATE proxies SET state = 'disabled' WHERE id = $1`, active)
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{active}))
	_, ok := f.zscore(f.keys.ProxyReady(s1.Key), ak)
	require.False(t, ok)
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, nil))
}

func TestRemoveProxies(t *testing.T) {
	f := newFixture(t)
	s1 := f.addSite(f.ns, "one", "web")
	s2 := f.addSite(f.ns, "two", "web")
	typ1 := f.addType(s1, "web", "cookie")
	typ2 := f.addType(s2, "web", "cookie")
	p1, k1 := f.addProxy(f.ns, proxySpec{})
	p2, k2 := f.addProxy(f.ns, proxySpec{})
	p3, k3 := f.addProxy(f.ns, proxySpec{})
	i1, ik1 := f.addIdentity(s1, typ1, identitySpec{})
	i2, ik2 := f.addIdentity(s2, typ2, identitySpec{})
	i3, ik3 := f.addIdentity(s2, typ2, identitySpec{})
	f.bind(i1, p1)
	f.bind(i2, p2)
	f.bind(i3, p3)
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{p1, p2, p3}))
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, s1.ID, []string{i1}, SyncOptions{}))
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, s2.ID, []string{i2, i3}, SyncOptions{}))
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id) VALUES ($1, 'ps', $2), ($1, 'ps', $3)`, s1.ID, p1, p2)

	// p1 still exists in PostgreSQL; p2 was deleted before the call (scan fallback),
	// and SyncProxies removes deleted proxies too.
	f.exec(`DELETE FROM proxies WHERE id = $1`, p2)
	require.NoError(t, f.syncer.RemoveProxies(f.ctx, f.ns.ID, []string{p1, p2}))
	for _, s := range []int64{s1.Key, s2.Key} {
		for _, k := range []int64{k1, k2} {
			require.False(t, f.exists(f.keys.ProxySite(s, k)))
			_, ok := f.zscore(f.keys.ProxyReady(s), k)
			require.False(t, ok)
		}
	}
	require.Equal(t, "", f.hgetall(f.keys.Identity(s1.Key, ik1))["px"])
	require.Equal(t, "", f.hgetall(f.keys.Identity(s2.Key, ik2))["px"])
	require.Equal(t, key(k3), f.hgetall(f.keys.Identity(s2.Key, ik3))["px"])
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM hot_state_snapshots WHERE subject = 'ps'`).Scan(&n))
	require.Zero(t, n)

	f.exec(`DELETE FROM proxies WHERE id = $1`, p3)
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{p3}))
	require.False(t, f.exists(f.keys.ProxySite(s2.Key, k3)))
	require.Equal(t, "", f.hgetall(f.keys.Identity(s2.Key, ik3))["px"])
	require.NoError(t, f.syncer.RemoveProxies(f.ctx, f.ns.ID, nil))
}

func TestSyncAccountsPushesInBoundedCalls(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	groups := []int64{site.Groups[groupKey("web", "_default")].Key}
	for _, name := range []string{"search", "detail", "feed"} {
		groups = append(groups, f.addGroup(site, "web", name).Key)
	}
	accID, accKey := f.addAccount(site, "shared", "active", nil)
	// 4 groups → at most 250 members per push call, so 600 members need 3 calls.
	const members = 600
	f.exec(`
		INSERT INTO identities (id, site_id, client, type_id, account_id, state, unique_hash, payload_hash)
		SELECT 'idt_' || lpad(to_hex(n), 32, '0'), $1, 'web', $2, $3, 'active',
		       decode(lpad(to_hex(n), 32, '0'), 'hex'), decode(lpad(to_hex(n), 32, '0'), 'hex')
		FROM generate_series(1, $4::int) AS n`, site.ID, typ.ID, accID, members)
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	require.Len(t, f.smembers(f.keys.AccountMembers(site.Key, accKey)), members)

	cd := f.now.Add(45 * time.Minute)
	f.exec(`UPDATE accounts SET cooldown_until = $1 WHERE id = $2`, cd, accID)
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))
	for _, g := range groups {
		n, err := f.rdb.Do(f.ctx, f.rdb.B().Zcount().Key(f.keys.Ready(site.Key, g)).
			Min(ms(cd)).Max(ms(cd)).Build()).AsInt64()
		require.NoError(t, err)
		require.EqualValues(t, members, n, "group %d", g)
	}
	require.Equal(t, ms(cd), f.hgetall(f.keys.Account(site.Key, accKey))["cd"])

	// Nothing moved: no push, the membership is rebuilt unchanged.
	require.NoError(t, f.syncer.SyncAccounts(f.ctx, site.ID, []string{accID}))
	require.Len(t, f.smembers(f.keys.AccountMembers(site.Key, accKey)), members)
}

// withoutSct drops the lifecycle change time from a hash.
func withoutSct(h map[string]string) map[string]string {
	delete(h, "sct")
	return h
}
