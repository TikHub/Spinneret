package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func seedOverview(e *env) {
	t := e.t
	t.Helper()
	nowMs := e.now.UnixMilli()

	// Identities: shop 2 active, 1 pending, 1 banned; forum 1 active.
	i1 := e.identity("idt_a1", siteA, typeA, "web", "active", "", "", nil)
	i2 := e.identity("idt_a2", siteA, typeA, "web", "active", "", "", nil)
	i3 := e.identity("idt_a3", siteA, typeA, "web", "pending", "", "", nil)
	e.identity("idt_a4", siteA, typeA, "web", "banned", "", "", nil)
	k1 := e.identity("idt_k1", siteB, typeB, "web", "active", "", "", nil)
	e.proxy("pxy_1", "active")
	e.proxy("pxy_2", "active")
	e.proxy("pxy_3", "dead")

	// Window of 5 minutes = the complete minutes [now-5m, now) floored.
	cur := minuteFloor(e.now)
	e.acquireStat(cur.Add(-1*time.Minute), siteA, egSearch, "ok", 240)
	e.acquireStat(cur.Add(-2*time.Minute), siteA, egSearch, "exhausted", 60)
	e.acquireStat(cur.Add(-5*time.Minute), siteA, egFeed, "ok", 300)
	e.acquireStat(cur.Add(-6*time.Minute), siteA, egFeed, "ok", 9999) // outside the window
	e.acquireStat(cur, siteA, egFeed, "ok", 7777)                     // current minute, excluded
	e.acquireStat(cur.Add(-3*time.Minute), siteB, egForumSearch, "circuit_open", 30)
	e.outcomeStat(cur.Add(-1*time.Minute), siteA, egSearch, "pxy_1", "success", 200, 1000)
	e.outcomeStat(cur.Add(-1*time.Minute), siteA, egSearch, "pxy_2", "success", 100, 1000)
	e.outcomeStat(cur.Add(-2*time.Minute), siteA, egSearch, "", "rate_limited", 50, 0)
	e.outcomeStat(cur.Add(-2*time.Minute), siteA, egSearch, "", "captcha", 25, 0)
	e.outcomeStat(cur.Add(-3*time.Minute), siteA, "", "", "unknown", 15, 0)
	e.outcomeStat(cur.Add(-4*time.Minute), siteA, egFeed, "", "client_error", 10, 0)
	e.outcomeStat(cur.Add(-7*time.Minute), siteA, egFeed, "", "banned", 5000, 0) // outside the window
	e.outcomeStat(cur.Add(-1*time.Minute), siteB, egForumSearch, "", "success", 60, 0)

	k := e.keys
	e.redisDo(
		// shop web search: 2 ready now, 1 later; feed: 1 ready; app feed: empty.
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, egSearchKey)).ScoreMember().
			ScoreMember(float64(nowMs-10), itoa(i1)).ScoreMember(float64(nowMs), itoa(i2)).
			ScoreMember(float64(nowMs+60_000), itoa(i3)).Build(),
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, egFeedKey)).ScoreMember().ScoreMember(float64(nowMs-1), itoa(i3)).Build(),
		e.rdb.B().Zadd().Key(k.Ready(siteBKey, egForumKey)).ScoreMember().ScoreMember(float64(nowMs-1), itoa(k1)).Build(),
		// Breakers: search open, feed half_open, app feed stale member (closed),
		// unknown key ignored.
		e.rdb.B().Sadd().Key(k.OpenBreakers(siteAKey)).Member(itoa(egSearchKey), itoa(egFeedKey), itoa(egAppFeedKey), "999999", "junk").Build(),
		e.rdb.B().Hset().Key(k.Breaker(siteAKey, egSearchKey)).FieldValue().FieldValue("st", "open").Build(),
		e.rdb.B().Hset().Key(k.Breaker(siteAKey, egFeedKey)).FieldValue().FieldValue("st", "half_open").Build(),
		e.rdb.B().Hset().Key(k.Breaker(siteAKey, egAppFeedKey)).FieldValue().FieldValue("st", "closed").Build(),
		e.rdb.B().Hset().Key(k.Breaker(siteAKey, 999999)).FieldValue().FieldValue("st", "open").Build(),
		e.rdb.B().Sadd().Key(k.OpenBreakers(siteBKey)).Member(itoa(egForumKey)).Build(),
		e.rdb.B().Hset().Key(k.Breaker(siteBKey, egForumKey)).FieldValue().FieldValue("st", "open").Build(),
	)
	// Streams: shard 0 has a consumer group with 1 pending and 2 undelivered
	// entries, shard 1 has 2 entries and no group, shard 2 does not exist.
	for range 3 {
		e.redisDo(e.rdb.B().Xadd().Key(k.Stream(0)).Id("*").FieldValue().FieldValue("d", "{}").Build())
	}
	for range 2 {
		e.redisDo(e.rdb.B().Xadd().Key(k.Stream(1)).Id("*").FieldValue().FieldValue("d", "{}").Build())
	}
	e.redisDo(e.rdb.B().XgroupCreate().Key(k.Stream(0)).Group(reportConsumerGroup).Id("0").Build())
	_, err := e.rdb.Do(e.ctx, e.rdb.B().Xreadgroup().Group(reportConsumerGroup, "c1").Count(1).
		Streams().Key(k.Stream(0)).Id(">").Build()).AsXRead()
	require.NoError(t, err)
}

func TestOverview(t *testing.T) {
	e := newEnv(t, false)
	seedOverview(e)

	ov, err := e.svc.Overview(e.ctx, allScope(), 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 5*time.Minute, ov.Window)
	require.Equal(t, e.now, ov.GeneratedAt)
	require.Equal(t, int64(5), ov.StreamsPending)
	require.Len(t, ov.Sites, 2)

	// Sites come back ordered by name: forum, then shop.
	a := ov.Sites[1]
	require.Equal(t, "shop", a.Site)
	require.Equal(t, siteA, a.SiteID)
	require.Equal(t, "Shop (example site)", a.DisplayName)
	require.False(t, a.Paused)
	require.Equal(t, map[string]int64{"active": 2, "pending": 1, "banned": 1}, a.IdentitiesByState)
	require.Equal(t, map[string]int64{"active": 2, "dead": 1}, a.ProxiesByState)
	// web: max(search 2, feed 1, _default 0) = 2; app: 0.
	require.Equal(t, int64(2), a.AvailableIdentities)
	require.InDelta(t, 600.0/300, a.AcquireQPS, 1e-9)
	require.InDelta(t, 60.0/600, a.AcquireFailureRatio, 1e-9)
	require.InDelta(t, 400.0/300, a.ReportQPS, 1e-9)
	require.InDelta(t, 300.0/400, a.SuccessRatio, 1e-9)
	require.InDelta(t, 75.0/400, a.RiskRatio, 1e-9)
	require.InDelta(t, 15.0/400, a.UnknownRatio, 1e-9)
	require.InDelta(t, 10.0/400, a.ClientErrorRatio, 1e-9)
	require.Equal(t, 1, a.OpenBreakers)
	require.Equal(t, 1, a.HalfOpenBreakers)
	require.Equal(t, []LowWatermarkWarning{{
		Client: "web", EndpointGroup: "search", EndpointGroupID: egSearch, Available: 2, LowWatermark: 3,
	}}, a.LowWatermarkWarnings)

	b := ov.Sites[0]
	require.Equal(t, "forum", b.Site)
	require.True(t, b.Paused)
	require.Equal(t, int64(1), b.AvailableIdentities)
	require.InDelta(t, 30.0/300, b.AcquireQPS, 1e-9)
	require.InDelta(t, 1.0, b.AcquireFailureRatio, 1e-9)
	require.InDelta(t, 1.0, b.SuccessRatio, 1e-9)
	require.Equal(t, 1, b.OpenBreakers)
	require.Empty(t, b.LowWatermarkWarnings)

	tot := ov.Totals
	require.Empty(t, tot.Site)
	require.Equal(t, map[string]int64{"active": 3, "pending": 1, "banned": 1}, tot.IdentitiesByState)
	require.Equal(t, map[string]int64{"active": 2, "dead": 1}, tot.ProxiesByState)
	require.Equal(t, int64(3), tot.AvailableIdentities)
	require.InDelta(t, 630.0/300, tot.AcquireQPS, 1e-9)
	require.InDelta(t, 90.0/630, tot.AcquireFailureRatio, 1e-9)
	require.InDelta(t, 460.0/300, tot.ReportQPS, 1e-9)
	require.InDelta(t, 360.0/460, tot.SuccessRatio, 1e-9)
	require.Equal(t, 2, tot.OpenBreakers)
	require.Equal(t, 1, tot.HalfOpenBreakers)

	// A one minute window only sees the last complete minute.
	ov, err = e.svc.Overview(e.ctx, allScope(), time.Minute)
	require.NoError(t, err)
	require.InDelta(t, 240.0/60, ov.Sites[1].AcquireQPS, 1e-9)
	require.InDelta(t, 1.0, ov.Sites[1].SuccessRatio, 1e-9)

	// A site-restricted scope only returns its sites.
	ov, err = e.svc.Overview(e.ctx, Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB, "sit_other_namespace"}}, 5*time.Minute)
	require.NoError(t, err)
	require.Len(t, ov.Sites, 1)
	require.Equal(t, "forum", ov.Sites[0].Site)
	require.Equal(t, map[string]int64{"active": 1}, ov.Totals.IdentitiesByState)
	require.InDelta(t, 30.0/300, ov.Totals.AcquireQPS, 1e-9)

	// No readable site: empty lists, namespace level data only.
	ov, err = e.svc.Overview(e.ctx, Scope{NamespaceID: namespaceID}, 5*time.Minute)
	require.NoError(t, err)
	require.Empty(t, ov.Sites)
	require.Zero(t, ov.Totals.AcquireQPS)
	require.Equal(t, int64(5), ov.StreamsPending)
}

func TestOverviewErrors(t *testing.T) {
	e := newEnv(t, false)
	for _, tc := range []struct {
		name   string
		scope  Scope
		window time.Duration
		code   string
	}{
		{"window too small", allScope(), 30 * time.Second, "invalid_argument"},
		{"window not whole minutes", allScope(), 90 * time.Second, "invalid_argument"},
		{"window too large", allScope(), 25 * time.Hour, "invalid_argument"},
		{"missing namespace", Scope{}, 5 * time.Minute, "invalid_argument"},
		{"unknown namespace", Scope{NamespaceID: "ns_missing"}, 5 * time.Minute, "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Overview(e.ctx, tc.scope, tc.window)
			require.Error(t, err)
			require.Equal(t, apperr.Reason(tc.code), apperr.ReasonOf(err))
		})
	}
}

func TestStreamsPendingAfterAck(t *testing.T) {
	e := newEnv(t, false)
	k := e.keys
	for range 4 {
		e.redisDo(e.rdb.B().Xadd().Key(k.Stream(2)).Id("*").FieldValue().FieldValue("d", "{}").Build())
	}
	e.redisDo(e.rdb.B().XgroupCreate().Key(k.Stream(2)).Group(reportConsumerGroup).Id("0").Build())
	read, err := e.rdb.Do(e.ctx, e.rdb.B().Xreadgroup().Group(reportConsumerGroup, "c1").Count(3).
		Streams().Key(k.Stream(2)).Id(">").Build()).AsXRead()
	require.NoError(t, err)
	entries := read[k.Stream(2)]
	require.Len(t, entries, 3)
	e.redisDo(e.rdb.B().Xack().Key(k.Stream(2)).Group(reportConsumerGroup).Id(entries[0].ID, entries[1].ID).Build())

	pending, err := e.svc.streamsPending(e.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), pending) // 1 unacknowledged + 1 undelivered; acknowledged history is ignored
}

func TestStreamsPendingUnknownLag(t *testing.T) {
	e := newEnv(t, false)
	k := e.keys
	ids := make([]string, 0, 3)
	for range 3 {
		id, err := e.rdb.Do(e.ctx, e.rdb.B().Xadd().Key(k.Stream(0)).Id("*").FieldValue().FieldValue("d", "{}").Build()).ToString()
		require.NoError(t, err)
		ids = append(ids, id)
	}
	// A deleted entry after the group position makes the lag unknown: the
	// whole stream counts as backlog.
	e.redisDo(e.rdb.B().XgroupCreate().Key(k.Stream(0)).Group(reportConsumerGroup).Id("0").Build())
	e.redisDo(e.rdb.B().Xdel().Key(k.Stream(0)).Id(ids[1]).Build())
	// Shard 1 only has a foreign consumer group: every entry is unprocessed.
	e.redisDo(e.rdb.B().Xadd().Key(k.Stream(1)).Id("*").FieldValue().FieldValue("d", "{}").Build())
	e.redisDo(e.rdb.B().XgroupCreate().Key(k.Stream(1)).Group("other").Id("$").Build())

	pending, err := e.svc.streamsPending(e.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2+1), pending)
}

func TestOverviewConcurrent(t *testing.T) {
	e := newEnv(t, false)
	seedOverview(e)
	want, err := e.svc.Overview(e.ctx, allScope(), 5*time.Minute)
	require.NoError(t, err)

	const workers = 8
	errs := make(chan error, workers)
	results := make(chan Overview, workers)
	for range workers {
		go func() {
			ov, err := e.svc.Overview(e.ctx, allScope(), 5*time.Minute)
			errs <- err
			results <- ov
		}()
	}
	for range workers {
		require.NoError(t, <-errs)
		require.Equal(t, want, <-results)
	}
}
