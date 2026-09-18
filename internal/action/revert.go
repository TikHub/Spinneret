package action

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/action/actiondb"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// revertIdentitiesSQL moves identities back to pending when they are still in
// the state (and at the change) produced by the reverted event.
const revertIdentitiesSQL = `
UPDATE identities AS i
SET state            = 'pending',
    state_reason     = $1,
    state_changed_at = $2,
    ban_until        = NULL,
    quarantine_until = NULL,
    updated_at       = $2
FROM (
    SELECT x.id, x.state
    FROM identities AS x
    JOIN unnest($3::text[], $4::text[], $5::timestamptz[]) AS u(id, state, changed_before) ON u.id = x.id
    WHERE x.state = u.state
      AND x.state_changed_at <= u.changed_before
    FOR UPDATE OF x
) AS prev
WHERE i.id = prev.id
RETURNING i.id, i.hkey, i.client, i.type_id, prev.state`

// revertableActions are the automatic actions RevertActions can roll back.
func revertableActions() []string {
	return []string{OpBan, OpQuarantine, OpExpire, OpCooldown}
}

// revertCandidate is an automatic lifecycle event whose effect is still current.
type revertCandidate struct {
	eventID  string
	siteID   string
	identity string
	state    string
	at       time.Time
	action   string
}

// revertRun accumulates the outcome of one RevertActions call.
type revertRun struct {
	res      BulkResult
	affected []string
	seen     map[string]struct{}
}

func (r *revertRun) touch(id string) {
	if _, ok := r.seen[id]; ok || len(r.affected) >= MaxRevertIDs {
		return
	}
	r.seen[id] = struct{}{}
	r.affected = append(r.affected, id)
}

// RevertActions rolls back automatic actions recorded in state events
// (shadow=false, actor system) matching req: identities still banned,
// quarantined or expired by the latest matching event go back to pending,
// automatically banned accounts are unbanned, and identity cooldowns still in
// effect are cleared. With DryRun nothing changes. It returns up to
// MaxRevertIDs affected identity IDs.
func (o *Operator) RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req RevertRequest) (BulkResult, []string, error) {
	if p == nil {
		return BulkResult{}, nil, errUnauthenticated()
	}
	if ns == nil {
		return BulkResult{}, nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	now := o.now()
	to := req.To
	if to.IsZero() {
		to = now
	}
	if req.From.IsZero() || !to.After(req.From) {
		return BulkResult{}, nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "a time range with from before to is required")
	}
	actions, err := normalizeRevertActions(req.Actions)
	if err != nil {
		return BulkResult{}, nil, err
	}
	sites, err := o.revertSites(p, ns, req.SiteID)
	if err != nil || len(sites) == 0 {
		return BulkResult{}, nil, err
	}
	siteIDs := make([]string, len(sites))
	for i, s := range sites {
		siteIDs[i] = s.ID
	}
	filter := revertFilter{ns: ns, siteIDs: siteIDs, from: req.From, to: to, policyID: req.PolicyID, rule: req.Rule}
	run := &revertRun{seen: map[string]struct{}{}}
	if lifecycle := slices.DeleteFunc(slices.Clone(actions), func(a string) bool { return a == OpCooldown }); len(lifecycle) > 0 {
		if err := o.revertLifecycle(ctx, p, filter, lifecycle, req, now, run); err != nil {
			return BulkResult{}, nil, err
		}
		if slices.Contains(lifecycle, OpBan) {
			if err := o.revertAccounts(ctx, p, filter, req, run); err != nil {
				return BulkResult{}, nil, err
			}
		}
	}
	if slices.Contains(actions, OpCooldown) {
		if err := o.revertCooldowns(ctx, p, filter, req, now, run); err != nil {
			return BulkResult{}, nil, err
		}
	}
	if !req.DryRun {
		o.recordRevertAudit(ctx, p, ns, req, run)
	}
	return run.res, run.affected, nil
}

type revertFilter struct {
	ns       *catalog.Namespace
	siteIDs  []string
	from, to time.Time
	policyID string
	rule     string
}

func normalizeRevertActions(in []string) ([]string, error) {
	if len(in) == 0 {
		return revertableActions(), nil
	}
	out := make([]string, 0, len(in))
	for _, a := range in {
		if !slices.Contains(revertableActions(), a) {
			return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "action %q cannot be reverted", a)
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// revertSites returns the sites the revert covers: the requested site (which
// the principal must be allowed to operate) or every operable site.
func (o *Operator) revertSites(p *authz.Principal, ns *catalog.Namespace, siteID string) ([]*catalog.Site, error) {
	if siteID != "" {
		s, ok := ns.SitesByID[siteID]
		if !ok {
			return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site not found")
		}
		if err := p.Require(authz.PermIdentityOperate, siteResource(ns, s)); err != nil {
			return nil, err
		}
		return []*catalog.Site{s}, nil
	}
	all := make([]*catalog.Site, 0, len(ns.SitesByID))
	for _, s := range ns.SitesByID {
		all = append(all, s)
	}
	if len(all) == 0 {
		// Nothing to revert, but callers without any identity:operate grant
		// in the namespace are still denied.
		if every, siteIDs := p.SiteFilter(ns.TenantID, ns.ID, authz.PermIdentityOperate); !every && len(siteIDs) == 0 {
			return nil, p.Require(authz.PermIdentityOperate, authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name})
		}
		return nil, nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	out := make([]*catalog.Site, 0, len(all))
	for _, s := range all {
		if p.Can(authz.PermIdentityOperate, siteResource(ns, s)) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, p.Require(authz.PermIdentityOperate, siteResource(ns, all[0]))
	}
	return out, nil
}

// revertLifecycle reverts ban/quarantine/expire events of identities.
func (o *Operator) revertLifecycle(ctx context.Context, p *authz.Principal, f revertFilter, actions []string, req RevertRequest, now time.Time, run *revertRun) error {
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	rows, err := actiondb.New(o.pool).ActionRevertLifecycleCandidates(qctx, actiondb.ActionRevertLifecycleCandidatesParams{
		NamespaceID: f.ns.ID, Actions: actions, SiteIds: f.siteIDs, FromTs: f.from, ToTs: f.to,
		PolicyID: f.policyID, Rule: f.rule, MaxRows: maxRevertCandidates,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("query revert candidates: %w", err)
	}
	bySite := map[string][]revertCandidate{}
	var order []string
	for _, r := range rows {
		if r.CurrentState != r.ToState || r.StateChangedAt.After(r.CreatedAt) {
			continue // the identity changed since the event
		}
		run.res.Matched++
		if req.DryRun {
			run.touch(r.SubjectID)
			continue
		}
		if _, ok := bySite[r.SiteID]; !ok {
			order = append(order, r.SiteID)
		}
		bySite[r.SiteID] = append(bySite[r.SiteID], revertCandidate{
			eventID: r.ID, siteID: r.SiteID, identity: r.SubjectID, state: r.ToState, at: r.CreatedAt, action: r.Action,
		})
	}
	for _, siteID := range order {
		s := f.ns.SitesByID[siteID]
		cands := bySite[siteID]
		for start := 0; start < len(cands); start += dbChunk {
			chunk := cands[start:min(start+dbChunk, len(cands))]
			if err := o.revertChunk(ctx, p, f.ns, s, chunk, req, now, run); err != nil {
				o.logger.Error("revert identities failed", "site_id", siteID, "error", err)
				for _, c := range chunk {
					run.res.fail(c.identity, FailureInternal, "revert failed, retry later")
				}
			}
		}
	}
	return nil
}

func (o *Operator) revertChunk(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, chunk []revertCandidate, req RevertRequest, now time.Time, run *revertRun) error {
	ids, states, before := make([]string, len(chunk)), make([]string, len(chunk)), make([]time.Time, len(chunk))
	byID := make(map[string]revertCandidate, len(chunk))
	for i, c := range chunk {
		ids[i], states[i], before[i] = c.identity, c.state, c.at
		byID[c.identity] = c
	}
	var changed []changedIdentity
	var changes []StateChange
	err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, _ *actiondb.Queries) error {
		out, err := tx.Query(ctx, revertIdentitiesSQL, ReasonReverted, now, ids, states, before)
		if err != nil {
			return fmt.Errorf("revert identities: %w", err)
		}
		changed, changes = changed[:0], changes[:0]
		for out.Next() {
			var c changedIdentity
			if err := out.Scan(&c.ID, &c.Key, &c.Client, &c.TypeID, &c.From); err != nil {
				out.Close()
				return fmt.Errorf("scan reverted identity: %w", err)
			}
			c.To = StatePending
			changed = append(changed, c)
		}
		out.Close()
		if err := out.Err(); err != nil {
			return fmt.Errorf("revert identities: %w", err)
		}
		for _, c := range changed {
			cand := byID[c.ID]
			changes = append(changes, StateChange{
				At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: cand.siteID,
				SubjectKind: SubjectIdentity, SubjectID: c.ID, SubjectKey: c.Key,
				FromState: c.From, ToState: StatePending, Action: OpRevert, Scope: scopeIdentity,
				Actor: p.Actor(), Reason: ReasonReverted, PolicyID: req.PolicyID, Rule: req.Rule,
				Details: map[string]any{"reverted_event_id": cand.eventID, "reverted_action": cand.action},
			})
		}
		_, err = insertStateEvents(ctx, tx, changes)
		return err
	})
	if err != nil {
		return err
	}
	done := make(idSet, len(changed))
	for _, c := range changed {
		done[c.ID] = true
		run.touch(c.ID)
	}
	for _, c := range chunk {
		if !done[c.identity] {
			run.res.fail(c.identity, FailureStateChanged, "identity state changed concurrently")
		}
	}
	run.res.Succeeded += len(changed)
	if s != nil {
		_ = o.pushTransitions(ctx, s, changed, hotTransition{at: now, to: StatePending, resetFail: req.ResetFailures, resetHP: req.ResetHealth})
	} else if len(changed) > 0 {
		_ = o.syncIdentities(ctx, chunk[0].siteID, done.ids(), SyncOptions{ResetHealth: req.ResetHealth, ResetFailures: req.ResetFailures})
	}
	o.notify.publishAll(ctx, changes)
	return nil
}

// idSet is a set of IDs.
type idSet map[string]bool

func (s idSet) ids() []string {
	out := make([]string, 0, len(s))
	for id := range s {
		out = append(out, id)
	}
	return out
}

// revertAccounts unbans accounts still banned by a matching automatic event.
func (o *Operator) revertAccounts(ctx context.Context, p *authz.Principal, f revertFilter, req RevertRequest, run *revertRun) error {
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	rows, err := actiondb.New(o.pool).ActionRevertAccountCandidates(qctx, actiondb.ActionRevertAccountCandidatesParams{
		NamespaceID: f.ns.ID, SiteIds: f.siteIDs, FromTs: f.from, ToTs: f.to, PolicyID: f.policyID, Rule: f.rule, MaxRows: maxRevertCandidates,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("query account revert candidates: %w", err)
	}
	t, _ := accountTransitionFor(OpUnban, ReasonReverted)
	for _, r := range rows {
		run.res.Matched++
		if req.DryRun {
			continue
		}
		s := f.ns.SitesByID[r.SiteID]
		acc, err := o.loadAccount(ctx, f.ns.ID, r.SubjectID)
		if err == nil && s == nil {
			err = apperr.NotFound("account site not found")
		}
		var members []changedIdentity
		if err == nil {
			members, err = o.revertAccount(ctx, p, f.ns, s, acc, t, req)
		}
		if err != nil {
			reason := FailureInternal
			if e, ok := apperr.As(err); ok && e.Reason != apperr.ReasonInternal {
				reason = FailureStateChanged
			} else {
				o.logger.Error("revert account ban failed", "account_id", r.SubjectID, "error", err)
			}
			run.res.fail(r.SubjectID, reason, "account ban could not be reverted")
			continue
		}
		run.res.Succeeded++
		for _, m := range members {
			run.touch(m.ID)
		}
	}
	return nil
}

// revertAccount unbans one automatically banned account and releases the
// member identities its ban propagated to (with the request's reset flags).
func (o *Operator) revertAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, acc accountRow, t accountTransition, req RevertRequest) ([]changedIdentity, error) {
	op := OperationRequest{Operation: OpRevert, Reason: ReasonReverted, ResetFailures: req.ResetFailures, ResetHealth: req.ResetHealth}
	_, members, err := o.accountLifecycle(ctx, p, ns, s, acc, t, op, o.now())
	return members, err
}
