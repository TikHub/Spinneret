package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

func TestAcquireWritesLeaseState(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.LeaseTTL = durationx.Duration(20 * time.Second)
	rot.Rotation.MaxLeaseLifetime = durationx.Duration(60 * time.Second)
	f.identity(21, 0, []int64{testWebGroup, testSearchKey})
	now := f.nowMs()

	g := f.mustAcquire(AcquireRequest{SessionKey: "task-1"})
	require.Equal(t, "idt_21", g.Lease.IdentityID)
	require.Equal(t, testTypeName, g.Lease.IdentityType)
	require.Equal(t, "_default", g.Lease.EndpointGroup)
	require.Equal(t, time.UnixMilli(now+20000), g.Lease.ExpiresAt)
	require.False(t, g.Lease.Sticky)
	require.False(t, g.Lease.Probe)
	require.Nil(t, g.Proxy)
	require.Equal(t, 5*time.Second, g.RenewBefore)
	require.Equal(t, "sid=idt_21", g.Credential.CookieHeader)
	require.Equal(t, 3, g.Credential.Values["pv"])

	ref, err := idgen.ParseLeaseID(g.Lease.ID)
	require.NoError(t, err)
	require.Equal(t, testSiteKey, ref.SiteKey)
	require.Equal(t, 21%testShards, ref.Shard)
	require.Equal(t, fmt.Sprintf("%02x", 21%testShards), g.Lease.ID[len(g.Lease.ID)-2:])

	ls := f.lease(g.Lease.ID)
	require.Equal(t, map[string]string{
		"i": "21", "iid": "idt_21", "e": key(testWebGroup), "p": "", "pid": "", "n": "node-1", "tk": "tok_1",
		"ns": "ns_1", "sk": "task-1", "a": key(now), "x": key(now + 20000), "cap": key(now + 60000),
		"ttl": "20000", "st": "active", "pr": "0", "mc": "1", "ri": "0", "ra": "r", "rs": "e", "rc": "0",
	}, ls)
	require.Equal(t, int64(-1), f.pttl(f.keys.Lease(testSiteKey, g.Lease.ID)), "active lease hashes have no TTL")
	require.Equal(t, now+20000, f.zscore(f.keys.LeaseExpiry(testSiteKey), g.Lease.ID))

	require.Equal(t, "1", f.idField(21, "al"))
	require.Equal(t, key(now+20000), f.idField(21, "xl"))
	require.Equal(t, key(now), f.idField(21, "lu"))
	hs := f.hget(f.keys.Health(testSiteKey, testWebGroup), "21")
	require.Equal(t, fmt.Sprintf("70.00|%d|0|0|0|0|0|%d", now, now), hs)
	require.Equal(t, now+20000, f.ready(testWebGroup, 21), "exclusive lease pushes to expiry")
	require.Equal(t, int64(0), f.ready(testSearchKey, 21), "other groups are corrected lazily")
	require.Equal(t, now, f.zscore(f.keys.ActiveGroups(testSiteKey), key(testWebGroup)))
	require.True(t, f.sismember(f.keys.Dirty(testSiteKey), "e7000:21"))
	require.True(t, f.sismember(f.keys.Dirty(testSiteKey), "g21"))
	require.Equal(t, "", f.get(f.keys.Sticky(testSiteKey, testWebGroup, "task-1")), "sticky disabled by policy")

	require.Equal(t, []string{ResultOK}, f.rec.acquireResults())
	var m dto.Metric
	require.NoError(t, f.metrics.AcquireTotal.WithLabelValues("demo", "_default", ResultOK).Write(&m))
	require.Equal(t, 1.0, m.GetCounter().GetValue())
	rec := f.rec.acquires[0]
	require.Equal(t, "ity_1", rec.IdentityTypeID)
	require.Equal(t, g.Lease.ID, rec.LeaseID)
	require.Equal(t, "sit_1", rec.SiteID)
	require.Equal(t, "web", rec.Client)
	require.Equal(t, "node-1", rec.Node)
}

func TestAcquireExhaustedRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
		want  int64
	}{
		{name: "empty ready set", setup: func(*fixture) {}, want: 60000},
		{name: "future identity", setup: func(f *fixture) {
			f.identity(1, f.nowMs()+1234, []int64{testWebGroup})
		}, want: 1234},
		{name: "clamped low", setup: func(f *fixture) {
			f.identity(1, f.nowMs()+10, []int64{testWebGroup})
		}, want: 50},
		{name: "clamped high", setup: func(f *fixture) {
			f.identity(1, f.nowMs()+100000, []int64{testWebGroup})
		}, want: 60000},
		{name: "cooldown pushes to avail", setup: func(f *fixture) {
			f.identity(1, 0, []int64{testWebGroup})
			f.health(testWebGroup, 1, fmt.Sprintf("50|0|0|0|0|%d|0|0", f.nowMs()+4000))
		}, want: 4000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			e := requireAppErr(t, err, apperr.ReasonNoIdentityAvailable)
			require.Equal(t, connect.CodeResourceExhausted, e.Code)
			require.Equal(t, tc.want, e.RetryAfterMs)
			require.Equal(t, []string{ResultExhausted}, f.rec.acquireResults())
		})
	}
}

func TestAcquireExclusivityUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	for i := int64(1); i <= 10; i++ {
		f.identity(i, 0, []int64{testWebGroup})
	}
	ctx := f.tokenCtx()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		leases  []*Grant
		exhaust int
	)
	for w := 0; w < 100; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := f.acquire(ctx, AcquireRequest{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if apperr.ReasonOf(err) == apperr.ReasonNoIdentityAvailable {
					exhaust++
					return
				}
				t.Errorf("acquire: %v", err)
				return
			}
			leases = append(leases, g)
		}()
	}
	wg.Wait()
	require.Len(t, leases, 10)
	require.Equal(t, 90, exhaust)
	active := map[string]int{}
	for _, g := range leases {
		ls := f.lease(g.Lease.ID)
		require.Equal(t, "active", ls["st"])
		active[ls["i"]]++
	}
	require.Len(t, active, 10)
	for i, n := range active {
		require.Equal(t, 1, n, "identity %s has %d active leases", i, n)
		require.Equal(t, "1", f.hget(f.keys.Identity(testSiteKey, atoi64(i)), "al"))
	}
}

func TestAcquireReleaseLoopNeverSharesIdentity(t *testing.T) {
	f := newFixture(t)
	for i := int64(1); i <= 10; i++ {
		f.identity(i, 0, []int64{testWebGroup})
	}
	ctx := f.tokenCtx()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		held = map[string]bool{}
	)
	for w := 0; w < 50; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				g, err := f.acquire(ctx, AcquireRequest{})
				if err != nil {
					if apperr.ReasonOf(err) != apperr.ReasonNoIdentityAvailable {
						t.Errorf("acquire: %v", err)
					}
					continue
				}
				mu.Lock()
				if held[g.Lease.IdentityID] {
					t.Errorf("identity %s leased twice", g.Lease.IdentityID)
				}
				held[g.Lease.IdentityID] = true
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				delete(held, g.Lease.IdentityID)
				mu.Unlock()
				if _, err := f.svc.Release(ctx, g.Lease.ID); err != nil {
					t.Errorf("release: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	for i := int64(1); i <= 10; i++ {
		require.Equal(t, "0", f.idField(i, "al"))
		require.Equal(t, "0", f.idField(i, "xl"))
	}
}

func TestAcquireBatchDistinct(t *testing.T) {
	f := newFixture(t)
	for i := int64(1); i <= 10; i++ {
		f.identity(i, 0, []int64{testWebGroup})
	}
	ctx := f.tokenCtx()
	grants, err := f.svc.AcquireBatch(ctx, AcquireRequest{Site: "demo", Client: "web", Count: 4})
	require.NoError(t, err)
	require.Len(t, grants, 4)
	seen := map[string]bool{}
	for _, g := range grants {
		require.False(t, seen[g.Lease.IdentityID])
		seen[g.Lease.IdentityID] = true
	}

	grants, err = f.svc.AcquireBatch(ctx, AcquireRequest{Site: "demo", Client: "web", Count: 50})
	require.NoError(t, err)
	require.Len(t, grants, 6, "only the remaining identities are issued")
	for _, g := range grants {
		require.False(t, seen[g.Lease.IdentityID])
		seen[g.Lease.IdentityID] = true
	}

	_, err = f.svc.AcquireBatch(ctx, AcquireRequest{Site: "demo", Client: "web", Count: 2})
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)

	for _, n := range []int{0, 51} {
		_, err = f.svc.AcquireBatch(ctx, AcquireRequest{Site: "demo", Client: "web", Count: n})
		requireReason(t, err, apperr.ReasonInvalidArgument)
	}
}

// AcquireBatch returns fewer leases than requested only when fewer identities
// are available: picks of one sampling round must not end the loop, and a
// round samples at least as many candidates as the batch still needs.
func TestAcquireBatchFillsCountAcrossRounds(t *testing.T) {
	tests := []struct {
		name   string
		sample int // candidate_sample (0 = default)
		stale  int // identities in cooldown at the head of the ready set
		avail  int
		count  int
		want   int
	}{
		{name: "count above the default sample", avail: 100, count: 50, want: 50},
		{name: "stale head then available identities", stale: 30, avail: 10, count: 5, want: 5},
		{name: "sample of one", sample: 1, avail: 10, count: 5, want: 5},
		{name: "small sample behind stale head", sample: 2, stale: 4, avail: 3, count: 3, want: 3},
		{name: "fewer available than requested", sample: 2, stale: 7, avail: 3, count: 10, want: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.sample > 0 {
				f.rotation(f.web).Rotation.CandidateSample = tc.sample
			}
			now := f.nowMs()
			for i := 1; i <= tc.stale; i++ {
				f.identity(int64(i), 0, []int64{testWebGroup})
				f.health(testWebGroup, int64(i), fmt.Sprintf("70|0|0|0|0|%d|0|0", now+60000))
			}
			for i := tc.stale + 1; i <= tc.stale+tc.avail; i++ {
				f.identity(int64(i), 1, []int64{testWebGroup})
			}
			grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: tc.count})
			require.NoError(t, err)
			require.Len(t, grants, tc.want)
			seen := map[string]bool{}
			for _, g := range grants {
				require.False(t, seen[g.Lease.IdentityID], "identity %s leased twice", g.Lease.IdentityID)
				seen[g.Lease.IdentityID] = true
				require.Equal(t, "1", f.idField(atoi64(f.lease(g.Lease.ID)["i"]), "al"))
			}
			if tc.want == tc.count {
				return
			}
			for i := 1; i <= tc.stale; i++ {
				require.Equal(t, now+60000, f.ready(testWebGroup, int64(i)), "filtered identities are pushed")
			}
		})
	}
}

func TestAcquireWaitsForRelease(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup})
	first := f.mustAcquire(AcquireRequest{})

	go func() {
		time.Sleep(120 * time.Millisecond)
		f.mustRelease(first.Lease.ID)
	}()
	start := time.Now()
	g, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 2 * time.Second})
	require.NoError(t, err)
	require.Equal(t, "idt_1", g.Lease.IdentityID)
	require.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)

	start = time.Now()
	_, err = f.acquire(f.tokenCtx(), AcquireRequest{Wait: 300 * time.Millisecond})
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
	require.Less(t, elapsed, 2*time.Second)

	ctx, cancel := context.WithTimeout(f.tokenCtx(), 80*time.Millisecond)
	defer cancel()
	_, err = f.acquire(ctx, AcquireRequest{Wait: 5 * time.Second})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	results := f.rec.acquireResults()
	require.Equal(t, ResultExhausted, results[len(results)-1])

	_, err = f.acquire(f.tokenCtx(), AcquireRequest{Wait: 6 * time.Second})
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestAcquireResolution(t *testing.T) {
	f := newFixture(t)
	for i := int64(1); i <= 3; i++ {
		f.identity(i, 0, []int64{testWebGroup, testSearchKey, testOtherKey})
	}
	tests := []struct {
		name      string
		req       AcquireRequest
		wantGroup string
		reason    apperr.Reason
	}{
		{name: "default group", req: AcquireRequest{}, wantGroup: "_default"},
		{name: "uri match", req: AcquireRequest{URI: "/search/items?q=1"}, wantGroup: "search"},
		{name: "absolute uri", req: AcquireRequest{URI: "https://example.com/search/x"}, wantGroup: "search"},
		{name: "uri fallback", req: AcquireRequest{URI: "/nothing"}, wantGroup: "_default"},
		{name: "explicit group wins", req: AcquireRequest{URI: "/search/x", EndpointGroup: "other"}, wantGroup: "other"},
		{name: "unknown site", req: AcquireRequest{Site: "nope"}, reason: apperr.ReasonSiteUnknown},
		{name: "unknown client", req: AcquireRequest{Client: "desktop"}, reason: apperr.ReasonClientUnknown},
		{name: "unknown group", req: AcquireRequest{EndpointGroup: "missing"}, reason: apperr.ReasonEndpointGroupUnknown},
		{name: "invalid uri", req: AcquireRequest{URI: "search"}, reason: apperr.ReasonURIInvalid},
		{name: "too long uri", req: AcquireRequest{URI: "/" + string(make([]byte, 2048))}, reason: apperr.ReasonURIInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := f.acquire(f.tokenCtx(), tc.req)
			if tc.reason != "" {
				requireReason(t, err, tc.reason)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantGroup, g.Lease.EndpointGroup)
			f.mustRelease(g.Lease.ID)
		})
	}
}

func TestAcquirePermissionsAndPause(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup})

	_, err := f.svc.Acquire(context.Background(), AcquireRequest{Site: "demo", Client: "web"})
	requireReason(t, err, apperr.ReasonSessionInvalid)

	_, err = f.acquire(f.tokenCtx("report:write"), AcquireRequest{})
	e := requireAppErr(t, err, apperr.ReasonScopeMissing)
	require.Equal(t, connect.CodePermissionDenied, e.Code)

	_, err = f.acquire(f.tokenCtx("lease:acquire:other-site"), AcquireRequest{})
	requireReason(t, err, apperr.ReasonScopeMissing)

	g, err := f.acquire(f.tokenCtx("lease:acquire:demo"), AcquireRequest{})
	require.NoError(t, err)
	f.mustRelease(g.Lease.ID)

	user := userCtx(f, authz.RoleOwner)
	_, err = f.acquire(user, AcquireRequest{})
	requireReason(t, err, apperr.ReasonPermissionDenied)

	f.site.Paused = true
	_, err = f.acquire(f.tokenCtx(), AcquireRequest{})
	e = requireAppErr(t, err, apperr.ReasonSitePaused)
	require.Equal(t, connect.CodeUnavailable, e.Code)
	require.Equal(t, int64(30000), e.RetryAfterMs)
	results := f.rec.acquireResults()
	require.Equal(t, ResultSitePaused, results[len(results)-1])
}

func TestAcquireRenderFailureReleasesLease(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup})
	f.identity(2, 1, []int64{testWebGroup})
	f.creds.fail["idt_1"] = true

	f.setRandoms(0, 0, 0, 0)
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	e := requireAppErr(t, err, apperr.ReasonInternal)
	require.Equal(t, connect.CodeInternal, e.Code)
	require.Equal(t, "0", f.idField(1, "al"), "the lease was released")
	require.Equal(t, []string{ResultError}, f.rec.acquireResults())
	require.Empty(t, f.rec.endKinds(), "undelivered leases are not recorded as released")

	f.setRandoms(0, 0, 0, 0, 0, 0, 0, 0)
	grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 2})
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, "idt_2", grants[0].Lease.IdentityID)

	f.creds = nil
	f.svc.creds = nil
	f.mustRelease(grants[0].Lease.ID)
	_, err = f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
}

func TestAcquireUnknownTypeAndCanceledContext(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup}, "ty", "gone_type")
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
	require.Equal(t, "0", f.idField(1, "al"))

	f.identity(2, 0, []int64{testWebGroup})
	ctx, cancel := context.WithCancel(f.tokenCtx())
	cancel()
	_, err = f.acquire(ctx, AcquireRequest{})
	require.Error(t, err)
	require.Equal(t, "0", f.idField(2, "al"))
}

func TestParseAcquireRejectsMalformedReplies(t *testing.T) {
	for _, vals := range [][]string{
		{"OK"},
		{"WHAT", "0", "0"},
		{"OK", "0", "1", "lse"},
	} {
		_, err := parseAcquire(vals)
		require.Error(t, err)
	}
	out, err := parseAcquire([]string{"NO_PROXY", "1", "250"})
	require.NoError(t, err)
	require.True(t, out.Transition)
	require.Equal(t, int64(250), out.RetryAfterMs)
	require.Equal(t, int64(50), clampRetry(0))
	require.Equal(t, "w", strategyCode(policy.StrategyWeightedRandom))
	require.Equal(t, "n", proxyModeCode(""))
	require.Equal(t, strconv.Itoa(0), groupLayout(nil)[0])
}
