package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func values(s Series) []float64 {
	out := make([]float64, len(s.Points))
	for i, p := range s.Points {
		out[i] = p.Value
	}
	return out
}

func requireValues(t *testing.T, want []float64, s Series) {
	t.Helper()
	got := values(s)
	require.Len(t, got, len(want), "series %s", s.Name)
	for i := range want {
		require.InDelta(t, want[i], got[i], 1e-9, "series %s point %d", s.Name, i)
	}
}

func TestTimeSeries(t *testing.T) {
	e := newEnv(t, false)
	t0 := alignDown(e.now.Add(-30*time.Minute), 5*time.Minute)
	e.acquireStat(t0.Add(time.Minute), siteA, egSearch, "ok", 30)
	e.acquireStat(t0.Add(2*time.Minute), siteA, egSearch, "exhausted", 6)
	e.acquireStat(t0.Add(11*time.Minute), siteA, egAppFeed, "ok", 60)
	e.acquireStat(t0.Add(11*time.Minute), siteB, egForumSearch, "ok", 600)
	e.acquireStat(t0.Add(-time.Minute), siteA, egSearch, "ok", 1000)   // before the range
	e.acquireStat(t0.Add(15*time.Minute), siteA, egSearch, "ok", 1000) // range end is exclusive
	e.outcomeStat(t0.Add(time.Minute), siteA, egSearch, "pxy_1", "success", 40, 4000)
	e.outcomeStat(t0.Add(3*time.Minute), siteA, egSearch, "", "rate_limited", 10, 1000)
	e.outcomeStat(t0.Add(12*time.Minute), siteA, egAppFeed, "", "success", 20, 200)
	e.outcomeStat(t0.Add(12*time.Minute), siteA, "", "", "captcha", 5, 0)
	e.outcomeStat(t0.Add(time.Minute), siteB, egForumSearch, "", "success", 100, 100)

	rng := TimeRange{Start: ptr(t0), End: ptr(t0.Add(15 * time.Minute))}
	query := func(t *testing.T, scope Scope, q TimeSeriesQuery) TimeSeries {
		t.Helper()
		q.Range, q.Step = rng, 5*time.Minute
		ts, err := e.svc.TimeSeries(e.ctx, scope, q)
		require.NoError(t, err)
		require.Equal(t, 5*time.Minute, ts.Step)
		require.Equal(t, q.Metric, ts.Metric)
		for _, s := range ts.Series {
			require.Len(t, s.Points, 3)
			for i, p := range s.Points {
				require.True(t, t0.Add(time.Duration(i)*5*time.Minute).Equal(p.TS))
			}
		}
		return ts
	}

	t.Run("acquire rate over all sites", func(t *testing.T) {
		ts := query(t, allScope(), TimeSeriesQuery{Metric: MetricAcquireRate})
		require.Len(t, ts.Series, 1)
		require.Equal(t, MetricAcquireRate, ts.Series[0].Name)
		requireValues(t, []float64{36.0 / 300, 0, 660.0 / 300}, ts.Series[0])
	})
	t.Run("acquire results", func(t *testing.T) {
		ts := query(t, allScope(), TimeSeriesQuery{Metric: MetricAcquireResults})
		require.Len(t, ts.Series, 2)
		require.Equal(t, "exhausted", ts.Series[0].Name)
		require.Equal(t, map[string]string{"result": "exhausted"}, ts.Series[0].Labels)
		requireValues(t, []float64{6, 0, 0}, ts.Series[0])
		requireValues(t, []float64{30, 0, 660}, ts.Series[1])
	})
	t.Run("outcomes of a site", func(t *testing.T) {
		ts := query(t, allScope(), TimeSeriesQuery{Metric: MetricOutcomes, SiteID: siteA})
		require.Len(t, ts.Series, 3)
		require.Equal(t, []string{"captcha", "rate_limited", "success"},
			[]string{ts.Series[0].Name, ts.Series[1].Name, ts.Series[2].Name})
		requireValues(t, []float64{0, 0, 5}, ts.Series[0])
		requireValues(t, []float64{10, 0, 0}, ts.Series[1])
		requireValues(t, []float64{40, 0, 20}, ts.Series[2])
	})
	t.Run("ratios and latency", func(t *testing.T) {
		requireValues(t, []float64{40.0 / 50, 0, 20.0 / 25},
			query(t, allScope(), TimeSeriesQuery{Metric: MetricSuccessRatio, SiteID: siteA}).Series[0])
		requireValues(t, []float64{10.0 / 50, 0, 5.0 / 25},
			query(t, allScope(), TimeSeriesQuery{Metric: MetricRiskRatio, SiteID: siteA}).Series[0])
		requireValues(t, []float64{5000.0 / 50, 0, 200.0 / 25},
			query(t, allScope(), TimeSeriesQuery{Metric: MetricLatencyAvg, SiteID: siteA}).Series[0])
	})
	t.Run("client filter", func(t *testing.T) {
		ts := query(t, allScope(), TimeSeriesQuery{Metric: MetricAcquireRate, SiteID: siteA, Client: "app"})
		requireValues(t, []float64{0, 0, 60.0 / 300}, ts.Series[0])
	})
	t.Run("endpoint group filter", func(t *testing.T) {
		ts := query(t, allScope(), TimeSeriesQuery{Metric: MetricAcquireResults, EndpointGroupID: egSearch})
		require.Len(t, ts.Series, 2)
		requireValues(t, []float64{30, 0, 0}, ts.Series[1])
		ts = query(t, allScope(), TimeSeriesQuery{Metric: MetricAcquireResults, SiteID: siteA, Client: "web", EndpointGroupID: egSearch})
		require.Len(t, ts.Series, 2)
	})
	t.Run("site restricted scope", func(t *testing.T) {
		ts := query(t, Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}, TimeSeriesQuery{Metric: MetricAcquireRate})
		requireValues(t, []float64{0, 0, 600.0 / 300}, ts.Series[0])
		ts = query(t, Scope{NamespaceID: namespaceID}, TimeSeriesQuery{Metric: MetricOutcomes})
		require.Empty(t, ts.Series)
		ts = query(t, Scope{NamespaceID: namespaceID}, TimeSeriesQuery{Metric: MetricSuccessRatio})
		requireValues(t, []float64{0, 0, 0}, ts.Series[0])
	})
	t.Run("default range and automatic step fill empty buckets", func(t *testing.T) {
		ts, err := e.svc.TimeSeries(e.ctx, allScope(), TimeSeriesQuery{Metric: MetricAcquireRate})
		require.NoError(t, err)
		require.Equal(t, time.Minute, ts.Step)
		require.Len(t, ts.Series[0].Points, 61)
		require.True(t, minuteFloor(e.now.Add(-time.Hour)).Equal(ts.Series[0].Points[0].TS))
		var sum float64
		for _, p := range ts.Series[0].Points {
			sum += p.Value * 60
		}
		require.InDelta(t, 36+660+2000.0, sum, 1e-6)
	})
}

func TestTimeSeriesErrors(t *testing.T) {
	e := newEnv(t, false)
	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name   string
		scope  Scope
		q      TimeSeriesQuery
		reason apperr.Reason
	}{
		{"unknown metric", allScope(), TimeSeriesQuery{Metric: "nope"}, apperr.ReasonInvalidArgument},
		{"client without site", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, Client: "web"}, apperr.ReasonInvalidArgument},
		{"unknown client", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, SiteID: siteB, Client: "app"}, apperr.ReasonClientUnknown},
		{"unknown site", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, SiteID: "sit_missing"}, apperr.ReasonSiteUnknown},
		{"unreadable site", restricted, TimeSeriesQuery{Metric: MetricOutcomes, SiteID: siteA}, apperr.ReasonPermissionDenied},
		{"unknown group", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, EndpointGroupID: "eg_missing"}, apperr.ReasonEndpointGroupUnknown},
		{"unreadable group", restricted, TimeSeriesQuery{Metric: MetricOutcomes, EndpointGroupID: egSearch}, apperr.ReasonPermissionDenied},
		{"group of another site", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, SiteID: siteB, EndpointGroupID: egSearch}, apperr.ReasonEndpointGroupUnknown},
		{"group of another client", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, SiteID: siteA, Client: "app", EndpointGroupID: egSearch}, apperr.ReasonEndpointGroupUnknown},
		{"range too long", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, Range: TimeRange{Start: ptr(e.now.Add(-32 * 24 * time.Hour))}}, apperr.ReasonInvalidArgument},
		{"range reversed", allScope(), TimeSeriesQuery{Metric: MetricOutcomes, Range: TimeRange{Start: ptr(e.now), End: ptr(e.now.Add(-time.Minute))}}, apperr.ReasonInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.TimeSeries(e.ctx, tc.scope, tc.q)
			require.Error(t, err)
			require.Equal(t, tc.reason, apperr.ReasonOf(err), "error: %v", err)
		})
	}
}

func TestChooseStep(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 7, 31, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name      string
		length    time.Duration
		requested time.Duration
		want      time.Duration
	}{
		{"one hour auto", time.Hour, 0, time.Minute},
		{"twelve hours auto", 12 * time.Hour, 0, 5 * time.Minute},
		{"one day auto", day, 0, 5 * time.Minute},
		{"seven days auto", 7 * day, 0, 15 * time.Minute},
		{"thirty one days auto", 31 * day, 0, 6 * time.Hour},
		{"requested step kept", day, time.Hour, time.Hour},
		{"too small step raised", 7 * day, time.Minute, 15 * time.Minute},
		{"daily step", 31 * day, 24 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseStep(base, base.Add(tc.length), tc.requested)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, bucketCount(base, base.Add(tc.length), got), int64(MaxSeriesPoints))
		})
	}
	_, err := chooseStep(base, base.Add(2000*day), 0)
	require.Error(t, err)
}

func TestStepAndWindowNames(t *testing.T) {
	for _, name := range []string{"1m", "5m", "15m", "1h", "6h", "1d"} {
		d, err := ParseStep(name)
		require.NoError(t, err)
		require.Equal(t, name, StepName(d))
	}
	d, err := ParseStep("")
	require.NoError(t, err)
	require.Zero(t, d)
	_, err = ParseStep("2m")
	require.Error(t, err)
	require.Equal(t, "2m0s", StepName(2*time.Minute))

	for _, name := range []string{"1m", "5m", "15m", "1h"} {
		d, err := ParseWindow(name)
		require.NoError(t, err)
		require.Equal(t, name, WindowName(d))
	}
	d, err = ParseWindow("")
	require.NoError(t, err)
	require.Equal(t, DefaultWindow, d)
	_, err = ParseWindow("6h")
	require.Error(t, err)
	require.Equal(t, "2m0s", WindowName(2*time.Minute))
}

func TestAlignment(t *testing.T) {
	ts := time.Date(2026, 9, 1, 10, 7, 31, 500, time.UTC)
	require.Equal(t, time.Date(2026, 9, 1, 10, 5, 0, 0, time.UTC), alignDown(ts, 5*time.Minute))
	require.Equal(t, time.Date(2026, 9, 1, 10, 10, 0, 0, time.UTC), alignUp(ts, 5*time.Minute))
	aligned := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, aligned, alignUp(aligned, 6*time.Hour))
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), alignDown(ts, 24*time.Hour))
	neg := time.Unix(-90, 0)
	require.Equal(t, time.Unix(-120, 0).UTC(), alignDown(neg, time.Minute))
	require.Equal(t, "31 days", formatRangeLimit(MaxAggregateRange))
	require.Equal(t, "1 day", formatRangeLimit(24*time.Hour))
	require.Equal(t, "1h30m0s", formatRangeLimit(90*time.Minute))
}
