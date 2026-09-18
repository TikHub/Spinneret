//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

// pickWeb returns the web identity at index i (scenarios use distinct identities).
func (f *fixture) pickWeb(i int) identityInfo {
	return f.web[i%len(f.web)]
}

// hotGroup returns the hot state of identity id for an endpoint group.
func (f *fixture) hotGroup(ctx context.Context, t *testing.T, id, groupID string) (*spinneretv1.IdentityHotState, *spinneretv1.EndpointHotState) {
	t.Helper()
	res, err := f.console.ids.GetIdentityHotState(ctx, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
	require.NoError(t, err, "GetIdentityHotState")
	hs := res.Msg.GetHotState()
	for _, g := range hs.GetGroups() {
		if g.GetEndpointGroupId() == groupID {
			return hs, g
		}
	}
	return hs, nil
}

// stateEvents lists the state events of an identity with the given actions.
func (f *fixture) stateEvents(ctx context.Context, t *testing.T, id string, actions ...string) []*spinneretv1.StateEvent {
	t.Helper()
	res, err := f.console.ids.ListStateEvents(ctx, connect.NewRequest(&spinneretv1.ListStateEventsRequest{
		Namespace: f.namespace, SubjectKind: "identity", SubjectId: id, Actions: actions, PageSize: 100,
	}))
	require.NoError(t, err, "ListStateEvents")
	return res.Msg.GetEvents()
}

func (f *fixture) identity(ctx context.Context, t *testing.T, id string) *spinneretv1.GetIdentityResponse {
	t.Helper()
	res, err := f.console.ids.GetIdentity(ctx, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
	require.NoError(t, err, "GetIdentity")
	return res.Msg
}

// scenarioRateLimited (c): the mock target answers 429 to one identity only; the identity gets an
// identity × endpoint cooldown while the other identities stay usable.
func scenarioRateLimited(ctx context.Context, t *testing.T, f *fixture) {
	target, other := f.pickWeb(1), f.pickWeb(2)
	search := f.group("web", "search")
	f.mock.setRules(ctx, t, mockRule{Prefix: searchURIPrefix, Mode: "rate_limit", Identities: []string{target.SessionID}})

	lease := f.acquireFor(ctx, t, "web", "/site/search?q=limited", target.ID, 30*time.Second)
	sent := time.Now()
	res := f.visit(ctx, t, lease, "/site/search?q=limited")
	require.Equal(t, 429, res.Status)

	eventually(t, 15*time.Second, 100*time.Millisecond, "identity × endpoint cooldown", func() (bool, string) {
		_, g := f.hotGroup(ctx, t, target.ID, search.GetId())
		if g == nil {
			return false, "no hot state for the search group"
		}
		cd := g.GetCooldownUntil()
		// The cooldown is 3s × U(0.8, 1.2) from the processing time of the report.
		ok := cd != nil && cd.AsTime().After(sent.Add(2*time.Second)) && g.GetConsecutiveFailures() >= 1
		return ok, describe("cooldown_until %v, failures %d", cd.AsTime(), g.GetConsecutiveFailures())
	})
	eventually(t, 15*time.Second, 250*time.Millisecond, "cooldown state event", func() (bool, string) {
		evs := f.stateEvents(ctx, t, target.ID, "cooldown")
		for _, ev := range evs {
			if ev.GetRule() == "rate-limited-cooldown" && ev.GetOutcome() == "rate_limited" && ev.GetScope() == "identity_endpoint" {
				return true, ""
			}
		}
		return false, describe("%d cooldown events", len(evs))
	})
	// The identity stays active; only its search endpoint is cooling down.
	require.Equal(t, "active", f.identity(ctx, t, target.ID).GetIdentity().GetState())

	// Another identity is served normally through the same rule set and has no cooldown.
	lease = f.acquireFor(ctx, t, "web", "/site/search?q=fine", other.ID, 30*time.Second)
	res = f.visit(ctx, t, lease, "/site/search?q=fine")
	require.Equal(t, 200, res.Status)
	_, g := f.hotGroup(ctx, t, other.ID, search.GetId())
	require.NotNil(t, g)
	if cd := g.GetCooldownUntil(); cd != nil {
		require.Falsef(t, cd.AsTime().After(time.Now()), "unaffected identity %s has a cooldown until %s", other.ID, cd.AsTime())
	}
	require.Empty(t, f.stateEvents(ctx, t, other.ID, "cooldown"), "unaffected identity must have no cooldown events")

	// The detail endpoint of the rate-limited identity is not affected by the search cooldown.
	lease = f.acquireFor(ctx, t, "web", "/site/item/7", target.ID, 30*time.Second)
	require.Equal(t, 200, f.visit(ctx, t, lease, "/site/item/7").Status)
}

// scenarioCaptchaBan (d): three captcha pages ban the identity (rule captcha-ban); a manual unban makes
// it leasable again.
func scenarioCaptchaBan(ctx context.Context, t *testing.T, f *fixture) {
	target := f.pickWeb(4)
	f.mock.setRules(ctx, t, mockRule{Prefix: "/site/", Mode: "captcha", Identities: []string{target.SessionID}})

	start := time.Now()
	for i := range 3 {
		// Each captcha puts the identity into a 5 s site cooldown; acquireFor waits for it to end.
		lease := f.acquireFor(ctx, t, "web", "/site/item/100", target.ID, 30*time.Second)
		res := f.visit(ctx, t, lease, "/site/item/100")
		require.Equalf(t, "captcha_page", res.Marker, "captcha %d", i+1)
		if i < 2 {
			eventually(t, 15*time.Second, 100*time.Millisecond, "captcha site cooldown", func() (bool, string) {
				evs := f.stateEvents(ctx, t, target.ID, "cooldown")
				n := 0
				for _, ev := range evs {
					if ev.GetRule() == "captcha-cooldown" && ev.GetScope() == "identity_site" {
						n++
					}
				}
				return n >= i+1, describe("%d captcha cooldown events", n)
			})
		}
	}
	eventually(t, 20*time.Second, 200*time.Millisecond, "identity banned", func() (bool, string) {
		idt := f.identity(ctx, t, target.ID).GetIdentity()
		return idt.GetState() == "banned", describe("state %s", idt.GetState())
	})
	idt := f.identity(ctx, t, target.ID)
	require.NotNil(t, idt.GetIdentity().GetBanUntil(), "a 1h ban is temporary")
	until := idt.GetIdentity().GetBanUntil().AsTime()
	require.WithinDuration(t, start.Add(time.Hour), until, 2*time.Minute)
	require.Equal(t, "banned", idt.GetHotState().GetState(), "the scheduler sees the ban")

	eventually(t, 15*time.Second, 250*time.Millisecond, "ban state event", func() (bool, string) {
		evs := f.stateEvents(ctx, t, target.ID, "ban")
		for _, ev := range evs {
			if ev.GetRule() == "captcha-ban" && ev.GetToState() == "banned" && ev.GetPolicyId() == f.policyIDs["action"] && ev.GetActor() == "system" {
				return true, ""
			}
		}
		return false, describe("%d ban events", len(evs))
	})

	// A banned identity is never leased.
	f.mock.clearRules(ctx, t)
	for range 3 {
		res, err := f.node.leases.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{
			Site: f.site, Client: "web", Uri: "/site/item/101", Count: 50, WaitMs: 200,
		}))
		if err != nil {
			continue
		}
		for _, l := range res.Msg.GetLeases() {
			require.NotEqual(t, target.ID, l.GetLease().GetIdentityId(), "banned identity leased")
			f.release(ctx, t, l.GetLease().GetLeaseId())
		}
	}

	op, err := f.console.ids.OperateIdentities(ctx, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{
		Ids: []string{target.ID}, Operation: "unban", Reason: "e2e: captcha solved", ResetFailures: true, ResetHealth: true,
	}))
	require.NoError(t, err, "OperateIdentities unban")
	require.EqualValues(t, 1, op.Msg.GetResult().GetSucceeded())
	state := f.identity(ctx, t, target.ID).GetIdentity().GetState()
	require.Containsf(t, []string{"active", "pending"}, state, "state after unban")

	lease := f.acquireFor(ctx, t, "web", "/site/item/102", target.ID, 30*time.Second)
	require.Equal(t, 200, f.visit(ctx, t, lease, "/site/item/102").Status)
	unbans := f.stateEvents(ctx, t, target.ID, "unban")
	require.NotEmpty(t, unbans, "manual unban recorded")
	require.Contains(t, unbans[0].GetActor(), "user:")
}

// scenarioLoginRedirect (e): a login redirect expires the identity and the identity_expired webhook is
// delivered with a valid signature.
func scenarioLoginRedirect(ctx context.Context, t *testing.T, f *fixture) {
	target := f.pickWeb(7)
	f.mock.setRules(ctx, t, mockRule{Prefix: "/site/", Mode: "login_redirect", Identities: []string{target.SessionID}})

	lease := f.acquireFor(ctx, t, "web", "/site/search?q=session", target.ID, 30*time.Second)
	res := f.visit(ctx, t, lease, "/site/search?q=session")
	require.Equal(t, 302, res.Status)
	require.Equal(t, "login_redirect", res.Marker)

	eventually(t, 20*time.Second, 200*time.Millisecond, "identity expired", func() (bool, string) {
		idt := f.identity(ctx, t, target.ID).GetIdentity()
		return idt.GetState() == "expired", describe("state %s", idt.GetState())
	})
	d := f.sink.wait(t, 90*time.Second, "identity_expired", func(d webhookDelivery) bool {
		return d.Kind == "identity_expired" && d.Details["identity_id"] == target.ID
	})
	require.Equal(t, f.site, d.Site)
	require.Equal(t, f.namespace, d.Namespace)
	require.Equal(t, webTypeName, d.Details["type"])
	f.sink.requireValidSignatures(t)

	evs := f.stateEvents(ctx, t, target.ID, "expire")
	require.NotEmpty(t, evs)
	require.Equal(t, "auth-invalid-expire", evs[0].GetRule())
	require.Equal(t, "auth_invalid", evs[0].GetOutcome())
}
