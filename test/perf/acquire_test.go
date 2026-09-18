//go:build perf

package perf

import (
	"fmt"
	"testing"
)

// acquireRun measures one acquire.lua configuration. It collects the leases
// the block wrote so the maintain pass can put the pool back exactly as it was
// seeded; without that the leased identities leave the due range and later
// calls would measure a progressively emptier pool.
func acquireRun(b *testing.B, e *env, ds *dataset, name string, cfg acquireCfg, ops int) sample {
	s := scripts(b)
	warm(b, e, s.acquire)

	var (
		leases     []string
		identities []int64
		exhausted  int
	)
	// Pre-build one ARGV per call: the lease-id prefixes must be unique, and
	// building them inside the timed section would measure Go, not Valkey.
	argv := make([][]string, ops)
	for i := range argv {
		argv[i] = ds.acquireArgs(cfg, ds.epoch, i)
	}
	p := plan{
		name: name, ops: ops, scriptCmds: []string{"evalsha"}, chunk: *flagChunk,
		maintain: func() {
			ds.restore(b, e, leases, identities)
			leases, identities = leases[:0], identities[:0]
		},
		body: func(i int) {
			status, lids, ids := leaseReply(b, runScript(b, e, s.acquire, []string{ds.meta()}, argv[i]))
			if status != "OK" {
				exhausted++
				return
			}
			leases = append(leases, lids...)
			identities = append(identities, ids...)
		},
	}
	out := measure(b, e, p)
	ds.restore(b, e, leases, identities)
	if exhausted > 0 {
		b.Errorf("%s: %d of %d calls did not acquire; the pool drained and the numbers are not comparable",
			name, exhausted, ops)
	}
	return out
}

// BenchmarkAcquire measures acquire.lua over the candidate sample sizes and
// pool conditions of the design (spec §6.1, docs/design/0_first_doc.md §18.4).
func BenchmarkAcquire(b *testing.B) {
	e, ds := benchEnv(b)
	ops := *flagOps

	samples := make([]sample, 0, 16)
	add := func(s sample) { samples = append(samples, s); s.report(b) }

	for _, k := range []int{1, 4, 8, 32} {
		b.Run(fmt.Sprintf("clean/K%d", k), func(b *testing.B) {
			cfg := defaultAcquireCfg()
			cfg.sample = k
			add(acquireRun(b, e, ds, fmt.Sprintf("acquire K=%-2d first candidate passes", k), cfg, ops))
		})
	}
	for _, k := range []int{1, 4, 8, 32} {
		b.Run(fmt.Sprintf("filtered50/K%d", k), func(b *testing.B) {
			cfg := defaultAcquireCfg()
			cfg.sample = k
			cfg.eg = ds.egMixed()
			add(acquireRun(b, e, ds, fmt.Sprintf("acquire K=%-2d 50%% candidates filtered", k), cfg, ops))
		})
	}
	b.Run("batch10", func(b *testing.B) {
		cfg := defaultAcquireCfg()
		cfg.count = 10
		add(acquireRun(b, e, ds, "acquire batch count=10 K=32", cfg, ops/2))
	})
	b.Run("batch10/filtered50", func(b *testing.B) {
		cfg := defaultAcquireCfg()
		cfg.count = 10
		cfg.eg = ds.egMixed()
		add(acquireRun(b, e, ds, "acquire batch count=10 K=32 50% filtered", cfg, ops/2))
	})
	b.Run("quota1", func(b *testing.B) {
		cfg := defaultAcquireCfg()
		cfg.quotas = []int64{3_600_000}
		add(acquireRun(b, e, ds, "acquire K=32 with 1 quota window", cfg, ops))
	})
	b.Run("mc4", func(b *testing.B) {
		cfg := defaultAcquireCfg()
		cfg.maxLeases = 4
		add(acquireRun(b, e, ds, "acquire K=32 max_concurrent_leases=4", cfg, ops))
	})
	b.Run("strategy_best_health", func(b *testing.B) {
		cfg := defaultAcquireCfg()
		cfg.strategy = "b"
		add(acquireRun(b, e, ds, "acquire K=32 strategy=best_health", cfg, ops))
	})

	b.Log("\nper-primitive breakdown of the last configuration:")
	if len(samples) > 0 {
		samples[len(samples)-1].reportNested(b)
	}
}

// BenchmarkAcquireBreakdown prints the exact command mix of one acquire call,
// which is what the cost-attribution table multiplies the calibrated primitive
// costs with.
func BenchmarkAcquireBreakdown(b *testing.B) {
	e, ds := benchEnv(b)
	cfg := defaultAcquireCfg()
	s := acquireRun(b, e, ds, "acquire K=32 (breakdown)", cfg, *flagOps)
	s.report(b)
	s.reportNested(b)

	cfg.eg = ds.egMixed()
	f := acquireRun(b, e, ds, "acquire K=32 50% filtered (breakdown)", cfg, *flagOps)
	f.report(b)
	f.reportNested(b)
}
