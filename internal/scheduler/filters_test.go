package scheduler

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

func TestAcquireFiltersPushScores(t *testing.T) {
	const i = int64(1)
	tests := []struct {
		name    string
		setup   func(f *fixture, now int64)
		removed bool
		want    func(now int64) int64
	}{
		{name: "banned state removed", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "st", "banned")
		}, removed: true},
		{name: "quarantined state removed", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "st", "quarantined")
		}, removed: true},
		{name: "missing identity hash removed", setup: func(f *fixture, now int64) {
			f.zadd(f.keys.Ready(testSiteKey, testWebGroup), 0, key(i))
		}, removed: true},
		{name: "endpoint cooldown", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup})
			f.health(testWebGroup, i, fmt.Sprintf("70|0|0|0|0|%d|0|0", now+5000))
		}, want: func(now int64) int64 { return now + 5000 }},
		{name: "endpoint reuse", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup})
			f.health(testWebGroup, i, fmt.Sprintf("70|0|0|0|0|0|%d|0", now+6000))
		}, want: func(now int64) int64 { return now + 6000 }},
		{name: "site cooldown", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "scd", key(now+7000))
		}, want: func(now int64) int64 { return now + 7000 }},
		{name: "site reuse", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "sru", key(now+8000))
		}, want: func(now int64) int64 { return now + 8000 }},
		{name: "exclusive lease elsewhere", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "al", "1", "xl", key(now+9000))
		}, want: func(now int64) int64 { return now + 9000 }},
		{name: "stale xl ignored when idle but concurrency counts", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "al", "1", "xl", key(now-1))
		}, want: func(now int64) int64 { return now + 5000 }},
		{name: "account cooldown", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "acc", "3")
			f.hset(f.keys.Account(testSiteKey, 3), "st", "active", "cd", key(now+11000))
		}, want: func(now int64) int64 { return now + 11000 }},
		{name: "account banned until", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "acc", "3")
			f.hset(f.keys.Account(testSiteKey, 3), "st", "banned", "bu", key(now+12000))
		}, want: func(now int64) int64 { return now + 12000 }},
		{name: "account banned permanently", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "acc", "3")
			f.hset(f.keys.Account(testSiteKey, 3), "st", "banned", "bu", "-1")
		}, want: func(now int64) int64 { return now + 600000 }},
		{name: "account ban awaiting expiry job", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "acc", "3")
			f.hset(f.keys.Account(testSiteKey, 3), "st", "banned", "bu", key(now-5))
		}, want: func(now int64) int64 { return now + 10000 }},
		{name: "account disabled", setup: func(f *fixture, now int64) {
			f.identity(i, 0, []int64{testWebGroup}, "acc", "3")
			f.hset(f.keys.Account(testSiteKey, 3), "st", "disabled")
		}, want: func(now int64) int64 { return now + 600000 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			now := f.nowMs()
			tc.setup(f, now)
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			requireReason(t, err, apperr.ReasonNoIdentityAvailable)
			if tc.removed {
				require.Equal(t, int64(-1), f.ready(testWebGroup, i))
				return
			}
			require.Equal(t, tc.want(now), f.ready(testWebGroup, i))
		})
	}
}

func TestAcquireConcurrencyLimits(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.MaxConcurrentLeases = 3
	rot.Rotation.Probe.MaxLeases = 2
	rot.Rotation.LeaseTTL = durationx.Duration(3 * time.Second)
	now := f.nowMs()
	f.identity(1, 0, []int64{testWebGroup})
	f.identity(2, 1, []int64{testWebGroup}, "st", "pending")

	var byIdentity = map[string]int{}
	for n := 0; n < 5; n++ {
		g := f.mustAcquire(AcquireRequest{})
		byIdentity[g.Lease.IdentityID]++
		if g.Lease.IdentityID == "idt_2" {
			require.True(t, g.Lease.Probe, "pending identities are probes")
			require.Equal(t, "0", f.lease(g.Lease.ID)["pr"], "only breaker probes set pr")
		}
	}
	require.Equal(t, map[string]int{"idt_1": 3, "idt_2": 2}, byIdentity)
	for _, rec := range f.rec.acquires {
		require.False(t, rec.Probe, "statistics mark breaker probes only")
	}
	require.Equal(t, "", f.idField(1, "xl"), "no exclusive lease with max_concurrent_leases > 1")
	require.Equal(t, now+3000, f.ready(testWebGroup, 1), "full identity is pushed by min(ttl, 5s)")
	require.Equal(t, now+3000, f.ready(testWebGroup, 2))

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)
}

func TestReuseIntervalAnchorsAndScopes(t *testing.T) {
	const ri = 30 * time.Second
	tests := []struct {
		name   string
		anchor string
		scope  string
	}{
		{name: "acquired endpoint", anchor: policy.ReuseAnchorAcquired, scope: policy.ReuseScopeEndpointGroup},
		{name: "released endpoint", anchor: policy.ReuseAnchorReleased, scope: policy.ReuseScopeEndpointGroup},
		{name: "acquired site", anchor: policy.ReuseAnchorAcquired, scope: policy.ReuseScopeSite},
		{name: "released site", anchor: policy.ReuseAnchorReleased, scope: policy.ReuseScopeSite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			for _, g := range f.site.GroupsByID {
				if g.Client != "web" {
					continue
				}
				rot := f.rotation(g)
				rot.Rotation.ReuseInterval = durationx.Duration(ri)
				rot.Rotation.ReuseAnchor = tc.anchor
				rot.Rotation.ReuseScope = tc.scope
			}
			f.identity(1, 0, []int64{testWebGroup, testSearchKey})
			start := f.nowMs()

			g := f.mustAcquire(AcquireRequest{})
			ls := f.lease(g.Lease.ID)
			require.Equal(t, anchorCode(tc.anchor), ls["ra"])
			require.Equal(t, scopeCode(tc.scope), ls["rs"])
			require.Equal(t, "30000", ls["ri"])

			f.advance(5 * time.Second)
			f.mustRelease(g.Lease.ID)
			anchorAt := start
			if tc.anchor == policy.ReuseAnchorReleased {
				anchorAt = start + 5000
			}
			hs := f.hget(f.keys.Health(testSiteKey, testWebGroup), "1")
			if tc.scope == policy.ReuseScopeSite {
				require.Equal(t, key(anchorAt+30000), f.idField(1, "sru"))
				require.Contains(t, hs, "|0|"+key(start)) // ru stays 0, lu = acquire time
			} else {
				require.Equal(t, "", f.idField(1, "sru"))
				require.Equal(t, fmt.Sprintf("70.00|%d|0|0|0|0|%d|%d", start, anchorAt+30000, start), hs)
			}
			require.Equal(t, anchorAt+30000, f.ready(testWebGroup, 1))

			// Still inside the interval.
			f.advance(10 * time.Second)
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			requireReason(t, err, apperr.ReasonNoIdentityAvailable)
			sg, err := f.acquire(f.tokenCtx(), AcquireRequest{EndpointGroup: "search"})
			if tc.scope == policy.ReuseScopeSite {
				requireReason(t, err, apperr.ReasonNoIdentityAvailable)
				require.Equal(t, anchorAt+30000, f.ready(testSearchKey, 1), "site scope applies to every group")
			} else {
				require.NoError(t, err, "endpoint scope does not block other groups")
				f.mustRelease(sg.Lease.ID)
			}

			f.advance(time.Duration(anchorAt+30000-f.nowMs()) * time.Millisecond)
			f.mustAcquire(AcquireRequest{})
		})
	}
}

func TestQuotaWindowsAndWarmup(t *testing.T) {
	t.Run("current window limit", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 2, Window: durationx.Duration(time.Minute)}}
		f.identity(1, 0, []int64{testWebGroup})
		now := f.nowMs()
		for n := 0; n < 2; n++ {
			g := f.mustAcquire(AcquireRequest{})
			f.mustRelease(g.Lease.ID)
		}
		q := f.hgetall(f.keys.Quota(testSiteKey, testWebGroup, 1))
		require.Equal(t, map[string]string{"60000:c": key(now / 60000), "60000:n": "2", "60000:p": "0"}, q)
		require.InDelta(t, 120000, f.pttl(f.keys.Quota(testSiteKey, testWebGroup, 1)), 2000)

		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		require.Equal(t, now+60000, f.ready(testWebGroup, 1), "pushed to the window end")

		// In the next window the previous count still weighs: at 30s the
		// estimate is 2*0.5 = 1 so one more request fits.
		f.advance(90 * time.Second)
		g := f.mustAcquire(AcquireRequest{})
		f.mustRelease(g.Lease.ID)
		q = f.hgetall(f.keys.Quota(testSiteKey, testWebGroup, 1))
		require.Equal(t, "1", q["60000:n"])
		require.Equal(t, "2", q["60000:p"])
		_, err = f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		// est = 2*(1-f) + 1 must drop to <= 1 → f >= 1 - (2-1-1)/2 = 1 → window end.
		require.Equal(t, now+120000, f.ready(testWebGroup, 1))
	})

	t.Run("sliding estimate pushes inside the window", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 2, Window: durationx.Duration(time.Minute)}}
		f.identity(1, 0, []int64{testWebGroup})
		now := f.nowMs()
		idx := now / 60000
		// Previous window saw 2 requests; 6 s into the current window.
		f.hset(f.keys.Quota(testSiteKey, testWebGroup, 1), "60000:c", key(idx-1), "60000:n", "2", "60000:p", "0")
		f.advance(6 * time.Second)
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		// 2*(1-f) + 0 + 1 <= 2 → f >= 0.5 → 30 s (+1 ms margin).
		require.Equal(t, idx*60000+30001, f.ready(testWebGroup, 1))
		f.advance(24*time.Second + 2*time.Millisecond)
		f.mustAcquire(AcquireRequest{})
	})

	t.Run("multiple windows take the latest push", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Quota = []policy.QuotaSpec{
			{Limit: 5, Window: durationx.Duration(time.Minute)},
			{Limit: 1, Window: durationx.Duration(time.Hour)},
		}
		f.identity(1, 0, []int64{testWebGroup})
		now := f.nowMs()
		g := f.mustAcquire(AcquireRequest{})
		f.mustRelease(g.Lease.ID)
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		hourIdx := now / 3600000
		require.Equal(t, (hourIdx+1)*3600000, f.ready(testWebGroup, 1))
		require.InDelta(t, 2*3600000, f.pttl(f.keys.Quota(testSiteKey, testWebGroup, 1)), 5000)
	})

	t.Run("warmup scales limits", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 4, Window: durationx.Duration(time.Minute)}}
		rot.Rotation.Warmup = policy.WarmupSpec{Duration: durationx.Duration(time.Hour), QuotaFactor: 0.5}
		now := f.nowMs()
		f.identity(1, 0, []int64{testWebGroup}, "act", key(now-1000))
		f.identity(2, 0, []int64{testWebGroup}, "act", key(now-2*3600000))
		counts := map[string]int{}
		for {
			g, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			if err != nil {
				requireReason(t, err, apperr.ReasonNoIdentityAvailable)
				break
			}
			counts[g.Lease.IdentityID]++
			f.mustRelease(g.Lease.ID)
		}
		require.Equal(t, map[string]int{"idt_1": 2, "idt_2": 4}, counts)
	})

	t.Run("warmup never blocks completely", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 1, Window: durationx.Duration(time.Minute)}}
		rot.Rotation.Warmup = policy.WarmupSpec{Duration: durationx.Duration(time.Hour), QuotaFactor: 0.1}
		f.identity(1, 0, []int64{testWebGroup}, "act", key(f.nowMs()-10))
		f.mustAcquire(AcquireRequest{})
	})
}

func TestSelectionStrategies(t *testing.T) {
	now := testEpoch.UnixMilli()
	type ident struct {
		i     int64
		score float64
		lu    int64
		state string
	}
	idents := []ident{{i: 5, score: 10, lu: now - 1000}, {i: 3, score: 50, lu: now - 5000}, {i: 9, score: 90, lu: now - 3000}}
	tests := []struct {
		name     string
		strategy string
		idents   []ident
		randoms  []float64
		rr       string
		want     string
	}{
		{name: "weighted first", strategy: policy.StrategyWeightedRandom, idents: idents, randoms: []float64{0, 0}, want: "idt_5"},
		{name: "weighted middle", strategy: policy.StrategyWeightedRandom, idents: idents, randoms: []float64{0.4, 0.1}, want: "idt_3"},
		{name: "weighted last", strategy: policy.StrategyWeightedRandom, idents: idents, randoms: []float64{0.9, 0.5}, want: "idt_9"},
		{name: "weighted floor of 5", strategy: policy.StrategyWeightedRandom,
			idents: []ident{{i: 1, score: 0}, {i: 2, score: 5}}, randoms: []float64{0, 0.001}, want: "idt_1"},
		{name: "weighted pending factor", strategy: policy.StrategyWeightedRandom,
			idents: []ident{{i: 1, score: 70, state: "pending"}, {i: 2, score: 70}}, randoms: []float64{0, 0.1, 0.5, 0.9, 0.1}, want: "idt_2"},
		{name: "weighted rejections fall back to roulette", strategy: policy.StrategyWeightedRandom, idents: idents,
			randoms: []float64{0, 0.99, 0, 0.99, 0, 0.99, 0, 0.99, 0, 0.99, 0, 0.99, 0, 0.99, 0, 0.99, 0.2}, want: "idt_3"},
		{name: "weighted pending picked", strategy: policy.StrategyWeightedRandom,
			idents: []ident{{i: 1, score: 70, state: "pending"}, {i: 2, score: 70}}, randoms: []float64{0, 0.1, 0.05}, want: "idt_1"},
		{name: "least recently used", strategy: policy.StrategyLeastRecentlyUsed, idents: idents, want: "idt_3"},
		{name: "best health", strategy: policy.StrategyBestHealth, idents: idents, want: "idt_9"},
		{name: "round robin after cursor", strategy: policy.StrategyRoundRobin, idents: idents, rr: "3", want: "idt_5"},
		{name: "round robin wraps", strategy: policy.StrategyRoundRobin, idents: idents, rr: "9", want: "idt_3"},
		{name: "round robin without cursor", strategy: policy.StrategyRoundRobin, idents: idents, want: "idt_3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rot := f.rotation(f.web)
			rot.Rotation.Strategy = tc.strategy
			for n, id := range tc.idents {
				state := id.state
				if state == "" {
					state = "active"
				}
				f.identity(id.i, int64(n), []int64{testWebGroup}, "st", state)
				f.health(testWebGroup, id.i, fmt.Sprintf("%.2f|%d|10|0|0|0|0|%d", id.score, now, id.lu))
			}
			if tc.rr != "" {
				require.NoError(t, f.do(f.rdb.B().Set().Key(f.keys.RoundRobin(testSiteKey, testWebGroup)).Value(tc.rr).Build()).Error())
			}
			if tc.randoms != nil {
				f.setRandoms(append(tc.randoms, 0, 0, 0)...)
			}
			g := f.mustAcquire(AcquireRequest{})
			require.Equal(t, tc.want, g.Lease.IdentityID)
			if tc.strategy == policy.StrategyRoundRobin {
				require.Equal(t, g.Lease.IdentityID[len("idt_"):], f.get(f.keys.RoundRobin(testSiteKey, testWebGroup)))
			}
		})
	}
}

func TestWeightedRandomDistribution(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.MaxConcurrentLeases = 10000
	now := f.nowMs()
	// Weights: pending 70^2*0.1 = 490, active 70^2 = 4900, active 35^2 = 1225.
	f.identity(1, 0, []int64{testWebGroup}, "st", "pending")
	f.health(testWebGroup, 1, fmt.Sprintf("70|%d|10|0|0|0|0|0", now))
	f.identity(2, 0, []int64{testWebGroup})
	f.health(testWebGroup, 2, fmt.Sprintf("70|%d|10|0|0|0|0|0", now))
	f.identity(3, 0, []int64{testWebGroup})
	f.health(testWebGroup, 3, fmt.Sprintf("35|%d|10|0|0|0|0|0", now))
	rng := rand.New(rand.NewPCG(1, 2))
	f.svc.random = rng.Float64
	rot.Rotation.Probe.MaxLeases = 10000

	const trials = 1500
	counts := map[string]int{}
	for n := 0; n < trials; n++ {
		g := f.mustAcquire(AcquireRequest{})
		counts[g.Lease.IdentityID]++
	}
	total := 490.0 + 4900 + 1225
	require.InDelta(t, 490/total, float64(counts["idt_1"])/trials, 0.03)
	require.InDelta(t, 4900/total, float64(counts["idt_2"])/trials, 0.04)
	require.InDelta(t, 1225/total, float64(counts["idt_3"])/trials, 0.04)
}

func TestWeightedRandomUsesDecayedScore(t *testing.T) {
	f := newFixture(t)
	now := f.nowMs()
	tau := policy.DefaultHealthTau.Milliseconds()
	// Identity 1 had score 100 one tau ago: decays to 70 + 30/e ≈ 81.04.
	f.identity(1, 0, []int64{testWebGroup})
	f.health(testWebGroup, 1, fmt.Sprintf("100|%d|10|0|0|0|0|0", now-tau))
	f.identity(2, 1, []int64{testWebGroup})
	f.health(testWebGroup, 2, fmt.Sprintf("81|%d|10|0|0|0|0|0", now))
	rot := f.rotation(f.web)
	rot.Rotation.Strategy = policy.StrategyBestHealth
	g := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "idt_1", g.Lease.IdentityID, "decayed %.2f beats 81", 70+30/math.E)
}

func TestStickySessions(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.Sticky = policy.StickySpec{Enabled: true, TTL: durationx.Duration(10 * time.Minute)}
	rot.Rotation.ReuseInterval = durationx.Duration(time.Hour)
	f.identity(1, 0, []int64{testWebGroup})
	f.identity(2, 1, []int64{testWebGroup})
	f.setRandoms(0, 0, 0, 0)

	g := f.mustAcquire(AcquireRequest{SessionKey: "task-1"})
	require.False(t, g.Lease.Sticky)
	require.Equal(t, "idt_1", g.Lease.IdentityID)
	stk := f.keys.Sticky(testSiteKey, testWebGroup, "task-1")
	require.Equal(t, "1", f.get(stk))
	require.InDelta(t, 600000, f.pttl(stk), 2000)
	f.mustRelease(g.Lease.ID)

	// The reuse interval is bypassed for sticky reuse.
	g = f.mustAcquire(AcquireRequest{SessionKey: "task-1"})
	require.True(t, g.Lease.Sticky)
	require.Equal(t, "idt_1", g.Lease.IdentityID)

	// While leased (exclusive) the sticky identity is not reused.
	g2 := f.mustAcquire(AcquireRequest{SessionKey: "task-1"})
	require.False(t, g2.Lease.Sticky)
	require.Equal(t, "idt_2", g2.Lease.IdentityID)
	require.Equal(t, "2", f.get(stk), "the session is rebound to the new identity")
	f.mustRelease(g.Lease.ID)
	f.mustRelease(g2.Lease.ID)

	// A cooldown breaks stickiness too.
	f.health(testWebGroup, 2, fmt.Sprintf("70|0|0|0|0|%d|0|0", f.nowMs()+60000))
	f.hset(f.keys.Health(testSiteKey, testWebGroup), "1", "70|0|0|0|0|0|0|0")
	f.zadd(f.keys.Ready(testSiteKey, testWebGroup), 0, "1")
	g = f.mustAcquire(AcquireRequest{SessionKey: "task-1"})
	require.False(t, g.Lease.Sticky)
	require.Equal(t, "idt_1", g.Lease.IdentityID)
	f.mustRelease(g.Lease.ID)

	// Long session keys are hashed.
	longKey := "task/" + fmt.Sprintf("%0100d", 7)
	f.zadd(f.keys.Ready(testSiteKey, testWebGroup), 0, "1")
	f.hset(f.keys.Health(testSiteKey, testWebGroup), "1", "70|0|0|0|0|0|0|0")
	g = f.mustAcquire(AcquireRequest{SessionKey: longKey})
	require.Equal(t, "1", f.get(f.keys.Sticky(testSiteKey, testWebGroup, redis.NormalizeSessionKey(longKey))))
	require.Len(t, f.lease(g.Lease.ID)["sk"], 32)

	// Batches ignore stickiness.
	f.mustRelease(g.Lease.ID)
	for _, i := range []int64{1, 2} {
		f.health(testWebGroup, i, "70|0|0|0|0|0|0|0")
		f.zadd(f.keys.Ready(testSiteKey, testWebGroup), 0, key(i))
	}
	grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", SessionKey: "task-9", Count: 2})
	require.NoError(t, err)
	require.NotEmpty(t, grants)
	require.Equal(t, "", f.get(f.keys.Sticky(testSiteKey, testWebGroup, "task-9")))
}
