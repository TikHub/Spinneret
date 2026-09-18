//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
)

func (f *fixture) proxy(ctx context.Context, t *testing.T, id string) *spinneretv1.Proxy {
	t.Helper()
	res, err := f.console.proxies.GetProxy(ctx, connect.NewRequest(&spinneretv1.GetProxyRequest{Id: id}))
	require.NoError(t, err, "GetProxy")
	return res.Msg.GetProxy()
}

// scenarioProxyHealth (g): the mock proxy rejects one proxy with 407; the periodic health checks mark it
// dead and identities bound to it are rebound to a healthy proxy on their next acquire. When the proxy
// works again the health checker revives it.
func scenarioProxyHealth(ctx context.Context, t *testing.T, f *fixture) {
	// Choose the proxy with the most bound, leasable web identities.
	list, err := f.console.ids.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{
		Namespace: f.namespace, PageSize: 500,
		Filter: &spinneretv1.IdentityFilter{Site: f.site, Type: webTypeName, States: []string{"active", "pending"}},
	}))
	require.NoError(t, err, "ListIdentities")
	bound := map[string][]string{}
	for _, idt := range list.Msg.GetIdentities() {
		if p := idt.GetBoundProxyId(); p != "" {
			bound[p] = append(bound[p], idt.GetId())
		}
	}
	var dead proxyInfo
	for _, p := range f.proxies {
		if len(bound[p.ID]) > len(bound[dead.ID]) {
			dead = p
		}
	}
	require.NotEmpty(t, dead.ID, "identities must be bound to proxies after the crawl")
	victims := bound[dead.ID]
	t.Logf("failing proxy %s (%s) with %d bound identities", dead.Label, dead.ID, len(victims))

	f.mock.setProxyRules(ctx, t, mockProxyRule{ProxyID: dead.Label, Mode: "auth_fail"})
	defer f.mock.clearProxyRules(ctx, t)
	failedAt := time.Now()
	eventually(t, 45*time.Second, time.Second, "proxy marked dead by health checks", func() (bool, string) {
		p := f.proxy(ctx, t, dead.ID)
		return p.GetState() == "dead", describe("state %s, consecutive failures %d, last check ok %t",
			p.GetState(), p.GetConsecutiveCheckFailures(), p.GetLastCheckOk())
	})
	p := f.proxy(ctx, t, dead.ID)
	require.False(t, p.GetLastCheckOk())
	require.GreaterOrEqual(t, p.GetConsecutiveCheckFailures(), int32(3))
	t.Logf("proxy dead %s after the failure started", time.Since(failedAt).Round(time.Millisecond))

	// The scheduler sees the dead proxy (site hot state) before identities are rebound.
	eventually(t, 15*time.Second, 250*time.Millisecond, "dead proxy in site hot state", func() (bool, string) {
		for _, s := range f.proxy(ctx, t, dead.ID).GetSites() {
			if s.GetSiteId() == f.siteID {
				return s.GetState() == "dead", describe("site state %s", s.GetState())
			}
		}
		return false, "no site state"
	})

	checked, skipped := 0, 0
	for _, id := range victims {
		if checked == 3 || checked+skipped == 5 {
			break
		}
		// An identity that stays busy (cooldown, reuse interval from the other scenarios) is skipped:
		// the rebinding is proven by the identities that can be leased.
		lease, why := f.tryAcquireFor(ctx, t, "web", "/site/item/500", id, 20*time.Second)
		if lease == nil {
			skipped++
			t.Logf("skipping %s: %s", id, why)
			continue
		}
		checked++
		got := lease.GetProxy().GetProxyId()
		require.NotEqualf(t, dead.ID, got, "identity %s still leased with the dead proxy", id)
		require.Containsf(t, f.proxyByID, got, "identity %s leased with unknown proxy %s", id, got)
		res := f.visit(ctx, t, lease, "/site/item/500")
		require.Equal(t, 200, res.Status)
		require.Equal(t, f.proxyByID[got].Label, res.Proxy, "the request went through the rebound proxy")
		require.Equal(t, got, f.identity(ctx, t, id).GetIdentity().GetBoundProxyId(), "binding updated")
	}
	require.GreaterOrEqualf(t, checked, 2, "too few identities of the dead proxy could be re-leased (%d skipped)", skipped)

	// Recovery: the next successful check revives the proxy (dead checks back off from the interval).
	f.mock.clearProxyRules(ctx, t)
	eventually(t, 90*time.Second, time.Second, "proxy revived by health checks", func() (bool, string) {
		p := f.proxy(ctx, t, dead.ID)
		return p.GetState() == "active", describe("state %s, consecutive failures %d", p.GetState(), p.GetConsecutiveCheckFailures())
	})
}
