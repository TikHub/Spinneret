package action

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/policy"
)

func TestExpiryJob(t *testing.T) {
	e := newEnv(t, true)
	// Site B identities go straight back to active when a ban expires.
	spec := policy.Default(policy.KindAction).(*policy.ActionSpec)
	spec.BanExpiryState = policy.BanExpiryActive
	compiled, err := policy.CompileAction([]*policy.ActionSpec{spec})
	require.NoError(t, err)
	catalogtest.SetPolicies(e.defB, catalogtest.Policies{Action: compiled})

	op := e.operator()
	fixed := time.Now().UTC().Truncate(time.Microsecond)
	op.now = func() time.Time { return fixed }
	past, future := fixed.Add(-time.Minute), fixed.Add(time.Hour)

	e.addIdentity(identitySeed{ID: "idt_ban_due", Key: 101, State: StateBanned, BanUntil: &past})
	e.addIdentity(identitySeed{ID: "idt_ban_future", Key: 102, State: StateBanned, BanUntil: &future})
	e.addIdentity(identitySeed{ID: "idt_ban_perm", Key: 103, State: StateBanned})
	e.addIdentity(identitySeed{ID: "idt_quar_due", Key: 104, State: StateQuarantined, QuarUntil: &past})
	e.addIdentity(identitySeed{ID: "idt_quar_future", Key: 105, State: StateQuarantined, QuarUntil: &future})
	e.addIdentity(identitySeed{ID: "idt_b_due", Key: 201, Site: e.siteB, State: StateBanned, BanUntil: &past})
	e.addAccount("acc_due", 7, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned', ban_until = $1 WHERE id = 'acc_due'`, past)
	e.addIdentity(identitySeed{ID: "idt_acc_member", Key: 106, State: StateBanned, BanUntil: &past, AccountID: "acc_due", AccountKey: 7})
	e.addAccount("acc_perm", 8, e.siteA, StateActive)
	e.exec(`UPDATE accounts SET state = 'banned' WHERE id = 'acc_perm'`)
	e.addIdentity(identitySeed{ID: "idt_perm_member", Key: 107, State: StateBanned, BanUntil: &past, AccountID: "acc_perm", AccountKey: 8})
	e.addProxy("pxy_due", 55, StateActive)
	e.exec(`UPDATE proxies SET state = 'banned', ban_until = $1 WHERE id = 'pxy_due'`, past)
	e.addProxy("pxy_quar", 56, StateActive)
	e.exec(`UPDATE proxies SET state = 'quarantined', ban_until = $1 WHERE id = 'pxy_quar'`, past)
	e.addProxy("pxy_future", 57, StateActive)
	e.exec(`UPDATE proxies SET state = 'banned', ban_until = $1 WHERE id = 'pxy_future'`, future)

	job := op.ExpiryJob()
	require.NoError(t, job.Validate())
	require.Equal(t, jobs.Leader, job.Mode)
	require.Equal(t, 10*time.Second, job.Interval)
	require.NoError(t, job.Run(e.ctx))

	expect := map[string]string{
		"idt_ban_due": StatePending, "idt_ban_future": StateBanned, "idt_ban_perm": StateBanned,
		"idt_quar_due": StatePending, "idt_quar_future": StateQuarantined, "idt_b_due": StateActive,
		"idt_acc_member": StatePending, "idt_perm_member": StateBanned,
	}
	for id, want := range expect {
		st, _, _, _ := e.identityState(id)
		require.Equal(t, want, st, id)
	}
	accState, _, _ := e.accountState("acc_due")
	require.Equal(t, StateActive, accState)
	accState, _, _ = e.accountState("acc_perm")
	require.Equal(t, StateBanned, accState)
	var p1, p2, p3 string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT (SELECT state FROM proxies WHERE id='pxy_due'), (SELECT state FROM proxies WHERE id='pxy_quar'), (SELECT state FROM proxies WHERE id='pxy_future')`).Scan(&p1, &p2, &p3))
	require.Equal(t, []string{StateActive, StateActive, StateBanned}, []string{p1, p2, p3})

	// Redis follows, eligible groups get the identities back.
	require.Equal(t, StatePending, e.hget(e.keys.Identity(siteAKey, 101), "st"))
	_, inReady := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.True(t, inReady)
	require.Equal(t, StateActive, e.hget(e.keys.Identity(siteBKey, 201), "st"))
	require.Equal(t, StateActive, e.hget(e.keys.Account(siteAKey, 7), "st"))

	evs := e.stateEvents("idt_ban_due")
	require.Len(t, evs, 1)
	require.Equal(t, []string{OpUnban, StateBanned, StatePending, ActorSystem, ReasonBanExpired}, []string{evs[0].Action, evs[0].From, evs[0].To, evs[0].Actor, evs[0].Reason})
	require.Equal(t, OpUnquarantine, e.stateEvents("idt_quar_due")[0].Action)
	require.Equal(t, OpUnquarantine, e.stateEvents("pxy_quar")[0].Action)
	require.Len(t, e.stateEvents("acc_due"), 1)
	require.ElementsMatch(t, []string{"pxy_due", "pxy_quar"}, e.hot.proxies[0].IDs)
	require.NotEmpty(t, e.hot.accounts)
	require.Len(t, e.evts.all(), 7)

	// A second pass is a no-op.
	require.NoError(t, op.RunExpiry(e.ctx))
	require.Len(t, e.stateEvents("idt_ban_due"), 1)
}

func TestExpiryUsesTenancyFallback(t *testing.T) {
	e := newEnv(t, true)
	op := e.operator()
	past := time.Now().UTC().Add(-time.Minute)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StateQuarantined, QuarUntil: &past})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, State: StateBanned, BanUntil: &past})
	// The catalog no longer knows the namespace (e.g. stale snapshot).
	e.cat.Remove(testNamespace)
	require.NoError(t, op.RunExpiry(e.ctx))
	for _, id := range []string{"idt_1", "idt_2"} {
		st, _, _, _ := e.identityState(id)
		require.Equal(t, StatePending, st)
		evs := e.stateEvents(id)
		require.Len(t, evs, 1)
	}
	var tenant string
	require.NoError(t, e.pool.QueryRow(e.ctx, `SELECT tenant_id FROM state_events WHERE subject_id = 'idt_1'`).Scan(&tenant))
	require.Equal(t, testTenant, tenant)
	require.ElementsMatch(t, []string{"idt_1", "idt_2"}, e.hot.syncedIdentities())
}
