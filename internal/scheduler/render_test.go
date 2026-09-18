package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// A lease released because its credential could not be rendered was never
// delivered: it must not consume the reuse interval, the quota or a half-open
// probe slot of the identity.
func TestRenderFailureDoesNotConsumeIdentity(t *testing.T) {
	const ri = 10 * time.Minute
	tests := []struct {
		name   string
		anchor string
		scope  string
	}{
		{name: "released anchor", anchor: policy.ReuseAnchorReleased, scope: policy.ReuseScopeEndpointGroup},
		{name: "acquired anchor endpoint scope", anchor: policy.ReuseAnchorAcquired, scope: policy.ReuseScopeEndpointGroup},
		{name: "acquired anchor site scope", anchor: policy.ReuseAnchorAcquired, scope: policy.ReuseScopeSite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rot := f.rotation(f.web)
			rot.Rotation.ReuseInterval = durationx.Duration(ri)
			rot.Rotation.ReuseAnchor = tc.anchor
			rot.Rotation.ReuseScope = tc.scope
			rot.Rotation.Quota = []policy.QuotaSpec{{Limit: 1, Window: durationx.Duration(time.Hour)}}
			now := f.nowMs()
			f.identity(1, 0, []int64{testWebGroup})
			// A reuse value from an earlier lease that already passed.
			f.health(testWebGroup, 1, fmt.Sprintf("70|%d|0|0|0|0|%d|0", now, now-1000))
			f.hset(f.keys.Identity(testSiteKey, 1), "sru", key(now-2000))

			f.creds.fail["idt_1"] = true
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			requireReason(t, err, apperr.ReasonInternal)
			require.Equal(t, "0", f.idField(1, "al"))
			require.Equal(t, key(now-2000), f.idField(1, "sru"), "the site reuse value is restored")
			require.Contains(t, f.hget(f.keys.Health(testSiteKey, testWebGroup), "1"), fmt.Sprintf("|0|%d|", now-1000),
				"the endpoint reuse value is restored")
			require.Equal(t, "0", f.hget(f.keys.Quota(testSiteKey, testWebGroup, 1), "3600000:n"), "the quota count is rolled back")
			require.LessOrEqual(t, f.ready(testWebGroup, 1), now)

			f.creds.fail["idt_1"] = false
			f.advance(10 * time.Millisecond)
			g, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			require.NoError(t, err, "the identity was not consumed by the failed delivery")
			require.Equal(t, "idt_1", g.Lease.IdentityID)

			// A delivered lease still applies the reuse anchor and the quota.
			f.mustRelease(g.Lease.ID)
			require.Equal(t, "1", f.hget(f.keys.Quota(testSiteKey, testWebGroup, 1), "3600000:n"))
			require.Greater(t, f.ready(testWebGroup, 1), f.nowMs()+int64(ri/time.Millisecond)-1000)
		})
	}
}

func TestRenderFailureReturnsHalfOpenProbeSlot(t *testing.T) {
	f := newFixture(t)
	p := policy.Default(policy.KindBreaker).(*policy.BreakerSpec)
	p.HalfOpen.ProbeLeasesPer10s = 1
	f.web.Breaker = p
	f.identity(1, 0, []int64{testWebGroup})
	brk := f.keys.Breaker(testSiteKey, testWebGroup)
	f.hset(brk, "st", "half_open", "hw", key(f.nowMs()), "hc", "0")

	f.creds.fail["idt_1"] = true
	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
	require.Equal(t, "0", f.hget(brk, "hc"), "the undelivered probe does not use the window's probe slot")

	f.creds.fail["idt_1"] = false
	g, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	require.NoError(t, err)
	require.True(t, g.Lease.Probe)
	require.Equal(t, "1", f.hget(brk, "hc"))
}

func TestCanceledRenderAbortsLeases(t *testing.T) {
	f := newFixture(t)
	rot := f.rotation(f.web)
	rot.Rotation.ReuseInterval = durationx.Duration(time.Hour)
	f.identity(1, 0, []int64{testWebGroup})
	ctx, cancel := context.WithCancel(f.tokenCtx())
	defer cancel()
	f.creds.onCall = func(string) { cancel() }

	_, err := f.acquire(ctx, AcquireRequest{})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "0", f.idField(1, "al"))
	require.LessOrEqual(t, f.ready(testWebGroup, 1), f.nowMs(), "an abandoned request does not start the reuse interval")
}

// With a site-scoped "acquired" reuse anchor, an acquire in another endpoint
// group (of any client) that samples the identity while the undelivered lease
// is being rendered pushes it to the raised site reuse value. Aborting the
// lease restores the site reuse value and must pull those scores back too.
func TestRenderFailureRestoresSiteReuseInOtherGroups(t *testing.T) {
	const testAppGroup = testSiteKey*1000 + 1 // "_default" of client app
	tests := []struct {
		name  string
		req   AcquireRequest
		group int64
	}{
		{name: "same client", req: AcquireRequest{EndpointGroup: "search"}, group: testSearchKey},
		{name: "other client", req: AcquireRequest{Client: "app"}, group: testAppGroup},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			rot := f.rotation(f.web)
			rot.Rotation.ReuseInterval = durationx.Duration(10 * time.Minute)
			rot.Rotation.ReuseAnchor = policy.ReuseAnchorAcquired
			rot.Rotation.ReuseScope = policy.ReuseScopeSite
			f.identity(1, 0, []int64{testWebGroup, tc.group})
			// A later push of the other group that the aborted lease did not cause.
			f.identity(2, 0, []int64{testWebGroup, tc.group}, "scd", key(f.nowMs()+time.Hour.Milliseconds()))

			var nested error
			f.creds.fail["idt_1"] = true
			f.creds.onCall = func(string) {
				f.creds.onCall = nil // runs under the fake's lock
				_, nested = f.acquire(f.tokenCtx(), tc.req)
			}
			f.setRandoms(0) // pick identity 1 first
			_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
			requireReason(t, err, apperr.ReasonInternal)
			requireReason(t, nested, apperr.ReasonNoIdentityAvailable)
			now := f.nowMs()
			require.LessOrEqual(t, f.ready(testWebGroup, 1), now)
			require.LessOrEqual(t, f.ready(tc.group, 1), now, "the other group gets the identity back")
			require.Equal(t, now+time.Hour.Milliseconds(), f.ready(tc.group, 2), "unrelated pushes stay")

			f.creds.fail["idt_1"] = false
			g, err := f.acquire(f.tokenCtx(), tc.req)
			require.NoError(t, err)
			require.Equal(t, "idt_1", g.Lease.IdentityID)
		})
	}
}
