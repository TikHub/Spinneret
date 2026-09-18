package hotstate

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/jobs"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

func TestIdentityHotState(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	search := f.addGroup(site, "web", "search")
	search.IdentityTypeIDs = nil
	accID, accKey := f.addAccount(site, "user", "active", nil)
	id, hk := f.addIdentity(site, typ, identitySpec{accountID: accID})
	pid, _ := f.addProxy(f.ns, proxySpec{})
	f.bind(id, pid)

	t.Run("not materialized", func(t *testing.T) {
		hot, err := f.syncer.IdentityHotState(f.ctx, site, id)
		require.NoError(t, err)
		require.False(t, hot.Present)
		require.Len(t, hot.Groups, 2)
		require.InDelta(t, 70, hot.GlobalScore, 0.001)
		for _, eg := range hot.Groups {
			require.False(t, eg.InReadyQueue)
			require.True(t, eg.AvailableAt.IsZero())
			require.InDelta(t, 70, eg.Score, 0.001)
		}
	})

	require.NoError(t, f.syncer.SyncProxies(f.ctx, f.ns.ID, []string{pid}))
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, []string{id}, SyncOptions{}))
	cd := f.now.Add(20 * time.Minute)
	scoreTS := f.now.Add(-6 * time.Hour)
	f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().
		FieldValue(key(hk), "30.00|"+ms(scoreTS)+"|25|6|"+ms(f.now)+"|"+ms(cd)+"|0|"+ms(f.now)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, hk)).FieldValue().
		FieldValue("al", "1").FieldValue("xl", ms(cd)).FieldValue("scd", ms(f.now.Add(time.Minute))).
		FieldValue("gs", "90.00").FieldValue("gts", ms(f.now)).FieldValue("gn", "31").Build())
	f.do(f.rdb.B().Hset().Key(f.keys.Account(site.Key, accKey)).FieldValue().FieldValue("cd", ms(f.now.Add(time.Hour))).Build())
	f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, g.Key)).ScoreMember().ScoreMember(float64(cd.UnixMilli()), key(hk)).Build())

	hot, err := f.syncer.IdentityHotState(f.ctx, site, id)
	require.NoError(t, err)
	require.True(t, hot.Present)
	require.Equal(t, "active", hot.State)
	require.Equal(t, 1, hot.ActiveLeases)
	require.Equal(t, cd.UTC(), hot.ExclusiveUntil)
	require.Equal(t, f.now.Add(time.Minute).UTC(), hot.SiteCooldownUntil)
	require.True(t, hot.SiteReuseUntil.IsZero())
	require.Equal(t, pid, hot.BoundProxyID)
	require.InDelta(t, 90, hot.GlobalScore, 0.001)
	require.Equal(t, 31, hot.GlobalSamples)
	require.Equal(t, f.now.Add(time.Hour).UTC(), hot.AccountCooldownUntil)
	require.Len(t, hot.Groups, 2)
	def := hot.Groups[0]
	require.Equal(t, "_default", def.EndpointGroup)
	require.Equal(t, g.ID, def.EndpointGroupID)
	require.Equal(t, "web", def.Client)
	require.InDelta(t, 70+(30-70)*math.Exp(-1), def.Score, 0.01, "one tau of decay")
	require.Equal(t, 25, def.Samples)
	require.Equal(t, 6, def.ConsecutiveFailures)
	require.Equal(t, cd.UTC(), def.CooldownUntil)
	require.Equal(t, f.now.UTC(), def.LastUsedAt)
	require.True(t, def.InReadyQueue)
	require.Equal(t, cd.UTC(), def.AvailableAt)
	require.Equal(t, "search", hot.Groups[1].EndpointGroup)
	require.False(t, hot.Groups[1].InReadyQueue)

	t.Run("bound proxy fallback to PostgreSQL", func(t *testing.T) {
		f.do(f.rdb.B().Hdel().Key(f.keys.ProxySite(site.Key, mustProxyKey(t, f, pid))).Field("pid").Build())
		hot, err := f.syncer.IdentityHotState(f.ctx, site, id)
		require.NoError(t, err)
		require.Equal(t, pid, hot.BoundProxyID)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := f.syncer.IdentityHotState(f.ctx, site, idgen.New(idgen.Identity))
		require.True(t, apperr.IsNotFound(err))
		_, err = f.syncer.IdentityHotState(f.ctx, nil, id)
		require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
		_, err = f.syncer.ReadyCounts(f.ctx, nil, f.now)
		require.Error(t, err)
	})
}

func mustProxyKey(t *testing.T, f *fixture, proxyID string) int64 {
	t.Helper()
	var k int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT hkey FROM proxies WHERE id = $1`, proxyID).Scan(&k))
	return k
}

func TestReadyCounts(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	search := f.addGroup(site, "web", "search")
	var ids []string
	for i := 0; i < 5; i++ {
		id, hk := f.addIdentity(site, typ, identitySpec{})
		ids = append(ids, id)
		if i < 2 {
			f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().
				FieldValue(key(hk), "70.00|0|0|0|0|"+ms(f.now.Add(time.Minute))+"|0|0").Build())
		}
	}
	require.NoError(t, f.syncer.SyncIdentities(f.ctx, site.ID, ids, SyncOptions{}))
	counts, err := f.syncer.ReadyCounts(f.ctx, site, f.now)
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{g.Key: 3, search.Key: 5}, counts)

	empty := f.addSite(f.ns, "empty", "web")
	for id := range empty.GroupsByID {
		delete(empty.GroupsByID, id)
	}
	counts, err = f.syncer.ReadyCounts(f.ctx, empty, f.now)
	require.NoError(t, err)
	require.Empty(t, counts)
}

func TestSnapshotJobRoundTrip(t *testing.T) {
	f := newFixture(t)
	site := f.addSite(f.ns, "demo", "web")
	typ := f.addType(site, "web", "cookie")
	g := site.Groups[groupKey("web", "_default")]
	id1, k1 := f.addIdentity(site, typ, identitySpec{})
	id2, k2 := f.addIdentity(site, typ, identitySpec{})
	pid, pk := f.addProxy(f.ns, proxySpec{})
	cd := f.now.Add(10 * time.Minute)
	sts := f.now.Add(-time.Minute)

	f.do(f.rdb.B().Hset().Key(f.keys.Health(site.Key, g.Key)).FieldValue().
		FieldValue(key(k1), "44.25|"+ms(sts)+"|12|3|"+ms(sts)+"|"+ms(cd)+"|"+ms(cd)+"|"+ms(sts)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.Identity(site.Key, k1)).FieldValue().
		FieldValue("gs", "51.00").FieldValue("gts", ms(sts)).FieldValue("gn", "8").FieldValue("scd", ms(cd)).Build())
	f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, pk)).FieldValue().
		FieldValue("sc", "33.00").FieldValue("sts", ms(sts)).FieldValue("sn", "5").FieldValue("nf", "2").
		FieldValue("lf", ms(sts)).FieldValue("cd", ms(cd)).Build())
	// id2 had a snapshot row but its health entry is gone now.
	f.exec(`INSERT INTO hot_state_snapshots (site_id, subject, subject_id, endpoint_group_id, score) VALUES ($1, 'ie', $2, $3, 10)`,
		site.ID, id2, g.ID)
	f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member(
		"e"+key(g.Key)+":"+key(k1), "g"+key(k1), "p"+key(pk), "e"+key(g.Key)+":"+key(k2),
		"g999999", "p999999", "e1:", "x1", "garbage").Build())

	job := f.syncer.SnapshotJob()
	require.Equal(t, "hotstate_snapshot", job.Name)
	require.Equal(t, jobs.Leader, job.Mode)
	require.Equal(t, time.Minute, job.Interval)
	require.NoError(t, job.Validate())
	require.NoError(t, job.Run(f.ctx))
	require.Empty(t, f.smembers(f.keys.Dirty(site.Key)))

	type row struct {
		score                     float64
		samples, failures         int
		lastFail, cooldown, reuse *time.Time
		lastUsed                  *time.Time
		updated                   time.Time
	}
	load := func(subject, subjectID, groupID string) (row, bool) {
		var r row
		err := f.pool.QueryRow(f.ctx, `SELECT score, samples, consecutive_failures, last_failure_at, cooldown_until,
		        reuse_until, last_used_at, updated_at FROM hot_state_snapshots
		        WHERE site_id = $1 AND subject = $2 AND subject_id = $3 AND endpoint_group_id = $4`,
			site.ID, subject, subjectID, groupID).Scan(&r.score, &r.samples, &r.failures, &r.lastFail, &r.cooldown,
			&r.reuse, &r.lastUsed, &r.updated)
		if err != nil {
			return r, false
		}
		return r, true
	}
	ie, ok := load("ie", id1, g.ID)
	require.True(t, ok)
	require.InDelta(t, 44.25, ie.score, 0.001)
	require.Equal(t, 12, ie.samples)
	require.Equal(t, 3, ie.failures)
	require.Equal(t, sts.UnixMilli(), ie.lastFail.UnixMilli())
	require.Equal(t, cd.UnixMilli(), ie.cooldown.UnixMilli())
	require.Equal(t, cd.UnixMilli(), ie.reuse.UnixMilli())
	require.Equal(t, sts.UnixMilli(), ie.lastUsed.UnixMilli())
	require.Equal(t, sts.UnixMilli(), ie.updated.UnixMilli())

	ig, ok := load("ig", id1, "")
	require.True(t, ok)
	require.InDelta(t, 51, ig.score, 0.001)
	require.Equal(t, 8, ig.samples)
	require.Equal(t, cd.UnixMilli(), ig.cooldown.UnixMilli())
	require.Nil(t, ig.reuse)

	ps, ok := load("ps", pid, "")
	require.True(t, ok)
	require.InDelta(t, 33, ps.score, 0.001)
	require.Equal(t, 5, ps.samples)
	require.Equal(t, 2, ps.failures)
	require.Equal(t, cd.UnixMilli(), ps.cooldown.UnixMilli())

	_, ok = load("ie", id2, g.ID)
	require.False(t, ok, "rows of vanished entries are deleted")

	// The restore path consumes what the job wrote.
	f.do(f.rdb.B().Del().Key(f.keys.Health(site.Key, g.Key), f.keys.Identity(site.Key, k1), f.keys.ProxySite(site.Key, pk)).Build())
	require.NoError(t, f.syncer.RebuildAll(f.ctx))
	restored := f.hgetall(f.keys.Health(site.Key, g.Key))[key(k1)]
	require.Equal(t, "44.25|"+ms(sts)+"|12|3|"+ms(sts)+"|"+ms(cd)+"|"+ms(cd)+"|"+ms(sts), restored)

	t.Run("failed writes requeue entries", func(t *testing.T) {
		f.do(f.rdb.B().Hset().Key(f.keys.ProxySite(site.Key, pk)).FieldValue().FieldValue("sc", "12.00").Build())
		f.do(f.rdb.B().Sadd().Key(f.keys.Dirty(site.Key)).Member("p" + key(pk)).Build())
		// The site ID violates the foreign key, so the upsert fails after the pop.
		bad := snapshotSite{id: idgen.New(idgen.Site), key: site.Key, namespaceID: f.ns.ID}
		_, err := f.syncer.snapshotRound(f.ctx, bad, newSnapshotRun())
		require.Error(t, err)
		require.ElementsMatch(t, []string{"p" + key(pk)}, f.smembers(f.keys.Dirty(site.Key)))
		good := snapshotSite{id: site.ID, key: site.Key, namespaceID: f.ns.ID}
		n, err := f.syncer.snapshotRound(f.ctx, good, newSnapshotRun())
		require.NoError(t, err)
		require.Equal(t, 1, n)
		ps, ok := load("ps", pid, "")
		require.True(t, ok)
		require.InDelta(t, 12, ps.score, 0.001)
	})
}
