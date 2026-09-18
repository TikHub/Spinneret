package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

func TestRenew(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.LeaseTTL = durationx.Duration(10 * time.Second)
	rot.Rotation.MaxLeaseLifetime = durationx.Duration(15 * time.Second)
	f.identity(1, 0, []int64{testWebGroup})
	start := f.nowMs()
	g := f.mustAcquire(AcquireRequest{})
	ctx := f.tokenCtx()

	f.advance(2 * time.Second)
	exp, err := f.svc.Renew(ctx, g.Lease.ID, 0)
	require.NoError(t, err)
	require.Equal(t, time.UnixMilli(start+12000), exp, "extend 0 uses the lease ttl")
	require.Equal(t, key(start+12000), f.lease(g.Lease.ID)["x"])
	require.Equal(t, start+12000, f.zscore(f.keys.LeaseExpiry(testSiteKey), g.Lease.ID))
	require.Equal(t, key(start+12000), f.idField(1, "xl"))
	require.Equal(t, start+12000, f.ready(testWebGroup, 1))

	f.advance(time.Second)
	exp, err = f.svc.Renew(ctx, g.Lease.ID, time.Second)
	require.NoError(t, err)
	require.Equal(t, time.UnixMilli(start+12000), exp, "renew never shortens a lease")

	f.advance(5 * time.Second)
	exp, err = f.svc.Renew(ctx, g.Lease.ID, 20*time.Second)
	require.NoError(t, err)
	require.Equal(t, time.UnixMilli(start+15000), exp, "capped by the lifetime")

	_, err = f.svc.Renew(ctx, g.Lease.ID, time.Second)
	requireReason(t, err, apperr.ReasonLeaseLifetimeExceeded)

	f.advance(8 * time.Second)
	_, err = f.svc.Renew(ctx, g.Lease.ID, time.Second)
	e := requireAppErr(t, err, apperr.ReasonLeaseExpired)
	require.Equal(t, connect.CodeFailedPrecondition, e.Code)

	require.NoError(t, f.svc.reapOnce(f.ctx))
	_, err = f.svc.Renew(ctx, g.Lease.ID, time.Second)
	requireReason(t, err, apperr.ReasonLeaseExpired)

	g = f.mustAcquire(AcquireRequest{})
	f.mustRelease(g.Lease.ID)
	_, err = f.svc.Renew(ctx, g.Lease.ID, time.Second)
	requireReason(t, err, apperr.ReasonLeaseReleased)

	_, err = f.svc.Renew(ctx, g.Lease.ID, 31*time.Minute)
	requireReason(t, err, apperr.ReasonInvalidArgument)

	kinds := f.rec.endKinds()
	require.Equal(t, []string{EndRenewed, EndRenewed, EndRenewed, EndAbandoned, EndReleased}, kinds[:5])
	require.Equal(t, "_default", f.rec.ends[0].EndpointGroup)
	require.Equal(t, "idt_1", f.rec.ends[0].IdentityID)
}

func TestLeaseAccessIsScopedToNamespaceAndSite(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup})
	g := f.mustAcquire(AcquireRequest{})

	// A second namespace with its own site and lease.
	ns2 := catalogtest.NewNamespace("ten_1", "ns_2", "staging")
	catalogtest.AddSite(ns2, "sit_2", "demo", 8, "web")
	f.cat.Put(ns2)
	foreign := idgen.LeasePrefix(8) + "01"

	tests := []struct {
		name    string
		ctx     context.Context
		leaseID string
		reason  apperr.Reason
	}{
		{name: "malformed id", ctx: f.tokenCtx(), leaseID: "lse_nope", reason: apperr.ReasonLeaseUnknown},
		{name: "unknown site key", ctx: f.tokenCtx(), leaseID: idgen.LeasePrefix(999) + "00", reason: apperr.ReasonLeaseUnknown},
		{name: "other namespace", ctx: f.tokenCtx(), leaseID: foreign, reason: apperr.ReasonLeaseUnknown},
		{name: "scope for another site", ctx: f.tokenCtx("lease:acquire:other"), leaseID: g.Lease.ID, reason: apperr.ReasonLeaseUnknown},
		{name: "missing scope", ctx: f.tokenCtx("report:write"), leaseID: g.Lease.ID, reason: apperr.ReasonLeaseUnknown},
		{name: "unknown lease", ctx: f.tokenCtx(), leaseID: idgen.LeasePrefix(testSiteKey) + "03", reason: apperr.ReasonLeaseUnknown},
		{name: "user principal", ctx: userCtx(f, "owner"), leaseID: g.Lease.ID, reason: apperr.ReasonPermissionDenied},
		{name: "unauthenticated", ctx: context.Background(), leaseID: g.Lease.ID, reason: apperr.ReasonSessionInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.svc.Release(tc.ctx, tc.leaseID)
			e := requireAppErr(t, err, tc.reason)
			if tc.reason == apperr.ReasonLeaseUnknown {
				require.Equal(t, connect.CodeNotFound, e.Code)
			}
			_, err = f.svc.Renew(tc.ctx, tc.leaseID, 0)
			requireReason(t, err, tc.reason)
		})
	}

	// A lease hash of another namespace under the same site key is not visible.
	forged := idgen.LeasePrefix(testSiteKey) + "05"
	f.hset(f.keys.Lease(testSiteKey, forged), "st", "active", "ns", "ns_2", "i", "1", "e", key(testWebGroup), "x", key(f.nowMs()+1000), "cap", key(f.nowMs()+5000))
	_, err := f.svc.Release(f.tokenCtx(), forged)
	requireReason(t, err, apperr.ReasonLeaseUnknown)
	_, err = f.svc.Renew(f.tokenCtx(), forged, 0)
	requireReason(t, err, apperr.ReasonLeaseUnknown)

	ok, err := f.svc.Release(f.tokenCtx("lease:acquire:demo"), g.Lease.ID)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestReleaseIdempotentAndRescoring(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup, testSearchKey, testOtherKey})
	g := f.mustAcquire(AcquireRequest{})
	x := g.Lease.ExpiresAt.UnixMilli()

	// Another group sampled the leased identity and pushed it to the
	// exclusive expiry; a third group has an unrelated later push.
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{EndpointGroup: "search"})
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)
	require.Equal(t, x, f.ready(testSearchKey, 1))
	f.zadd(f.keys.Ready(testSiteKey, testOtherKey), x+50000, "1")

	f.advance(3 * time.Second)
	ok, err := f.svc.Release(f.tokenCtx(), g.Lease.ID)
	require.NoError(t, err)
	require.True(t, ok)
	now := f.nowMs()
	ls := f.lease(g.Lease.ID)
	require.Equal(t, "released", ls["st"])
	require.Equal(t, key(now), ls["end"])
	require.InDelta(t, 600000, f.pttl(f.keys.Lease(testSiteKey, g.Lease.ID)), 2000)
	require.Equal(t, int64(-1), f.zscore(f.keys.LeaseExpiry(testSiteKey), g.Lease.ID))
	require.Equal(t, "0", f.idField(1, "al"))
	require.Equal(t, "0", f.idField(1, "xl"))
	require.Equal(t, now, f.ready(testWebGroup, 1))
	require.Equal(t, now, f.ready(testSearchKey, 1), "exclusive push in other groups is undone")
	require.Equal(t, x+50000, f.ready(testOtherKey, 1), "unrelated later pushes are kept")

	ok, err = f.svc.Release(f.tokenCtx(), g.Lease.ID)
	require.NoError(t, err)
	require.False(t, ok, "second release is a no-op")
	require.Equal(t, "0", f.idField(1, "al"))

	// Worker path.
	g = f.mustAcquire(AcquireRequest{})
	ok, err = f.svc.ReleaseLease(f.ctx, testSiteKey, g.Lease.ID, f.clock())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = f.svc.ReleaseLease(f.ctx, testSiteKey, g.Lease.ID, f.clock())
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = f.svc.ReleaseLease(f.ctx, testSiteKey, "lse_missing", f.clock())
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, []string{EndReleased, EndReleased}, f.rec.endKinds())

	// The identity hash vanished while leased (identity deleted): release
	// must not recreate it.
	g = f.mustAcquire(AcquireRequest{})
	require.NoError(t, f.do(f.rdb.B().Del().Key(f.keys.Identity(testSiteKey, 1)).Build()).Error())
	ok, err = f.svc.ReleaseLease(f.ctx, testSiteKey, g.Lease.ID, f.clock())
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, f.exists(f.keys.Identity(testSiteKey, 1)))
}

// TestExclusivePushMarkerScopesTheRestore pins the "xg" marker: only an acquire
// that really pushed the identity because of the exclusive lease makes the lease
// end walk the other endpoint groups, and it walks only the groups that pushed.
// Without the marker every lease end costs one ZSCORE per endpoint group of the
// client (measured at 2.25 us of Redis CPU per group, see docs/benchmarks.md),
// which dominates the hot path on a site with many groups.
func TestExclusivePushMarkerScopesTheRestore(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup, testSearchKey, testOtherKey})

	// No other group sampled the identity while it was leased.
	g := f.mustAcquire(AcquireRequest{})
	x := g.Lease.ExpiresAt.UnixMilli()
	require.Equal(t, "", f.idField(1, "xg"), "nothing pushed the identity")
	// A score that looks exactly like an exclusive push but was not written by
	// one (a hot-state synchronization, a manual fix): the lease end must not
	// scan the groups for it.
	f.zadd(f.keys.Ready(testSiteKey, testSearchKey), x, "1")
	f.advance(time.Second)
	f.mustRelease(g.Lease.ID)
	require.Equal(t, x, f.ready(testSearchKey, 1),
		"an unmarked identity is not rescored in the other groups")
	require.Equal(t, f.nowMs(), f.ready(testWebGroup, 1))

	// With a real push from another group the marker is set, the restore happens
	// and the marker is cleared again.
	f.zadd(f.keys.Ready(testSiteKey, testSearchKey), 0, "1")
	g = f.mustAcquire(AcquireRequest{})
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{EndpointGroup: "search"})
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)
	require.Equal(t, ","+key(testSearchKey)+",", f.idField(1, "xg"),
		"the pushing acquire names its endpoint group in the marker")
	require.Equal(t, g.Lease.ExpiresAt.UnixMilli(), f.ready(testSearchKey, 1))
	// A second group of the same client carries a score that looks like the same
	// exclusive push but never pushed: it is not in the marker and must be left
	// alone, which is what makes the restore cost one ZSCORE instead of one per
	// endpoint group.
	other := g.Lease.ExpiresAt.UnixMilli()
	f.zadd(f.keys.Ready(testSiteKey, testOtherKey), other, "1")
	f.advance(time.Second)
	f.mustRelease(g.Lease.ID)
	require.Equal(t, f.nowMs(), f.ready(testSearchKey, 1), "the marked group is restored")
	require.Equal(t, other, f.ready(testOtherKey, 1), "an unmarked group is not restored")
	require.Equal(t, "", f.idField(1, "xg"), "the marker is cleared with the lease")

	// The bare "1" an earlier release wrote does not name a group; it still has
	// to restore every endpoint group of the client.
	f.zadd(f.keys.Ready(testSiteKey, testSearchKey), 0, "1")
	f.zadd(f.keys.Ready(testSiteKey, testOtherKey), 0, "1")
	g = f.mustAcquire(AcquireRequest{})
	legacy := g.Lease.ExpiresAt.UnixMilli()
	f.hset(f.keys.Identity(testSiteKey, 1), "xg", "1")
	f.zadd(f.keys.Ready(testSiteKey, testSearchKey), legacy, "1")
	f.zadd(f.keys.Ready(testSiteKey, testOtherKey), legacy, "1")
	f.advance(time.Second)
	f.mustRelease(g.Lease.ID)
	require.Equal(t, f.nowMs(), f.ready(testSearchKey, 1), "legacy marker restores every group")
	require.Equal(t, f.nowMs(), f.ready(testOtherKey, 1), "legacy marker restores every group")
	require.Equal(t, "", f.idField(1, "xg"), "the marker is cleared with the lease")
}

func TestReleaseWithAccountAndProxyGone(t *testing.T) {
	f := poolFixture(t, "pool", nil)
	f.identity(1, 0, []int64{testWebGroup}, "acc", "4")
	f.hset(f.keys.Account(testSiteKey, 4), "st", "active", "cd", "0")
	f.proxy(3, 0, "mc", "2")
	g := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "pxy_3", g.Proxy.ID)
	// Account cooldown applied during the lease is honored on release.
	f.hset(f.keys.Account(testSiteKey, 4), "cd", key(f.nowMs()+70000))
	require.NoError(t, f.do(f.rdb.B().Del().Key(f.keys.ProxySite(testSiteKey, 3)).Build()).Error())
	f.mustRelease(g.Lease.ID)
	require.Equal(t, f.nowMs()+70000, f.ready(testWebGroup, 1))
	require.False(t, f.exists(f.keys.ProxySite(testSiteKey, 3)), "release must not recreate a deleted proxy")
}

func TestReapExpiredAndAbandoned(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.LeaseTTL = durationx.Duration(10 * time.Second)
	rot.Rotation.ReuseInterval = durationx.Duration(time.Minute)
	f.identity(1, 0, []int64{testWebGroup})
	f.identity(2, 1, []int64{testWebGroup})
	f.identity(3, 2, []int64{testWebGroup})
	f.setRandoms(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 2})
	require.NoError(t, err)
	require.Len(t, grants, 2)
	reported, abandoned := grants[0], grants[1]
	f.hset(f.keys.Lease(testSiteKey, reported.Lease.ID), "rc", "3")
	f.advance(5 * time.Second)
	fresh := f.mustAcquire(AcquireRequest{})
	start := testEpoch.UnixMilli()

	f.advance(6 * time.Second)
	require.NoError(t, f.svc.reapOnce(f.ctx))
	for _, g := range []*Grant{reported, abandoned} {
		ls := f.lease(g.Lease.ID)
		require.Equal(t, "expired", ls["st"])
		require.Equal(t, key(start+10000), ls["end"])
		require.InDelta(t, 600000, f.pttl(f.keys.Lease(testSiteKey, g.Lease.ID)), 2000)
		i := atoi64(ls["i"])
		require.Equal(t, "0", f.idField(i, "al"))
		require.Equal(t, start+10000+60000, f.ready(testWebGroup, i), "released anchor counts from the expiry")
	}
	require.Equal(t, "active", f.lease(fresh.Lease.ID)["st"])
	require.ElementsMatch(t, []string{EndExpired, EndAbandoned}, f.rec.endKinds())
	require.Equal(t, 1.0, counterValue(t, f, EndAbandoned))
	require.Equal(t, 1.0, counterValue(t, f, EndExpired))

	// The lock is held for the rest of the second: a second pass is skipped.
	f.advance(time.Minute)
	require.NoError(t, f.svc.reapOnce(f.ctx))
	require.Equal(t, "active", f.lease(fresh.Lease.ID)["st"])
	require.NoError(t, redis.Unlock(f.ctx, f.rdb, f.keys.SiteLock(testSiteKey, reapLockName), f.svc.owner))
	require.NoError(t, f.svc.reapOnce(f.ctx))
	require.Equal(t, "expired", f.lease(fresh.Lease.ID)["st"])

	// A stale lsexp entry without a lease hash is dropped.
	f.zadd(f.keys.LeaseExpiry(testSiteKey), 1, "lse_stale")
	require.NoError(t, redis.Unlock(f.ctx, f.rdb, f.keys.SiteLock(testSiteKey, reapLockName), f.svc.owner))
	require.NoError(t, f.svc.reapOnce(f.ctx))
	require.Equal(t, int64(-1), f.zscore(f.keys.LeaseExpiry(testSiteKey), "lse_stale"))

	job := f.svc.ReapJob()
	require.Equal(t, reapJobName, job.Name)
	require.Equal(t, time.Second, job.Interval)
	require.NoError(t, job.Validate())
}

func TestReapDrainsLargeBacklogInBatches(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.MaxConcurrentLeases = 1000
	rot.Rotation.LeaseTTL = durationx.Duration(5 * time.Second)
	f.identity(1, 0, []int64{testWebGroup})
	const n = 450
	for k := 0; k < n; k++ {
		f.mustAcquire(AcquireRequest{})
	}
	require.Equal(t, fmt.Sprint(n), f.idField(1, "al"))
	f.advance(10 * time.Second)
	expired, err := f.svc.reapSite(f.ctx, f.ns, f.site)
	require.NoError(t, err)
	require.Equal(t, n, expired)
	require.Equal(t, "0", f.idField(1, "al"))
}

func TestNewDefaultsAndNilDependencies(t *testing.T) {
	svc := New(Config{ReportShards: 1000}, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil, nil, nil)
	require.Equal(t, 256, svc.cfg.ReportShards)
	require.Equal(t, DefaultLateReportWindow, svc.cfg.LateReportWindow)
	svc = New(Config{}, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil, nil, nil)
	require.Equal(t, DefaultReportShards, svc.cfg.ReportShards)
	svc.rec.RecordAcquire(AcquireRecord{})
	svc.rec.RecordLeaseEnd(LeaseEndRecord{})
	svc.observeAcquire("s", "g", ResultOK, time.Millisecond)
	require.NoError(t, svc.reapOnce(context.Background()))
}

func counterValue(t *testing.T, f *fixture, kind string) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, f.metrics.LeaseReaped.WithLabelValues("demo", kind).Write(&m))
	return m.GetCounter().GetValue()
}
