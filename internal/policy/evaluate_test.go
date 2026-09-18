package policy

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustAction(t testing.TB, src string) *ActionSpec {
	t.Helper()
	spec, err := ParseYAML(KindAction, []byte(src))
	require.NoError(t, err)
	return spec.(*ActionSpec)
}

func compileActions(t testing.TB, specs ...*ActionSpec) *CompiledAction {
	t.Helper()
	c, err := CompileAction(specs)
	require.NoError(t, err)
	return c
}

func defaultAction(t testing.TB) *CompiledAction {
	t.Helper()
	return compileActions(t, Default(KindAction).(*ActionSpec))
}

const (
	day  = 24 * time.Hour
	week = 7 * day
)

func TestEvaluateDefaultPolicy(t *testing.T) {
	c := defaultAction(t)
	captcha24h := CounterRequest{Subject: SubjectIdentity, Outcome: OutcomeCaptcha, Window: day}
	empty10m := CounterRequest{Subject: SubjectIdentity, Outcome: OutcomeEmpty, Window: 10 * time.Minute}
	tests := []struct {
		name string
		in   EvalInput
		want []PlannedAction
	}{
		{"rate limited streak 1", EvalInput{Outcome: OutcomeRateLimited, Blame: BlameBoth, HasProxy: true, EndpointStreak: 1},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: time.Minute, Severity: 1, RuleIndex: 0, RuleName: "rate-limited-cooldown", Source: SourceRule}}},
		{"rate limited streak 4 uses endpoint streak", EvalInput{Outcome: OutcomeRateLimited, Blame: BlameBoth, EndpointStreak: 4, SiteStreak: 1},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: 8 * time.Minute, Severity: 1, RuleIndex: 0, RuleName: "rate-limited-cooldown", Source: SourceRule}}},
		{"rate limited blamed on proxy only", EvalInput{Outcome: OutcomeRateLimited, Blame: BlameProxy, HasProxy: true, EndpointStreak: 3}, nil},
		{"empty below count", EvalInput{Outcome: OutcomeEmpty, Blame: BlameIdentity, Counts: map[CounterRequest]int64{empty10m: 2}}, nil},
		{"empty missing counter", EvalInput{Outcome: OutcomeEmpty, Blame: BlameIdentity}, nil},
		{"empty at count", EvalInput{Outcome: OutcomeEmpty, Blame: BlameIdentity, EndpointStreak: 9, Counts: map[CounterRequest]int64{empty10m: 3}},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: 5 * time.Minute, Severity: 1, RuleIndex: 1, RuleName: "empty-cooldown", Source: SourceRule}}},
		{"captcha cooldown uses site streak and cap", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, SiteStreak: 5, Counts: map[CounterRequest]int64{captcha24h: 2}},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentitySite, Duration: 6 * time.Hour, Severity: 1, RuleIndex: 2, RuleName: "captcha-cooldown", Source: SourceRule}}},
		{"captcha third in 24h bans 12h", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, SiteStreak: 3, Counts: map[CounterRequest]int64{captcha24h: 3}},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Duration: 12 * time.Hour, Severity: 4, RuleIndex: 3, RuleName: "captcha-ban", Source: SourceRule}}},
		{"second ban in 7d escalates to 72h", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, Counts: map[CounterRequest]int64{captcha24h: 3},
			BanCounts: map[time.Duration]int64{week: 1, 30 * day: 1}},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Duration: 72 * time.Hour, Severity: 4, RuleIndex: 3, RuleName: "captcha-ban", Source: SourceEscalation}}},
		{"third ban in 30d escalates to permanent", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, Counts: map[CounterRequest]int64{captcha24h: 4},
			BanCounts: map[time.Duration]int64{week: 2, 30 * day: 2}},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Permanent: true, Severity: 5, RuleIndex: 3, RuleName: "captcha-ban", Source: SourceEscalation}}},
		{"old bans only in 30d window", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, Counts: map[CounterRequest]int64{captcha24h: 3},
			BanCounts: map[time.Duration]int64{week: 0, 30 * day: 2}},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Permanent: true, Severity: 5, RuleIndex: 3, RuleName: "captcha-ban", Source: SourceEscalation}}},
		{"one old ban does not escalate", EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, Counts: map[CounterRequest]int64{captcha24h: 3},
			BanCounts: map[time.Duration]int64{30 * day: 1}},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Duration: 12 * time.Hour, Severity: 4, RuleIndex: 3, RuleName: "captcha-ban", Source: SourceRule}}},
		{"auth invalid expires", EvalInput{Outcome: OutcomeAuthInvalid, Blame: BlameIdentity, IdentityState: "pending"},
			[]PlannedAction{{Action: ActionExpire, Scope: ScopeIdentity, Severity: 3, RuleIndex: 4, RuleName: "auth-invalid-expire", Source: SourceRule}}},
		{"banned with account", EvalInput{Outcome: OutcomeBanned, Blame: BlameIdentity, HasAccount: true},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeAccount, Permanent: true, Severity: 5, RuleIndex: 5, RuleName: "banned-account", Source: SourceRule}}},
		{"banned without account degrades to identity", EvalInput{Outcome: OutcomeBanned, Blame: BlameIdentity},
			[]PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Permanent: true, Severity: 5, RuleIndex: 5, RuleName: "banned-account", Source: SourceRule}}},
		{"banned blamed on proxy skips account", EvalInput{Outcome: OutcomeBanned, Blame: BlameProxy, HasAccount: true, HasProxy: true}, nil},
		{"proxy error with proxy", EvalInput{Outcome: OutcomeProxyError, Blame: BlameProxy, HasProxy: true, ProxyStreak: 3, EndpointStreak: 1},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeProxySite, Duration: 2 * time.Minute, Severity: 1, RuleIndex: 6, RuleName: "proxy-error-cooldown", Source: SourceRule}}},
		{"proxy error without proxy", EvalInput{Outcome: OutcomeProxyError, Blame: BlameProxy}, nil},
		{"proxy error blamed on identity", EvalInput{Outcome: OutcomeProxyError, Blame: BlameIdentity, HasProxy: true}, nil},
		{"network error no rule", EvalInput{Outcome: OutcomeNetworkError, Blame: BlameProxy, HasProxy: true}, nil},
		{"unknown outcome string", EvalInput{Outcome: "weird", Blame: BlameBoth}, nil},
		{"success active identity", EvalInput{Outcome: OutcomeSuccess, Blame: BlameNone, IdentityState: "active"}, nil},
		{"success pending identity activates", EvalInput{Outcome: OutcomeSuccess, Blame: BlameNone, IdentityState: "pending"},
			[]PlannedAction{{Action: ActionActivate, Scope: ScopeIdentity, Severity: 0, RuleIndex: -1, RuleName: RuleNameActivate, Source: SourceLifecycle}}},
		{"failure on pending identity does not activate", EvalInput{Outcome: OutcomeEmpty, Blame: BlameIdentity, IdentityState: "pending"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, c.Evaluate(tc.in))
		})
	}
}

func TestEvaluateEmptyBlameUsesDefault(t *testing.T) {
	c := defaultAction(t)
	tests := []struct {
		name string
		in   EvalInput
		want []PlannedAction
	}{
		{"rate limited defaults to both", EvalInput{Outcome: OutcomeRateLimited, EndpointStreak: 1},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: time.Minute, Severity: 1, RuleIndex: 0, RuleName: "rate-limited-cooldown", Source: SourceRule}}},
		{"proxy error defaults to proxy", EvalInput{Outcome: OutcomeProxyError, HasProxy: true, ProxyStreak: 1},
			[]PlannedAction{{Action: ActionCooldown, Scope: ScopeProxySite, Duration: 2 * time.Minute, Severity: 1, RuleIndex: 6, RuleName: "proxy-error-cooldown", Source: SourceRule}}},
		{"auth invalid defaults to identity", EvalInput{Outcome: OutcomeAuthInvalid},
			[]PlannedAction{{Action: ActionExpire, Scope: ScopeIdentity, Severity: 3, RuleIndex: 4, RuleName: "auth-invalid-expire", Source: SourceRule}}},
		{"explicit none is respected", EvalInput{Outcome: OutcomeAuthInvalid, Blame: BlameNone}, nil},
		{"unknown outcome has no blame", EvalInput{Outcome: "weird", GlobalScore: 1, GlobalSamples: 50}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, c.Evaluate(tc.in))
		})
	}

	v, ok := c.Observation(OutcomeCaptcha, "")
	require.True(t, ok)
	require.Zero(t, v)
	_, ok = c.Observation(OutcomeNetworkError, "")
	require.False(t, ok, "network_error defaults to proxy blame")
	v, ok = c.ProxyObservation(OutcomeNetworkError, "")
	require.True(t, ok)
	require.Equal(t, 40.0, v)
	_, ok = c.ProxyObservation(OutcomeProxyError, BlameIdentity)
	require.False(t, ok)
}

func TestEvaluateCooldownLadderFromDesignDoc(t *testing.T) {
	c := defaultAction(t)
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		got := c.Evaluate(EvalInput{Outcome: OutcomeRateLimited, Blame: BlameIdentity, EndpointStreak: i + 1})
		require.Len(t, got, 1)
		require.Equal(t, w, got[0].Duration, "streak %d", i+1)
	}
}

func TestEvaluateEveryScopeActionPair(t *testing.T) {
	type pair struct {
		action   ActionKind
		scope    ActionScope
		extra    string
		want     ActionScope
		duration time.Duration
		perm     bool
	}
	pairs := []pair{
		{ActionCooldown, ScopeIdentityEndpoint, "base: 1m\n    multiplier: 2", ScopeIdentityEndpoint, 2 * time.Minute, false}, // endpoint streak 2
		{ActionCooldown, ScopeIdentitySite, "base: 1m\n    multiplier: 2", ScopeIdentitySite, 4 * time.Minute, false},         // site streak 3
		{ActionCooldown, ScopeIdentity, "base: 1m\n    multiplier: 2", ScopeIdentitySite, 4 * time.Minute, false},             // identity ≡ identity_site
		{ActionCooldown, ScopeAccount, "base: 1m\n    multiplier: 2", ScopeAccount, 4 * time.Minute, false},                   // site streak
		{ActionCooldown, ScopeProxySite, "base: 1m\n    multiplier: 2", ScopeProxySite, 8 * time.Minute, false},               // proxy streak 4
		{ActionCooldown, ScopeProxy, "base: 1m\n    multiplier: 2", ScopeProxy, 8 * time.Minute, false},
		{ActionBan, ScopeIdentity, "duration: 2h", ScopeIdentity, 2 * time.Hour, false},
		{ActionBan, ScopeAccount, "duration: 3h", ScopeAccount, 3 * time.Hour, false},
		{ActionBan, ScopeProxy, "duration: permanent", ScopeProxy, 0, true},
		{ActionExpire, ScopeIdentity, "", ScopeIdentity, 0, false},
		{ActionQuarantine, ScopeIdentity, "duration: 1h", ScopeIdentity, time.Hour, false},
		{ActionQuarantine, ScopeProxy, "duration: 90m", ScopeProxy, 90 * time.Minute, false},
	}
	for _, p := range pairs {
		t.Run(string(p.action)+"/"+string(p.scope), func(t *testing.T) {
			src := "name: pair\nrules:\n  - when: {outcome: forbidden}\n    action: " + string(p.action) + "\n    scope: " + string(p.scope) + "\n"
			if p.extra != "" {
				src += "    " + p.extra + "\n"
			}
			c := compileActions(t, mustAction(t, src))
			got := c.Evaluate(EvalInput{
				Outcome: OutcomeForbidden, Blame: BlameBoth, HasAccount: true, HasProxy: true,
				EndpointStreak: 2, SiteStreak: 3, ProxyStreak: 4,
			})
			require.Len(t, got, 1)
			a := got[0]
			require.Equal(t, p.action, a.Action)
			require.Equal(t, p.want, a.Scope)
			require.Equal(t, p.duration, a.Duration)
			require.Equal(t, p.perm, a.Permanent)
			require.Equal(t, Severity(a), a.Severity)
			require.Equal(t, "pair.rules[0]", a.RuleName)
		})
	}
}

func TestEvaluateAccountCooldownDegradation(t *testing.T) {
	c := compileActions(t, mustAction(t, `
name: acct
rules:
  - when: {outcome: forbidden, count: {gte: 2, within: 1h}}
    action: cooldown
    scope: account
    base: 10m
    multiplier: 3
`))
	req := CounterRequest{Subject: SubjectAccount, Outcome: OutcomeForbidden, Window: time.Hour}
	require.Equal(t, []CounterRequest{req}, c.CounterRequests(OutcomeForbidden))

	withAccount := c.Evaluate(EvalInput{Outcome: OutcomeForbidden, Blame: BlameIdentity, HasAccount: true, SiteStreak: 2, Counts: map[CounterRequest]int64{req: 2}})
	require.Equal(t, []PlannedAction{{Action: ActionCooldown, Scope: ScopeAccount, Duration: 30 * time.Minute, Severity: 1, RuleIndex: 0, RuleName: "acct.rules[0]", Source: SourceRule}}, withAccount)

	noAccount := c.Evaluate(EvalInput{Outcome: OutcomeForbidden, Blame: BlameIdentity, SiteStreak: 1, Counts: map[CounterRequest]int64{req: 5}})
	require.Equal(t, []PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentitySite, Duration: 10 * time.Minute, Severity: 1, RuleIndex: 0, RuleName: "acct.rules[0]", Source: SourceRule}}, noAccount)

	require.Empty(t, c.Evaluate(EvalInput{Outcome: OutcomeForbidden, Blame: BlameIdentity, HasAccount: true, Counts: map[CounterRequest]int64{req: 1}}))
}

func TestEvaluateHealth(t *testing.T) {
	c := defaultAction(t)
	endpointLow := PlannedAction{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: 6 * time.Hour, Severity: 1, RuleIndex: -1, RuleName: RuleNameEndpointLow, Source: SourceHealth}
	quarantine := PlannedAction{Action: ActionQuarantine, Scope: ScopeIdentity, Duration: 24 * time.Hour, Severity: 2, RuleIndex: -1, RuleName: RuleNameQuarantine, Source: SourceHealth}
	healthy := EvalInput{EndpointScore: 70, EndpointSamples: 50, GlobalScore: 70, GlobalSamples: 50}
	with := func(base EvalInput, mod func(*EvalInput)) EvalInput {
		mod(&base)
		return base
	}
	tests := []struct {
		name string
		in   EvalInput
		want []PlannedAction
	}{
		{"endpoint low", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.EndpointScore, in.EndpointSamples = OutcomeForbidden, BlameIdentity, 14.9, 10
		}), []PlannedAction{endpointLow}},
		{"endpoint score at threshold", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.EndpointScore, in.EndpointSamples = OutcomeForbidden, BlameIdentity, 15, 10
		}), nil},
		{"endpoint too few samples", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.EndpointScore, in.EndpointSamples = OutcomeForbidden, BlameIdentity, 1, 9
		}), nil},
		{"endpoint already cooling long enough", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.EndpointScore, in.EndpointSamples = OutcomeForbidden, BlameIdentity, 1, 10
			in.EndpointCooldownRemaining = 6 * time.Hour
		}), nil},
		{"endpoint cooldown nearly over is re-applied", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.EndpointScore, in.EndpointSamples = OutcomeForbidden, BlameIdentity, 1, 10
			in.EndpointCooldownRemaining = time.Minute
		}), []PlannedAction{endpointLow}},
		{"global low quarantines", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeForbidden, BlameIdentity, 19.99, 10
		}), []PlannedAction{quarantine}},
		{"global score at threshold", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeForbidden, BlameIdentity, 20, 10
		}), nil},
		{"global too few samples", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeForbidden, BlameIdentity, 0, 9
		}), nil},
		{"quarantine beats endpoint cooldown", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame = OutcomeForbidden, BlameIdentity
			in.EndpointScore, in.EndpointSamples, in.GlobalScore, in.GlobalSamples = 1, 10, 1, 10
		}), []PlannedAction{quarantine}},
		{"failure outcome blamed elsewhere still checks health", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeEmpty, BlameNone, 5, 10
		}), []PlannedAction{quarantine}},
		{"network error blamed on identity checks health", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeNetworkError, BlameIdentity, 5, 10
		}), []PlannedAction{quarantine}},
		{"network error blamed on proxy skips health", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.HasProxy, in.GlobalScore, in.GlobalSamples = OutcomeNetworkError, BlameProxy, true, 5, 10
		}), nil},
		{"success skips health", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples, in.EndpointScore, in.EndpointSamples = OutcomeSuccess, BlameNone, 5, 10, 5, 10
		}), nil},
		{"target error skips health", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.GlobalScore, in.GlobalSamples = OutcomeTargetError, BlameNone, 5, 10
		}), nil},
		{"rule ban outranks health quarantine", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.HasAccount = OutcomeBanned, BlameIdentity, false
			in.GlobalScore, in.GlobalSamples = 5, 10
		}), []PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Permanent: true, Severity: 5, RuleIndex: 5, RuleName: "banned-account", Source: SourceRule}}},
		{"account ban and identity quarantine are separate subjects", with(healthy, func(in *EvalInput) {
			in.Outcome, in.Blame, in.HasAccount = OutcomeBanned, BlameIdentity, true
			in.GlobalScore, in.GlobalSamples = 5, 10
		}), []PlannedAction{quarantine, {Action: ActionBan, Scope: ScopeAccount, Permanent: true, Severity: 5, RuleIndex: 5, RuleName: "banned-account", Source: SourceRule}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, c.Evaluate(tc.in))
		})
	}
}

func TestEvaluateActivation(t *testing.T) {
	c := compileActions(t, mustAction(t, `
name: act
rules:
  - when: {outcome: success}
    action: cooldown
    scope: proxy_site
    base: 1s
  - when: {outcome: [success, empty]}
    action: cooldown
    scope: identity_endpoint
    base: 1m
`))
	activate := PlannedAction{Action: ActionActivate, Scope: ScopeIdentity, RuleIndex: -1, RuleName: RuleNameActivate, Source: SourceLifecycle}
	proxyCooldown := PlannedAction{Action: ActionCooldown, Scope: ScopeProxySite, Duration: time.Second, Severity: 1, RuleIndex: 0, RuleName: "act.rules[0]", Source: SourceRule}

	// Blame excludes the identity: the identity rule is skipped, activation and the proxy action coexist.
	got := c.Evaluate(EvalInput{Outcome: OutcomeSuccess, Blame: BlameProxy, HasProxy: true, IdentityState: "pending"})
	require.Equal(t, []PlannedAction{activate, proxyCooldown}, got)

	// Another identity action drops the activation.
	got = c.Evaluate(EvalInput{Outcome: OutcomeSuccess, Blame: BlameIdentity, IdentityState: "pending"})
	require.Equal(t, []PlannedAction{{Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: time.Minute, Severity: 1, RuleIndex: 1, RuleName: "act.rules[1]", Source: SourceRule}}, got)
}

func TestEvaluateShadowModeStillPlans(t *testing.T) {
	spec := Default(KindAction).(*ActionSpec)
	spec.Mode = ModeShadow
	c := compileActions(t, spec)
	require.True(t, c.Shadow())
	require.Equal(t, ModeShadow, c.Mode)
	got := c.Evaluate(EvalInput{Outcome: OutcomeAuthInvalid, Blame: BlameIdentity})
	require.Len(t, got, 1)
	require.Equal(t, ActionExpire, got[0].Action)
	require.False(t, defaultAction(t).Shadow())
}

func TestEvaluateEscalationDetails(t *testing.T) {
	c := compileActions(t, mustAction(t, `
name: esc
rules:
  - {name: long-ban, when: {outcome: banned}, action: ban, scope: identity, duration: 100h}
  - {name: proxy-ban, when: {outcome: proxy_error}, action: ban, scope: proxy, duration: 1h}
  - {name: acct-ban, when: {outcome: forbidden}, action: ban, scope: account, duration: 1h}
escalation:
  - when: {bans: {gte: 2, within: 7d}}
    duration: 72h
`))
	require.Equal(t, []time.Duration{week}, c.EscalationWindows())
	bans := map[time.Duration]int64{week: 5}

	got := c.Evaluate(EvalInput{Outcome: OutcomeBanned, Blame: BlameIdentity, BanCounts: bans})
	require.Equal(t, 100*time.Hour, got[0].Duration, "escalation never shortens a ban")
	require.Equal(t, SourceRule, got[0].Source)

	got = c.Evaluate(EvalInput{Outcome: OutcomeProxyError, Blame: BlameProxy, HasProxy: true, BanCounts: bans})
	require.Equal(t, time.Hour, got[0].Duration, "proxy bans are not escalated with identity ban history")

	got = c.Evaluate(EvalInput{Outcome: OutcomeForbidden, Blame: BlameIdentity, HasAccount: true, BanCounts: bans})
	require.Equal(t, PlannedAction{Action: ActionBan, Scope: ScopeAccount, Duration: 72 * time.Hour, Severity: 4, RuleIndex: 2, RuleName: "acct-ban", Source: SourceEscalation}, got[0])

	// Tie-breaking among steps with equal thresholds (not reachable through
	// validation, exercised directly).
	tie := &CompiledAction{escalation: []escalationStep{
		{gte: 1, window: day, duration: 2 * time.Hour},
		{gte: 1, window: day, duration: 5 * time.Hour},
		{gte: 1, window: day, permanent: true},
		{gte: 1, window: day, duration: 3 * time.Hour},
	}}
	pa := PlannedAction{Action: ActionBan, Scope: ScopeIdentity, Duration: time.Hour}
	tie.escalate(&pa, &EvalInput{})
	require.True(t, pa.Permanent)
	require.Zero(t, pa.Duration)

	tie.escalation = tie.escalation[:2]
	pa = PlannedAction{Action: ActionBan, Scope: ScopeIdentity, Duration: time.Hour}
	tie.escalate(&pa, &EvalInput{})
	require.Equal(t, 5*time.Hour, pa.Duration)
	require.True(t, stepLonger(&escalationStep{permanent: true}, &escalationStep{duration: time.Hour}))
	require.False(t, stepLonger(&escalationStep{duration: time.Hour}, &escalationStep{permanent: true}))
}

func TestEvaluateJitter(t *testing.T) {
	c := defaultAction(t)
	tests := []struct {
		name   string
		jitter func() float64
		want   time.Duration
	}{
		{"nil", nil, time.Minute},
		{"zero", func() float64 { return 0 }, 48 * time.Second},
		{"half", func() float64 { return 0.5 }, time.Minute},
		{"almost one", func() float64 { return 0.999999 }, 72 * time.Second},
		{"negative", func() float64 { return -3 }, 48 * time.Second},
		{"nan", func() float64 { return math.NaN() }, 48 * time.Second},
		{"above one", func() float64 { return 7 }, 72 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Evaluate(EvalInput{Outcome: OutcomeRateLimited, Blame: BlameBoth, EndpointStreak: 1, Jitter: tc.jitter})
			require.Len(t, got, 1)
			require.Equal(t, tc.want, got[0].Duration)
		})
	}
}

func TestCounterRequestsAndWindows(t *testing.T) {
	c := defaultAction(t)
	require.Equal(t, []CounterRequest{{Subject: SubjectIdentity, Outcome: OutcomeEmpty, Window: 10 * time.Minute}}, c.CounterRequests(OutcomeEmpty))
	require.Equal(t, []CounterRequest{{Subject: SubjectIdentity, Outcome: OutcomeCaptcha, Window: day}}, c.CounterRequests(OutcomeCaptcha))
	require.Nil(t, c.CounterRequests(OutcomeRateLimited))
	require.Nil(t, c.CounterRequests("nope"))
	require.Equal(t, []time.Duration{week, 30 * day}, c.EscalationWindows())

	custom := compileActions(t, mustAction(t, `
name: counters
rules:
  - {when: {outcome: [captcha, forbidden], count: {gte: 2, within: 1h}}, action: cooldown, scope: identity_endpoint, base: 1m}
  - {when: {outcome: captcha, count: {gte: 3, within: 60m}}, action: ban, scope: identity, duration: 1h}
  - {when: {outcome: captcha, count: {gte: 3, within: 1h}}, action: cooldown, scope: proxy, base: 1m}
  - {when: {outcome: captcha, count: {gte: 3, within: 1d}}, action: ban, scope: account, duration: 1h}
  - {when: {outcome: captcha}, action: cooldown, scope: identity_site, base: 1m}
`))
	require.Equal(t, []CounterRequest{
		{Subject: SubjectIdentity, Outcome: OutcomeCaptcha, Window: time.Hour},
		{Subject: SubjectProxy, Outcome: OutcomeCaptcha, Window: time.Hour},
		{Subject: SubjectAccount, Outcome: OutcomeCaptcha, Window: day},
	}, custom.CounterRequests(OutcomeCaptcha))
	require.Equal(t, []CounterRequest{{Subject: SubjectIdentity, Outcome: OutcomeForbidden, Window: time.Hour}}, custom.CounterRequests(OutcomeForbidden))
	require.Nil(t, custom.EscalationWindows())

	// Returned slices are copies.
	reqs := c.CounterRequests(OutcomeEmpty)
	reqs[0].Window = time.Nanosecond
	require.Equal(t, 10*time.Minute, c.CounterRequests(OutcomeEmpty)[0].Window)
	wins := c.EscalationWindows()
	wins[0] = 0
	require.Equal(t, week, c.EscalationWindows()[0])
}

func TestCompileActionChain(t *testing.T) {
	root := mustAction(t, `
name: root
mode: shadow
rules:
  - {name: r1, when: {outcome: captcha}, action: cooldown, scope: identity_site, base: 1m, failure_reset_after: 3h}
escalation:
  - when: {bans: {gte: 2, within: 1d}}
    duration: 1h
health: {quarantine_score: 50, observations: {auth_invalid: 5}}
ban_expiry_state: active
cross_attribution: {enabled: false}
`)
	mid := mustAction(t, `
name: mid
extends: root
rules:
  - {when: {outcome: captcha}, action: ban, scope: identity, duration: 10m}
escalation:
  - when: {bans: {gte: 3, within: 2d}}
    duration: 2h
`)
	leaf := mustAction(t, `
name: leaf
extends: mid
rules:
  - {when: {outcome: forbidden}, action: cooldown, scope: identity_endpoint, base: 1m, failure_reset_after: 2h}
`)
	c := compileActions(t, root, mid, leaf)
	require.Equal(t, ModeEnforce, c.Mode, "mode comes from the leaf")
	require.Equal(t, BanExpiryPending, c.BanExpiryState)
	require.True(t, c.CrossAttribution.Enabled)
	require.Equal(t, DefaultQuarantineScore, c.Health.QuarantineScore)
	require.Equal(t, []time.Duration{2 * day}, c.EscalationWindows(), "escalation comes from the nearest ancestor defining it")
	require.Equal(t, 3*time.Hour, c.FailureResetAfter())

	got := c.Evaluate(EvalInput{Outcome: OutcomeCaptcha, Blame: BlameIdentity, BanCounts: map[time.Duration]int64{2 * day: 2}})
	require.Equal(t, []PlannedAction{{Action: ActionBan, Scope: ScopeIdentity, Duration: 2 * time.Hour, Severity: 4, RuleIndex: 1, RuleName: "mid.rules[0]", Source: SourceEscalation}}, got)
	got = c.Evaluate(EvalInput{Outcome: OutcomeForbidden, Blame: BlameIdentity, EndpointStreak: 1})
	require.Equal(t, 2, got[0].RuleIndex)
	require.Equal(t, "leaf.rules[0]", got[0].RuleName)

	rootOnly := compileActions(t, root)
	require.Equal(t, ModeShadow, rootOnly.Mode)
	require.Equal(t, BanExpiryActive, rootOnly.BanExpiryState)
	require.False(t, rootOnly.CrossAttribution.Enabled)
	require.Equal(t, 50.0, rootOnly.Health.QuarantineScore)

	// Compiled health observations are copied.
	rootOnly.Health.Observations[OutcomeAuthInvalid] = 99
	require.Equal(t, 5.0, root.Health.Observations[OutcomeAuthInvalid])
	v, ok := rootOnly.Observation(OutcomeAuthInvalid, BlameIdentity)
	require.True(t, ok)
	require.Equal(t, 5.0, v)
}

func TestCompileActionErrors(t *testing.T) {
	_, err := CompileAction(nil)
	require.ErrorContains(t, err, "chain is empty")
	_, err = CompileAction([]*ActionSpec{Default(KindAction).(*ActionSpec), nil})
	require.ErrorContains(t, err, "chain element 1 is nil")
	_, err = CompileAction([]*ActionSpec{{Name: "raw"}})
	require.ErrorContains(t, err, `compile action policy "raw"`)

	var nilAction *CompiledAction
	require.Nil(t, nilAction.Evaluate(EvalInput{Outcome: OutcomeBanned, Blame: BlameIdentity}))
	require.Nil(t, nilAction.CounterRequests(OutcomeEmpty))
	require.Nil(t, nilAction.EscalationWindows())
	require.Equal(t, DefaultFailureResetAfter, nilAction.FailureResetAfter())
	require.False(t, nilAction.Shadow())
	v, ok := nilAction.Observation(OutcomeSuccess, BlameNone)
	require.False(t, ok)
	require.Zero(t, v)
}

func TestFailureResetAfter(t *testing.T) {
	noCooldown := compileActions(t, mustAction(t, "name: a\nrules:\n  - {when: {outcome: banned}, action: ban, scope: identity, duration: 1h}\n"))
	require.Equal(t, time.Hour, noCooldown.FailureResetAfter())
	short := compileActions(t, mustAction(t, "name: a\nrules:\n  - {when: {outcome: captcha}, action: cooldown, scope: identity_site, base: 1m, failure_reset_after: 10m}\n"))
	require.Equal(t, 10*time.Minute, short.FailureResetAfter())
	require.Equal(t, time.Hour, defaultAction(t).FailureResetAfter())
}

func TestObservation(t *testing.T) {
	c := defaultAction(t)
	tests := []struct {
		outcome string
		blame   Blame
		value   float64
		affects bool
	}{
		{OutcomeSuccess, BlameNone, 100, true},
		{OutcomeSuccess, BlameProxy, 100, true},
		{OutcomeEmpty, BlameIdentity, 60, true},
		{OutcomeEmpty, BlameProxy, 60, false},
		{OutcomeNetworkError, BlameIdentity, 50, true},
		{OutcomeNetworkError, BlameProxy, 50, false},
		{OutcomeRateLimited, BlameBoth, 30, true},
		{OutcomeForbidden, BlameIdentity, 10, true},
		{OutcomeCaptcha, BlameIdentity, 0, true},
		{OutcomeCaptcha, BlameNone, 0, false},
		{OutcomeAuthInvalid, BlameIdentity, 0, false},
		{OutcomeTargetError, BlameBoth, 0, false},
		{"bogus", BlameBoth, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.outcome+"/"+string(tc.blame), func(t *testing.T) {
			v, ok := c.Observation(tc.outcome, tc.blame)
			require.Equal(t, tc.value, v)
			require.Equal(t, tc.affects, ok)
		})
	}

	overridden := compileActions(t, mustAction(t, "name: o\nhealth:\n  observations: {success: 90, captcha: 5, banned: 0}\n"))
	v, ok := overridden.Observation(OutcomeSuccess, BlameNone)
	require.True(t, ok)
	require.Equal(t, 90.0, v)
	v, ok = overridden.Observation(OutcomeCaptcha, BlameIdentity)
	require.True(t, ok)
	require.Equal(t, 5.0, v)
	v, ok = overridden.Observation(OutcomeBanned, BlameIdentity)
	require.True(t, ok)
	require.Zero(t, v)
	v, ok = overridden.Observation(OutcomeEmpty, BlameIdentity)
	require.True(t, ok)
	require.Equal(t, 60.0, v, "non-overridden outcomes keep defaults")
}

func TestProxyObservation(t *testing.T) {
	c := defaultAction(t)
	tests := []struct {
		outcome string
		blame   Blame
		value   float64
		affects bool
	}{
		{OutcomeSuccess, BlameNone, 100, true},
		{OutcomeProxyError, BlameProxy, 0, true},
		{OutcomeProxyError, BlameIdentity, 0, false},
		{OutcomeNetworkError, BlameProxy, 40, true},
		{OutcomeNetworkError, BlameNone, 40, false},
		{OutcomeRateLimited, BlameBoth, 30, true},
		{OutcomeRateLimited, BlameIdentity, 30, false},
		{OutcomeCaptcha, BlameProxy, 0, false},
		{OutcomeEmpty, BlameBoth, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.outcome+"/"+string(tc.blame), func(t *testing.T) {
			v, ok := c.ProxyObservation(tc.outcome, tc.blame)
			require.Equal(t, tc.value, v)
			require.Equal(t, tc.affects, ok)
		})
	}
}

func TestPlanRuleUnknownScope(t *testing.T) {
	c := &CompiledAction{}
	_, ok := c.planRule(&actionMatcher{action: ActionCooldown, scope: "moon"}, &EvalInput{Blame: BlameBoth})
	require.False(t, ok)
	require.Equal(t, 7, streakFor(ScopeAccount, &EvalInput{SiteStreak: 7}))
}

func BenchmarkEvaluateDefaultCaptcha(b *testing.B) {
	c := defaultAction(b)
	in := EvalInput{
		Outcome: OutcomeCaptcha, Blame: BlameIdentity, IdentityState: "active", HasProxy: true,
		Counts:     map[CounterRequest]int64{{Subject: SubjectIdentity, Outcome: OutcomeCaptcha, Window: day}: 3},
		BanCounts:  map[time.Duration]int64{week: 1, 30 * day: 1},
		SiteStreak: 3, EndpointScore: 40, EndpointSamples: 20, GlobalScore: 50, GlobalSamples: 20,
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Evaluate(in)
	}
}
