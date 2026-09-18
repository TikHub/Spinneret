//go:build perf

package perf

import (
	"fmt"
	"testing"

	storeredis "github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// acquireFloorLua issues exactly the twelve Redis commands that one successful
// acquire.lua call issues on the common path (no proxy, no quota, no sticky
// session, closed breaker, first candidate accepted), with the same keys and
// the same payload sizes, and none of the decision logic. It is not a correct
// scheduler: it is the hard lower bound of any Lua implementation of acquire on
// this key schema, so the difference to acquire.lua is the part that is
// interpreter work rather than data work.
//
// ARGV: 1 now ms, 2 eg hkey, 3 lease id, 4 ttl ms, 5 node, 6 token, 7 ns.
const acquireFloorLua = `
local base = sp_base()
local now = tonumber(ARGV[1])
local eg = ARGV[2]
local lid = ARGV[3]
local ttl = tonumber(ARGV[4])
local rdy = base .. 'rdy:' .. eg
local hskey = base .. 'hs:' .. eg

redis.call('HMGET', base .. 'brk:' .. eg, 'st', 'ou', 'man', 'hw', 'hc')
local ids = redis.call('ZRANGEBYSCORE', rdy, '-inf', now, 'LIMIT', 0, 32)
if #ids == 0 then
  return {'EXHAUSTED', '0', '50'}
end
redis.call('HMGET', hskey, unpack(ids))
local i = ids[1]
local idkey = base .. 'id:' .. i
local v = redis.call('HMGET', idkey, 'st', 'al', 'xl', 'scd', 'sru', 'acc', 'act', 'px', 'rbd', 'rbn', 'rg',
  'iid', 'ty', 'pv', 'tv')
local x = sp_int_str(now + ttl)
redis.call('HSET', base .. 'ls:' .. lid,
  'i', i, 'iid', v[12] or '', 'e', eg, 'p', '', 'pid', '', 'n', ARGV[5], 'tk', ARGV[6],
  'ns', ARGV[7], 'sk', '', 'a', sp_int_str(now), 'x', x, 'cap', sp_int_str(now + 1800000),
  'ttl', sp_int_str(ttl), 'st', 'active', 'pr', '0', 'mc', '1', 'ri', '0',
  'ra', 'r', 'rs', 'e', 'rc', '0')
redis.call('ZADD', base .. 'lsexp', x, lid)
redis.call('HINCRBY', idkey, 'al', 1)
redis.call('HSET', idkey, 'lu', sp_int_str(now), 'xl', x)
redis.call('HSET', hskey, i, '70.00|' .. sp_int_str(now) .. '|1|0|0|0|0|' .. sp_int_str(now))
redis.call('ZADD', rdy, 'XX', sp_int_str(now + 5000), i)
redis.call('SADD', base .. 'dirty', 'e' .. eg .. ':' .. i, 'g' .. i)
redis.call('ZADD', base .. 'aeg', sp_int_str(now), eg)
return {'OK', '0', '1', lid, v[12] or '', v[13] or '', v[14] or '0', v[15] or '0', x, '0', '0', '0', '', '',
  v[1] or '', i, '0'}
`

// BenchmarkAcquireFloor measures the lower bound above and prints the gap to the
// production script, which is the budget the optimization track can work in.
func BenchmarkAcquireFloor(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps

	floor := storeredis.NewScript("acquire_floor", acquireFloorLua)
	warm(b, e, floor)
	warm(b, e, s.acquire)

	var leases []string
	var identities []int64
	floorArgs := make([][]string, ops)
	for i := range floorArgs {
		floorArgs[i] = []string{
			i64(ds.epoch), i64(ds.eg()), fmt.Sprintf("lse_floor%d_1_00", i),
			"60000", "perf-node", "tok_perf", ds.ns,
		}
	}
	fl := measure(b, e, plan{
		name: "acquire FLOOR: same 12 commands, no logic", ops: ops, scriptCmds: []string{"evalsha"},
		chunk: *flagChunk,
		maintain: func() {
			ds.restore(b, e, leases, identities)
			leases, identities = leases[:0], identities[:0]
		},
		body: func(i int) {
			_, lids, ids := leaseReply(b, runScript(b, e, floor, []string{ds.meta()}, floorArgs[i]))
			leases = append(leases, lids...)
			identities = append(identities, ids...)
		},
	})
	ds.restore(b, e, leases, identities)
	leases, identities = leases[:0], identities[:0]
	fl.report(b)
	fl.reportNested(b)

	cfg := defaultAcquireCfg()
	real := acquireRun(b, e, ds, "acquire.lua K=32 (same conditions)", cfg, ops)
	real.report(b)

	logBoth(b, fmt.Sprintf("acquire.lua %.1f us vs floor %.1f us: %.1f us (%.0f%%) of the call is interpreter work above the data work",
		real.serverPerOp(), fl.serverPerOp(), real.serverPerOp()-fl.serverPerOp(),
		100*(real.serverPerOp()-fl.serverPerOp())/real.serverPerOp()))
}

// BenchmarkAcquireLocalGlobals tests whether the saving the FUNCTION prototype
// shows can be had without FUNCTION. In an EVAL script KEYS and ARGV are
// globals, so every one of the ~80 ARGV reads of acquire.lua is a hash lookup in
// the globals table; a function library turns them into upvalues. Binding them
// (and the standard-library functions the prelude calls most) to chunk-level
// locals in front of the prelude has the same effect and changes no behaviour.
func BenchmarkAcquireLocalGlobals(b *testing.B) {
	e, ds := benchEnv(b)
	s := scripts(b)
	ops := *flagOps
	cfg := defaultAcquireCfg()

	variants := []struct{ name, prologue string }{
		{"acquire.lua as shipped", ""},
		{"+ local KEYS, ARGV", "local KEYS, ARGV = KEYS, ARGV\n"},
		{"+ local KEYS, ARGV, stdlib", "local KEYS, ARGV = KEYS, ARGV\n" +
			"local tonumber, tostring, type, ipairs, pairs, unpack, error = tonumber, tostring, type, ipairs, pairs, unpack, error\n" +
			"local math, string, redis = math, string, redis\n"},
	}
	scs := make([]*storeredis.Script, len(variants))
	for i, v := range variants {
		if v.prologue == "" {
			scs[i] = s.acquire
		} else {
			scs[i] = storeredis.NewScript("acquire["+v.name+"]", v.prologue+s.commonSrc+"\n"+s.acquireSrc)
		}
		warm(b, e, scs[i])
	}
	argv := make([][]string, ops)
	for i := range argv {
		argv[i] = ds.acquireArgs(cfg, ds.epoch, i)
	}

	// Run the variants interleaved, three rounds each, and keep the best round
	// of every variant: run-to-run drift on this machine is +-6 %, which is
	// larger than the effect being looked for.
	const rounds = 3
	best := make([]float64, len(variants))
	for r := 0; r < rounds; r++ {
		for i := range variants {
			sc := scs[i]
			var leases []string
			var identities []int64
			out := measure(b, e, plan{
				name: variants[i].name, ops: ops, scriptCmds: []string{"evalsha"}, chunk: *flagChunk,
				maintain: func() {
					ds.restore(b, e, leases, identities)
					leases, identities = leases[:0], identities[:0]
				},
				body: func(j int) {
					_, lids, ids := leaseReply(b, runScript(b, e, sc, []string{ds.meta()}, argv[j]))
					leases = append(leases, lids...)
					identities = append(identities, ids...)
				},
			})
			ds.restore(b, e, leases, identities)
			if r == 0 || out.serverPerOp() < best[i] {
				best[i] = out.serverPerOp()
			}
		}
	}
	logBoth(b, fmt.Sprintf("\n%-34s %10s %12s   (best of %d interleaved rounds, %d ops each)",
		"acquire variant", "us/call", "vs shipped", rounds, ops))
	for i := range variants {
		logBoth(b, fmt.Sprintf("%-34s %10.1f %+12.1f", variants[i].name, best[i], best[i]-best[0]))
	}
}
