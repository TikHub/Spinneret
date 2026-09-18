// Package analytics implements the console dashboard queries (DashboardService):
// live counts from the Redis hot state, per-minute aggregates and risk events
// from PostgreSQL, and raw report events from ClickHouse.
//
// The service is read-only. Authorization is the caller's job: every method
// takes a Scope (or a site that the caller has already authorized) and only
// returns data of the sites listed in it.
//
// Data sources per method:
//
//   - Overview: identities (PG, GROUP BY state), proxies (PG, per namespace),
//     acquire_stats_minutely and outcome_stats_minutely over the complete
//     minutes of the window, ready queues (ZCOUNT rdy -inf now), breakers
//     (SMEMBERS brko + HGET brk st) and the report stream backlog
//     (XINFO GROUPS pending + lag of the worker consumer group, XLEN when the
//     group is missing or its lag is unknown).
//   - TimeSeries: acquire_stats_minutely / outcome_stats_minutely bucketed with
//     date_bin on steps aligned to the Unix epoch, empty buckets filled with 0.
//   - Heatmap: identities of a site client (PG, keyset on id) × endpoint groups
//     of the client, one pipelined round trip to Redis (HMGET hs:<eg> and
//     ZMSCORE rdy:<eg> per group, HMGET id:<i> per identity, HMGET acc:<a> per
//     account).
//   - RiskEvents: risk_events keyset pagination (created_at DESC, id DESC).
//   - RequestEvents: ClickHouse report_events with server-side query
//     parameters, keyset pagination (event_time DESC, report_id DESC,
//     lease_id DESC) and an optional summary.
//   - NodeStats: node_stats_minutely sums per node.
package analytics

import (
	"log/slog"
	"sort"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/analytics/analyticsdb"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Defaults of a Service.
const (
	// DefaultReportShards is the number of report stream shards inspected for
	// the stream backlog (SPINNERET_REPORT_SHARDS default).
	DefaultReportShards = 16
	// DefaultQueryTimeout bounds the PostgreSQL and Redis work of one call.
	DefaultQueryTimeout = 15 * time.Second
	// DefaultClickHouseTimeout bounds the ClickHouse work of one call.
	DefaultClickHouseTimeout = 30 * time.Second
	// maxReportShards caps the shard count accepted by WithReportShards.
	maxReportShards = 4096
)

// Service answers dashboard queries. It is safe for concurrent use.
type Service struct {
	q      *analyticsdb.Queries
	ch     chdriver.Conn
	rdb    rueidis.Client
	keys   redis.Keys
	cat    catalog.Catalog
	logger *slog.Logger

	now          func() time.Time
	reportShards int
	queryTimeout time.Duration
	chTimeout    time.Duration
}

// Option customizes a Service.
type Option func(*Service)

// WithReportShards sets the number of report stream shards
// (SPINNERET_REPORT_SHARDS). Values outside 1..4096 are ignored.
func WithReportShards(n int) Option {
	return func(s *Service) {
		if n >= 1 && n <= maxReportShards {
			s.reportShards = n
		}
	}
}

// WithClock replaces the wall clock (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithTimeouts overrides the per-call timeouts of PostgreSQL/Redis and of
// ClickHouse work. Non-positive values keep the defaults.
func WithTimeouts(query, clickhouse time.Duration) Option {
	return func(s *Service) {
		if query > 0 {
			s.queryTimeout = query
		}
		if clickhouse > 0 {
			s.chTimeout = clickhouse
		}
	}
}

// New creates the service. pool, rdb and cat are required; ch is nil when
// ClickHouse is disabled (RequestEvents then fails with unavailable); logger
// may be nil. Options set the report shard count (default 16), the clock and
// the query timeouts.
func New(pool *pgxpool.Pool, ch chdriver.Conn, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger, opts ...Option) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		ch:           ch,
		rdb:          rdb,
		keys:         keys,
		cat:          cat,
		logger:       logger.With(slog.String("component", "analytics")),
		now:          time.Now,
		reportShards: DefaultReportShards,
		queryTimeout: DefaultQueryTimeout,
		chTimeout:    DefaultClickHouseTimeout,
	}
	if pool != nil {
		s.q = analyticsdb.New(pool)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ClickHouseEnabled reports whether raw request events can be queried.
func (s *Service) ClickHouseEnabled() bool {
	return s.ch != nil
}

// Scope is the part of a namespace a caller may read.
type Scope struct {
	// NamespaceID identifies the namespace.
	NamespaceID string
	// AllSites is true when every site of the namespace is readable.
	AllSites bool
	// SiteIDs lists the readable sites when AllSites is false. IDs that are
	// not sites of the namespace are ignored.
	SiteIDs []string
}

// resolvedScope is a Scope bound to the current namespace snapshot.
type resolvedScope struct {
	ns       *catalog.Namespace
	allSites bool
	// sites are the readable sites ordered by name.
	sites []*catalog.Site
	byID  map[string]*catalog.Site
}

// resolve binds the scope to the namespace snapshot of the catalog.
func (s *Service) resolve(scope Scope) (*resolvedScope, error) {
	if scope.NamespaceID == "" {
		return nil, apperr.InvalidArgument("", "namespace is required")
	}
	ns, ok := s.cat.Namespace(scope.NamespaceID)
	if !ok {
		return nil, apperr.NotFound("namespace not found")
	}
	rs := &resolvedScope{ns: ns, allSites: scope.AllSites, byID: make(map[string]*catalog.Site)}
	if scope.AllSites {
		for id, site := range ns.SitesByID {
			rs.byID[id] = site
		}
	} else {
		for _, id := range scope.SiteIDs {
			if site, ok := ns.SitesByID[id]; ok {
				rs.byID[id] = site
			}
		}
	}
	rs.sites = make([]*catalog.Site, 0, len(rs.byID))
	for _, site := range rs.byID {
		rs.sites = append(rs.sites, site)
	}
	sort.Slice(rs.sites, func(i, j int) bool { return rs.sites[i].Name < rs.sites[j].Name })
	return rs, nil
}

// site returns a readable site by ID. Sites of the namespace outside the scope
// yield permission_denied, unknown sites site_unknown.
func (rs *resolvedScope) site(id string) (*catalog.Site, error) {
	if site, ok := rs.byID[id]; ok {
		return site, nil
	}
	if _, exists := rs.ns.SitesByID[id]; exists {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "site is not readable")
	}
	return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site not found")
}

// group returns a readable endpoint group by ID.
func (rs *resolvedScope) group(id string) (*catalog.EndpointGroup, *catalog.Site, error) {
	for _, site := range rs.ns.SitesByID {
		if g, ok := site.GroupsByID[id]; ok {
			if _, readable := rs.byID[site.ID]; !readable {
				return nil, nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "site is not readable")
			}
			return g, site, nil
		}
	}
	return nil, nil, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group not found")
}

// siteIDs returns the IDs of the readable sites ordered by site name.
func (rs *resolvedScope) siteIDs() []string {
	ids := make([]string, len(rs.sites))
	for i, site := range rs.sites {
		ids[i] = site.ID
	}
	return ids
}
