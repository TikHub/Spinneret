// Package breaker implements endpoint group circuit breaking (spec §6.7):
// sliding-window evaluation in Lua (breaker_eval.lua), the automatic
// closed/open/half_open state machine, manual open/close (breaker_set.lua),
// reverting recent cooldowns when a breaker opens (revert.lua), the site
// switch, the breaker event history and the read-only "_runtime" config
// content (kinds "breakers" and "site_switches").
//
// # Events
//
// Every breaker transition and site switch publishes, on
// events.NamespaceChannel(namespaceID), an event of type
// events.TypeBreakerTransition whose data is TransitionData:
//
//	{"namespace","site","site_id","client","endpoint_group","endpoint_group_id",
//	 "from","to","trigger","reason","open_until","consecutive_opens","manual",
//	 "actor","metrics":{…}}
//
// For site switches endpoint_group, endpoint_group_id and client are empty,
// trigger is "site_switch" and from/to are "running"/"paused". Every change of
// runtime content publishes, on events.ChannelRuntime, an event of type
// RuntimeEventType whose data is RuntimeEventData {"ns","kind","version"}.
//
// # Redis
//
// The package owns the breaker hash "brk:<eg>" and the "brko" set, reads the
// window buckets "win:<eg>:<b>"/"winh:<eg>:<b>" and "aeg", consumes
// "rcd:<eg>"/"rcds" when reverting (writing "hs:<eg>", "id:<i>" scd, "rdy:<eg>"
// scores and "dirty"), writes "meta" paused and increments "rtv:<ns>" fields
// "breakers" and "site_switches".
package breaker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/breaker/breakerdb"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Configuration defaults.
const (
	DefaultEvalInterval      = 5 * time.Second
	DefaultActiveWindow      = 2 * time.Minute
	DefaultNotifyMinInterval = time.Second
	DefaultLockTTL           = 4 * time.Second
	DefaultNotifyBuffer      = 4096
	DefaultConcurrency       = 16
	DefaultIOTimeout         = 5 * time.Second
)

// Config configures the breaker service. Zero values select the defaults.
type Config struct {
	// EvalInterval is the period of the evaluation sweep job (5 s).
	EvalInterval time.Duration
	// ActiveWindow selects sweep candidates: groups with acquire activity
	// ("aeg") within this window, in addition to every non-closed breaker (2 m).
	ActiveWindow time.Duration
	// NotifyMinInterval is the minimum time between two evaluations of one
	// group triggered by NotifyRisk (1 s).
	NotifyMinInterval time.Duration
	// LockTTL is the TTL of the per-group evaluation lock "lock:brk:<eg>" (4 s).
	LockTTL time.Duration
	// NotifyBuffer bounds queued risk notifications (4096); notifications
	// beyond it are dropped and caught by the next sweep.
	NotifyBuffer int
	// Concurrency bounds parallel group evaluations per sweep and in Run (16).
	Concurrency int
	// IOTimeout bounds each Redis/PostgreSQL round trip of background work (5 s).
	IOTimeout time.Duration
	// Now returns the current time; nil means time.Now. Intended for tests.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.EvalInterval <= 0 {
		c.EvalInterval = DefaultEvalInterval
	}
	if c.ActiveWindow <= 0 {
		c.ActiveWindow = DefaultActiveWindow
	}
	if c.NotifyMinInterval <= 0 {
		c.NotifyMinInterval = DefaultNotifyMinInterval
	}
	if c.LockTTL <= 0 {
		c.LockTTL = DefaultLockTTL
	}
	if c.NotifyBuffer <= 0 {
		c.NotifyBuffer = DefaultNotifyBuffer
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.IOTimeout <= 0 {
		c.IOTimeout = DefaultIOTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// groupRef addresses an endpoint group in the hot state.
type groupRef struct {
	SiteKey  int64
	GroupKey int64
}

// Service evaluates and operates endpoint group breakers. It is safe for
// concurrent use.
type Service struct {
	cfg     Config
	pool    *pgxpool.Pool
	q       *breakerdb.Queries
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	bus     events.Bus
	audit   audit.Recorder
	metrics *observability.Metrics
	logger  *slog.Logger

	lockOwner string
	lockSeq   atomic.Uint64

	notify    chan groupRef
	pendingMu sync.Mutex
	pending   map[groupRef]struct{}

	runtime *runtimeCache
}

// New creates a breaker service. audit, metrics and logger may be nil.
func New(cfg Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, bus events.Bus, rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *Service {
	cfg = cfg.withDefaults()
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		cfg:       cfg,
		pool:      pool,
		q:         breakerdb.New(pool),
		rdb:       rdb,
		keys:      keys,
		cat:       cat,
		bus:       bus,
		audit:     rec,
		metrics:   metrics,
		logger:    logger.With(slog.String("component", "breaker")),
		lockOwner: "brk-" + randomHex(6),
		notify:    make(chan groupRef, cfg.NotifyBuffer),
		pending:   make(map[groupRef]struct{}),
		runtime:   newRuntimeCache(),
	}
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// nextLockOwner returns a lock owner token unique to one evaluation.
func (s *Service) nextLockOwner() string {
	return fmt.Sprintf("%s-%d", s.lockOwner, s.lockSeq.Add(1))
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; fall back to time.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ioContext bounds one background round trip.
func (s *Service) ioContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.cfg.IOTimeout)
}

// resolveGroup finds an endpoint group by ID among the namespaces the
// principal can act in: the token namespace, the active tenant of a user, or
// every namespace for system principals and platform admins without an active
// tenant.
func (s *Service) resolveGroup(p *authz.Principal, groupID string) (*catalog.Namespace, *catalog.Site, *catalog.EndpointGroup, error) {
	if p == nil {
		return nil, nil, nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if groupID == "" {
		return nil, nil, nil, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint_group_id is required")
	}
	var namespaces []*catalog.Namespace
	switch {
	case p.Kind == authz.KindToken:
		if ns, ok := s.cat.Namespace(p.NamespaceID); ok {
			namespaces = []*catalog.Namespace{ns}
		}
	case p.TenantID != "":
		namespaces = s.cat.Namespaces(p.TenantID)
	case p.Kind == authz.KindSystem || p.IsPlatformAdmin:
		namespaces = s.cat.Namespaces("")
	}
	for _, ns := range namespaces {
		for _, site := range ns.SitesByID {
			if g, ok := site.GroupsByID[groupID]; ok {
				return ns, site, g, nil
			}
		}
	}
	return nil, nil, nil, apperr.NotFound("endpoint group %q not found", groupID)
}

// siteResource builds the authorization resource of a site-scoped object.
func siteResource(ns *catalog.Namespace, site *catalog.Site) authz.Resource {
	return authz.Resource{
		TenantID:      ns.TenantID,
		NamespaceID:   ns.ID,
		NamespaceName: ns.Name,
		SiteID:        site.ID,
		SiteName:      site.Name,
	}
}

// namespaceResource builds the authorization resource of a namespace.
func namespaceResource(ns *catalog.Namespace) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
}

// accessibleSites returns the sites of ns the principal holds perm on. all is
// true when perm is held for every site of the namespace (including sites that
// no longer exist in the snapshot).
func accessibleSites(p *authz.Principal, ns *catalog.Namespace, perm authz.Permission) (all bool, sites map[string]*catalog.Site) {
	all, _ = p.SiteFilter(ns.TenantID, ns.ID, perm)
	sites = make(map[string]*catalog.Site, len(ns.SitesByID))
	for id, site := range ns.SitesByID {
		if all || p.Can(perm, siteResource(ns, site)) {
			sites[id] = site
		}
	}
	return all, sites
}

// recordAudit writes one audit entry for an operation of p.
func (s *Service) recordAudit(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, kind, id, name, result string, details map[string]any) {
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, action, kind, id, name, result, details))
}

// recordDenied writes the audit entry of a mutation rejected by authorization.
func (s *Service) recordDenied(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, kind, id, name string, perm authz.Permission) {
	s.recordAudit(ctx, p, ns, action, kind, id, name, audit.ResultDenied, map[string]any{"permission": string(perm)})
}

// stateValue maps a breaker state to the spinneret_breaker_state gauge value.
func stateValue(state string) float64 {
	switch state {
	case StateOpen:
		return observability.BreakerOpenValue
	case StateHalfOpen:
		return observability.BreakerHalfOpenValue
	default:
		return observability.BreakerClosedValue
	}
}

// observeState updates the breaker state gauge.
func (s *Service) observeState(site *catalog.Site, g *catalog.EndpointGroup, state string) {
	if s.metrics == nil {
		return
	}
	s.metrics.BreakerState.WithLabelValues(observability.Label(site.Name), observability.Label(g.Name)).Set(stateValue(state))
}

// observeTransition counts a transition.
func (s *Service) observeTransition(site *catalog.Site, g *catalog.EndpointGroup, to string) {
	if s.metrics == nil {
		return
	}
	s.metrics.BreakerTransitions.WithLabelValues(observability.Label(site.Name), observability.Label(g.Name), to).Inc()
}

// errorCollector accumulates a bounded number of errors from concurrent work.
type errorCollector struct {
	mu    sync.Mutex
	errs  []error
	extra int
}

const maxCollectedErrors = 10

func (c *errorCollector) add(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) < maxCollectedErrors {
		c.errs = append(c.errs, err)
		return
	}
	c.extra++
}

func (c *errorCollector) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) == 0 {
		return nil
	}
	errs := c.errs
	if c.extra > 0 {
		errs = append(append([]error(nil), errs...), fmt.Errorf("%d more errors", c.extra))
	}
	return errors.Join(errs...)
}
