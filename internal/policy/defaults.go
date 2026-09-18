package policy

import (
	"embed"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

//go:embed defaults/*.yaml
var defaultFiles embed.FS

// DefaultYAML returns the embedded YAML source of the built-in default policy
// of kind, or "" for unknown kinds. The document parses into exactly
// Default(kind).
func DefaultYAML(kind Kind) string {
	if !ValidKind(kind) {
		return ""
	}
	data, err := defaultFiles.ReadFile("defaults/" + string(kind) + ".yaml")
	if err != nil {
		return ""
	}
	return string(data)
}

// Default returns a fresh copy of the built-in default policy of kind (named
// DefaultPolicyName(kind)), or nil for unknown kinds.
func Default(kind Kind) Spec {
	switch kind {
	case KindRotation:
		s := newRotationBase()
		s.Name = DefaultPolicyName(kind)
		s.Description = "Built-in default rotation policy."
		return s
	case KindSignal:
		s := newSignalBase()
		s.Name = DefaultPolicyName(kind)
		s.Description = "Built-in default signal policy."
		s.Rules = defaultSignalRules()
		return s
	case KindAction:
		s := newActionBase()
		s.Name = DefaultPolicyName(kind)
		s.Description = "Built-in default action policy."
		s.Rules = defaultActionRules()
		s.Escalation = []EscalationStep{
			{When: EscalationWhen{Bans: CountCondition{Gte: 2, Within: days(7)}}, Duration: dur(72 * time.Hour)},
			{When: EscalationWhen{Bans: CountCondition{Gte: 3, Within: days(30)}}, Duration: durationx.Permanent},
		}
		s.ApplyDefaults()
		return s
	case KindBreaker:
		s := newBreakerBase()
		s.Name = DefaultPolicyName(kind)
		s.Description = "Built-in default breaker policy."
		return s
	}
	return nil
}

// defaultSignalRules is design doc §7.3 without the business-code rule, plus
// 401 → auth_invalid and 403 → forbidden before the success rule.
func defaultSignalRules() []SignalRule {
	return []SignalRule{
		{Name: "proxy-error", When: SignalWhen{ErrorKind: StringList{ErrorKindProxyAuth, ErrorKindConnRefused}}, Outcome: OutcomeProxyError},
		{Name: "network-error", When: SignalWhen{ErrorKind: StringList{ErrorKindTimeout, ErrorKindConnReset, ErrorKindTLS, ErrorKindDNS}}, Outcome: OutcomeNetworkError},
		{Name: "captcha", When: SignalWhen{Markers: StringList{"captcha_page"}}, Outcome: OutcomeCaptcha},
		{Name: "login-redirect", When: SignalWhen{Markers: StringList{"login_redirect"}}, Outcome: OutcomeAuthInvalid},
		{Name: "rate-limited", When: SignalWhen{HTTPStatus: statuses(429)}, Outcome: OutcomeRateLimited},
		{Name: "target-error", When: SignalWhen{HTTPStatus: &IntMatcher{Range: &IntRange{Gte: i64(500)}}}, Outcome: OutcomeTargetError},
		{Name: "empty-list", When: SignalWhen{HTTPStatus: statuses(200), Markers: StringList{"empty_list"}}, Outcome: OutcomeEmpty},
		{Name: "client-error", When: SignalWhen{HTTPStatus: statuses(400)}, Outcome: OutcomeClientError},
		{Name: "auth-invalid", When: SignalWhen{HTTPStatus: statuses(401)}, Outcome: OutcomeAuthInvalid},
		{Name: "forbidden", When: SignalWhen{HTTPStatus: statuses(403)}, Outcome: OutcomeForbidden},
		{Name: "success", When: SignalWhen{HTTPStatus: &IntMatcher{Range: &IntRange{Gte: i64(200), Lt: i64(300)}}}, Outcome: OutcomeSuccess},
	}
}

// defaultActionRules is the rule list of design doc §8.4 (without extends).
// Cooldown parameters left unset receive their defaults from ApplyDefaults.
func defaultActionRules() []ActionRule {
	return []ActionRule{
		{
			Name: "rate-limited-cooldown", When: ActionWhen{Outcome: OutcomeList{OutcomeRateLimited}},
			Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Base: dur(time.Minute), Multiplier: 2, Max: dur(30 * time.Minute),
		},
		{
			Name: "empty-cooldown", When: ActionWhen{Outcome: OutcomeList{OutcomeEmpty}, Count: &CountCondition{Gte: 3, Within: dur(10 * time.Minute)}},
			Action: ActionCooldown, Scope: ScopeIdentityEndpoint, Base: dur(5 * time.Minute),
		},
		{
			Name: "captcha-cooldown", When: ActionWhen{Outcome: OutcomeList{OutcomeCaptcha}},
			Action: ActionCooldown, Scope: ScopeIdentitySite, Base: dur(30 * time.Minute), Multiplier: 2, Max: dur(6 * time.Hour),
		},
		{
			Name: "captcha-ban", When: ActionWhen{Outcome: OutcomeList{OutcomeCaptcha}, Count: &CountCondition{Gte: 3, Within: dur(24 * time.Hour)}},
			Action: ActionBan, Scope: ScopeIdentity, Duration: dur(12 * time.Hour),
		},
		{
			Name: "auth-invalid-expire", When: ActionWhen{Outcome: OutcomeList{OutcomeAuthInvalid}},
			Action: ActionExpire, Scope: ScopeIdentity,
		},
		{
			Name: "banned-account", When: ActionWhen{Outcome: OutcomeList{OutcomeBanned}},
			Action: ActionBan, Scope: ScopeAccount, Duration: durationx.Permanent,
		},
		{
			Name: "proxy-error-cooldown", When: ActionWhen{Outcome: OutcomeList{OutcomeProxyError}},
			Action: ActionCooldown, Scope: ScopeProxySite, Base: dur(2 * time.Minute), Max: dur(30 * time.Minute),
		},
	}
}

func dur(d time.Duration) durationx.Duration { return durationx.Duration(d) }

func days(n int) durationx.Duration { return durationx.Duration(time.Duration(n) * 24 * time.Hour) }

func i64(v int64) *int64 { return &v }

func statuses(codes ...int) *IntMatcher { return &IntMatcher{Values: codes} }
