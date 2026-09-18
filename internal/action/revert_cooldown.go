package action

import (
	"context"
	"fmt"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/action/actiondb"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// cooldownCandidate is an automatic identity cooldown that may still be in effect.
type cooldownCandidate struct {
	row actiondb.ActionRevertCooldownCandidatesRow
	op  luaOp
}

// revertCooldowns clears identity × endpoint and identity × site cooldowns
// set by matching automatic events when they are still in effect and not
// extended beyond the event's end.
func (o *Operator) revertCooldowns(ctx context.Context, p *authz.Principal, f revertFilter, req RevertRequest, now time.Time, run *revertRun) error {
	qctx, cancel := context.WithTimeout(ctx, dbTimeout)
	rows, err := actiondb.New(o.pool).ActionRevertCooldownCandidates(qctx, actiondb.ActionRevertCooldownCandidatesParams{
		NamespaceID: f.ns.ID, Now: now, SiteIds: f.siteIDs, FromTs: f.from, ToTs: f.to,
		PolicyID: f.policyID, Rule: f.rule, MaxRows: maxRevertCandidates,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("query cooldown revert candidates: %w", err)
	}
	flags := 0
	if req.ResetFailures {
		flags |= flagResetFails
	}
	if req.ResetHealth {
		flags |= flagResetHealth
	}
	bySite := map[string][]cooldownCandidate{}
	var order []string
	siteLevel := map[string]int{} // identity → index in bySite of its identity_site candidate
	for _, r := range rows {
		s := f.ns.SitesByID[r.SiteID]
		if s == nil || r.Until == nil {
			continue
		}
		c := cooldownCandidate{row: r, op: luaOp{Op: luaClear, Subject: r.Hkey, Until: r.Until.UnixMilli(), Flags: flags}}
		if r.Scope == scopeEndpoint {
			g, ok := s.GroupsByID[r.EndpointGroupID]
			if !ok {
				continue
			}
			c.op.Scope, c.op.Group, c.op.Baselines = luaScopeIdentityEndpoint, g.Key, baselines(s, g.Client)
		} else {
			c.op.Scope, c.op.Groups, c.op.Baselines = luaScopeIdentitySite, groupKeys(s, r.Client), baselines(s, r.Client)
			if idx, ok := siteLevel[r.SubjectID]; ok {
				// Several groups recorded the same site cooldown: keep the latest end.
				prev := &bySite[r.SiteID][idx]
				if r.Until.After(*prev.row.Until) {
					prev.row, prev.op.Until = r, r.Until.UnixMilli()
				}
				continue
			}
			siteLevel[r.SubjectID] = len(bySite[r.SiteID])
		}
		if _, ok := bySite[r.SiteID]; !ok {
			order = append(order, r.SiteID)
		}
		bySite[r.SiteID] = append(bySite[r.SiteID], c)
	}
	for _, siteID := range order {
		cands := bySite[siteID]
		run.res.Matched += len(cands)
		if req.DryRun {
			for _, c := range cands {
				run.touch(c.row.SubjectID)
			}
			continue
		}
		o.clearCooldowns(ctx, p, f.ns, f.ns.SitesByID[siteID], cands, req, now, run)
	}
	return nil
}

func (o *Operator) clearCooldowns(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, cands []cooldownCandidate, req RevertRequest, now time.Time, run *revertRun) {
	ops := make([]luaOp, len(cands))
	for i, c := range cands {
		ops[i] = c.op
	}
	results, err := o.apply.run(ctx, s.Key, now, ops)
	if err != nil {
		o.logger.Error("clear cooldowns failed", "site_id", s.ID, "error", err)
		for _, c := range cands {
			run.res.fail(c.row.SubjectID, FailureInternal, "cooldown revert failed, retry later")
		}
		return
	}
	var changes []StateChange
	for i, c := range cands {
		lr := results[i]
		switch {
		case lr.Applied:
			run.res.Succeeded++
			run.touch(c.row.SubjectID)
			changes = append(changes, StateChange{
				At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID,
				SubjectKind: SubjectIdentity, SubjectID: c.row.SubjectID, SubjectKey: c.row.Hkey,
				EndpointGroupID: c.row.EndpointGroupID, FromState: lr.From, ToState: lr.To,
				Action: OpRevert, Scope: c.row.Scope, Actor: p.Actor(), Reason: ReasonReverted,
				PolicyID: req.PolicyID, Rule: req.Rule,
				Details: map[string]any{"reverted_event_id": c.row.ID, "reverted_action": OpCooldown, "cleared_until_ms": lr.Until},
			})
		case lr.Reason == skipMissing:
			run.res.fail(c.row.SubjectID, FailureNotInHotState, "identity is not loaded in the hot state")
		default:
			run.res.fail(c.row.SubjectID, FailureStateChanged, "cooldown already ended or was extended")
		}
	}
	o.writeEvents(ctx, changes)
}

func (o *Operator) recordRevertAudit(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req RevertRequest, run *revertRun) {
	details := map[string]any{
		"from":      req.From,
		"to":        req.To,
		"actions":   req.Actions,
		"matched":   run.res.Matched,
		"succeeded": run.res.Succeeded,
		"failed":    len(run.res.Failed),
	}
	if req.SiteID != "" {
		details["site_id"] = req.SiteID
	}
	if req.PolicyID != "" {
		details["policy_id"] = req.PolicyID
	}
	if req.Rule != "" {
		details["rule"] = req.Rule
	}
	o.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity.revert_actions", "identity", "", "", run.res.auditResult(), details))
}
