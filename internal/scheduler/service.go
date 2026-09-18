package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	randv2 "math/rand/v2"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
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
	return &Service{
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
	}
}

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
