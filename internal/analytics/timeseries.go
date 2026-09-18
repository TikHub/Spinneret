package analytics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/policy"
)

// Time series metrics.
const (
	MetricAcquireRate    = "acquire_rate"
	MetricAcquireResults = "acquire_results"
	MetricOutcomes       = "outcomes"
	MetricSuccessRatio   = "success_ratio"
	MetricRiskRatio      = "risk_ratio"
	MetricLatencyAvg     = "latency_avg"
)

// TimeSeriesQuery selects a metric, filters and a range.
type TimeSeriesQuery struct {
	Metric string
	// SiteID restricts the series to one readable site ("" = every readable site).
	SiteID string
	// Client restricts the series to the endpoint groups of one client (requires SiteID).
	Client string
	// EndpointGroupID restricts the series to one endpoint group.
	EndpointGroupID string
	Range           TimeRange
	// Step is the bucket size; 0 selects one automatically. A step that would
	// yield more than MaxSeriesPoints buckets is raised to the next supported one.
	Step time.Duration
}

// Point is one bucket of a series.
type Point struct {
	TS    time.Time
	Value float64
}

// Series is one time series.
type Series struct {
	Name   string
	Labels map[string]string
	// Points cover every bucket of the range in time order; empty buckets are 0.
	Points []Point
}

// TimeSeries is the result of a time series query.
type TimeSeries struct {
	Metric string
	// Step is the effective bucket size.
	Step   time.Duration
	Series []Series
}

// seriesFilter is the resolved site / endpoint group filter of a query.
type seriesFilter struct {
	allSites bool
	siteIDs  []string
	// groupIDs restricts the endpoint groups; empty disables the filter.
	groupIDs []string
	// none is true when the filter cannot match any row (a client without
	// endpoint groups): an empty groupIDs must then not widen the query.
	none bool
}

// TimeSeries returns a metric over time from the minute aggregates.
func (s *Service) TimeSeries(ctx context.Context, scope Scope, q TimeSeriesQuery) (TimeSeries, error) {
	if !validMetric(q.Metric) {
		return TimeSeries{}, apperr.InvalidArgument("", "unsupported metric %q", q.Metric)
	}
	rs, err := s.resolve(scope)
	if err != nil {
		return TimeSeries{}, err
	}
	filter, err := resolveSeriesFilter(rs, q)
	if err != nil {
		return TimeSeries{}, err
	}
	start, end, err := q.Range.resolve(s.now(), DefaultTimeSeriesRange, MaxAggregateRange)
	if err != nil {
		return TimeSeries{}, err
	}
	step, err := chooseStep(start, end, q.Step)
	if err != nil {
		return TimeSeries{}, err
	}
	from, to := alignDown(start, step), alignUp(end, step)
	buckets := int(to.Sub(from) / step)

	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	out := TimeSeries{Metric: q.Metric, Step: step}
	if filter.none {
		out.Series = emptySeries(q.Metric, from, step, buckets)
		return out, nil
	}
	switch q.Metric {
	case MetricAcquireRate, MetricAcquireResults:
		rows, err := s.q.AnalyticsAcquireSeries(ctx, analyticsdb.AnalyticsAcquireSeriesParams{
			StepSeconds: step.Seconds(), NamespaceID: rs.ns.ID, FromBucket: from, ToBucket: to,
			AllSites: filter.allSites, SiteIds: filter.siteIDs, EndpointGroupIds: filter.groupIDs,
		})
		if err != nil {
			return TimeSeries{}, fmt.Errorf("query acquire series: %w", err)
		}
		out.Series = acquireSeries(q.Metric, rows, from, step, buckets)
	default:
		rows, err := s.q.AnalyticsOutcomeSeries(ctx, analyticsdb.AnalyticsOutcomeSeriesParams{
			StepSeconds: step.Seconds(), NamespaceID: rs.ns.ID, FromBucket: from, ToBucket: to,
			AllSites: filter.allSites, SiteIds: filter.siteIDs, EndpointGroupIds: filter.groupIDs,
		})
		if err != nil {
			return TimeSeries{}, fmt.Errorf("query outcome series: %w", err)
		}
		out.Series = outcomeSeries(q.Metric, rows, from, step, buckets)
	}
	return out, nil
}

func validMetric(m string) bool {
	switch m {
	case MetricAcquireRate, MetricAcquireResults, MetricOutcomes, MetricSuccessRatio, MetricRiskRatio, MetricLatencyAvg:
		return true
	default:
		return false
	}
}

// resolveSeriesFilter checks the site, client and endpoint group filters
// against the scope and turns them into SQL filter arguments.
func resolveSeriesFilter(rs *resolvedScope, q TimeSeriesQuery) (seriesFilter, error) {
	f := seriesFilter{allSites: rs.allSites, siteIDs: rs.siteIDs(), groupIDs: []string{}}
	if q.Client != "" && q.SiteID == "" {
		return f, apperr.InvalidArgument("", "client requires a site")
	}
	if q.SiteID != "" {
		site, err := rs.site(q.SiteID)
		if err != nil {
			return f, err
		}
		f.allSites, f.siteIDs = false, []string{site.ID}
		if q.Client != "" {
			if !site.HasClient(q.Client) {
				return f, apperr.InvalidArgument(apperr.ReasonClientUnknown, "site %q has no client %q", site.Name, q.Client)
			}
			for _, g := range site.GroupsOfClient(q.Client) {
				f.groupIDs = append(f.groupIDs, g.ID)
			}
			sort.Strings(f.groupIDs)
			f.none = len(f.groupIDs) == 0
		}
	}
	if q.EndpointGroupID != "" {
		g, site, err := rs.group(q.EndpointGroupID)
		if err != nil {
			return f, err
		}
		if q.SiteID != "" && site.ID != q.SiteID {
			return f, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group does not belong to the site")
		}
		if q.Client != "" && g.Client != q.Client {
			return f, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group does not belong to the client")
		}
		f.allSites, f.siteIDs, f.groupIDs, f.none = false, []string{site.ID}, []string{g.ID}, false
	}
	return f, nil
}

// emptySeries returns the series of a metric over buckets without data:
// metrics with one series per result or outcome have no series, the others
// one zero-filled series.
func emptySeries(metric string, from time.Time, step time.Duration, buckets int) []Series {
	if metric == MetricAcquireResults || metric == MetricOutcomes {
		return []Series{}
	}
	return []Series{{Name: metric, Labels: map[string]string{}, Points: newPoints(from, step, buckets)}}
}

// bucketIndex returns the index of ts among buckets starting at from, or -1.
func bucketIndex(ts, from time.Time, step time.Duration, buckets int) int {
	d := ts.Sub(from)
	if d < 0 {
		return -1
	}
	i := int(d / step)
	if i >= buckets {
		return -1
	}
	return i
}

// newPoints returns zero-valued points for every bucket.
func newPoints(from time.Time, step time.Duration, buckets int) []Point {
	points := make([]Point, buckets)
	for i := range points {
		points[i].TS = from.Add(time.Duration(i) * step)
	}
	return points
}

// acquireSeries builds the acquire_rate or acquire_results series.
func acquireSeries(metric string, rows []analyticsdb.AnalyticsAcquireSeriesRow, from time.Time, step time.Duration, buckets int) []Series {
	if metric == MetricAcquireRate {
		points := newPoints(from, step, buckets)
		for _, r := range rows {
			if i := bucketIndex(r.Ts, from, step, buckets); i >= 0 {
				points[i].Value += float64(r.Count)
			}
		}
		for i := range points {
			points[i].Value /= step.Seconds()
		}
		return []Series{{Name: MetricAcquireRate, Labels: map[string]string{}, Points: points}}
	}
	byResult := map[string][]Point{}
	for _, r := range rows {
		i := bucketIndex(r.Ts, from, step, buckets)
		if i < 0 {
			continue
		}
		points, ok := byResult[r.Result]
		if !ok {
			points = newPoints(from, step, buckets)
			byResult[r.Result] = points
		}
		points[i].Value += float64(r.Count)
	}
	return namedSeries(byResult, "result")
}

// outcomeSeries builds the outcome based series.
func outcomeSeries(metric string, rows []analyticsdb.AnalyticsOutcomeSeriesRow, from time.Time, step time.Duration, buckets int) []Series {
	if metric == MetricOutcomes {
		byOutcome := map[string][]Point{}
		for _, r := range rows {
			i := bucketIndex(r.Ts, from, step, buckets)
			if i < 0 {
				continue
			}
			points, ok := byOutcome[r.Outcome]
			if !ok {
				points = newPoints(from, step, buckets)
				byOutcome[r.Outcome] = points
			}
			points[i].Value += float64(r.Count)
		}
		return namedSeries(byOutcome, "outcome")
	}
	total := make([]int64, buckets)
	part := make([]int64, buckets)
	for _, r := range rows {
		i := bucketIndex(r.Ts, from, step, buckets)
		if i < 0 {
			continue
		}
		total[i] += r.Count
		switch metric {
		case MetricSuccessRatio:
			if r.Outcome == policy.OutcomeSuccess {
				part[i] += r.Count
			}
		case MetricRiskRatio:
			if policy.IsRiskOutcome(r.Outcome) {
				part[i] += r.Count
			}
		case MetricLatencyAvg:
			part[i] += r.LatencyMsSum
		}
	}
	points := newPoints(from, step, buckets)
	for i := range points {
		points[i].Value = ratio(part[i], total[i])
	}
	return []Series{{Name: metric, Labels: map[string]string{}, Points: points}}
}

// namedSeries returns one series per key, ordered by name.
func namedSeries(byName map[string][]Point, label string) []Series {
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Series, 0, len(names))
	for _, name := range names {
		out = append(out, Series{Name: name, Labels: map[string]string{label: name}, Points: byName[name]})
	}
	return out
}
