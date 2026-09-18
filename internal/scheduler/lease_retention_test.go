package scheduler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/pkg/durationx"
)

// An active lease hash must not expire on its own: the reaper resolves the
// identity and proxy of an overdue lease only from the hash, so a hash that
// vanished while active would leave id.al and px.al raised forever.
func TestActiveLeaseHashOutlivesReaperOutage(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.LateReportWindow = time.Millisecond
	rot := f.rotation(f.web)
	rot.Rotation.LeaseTTL = durationx.Duration(100 * time.Millisecond)
	rot.Rotation.MaxLeaseLifetime = durationx.Duration(100 * time.Millisecond)
	f.identity(1, 0, []int64{testWebGroup})

	g := f.mustAcquire(AcquireRequest{})
	lkey := f.keys.Lease(testSiteKey, g.Lease.ID)
	require.Equal(t, int64(-1), f.pttl(lkey), "an active lease hash has no TTL")

	// Every reaper stays down well past cap + late window (real Redis time).
	time.Sleep(250 * time.Millisecond)
	require.True(t, f.exists(lkey), "the active lease hash is still there")

	f.advance(time.Second)
	require.NoError(t, f.svc.reapOnce(f.ctx))
	require.Equal(t, "0", f.idField(1, "al"), "the reaper decremented the active lease count")
	require.Equal(t, []string{EndAbandoned}, f.rec.endKinds())

	again := f.mustAcquire(AcquireRequest{})
	require.Equal(t, "idt_1", again.Lease.IdentityID, "the identity is back in rotation")
}

// Ingest records accepted reports on the lease hash ("rv", and "kx" = the
// time until which the hash must stay readable for the worker). Ending the
// lease keeps the hash for max(late window, kx - now) and a lease with
// ingested but not yet processed reports is not abandoned.
func TestLeaseEndKeepsHashForIngestedReports(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.LateReportWindow = 50 * time.Millisecond
	rot := f.rotation(f.web)
	rot.Rotation.LeaseTTL = durationx.Duration(10 * time.Second)
	for i := int64(1); i <= 4; i++ {
		f.identity(i, i, []int64{testWebGroup})
	}
	f.setRandoms(make([]float64, 64)...)
	grants, err := f.svc.AcquireBatch(f.tokenCtx(), AcquireRequest{Site: "demo", Client: "web", Count: 4})
	require.NoError(t, err)
	require.Len(t, grants, 4)
	start := f.nowMs()
	retainUntil := start + time.Hour.Milliseconds()
	reportedRelease, silentRelease, reportedReap, silentReap := grants[0].Lease.ID, grants[1].Lease.ID, grants[2].Lease.ID, grants[3].Lease.ID
	for _, id := range []string{reportedRelease, reportedReap} {
		f.hset(f.keys.Lease(testSiteKey, id), "rv", "2", "kx", key(retainUntil))
	}

	f.advance(time.Second)
	f.mustRelease(reportedRelease)
	f.mustRelease(silentRelease)
	require.InDelta(t, retainUntil-f.nowMs(), f.pttl(f.keys.Lease(testSiteKey, reportedRelease)), 2000)
	require.LessOrEqual(t, f.pttl(f.keys.Lease(testSiteKey, silentRelease)), int64(50))

	f.advance(10 * time.Second)
	require.NoError(t, f.svc.reapOnce(f.ctx))
	require.InDelta(t, retainUntil-f.nowMs(), f.pttl(f.keys.Lease(testSiteKey, reportedReap)), 2000)
	kinds := f.rec.endKinds()
	require.Len(t, kinds, 4)
	require.Equal(t, []string{EndReleased, EndReleased}, kinds[:2])
	require.ElementsMatch(t, []string{EndExpired, EndAbandoned}, kinds[2:],
		"a lease with ingested reports is expired, not abandoned")
	for _, e := range f.rec.ends[2:] {
		if e.LeaseID == reportedReap {
			require.Equal(t, EndExpired, e.Kind)
		}
	}

	// Real Redis time: the late window passes, the retained hashes stay.
	time.Sleep(150 * time.Millisecond)
	require.True(t, f.exists(f.keys.Lease(testSiteKey, reportedRelease)))
	require.True(t, f.exists(f.keys.Lease(testSiteKey, reportedReap)))
	require.False(t, f.exists(f.keys.Lease(testSiteKey, silentRelease)))
	require.False(t, f.exists(f.keys.Lease(testSiteKey, silentReap)))
}
