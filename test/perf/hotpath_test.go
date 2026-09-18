//go:build perf

package perf

import (
	"strconv"
	"strings"
	"testing"

	"github.com/redis/rueidis"
)

// makeLeases writes n active lease hashes with plain commands (no EVALSHA, so
// the SLOWLOG filter of the measured block stays clean) and points the matching
// identities at them. Lease i uses identity (start+i)%identities + 1.
//
// xgMark is the exclusive-push marker to put on the identity ("" for none). The
// shipped acquire writes ",<eg>," for the group that pushed (spec §5.2); a bare
// "1" is what an earlier release wrote and means "every group of the client".
func makeLeases(b *testing.B, e *env, ds *dataset, n, start int, expiresMs int64, xgMark string) (ids []string, identities []int64) {
	c := e.client
	var cmds rueidis.Commands
	ids = make([]string, 0, n)
	identities = make([]int64, 0, n)
	for j := 0; j < n; j++ {
		ik := int64((start+j)%int(ds.identities)) + 1
		lid := "lse_perf" + strconv.Itoa(start+j) + "_1_00"
		ids = append(ids, lid)
		identities = append(identities, ik)
		cmds = append(cmds, c.B().Hset().Key(ds.keys.Lease(ds.siteKey, lid)).FieldValue().
			FieldValue("i", i64(ik)).FieldValue("iid", "idt_"+i64(ik)).FieldValue("e", i64(ds.eg())).
			FieldValue("p", "").FieldValue("pid", "").FieldValue("n", "perf-node").
			FieldValue("tk", "tok_perf").FieldValue("ns", ds.ns).FieldValue("sk", "").
			FieldValue("a", i64(ds.epoch-1000)).FieldValue("x", i64(expiresMs)).
			FieldValue("cap", i64(ds.epoch+1_800_000)).FieldValue("ttl", "60000").
			FieldValue("st", "active").FieldValue("pr", "0").FieldValue("mc", "1").
			FieldValue("ri", "0").FieldValue("ra", "r").FieldValue("rs", "e").
			FieldValue("rc", "0").FieldValue("rv", "0").Build())
		cmds = append(cmds, c.B().Zadd().Key(ds.keys.LeaseExpiry(ds.siteKey)).ScoreMember().
			ScoreMember(float64(expiresMs), lid).Build())
		idset := c.B().Hset().Key(ds.keys.Identity(ds.siteKey, ik)).FieldValue().
			FieldValue("al", "1").FieldValue("xl", i64(expiresMs))
		if xgMark != "" {
			idset = idset.FieldValue("xg", xgMark)
		} else {
			// An earlier block may have left the marker behind (an acquire that
			// pushed the identity sets it); clear it so the O(1) case really is
			// the O(1) case.
			cmds = append(cmds, c.B().Hdel().Key(ds.keys.Identity(ds.siteKey, ik)).Field("xg").Build())
		}
		cmds = append(cmds, idset.Build())
	}
	for _, res := range c.DoMulti(e.ctx, cmds...) {
		if err := res.Error(); err != nil {
			b.Fatalf("makeLeases: %v", err)
		}
	}
	return ids, identities
}

// BenchmarkLeaseEnd measures release.lua and reap.lua, the two scripts built on
// lease_end.lua. The "xg" variants exercise the exclusive-lease restore that
// walks every endpoint group of the client (documents/en/17-performance.md, "The lease-end
// fix"); without the marker the walk is skipped entirely.
func BenchmarkLeaseEnd(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	warm(b, e, s.release)
	warm(b, e, s.reap)
	warm(b, e, s.renew)
	ops, chunk := *flagOps, *flagChunk

	releaseCase := func(name, xgMark string) {
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
				runScript(b, e, s.release, []string{ds.meta()}, ds.releaseArgs(ds.epoch, ids[i%chunk]))
			},
		})
		ds.restore(b, e, ids, identities)
		out.report(b)
		out.reportNested(b)
	}
	b.Run("release", func(b *testing.B) {
		releaseCase("release.lua (xg unset, O(1))", "")
	})
	b.Run("release_xg", func(b *testing.B) {
		releaseCase("release.lua (xg set, walks the pushing group)", ","+i64(ds.egIdle())+",")
	})

	b.Run("renew", func(b *testing.B) {
		ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
		out := measure(b, e, plan{name: "renew.lua", ops: ops, scriptCmds: []string{"evalsha"},
			body: func(int) { runScript(b, e, s.renew, []string{ds.meta()}, ds.renewArgs(ds.epoch, ids[0])) }})
		ds.restore(b, e, ids, identities)
		out.report(b)
		out.reportNested(b)
	})

	b.Run("reap_batch100", func(b *testing.B) {
		const batch = 100
		reapOps := max(ops/100, 10)
		var ids []string
		var identities []int64
		round := 0
		out := measure(b, e, plan{
			name: "reap.lua batch=100", ops: reapOps, scriptCmds: []string{"evalsha"}, chunk: 1,
			maintain: func() {
				if len(ids) > 0 {
					ds.restore(b, e, ids, identities)
				}
				ids, identities = makeLeases(b, e, ds, batch, round*batch, ds.epoch-1000, "")
				round++
			},
			body: func(int) {
				runScript(b, e, s.reap, []string{ds.meta()}, ds.reapArgs(ds.epoch, batch))
			},
		})
		ds.restore(b, e, ids, identities)
		// reap.lua ends 100 leases per call; normalise to one lease.
		out.report(b)
		b.Logf("    reap.lua per expired lease: %.2f us server, %.2f nested commands",
			out.serverPerOp()/batch, out.nestedCallsPerOp()/batch)
		out.reportNested(b)
	})
}

// BenchmarkObserve measures observe.lua, the worker-side state update, which
// documents/en/17-performance.md puts at 25 % of the Valkey CPU of an acquire->report cycle.
func BenchmarkObserve(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	warm(b, e, s.observe)
	ops := *flagOps

	ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
	defer ds.restore(b, e, ids, identities)

	cases := []struct {
		name string
		o    observeOpts
	}{
		{"observe success, no counters", observeOpts{outcome: "success", blame: "none"}},
		{"observe success, 1 quota window", observeOpts{outcome: "success", blame: "none", quotas: []int64{3_600_000}}},
		{"observe rate_limited, no counters", observeOpts{outcome: "rate_limited", blame: "identity"}},
		{"observe rate_limited, 2 counters", observeOpts{outcome: "rate_limited", blame: "identity", counters: 2}},
		{"observe rate_limited, 2 counters + 2 ban windows", observeOpts{
			outcome: "rate_limited", blame: "identity", counters: 2, bans: []int64{604_800_000, 2_592_000_000}}},
	}
	seq := 0
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			out := measure(b, e, plan{name: c.name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
				seq++
				args := ds.observeArgs(ds.epoch, 0, i64(ds.epoch)+"-"+strconv.Itoa(seq), ids[0], identities[0], c.o)
				res := runScript(b, e, s.observe, []string{ds.meta()}, args)
				arr, err := res.ToArray()
				if err != nil || len(arr) == 0 {
					b.Fatalf("observe reply: %v", err)
				}
				if st, _ := arr[0].ToString(); st != "OK" {
					b.Fatalf("observe returned %q, expected OK", st)
				}
			}})
			out.report(b)
			out.reportNested(b)
		})
	}
	// The observe block wrote a checkpoint and window buckets; drop them so a
	// later block starts from the seeded state.
	for _, k := range []string{ds.keys.Checkpoint(ds.siteKey), ds.keys.ActiveGroups(ds.siteKey)} {
		_ = e.client.Do(e.ctx, e.client.B().Del().Key(k).Build()).Error()
	}
}

// BenchmarkSignal measures ingest.lua and lease_retain.lua, the two scripts on
// the report reception path.
func BenchmarkSignal(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	warm(b, e, s.ingest)
	warm(b, e, s.leaseRetain)
	ops := *flagOps

	// A report event of the size the stream really carries (~440 B per entry,
	// documents/en/17-performance.md "Redis sizing").
	payload := `{"lease":"lse_perf0_1_00","report":"rpt_x","outcome":"success","ts":` + i64(ds.epoch) +
		`,"status":200,"bytes":21544,"latency_ms":318,"url":"https://example.invalid/api/g0/items?page=3&cursor=` +
		strings.Repeat("a", 200) + `"}`
	for _, batch := range []int{1, 100} {
		b.Run("ingest_batch"+strconv.Itoa(batch), func(b *testing.B) {
			n := ops
			if batch > 1 {
				n = max(ops/batch, 20)
			}
			seq := 0
			out := measure(b, e, plan{name: "ingest.lua batch=" + strconv.Itoa(batch), ops: n,
				scriptCmds: []string{"evalsha"}, body: func(int) {
					keys := make([]string, 0, batch+1)
					args := make([]string, 0, batch+2)
					keys = append(keys, ds.keys.Stream(0))
					args = append(args, "3600000", "1000000")
					for j := 0; j < batch; j++ {
						seq++
						keys = append(keys, ds.keys.Dedup(0, "rpt_perf"+strconv.Itoa(batch)+"_"+strconv.Itoa(seq)))
						args = append(args, payload)
					}
					runScript(b, e, s.ingest, keys, args)
				}})
			out.report(b)
			out.reportNested(b)
			if batch > 1 {
				b.Logf("    ingest.lua per report: %.2f us server", out.serverPerOp()/float64(batch))
			}
		})
	}
	// Reclaim the stream this block created; the dedup markers expire on their
	// own and are removed with the rest of the prefix at exit.
	_ = e.client.Do(e.ctx, e.client.B().Del().Key(ds.keys.Stream(0)).Build()).Error()

	// lease_retain.lua is called once per distinct lease of a request; a report
	// batch of 100 usually touches close to 100 leases, which is why the script
	// takes them all in one call (spec §6.2).
	for _, batch := range []int{1, 100} {
		b.Run("lease_retain_batch"+strconv.Itoa(batch), func(b *testing.B) {
			ids, identities := makeLeases(b, e, ds, batch, 0, ds.epoch+60_000, "")
			defer ds.restore(b, e, ids, identities)
			keys := make([]string, batch)
			for i, id := range ids {
				keys[i] = ds.keys.Lease(ds.siteKey, id)
			}
			args := ds.leaseRetainArgs(ds.epoch, batch)
			n := ops
			if batch > 1 {
				n = max(ops/batch, 20)
			}
			out := measure(b, e, plan{name: "lease_retain.lua batch=" + strconv.Itoa(batch), ops: n,
				scriptCmds: []string{"evalsha"}, body: func(int) {
					runScript(b, e, s.leaseRetain, keys, args)
				}})
			out.report(b)
			out.reportNested(b)
			if batch > 1 {
				b.Logf("    lease_retain.lua per lease: %.2f us server", out.serverPerOp()/float64(batch))
			}
		})
	}
}

// BenchmarkApplyBreaker measures apply.lua (the disposition executor) and
// breaker_eval.lua (the per-group breaker evaluation).
func BenchmarkApplyBreaker(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	warm(b, e, s.apply)
	warm(b, e, s.breakerEval)
	ops := *flagOps

	// Both scripts write state that would change how acquire behaves (an
	// identity x endpoint cooldown, and a breaker that trips): they run against
	// an endpoint group no acquire block samples, and their state is dropped
	// afterwards, so benchmark order cannot move another block's numbers.
	eg := ds.egIdle()
	b.Run("apply_cooldown_ie", func(b *testing.B) {
		applySeq := int64(0)
		out := measure(b, e, plan{name: "apply.lua cooldown identity x endpoint", ops: ops,
			scriptCmds: []string{"evalsha"}, body: func(i int) {
				ik := int64(i%int(ds.identities)) + 1
				applySeq++
				runScript(b, e, s.apply, []string{ds.meta()},
					ds.applyCooldownArgs(ds.epoch, eg, ik, ds.epoch+300_000+applySeq))
			}})
		out.report(b)
		out.reportNested(b)
	})

	// breaker_eval sums 12 window buckets; seed them so the script does real work.
	seedWindows(b, e, ds, eg, 12)
	for _, mode := range []string{"read", "eval"} {
		b.Run("breaker_eval_"+mode, func(b *testing.B) {
			out := measure(b, e, plan{name: "breaker_eval.lua mode=" + mode, ops: ops,
				scriptCmds: []string{"evalsha"}, body: func(int) {
					runScript(b, e, s.breakerEval, []string{ds.meta()}, ds.breakerEvalArgs(ds.epoch, eg, mode))
				}})
			out.report(b)
			out.reportNested(b)
		})
	}
	dropBreakerState(b, e, ds, eg, 12)
}

// dropBreakerState removes everything BenchmarkApplyBreaker wrote: the recent
// cooldown log, the window buckets and HLLs, the breaker hash and the open-
// breaker set. A tripped breaker left behind makes every later acquire return
// BREAKER_OPEN in a quarter of the time, which silently invalidates it.
func dropBreakerState(b *testing.B, e *env, ds *dataset, eg int64, buckets int) {
	keys := []string{
		ds.keys.RecentCooldowns(ds.siteKey, eg),
		ds.keys.Breaker(ds.siteKey, eg),
		ds.keys.OpenBreakers(ds.siteKey),
		ds.keys.ActiveGroups(ds.siteKey),
	}
	cur := ds.epoch / 5000
	for j := 0; j <= buckets; j++ {
		keys = append(keys, ds.keys.Window(ds.siteKey, eg, cur-int64(j)),
			ds.keys.WindowHLL(ds.siteKey, eg, cur-int64(j)))
	}
	for _, k := range keys {
		if err := e.client.Do(e.ctx, e.client.B().Del().Key(k).Build()).Error(); err != nil {
			b.Logf("drop %s: %v", k, err)
		}
	}
}

// seedWindows writes n breaker window buckets and their captcha HLLs.
func seedWindows(b *testing.B, e *env, ds *dataset, eg int64, n int) {
	c := e.client
	cur := ds.epoch / 5000
	var cmds rueidis.Commands
	for j := 0; j < n; j++ {
		bucket := cur - int64(j)
		cmds = append(cmds, c.B().Hset().Key(ds.keys.Window(ds.siteKey, eg, bucket)).FieldValue().
			FieldValue("t", "120").FieldValue("s", "100").FieldValue("r", "20").Build())
		pf := c.B().Pfadd().Key(ds.keys.WindowHLL(ds.siteKey, eg, bucket)).Element()
		for k := 0; k < 8; k++ {
			pf = pf.Element(strconv.Itoa(j*8 + k))
		}
		cmds = append(cmds, pf.Build())
	}
	for _, res := range c.DoMulti(e.ctx, cmds...) {
		if err := res.Error(); err != nil {
			b.Fatalf("seedWindows: %v", err)
		}
	}
}
