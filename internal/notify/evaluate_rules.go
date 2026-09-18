package notify

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/notify/notifydb"
)

// banBaseline caches the ban counts of the previous hour per site.
type banBaseline struct {
	computedAt time.Time
	counts     map[string]int64 // site ID → bans in [computedAt-65m, computedAt-5m)
}

// lowWatermarkCandidate is an endpoint group with a configured low watermark.
type lowWatermarkCandidate struct {
	ns    *catalog.Namespace
	site  *catalog.Site
	group *catalog.EndpointGroup
}

// ruleIdentityLowWatermark alerts when an endpoint group has fewer
// identities available now (ZCOUNT rdy -inf now) than its low watermark.
func (s *Service) ruleIdentityLowWatermark(ctx context.Context) ([]Alert, error) {
	var candidates []lowWatermarkCandidate
	for _, ns := range s.sortedNamespaces() {
		for _, site := range sortedSites(ns) {
			for _, g := range sortedGroups(site) {
				if g.LowWatermark > 0 {
					candidates = append(candidates, lowWatermarkCandidate{ns: ns, site: site, group: g})
				}
			}
		}
	}
	nowMs := strconv.FormatInt(s.now().UnixMilli(), 10)
	var alerts []Alert
	for start := 0; start < len(candidates); start += lowWatermarkBatch {
		batch := candidates[start:min(start+lowWatermarkBatch, len(candidates))]
		cmds := make(rueidis.Commands, len(batch))
		for i, c := range batch {
			cmds[i] = s.rdb.B().Zcount().Key(s.keys.Ready(c.site.Key, c.group.Key)).Min("-inf").Max(nowMs).Build()
		}
		rctx, cancel := context.WithTimeout(ctx, evaluateRedisTimeout)
		results := s.rdb.DoMulti(rctx, cmds...)
		cancel()
		for i, res := range results {
			available, err := res.AsInt64()
			if err != nil {
				return alerts, fmt.Errorf("count ready identities: %w", err)
			}
			c := batch[i]
			if available >= int64(c.group.LowWatermark) {
				continue
			}
			subject := c.site.Name + "/" + c.group.Client + "/" + c.group.Name
			alerts = append(alerts, Alert{
				Kind: KindIdentityLowWatermark, Severity: SeverityWarning,
				TenantID: c.ns.TenantID, NamespaceID: c.ns.ID, SiteID: c.site.ID,
				Title: "Available identities below low watermark: " + subject,
				Message: fmt.Sprintf("Endpoint group %s has %d identities available now; its low watermark is %d.",
					subject, available, c.group.LowWatermark),
				Details: map[string]any{
					"site": c.site.Name, "site_id": c.site.ID, "client": c.group.Client,
					"endpoint_group": c.group.Name, "endpoint_group_id": c.group.ID,
					"available": available, "low_watermark": c.group.LowWatermark,
				},
				DedupKey: "group:" + c.group.ID,
			})
		}
	}
	return alerts, nil
}

// ruleProxyLowWatermark alerts for namespaces with at least 5 proxies whose
// active share among active and dead proxies is below 20%.
func (s *Service) ruleProxyLowWatermark(ctx context.Context) ([]Alert, error) {
	rows, err := s.q.NotifyProxyStateCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count proxies: %w", err)
	}
	var alerts []Alert
	for _, r := range rows {
		considered := r.Active + r.Dead
		if r.Total < proxyMinCount || considered == 0 {
			continue
		}
		share := ratio(r.Active, considered)
		if share >= proxyActiveRatioMin {
			continue
		}
		nsName := r.NamespaceID
		if ns, ok := s.cat.Namespace(r.NamespaceID); ok {
			nsName = ns.Name
		}
		alerts = append(alerts, Alert{
			Kind: KindProxyLowWatermark, Severity: SeverityWarning, TenantID: r.TenantID, NamespaceID: r.NamespaceID,
			Title: "Healthy proxies below low watermark: " + nsName,
			Message: fmt.Sprintf("Only %d of %d active or dead proxies in namespace %s are active (%.0f%%, threshold %.0f%%).",
				r.Active, considered, nsName, share*100, proxyActiveRatioMin*100),
			Details: map[string]any{
				"namespace": nsName, "active": r.Active, "dead": r.Dead, "total": r.Total,
				"active_ratio": round2(share), "threshold": proxyActiveRatioMin,
			},
			DedupKey: "namespace:" + r.NamespaceID,
		})
	}
	return alerts, nil
}

// ruleBanSpike alerts when the enforced bans of a site in the last 5 minutes
// exceed max(10, 3 × the average 5-minute count of the previous hour). The
// previous-hour baseline is refreshed every 5 minutes.
func (s *Service) ruleBanSpike(ctx context.Context) ([]Alert, error) {
	namespaces := s.sortedNamespaces()
	if len(namespaces) == 0 {
		return nil, nil
	}
	nsIDs := make([]string, len(namespaces))
	for i, ns := range namespaces {
		nsIDs[i] = ns.ID
	}
	now := s.now().UTC()
	recent, err := s.banCounts(ctx, nsIDs, now.Add(-banRecentWindow), now)
	if err != nil {
		return nil, err
	}
	if len(recent) == 0 {
		return nil, nil
	}
	baseline, err := s.banBaselineCounts(ctx, nsIDs, now)
	if err != nil {
		return nil, err
	}
	var alerts []Alert
	for _, r := range recent {
		avg := float64(baseline[r.SiteID]) / baselineWindows
		threshold := math.Max(banSpikeMinimum, banSpikeFactor*avg)
		if float64(r.Bans) <= threshold {
			continue
		}
		siteName := r.SiteID
		if site, _, ok := s.cat.Site(r.SiteID); ok {
			siteName = site.Name
		}
		alerts = append(alerts, Alert{
			Kind: KindBanSpike, Severity: SeverityWarning, TenantID: r.TenantID, NamespaceID: r.NamespaceID, SiteID: r.SiteID,
			Title: "Ban spike on " + siteName,
			Message: fmt.Sprintf("%d bans on site %s in the last 5 minutes (threshold %.1f, previous-hour average %.1f per 5 minutes).",
				r.Bans, siteName, threshold, avg),
			Details: map[string]any{
				"site": siteName, "site_id": r.SiteID, "bans_5m": r.Bans,
				"baseline_avg_5m": round2(avg), "threshold": round2(threshold),
			},
			DedupKey: "site:" + r.SiteID,
		})
	}
	return alerts, nil
}

// banCounts counts bans per site in [from, to) in namespace ID batches.
func (s *Service) banCounts(ctx context.Context, nsIDs []string, from, to time.Time) ([]notifydb.NotifyBanCountsRow, error) {
	var out []notifydb.NotifyBanCountsRow
	for start := 0; start < len(nsIDs); start += namespaceIDBatch {
		rows, err := s.q.NotifyBanCounts(ctx, notifydb.NotifyBanCountsParams{
			NamespaceIds: nsIDs[start:min(start+namespaceIDBatch, len(nsIDs))], FromAt: from, ToAt: to,
		})
		if err != nil {
			return nil, fmt.Errorf("count bans: %w", err)
		}
		out = append(out, rows...)
	}
	return out, nil
}

// banBaselineCounts returns the cached previous-hour ban counts per site,
// recomputing them when older than banBaselineRefresh.
func (s *Service) banBaselineCounts(ctx context.Context, nsIDs []string, now time.Time) (map[string]int64, error) {
	s.banMu.Lock()
	defer s.banMu.Unlock()
	if s.banBaseline.counts != nil && now.Sub(s.banBaseline.computedAt) < banBaselineRefresh && !now.Before(s.banBaseline.computedAt) {
		return s.banBaseline.counts, nil
	}
	end := now.Add(-banRecentWindow)
	rows, err := s.banCounts(ctx, nsIDs, end.Add(-banBaselineWindow), end)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.SiteID] += r.Bans
	}
	s.banBaseline = banBaseline{computedAt: now, counts: counts}
	return counts, nil
}

// backlog summarizes the report stream backlog.
type backlog struct {
	pending  int64
	lag      int64
	perShard map[string]int64
}

// ruleReportBacklog alerts every tenant subscribed to report_backlog when the
// undelivered plus unacknowledged reports of all shards exceed 50 000.
func (s *Service) ruleReportBacklog(ctx context.Context) ([]Alert, error) {
	b, err := s.reportBacklog(ctx)
	if err != nil {
		return nil, err
	}
	total := b.pending + b.lag
	if total <= reportBacklogThreshold {
		return nil, nil
	}
	tenants, err := s.q.NotifyTenantsSubscribed(ctx, notifydb.NotifyTenantsSubscribedParams{
		Kind: KindReportBacklog, LimitRows: maxTenantsForBacklog,
	})
	if err != nil {
		return nil, fmt.Errorf("list subscribed tenants: %w", err)
	}
	alerts := make([]Alert, 0, len(tenants))
	for _, tenantID := range tenants {
		alerts = append(alerts, Alert{
			Kind: KindReportBacklog, Severity: SeverityCritical, TenantID: tenantID,
			Title: "Report processing backlog",
			Message: fmt.Sprintf("%d reports are waiting to be processed (%d pending acknowledgement, %d not yet delivered; threshold %d).",
				total, b.pending, b.lag, reportBacklogThreshold),
			Details: map[string]any{
				"backlog": total, "pending": b.pending, "lag": b.lag, "shards": s.cfg.ReportShards,
				"threshold": reportBacklogThreshold, "per_shard": b.perShard,
			},
			DedupKey: "cluster",
		})
	}
	return alerts, nil
}

// reportBacklog reads XINFO GROUPS of every shard. A shard's backlog is the
// consumer group's pending count plus its lag; without the group (no worker
// consumed yet) or with an unknown lag, the stream length is used instead.
func (s *Service) reportBacklog(ctx context.Context) (backlog, error) {
	shards := s.cfg.ReportShards
	cmds := make(rueidis.Commands, 0, shards*2)
	for shard := range shards {
		key := s.keys.Stream(shard)
		cmds = append(cmds, s.rdb.B().XinfoGroups().Key(key).Build(), s.rdb.B().Xlen().Key(key).Build())
	}
	rctx, cancel := context.WithTimeout(ctx, evaluateRedisTimeout)
	defer cancel()
	results := s.rdb.DoMulti(rctx, cmds...)
	b := backlog{perShard: map[string]int64{}}
	for shard := range shards {
		length, err := results[shard*2+1].AsInt64()
		if err != nil {
			return backlog{}, fmt.Errorf("stream length of shard %d: %w", shard, err)
		}
		pending, lag, err := groupBacklog(results[shard*2], length)
		if err != nil {
			return backlog{}, fmt.Errorf("stream groups of shard %d: %w", shard, err)
		}
		b.pending += pending
		b.lag += lag
		if pending+lag > 0 {
			b.perShard[strconv.Itoa(shard)] = pending + lag
		}
	}
	return b, nil
}

// groupBacklog extracts pending and lag of the report consumer group.
func groupBacklog(res rueidis.RedisResult, streamLen int64) (pending, lag int64, err error) {
	groups, err := res.ToArray()
	if err != nil {
		if isNoSuchKey(err) {
			return 0, streamLen, nil
		}
		return 0, 0, err
	}
	for _, g := range groups {
		fields, err := g.AsMap()
		if err != nil {
			return 0, 0, err
		}
		name := fields["name"]
		if n, _ := name.ToString(); n != ReportConsumerGroup {
			continue
		}
		p := fields["pending"]
		pending, _ = p.AsInt64()
		l, ok := fields["lag"]
		if !ok || l.IsNil() {
			return pending, max(streamLen-pending, 0), nil
		}
		// An unreadable lag is estimated from the stream length like a missing one.
		if n, lagErr := l.AsInt64(); lagErr == nil {
			return pending, n, nil
		}
		return pending, max(streamLen-pending, 0), nil
	}
	return 0, streamLen, nil
}

func isNoSuchKey(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such key")
}

// ruleOutcomeRatios alerts per site over the last 5 minutes (at least 100
// reports) when unknown outcomes exceed 20% or client errors exceed 10%.
func (s *Service) ruleOutcomeRatios(ctx context.Context) ([]Alert, error) {
	from := s.now().UTC().Add(-outcomeWindow).Truncate(time.Minute)
	rows, err := s.q.NotifyOutcomeTotals(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("sum outcomes: %w", err)
	}
	var alerts []Alert
	for _, r := range rows {
		// Rows without a site cannot be attributed; ratios are per site.
		if r.Total < outcomeMinTotal || r.SiteID == "" {
			continue
		}
		ns, ok := s.cat.Namespace(r.NamespaceID)
		if !ok {
			continue
		}
		siteName := r.SiteID
		if site, ok := ns.SitesByID[r.SiteID]; ok {
			siteName = site.Name
		}
		base := Alert{Severity: SeverityWarning, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: r.SiteID}
		if u := ratio(r.Unknown, r.Total); u > unknownRatioMax {
			a := base
			a.Kind = KindUnknownRatioHigh
			a.Title = "High unknown outcome ratio on " + siteName
			a.Message = fmt.Sprintf("%.0f%% of %d reports on site %s in the last 5 minutes were classified unknown (threshold %.0f%%); signal rules may need updating.",
				u*100, r.Total, siteName, unknownRatioMax*100)
			a.Details = map[string]any{"site": siteName, "site_id": r.SiteID, "total": r.Total, "unknown": r.Unknown,
				"ratio": round2(u), "threshold": unknownRatioMax}
			a.DedupKey = "site:" + r.SiteID
			alerts = append(alerts, a)
		}
		if c := ratio(r.ClientError, r.Total); c > clientErrorRatioMax {
			a := base
			a.Kind = KindClientErrorSpike
			a.Title = "Client error spike on " + siteName
			a.Message = fmt.Sprintf("%.0f%% of %d reports on site %s in the last 5 minutes were client errors (threshold %.0f%%).",
				c*100, r.Total, siteName, clientErrorRatioMax*100)
			a.Details = map[string]any{"site": siteName, "site_id": r.SiteID, "total": r.Total, "client_error": r.ClientError,
				"ratio": round2(c), "threshold": clientErrorRatioMax}
			a.DedupKey = "site:" + r.SiteID
			alerts = append(alerts, a)
		}
	}
	return alerts, nil
}

// ruleSecretExpiring alerts once a day for secrets expiring within 7 days
// (and for 7 days after they expired).
func (s *Service) ruleSecretExpiring(ctx context.Context) ([]Alert, error) {
	now := s.now().UTC()
	rows, err := s.q.NotifySecretsExpiring(ctx, notifydb.NotifySecretsExpiringParams{
		FromAt: now.Add(-secretExpiredLookback), ToAt: now.Add(secretExpiryHorizon), LimitRows: maxSecretsPerRun,
	})
	if err != nil {
		return nil, fmt.Errorf("list expiring secrets: %w", err)
	}
	alerts := make([]Alert, 0, len(rows))
	for _, r := range rows {
		nsName := r.NamespaceID
		if ns, ok := s.cat.Namespace(r.NamespaceID); ok {
			nsName = ns.Name
		}
		expires := r.ExpiresAt.UTC().Format(time.RFC3339)
		title := "Secret expiring: " + r.Path
		message := fmt.Sprintf("Secret %s in namespace %s expires at %s (in %s).", r.Path, nsName, expires,
			r.ExpiresAt.Sub(now).Truncate(time.Minute))
		if !r.ExpiresAt.After(now) {
			title = "Secret expired: " + r.Path
			message = fmt.Sprintf("Secret %s in namespace %s expired at %s.", r.Path, nsName, expires)
		}
		alerts = append(alerts, Alert{
			Kind: KindSecretExpiring, Severity: SeverityWarning, TenantID: r.TenantID, NamespaceID: r.NamespaceID,
			Title: title, Message: message,
			Details: map[string]any{
				"secret_id": r.ID, "path": r.Path, "namespace": nsName, "expires_at": expires,
				"expired": !r.ExpiresAt.After(now),
			},
			DedupKey: "secret:" + r.ID,
			DedupTTL: secretExpiringDedupTTL,
		})
	}
	return alerts, nil
}
