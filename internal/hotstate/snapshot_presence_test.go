package hotstate

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSnapshotWithoutScores(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id, hk := f.addIdentity(site, typ, identitySpec{})
	pid, pk := f.addProxy(f.ns, proxySpec{})
	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	scd := f.now.Add(2 * time.Hour)
	pcd := f.now.Add(30 * time.Minute)

	// Manual cooldowns of an identity and a proxy that were never observed:
	// no global score, no proxy score.
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, hk)).FieldValue().FieldValue("scd", ms(scd)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, pk)).FieldValue().FieldValue("cd", ms(pcd)).Build())
	f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member("g"+key(hk), "p"+key(pk)).Build())
	require.NoError(t, f.syncer.SnapshotJob().Run(f.ctx))

	var samples int
	var cooldown *time.Time
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT samples, cooldown_until FROM hot_state_snapshots
	        WHERE site_id = $1 AND subject = 'ig' AND subject_id = $2`, site.ID, id).Scan(&samples, &cooldown))
	require.Zero(t, samples)
	require.Equal(t, scd.UnixMilli(), cooldown.UnixMilli())
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT samples, cooldown_until FROM hot_state_snapshots
	        WHERE site_id = $1 AND subject = 'ps' AND subject_id = $2`, site.ID, pid).Scan(&samples, &cooldown))
	require.Zero(t, samples)
	require.Equal(t, pcd.UnixMilli(), cooldown.UnixMilli())

	// Redis loses its data: cooldowns come back, scores stay at the baseline.
	f.do(f.rdb.B().Del().Key(f.keys.Identity(site.Key, hk), f.keys.ProxySite(site.Key, pk), f.keys.Epoch()).Build())
	rebuilt, err := f.syncer.EnsureBuilt(f.ctx)
	require.NoError(t, err)
	require.True(t, rebuilt)
	h := f.hgetall(f.keys.Identity(site.Key, hk))
	require.Equal(t, ms(scd), h["scd"])
	require.NotContains(t, h, "gs")
	require.NotContains(t, h, "gn")
	score, ok := f.zscore(f.keys.Ready(site.Key, g.Key), hk)
	require.True(t, ok)
	require.Equal(t, scd.UnixMilli(), score)
	px := f.hgetall(f.keys.ProxySite(site.Key, pk))
	require.Equal(t, ms(pcd), px["cd"])
	require.NotContains(t, px, "sc")
	require.NotContains(t, px, "sn")
	pscore, ok := f.zscore(f.keys.ProxyReady(site.Key), pk)
	require.True(t, ok)
	require.Equal(t, pcd.UnixMilli(), pscore)

	// Once every value is gone the rows are deleted.
	f.do(f.rdb.B().Hdel().Key(f.keys.Identity(site.Key, hk)).Field("scd").Build())
	f.do(f.rdb.B().Hdel().Key(f.keys.ProxySite(site.Key, pk)).Field("cd").Build())
	f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member("g"+key(hk), "p"+key(pk)).Build())
	require.NoError(t, f.syncer.SnapshotJob().Run(f.ctx))
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM hot_state_snapshots WHERE site_id = $1`, site.ID).Scan(&n))
	require.Zero(t, n)
}

func TestSnapshotToleratesMalformedEntries(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id, hk := f.addIdentity(site, typ, identitySpec{})

	// A far-future cooldown, an overflowing sample count and the same entry
	// spelled twice must not fail (and endlessly requeue) the batch.
	f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().
		FieldValue(key(hk), "50.00|1700000000000|99999999999|2|0|99999999999999999|0|0").Build())
	f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member(
		fmt.Sprintf("e%d:%d", g.Key, hk), fmt.Sprintf("e%d:0%d", g.Key, hk), fmt.Sprintf("e0%d:%d", g.Key, hk)).Build())
	require.NoError(t, f.syncer.SnapshotJob().Run(f.ctx))
	require.Empty(t, f.smembers(f.keys.Dirty(site.Key)))

	var samples int
	var cooldown time.Time
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT samples, cooldown_until FROM hot_state_snapshots
	        WHERE site_id = $1 AND subject = 'ie' AND subject_id = $2 AND endpoint_group_id = $3`,
		site.ID, id, g.ID).Scan(&samples, &cooldown))
	require.Equal(t, int(clampInt32(99999999999)), samples)
	require.Equal(t, 9999, cooldown.UTC().Year())
}

func TestSnapshotCodecClamps(t *testing.T) {
	require.Equal(t, int32(7), clampInt32(7))
	require.Equal(t, int32(2147483647), clampInt32(1<<40))
	require.Equal(t, int32(-2147483648), clampInt32(-(1 << 40)))
	require.Equal(t, maxTimestampMs, msToTimePtr(1<<62).UnixMilli())
	require.Equal(t, proxyQuarantineUntilMs(stateQuar, nil), int64(0))
	until := time.UnixMilli(42)
	require.Equal(t, int64(42), proxyQuarantineUntilMs(stateQuar, &until))
	require.Equal(t, int64(0), proxyQuarantineUntilMs(stateBanned, &until))
}
