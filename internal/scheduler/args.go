package scheduler

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
)

// Script status values and reply layout.
const (
	statusOK               = "OK"
	statusExhausted        = "EXHAUSTED"
	statusBreakerOpen      = "BREAKER_OPEN"
	statusNoProxy          = "NO_PROXY"
	statusReleased         = "RELEASED"
	statusEnded            = "ENDED"
	statusUnknown          = "UNKNOWN"
	statusExpired          = "EXPIRED"
	statusLifetimeExceeded = "LIFETIME_EXCEEDED"

	acquireLeaseFields   = 14
	reapLeaseFields      = 10
	randomsPerLease      = 4
	randomsExtra         = 16
	acquireFixedArgs     = 30
	acquireListCountArgs = 5
	rebindDayLayout      = "20060102"
)

// acquireCall carries the per-call inputs of acquire.lua.
type acquireCall struct {
	now      time.Time
	count    int
	session  string // normalized sticky session key or ""
	node     string
	tokenID  string
	prefixes []string
	randoms  []float64
}

// strategyCode maps a rotation strategy to its acquire.lua code.
func strategyCode(s string) string {
	switch s {
	case policy.StrategyLeastRecentlyUsed:
		return "l"
	case policy.StrategyRoundRobin:
		return "r"
	case policy.StrategyBestHealth:
		return "b"
	default:
		return "w"
	}
}

// proxyModeCode maps a proxy mode to its acquire.lua code.
func proxyModeCode(m string) string {
	switch m {
	case policy.ProxyModePool:
		return "p"
	case policy.ProxyModeBindIdentity:
		return "b"
	case policy.ProxyModeRegionMatch:
		return "r"
	default:
		return "n"
	}
}

func anchorCode(a string) string {
	if a == policy.ReuseAnchorAcquired {
		return "a"
	}
	return "r"
}

func scopeCode(s string) string {
	if s == policy.ReuseScopeSite {
		return "s"
	}
	return "e"
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ppm renders a fraction as parts per million, rounded like the "%.6f" the
// script used to parse. Lua 5.1 parses a numeral with a fractional part, and
// any numeral of ten digits or more, about eight times slower than a short
// integer (0.52 us against 0.07 us on Valkey 8.1.10), and acquire.lua converts
// several of these per call, so fractions travel as integers and the script
// divides (spec §5.7, §6.1).
func ppm(f float64) string { return itoa(int64(math.Round(f * 1e6))) }

// rotationOf returns the group's rotation policy or the built-in default.
func rotationOf(g *catalog.EndpointGroup) *policy.RotationSpec {
	if g.Rotation != nil {
		return g.Rotation
	}
	return policy.Default(policy.KindRotation).(*policy.RotationSpec)
}

// healthOf returns the health baseline and decay constant of the group.
func healthOf(g *catalog.EndpointGroup) (baseline float64, tau time.Duration) {
	if g.Action != nil {
		return g.Action.Health.Baseline, g.Action.Health.Tau.Std()
	}
	return policy.DefaultHealthBaseline, policy.DefaultHealthTau
}

// probeLimitOf returns the half-open probe leases allowed per 10 s window.
func probeLimitOf(g *catalog.EndpointGroup) int {
	if g.Breaker != nil && g.Breaker.HalfOpen.ProbeLeasesPer10s > 0 {
		return g.Breaker.HalfOpen.ProbeLeasesPer10s
	}
	return policy.DefaultProbeLeasesPer10s
}

// acquireArgs builds the ARGV of acquire.lua (layout documented in the script).
func (s *Service) acquireArgs(nsID string, g *catalog.EndpointGroup, rot *policy.RotationSpec, call acquireCall) []string {
	r := &rot.Rotation
	px := &rot.Proxy
	baseline, tau := healthOf(g)
	stickyTTL := int64(0)
	if r.Sticky.Enabled && call.session != "" {
		stickyTTL = r.Sticky.TTL.Milliseconds()
	}
	now := call.now.UnixMilli()
	size := acquireFixedArgs + acquireListCountArgs + len(px.Kinds) + len(px.Providers) + len(px.Regions) +
		len(px.Tags) + 2*len(r.Quota) + len(call.prefixes) + len(call.randoms)
	args := make([]string, 0, size)
	args = append(args,
		itoa(now),
		strconv.Itoa(call.count),
		itoa(g.Key),
		strategyCode(r.Strategy),
		strconv.Itoa(r.CandidateSample),
		itoa(r.LeaseTTL.Milliseconds()),
		itoa(r.MaxLeaseLifetime.Milliseconds()),
		strconv.Itoa(r.MaxConcurrentLeases),
		itoa(r.ReuseInterval.Milliseconds()),
		anchorCode(r.ReuseAnchor),
		scopeCode(r.ReuseScope),
		ppm(r.Probe.WeightFactor),
		strconv.Itoa(r.Probe.MaxLeases),
		itoa(r.Warmup.Duration.Milliseconds()),
		ppm(r.Warmup.QuotaFactor),
		ftoa(baseline),
		itoa(tau.Milliseconds()),
		strconv.Itoa(probeLimitOf(g)),
		call.session,
		itoa(stickyTTL),
		itoa(s.cfg.LateReportWindow.Milliseconds()),
		strconv.Itoa(s.cfg.ReportShards),
		call.node,
		call.tokenID,
		nsID,
		proxyModeCode(px.Mode),
		boolArg(px.RegionMatch),
		itoa(px.RebindTolerance.Milliseconds()),
		strconv.Itoa(px.MaxRebindsPerDay),
		call.now.UTC().Format(rebindDayLayout),
	)
	args = appendList(args, px.Kinds)
	args = appendList(args, px.Providers)
	args = appendList(args, px.Regions)
	args = appendList(args, px.Tags)
	args = append(args, strconv.Itoa(len(r.Quota)))
	for _, q := range r.Quota {
		args = append(args, itoa(q.Limit), itoa(q.Window.Milliseconds()))
	}
	args = append(args, call.prefixes...)
	for _, f := range call.randoms {
		args = append(args, ppm(f))
	}
	return args
}

func appendList(args []string, list []string) []string {
	args = append(args, strconv.Itoa(len(list)))
	return append(args, list...)
}

// layoutCache memoizes groupLayout per site. A site with 50 endpoint groups
// has a 101-entry layout, and release.lua is called once per lease, so
// rebuilding it per call formats a hundred strings on the hot path. Catalog
// snapshots are immutable and replaced wholesale on reload, so the snapshot
// pointer is the cache key and a reload simply replaces the entry.
type layoutCache struct {
	mu sync.Mutex
	m  map[int64]layoutEntry
}

type layoutEntry struct {
	site *catalog.Site
	args []string
}

// layout returns the cached layout of site, building it on the first call and
// after every catalog reload. The returned slice is shared and must not be
// modified by callers; they append it to their own ARGV.
func (c *layoutCache) layout(site *catalog.Site) []string {
	if site == nil {
		return groupLayout(nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[site.Key]; ok && e.site == site {
		return e.args
	}
	args := groupLayout(site)
	if c.m == nil {
		c.m = make(map[int64]layoutEntry)
	}
	c.m[site.Key] = layoutEntry{site: site, args: args}
	return args
}

// groupLayout builds the endpoint-group layout consumed by ls_groups in
// lease_end.lua: the number of clients, then one entry per client holding that
// client's "hkey,baseline" pairs separated by commas.
//
// One entry per client rather than two per group: a site with 50 endpoint
// groups would otherwise put 101 bulk strings on the wire for every release,
// and Valkey pays about 0.1 us per ARGV entry just to receive them - 10 us on a
// 50 us script. lease_end.lua parses the entries only when a lease end really
// has to walk the other groups.
func groupLayout(site *catalog.Site) []string {
	if site == nil {
		return []string{"0"}
	}
	byClient := make(map[string][]*catalog.EndpointGroup, len(site.Clients))
	for _, g := range site.GroupsByID {
		byClient[g.Client] = append(byClient[g.Client], g)
	}
	out := make([]string, 0, 1+len(byClient))
	out = append(out, strconv.Itoa(len(byClient)))
	var b strings.Builder
	for _, groups := range byClient {
		b.Reset()
		for i, g := range groups {
			if i > 0 {
				b.WriteByte(',')
			}
			baseline, _ := healthOf(g)
			b.WriteString(itoa(g.Key))
			b.WriteByte(',')
			b.WriteString(ftoa(baseline))
		}
		out = append(out, b.String())
	}
	return out
}

// scriptLease is one lease returned by acquire.lua.
type scriptLease struct {
	ID             string
	IdentityID     string
	TypeName       string
	PayloadVersion int
	TypeVersion    int
	ExpiresMs      int64
	Probe          bool
	Sticky         bool
	Bound          bool // a binding was created (first binding or rebind)
	Rebound        bool
	ProxyKey       int64
	ProxyID        string
	State          string
	IdentityKey    int64
	RebindsToday   int
}

// acquireOutcome is the decoded result of acquire.lua.
type acquireOutcome struct {
	Status       string
	Transition   bool
	RetryAfterMs int64
	Leases       []scriptLease
}

// errEmptyReply is returned for script replies without a status element.
var errEmptyReply = errors.New("empty script reply")

// replyStrings decodes a Lua array reply of strings.
func replyStrings(res rueidis.RedisResult) ([]string, error) {
	msgs, err := res.ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(msgs))
	for i := range msgs {
		v, err := msgs[i].ToString()
		if err != nil {
			n, ierr := msgs[i].AsInt64()
			if ierr != nil {
				return nil, fmt.Errorf("reply element %d: %w", i, err)
			}
			v = strconv.FormatInt(n, 10)
		}
		out[i] = v
	}
	return out, nil
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseAcquire decodes the acquire.lua reply.
func parseAcquire(vals []string) (acquireOutcome, error) {
	if len(vals) < 3 {
		return acquireOutcome{}, fmt.Errorf("acquire: short reply (%d elements)", len(vals))
	}
	out := acquireOutcome{Status: vals[0], Transition: vals[1] == "1"}
	switch out.Status {
	case statusExhausted, statusBreakerOpen, statusNoProxy:
		out.RetryAfterMs = atoi64(vals[2])
		return out, nil
	case statusOK:
	default:
		return acquireOutcome{}, fmt.Errorf("acquire: unexpected status %q", out.Status)
	}
	n := int(atoi64(vals[2]))
	if n < 0 || len(vals) != 3+n*acquireLeaseFields {
		return acquireOutcome{}, fmt.Errorf("acquire: malformed reply (%d leases, %d elements)", n, len(vals))
	}
	out.Leases = make([]scriptLease, n)
	for j := 0; j < n; j++ {
		// The length check above guarantees acquireLeaseFields elements per
		// lease; the array conversion makes every index bounds-checked at
		// compile time.
		start := 3 + j*acquireLeaseFields
		f := [acquireLeaseFields]string(vals[start : start+acquireLeaseFields])
		out.Leases[j] = scriptLease{
			ID:             f[0],
			IdentityID:     f[1],
			TypeName:       f[2],
			PayloadVersion: int(atoi64(f[3])),
			TypeVersion:    int(atoi64(f[4])),
			ExpiresMs:      atoi64(f[5]),
			Probe:          f[6] == "1",
			Sticky:         f[7] == "1",
			Bound:          f[8] == "b" || f[8] == "r",
			Rebound:        f[8] == "r",
			ProxyKey:       atoi64(f[9]),
			ProxyID:        f[10],
			State:          f[11],
			IdentityKey:    atoi64(f[12]),
			RebindsToday:   int(atoi64(f[13])),
		}
	}
	return out, nil
}

// clampRetry clamps a retry hint to [50 ms, 60 s].
func clampRetry(ms int64) int64 {
	switch {
	case ms < 50:
		return 50
	case ms > 60000:
		return 60000
	default:
		return ms
	}
}
