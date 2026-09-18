package policy

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

// Action policy modes and ban expiry states.
const (
	ModeEnforce = "enforce"
	ModeShadow  = "shadow"

	BanExpiryPending = "pending"
	BanExpiryActive  = "active"
)

// Action defaults and limits (spec §7).
const (
	DefaultCooldownMultiplier      = 1.0
	DefaultCooldownMax             = 24 * time.Hour
	DefaultCooldownMaxExponent     = 10
	DefaultFailureResetAfter       = time.Hour
	DefaultHealthAlpha             = 0.1
	DefaultHealthBaseline          = 70.0
	DefaultHealthTau               = 6 * time.Hour
	DefaultEndpointLowScore        = 15.0
	DefaultEndpointLowMinSamples   = 10
	DefaultEndpointLowCooldown     = 6 * time.Hour
	DefaultQuarantineScore         = 20.0
	DefaultQuarantineMinSamples    = 10
	DefaultQuarantineDuration      = 24 * time.Hour
	DefaultCrossAttributionWindow  = 10 * time.Minute
	DefaultProxyDistinctIdentities = 3
	DefaultIdentityDistinctProxies = 3

	maxCooldownExponent = 64
	maxBanHistoryWindow = 30 * 24 * time.Hour
	minCounterWindow    = time.Second
)

// SubjectKind is the entity an action applies to.
type SubjectKind string

// Subject kinds.
const (
	SubjectIdentity SubjectKind = "identity"
	SubjectAccount  SubjectKind = "account"
	SubjectProxy    SubjectKind = "proxy"
)

// ActionKind is a disposition applied to a subject.
type ActionKind string

// Action kinds. ActionActivate is only planned by the lifecycle (pending
// identity + success) and cannot be used in rules.
const (
	ActionCooldown   ActionKind = "cooldown"
	ActionExpire     ActionKind = "expire"
	ActionQuarantine ActionKind = "quarantine"
	ActionBan        ActionKind = "ban"
	ActionActivate   ActionKind = "activate"
)

// ActionScope is the level an action applies at.
type ActionScope string

// Action scopes.
const (
	ScopeIdentityEndpoint ActionScope = "identity_endpoint"
	ScopeIdentitySite     ActionScope = "identity_site"
	ScopeIdentity         ActionScope = "identity"
	ScopeAccount          ActionScope = "account"
	ScopeProxySite        ActionScope = "proxy_site"
	ScopeProxy            ActionScope = "proxy"
)

// Subject returns the subject kind of the scope, or "" for unknown scopes.
func (s ActionScope) Subject() SubjectKind {
	switch s {
	case ScopeIdentityEndpoint, ScopeIdentitySite, ScopeIdentity:
		return SubjectIdentity
	case ScopeAccount:
		return SubjectAccount
	case ScopeProxySite, ScopeProxy:
		return SubjectProxy
	}
	return ""
}

// ScopeAllowed reports whether action may be used with scope (spec §7):
// cooldown → identity_endpoint, identity_site, identity, account, proxy_site,
// proxy; ban → identity, account, proxy; expire → identity; quarantine →
// identity, proxy.
func ScopeAllowed(action ActionKind, scope ActionScope) bool {
	switch action {
	case ActionCooldown:
		return scope.Subject() != ""
	case ActionBan:
		return scope == ScopeIdentity || scope == ScopeAccount || scope == ScopeProxy
	case ActionExpire:
		return scope == ScopeIdentity
	case ActionQuarantine:
		return scope == ScopeIdentity || scope == ScopeProxy
	}
	return false
}

// ActionSpec is an action policy: rules turning classified reports into
// cooldowns, bans, expiry and quarantine, plus health and attribution
// settings.
type ActionSpec struct {
	Name             string               `yaml:"name" json:"name"`
	Description      string               `yaml:"description,omitempty" json:"description,omitempty"`
	Bind             *Binding             `yaml:"bind,omitempty" json:"bind,omitempty"`
	Extends          string               `yaml:"extends,omitempty" json:"extends,omitempty"`
	Mode             string               `yaml:"mode" json:"mode"`
	Rules            []ActionRule         `yaml:"rules" json:"rules"`
	Escalation       []EscalationStep     `yaml:"escalation" json:"escalation"`
	Health           HealthSpec           `yaml:"health" json:"health"`
	BanExpiryState   string               `yaml:"ban_expiry_state" json:"ban_expiry_state"`
	CrossAttribution CrossAttributionSpec `yaml:"cross_attribution" json:"cross_attribution"`
}

// ActionRule plans Action at Scope for reports matching When. Base,
// Multiplier, Max, MaxExponent and FailureResetAfter apply to cooldowns;
// Duration applies to ban (a duration or "permanent") and quarantine.
//
// Zero cooldown parameters select their defaults (multiplier 1, max 24h,
// max_exponent 10, failure_reset_after 1h). In particular max_exponent: 0
// cannot be expressed; use multiplier: 1 for a constant cooldown.
type ActionRule struct {
	Name              string             `yaml:"name,omitempty" json:"name,omitempty"`
	When              ActionWhen         `yaml:"when" json:"when"`
	Action            ActionKind         `yaml:"action" json:"action"`
	Scope             ActionScope        `yaml:"scope" json:"scope"`
	Base              durationx.Duration `yaml:"base,omitempty" json:"base,omitempty"`
	Multiplier        float64            `yaml:"multiplier,omitempty" json:"multiplier,omitempty"`
	Max               durationx.Duration `yaml:"max,omitempty" json:"max,omitempty"`
	MaxExponent       int                `yaml:"max_exponent,omitempty" json:"max_exponent,omitempty"`
	Duration          durationx.Duration `yaml:"duration,omitempty" json:"duration,omitempty"`
	FailureResetAfter durationx.Duration `yaml:"failure_reset_after,omitempty" json:"failure_reset_after,omitempty"`
}

// ActionWhen is the condition of an action rule. Count, when set, is checked
// against the counter of the reported outcome for the subject of the rule's
// scope (see CompiledAction.CounterRequests).
type ActionWhen struct {
	Outcome OutcomeList     `yaml:"outcome" json:"outcome"`
	Count   *CountCondition `yaml:"count,omitempty" json:"count,omitempty"`
}

// CountCondition requires at least Gte events within the sliding window.
type CountCondition struct {
	Gte    int64              `yaml:"gte" json:"gte"`
	Within durationx.Duration `yaml:"within" json:"within"`
}

// EscalationStep replaces the duration of a temporary ban when the identity
// was banned at least When.Bans.Gte times (the new ban included) within
// When.Bans.Within.
type EscalationStep struct {
	When     EscalationWhen     `yaml:"when" json:"when"`
	Duration durationx.Duration `yaml:"duration" json:"duration"`
}

// EscalationWhen is the condition of an escalation step.
type EscalationWhen struct {
	Bans CountCondition `yaml:"bans" json:"bans"`
}

// HealthSpec configures health scoring and score-driven actions.
type HealthSpec struct {
	Alpha                 float64            `yaml:"alpha" json:"alpha"`
	Baseline              float64            `yaml:"baseline" json:"baseline"`
	Tau                   durationx.Duration `yaml:"tau" json:"tau"`
	Observations          map[string]float64 `yaml:"observations" json:"observations"`
	EndpointLowScore      float64            `yaml:"endpoint_low_score" json:"endpoint_low_score"`
	EndpointLowMinSamples int                `yaml:"endpoint_low_min_samples" json:"endpoint_low_min_samples"`
	EndpointLowCooldown   durationx.Duration `yaml:"endpoint_low_cooldown" json:"endpoint_low_cooldown"`
	QuarantineScore       float64            `yaml:"quarantine_score" json:"quarantine_score"`
	QuarantineMinSamples  int                `yaml:"quarantine_min_samples" json:"quarantine_min_samples"`
	QuarantineDuration    durationx.Duration `yaml:"quarantine_duration" json:"quarantine_duration"`
}

// CrossAttributionSpec configures identity/proxy cross attribution (spec §6.5).
type CrossAttributionSpec struct {
	Enabled                 bool               `yaml:"enabled" json:"enabled"`
	Window                  durationx.Duration `yaml:"window" json:"window"`
	ProxyDistinctIdentities int                `yaml:"proxy_distinct_identities" json:"proxy_distinct_identities"`
	IdentityDistinctProxies int                `yaml:"identity_distinct_proxies" json:"identity_distinct_proxies"`
}

// newActionBase returns an unnamed action spec carrying every default.
func newActionBase() *ActionSpec {
	s := &ActionSpec{
		Health: HealthSpec{
			Baseline:         DefaultHealthBaseline,
			EndpointLowScore: DefaultEndpointLowScore,
			QuarantineScore:  DefaultQuarantineScore,
		},
		CrossAttribution: CrossAttributionSpec{Enabled: true},
	}
	s.ApplyDefaults()
	return s
}

// Kind implements Spec.
func (s *ActionSpec) Kind() Kind { return KindAction }

// PolicyName implements Spec.
func (s *ActionSpec) PolicyName() string {
	if s == nil {
		return ""
	}
	return s.Name
}

// PolicyBinding implements Spec.
func (s *ActionSpec) PolicyBinding() *Binding {
	if s == nil {
		return nil
	}
	return copyBinding(s.Bind)
}

// ApplyDefaults fills zero values whose zero is not a legal setting. Cooldown
// rule parameters are only defaulted on cooldown rules. health.baseline, the
// score thresholds and cross_attribution.enabled accept zero/false and are
// therefore only defaulted by the parser.
func (s *ActionSpec) ApplyDefaults() {
	if s == nil {
		return
	}
	setString(&s.Mode, ModeEnforce)
	setString(&s.BanExpiryState, BanExpiryPending)
	if s.Rules == nil {
		s.Rules = []ActionRule{}
	}
	for i := range s.Rules {
		r := &s.Rules[i]
		if r.Action != ActionCooldown {
			continue
		}
		if r.Multiplier == 0 {
			r.Multiplier = DefaultCooldownMultiplier
		}
		setDuration(&r.Max, DefaultCooldownMax)
		setInt(&r.MaxExponent, DefaultCooldownMaxExponent)
		setDuration(&r.FailureResetAfter, DefaultFailureResetAfter)
	}
	if s.Escalation == nil {
		s.Escalation = []EscalationStep{}
	}
	h := &s.Health
	if h.Alpha == 0 {
		h.Alpha = DefaultHealthAlpha
	}
	setDuration(&h.Tau, DefaultHealthTau)
	if h.Observations == nil {
		h.Observations = map[string]float64{}
	}
	setInt(&h.EndpointLowMinSamples, DefaultEndpointLowMinSamples)
	setDuration(&h.EndpointLowCooldown, DefaultEndpointLowCooldown)
	setInt(&h.QuarantineMinSamples, DefaultQuarantineMinSamples)
	setDuration(&h.QuarantineDuration, DefaultQuarantineDuration)
	c := &s.CrossAttribution
	setDuration(&c.Window, DefaultCrossAttributionWindow)
	setInt(&c.ProxyDistinctIdentities, DefaultProxyDistinctIdentities)
	setInt(&c.IdentityDistinctProxies, DefaultIdentityDistinctProxies)
}

// Validate implements Spec.
func (s *ActionSpec) Validate() error {
	if s == nil {
		return &ValidationError{Kind: KindAction, Problems: []Problem{{Message: "spec is nil"}}}
	}
	v := &validator{}
	validateCommon(v, s.Name, s.Description, s.Bind)
	validateExtends(v, s.Name, s.Extends)
	v.enum("mode", s.Mode, ModeEnforce, ModeShadow)
	names := make(map[string]int, len(s.Rules))
	for i := range s.Rules {
		path := item("rules", i)
		validateRuleName(v, path, s.Rules[i].Name, i, names)
		if reservedActionRuleName(s.Rules[i].Name) {
			v.addf(field(path, "name"), "%q is reserved for built-in health and lifecycle actions", s.Rules[i].Name)
		}
		s.Rules[i].validate(v, path)
	}
	validateEscalation(v, s.Escalation)
	s.Health.validate(v, "health")
	v.enum("ban_expiry_state", s.BanExpiryState, BanExpiryPending, BanExpiryActive)
	c := &s.CrossAttribution
	v.duration("cross_attribution.window", c.Window, time.Second, 0)
	v.minInt("cross_attribution.proxy_distinct_identities", int64(c.ProxyDistinctIdentities), 1)
	v.minInt("cross_attribution.identity_distinct_proxies", int64(c.IdentityDistinctProxies), 1)
	return v.result(KindAction, s.Name)
}

func (r *ActionRule) validate(v *validator, path string) {
	outcomePath := field(path, "when.outcome")
	if len(r.When.Outcome) == 0 {
		v.addf(outcomePath, "is required")
	}
	seen := make(map[string]struct{}, len(r.When.Outcome))
	for i, o := range r.When.Outcome {
		if !ValidOutcome(o) {
			v.addf(item(outcomePath, i), "must be one of %s (got %q)", strings.Join(Outcomes(), "|"), o)
			continue
		}
		if _, dup := seen[o]; dup {
			v.addf(item(outcomePath, i), "duplicate outcome %q", o)
		}
		seen[o] = struct{}{}
	}
	if c := r.When.Count; c != nil {
		v.minInt(field(path, "when.count.gte"), c.Gte, 1)
		v.duration(field(path, "when.count.within"), c.Within, minCounterWindow, 0)
	}
	scopeValid := r.Scope.Subject() != ""
	if !scopeValid {
		v.addf(field(path, "scope"), "must be one of identity_endpoint|identity_site|identity|account|proxy_site|proxy (got %q)", r.Scope)
	}
	switch r.Action {
	case ActionCooldown:
		r.validateCooldown(v, path)
	case ActionBan:
		r.rejectCooldownFields(v, path)
		if r.Duration == 0 {
			v.addf(field(path, "duration"), "is required for ban (a duration or permanent)")
		} else if !r.Duration.IsPermanent() {
			v.duration(field(path, "duration"), r.Duration, time.Second, 0)
		}
	case ActionQuarantine:
		r.rejectCooldownFields(v, path)
		if r.Duration == 0 {
			v.addf(field(path, "duration"), "is required for quarantine")
		} else {
			v.duration(field(path, "duration"), r.Duration, time.Second, 0)
		}
	case ActionExpire:
		r.rejectCooldownFields(v, path)
		if r.Duration != 0 {
			v.addf(field(path, "duration"), "is not supported for expire")
		}
	default:
		v.addf(field(path, "action"), "must be one of cooldown|expire|quarantine|ban (got %q)", r.Action)
		return
	}
	if scopeValid && !ScopeAllowed(r.Action, r.Scope) {
		v.addf(field(path, "scope"), "%s does not support scope %s (allowed: %s)", r.Action, r.Scope, allowedScopes(r.Action))
	}
}

func (r *ActionRule) validateCooldown(v *validator, path string) {
	if r.Base == 0 {
		v.addf(field(path, "base"), "is required for cooldown")
	} else {
		v.duration(field(path, "base"), r.Base, time.Millisecond, 0)
	}
	if !(r.Multiplier >= 1) || math.IsInf(r.Multiplier, 1) {
		v.addf(field(path, "multiplier"), "must be a finite number of at least 1 (got %v)", r.Multiplier)
	}
	v.duration(field(path, "max"), r.Max, 0, 0)
	if !r.Max.IsPermanent() && !r.Base.IsPermanent() && r.Base > 0 && r.Max < r.Base {
		v.addf(field(path, "max"), "must be at least base (%s)", r.Base)
	}
	v.intRange(field(path, "max_exponent"), int64(r.MaxExponent), 0, maxCooldownExponent)
	v.duration(field(path, "failure_reset_after"), r.FailureResetAfter, time.Second, 0)
	if r.Duration != 0 {
		v.addf(field(path, "duration"), "is not supported for cooldown (use base/multiplier/max)")
	}
}

func (r *ActionRule) rejectCooldownFields(v *validator, path string) {
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"base", r.Base != 0},
		{"multiplier", r.Multiplier != 0},
		{"max", r.Max != 0},
		{"max_exponent", r.MaxExponent != 0},
		{"failure_reset_after", r.FailureResetAfter != 0},
	} {
		if f.set {
			v.addf(field(path, f.name), "is only supported for cooldown")
		}
	}
}

// reservedActionRuleName reports whether name collides with the rule names of
// actions planned outside policy rules (state events and rule-based rollback
// would otherwise be ambiguous).
func reservedActionRuleName(name string) bool {
	switch name {
	case RuleNameActivate, RuleNameEndpointLow, RuleNameQuarantine:
		return true
	}
	return false
}

func allowedScopes(a ActionKind) string {
	switch a {
	case ActionCooldown:
		return "identity_endpoint|identity_site|identity|account|proxy_site|proxy"
	case ActionBan:
		return "identity|account|proxy"
	case ActionExpire:
		return "identity"
	case ActionQuarantine:
		return "identity|proxy"
	}
	return ""
}

// validateEscalation checks each step and that steps are ordered by strictly
// increasing ban thresholds with non-decreasing durations.
func validateEscalation(v *validator, steps []EscalationStep) {
	for i, st := range steps {
		path := item("escalation", i)
		v.minInt(field(path, "when.bans.gte"), st.When.Bans.Gte, 1)
		v.duration(field(path, "when.bans.within"), st.When.Bans.Within, time.Second, maxBanHistoryWindow)
		switch {
		case st.Duration == 0:
			v.addf(field(path, "duration"), "is required (a duration or permanent)")
		case !st.Duration.IsPermanent():
			v.duration(field(path, "duration"), st.Duration, time.Second, 0)
		}
		if i == 0 {
			continue
		}
		prev := steps[i-1]
		if st.When.Bans.Gte <= prev.When.Bans.Gte {
			v.addf(field(path, "when.bans.gte"), "must be greater than escalation[%d].when.bans.gte (%d)", i-1, prev.When.Bans.Gte)
		}
		if prev.Duration.IsPermanent() && !st.Duration.IsPermanent() ||
			!prev.Duration.IsPermanent() && !st.Duration.IsPermanent() && st.Duration < prev.Duration {
			v.addf(field(path, "duration"), "must not be shorter than escalation[%d].duration (%s)", i-1, prev.Duration)
		}
	}
}

func (h *HealthSpec) validate(v *validator, path string) {
	v.ratio(field(path, "alpha"), h.Alpha, true)
	v.score(field(path, "baseline"), h.Baseline)
	v.duration(field(path, "tau"), h.Tau, time.Second, 0)
	outcomes := make([]string, 0, len(h.Observations))
	for outcome := range h.Observations {
		outcomes = append(outcomes, outcome)
	}
	sort.Strings(outcomes)
	for _, outcome := range outcomes {
		p := field(field(path, "observations"), outcome)
		if !ValidOutcome(outcome) {
			v.addf(p, "unknown outcome %q", outcome)
			continue
		}
		v.score(p, h.Observations[outcome])
	}
	v.score(field(path, "endpoint_low_score"), h.EndpointLowScore)
	v.minInt(field(path, "endpoint_low_min_samples"), int64(h.EndpointLowMinSamples), 1)
	v.duration(field(path, "endpoint_low_cooldown"), h.EndpointLowCooldown, time.Second, 0)
	v.score(field(path, "quarantine_score"), h.QuarantineScore)
	v.minInt(field(path, "quarantine_min_samples"), int64(h.QuarantineMinSamples), 1)
	v.duration(field(path, "quarantine_duration"), h.QuarantineDuration, time.Second, 0)
}
