package scheduler

import (
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

func TestBreakerGate(t *testing.T) {
	brk := func(f *fixture) string { return f.keys.Breaker(testSiteKey, testWebGroup) }

	t.Run("open until", func(t *testing.T) {
		f := newFixture(t)
		f.identity(1, 0, []int64{testWebGroup})
		f.hset(brk(f), "st", "open", "ou", key(f.nowMs()+45000), "man", "0", "v", "3")
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 100 * time.Millisecond})
		e := requireAppErr(t, err, apperr.ReasonCircuitOpen)
		require.Equal(t, connect.CodeUnavailable, e.Code)
		require.Equal(t, int64(45000), e.RetryAfterMs)
		require.Equal(t, []string{ResultCircuitOpen}, f.rec.acquireResults())
		require.Equal(t, "0", f.idField(1, "al"))
	})

	t.Run("manual indefinite", func(t *testing.T) {
		f := newFixture(t)
		f.identity(1, 0, []int64{testWebGroup})
		f.hset(brk(f), "st", "open", "ou", "0", "man", "1")
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		e := requireAppErr(t, err, apperr.ReasonCircuitOpen)
		require.Equal(t, int64(60000), e.RetryAfterMs)
	})

	t.Run("lazy half-open transition issues one best-health probe", func(t *testing.T) {
		f := newFixture(t)
		now := f.nowMs()
		f.identity(1, 0, []int64{testWebGroup})
		f.health(testWebGroup, 1, fmt.Sprintf("40|%d|10|0|0|0|0|0", now))
		f.identity(2, 1, []int64{testWebGroup})
		f.health(testWebGroup, 2, fmt.Sprintf("95|%d|10|0|0|0|0|0", now))
		f.identity(3, 2, []int64{testWebGroup})
		f.hset(brk(f), "st", "open", "ou", key(now-1), "man", "0", "v", "3", "ps", "4", "pk", "2")
		f.setRandoms(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)

		grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 3})
		require.NoError(t, err)
		require.Len(t, grants, 1, "half-open issues a single probe")
		require.Equal(t, "idt_2", grants[0].Lease.IdentityID, "probes use best_health")
		require.True(t, grants[0].Lease.Probe)
		require.Equal(t, "1", f.lease(grants[0].Lease.ID)["pr"])
		b := f.hgetall(brk(f))
		require.Equal(t, "half_open", b["st"])
		require.Equal(t, key(now), b["hw"])
		require.Equal(t, "1", b["hc"])
		require.Equal(t, "0", b["ps"])
		require.Equal(t, "0", b["pk"])
		require.Equal(t, "4", b["v"])
		require.Equal(t, []int64{testWebGroup}, f.halfOpen)
		require.True(t, f.rec.acquires[0].Probe)
	})

	t.Run("probe window limit and reset", func(t *testing.T) {
		f := newFixture(t)
		now := f.nowMs()
		for i := int64(1); i <= 3; i++ {
			f.identity(i, i, []int64{testWebGroup})
		}
		f.hset(brk(f), "st", "half_open", "hw", key(now-4000), "hc", "4", "v", "9")
		f.mustAcquire(AcquireRequest{})
		require.Equal(t, "5", f.hget(brk(f), "hc"))

		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		e := requireAppErr(t, err, apperr.ReasonCircuitOpen)
		require.Equal(t, int64(6000), e.RetryAfterMs)

		f.advance(6 * time.Second)
		g := f.mustAcquire(AcquireRequest{})
		require.True(t, g.Lease.Probe)
		b := f.hgetall(brk(f))
		require.Equal(t, key(f.nowMs()), b["hw"])
		require.Equal(t, "1", b["hc"])
		require.Equal(t, "9", b["v"], "window resets are not transitions")
		require.Empty(t, f.halfOpen)
	})

	t.Run("custom probe limit", func(t *testing.T) {
		f := newFixture(t)
		p := policy.Default(policy.KindBreaker).(*policy.BreakerSpec)
		p.HalfOpen.ProbeLeasesPer10s = 1
		f.web.Breaker = p
		for i := int64(1); i <= 2; i++ {
			f.identity(i, i, []int64{testWebGroup})
		}
		f.hset(brk(f), "st", "half_open", "hw", key(f.nowMs()), "hc", "0")
		f.mustAcquire(AcquireRequest{})
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonCircuitOpen)
	})

	t.Run("probes bypass sticky sessions", func(t *testing.T) {
		f := newFixture(t)
		rot := f.rotation(f.web)
		rot.Rotation.Sticky = policy.StickySpec{Enabled: true, TTL: durationx.Duration(10 * time.Minute)}
		now := f.nowMs()
		f.identity(1, 0, []int64{testWebGroup})
		f.health(testWebGroup, 1, fmt.Sprintf("20|%d|10|0|0|0|0|0", now))
		f.identity(2, 1, []int64{testWebGroup})
		f.health(testWebGroup, 2, fmt.Sprintf("95|%d|10|0|0|0|0|0", now))
		stk := f.keys.Sticky(testSiteKey, testWebGroup, "s1")
		require.NoError(t, f.do(f.rdb.B().Set().Key(stk).Value("1").Build()).Error())
		f.hset(brk(f), "st", "half_open", "hw", key(now), "hc", "0")

		g := f.mustAcquire(AcquireRequest{SessionKey: "s1"})
		require.Equal(t, "idt_2", g.Lease.IdentityID, "the probe goes to the healthiest identity")
		require.False(t, g.Lease.Sticky)
		require.True(t, g.Lease.Probe)
		require.Equal(t, "1", f.get(stk), "a probe does not rebind the session")
	})

	t.Run("closed breaker is transparent", func(t *testing.T) {
		f := newFixture(t)
		f.identity(1, 0, []int64{testWebGroup})
		f.hset(brk(f), "st", "closed", "v", "2")
		g := f.mustAcquire(AcquireRequest{})
		require.False(t, g.Lease.Probe)
	})
}

// poolFixture installs a pool-mode rotation on the web default group.
func poolFixture(t *testing.T, mode string, edit func(px *policy.ProxySpec)) *fixture {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Proxy.Mode = mode
	if edit != nil {
		edit(&rot.Proxy)
	}
	return f
}

func TestProxyPoolFilters(t *testing.T) {
	f := poolFixture(t, policy.ProxyModePool, func(px *policy.ProxySpec) {
		px.Kinds = []string{policy.ProxyKindResidential}
		px.Providers = []string{"acme"}
		px.Regions = []string{"US", "DE"}
		px.Tags = []string{"premium", "fast"}
	})
	now := f.nowMs()
	f.identity(1, 0, []int64{testWebGroup})
	f.identity(2, 1, []int64{testWebGroup})
	base := []string{"kd", "residential", "pv", "acme", "rg", "US", "tg", ",premium,fast,slow,"}
	with := func(kv ...string) []string { return append(append([]string(nil), base...), kv...) }
	f.proxy(10, 0, with("kd", "datacenter")...)
	f.proxy(11, 0, with("pv", "other")...)
	f.proxy(12, 0, with("rg", "FR")...)
	f.proxy(13, 0, with("tg", ",premium,")...)
	f.proxy(14, 0, with("st", "dead")...)
	f.proxy(15, 0, with("al", "1", "mc", "1")...)
	f.proxy(16, 0, with("cd", key(now+30000))...)
	f.proxy(17, 0, with("gcd", key(now+40000))...)
	f.proxy(20, 0, base...)

	f.setRandoms(0, 0, 0, 0)
	g := f.mustAcquire(AcquireRequest{})
	require.NotNil(t, g.Proxy)
	require.Equal(t, "pxy_20", g.Proxy.ID)
	require.Equal(t, "http://proxy.invalid/pxy_20", g.Proxy.URL)
	ls := f.lease(g.Lease.ID)
	require.Equal(t, "20", ls["p"])
	require.Equal(t, "pxy_20", ls["pid"])
	require.Equal(t, "1", f.hget(f.keys.ProxySite(testSiteKey, 20), "al"))
	require.Equal(t, atoi64(ls["x"]), f.zscore(f.keys.ProxyReady(testSiteKey), "20"), "full single-slot proxy pushed to lease expiry")
	require.Equal(t, now+30000, f.zscore(f.keys.ProxyReady(testSiteKey), "16"), "cooling proxy pushed")
	require.Equal(t, now+40000, f.zscore(f.keys.ProxyReady(testSiteKey), "17"))
	require.Equal(t, int64(0), f.zscore(f.keys.ProxyReady(testSiteKey), "10"), "filter mismatches keep their score")
	require.Equal(t, int64(-1), f.zscore(f.keys.ProxyReady(testSiteKey), "14"), "inactive proxy removed from pxrdy")
	require.Equal(t, now+5000, f.zscore(f.keys.ProxyReady(testSiteKey), "15"), "full proxy pushed by min(ttl, 5s)")

	// The only matching proxy is busy: no proxy, and no lease is written.
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	e := requireAppErr(t, err, apperr.ReasonNoProxyAvailable)
	require.Equal(t, connect.CodeResourceExhausted, e.Code)
	require.Equal(t, "0", f.idField(2, "al"))
	require.Equal(t, ResultNoProxy, f.rec.acquireResults()[1])

	// Releasing frees the proxy and restores its availability.
	f.mustRelease(g.Lease.ID)
	require.Equal(t, "0", f.hget(f.keys.ProxySite(testSiteKey, 20), "al"))
	require.Equal(t, now, f.zscore(f.keys.ProxyReady(testSiteKey), "20"))
	g = f.mustAcquire(AcquireRequest{})
	require.Equal(t, "pxy_20", g.Proxy.ID)
}

func TestProxyPoolNoProxyRetryAndResolverFailure(t *testing.T) {
	f := poolFixture(t, policy.ProxyModePool, nil)
	f.identity(1, 0, []int64{testWebGroup})

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	e := requireAppErr(t, err, apperr.ReasonNoProxyAvailable)
	require.Equal(t, int64(60000), e.RetryAfterMs, "empty proxy set")

	f.proxy(5, f.nowMs()+2500)
	_, err = f.acquire(f.tokenCtx(), AcquireRequest{})
	e = requireAppErr(t, err, apperr.ReasonNoProxyAvailable)
	require.Equal(t, int64(2500), e.RetryAfterMs)

	f.proxy(6, 0)
	f.proxies.fail["pxy_6"] = true
	_, err = f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
	require.Equal(t, "0", f.idField(1, "al"))
	require.Equal(t, "0", f.hget(f.keys.ProxySite(testSiteKey, 6), "al"))
}

func TestProxyPoolWindowsSkipUnusableProxies(t *testing.T) {
	t.Run("sequential windows account for pushed proxies", func(t *testing.T) {
		f := poolFixture(t, policy.ProxyModePool, nil)
		f.identity(1, 0, []int64{testWebGroup})
		now := f.nowMs()
		// The first window holds only cooling proxies; pushing them shifts
		// the usable proxies to the front of the due range.
		for p := int64(10); p < 26; p++ {
			f.proxy(p, 0, "cd", key(now+60000))
		}
		for p := int64(30); p < 34; p++ {
			f.proxy(p, 1)
		}
		g, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		require.NoError(t, err)
		require.NotNil(t, g.Proxy)
		require.Contains(t, []string{"pxy_30", "pxy_31", "pxy_32", "pxy_33"}, g.Proxy.ID)
		for p := int64(10); p < 26; p++ {
			require.Equal(t, now+60000, f.zscore(f.keys.ProxyReady(testSiteKey), key(p)))
		}
	})

	t.Run("full and inactive proxies leave the due range", func(t *testing.T) {
		f := poolFixture(t, policy.ProxyModePool, nil)
		f.identity(1, 0, []int64{testWebGroup})
		now := f.nowMs()
		for p := int64(100); p < 140; p++ {
			f.proxy(p, 0, "mc", "5", "al", "5")
		}
		for p := int64(140); p < 160; p++ {
			f.proxy(p, 0, "st", "disabled")
		}
		f.proxy(900, 1, "mc", "5")
		// A random window sees at most 48 of the 61 due proxies, but every
		// unusable proxy it sees is pushed or removed, so a retry succeeds.
		var g *Grant
		for attempt := 0; attempt < 3 && g == nil; attempt++ {
			var err error
			g, err = f.acquire(f.tokenCtx(), AcquireRequest{})
			if err != nil {
				requireReason(t, err, apperr.ReasonNoProxyAvailable)
			}
		}
		require.NotNil(t, g)
		require.Equal(t, "pxy_900", g.Proxy.ID)
		for p := int64(100); p < 140; p++ {
			if s := f.zscore(f.keys.ProxyReady(testSiteKey), key(p)); s != 0 {
				require.Equal(t, now+5000, s, "proxy %d", p)
			}
		}
		for p := int64(140); p < 160; p++ {
			if s := f.zscore(f.keys.ProxyReady(testSiteKey), key(p)); s != 0 {
				require.Equal(t, int64(-1), s, "proxy %d", p)
			}
		}
	})
}

func TestProxyPoolLargeSetAndWeights(t *testing.T) {
	f := poolFixture(t, policy.ProxyModePool, func(px *policy.ProxySpec) {
		px.Tags = []string{"gold"}
	})
	f.identity(1, 0, []int64{testWebGroup})
	// 60 non-matching proxies and one matching proxy with a high member hkey.
	for p := int64(100); p < 160; p++ {
		f.proxy(p, 0)
	}
	f.proxy(900, 0, "tg", ",gold,", "mc", "10", "sc", "90")
	// Identity pick (proposal 1, accepted), then a proxy window at offset
	// floor(0.99*46) = 45, which covers the 61st member.
	f.setRandoms(0, 0, 0.99, 0.5)
	g, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	require.NoError(t, err, "random windows reach proxies beyond the first 48")
	require.Equal(t, "pxy_900", g.Proxy.ID)
}

func TestProxyRegionMatch(t *testing.T) {
	for _, mode := range []string{policy.ProxyModeRegionMatch, policy.ProxyModePool} {
		t.Run(mode, func(t *testing.T) {
			f := poolFixture(t, mode, func(px *policy.ProxySpec) { px.RegionMatch = true })
			f.identity(1, 0, []int64{testWebGroup}, "rg", "JP")
			f.proxy(1, 0, "rg", "US", "mc", "5")
			f.proxy(2, 0, "rg", "JP", "mc", "5")
			for n := 0; n < 3; n++ {
				f.setRandoms(0, 0, 0, 0)
				g := f.mustAcquire(AcquireRequest{})
				require.Equal(t, "pxy_2", g.Proxy.ID)
				f.mustRelease(g.Lease.ID)
			}
			f.hset(f.keys.Identity(testSiteKey, 1), "rg", "BR")
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			requireReason(t, err, apperr.ReasonNoProxyAvailable)
		})
	}
}

func TestProxyBindIdentity(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		f := poolFixture(t, policy.ProxyModeBindIdentity, func(px *policy.ProxySpec) {
			px.RebindTolerance = durationx.Duration(5 * time.Minute)
			px.MaxRebindsPerDay = 2
		})
		f.identity(1, 0, []int64{testWebGroup})
		return f
	}
	today := testEpoch.UTC().Format(rebindDayLayout)

	t.Run("first binding then reuse", func(t *testing.T) {
		f := setup(t)
		f.proxy(7, 0, "mc", "3")
		g := f.mustAcquire(AcquireRequest{})
		require.Equal(t, "pxy_7", g.Proxy.ID)
		require.Equal(t, "7", f.idField(1, "px"))
		require.Equal(t, "", f.idField(1, "rbn"), "first binding is not a rebind")
		require.Len(t, f.bindings, 1)
		require.False(t, f.bindings[0].Rebound)
		require.Equal(t, "pxy_7", f.bindings[0].ProxyID)
		require.Equal(t, int64(1), f.bindings[0].IdentityKey)
		f.mustRelease(g.Lease.ID)

		f.proxy(8, 0, "mc", "3", "sc", "100")
		g = f.mustAcquire(AcquireRequest{})
		require.Equal(t, "pxy_7", g.Proxy.ID, "bound proxy is reused")
		require.Len(t, f.bindings, 1)
	})

	t.Run("dead proxy rebinds and counts", func(t *testing.T) {
		f := setup(t)
		f.identity(1, 0, []int64{testWebGroup}, "px", "7", "rbd", "20000101", "rbn", "9")
		f.proxy(7, 0, "st", "dead")
		f.proxy(8, 0)
		g := f.mustAcquire(AcquireRequest{})
		require.Equal(t, "pxy_8", g.Proxy.ID)
		require.Equal(t, "8", f.idField(1, "px"))
		require.Equal(t, today, f.idField(1, "rbd"))
		require.Equal(t, "1", f.idField(1, "rbn"), "a new day resets the counter")
		require.True(t, f.bindings[0].Rebound)
		require.Equal(t, 1, f.bindings[0].RebindsToday)
		require.True(t, f.sismember(f.keys.Dirty(testSiteKey), "g1"))
	})

	t.Run("rebind never reuses the old proxy", func(t *testing.T) {
		f := setup(t)
		f.identity(1, 0, []int64{testWebGroup}, "px", "7")
		f.proxy(7, 0, "cd", key(f.nowMs()+time.Hour.Milliseconds()))
		f.zadd(f.keys.ProxyReady(testSiteKey), 0, "7")
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoProxyAvailable)
	})

	t.Run("daily rebind limit", func(t *testing.T) {
		f := setup(t)
		f.identity(1, 0, []int64{testWebGroup}, "px", "7", "rbd", today, "rbn", "2")
		f.proxy(7, 0, "st", "banned")
		f.proxy(8, 0)
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		require.Equal(t, f.nowMs()+600000, f.ready(testWebGroup, 1))
	})

	t.Run("short cooldown waits within tolerance", func(t *testing.T) {
		f := setup(t)
		now := f.nowMs()
		f.identity(1, 0, []int64{testWebGroup}, "px", "7")
		f.proxy(7, 0, "cd", key(now+60000), "gcd", key(now+90000))
		f.proxy(8, 0)
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		require.Equal(t, now+90000, f.ready(testWebGroup, 1))
	})

	t.Run("long cooldown rebinds", func(t *testing.T) {
		f := setup(t)
		now := f.nowMs()
		f.identity(1, 0, []int64{testWebGroup}, "px", "7")
		f.proxy(7, 0, "cd", key(now+10*60000))
		f.proxy(8, 0)
		g := f.mustAcquire(AcquireRequest{})
		require.Equal(t, "pxy_8", g.Proxy.ID)
		require.Equal(t, "1", f.idField(1, "rbn"))
	})

	t.Run("busy bound proxy pushes briefly", func(t *testing.T) {
		f := setup(t)
		f.identity(1, 0, []int64{testWebGroup}, "px", "7")
		f.proxy(7, 0, "al", "1", "mc", "1")
		f.proxy(8, 0)
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
		requireReason(t, err, apperr.ReasonNoIdentityAvailable)
		require.Equal(t, f.nowMs()+5000, f.ready(testWebGroup, 1))
	})

	t.Run("batch shares a bound proxy up to its capacity", func(t *testing.T) {
		f := setup(t)
		f.identity(2, 1, []int64{testWebGroup}, "px", "7")
		f.hset(f.keys.Identity(testSiteKey, 1), "px", "7")
		f.proxy(7, 0, "mc", "1")
		grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 2})
		require.NoError(t, err)
		require.Len(t, grants, 1)
		require.Equal(t, "1", f.hget(f.keys.ProxySite(testSiteKey, 7), "al"))
	})
}

func TestStickyFallsBackWhenItsProxyIsUnavailable(t *testing.T) {
	f := poolFixture(t, policy.ProxyModeRegionMatch, nil)
	rot := f.web.Rotation
	rot.Rotation.Sticky = policy.StickySpec{Enabled: true, TTL: durationx.Duration(time.Minute)}
	f.identity(1, 0, []int64{testWebGroup}, "rg", "BR")
	f.identity(2, 1, []int64{testWebGroup}, "rg", "JP")
	f.proxy(5, 0, "rg", "JP", "mc", "5")
	stk := f.keys.Sticky(testSiteKey, testWebGroup, "s1")
	require.NoError(t, f.do(f.rdb.B().Set().Key(stk).Value("1").Build()).Error())

	g := f.mustAcquire(AcquireRequest{SessionKey: "s1"})
	require.Equal(t, "idt_2", g.Lease.IdentityID)
	require.False(t, g.Lease.Sticky)
	require.Equal(t, "pxy_5", g.Proxy.ID)
	require.Equal(t, "2", f.get(stk))
}
