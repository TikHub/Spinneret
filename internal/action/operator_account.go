package action

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/action/actiondb"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// accountTransition describes a lifecycle operation on an account and its
// propagation to member identities (design doc §5.5).
type accountTransition struct {
	to   string
	from []string
	// Member propagation: identities in memberFrom (and carrying memberOnly
	// as state_reason when set) move to memberTo with memberReason.
	memberTo     string
	memberFrom   []string
	memberOnly   string
	memberReason string
}

func accountTransitionFor(op, reason string) (accountTransition, bool) {
	switch op {
	case OpBan:
		return accountTransition{to: StateBanned, from: []string{StateActive, StateDisabled, StateBanned}, memberTo: StateBanned, memberReason: ReasonAccountBan}, true
	case OpUnban:
		return accountTransition{to: StateActive, from: []string{StateBanned},
			memberTo: StatePending, memberFrom: []string{StateBanned}, memberOnly: ReasonAccountBan, memberReason: reason}, true
	case OpDisable:
		return accountTransition{to: StateDisabled, from: []string{StateActive},
			memberTo: StateDisabled, memberFrom: []string{StateActive, StatePending}, memberReason: ReasonAccountDisabled}, true
	case OpEnable:
		return accountTransition{to: StateActive, from: []string{StateDisabled},
			memberTo: StateActive, memberFrom: []string{StateDisabled}, memberOnly: ReasonAccountDisabled, memberReason: reason}, true
	}
	return accountTransition{}, false
}

// accountRow is an account loaded for an operation.
type accountRow = actiondb.ActionLoadAccountRow

// OperateAccount applies ban, unban, cooldown, disable or enable to an
// account. Bans and disables propagate to the account's identities; unban and
// enable release the identities the propagation changed. The BulkResult
// describes the member identities (Matched = members, Succeeded = changed).
func (o *Operator) OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req OperationRequest) (BulkResult, error) {
	if p == nil {
		return BulkResult{}, errUnauthenticated()
	}
	if ns == nil || accountID == "" {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace and account id are required")
	}
	acc, err := o.loadAccount(ctx, ns.ID, accountID)
	if err != nil {
		return BulkResult{}, err
	}
	s, ok := ns.SitesByID[acc.SiteID]
	if !ok {
		return BulkResult{}, apperr.NotFound("account not found")
	}
	if err := p.Require(authz.PermIdentityOperate, siteResource(ns, s)); err != nil {
		return BulkResult{}, err
	}
	now := o.now()
	var res BulkResult
	switch req.Operation {
	case OpCooldown:
		res, err = o.accountCooldown(ctx, p, ns, s, acc, req, now)
	default:
		t, ok := accountTransitionFor(req.Operation, req.Reason)
		if !ok {
			return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "unsupported account operation %q", req.Operation)
		}
		res, _, err = o.accountLifecycle(ctx, p, ns, s, acc, t, req, now)
	}
	if err != nil {
		return BulkResult{}, err
	}
	o.recordAudit(ctx, p, ns, "account.operate", "account", []string{acc.ID}, req, &res)
	return res, nil
}

func (o *Operator) loadAccount(ctx context.Context, nsID, id string) (accountRow, error) {
	ctx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	acc, err := actiondb.New(o.pool).ActionLoadAccount(ctx, actiondb.ActionLoadAccountParams{ID: id, NamespaceID: nsID})
	if errors.Is(err, pgx.ErrNoRows) {
		return acc, apperr.NotFound("account not found")
	}
	if err != nil {
		return acc, fmt.Errorf("load account: %w", err)
	}
	return acc, nil
}

// accountLifecycle changes the account state and propagates it to members.
// It returns the member identities it changed. req.ResetFailures and
// req.ResetHealth apply to members that become schedulable again.
func (o *Operator) accountLifecycle(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, acc accountRow, t accountTransition, req OperationRequest, now time.Time) (BulkResult, []changedIdentity, error) {
	var until *time.Time
	permanent := false
	if t.to == StateBanned {
		switch d := req.Duration; {
		case d.IsPermanent():
			permanent = true
		case d > 0:
			u := now.Add(d.Std())
			until = &u
		default:
			return BulkResult{}, nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "duration is required for ban")
		}
	}
	if !contains(t.from, acc.State) {
		return BulkResult{}, nil, apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "cannot %s an account in state %s", req.Operation, acc.State)
	}
	resetFail := req.ResetFailures && schedulable(t.memberTo)
	resetHP := req.ResetHealth && schedulable(t.memberTo)
	base := StateChange{
		At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID, Action: req.Operation,
		Scope: SubjectAccount, Until: until, Permanent: permanent, Actor: p.Actor(), Reason: req.Reason,
	}
	var members []changedIdentity
	var changes []StateChange
	var total int32
	err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
		row, err := q.ActionTransitionAccount(ctx, actiondb.ActionTransitionAccountParams{
			ToState: t.to, BanUntil: until, ChangedAt: now, ID: acc.ID, FromStates: t.from,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "account state changed concurrently")
		}
		if err != nil {
			return fmt.Errorf("transition account: %w", err)
		}
		if total, err = q.ActionCountAccountMembers(ctx, acc.ID); err != nil {
			return fmt.Errorf("count account members: %w", err)
		}
		if members, err = o.propagateToMembers(ctx, q, acc.ID, t, until, now); err != nil {
			return err
		}
		c := base
		c.SubjectKind, c.SubjectID, c.SubjectKey, c.FromState, c.ToState = SubjectAccount, row.ID, row.Hkey, row.FromState, t.to
		changes = append(changes[:0], c)
		for _, m := range members {
			mc := base
			mc.SubjectKind, mc.SubjectID, mc.SubjectKey, mc.FromState, mc.ToState = SubjectIdentity, m.ID, m.Key, m.From, m.To
			mc.Details = resetDetails(resetFail, resetHP)
			changes = append(changes, mc)
		}
		_, err = insertStateEvents(ctx, tx, changes)
		return err
	})
	if err != nil {
		return BulkResult{}, nil, err
	}
	_ = o.pushAccount(ctx, s, acc, t.to, untilMs(derefTime(until), permanent), now)
	if len(members) > 0 {
		_ = o.pushTransitions(ctx, s, members, hotTransition{
			at: now, to: t.memberTo, until: until, permanent: permanent, recordBan: t.memberTo == StateBanned,
			resetFail: resetFail, resetHP: resetHP,
		})
	}
	_ = o.syncAccounts(ctx, s.ID, []string{acc.ID})
	o.notify.publishAll(ctx, changes)
	return BulkResult{Matched: int(total), Succeeded: len(members)}, members, nil
}

// propagateToMembers applies the member side of an account transition.
func (o *Operator) propagateToMembers(ctx context.Context, q *actiondb.Queries, accountID string, t accountTransition, until *time.Time, now time.Time) ([]changedIdentity, error) {
	var out []changedIdentity
	if t.to == StateBanned {
		rows, err := q.ActionBanAccountMembers(ctx, actiondb.ActionBanAccountMembersParams{
			Reason: t.memberReason, ChangedAt: now, BanUntil: until, AccountID: accountID,
		})
		if err != nil {
			return nil, fmt.Errorf("ban account members: %w", err)
		}
		for _, r := range rows {
			out = append(out, changedIdentity{ID: r.ID, Client: r.Client, TypeID: r.TypeID, Key: r.Hkey, From: r.FromState, To: StateBanned})
		}
		return out, nil
	}
	rows, err := q.ActionSetAccountMembersState(ctx, actiondb.ActionSetAccountMembersStateParams{
		ToState: t.memberTo, Reason: t.memberReason, ChangedAt: now, AccountID: accountID,
		FromStates: t.memberFrom, OnlyReason: t.memberOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("update account members: %w", err)
	}
	for _, r := range rows {
		out = append(out, changedIdentity{ID: r.ID, Client: r.Client, TypeID: r.TypeID, Key: r.Hkey, From: r.FromState, To: t.memberTo})
	}
	return out, nil
}

// pushAccount writes the account state committed at at to Redis (skipped when
// the account holds a newer Redis-first change). Errors are logged and
// returned.
func (o *Operator) pushAccount(ctx context.Context, s *catalog.Site, acc accountRow, to string, until int64, at time.Time) error {
	op := luaOp{Op: luaSet, Scope: luaScopeAccount, Subject: acc.Hkey, To: to, Until: until, Flags: flagForce}
	actx, cancel := detached(ctx)
	defer cancel()
	if _, err := o.apply.run(actx, s.Key, at, []luaOp{op}); err != nil {
		o.logger.Warn("apply account state to hot state", "site_id", s.ID, "error", err)
		return fmt.Errorf("apply account state on site %s: %w", s.ID, err)
	}
	return nil
}

// accountCooldown extends the account cooldown in PostgreSQL and Redis.
func (o *Operator) accountCooldown(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, s *catalog.Site, acc accountRow, req OperationRequest, now time.Time) (BulkResult, error) {
	d := req.Duration
	if d.IsPermanent() || d <= 0 {
		return BulkResult{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "cooldown requires a positive duration")
	}
	until := now.Add(d.Std())
	var total int32
	var change StateChange
	err := o.inTx(ctx, func(ctx context.Context, tx pgx.Tx, q *actiondb.Queries) error {
		row, err := q.ActionCooldownAccount(ctx, actiondb.ActionCooldownAccountParams{Until: until, ChangedAt: now, ID: acc.ID})
		if err != nil {
			return fmt.Errorf("cool down account: %w", err)
		}
		if total, err = q.ActionCountAccountMembers(ctx, acc.ID); err != nil {
			return fmt.Errorf("count account members: %w", err)
		}
		change = StateChange{
			At: now, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: s.ID,
			SubjectKind: SubjectAccount, SubjectID: row.ID, SubjectKey: row.Hkey,
			FromState: row.State, ToState: row.State, Action: OpCooldown, Scope: SubjectAccount,
			Until: row.CooldownUntil, Actor: p.Actor(), Reason: req.Reason,
		}
		_, err = insertStateEvents(ctx, tx, []StateChange{change})
		return err
	})
	if err != nil {
		return BulkResult{}, err
	}
	op := luaOp{Op: luaCooldown, Scope: luaScopeAccount, Subject: acc.Hkey, Until: until.UnixMilli(), Groups: groupKeys(s, "")}
	actx, cancel := detached(ctx)
	defer cancel()
	if _, err := o.apply.run(actx, s.Key, now, []luaOp{op}); err != nil {
		o.logger.Warn("apply account cooldown to hot state", "site_id", s.ID, "error", err)
	}
	_ = o.syncAccounts(ctx, s.ID, []string{acc.ID})
	return BulkResult{Matched: int(total), Succeeded: int(total)}, nil
}
