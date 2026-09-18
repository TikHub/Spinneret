//go:build perf

package perf

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// loopIters is how many times a calibration script repeats its body inside one
// EVALSHA call. Large enough that the fixed EVALSHA cost is negligible, small
// enough to stay far below the 5 s busy-script limit.
const loopIters = 2000

// calibration holds the measured cost of one Redis primitive as the Lua
// interpreter pays for it: the redis.call() dispatch plus the command itself.
type calibration struct {
	name   string
	usec   float64 // per call, net of the empty-loop baseline
	perOp  float64 // raw, including the loop baseline
	source string
}

// calibrate measures one loop body. It returns the per-iteration cost with the
// empty-loop cost already subtracted.
func calibrate(b *testing.B, e *env, name, body string, baseline float64, prologue ...string) calibration {
	src := "local n = tonumber(ARGV[1])\nlocal base = sp_base()\nlocal now = tonumber(ARGV[2])\n" +
		joinLua(prologue...) +
		"for _i = 1, n do\n" + body + "\nend\nreturn 1"
	sc := storeredis.NewScript("calib:"+name, src)
	warm(b, e, sc)
	ds := sharedDS
	args := []string{strconv.Itoa(loopIters), i64(ds.epoch)}
	// 12 outer calls are plenty: each already averages loopIters iterations.
	s := measure(b, e, plan{name: name, ops: 12, scriptCmds: []string{"evalsha"}, body: func(int) {
		runScript(b, e, sc, []string{ds.meta()}, args)
	}})
	per := s.serverPerOp() / loopIters
	return calibration{name: name, usec: per - baseline, perOp: per, source: body}
}

// BenchmarkPrimitives calibrates every Redis primitive the hot-path scripts
// use, at the data sizes of the seeded site. The numbers are the ones to
// multiply the per-script command counts with; INFO commandstats truncates
// sub-microsecond durations to whole microseconds and under-reports the same
// calls by roughly 3x, so it cannot be used for this.
func BenchmarkPrimitives(b *testing.B) {
	e, ds := benchEnv(b)
	_ = scripts(b)

	hs := "base .. 'hs:' .. " + i64(ds.eg())
	rdy := "base .. 'rdy:' .. " + i64(ds.eg())
	idk := "base .. 'id:' .. _i"
	scratch := "base .. 'perfscratch'"
	scratch2 := "base .. 'perfscratch2'"
	scratch3 := "base .. 'perfscratch3'"

	ids32 := make([]string, 32)
	for j := range ids32 {
		ids32[j] = "'" + strconv.Itoa(j+1) + "'"
	}
	idList := strings.Join(ids32, ", ")

	leaseFields := []string{}
	for _, f := range []string{"i", "iid", "e", "p", "pid", "n", "tk", "ns", "sk", "a", "x", "cap", "ttl", "st", "pr", "mc", "ri", "ra", "rs", "rc"} {
		leaseFields = append(leaseFields, "'"+f+"', 'v"+f+"'")
	}

	baselineCal := calibrate(b, e, "empty loop body", "local _x = _i", 0)
	base := baselineCal.perOp

	cases := []struct{ name, body string }{
		{"HGET hs:<eg> <id> (packed 40 B)", "redis.call('HGET', " + hs + ", '1')"},
		{"HMGET id:<i> x15 fields", "redis.call('HMGET', " + idk + ", 'st','al','xl','scd','sru','acc','act','px','rbd','rbn','rg','iid','ty','pv','tv')"},
		{"HMGET hs:<eg> x32 members", "redis.call('HMGET', " + hs + ", " + idList + ")"},
		{"HMGET brk:<eg> x5 (missing key)", "redis.call('HMGET', base .. 'brk:" + i64(ds.eg()) + "', 'st','ou','man','hw','hc')"},
		{"ZRANGEBYSCORE rdy LIMIT 0 32", "redis.call('ZRANGEBYSCORE', " + rdy + ", '-inf', now, 'LIMIT', 0, 32)"},
		{"ZRANGEBYSCORE rdy LIMIT 0 1", "redis.call('ZRANGEBYSCORE', " + rdy + ", '-inf', now, 'LIMIT', 0, 1)"},
		{"ZADD rdy XX <score> <id>", "redis.call('ZADD', " + rdy + ", 'XX', now, '1')"},
		{"ZADD rdy XX GT CH", "redis.call('ZADD', " + rdy + ", 'XX', 'GT', 'CH', now, '1')"},
		{"ZSCORE rdy <id>", "redis.call('ZSCORE', " + rdy + ", '1')"},
		{"HSET ls:<id> x20 fields", "redis.call('HSET', base .. 'ls:perf', " + strings.Join(leaseFields, ", ") + ")"},
		{"HSET hs:<eg> <id> <packed>", "redis.call('HSET', " + hs + ", '1', '70.00|1|1|0|0|0|0|1')"},
		{"HSET id:<i> x2 fields", "redis.call('HSET', " + idk + ", 'lu', now, 'xl', now)"},
		{"HINCRBY id:<i> al 1", "redis.call('HINCRBY', " + idk + ", 'al', 1)"},
		{"SADD dirty x2", "redis.call('SADD', " + scratch + ", 'e1:' .. _i, 'g' .. _i)"},
		{"SET k v PX (dedup marker)", "redis.call('SET', " + scratch2 + ", '1', 'PX', 3600000)"},
		{"SET k v NX PX (dedup, exists)", "redis.call('SET', " + scratch2 + ", '1', 'NX', 'PX', 3600000)"},
		{"PEXPIRE", "redis.call('PEXPIRE', " + scratch2 + ", 3600000)"},
		{"GET (miss)", "redis.call('GET', " + scratch3 + ")"},
		{"PTTL", "redis.call('PTTL', " + scratch2 + ")"},
		{"XADD MAXLEN ~ 1e6 (440 B)", "redis.call('XADD', base .. 'perfstream', 'MAXLEN', '~', 1000000, '*', 'v', '1', 'd', string.rep('x', 400))"},
		{"string.format %.0f", "local _s = string.format('%.0f', now + _i)"},
		{"table alloc {} + 8 fields", "local _t = {a=1,b=2,c=3,d=4,e=5,f=6,g=7,h=8}"},
		{"closure creation (1 local fn)", "local _f = function(x) return x + 1 end"},
	}

	out := make([]calibration, 0, len(cases)+1)
	out = append(out, calibration{name: baselineCal.name, usec: base, perOp: base})
	for _, c := range cases {
		out = append(out, calibrate(b, e, c.name, c.body, base))
	}

	// Housekeeping: the calibration wrote a few scratch keys.
	for _, k := range []string{ds.base() + "perfscratch", ds.base() + "perfscratch2", ds.base() + "perfscratch3", ds.base() + "perfstream", ds.base() + "ls:perf"} {
		if err := e.client.Do(e.ctx, e.client.B().Del().Key(k).Build()).Error(); err != nil {
			b.Logf("cleanup %s: %v", k, err)
		}
	}
	ds.restore(b, e, nil, nil)

	sort.SliceStable(out[1:], func(i, j int) bool { return out[1:][i].usec > out[1:][j].usec })
	hdr := fmt.Sprintf("\n%-40s %10s %10s", "primitive (called from Lua)", "us/call", "us/call+loop")
	b.Log(hdr)
	fmt.Fprintln(os.Stderr, hdr)
	for _, c := range out {
		line := fmt.Sprintf("%-40s %10.3f %10.3f", c.name, c.usec, c.perOp)
		b.Log(line)
		fmt.Fprintln(os.Stderr, line)
	}
	primitiveCosts = make(map[string]float64, len(out))
	for _, c := range out {
		primitiveCosts[c.name] = c.usec
	}
}

// primitiveCosts is filled by BenchmarkPrimitives so later blocks can attribute
// their command counts. It stays empty when only a subset of the benchmarks run.
var primitiveCosts map[string]float64
