//go:build perf

package perf

import (
	"fmt"
	"os"
	"strings"
	"testing"

	storeredis "github.com/TikHub/Spinneret/internal/store/redis"
)

// BenchmarkAcquirePhases attributes the interpreter time of acquire.lua by
// truncating the assembled script after each of its parts and returning an
// empty OK reply there. Each phase runs the identical ARGV of a real call, so
// the difference between two consecutive rows is the cost of the part that was
// added, with no modelling in between.
//
// The parts are, in assembly order:
//
//	0 prelude only            common.lua, 17 helper closures
//	1 + acquire.lua           ARGV decoding, the C table, next_arg/read_set/rnd
//	2 + acquire_filters.lua   4 more local closures (not called)
//	3 + acquire_proxy.lua     proxy closures (not called)
//	4 + acquire_write.lua     quota_write / write_lease closures (not called)
//	5 + acquire_select.lua    pool and strategy closures (not called)
//	6 + acquire_main.lua      the real work: sample, filter, select, write
func BenchmarkAcquirePhases(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps
	cfg := defaultAcquireCfg()

	// A tail that satisfies the "return a status array" contract without doing
	// any work, so a truncated script is still a valid acquire reply.
	const stop = "\nreturn {'OK', '0', '0'}\n"

	names := []string{
		"0 prelude only (common.lua)",
		"1 + acquire.lua      (ARGV decode)",
		"2 + acquire_filters  (closures)",
		"3 + acquire_proxy    (closures)",
		"4 + acquire_write    (closures)",
		"5 + acquire_select   (closures)",
		"6 + acquire_main     (the real work)",
	}

	rows := make([]sample, 0, len(names))
	argv := make([][]string, ops)
	for i := range argv {
		argv[i] = ds.acquireArgs(cfg, ds.epoch, i)
	}

	for phase := 0; phase < len(names); phase++ {
		var src string
		if phase == 0 {
			src = "return {'OK', '0', '0'}"
		} else if phase == len(names)-1 {
			src = s.acquireSrc
		} else {
			src = joinLua(s.acquireParts[:phase]...) + stop
		}
		sc := storeredis.NewScript(names[phase], src)
		warm(b, e, sc)

		var leases []string
		var identities []int64
		out := measure(b, e, plan{
			name: names[phase], ops: ops, scriptCmds: []string{"evalsha"}, chunk: *flagChunk,
			maintain: func() {
				ds.restore(b, e, leases, identities)
				leases, identities = leases[:0], identities[:0]
			},
			body: func(i int) {
				res := runScript(b, e, sc, []string{ds.meta()}, argv[i])
				if phase == len(names)-1 {
					status, lids, ids := leaseReply(b, res)
					if status != "OK" {
						// A drained pool or a breaker another block left open
						// would make the last phase look cheap; fail loudly.
						b.Fatalf("phase %d returned %q instead of OK", phase, status)
					}
					leases = append(leases, lids...)
					identities = append(identities, ids...)
				}
			},
		})
		ds.restore(b, e, leases, identities)
		rows = append(rows, out)
	}

	hdr := fmt.Sprintf("\n%-40s %10s %10s %10s", "acquire.lua phase (K=32, clean pool)", "us/call", "delta", "share")
	logBoth(b, hdr)
	total := rows[len(rows)-1].serverPerOp()
	prev := 0.0
	for i, r := range rows {
		d := r.serverPerOp() - prev
		prev = r.serverPerOp()
		logBoth(b, fmt.Sprintf("%-40s %10.1f %10.1f %9.1f%%", names[i], r.serverPerOp(), d, 100*d/total))
	}
	logBoth(b, fmt.Sprintf("%-40s %10.1f", "EVALSHA dispatch (return 0, no prelude)", 0.0))
}

// BenchmarkAcquireArgv isolates the cost of the ARGV vector itself: the same
// decoding work with a shrinking argument list. It answers how much of the
// per-call cost Go could remove by shipping fewer, pre-packed arguments.
func BenchmarkAcquireArgv(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps
	cfg := defaultAcquireCfg()

	full := ds.acquireArgs(cfg, ds.epoch, 0)
	// The head of acquire.lua up to (but not including) the key derivation:
	// everything that only decodes ARGV.
	head := s.acquireParts[0]
	cut := strings.Index(head, "local rdy = base ..")
	if cut < 0 {
		b.Fatal("acquire.lua: cannot find the end of the argument-decoding section")
	}
	decode := storeredis.NewScript("acquire argv decode", head[:cut]+"\nreturn {'OK', sp_int_str(pos), sp_int_str(rand_n)}\n")
	warm(b, e, decode)

	rows := []struct {
		name string
		args []string
	}{
		{fmt.Sprintf("decode %d ARGV (production shape)", len(full)), full},
		{fmt.Sprintf("decode %d ARGV (20 randoms dropped)", len(full)-20), full[:len(full)-20]},
	}
	for _, r := range rows {
		out := measure(b, e, plan{name: r.name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			runScript(b, e, decode, []string{ds.meta()}, r.args)
		}})
		out.report(b)
	}
}

// logBoth writes one line to the benchmark log and to stderr, where it stays
// visible without -v.
func logBoth(tb testing.TB, line string) {
	tb.Helper()
	tb.Log(line)
	fmt.Fprintln(os.Stderr, line)
}
