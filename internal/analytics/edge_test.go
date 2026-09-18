package analytics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// A client declared by the site without endpoint groups (a broken snapshot)
// must yield empty series instead of silently dropping the group filter and
// returning the data of the whole site.
func TestTimeSeriesClientWithoutGroups(t *testing.T) {
	e := newEnv(t, false)
	e.siteA.Clients = append(e.siteA.Clients, "bot")
	cur := minuteFloor(e.now)
	e.acquireStat(cur.Add(-2*time.Minute), siteA, egSearch, "ok", 120)
	e.outcomeStat(cur.Add(-2*time.Minute), siteA, egSearch, "", "success", 60, 600)
	rng := TimeRange{Start: ptr(cur.Add(-5 * time.Minute)), End: ptr(cur)}

	for _, metric := range []string{MetricAcquireRate, MetricSuccessRatio, MetricRiskRatio, MetricLatencyAvg} {
		t.Run(metric, func(t *testing.T) {
			ts, err := e.svc.TimeSeries(e.ctx, allScope(), TimeSeriesQuery{Metric: metric, SiteID: siteA, Client: "bot", Range: rng, Step: time.Minute})
			require.NoError(t, err)
			require.Len(t, ts.Series, 1)
			require.Equal(t, metric, ts.Series[0].Name)
			requireValues(t, []float64{0, 0, 0, 0, 0}, ts.Series[0])
		})
	}
	for _, metric := range []string{MetricAcquireResults, MetricOutcomes} {
		t.Run(metric, func(t *testing.T) {
			ts, err := e.svc.TimeSeries(e.ctx, allScope(), TimeSeriesQuery{Metric: metric, SiteID: siteA, Client: "bot", Range: rng, Step: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, ts.Series)
			require.Empty(t, ts.Series)
		})
	}

	// The same site without the client filter still sees its data.
	ts, err := e.svc.TimeSeries(e.ctx, allScope(), TimeSeriesQuery{Metric: MetricAcquireRate, SiteID: siteA, Range: rng, Step: time.Minute})
	require.NoError(t, err)
	requireValues(t, []float64{0, 0, 0, 2, 0}, ts.Series[0])
}

// Ready queue scores that do not fit an int64 (e.g. +inf) must not make an
// identity look available.
func TestHeatmapExtremeReadyScores(t *testing.T) {
	e := newEnv(t, false)
	k := e.keys
	search := e.siteA.Groups[groupKey("web", "search")]
	far := e.identity("idt_far", siteA, typeA, "web", "active", "", "", nil)
	past := e.identity("idt_past", siteA, typeA, "web", "active", "", "", nil)
	e.redisDo(
		e.rdb.B().Zadd().Key(k.Ready(siteAKey, search.Key)).ScoreMember().
			ScoreMember(1e300, itoa(far)).ScoreMember(-1e300, itoa(past)).Build(),
		e.rdb.B().Arbitrary("ZADD").Keys(k.Ready(siteAKey, egFeedKey)).Args("+inf", itoa(far)).Build(),
	)

	hm, err := e.svc.Heatmap(e.ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web"})
	require.NoError(t, err)
	cells := cellMap(hm)
	for _, g := range []string{"search", "feed"} {
		c, ok := cells[cellKey{"idt_far", g}]
		require.True(t, ok, g)
		require.False(t, c.Available, g)
	}
	_, ok := cells[cellKey{"idt_past", "search"}]
	require.False(t, ok, "an available identity with default hot state is omitted")
}

// Backend failures (here a cancelled context) surface as errors, never as
// panics or partial results.
func TestBackendFailures(t *testing.T) {
	e := newEnv(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.svc.Overview(ctx, allScope(), 5*time.Minute)
	require.Error(t, err)
	_, err = e.svc.TimeSeries(ctx, allScope(), TimeSeriesQuery{Metric: MetricAcquireRate})
	require.Error(t, err)
	_, err = e.svc.TimeSeries(ctx, allScope(), TimeSeriesQuery{Metric: MetricOutcomes})
	require.Error(t, err)
	e.identity("idt_fail", siteA, typeA, "web", "active", "", "", nil)
	_, err = e.svc.Heatmap(ctx, allScope(), HeatmapQuery{SiteID: siteA, Client: "web"})
	require.Error(t, err)
	_, err = e.svc.RiskEvents(ctx, allScope(), RiskEventQuery{})
	require.Error(t, err)
	_, err = e.svc.RequestEvents(ctx, allScope(), RequestEventQuery{IncludeSummary: true})
	require.Error(t, err)
	_, err = e.svc.NodeStats(ctx, namespaceID, TimeRange{})
	require.Error(t, err)
	_, err = e.svc.streamsPending(ctx)
	require.Error(t, err)
	_, isAppErr := apperr.As(err)
	require.False(t, isAppErr, "backend failures are internal errors: %v", err)

	svc := New(e.pool, e.ch, e.rdb, e.keys, e.cat, nil, WithTimeouts(time.Nanosecond, time.Nanosecond), WithTimeouts(-1, 0))
	require.Equal(t, time.Nanosecond, svc.queryTimeout)
	require.Equal(t, time.Nanosecond, svc.chTimeout)
	_, err = svc.NodeStats(e.ctx, namespaceID, TimeRange{})
	require.Error(t, err)
}
