//go:build perf

package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/redis/rueidis"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Interleaved before/after measurement.
//
// The run-to-run drift of the same script on the same data is +-8 % on a
// developer machine, which is larger than most single optimizations, so a
// "before" run followed by an "after" run cannot decide anything. Every
// comparison here therefore runs both variants in one process, alternating
// blocks, and keeps the best block of each: a slow patch in the machine hits
// both variants, and the best block of each is the one least disturbed by it.
//
// The "before" sources are frozen copies under testdata/baseline, assembled the
// way the loader assembled them before helper selection existed: the whole
// common.lua in front of every body. They are data, never compiled into the
// server, and exist so the before/after numbers in docs/benchmarks.md stay
// reproducible after the production sources have moved on.

// baselineFile reads one frozen source, skipping the benchmark when the
// baseline is not present.
func baselineFile(tb testing.TB, name string) string {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(), "test", "perf", "testdata", "baseline", name))
	if err != nil {
		tb.Skipf("baseline source %s: %v", name, err)
	}
	return string(b)
}

// baselineSource assembles a frozen script the way store/redis.NewScript did
// before helper selection: the complete common.lua, then the body parts.
func baselineSource(tb testing.TB, name string, parts ...string) string {
	tb.Helper()
	srcs := make([]string, 0, len(parts)+2)
	srcs = append(srcs, baselineFile(tb, "common.lua"), "-- ==== script: "+name+" ====")
	for _, p := range parts {
		srcs = append(srcs, baselineFile(tb, p))
	}
	return joinLua(srcs...)
}

// loadBaseline compiles and server-side caches a frozen script.
func loadBaseline(tb testing.TB, e *env, name string, parts ...string) *rueidis.Lua {
	tb.Helper()
	src := baselineSource(tb, name, parts...)
	if err := e.client.Do(e.ctx, e.client.B().ScriptLoad().Script(src).Build()).Error(); err != nil {
		tb.Fatalf("script load baseline %s: %v", name, err)
	}
	return rueidis.NewLuaScript(src)
}

// execFn runs one call of the script under test.
type execFn func(keys, args []string) rueidis.RedisResult

// baselineExec adapts a frozen script to execFn.
func baselineExec(tb testing.TB, e *env, s *rueidis.Lua) execFn {
	return func(keys, args []string) rueidis.RedisResult {
		res := s.Exec(e.ctx, e.client, keys, args)
		if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
			tb.Fatalf("baseline script: %v", err)
		}
		return res
	}
}

// abVariant is one side of an interleaved comparison.
type abVariant struct {
	name string
	run  func() sample
}

// abRounds is the number of interleaved rounds every comparison below runs.
const abRounds = 3

// abRun runs the variants interleaved and reports the best block of each with
// the delta against the first variant. scale divides the per-call figure (a
// reap call ends a whole batch).
func abRun(b *testing.B, title string, scale float64, variants ...abVariant) {
	best := make([]float64, len(variants))
	for r := 0; r < abRounds; r++ {
		for i, v := range variants {
			got := v.run().serverPerOp() / scale
			if r == 0 || got < best[i] {
				best[i] = got
			}
		}
	}
	logBoth(b, fmt.Sprintf("\n%s  (best of %d interleaved rounds)", title, abRounds))
	logBoth(b, fmt.Sprintf("  %-40s %9s %9s %8s", "variant", "us/call", "delta", "change"))
	for i, v := range variants {
		d := best[i] - best[0]
		logBoth(b, fmt.Sprintf("  %-40s %9.2f %+9.2f %7.1f%%", v.name, best[i], d, 100*d/best[0]))
	}
}

// BenchmarkABAcquire compares the shipped acquire.lua with the frozen baseline
// at the candidate sample sizes of the design (spec §6.1).
func BenchmarkABAcquire(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps
	warm(b, e, s.acquire)
	base := loadBaseline(b, e, "acquire", "acquire.lua", "acquire_filters.lua", "acquire_proxy.lua",
		"acquire_write.lua", "acquire_select.lua", "acquire_main.lua")

	block := func(name string, argv [][]string, exec execFn) func() sample {
		return func() sample {
			var leases []string
			var identities []int64
			out := measure(b, e, plan{
				name: name, ops: ops, scriptCmds: []string{"evalsha"}, chunk: *flagChunk,
				maintain: func() {
					ds.restore(b, e, leases, identities)
					leases, identities = leases[:0], identities[:0]
				},
				body: func(i int) {
					status, lids, ids := leaseReply(b, exec([]string{ds.meta()}, argv[i]))
					if status != "OK" {
						b.Fatalf("%s: status %s at call %d; the pool drained", name, status, i)
					}
					leases = append(leases, lids...)
					identities = append(identities, ids...)
				},
			})
			ds.restore(b, e, leases, identities)
			return out
		}
	}

	for _, k := range []int{1, 8, 32} {
		cfg := defaultAcquireCfg()
		cfg.sample = k
		newArgv := make([][]string, ops)
		oldArgv := make([][]string, ops)
		for i := range newArgv {
			newArgv[i] = ds.acquireArgs(cfg, ds.epoch, i)
			oldArgv[i] = ppmToFractionArgs(newArgv[i], cfg.count)
		}
		abRun(b, fmt.Sprintf("acquire.lua, candidate_sample %d, clean pool", k), 1,
			abVariant{"before (frozen baseline)", block("before", oldArgv, baselineExec(b, e, base))},
			abVariant{"after (shipped)", block("after", newArgv, func(keys, args []string) rueidis.RedisResult {
				return runScript(b, e, s.acquire, keys, args)
			})},
		)
	}

	cfg := defaultAcquireCfg()
	cfg.eg = ds.egMixed()
	newArgv := make([][]string, ops)
	oldArgv := make([][]string, ops)
	for i := range newArgv {
		newArgv[i] = ds.acquireArgs(cfg, ds.epoch, i)
		oldArgv[i] = ppmToFractionArgs(newArgv[i], cfg.count)
	}
	abRun(b, "acquire.lua, candidate_sample 32, 50% of candidates filtered", 1,
		abVariant{"before (frozen baseline)", block("before", oldArgv, baselineExec(b, e, base))},
		abVariant{"after (shipped)", block("after", newArgv, func(keys, args []string) rueidis.RedisResult {
			return runScript(b, e, s.acquire, keys, args)
		})},
	)
}

// ppmToFractionArgs converts the parts-per-million ARGV entries of the shipped
// layout back to the decimal fractions the frozen baseline parses: ARGV[12],
// ARGV[15] and the trailing count*4+16 random values. The numeric value is the
// same; only the wire form differs (spec §5.7).
func ppmToFractionArgs(args []string, count int) []string {
	out := make([]string, len(args))
	copy(out, args)
	frac := func(i int) {
		n, err := strconv.ParseInt(out[i], 10, 64)
		if err != nil {
			panic("perf: ARGV[" + strconv.Itoa(i+1) + "] is not a ppm integer: " + out[i])
		}
		out[i] = strconv.FormatFloat(float64(n)/1e6, 'f', 6, 64)
	}
	frac(11)
	frac(14)
	for i := len(out) - (count*4 + 16); i < len(out); i++ {
		frac(i)
	}
	return out
}

// BenchmarkABLeaseEnd compares release.lua, renew.lua and reap.lua with their
// frozen baselines. Their ARGV layout did not change.
func BenchmarkABLeaseEnd(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops, chunk := *flagOps, *flagChunk
	warm(b, e, s.release)
	warm(b, e, s.renew)
	warm(b, e, s.reap)
	relBase := loadBaseline(b, e, "release", "lease_end.lua", "release.lua")
	renewBase := loadBaseline(b, e, "renew", "renew.lua")
	reapBase := loadBaseline(b, e, "reap", "lease_end.lua", "reap.lua")

	releaseBlock := func(name, xgMark string, v1 bool, exec execFn) func() sample {
		return func() sample {
			var ids []string
			var identities []int64
			round := 0
			out := measure(b, e, plan{
				name: name, ops: ops, scriptCmds: []string{"evalsha"}, chunk: chunk,
				maintain: func() {
					if len(ids) > 0 {
						ds.restore(b, e, ids, identities)
					}
					ids, identities = makeLeases(b, e, ds, chunk, round*chunk, ds.epoch+60_000, xgMark)
					round++
				},
				body: func(i int) {
					exec([]string{ds.meta()}, ds.releaseArgs(ds.epoch, ids[i%chunk], v1))
				},
			})
			ds.restore(b, e, ids, identities)
			return out
		}
	}
	// Each side gets the wire forms its own code reads: the expanded endpoint
	// group layout for the frozen baseline, the packed one for the shipped
	// script, and the exclusive-push marker its own acquire writes: the
	// frozen baseline only ever wrote "1", which makes the lease end walk every
	// endpoint group of the client, while the shipped acquire names the group
	// that pushed (spec §5.2). The marker change is part of what is measured.
	for _, xg := range []bool{false, true} {
		title, oldMark, newMark := "release.lua (xg unset, O(1))", "", ""
		if xg {
			title = "release.lua (xg set by one other group)"
			oldMark, newMark = "1", ","+i64(ds.egIdle())+","
		}
		abRun(b, title, 1,
			abVariant{"before (frozen baseline)", releaseBlock("before", oldMark, true, baselineExec(b, e, relBase))},
			abVariant{"after (shipped)", releaseBlock("after", newMark, false, func(keys, args []string) rueidis.RedisResult {
				return runScript(b, e, s.release, keys, args)
			})},
		)
	}

	renewBlock := func(name string, exec execFn) func() sample {
		return func() sample {
			ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
			out := measure(b, e, plan{name: name, ops: ops, scriptCmds: []string{"evalsha"},
				body: func(int) { exec([]string{ds.meta()}, ds.renewArgs(ds.epoch, ids[0])) }})
			ds.restore(b, e, ids, identities)
			return out
		}
	}
	abRun(b, "renew.lua", 1,
		abVariant{"before (frozen baseline)", renewBlock("before", baselineExec(b, e, renewBase))},
		abVariant{"after (shipped)", renewBlock("after", func(keys, args []string) rueidis.RedisResult {
			return runScript(b, e, s.renew, keys, args)
		})},
	)

	const batch = 100
	reapOps := max(ops/100, 10)
	reapBlock := func(name string, v1 bool, exec execFn) func() sample {
		return func() sample {
			var ids []string
			var identities []int64
			round := 0
			out := measure(b, e, plan{
				name: name, ops: reapOps, scriptCmds: []string{"evalsha"}, chunk: 1,
				maintain: func() {
					if len(ids) > 0 {
						ds.restore(b, e, ids, identities)
					}
					ids, identities = makeLeases(b, e, ds, batch, round*batch, ds.epoch-1000, "")
					round++
				},
				body: func(int) { exec([]string{ds.meta()}, ds.reapArgs(ds.epoch, batch, v1)) },
			})
			ds.restore(b, e, ids, identities)
			return out
		}
	}
	abRun(b, "reap.lua batch 100 (us per expired lease)", batch,
		abVariant{"before (frozen baseline)", reapBlock("before", true, baselineExec(b, e, reapBase))},
		abVariant{"after (shipped)", reapBlock("after", false, func(keys, args []string) rueidis.RedisResult {
			return runScript(b, e, s.reap, keys, args)
		})},
	)
}

// observeABBlock measures one observe.lua block against the given executor.
func observeABBlock(b *testing.B, e *env, ds *dataset, ops int, exec execFn) func() sample {
	// observe.lua compares the stream id of the report against the checkpoint of
	// the shard numerically and answers DUP - on a far shorter path - for
	// anything that does not advance it. The id therefore has to be numeric and
	// strictly increasing across every block of every round, and the checkpoint
	// is dropped around the comparison so that neither this benchmark nor
	// BenchmarkObserve inherits the other's.
	seq := 0
	dropCheckpoint := func() {
		_ = e.client.Do(e.ctx, e.client.B().Del().Key(ds.keys.Checkpoint(ds.siteKey)).Build()).Error()
	}
	return func() sample {
		ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
		dropCheckpoint()
		defer dropCheckpoint()
		out := measure(b, e, plan{name: "observe", ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			seq++
			args := ds.observeArgs(ds.epoch, 0, i64(ds.epoch)+"-"+strconv.Itoa(seq), ids[0], identities[0],
				observeOpts{outcome: "success", blame: "none"})
			arr, err := exec([]string{ds.meta()}, args).ToArray()
			if err != nil || len(arr) == 0 {
				b.Fatalf("observe reply: %v", err)
			}
			if st, _ := arr[0].ToString(); st != "OK" {
				b.Fatalf("observe returned %q, expected OK", st)
			}
		}})
		ds.restore(b, e, ids, identities)
		return out
	}
}

// leaseRetainABBlock measures one lease_retain.lua block against the executor.
func leaseRetainABBlock(b *testing.B, e *env, ds *dataset, ops int, exec execFn) func() sample {
	return func() sample {
		ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
		out := measure(b, e, plan{name: "lease_retain", ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			exec([]string{ds.keys.Lease(ds.siteKey, ids[0])}, ds.leaseRetainArgs(ds.epoch, 1))
		}})
		ds.restore(b, e, ids, identities)
		return out
	}
}

// BenchmarkABPrelude compares the scripts this track did not touch with the
// same bodies in front of the frozen common.lua. It isolates what the shared
// prelude change (helper selection plus the faster packed-state codec and
// number parsing) is worth to the rest of the system, which pays for
// common.lua on every call without owning any of it.
func BenchmarkABPrelude(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps
	oldCommon := baselineFile(b, "common.lua")

	// The bodies are the shipped ones; only the prelude in front of them
	// differs, so any difference is the prelude's.
	cases := []struct {
		name string
		rel  string
		prod *storeredis.Script
		run  func(exec execFn) func() sample
	}{
		{name: "observe.lua (success)", rel: "internal/worker/lua/observe.lua", prod: s.observe,
			run: func(exec execFn) func() sample { return observeABBlock(b, e, ds, ops, exec) }},
		{name: "lease_retain.lua", rel: "internal/signal/lua/lease_retain.lua", prod: s.leaseRetain,
			run: func(exec execFn) func() sample { return leaseRetainABBlock(b, e, ds, ops, exec) }},
	}
	for _, c := range cases {
		src := joinLua(oldCommon, "-- ==== script: ab ====", luaFile(b, c.rel))
		if err := e.client.Do(e.ctx, e.client.B().ScriptLoad().Script(src).Build()).Error(); err != nil {
			b.Fatalf("script load %s: %v", c.rel, err)
		}
		old := rueidis.NewLuaScript(src)
		warm(b, e, c.prod)
		prod := c.prod
		abRun(b, c.name, 1,
			abVariant{"before (frozen common.lua)", c.run(baselineExec(b, e, old))},
			abVariant{"after (shipped prelude)", c.run(func(keys, args []string) rueidis.RedisResult {
				return runScript(b, e, prod, keys, args)
			})},
		)
	}
}
