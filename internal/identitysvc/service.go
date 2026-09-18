package identitysvc

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Limits and defaults of the identity administration service.
const (
	// MaxPayloadVersions is the number of payload versions kept per identity.
	MaxPayloadVersions = 5
	// ImportMaxRows is the maximum number of rows of one import.
	ImportMaxRows = 50_000
	// ImportMaxBytes is the maximum size of one import.
	ImportMaxBytes int64 = 32 << 20
	// ImportChunkSize is the number of rows written per transaction.
	ImportChunkSize = 500
	// MaxImportFailures bounds the failures reported by one import; further
	// failures are summarized in one extra entry with line 0.
	MaxImportFailures = 1000
	// MaxBulkIdentities is the maximum number of identities a filter-based
	// bulk operation may affect.
	MaxBulkIdentities = 100_000
	// OperateChunkSize is the number of identity IDs passed to one
	// Operator.OperateIdentities call.
	OperateChunkSize = 1000
	// MaxOperateIDs is the maximum number of explicit IDs of one operation.
	MaxOperateIDs = 1000
	// MaxRevertIdentityIDs bounds the affected identity IDs returned by a revert.
	MaxRevertIdentityIDs = 1000
	// RecentEventsLimit is the number of state events GetIdentity returns.
	RecentEventsLimit = 20
	// DefaultBaselineScore is the health score assumed for identities that
	// have no hot-state snapshot yet (the default action policy baseline).
	DefaultBaselineScore = 70.0
	// MaxStateEventsPublished bounds the identity.state console events one
	// request publishes; the console refetches lists periodically anyway.
	MaxStateEventsPublished = 100
)

// postCommitTimeout bounds hot-state synchronization, catalog invalidation
// and event publishing after a commit.
const postCommitTimeout = 2 * time.Minute

// SyncOptions controls how hot state is materialized. It mirrors
// hotstate.SyncOptions; the server wiring adapts the types.
type SyncOptions struct {
	ResetHealth   bool
	ResetFailures bool
}

// HotSyncer materializes identities and accounts in Redis after PostgreSQL
// changes (provided by hotstate).
type HotSyncer interface {
	SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts SyncOptions) error
	RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error
	SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error
}

// OperationRequest is a manual operation on identities or an account. It
// mirrors action.OperationRequest.
type OperationRequest struct {
	Operation       string
	Scope           string
	EndpointGroupID string
	Duration        durationx.Duration
	Reason          string
	ResetFailures   bool
	ResetHealth     bool
}

// RevertRequest selects automatic actions to revert. It mirrors
// action.RevertRequest.
type RevertRequest struct {
	SiteID        string
	PolicyID      string
	Rule          string
	Actions       []string
	From, To      time.Time
	ResetFailures bool
	ResetHealth   bool
	DryRun        bool
}

// BulkFailure describes one item of a bulk operation that was not applied.
type BulkFailure struct {
	ID      string
	Reason  string
	Message string
}

// BulkResult summarizes a bulk operation. It mirrors action.BulkResult.
type BulkResult struct {
	Matched   int
	Succeeded int
	Failed    []BulkFailure
}

// merge adds other to r.
func (r *BulkResult) merge(other BulkResult) {
	r.Matched += other.Matched
	r.Succeeded += other.Succeeded
	r.Failed = append(r.Failed, other.Failed...)
}

// Operator applies manual operations and reverts (provided by action). The
// server wiring adapts the request and result types.
type Operator interface {
	OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req OperationRequest) (BulkResult, error)
	OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req OperationRequest) (BulkResult, error)
	RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req RevertRequest) (BulkResult, []string, error)
}

// EndpointHotState is the live identity × endpoint group state. It mirrors
// the proto EndpointHotState.
type EndpointHotState struct {
	EndpointGroup       string
	EndpointGroupID     string
	Client              string
	Score               float64
	Samples             int
	ConsecutiveFailures int
	CooldownUntil       time.Time
	ReuseUntil          time.Time
	LastUsedAt          time.Time
	AvailableAt         time.Time
	InReadyQueue        bool
}

// HotState is the live Redis state of an identity. It mirrors the proto
// IdentityHotState (and hotstate.IdentityHot); zero times mean unset.
type HotState struct {
	Present              bool
	State                string
	ActiveLeases         int
	SiteCooldownUntil    time.Time
	SiteReuseUntil       time.Time
	ExclusiveUntil       time.Time
	BoundProxyID         string
	GlobalScore          float64
	GlobalSamples        int
	Groups               []EndpointHotState
	AccountCooldownUntil time.Time
}

// HotReader reads the live hot state of an identity (provided by hotstate).
type HotReader interface {
	IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (HotState, error)
}

// Service implements identity administration.
type Service struct {
	pool   *pgxpool.Pool
	cipher *vault.Cipher
	pepper []byte
	cat    catalog.Catalog
	hot    HotSyncer
	ops    Operator
	audit  audit.Recorder
	bus    events.Bus
	logger *slog.Logger
	now    func() time.Time
}

// NewService creates the identity administration service. pepper is the
// dedupe pepper (vault.SystemKey "dedupe_pepper", 32 bytes); methods that hash
// payloads fail with an internal error when it is shorter than 16 bytes.
// audit, bus and logger may be nil (no-op audit, no events, default logger).
func NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, cat catalog.Catalog, hot HotSyncer,
	ops Operator, auditRec audit.Recorder, bus events.Bus, logger *slog.Logger) *Service {
	if auditRec == nil {
		auditRec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool:   pool,
		cipher: cipher,
		pepper: slices.Clone(pepper),
		cat:    cat,
		hot:    hot,
		ops:    ops,
		audit:  auditRec,
		bus:    bus,
		logger: logger.With(slog.String("component", "identitysvc")),
		now:    time.Now,
	}
}

// detached returns a context for post-commit work that survives the caller's
// cancellation but keeps its values, bounded by postCommitTimeout.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), postCommitTimeout)
}

// invalidateCatalog reloads the namespace snapshot after an identity type
// change. Failures are logged: peers also reload periodically.
func (s *Service) invalidateCatalog(ctx context.Context, namespaceID string) {
	if s.cat == nil {
		return
	}
	cctx, cancel := detached(ctx)
	defer cancel()
	if err := s.cat.Invalidate(cctx, namespaceID); err != nil {
		s.logger.Warn("catalog invalidation failed", slog.String("namespace_id", namespaceID), slog.Any("error", err))
	}
}

// syncGroup is a set of identities synchronized with the same options.
type syncGroup struct {
	ids  []string
	opts SyncOptions
}

// syncHot materializes identities and accounts of one site. It returns false
// when any synchronization failed (already logged).
func (s *Service) syncHot(ctx context.Context, siteID string, groups []syncGroup, accountIDs []string) bool {
	if s.hot == nil {
		return true
	}
	cctx, cancel := detached(ctx)
	defer cancel()
	ok := true
	if len(accountIDs) > 0 {
		if err := s.hot.SyncAccounts(cctx, siteID, dedupe(accountIDs)); err != nil {
			ok = false
			s.logger.Error("hot-state account sync failed",
				slog.String("site_id", siteID), slog.Int("accounts", len(accountIDs)), slog.Any("error", err))
		}
	}
	for _, g := range groups {
		if len(g.ids) == 0 {
			continue
		}
		if err := s.hot.SyncIdentities(cctx, siteID, g.ids, g.opts); err != nil {
			ok = false
			s.logger.Error("hot-state identity sync failed",
				slog.String("site_id", siteID), slog.Int("identities", len(g.ids)), slog.Any("error", err))
		}
	}
	return ok
}

// dedupe returns the distinct non-empty values of in, in first-seen order.
func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
