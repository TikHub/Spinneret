package policy

import (
	"math"
	"time"
)

// Severity levels (spec §6.6).
const (
	SeverityActivate     = 0
	SeverityCooldown     = 1
	SeverityQuarantine   = 2
	SeverityExpire       = 3
	SeverityTemporaryBan = 4
	SeverityPermanentBan = 5
)

// Severity returns the severity of a planned action: activate 0 < cooldown 1
// < quarantine 2 < expire 3 < temporary ban 4 < permanent ban 5, and -1 for
// unknown action kinds. It ignores a.Severity.
func Severity(a PlannedAction) int {
	switch a.Action {
	case ActionActivate:
		return SeverityActivate
	case ActionCooldown:
		return SeverityCooldown
	case ActionQuarantine:
		return SeverityQuarantine
	case ActionExpire:
		return SeverityExpire
	case ActionBan:
		if a.Permanent {
			return SeverityPermanentBan
		}
		return SeverityTemporaryBan
	}
	return -1
}

// MostSevere keeps one action per subject (identity: identity_endpoint,
// identity_site, identity; account; proxy: proxy_site, proxy). The winner has
// the highest severity; ties go to the longer duration, then the broader
// scope, then the lower rule index (-1 for health/lifecycle actions); remaining
// ties keep the first action. Consequently an activation is dropped whenever
// another identity action exists. Results are ordered identity, account,
// proxy, followed by the winner among actions with unknown scopes, with
// Severity recomputed. The input slice is not modified.
func MostSevere(actions []PlannedAction) []PlannedAction {
	if len(actions) == 0 {
		return nil
	}
	// best holds the winning index per subject: identity, account, proxy, unknown.
	best := [4]int{-1, -1, -1, -1}
	for i := range actions {
		g := 3
		switch actions[i].Scope.Subject() {
		case SubjectIdentity:
			g = 0
		case SubjectAccount:
			g = 1
		case SubjectProxy:
			g = 2
		}
		if best[g] < 0 || moreSevere(&actions[i], &actions[best[g]]) {
			best[g] = i
		}
	}
	out := make([]PlannedAction, 0, len(best))
	for _, idx := range best {
		if idx < 0 {
			continue
		}
		a := actions[idx]
		a.Severity = Severity(a)
		out = append(out, a)
	}
	return out
}

// moreSevere reports whether a strictly outranks b.
func moreSevere(a, b *PlannedAction) bool {
	if sa, sb := Severity(*a), Severity(*b); sa != sb {
		return sa > sb
	}
	if da, db := effectiveDuration(a), effectiveDuration(b); da != db {
		return da > db
	}
	if ba, bb := scopeBreadth(a.Scope), scopeBreadth(b.Scope); ba != bb {
		return ba > bb
	}
	return a.RuleIndex < b.RuleIndex
}

func effectiveDuration(a *PlannedAction) time.Duration {
	if a.Permanent {
		return math.MaxInt64
	}
	return a.Duration
}

// scopeBreadth orders scopes of the same subject from narrow to broad.
func scopeBreadth(s ActionScope) int {
	switch s {
	case ScopeIdentityEndpoint, ScopeProxySite:
		return 0
	case ScopeIdentitySite, ScopeProxy, ScopeAccount:
		return 1
	case ScopeIdentity:
		return 2
	}
	return -1
}

// CooldownDuration computes min(base·multiplier^min(streak−1, maxExponent), maxDur)·jitter
// (spec §6.6). streak < 1 counts as 1, a negative maxExponent as 0, a
// multiplier below 1 (or NaN) as 1, maxDur <= 0 as no cap; jitter is clamped to
// [0.8, 1.2] (NaN → 1). The result is rounded to whole milliseconds, is at
// least 1ms when base > 0, saturates instead of overflowing, and is 0 when
// base <= 0.
func CooldownDuration(base, maxDur time.Duration, multiplier float64, maxExponent, streak int, jitter float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if streak < 1 {
		streak = 1
	}
	if maxExponent < 0 {
		maxExponent = 0
	}
	if !(multiplier >= 1) {
		multiplier = 1
	}
	switch {
	case math.IsNaN(jitter):
		jitter = 1
	case jitter < 0.8:
		jitter = 0.8
	case jitter > 1.2:
		jitter = 1.2
	}
	exponent := streak - 1
	if exponent > maxExponent {
		exponent = maxExponent
	}
	d := float64(base) * math.Pow(multiplier, float64(exponent))
	if maxDur > 0 && d > float64(maxDur) {
		d = float64(maxDur)
	}
	d *= jitter
	const maxFloat = float64(math.MaxInt64)
	if d >= maxFloat || math.IsInf(d, 1) {
		return time.Duration(math.MaxInt64)
	}
	ms := math.Round(d / float64(time.Millisecond))
	if ms < 1 {
		ms = 1
	}
	if ms*float64(time.Millisecond) >= maxFloat {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(ms) * time.Millisecond
}
