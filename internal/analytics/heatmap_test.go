package analytics

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

type cellKey struct{ identity, group string }

func cellMap(h Heatmap) map[cellKey]HeatmapCell {
	out := map[cellKey]HeatmapCell{}
	for _, c := range h.Cells {
		out[cellKey{h.Rows[c.Row].IdentityID, h.Columns[c.Col].EndpointGroup}] = c
	}
	return out
}

func TestHeatmap(t *testing.T) {
	e := newEnv(t, false)
	nowMs := e.now.UnixMilli()
	k := e.keys
	webDefault := e.siteA.Groups[groupKey("web", "_default")]

	acc := e.account("acc_1", siteA, "alice")
	h1 := e.identity("idt_h1", siteA, typeA, "web", "active", "", "acc_1", nil)
	h2 := e.identity("idt_h2", siteA, typeA, "web", "active", "eu", "", nil)
	h3 := e.identity("idt_h3", siteA, typeA, "web", "banned", "", "", ptr(e.now.Add(time.Hour)))
	e.identity("idt_h4", siteA, typeA, "web", "banned", "", "", nil)
	e.identity("idt_h5", siteA, typeA, "web", "pending", "", "", nil)
	e.identity("idt_h6", siteA, typeA, "web", "retired", "", "", nil)
	e.identity("idt_h7", siteA, typeA, "app", "active", "", "", nil)
	h8 := e.identity("idt_h8", siteA, typeA, "web", "active", "", "", nil)
	tau := policy.DefaultHealthTau.Milliseconds()

	e.redisDo(
		// h1: endpoint cooldown on search, account cooldown everywhere.
		e.rdb.B().Hset().Key(k.Health(siteAKey, egSearchKey)).FieldValue().
			FieldValue(itoa(h1), fmt.Sprintf("50.00|%d|12|1|0|%d|0|0", nowMs, nowMs+60_000)).
			FieldValue(itoa(h8), fmt.Sprintf("90.00|%d|30|0|0|0|0|0", nowMs-tau)).Build(),
		e.rdb.B().Hset().Key(k.Account(siteAKey, acc)).FieldValue().FieldValue("st", "active").FieldValue("cd", itoa(nowMs+120_000)).Build(),
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, egSearchKey)).ScoreMember().
			ScoreMember(float64(nowMs+120_000), itoa(h1)).ScoreMember(float64(nowMs+30_000), itoa(h2)).
			ScoreMember(float64(nowMs-5), itoa(h8)).Build(),
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, egFeedKey)).ScoreMember().
			ScoreMember(float64(nowMs+120_000), itoa(h1)).ScoreMember(float64(nowMs+30_000), itoa(h2)).
			ScoreMember(float64(nowMs-5), itoa(h8)).Build(),
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, webDefault.Key)).ScoreMember().
			ScoreMember(float64(nowMs+120_000), itoa(h1)).ScoreMember(float64(nowMs+30_000), itoa(h2)).
			ScoreMember(float64(nowMs-5), itoa(h8)).Build(),
		// h2: site cooldown; h3: temporary ban in Redis.
		e.rdb.B().Hset().Key(k.Identity(siteAKey, h2)).FieldValue().FieldValue("st", "active").FieldValue("scd", itoa(nowMs+30_000)).Build(),
		e.rdb.B().Hset().Key(k.Identity(siteAKey, h3)).FieldValue().FieldValue("st", "banned").FieldValue("bu", itoa(nowMs+3_600_000)).Build(),
	)

	hm, err := e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web"})
	require.NoError(t, err)
	require.Equal(t, int64(6), hm.Total)
	require.Empty(t, hm.NextAfterID)
	require.Equal(t, e.now, hm.GeneratedAt)
	require.Equal(t, []HeatmapColumn{
		{EndpointGroupID: webDefault.ID, EndpointGroup: "_default"},
		{EndpointGroupID: egFeed, EndpointGroup: "feed"},
		{EndpointGroupID: egSearch, EndpointGroup: "search"},
	}, hm.Columns)
	require.Equal(t, []HeatmapRow{
		{IdentityID: "idt_h1", Label: "alice", State: "active"},
		{IdentityID: "idt_h2", Label: "eu", State: "active"},
		{IdentityID: "idt_h3", Label: "idt_h3", State: "banned"},
		{IdentityID: "idt_h4", Label: "idt_h4", State: "banned"},
		{IdentityID: "idt_h5", Label: "idt_h5", State: "pending"},
		{IdentityID: "idt_h8", Label: "idt_h8", State: "active"},
	}, hm.Rows)

	cells := cellMap(hm)
	for _, g := range []string{"_default", "feed", "search"} {
		c := cells[cellKey{"idt_h1", g}]
		require.Equal(t, int64(120_000), c.CooldownRemainingMs, g)
		require.False(t, c.Available, g)
		c = cells[cellKey{"idt_h2", g}]
		require.Equal(t, int64(30_000), c.CooldownRemainingMs, g)
		require.InDelta(t, policy.DefaultHealthBaseline, c.Score, 1e-9)
		require.False(t, c.Available, g)
		c = cells[cellKey{"idt_h3", g}]
		require.Equal(t, int64(3_600_000), c.CooldownRemainingMs, g)
		require.False(t, c.Available)
		c = cells[cellKey{"idt_h4", g}]
		require.Equal(t, int64(math.MaxInt64), c.CooldownRemainingMs, g)
		// Pending identity missing from the ready queues is reported unavailable.
		c, ok := cells[cellKey{"idt_h5", g}]
		require.True(t, ok, g)
		require.False(t, c.Available)
		require.Zero(t, c.CooldownRemainingMs)
	}
	require.InDelta(t, 50.0, cells[cellKey{"idt_h1", "search"}].Score, 1e-9)
	// h8: available with baseline state in _default/feed (omitted), decayed score on search.
	_, ok := cells[cellKey{"idt_h8", "feed"}]
	require.False(t, ok)
	_, ok = cells[cellKey{"idt_h8", "_default"}]
	require.False(t, ok)
	c := cells[cellKey{"idt_h8", "search"}]
	require.True(t, c.Available)
	require.InDelta(t, policy.DefaultHealthBaseline+20*math.Exp(-1), c.Score, 1e-6)
	require.Len(t, hm.Cells, 3*5+1)

	// Pagination and state filter.
	page, err := e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web", Limit: 4})
	require.NoError(t, err)
	require.Len(t, page.Rows, 4)
	require.Equal(t, "idt_h4", page.NextAfterID)
	page, err = e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web", Limit: 4, AfterID: page.NextAfterID})
	require.NoError(t, err)
	require.Len(t, page.Rows, 2)
	require.Equal(t, "idt_h8", page.Rows[1].IdentityID)
	require.Empty(t, page.NextAfterID)
	require.Equal(t, int64(6), page.Total)
	for _, cell := range page.Cells {
		require.Less(t, cell.Row, 2)
	}

	banned, err := e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web", States: []string{"banned", "retired"}, Limit: 1000})
	require.NoError(t, err)
	require.Equal(t, int64(3), banned.Total)
	require.Len(t, banned.Rows, 3)

	empty, err := e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteB, Client: "web"})
	require.NoError(t, err)
	require.Empty(t, empty.Rows)
	require.Empty(t, empty.Cells)
	require.Len(t, empty.Columns, 2)
}

func TestHeatmapErrors(t *testing.T) {
	e := newEnv(t, false)
	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name   string
		scope  Scope
		q      HeatmapQuery
		reason apperr.Reason
	}{
		{"unknown client", allScope(), HeatmapQuery{SiteID: siteB, Client: "app"}, apperr.ReasonClientUnknown},
		{"unknown state", allScope(), HeatmapQuery{SiteID: siteA, Client: "web", States: []string{"zombie"}}, apperr.ReasonInvalidArgument},
		{"unreadable site", restricted, HeatmapQuery{SiteID: siteA, Client: "web"}, apperr.ReasonPermissionDenied},
		{"unknown site", allScope(), HeatmapQuery{SiteID: "sit_missing", Client: "web"}, apperr.ReasonSiteUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Heatmap(e.ctx, tc.scope, tc.q)
			require.Error(t, err)
			require.Equal(t, tc.reason, apperr.ReasonOf(err), "error: %v", err)
		})
	}
}

func TestParseHealthAndHelpers(t *testing.T) {
	h := parseHealth("", 70, 1000)
	require.Equal(t, healthEntry{score: 70, scoreTS: 1000}, h)
	h = parseHealth("12.5|500|3|1|0|9000|0|0", 70, 1000)
	require.Equal(t, healthEntry{score: 12.5, scoreTS: 500, cooldown: 9000}, h)
	h = parseHealth("bad|x|1|1|1|12.9", 70, 1000)
	require.Equal(t, healthEntry{score: 70, scoreTS: 1000, cooldown: 12}, h)

	require.InDelta(t, 80.0, decayScore(80, 2000, 1000, 70, time.Hour), 1e-9)
	require.InDelta(t, 80.0, decayScore(80, 0, 1000, 70, 0), 1e-9)
	require.Equal(t, int64(5), parseInt("nan", 5))
	require.Equal(t, int64(5), parseInt("1e300", 5))
	require.InDelta(t, 3.0, parseFloat("inf", 3), 1e-9)
	require.Zero(t, ratio(1, 0))

	require.Equal(t, policy.DefaultHealthBaseline, healthOf(nil).Baseline)

	var ban identityHot
	ban.setBan(permanentBanMillis)
	require.True(t, ban.banPermanent)
	ban = identityHot{}
	ban.setBan(500)
	ban.setBan(300)
	require.Equal(t, int64(500), ban.banUntil)
}
