//go:build perf

package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/redis/rueidis"
)

// Interleaved before/after measurement of the worker-path scripts, the
// companion of BenchmarkAB* for the scheduler path. The frozen "before"
// sources live under testdata/worker_baseline and are the scripts as they
// stood after the scheduler-path optimization and before the worker-path one,
// so these rows isolate what this track changed.

// spName matches an sp_* identifier, like the loader's own scanner.
var spName = regexp.MustCompile(`\bsp_[a-z0-9_]+`)

// stripComments blanks Lua comments so that a helper a doc block only mentions
// is not mistaken for a call. common.lua has no long string literals.
func stripComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i := 0
	for i < len(src) {
		if strings.HasPrefix(src[i:], "--[[") {
			if end := strings.Index(src[i:], "]]"); end >= 0 {
				for _, r := range src[i : i+end+2] {
					if r == '\n' {
						b.WriteByte('\n')
					}
				}
				i += end + 2
				continue
			}
		}
		if strings.HasPrefix(src[i:], "--") {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				break
			}
			i += end
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

// selectPrelude reproduces store/redis.NewScript's helper selection against an
// arbitrary common.lua text, so a frozen baseline body gets exactly the helpers
// it names and is not slowed down by eighteen closures the shipped side does
// not create either.
func selectPrelude(common, body string) string {
	const marker = "--@sp "
	var header strings.Builder
	type section struct {
		name string
		src  string
		deps []string
	}
	var sections []section
	byName := map[string]int{}
	var cur *section
	var buf strings.Builder
	flush := func() {
		if cur == nil {
			return
		}
		cur.src = buf.String()
		for _, m := range spName.FindAllString(stripComments(cur.src), -1) {
			if m != cur.name {
				cur.deps = append(cur.deps, m)
			}
		}
		byName[cur.name] = len(sections)
		sections = append(sections, *cur)
		buf.Reset()
	}
	for _, line := range strings.Split(common, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, marker) {
			flush()
			cur = &section{name: strings.TrimSpace(strings.TrimPrefix(t, marker))}
			continue
		}
		if cur == nil {
			header.WriteString(line)
			header.WriteByte('\n')
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()

	need := make([]bool, len(sections))
	var mark func(string)
	mark = func(name string) {
		idx, ok := byName[name]
		if !ok || need[idx] {
			return
		}
		need[idx] = true
		for _, d := range sections[idx].deps {
			mark(d)
		}
	}
	for _, m := range spName.FindAllString(stripComments(body), -1) {
		mark(m)
	}
	var out strings.Builder
	out.WriteString(header.String())
	for i, s := range sections {
		if need[i] {
			out.WriteString(s.src)
		}
	}
	return out.String()
}

var (
	workerBaselineOnce sync.Once
	workerBaseline     map[string]*rueidis.Lua
)

// workerBaselines compiles the frozen worker-path scripts once per process,
// each with the helper selection of its own frozen common.lua.
func workerBaselines(tb testing.TB, e *env) map[string]*rueidis.Lua {
	tb.Helper()
	workerBaselineOnce.Do(func() {
		dir := filepath.Join(repoRoot(), "test", "perf", "testdata", "worker_baseline")
		read := func(name string) string {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				tb.Skipf("worker baseline %s: %v", name, err)
			}
			return string(b)
		}
		common := read("common.lua")
		workerBaseline = map[string]*rueidis.Lua{}
		for _, name := range []string{"observe", "apply", "lease_retain", "breaker_eval"} {
			body := read(name + ".lua")
			src := selectPrelude(common, body) + "\n-- ==== script: " + name + " ====\n" + body
			if err := e.client.Do(e.ctx, e.client.B().ScriptLoad().Script(src).Build()).Error(); err != nil {
				tb.Fatalf("script load worker baseline %s: %v", name, err)
			}
			workerBaseline[name] = rueidis.NewLuaScript(src)
		}
	})
	return workerBaseline
}

// observeArgsV1 is the pre-optimization ARGV of observe.lua: the health and
// cross-attribution constants as eight separate entries, and the breaker window
// as the window and bucket lengths rather than the bucket the script writes.
func (d *dataset) observeArgsV1(now int64, shard int, streamID, leaseID string, identity int64, o observeOpts) []string {
	obsIdentity, failure, risk := "100", "0", "0"
	if o.outcome != "success" {
		obsIdentity, failure, risk = "0", "1", "1"
	}
	args := []string{
		"full", itoa(shard), streamID,
		i64(now), leaseID,
		i64(d.eg()), i64(identity), "",
		o.outcome, o.blame, "0",
		obsIdentity, "",
		failure, risk,
		"0.1", "70", "600000",
		"3600000",
		"60000", "5000",
		"0", "300000", "5", "3",
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

// leaseRetainArgsV1 is the pre-optimization ARGV: one lease per call.
func (d *dataset) leaseRetainArgsV1(now int64) []string {
	return []string{d.ns, i64(now), "3600000", "1", "600000"}
}

// BenchmarkABWorker compares the shipped worker-path scripts with the frozen
// pre-optimization sources, interleaved.
func BenchmarkABWorker(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	base := workerBaselines(b, e)
	ops := *flagOps
	warm(b, e, s.observe)
	warm(b, e, s.apply)
	warm(b, e, s.leaseRetain)
	warm(b, e, s.breakerEval)

	leases, identities := makeLeases(b, e, ds, 100, 0, ds.epoch+60_000, "")
	defer ds.restore(b, e, leases, identities)
	leaseKeys := make([]string, len(leases))
	for i, id := range leases {
		leaseKeys[i] = ds.keys.Lease(ds.siteKey, id)
	}

	seq := 0
	observeBlock := func(name string, o observeOpts, v1 bool) func() sample {
		return func() sample {
			return measure(b, e, plan{name: name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
				seq++
				sid := i64(ds.epoch) + "-" + strconv.Itoa(seq)
				var res rueidis.RedisResult
				if v1 {
					res = base["observe"].Exec(e.ctx, e.client, []string{ds.meta()},
						ds.observeArgsV1(ds.epoch, 0, sid, leases[0], identities[0], o))
				} else {
					res = runScript(b, e, s.observe, []string{ds.meta()},
						ds.observeArgs(ds.epoch, 0, sid, leases[0], identities[0], o))
				}
				arr, err := res.ToArray()
				if err != nil || len(arr) == 0 {
					b.Fatalf("%s: %v", name, err)
				}
				if st, _ := arr[0].ToString(); st != "OK" {
					b.Fatalf("%s returned %q", name, st)
				}
			}})
		}
	}
	for _, c := range []struct {
		title string
		o     observeOpts
	}{
		{"observe.lua success, no counters", observeOpts{outcome: "success", blame: "none"}},
		{"observe.lua rate_limited, 2 counters + 2 ban windows", observeOpts{
			outcome: "rate_limited", blame: "identity", counters: 2, bans: []int64{604_800_000, 2_592_000_000}}},
	} {
		abRun(b, c.title, 1,
			abVariant{name: "before", run: observeBlock("observe before", c.o, true)},
			abVariant{name: "after", run: observeBlock("observe after", c.o, false)})
	}
	for _, k := range []string{ds.keys.Checkpoint(ds.siteKey), ds.keys.ActiveGroups(ds.siteKey)} {
		_ = e.client.Do(e.ctx, e.client.B().Del().Key(k).Build()).Error()
	}

	// lease_retain: one lease per call before, the whole site in one call after.
	retainBefore := func() sample {
		return measure(b, e, plan{name: "lease_retain before", ops: ops, scriptCmds: []string{"evalsha"},
			body: func(i int) {
				res := base["lease_retain"].Exec(e.ctx, e.client,
					[]string{leaseKeys[i%len(leaseKeys)]}, ds.leaseRetainArgsV1(ds.epoch))
				if err := res.Error(); err != nil {
					b.Fatalf("lease_retain before: %v", err)
				}
			}})
	}
	retainAfter := func(batch int) func() sample {
		keys := leaseKeys[:batch]
		args := ds.leaseRetainArgs(ds.epoch, batch)
		n := max(ops/batch, 20)
		return func() sample {
			out := measure(b, e, plan{name: "lease_retain after", ops: n, scriptCmds: []string{"evalsha"},
				body: func(int) { runScript(b, e, s.leaseRetain, keys, args) }})
			return out
		}
	}
	abRun(b, "lease_retain.lua, a request with one lease", 1,
		abVariant{name: "before (1 call)", run: retainBefore},
		abVariant{name: "after (1 call)", run: retainAfter(1)})

	// Per lease of a request that touches 100 leases of one site: 100 calls
	// before, one call after. abRun cannot scale the two sides differently, so
	// the rounds are run here.
	bestBefore, bestAfter := 0.0, 0.0
	for r := 0; r < abRounds; r++ {
		if got := retainBefore().serverPerOp(); r == 0 || got < bestBefore {
			bestBefore = got
		}
		if got := retainAfter(100)().serverPerOp() / 100; r == 0 || got < bestAfter {
			bestAfter = got
		}
	}
	logBoth(b, fmt.Sprintf("\nlease_retain.lua per lease, 100 leases of one site  (best of %d interleaved rounds)", abRounds))
	logBoth(b, fmt.Sprintf("  %-40s %9s %9s %8s", "variant", "us/lease", "delta", "change"))
	logBoth(b, fmt.Sprintf("  %-40s %9.2f %+9.2f %7.1f%%", "before (100 calls)", bestBefore, 0.0, 0.0))
	logBoth(b, fmt.Sprintf("  %-40s %9.2f %+9.2f %7.1f%%", "after (1 call)", bestAfter,
		bestAfter-bestBefore, 100*(bestAfter-bestBefore)/bestBefore))

	eg := ds.egIdle()
	applySeq := int64(0)
	applyBlock := func(v1 bool) func() sample {
		return func() sample {
			return measure(b, e, plan{name: "apply", ops: ops, scriptCmds: []string{"evalsha"}, body: func(i int) {
				ik := int64(i%int(ds.identities)) + 1
				applySeq++
				args := ds.applyCooldownArgs(ds.epoch, eg, ik, ds.epoch+300_000+applySeq)
				if v1 {
					if err := base["apply"].Exec(e.ctx, e.client, []string{ds.meta()}, args).Error(); err != nil {
						b.Fatalf("apply before: %v", err)
					}
					return
				}
				runScript(b, e, s.apply, []string{ds.meta()}, args)
			}})
		}
	}
	abRun(b, "apply.lua, automatic identity x endpoint cooldown", 1,
		abVariant{name: "before", run: applyBlock(true)},
		abVariant{name: "after", run: applyBlock(false)})

	seedWindows(b, e, ds, eg, 12)
	for _, mode := range []string{"read", "eval"} {
		breakerBlock := func(v1 bool) func() sample {
			return func() sample {
				return measure(b, e, plan{name: "breaker_eval", ops: ops, scriptCmds: []string{"evalsha"},
					body: func(int) {
						args := ds.breakerEvalArgs(ds.epoch, eg, mode)
						if v1 {
							if err := base["breaker_eval"].Exec(e.ctx, e.client, []string{ds.meta()}, args).Error(); err != nil {
								b.Fatalf("breaker_eval before: %v", err)
							}
							return
						}
						runScript(b, e, s.breakerEval, []string{ds.meta()}, args)
					}})
			}
		}
		abRun(b, "breaker_eval.lua mode="+mode+", 12-bucket window", 1,
			abVariant{name: "before", run: breakerBlock(true)},
			abVariant{name: "after", run: breakerBlock(false)})
	}
	dropBreakerState(b, e, ds, eg, 12)
}
