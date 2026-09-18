package analytics

import (
	"context"
	"fmt"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
)

// maxOverviewWindow bounds the window accepted by Overview.
const maxOverviewWindow = 24 * time.Hour

// Acquire results that count as failures in the acquire failure ratio.
var acquireFailureResults = []string{"exhausted", "circuit_open", "site_paused", "no_proxy", "overloaded"}

// SiteOverview summarizes the state and throughput of one site. Totals use
// the same type with empty site fields.
type SiteOverview struct {
	SiteID      string
	Site        string
	DisplayName string
	Paused      bool
	// IdentitiesByState counts identities per lifecycle state (states without
	// identities are absent).
	IdentitiesByState map[string]int64
	// AvailableIdentities is the number of identities leasable now: per client
	// the largest ready count over its endpoint groups, summed over clients.
	AvailableIdentities int64
	// ProxiesByState counts the namespace proxies per state.
	ProxiesByState      map[string]int64
	AcquireQPS          float64
	ReportQPS           float64
	SuccessRatio        float64
	RiskRatio           float64
	UnknownRatio        float64
	ClientErrorRatio    float64
	AcquireFailureRatio float64
	OpenBreakers        int
	HalfOpenBreakers    int
	// LowWatermarkWarnings lists endpoint groups below their low watermark,
	// ordered by client and name (empty in totals).
	LowWatermarkWarnings []LowWatermarkWarning
}

// LowWatermarkWarning flags an endpoint group with too few available identities.
type LowWatermarkWarning struct {
	Client          string
	EndpointGroup   string
	EndpointGroupID string
	Available       int64
	LowWatermark    int
}

// Overview is the namespace overview.
type Overview struct {
	// Sites are ordered by site name.
	Sites  []SiteOverview
	Totals SiteOverview
	// StreamsPending is the report backlog of all shards (global).
	StreamsPending int64
	// Window is the effective window of rates and ratios.
	Window      time.Duration
	GeneratedAt time.Time
}

// siteCounters holds the raw counts behind the rates and ratios of a site.
type siteCounters struct {
	acquires map[string]int64
	outcomes map[string]int64
}

func newSiteCounters() *siteCounters {
	return &siteCounters{acquires: map[string]int64{}, outcomes: map[string]int64{}}
}

func (c *siteCounters) add(o *siteCounters) {
	for k, v := range o.acquires {
		c.acquires[k] += v
	}
	for k, v := range o.outcomes {
		c.outcomes[k] += v
	}
}

// fill computes the rates and ratios of ov from the counters.
func (c *siteCounters) fill(ov *SiteOverview, window time.Duration) {
	var acquires, failures, reports, risk int64
	for result, n := range c.acquires {
		acquires += n
		for _, f := range acquireFailureResults {
			if result == f {
				failures += n
			}
		}
	}
	for outcome, n := range c.outcomes {
		reports += n
		if policy.IsRiskOutcome(outcome) {
			risk += n
		}
	}
	seconds := window.Seconds()
	ov.AcquireQPS = float64(acquires) / seconds
	ov.ReportQPS = float64(reports) / seconds
	ov.AcquireFailureRatio = ratio(failures, acquires)
	ov.SuccessRatio = ratio(c.outcomes[policy.OutcomeSuccess], reports)
	ov.RiskRatio = ratio(risk, reports)
	ov.UnknownRatio = ratio(c.outcomes[policy.OutcomeUnknown], reports)
	ov.ClientErrorRatio = ratio(c.outcomes[policy.OutcomeClientError], reports)
}

// pgOverview is the PostgreSQL part of an overview.
type pgOverview struct {
	identities map[string]map[string]int64 // site id -> state -> count
	proxies    map[string]int64
	counters   map[string]*siteCounters // site id
}

// Overview returns per-site health and throughput of the readable sites of
// the scope. Rates and ratios cover the complete minutes of the window before
// now (the current, partially flushed minute is excluded).
func (s *Service) Overview(ctx context.Context, scope Scope, window time.Duration) (Overview, error) {
	if window < time.Minute || window > maxOverviewWindow || window%time.Minute != 0 {
		return Overview{}, apperr.InvalidArgument("", "window must be a whole number of minutes between 1m and 24h")
	}
	rs, err := s.resolve(scope)
	if err != nil {
		return Overview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	now := s.now().UTC()
	to := minuteFloor(now)
	from := to.Add(-window)

	var (
		pg      pgOverview
		live    []siteLive
		pending int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		pg, err = s.overviewPG(gctx, rs, from, to)
		return err
	})
	g.Go(func() error {
		var err error
		live, err = s.readLive(gctx, rs.sites, now)
		return err
	})
	g.Go(func() error {
		var err error
		pending, err = s.streamsPending(gctx)
		return err
	})
	if err := g.Wait(); err != nil {
		return Overview{}, err
	}

	out := Overview{
		Sites:          make([]SiteOverview, 0, len(rs.sites)),
		StreamsPending: pending,
		Window:         window,
		GeneratedAt:    now,
		Totals: SiteOverview{
			IdentitiesByState: map[string]int64{},
			ProxiesByState:    copyCounts(pg.proxies),
		},
	}
	totals := newSiteCounters()
	for i, site := range rs.sites {
		ov := buildSiteOverview(site, pg, live[i])
		counters := pg.counters[site.ID]
		if counters == nil {
			counters = newSiteCounters()
		}
		counters.fill(&ov, window)
		totals.add(counters)
		for state, n := range ov.IdentitiesByState {
			out.Totals.IdentitiesByState[state] += n
		}
		out.Totals.AvailableIdentities += ov.AvailableIdentities
		out.Totals.OpenBreakers += ov.OpenBreakers
		out.Totals.HalfOpenBreakers += ov.HalfOpenBreakers
		out.Sites = append(out.Sites, ov)
	}
	totals.fill(&out.Totals, window)
	return out, nil
}

// buildSiteOverview combines the PostgreSQL and Redis data of one site.
func buildSiteOverview(site *catalog.Site, pg pgOverview, live siteLive) SiteOverview {
	ov := SiteOverview{
		SiteID:               site.ID,
		Site:                 site.Name,
		DisplayName:          site.DisplayName,
		Paused:               site.Paused,
		IdentitiesByState:    copyCounts(pg.identities[site.ID]),
		ProxiesByState:       copyCounts(pg.proxies),
		OpenBreakers:         live.open,
		HalfOpenBreakers:     live.halfOpen,
		LowWatermarkWarnings: []LowWatermarkWarning{},
	}
	best := map[string]int64{} // client -> largest ready count
	for _, g := range site.GroupsByID {
		ready := live.ready[g.Key]
		if ready > best[g.Client] {
			best[g.Client] = ready
		}
		if g.LowWatermark > 0 && ready < int64(g.LowWatermark) {
			ov.LowWatermarkWarnings = append(ov.LowWatermarkWarnings, LowWatermarkWarning{
				Client:          g.Client,
				EndpointGroup:   g.Name,
				EndpointGroupID: g.ID,
				Available:       ready,
				LowWatermark:    g.LowWatermark,
			})
		}
	}
	for _, n := range best {
		ov.AvailableIdentities += n
	}
	sort.Slice(ov.LowWatermarkWarnings, func(i, j int) bool {
		a, b := ov.LowWatermarkWarnings[i], ov.LowWatermarkWarnings[j]
		if a.Client != b.Client {
			return a.Client < b.Client
		}
		return a.EndpointGroup < b.EndpointGroup
	})
	return ov
}

// overviewPG runs the PostgreSQL queries of an overview.
func (s *Service) overviewPG(ctx context.Context, rs *resolvedScope, from, to time.Time) (pgOverview, error) {
	out := pgOverview{
		identities: map[string]map[string]int64{},
		proxies:    map[string]int64{},
		counters:   map[string]*siteCounters{},
	}
	proxies, err := s.q.AnalyticsProxyStateCounts(ctx, rs.ns.ID)
	if err != nil {
		return out, fmt.Errorf("count proxies: %w", err)
	}
	for _, r := range proxies {
		out.proxies[r.State] = r.Count
	}
	if len(rs.sites) == 0 {
		return out, nil
	}
	siteIDs := rs.siteIDs()
	identities, err := s.q.AnalyticsIdentityStateCounts(ctx, siteIDs)
	if err != nil {
		return out, fmt.Errorf("count identities: %w", err)
	}
	for _, r := range identities {
		m := out.identities[r.SiteID]
		if m == nil {
			m = map[string]int64{}
			out.identities[r.SiteID] = m
		}
		m[r.State] = r.Count
	}
	counter := func(siteID string) *siteCounters {
		c := out.counters[siteID]
		if c == nil {
			c = newSiteCounters()
			out.counters[siteID] = c
		}
		return c
	}
	acquires, err := s.q.AnalyticsAcquireTotalsBySite(ctx, analyticsdb.AnalyticsAcquireTotalsBySiteParams{
		NamespaceID: rs.ns.ID, FromBucket: from, ToBucket: to, SiteIds: siteIDs,
	})
	if err != nil {
		return out, fmt.Errorf("sum acquire statistics: %w", err)
	}
	for _, r := range acquires {
		counter(r.SiteID).acquires[r.Result] += r.Count
	}
	outcomes, err := s.q.AnalyticsOutcomeTotalsBySite(ctx, analyticsdb.AnalyticsOutcomeTotalsBySiteParams{
		NamespaceID: rs.ns.ID, FromBucket: from, ToBucket: to, SiteIds: siteIDs,
	})
	if err != nil {
		return out, fmt.Errorf("sum outcome statistics: %w", err)
	}
	for _, r := range outcomes {
		counter(r.SiteID).outcomes[r.Outcome] += r.Count
	}
	return out, nil
}

// copyCounts returns a copy of m (never nil).
func copyCounts(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
