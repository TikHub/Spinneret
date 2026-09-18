// Package policysvc implements policy administration on top of PostgreSQL:
// drafts, published versions, rollbacks, bindings, resolution, the rule
// debugger and the installation of the built-in default policies of a new
// namespace.
//
// Every mutation runs in one transaction, then invalidates the namespace
// catalog snapshot, resynchronizes the hot state of the sites whose eligible
// identity types may have changed (rotation policies only) and publishes a
// policy.published console event on events.NamespaceChannel(namespaceID) with
// data {"policy_id","name","kind","version","action","binding_id","site_id",
// "actor"}, where action is one of publish, rollback, delete, bind, unbind.
package policysvc

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// Limits and timeouts.
const (
	// MaxYAMLBytes bounds policy YAML documents.
	MaxYAMLBytes = 1 << 20
	// MaxCommentLength bounds publish comments.
	MaxCommentLength = 1024
	// MaxSearchLength bounds the policy name search string.
	MaxSearchLength = 256
	// DefaultPageSize and MaxPageSize bound list pages.
	DefaultPageSize = 50
	MaxPageSize     = 500

	// maxBindingRows bounds the bindings loaded for one namespace or page.
	maxBindingRows = 20_000
	// maxPublishedScan bounds the published policies of one kind scanned
	// for extends depth checks.
	maxPublishedScan = 100_000
	// operationTimeout bounds one service call.
	operationTimeout = 30 * time.Second
	// postCommitTimeout bounds catalog invalidation and hot-state syncs after
	// a committed mutation.
	postCommitTimeout = 2 * time.Minute
	// syncConcurrency bounds the concurrent hot-state syncs of one mutation.
	syncConcurrency = 4
	// rollbackTimeout bounds the rollback of a failed transaction.
	rollbackTimeout = 5 * time.Second
)

// Audit actions and resource kinds.
const (
	AuditPolicyCreate    = "policy.create"
	AuditPolicySaveDraft = "policy.save_draft"
	AuditPolicyPublish   = "policy.publish"
	AuditPolicyRollback  = "policy.rollback"
	AuditPolicyDelete    = "policy.delete"
	AuditBindingSet      = "policy.bind"
	AuditBindingDelete   = "policy.unbind"

	resourcePolicy        = "policy"
	resourcePolicyBinding = "policy_binding"
)

// Event actions carried in policy.published events.
const (
	EventActionPublish  = "publish"
	EventActionRollback = "rollback"
	EventActionDelete   = "delete"
	EventActionBind     = "bind"
	EventActionUnbind   = "unbind"
)

// HotSyncer rematerializes the Redis hot state of a site (provided by
// hotstate.Syncer).
type HotSyncer interface {
	SyncSite(ctx context.Context, siteID string) error
}

// Option customizes a Service.
type Option func(*Service)

// WithEventBus publishes policy.published console events on bus.
func WithEventBus(bus events.Bus) Option {
	return func(s *Service) { s.bus = bus }
}

// Service is the policy administration service. It is safe for concurrent use.
type Service struct {
	pool   *pgxpool.Pool
	cat    catalog.Catalog
	hot    HotSyncer
	audit  audit.Recorder
	bus    events.Bus
	logger *slog.Logger
}

// NewService creates the service. hot may be nil (no hot-state syncs), rec
// nil discards audit entries and logger nil uses slog.Default().
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot HotSyncer, rec audit.Recorder, logger *slog.Logger, opts ...Option) *Service {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{pool: pool, cat: cat, hot: hot, audit: rec, logger: logger}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// PublishedEvent is the data of a policy.published event.
type PublishedEvent struct {
	PolicyID  string `json:"policy_id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
	Action    string `json:"action"`
	BindingID string `json:"binding_id,omitempty"`
	SiteID    string `json:"site_id,omitempty"`
	Actor     string `json:"actor"`
}

// effects describes what must happen after a committed mutation.
type effects struct {
	ns        *catalog.Namespace
	kind      policy.Kind
	syncAll   bool
	syncSites []string
	event     *PublishedEvent
}

// addSite records a site whose hot state must be resynchronized. A nil or
// empty siteID means a namespace-level binding, which affects every site.
func (e *effects) addSite(siteID *string) {
	if siteID == nil || *siteID == "" {
		e.syncAll = true
		return
	}
	if !slices.Contains(e.syncSites, *siteID) {
		e.syncSites = append(e.syncSites, *siteID)
	}
}

// apply invalidates the catalog, publishes the console event and resyncs the
// hot state of rotation-affected sites (at most syncConcurrency at a time).
// Failures are logged: the mutation is already committed and the catalog
// reloads periodically.
func (s *Service) apply(ctx context.Context, e effects) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), postCommitTimeout)
	defer cancel()
	if err := s.cat.Invalidate(ctx, e.ns.ID); err != nil {
		s.logger.Error("policy: invalidate catalog", slog.String("namespace_id", e.ns.ID), slog.Any("error", err))
	}
	s.publishEvent(ctx, e)
	if s.hot == nil || e.kind != policy.KindRotation || (!e.syncAll && len(e.syncSites) == 0) {
		return
	}
	var g errgroup.Group
	g.SetLimit(syncConcurrency)
	for _, siteID := range s.sitesToSync(e) {
		g.Go(func() error {
			if err := s.hot.SyncSite(ctx, siteID); err != nil {
				s.logger.Error("policy: sync site hot state",
					slog.String("namespace_id", e.ns.ID), slog.String("site_id", siteID), slog.Any("error", err))
			}
			return nil
		})
	}
	_ = g.Wait() // workers log their own failures and never return errors
}

// publishEvent publishes the policy.published console event of e, if any.
func (s *Service) publishEvent(ctx context.Context, e effects) {
	if s.bus == nil || e.event == nil {
		return
	}
	data, err := json.Marshal(e.event)
	if err != nil {
		s.logger.Error("policy: encode event", slog.Any("error", err))
		return
	}
	ev := events.Event{
		Type:        events.TypePolicyPublished,
		TenantID:    e.ns.TenantID,
		NamespaceID: e.ns.ID,
		SiteID:      e.event.SiteID,
		At:          time.Now().UTC(),
		Data:        data,
	}
	if err := s.bus.Publish(ctx, events.NamespaceChannel(e.ns.ID), ev); err != nil {
		s.logger.Warn("policy: publish event", slog.String("namespace_id", e.ns.ID), slog.Any("error", err))
	}
}

// sitesToSync returns the sorted site IDs to resync, reading the refreshed
// snapshot for namespace-wide changes.
func (s *Service) sitesToSync(e effects) []string {
	if !e.syncAll {
		out := slices.Clone(e.syncSites)
		slices.Sort(out)
		return out
	}
	ns := e.ns
	if fresh, ok := s.cat.Namespace(e.ns.ID); ok {
		ns = fresh
	}
	out := make([]string, 0, len(ns.SitesByID))
	for id := range ns.SitesByID {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// record writes an audit entry.
func (s *Service) record(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, kind, id, name, result string, details map[string]any) {
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, action, kind, id, name, result, details))
}

// authorize checks perm on r and records a denied audit entry for mutations.
func (s *Service) authorize(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, perm authz.Permission, r authz.Resource, action, kind, id, name string) error {
	if err := p.Require(perm, r); err != nil {
		if action != "" && p != nil {
			s.record(ctx, p, ns, action, kind, id, name, audit.ResultDenied, map[string]any{"permission": string(perm)})
		}
		return err
	}
	return nil
}

// namespace returns the snapshot of a namespace referenced by a stored row.
func (s *Service) namespace(id string, what string) (*catalog.Namespace, error) {
	ns, ok := s.cat.Namespace(id)
	if !ok {
		return nil, apperr.NotFound("%s not found", what)
	}
	return ns, nil
}

// withTimeout bounds one service call.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, operationTimeout)
}

// requirePrincipal rejects a nil principal.
func requirePrincipal(p *authz.Principal) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	return nil
}
