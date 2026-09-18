package analytics

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
)

// Redis field names of the hot-state schema (spec §5) read by this package.
const (
	fieldState         = "st"  // id, acc, brk: state
	fieldBanUntil      = "bu"  // id, acc: ban until ms (-1 permanent)
	fieldSiteCooldown  = "scd" // id: identity x site cooldown until ms
	fieldCooldown      = "cd"  // acc: account cooldown until ms
	breakerOpen        = "open"
	breakerHalfOpen    = "half_open"
	stateBanned        = "banned"
	permanentBanMillis = -1
)

// healthEntry is the decoded packed identity x endpoint group state
// "score|sts|samples|nfail|lastfail|cd|ru|lu" (spec §5.1).
type healthEntry struct {
	score    float64
	scoreTS  int64
	cooldown int64
}

// parseHealth decodes the fields of a packed health value used by the
// dashboard. Missing or malformed fields take the spec defaults (score =
// baseline, sts = now, cooldown 0).
func parseHealth(packed string, baseline float64, nowMs int64) healthEntry {
	parts := strings.SplitN(packed, "|", 7)
	field := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	return healthEntry{
		score:    parseFloat(field(0), baseline),
		scoreTS:  parseInt(field(1), nowMs),
		cooldown: parseInt(field(5), 0),
	}
}

// decayScore applies the lazy regression towards the baseline (spec §6.4).
func decayScore(score float64, scoreTS, nowMs int64, baseline float64, tau time.Duration) float64 {
	tauMs := float64(tau.Milliseconds())
	dt := float64(nowMs - scoreTS)
	if tauMs <= 0 || dt <= 0 {
		return score
	}
	return baseline + (score-baseline)*math.Exp(-dt/tauMs)
}

// healthOf returns the health parameters of an endpoint group's action
// policy, falling back to the built-in defaults.
func healthOf(g *catalog.EndpointGroup) policy.HealthSpec {
	if g != nil && g.Action != nil {
		return g.Action.Health
	}
	if spec, ok := policy.Default(policy.KindAction).(*policy.ActionSpec); ok && spec != nil {
		return spec.Health
	}
	return policy.HealthSpec{Baseline: policy.DefaultHealthBaseline}
}

// parseInt parses a base-10 integer field; empty or malformed values yield
// def. Values with a fractional part are truncated.
func parseInt(v string, def int64) int64 {
	if v == "" {
		return def
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f > math.MaxInt64 || f < math.MinInt64 {
		return def
	}
	return int64(f)
}

// parseFloat parses a float field; empty or malformed values yield def.
func parseFloat(v string, def float64) float64 {
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return f
}

// optionalString returns the string value of a reply element and whether it
// was present (not nil).
func optionalString(m rueidis.RedisMessage) (string, bool, error) {
	if m.IsNil() {
		return "", false, nil
	}
	v, err := m.ToString()
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// stringsOf converts an array reply of optional strings; nil elements become "".
func stringsOf(res rueidis.RedisResult) ([]string, error) {
	arr, err := res.ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(arr))
	for i, m := range arr {
		v, _, err := optionalString(m)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// itoa formats an int64 as base-10.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// ratio returns part/total, or 0 when total is not positive.
func ratio(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}
