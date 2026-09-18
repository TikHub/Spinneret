//go:build perf

package perf

import (
	"fmt"
	"os"
	"testing"

	"github.com/redis/rueidis"
)

// BenchmarkBaseline isolates the fixed per-call cost of the script mechanism:
// the network round trip, the EVALSHA dispatch, and the creation of the 17
// chunk-level helper closures of common.lua, which Redis re-creates on every
// EVAL/EVALSHA call.
func BenchmarkBaseline(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps

	if err := e.client.Do(e.ctx, e.client.B().ScriptLoad().Script("return 0").Build()).Error(); err != nil {
		b.Fatalf("script load: %v", err)
	}
	warm(b, e, s.prelude)

	b.Run("ping", func(b *testing.B) {
		measure(b, e, plan{name: "PING (round trip only)", ops: ops, scriptCmds: []string{"ping"}, body: func(int) {
			e.do(b, e.client.B().Ping().Build())
		}}).report(b)
	})
	b.Run("evalsha_empty", func(b *testing.B) {
		measure(b, e, plan{name: "EVALSHA return 0 (no prelude)", ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			if err := s.empty.Exec(e.ctx, e.client, []string{ds.meta()}, nil).Error(); err != nil {
				b.Fatalf("empty: %v", err)
			}
		}}).report(b)
	})
	b.Run("evalsha_prelude", func(b *testing.B) {
		measure(b, e, plan{name: "EVALSHA common.lua + return 0", ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			runScript(b, e, s.prelude, []string{ds.meta()}, nil)
		}}).report(b)
	})
	b.Run("evalsha_prelude_argv", func(b *testing.B) {
		// The same prelude call carrying an ARGV of the size acquire.lua uses,
		// so the cost of shipping and decoding ~60 arguments is visible.
		args := ds.acquireArgs(defaultAcquireCfg(), ds.epoch, 0)
		measure(b, e, plan{name: fmt.Sprintf("EVALSHA prelude + %d ARGV", len(args)), ops: ops,
			scriptCmds: []string{"evalsha"}, body: func(int) {
				runScript(b, e, s.prelude, []string{ds.meta()}, args)
			}}).report(b)
	})
	b.Run("eval_source", func(b *testing.B) {
		// EVAL of an already-cached source: the same work plus shipping and
		// hashing the 40 KB script body, i.e. what a NOSCRIPT fallback costs.
		src := s.prelude.Source()
		measure(b, e, plan{name: "EVAL full source (cached, NOSCRIPT path)", ops: ops / 4,
			scriptCmds: []string{"eval"}, body: func(int) {
				if err := e.client.Do(e.ctx, e.client.B().Eval().Script(src).Numkeys(1).Key(ds.meta()).Build()).Error(); err != nil {
					b.Fatalf("eval: %v", err)
				}
			}}).report(b)
	})
}

// BenchmarkSlowlogOverhead quantifies what "slowlog-log-slower-than 0" adds to
// every measurement, so the numbers of a slowlog run can be corrected.
func BenchmarkSlowlogOverhead(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	warm(b, e, s.acquire)
	ops := *flagOps
	cfg := defaultAcquireCfg()
	argv := make([][]string, ops)
	for i := range argv {
		argv[i] = ds.acquireArgs(cfg, ds.epoch, i)
	}

	// Restore whatever the server had, whether or not this run enabled SLOWLOG.
	original := e.configGet(b, "slowlog-log-slower-than")
	b.Cleanup(func() { e.rawConfigSet(b, "slowlog-log-slower-than", original) })

	run := func(name, threshold string) sample {
		e.rawConfigSet(b, "slowlog-log-slower-than", threshold)
		saved := slowlogOn
		slowlogOn = false
		defer func() { slowlogOn = saved }()
		var leases []string
		var identities []int64
		out := measure(b, e, plan{name: name, ops: ops, scriptCmds: []string{"evalsha"},
			chunk: *flagChunk,
			maintain: func() {
				ds.restore(b, e, leases, identities)
				leases, identities = leases[:0], identities[:0]
			},
			body: func(i int) {
				_, lids, ids := leaseReply(b, runScript(b, e, s.acquire, []string{ds.meta()}, argv[i]))
				leases = append(leases, lids...)
				identities = append(identities, ids...)
			}})
		ds.restore(b, e, leases, identities)
		return out
	}
	off := run("acquire, slowlog off (10000us)", "10000")
	on := run("acquire, slowlog threshold 0", "0")
	off.report(b)
	on.report(b)
	line := fmt.Sprintf("SLOWLOG threshold 0 costs %+.1f us per acquire call (%.1f%%)",
		on.serverPerOp()-off.serverPerOp(), 100*(on.serverPerOp()-off.serverPerOp())/off.serverPerOp())
	b.Log(line)
	fmt.Fprintln(os.Stderr, line)
}

// leaseReply extracts the lease ids and identity hkeys of an acquire reply.
func leaseReply(b *testing.B, res rueidis.RedisResult) (status string, leaseIDs []string, identities []int64) {
	arr, err := res.ToArray()
	if err != nil {
		b.Fatalf("acquire reply: %v", err)
	}
	if len(arr) < 3 {
		b.Fatalf("acquire reply: %d elements", len(arr))
	}
	status, _ = arr[0].ToString()
	if status != "OK" {
		return status, nil, nil
	}
	n, _ := arr[2].AsInt64()
	for j := int64(0); j < n; j++ {
		start := 3 + int(j)*14
		if start+13 >= len(arr) {
			break
		}
		lid, _ := arr[start].ToString()
		ik, _ := arr[start+12].AsInt64()
		leaseIDs = append(leaseIDs, lid)
		identities = append(identities, ik)
	}
	return status, leaseIDs, identities
}
