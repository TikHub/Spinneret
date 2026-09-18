package action

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (e *env) runOps(now time.Time, ops ...luaOp) []luaResult {
	e.t.Helper()
	res, err := newApplier(e.rdb, e.keys).run(e.ctx, siteAKey, now, ops)
	require.NoError(e.t, err)
	require.Len(e.t, res, len(ops))
	return res
}

func (e *env) setHS(eg, i int64, packed string) {
	e.do(e.rdb.B().Hset().Key(e.keys.Health(siteAKey, eg)).FieldValue().FieldValue(strconv.FormatInt(i, 10), packed).Build())
}

func (e *env) hs(eg, i int64) string {
	return e.hget(e.keys.Health(siteAKey, eg), strconv.FormatInt(i, 10))
}

func ms(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func TestApplyCooldownIdentityEndpoint(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102})
	e.setHS(e.search.Key, 101, "40.00|1757999990000|12|4|1757999990000|0|0|1757999990000")
	until := now.Add(5 * time.Minute)

	res := e.runOps(now,
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.search.Key, Until: until.UnixMilli(), Flags: flagAutomatic, Trim: 600000, PrevFail: 1, Baseline: 70},
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 102, Group: e.search.Key, Until: until.UnixMilli(), Baseline: 70},
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 999, Group: e.search.Key, Until: until.UnixMilli()},
	)
	require.Equal(t, luaResult{Applied: true, From: StateActive, To: StateActive, Until: until.UnixMilli()}, res[0])
	require.True(t, res[1].Applied)
	require.Equal(t, skipMissing, res[2].Reason)

	require.Equal(t, "40.00|1757999990000|12|4|1757999990000|"+ms(until)+"|0|1757999990000", e.hs(e.search.Key, 101))
	require.Equal(t, "70.00|"+ms(now)+"|0|0|0|"+ms(until)+"|0|0", e.hs(e.search.Key, 102))
	score, ok := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.True(t, ok)
	require.Equal(t, float64(until.UnixMilli()), score)
	_, ok = e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 101)
	require.True(t, ok, "other groups keep the identity")

	// Only the automatic cooldown is recorded, with the streak before the report.
	require.Equal(t, []string{"101|0|3"}, e.zmembers(e.keys.RecentCooldowns(siteAKey, e.search.Key)))
	require.Greater(t, e.pttl(e.keys.RecentCooldowns(siteAKey, e.search.Key)), int64(0))
	require.ElementsMatch(t, []string{"e11500:101", "e11500:102"}, e.smembers(e.keys.Dirty(siteAKey)))

	// A shorter or equal cooldown is skipped; a longer one extends.
	res = e.runOps(now.Add(time.Second),
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.search.Key, Until: until.UnixMilli(), Flags: flagAutomatic},
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 102, Group: e.search.Key, Until: until.Add(time.Minute).UnixMilli(), Flags: flagAutomatic, Trim: 600000},
	)
	require.Equal(t, luaResult{Reason: skipAlreadyCooling, From: StateActive, To: StateActive, Until: until.UnixMilli()}, res[0])
	require.True(t, res[1].Applied)
	require.Contains(t, e.zmembers(e.keys.RecentCooldowns(siteAKey, e.search.Key)), "102|"+ms(until)+"|0")

	// Entries older than the retention are trimmed.
	e.runOps(now.Add(20*time.Minute),
		luaOp{Op: luaCooldown, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.search.Key, Until: now.Add(time.Hour).UnixMilli(), Flags: flagAutomatic, Trim: 600000})
	require.Equal(t, []string{"101|" + ms(until) + "|4"}, e.zmembers(e.keys.RecentCooldowns(siteAKey, e.search.Key)))
}

func TestApplyCooldownIdentitySite(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	until := now.Add(30 * time.Minute)
	egs := groupKeys(e.siteA, "web")

	res := e.runOps(now, luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: 101, Until: until.UnixMilli(), Flags: flagAutomatic, Groups: egs})
	require.True(t, res[0].Applied)
	require.Equal(t, ms(until), e.hget(e.keys.Identity(siteAKey, 101), "scd"))
	for _, eg := range egs {
		score, ok := e.zscore(e.keys.Ready(siteAKey, eg), 101)
		require.True(t, ok)
		require.Equal(t, float64(until.UnixMilli()), score)
	}
	_, ok := e.zscore(e.keys.Ready(siteAKey, e.defApp.Key), 101)
	require.False(t, ok, "pushes never add members")
	require.Equal(t, []string{"101|0"}, e.zmembers(e.keys.RecentSiteCooldowns(siteAKey)))
	require.Equal(t, []string{"g101"}, e.smembers(e.keys.Dirty(siteAKey)))

	res = e.runOps(now, luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: 101, Until: now.Add(time.Minute).UnixMilli(), Groups: egs})
	require.Equal(t, skipAlreadyCooling, res[0].Reason)
	// Forced (manual override) cooldowns may shorten.
	res = e.runOps(now, luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: 101, Until: now.Add(time.Minute).UnixMilli(), Flags: flagForce, Groups: egs})
	require.True(t, res[0].Applied)
	require.Len(t, e.zmembers(e.keys.RecentSiteCooldowns(siteAKey)), 1)

	res = e.runOps(now,
		luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: 404, Until: until.UnixMilli()},
		luaOp{Op: luaCooldown, Scope: luaScopeIdentitySite, Subject: 101, Until: 0},
		luaOp{Op: luaCooldown, Scope: "zz", Subject: 101, Until: until.UnixMilli()},
		luaOp{Op: "nope", Subject: 101},
	)
	require.Equal(t, []string{skipMissing, "invalid", "invalid", "invalid"}, []string{res[0].Reason, res[1].Reason, res[2].Reason, res[3].Reason})
}

func TestApplyCooldownAccountAndProxy(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, Client: "app", AccountKey: 7})
	e.addProxy("pxy_1", 55, StateActive)
	until := now.Add(10 * time.Minute)

	res := e.runOps(now,
		luaOp{Op: luaCooldown, Scope: luaScopeAccount, Subject: 7, Until: until.UnixMilli(), Groups: groupKeys(e.siteA, "")},
		luaOp{Op: luaCooldown, Scope: luaScopeAccount, Subject: 8, Until: until.UnixMilli()},
		luaOp{Op: luaCooldown, Scope: luaScopeProxySite, Subject: 55, Until: until.UnixMilli()},
		luaOp{Op: luaCooldown, Scope: luaScopeProxyGlobal, Subject: 55, Until: until.Add(time.Minute).UnixMilli()},
		luaOp{Op: luaCooldown, Scope: luaScopeProxySite, Subject: 56, Until: until.UnixMilli()},
	)
	require.True(t, res[0].Applied)
	require.Equal(t, skipMissing, res[1].Reason)
	require.True(t, res[2].Applied)
	require.True(t, res[3].Applied)
	require.Equal(t, skipMissing, res[4].Reason)

	require.Equal(t, ms(until), e.hget(e.keys.Account(siteAKey, 7), "cd"))
	for _, m := range []struct {
		eg int64
		i  int64
	}{{e.defWeb.Key, 101}, {e.search.Key, 101}, {e.defApp.Key, 102}} {
		score, ok := e.zscore(e.keys.Ready(siteAKey, m.eg), m.i)
		require.True(t, ok)
		require.Equal(t, float64(until.UnixMilli()), score)
	}
	require.Equal(t, ms(until), e.hget(e.keys.ProxySite(siteAKey, 55), "cd"))
	require.Equal(t, ms(until.Add(time.Minute)), e.hget(e.keys.ProxySite(siteAKey, 55), "gcd"))
	score, ok := e.zscore(e.keys.ProxyReady(siteAKey), 55)
	require.True(t, ok)
	require.Equal(t, float64(until.Add(time.Minute).UnixMilli()), score)
	require.Contains(t, e.smembers(e.keys.Dirty(siteAKey)), "p55")

	res = e.runOps(now,
		luaOp{Op: luaCooldown, Scope: luaScopeAccount, Subject: 7, Until: until.UnixMilli()},
		luaOp{Op: luaCooldown, Scope: luaScopeProxySite, Subject: 55, Until: until.UnixMilli()},
	)
	require.Equal(t, skipAlreadyCooling, res[0].Reason)
	require.Equal(t, skipAlreadyCooling, res[1].Reason)
}

func TestApplyBanIdentity(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	for i, st := range []string{StateActive, StateBanned, StateBanned, StateDisabled, StateQuarantined} {
		e.addIdentity(identitySeed{ID: "idt_" + strconv.Itoa(i), Key: int64(100 + i), State: st})
	}
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 101)).FieldValue().FieldValue("bu", "-1").Build())
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 102)).FieldValue().FieldValue("bu", ms(now.Add(48*time.Hour))).Build())
	egs := groupKeys(e.siteA, "web")
	until := now.Add(12 * time.Hour).UnixMilli()
	ban := func(i int64, u int64, flags int) luaOp {
		return luaOp{Op: luaBan, Scope: luaScopeIdentity, Subject: i, Until: u, Flags: flags, Groups: egs}
	}
	res := e.runOps(now,
		ban(100, until, flagAutomatic|flagRecordBans),
		ban(101, until, flagRecordBans),                                    // permanent ban stays
		ban(102, until, flagRecordBans),                                    // longer ban stays
		ban(103, until, flagRecordBans),                                    // disabled is not applicable
		ban(104, luaPermanent, flagRecordBans),                             // quarantined → permanent ban
		ban(102, luaPermanent, flagRecordBans),                             // upgrade to permanent
		ban(999, until, 0),                                                 // missing
		luaOp{Op: luaBan, Scope: luaScopeIdentity, Subject: 100, Until: 0}, // invalid
	)
	require.Equal(t, luaResult{Applied: true, From: StateActive, To: StateBanned, Until: until}, res[0])
	require.Equal(t, "already_banned", res[1].Reason)
	require.Equal(t, "already_banned", res[2].Reason)
	require.Equal(t, "not_applicable", res[3].Reason)
	require.Equal(t, luaResult{Applied: true, From: StateQuarantined, To: StateBanned, Until: luaPermanent}, res[4])
	require.Equal(t, luaResult{Applied: true, From: StateBanned, To: StateBanned, Until: luaPermanent}, res[5])
	require.Equal(t, skipMissing, res[6].Reason)
	require.Equal(t, "invalid", res[7].Reason)

	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 100), "st"))
	require.Equal(t, strconv.FormatInt(until, 10), e.hget(e.keys.Identity(siteAKey, 100), "bu"))
	require.Equal(t, "-1", e.hget(e.keys.Identity(siteAKey, 104), "bu"))
	for _, eg := range egs {
		_, ok := e.zscore(e.keys.Ready(siteAKey, eg), 100)
		require.False(t, ok)
	}
	require.Equal(t, []string{ms(now)}, e.zmembers(e.keys.Bans(siteAKey, 100)))
	require.Greater(t, e.pttl(e.keys.Bans(siteAKey, 100)), int64(29*24*3600*1000))
	require.Empty(t, e.zmembers(e.keys.Bans(siteAKey, 101)))

	// Force bypasses severity rules (manual operations).
	res = e.runOps(now, ban(103, until, flagForce))
	require.True(t, res[0].Applied)
	// Ban history older than 30 days is trimmed.
	e.runOps(now.Add(31*24*time.Hour), ban(104, luaPermanent, flagForce|flagRecordBans))
	require.Equal(t, []string{ms(now.Add(31 * 24 * time.Hour))}, e.zmembers(e.keys.Bans(siteAKey, 104)))
}

func TestApplyBanAccount(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addAccount("acc_1", 7, e.siteA, StateActive)
	e.addAccount("acc_2", 8, e.siteA, StateDisabled)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, AccountKey: 7})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, Client: "app", AccountKey: 7, State: StatePending})
	e.addIdentity(identitySeed{ID: "idt_3", Key: 103, AccountKey: 7, State: StateDisabled})
	all := groupKeys(e.siteA, "")

	res := e.runOps(now,
		luaOp{Op: luaBan, Scope: luaScopeAccount, Subject: 7, Until: luaPermanent, Flags: flagAutomatic | flagRecordBans, Groups: all},
		luaOp{Op: luaBan, Scope: luaScopeAccount, Subject: 8, Until: luaPermanent, Groups: all},
		luaOp{Op: luaBan, Scope: luaScopeAccount, Subject: 9, Until: luaPermanent, Groups: all},
	)
	require.True(t, res[0].Applied)
	require.Equal(t, StateActive, res[0].From)
	require.ElementsMatch(t, []memberChange{
		{Key: 101, ID: "idt_1", From: StateActive, To: StateBanned},
		{Key: 102, ID: "idt_2", From: StatePending, To: StateBanned},
	}, res[0].Members)
	require.Equal(t, "not_applicable", res[1].Reason)
	require.Equal(t, skipMissing, res[2].Reason)

	require.Equal(t, StateBanned, e.hget(e.keys.Account(siteAKey, 7), "st"))
	require.Equal(t, "-1", e.hget(e.keys.Account(siteAKey, 7), "bu"))
	require.Equal(t, StateBanned, e.hget(e.keys.Identity(siteAKey, 102), "st"))
	require.Equal(t, StateDisabled, e.hget(e.keys.Identity(siteAKey, 103), "st"))
	_, ok := e.zscore(e.keys.Ready(siteAKey, e.defApp.Key), 102)
	require.False(t, ok)
	require.Len(t, e.zmembers(e.keys.Bans(siteAKey, 101)), 1)

	res = e.runOps(now, luaOp{Op: luaBan, Scope: luaScopeAccount, Subject: 7, Until: now.Add(time.Hour).UnixMilli(), Groups: all})
	require.Equal(t, "already_banned", res[0].Reason)
	require.Empty(t, res[0].Members)
}

func TestApplyProxyLifecycle(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addProxy("pxy_1", 55, StateActive)
	e.addProxy("pxy_2", 56, StateActive)
	e.addProxy("pxy_3", 57, StateDisabled)
	until := now.Add(time.Hour).UnixMilli()

	res := e.runOps(now,
		luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: 55, Until: until},
		luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: 55, Until: until},        // temp re-ban skipped
		luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: 55, Until: luaPermanent}, // upgrade
		luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: 57, Until: until},        // disabled
		luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: 56, Until: until},
		luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: 56, Until: until - 1},
		luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: 55, Until: until},
		luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: 57, Until: until},
		luaOp{Op: luaQuarantine, Scope: luaScopeProxy, Subject: 58, Until: until},
		luaOp{Op: luaBan, Scope: luaScopeProxy, Subject: 58, Until: until},
		luaOp{Op: luaBan, Scope: "zz", Subject: 58, Until: until},
		luaOp{Op: luaQuarantine, Scope: "zz", Subject: 58, Until: until},
	)
	require.True(t, res[0].Applied)
	require.Equal(t, "already_banned", res[1].Reason)
	require.Equal(t, luaResult{Applied: true, From: StateBanned, To: StateBanned, Until: luaPermanent}, res[2])
	require.Equal(t, "not_applicable", res[3].Reason)
	require.Equal(t, luaResult{Applied: true, From: StateActive, To: StateQuarantined, Until: until}, res[4])
	require.Equal(t, "already_quarantined", res[5].Reason)
	require.Equal(t, "more_severe", res[6].Reason)
	require.Equal(t, "not_applicable", res[7].Reason)
	require.Equal(t, []string{skipMissing, skipMissing, "invalid", "invalid"}, []string{res[8].Reason, res[9].Reason, res[10].Reason, res[11].Reason})
	require.Empty(t, e.zmembers(e.keys.ProxyReady(siteAKey)))
	require.Equal(t, StateBanned, e.hget(e.keys.ProxySite(siteAKey, 55), "st"))
	require.Equal(t, StateActive, e.hget(e.keys.ProxySite(siteBKey, 55), "st"), "other sites are handled by the executor")
}

func TestApplyExpireQuarantineActivate(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	states := []string{StateActive, StateBanned, StateExpired, StateDisabled, StateQuarantined, StatePending, StateActive}
	for i, st := range states {
		e.addIdentity(identitySeed{ID: "idt_" + strconv.Itoa(i), Key: int64(100 + i), State: st})
	}
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 104)).FieldValue().FieldValue("qu", ms(now.Add(48*time.Hour))).Build())
	egs := groupKeys(e.siteA, "web")
	until := now.Add(24 * time.Hour).UnixMilli()
	exp := func(i int64) luaOp { return luaOp{Op: luaExpire, Scope: luaScopeIdentity, Subject: i, Groups: egs} }
	qua := func(i int64) luaOp {
		return luaOp{Op: luaQuarantine, Scope: luaScopeIdentity, Subject: i, Until: until, Groups: egs}
	}
	act := func(i int64) luaOp { return luaOp{Op: luaActivate, Scope: luaScopeIdentity, Subject: i, Groups: egs} }

	res := e.runOps(now, exp(100), exp(101), exp(102), exp(103), exp(999),
		qua(101), qua(102), qua(103), qua(104), qua(105), qua(999), luaOp{Op: luaQuarantine, Scope: luaScopeIdentity, Subject: 106},
		act(105), act(106), act(999))
	reasons := make([]string, len(res))
	for i, r := range res {
		reasons[i] = r.Reason
	}
	require.Equal(t, []string{
		"", "more_severe", "already_expired", "not_applicable", skipMissing,
		"more_severe", "more_severe", "not_applicable", "already_quarantined", "", skipMissing, "invalid",
		"not_pending", "not_pending", skipMissing,
	}, reasons)
	require.Equal(t, StateExpired, e.hget(e.keys.Identity(siteAKey, 100), "st"))
	_, ok := e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 100)
	require.False(t, ok)
	require.Equal(t, StateQuarantined, e.hget(e.keys.Identity(siteAKey, 105), "st"))
	require.Equal(t, strconv.FormatInt(until, 10), e.hget(e.keys.Identity(siteAKey, 105), "qu"))

	// Activation: pending → active, act=now, scores recomputed from availability.
	e.addIdentity(identitySeed{ID: "idt_p", Key: 120, State: StatePending})
	e.setHS(e.search.Key, 120, "70.00|0|0|0|0|"+ms(now.Add(time.Minute))+"|0|0")
	res = e.runOps(now, act(120))
	require.Equal(t, luaResult{Applied: true, From: StatePending, To: StateActive}, res[0])
	require.Equal(t, ms(now), e.hget(e.keys.Identity(siteAKey, 120), "act"))
	score, ok := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 120)
	require.True(t, ok)
	require.Equal(t, float64(now.Add(time.Minute).UnixMilli()), score)
}

func TestApplySetState(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101, State: StateBanned})
	e.addIdentity(identitySeed{ID: "idt_2", Key: 102, State: StateActive})
	e.addAccount("acc_1", 7, e.siteA, StateBanned)
	e.setHS(e.search.Key, 101, "20.00|5|30|6|7|"+ms(now.Add(time.Minute))+"|0|9")
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 101)).FieldValue().FieldValue("gs", "10").FieldValue("gn", "40").Build())
	egs := groupKeys(e.siteA, "web")
	add := eligibleGroupKeys(e.siteA, "web", typeWebID)

	res := e.runOps(now,
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 101, To: StateActive, Flags: flagForce | flagResetFails | flagResetHealth,
			Groups: egs, Add: add, Baselines: map[string]float64{"11500": 65}},
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 102, To: StateBanned, Until: luaPermanent, Flags: flagForce | flagRecordBans, Groups: egs},
		luaOp{Op: luaSet, Scope: luaScopeAccount, Subject: 7, To: StateActive},
		luaOp{Op: luaSet, Scope: luaScopeAccount, Subject: 7, To: "pending"},
		luaOp{Op: luaSet, Scope: luaScopeAccount, Subject: 9, To: StateActive},
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 101, To: "bogus"},
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 999, To: StateActive},
	)
	require.Equal(t, luaResult{Applied: true, From: StateBanned, To: StateActive}, res[0])
	require.Equal(t, luaResult{Applied: true, From: StateActive, To: StateBanned, Until: luaPermanent}, res[1])
	require.Equal(t, luaResult{Applied: true, From: StateBanned, To: StateActive}, res[2])
	require.Equal(t, "invalid", res[3].Reason)
	require.Equal(t, skipMissing, res[4].Reason)
	require.Equal(t, "invalid", res[5].Reason)
	require.Equal(t, skipMissing, res[6].Reason)

	require.Equal(t, "65.00|"+ms(now)+"|0|0|0|"+ms(now.Add(time.Minute))+"|0|9", e.hs(e.search.Key, 101))
	require.Empty(t, e.hget(e.keys.Identity(siteAKey, 101), "gs"))
	require.Equal(t, ms(now), e.hget(e.keys.Identity(siteAKey, 101), "act"))
	score, ok := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.True(t, ok)
	require.Equal(t, float64(now.Add(time.Minute).UnixMilli()), score)
	_, ok = e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 101)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"e11500:101", "g101"}, e.smembers(e.keys.Dirty(siteAKey)))

	require.Equal(t, "-1", e.hget(e.keys.Identity(siteAKey, 102), "bu"))
	_, ok = e.zscore(e.keys.Ready(siteAKey, e.defWeb.Key), 102)
	require.False(t, ok)
	require.Len(t, e.zmembers(e.keys.Bans(siteAKey, 102)), 1)
	require.Equal(t, StateActive, e.hget(e.keys.Account(siteAKey, 7), "st"))

	// Groups no longer eligible lose the identity when it becomes schedulable.
	res = e.runOps(now, luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 101, To: StatePending, Groups: egs, Add: []int64{e.defWeb.Key}})
	require.True(t, res[0].Applied)
	_, ok = e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.False(t, ok)
	// Quarantine via set stores qu.
	res = e.runOps(now, luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 101, To: StateQuarantined, Until: now.Add(time.Hour).UnixMilli(), Groups: egs})
	require.Equal(t, now.Add(time.Hour).UnixMilli(), res[0].Until)
	require.Equal(t, ms(now.Add(time.Hour)), e.hget(e.keys.Identity(siteAKey, 101), "qu"))
}

func TestApplyClearAndResetStats(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	e.addIdentity(identitySeed{ID: "idt_1", Key: 101})
	cd := now.Add(10 * time.Minute)
	e.setHS(e.search.Key, 101, "30.00|5|20|5|7|"+ms(cd)+"|0|9")
	e.setHS(e.defWeb.Key, 101, "50.00|5|20|2|7|0|0|9")
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 101)).FieldValue().FieldValue("scd", ms(cd)).FieldValue("gs", "33").Build())
	e.do(e.rdb.B().Zadd().Key(e.keys.Ready(siteAKey, e.search.Key)).ScoreMember().ScoreMember(float64(cd.UnixMilli()), "101").Build())
	egs := groupKeys(e.siteA, "web")

	res := e.runOps(now,
		luaOp{Op: luaClear, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.search.Key, Until: cd.Add(-time.Minute).UnixMilli()}, // extended beyond event
		luaOp{Op: luaClear, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.defWeb.Key, Until: cd.UnixMilli()},                   // not cooling
		luaOp{Op: luaClear, Scope: luaScopeIdentityEndpoint, Subject: 101, Group: e.search.Key, Until: cd.UnixMilli(), Flags: flagResetFails, Baseline: 70},
		luaOp{Op: luaClear, Scope: luaScopeIdentitySite, Subject: 101, Until: cd.UnixMilli(), Groups: egs, Flags: flagResetHealth},
		luaOp{Op: luaClear, Scope: luaScopeIdentitySite, Subject: 101, Until: cd.UnixMilli(), Groups: egs},
		luaOp{Op: luaClear, Scope: luaScopeIdentitySite, Subject: 999, Until: cd.UnixMilli()},
		luaOp{Op: luaClear, Scope: "zz", Subject: 101},
	)
	reasons := make([]string, len(res))
	for i, r := range res {
		reasons[i] = r.Reason
	}
	require.Equal(t, []string{"not_cooling", "not_cooling", "", "", "not_cooling", skipMissing, "invalid"}, reasons)
	require.Equal(t, cd.UnixMilli(), res[2].Until)
	require.Equal(t, "0", e.hget(e.keys.Identity(siteAKey, 101), "scd"))
	// Endpoint clear reset failures; site clear with reset health reset every group's score.
	require.Equal(t, "70.00|"+ms(now)+"|0|0|0|0|0|9", e.hs(e.search.Key, 101))
	require.Equal(t, "70.00|"+ms(now)+"|0|2|7|0|0|9", e.hs(e.defWeb.Key, 101))
	score, _ := e.zscore(e.keys.Ready(siteAKey, e.search.Key), 101)
	require.Zero(t, score)

	// reset_stats: scores, streaks, global score and counters; state unchanged.
	counter := e.keys.Counter(siteAKey, "i101", "captcha", 86400000)
	e.do(e.rdb.B().Hset().Key(counter).FieldValue().FieldValue("1", "3").Build())
	e.setHS(e.search.Key, 101, "30.00|5|20|5|7|"+ms(cd)+"|0|9")
	e.do(e.rdb.B().Hset().Key(e.keys.Identity(siteAKey, 101)).FieldValue().FieldValue("gs", "12").Build())
	res = e.runOps(now,
		luaOp{Op: luaResetStats, Scope: luaScopeIdentity, Subject: 101, Groups: []int64{e.search.Key}, Global: 1,
			Counters: counterSuffixes(e.siteA, "web", 101)},
		luaOp{Op: luaResetStats, Scope: luaScopeIdentity, Subject: 999},
	)
	require.Equal(t, luaResult{Applied: true, From: StateActive, To: StateActive}, res[0])
	require.Equal(t, skipMissing, res[1].Reason)
	require.Equal(t, "70.00|"+ms(now)+"|0|0|0|"+ms(cd)+"|0|9", e.hs(e.search.Key, 101))
	require.Empty(t, e.hget(e.keys.Identity(siteAKey, 101), "gs"))
	n, err := e.rdb.Do(e.ctx, e.rdb.B().Exists().Key(counter).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestApplyResetsGlobalStreak covers the worker-owned global failure streak
// (gnf/glf): every identity-level failure reset clears it, endpoint-level
// resets and health-only resets keep it.
func TestApplyResetsGlobalStreak(t *testing.T) {
	e := newEnv(t, false)
	now := time.UnixMilli(1_758_000_000_000)
	cd := now.Add(10 * time.Minute)
	egs := groupKeys(e.siteA, "web")
	for _, key := range []int64{101, 102, 103, 104, 105, 106} {
		e.addIdentity(identitySeed{ID: "idt_" + strconv.FormatInt(key, 10), Key: key, State: StateBanned})
	}
	idKey := func(i int64) string { return e.keys.Identity(siteAKey, i) }
	seedGlobal := func(i int64) {
		e.do(e.rdb.B().Hset().Key(idKey(i)).FieldValue().
			FieldValue("gs", "40.00").FieldValue("gts", ms(now)).FieldValue("gn", "12").
			FieldValue("gnf", "6").FieldValue("glf", ms(now.Add(-time.Minute))).
			FieldValue("scd", ms(cd)).Build())
		e.setHS(e.search.Key, i, "30.00|5|20|5|7|"+ms(cd)+"|0|9")
	}
	global := func(i int64) []string {
		out := make([]string, 0, 5)
		for _, f := range []string{"gs", "gts", "gn", "gnf", "glf"} {
			out = append(out, e.hget(idKey(i), f))
		}
		return out
	}
	full := []string{"40.00", ms(now), "12", "6", ms(now.Add(-time.Minute))}
	for _, key := range []int64{101, 102, 103, 104, 105, 106} {
		seedGlobal(key)
	}

	res := e.runOps(now,
		// unban/enable/restore with reset_failures only.
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 101, To: StateActive, Flags: flagForce | flagResetFails, Groups: egs},
		// reset_health only keeps the streak.
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 102, To: StateActive, Flags: flagForce | flagResetHealth, Groups: egs, Baseline: 70},
		// No reset flags.
		luaOp{Op: luaSet, Scope: luaScopeIdentity, Subject: 103, To: StateActive, Flags: flagForce, Groups: egs},
		// Site cooldown revert with reset_failures.
		luaOp{Op: luaClear, Scope: luaScopeIdentitySite, Subject: 104, Until: cd.UnixMilli(), Groups: egs, Flags: flagResetFails},
		// Endpoint cooldown revert with reset_failures only touches the endpoint entry.
		luaOp{Op: luaClear, Scope: luaScopeIdentityEndpoint, Subject: 105, Group: e.search.Key, Until: cd.UnixMilli(), Flags: flagResetFails, Baseline: 70},
		// reset_stats of one endpoint group keeps the global fields.
		luaOp{Op: luaResetStats, Scope: luaScopeIdentity, Subject: 106, Groups: []int64{e.search.Key}, Baseline: 70},
	)
	for i, r := range res {
		require.True(t, r.Applied, "op %d: %s", i, r.Reason)
	}

	require.Equal(t, []string{"40.00", ms(now), "12", "", ""}, global(101))
	require.Equal(t, "30.00|5|20|0|0|"+ms(cd)+"|0|9", e.hs(e.search.Key, 101), "endpoint streak reset too, score kept")
	require.Equal(t, []string{"", "", "", "6", ms(now.Add(-time.Minute))}, global(102))
	require.Equal(t, full, global(103))
	require.Equal(t, []string{"40.00", ms(now), "12", "", ""}, global(104))
	require.Equal(t, "0", e.hget(idKey(104), "scd"))
	require.Equal(t, full, global(105))
	require.Equal(t, "30.00|5|20|0|0|0|0|9", e.hs(e.search.Key, 105))
	require.Equal(t, full, global(106))

	// reset_stats with the global flag clears the global score and streak.
	res = e.runOps(now, luaOp{Op: luaResetStats, Scope: luaScopeIdentity, Subject: 106, Groups: egs, Global: 1, Baseline: 70,
		Counters: counterSuffixes(e.siteA, "web", 106)})
	require.True(t, res[0].Applied)
	require.Equal(t, []string{"", "", "", "", ""}, global(106))
	require.Equal(t, StateBanned, e.hget(idKey(106), "st"), "reset_stats keeps the state")
	require.Contains(t, e.smembers(e.keys.Dirty(siteAKey)), "g106")
}
