package breaker

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// luaEnv runs the breaker scripts against one endpoint group of one site.
type luaEnv struct {
	t       *testing.T
	rdb     rueidis.Client
	keys    redis.Keys
	siteKey int64
	egKey   int64
}

// t0 is aligned to 5 s buckets.
var t0 = time.UnixMilli(1_758_011_400_000)

func newLuaEnv(t *testing.T) *luaEnv {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	return &luaEnv{t: t, rdb: rdb, keys: keys, siteKey: 7, egKey: 70}
}

func (e *luaEnv) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

func (e *luaEnv) do(cmd rueidis.Completed) rueidis.RedisResult {
	e.t.Helper()
	res := e.rdb.Do(e.ctx(), cmd)
	require.NoError(e.t, res.Error())
	return res
}

// bucket adds counts to the 5 s bucket containing at.
func (e *luaEnv) bucket(at time.Time, total, success, risk int64, captcha ...int64) {
	e.t.Helper()
	b := at.UnixMilli() / 5000
	key := e.keys.Window(e.siteKey, e.egKey, b)
	e.do(e.rdb.B().Hincrby().Key(key).Field("t").Increment(total).Build())
	e.do(e.rdb.B().Hincrby().Key(key).Field("s").Increment(success).Build())
	e.do(e.rdb.B().Hincrby().Key(key).Field("r").Increment(risk).Build())
	if len(captcha) > 0 {
		members := make([]string, 0, len(captcha))
		for _, c := range captcha {
			members = append(members, strconv.FormatInt(c, 10))
		}
		e.do(e.rdb.B().Pfadd().Key(e.keys.WindowHLL(e.siteKey, e.egKey, b)).Element(members...).Build())
	}
}

func (e *luaEnv) eval(now time.Time, p evalParams, mode string) evalResult {
	e.t.Helper()
	call := evalExec(e.keys, e.siteKey, e.egKey, now, p, mode)
	res, err := parseEvalResult(evalScript.Exec(e.ctx(), e.rdb, call.Keys, call.Args))
	require.NoError(e.t, err)
	return res
}

func (e *luaEnv) set(now time.Time, op string, d time.Duration, reason string) setResult {
	e.t.Helper()
	call := setExec(e.keys, e.siteKey, e.egKey, now, op, d, reason)
	res, err := parseSetResult(setScript.Exec(e.ctx(), e.rdb, call.Keys, call.Args))
	require.NoError(e.t, err)
	return res
}

func (e *luaEnv) hset(key string, kv ...string) {
	e.t.Helper()
	cmd := e.rdb.B().Hset().Key(key).FieldValue()
	for i := 0; i+1 < len(kv); i += 2 {
		cmd = cmd.FieldValue(kv[i], kv[i+1])
	}
	e.do(cmd.Build())
}

func (e *luaEnv) setBreaker(kv ...string) {
	e.t.Helper()
	e.hset(e.keys.Breaker(e.siteKey, e.egKey), kv...)
}

func (e *luaEnv) breaker() map[string]string {
	e.t.Helper()
	m, err := e.do(e.rdb.B().Hgetall().Key(e.keys.Breaker(e.siteKey, e.egKey)).Build()).AsStrMap()
	require.NoError(e.t, err)
	return m
}

func (e *luaEnv) isOpenMember() bool {
	e.t.Helper()
	ok, err := e.do(e.rdb.B().Sismember().Key(e.keys.OpenBreakers(e.siteKey)).Member(strconv.FormatInt(e.egKey, 10)).Build()).AsBool()
	require.NoError(e.t, err)
	return ok
}

func defaultParams() evalParams { return paramsFor(nil) }

func TestEvalTripConditions(t *testing.T) {
	t.Parallel()
	withoutSuccess := defaultParams()
	withoutSuccess.SuccessRatioLte = 0
	withoutSuccess.RiskRatioGte = 0
	captchaIDs := func(n int64) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(i + 1)
		}
		return out
	}
	tests := []struct {
		name       string
		params     evalParams
		seed       func(e *luaEnv)
		wantOpen   bool
		wantReason string
		wantTotal  int64
	}{
		{name: "no samples", params: defaultParams(), seed: func(*luaEnv) {}},
		{
			name: "below min_requests", params: defaultParams(), wantTotal: 49,
			seed: func(e *luaEnv) { e.bucket(t0, 49, 0, 49) },
		},
		{
			name: "risk ratio at threshold", params: defaultParams(), wantOpen: true, wantTotal: 50,
			wantReason: "risk_ratio 0.40 >= 0.40",
			seed:       func(e *luaEnv) { e.bucket(t0, 50, 30, 20) },
		},
		{
			name: "risk ratio below threshold", params: defaultParams(), wantTotal: 50,
			seed: func(e *luaEnv) { e.bucket(t0, 50, 31, 19) },
		},
		{
			name: "distinct captcha identities", params: defaultParams(), wantOpen: true, wantTotal: 100,
			wantReason: "captcha_identities 10 >= 10",
			seed: func(e *luaEnv) {
				e.bucket(t0.Add(-10*time.Second), 50, 45, 5, captchaIDs(6)...)
				e.bucket(t0, 50, 45, 5, captchaIDs(10)...)
			},
		},
		{
			name: "success ratio", params: defaultParams(), wantOpen: true, wantTotal: 50,
			wantReason: "success_ratio 0.20 <= 0.20",
			seed:       func(e *luaEnv) { e.bucket(t0, 50, 10, 0) },
		},
		{
			name: "disabled conditions do not trip", params: withoutSuccess, wantTotal: 50,
			seed: func(e *luaEnv) { e.bucket(t0, 50, 0, 50) },
		},
		{
			name: "samples spread over the window", params: defaultParams(), wantOpen: true, wantTotal: 60,
			wantReason: "risk_ratio 0.50 >= 0.40",
			seed: func(e *luaEnv) {
				for i := range 6 {
					e.bucket(t0.Add(-time.Duration(i)*10*time.Second), 10, 5, 5)
				}
			},
		},
		{
			name: "buckets outside the window are ignored", params: defaultParams(), wantTotal: 0,
			seed: func(e *luaEnv) { e.bucket(t0.Add(-60*time.Second), 100, 0, 100) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newLuaEnv(t)
			tc.seed(e)
			res := e.eval(t0, tc.params, modeEval)
			require.Equal(t, tc.wantTotal, res.Window.Total)
			if !tc.wantOpen {
				require.False(t, res.Transitioned())
				require.Equal(t, StateClosed, res.Hash.State)
				require.Empty(t, e.breaker())
				require.False(t, e.isOpenMember())
				return
			}
			require.Equal(t, StateClosed, res.From)
			require.Equal(t, StateOpen, res.To)
			require.Equal(t, TriggerAuto, res.Trigger)
			require.Contains(t, res.Hash.Reason, tc.wantReason)
			require.Equal(t, int64(1), res.Hash.OpenCount)
			require.Equal(t, t0.Add(2*time.Minute).UnixMilli(), res.Hash.OpenUntilMs)
			require.Equal(t, t0.UnixMilli(), res.Hash.LastOpenedMs)
			require.Equal(t, int64(1), res.Hash.Version)
			require.True(t, e.isOpenMember())
			h := e.breaker()
			require.Equal(t, "open", h["st"])
			require.Equal(t, "0", h["man"])
			require.Equal(t, "1", h["v"])
		})
	}
}

func TestEvalOpenDurationDoublingAndCap(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	p := defaultParams()
	p.MaxOpenMs = (5 * time.Minute).Milliseconds()
	e.bucket(t0, 50, 0, 50)

	now := t0
	res := e.eval(now, p, modeEval)
	require.Equal(t, StateOpen, res.To)
	require.Equal(t, now.Add(2*time.Minute).UnixMilli(), res.Hash.OpenUntilMs)

	wantDurations := []time.Duration{4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, want := range wantDurations {
		now = time.UnixMilli(res.Hash.OpenUntilMs)
		half := e.eval(now, p, modeEval)
		require.Equal(t, StateOpen, half.From, "round %d", i)
		require.Equal(t, StateHalfOpen, half.To, "round %d", i)

		e.setBreaker("ps", "5", "pk", "1")
		now = now.Add(10 * time.Second)
		res = e.eval(now, p, modeEval)
		require.Equal(t, StateHalfOpen, res.From, "round %d", i)
		require.Equal(t, StateOpen, res.To, "round %d", i)
		require.Equal(t, TriggerProbe, res.Trigger)
		require.Equal(t, int64(i+2), res.Hash.OpenCount, "round %d", i)
		require.Equal(t, now.Add(want).UnixMilli(), res.Hash.OpenUntilMs, "round %d", i)
		require.Contains(t, res.Hash.Reason, "probe success_ratio 0.20 < 0.80")
		require.True(t, e.isOpenMember())
	}
}

func TestEvalResetOpenCountAfter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		closedFor  time.Duration
		wantOCount int64
	}{
		{name: "recently closed keeps counting", closedFor: 10 * time.Minute, wantOCount: 4},
		{name: "closed long enough resets", closedFor: 30 * time.Minute, wantOCount: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newLuaEnv(t)
			now := t0.Add(tc.closedFor)
			e.setBreaker("st", "closed", "oc", "3", "lc", strconv.FormatInt(t0.UnixMilli(), 10), "v", "6")
			e.bucket(now, 50, 0, 50)
			res := e.eval(now, defaultParams(), modeEval)
			require.Equal(t, StateOpen, res.To)
			require.Equal(t, tc.wantOCount, res.Hash.OpenCount)
			require.Equal(t, int64(7), res.Hash.Version)
		})
	}
}

func TestEvalIgnoresSamplesBeforeClose(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	closedAt := t0.Add(-20 * time.Second)
	e.setBreaker("st", "closed", "lc", strconv.FormatInt(closedAt.UnixMilli(), 10))
	e.bucket(t0.Add(-30*time.Second), 100, 0, 100) // before the close
	e.bucket(t0.Add(-10*time.Second), 20, 20, 0)   // after the close
	res := e.eval(t0, defaultParams(), modeEval)
	require.False(t, res.Transitioned())
	require.Equal(t, Window{Total: 20, Success: 20}, res.Window)
}

func TestEvalHalfOpen(t *testing.T) {
	t.Parallel()
	p := defaultParams()
	openUntil := t0.Add(2 * time.Minute)

	t.Run("open until expiry then half-open", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.setBreaker("st", "open", "ou", strconv.FormatInt(openUntil.UnixMilli(), 10), "oc", "1", "man", "0", "v", "3", "rsn", "tripped")
		require.False(t, e.eval(openUntil.Add(-time.Millisecond), p, modeEval).Transitioned())

		res := e.eval(openUntil, p, modeEval)
		require.Equal(t, StateOpen, res.From)
		require.Equal(t, StateHalfOpen, res.To)
		require.Equal(t, TriggerAuto, res.Trigger)
		require.False(t, res.LazyHalfOpen)
		require.Zero(t, res.Hash.OpenUntilMs, "recorded half-open transitions clear ou")
		require.Equal(t, openUntil.UnixMilli(), res.Hash.HalfOpenMs)
		require.Equal(t, int64(4), res.Hash.Version)
		require.Equal(t, "tripped", res.Hash.Reason)
		require.True(t, e.isOpenMember())
	})

	t.Run("corrupt automatic open without end half-opens", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.setBreaker("st", "open", "ou", "0", "man", "0")
		require.Equal(t, StateHalfOpen, e.eval(t0, p, modeEval).To)
	})

	tests := []struct {
		name       string
		ps, pk     string
		wantTo     string
		wantOC     int64
		wantMember bool
	}{
		{name: "not enough probe samples", ps: "4", pk: "4", wantOC: 2, wantMember: true},
		{name: "probe success closes", ps: "5", pk: "4", wantTo: StateClosed, wantOC: 2},
		{name: "probe failure reopens", ps: "5", pk: "3", wantTo: StateOpen, wantOC: 3, wantMember: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newLuaEnv(t)
			// Half-opened lazily by acquire.lua: only st/hw/hc/ps/pk/v changed.
			e.setBreaker("st", "half_open", "ou", strconv.FormatInt(openUntil.UnixMilli(), 10), "oc", "2", "man", "1",
				"hw", strconv.FormatInt(openUntil.UnixMilli(), 10), "hc", "5", "ps", tc.ps, "pk", tc.pk, "v", "9")
			require.NoError(t, e.rdb.Do(e.ctx(), e.rdb.B().Sadd().Key(e.keys.OpenBreakers(e.siteKey)).Member(strconv.FormatInt(e.egKey, 10)).Build()).Error())
			now := openUntil.Add(20 * time.Second)
			// The first evaluation only reports the unrecorded lazy transition.
			lazy := e.eval(now, p, modeEval)
			require.True(t, lazy.LazyHalfOpen)
			require.Equal(t, openUntil.UnixMilli(), lazy.LazyAtMs)
			require.False(t, lazy.Transitioned())
			require.Equal(t, StateHalfOpen, lazy.Hash.State)
			require.Zero(t, lazy.Hash.OpenUntilMs)
			require.Equal(t, int64(9), lazy.Hash.Version)
			require.Equal(t, "0", e.breaker()["ou"])

			res := e.eval(now, p, modeEval)
			require.False(t, res.LazyHalfOpen)
			require.Equal(t, tc.wantTo, res.To)
			require.Equal(t, tc.wantOC, res.Hash.OpenCount)
			require.Equal(t, tc.wantMember, e.isOpenMember())
			switch tc.wantTo {
			case StateClosed:
				require.Equal(t, TriggerProbe, res.Trigger)
				require.Equal(t, now.UnixMilli(), res.Hash.LastClosedMs)
				require.False(t, res.Hash.Manual)
				require.Zero(t, res.Hash.ProbeSamples)
				require.Equal(t, int64(10), res.Hash.Version)
				require.Contains(t, res.Hash.Reason, "probe success_ratio 0.80 >= 0.80")
			case StateOpen:
				require.Equal(t, now.Add(8*time.Minute).UnixMilli(), res.Hash.OpenUntilMs)
				require.False(t, res.Hash.Manual)
			default:
				require.Equal(t, StateHalfOpen, res.Hash.State)
			}
		})
	}
}

func TestBreakerSetManual(t *testing.T) {
	t.Parallel()
	p := defaultParams()

	t.Run("indefinite open never half-opens", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		res := e.set(t0, "open", 0, "maintenance")
		require.True(t, res.Changed)
		require.Equal(t, StateClosed, res.From)
		require.Equal(t, StateOpen, res.To)
		require.True(t, res.Hash.Manual)
		require.Zero(t, res.Hash.OpenUntilMs)
		require.Equal(t, "maintenance", res.Hash.Reason)
		require.True(t, e.isOpenMember())
		require.False(t, e.eval(t0.Add(365*24*time.Hour), p, modeEval).Transitioned())
	})

	t.Run("timed open half-opens at its end", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.setBreaker("oc", "2")
		res := e.set(t0, "open", 30*time.Minute, "deploy")
		require.Equal(t, t0.Add(30*time.Minute).UnixMilli(), res.Hash.OpenUntilMs)
		require.Equal(t, int64(2), res.Hash.OpenCount)
		// Tripping data does not matter while open.
		e.bucket(t0.Add(10*time.Minute), 50, 0, 50)
		require.False(t, e.eval(t0.Add(10*time.Minute), p, modeEval).Transitioned())
		half := e.eval(t0.Add(30*time.Minute), p, modeEval)
		require.Equal(t, StateHalfOpen, half.To)
	})

	t.Run("close keeps the open count", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.bucket(t0, 50, 0, 50)
		require.Equal(t, StateOpen, e.eval(t0, p, modeEval).To)
		now := t0.Add(time.Minute)
		res := e.set(now, "close", 0, "false alarm")
		require.True(t, res.Changed)
		require.Equal(t, StateOpen, res.From)
		require.Equal(t, StateClosed, res.To)
		require.Equal(t, int64(1), res.Hash.OpenCount)
		require.Equal(t, now.UnixMilli(), res.Hash.LastClosedMs)
		require.Zero(t, res.Hash.OpenUntilMs)
		require.Equal(t, int64(2), res.Hash.Version)
		require.False(t, e.isOpenMember())
		// The samples from before the close do not re-trip the breaker.
		require.False(t, e.eval(now, p, modeEval).Transitioned())

		again := e.set(now.Add(time.Second), "close", 0, "again")
		require.False(t, again.Changed)
		require.Equal(t, int64(2), again.Hash.Version)
		require.Equal(t, "false alarm", again.Hash.Reason)
	})

	t.Run("operation on a lazily half-opened breaker reports it", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		hw := t0.Add(-time.Second)
		e.setBreaker("st", "half_open", "ou", strconv.FormatInt(t0.Add(-2*time.Second).UnixMilli(), 10), "man", "1",
			"hw", strconv.FormatInt(hw.UnixMilli(), 10), "rsn", "deploy", "oc", "1", "v", "3")
		res := e.set(t0, "close", 0, "done")
		require.True(t, res.Changed)
		require.True(t, res.LazyHalfOpen)
		require.Equal(t, hw.UnixMilli(), res.LazyAtMs)
		require.Equal(t, "deploy", res.LazyReason)
		require.True(t, res.LazyManual)
		require.Equal(t, StateHalfOpen, res.From)
		require.Equal(t, "done", res.Hash.Reason)
		require.Equal(t, int64(4), res.Hash.Version)

		plain := e.set(t0, "open", 0, "again")
		require.False(t, plain.LazyHalfOpen)
	})

	t.Run("unknown operation fails", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		call := setExec(e.keys, e.siteKey, e.egKey, t0, "toggle", 0, "")
		_, err := parseSetResult(setScript.Exec(e.ctx(), e.rdb, call.Keys, call.Args))
		require.ErrorContains(t, err, "unknown operation")
	})
}

func TestEvalDisabledPolicy(t *testing.T) {
	t.Parallel()
	p := defaultParams()
	p.Enabled = false
	future := strconv.FormatInt(t0.Add(time.Hour).UnixMilli(), 10)
	past := strconv.FormatInt(t0.Add(-time.Minute).UnixMilli(), 10)
	tests := []struct {
		name   string
		fields []string
		wantTo string
	}{
		{name: "closed never trips", fields: []string{"st", "closed"}},
		{name: "automatic open closes", fields: []string{"st", "open", "ou", future, "man", "0"}, wantTo: StateClosed},
		{name: "half-open closes", fields: []string{"st", "half_open", "man", "0"}, wantTo: StateClosed},
		{name: "manual indefinite open is kept", fields: []string{"st", "open", "ou", "0", "man", "1"}},
		{name: "manual timed open is kept until its end", fields: []string{"st", "open", "ou", future, "man", "1"}},
		{name: "expired manual open closes", fields: []string{"st", "open", "ou", past, "man", "1"}, wantTo: StateClosed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newLuaEnv(t)
			e.setBreaker(tc.fields...)
			e.bucket(t0, 50, 0, 50)
			res := e.eval(t0, p, modeEval)
			require.Equal(t, tc.wantTo, res.To)
			if tc.wantTo == StateClosed {
				require.Equal(t, TriggerAuto, res.Trigger)
				require.Contains(t, res.Hash.Reason, "disabled")
				require.False(t, e.isOpenMember())
			}
		})
	}
}

func TestEvalReadModeNeverWrites(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	e.bucket(t0, 50, 0, 50, 1, 2, 3)
	res := e.eval(t0, defaultParams(), modeRead)
	require.False(t, res.Transitioned())
	require.Equal(t, Window{Total: 50, Success: 0, Risk: 50, CaptchaIdentities: 3}, res.Window)
	require.Empty(t, e.breaker())

	e.setBreaker("st", "open", "ou", strconv.FormatInt(t0.Add(-time.Second).UnixMilli(), 10), "v", "1")
	res = e.eval(t0, defaultParams(), modeRead)
	require.False(t, res.Transitioned())
	require.Equal(t, StateOpen, res.Hash.State)
	require.Equal(t, "1", e.breaker()["v"])
}

func TestEvalHealsOpenBreakersSet(t *testing.T) {
	t.Parallel()
	member := func(e *luaEnv) string { return strconv.FormatInt(e.egKey, 10) }

	t.Run("stale member of a closed breaker is removed", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.do(e.rdb.B().Sadd().Key(e.keys.OpenBreakers(e.siteKey)).Member(member(e)).Build())
		require.False(t, e.eval(t0, defaultParams(), modeRead).Transitioned())
		require.True(t, e.isOpenMember(), "read mode never writes")

		res := e.eval(t0, defaultParams(), modeEval)
		require.False(t, res.Transitioned())
		require.False(t, e.isOpenMember())
		require.Empty(t, e.breaker())
	})

	t.Run("missing member of an open breaker is added", func(t *testing.T) {
		t.Parallel()
		e := newLuaEnv(t)
		e.setBreaker("st", "open", "ou", strconv.FormatInt(t0.Add(time.Minute).UnixMilli(), 10), "v", "1")
		res := e.eval(t0, defaultParams(), modeEval)
		require.False(t, res.Transitioned())
		require.True(t, e.isOpenMember())
		require.Equal(t, "1", e.breaker()["v"])
	})
}

// hsEntry packs identity x endpoint state for seeding.
func hsEntry(nfail int64, cd int64) string {
	return "70.00|" + strconv.FormatInt(t0.UnixMilli(), 10) + "|10|" + strconv.FormatInt(nfail, 10) + "|" +
		strconv.FormatInt(t0.UnixMilli(), 10) + "|" + strconv.FormatInt(cd, 10) + "|0|0"
}

func (e *luaEnv) zadd(key string, score time.Time, member string) {
	e.t.Helper()
	e.do(e.rdb.B().Zadd().Key(key).ScoreMember().ScoreMember(float64(score.UnixMilli()), member).Build())
}

func (e *luaEnv) zscore(key, member string) (float64, bool) {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx(), e.rdb.B().Zscore().Key(key).Member(member).Build()).AsFloat64()
	if rueidis.IsRedisNil(err) {
		return 0, false
	}
	require.NoError(e.t, err)
	return v, true
}

func (e *luaEnv) members(key string) []string {
	e.t.Helper()
	m, err := e.do(e.rdb.B().Smembers().Key(key).Build()).AsStrSlice()
	require.NoError(e.t, err)
	return m
}

func (e *luaEnv) revert(mode policy.RevertMode, groups []int64) (int64, int64) {
	e.t.Helper()
	call := revertExec(e.keys, e.siteKey, e.egKey, t0, 60_000, mode, groups)
	ep, site, err := parseRevertResult(revertScript.Exec(e.ctx(), e.rdb, call.Keys, call.Args))
	require.NoError(e.t, err)
	return ep, site
}

func TestRevertEndpointCooldowns(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	cd := t0.Add(30 * time.Minute).UnixMilli()
	prevCd := t0.Add(5 * time.Minute).UnixMilli()
	hs := e.keys.Health(e.siteKey, e.egKey)
	rcd := e.keys.RecentCooldowns(e.siteKey, e.egKey)
	rdy := e.keys.Ready(e.siteKey, e.egKey)

	// Identity 1: cooldown applied in the window, fully reverted.
	e.hset(hs, "1", hsEntry(3, cd))
	e.zadd(rcd, t0.Add(-10*time.Second), "1|0|2")
	e.zadd(rdy, time.UnixMilli(cd), "1")
	// Identity 2: applied before the window, untouched.
	e.hset(hs, "2", hsEntry(4, cd))
	e.zadd(rcd, t0.Add(-2*time.Minute), "2|0|0")
	e.zadd(rdy, time.UnixMilli(cd), "2")
	// Identity 3: cooldown already lowered below the recorded value, streak reverted.
	e.hset(hs, "3", hsEntry(5, 100))
	e.zadd(rcd, t0.Add(-5*time.Second), "3|"+strconv.FormatInt(prevCd, 10)+"|1")
	// Identity 4: health entry gone.
	e.zadd(rcd, t0.Add(-5*time.Second), "4|0|0")
	// Identity 5: reverted to a pre-existing cooldown.
	e.hset(hs, "5", hsEntry(2, cd))
	e.zadd(rcd, t0.Add(-1*time.Second), "5|"+strconv.FormatInt(prevCd, 10)+"|2")
	e.zadd(rdy, time.UnixMilli(cd), "5")
	// A site cooldown record is ignored in endpoint mode.
	e.zadd(e.keys.RecentSiteCooldowns(e.siteKey), t0, "1|0")

	ep, site := e.revert(policy.RevertEndpoint, nil)
	require.Equal(t, int64(3), ep)
	require.Zero(t, site)

	get := func(i string) string {
		v, err := e.do(e.rdb.B().Hget().Key(hs).Field(i).Build()).ToString()
		require.NoError(t, err)
		return v
	}
	require.Equal(t, hsEntry(2, 0), get("1"))
	require.Equal(t, hsEntry(4, cd), get("2"))
	require.Equal(t, hsEntry(1, 100), get("3"))
	require.Equal(t, hsEntry(2, prevCd), get("5"))

	score, ok := e.zscore(rdy, "1")
	require.True(t, ok)
	require.Zero(t, score)
	score, _ = e.zscore(rdy, "2")
	require.Equal(t, float64(cd), score)
	score, _ = e.zscore(rdy, "5")
	require.Equal(t, float64(prevCd), score)
	_, ok = e.zscore(rdy, "3")
	require.False(t, ok, "rescoring never adds identities to the ready queue")

	remaining, err := e.do(e.rdb.B().Zrange().Key(rcd).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(t, err)
	require.Equal(t, []string{"2|0|0"}, remaining)
	require.ElementsMatch(t, []string{"e70:1", "e70:3", "e70:5"}, e.members(e.keys.Dirty(e.siteKey)))
	_, ok = e.zscore(e.keys.RecentSiteCooldowns(e.siteKey), "1|0")
	require.True(t, ok)
}

func TestRevertAllCooldowns(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	scd := t0.Add(time.Hour).UnixMilli()
	otherGroup := int64(71)
	rcds := e.keys.RecentSiteCooldowns(e.siteKey)

	e.hset(e.keys.Identity(e.siteKey, 1), "scd", strconv.FormatInt(scd, 10), "al", "0")
	e.zadd(rcds, t0.Add(-30*time.Second), "1|0")
	e.zadd(e.keys.Ready(e.siteKey, e.egKey), time.UnixMilli(scd), "1")
	e.zadd(e.keys.Ready(e.siteKey, otherGroup), time.UnixMilli(scd), "1")
	// Identity 2: current scd not above the recorded value.
	e.hset(e.keys.Identity(e.siteKey, 2), "scd", "5")
	e.zadd(rcds, t0.Add(-30*time.Second), "2|10")
	// Identity 3: outside the window.
	e.hset(e.keys.Identity(e.siteKey, 3), "scd", strconv.FormatInt(scd, 10))
	e.zadd(rcds, t0.Add(-5*time.Minute), "3|0")
	// Endpoint record in the same call.
	e.hset(e.keys.Health(e.siteKey, e.egKey), "4", hsEntry(1, scd))
	e.zadd(e.keys.RecentCooldowns(e.siteKey, e.egKey), t0, "4|0|0")

	ep, site := e.revert(policy.RevertAll, []int64{e.egKey, otherGroup})
	require.Equal(t, int64(1), ep)
	require.Equal(t, int64(1), site)

	scdOf := func(i int64) string {
		v, err := e.do(e.rdb.B().Hget().Key(e.keys.Identity(e.siteKey, i)).Field("scd").Build()).ToString()
		require.NoError(t, err)
		return v
	}
	require.Equal(t, "0", scdOf(1))
	require.Equal(t, "5", scdOf(2))
	require.Equal(t, strconv.FormatInt(scd, 10), scdOf(3))
	for _, g := range []int64{e.egKey, otherGroup} {
		score, ok := e.zscore(e.keys.Ready(e.siteKey, g), "1")
		require.True(t, ok)
		require.Zero(t, score)
	}
	require.ElementsMatch(t, []string{"g1", "e70:4"}, e.members(e.keys.Dirty(e.siteKey)))
	remaining, err := e.do(e.rdb.B().Zrange().Key(rcds).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(t, err)
	require.Equal(t, []string{"3|0"}, remaining)
}

func TestSitePausedScript(t *testing.T) {
	t.Parallel()
	e := newLuaEnv(t)
	meta := e.keys.SiteMeta(e.siteKey)
	n, err := sitePausedScript.Exec(e.ctx(), e.rdb, []string{meta}, []string{"1"}).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)
	exists, err := e.do(e.rdb.B().Exists().Key(meta).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, exists)

	e.hset(meta, "site", "sit_1")
	n, err = sitePausedScript.Exec(e.ctx(), e.rdb, []string{meta}, []string{"1"}).AsInt64()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	v, err := e.do(e.rdb.B().Hget().Key(meta).Field("paused").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "1", v)
}
