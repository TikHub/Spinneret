package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/jobs"
)

// Alert evaluator settings.
const (
	// EvaluateJobName is the name of the leader job returned by EvaluateJob.
	EvaluateJobName  = "notify_alert_evaluation"
	evaluateInterval = 30 * time.Second
	evaluateTimeout  = 25 * time.Second
	// maxAlertsPerRule bounds the alerts one rule stores per run; the rest is
	// picked up by later runs (alerts suppressed by de-duplication do not
	// count, so they cannot starve the remaining candidates).
	maxAlertsPerRule = 500
	// maxErrorsPerRule bounds the errors a rule reports per run.
	maxErrorsPerRule = 5

	proxyMinCount          = 5
	proxyActiveRatioMin    = 0.2
	banRecentWindow        = 5 * time.Minute
	banBaselineWindow      = time.Hour
	banBaselineRefresh     = 5 * time.Minute
	banSpikeMinimum        = 10
	banSpikeFactor         = 3
	reportBacklogThreshold = 50_000
	// ReportConsumerGroup is the stream consumer group of the report workers.
	ReportConsumerGroup    = "workers"
	outcomeWindow          = 5 * time.Minute
	outcomeMinTotal        = 100
	unknownRatioMax        = 0.2
	clientErrorRatioMax    = 0.1
	secretExpiryHorizon    = 7 * 24 * time.Hour
	secretExpiredLookback  = 7 * 24 * time.Hour
	secretExpiringDedupTTL = 24 * time.Hour
	maxSecretsPerRun       = 1000
	maxTenantsForBacklog   = 10_000
	lowWatermarkBatch      = 256
	namespaceIDBatch       = 1000
	evaluateRedisTimeout   = 5 * time.Second
	baselineWindows        = 12 // previous hour / 5-minute window
)

// EvaluateJob returns the alert evaluation job: every 30 s on the leader it
// recovers identity_expired alerts lost by the bus path from state_events and
// checks identity and proxy low watermarks, ban spikes, the report backlog,
// unknown-outcome and client-error ratios and expiring secrets.
func (s *Service) EvaluateJob() jobs.Job {
	return jobs.Job{
		Name:     EvaluateJobName,
		Interval: evaluateInterval,
		Mode:     jobs.Leader,
		Timeout:  evaluateTimeout,
		Run:      s.evaluate,
	}
}

// evaluate recovers identity_expired alerts and runs every rule; a failing
// step does not stop the others.
func (s *Service) evaluate(ctx context.Context) error {
	var errs []error
	if ctx.Err() == nil {
		if err := s.recoverExpiredIdentities(ctx); err != nil {
			errs = append(errs, fmt.Errorf("recover identity_expired alerts: %w", err))
		}
	}
	rules := []struct {
		name string
		fn   func(context.Context) ([]Alert, error)
	}{
		{KindIdentityLowWatermark, s.ruleIdentityLowWatermark},
		{KindProxyLowWatermark, s.ruleProxyLowWatermark},
		{KindBanSpike, s.ruleBanSpike},
		{KindReportBacklog, s.ruleReportBacklog},
		{"outcome_ratios", s.ruleOutcomeRatios},
		{KindSecretExpiring, s.ruleSecretExpiring},
	}
	for _, r := range rules {
		if ctx.Err() != nil {
			break
		}
		alerts, err := r.fn(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %s: %w", r.name, err))
		}
		if err := s.emitAll(ctx, r.name, alerts); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// emitAll emits the alerts of a rule until maxAlertsPerRule of them were
// stored (de-duplicated alerts are not counted) and reports the first errors.
func (s *Service) emitAll(ctx context.Context, rule string, alerts []Alert) error {
	var errs []error
	failed, stored := 0, 0
	for i, a := range alerts {
		if stored >= maxAlertsPerRule {
			s.logger.Warn("alert rule produced too many alerts", slog.String("rule", rule),
				slog.Int("alerts", len(alerts)), slog.Int("stored", stored), slog.Int("deferred", len(alerts)-i))
			break
		}
		if ctx.Err() != nil {
			break
		}
		id, err := s.emit(ctx, a)
		if err != nil {
			failed++
			if len(errs) < maxErrorsPerRule {
				errs = append(errs, fmt.Errorf("rule %s: emit %s: %w", rule, a.Kind, err))
			}
			continue
		}
		if id != "" {
			stored++
		}
	}
	if failed > len(errs) {
		errs = append(errs, fmt.Errorf("rule %s: %d more emit errors", rule, failed-len(errs)))
	}
	return errors.Join(errs...)
}

// sortedNamespaces returns every namespace snapshot ordered by ID.
func (s *Service) sortedNamespaces() []*catalog.Namespace {
	// Clone: the catalog may share the returned slice.
	all := slices.Clone(s.cat.Namespaces(""))
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// sortedSites returns the sites of a namespace ordered by ID.
func sortedSites(ns *catalog.Namespace) []*catalog.Site {
	out := make([]*catalog.Site, 0, len(ns.SitesByID))
	for _, site := range ns.SitesByID {
		out = append(out, site)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sortedGroups returns the endpoint groups of a site ordered by ID.
func sortedGroups(site *catalog.Site) []*catalog.EndpointGroup {
	out := make([]*catalog.EndpointGroup, 0, len(site.GroupsByID))
	for _, g := range site.GroupsByID {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ratio returns part/total (0 when total is 0).
func ratio(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// round2 rounds to two decimals for display.
func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
