package breaker

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

var (
	//go:embed lua/breaker_eval.lua
	evalSource string
	//go:embed lua/breaker_set.lua
	setSource string
	//go:embed lua/revert.lua
	revertSource string
	//go:embed lua/site_paused.lua
	sitePausedSource string

	evalScript       = redis.NewScript("breaker_eval", evalSource)
	setScript        = redis.NewScript("breaker_set", setSource)
	revertScript     = redis.NewScript("revert", revertSource)
	sitePausedScript = redis.NewScript("site_paused", sitePausedSource)
)

// Breaker states (spec §5.5).
const (
	StateClosed   = "closed"
	StateOpen     = "open"
	StateHalfOpen = "half_open"
)

// Transition triggers stored in breaker_events.trigger.
const (
	TriggerAuto       = "auto"
	TriggerManual     = "manual"
	TriggerProbe      = "probe"
	TriggerSiteSwitch = "site_switch"
)

// Site switch pseudo-states recorded in breaker_events rows with trigger
// site_switch.
const (
	SiteRunning = "running"
	SitePaused  = "paused"
)

// Script modes of breaker_eval.lua.
const (
	modeEval = "eval"
	modeRead = "read"
)

// probeWindow is the half-open probe issuance window (spec §6.1).
const probeWindow = 10 * time.Second

// maxRevertEntries bounds the recent-cooldown entries one revert call processes
// per set, keeping the script short.
const maxRevertEntries = 10000

// Hash holds the fields of the breaker hash "P:T:brk:<eg>" (spec §5.5).
type Hash struct {
	State          string
	OpenUntilMs    int64
	OpenCount      int64
	LastOpenedMs   int64
	LastClosedMs   int64
	Manual         bool
	HalfOpenMs     int64
	ProbesIssued   int64
	ProbeSamples   int64
	ProbeSuccesses int64
	Reason         string
	Version        int64
}

// Window holds the sliding window sums of an endpoint group.
type Window struct {
	Total             int64
	Success           int64
	Risk              int64
	CaptchaIdentities int64
}

// RiskRatio returns Risk / Total (0 when Total is 0).
func (w Window) RiskRatio() float64 {
	if w.Total <= 0 {
		return 0
	}
	return float64(w.Risk) / float64(w.Total)
}

// SuccessRatio returns Success / Total (0 when Total is 0).
func (w Window) SuccessRatio() float64 {
	if w.Total <= 0 {
		return 0
	}
	return float64(w.Success) / float64(w.Total)
}

// evalResult is the decoded reply of breaker_eval.lua.
type evalResult struct {
	From    string
	To      string
	Trigger string
	Window  Window
	// Hash is the breaker state after the evaluation.
	Hash Hash
	// ProbeSamples and ProbeSuccesses are the probe statistics the evaluation
	// was based on (a transition resets them in Hash).
	ProbeSamples   int64
	ProbeSuccesses int64
	// LazyHalfOpen reports a half-open transition performed earlier by
	// acquire.lua and not recorded yet; it happened at LazyAtMs and Hash is
	// the half-open state (no other transition is evaluated in that call).
	LazyHalfOpen bool
	LazyAtMs     int64
}

// Transitioned reports whether the evaluation changed the breaker state.
func (r evalResult) Transitioned() bool { return r.To != "" }

// setResult is the decoded reply of breaker_set.lua.
type setResult struct {
	From    string
	To      string
	Changed bool
	Hash    Hash
	// LazyHalfOpen reports an unrecorded half-open transition performed by
	// acquire.lua at LazyAtMs, with the reason and manual flag it carried.
	LazyHalfOpen bool
	LazyAtMs     int64
	LazyReason   string
	LazyManual   bool
}

// evalParams are the policy-derived arguments of breaker_eval.lua.
type evalParams struct {
	BucketMs             int64
	Buckets              int
	MinRequests          int
	RiskRatioGte         float64
	CaptchaGte           int
	SuccessRatioLte      float64
	OpenMs               int64
	MaxOpenMs            int64
	ResetAfterMs         int64
	CloseMinSamples      int
	CloseSuccessRatioGte float64
	Enabled              bool
	WindowMs             int64
	Revert               policy.RevertMode
}

// paramsFor derives script arguments from a breaker policy, falling back to
// the built-in defaults for missing or unusable values.
func paramsFor(spec *policy.BreakerSpec) evalParams {
	def := defaultSpec()
	if spec == nil {
		spec = def
	}
	p := evalParams{
		Buckets:              spec.Buckets,
		MinRequests:          spec.MinRequests,
		RiskRatioGte:         spec.Trip.RiskRatioGte,
		CaptchaGte:           spec.Trip.DistinctCaptchaIdentitiesGte,
		SuccessRatioLte:      spec.Trip.SuccessRatioLte,
		OpenMs:               positiveMs(spec.OpenDuration.Milliseconds(), def.OpenDuration.Milliseconds()),
		MaxOpenMs:            positiveMs(spec.MaxOpenDuration.Milliseconds(), def.MaxOpenDuration.Milliseconds()),
		ResetAfterMs:         positiveMs(spec.ResetOpenCountAfter.Milliseconds(), def.ResetOpenCountAfter.Milliseconds()),
		CloseMinSamples:      spec.HalfOpen.CloseMinSamples,
		CloseSuccessRatioGte: spec.HalfOpen.CloseSuccessRatioGte,
		Enabled:              spec.Enabled,
		WindowMs:             positiveMs(spec.Window.Milliseconds(), def.Window.Milliseconds()),
		Revert:               spec.RevertRecentCooldowns,
	}
	if p.Buckets <= 0 || p.Buckets > 60 {
		p.Buckets = def.Buckets
	}
	p.BucketMs = p.WindowMs / int64(p.Buckets)
	if p.BucketMs <= 0 {
		p.BucketMs = 1
	}
	if p.MinRequests <= 0 {
		p.MinRequests = def.MinRequests
	}
	if p.CloseMinSamples <= 0 {
		p.CloseMinSamples = def.HalfOpen.CloseMinSamples
	}
	if p.CloseSuccessRatioGte <= 0 {
		p.CloseSuccessRatioGte = def.HalfOpen.CloseSuccessRatioGte
	}
	if p.MaxOpenMs < p.OpenMs {
		p.MaxOpenMs = p.OpenMs
	}
	if !policy.ValidRevertMode(p.Revert) {
		p.Revert = policy.RevertEndpoint
	}
	return p
}

// builtinBreaker is the built-in default breaker policy, computed once.
var builtinBreaker = sync.OnceValue(func() *policy.BreakerSpec {
	if spec, ok := policy.Default(policy.KindBreaker).(*policy.BreakerSpec); ok && spec != nil {
		return spec
	}
	// policy.Default always returns a breaker spec for KindBreaker; this only
	// guards against a future refactoring mistake.
	spec := &policy.BreakerSpec{
		Enabled: true,
		Trip: policy.TripSpec{
			RiskRatioGte:                 policy.DefaultTripRiskRatioGte,
			DistinctCaptchaIdentitiesGte: policy.DefaultTripDistinctCaptchaIdentities,
			SuccessRatioLte:              policy.DefaultTripSuccessRatioLte,
		},
	}
	spec.ApplyDefaults()
	return spec
})

func defaultSpec() *policy.BreakerSpec { return builtinBreaker() }

func positiveMs(v, def int64) int64 {
	if v > 0 {
		return v
	}
	return def
}

// evalExec builds a breaker_eval.lua call.
func evalExec(keys redis.Keys, siteKey, egKey int64, now time.Time, p evalParams, mode string) rueidis.LuaExec {
	enabled := "0"
	if p.Enabled {
		enabled = "1"
	}
	return rueidis.LuaExec{
		Keys: []string{keys.SiteMeta(siteKey)},
		Args: []string{
			strconv.FormatInt(egKey, 10),
			strconv.FormatInt(now.UnixMilli(), 10),
			strconv.FormatInt(p.BucketMs, 10),
			strconv.Itoa(p.Buckets),
			strconv.Itoa(p.MinRequests),
			formatFloat(p.RiskRatioGte),
			strconv.Itoa(p.CaptchaGte),
			formatFloat(p.SuccessRatioLte),
			strconv.FormatInt(p.OpenMs, 10),
			strconv.FormatInt(p.MaxOpenMs, 10),
			strconv.FormatInt(p.ResetAfterMs, 10),
			strconv.Itoa(p.CloseMinSamples),
			formatFloat(p.CloseSuccessRatioGte),
			mode,
			enabled,
		},
	}
}

// setExec builds a breaker_set.lua call. duration <= 0 means indefinite.
func setExec(keys redis.Keys, siteKey, egKey int64, now time.Time, op string, duration time.Duration, reason string) rueidis.LuaExec {
	ms := int64(0)
	if duration > 0 {
		ms = duration.Milliseconds()
	}
	return rueidis.LuaExec{
		Keys: []string{keys.SiteMeta(siteKey)},
		Args: []string{
			strconv.FormatInt(egKey, 10),
			strconv.FormatInt(now.UnixMilli(), 10),
			op,
			strconv.FormatInt(ms, 10),
			reason,
		},
	}
}

// revertExec builds a revert.lua call.
func revertExec(keys redis.Keys, siteKey, egKey int64, now time.Time, windowMs int64, mode policy.RevertMode, groupKeys []int64) rueidis.LuaExec {
	parts := make([]string, 0, len(groupKeys))
	for _, k := range groupKeys {
		parts = append(parts, strconv.FormatInt(k, 10))
	}
	return rueidis.LuaExec{
		Keys: []string{keys.SiteMeta(siteKey)},
		Args: []string{
			strconv.FormatInt(egKey, 10),
			strconv.FormatInt(now.UnixMilli(), 10),
			strconv.FormatInt(windowMs, 10),
			string(mode),
			strconv.Itoa(maxRevertEntries),
			strings.Join(parts, ","),
		},
	}
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// parseEvalResult decodes the reply of breaker_eval.lua.
func parseEvalResult(res rueidis.RedisResult) (evalResult, error) {
	vals, err := stringArray(res, 23)
	if err != nil {
		return evalResult{}, fmt.Errorf("breaker_eval reply: %w", err)
	}
	n := newNumParser(vals)
	out := evalResult{
		From:    vals[0],
		To:      vals[1],
		Trigger: vals[2],
		Window: Window{
			Total:             n.at(3),
			Success:           n.at(4),
			Risk:              n.at(5),
			CaptchaIdentities: n.at(6),
		},
		Hash:           parseHash(n, 7),
		ProbeSamples:   n.at(19),
		ProbeSuccesses: n.at(20),
		LazyHalfOpen:   n.at(21) == 1,
		LazyAtMs:       n.at(22),
	}
	if n.err != nil {
		return evalResult{}, fmt.Errorf("breaker_eval reply: %w", n.err)
	}
	return out, nil
}

// parseSetResult decodes the reply of breaker_set.lua.
func parseSetResult(res rueidis.RedisResult) (setResult, error) {
	vals, err := stringArray(res, 19)
	if err != nil {
		return setResult{}, fmt.Errorf("breaker_set reply: %w", err)
	}
	n := newNumParser(vals)
	out := setResult{
		From:         vals[0],
		To:           vals[1],
		Changed:      vals[2] == "1",
		Hash:         parseHash(n, 3),
		LazyHalfOpen: n.at(15) == 1,
		LazyAtMs:     n.at(16),
		LazyReason:   vals[17],
		LazyManual:   n.at(18) == 1,
	}
	if n.err != nil {
		return setResult{}, fmt.Errorf("breaker_set reply: %w", n.err)
	}
	return out, nil
}

// parseHash decodes the breaker fields {st, ou, oc, lo, lc, man, hw, hc, ps,
// pk, rsn, v} starting at offset of the parser's values.
func parseHash(n *numParser, offset int) Hash {
	str := func(i int) string {
		if i < len(n.vals) {
			return n.vals[i]
		}
		return ""
	}
	return Hash{
		State:          normalizeState(str(offset)),
		OpenUntilMs:    n.at(offset + 1),
		OpenCount:      n.at(offset + 2),
		LastOpenedMs:   n.at(offset + 3),
		LastClosedMs:   n.at(offset + 4),
		Manual:         n.at(offset+5) == 1,
		HalfOpenMs:     n.at(offset + 6),
		ProbesIssued:   n.at(offset + 7),
		ProbeSamples:   n.at(offset + 8),
		ProbeSuccesses: n.at(offset + 9),
		Reason:         str(offset + 10),
		Version:        n.at(offset + 11),
	}
}

func normalizeState(s string) string {
	switch s {
	case StateOpen, StateHalfOpen:
		return s
	default:
		return StateClosed
	}
}

// parseRevertResult decodes {endpoint reverted, site reverted}.
func parseRevertResult(res rueidis.RedisResult) (endpoint, site int64, err error) {
	arr, err := res.ToArray()
	if err != nil {
		return 0, 0, fmt.Errorf("revert reply: %w", err)
	}
	if len(arr) != 2 {
		return 0, 0, fmt.Errorf("revert reply: got %d values, want 2", len(arr))
	}
	if endpoint, err = arr[0].AsInt64(); err != nil {
		return 0, 0, fmt.Errorf("revert reply: %w", err)
	}
	if site, err = arr[1].AsInt64(); err != nil {
		return 0, 0, fmt.Errorf("revert reply: %w", err)
	}
	return endpoint, site, nil
}

func stringArray(res rueidis.RedisResult, want int) ([]string, error) {
	arr, err := res.ToArray()
	if err != nil {
		return nil, err
	}
	if len(arr) != want {
		return nil, fmt.Errorf("got %d values, want %d", len(arr), want)
	}
	out := make([]string, len(arr))
	for i, m := range arr {
		s, err := m.ToString()
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", i, err)
		}
		out[i] = s
	}
	return out, nil
}

// numParser parses integer strings and remembers the first failure.
type numParser struct {
	vals []string
	err  error
}

func newNumParser(vals []string) *numParser { return &numParser{vals: vals} }

func (p *numParser) at(i int) int64 {
	if i < 0 || i >= len(p.vals) {
		if p.err == nil {
			p.err = fmt.Errorf("value %d missing", i)
		}
		return 0
	}
	s := p.vals[i]
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("value %d: %w", i, err)
	}
	return v
}

// parseHashFields decodes an HMGET/HGETALL style map of the breaker hash.
func parseHashFields(m map[string]string) Hash {
	num := func(k string) int64 {
		v, err := strconv.ParseInt(m[k], 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	return Hash{
		State:          normalizeState(m["st"]),
		OpenUntilMs:    num("ou"),
		OpenCount:      num("oc"),
		LastOpenedMs:   num("lo"),
		LastClosedMs:   num("lc"),
		Manual:         num("man") == 1,
		HalfOpenMs:     num("hw"),
		ProbesIssued:   num("hc"),
		ProbeSamples:   num("ps"),
		ProbeSuccesses: num("pk"),
		Reason:         m["rsn"],
		Version:        num("v"),
	}
}
