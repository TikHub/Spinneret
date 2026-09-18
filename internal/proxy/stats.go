package proxy

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
)

// DefaultStatsRange is the request statistics range when none is given.
const DefaultStatsRange = 24 * time.Hour

// riskOutcomes are the outcomes counted in ProviderStats.RiskRatio.
var riskOutcomes = []string{policy.OutcomeRateLimited, policy.OutcomeCaptcha, policy.OutcomeForbidden, policy.OutcomeBanned}

// StatsRequest selects the scope of provider statistics.
type StatsRequest struct {
	// Site restricts request statistics to one site (by name); empty means
	// every site the principal can read.
	Site string
	// Start and End bound the request statistics ([Start, End)); nil means
	// End = now and Start = End - 24h.
	Start, End *time.Time
}

// ProviderStats aggregates pool health and request outcomes of one provider.
type ProviderStats struct {
	Provider     string
	Proxies      int
	Active       int
	Dead         int
	Requests     int64
	SuccessRatio float64
	RiskRatio    float64
	AvgLatencyMs float64
}

// GetProviderStats aggregates proxies per provider (proxy:read): proxy counts
// by state from proxies and request outcomes from outcome_stats_minutely,
// ordered by requests descending.
func (s *Service) GetProviderStats(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req StatsRequest) ([]ProviderStats, error) {
	if err := requireNamespace(p, ns, authz.PermProxyRead); err != nil {
		return nil, err
	}
	start, end, err := statsRange(req, s.now())
	if err != nil {
		return nil, err
	}
	allSites, siteIDs, err := statsSites(p, ns, req.Site)
	if err != nil {
		return nil, err
	}
	counts, err := s.q.ProxyProviderCounts(ctx, ns.ID)
	if err != nil {
		return nil, fmt.Errorf("count proxies by provider: %w", err)
	}
	byProvider := make(map[string]*ProviderStats, len(counts))
	get := func(name string) *ProviderStats {
		st, ok := byProvider[name]
		if !ok {
			st = &ProviderStats{Provider: name}
			byProvider[name] = st
		}
		return st
	}
	for _, c := range counts {
		st := get(c.Provider)
		st.Proxies, st.Active, st.Dead = int(c.Proxies), int(c.Active), int(c.Dead)
	}
	if allSites || len(siteIDs) > 0 {
		outcomes, err := s.q.ProxyProviderOutcomes(ctx, proxydb.ProxyProviderOutcomesParams{
			RiskOutcomes: riskOutcomes, NamespaceID: ns.ID, RangeStart: start, RangeEnd: end,
			AllSites: allSites, SiteIds: nonNil(siteIDs),
		})
		if err != nil {
			return nil, fmt.Errorf("aggregate provider outcomes: %w", err)
		}
		for _, o := range outcomes {
			st := get(o.Provider)
			st.Requests = o.Requests
			if o.Requests > 0 {
				st.SuccessRatio = float64(o.Successes) / float64(o.Requests)
				st.RiskRatio = float64(o.Risks) / float64(o.Requests)
				st.AvgLatencyMs = float64(o.LatencyMsSum) / float64(o.Requests)
			}
		}
	}
	out := make([]ProviderStats, 0, len(byProvider))
	for _, st := range byProvider {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

// statsRange resolves the time range of provider statistics.
func statsRange(req StatsRequest, now time.Time) (time.Time, time.Time, error) {
	end := now
	if req.End != nil {
		end = req.End.UTC()
	}
	start := end.Add(-DefaultStatsRange)
	if req.Start != nil {
		start = req.Start.UTC()
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, apperr.InvalidArgument("", "time_range.start must be before time_range.end")
	}
	return start, end, nil
}

// statsSites resolves which sites' request statistics the principal may see.
func statsSites(p *authz.Principal, ns *catalog.Namespace, siteName string) (all bool, siteIDs []string, err error) {
	if siteName != "" {
		site, ok := ns.Sites[siteName]
		if !ok {
			return false, nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", truncate(siteName, 64))
		}
		if err := p.Require(authz.PermProxyRead, siteResource(ns, site)); err != nil {
			return false, nil, err
		}
		return false, []string{site.ID}, nil
	}
	sites := sortedSites(ns)
	for _, site := range sites {
		if p.Can(authz.PermProxyRead, siteResource(ns, site)) {
			siteIDs = append(siteIDs, site.ID)
		}
	}
	if len(siteIDs) == len(sites) && p.Can(authz.PermProxyRead, namespaceResource(ns)) {
		all, _ := p.SiteFilter(ns.TenantID, ns.ID, authz.PermProxyRead)
		if all {
			return true, nil, nil
		}
	}
	return false, siteIDs, nil
}
