package action

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func (e *env) executor(record bool) (*Executor, *StateWriter, *observability.Metrics) {
	metrics := observability.NewMetrics()
	var w *StateWriter
	if e.pool != nil {
		w = NewStateWriter(e.pool, metrics, nil)
	}
	return NewExecutor(ExecutorConfig{RecordCooldownEvents: record}, e.rdb, e.keys, w, e.bus, metrics, nil), w, metrics
}

func (e *env) report(group *catalog.EndpointGroup) ReportContext {
	return ReportContext{
		Namespace: e.ns, Site: e.siteA, Group: group, LeaseID: "lse_1", ReportID: "rep_1",
		IdentityKey: 101, IdentityID: "idt_1", ProxyKey: 55, ProxyID: "pxy_1", Outcome: policy.OutcomeCaptcha, RuleName: "sig-captcha",
	}
}

func planned(action policy.ActionKind, scope policy.ActionScope, d time.Duration, rule string) policy.PlannedAction {
	pa := policy.PlannedAction{Action: action, Scope: scope, Duration: d, RuleName: rule, RuleIndex: 0, Source: policy.SourceRule}
	pa.Severity = policy.Severity(pa)
	return pa
}

func decodeStateEvent(t *testing.T, ev events.Event) StateEventData {
	t.Helper()
	var d StateEventData
	require.NoError(t, json.Unmarshal(ev.Data, &d))
	return d
}

func TestExecutorEnforce(t *testing.T) {
	e := newEnv(t, true)
	e.search.ActionRef = catalog.PolicyRef{PolicyID: "pol_action", Version: 3}
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	e.addProxy("pxy_1", 55, StateActive)
	exec, w, metrics := e.executor(true)
	now := time.Now().UTC().Truncate(time.Millisecond)

	res, err := exec.Execute(e.ctx, ExecInput{
		ReportContext: e.report(e.search),
		Planned: []policy.PlannedAction{
			planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, 5*time.Minute, "rl-cooldown"),
			planned(policy.ActionBan, policy.ScopeIdentity, 12*time.Hour, "captcha-ban"),
			planned(policy.ActionCooldown, policy.ScopeProxySite, 2*time.Minute, "px-cooldown"),
		},
		Now: now,
	})
	require.NoError(t, err)
	require.Len(t, res.Applied, 3)
	require.False(t, res.Applied[0].Skipped)
	require.Equal(t, now.Add(5*time.Minute).UnixMilli(), res.Applied[0].Until.UnixMilli())
	require.Equal(t, AppliedAction{
		Planned: res.Applied[1].Planned, SubjectKind: policy.SubjectIdentity, SubjectID: "idt_1", SubjectKey: 101,
		FromState: StateActive, ToState: StateBanned, Until: now.Add(12 * time.Hour).UTC(),
	}, res.Applied[1])
	require.Equal(t, policy.SubjectProxy, res.Applied[2].SubjectKind)
	require.False(t, res.Applied[2].Skipped)

	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 101), "st"))
	require.Contains(t, e.hs(e.search.Key, 101), "|"+ms(now.Add(5*time.Minute))+"|")
	require.Equal(t, ms(now.Add(2*time.Minute)), e.hget(e.keys.ProxySite(siteAKey, 55), "cd"))
	require.Equal(t, 1.0, counterValue(metrics.ActionsTotal.WithLabelValues("site-a", "ban", "identity", modeEnforce)))
	require.Equal(t, 1.0, counterValue(metrics.ActionsTotal.WithLabelValues("site-a", "cooldown", "proxy_site", modeEnforce)))

	require.NoError(t, w.Flush(e.ctx))
	state, reason, bu, _ := e.identityState("idt_1")
	require.Equal(t, StateBanned, state)
	require.Equal(t, "captcha-ban", reason)
	require.Equal(t, now.Add(12*time.Hour).UnixMilli(), bu.UnixMilli())
	evs := e.stateEvents("idt_1")
	require.Len(t, evs, 2)
	require.Equal(t, stateEventRow{SubjectKind: SubjectIdentity, SubjectID: "idt_1", From: StateActive, To: StateActive, Action: "cooldown",
		Scope: "identity_endpoint", Actor: ActorSystem, Reason: "rl-cooldown", Rule: "rl-cooldown", EG: "eg_search", Until: evs[0].Until}, evs[0])
	require.Equal(t, "ban", evs[1].Action)
	var policyID, reportID, leaseID, outcome string
	var version int
	var details []byte
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT policy_id, policy_version, report_id, lease_id, outcome, details FROM state_events WHERE subject_id = 'idt_1' AND action = 'ban'`).
		Scan(&policyID, &version, &reportID, &leaseID, &outcome, &details))
	require.Equal(t, []any{"pol_action", 3, "rep_1", "lse_1", "captcha"}, []any{policyID, version, reportID, leaseID, outcome})
	require.JSONEq(t, `{"source":"rule","severity":4,"duration_ms":43200000,"signal_rule":"sig-captcha"}`, string(details))
	var pxState string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state FROM proxies WHERE id = 'pxy_1'`).Scan(&pxState))
	require.Equal(t, StateActive, pxState)
	require.Len(t, e.stateEvents("pxy_1"), 1)

	busEvents := e.evts.all()
	require.Len(t, busEvents, 1)
	require.Equal(t, events.TypeIdentityState, busEvents[0].Type)
	data := decodeStateEvent(t, busEvents[0])
	require.Equal(t, "idt_1", data.SubjectID)
	require.Equal(t, StateBanned, data.To)
	require.Equal(t, siteAID, data.SiteID)
}

func TestExecutorAccountBanPropagates(t *testing.T) {
	e := newEnv(t, true)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountID: "acc_1", AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, Client: "app", AccountID: "acc_1", AccountKey: 7})
	exec, w, _ := e.executor(false)
	in := ExecInput{ReportContext: e.report(e.search), Planned: []policy.PlannedAction{{Action: policy.ActionBan, Scope: policy.ScopeAccount, Permanent: true, RuleName: "banned-account"}}}
	in.AccountKey = 7
	res, err := exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.Equal(t, policy.SubjectAccount, res.Applied[0].SubjectKind)
	require.EqualValues(t, 7, res.Applied[0].SubjectKey)
	require.True(t, res.Applied[0].Until.IsZero())
	_, ok := e.zscore(e.keys.Ready(siteAKey, e.defApp.Key), 102)
	require.False(t, ok)

	require.NoError(t, w.Flush(e.ctx))
	var accState string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state FROM accounts WHERE id = 'acc_1'`).Scan(&accState))
	require.Equal(t, StateBanned, accState)
	for _, id := range []string{"idt_1", "idt_2"} {
		state, reason, bu, _ := e.identityState(id)
		require.Equal(t, []any{StateBanned, ReasonAccountBan, (*time.Time)(nil)}, []any{state, reason, bu})
		evs := e.stateEvents(id)
		require.Len(t, evs, 1)
		require.Equal(t, "account", evs[0].Scope)
		require.True(t, evs[0].Permanent)
	}
	require.Len(t, e.stateEvents("acc_1"), 1)
	require.Len(t, e.evts.all(), 3)
}

func TestExecutorAccountScopeDegradesToIdentity(t *testing.T) {
	e := newEnv(t, false)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	exec, _, _ := e.executor(true)
	res, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.defWeb), Planned: []policy.PlannedAction{
		planned(policy.ActionCooldown, policy.ScopeAccount, time.Hour, "acc-cd"),
		planned(policy.ActionCooldown, policy.ScopeIdentity, 2*time.Hour, "id-cd"),
		planned(policy.ActionBan, policy.ScopeAccount, time.Hour, "acc-ban"),
	}})
	require.NoError(t, err)
	require.Equal(t, policy.SubjectIdentity, res.Applied[0].SubjectKind)
	require.False(t, res.Applied[0].Skipped)
	require.False(t, res.Applied[1].Skipped)
	require.Equal(t, StateBanned, res.Applied[2].ToState)
	require.NotEmpty(t, e.hget(e.keys.Identity(siteAKey, 101), "scd"))
}

func TestExecutorProxyGlobalScope(t *testing.T) {
	e := newEnv(t, true)
	e.addProxy("pxy_1", 55, StateActive)
	e.addProxy("pxy_2", 56, StateActive)
	// pxy_2 is not materialized on the report's site.
	e.do(e.rdb.B().Del().Key(e.keys.ProxySite(siteAKey, 56)).Build())
	exec, w, _ := e.executor(true)
	now := time.Now().UTC()

	res, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.defWeb), Now: now,
		Planned: []policy.PlannedAction{planned(policy.ActionBan, policy.ScopeProxy, time.Hour, "px-ban")}})
	require.NoError(t, err)
	require.Equal(t, StateBanned, res.Applied[0].ToState)
	for _, site := range []int64{siteAKey, siteBKey} {
		require.Equal(t, StateBanned, e.hget(e.keys.ProxySite(site, 55), "st"))
		_, ok := e.zscore(e.keys.ProxyReady(site), 55)
		require.False(t, ok)
	}

	in := ExecInput{ReportContext: e.report(e.defWeb), Now: now, Planned: []policy.PlannedAction{
		planned(policy.ActionQuarantine, policy.ScopeProxy, time.Hour, "px-q"),
	}}
	in.ProxyKey, in.ProxyID = 56, "pxy_2"
	res, err = exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.False(t, res.Applied[0].Skipped)
	require.Equal(t, StateQuarantined, e.hget(e.keys.ProxySite(siteBKey, 56), "st"))

	in.Planned = []policy.PlannedAction{planned(policy.ActionCooldown, policy.ScopeProxy, time.Minute, "px-gcd")}
	in.ProxyKey, in.ProxyID = 55, "pxy_1"
	_, err = exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.Equal(t, ms(now.Add(time.Minute)), e.hget(e.keys.ProxySite(siteBKey, 55), "gcd"))

	require.NoError(t, w.Flush(e.ctx))
	var s1, s2 string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT (SELECT state FROM proxies WHERE id = 'pxy_1'), (SELECT state FROM proxies WHERE id = 'pxy_2')`).Scan(&s1, &s2))
	require.Equal(t, []string{StateBanned, StateQuarantined}, []string{s1, s2})
	types := map[string]int{}
	for _, ev := range e.evts.all() {
		types[ev.Type]++
	}
	require.Equal(t, map[string]int{events.TypeProxyState: 2}, types)
}

func TestExecutorShadow(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	exec, w, metrics := e.executor(true)
	res, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.search), Shadow: true, Planned: []policy.PlannedAction{
		planned(policy.ActionBan, policy.ScopeIdentity, time.Hour, "captcha-ban"),
		planned(policy.ActionCooldown, policy.ScopeProxySite, time.Minute, "px"),
		planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, 0, "zero"),
	}})
	require.NoError(t, err)
	require.Equal(t, []string{SkipShadow, SkipShadow, SkipInvalidDuration},
		[]string{res.Applied[0].SkipReason, res.Applied[1].SkipReason, res.Applied[2].SkipReason})
	require.Equal(t, StateBanned, res.Applied[0].ToState)
	require.Equal(t, StateActive, e.hget(e.keys.Identity(siteAKey, 101), "st"))
	require.Equal(t, 1.0, counterValue(metrics.ActionsTotal.WithLabelValues("site-a", "ban", "identity", modeShadow)))

	require.NoError(t, w.Flush(e.ctx))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateActive, state)
	evs := e.stateEvents("idt_1")
	require.Len(t, evs, 1)
	require.True(t, evs[0].Shadow)
	require.Len(t, e.stateEvents("pxy_1"), 1)
	require.Empty(t, e.evts.all())
}

func TestExecutorSkips(t *testing.T) {
	e := newEnv(t, true)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StatePending})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, State: StateBanned})
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 102)).FieldValue().FieldValue("bu", "-1").Build())
	exec, w, _ := e.executor(false)

	in := ExecInput{ReportContext: e.report(e.search), Planned: []policy.PlannedAction{
		{Action: policy.ActionActivate, Scope: policy.ScopeIdentity, RuleName: policy.RuleNameActivate, Source: policy.SourceLifecycle},
		planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, time.Minute, "cd"),
		planned(policy.ActionQuarantine, policy.ScopeAccount, time.Hour, "weird"),
		planned(policy.ActionBan, policy.ScopeIdentity, 0, "zero-ban"),
		planned(policy.ActionCooldown, policy.ScopeProxySite, time.Minute, "px"),
	}}
	in.ProxyKey, in.ProxyID, in.Outcome, in.AccountKey = 0, "", policy.OutcomeSuccess, 7
	res, err := exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.Equal(t, []bool{false, false, true, true, true},
		[]bool{res.Applied[0].Skipped, res.Applied[1].Skipped, res.Applied[2].Skipped, res.Applied[3].Skipped, res.Applied[4].Skipped})
	require.Equal(t, []string{SkipUnsupported, SkipInvalidDuration, SkipNoSubject},
		[]string{res.Applied[2].SkipReason, res.Applied[3].SkipReason, res.Applied[4].SkipReason})

	in = ExecInput{ReportContext: e.report(nil), Planned: []policy.PlannedAction{
		planned(policy.ActionBan, policy.ScopeIdentity, time.Hour, "ban"),
		planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, time.Minute, "no-group"),
	}}
	in.IdentityKey, in.IdentityID = 102, "idt_2"
	res, err = exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.Equal(t, "already_banned", res.Applied[0].SkipReason)
	require.Equal(t, SkipUnsupported, res.Applied[1].SkipReason)

	in.IdentityKey, in.IdentityID = 404, "idt_missing"
	in.Planned = in.Planned[:1]
	res, err = exec.Execute(e.ctx, in)
	require.NoError(t, err)
	require.Equal(t, skipMissing, res.Applied[0].SkipReason)

	require.NoError(t, w.Flush(e.ctx))
	evs := e.stateEvents("idt_1")
	require.Len(t, evs, 1, "cooldown events are not recorded when disabled")
	require.Equal(t, "activate", evs[0].Action)
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateActive, state)
	require.Empty(t, e.stateEvents("idt_2"))

	res, err = exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.search)})
	require.NoError(t, err)
	require.Empty(t, res.Applied)
	_, err = exec.Execute(e.ctx, ExecInput{Planned: in.Planned})
	require.Error(t, err)
}

func TestExecutorRedisFailure(t *testing.T) {
	e := newEnv(t, false)
	url := os.Getenv(testutil.RedisURLEnv)
	if url == "" {
		t.Skip("requires " + testutil.RedisURLEnv)
	}
	client, err := redis.Open(e.ctx, url, nil)
	require.NoError(t, err)
	client.Close()
	exec := NewExecutor(ExecutorConfig{}, client, e.keys, nil, nil, nil, nil)
	_, err = exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.search), Planned: []policy.PlannedAction{
		planned(policy.ActionBan, policy.ScopeIdentity, time.Hour, "ban"),
	}})
	require.Error(t, err)
}

func TestExecutorConcurrent(t *testing.T) {
	e := newEnv(t, true)
	const identities = 20
	for i := 0; i < identities; i++ {
		e.addIdentity(identitySeed{ID: "idt_" + strconv.Itoa(i), Key: int64(200 + i)})
	}
	exec, w, _ := e.executor(true)
	ctx, cancel := context.WithCancel(e.ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	var wg sync.WaitGroup
	for i := 0; i < identities; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				in := ExecInput{ReportContext: e.report(e.search), Planned: []policy.PlannedAction{
					planned(policy.ActionCooldown, policy.ScopeIdentityEndpoint, time.Duration(j+1)*time.Minute, "cd"),
				}}
				if j == 9 {
					in.Planned = append(in.Planned, planned(policy.ActionQuarantine, policy.ScopeIdentity, time.Hour, "q"))
				}
				in.IdentityKey, in.IdentityID = int64(200+i), "idt_"+strconv.Itoa(i)
				_, err := exec.Execute(e.ctx, in)
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	cancel()
	require.NoError(t, <-runDone)
	var quarantined, events int
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM identities WHERE state = 'quarantined'`).Scan(&quarantined))
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM state_events`).Scan(&events))
	require.Equal(t, identities, quarantined)
	require.Equal(t, identities*11, events)
}

func TestExecutorHelpers(t *testing.T) {
	require.Equal(t, 1, streakIncrement(policy.OutcomeCaptcha))
	require.Equal(t, 1, streakIncrement(policy.OutcomeNetworkError))
	require.Equal(t, 0, streakIncrement(policy.OutcomeSuccess))
	require.Equal(t, "", targetState(policy.ActionCooldown))
	g := &catalog.EndpointGroup{Breaker: &policy.BreakerSpec{Window: durationx.Duration(15 * time.Minute)}}
	require.Equal(t, 30*time.Minute, recentCooldownRetention(&ExecInput{ReportContext: ReportContext{Group: g}}))
	require.Equal(t, minRecentCooldownRetention, recentCooldownRetention(&ExecInput{}))
}

func TestExecutorProxyGlobalScopeKeepsReportSiteSkip(t *testing.T) {
	e := newEnv(t, true)
	e.addProxy("pxy_1", 55, StateActive)
	e.exec(`UPDATE proxies SET state = 'banned', ban_until = NULL, state_changed_at = now() - interval '1 minute' WHERE id = 'pxy_1'`)
	// The report's site knows the permanent ban; site B lags behind.
	e.do(e.rdb.B().Hset().Key(e.keys.ProxySite(siteAKey, 55)).FieldValue().FieldValue("st", StateBanned).FieldValue("bu", "-1").Build())
	exec, w, _ := e.executor(true)

	res, err := exec.Execute(e.ctx, ExecInput{ReportContext: e.report(e.defWeb),
		Planned: []policy.PlannedAction{planned(policy.ActionBan, policy.ScopeProxy, time.Hour, "px-ban")}})
	require.NoError(t, err)
	require.True(t, res.Applied[0].Skipped)
	require.Equal(t, "already_banned", res.Applied[0].SkipReason)

	require.NoError(t, w.Flush(e.ctx))
	var state string
	var banUntil *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT state, ban_until FROM proxies WHERE id = 'pxy_1'`).Scan(&state, &banUntil))
	require.Equal(t, StateBanned, state)
	require.Nil(t, banUntil, "the permanent ban is not downgraded by a lagging site")
	require.Empty(t, e.stateEvents("pxy_1"))
	require.Empty(t, e.evts.all())
}
