package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	randv2 "math/rand/v2"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/admit"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// redisTimeout bounds a single script call.
const redisTimeout = 2 * time.Second

// Service implements the lease operations. It is safe for concurrent use.
type Service struct {
	cfg     Config
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	creds   CredentialSource
	proxies ProxyResolver
	rec     StatsRecorder
	metrics *observability.Metrics
	logger  *slog.Logger

	// layouts memoizes the endpoint group layout ARGV of release.lua and
	// reap.lua per catalog snapshot.
	layouts layoutCache

	// owner identifies this instance in per-site reaper locks.
	owner string
	// clock and random are replaceable in tests.
	clock  func() time.Time
	random func() float64

	// admit bounds concurrent acquire.lua calls. It is nil when admission
	// control is disabled; every Gate method is nil-safe.
	admit *admit.Gate
	// peers is the number of live API instances the fleet-wide acquire budget
	// is divided among. It is 1 until the peer registry reports otherwise.
	peers atomic.Int64
}

// New creates the scheduler service. rec and metrics may be nil.
func New(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, creds CredentialSource, proxies ProxyResolver, rec StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *Service {
	if cfg.ReportShards <= 0 {
		cfg.ReportShards = DefaultReportShards
	}
	if cfg.ReportShards > maxReportShards {
		cfg.ReportShards = maxReportShards
	}
	if cfg.LateReportWindow <= 0 {
		cfg.LateReportWindow = DefaultLateReportWindow
	}
	if logger == nil {
		logger = slog.Default()
	}
	if rec == nil {
		rec = nopRecorder{}
	}
	var gate *admit.Gate
	if limit := initialAcquireLimit(cfg); limit > 0 {
		gate = admit.New(admit.Config{Limit: limit, MaxWait: acquireAdmissionWait})
	}
	s := &Service{
		cfg:     cfg,
		rdb:     rdb,
		keys:    keys,
		cat:     cat,
		creds:   creds,
		proxies: proxies,
		rec:     rec,
		metrics: metrics,
		logger:  logger.With(slog.String("component", "scheduler")),
		owner:   randomOwner(),
		clock:   time.Now,
		random:  randv2.Float64,
		admit:   gate,
	}
	s.peers.Store(1)
	if metrics != nil && gate != nil {
		// With the limit pinned no peer registry runs, so there is no live count
		// to report: an absent series reads as "not applicable", while a
		// hardcoded 1 on every instance of a twenty-replica fleet reads as a
		// broken registry. RegisterAcquireGate skips nil gauges.
		peers := s.acquirePeers
		if cfg.AcquireMaxInflight > 0 {
			peers = nil
		}
		metrics.RegisterAcquireGate(gate.Limit, gate.InFlight, gate.Queued, peers)
	}
	return s
}

// initialAcquireLimit returns the per-instance acquire limit before any peer
// count is known, or 0 when admission control is disabled.
func initialAcquireLimit(cfg Config) int {
	switch {
	case cfg.AcquireMaxInflight > 0:
		return min(cfg.AcquireMaxInflight, maxAcquireInflight)
	case cfg.AcquireFleetInflight > 0:
		// Until the first heartbeat this instance assumes it is alone, which is
		// the widest it ever gets; every peer it discovers narrows the limit.
		return clampAcquireLimit(cfg.AcquireFleetInflight)
	default:
		return 0
	}
}

// clampAcquireLimit clamps a derived per-instance limit to the supported range.
func clampAcquireLimit(n int) int {
	return min(max(n, minAcquireInflight), maxAcquireInflight)
}

// SetAcquirePeers divides the fleet-wide acquire budget by the number of live
// API instances. It is called by the peer registry loop and is a no-op when
// admission control is disabled or the limit is pinned.
func (s *Service) SetAcquirePeers(live int) {
	if s.admit == nil || s.cfg.AcquireMaxInflight > 0 || live < 1 {
		return
	}
	// Integer truncation is deliberate: the fleet total is never allowed to
	// exceed the configured budget, and the per-instance floor then applies.
	limit := clampAcquireLimit(s.cfg.AcquireFleetInflight / live)
	// The count and the limit are published as two separate stores, so a scrape
	// landing between them reports one of them one beat stale. They are
	// eventually consistent within a single scrape and cannot drift further,
	// because only the registry's serial heartbeat loop calls this.
	prev := int(s.peers.Swap(int64(live)))
	s.admit.SetLimit(limit)
	// Logged on change only: a beat every couple of seconds must not produce a
	// log line, but "the acquire limit of my instance changed by itself" is the
	// one new behaviour an operator cannot otherwise correlate with a burst of
	// sheds, and one line per scaling event is the difference between a short
	// incident and a long one.
	if prev != live {
		s.logger.Info("acquire admission limit divided among live api instances",
			slog.Int("previous_peers", prev),
			slog.Int("peers", live),
			slog.Int("limit", limit),
			slog.Int("fleet_inflight", s.cfg.AcquireFleetInflight))
	}
}

// acquirePeers reports the live API instance count behind the current limit.
func (s *Service) acquirePeers() int { return int(s.peers.Load()) }

type nopRecorder struct{}

func (nopRecorder) RecordAcquire(AcquireRecord)   {}
func (nopRecorder) RecordLeaseEnd(LeaseEndRecord) {}

func randomOwner() string {
	var b [12]byte
	// crypto/rand.Read never returns an error (it aborts the process instead).
	_, _ = rand.Read(b[:])
	return "scheduler-" + hex.EncodeToString(b[:])
}

// principalNamespace returns the namespace snapshot of a token principal.
// Leases are node operations: other principal kinds are denied.
func (s *Service) principalNamespace(p *authz.Principal) (*catalog.Namespace, error) {
	if p.Kind != authz.KindToken || p.NamespaceID == "" {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "lease operations require an API token")
	}
	ns, ok := s.cat.Namespace(p.NamespaceID)
	if !ok {
		return nil, apperr.NotFound("namespace not found")
	}
	return ns, nil
}

// siteResource builds the authorization resource of a site.
func siteResource(ns *catalog.Namespace, site *catalog.Site) authz.Resource {
	return authz.Resource{
		TenantID:      ns.TenantID,
		NamespaceID:   ns.ID,
		NamespaceName: ns.Name,
		SiteID:        site.ID,
		SiteName:      site.Name,
	}
}

// leaseUnknown is returned for leases that do not exist or that the caller
// may not see.
func leaseUnknown() *apperr.Error {
	return apperr.New(connect.CodeNotFound, apperr.ReasonLeaseUnknown, "lease not found")
}

// scriptContext bounds a script call and detaches it from cancellation when
// the call must complete (releasing leases on error paths).
func scriptContext(ctx context.Context, detach bool) (context.Context, context.CancelFunc) {
	if detach {
		ctx = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(ctx, redisTimeout)
}

func metricLabel(v string) string { return observability.Label(v) }

// observeAcquire records acquire metrics.
func (s *Service) observeAcquire(site, group, result string, d time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.AcquireTotal.WithLabelValues(metricLabel(site), metricLabel(group), result).Inc()
	s.metrics.AcquireDuration.WithLabelValues(metricLabel(site)).Observe(d.Seconds())
}

// observeAdmission records one admission decision. With admission control
// disabled there is no decision to record, so no admission series is exported
// at all and the pre-gate metric surface is reproduced exactly.
func (s *Service) observeAdmission(result string, waited time.Duration) {
	if s.metrics == nil || s.admit == nil {
		return
	}
	s.metrics.AcquireAdmissionTotal.WithLabelValues(result).Inc()
	s.metrics.AcquireAdmissionWait.Observe(waited.Seconds())
}

// observeAcquireScript records one acquire.lua round trip. Unlike the admission
// series this one is recorded with admission control disabled too, because it is
// the observable that separates "Redis is slow" from "the limit is narrow" and
// is useful either way. It means the off arm of an A/B run already carries the
// two clock reads, so the measured difference between the arms is the gate
// alone — not the whole change.
func (s *Service) observeAcquireScript(d time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.AcquireScriptDuration.Observe(d.Seconds())
}
