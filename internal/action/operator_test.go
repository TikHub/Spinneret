package action

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

func failureReasons(res BulkResult) map[string]string {
	out := map[string]string{}
	for _, f := range res.Failed {
		out[f.ID] = f.Reason
	}
	return out
}

func TestOperateIdentitiesTransitionMatrix(t *testing.T) {
	all := []string{StatePending, StateActive, StateExpired, StateBanned, StateQuarantined, StateDisabled, StateRetired}
	cases := []struct {
		op  string
		dur durationx.Duration
	}{
		{OpBan, durationx.Duration(time.Hour)}, {OpUnban, 0}, {OpQuarantine, 0}, {OpUnquarantine, 0}, {OpExpire, 0},
		{OpDisable, 0}, {OpEnable, 0}, {OpArchive, 0}, {OpRestore, 0}, {OpActivate, 0},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			e := newEnv(t, true)
			op := e.operator()
			ids := make([]string, len(all))
			for i, st := range all {
				ids[i] = fmt.Sprintf("idt_%s", st)
				e.addIdentity(identitySeed{ID: ids[i], Key: int64(100 + i), State: st})
			}
			to, from, ok := transition(tc.op)
			require.True(t, ok)
			res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, ids, OperationRequest{Operation: tc.op, Duration: tc.dur, Reason: "manual"})
			require.NoError(t, err)
			require.Equal(t, len(all), res.Matched)
			require.Equal(t, len(from), res.Succeeded)
			failed := failureReasons(res)
			for i, st := range all {
				state, reason, _, _ := e.identityState(ids[i])
				if contains(from, st) {
					require.NotContains(t, failed, ids[i])
					require.Equal(t, to, state, "from %s", st)
					require.Equal(t, "manual", reason)
					require.Equal(t, to, e.hget(e.keys.Identity(siteAKey, int64(100+i)), "st"))
					_, inReady := e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), int64(100+i))
					require.Equal(t, schedulable(to), inReady, "ready membership after %s from %s", tc.op, st)
					evs := e.stateEvents(ids[i])
					require.Len(t, evs, 1)
					require.Equal(t, stateEventRow{SubjectKind: SubjectIdentity, SubjectID: ids[i], From: st, To: to, Action: tc.op,
						Scope: "identity", Actor: "user:usr_op", Reason: "manual", Until: evs[0].Until}, evs[0])
				} else {
					require.Equal(t, FailureInvalidTransition, failed[ids[i]], "from %s", st)
					require.Equal(t, st, state)
				}
			}
			require.ElementsMatch(t, idsFrom(all, from), e.hot.syncedIdentities())
			entries := e.audit.all()
			require.Len(t, entries, 1)
			require.Equal(t, "identity.operate", entries[0].Action)
			require.Equal(t, "usr_op", entries[0].ActorID)
		})
	}
}

func idsFrom(all, from []string) []string {
	var out []string
	for _, st := range all {
		if contains(from, st) {
			out = append(out, "idt_"+st)
		}
	}
	return out
}

func TestOperateIdentitiesDurationsAndSideEffects(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102})
	e.addIdentity(identitySeed{ID: "idt_3", Key: 103, Client: "app"})
	e.addIdentity(identitySeed{ID: "idt_4", Key: 104, State: StateBanned})
	e.setHS(e.search.Key, 104, "20.00|1|30|6|7|0|0|9")

	res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1", "idt_1", ""}, OperationRequest{Operation: OpBan, Duration: durationx.MustParse("7d")})
	require.NoError(t, err)
	require.Equal(t, BulkResult{Matched: 1, Succeeded: 1}, res)
	_, _, bu, _ := e.identityState("idt_1")
	require.Equal(t, fixed.Add(7*24*time.Hour), bu.UTC())
	require.Equal(t, ms(fixed.Add(7*24*time.Hour)), e.hget(e.keys.Identity(siteAKey, 101), "bu"))
	require.Len(t, e.zmembers(e.keys.Bans(siteAKey, 101)), 1)

	res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_2"}, OperationRequest{Operation: OpBan, Duration: durationx.Permanent})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	_, _, bu, _ = e.identityState("idt_2")
	require.Nil(t, bu)
	require.Equal(t, "-1", e.hget(e.keys.Identity(siteAKey, 102), "bu"))
	require.True(t, e.stateEvents("idt_2")[0].Permanent)

	// Quarantine without duration uses the action policy default (24h).
	res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_3"}, OperationRequest{Operation: OpQuarantine})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	_, _, _, qu := e.identityState("idt_3")
	require.Equal(t, fixed.Add(24*time.Hour), qu.UTC())

	// Unban with reset flags resets hot health and passes the options to the syncer.
	res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_4"}, OperationRequest{Operation: OpUnban, ResetFailures: true, ResetHealth: true})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, "70.00|"+ms(fixed)+"|0|0|0|0|0|9", e.hs(e.search.Key, 104))
	last := e.hot.identities[len(e.hot.identities)-1]
	require.Equal(t, hotCall{Scope: siteAID, IDs: []string{"idt_4"}, Opts: SyncOptions{ResetHealth: true, ResetFailures: true}}, last)
	_, inReady := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 104)
	require.True(t, inReady)

	var stateEvents int
	for _, ev := range e.evts.all() {
		require.Equal(t, events.TypeIdentityState, ev.Type)
		require.Equal(t, events.NamespaceChannel(testNamespace), "ns:"+ev.NamespaceID)
		stateEvents++
	}
	require.Equal(t, 4, stateEvents)
	require.Equal(t, "user:usr_op", decodeStateEvent(t, e.evts.all()[0]).Actor)
}

func TestOperateIdentitiesValidation(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	ids := []string{"idt_1"}
	cases := []struct {
		name string
		req  OperationRequest
		ids  []string
	}{
		{"unknown operation", OperationRequest{Operation: "explode"}, ids},
		{"ban without duration", OperationRequest{Operation: OpBan}, ids},
		{"cooldown without duration", OperationRequest{Operation: OpCooldown}, ids},
		{"permanent cooldown", OperationRequest{Operation: OpCooldown, Duration: durationx.Permanent}, ids},
		{"permanent quarantine", OperationRequest{Operation: OpQuarantine, Duration: durationx.Permanent}, ids},
		{"bad scope", OperationRequest{Operation: OpCooldown, Scope: "galaxy", Duration: durationx.MustParse("1m")}, ids},
		{"endpoint scope without group", OperationRequest{Operation: OpCooldown, Scope: scopeEndpoint, Duration: durationx.MustParse("1m")}, ids},
		{"unknown group", OperationRequest{Operation: OpCooldown, Scope: scopeEndpoint, EndpointGroupID: "eg_nope", Duration: durationx.MustParse("1m")}, ids},
		{"no ids", OperationRequest{Operation: OpDisable}, []string{""}},
		{"too many ids", OperationRequest{Operation: OpDisable}, make([]string, MaxOperationIDs+1)},
	}
	for i := range cases[len(cases)-1].ids {
		cases[len(cases)-1].ids[i] = "idt_" + strconv.Itoa(i)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, tc.ids, tc.req)
			require.Error(t, err)
			appErr, ok := apperr.As(err)
			require.True(t, ok)
			require.Contains(t, []apperr.Reason{apperr.ReasonInvalidArgument, apperr.ReasonEndpointGroupUnknown}, appErr.Reason)
		})
	}
	_, err := op.OperateIdentities(e.ctx, operatorUser(), nil, ids, OperationRequest{Operation: OpDisable})
	require.Error(t, err)
}

func TestOperateIdentitiesPermissions(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	e.addIdentity(identitySeed{ID: "idt_a", Key: 101})
	e.addIdentity(identitySeed{ID: "idt_b", Key: 201, Site: e.siteB})
	ids := []string{"idt_a", "idt_b", "idt_missing"}
	req := OperationRequest{Operation: OpDisable}

	cases := []struct {
		name      string
		principal func(t *testing.T) *authz.Principal
		succeeded []string
	}{
		{"viewer", func(*testing.T) *authz.Principal { return viewerUser() }, nil},
		{"site restricted operator", func(*testing.T) *authz.Principal { return operatorUser(siteBID) }, []string{"idt_b"}},
		{"token scoped to site a", func(t *testing.T) *authz.Principal { return tokenPrincipal(t, "identity:write:site-a") }, []string{"idt_a"}},
		{"token without scope", func(t *testing.T) *authz.Principal { return tokenPrincipal(t, "lease:acquire") }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.exec(`UPDATE identities SET state = 'active'`)
			p := tc.principal(t)
			res, err := op.OperateIdentities(e.ctx, p, e.ns, ids, req)
			require.NoError(t, err)
			require.Equal(t, 3, res.Matched)
			require.Equal(t, len(tc.succeeded), res.Succeeded)
			failed := failureReasons(res)
			require.Equal(t, FailureNotFound, failed["idt_missing"])
			entries := e.audit.all()
			wantResult := audit.ResultOK
			if len(tc.succeeded) == 0 {
				wantResult = audit.ResultError // not_found is not an authorization failure
			}
			require.Equal(t, wantResult, entries[len(entries)-1].Result)
			for _, id := range []string{"idt_a", "idt_b"} {
				state, _, _, _ := e.identityState(id)
				if contains(tc.succeeded, id) {
					require.Equal(t, StateDisabled, state)
				} else {
					require.Equal(t, FailurePermissionDenied, failed[id])
					require.Equal(t, StateActive, state)
				}
			}
		})
	}
}

func TestOperateIdentitiesAuditsDenials(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	e.addIdentity(identitySeed{ID: "idt_a", Key: 101})
	e.addIdentity(identitySeed{ID: "idt_b", Key: 201, Site: e.siteB})
	res, err := op.OperateIdentities(e.ctx, operatorUser(siteBID), e.ns, []string{"idt_a"}, OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"idt_a": FailurePermissionDenied}, failureReasons(res))
	entries := e.audit.all()
	require.Len(t, entries, 1)
	require.Equal(t, audit.ResultDenied, entries[0].Result)

	_, err = op.OperateIdentities(e.ctx, operatorUser(siteBID), e.ns, []string{"idt_a", "idt_b"}, OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	require.Equal(t, audit.ResultOK, e.audit.all()[1].Result)
	_, err = op.OperateIdentities(e.ctx, operatorUser(siteBID), e.ns, []string{"idt_b"}, OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	require.Equal(t, audit.ResultError, e.audit.all()[2].Result, "invalid transitions are errors, not denials")
}

func TestOperateIdentitiesCooldownAndResetStats(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, Client: "app"})
	e.addIdentity(identitySeed{ID: "idt_3", Key: 103, NoRedis: true})
	e.addIdentity(identitySeed{ID: "idt_4", Key: 104, State: StateRetired})
	ids := []string{"idt_1", "idt_2", "idt_3", "idt_4"}

	res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, ids, OperationRequest{
		Operation: OpCooldown, Scope: scopeEndpoint, EndpointGroupID: e.search.ID, Duration: durationx.MustParse("30m"), Reason: "slow down",
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, map[string]string{"idt_2": FailureInvalidArgument, "idt_3": FailureNotInHotState, "idt_4": FailureInvalidTransition}, failureReasons(res))
	require.Contains(t, e.hs(e.search.Key, 101), "|"+ms(fixed.Add(30*time.Minute))+"|")
	evs := e.stateEvents("idt_1")
	require.Len(t, evs, 1)
	require.Equal(t, []string{OpCooldown, scopeEndpoint, e.search.ID, "slow down"}, []string{evs[0].Action, evs[0].Scope, evs[0].EG, evs[0].Reason})
	require.Empty(t, e.zmembers(e.keys.RecentCooldowns(siteAKey, e.search.Key)), "manual cooldowns are not revert records")

	// A shorter cooldown on an identity already cooling succeeds without an event.
	res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1", "idt_2"}, OperationRequest{Operation: OpCooldown, Scope: "identity", Duration: durationx.MustParse("1h")})
	require.NoError(t, err)
	require.Equal(t, 2, res.Succeeded)
	res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1"}, OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("1m")})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	require.Len(t, e.stateEvents("idt_1"), 2)
	require.Equal(t, ms(fixed.Add(time.Hour)), e.hget(e.keys.Identity(siteAKey, 102), "scd"))

	// reset_stats at every level.
	e.setHS(e.search.Key, 101, "10.00|1|50|8|9|0|0|3")
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 101)).FieldValue().FieldValue("gs", "5").Build())
	counter := e.keys.Counter(siteAKey, "i101", "captcha", 86400000)
	e.do(e.rdb.B().Hset().Key(counter).FieldValue().FieldValue("1", "3").Build())
	for _, scope := range []string{scopeEndpoint, scopeSite, scopeAll} {
		res, err = op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1", "idt_3"}, OperationRequest{Operation: OpResetStats, Scope: scope, EndpointGroupID: e.search.ID})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded)
		require.Equal(t, map[string]string{"idt_3": FailureNotInHotState}, failureReasons(res))
	}
	require.Equal(t, "70.00|"+ms(fixed)+"|0|0|0|0|0|3", e.hs(e.search.Key, 101))
	require.Empty(t, e.hget(e.keys.Identity(siteAKey, 101), "gs"))
	require.Empty(t, e.hget(counter, "1"))
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateActive, state)
	require.Len(t, e.stateEvents("idt_1"), 5)
}

func TestOperateIdentitiesConcurrent(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	const n = 30
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "idt_" + strconv.Itoa(i)
		e.addIdentity(identitySeed{ID: ids[i], Key: int64(300 + i)})
	}
	var wg sync.WaitGroup
	results := make([]BulkResult, 4)
	for w := range results {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, ids, OperationRequest{Operation: OpBan, Duration: durationx.MustParse("1h")})
			if err != nil {
				t.Error(err)
			}
			results[w] = res
		}(w)
	}
	wg.Wait()
	total := 0
	for _, r := range results {
		total += r.Succeeded
		require.Equal(t, n, r.Succeeded+len(r.Failed))
	}
	require.Equal(t, n, total, "every identity is banned exactly once")
	var events int
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT count(*) FROM state_events WHERE action = 'ban'`).Scan(&events))
	require.Equal(t, n, events)
}

func TestOperateIdentitiesHotFailuresAreBestEffort(t *testing.T) {
	e := newEnv(t, true)
	e.hot.err = fmt.Errorf("hot state unavailable")
	op := e.operator()
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, NoRedis: true})
	res, err := op.OperateIdentities(e.ctx, operatorUser(), e.ns, []string{"idt_1"}, OperationRequest{Operation: OpDisable})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	state, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateDisabled, state)
}

func TestOperatorRequiresPrincipal(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	_, err := op.OperateIdentities(e.ctx, nil, e.ns, []string{"idt_1"}, OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))
	_, err = op.OperateAccount(e.ctx, nil, e.ns, "acc_1", OperationRequest{Operation: OpDisable})
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))
	_, _, err = op.RevertActions(e.ctx, nil, e.ns, RevertRequest{From: time.Now().Add(-time.Hour)})
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))
	st, _, _, _ := e.identityState("idt_1")
	require.Equal(t, StateActive, st)
	require.Empty(t, e.audit.all())
}
