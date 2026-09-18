package action

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

func (e *env) accountState(id string) (state string, banUntil, cooldownUntil *time.Time) {
	e.t.Helper()
	require.NoError(e.t, e.pool.QueryRow(e.ctx, `SELECT state, ban_until, cooldown_until FROM accounts WHERE id = $1`, id).Scan(&state, &banUntil, &cooldownUntil))
	return
}

func TestOperateAccountBanUnban(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountID: "acc_1", AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, Client: "app", AccountID: "acc_1", AccountKey: 7, State: StatePending})
	e.addIdentity(identitySeed{ID: "idt_3", Key: 103, AccountID: "acc_1", AccountKey: 7, State: StateDisabled})
	shortBan := fixed.Add(time.Hour)
	e.addIdentity(identitySeed{ID: "idt_4", Key: 104, AccountID: "acc_1", AccountKey: 7, State: StateBanned, BanUntil: &shortBan})
	permanent := identitySeed{ID: "idt_5", Key: 105, AccountID: "acc_1", AccountKey: 7, State: StateBanned}
	e.addIdentity(permanent)

	res, err := op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpBan, Duration: durationx.MustParse("2d"), Reason: "fraud"})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 5, Succeeded: 3}, res)
	state, bu, _ := e.accountState("acc_1")
	require.Equal(t, StateBanned, state)
	require.Equal(t, fixed.Add(48*time.Hour), bu.UTC())
	require.Equal(t, StateBanned, e.hget(e.keys.Account(siteAKey, 7), "st"))
	for _, id := range []string{"idt_1", "idt_2", "idt_4"} {
		st, reason, ibu, _ := e.identityState(id)
		require.Equal(t, []any{StateBanned, ReasonAccountBan}, []any{st, reason}, id)
		require.Equal(t, fixed.Add(48*time.Hour), ibu.UTC())
	}
	st, _, _, _ := e.identityState("idt_3")
	require.Equal(t, StateDisabled, st)
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 102), "st"))
	_, inReady := e.zscore(e.keys.Ready(siteAKey, e.defApp.Key), 102)
	require.False(t, inReady)
	require.Len(t, e.stateEvents("acc_1"), 1)
	require.Equal(t, "account", e.stateEvents("idt_1")[0].Scope)
	require.Equal(t, []hotCall{{Scope: siteAID, IDs: []string{"acc_1"}}}, e.hot.accounts)

	// Unban releases the members banned through the account only.
	res, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpUnban})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 5, Succeeded: 3}, res)
	state, bu, _ = e.accountState("acc_1")
	require.Equal(t, StateActive, state)
	require.Nil(t, bu)
	for _, id := range []string{"idt_1", "idt_2", "idt_4"} {
		st, _, _, _ := e.identityState(id)
		require.Equal(t, StatePending, st, id)
	}
	st, _, _, _ = e.identityState("idt_5")
	require.Equal(t, StateBanned, st)
	_, inReady = e.zscore(e.keys.Ready(siteAKey, e.defApp.Key), 102)
	require.True(t, inReady)
	require.Equal(t, StateActive, e.hget(e.keys.Account(siteAKey, 7), "st"))

	// Invalid transitions and errors.
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpUnban})
	require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpBan})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpArchive})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_missing", OperationRequest{Operation: OpDisable})
	require.True(t, apperr.IsNotFound(err))
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, viewerUser(), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, operatorUser(siteBID), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonPermissionDenied, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, tokenPrincipal(t, "identity:write:site-b"), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err))
	require.Len(t, e.audit.all(), 2)
}

func TestOperateAccountDisableEnableCooldown(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountID: "acc_1", AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, AccountID: "acc_1", AccountKey: 7, State: StateExpired})

	res, err := op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 2, Succeeded: 1}, res)
	st, reason, _, _ := e.identityState("idt_1")
	require.Equal(t, []string{StateDisabled, ReasonAccountDisabled}, []string{st, reason})
	state, _, _ := e.accountState("acc_1")
	require.Equal(t, StateDisabled, state)
	require.Equal(t, StateDisabled, e.hget(e.keys.Identity(siteAKey, 101), "st"))

	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(err))

	res, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpEnable})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	st, _, _, _ = e.identityState("idt_1")
	require.Equal(t, StateActive, st)
	_, inReady := e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 101)
	require.True(t, inReady)

	res, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("15m")})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 2, Succeeded: 2}, res)
	_, _, cd := e.accountState("acc_1")
	require.Equal(t, fixed.Add(15*time.Minute), cd.UTC())
	require.Equal(t, ms(fixed.Add(15*time.Minute)), e.hget(e.keys.Account(siteAKey, 7), "cd"))
	score, _ := e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 101)
	require.Equal(t, float64(fixed.Add(15*time.Minute).UnixMilli()), score)
	evs := e.stateEvents("acc_1")
	require.Len(t, evs, 3)
	require.Equal(t, OpCooldown, evs[2].Action)

	// A shorter cooldown never shortens the stored one.
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("1m")})
	require.NoError(t, err)
	_, _, cd = e.accountState("acc_1")
	require.Equal(t, fixed.Add(15*time.Minute), cd.UTC())
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpCooldown})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))

	// Permanent ban.
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpBan, Duration: durationx.Permanent})
	require.NoError(t, err)
	state, bu, _ := e.accountState("acc_1")
	require.Equal(t, StateBanned, state)
	require.Nil(t, bu)
	require.Equal(t, "-1", e.hget(e.keys.Account(siteAKey, 7), "bu"))
}

func TestOperateAccountBanOfDisabledAccountAndResetFlags(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountID: "acc_1", AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, AccountID: "acc_1", AccountKey: 7, State: StateDisabled})

	_, err := op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	res, err := op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpBan, Duration: durationx.MustParse("1h")})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 2, Succeeded: 1}, res)
	st, reason, _, _ := e.identityState("idt_1")
	require.Equal(t, []string{StateBanned, ReasonAccountBan}, []string{st, reason}, "members disabled by the account follow it into the ban")
	st, _, _, _ = e.identityState("idt_2")
	require.Equal(t, StateDisabled, st, "members disabled on their own stay disabled")

	e.setHS(e.search.Key, 101, "30.00|1|20|5|7|0|0|9")
	res, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpUnban, ResetFailures: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	st, _, _, _ = e.identityState("idt_1")
	require.Equal(t, StatePending, st)
	require.Equal(t, "30.00|1|20|0|0|0|0|9", e.hs(e.search.Key, 101))
	last := e.hot.identities[len(e.hot.identities)-1]
	require.Equal(t, hotCall{Scope: siteAID, IDs: []string{"idt_1"}, Opts: SyncOptions{ResetFailures: true}}, last)
	evs := e.stateEvents("idt_1")
	require.Len(t, evs, 3)
}
