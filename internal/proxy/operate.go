package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
)

// Manual operations.
const (
	OpDisable      = "disable"
	OpEnable       = "enable"
	OpBan          = "ban"
	OpUnban        = "unban"
	OpCooldown     = "cooldown"
	OpQuarantine   = "quarantine"
	OpUnquarantine = "unquarantine"
	OpArchive      = "archive"
	OpRestore      = "restore"
	OpResetStats   = "reset_stats"
)

// Bulk limits and defaults.
const (
	// MaxBulkIDs bounds the proxy IDs of one bulk request.
	MaxBulkIDs = 1000
	// DefaultQuarantineDuration applies to a quarantine without duration (the
	// default of action policies, spec §7).
	DefaultQuarantineDuration = 24 * time.Hour
	maxReasonLength           = 512
)

// State event scopes.
const (
	scopeProxy     = "proxy"
	scopeProxySite = "proxy_site"
)

// Bulk failure reasons.
const (
	reasonNotFound           = string(apperr.ReasonNotFound)
	reasonFailedPrecondition = string(apperr.ReasonFailedPrecondition)
	reasonSiteUnknown        = string(apperr.ReasonSiteUnknown)
)

// OperationRequest is a manual operation applied to proxies.
type OperationRequest struct {
	// Operation is one of the Op* constants.
	Operation string
	// Site restricts cooldown and reset_stats to one site (by name); empty = global.
	Site string
	// Duration of cooldown, ban and quarantine. Required (> 0) for cooldown
	// and ban; durationx.Permanent is accepted for ban only.
	Duration durationx.Duration
	// Reason is recorded in state events and the audit log.
	Reason string
}

// BulkResult summarizes a bulk operation.
type BulkResult struct {
	// Matched counts the proxies found (and visible to the caller).
	Matched int
	// Succeeded counts the proxies the operation was applied to (no-ops included).
	Succeeded int
	Failed    []BulkFailure
}

// BulkFailure describes one proxy the operation could not be applied to.
type BulkFailure struct {
	ID      string
	Reason  string
	Message string
}

// opPlan is the effect of an operation on one proxy.
type opPlan struct {
	row       proxydb.Proxy
	params    proxydb.ProxySetStateParams
	write     bool
	event     bool
	from, to  string
	until     *time.Time
	permanent bool
}

// validateOperation checks an operation request.
func validateOperation(req OperationRequest) error {
	switch req.Operation {
	case OpDisable, OpEnable, OpBan, OpUnban, OpCooldown, OpQuarantine, OpUnquarantine, OpArchive, OpRestore, OpResetStats:
	default:
		return apperr.InvalidArgument("", "unknown operation %q", truncate(req.Operation, 32))
	}
	if req.Site != "" && req.Operation != OpCooldown && req.Operation != OpResetStats {
		return apperr.InvalidArgument("", "site is only allowed for cooldown and reset_stats")
	}
	switch req.Operation {
	case OpCooldown:
		if req.Duration.IsPermanent() || req.Duration <= 0 {
			return apperr.InvalidArgument("", "duration is required for cooldown and must be greater than zero")
		}
	case OpBan:
		if !req.Duration.IsPermanent() && req.Duration <= 0 {
			return apperr.InvalidArgument("", "duration is required for ban and must be greater than zero")
		}
	case OpQuarantine:
		if req.Duration.IsPermanent() || req.Duration < 0 {
			return apperr.InvalidArgument("", "quarantine duration must not be negative or permanent")
		}
	}
	if len(req.Reason) > maxReasonLength {
		return apperr.InvalidArgument("", "reason must be at most %d bytes", maxReasonLength)
	}
	return nil
}

// validateIDs de-duplicates and bounds bulk IDs.
func validateIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, apperr.InvalidArgument("", "ids must not be empty")
	}
	if len(ids) > MaxBulkIDs {
		return nil, apperr.InvalidArgument("", "at most %d ids are allowed", MaxBulkIDs)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, apperr.InvalidArgument("", "ids must not contain empty values")
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// nsGroup is the set of authorized proxies of one namespace.
type nsGroup struct {
	ns  *catalog.Namespace
	ids []string
}

// authorizeBulk loads proxies and groups the authorized ones by namespace.
// IDs that do not exist or are not readable become not_found failures; a
// readable proxy lacking perm fails the whole request.
func (s *Service) authorizeBulk(ctx context.Context, p *authz.Principal, ids []string, perm authz.Permission) ([]nsGroup, BulkResult, error) {
	if err := requireTokenScope(s.cat, p, perm); err != nil {
		return nil, BulkResult{}, err
	}
	rows, err := s.q.ProxyGetMany(ctx, ids)
	if err != nil {
		return nil, BulkResult{}, fmt.Errorf("load proxies: %w", err)
	}
	byID := make(map[string]proxydb.Proxy, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	res := BulkResult{Failed: []BulkFailure{}}
	groups := map[string]*nsGroup{}
	var order []string
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			res.Failed = append(res.Failed, BulkFailure{ID: id, Reason: reasonNotFound, Message: "proxy not found"})
			continue
		}
		ns, err := authorizeRow(s.cat, p, row, perm)
		if apperr.IsNotFound(err) {
			res.Failed = append(res.Failed, BulkFailure{ID: id, Reason: reasonNotFound, Message: "proxy not found"})
			continue
		}
		if err != nil {
			return nil, BulkResult{}, err
		}
		res.Matched++
		g, ok := groups[ns.ID]
		if !ok {
			g = &nsGroup{ns: ns}
			groups[ns.ID] = g
			order = append(order, ns.ID)
		}
		g.ids = append(g.ids, id)
	}
	out := make([]nsGroup, 0, len(order))
	for _, nsID := range order {
		out = append(out, *groups[nsID])
	}
	return out, res, nil
}

// OperateProxies applies a manual operation to proxies (proxy:operate).
// PostgreSQL is updated first (with state_events rows), then the hot state is
// synchronized, cooldown/statistics fields are written to Redis, and
// proxy.state events and audit entries are emitted.
func (s *Service) OperateProxies(ctx context.Context, p *authz.Principal, ids []string, req OperationRequest) (BulkResult, error) {
	if err := validateOperation(req); err != nil {
		return BulkResult{}, err
	}
	ids, err := validateIDs(ids)
	if err != nil {
		return BulkResult{}, err
	}
	groups, res, err := s.authorizeBulk(ctx, p, ids, authz.PermProxyOperate)
	if err != nil {
		return BulkResult{}, err
	}
	var sideErr error
	for _, g := range groups {
		var site *catalog.Site
		if req.Site != "" {
			st, ok := g.ns.Sites[req.Site]
			if !ok {
				for _, id := range g.ids {
					res.Failed = append(res.Failed, BulkFailure{ID: id, Reason: reasonSiteUnknown, Message: "site not found"})
				}
				continue
			}
			site = st
		}
		plans, failed, err := s.applyOperation(ctx, p, g, site, req)
		if err != nil {
			return BulkResult{}, err
		}
		res.Failed = append(res.Failed, failed...)
		res.Succeeded += len(plans)
		if err := s.afterOperation(ctx, p, g.ns, site, req, plans); err != nil && sideErr == nil {
			sideErr = err
		}
	}
	if sideErr != nil {
		return res, apperr.Internal(sideErr)
	}
	return res, nil
}

// applyOperation locks and updates the proxies of one namespace in a transaction.
func (s *Service) applyOperation(ctx context.Context, p *authz.Principal, g nsGroup, site *catalog.Site, req OperationRequest) ([]opPlan, []BulkFailure, error) {
	var plans []opPlan
	var failed []BulkFailure
	err := inTx(ctx, s.pool, func(q *proxydb.Queries) error {
		plans, failed = nil, nil
		rows, err := q.ProxyGetManyForUpdate(ctx, g.ids)
		if err != nil {
			return fmt.Errorf("lock proxies: %w", err)
		}
		now := s.now()
		var updates []proxydb.ProxySetStateParams
		var stateEvents []proxydb.ProxyInsertStateEventsParams
		found := make(map[string]bool, len(rows))
		for _, row := range rows {
			found[row.ID] = true
			if row.NamespaceID != g.ns.ID {
				failed = append(failed, BulkFailure{ID: row.ID, Reason: reasonNotFound, Message: "proxy not found"})
				continue
			}
			plan, msg := planOperation(row, req, site != nil, now)
			if msg != "" {
				failed = append(failed, BulkFailure{ID: row.ID, Reason: reasonFailedPrecondition, Message: msg})
				continue
			}
			plans = append(plans, plan)
			if plan.write {
				updates = append(updates, plan.params)
			}
			if plan.event {
				stateEvents = append(stateEvents, stateEventRow(g.ns, site, p, req, plan, now))
			}
		}
		for _, id := range g.ids {
			if !found[id] {
				failed = append(failed, BulkFailure{ID: id, Reason: reasonNotFound, Message: "proxy not found"})
			}
		}
		if err := execSetState(ctx, q, updates); err != nil {
			return err
		}
		if len(stateEvents) > 0 {
			if _, err := q.ProxyInsertStateEvents(ctx, stateEvents); err != nil {
				return fmt.Errorf("insert proxy state events: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return plans, failed, nil
}

// execSetState runs the batched state updates.
func execSetState(ctx context.Context, q *proxydb.Queries, updates []proxydb.ProxySetStateParams) error {
	if len(updates) == 0 {
		return nil
	}
	var batchErr error
	br := q.ProxySetState(ctx, updates)
	br.Exec(func(_ int, err error) {
		if err != nil && batchErr == nil {
			batchErr = err
		}
	})
	if err := br.Close(); err != nil && batchErr == nil {
		batchErr = err
	}
	if batchErr != nil {
		return fmt.Errorf("update proxy states: %w", batchErr)
	}
	return nil
}

// stateEventRow builds the state_events row of a plan.
func stateEventRow(ns *catalog.Namespace, site *catalog.Site, p *authz.Principal, req OperationRequest, plan opPlan, now time.Time) proxydb.ProxyInsertStateEventsParams {
	scope, siteID := scopeProxy, ""
	if site != nil {
		scope, siteID = scopeProxySite, site.ID
	}
	return proxydb.ProxyInsertStateEventsParams{
		ID: idgen.New(idgen.StateEvent), CreatedAt: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: siteID,
		SubjectKind: auditResourceKind, SubjectID: plan.row.ID, FromState: plan.from, ToState: plan.to,
		Action: eventAction(req.Operation), Scope: scope, Until: plan.until, Permanent: plan.permanent,
		Actor: p.Actor(), Reason: req.Reason, Details: json.RawMessage(`{}`),
	}
}

// eventAction maps an operation to its state event action.
func eventAction(op string) string {
	if op == OpUnquarantine {
		return "activate"
	}
	return op
}

// planOperation computes the effect of req on row. A non-empty message means
// the operation is not applicable to the proxy's state.
func planOperation(row proxydb.Proxy, req OperationRequest, siteScoped bool, now time.Time) (opPlan, string) {
	plan := opPlan{row: row, from: row.State, to: row.State}
	plan.params = proxydb.ProxySetStateParams{
		ID: row.ID, State: row.State, StateReason: row.StateReason, StateChangedAt: row.StateChangedAt,
		BanUntil: row.BanUntil, CooldownUntil: row.CooldownUntil,
		ConsecutiveCheckFailures: row.ConsecutiveCheckFailures, NextCheckAt: row.NextCheckAt, UpdatedAt: now,
	}
	transition := func(to string) {
		plan.to, plan.write, plan.event = to, true, true
		plan.params.State, plan.params.StateReason, plan.params.StateChangedAt = to, req.Reason, now
		if to != StateBanned && to != StateQuarantined {
			// ban_until only holds the end of a ban or a quarantine.
			plan.params.BanUntil = nil
		}
	}
	activate := func() {
		transition(StateActive)
		plan.params.NextCheckAt = now
	}
	until := func(d time.Duration) *time.Time {
		t := now.Add(d)
		return &t
	}
	retired := row.State == StateRetired
	const retiredMsg = "proxy is retired; restore it first"

	switch req.Operation {
	case OpDisable:
		if retired {
			return plan, retiredMsg
		}
		if row.State != StateDisabled {
			transition(StateDisabled)
		}
	case OpEnable:
		switch row.State {
		case StateDisabled, StateDead:
			activate()
			plan.params.ConsecutiveCheckFailures = 0
		case StateActive:
		default:
			return plan, fmt.Sprintf("proxy is %s; use unban, unquarantine or restore", row.State)
		}
	case OpBan:
		if retired {
			return plan, retiredMsg
		}
		transition(StateBanned)
		if req.Duration.IsPermanent() {
			plan.params.BanUntil, plan.permanent = nil, true
		} else {
			plan.params.BanUntil = until(req.Duration.Std())
			plan.until = plan.params.BanUntil
		}
	case OpUnban:
		if row.State != StateBanned {
			return plan, "proxy is not banned"
		}
		activate()
	case OpCooldown:
		if retired {
			return plan, retiredMsg
		}
		plan.until, plan.event = until(req.Duration.Std()), true
		if !siteScoped {
			plan.params.CooldownUntil, plan.write = plan.until, true
		}
	case OpQuarantine:
		switch row.State {
		case StateActive, StateDisabled, StateDead:
			transition(StateQuarantined)
		case StateQuarantined:
			// Quarantining a quarantined proxy replaces the end of the
			// quarantine: a lifecycle change, so the change time moves too (the
			// hot-state synchronization keeps newer Redis-first lifecycle
			// fields, compared by this time).
			plan.write, plan.event = true, true
			plan.params.StateChangedAt = now
		default:
			return plan, fmt.Sprintf("proxy is %s and cannot be quarantined", row.State)
		}
		d := req.Duration.Std()
		if d == 0 {
			d = DefaultQuarantineDuration
		}
		// proxies have no quarantine_until column: like the action track's
		// state writer, ban_until holds the end of the quarantine, which the
		// ban/quarantine expiry job (action) releases to active.
		plan.until = until(d)
		plan.params.BanUntil = plan.until
	case OpUnquarantine:
		if row.State != StateQuarantined {
			return plan, "proxy is not quarantined"
		}
		activate()
	case OpArchive:
		if !retired {
			transition(StateRetired)
		}
	case OpRestore:
		if !retired {
			return plan, "proxy is not retired"
		}
		activate()
		plan.params.BanUntil, plan.params.CooldownUntil, plan.params.ConsecutiveCheckFailures = nil, nil, 0
	case OpResetStats:
		plan.event = true
		if !siteScoped && row.ConsecutiveCheckFailures != 0 {
			plan.params.ConsecutiveCheckFailures, plan.write = 0, true
		}
	}
	return plan, ""
}

// afterOperation runs the side effects of committed plans of one namespace.
func (s *Service) afterOperation(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site *catalog.Site, req OperationRequest, plans []opPlan) error {
	if len(plans) == 0 {
		return nil
	}
	sctx, cancel := detached(ctx)
	defer cancel()
	refs := make([]hotRef, len(plans))
	ids := make([]string, len(plans))
	for i, pl := range plans {
		refs[i], ids[i] = hotRef{id: pl.row.ID, key: pl.row.Hkey}, pl.row.ID
	}
	var errs []error
	if req.Operation != OpResetStats && site == nil {
		errs = append(errs, s.syncProxies(sctx, ns.ID, ids))
	}
	sites := sortedSites(ns)
	if site != nil {
		sites = []*catalog.Site{site}
	}
	switch req.Operation {
	case OpCooldown:
		field := fieldGlobalCooldown
		if site != nil {
			field = fieldCooldown
		}
		errs = append(errs, s.writeCooldowns(sctx, sites, field, plans))
	case OpResetStats:
		errs = append(errs, resetStats(sctx, s.rdb, s.keys, sites, refs))
	}

	var evs []StateEventData
	details := map[string]any{"reason": req.Reason}
	if req.Site != "" {
		details["site"] = req.Site
	}
	if req.Duration != 0 {
		details["duration"] = req.Duration.String()
	}
	for _, pl := range plans {
		if pl.event {
			ev := StateEventData{SubjectID: pl.row.ID, From: pl.from, To: pl.to, Action: eventAction(req.Operation), Until: pl.until, Reason: req.Reason}
			if site != nil {
				ev.SiteID = site.ID
			}
			evs = append(evs, ev)
		}
		s.record(sctx, p, ns, "proxy."+req.Operation, pl.row.ID, pl.row.DisplayUrl, audit.ResultOK, details)
	}
	if err := publishState(sctx, s.bus, ns, evs); err != nil {
		s.logger.Warn("proxy operation: publish events failed", slog.String("namespace_id", ns.ID), slog.Any("error", err))
	}
	if err := errors.Join(errs...); err != nil {
		s.logger.Error("proxy operation: hot state update failed", slog.String("namespace_id", ns.ID),
			slog.String("operation", req.Operation), slog.Any("error", err))
		return err
	}
	return nil
}

// writeCooldowns writes cooldown fields; all plans of one operation share the same until.
func (s *Service) writeCooldowns(ctx context.Context, sites []*catalog.Site, field string, plans []opPlan) error {
	byUntil := map[int64][]hotRef{}
	for _, pl := range plans {
		if pl.until == nil {
			continue
		}
		ms := pl.until.UnixMilli()
		byUntil[ms] = append(byUntil[ms], hotRef{id: pl.row.ID, key: pl.row.Hkey})
	}
	for ms, refs := range byUntil {
		if err := writeCooldown(ctx, s.rdb, s.keys, sites, field, time.UnixMilli(ms), refs); err != nil {
			return err
		}
	}
	return nil
}
