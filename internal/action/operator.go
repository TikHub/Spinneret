package action

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/action/actiondb"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Operator timeouts.
const (
	// dbTimeout bounds one PostgreSQL transaction.
	dbTimeout = 30 * time.Second
	// hotTimeout bounds the hot-state side effects that follow a commit.
	hotTimeout = 30 * time.Second
)

// Operator implements manual identity and account operations, rollback of
// automatic actions and ban/quarantine expiry. Changes are written to
// PostgreSQL first (with their state events, in the same transaction) and then
// pushed to Redis: apply.lua for the immediate effect (at the commit time, so
// a newer Redis-first change is not overwritten) and HotSyncer for the
// authoritative materialization. It is safe for concurrent use.
type Operator struct {
	pool   *pgxpool.Pool
	cat    catalog.Catalog
	hot    HotSyncer
	writer *StateWriter
	audit  audit.Recorder
	notify notifier
	apply  *applier
	logger *slog.Logger
	now    func() time.Time

	// retry holds expiry releases whose hot-state push failed after the
	// PostgreSQL commit; every expiry pass retries them (see expiry.go).
	retry *hotRetry
	// lastExpiry is the start of the previous expiry pass of this instance
	// (Unix ns, 0 before the first pass).
	lastExpiry atomic.Int64
}

// NewOperator creates an operator. hot, writer, auditRec, bus and logger may
// be nil (the corresponding side effects are skipped).
func NewOperator(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot HotSyncer, writer *StateWriter, auditRec audit.Recorder, bus events.Bus, logger *slog.Logger) *Operator {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if auditRec == nil {
		auditRec = audit.Nop{}
	}
	logger = logger.With("component", "action.operator")
	return &Operator{
		pool:   pool,
		cat:    cat,
		hot:    hot,
		writer: writer,
		audit:  auditRec,
		notify: notifier{bus: bus, logger: logger},
		apply:  newApplier(rdb, keys),
		logger: logger,
		now:    func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
		retry:  newHotRetry(maxHotRetryEntries),
	}
}

// identityRow is an identity loaded for an operation.
type identityRow = actiondb.ActionLoadIdentitiesRow

// OperateIdentities applies one manual operation to identities of ns. Every
// identity is authorized individually (identity:operate on its site); per-ID
// problems are reported in BulkResult.Failed and only request-level problems
// (invalid operation, unknown endpoint group, database unavailable) return an
// error.
func (o *Operator) OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req OperationRequest) (BulkResult, error) {
	if p == nil {
		return BulkResult{}, errUnauthenticated()
	}
	if ns == nil {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	spec, err := newOpSpec(req)
	if err != nil {
		return BulkResult{}, err
	}
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at least one identity id is required")
	}
	if len(ids) > MaxOperationIDs {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d identity ids per call", MaxOperationIDs)
	}
	if spec.scope == scopeEndpoint {
		if !namespaceHasGroup(ns, req.EndpointGroupID) {
			return BulkResult{}, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group %q not found", req.EndpointGroupID)
		}
	}
	rows, err := o.loadIdentities(ctx, ns.ID, ids)
	if err != nil {
		return BulkResult{}, err
	}
	res := &BulkResult{Matched: len(ids)}
	groups := o.groupBySite(p, ns, ids, rows, spec, res)
	now := o.now()
	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			o.recordAudit(ctx, p, ns, "identity.operate", "identity", ids, req, res)
			return *res, err
		}
		o.runGroup(ctx, p, ns, g, spec, req, now, res)
	}
	o.recordAudit(ctx, p, ns, "identity.operate", "identity", ids, req, res)
	return *res, nil
}

// operationGroup is the set of identities of one site and client handled together.
type operationGroup struct {
	site   *catalog.Site
	client string
	rows   []identityRow
}

// groupBySite authorizes identities and validates transitions, recording
// failures, and returns the remaining identities grouped by site and client
// in request order.
func (o *Operator) groupBySite(p *authz.Principal, ns *catalog.Namespace, ids []string, rows []identityRow, spec opSpec, res *BulkResult) []*operationGroup {
	byID := make(map[string]identityRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	allowed := map[string]error{}
	index := map[[2]string]*operationGroup{}
	var groups []*operationGroup
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			res.fail(id, FailureNotFound, "identity not found")
			continue
		}
		s, ok := ns.SitesByID[row.SiteID]
		if !ok {
			res.fail(id, FailureNotFound, "identity site not found")
			continue
		}
		permErr, checked := allowed[s.ID]
		if !checked {
			permErr = p.Require(authz.PermIdentityOperate, siteResource(ns, s))
			allowed[s.ID] = permErr
		}
		if permErr != nil {
			res.fail(id, FailurePermissionDenied, permErr.Error())
			continue
		}
		if reason, msg := spec.check(s, row); reason != "" {
			res.fail(id, reason, msg)
			continue
		}
		k := [2]string{s.ID, row.Client}
		g := index[k]
		if g == nil {
			g = &operationGroup{site: s, client: row.Client}
			index[k] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, row)
	}
	return groups
}

// runGroup executes the operation on one site/client group in chunks.
func (o *Operator) runGroup(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, g *operationGroup, spec opSpec, req OperationRequest, now time.Time, res *BulkResult) {
	for start := 0; start < len(g.rows); start += dbChunk {
		chunk := g.rows[start:min(start+dbChunk, len(g.rows))]
		var err error
		switch spec.kind {
		case kindLifecycle:
			err = o.transitionChunk(ctx, p, ns, g.site, g.client, chunk, spec, req, now, res)
		case kindCooldown:
			err = o.cooldownChunk(ctx, p, ns, g.site, chunk, spec, req, now, res)
		case kindResetStats:
			err = o.resetStatsChunk(ctx, p, ns, g.site, chunk, spec, req, now, res)
		}
		if err != nil {
			o.logger.Error("identity operation failed", "operation", spec.op, "site_id", g.site.ID, "error", err)
			for _, r := range chunk {
				res.fail(r.ID, FailureInternal, "operation failed, retry later")
			}
		}
	}
}

// loadIdentities loads identities of namespace nsID by ID.
func (o *Operator) loadIdentities(ctx context.Context, nsID string, ids []string) ([]identityRow, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := actiondb.New(o.pool).ActionLoadIdentities(ctx, actiondb.ActionLoadIdentitiesParams{NamespaceID: nsID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("load identities: %w", err)
	}
	return rows, nil
}

// inTx runs fn in a READ COMMITTED transaction with the operator timeout.
// The callback receives the transaction context and must use it for every query.
func (o *Operator) inTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error) error {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tx, err := o.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()
	if err := fn(ctx, tx, actiondb.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// writeEvents inserts state events synchronously and falls back to the
// asynchronous writer when the insert fails.
func (o *Operator) writeEvents(ctx context.Context, changes []StateChange) {
	if len(changes) == 0 {
		return
	}
	// IDs are fixed first, so a deferred event whose insert did commit is
	// not stored twice by the state writer.
	rows := make([]StateChange, len(changes))
	now := time.Now()
	for i, c := range changes {
		c.normalize(now)
		rows[i] = c
	}
	ictx, cancel := detached(ctx)
	defer cancel()
	if _, err := insertStateEvents(ictx, o.pool, rows); err != nil {
		o.logger.Warn("insert state events, deferring to state writer", "error", err, "events", len(rows))
		if o.writer != nil {
			for _, c := range rows {
				o.writer.Enqueue(c)
			}
		}
	}
}

// detached returns a context for side effects that must complete after a
// PostgreSQL commit even when the caller's context is canceled.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), hotTimeout)
}

// syncIdentities runs the hot syncer for changed identities of a site. Errors
// are logged and returned (callers that cannot retry ignore them).
func (o *Operator) syncIdentities(ctx context.Context, siteID string, ids []string, opts SyncOptions) error {
	if o.hot == nil || len(ids) == 0 {
		return nil
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := o.hot.SyncIdentities(ctx, siteID, ids, opts); err != nil {
		o.logger.Error("hot sync identities failed", "site_id", siteID, "identities", len(ids), "error", err)
		return fmt.Errorf("sync identities of site %s: %w", siteID, err)
	}
	return nil
}

// syncAccounts runs the hot syncer for changed accounts of a site; errors are
// logged and returned.
func (o *Operator) syncAccounts(ctx context.Context, siteID string, ids []string) error {
	if o.hot == nil || len(ids) == 0 {
		return nil
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := o.hot.SyncAccounts(ctx, siteID, ids); err != nil {
		o.logger.Error("hot sync accounts failed", "site_id", siteID, "accounts", len(ids), "error", err)
		return fmt.Errorf("sync accounts of site %s: %w", siteID, err)
	}
	return nil
}

// syncProxies runs the hot syncer for changed proxies of a namespace; errors
// are logged and returned.
func (o *Operator) syncProxies(ctx context.Context, nsID string, ids []string) error {
	if o.hot == nil || len(ids) == 0 {
		return nil
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := o.hot.SyncProxies(ctx, nsID, ids); err != nil {
		o.logger.Error("hot sync proxies failed", "namespace_id", nsID, "proxies", len(ids), "error", err)
		return fmt.Errorf("sync proxies of namespace %s: %w", nsID, err)
	}
	return nil
}

// recordAudit writes one audit entry summarizing an operation.
func (o *Operator) recordAudit(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, kind string, ids []string, req OperationRequest, res *BulkResult) {
	resourceID := ""
	if len(ids) == 1 {
		resourceID = ids[0]
	}
	details := map[string]any{
		"operation": req.Operation,
		"matched":   res.Matched,
		"succeeded": res.Succeeded,
		"failed":    len(res.Failed),
	}
	if req.Scope != "" {
		details["scope"] = req.Scope
	}
	if req.EndpointGroupID != "" {
		details["endpoint_group_id"] = req.EndpointGroupID
	}
	if req.Duration != 0 {
		details["duration"] = req.Duration.String()
	}
	if req.Reason != "" {
		details["reason"] = req.Reason
	}
	if req.ResetFailures || req.ResetHealth {
		details["reset_failures"], details["reset_health"] = req.ResetFailures, req.ResetHealth
	}
	if len(ids) > 1 {
		details["ids"] = ids[:min(len(ids), 50)]
	}
	o.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, action, kind, resourceID, "", res.auditResult(), details))
}

func (r *BulkResult) fail(id, reason, message string) {
	r.Failed = append(r.Failed, BulkFailure{ID: id, Reason: reason, Message: message})
}

// auditResult is the audit result of a bulk operation: ok when anything
// succeeded (or nothing failed), denied when every item failed authorization,
// error otherwise.
func (r *BulkResult) auditResult() string {
	if r.Succeeded > 0 || len(r.Failed) == 0 {
		return audit.ResultOK
	}
	for _, f := range r.Failed {
		if f.Reason != FailurePermissionDenied {
			return audit.ResultError
		}
	}
	return audit.ResultDenied
}

// errUnauthenticated is returned when an operation has no principal.
func errUnauthenticated() error {
	return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
}

func siteResource(ns *catalog.Namespace, s *catalog.Site) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, SiteID: s.ID, SiteName: s.Name}
}

func namespaceHasGroup(ns *catalog.Namespace, groupID string) bool {
	for _, s := range ns.SitesByID {
		if _, ok := s.GroupsByID[groupID]; ok {
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
