//go:build perf

package perf

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	storeredis "github.com/TikHub/Spinneret/internal/store/redis"
)

// cutAt truncates src just before the first line that starts with anchor and
// appends tail. It fails the benchmark when the anchor is gone, which is what
// keeps this attribution honest after the script is edited.
func cutAt(tb testing.TB, src, anchor, tail string) string {
	tb.Helper()
	idx := strings.Index(src, anchor)
	if idx < 0 {
		tb.Fatalf("phase anchor %q not found in the script", anchor)
	}
	return src[:idx] + tail
}

// BenchmarkObservePhases attributes the interpreter time of observe.lua by
// truncating the body after each of its top-level blocks and returning there.
// Every phase runs the identical ARGV of a real success report, so the
// difference between two consecutive rows is the cost of the block that was
// added, with no modelling in between.
func BenchmarkObservePhases(b *testing.B) {
	e, ds := benchEnv(b)
	ops := *flagOps

	ids, identities := makeLeases(b, e, ds, 1, 0, ds.epoch+60_000, "")
	defer ds.restore(b, e, ids, identities)

	src := luaFile(b, "internal/worker/lua/observe.lua")
	const stop = "\nreturn {'OK'}\n"

	// Phases 8..12 cut inside the "if not suppressed …" block, so their tail
	// has to close it before returning.
	const stopIn = "\nend\nreturn {'OK'}\n"
	phases := []struct{ name, anchor, tail string }{
		{"0 prelude only", "", ""},
		{"1 + checkpoint (ckpt read/write, stream id)", "if #A < 28 then", stop},
		{"2 + ARGV decode", "local is_success = outcome ==", stop},
		{"3 + lease rc / quota", "-- Breaker window buckets and activity.", stop},
		{"4 + breaker window buckets", "-- Breaker state, suppression and probe", stop},
		{"5 + breaker state read", "-- Cross attribution (spec", stop},
		{"6 + cross attribution (disabled)", "local id_key = ob_base .. 'id:' .. ident", stop},
		{"7 + identity HMGET + locals", "if not suppressed and not late and ident", stop},
		{"8 + hs read, decay, ewma, write", "  -- Identity global score and streak.", stopIn},
		{"9 + identity global score/streak", "  -- Proxy x site health and streak.", stopIn},
		{"10 + proxy block (no proxy here)", "  -- Sliding-window outcome counters.", stopIn},
		{"11 + counter loop (none here)", "  -- Ban history per escalation window.", stopIn},
		{"12 + ban loop (none here)", "return {\n  'OK',", stop},
		{"13 + reply table (full)", "", ""},
	}

	rows := make([]sample, len(phases))
	seq := 0
	for i, ph := range phases {
		var body string
		switch {
		case i == 0:
			body = "return {'OK'}"
		case i == len(phases)-1:
			body = src
		default:
			body = cutAt(b, src, ph.anchor, ph.tail)
		}
		sc := storeredis.NewScript("observe phase "+strconv.Itoa(i), body)
		warm(b, e, sc)
		rows[i] = measure(b, e, plan{name: ph.name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			seq++
			args := ds.observeArgs(ds.epoch, 0, i64(ds.epoch)+"-"+strconv.Itoa(seq), ids[0], identities[0],
				observeOpts{outcome: "success", blame: "none"})
			res := runScript(b, e, sc, []string{ds.meta()}, args)
			arr, err := res.ToArray()
			if err != nil || len(arr) == 0 {
				b.Fatalf("phase %d reply: %v", i, err)
			}
			if st, _ := arr[0].ToString(); st != "OK" {
				b.Fatalf("phase %d returned %q, expected OK", i, st)
			}
		}})
	}

	total := rows[len(rows)-1].serverPerOp()
	logBoth(b, fmt.Sprintf("\n%-46s %9s %9s %8s", "observe.lua phase (success, no counters)", "us/call", "delta", "share"))
	prev := 0.0
	for i, ph := range phases {
		d := rows[i].serverPerOp() - prev
		prev = rows[i].serverPerOp()
		logBoth(b, fmt.Sprintf("%-46s %9.1f %9.1f %7.1f%%", ph.name, rows[i].serverPerOp(), d, 100*d/total))
	}
	for _, k := range []string{ds.keys.Checkpoint(ds.siteKey), ds.keys.ActiveGroups(ds.siteKey)} {
		_ = e.client.Do(e.ctx, e.client.B().Del().Key(k).Build()).Error()
	}
}

// BenchmarkApplyPhases does the same for apply.lua: the chunk-level closures
// and constant tables are created on every call, while exactly one handler
// runs.
func BenchmarkApplyPhases(b *testing.B) {
	e, ds := benchEnv(b)
	ops := *flagOps
	eg := ds.egIdle()

	src := luaFile(b, "internal/action/lua/apply.lua")
	const stop = "\nreturn {}\n"

	phases := []struct{ name, anchor string }{
		{"0 prelude only", ""},
		{"1 + header (base, now, replay GET)", "local BAN_HISTORY_MS"},
		{"2 + 9 shared helper closures", "local function ap_cooldown(op)"},
		{"3 + 5 handler closures", "local STATES = {"},
		{"4 + STATES table", "local function ap_set(op)"},
		{"5 + 3 handler closures", "local HANDLERS = {"},
		{"6 + HANDLERS table", "local out = {}"},
		{"7 + one cooldown op (full)", ""},
	}
	rows := make([]sample, len(phases))
	applySeq := int64(0)
	for i, ph := range phases {
		var body string
		switch {
		case i == 0:
			body = "return {}"
		case i == len(phases)-1:
			body = src
		default:
			body = cutAt(b, src, ph.anchor, stop)
		}
		sc := storeredis.NewScript("apply phase "+strconv.Itoa(i), body)
		warm(b, e, sc)
		rows[i] = measure(b, e, plan{name: ph.name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(n int) {
			ik := int64(n%int(ds.identities)) + 1
			applySeq++
			runScript(b, e, sc, []string{ds.meta()}, ds.applyCooldownArgs(ds.epoch, eg, ik, ds.epoch+300_000+applySeq))
		}})
	}
	total := rows[len(rows)-1].serverPerOp()
	logBoth(b, fmt.Sprintf("\n%-46s %9s %9s %8s", "apply.lua phase (one cooldown ie op)", "us/call", "delta", "share"))
	prev := 0.0
	for i, ph := range phases {
		d := rows[i].serverPerOp() - prev
		prev = rows[i].serverPerOp()
		logBoth(b, fmt.Sprintf("%-46s %9.1f %9.1f %7.1f%%", ph.name, rows[i].serverPerOp(), d, 100*d/total))
	}
	dropBreakerState(b, e, ds, eg, 0)
}

// BenchmarkBreakerParts splits breaker_eval.lua's window sum from its PFCOUNT
// over the captcha HyperLogLogs, which is the part a disabled captcha trip
// condition does not need.
func BenchmarkBreakerParts(b *testing.B) {
	e, ds := benchEnv(b)
	ops := *flagOps
	eg := ds.egIdle()
	seedWindows(b, e, ds, eg, 12)

	base := ds.base()
	cur := ds.epoch / 5000
	var hmgets, pfkeys strings.Builder
	for j := 0; j < 12; j++ {
		bucket := cur - int64(11-j)
		hmgets.WriteString(fmt.Sprintf("redis.call('HMGET', '%swin:%d:%d', 't', 's', 'r')\n", base, eg, bucket))
		pfkeys.WriteString(fmt.Sprintf(", '%swinh:%d:%d'", base, eg, bucket))
	}
	cases := []struct{ name, body string }{
		{"12 x HMGET win (t,s,r)", hmgets.String() + "return 0"},
		{"PFCOUNT over 12 winh HLLs", "redis.call('PFCOUNT'" + pfkeys.String() + ")\nreturn 0"},
		{"both", hmgets.String() + "redis.call('PFCOUNT'" + pfkeys.String() + ")\nreturn 0"},
	}
	for _, c := range cases {
		sc := storeredis.NewScript(c.name, c.body)
		warm(b, e, sc)
		out := measure(b, e, plan{name: c.name, ops: ops, scriptCmds: []string{"evalsha"}, body: func(int) {
			runScript(b, e, sc, []string{ds.meta()}, nil)
		}})
		out.report(b)
	}
	dropBreakerState(b, e, ds, eg, 12)
}
