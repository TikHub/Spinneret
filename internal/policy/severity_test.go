package policy

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSeverity(t *testing.T) {
	tests := []struct {
		action PlannedAction
		want   int
	}{
		{PlannedAction{Action: ActionActivate}, 0},
		{PlannedAction{Action: ActionCooldown, Duration: day}, 1},
		{PlannedAction{Action: ActionQuarantine}, 2},
		{PlannedAction{Action: ActionExpire}, 3},
		{PlannedAction{Action: ActionBan, Duration: time.Hour}, 4},
		{PlannedAction{Action: ActionBan, Permanent: true}, 5},
		{PlannedAction{Action: "nuke", Severity: 9}, -1},
	}
	for _, tc := range tests {
		t.Run(string(tc.action.Action), func(t *testing.T) {
			require.Equal(t, tc.want, Severity(tc.action))
		})
	}
}

func TestMostSevere(t *testing.T) {
	cool := func(scope ActionScope, d time.Duration, idx int) PlannedAction {
		return PlannedAction{Action: ActionCooldown, Scope: scope, Duration: d, RuleIndex: idx, Source: SourceRule}
	}
	withSeverity := func(a PlannedAction) PlannedAction {
		a.Severity = Severity(a)
		return a
	}
	activate := PlannedAction{Action: ActionActivate, Scope: ScopeIdentity, RuleIndex: -1, Source: SourceLifecycle}
	tempBan := PlannedAction{Action: ActionBan, Scope: ScopeIdentity, Duration: 1000 * time.Hour, RuleIndex: 3}
	permBan := PlannedAction{Action: ActionBan, Scope: ScopeIdentity, Permanent: true, RuleIndex: 4}
	expire := PlannedAction{Action: ActionExpire, Scope: ScopeIdentity, RuleIndex: 2}
	quarantine := PlannedAction{Action: ActionQuarantine, Scope: ScopeIdentity, Duration: time.Minute, RuleIndex: 5}

	tests := []struct {
		name string
		in   []PlannedAction
		want []PlannedAction
	}{
		{"empty", nil, nil},
		{"single keeps and sets severity", []PlannedAction{cool(ScopeIdentityEndpoint, time.Minute, 0)}, []PlannedAction{withSeverity(cool(ScopeIdentityEndpoint, time.Minute, 0))}},
		{"permanent beats long temporary ban", []PlannedAction{tempBan, permBan}, []PlannedAction{withSeverity(permBan)}},
		{"temporary ban beats expire", []PlannedAction{expire, tempBan}, []PlannedAction{withSeverity(tempBan)}},
		{"expire beats quarantine", []PlannedAction{quarantine, expire}, []PlannedAction{withSeverity(expire)}},
		{"quarantine beats long cooldown", []PlannedAction{cool(ScopeIdentitySite, 100*day, 0), quarantine}, []PlannedAction{withSeverity(quarantine)}},
		{"cooldown beats activate", []PlannedAction{activate, cool(ScopeIdentityEndpoint, time.Second, 7)}, []PlannedAction{withSeverity(cool(ScopeIdentityEndpoint, time.Second, 7))}},
		{"activate alone", []PlannedAction{activate}, []PlannedAction{withSeverity(activate)}},
		{"longer duration wins tie", []PlannedAction{cool(ScopeIdentitySite, time.Minute, 0), cool(ScopeIdentityEndpoint, time.Hour, 1)}, []PlannedAction{withSeverity(cool(ScopeIdentityEndpoint, time.Hour, 1))}},
		{"broader identity scope wins equal duration", []PlannedAction{cool(ScopeIdentityEndpoint, time.Hour, 0), cool(ScopeIdentitySite, time.Hour, 1)}, []PlannedAction{withSeverity(cool(ScopeIdentitySite, time.Hour, 1))}},
		{"broader proxy scope wins equal duration", []PlannedAction{cool(ScopeProxySite, time.Hour, 0), cool(ScopeProxy, time.Hour, 1)}, []PlannedAction{withSeverity(cool(ScopeProxy, time.Hour, 1))}},
		{"earlier rule index wins full tie", []PlannedAction{cool(ScopeIdentitySite, time.Hour, 5), cool(ScopeIdentitySite, time.Hour, 2)}, []PlannedAction{withSeverity(cool(ScopeIdentitySite, time.Hour, 2))}},
		{"first wins identical", []PlannedAction{{Action: ActionCooldown, Scope: ScopeProxy, Duration: time.Hour, RuleName: "a"}, {Action: ActionCooldown, Scope: ScopeProxy, Duration: time.Hour, RuleName: "b"}},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeProxy, Duration: time.Hour, RuleName: "a", Severity: 1}}},
		{"one action per subject ordered identity account proxy", []PlannedAction{
			cool(ScopeProxySite, time.Minute, 0),
			{Action: ActionBan, Scope: ScopeAccount, Permanent: true, RuleIndex: 1},
			cool(ScopeAccount, time.Hour, 2),
			cool(ScopeIdentityEndpoint, time.Minute, 3),
			{Action: ActionQuarantine, Scope: ScopeProxy, Duration: time.Hour, RuleIndex: 4},
			activate,
		}, []PlannedAction{
			withSeverity(cool(ScopeIdentityEndpoint, time.Minute, 3)),
			withSeverity(PlannedAction{Action: ActionBan, Scope: ScopeAccount, Permanent: true, RuleIndex: 1}),
			withSeverity(PlannedAction{Action: ActionQuarantine, Scope: ScopeProxy, Duration: time.Hour, RuleIndex: 4}),
		}},
		{"unknown scopes share one group after known ones", []PlannedAction{
			{Action: ActionCooldown, Scope: "zeta", Duration: time.Minute},
			{Action: ActionCooldown, Scope: "alpha", Duration: time.Hour},
			{Action: ActionCooldown, Scope: "zeta", Duration: time.Minute},
			cool(ScopeProxy, time.Second, 0),
		}, []PlannedAction{
			withSeverity(cool(ScopeProxy, time.Second, 0)),
			{Action: ActionCooldown, Scope: "alpha", Duration: time.Hour, Severity: 1},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var input []PlannedAction
			if tc.in != nil {
				input = append([]PlannedAction(nil), tc.in...)
			}
			require.Equal(t, tc.want, MostSevere(input))
			require.Equal(t, tc.in, input, "input must not be modified")
		})
	}
	require.Equal(t, -1, scopeBreadth("moon"))
}

func TestCooldownDuration(t *testing.T) {
	const maxDuration = time.Duration(math.MaxInt64)
	tests := []struct {
		name        string
		base, max   time.Duration
		multiplier  float64
		maxExponent int
		streak      int
		jitter      float64
		want        time.Duration
	}{
		// Design doc §8.3: base 60s, multiplier 2, max 30m → 1, 2, 4, 8, 16, 30 minutes.
		{"doc streak 1", time.Minute, 30 * time.Minute, 2, 10, 1, 1, time.Minute},
		{"doc streak 2", time.Minute, 30 * time.Minute, 2, 10, 2, 1, 2 * time.Minute},
		{"doc streak 3", time.Minute, 30 * time.Minute, 2, 10, 3, 1, 4 * time.Minute},
		{"doc streak 4", time.Minute, 30 * time.Minute, 2, 10, 4, 1, 8 * time.Minute},
		{"doc streak 5", time.Minute, 30 * time.Minute, 2, 10, 5, 1, 16 * time.Minute},
		{"doc streak 6", time.Minute, 30 * time.Minute, 2, 10, 6, 1, 30 * time.Minute},
		{"doc streak 100", time.Minute, 30 * time.Minute, 2, 10, 100, 1, 30 * time.Minute},
		{"streak zero counts as one", time.Minute, time.Hour, 2, 10, 0, 1, time.Minute},
		{"negative streak", time.Minute, time.Hour, 2, 10, -5, 1, time.Minute},
		{"max exponent caps growth", time.Minute, 24 * time.Hour, 2, 2, 10, 1, 4 * time.Minute},
		{"zero max exponent", time.Minute, 24 * time.Hour, 2, 0, 10, 1, time.Minute},
		{"negative max exponent", time.Minute, 24 * time.Hour, 2, -1, 10, 1, time.Minute},
		{"multiplier one is constant", 5 * time.Minute, 24 * time.Hour, 1, 10, 9, 1, 5 * time.Minute},
		{"multiplier below one treated as one", 5 * time.Minute, 24 * time.Hour, 0.5, 10, 9, 1, 5 * time.Minute},
		{"multiplier nan treated as one", 5 * time.Minute, 24 * time.Hour, math.NaN(), 10, 9, 1, 5 * time.Minute},
		{"fractional multiplier", time.Second, time.Hour, 1.5, 10, 3, 1, 2250 * time.Millisecond},
		{"no cap when max is zero", time.Second, 0, 10, 10, 4, 1, 1000 * time.Second},
		{"jitter low", time.Minute, time.Hour, 1, 10, 1, 0.8, 48 * time.Second},
		{"jitter high", time.Minute, time.Hour, 1, 10, 1, 1.2, 72 * time.Second},
		{"jitter clamped low", time.Minute, time.Hour, 1, 10, 1, 0, 48 * time.Second},
		{"jitter clamped high", time.Minute, time.Hour, 1, 10, 1, 9, 72 * time.Second},
		{"jitter nan", time.Minute, time.Hour, 1, 10, 1, math.NaN(), time.Minute},
		{"jitter applies after cap", time.Minute, 30 * time.Minute, 2, 10, 20, 1.2, 36 * time.Minute},
		{"zero base", 0, time.Hour, 2, 10, 3, 1, 0},
		{"negative base", -time.Second, time.Hour, 2, 10, 3, 1, 0},
		{"sub millisecond base rounds up to 1ms", time.Nanosecond, time.Hour, 1, 10, 1, 1, time.Millisecond},
		{"rounds to nearest millisecond", 1500 * time.Microsecond, time.Hour, 1, 10, 1, 1, 2 * time.Millisecond},
		{"huge growth saturates", 24 * time.Hour, 0, 1e10, 64, 64, 1.2, maxDuration},
		{"infinite growth saturates", 24 * time.Hour, 0, math.Inf(1), 64, 64, 1, maxDuration},
		{"near max rounds to milliseconds", maxDuration - time.Millisecond, 0, 1, 0, 1, 1, time.Duration(9223372036854) * time.Millisecond},
		{"rounding up past max saturates", time.Duration(9223372036854774784), 0, 1, 0, 1, 1, maxDuration},
		{"capped by huge max", time.Hour, maxDuration, 1e6, 10, 11, 1, maxDuration},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CooldownDuration(tc.base, tc.max, tc.multiplier, tc.maxExponent, tc.streak, tc.jitter)
			require.Equal(t, tc.want, got)
		})
	}
}
