// Package sitesvc is the PostgreSQL-backed domain service for sites, endpoint
// groups and URI rules. Every mutation runs in one transaction and is then
// propagated: the namespace catalog snapshot is invalidated, the site's Redis
// hot state is resynchronized and, when a site or endpoint group was created
// or deleted, the namespace's "breakers" and "site_switches" runtime versions
// are bumped and announced on events.ChannelRuntime.
package sitesvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/sitesvc/sitesvcdb"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Limits enforced by the service.
const (
	MaxClients             = 16
	MaxRulesPerGroup       = 1000
	MaxRegexRulesPerClient = 200
	MaxDisplayNameLength   = 128
	MaxDescriptionLength   = 1024
	MaxPageSize            = 500
	DefaultClient          = "web"

	// propagateTimeout bounds catalog invalidation and hot-state sync after a
	// commit; they run detached from the request's cancellation so that a
	// disconnecting client cannot leave peers with a stale snapshot.
	propagateTimeout = 30 * time.Second
	// redisTimeout bounds the hot-state reads of list and get calls.
	redisTimeout = 3 * time.Second
	// rollbackTimeout bounds the rollback of a failed transaction.
	rollbackTimeout = 5 * time.Second
)

// Audit actions and resource kinds.
const (
	ActionSiteCreate          = "site.create"
	ActionSiteUpdate          = "site.update"
	ActionSiteDelete          = "site.delete"
	ActionEndpointGroupCreate = "endpoint_group.create"
	ActionEndpointGroupUpdate = "endpoint_group.update"
	ActionEndpointGroupDelete = "endpoint_group.delete"
	ActionURIRulesReplace     = "uri_rules.replace"

	ResourceSite          = "site"
	ResourceEndpointGroup = "endpoint_group"
)

// Breaker states reported for endpoint groups.
const (
	BreakerClosed   = "closed"
	BreakerOpen     = "open"
	BreakerHalfOpen = "half_open"
)

// HotSyncer materializes site changes into the Redis hot state (provided by
// hotstate.Syncer).
type HotSyncer interface {
	SyncSite(ctx context.Context, siteID string) error
	RemoveSite(ctx context.Context, siteKey int64) error
}

// Service manages sites, endpoint groups and URI rules. It is safe for
// concurrent use. Authorization is the caller's responsibility (the Connect
// handler checks permissions); the principal passed to mutations is used for
// audit entries only.
type Service struct {
	pool   *pgxpool.Pool
	cat    catalog.Catalog
	hot    HotSyncer
	audit  audit.Recorder
	rdb    rueidis.Client
	keys   redis.Keys
	bus    events.Bus
	logger *slog.Logger
	now    func() time.Time
}

// NewService creates the service.
//
// Deviation from the service contract: besides
// (pool, cat, hot, audit, logger) the service takes the Redis client and key
// builder, used read-only for EndpointGroup.AvailableIdentities (ZCOUNT of the
// ready queue) and EndpointGroup.BreakerState (HGET of the breaker hash). rdb
// may be nil, in which case those fields report 0 and "closed". A nil hot
// syncer skips hot-state propagation, a nil recorder discards audit entries
// and a nil logger discards logs.
//
// The service also writes to Redis: creating or deleting a site or endpoint
// group (including endpoint groups added or removed with site clients)
// increments the "breakers" and "site_switches" fields of
// Keys.RuntimeVersions(namespaceID) and, with WithEventBus, publishes the
// runtime events {"ns","kind","version"} on events.ChannelRuntime, like the
// breaker service does for its own changes.
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot HotSyncer, rec audit.Recorder, rdb rueidis.Client, keys redis.Keys, logger *slog.Logger, opts ...Option) *Service {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		pool:   pool,
		cat:    cat,
		hot:    hot,
		audit:  rec,
		rdb:    rdb,
		keys:   keys,
		logger: logger.With(slog.String("component", "sitesvc")),
		now:    time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// SiteRef identifies a site and its namespace and tenant.
type SiteRef struct {
	ID            string
	Key           int64
	Name          string
	NamespaceID   string
	NamespaceName string
	TenantID      string
}

// Site is a site with its counts.
type Site struct {
	SiteRef
	DisplayName        string
	Description        string
	Clients            []string
	Paused             bool
	PausedReason       string
	PausedAt           *time.Time
	EndpointGroupCount int
	IdentityCount      int // identities that are not retired
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// GroupRef identifies an endpoint group and its site.
type GroupRef struct {
	ID     string
	Key    int64
	Name   string
	Client string
	Site   SiteRef
}

// URIRule is one URI rule of an endpoint group.
type URIRule struct {
	ID       string
	Kind     site.RuleKind
	Pattern  string
	Position int
}

// EndpointGroup is an endpoint group with its rules and live hot-state figures.
type EndpointGroup struct {
	GroupRef
	Description  string
	LowWatermark int
	Rules        []URIRule
	// AvailableIdentities counts identities of the ready queue whose
	// available-at time has passed.
	AvailableIdentities int64
	// BreakerState is closed, open or half_open.
	BreakerState string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// inTx runs fn in a READ COMMITTED transaction, committing when fn returns nil.
func (s *Service) inTx(ctx context.Context, fn func(q *sitesvcdb.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rctx) // no-op after a successful commit
	}()
	if err := fn(sitesvcdb.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// change describes a committed mutation to propagate.
type change struct {
	tenantID    string
	namespaceID string
	siteID      string
	// removedKey is the hot-state key of a deleted site (0 otherwise).
	removedKey int64
	// structural is set when a site or endpoint group was created or deleted,
	// which changes the "_runtime" documents.
	structural bool
}

// propagate invalidates the catalog snapshot of the namespace, then
// synchronizes (or removes, when removedKey > 0) the site's hot state and,
// for structural changes, bumps the runtime versions. The database change is
// already committed, so failures are logged and returned as internal errors
// without undoing it; the periodic catalog reload and hot-state rebuilds
// converge eventually.
func (s *Service) propagate(ctx context.Context, c change) error {
	namespaceID, siteID, removedKey := c.namespaceID, c.siteID, c.removedKey
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), propagateTimeout)
	defer cancel()
	var errs []error
	if err := s.cat.Invalidate(ctx, namespaceID); err != nil {
		s.logger.Error("catalog invalidation after site change failed",
			slog.String("namespace_id", namespaceID), slog.String("site_id", siteID), slog.Any("error", err))
		errs = append(errs, fmt.Errorf("invalidate catalog: %w", err))
	}
	if s.hot != nil {
		var err error
		if removedKey > 0 {
			err = s.hot.RemoveSite(ctx, removedKey)
		} else {
			err = s.hot.SyncSite(ctx, siteID)
		}
		if err != nil {
			s.logger.Error("hot-state sync after site change failed",
				slog.String("namespace_id", namespaceID), slog.String("site_id", siteID), slog.Any("error", err))
			errs = append(errs, fmt.Errorf("sync hot state: %w", err))
		}
	}
	if c.structural {
		// After the hot-state sync, so that re-rendered documents no longer
		// see breakers of deleted endpoint groups.
		if err := s.bumpRuntime(ctx, c.tenantID, namespaceID); err != nil {
			errs = append(errs, fmt.Errorf("bump runtime versions: %w", err))
		}
	}
	if len(errs) > 0 {
		return apperr.Internal(fmt.Errorf("propagate change of site %s: %w", siteID, errors.Join(errs...)))
	}
	return nil
}

// record writes an audit entry for a successful mutation.
func (s *Service) record(ctx context.Context, p *authz.Principal, ref SiteRef, action, kind, id, name string, details map[string]any) {
	s.audit.Record(ctx, audit.FromPrincipal(p, ref.TenantID, ref.NamespaceID, action, kind, id, name, audit.ResultOK, details))
}

// mapDBError converts database errors into application errors, keeping
// application errors unchanged.
func mapDBError(err error, what string) error {
	if err == nil {
		return nil
	}
	mapped := pgstore.MapError(err, what)
	if _, ok := apperr.As(mapped); ok {
		return mapped
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperr.Internal(mapped)
}
