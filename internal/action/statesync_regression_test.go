package action

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/hotstate"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// realHot adapts the hot-state syncer like the server wiring does.
type realHot struct{ s *hotstate.Syncer }

func (h realHot) SyncIdentities(ctx context.Context, siteID string, ids []string, opts SyncOptions) error {
	return h.s.SyncIdentities(ctx, siteID, ids, hotstate.SyncOptions(opts))
}

func (h realHot) SyncAccounts(ctx context.Context, siteID string, ids []string) error {
	return h.s.SyncAccounts(ctx, siteID, ids)
}

func (h realHot) SyncProxies(ctx context.Context, nsID string, ids []string) error {
	return h.s.SyncProxies(ctx, nsID, ids)
}

func (e *env) realSyncer() realHot {
	return realHot{s: hotstate.NewSyncer(e.pool, e.rdb, e.keys, e.cat, nil)}
}

// A Redis-first automatic ban that the StateWriter has not persisted yet is
// not reverted by an authoritative synchronization of an attribute change,
// and a later PostgreSQL-first unban still wins.
func TestAutomaticBanSurvivesAuthoritativeSync(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	hot := e.realSyncer()
	require.NoError(t, hot.SyncIdentities(e.ctx, siteAID, []string{"idt_1"}, SyncOptions{}))
	exec, w, _ := e.executor(false)
	now := time.Now().UTC().Truncate(time.Millisecond)

	_, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.search), Now: now,
		Planned: []policy.PlannedAction{{Action: policy.ActionBan, Scope: policy.ScopeIdentity, Permanent: true, RuleName: "captcha-ban"}}})
	require.NoError(t, err)
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 101), "st"))

	// An operator edits the identity's tags before the ban reaches PostgreSQL.
	e.exec(`UPDATE identities SET tags = '{vip}' WHERE id = 'idt_1'`)
	require.NoError(t, hot.SyncIdentities(e.ctx, siteAID, []string{"idt_1"}, SyncOptions{}))
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 101), "st"))
	require.Equal(t, "-1", e.hget(e.keys.Identity(siteAKey, 101), "bu"))
	for _, g := range []int64{e.defWeb.Key, e.search.Key} {
		_, ok := e.zscore(e.keys.Ready(siteAKey, g), 101)
		require.False(t, ok, "a banned identity must not be leasable")
	}

	require.NoError(t, w.Flush(e.ctx))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state)

	op := NewOperator(e.pool, e.rdb, e.keys, e.cat, hot, w, e.audit, e.bus, nil)
	res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1"}, OperationRequest{Operation: OpUnban})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, StatePending, e.hget(e.keys.Identity(siteAKey, 101), "st"))
	_, ok := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.True(t, ok)
}

// A worker retries Execute after a call that ran in Redis but whose reply was
// lost: the retry reports the original results, so the ban is persisted, and
// replays never duplicate state events.
func TestExecuteRetryAfterAppliedCallPersistsState(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	exec, w, _ := e.executor(false)
	now := time.Now().UTC().Truncate(time.Millisecond)
	in := ExecInput{ReportContext: e.report(e.search), Now: now,
		Planned: []policy.PlannedAction{planned(policy.ActionBan, policy.ScopeIdentity, 12*time.Hour, "captcha-ban")}}

	// First attempt: apply.lua runs, the client gives up before finishing.
	first := make([]plan, len(in.Planned))
	for i, pa := range in.Planned {
		first[i] = exec.buildPlan(&in, pa, now)
	}
	require.NoError(t, exec.applyPlans(e.ctx, &in, first, now))
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 101), "st"))

	// Retry of the same report.
	res, err := exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.False(t, res.Applied[0].Skipped, "reason %q", res.Applied[0].SkipReason)
	require.Equal(t, StateBanned, res.Applied[0].ToState)
	require.NoError(t, w.Flush(e.ctx))
	state, _, bu, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state)
	require.Equal(t, now.Add(12*time.Hour).UnixMilli(), bu.UnixMilli())
	require.Len(t, e.stateEvents("idt_1"), 1)
	require.Len(t, e.evts.all(), 1)

	// A redelivered report is replayed and its event is stored once.
	res, err = exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.False(t, res.Applied[0].Skipped)
	require.NoError(t, w.Flush(e.ctx))
	require.Len(t, e.stateEvents("idt_1"), 1)

	// Another report is applied normally (the identity is already banned).
	other := in
	other.ReportID = "rep_2"
	res, err = exec.Execute(e.ctx, other)
	require.NoError(t, err)
	require.Equal(t, "already_banned", res.Applied[0].SkipReason)
}

// A PostgreSQL-first push never overwrites a newer Redis-first change: an
// automatic ban applied between the operator's commit and its push stays,
// and PostgreSQL converges to it when the StateWriter persists the ban.
func TestOperatorPushKeepsNewerAutomaticChange(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StateQuarantined})
	future := time.Now().Add(time.Hour)
	e.exec(`UPDATE identities SET quarantine_until = $1 WHERE id = 'idt_1'`, future)
	exec, w, _ := e.executor(false)
	committed := time.Now().UTC().Truncate(time.Microsecond)
	banned := committed.Add(time.Second)

	_, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.search), Now: banned,
		Planned: []policy.PlannedAction{{Action: policy.ActionBan, Scope: policy.ScopeIdentity, Permanent: true, RuleName: "ban", Source: policy.SourceRule}}})
	require.NoError(t, err)

	op := NewOperator(e.pool, e.rdb, e.keys, e.cat, e.hot, w, e.audit, e.bus, nil)
	op.now = func() time.Time { return committed }
	res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1"}, OperationRequest{Operation: OpUnquarantine})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 101), "st"), "the newer automatic ban stays in Redis")

	require.NoError(t, w.Flush(e.ctx))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state, "PostgreSQL converges to the newer ban")
}

// An automatic account ban is persisted by the StateWriter although an
// unrelated account change bumped updated_at before the flush.
func TestAccountBanPersistedAfterAccountUpdate(t *testing.T) {
	e := newEnv(t, true)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountID: "acc_1", AccountKey: 7})
	exec, w, _ := e.executor(false)
	bannedAt := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	in := ExecInput{ReportContext: e.report(e.search), Now: bannedAt,
		Planned: []policy.PlannedAction{{Action: policy.ActionBan, Scope: policy.ScopeAccount, Permanent: true, RuleName: "acc-ban", Source: policy.SourceRule}}}
	in.AccountKey = 7
	_, err := exec.Execute(e.ctx, in)
	require.NoError(t, err)

	// The account's notes are edited before the StateWriter flushes.
	e.exec(`UPDATE accounts SET notes = 'checked', updated_at = now() WHERE id = 'acc_1'`)
	require.NoError(t, w.Flush(e.ctx))
	state, _, _ := e.accountState("acc_1")
	require.Equal(t, StateBanned, state, "the automatic ban is persisted despite the newer updated_at")

	// A newer lifecycle change is still never overridden by an older one.
	e.exec(`UPDATE accounts SET state = 'disabled' WHERE id = 'acc_1'`)
	_, err = insertStateEvents(e.ctx, e.pool, []StateChange{{
		At: bannedAt.Add(time.Second), TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectAccount, SubjectID: "acc_1", FromState: StateBanned, ToState: StateDisabled,
		Action: OpDisable, Scope: SubjectAccount, Actor: "user:usr_op",
	}})
	require.NoError(t, err)
	w.Enqueue(StateChange{At: bannedAt, TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectAccount, SubjectID: "acc_1", FromState: StateActive, ToState: StateBanned, Action: OpBan,
		Permanent: true, UpdateSubject: true})
	require.NoError(t, w.Flush(e.ctx))
	state, _, _ = e.accountState("acc_1")
	require.Equal(t, StateDisabled, state)
}

// An automatic account ban stays revertible after later account updates
// (attribute edits and manual cooldowns bump updated_at).
func TestAccountBanRevertAfterAccountUpdate(t *testing.T) {
	e := newEnv(t, true)
	bannedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Minute)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned', updated_at = $1 WHERE id = 'acc_1'`, bannedAt)
	_, err := insertStateEvents(e.ctx, e.pool, []StateChange{{
		At: bannedAt, TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectAccount, SubjectID: "acc_1", SubjectKey: 7, FromState: StateActive, ToState: StateBanned,
		Action: OpBan, Scope: SubjectAccount, Permanent: true, Rule: "acc-ban", Actor: ActorSystem,
	}})
	require.NoError(t, err)

	op := e.operator()
	_, err = op.OperateAccount(e.ctx, operatorUser(), e.ns, "acc_1", OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("10m")})
	require.NoError(t, err)
	e.exec(`UPDATE accounts SET notes = 'checked', updated_at = now() WHERE id = 'acc_1'`)

	req := RevertRequest{From: bannedAt.Add(-time.Minute), Actions: []string{OpBan}}
	res, _, err := op.RevertActions(e.ctx, operatorUser(), e.ns, req)
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 1, Succeeded: 1}, res)
	state, _, _ := e.accountState("acc_1")
	require.Equal(t, StateActive, state)

	// Once reverted (a later lifecycle event exists) it is no longer matched.
	res, _, err = op.RevertActions(e.ctx, operatorUser(), e.ns, req)
	require.NoError(t, err)
	require.Zero(t, res.Matched)
}

// A release whose hot-state push fails after the PostgreSQL commit is
// re-synchronized by the following passes until that succeeds.
func TestExpiryRetriesFailedHotPush(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	past := time.Now().UTC().Add(-time.Minute)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StateBanned, BanUntil: &past})
	e.addProxy("pxy_1", 55, StateActive)
	e.exec(`UPDATE proxies SET state = 'banned', ban_until = $1 WHERE id = 'pxy_1'`, past)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned', ban_until = $1 WHERE id = 'acc_1'`, past)

	e.hot.err = errors.New("redis timeout")
	require.NoError(t, op.RunExpiry(e.ctx))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StatePending, state)

	// Redis is still failing: the retry fails and the subjects stay recorded.
	require.Error(t, op.RunExpiry(e.ctx))

	e.hot.mu.Lock()
	e.hot.err, e.hot.identities, e.hot.proxies, e.hot.accounts = nil, nil, nil, nil
	e.hot.mu.Unlock()
	require.NoError(t, op.RunExpiry(e.ctx))
	require.Equal(t, []string{"idt_1"}, e.hot.syncedIdentities())
	require.Equal(t, []string{"pxy_1"}, e.hot.proxies[0].IDs)
	require.Equal(t, []string{"acc_1"}, e.hot.accounts[0].IDs)

	// Once synchronized they are not retried again.
	require.NoError(t, op.RunExpiry(e.ctx))
	require.Equal(t, []string{"idt_1"}, e.hot.syncedIdentities())
}

// A new leader re-synchronizes the releases of the last minutes, which may
// have failed to reach Redis on the previous leader.
func TestExpiryCatchUpOnFirstPass(t *testing.T) {
	e := newEnv(t, true)
	_, err := insertStateEvents(e.ctx, e.pool, []StateChange{{
		At: time.Now().Add(-time.Minute), TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectIdentity, SubjectID: "idt_released", FromState: StateBanned, ToState: StatePending,
		Action: OpUnban, Scope: SubjectIdentity, Actor: ActorSystem, Reason: ReasonBanExpired,
	}, {
		At: time.Now().Add(-time.Hour), TenantID: testTenant, NamespaceID: testNamespace, SiteID: siteAID,
		SubjectKind: SubjectIdentity, SubjectID: "idt_old", FromState: StateBanned, ToState: StatePending,
		Action: OpUnban, Scope: SubjectIdentity, Actor: ActorSystem, Reason: ReasonBanExpired,
	}})
	require.NoError(t, err)
	op := e.operator()
	require.NoError(t, op.RunExpiry(e.ctx))
	require.Equal(t, []string{"idt_released"}, e.hot.syncedIdentities())

	// Later passes of the same leader do not repeat it.
	require.NoError(t, op.RunExpiry(e.ctx))
	require.Equal(t, []string{"idt_released"}, e.hot.syncedIdentities())
}

// Nothing is released while Redis is unavailable.
func TestExpirySkipsPassWhileRedisIsDown(t *testing.T) {
	e := newEnv(t, true)
	url := os.Getenv(testutil.RedisURLEnv)
	if url == "" {
		t.Skip("requires " + testutil.RedisURLEnv)
	}
	client, err := redis.Open(e.ctx, url, nil)
	require.NoError(t, err)
	client.Close()
	past := time.Now().UTC().Add(-time.Minute)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StateBanned, BanUntil: &past})
	op := NewOperator(e.pool, client, e.keys, e.cat, e.hot, nil, e.audit, e.bus, nil)
	require.Error(t, op.RunExpiry(e.ctx))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state, "the ban stays due until Redis answers")
	require.Empty(t, e.stateEvents("idt_1"))
}
