package worker

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

//go:embed lua/observe.lua
var observeLua string

// observe.lua modes and statuses.
const (
	observeModeFull = "full"
	observeModeCkpt = "ckpt"
	observeOK       = "OK"
	observeDup      = "DUP"

	// observeReplyLen is the length of a full-mode reply.
	observeReplyLen = 18
)

// Counter subject kinds understood by observe.lua.
const (
	counterSubjectIdentity = "i"
	counterSubjectAccount  = "a"
	counterSubjectProxy    = "p"
)

// observeParams are the inputs of a full observe.lua call.
type observeParams struct {
	shard       int
	streamID    string
	now         time.Time
	leaseID     string
	group       *catalog.EndpointGroup
	action      *policy.CompiledAction
	identityKey int64
	proxyKey    int64 // 0 = no proxy
	outcome     string
	blame       policy.Blame
	late        bool
	counters    []policy.CounterRequest
	banWindows  []time.Duration
}

// observeResult is the decoded reply of a full observe.lua call.
type observeResult struct {
	dup                   bool
	blame                 policy.Blame
	suppressed            bool
	probe                 bool
	identityState         string
	accountKey            int64
	identityType          string
	endpointStreak        int
	globalStreak          int
	proxyStreak           int
	endpointScore         float64
	endpointSamples       int
	globalScore           float64
	globalSamples         int
	endpointCooldownUntil int64
	counts                []int64
	banCounts             []int64
}

// observe runs observe.lua in full mode for one report.
func (w *Worker) observe(ctx context.Context, siteKey int64, p observeParams) (observeResult, error) {
	args := observeArgs(p)
	res := w.observeScript.Exec(ctx, w.rdb, []string{w.keys.SiteMeta(siteKey)}, args)
	return parseObserveReply(res, len(p.counters), len(p.banWindows))
}

// checkpoint runs observe.lua in checkpoint-only mode; it reports whether the
// stream entry was already applied.
func (w *Worker) checkpoint(ctx context.Context, siteKey int64, shard int, streamID string) (dup bool, err error) {
	res := w.observeScript.Exec(ctx, w.rdb, []string{w.keys.SiteMeta(siteKey)},
		[]string{observeModeCkpt, strconv.Itoa(shard), streamID})
	arr, err := res.ToArray()
	if err != nil {
		return false, fmt.Errorf("observe checkpoint: %w", err)
	}
	if len(arr) == 0 {
		return false, errors.New("observe checkpoint: empty reply")
	}
	status, err := arr[0].ToString()
	if err != nil {
		return false, fmt.Errorf("observe checkpoint status: %w", err)
	}
	return status == observeDup, nil
}

// observeArgs builds the ARGV of a full observe.lua call (see the script header).
func observeArgs(p observeParams) []string {
	var quotas []policy.QuotaSpec
	if r := p.group.Rotation; r != nil {
		quotas = r.Rotation.Quota
	}
	args := make([]string, 0, 23+len(quotas)+2*len(p.counters)+len(p.banWindows))
	args = appendObserveHeader(args, p)
	args = append(args, strconv.Itoa(len(quotas)))
	for _, q := range quotas {
		args = append(args, strconv.FormatInt(q.Window.Milliseconds(), 10))
	}
	args = append(args, strconv.Itoa(len(p.counters)))
	for _, c := range p.counters {
		args = append(args, counterSubject(c.Subject), strconv.FormatInt(c.Window.Milliseconds(), 10))
	}
	args = append(args, strconv.Itoa(len(p.banWindows)))
	for _, bw := range p.banWindows {
		args = append(args, strconv.FormatInt(bw.Milliseconds(), 10))
	}
	return args
}

// appendObserveHeader appends the fixed-position arguments (mode through the
// cross attribution policy). The two packed entries and the window ttl are the
// constants of the compiled policy; only the window suffix moves per report,
// and the bucket index in it is computed here so that the script needs neither
// the bucket length nor a number-to-string conversion inside a key (spec §5.7).
func appendObserveHeader(args []string, p observeParams) []string {
	act := p.action
	obsIdentity, obsProxy := "", ""
	if v, ok := act.Observation(p.outcome, policy.BlameIdentity); ok {
		obsIdentity = formatFloat(v)
	}
	if v, ok := act.ProxyObservation(p.outcome, policy.BlameProxy); ok {
		obsProxy = formatFloat(v)
	}
	windowMs, bucketMs := breakerWindow(p.group.Breaker)
	winSuffix, winTTL := "", "0"
	if windowMs > 0 && bucketMs > 0 {
		nowMs := p.now.UnixMilli()
		winSuffix = strconv.FormatInt(p.group.Key, 10) + ":" + strconv.FormatInt(nowMs/bucketMs, 10)
		winTTL = strconv.FormatInt(2*windowMs, 10)
	}
	proxy := ""
	if p.proxyKey > 0 {
		proxy = strconv.FormatInt(p.proxyKey, 10)
	}
	return append(args,
		observeModeFull, strconv.Itoa(p.shard), p.streamID,
		strconv.FormatInt(p.now.UnixMilli(), 10), p.leaseID,
		strconv.FormatInt(p.group.Key, 10), strconv.FormatInt(p.identityKey, 10), proxy,
		p.outcome, string(p.blame), boolArg(p.late), obsIdentity, obsProxy,
		boolArg(policy.IsFailureOutcome(p.outcome)), boolArg(policy.IsRiskOutcome(p.outcome)),
		healthPolicyArg(act), winSuffix, winTTL, crossAttributionArg(act),
	)
}

// healthPolicyArg packs the health constants of a compiled action into ARGV[16]
// ("alpha,baseline,tau,failure_reset_after"). Four entries on the wire cost
// Valkey about 0.12 us each just to receive; one entry and one string.match in
// the script is cheaper, and the string depends only on the policy.
func healthPolicyArg(act *policy.CompiledAction) string {
	return formatFloat(act.Health.Alpha) + "," + formatFloat(act.Health.Baseline) + "," +
		strconv.FormatInt(act.Health.Tau.Milliseconds(), 10) + "," +
		strconv.FormatInt(act.FailureResetAfter().Milliseconds(), 10)
}

// crossAttributionArg packs the cross attribution policy into ARGV[19]
// ("enabled,window,proxy_distinct_identities,identity_distinct_proxies"). The
// script decodes it only for a risk outcome that has both a proxy and an
// identity and is not late, so most reports never parse it at all.
func crossAttributionArg(act *policy.CompiledAction) string {
	xa := act.CrossAttribution
	return boolArg(xa.Enabled) + "," + strconv.FormatInt(xa.Window.Milliseconds(), 10) + "," +
		strconv.Itoa(xa.ProxyDistinctIdentities) + "," + strconv.Itoa(xa.IdentityDistinctProxies)
}

// maxBreakerBuckets mirrors the bucket limit of breaker_eval.lua.
const maxBreakerBuckets = 60

// breakerWindow returns the breaker window and bucket length in milliseconds
// with the same fallbacks as the breaker evaluation (internal/breaker
// paramsFor): a missing policy, a non-positive window or a bucket count outside
// 1..60 use the built-in defaults (60s / 12), and the bucket is at least 1 ms.
// Window buckets written here must line up with the buckets breaker_eval.lua
// sums.
func breakerWindow(spec *policy.BreakerSpec) (windowMs, bucketMs int64) {
	windowMs, buckets := policy.DefaultBreakerWindow.Milliseconds(), int64(policy.DefaultBreakerBuckets)
	if spec != nil {
		if w := spec.Window.Milliseconds(); w > 0 {
			windowMs = w
		}
		if spec.Buckets > 0 && spec.Buckets <= maxBreakerBuckets {
			buckets = int64(spec.Buckets)
		}
	}
	bucketMs = max(windowMs/buckets, 1)
	return windowMs, bucketMs
}

// parseObserveReply decodes a full-mode reply.
func parseObserveReply(res rueidis.RedisResult, counters, banWindows int) (observeResult, error) {
	arr, err := res.ToArray()
	if err != nil {
		return observeResult{}, fmt.Errorf("observe: %w", err)
	}
	if len(arr) == 0 {
		return observeResult{}, errors.New("observe: empty reply")
	}
	p := replyParser{arr: arr}
	status := p.str(0)
	if p.err != nil {
		return observeResult{}, p.err
	}
	if status == observeDup {
		return observeResult{dup: true}, nil
	}
	if status != observeOK || len(arr) != observeReplyLen {
		return observeResult{}, fmt.Errorf("observe: unexpected reply %q with %d elements", status, len(arr))
	}
	out := observeResult{
		blame:                 policy.Blame(p.str(1)),
		suppressed:            p.int(2) == 1,
		probe:                 p.int(3) == 1,
		identityState:         p.str(4),
		accountKey:            p.optInt(5),
		identityType:          p.str(6),
		endpointStreak:        int(p.int(7)),
		globalStreak:          int(p.int(8)),
		proxyStreak:           int(p.int(9)),
		endpointScore:         p.float(10),
		endpointSamples:       int(p.int(11)),
		globalScore:           p.float(12),
		globalSamples:         int(p.int(13)),
		endpointCooldownUntil: p.int(14),
		// Element 15 (breaker state) is informational and not needed here.
		counts:    p.ints(16, counters),
		banCounts: p.ints(17, banWindows),
	}
	if p.err != nil {
		return observeResult{}, p.err
	}
	return out, nil
}

// replyParser decodes reply elements, keeping the first error.
type replyParser struct {
	arr []rueidis.RedisMessage
	err error
}

func (r *replyParser) fail(i int, err error) {
	if r.err == nil {
		r.err = fmt.Errorf("observe: reply element %d: %w", i, err)
	}
}

func (r *replyParser) str(i int) string {
	s, err := r.arr[i].ToString()
	if err != nil {
		r.fail(i, err)
	}
	return s
}

func (r *replyParser) int(i int) int64 {
	n, err := r.arr[i].AsInt64()
	if err != nil {
		r.fail(i, err)
	}
	return n
}

func (r *replyParser) optInt(i int) int64 {
	s := r.str(i)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		r.fail(i, err)
	}
	return n
}

func (r *replyParser) float(i int) float64 {
	f, err := strconv.ParseFloat(r.str(i), 64)
	if err != nil {
		r.fail(i, err)
	}
	return f
}

func (r *replyParser) ints(i, want int) []int64 {
	vals, err := r.arr[i].ToArray()
	if err != nil {
		r.fail(i, err)
		return nil
	}
	if len(vals) != want {
		r.fail(i, fmt.Errorf("got %d values, want %d", len(vals), want))
		return nil
	}
	out := make([]int64, len(vals))
	for k := range vals {
		n, err := vals[k].AsInt64()
		if err != nil {
			r.fail(i, err)
			return nil
		}
		out[k] = n
	}
	return out
}

func counterSubject(s policy.SubjectKind) string {
	switch s {
	case policy.SubjectAccount:
		return counterSubjectAccount
	case policy.SubjectProxy:
		return counterSubjectProxy
	default:
		return counterSubjectIdentity
	}
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
