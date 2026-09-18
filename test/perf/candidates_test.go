//go:build perf

package perf

import (
	"fmt"
	"testing"
)

// BenchmarkOptimizationCandidates measures a rewritten version of each
// expensive helper next to the one in common.lua, on identical input. The pairs
// are what the optimization track can act on: every row is "current" against
// "candidate", and the difference is the saving per call of that helper.
//
// No production file is changed; the candidates live only in this benchmark.
func BenchmarkOptimizationCandidates(b *testing.B) {
	e, _ := benchEnv(b)
	_ = scripts(b)

	const packed = "'82.50|1758011411962|37|0|0|0|0|1758011351962'"
	const ts = "'1758011411962'"

	base := calibrate(b, e, "empty loop body", "local _x = _i", 0).perOp

	// Hoisted standard-library references. Lua 5.1 resolves math.huge as two
	// table lookups (the global "math", then the field) on every evaluation;
	// a chunk-level local resolves to a register.
	const hoist = "local _huge, _floor, _fmt, _tonum, _find, _sub = math.huge, math.floor, string.format, tonumber, string.find, string.sub\n"

	const spNumLocal = hoist + `
local function _sp_num(v, default)
  if v == nil or v == false or v == '' then return default end
  local n = _tonum(v)
  if n == nil or n ~= n or n == _huge or n == -_huge then return default end
  return n
end
`
	const spIntStrLocal = hoist + `
local function _sp_int_str(x)
  local n = _tonum(x)
  if n == nil or n ~= n or n == _huge or n == -_huge then return '0' end
  local s = _fmt('%.0f', _floor(n))
  if s == '-0' then return '0' end
  return s
end
`
	// A codec that decodes the packed hs entry in one pass with gmatch-free
	// index arithmetic and no intermediate array, and that encodes without a
	// per-field function call.
	const hsFast = hoist + `
local function _hs_unpack(s, baseline, now)
  if s == nil or s == false or s == '' then
    return {score = baseline, sts = now, samples = 0, nfail = 0, lastfail = 0, cd = 0, ru = 0, lu = 0}
  end
  local p, out, n = 1, {}, 0
  for _f = 1, 8 do
    local i = _find(s, '|', p, true)
    n = n + 1
    if i then out[n] = _tonum(_sub(s, p, i - 1)) p = i + 1 else out[n] = _tonum(_sub(s, p)) break end
  end
  return {score = out[1] or baseline, sts = out[2] or now, samples = out[3] or 0, nfail = out[4] or 0,
          lastfail = out[5] or 0, cd = out[6] or 0, ru = out[7] or 0, lu = out[8] or 0}
end
local function _hs_pack(t)
  return _fmt('%.2f|%.0f|%.0f|%.0f|%.0f|%.0f|%.0f|%.0f', t.score, t.sts, t.samples, t.nfail,
              t.lastfail, t.cd, t.ru, t.lu)
end
`

	pairs := []struct {
		label, current, candidate, prologue string
	}{
		{
			label:     "sp_num(string)",
			current:   "local _v = sp_num(" + ts + ", 0)",
			candidate: "local _v = _sp_num(" + ts + ", 0)",
			prologue:  spNumLocal,
		},
		{
			label:     "tonumber(string) (floor of sp_num)",
			current:   "local _v = sp_num(" + ts + ", 0)",
			candidate: "local _v = tonumber(" + ts + ")",
		},
		{
			label:     "sp_int_str(ms)",
			current:   "local _v = sp_int_str(" + ts + " + 0)",
			candidate: "local _v = _sp_int_str(" + ts + " + 0)",
			prologue:  spIntStrLocal,
		},
		{
			label:     "sp_hs_unpack(packed)",
			current:   "local _v = sp_hs_unpack(" + packed + ", 70, now)",
			candidate: "local _v = _hs_unpack(" + packed + ", 70, now)",
			prologue:  hsFast,
		},
		{
			label:     "sp_hs_pack(table)",
			current:   "local _v = sp_hs_pack({score=82.5,sts=now,samples=37,nfail=0,lastfail=0,cd=0,ru=0,lu=now})",
			candidate: "local _v = _hs_pack({score=82.5,sts=now,samples=37,nfail=0,lastfail=0,cd=0,ru=0,lu=now})",
			prologue:  hsFast,
		},
		{
			label:     "unpack+HSET vs fixed-arity HSET (2 fields)",
			current:   "local _a = {'lu', '1', 'xl', '2'} redis.call('HSET', base .. 'perfscratch4', unpack(_a))",
			candidate: "redis.call('HSET', base .. 'perfscratch4', 'lu', '1', 'xl', '2')",
		},
	}

	logBoth(b, fmt.Sprintf("\n%-44s %9s %9s %9s", "helper", "current", "candidate", "saved"))
	for _, p := range pairs {
		cur := calibrateWithPrologue(b, e, p.label+" [current]", p.prologue, p.current, base)
		cand := calibrateWithPrologue(b, e, p.label+" [candidate]", p.prologue, p.candidate, base)
		logBoth(b, fmt.Sprintf("%-44s %9.3f %9.3f %9.3f", p.label, cur.usec, cand.usec, cur.usec-cand.usec))
	}
	_ = e.client.Do(e.ctx, e.client.B().Del().Key(sharedDS.base()+"perfscratch4").Build()).Error()
}

// calibrateWithPrologue is calibrate with extra chunk-level code in front of
// the loop (function definitions the loop body calls).
func calibrateWithPrologue(b *testing.B, e *env, name, prologue, body string, baseline float64) calibration {
	return calibrate(b, e, name, body, baseline, prologue)
}

// BenchmarkNumberParsing characterises string -> number conversion, which the
// helper calibration singles out as the dominant interpreter cost: every ARGV
// field, every packed hs field and every numeric hash field passes through it.
func BenchmarkNumberParsing(b *testing.B) {
	e, _ := benchEnv(b)
	_ = scripts(b)
	base := calibrate(b, e, "empty loop body", "local _x = _i", 0).perOp

	cases := []struct{ name, body string }{
		{"tonumber('0')", "local _v = tonumber('0')"},
		{"tonumber('70')", "local _v = tonumber('70')"},
		{"tonumber('60000')", "local _v = tonumber('60000')"},
		{"tonumber('82.50')", "local _v = tonumber('82.50')"},
		{"tonumber('1758011411962') 13 digits", "local _v = tonumber('1758011411962')"},
		{"tonumber('0.618033') float", "local _v = tonumber('0.618033')"},
		{"tonumber(number) no-op", "local _v = tonumber(_i)"},
		{"arithmetic on string coercion '1'+0", "local _v = '1758011411962' + 0"},
		{"string.byte digit decode (13 digits)",
			"local _s = '1758011411962' local _n = 0 for _k = 1, 13 do _n = _n * 10 + (string.byte(_s, _k) - 48) end"},
		{"cjson.decode 8-field object",
			"local _v = cjson.decode('{\"score\":82.5,\"sts\":1758011411962,\"samples\":37,\"nfail\":0,\"lastfail\":0,\"cd\":0,\"ru\":0,\"lu\":1758011351962}')"},
		{"cjson.encode 8-field object",
			"local _v = cjson.encode({score=82.5,sts=1758011411962,samples=37,nfail=0,lastfail=0,cd=0,ru=0,lu=1758011351962})"},
		{"struct.unpack 8 doubles", "local _ok, _v = pcall(function() return struct.unpack('>dddddddd', string.rep('\\0', 64)) end)"},
		{"20 x tonumber of ARGV-shaped values",
			"local _t = 0 for _k = 1, 20 do _t = _t + tonumber('1758011411962') end"},
	}
	out := make([]calibration, 0, len(cases))
	for _, c := range cases {
		out = append(out, calibrate(b, e, c.name, c.body, base))
	}
	logBoth(b, fmt.Sprintf("\n%-44s %10s", "number conversion", "us/call"))
	for _, c := range out {
		logBoth(b, fmt.Sprintf("%-44s %10.3f", c.name, c.usec))
	}
}
