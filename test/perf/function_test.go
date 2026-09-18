//go:build perf

package perf

import (
	"fmt"
	"os"
	"testing"
)

// functionLibrary is the name of the throwaway Valkey FUNCTION library the
// prototype loads. It is deleted when the benchmark finishes.
const functionLibrary = "spnrperf"

// buildLibrary wraps the unmodified production sources in a FUNCTION library.
//
// The only transformation is the two library-level locals KEYS and ARGV: in an
// EVAL script Redis injects them as globals on every call, in a function
// library they become upvalues the entry point assigns once per call. Every
// sp_* helper of common.lua and every local function of acquire.lua therefore
// closes over them unchanged, and — this is the point of the experiment — all
// of those closures are created once at FUNCTION LOAD instead of once per call.
func buildLibrary(s *scriptSet) string {
	return "#!lua name=" + functionLibrary + "\n" +
		"local KEYS, ARGV\n" +
		s.commonSrc + "\n" +
		"local function acquire_body()\n" + s.acquireSrc + "\nend\n" +
		"redis.register_function('spnr_noop', function(keys, args) KEYS = keys ARGV = args return 0 end)\n" +
		"redis.register_function('spnr_acquire', function(keys, args) KEYS = keys ARGV = args return acquire_body() end)\n"
}

// BenchmarkFunction compares EVALSHA with FCALL on exactly the same Lua code.
// The difference is the per-call script-environment setup that a function
// library pays only once, at FUNCTION LOAD.
func BenchmarkFunction(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps

	lib := buildLibrary(s)
	if err := e.client.Do(e.ctx, e.client.B().FunctionLoad().Replace().FunctionCode(lib).Build()).Error(); err != nil {
		b.Fatalf("FUNCTION LOAD: %v (library %d bytes)", err, len(lib))
	}
	b.Cleanup(func() {
		if err := e.client.Do(e.ctx, e.client.B().FunctionDelete().LibraryName(functionLibrary).Build()).Error(); err != nil {
			b.Logf("FUNCTION DELETE: %v", err)
		}
	})
	b.Logf("FUNCTION library %s loaded, %d bytes", functionLibrary, len(lib))

	warm(b, e, s.prelude)
	warm(b, e, s.acquire)

	// 1. Environment setup only.
	noop := measure(b, e, plan{name: "FCALL spnr_noop (prelude as upvalues)", ops: ops,
		scriptCmds: []string{"fcall"}, body: func(int) {
			if err := e.client.Do(e.ctx, e.client.B().Fcall().Function("spnr_noop").Numkeys(1).Key(ds.meta()).Build()).Error(); err != nil {
				b.Fatalf("fcall noop: %v", err)
			}
		}})
	noop.report(b)

	preludeEval := measure(b, e, plan{name: "EVALSHA common.lua + return 0", ops: ops,
		scriptCmds: []string{"evalsha"}, body: func(int) {
			runScript(b, e, s.prelude, []string{ds.meta()}, nil)
		}})
	preludeEval.report(b)

	// 2. The same acquire work, both ways.
	cfg := defaultAcquireCfg()
	argv := make([][]string, ops)
	for i := range argv {
		argv[i] = ds.acquireArgs(cfg, ds.epoch, i)
	}
	var leases []string
	var identities []int64

	fcallAcq := measure(b, e, plan{
		name: "FCALL spnr_acquire K=32", ops: ops, scriptCmds: []string{"fcall"}, chunk: *flagChunk,
		maintain: func() {
			ds.restore(b, e, leases, identities)
			leases, identities = leases[:0], identities[:0]
		},
		body: func(i int) {
			res := e.client.Do(e.ctx, e.client.B().Fcall().Function("spnr_acquire").
				Numkeys(1).Key(ds.meta()).Arg(argv[i]...).Build())
			if err := res.Error(); err != nil {
				b.Fatalf("fcall acquire: %v", err)
			}
			status, lids, ids := leaseReply(b, res)
			if status == "OK" {
				leases = append(leases, lids...)
				identities = append(identities, ids...)
			}
		},
	})
	ds.restore(b, e, leases, identities)
	leases, identities = leases[:0], identities[:0]
	fcallAcq.report(b)

	evalAcq := measure(b, e, plan{
		name: "EVALSHA acquire K=32", ops: ops, scriptCmds: []string{"evalsha"}, chunk: *flagChunk,
		maintain: func() {
			ds.restore(b, e, leases, identities)
			leases, identities = leases[:0], identities[:0]
		},
		body: func(i int) {
			status, lids, ids := leaseReply(b, runScript(b, e, s.acquire, []string{ds.meta()}, argv[i]))
			if status == "OK" {
				leases = append(leases, lids...)
				identities = append(identities, ids...)
			}
		},
	})
	ds.restore(b, e, leases, identities)
	evalAcq.report(b)

	for _, line := range []string{
		fmt.Sprintf("script-environment setup per call: EVALSHA prelude %.1f us vs FCALL noop %.1f us -> %+.1f us",
			preludeEval.serverPerOp(), noop.serverPerOp(), noop.serverPerOp()-preludeEval.serverPerOp()),
		fmt.Sprintf("acquire per call:                  EVALSHA %.1f us vs FCALL %.1f us -> %+.1f us (%.1f%%)",
			evalAcq.serverPerOp(), fcallAcq.serverPerOp(), fcallAcq.serverPerOp()-evalAcq.serverPerOp(),
			100*(fcallAcq.serverPerOp()-evalAcq.serverPerOp())/evalAcq.serverPerOp()),
	} {
		b.Log(line)
		fmt.Fprintln(os.Stderr, line)
	}
}
