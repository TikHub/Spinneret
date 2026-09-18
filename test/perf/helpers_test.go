//go:build perf

package perf

import (
	"fmt"
	"sort"
	"testing"
)

// BenchmarkLuaHelpers calibrates the pure-Lua helpers the hot path calls per
// candidate and per lease. They perform no Redis command at all, so whatever
// they cost is interpreter time that shows up in the "lua" column of the script
// measurements.
func BenchmarkLuaHelpers(b *testing.B) {
	e, _ := benchEnv(b)
	_ = scripts(b)

	const packed = "'82.50|1758011411962|37|0|0|0|0|1758011351962'"
	const ts = "1758011411962"

	base := calibrate(b, e, "empty loop body", "local _x = _i", 0).perOp

	cases := []struct{ name, body string }{
		{"sp_int_str(ms timestamp)", "local _v = sp_int_str(" + ts + " + _i)"},
		{"sp_score_str(ms timestamp)", "local _v = sp_score_str(" + ts + " + _i)"},
		{"sp_num(string)", "local _v = sp_num('" + ts + "', 0)"},
		{"sp_str(number)", "local _v = sp_str(_i)"},
		{"sp_split(packed hs, '|') -> 8", "local _v = sp_split(" + packed + ", '|')"},
		{"sp_hs_unpack(packed hs)", "local _v = sp_hs_unpack(" + packed + ", 70, now)"},
		{"sp_hs_pack(table)", "local _v = sp_hs_pack({score=82.5,sts=now,samples=37,nfail=0,lastfail=0,cd=0,ru=0,lu=now})"},
		{"sp_quota_est", "local _v = sp_quota_est(10, 5, now - 1000, 60000, now)"},
		{"hs_score() equivalent (find+exp)", "local _r = " + packed +
			"\nlocal _p1 = string.find(_r, '|', 1, true)\nlocal _sc = tonumber(string.sub(_r, 1, _p1 - 1))" +
			"\nlocal _p2 = string.find(_r, '|', _p1 + 1, true)\nlocal _sts = tonumber(string.sub(_r, _p1 + 1, _p2 - 1))" +
			"\n_sc = 70 + (_sc - 70) * math.exp((_sts - now) / 600000)"},
		{"hs_last_used() equivalent (7 finds)", "local _r = " + packed + "\nlocal _p = 0\nfor _k = 1, 7 do _p = string.find(_r, '|', _p + 1, true) end" +
			"\nlocal _v = tonumber(string.sub(_r, _p + 1))"},
		{"math.exp", "local _v = math.exp(-_i / 600000)"},
		{"key concat base..'id:'..i", "local _v = base .. 'id:' .. _i"},
		{"candidate table (20 fields)", "local _c = {i=1,n=1,st='a',hs=1,al=0,xl=0,scd=0,sru=0,acc='',act=0,px='',rbd='',rbn=0,rg='',acd=0,iid='',ty='',pv='0',tv='0',lim=1}"},
		{"pool arrays, 32 candidates", "local _P = {ids={},raws={},key={},c={},n=0}\nfor _k = 1, 32 do _P.n = _P.n + 1 _P.ids[_k] = _k _P.raws[_k] = " + packed + " _P.key[_k] = false _P.c[_k] = false end"},
		{"lease fields table (40 entries)", "local _f = {'i','1','iid','x','e','1','p','','pid','','n','n','tk','t','ns','n','sk','','a','1','x','1','cap','1','ttl','1','st','active','pr','0','mc','1','ri','0','ra','r','rs','e','rc','0'}"},
		{"unpack(40-entry table)", "local _f = {'i','1','iid','x','e','1','p','','pid','','n','n','tk','t','ns','n','sk','','a','1','x','1','cap','1','ttl','1','st','active','pr','0','mc','1','ri','0','ra','r','rs','e','rc','0'}\nlocal _a, _b = unpack(_f)"},
		{"string.format('%.2f')", "local _v = string.format('%.2f', 82.5 + _i)"},
		{"tostring(number)", "local _v = tostring(_i)"},
	}

	out := make([]calibration, 0, len(cases))
	for _, c := range cases {
		out = append(out, calibrate(b, e, c.name, c.body, base))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].usec > out[j].usec })

	logBoth(b, fmt.Sprintf("\n%-40s %10s", "pure-Lua helper (no Redis command)", "us/call"))
	for _, c := range out {
		logBoth(b, fmt.Sprintf("%-40s %10.3f", c.name, c.usec))
	}
}
