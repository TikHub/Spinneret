package hotstate

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
)

func TestSyncIdentitiesStates(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	search := f.addGroup(site, "web", "search")
	typ := f.addType(site, "web", "cookie")
	def := site.Groups[groupKey("web", "_default")]
	until := f.now.Add(2 * time.Hour)
	activated := f.now.Add(-time.Hour)

	tests := []struct {
		name      string
		spec      identitySpec
		wantReady bool
		wantBU    string
		wantQU    string
	}{
		{name: "active", spec: identitySpec{state: "active", activatedAt: &activated, region: "hk", payloadVersion: 4}, wantReady: true, wantBU: "0", wantQU: "0"},
		{name: "pending", spec: identitySpec{state: "pending"}, wantReady: true, wantBU: "0", wantQU: "0"},
		{name: "temporary ban", spec: identitySpec{state: "banned", banUntil: &until}, wantBU: ms(until), wantQU: "0"},
		{name: "permanent ban", spec: identitySpec{state: "banned"}, wantBU: "-1", wantQU: "0"},
		{name: "quarantined", spec: identitySpec{state: "quarantined", quarantineUntil: &until}, wantBU: "0", wantQU: ms(until)},
		{name: "expired", spec: identitySpec{state: "expired"}, wantBU: "0", wantQU: "0"},
		{name: "disabled", spec: identitySpec{state: "disabled", banUntil: &until}, wantBU: "0", wantQU: "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, hk := f.addIdentity(site, typ, tc.spec)
			require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id, id}, SyncOptions{}))

			h := f.hgetall(f.keys.Identity(site.Key, hk))
			require.Equal(t, id, h["iid"])
			require.Equal(t, tc.spec.state, h["st"])
			require.Equal(t, "cookie", h["ty"])
			require.Equal(t, "3", h["tv"])
			require.Equal(t, key(int64(max(tc.spec.payloadVersion, 1))), h["pv"])
			require.Equal(t, "", h["acc"])
			require.Equal(t, tc.spec.region, h["rg"])
			require.Equal(t, tc.wantBU, h["bu"])
			require.Equal(t, tc.wantQU, h["qu"])
			require.Equal(t, "", h["px"])
			if tc.spec.activatedAt != nil {
				require.Equal(t, ms(*tc.spec.activatedAt), h["act"])
			} else {
				require.Equal(t, "0", h["act"])
			}
			for _, g := range []int64{def.Key, search.Key} {
				score, ok := f.zscore(f.keys.Ready(site.Key, g), hk)
				require.Equal(t, tc.wantReady, ok, "group %d", g)
				if ok {
					require.Zero(t, score)
				}
			}
		})
	}

	t.Run("state change removes from ready queues", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{state: "active"})
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		_, ok := f.zscore(f.keys.Ready(site.Key, def.Key), hk)
		require.True(t, ok)
		f.exec(`UPDATE identities SET state = 'banned', ban_until = NULL WHERE id = $1`, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		_, ok = f.zscore(f.keys.Ready(site.Key, def.Key), hk)
		require.False(t, ok)
		require.Equal(t, "-1", f.hgetall(f.keys.Identity(site.Key, hk))["bu"])
	})

	t.Run("retired identities are removed", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{state: "active"})
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		require.True(t, f.exists(f.keys.Identity(site.Key, hk)))
		f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id) VALUES ($1, 'ig', $2)`, site.ID, id)
		f.exec(`UPDATE identities SET state = 'retired' WHERE id = $1`, id)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		require.False(t, f.exists(f.keys.Identity(site.Key, hk)))
		_, ok := f.zscore(f.keys.Ready(site.Key, def.Key), hk)
		require.False(t, ok)
		var n int
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM hot_state_snapshots WHERE subject_id = $1`, id).Scan(&n))
		require.Zero(t, n)
	})

	t.Run("bound proxy", func(t *testing.T) {
		id, hk := f.addIdentity(site, typ, identitySpec{state: "active"})
		pid, pk := f.addProxy(f.ns, proxySpec{})
		f.bind(id, pid)
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
		require.Equal(t, key(pk), f.hgetall(f.keys.Identity(site.Key, hk))["px"])
	})

	t.Run("unknown site", func(t *testing.T) {
		err := f.syncer.SyncIdentities(f.ctx, idgen.New(idgen.Site), []string{idgen.New(idgen.Identity)}, SyncOptions{})
		require.True(t, apperr.IsNotFound(err))
	})

	t.Run("empty input", func(t *testing.T) {
		require.NoError(t, f.syncer.SyncIdentities(f.ctx, idgen.New(idgen.Site), nil, SyncOptions{}))
		require.NoError(t, f.syncer.RemoveIdentities(f.ctx, idgen.New(idgen.Site), []string{""}))
	})
}

func TestSyncIdentitiesEligibilityByType(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web", "app")
	cookie := f.addType(site, "web", "cookie")
	device := f.addType(site, "web", "device")
	appType := f.addType(site, "app", "appdev")
	onlyCookie := f.addGroup(site, "web", "detail")
	onlyCookie.IdentityTypeIDs = []string{cookie.ID}
	both := site.Groups[groupKey("web", "_default")]
	appGroup := site.Groups[groupKey("app", "_default")]

	cid, ck := f.addIdentity(site, cookie, identitySpec{})
	did, dk := f.addIdentity(site, device, identitySpec{})
	aid, ak := f.addIdentity(site, appType, identitySpec{})
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{cid, did, aid}, SyncOptions{}))

	tests := []struct {
		group int64
		ident int64
		want  bool
	}{
		{onlyCookie.Key, ck, true},
		{onlyCookie.Key, dk, false},
		{both.Key, ck, true},
		{both.Key, dk, true},
		{both.Key, ak, false},
		{appGroup.Key, ak, true},
		{appGroup.Key, ck, false},
	}
	for _, tc := range tests {
		_, ok := f.zscore(f.keys.Ready(site.Key, tc.group), tc.ident)
		require.Equal(t, tc.want, ok, "group %d identity %d", tc.group, tc.ident)
	}

	// Eligibility changes are applied on the next synchronization, in both directions.
	onlyCookie.IdentityTypeIDs = []string{device.ID}
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{cid, did}, SyncOptions{}))
	_, ok := f.zscore(f.keys.Ready(site.Key, onlyCookie.Key), ck)
	require.False(t, ok)
	_, ok = f.zscore(f.keys.Ready(site.Key, onlyCookie.Key), dk)
	require.True(t, ok)
}

func TestSyncIdentitiesResetOptions(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id, hk := f.addIdentity(site, typ, identitySpec{})
	cd := f.now.Add(10 * time.Minute).UnixMilli()
	packed := "42.50|1700000000000|12|4|1700000001000|" + key(cd) + "|0|1700000002000"
	f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().FieldValue(key(hk), packed).Build())
	streak := func() {
		f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, hk)).FieldValue().
			FieldValue("gnf", "6").FieldValue("glf", key(f.now.Add(-time.Second).UnixMilli())).Build())
	}
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, hk)).FieldValue().
		FieldValue("gs", "33.00").FieldValue("gts", "1700000000000").FieldValue("gn", "9").Build())
	streak()

	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
	require.True(t, ok)
	require.Equal(t, cd, score, "score follows the endpoint cooldown")
	require.Equal(t, packed, f.hgetall(f.keys.Health(site.Key, g.Key))[key(hk)])
	require.Empty(t, f.smembers(f.keys.Dirty(site.Key)))
	require.Equal(t, "6", f.hgetall(f.keys.Identity(site.Key, hk))["gnf"], "no reset keeps the global streak")

	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetFailures: true}))
	require.Equal(t, "42.50|1700000000000|12|0|0|"+key(cd)+"|0|1700000002000",
		f.hgetall(f.keys.Health(site.Key, g.Key))[key(hk)])
	require.ElementsMatch(t, []string{"e" + key(g.Key) + ":" + key(hk)}, f.smembers(f.keys.Dirty(site.Key)))
	h := f.hgetall(f.keys.Identity(site.Key, hk))
	require.Equal(t, "33.00", h["gs"])
	require.NotContains(t, h, "gnf", "a failure reset clears the global streak")
	require.NotContains(t, h, "glf")

	streak()
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetHealth: true, ResetFailures: true}))
	h = f.hgetall(f.keys.Identity(site.Key, hk))
	require.NotContains(t, h, "gs")
	require.NotContains(t, h, "gts")
	require.NotContains(t, h, "gn")
	require.NotContains(t, h, "gnf", "a health reset clears the global streak")
	require.NotContains(t, h, "glf")
	require.ElementsMatch(t, []string{"e" + key(g.Key) + ":" + key(hk), "g" + key(hk)}, f.smembers(f.keys.Dirty(site.Key)))
}

func TestSyncIdentitiesHealthResetKeepsSchedulingState(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id, hk := f.addIdentity(site, typ, identitySpec{})
	cd := f.now.Add(time.Hour).UnixMilli()
	ru := f.now.Add(10 * time.Minute).UnixMilli()
	lu := f.now.Add(-time.Second).UnixMilli()
	packed := "42.50|1700000000000|12|4|1700000001000|" + key(cd) + "|" + key(ru) + "|" + key(lu)
	f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().FieldValue(key(hk), packed).Build())

	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetHealth: true, ResetFailures: true}))
	score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
	require.True(t, ok)
	require.Equal(t, cd, score, "a health reset keeps the endpoint cooldown")
	require.Equal(t, "70.00|"+key(f.now.UnixMilli())+"|0|0|0|"+key(cd)+"|"+key(ru)+"|"+key(lu),
		f.hgetall(f.keys.Health(site.Key, g.Key))[key(hk)], "health resets to the baseline and keeps cd, ru and lu")
	require.ElementsMatch(t, []string{"e" + key(g.Key) + ":" + key(hk), "g" + key(hk)}, f.smembers(f.keys.Dirty(site.Key)))

	// The baseline of the group's action policy is used.
	g.Action = &policy.CompiledAction{}
	g.Action.Health.Baseline = 55
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetHealth: true}))
	require.Equal(t, "55.00|"+key(f.now.UnixMilli())+"|0|0|0|"+key(cd)+"|"+key(ru)+"|"+key(lu),
		f.hgetall(f.keys.Health(site.Key, g.Key))[key(hk)])
}

func TestSyncNeverClobbersHotFields(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id, hk := f.addIdentity(site, typ, identitySpec{})
	idKey := f.keys.Identity(site.Key, hk)
	xl := f.now.Add(3 * time.Minute).UnixMilli()
	hot := map[string]string{
		"al": "2", "xl": key(xl), "scd": key(f.now.Add(time.Minute).UnixMilli()),
		"sru": key(f.now.Add(2 * time.Minute).UnixMilli()), "gs": "71.25", "gts": "1700000000000", "gn": "15",
		"lu": "1700000003000", "rbd": "20260917", "rbn": "2",
	}
	cmd := f.rdb.B().Hset().Key(idKey).FieldValue()
	for k, v := range hot {
		cmd = cmd.FieldValue(k, v)
	}
	f.do(cmd.Build())

	for _, sync := range []func() error{
		func() error { return f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}) },
		func() error {
			return f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{ResetFailures: true})
		},
		func() error { return f.syncer.SyncSite(f.ctx, site.ID) },
	} {
		require.NoError(t, sync())
		h := f.hgetall(idKey)
		for k, v := range hot {
			require.Equal(t, v, h[k], "field %s", k)
		}
		score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
		require.True(t, ok)
		require.Equal(t, xl, score, "availability includes xl while leases are active")
	}

	// Concurrent writers of hot counters are never lost while syncs run.
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 200; i++ {
			if err := f.rdb.Do(f.ctx, f.rdb.B().Hincrby().Key(idKey).Field("al").Increment(1).Build()).Error(); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 5; i++ {
		require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	}
	require.NoError(t, <-done)
	require.Equal(t, "202", f.hgetall(idKey)["al"])
}

func TestRemoveIdentities(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	accID, accKey := f.addAccount(site, "user-1", "active", nil)
	id1, k1 := f.addIdentity(site, typ, identitySpec{accountID: accID})
	id2, k2 := f.addIdentity(site, typ, identitySpec{})
	id3, k3 := f.addIdentity(site, typ, identitySpec{})
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id1, id2, id3}, SyncOptions{}))
	for _, k := range []int64{k1, k2} {
		f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().FieldValue(key(k), "50.00|1|1|0|0|0|0|0").Build())
		f.do(f.rdb.B().Zadd().Key(f.keys.Bans(site.Key, k)).ScoreMember().ScoreMember(1, "1").Build())
	}
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, endpoint_group_id) VALUES ($1, 'ie', $2, $3)`, site.ID, id2, g.ID)

	// id1 still exists in PostgreSQL; id2 is deleted first (scan fallback).
	f.exec(`DELETE FROM identities WHERE id = $1`, id2)
	require.NoError(t, f.syncer.RemoveIdentities(f.ctx, site.ID, []string{id1, id2, "not-an-id"}))
	for _, k := range []int64{k1, k2} {
		require.False(t, f.exists(f.keys.Identity(site.Key, k)))
		require.False(t, f.exists(f.keys.Bans(site.Key, k)))
		_, ok := f.zscore(f.keys.Ready(site.Key, g.Key), k)
		require.False(t, ok)
		_, ok = f.hgetall(f.keys.Health(site.Key, g.Key))[key(k)]
		require.False(t, ok)
	}
	require.Empty(t, f.smembers(f.keys.AccountMembers(site.Key, accKey)))
	require.True(t, f.exists(f.keys.Identity(site.Key, k3)))
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM hot_state_snapshots WHERE site_id = $1`, site.ID).Scan(&n))
	require.Zero(t, n)

	// SyncIdentities removes identities that no longer exist.
	f.exec(`DELETE FROM identities WHERE id = $1`, id3)
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id3}, SyncOptions{}))
	require.False(t, f.exists(f.keys.Identity(site.Key, k3)))
}
