package redis

import (
	"context"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
)

// commonProbeBody exposes every common.lua helper through ARGV[1] so the Go
// tests can exercise them against a real server. "<nil>" and "<false>" ARGV
// values are translated to Lua nil/false.
const commonProbeBody = `
local function arg(v)
  if v == '<nil>' then return nil end
  if v == '<false>' then return false end
  return v
end
local function f(x)
  if x == nil then return '<nil>' end
  return string.format('%.17g', x)
end
local op = ARGV[1]
if op == 'base' then
  return sp_base()
elseif op == 'num' then
  return f(sp_num(arg(ARGV[2]), tonumber(ARGV[3])))
elseif op == 'split' then
  return sp_split(arg(ARGV[2]), arg(ARGV[3]))
elseif op == 'int_str' then
  return sp_int_str(arg(ARGV[2]))
elseif op == 'str_num' then
  return sp_str(tonumber(ARGV[2]))
elseif op == 'str_other' then
  return sp_str(nil) .. '|' .. sp_str(true) .. '|' .. sp_str('x')
elseif op == 'score_str' then
  return sp_score_str(tonumber(ARGV[2]))
elseif op == 'unpack' then
  local t = sp_hs_unpack(arg(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4]))
  return {f(t.score), f(t.sts), f(t.samples), f(t.nfail), f(t.lastfail), f(t.cd), f(t.ru), f(t.lu)}
elseif op == 'pack' then
  return sp_hs_pack({score = tonumber(ARGV[2]), sts = tonumber(ARGV[3]), samples = tonumber(ARGV[4]),
    nfail = tonumber(ARGV[5]), lastfail = tonumber(ARGV[6]), cd = tonumber(ARGV[7]), ru = tonumber(ARGV[8]),
    lu = tonumber(ARGV[9])})
elseif op == 'pack_empty' then
  return sp_hs_pack({})
elseif op == 'hs_raw' then
  local f1, f2, f3, f4, f5, f6, f7, f8 = sp_hs_raw(arg(ARGV[2]))
  if f1 == nil then return '<nil>' end
  return {f1, f2, f3, f4, f5, f6, f7, f8}
elseif op == 'hs_get' then
  return sp_hs_pack(sp_hs_get(sp_base(), ARGV[2], ARGV[3], tonumber(ARGV[4]), tonumber(ARGV[5])))
elseif op == 'hs_set' then
  return sp_hs_set(sp_base(), ARGV[2], ARGV[3], sp_hs_unpack(ARGV[4], 0, 0))
elseif op == 'hs_numeric' then
  local base = sp_base()
  sp_hs_set(base, tonumber(ARGV[2]), tonumber(ARGV[3]), {score = 12.345, sts = 1758011411962, cd = 1758011471962})
  return sp_hs_pack(sp_hs_get(base, tonumber(ARGV[2]), tonumber(ARGV[3]), 70, 1))
elseif op == 'decay' then
  return f(sp_decay(tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4]), tonumber(ARGV[5]), tonumber(ARGV[6])))
elseif op == 'ewma' then
  return f(sp_ewma(tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4])))
elseif op == 'avail' then
  return f(sp_avail(sp_base(), ARGV[2], ARGV[3]))
elseif op == 'push_all' then
  return sp_push_all(sp_base(), sp_split(ARGV[3], ','), ARGV[2], tonumber(ARGV[4]))
elseif op == 'push_all_nil' then
  return sp_push_all(sp_base(), nil, ARGV[2], 1)
elseif op == 'rescore' then
  return f(sp_rescore(sp_base(), ARGV[2], ARGV[3]))
elseif op == 'quota' then
  return f(sp_quota_est(arg(ARGV[2]), arg(ARGV[3]), tonumber(ARGV[4]), tonumber(ARGV[5]), tonumber(ARGV[6])))
elseif op == 'hmget_map' then
  local fields = {}
  for k = 2, #ARGV do fields[#fields + 1] = ARGV[k] end
  local m = sp_hmget_map(KEYS[2], fields)
  local out = {}
  for _, name in ipairs(fields) do
    if m[name] ~= nil then out[#out + 1] = name; out[#out + 1] = m[name] end
  end
  return out
elseif op == 'hmget_map_nil' then
  local n = 0
  for _ in pairs(sp_hmget_map(KEYS[2], nil)) do n = n + 1 end
  return n
end
return redis.error_reply('unknown op ' .. tostring(op))
`

type luaProbe struct {
	t      *testing.T
	client rueidis.Client
	keys   Keys
	script *Script
	site   int64
}

func newLuaProbe(t *testing.T) *luaProbe {
	t.Helper()
	client, keys := newTestClient(t)
	return &luaProbe{t: t, client: client, keys: keys, script: NewScript("common_probe", commonProbeBody), site: 77}
}

func (p *luaProbe) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	p.t.Cleanup(cancel)
	return ctx
}

func (p *luaProbe) run(args ...string) rueidis.RedisResult {
	p.t.Helper()
	return p.script.Exec(p.ctx(), p.client, []string{p.keys.SiteMeta(p.site)}, args)
}

func (p *luaProbe) str(args ...string) string {
	p.t.Helper()
	s, err := p.run(args...).ToString()
	require.NoError(p.t, err, "%v", args)
	return s
}

func (p *luaProbe) float(args ...string) float64 {
	p.t.Helper()
	s := p.str(args...)
	v, err := strconv.ParseFloat(s, 64)
	require.NoError(p.t, err, "parse %q", s)
	return v
}

func (p *luaProbe) strings(args ...string) []string {
	p.t.Helper()
	ss, err := p.run(args...).AsStrSlice()
	require.NoError(p.t, err, "%v", args)
	return ss
}

func (p *luaProbe) do(cmd rueidis.Completed) {
	p.t.Helper()
	require.NoError(p.t, p.client.Do(p.ctx(), cmd).Error())
}

func TestLuaBase(t *testing.T) {
	p := newLuaProbe(t)
	require.Equal(t, p.keys.SiteBase(p.site), p.str("base"))

	ctx := p.ctx()
	err := p.script.Exec(ctx, p.client, []string{p.keys.Identity(p.site, 1)}, []string{"base"}).Error()
	require.ErrorContains(t, err, "sp_base")
	err = p.script.Exec(ctx, p.client, nil, []string{"base"}).Error()
	require.ErrorContains(t, err, "sp_base")
	err = p.script.Exec(ctx, p.client, []string{"meta"}, []string{"base"}).Error()
	require.ErrorContains(t, err, "sp_base")
	err = p.script.Exec(ctx, p.client, []string{p.keys.SiteMeta(p.site)}, []string{"nope"}).Error()
	require.ErrorContains(t, err, "unknown op")
}

func TestLuaNum(t *testing.T) {
	p := newLuaProbe(t)
	tests := []struct {
		name string
		v    string
		def  string
		want string
	}{
		{"integer", "42", "7", "42"},
		{"float", "12.5", "7", "12.5"},
		{"negative", "-1", "7", "-1"},
		{"large timestamp", "1758011411962", "0", "1758011411962"},
		{"ten digits", "1000000000", "0", "1000000000"},
		{"nine digits", "999999999", "0", "999999999"},
		{"fifteen digits", "999999999999999", "0", "999999999999999"},
		{"sixteen digits", "1000000000000000", "0", "1000000000000000"},
		{"long with sign", "-1758011411962", "0", "-1758011411962"},
		{"long with leading zero", "0758011411962", "0", "758011411962"},
		{"long fraction", "1758011411962.5", "0", "1758011411962.5"},
		{"long exponent", "1758011411962e1", "0", "17580114119620"},
		{"long with trailing junk", "1758011411962x", "7", "7"},
		{"long with inner junk", "17580114x1962", "7", "7"},
		{"nil", "<nil>", "7", "7"},
		{"false", "<false>", "7", "7"},
		{"empty", "", "7", "7"},
		{"garbage", "abc", "7", "7"},
		{"nan", "nan", "7", "7"},
		{"inf", "inf", "7", "7"},
		{"nil default", "<nil>", "<nil>", "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, p.str("num", tt.v, tt.def))
		})
	}
}

// TestLuaNumLongNumeralsMatchStrconv pins the contract of sp_dnum, which
// sp_num parses long numerals with: it must return exactly what a plain
// tonumber would. The helper splits a numeral of ten to fifteen digits into two
// halves because Lua 5.1 parses those about eight times slower in one piece,
// and both halves, their product and their sum are integers below 2^53, so the
// split is exact rather than close. The same trick on a fraction is not, which
// is why sp_dnum leaves fractions to tonumber.
func TestLuaNumLongNumeralsMatchStrconv(t *testing.T) {
	p := newLuaProbe(t)
	rng := rand.New(rand.NewSource(20240917)) //nolint:gosec // deterministic test vector, not security.
	for i := 0; i < 400; i++ {
		digits := 10 + rng.Intn(6)
		b := make([]byte, digits)
		b[0] = byte('1' + rng.Intn(9))
		for j := 1; j < digits; j++ {
			b[j] = byte('0' + rng.Intn(10))
		}
		in := string(b)
		want, err := strconv.ParseInt(in, 10, 64)
		require.NoError(t, err)
		require.Equal(t, strconv.FormatInt(want, 10), p.str("num", in, "0"), "sp_num(%q)", in)
	}
}

func TestLuaSplit(t *testing.T) {
	p := newLuaProbe(t)
	tests := []struct {
		name string
		s    string
		sep  string
		want []string
	}{
		{"pipes", "a|b|c", "|", []string{"a", "b", "c"}},
		{"empty fields kept", "a||b|", "|", []string{"a", "", "b", ""}},
		{"tags", ",t1,t2,", ",", []string{"", "t1", "t2", ""}},
		{"no separator present", "abc", ",", []string{"abc"}},
		{"literal dot separator", "a.b.c", ".", []string{"a", "b", "c"}},
		{"multi-char separator", "a::b", "::", []string{"a", "b"}},
		{"empty string", "", ",", []string{}},
		{"nil string", "<nil>", ",", []string{}},
		{"false string", "<false>", ",", []string{}},
		{"empty separator", "abc", "", []string{"abc"}},
		{"nil separator", "abc", "<nil>", []string{"abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, p.strings("split", tt.s, tt.sep))
		})
	}
}

func TestLuaIntAndScoreFormatting(t *testing.T) {
	p := newLuaProbe(t)
	intTests := []struct {
		in, want string
	}{
		{"0", "0"},
		{"1758011411962", "1758011411962"},
		{"4102444800000", "4102444800000"},
		{"9007199254740992", "9007199254740992"},
		{"12.9", "12"},
		{"-1", "-1"},
		{"-0.4", "-1"},
		{"-0", "0"},
		{"", "0"},
		{"<nil>", "0"},
		{"<false>", "0"},
		{"x", "0"},
		{"inf", "0"},
	}
	for _, tt := range intTests {
		require.Equal(t, tt.want, p.str("int_str", tt.in), "int_str(%q)", tt.in)
	}
	require.Equal(t, "1758011411962", p.str("str_num", "1758011411962"))
	require.Equal(t, "||x", p.str("str_other"))

	scoreTests := []struct {
		in, want string
	}{
		{"1758011411962", "1758011411962"},
		{"0", "0"},
		{"-5", "-5"},
		{"1.5", "1.5"},
		{"1e300", "1.0000000000000001e+300"},
	}
	for _, tt := range scoreTests {
		require.Equal(t, tt.want, p.str("score_str", tt.in), "score_str(%q)", tt.in)
	}
}

func TestLuaHealthStateCodec(t *testing.T) {
	p := newLuaProbe(t)
	const now = "1758011411962"

	t.Run("unpack missing entry", func(t *testing.T) {
		for _, missing := range []string{"<nil>", "<false>", ""} {
			require.Equal(t, []string{"70", now, "0", "0", "0", "0", "0", "0"}, p.strings("unpack", missing, "70", now))
		}
	})
	t.Run("unpack full entry", func(t *testing.T) {
		got := p.strings("unpack", "63.25|1758011411962|12|3|1758011400000|1758015011962|1758011471962|1758011411000", "70", "1")
		require.Equal(t, []string{"63.25", "1758011411962", "12", "3", "1758011400000", "1758015011962", "1758011471962", "1758011411000"}, got)
	})
	t.Run("unpack partial and malformed entry", func(t *testing.T) {
		got := p.strings("unpack", "abc||5", "70", now)
		require.Equal(t, []string{"70", now, "5", "0", "0", "0", "0", "0"}, got)
	})
	t.Run("unpack nil baseline and now", func(t *testing.T) {
		got := p.strings("unpack", "", "x", "y")
		require.Equal(t, []string{"0", "0", "0", "0", "0", "0", "0", "0"}, got)
	})
	t.Run("pack", func(t *testing.T) {
		got := p.str("pack", "63.256", "1758011411962", "12", "3", "1758011400000", "1758015011962.7", "0", "1758011411000")
		require.Equal(t, "63.26|1758011411962|12|3|1758011400000|1758015011962|0|1758011411000", got)
	})
	t.Run("pack rounds tiny negative score to zero", func(t *testing.T) {
		require.Equal(t, "0.00|0|0|0|0|0|0|0", p.str("pack", "-0.001", "0", "0", "0", "0", "0", "0", "0"))
	})
	t.Run("pack missing fields", func(t *testing.T) {
		require.Equal(t, "0.00|0|0|0|0|0|0|0", p.str("pack_empty"))
	})
	t.Run("raw splits exactly the eight fields", func(t *testing.T) {
		packed := "63.25|1758011411962|12|3|1758011400000|1758015011962|1758011471962|1758011411000"
		require.Equal(t,
			[]string{"63.25", "1758011411962", "12", "3", "1758011400000", "1758015011962", "1758011471962", "1758011411000"},
			p.strings("hs_raw", packed))
		// Empty fields are kept as empty strings, which sp_num reads as the
		// same defaults sp_hs_unpack applies.
		require.Equal(t, []string{"", "", "", "", "", "", "", ""}, p.strings("hs_raw", "|||||||"))
	})
	t.Run("raw refuses anything that is not eight fields", func(t *testing.T) {
		for _, bad := range []string{"<nil>", "<false>", "", "abc||5", "1|2|3|4|5|6|7", "1|2|3|4|5|6|7|8|9"} {
			require.Equal(t, "<nil>", p.str("hs_raw", bad), bad)
		}
	})
	t.Run("raw returns the fields sp_hs_pack wrote", func(t *testing.T) {
		packed := p.str("pack", "63.256", "1758011411962", "12", "3", "1758011400000", "1758015011962", "0", "1758011411000")
		raw := p.strings("hs_raw", packed)
		require.Equal(t, packed, strings.Join(raw, "|"))
		// The seven integer fields are the exact text sp_hs_unpack parses, so
		// splicing them back is lossless; only the score carries a rounding.
		unpacked := p.strings("unpack", packed, "70", "1")
		for i := 1; i < len(raw); i++ {
			require.Equal(t, unpacked[i], raw[i], "field %d", i+1)
		}
		require.Equal(t, "63.26", raw[0])
	})
	t.Run("round trip", func(t *testing.T) {
		packed := "99.10|4102444800000|100000|0|0|0|0|4102444800000"
		require.Equal(t, packed, p.str("pack", "99.1", "4102444800000", "100000", "0", "0", "0", "0", "4102444800000"))
		got := p.strings("unpack", packed, "70", "0")
		require.Equal(t, []string{"99.099999999999994", "4102444800000", "100000", "0", "0", "0", "0", "4102444800000"}, got)
	})
}

func TestLuaHealthStateStorage(t *testing.T) {
	p := newLuaProbe(t)
	const packed = "55.50|1758011411962|4|2|1758011400000|1758011471962|0|1758011411962"

	// Missing hash: defaults.
	require.Equal(t, "70.00|1758011411962|0|0|0|0|0|0", p.str("hs_get", "5", "9001", "70", "1758011411962"))

	require.Equal(t, packed, p.str("hs_set", "5", "9001", packed))
	stored, err := p.client.Do(p.ctx(), p.client.B().Hget().Key(p.keys.Health(p.site, 5)).Field("9001").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, packed, stored)
	require.Equal(t, packed, p.str("hs_get", "5", "9001", "70", "1"))

	// Numeric key parts are formatted as integers, not "%.14g".
	require.Equal(t, "12.35|1758011411962|0|0|0|1758011471962|0|0", p.str("hs_numeric", "123456789012345", "987654321098765"))
	stored, err = p.client.Do(p.ctx(), p.client.B().Hget().Key(p.keys.Health(p.site, 123456789012345)).Field("987654321098765").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "12.35|1758011411962|0|0|0|1758011471962|0|0", stored)
}

func TestLuaDecayAndEWMA(t *testing.T) {
	p := newLuaProbe(t)
	const tau = 6 * 3600 * 1000.0

	decay := func(score, sts, now, baseline, tauMs float64) float64 {
		return p.float("decay", ff(score), ff(sts), ff(now), ff(baseline), ff(tauMs))
	}
	require.InDelta(t, 70+(30-70)*math.Exp(-1), decay(30, 1758011411962, 1758011411962+tau, 70, tau), 1e-9)
	require.InDelta(t, 70+(100-70)*math.Exp(-0.5), decay(100, 0, tau/2, 70, tau), 1e-9)
	require.Equal(t, 30.0, decay(30, 1000, 1000, 70, tau), "no elapsed time")
	require.Equal(t, 30.0, decay(30, 2000, 1000, 70, tau), "clock skew")
	require.Equal(t, 30.0, decay(30, 0, 1e12, 70, 0), "tau disabled")
	require.InDelta(t, 70, decay(30, 0, 1758011411962, 70, 1000), 1e-9, "fully decayed")

	ewma := func(score, v, alpha float64) float64 {
		return p.float("ewma", ff(score), ff(v), ff(alpha))
	}
	require.InDelta(t, 0.1*100+0.9*70, ewma(70, 100, 0.1), 1e-9)
	require.InDelta(t, 0.1*0+0.9*70, ewma(70, 0, 0.1), 1e-9)
	require.Equal(t, 70.0, ewma(70, 100, 0))
	require.Equal(t, 100.0, ewma(70, 100, 1))
	require.Equal(t, 70.0, ewma(70, 100, -3), "alpha clamped to 0")
	require.Equal(t, 100.0, ewma(70, 100, 3), "alpha clamped to 1")
}

func TestLuaAvailability(t *testing.T) {
	const (
		eg  = 5
		i   = 9001
		acc = 33
	)
	tests := []struct {
		name  string
		hs    string // packed hs entry, "" = missing
		id    []string
		acdCd string // account cooldown, "" = missing account hash
		want  float64
	}{
		{name: "everything missing", want: 0},
		{name: "hs cooldown", hs: "70.00|1|0|0|0|1758011471962|1758011411000|0", want: 1758011471962},
		{name: "hs reuse", hs: "70.00|1|0|0|0|1758011400000|1758011499999|0", want: 1758011499999},
		{name: "site cooldown", hs: "70.00|1|0|0|0|10|20|0", id: []string{"scd", "1758099999999", "sru", "5"}, want: 1758099999999},
		{name: "site reuse", id: []string{"scd", "", "sru", "1758011411963"}, want: 1758011411963},
		{name: "exclusive lease ignored without active leases", id: []string{"al", "0", "xl", "1758011999999"}, want: 0},
		{name: "exclusive lease with active leases", id: []string{"al", "1", "xl", "1758011999999", "scd", "100"}, want: 1758011999999},
		{name: "empty account ignored", id: []string{"acc", "", "sru", "7"}, acdCd: "1758019999999", want: 7},
		{name: "account cooldown", id: []string{"acc", "33", "scd", "8"}, acdCd: "1758019999999", want: 1758019999999},
		{name: "account without hash", id: []string{"acc", "33", "scd", "8"}, want: 8},
		{name: "account cooldown smaller", hs: "70.00|1|0|0|0|1758030000000|0|0", id: []string{"acc", "33"}, acdCd: "1758019999999", want: 1758030000000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newLuaProbe(t)
			if tt.hs != "" {
				p.do(p.client.B().Hset().Key(p.keys.Health(p.site, eg)).FieldValue().FieldValue(strconv.Itoa(i), tt.hs).Build())
			}
			if len(tt.id) > 0 {
				cmd := p.client.B().Hset().Key(p.keys.Identity(p.site, i)).FieldValue()
				for k := 0; k < len(tt.id); k += 2 {
					cmd = cmd.FieldValue(tt.id[k], tt.id[k+1])
				}
				p.do(cmd.Build())
			}
			if tt.acdCd != "" {
				p.do(p.client.B().Hset().Key(p.keys.Account(p.site, acc)).FieldValue().FieldValue("st", "active").FieldValue("cd", tt.acdCd).Build())
			}
			require.Equal(t, tt.want, p.float("avail", strconv.Itoa(eg), strconv.Itoa(i)))
		})
	}
}

func TestLuaPushAllAndRescore(t *testing.T) {
	p := newLuaProbe(t)
	ctx := p.ctx()
	score := func(eg int64, member string) (float64, bool) {
		v, err := p.client.Do(ctx, p.client.B().Zscore().Key(p.keys.Ready(p.site, eg)).Member(member).Build()).AsFloat64()
		if rueidis.IsRedisNil(err) {
			return 0, false
		}
		require.NoError(t, err)
		return v, true
	}

	p.do(p.client.B().Zadd().Key(p.keys.Ready(p.site, 1)).ScoreMember().ScoreMember(1000, "9001").ScoreMember(0, "9002").Build())
	p.do(p.client.B().Zadd().Key(p.keys.Ready(p.site, 2)).ScoreMember().ScoreMember(1758099999999, "9001").Build())
	// eg 3 does not contain the identity.

	changed, err := p.run("push_all", "9001", "1,2,3", "1758011411962").AsInt64()
	require.NoError(t, err)
	require.Equal(t, int64(1), changed, "only eg 1 moves forward")

	v, ok := score(1, "9001")
	require.True(t, ok)
	require.Equal(t, 1758011411962.0, v)
	v, ok = score(2, "9001")
	require.True(t, ok)
	require.Equal(t, 1758099999999.0, v, "GT never moves a score backwards")
	_, ok = score(3, "9001")
	require.False(t, ok, "XX never adds members")

	changed, err = p.run("push_all", "9001", "", "1").AsInt64()
	require.NoError(t, err)
	require.Zero(t, changed)
	changed, err = p.run("push_all_nil", "9001").AsInt64()
	require.NoError(t, err)
	require.Zero(t, changed)

	// Rescore sets the exact availability, moving backwards when needed.
	p.do(p.client.B().Hset().Key(p.keys.Identity(p.site, 9001)).FieldValue().FieldValue("scd", "1758011400000").Build())
	require.Equal(t, 1758011400000.0, p.float("rescore", "2", "9001"))
	v, _ = score(2, "9001")
	require.Equal(t, 1758011400000.0, v)

	require.Equal(t, 1758011400000.0, p.float("rescore", "3", "9001"))
	_, ok = score(3, "9001")
	require.False(t, ok, "rescore never adds members")
}

func TestLuaQuotaEstimate(t *testing.T) {
	p := newLuaProbe(t)
	const start = 1758011400000.0
	tests := []struct {
		name  string
		prev  string
		cur   string
		start float64
		w     float64
		now   float64
		want  float64
	}{
		{"window start", "100", "0", start, 60000, start, 100},
		{"half window", "100", "10", start, 60000, start + 30000, 60},
		{"window end", "100", "10", start, 60000, start + 60000, 10},
		{"beyond window", "100", "10", start, 60000, start + 600000, 10},
		{"clock skew", "100", "10", start, 60000, start - 5000, 110},
		{"no previous", "<nil>", "3", start, 60000, start + 1, 3},
		{"no counts", "", "<false>", start, 60000, start + 1, 0},
		{"zero window", "100", "4", start, 0, start, 4},
		{"negative window", "100", "4", start, -1, start, 4},
		{"large hour window", "3600", "1", 1758009600000, 3600000, 1758011400000, 3600*(1-1800000.0/3600000) + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.float("quota", tt.prev, tt.cur, ff(tt.start), ff(tt.w), ff(tt.now))
			require.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestLuaHmgetMap(t *testing.T) {
	p := newLuaProbe(t)
	key := p.keys.Identity(p.site, 1)
	p.do(p.client.B().Hset().Key(key).FieldValue().FieldValue("st", "active").FieldValue("acc", "").FieldValue("lu", "1758011411962").Build())
	run := func(args ...string) []string {
		ss, err := p.script.Exec(p.ctx(), p.client, []string{p.keys.SiteMeta(p.site), key}, args).AsStrSlice()
		require.NoError(t, err)
		return ss
	}
	require.Equal(t, []string{"st", "active", "acc", "", "lu", "1758011411962"}, run("hmget_map", "st", "missing", "acc", "lu"))
	require.Equal(t, []string{}, run("hmget_map", "missing"))
	require.Equal(t, []string{}, run("hmget_map"))

	n, err := p.script.Exec(p.ctx(), p.client, []string{p.keys.SiteMeta(p.site), key}, []string{"hmget_map_nil"}).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)

	missingKey := p.keys.Identity(p.site, 2)
	ss, err := p.script.Exec(p.ctx(), p.client, []string{p.keys.SiteMeta(p.site), missingKey}, []string{"hmget_map", "st"}).AsStrSlice()
	require.NoError(t, err)
	require.Empty(t, ss)
}

// ff formats a float for ARGV without losing precision.
func ff(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
