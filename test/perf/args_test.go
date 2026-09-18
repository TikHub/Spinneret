//go:build perf

package perf

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// acquireCfg is the rotation policy the harness drives acquire.lua with. Its
// defaults mirror the seeded load-test policy (weighted_random,
// candidate_sample 32, lease_ttl 60s, max_concurrent_leases 1, no proxies),
// which is the configuration docs/benchmarks.md measured.
type acquireCfg struct {
	eg         int64 // 0 = the clean group
	count      int
	sample     int
	strategy   string // w | l | r | b
	ttlMs      int64
	lifetimeMs int64
	maxLeases  int
	quotas     []int64 // window lengths in ms; limits are set high enough to admit
	session    string
	stickyTTL  int64
}

func defaultAcquireCfg() acquireCfg {
	return acquireCfg{
		count:      1,
		sample:     32,
		strategy:   "w",
		ttlMs:      60_000,
		lifetimeMs: 1_800_000,
		maxLeases:  1,
	}
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }
func itoa(n int) string  { return strconv.Itoa(n) }

// acquireArgs builds the ARGV of acquire.lua (layout in the script header and
// in internal/scheduler/args.go).
func (d *dataset) acquireArgs(cfg acquireCfg, now int64, seq int) []string {
	args := make([]string, 0, 64+4*cfg.count+len(cfg.quotas)*2)
	eg := cfg.eg
	if eg == 0 {
		eg = d.eg()
	}
	args = append(args,
		i64(now),            //  1 now ms
		itoa(cfg.count),     //  2 count
		i64(eg),             //  3 endpoint group hkey
		cfg.strategy,        //  4 strategy
		itoa(cfg.sample),    //  5 candidate sample K
		i64(cfg.ttlMs),      //  6 lease ttl ms
		i64(cfg.lifetimeMs), //  7 max lease lifetime ms
		itoa(cfg.maxLeases), //  8 max concurrent leases
		"0",                 //  9 reuse interval ms
		"r",                 // 10 reuse anchor
		"e",                 // 11 reuse scope
		"100000",            // 12 probe weight factor (ppm, 0.1)
		"2",                 // 13 probe max leases
		"0",                 // 14 warmup duration ms
		"1000000",           // 15 warmup quota factor (ppm, 1.0)
		"70",                // 16 health baseline
		"600000",            // 17 health tau ms
		"5",                 // 18 half-open probes per 10 s
		cfg.session,         // 19 normalized session key
		i64(cfg.stickyTTL),  // 20 sticky ttl ms
		"600000",            // 21 late report window ms
		"16",                // 22 report shards
		"perf-node",         // 23 node
		"tok_perf",          // 24 token id
		d.ns,                // 25 namespace id
		"n",                 // 26 proxy mode
		"0",                 // 27 proxy region match
		"300000",            // 28 rebind tolerance ms
		"3",                 // 29 max rebinds per day
		time.UnixMilli(now).UTC().Format("20060102"), // 30 today
		"0", // kinds
		"0", // providers
		"0", // regions
		"0", // tags
	)
	args = append(args, itoa(len(cfg.quotas)))
	for _, w := range cfg.quotas {
		args = append(args, "1000000", i64(w)) // a limit no benchmark can reach
	}
	for j := 0; j < cfg.count; j++ {
		args = append(args, idgen.LeasePrefix(d.siteKey))
	}
	// Go supplies count*4+16 random values in parts per million; acquire.lua
	// derives further values from them.
	n := cfg.count*4 + 16
	for j := 0; j < n; j++ {
		args = append(args, randFloatStr(seq*n+j))
	}
	return args
}

// randTable is a fixed table of pseudo-random parts-per-million values, the
// encoding internal/scheduler.ppm produces. A deterministic table keeps runs
// comparable and keeps the generator out of the timed section.
var randTable = func() []string {
	const size = 8192
	out := make([]string, size)
	x := uint64(0x9E3779B97F4A7C15)
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = strconv.FormatInt(int64(math.Round(float64(x>>11)/float64(1<<53)*1e6)), 10)
	}
	return out
}()

func randFloatStr(i int) string { return randTable[i&(len(randTable)-1)] }

// groupLayout is the endpoint group layout consumed by ls_groups in
// lease_end.lua: the number of clients, then one entry per client holding that
// client's "hkey,baseline" pairs. It mirrors internal/scheduler.groupLayout.
func (d *dataset) groupLayout() []string {
	pairs := make([]string, 0, 2*len(d.groups))
	for _, g := range d.groups {
		pairs = append(pairs, i64(g), "70")
	}
	return []string{"1", strings.Join(pairs, ",")}
}

// groupLayoutV1 is the expanded layout the frozen baseline under
// testdata/baseline parses: n clients, then per client n groups and
// (hkey, baseline) pairs. It exists only so the before/after comparison can
// give each side the wire form its own code reads.
func (d *dataset) groupLayoutV1() []string {
	out := make([]string, 0, 2+2*len(d.groups))
	out = append(out, "1", itoa(len(d.groups)))
	for _, g := range d.groups {
		out = append(out, i64(g), "70")
	}
	return out
}

// releaseArgs builds the ARGV of release.lua. v1 selects the expanded layout
// of the frozen baseline.
func (d *dataset) releaseArgs(now int64, leaseID string, v1 ...bool) []string {
	args := []string{i64(now), leaseID, "600000", d.ns, "0", "0"}
	if len(v1) > 0 && v1[0] {
		return append(args, d.groupLayoutV1()...)
	}
	return append(args, d.groupLayout()...)
}

// reapArgs builds the ARGV of reap.lua. v1 selects the expanded layout of the
// frozen baseline.
func (d *dataset) reapArgs(now int64, limit int, v1 ...bool) []string {
	args := []string{i64(now), itoa(limit), "600000"}
	if len(v1) > 0 && v1[0] {
		return append(args, d.groupLayoutV1()...)
	}
	return append(args, d.groupLayout()...)
}

// renewArgs builds the ARGV of renew.lua.
func (d *dataset) renewArgs(now int64, leaseID string) []string {
	return []string{i64(now), leaseID, "0", d.ns}
}

// leaseRetainArgs builds the ARGV of lease_retain.lua for a batch of leases
// (one report ingested per lease).
func (d *dataset) leaseRetainArgs(now int64, leases int) []string {
	args := make([]string, 0, 4+leases)
	args = append(args, d.ns, i64(now), "3600000", "600000")
	for i := 0; i < leases; i++ {
		args = append(args, "1")
	}
	return args
}

// observeOpts selects an observe.lua variant.
type observeOpts struct {
	outcome  string // success | rate_limited
	blame    string // identity | proxy | both | none
	counters int    // count-condition counters requested for this outcome
	quotas   []int64
	bans     []int64
}

// observeArgs builds the ARGV of a full observe.lua call (layout in the script
// header and in internal/worker/observe.go).
func (d *dataset) observeArgs(now int64, shard int, streamID, leaseID string, identity int64, o observeOpts) []string {
	obsIdentity, failure, risk := "100", "0", "0"
	if o.outcome != "success" {
		obsIdentity, failure, risk = "0", "1", "1"
	}
	args := []string{
		"full", itoa(shard), streamID,
		i64(now), leaseID,
		i64(d.eg()), i64(identity), "", // no proxy
		o.outcome, o.blame, "0", // not late
		obsIdentity, "", // no proxy observation
		failure, risk,
		"0.1,70,600000,3600000",           // health alpha, baseline, tau, failure reset after
		i64(d.eg()) + ":" + i64(now/5000), // breaker window suffix (bucket 5 s)
		"120000",                          // breaker window key ttl (2 x 60 s window)
		"0,300000,5,3",                    // cross attribution disabled
	}
	args = append(args, itoa(len(o.quotas)))
	for _, w := range o.quotas {
		args = append(args, i64(w))
	}
	args = append(args, itoa(o.counters))
	for c := 0; c < o.counters; c++ {
		args = append(args, "i", i64(int64(3600_000*(c+1))))
	}
	args = append(args, itoa(len(o.bans)))
	for _, w := range o.bans {
		args = append(args, i64(w))
	}
	return args
}

// applyCooldownArgs builds the ARGV of an apply.lua identity x endpoint-group
// cooldown, the disposition the report pipeline applies most often.
//
// until must rise on every call of a block: an operation whose cooldown is not
// longer than the one already on the entry returns "already_cooling" without
// writing anything, which would measure the early return instead of the
// disposition.
func (d *dataset) applyCooldownArgs(now int64, eg, identity, until int64) []string {
	op := `{"op":"cd","sc":"ie","s":"` + i64(identity) + `","eg":"` + i64(eg) +
		`","u":` + i64(until) + `,"f":1,"trim":120000,"bl":70}`
	return []string{i64(now), "", op}
}

// breakerEvalArgs builds the ARGV of breaker_eval.lua in read/eval mode.
func (d *dataset) breakerEvalArgs(now int64, eg int64, mode string) []string {
	return []string{
		i64(eg), i64(now), "5000", "12", "50",
		"0.4", "10", "0.2",
		"120000", "3600000", "1800000",
		"5", "0.8", mode, "1",
	}
}
