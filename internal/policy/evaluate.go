package policy

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"
)

// Sources of planned actions.
const (
	SourceRule       = "rule"
	SourceHealth     = "health"
	SourceLifecycle  = "lifecycle"
	SourceEscalation = "escalation"
)

// Rule names of actions not produced by policy rules.
const (
	RuleNameActivate    = "lifecycle.activate"
	RuleNameEndpointLow = "health.endpoint_low"
	RuleNameQuarantine  = "health.quarantine"
)

// identityStatePending is the lifecycle state that success activates.
const identityStatePending = "pending"

// Built-in proxy observation values (spec §6.4).
const (
	proxyObservationSuccess      = 100.0
	proxyObservationProxyError   = 0.0
	proxyObservationNetworkError = 40.0
	proxyObservationRateLimited  = 30.0
)

// CounterRequest identifies a sliding-window counter needed by count
// conditions: events of Outcome for Subject within Window.
type CounterRequest struct {
	Subject SubjectKind
	Outcome string
	Window  time.Duration
}

// EvalInput carries everything Evaluate needs about one classified report.
type EvalInput struct {
	// Outcome and Blame come from classification (after cross attribution).
	// An empty Blame means DefaultBlame(Outcome).
	Outcome string
	Blame   Blame
	// IdentityState is the identity lifecycle state before this report.
	IdentityState string
	HasAccount    bool
	HasProxy      bool
	// Counts holds counter values including the current report, keyed by the
	// requests returned from CounterRequests. Missing keys count as 0.
	Counts map[CounterRequest]int64
	// BanCounts holds identity bans within each escalation window, excluding
	// the ban being evaluated. Missing keys count as 0.
	BanCounts map[time.Duration]int64
	// Failure streaks after this report: identity×endpoint group, identity
	// global (max endpoint streak) and proxy×site.
	EndpointStreak, SiteStreak, ProxyStreak int
	// Health scores and sample counts after this report.
	EndpointScore   float64
	EndpointSamples int
	GlobalScore     float64
	GlobalSamples   int
	// EndpointCooldownRemaining is the remaining identity×endpoint cooldown;
	// the low-score cooldown is only planned when it is shorter.
	EndpointCooldownRemaining time.Duration
	// Jitter returns a value in [0,1); nil disables jitter (factor 1).
	Jitter func() float64
}

// PlannedAction is one action chosen for a subject. Duration is 0 for
// permanent bans, expire and activate.
type PlannedAction struct {
	Action    ActionKind
	Scope     ActionScope
	Duration  time.Duration
	Permanent bool
	Severity  int
	RuleIndex int
	RuleName  string
	Source    string
}

// CompiledAction is an immutable, precompiled action rule chain. The exported
// fields come from the leaf policy; it is safe for concurrent use as long as
// callers do not modify them.
type CompiledAction struct {
	Mode             string
	Health           HealthSpec
	CrossAttribution CrossAttributionSpec
	BanExpiryState   string

	rules             []actionMatcher
	escalation        []escalationStep
	counters          [outcomeCount][]CounterRequest
	escalationWindows []time.Duration
	observations      [outcomeCount]float64
	observed          [outcomeCount]bool
	failureResetAfter time.Duration
	health            healthThresholds
}

// actionMatcher is a compiled action rule.
type actionMatcher struct {
	index       int
	name        string
	outcomes    uint32
	hasCount    bool
	countGte    int64
	countWindow time.Duration
	subject     SubjectKind // subject of the declared scope (counter key)
	action      ActionKind
	scope       ActionScope // normalized: cooldown identity → identity_site
	base        time.Duration
	maxDur      time.Duration
	multiplier  float64
	maxExponent int
	duration    time.Duration
	permanent   bool
}

type escalationStep struct {
	gte       int64
	window    time.Duration
	duration  time.Duration
	permanent bool
}

type healthThresholds struct {
	endpointLowScore      float64
	endpointLowMinSamples int
	endpointLowCooldown   time.Duration
	quarantineScore       float64
	quarantineMinSamples  int
	quarantineDuration    time.Duration
}

// CompileAction compiles an action chain ordered root → leaf (see
// ResolveExtends). Rules are concatenated in chain order; mode, health, cross
// attribution and ban expiry state come from the leaf; escalation comes from
// the leaf when non-empty, else from the nearest ancestor that defines it.
// Every spec is validated first.
func CompileAction(chain []*ActionSpec) (*CompiledAction, error) {
	if len(chain) == 0 {
		return nil, errors.New("compile action policy: chain is empty")
	}
	for i, s := range chain {
		if s == nil {
			return nil, fmt.Errorf("compile action policy: chain element %d is nil", i)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("compile action policy %q: %w", s.Name, err)
		}
	}
	leaf := chain[len(chain)-1]
	c := &CompiledAction{
		Mode:             leaf.Mode,
		Health:           leaf.Health,
		CrossAttribution: leaf.CrossAttribution,
		BanExpiryState:   leaf.BanExpiryState,
		health: healthThresholds{
			endpointLowScore:      leaf.Health.EndpointLowScore,
			endpointLowMinSamples: leaf.Health.EndpointLowMinSamples,
			endpointLowCooldown:   leaf.Health.EndpointLowCooldown.Std(),
			quarantineScore:       leaf.Health.QuarantineScore,
			quarantineMinSamples:  leaf.Health.QuarantineMinSamples,
			quarantineDuration:    leaf.Health.QuarantineDuration.Std(),
		},
	}
	c.Health.Observations = maps.Clone(leaf.Health.Observations)
	c.compileRules(chain)
	c.compileEscalation(chain)
	c.compileObservations(leaf.Health.Observations)
	return c, nil
}

func (c *CompiledAction) compileRules(chain []*ActionSpec) {
	c.failureResetAfter = DefaultFailureResetAfter
	sawCooldown := false
	for _, s := range chain {
		for i := range s.Rules {
			r := &s.Rules[i]
			m := actionMatcher{
				index:       len(c.rules),
				name:        ruleLabel(s.Name, i, r.Name),
				subject:     r.Scope.Subject(),
				action:      r.Action,
				scope:       r.Scope,
				base:        r.Base.Std(),
				maxDur:      r.Max.Std(),
				multiplier:  r.Multiplier,
				maxExponent: r.MaxExponent,
				permanent:   r.Duration.IsPermanent(),
			}
			if !m.permanent {
				m.duration = r.Duration.Std()
			}
			if m.action == ActionCooldown && m.scope == ScopeIdentity {
				m.scope = ScopeIdentitySite
			}
			for _, o := range r.When.Outcome {
				if idx := outcomeIndex(o); idx >= 0 {
					m.outcomes |= 1 << uint(idx)
				}
			}
			if r.When.Count != nil {
				m.hasCount = true
				m.countGte = r.When.Count.Gte
				m.countWindow = r.When.Count.Within.Std()
			}
			if m.action == ActionCooldown {
				d := r.FailureResetAfter.Std()
				if !sawCooldown || d > c.failureResetAfter {
					c.failureResetAfter = d
				}
				sawCooldown = true
			}
			c.rules = append(c.rules, m)
		}
	}
	names := Outcomes()
	for idx := 0; idx < outcomeCount; idx++ {
		var reqs []CounterRequest
		for i := range c.rules {
			m := &c.rules[i]
			if !m.hasCount || m.outcomes&(1<<uint(idx)) == 0 {
				continue
			}
			req := CounterRequest{Subject: m.subject, Outcome: names[idx], Window: m.countWindow}
			if !slices.Contains(reqs, req) {
				reqs = append(reqs, req)
			}
		}
		c.counters[idx] = reqs
	}
}

func (c *CompiledAction) compileEscalation(chain []*ActionSpec) {
	for i := len(chain) - 1; i >= 0; i-- {
		if len(chain[i].Escalation) == 0 {
			continue
		}
		for _, st := range chain[i].Escalation {
			step := escalationStep{
				gte:       st.When.Bans.Gte,
				window:    st.When.Bans.Within.Std(),
				permanent: st.Duration.IsPermanent(),
			}
			if !step.permanent {
				step.duration = st.Duration.Std()
			}
			c.escalation = append(c.escalation, step)
			if !slices.Contains(c.escalationWindows, step.window) {
				c.escalationWindows = append(c.escalationWindows, step.window)
			}
		}
		return
	}
}

func (c *CompiledAction) compileObservations(overrides map[string]float64) {
	defaults := [...]struct {
		outcome string
		value   float64
	}{
		{OutcomeSuccess, 100}, {OutcomeEmpty, 60}, {OutcomeNetworkError, 50},
		{OutcomeRateLimited, 30}, {OutcomeForbidden, 10}, {OutcomeCaptcha, 0},
	}
	for _, d := range defaults {
		idx := outcomeIndex(d.outcome)
		c.observations[idx], c.observed[idx] = d.value, true
	}
	for outcome, value := range overrides {
		if idx := outcomeIndex(outcome); idx >= 0 {
			c.observations[idx], c.observed[idx] = value, true
		}
	}
}

// CounterRequests returns the distinct counters (subject, outcome, window)
// needed to evaluate count conditions of rules matching outcome, in rule
// order. The subject is that of the rule's declared scope; when an
// account-scoped rule applies to an identity without an account, callers
// should count against the identity but keep the returned key, because
// Evaluate reads the count under that key. The result is a fresh slice.
func (c *CompiledAction) CounterRequests(outcome string) []CounterRequest {
	idx := outcomeIndex(outcome)
	if c == nil || idx < 0 {
		return nil
	}
	return slices.Clone(c.counters[idx])
}

// EscalationWindows returns the distinct ban-history windows referenced by
// the escalation ladder. The result is a fresh slice.
func (c *CompiledAction) EscalationWindows() []time.Duration {
	if c == nil {
		return nil
	}
	return slices.Clone(c.escalationWindows)
}

// FailureResetAfter returns the streak reset period for observe.lua: the
// longest failure_reset_after among cooldown rules, or the default (1h) when
// the chain has no cooldown rule.
func (c *CompiledAction) FailureResetAfter() time.Duration {
	if c == nil {
		return DefaultFailureResetAfter
	}
	return c.failureResetAfter
}

// Shadow reports whether the policy runs in shadow mode.
func (c *CompiledAction) Shadow() bool { return c != nil && c.Mode == ModeShadow }

// Observation returns the identity health observation value of outcome and
// whether it affects the identity: success always does; other observed
// outcomes (health.observations overrides the built-in table) only when the
// blame includes the identity. An empty blame means DefaultBlame(outcome).
func (c *CompiledAction) Observation(outcome string, blame Blame) (value float64, affectsIdentity bool) {
	idx := outcomeIndex(outcome)
	if c == nil || idx < 0 || !c.observed[idx] {
		return 0, false
	}
	if outcome == OutcomeSuccess {
		return c.observations[idx], true
	}
	return c.observations[idx], effectiveBlame(outcome, blame).Identity()
}

// ProxyObservation returns the proxy×site health observation value of outcome
// and whether it affects the proxy: success always does; proxy_error (0),
// network_error (40) and rate_limited (30) only when the blame includes the
// proxy. Other outcomes do not affect proxies. An empty blame means
// DefaultBlame(outcome).
func (c *CompiledAction) ProxyObservation(outcome string, blame Blame) (value float64, affectsProxy bool) {
	blame = effectiveBlame(outcome, blame)
	switch outcome {
	case OutcomeSuccess:
		return proxyObservationSuccess, true
	case OutcomeProxyError:
		return proxyObservationProxyError, blame.Proxy()
	case OutcomeNetworkError:
		return proxyObservationNetworkError, blame.Proxy()
	case OutcomeRateLimited:
		return proxyObservationRateLimited, blame.Proxy()
	}
	return 0, false
}

// Evaluate plans the actions for one report, already reduced with MostSevere
// (at most one action per subject, ordered identity, account, proxy). Mode is
// informational: shadow policies still plan actions.
func (c *CompiledAction) Evaluate(in EvalInput) []PlannedAction {
	if c == nil {
		return nil
	}
	in.Blame = effectiveBlame(in.Outcome, in.Blame)
	var buf [8]PlannedAction
	planned := buf[:0]
	if in.IdentityState == identityStatePending && in.Outcome == OutcomeSuccess {
		planned = append(planned, PlannedAction{
			Action: ActionActivate, Scope: ScopeIdentity, RuleIndex: -1, RuleName: RuleNameActivate, Source: SourceLifecycle,
		})
	}
	if idx := outcomeIndex(in.Outcome); idx >= 0 {
		bit := uint32(1) << uint(idx)
		for i := range c.rules {
			m := &c.rules[i]
			if m.outcomes&bit == 0 {
				continue
			}
			if m.hasCount && in.Counts[CounterRequest{Subject: m.subject, Outcome: in.Outcome, Window: m.countWindow}] < m.countGte {
				continue
			}
			if pa, ok := c.planRule(m, &in); ok {
				planned = append(planned, pa)
			}
		}
	}
	planned = c.appendHealth(planned, &in)
	return MostSevere(planned)
}

// planRule turns a matching rule into an action, applying blame gating,
// account degradation, cooldown math and ban escalation.
func (c *CompiledAction) planRule(m *actionMatcher, in *EvalInput) (PlannedAction, bool) {
	scope := m.scope
	switch scope.Subject() {
	case SubjectIdentity:
		if !in.Blame.Identity() {
			return PlannedAction{}, false
		}
	case SubjectAccount:
		if !in.Blame.Identity() {
			return PlannedAction{}, false
		}
		if !in.HasAccount {
			if m.action == ActionCooldown {
				scope = ScopeIdentitySite
			} else {
				scope = ScopeIdentity
			}
		}
	case SubjectProxy:
		if !in.Blame.Proxy() || !in.HasProxy {
			return PlannedAction{}, false
		}
	default:
		return PlannedAction{}, false
	}
	pa := PlannedAction{Action: m.action, Scope: scope, RuleIndex: m.index, RuleName: m.name, Source: SourceRule}
	switch m.action {
	case ActionCooldown:
		pa.Duration = CooldownDuration(m.base, m.maxDur, m.multiplier, m.maxExponent, streakFor(scope, in), jitterFactor(in.Jitter))
	case ActionBan:
		if m.permanent {
			pa.Permanent = true
		} else {
			pa.Duration = m.duration
			if scope.Subject() != SubjectProxy {
				c.escalate(&pa, in)
			}
		}
	case ActionQuarantine:
		pa.Duration = m.duration
	}
	pa.Severity = Severity(pa)
	return pa, true
}

// escalate applies the matching escalation step with the highest threshold
// (ties: longer duration) to a temporary identity or account ban; proxy bans
// are never escalated because ban history is tracked per identity. A step
// never shortens the ban.
func (c *CompiledAction) escalate(pa *PlannedAction, in *EvalInput) {
	best := -1
	for i := range c.escalation {
		st := &c.escalation[i]
		if in.BanCounts[st.window]+1 < st.gte {
			continue
		}
		if best < 0 || st.gte > c.escalation[best].gte ||
			st.gte == c.escalation[best].gte && stepLonger(st, &c.escalation[best]) {
			best = i
		}
	}
	if best < 0 {
		return
	}
	st := &c.escalation[best]
	switch {
	case st.permanent:
		pa.Permanent, pa.Duration, pa.Source = true, 0, SourceEscalation
	case st.duration > pa.Duration:
		pa.Duration, pa.Source = st.duration, SourceEscalation
	}
}

func stepLonger(a, b *escalationStep) bool {
	if a.permanent != b.permanent {
		return a.permanent
	}
	return a.duration > b.duration
}

// appendHealth adds score-driven actions when the report affected the
// identity (blame includes it or the outcome is a failure).
func (c *CompiledAction) appendHealth(planned []PlannedAction, in *EvalInput) []PlannedAction {
	if !in.Blame.Identity() && !IsFailureOutcome(in.Outcome) {
		return planned
	}
	h := &c.health
	if in.EndpointSamples >= h.endpointLowMinSamples && in.EndpointScore < h.endpointLowScore &&
		in.EndpointCooldownRemaining < h.endpointLowCooldown {
		pa := PlannedAction{
			Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Duration: h.endpointLowCooldown,
			RuleIndex: -1, RuleName: RuleNameEndpointLow, Source: SourceHealth,
		}
		pa.Severity = Severity(pa)
		planned = append(planned, pa)
	}
	if in.GlobalSamples >= h.quarantineMinSamples && in.GlobalScore < h.quarantineScore {
		pa := PlannedAction{
			Action: ActionQuarantine, Scope: ScopeIdentity, Duration: h.quarantineDuration,
			RuleIndex: -1, RuleName: RuleNameQuarantine, Source: SourceHealth,
		}
		pa.Severity = Severity(pa)
		planned = append(planned, pa)
	}
	return planned
}

// effectiveBlame returns blame, or DefaultBlame(outcome) when blame is empty.
func effectiveBlame(outcome string, blame Blame) Blame {
	if blame == "" {
		return DefaultBlame(outcome)
	}
	return blame
}

// streakFor selects the failure streak matching a cooldown scope.
func streakFor(scope ActionScope, in *EvalInput) int {
	switch scope {
	case ScopeIdentityEndpoint:
		return in.EndpointStreak
	case ScopeProxySite, ScopeProxy:
		return in.ProxyStreak
	}
	return in.SiteStreak
}

// jitterFactor maps a [0,1) random source to a [0.8,1.2) factor (1 when nil).
func jitterFactor(fn func() float64) float64 {
	if fn == nil {
		return 1
	}
	j := fn()
	switch {
	case math.IsNaN(j) || j < 0:
		j = 0
	case j > 1:
		j = 1
	}
	return 0.8 + 0.4*j
}
