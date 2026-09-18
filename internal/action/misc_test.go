package action

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

func TestWriteEventsFallsBackToWriter(t *testing.T) {
	e := newEnv(t, true)
	w := NewStateWriter(e.pool, nil, nil)
	op := NewOperator(e.pool, e.rdb, e.keys, e.cat, nil, w, nil, nil, nil)
	// No partition exists for 1999: the synchronous insert fails and the
	// change is handed to the asynchronous writer.
	op.writeEvents(e.ctx, []StateChange{identityChange("idt_1", time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC), StateActive, OpCooldown)})
	require.Equal(t, 1, w.Pending())
	op.writeEvents(e.ctx, nil)
	require.Equal(t, 1, w.Pending())
}

func TestStateWriterResolvesIdentityAndProxyKeys(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	e.addProxy("pxy_1", 55, StateActive)
	w := NewStateWriter(e.pool, nil, nil)
	now := time.Now().UTC()
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace, SubjectKind: SubjectIdentity, SubjectKey: 101, ToState: StateExpired, Action: OpExpire, UpdateSubject: true})
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace, SubjectKind: SubjectProxy, SubjectKey: 55, ToState: StateBanned, Action: OpBan, Permanent: true, UpdateSubject: true})
	w.Enqueue(StateChange{At: now, TenantID: testTenant, NamespaceID: testNamespace, SubjectKind: "site", SubjectKey: 1, Action: "noop"})
	require.NoError(t, w.Flush(e.ctx))
	st, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateExpired, st)
	var px string
	var bu *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state, ban_until FROM proxies WHERE id = 'pxy_1'`).Scan(&px, &bu))
	require.Equal(t, StateBanned, px)
	require.Nil(t, bu)
	require.Len(t, e.stateEvents("pxy_1"), 1)
}

func TestExecutorShadowPolicyMode(t *testing.T) {
	e := newEnv(t, false)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	spec := policy.Default(policy.KindAction).(*policy.ActionSpec)
	spec.Mode = policy.ModeShadow
	compiled, err := policy.CompileAction([]*policy.ActionSpec{spec})
	require.NoError(t, err)
	group := catalogtest.AddGroup(e.siteA, "eg_shadow", "web", "shadowed", 11600)
	catalogtest.SetPolicies(group, catalogtest.Policies{Action: compiled})
	exec, _, _ := e.executor(true)
	res, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(group), Planned: []policy.PlannedAction{
		planned(policy.ActionBan, policy.ScopeIdentity, time.Hour, "ban"),
	}})
	require.NoError(t, err)
	require.Equal(t, SkipShadow, res.Applied[0].SkipReason)
	require.Equal(t, StateActive, e.hget(e.keys.Identity(siteAKey, 101), "st"))
}

func TestHotSyncErrorsAreLogged(t *testing.T) {
	e := newEnv(t, true)
	e.hot.err = errors.New("unavailable")
	op := e.operator()
	past := time.Now().UTC().Add(-time.Minute)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned', ban_until = $1 WHERE id = 'acc_1'`, past)
	e.addProxy("pxy_1", 55, StateActive)
	e.exec(`UPDATE proxies SET state = 'banned', ban_until = $1 WHERE id = 'pxy_1'`, past)
	require.NoError(t, op.RunExpiry(e.ctx))
	state, _, _ := e.accountState("acc_1")
	require.Equal(t, StateActive, state)
	require.Len(t, e.hot.proxies, 1)
}

func TestOpSpecHelpers(t *testing.T) {
	s := catalogtest.AddSite(catalogtest.NewNamespace(testTenant, testNamespace, testNSName), "sit_x", "x", 99, "web")
	spec, err := newOpSpec(OperationRequest{Operation: OpQuarantine, Duration: durationx.MustParse("2h")})
	require.NoError(t, err)
	require.Equal(t, 2*time.Hour, spec.quarantineDuration(s, "web"))
	spec, err = newOpSpec(OperationRequest{Operation: OpQuarantine})
	require.NoError(t, err)
	require.Equal(t, policy.DefaultQuarantineDuration, spec.quarantineDuration(s, "app"))
	now := time.Now()
	require.Equal(t, now.Add(24*time.Hour), *spec.until(s, "web", now))
	unban, err := newOpSpec(OperationRequest{Operation: OpUnban})
	require.NoError(t, err)
	require.Nil(t, unban.until(s, "web", now))
	ban, err := newOpSpec(OperationRequest{Operation: OpBan, Duration: durationx.Permanent})
	require.NoError(t, err)
	require.Nil(t, ban.until(s, "web", now))
	require.False(t, resetApplies(OpBan))
	require.True(t, resetApplies(OpRestore))
	require.Equal(t, []string{"a", "b"}, uniqueStrings([]string{"a", "", "b", "a"}))
	require.True(t, namespaceHasGroup(&catalog.Namespace{SitesByID: map[string]*catalog.Site{s.ID: s}}, "sit_x_web_default"))
	require.Nil(t, groupKeys(nil, ""))
	require.Nil(t, eligibleGroupKeys(nil, "web", "t"))
	require.Nil(t, baselines(nil, "web"))
	require.Nil(t, counterSuffixes(nil, "web", 1))
	require.True(t, msTime(0).IsZero())
	require.EqualValues(t, 0, untilMs(time.Time{}, false))
}

func TestNotifierSkipsWithoutBus(t *testing.T) {
	n := notifier{}
	n.publish(t.Context(), StateChange{NamespaceID: testNamespace})
	e := newEnv(t, false)
	n = notifier{bus: e.bus, logger: e.operator().logger}
	n.publish(t.Context(), StateChange{NamespaceID: testNamespace, Shadow: true})
	n.publishAll(t.Context(), []StateChange{{NamespaceID: testNamespace, Action: OpCooldown, FromState: StateActive, ToState: StateActive}})
	require.Empty(t, e.evts.all())
	n.publish(t.Context(), StateChange{NamespaceID: testNamespace, SubjectKind: SubjectAccount, SubjectKey: 7})
	evs := e.evts.all()
	require.Len(t, evs, 1)
	require.EqualValues(t, 7, decodeStateEvent(t, evs[0]).SubjectKey)
}
