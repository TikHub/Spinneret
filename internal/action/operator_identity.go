package action

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/action/actiondb"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// changedIdentity is an identity whose state was changed in PostgreSQL.
type changedIdentity struct {
	ID, Client, TypeID string
	Key                int64
	From, To           string
}

// hotTransition describes the Redis side of committed identity transitions.
type hotTransition struct {
	// at is the commit time of the transitions (their state_changed_at).
	at        time.Time
	to        string
	until     *time.Time
	permanent bool
	resetFail bool
	resetHP   bool
	recordBan bool
}

// transitionChunk changes the lifecycle state of up to dbChunk identities of
// one site and client.
func (o *Operator) transitionChunk(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, client string, rows []identityRow, spec opSpec, req OperationRequest, now time.Time, res *BulkResult) error {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	until := spec.until(s, client, now)
	params := actiondb.ActionTransitionIdentitiesParams{
		ToState: spec.to, Reason: req.Reason, ChangedAt: now, Ids: ids, SiteID: s.ID, FromStates: spec.from,
	}
	switch spec.to {
	case StateBanned:
		params.BanUntil = until
	case StateQuarantined:
		params.QuarantineUntil = until
	}
	var changed []changedIdentity
	var changes []StateChange
	err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
		out, err := q.ActionTransitionIdentities(ctx, params)
		if err != nil {
			return err
		}
		changed, changes = changed[:0], changes[:0]
		for _, r := range out {
			changed = append(changed, changedIdentity{ID: r.ID, Client: r.Client, TypeID: r.TypeID, Key: r.Hkey, From: r.FromState, To: spec.to})
			changes = append(changes, StateChange{
				At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID,
				SubjectKind: SubjectIdentity, SubjectID: r.ID, SubjectKey: r.Hkey,
				FromState: r.FromState, ToState: spec.to, Action: spec.op, Scope: string(scopeIdentity),
				Until: until, Permanent: spec.to == StateBanned && spec.permanent,
				Actor: p.Actor(), Reason: req.Reason,
				Details: resetDetails(spec.resetFail, spec.resetHP),
			})
		}
		_, err = insertStateEvents(ctx, tx, changes)
		return err
	})
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(changed))
	for _, c := range changed {
		done[c.ID] = true
	}
	for _, r := range rows {
		if !done[r.ID] {
			res.fail(r.ID, FailureStateChanged, "identity state changed concurrently")
		}
	}
	res.Succeeded += len(changed)
	_ = o.pushTransitions(ctx, s, changed, hotTransition{
		at: now, to: spec.to, until: until, permanent: spec.permanent, resetFail: spec.resetFail, resetHP: spec.resetHP,
		recordBan: spec.to == StateBanned,
	})
	o.notify.publishAll(ctx, changes)
	return nil
}

// pushTransitions applies committed identity transitions of one site to Redis
// (apply.lua "set" at the commit time t.at, skipped for identities holding a
// newer Redis-first change) and then runs the hot syncer. Failures are logged
// and returned: the hot state does not converge by itself, so callers that
// can retry (the expiry job) re-synchronize the identities later.
func (o *Operator) pushTransitions(ctx context.Context, s *catalog.Site, changed []changedIdentity, t hotTransition) error {
	if len(changed) == 0 {
		return nil
	}
	ops := make([]luaOp, len(changed))
	ids := make([]string, len(changed))
	flags := flagForce
	if t.resetFail {
		flags |= flagResetFails
	}
	if t.resetHP {
		flags |= flagResetHealth
	}
	if t.recordBan {
		flags |= flagRecordBans
	}
	var until int64
	if t.to == StateBanned || t.to == StateQuarantined {
		until = untilMs(derefTime(t.until), t.permanent)
	}
	for i, c := range changed {
		ids[i] = c.ID
		op := luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: c.Key, To: t.to, Until: until, Flags: flags, Groups: groupKeys(s, c.Client)}
		if schedulable(t.to) {
			op.Add = eligibleGroupKeys(s, c.Client, c.TypeID)
		}
		if t.resetFail || t.resetHP {
			// Baselines are only read when health entries are reset.
			op.Baselines = baselines(s, c.Client)
		}
		ops[i] = op
	}
	at := t.at
	if at.IsZero() {
		at = o.now()
	}
	actx, cancel := detached(ctx)
	defer cancel()
	var applyErr error
	if _, err := o.apply.run(actx, s.Key, at, ops); err != nil {
		o.logger.Warn("apply identity transitions to hot state", "site_id", s.ID, "identities", len(ops), "error", err)
		applyErr = fmt.Errorf("apply identity transitions on site %s: %w", s.ID, err)
	}
	return errors.Join(applyErr, o.syncIdentities(ctx, s.ID, ids, SyncOptions{ResetHealth: t.resetHP, ResetFailures: t.resetFail}))
}

// cooldownChunk applies a manual cooldown (Redis only) and records state events.
func (o *Operator) cooldownChunk(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, rows []identityRow, spec opSpec, req OperationRequest, now time.Time, res *BulkResult) error {
	until := now.Add(spec.duration)
	ops := make([]luaOp, len(rows))
	egID := ""
	for i, r := range rows {
		op := luaOp{Op: luaCooldown, Subject: r.Hkey, Until: until.UnixMilli()}
		if spec.scope == scopeEndpoint {
			g := s.GroupsByID[spec.groupID]
			op.Scope, op.Group, egID = luaScopeIdentityEndpoint, g.Key, g.ID
			if g.Action != nil {
				op.Baseline = g.Action.Health.Baseline
			}
		} else {
			op.Scope, op.Groups = luaScopeIdentitySite, groupKeys(s, r.Client)
		}
		ops[i] = op
	}
	results, err := o.apply.run(ctx, s.Key, now, ops)
	if err != nil {
		return err
	}
	var changes []StateChange
	for i, r := range rows {
		lr := results[i]
		switch {
		case lr.Applied:
			changes = append(changes, StateChange{
				At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID,
				SubjectKind: SubjectIdentity, SubjectID: r.ID, SubjectKey: r.Hkey, EndpointGroupID: egID,
				FromState: lr.From, ToState: lr.To, Action: OpCooldown, Scope: spec.scope,
				Until: timePtr(until), Actor: p.Actor(), Reason: req.Reason,
			})
			res.Succeeded++
		case lr.Reason == skipAlreadyCooling:
			// A cooldown at least as long is already in effect.
			res.Succeeded++
		case lr.Reason == skipMissing:
			res.fail(r.ID, FailureNotInHotState, "identity is not loaded in the hot state")
		default:
			res.fail(r.ID, FailureInvalidTransition, "cooldown not applied: "+lr.Reason)
		}
	}
	o.writeEvents(ctx, changes)
	return nil
}

// resetStatsChunk resets health scores, failure streaks and counters (Redis only).
func (o *Operator) resetStatsChunk(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, rows []identityRow, spec opSpec, req OperationRequest, now time.Time, res *BulkResult) error {
	ops := make([]luaOp, len(rows))
	egID := ""
	for i, r := range rows {
		op := luaOp{Op: luaResetStats, Scope: luaScopeIdentity, Subject: r.Hkey, Baselines: baselines(s, r.Client)}
		switch spec.scope {
		case scopeEndpoint:
			g := s.GroupsByID[spec.groupID]
			op.Groups, egID = []int64{g.Key}, g.ID
		case scopeSite:
			op.Global, op.Counters = 1, counterSuffixes(s, r.Client, r.Hkey)
		default:
			op.Groups, op.Global, op.Counters = groupKeys(s, r.Client), 1, counterSuffixes(s, r.Client, r.Hkey)
		}
		ops[i] = op
	}
	results, err := o.apply.run(ctx, s.Key, now, ops)
	if err != nil {
		return err
	}
	var changes []StateChange
	for i, r := range rows {
		lr := results[i]
		if !lr.Applied {
			res.fail(r.ID, FailureNotInHotState, "identity is not loaded in the hot state")
			continue
		}
		res.Succeeded++
		changes = append(changes, StateChange{
			At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID,
			SubjectKind: SubjectIdentity, SubjectID: r.ID, SubjectKey: r.Hkey, EndpointGroupID: egID,
			FromState: lr.From, ToState: lr.To, Action: OpResetStats, Scope: spec.scope,
			Actor: p.Actor(), Reason: req.Reason,
		})
	}
	o.writeEvents(ctx, changes)
	return nil
}

// scopeIdentity is the state event scope of identity lifecycle changes.
const scopeIdentity = "identity"

func resetDetails(fails, health bool) map[string]any {
	if !fails && !health {
		return nil
	}
	return map[string]any{"reset_failures": fails, "reset_health": health}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
