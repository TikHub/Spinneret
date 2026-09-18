package action

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// revertFixture applies automatic actions through the Executor and flushes
// their state events.
type revertFixture struct {
	*env
	start time.Time
}

func newRevertFixture(t *testing.T) *revertFixture {
	e := newEnv(t, true)
	f := &revertFixture{env: e, start: time.Now().UTC().Add(-time.Minute)}
	e.search.ActionRef = catalog.PolicyRef{PolicyID: "pol_a", Version: 1}
	e.defB.ActionRef = catalog.PolicyRef{PolicyID: "pol_a", Version: 1}
	for i, id := range []string{"idt_1", "idt_2", "idt_3", "idt_4", "idt_5"} {
		e.addIdentity(identitySeed{ID: id, Key: int64(101 + i)})
	}
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_6", Key: 106, AccountID: "acc_1", AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_7", Key: 201, Site: e.siteB})

	exec, w, _ := e.executor(true)
	run := func(ctx ReportContext, pa policy.PlannedAction) {
		_, err := exec.Execute(e.ctx, ExecInput{ReportContext: ctx, Planned: []policy.PlannedAction{pa}})
		require.NoError(t, err)
	}
	onIdentity := func(key int64, id string) ReportContext {
		rc := e.report(e.search)
		rc.IdentityKey, rc.IdentityID, rc.ProxyKey, rc.ProxyID = key, id, 0, ""
		return rc
	}
	run(onIdentity(101, "idt_1"), planned(policy.ActionBan, policy.ScopeIdentity, 12*time.Hour, "captcha-ban"))
	run(onIdentity(102, "idt_2"), planned(policy.ActionQuarantine, policy.ScopeIdentity, 24*time.Hour, policy.RuleNameQuarantine))
	run(onIdentity(103, "idt_3"), planned(policy.ActionExpire, policy.ScopeIdentity, 0, "auth-invalid-expire"))
	run(onIdentity(104, "idt_4"), planned(policy.ActionBan, policy.ScopeIdentity, 12*time.Hour, "captcha-ban"))
	run(onIdentity(105, "idt_5"), planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, 30*time.Minute, "rl-cooldown"))
	run(onIdentity(105, "idt_5"), planned(policy.ActionCooldown, policy.ScopeIdentitySite, time.Hour, "captcha-cooldown"))
	acc := onIdentity(106, "idt_6")
	acc.AccountKey = 7
	run(acc, policy.PlannedAction{Action: policy.ActionBan, Scope: policy.ScopeAccount, Permanent: true, RuleName: "banned-account"})
	siteB := ReportContext{Namespace: e.ns, Site: e.siteB, Group: e.defB, IdentityKey: 201, IdentityID: "idt_7", Outcome: policy.OutcomeCaptcha}
	run(siteB, planned(policy.ActionBan, policy.ScopeIdentity, 12*time.Hour, "captcha-ban"))
	require.NoError(t, w.Flush(e.ctx))

	// idt_4 was changed manually after the automatic ban.
	op := e.operator()
	_, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_4"}, OperationRequest{Operation: OpUnban})
	require.NoError(t, err)
	_, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_4"}, OperationRequest{Operation: OpBan, Duration: durationx.MustParse("1h")})
	require.NoError(t, err)
	e.audit.reset()
	return f
}

func TestRevertActionsBansDryRunAndFilters(t *testing.T) {
	f := newRevertFixture(t)
	op := f.operator()
	req := RevertRequest{Rule: "captcha-ban", Actions: []string{OpBan}, From: f.start, DryRun: true}

	res, ids, err := op.RevertActions(f.ctx, operatorUser(), f.ns, req)
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 2}, res)
	require.ElementsMatch(t, []string{"idt_1", "idt_7"}, ids)
	st, _, _, _ := f.identityState("idt_1")
	require.Equal(t, StateBanned, st)
	require.Empty(t, f.audit.all(), "dry runs are not audited")

	// Site filter and site-restricted principals.
	siteReq := req
	siteReq.SiteID = siteAID
	res, ids, err = op.RevertActions(f.ctx, operatorUser(), f.ns, siteReq)
	require.NoError(t, err)
	require.Equal(t, 1, res.Matched)
	require.Equal(t, []string{"idt_1"}, ids)
	res, ids, err = op.RevertActions(f.ctx, operatorUser(siteBID), f.ns, req)
	require.NoError(t, err)
	require.Equal(t, 1, res.Matched)
	require.Equal(t, []string{"idt_7"}, ids)
	_, _, err = op.RevertActions(f.ctx, operatorUser(siteBID), f.ns, siteReq)
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err))
	_, _, err = op.RevertActions(f.ctx, viewerUser(), f.ns, req)
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err))
	policyReq := req
	policyReq.PolicyID = "pol_other"
	res, _, err = op.RevertActions(f.ctx, operatorUser(), f.ns, policyReq)
	require.NoError(t, err)
	require.Zero(t, res.Matched)

	// Real revert.
	req.DryRun, req.ResetFailures = false, true
	f.setHS(f.search.Key, 101, "40.00|1|20|5|7|0|0|9")
	res, ids, err = op.RevertActions(f.ctx, operatorUser(), f.ns, req)
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 2, Succeeded: 2}, res)
	require.ElementsMatch(t, []string{"idt_1", "idt_7"}, ids)
	for _, id := range []string{"idt_1", "idt_7"} {
		st, reason, bu, _ := f.identityState(id)
		require.Equal(t, []any{StatePending, ReasonReverted, (*time.Time)(nil)}, []any{st, reason, bu})
		evs := f.stateEvents(id)
		last := evs[len(evs)-1]
		require.Equal(t, []string{OpRevert, StateBanned, StatePending, "user:usr_op", "captcha-ban"}, []string{last.Action, last.From, last.To, last.Actor, last.Rule})
	}
	require.Equal(t, StatePending, f.hget(f.keys.Identity(siteAKey, 101), "st"))
	require.Equal(t, "40.00|1|20|0|0|0|0|9", f.hs(f.search.Key, 101))
	_, inReady := f.zscore(f.keys.Ready(siteBKey, f.defB.Key), 201)
	require.True(t, inReady)
	st, _, _, _ = f.identityState("idt_4")
	require.Equal(t, StateBanned, st)
	require.Len(t, f.audit.all(), 1)

	// Nothing left to revert.
	res, ids, err = op.RevertActions(f.ctx, operatorUser(), f.ns, req)
	require.NoError(t, err)
	require.Zero(t, res.Matched)
	require.Empty(t, ids)
}

func TestRevertActionsAllKinds(t *testing.T) {
	f := newRevertFixture(t)
	op := f.operator()
	scd := f.hget(f.keys.Identity(siteAKey, 105), "scd")
	require.NotEqual(t, "0", scd)

	dry, _, err := op.RevertActions(f.ctx, operatorUser(), f.ns, RevertRequest{From: f.start, SiteID: siteAID, DryRun: true})
	require.NoError(t, err)
	// idt_1 ban, idt_2 quarantine, idt_3 expire, idt_6 account-member ban, acc_1 account ban, 2 cooldowns.
	require.Equal(t, 7, dry.Matched)

	res, ids, err := op.RevertActions(f.ctx, operatorUser(), f.ns, RevertRequest{From: f.start, SiteID: siteAID})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 7, Succeeded: 7}, res)
	require.ElementsMatch(t, []string{"idt_1", "idt_2", "idt_3", "idt_5", "idt_6"}, ids)
	for _, id := range []string{"idt_1", "idt_2", "idt_3", "idt_6"} {
		st, _, _, _ := f.identityState(id)
		require.Equal(t, StatePending, st, id)
	}
	state, _, _ := f.accountState("acc_1")
	require.Equal(t, StateActive, state)
	require.Equal(t, StateActive, f.hget(f.keys.Account(siteAKey, 7), "st"))
	require.Equal(t, "0", f.hget(f.keys.Identity(siteAKey, 105), "scd"))
	require.Contains(t, f.hs(f.search.Key, 105), "|0|0|0")
	evs := f.stateEvents("idt_5")
	require.Equal(t, OpRevert, evs[len(evs)-1].Action)

	// Already reverted cooldowns are no longer matched as in effect.
	res, _, err = op.RevertActions(f.ctx, operatorUser(), f.ns, RevertRequest{From: f.start, SiteID: siteAID, Actions: []string{OpCooldown}})
	require.NoError(t, err)
	require.Equal(t, 2, res.Matched)
	require.Zero(t, res.Succeeded)
	require.Equal(t, map[string]string{"idt_5": FailureStateChanged}, failureReasons(res))
}

func TestRevertActionsValidation(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	now := time.Now()
	cases := []RevertRequest{
		{},
		{From: now, To: now.Add(-time.Hour)},
		{From: now.Add(-time.Hour), Actions: []string{"activate"}},
		{From: now.Add(-time.Hour), SiteID: "sit_nope"},
	}
	for _, req := range cases {
		_, _, err := op.RevertActions(e.ctx, operatorUser(), e.ns, req)
		require.Error(t, err)
	}
	_, _, err := op.RevertActions(e.ctx, operatorUser(), nil, RevertRequest{From: now.Add(-time.Hour)})
	require.Error(t, err)
	actions, err := normalizeRevertActions([]string{OpBan, OpBan, OpCooldown})
	require.NoError(t, err)
	require.Equal(t, []string{OpBan, OpCooldown}, actions)
	empty := catalogNamespaceWithoutSites()
	res, ids, err := op.RevertActions(e.ctx, operatorUser(), empty, RevertRequest{From: now.Add(-time.Hour)})
	require.NoError(t, err)
	require.Zero(t, res.Matched)
	require.Empty(t, ids)
}

func catalogNamespaceWithoutSites() *catalog.Namespace {
	return &catalog.Namespace{ID: testNamespace, TenantID: testTenant, Name: testNSName, SitesByID: map[string]*catalog.Site{}}
}

func TestRevertActionsEmptyNamespacePermissions(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	empty := catalogNamespaceWithoutSites()
	req := RevertRequest{From: time.Now().Add(-time.Hour)}

	_, _, err := op.RevertActions(e.ctx, viewerUser(), empty, req)
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err))
	_, _, err = op.RevertActions(e.ctx, tokenPrincipal(t, "lease:acquire"), empty, req)
	require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err))
	res, ids, err := op.RevertActions(e.ctx, tokenPrincipal(t, "identity:write"), empty, req)
	require.NoError(t, err)
	require.Zero(t, res.Matched)
	require.Empty(t, ids)
	_, _, err = op.RevertActions(e.ctx, operatorUser(siteAID), empty, req)
	require.NoError(t, err, "site-restricted operators hold identity:operate in the namespace")
}

func TestRevertActionsAccountBanReleasesMembers(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	bannedAt := fixed.Add(-10 * time.Minute)

	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned', updated_at = $1 WHERE id = 'acc_1'`, bannedAt)
	e.do(e.rdb.B().Hset().Key(e.keys.Account(siteAKey, 7)).FieldValue().FieldValue("st", StateBanned).FieldValue("bu", "-1").Build())
	e.addIdentity(identitySeed{ID: "idt_m", Key: 150, State: StateBanned, AccountID: "acc_1", AccountKey: 7})
	e.exec(`UPDATE identities SET state_reason = $1 WHERE id = 'idt_m'`, ReasonAccountBan)
	e.setHS(e.search.Key, 150, "30.00|1|20|5|7|0|0|9")
	// Only the account event exists (e.g. the member events fell outside the
	// candidate window): the account revert releases the member itself.
	_, err := insertStateEvents(e.ctx, e.pool, []StateChange{{
		At: bannedAt, TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectAccount, SubjectID: "acc_1", SubjectKey: 7, FromState: StateActive, ToState: StateBanned,
		Action: OpBan, Scope: SubjectAccount, Permanent: true, Rule: "banned-account", Actor: ActorSystem,
	}})
	require.NoError(t, err)

	req := RevertRequest{From: fixed.Add(-time.Hour), Actions: []string{OpBan}, ResetFailures: true}
	res, ids, err := op.RevertActions(e.ctx, operatorUser(), e.ns, req)
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 1, Succeeded: 1}, res)
	require.Equal(t, []string{"idt_m"}, ids)
	state, _, _ := e.accountState("acc_1")
	require.Equal(t, StateActive, state)
	st, reason, _, _ := e.identityState("idt_m")
	require.Equal(t, []string{StatePending, ReasonReverted}, []string{st, reason})
	require.Equal(t, StatePending, e.hget(e.keys.Identity(siteAKey, 150), "st"))
	require.Equal(t, "30.00|1|20|0|0|0|0|9", e.hs(e.search.Key, 150))
	last := e.hot.identities[len(e.hot.identities)-1]
	require.Equal(t, SyncOptions{ResetFailures: true}, last.Opts)
	evs := e.stateEvents("idt_m")
	require.Len(t, evs, 1)
	require.Equal(t, []string{OpRevert, StateBanned, StatePending, "user:usr_op"}, []string{evs[0].Action, evs[0].From, evs[0].To, evs[0].Actor})
}
