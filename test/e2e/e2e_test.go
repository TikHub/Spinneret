//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

// TestStack runs the scenarios in order against one fixture. Scenarios share the mock target, whose
// scripted rules are global, so they never run in parallel; each scenario resets the rules it sets.
func TestStack(t *testing.T) {
	cfg := loadConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()

	start := time.Now()
	f := setupFixture(ctx, t, cfg)
	t.Logf("setup (scenario a) took %s", time.Since(start).Round(time.Millisecond))

	scenarios := []struct {
		name string
		run  func(context.Context, *testing.T, *fixture)
	}{
		{"i_scale_out", scenarioScaleOut},
		{"h_config_secret_runtime", scenarioConfigSecretRuntime},
		{"b_crawler_simulation", scenarioCrawl},
		{"c_rate_limited_cooldown", scenarioRateLimited},
		{"d_captcha_ban_unban", scenarioCaptchaBan},
		{"e_login_redirect_expire_webhook", scenarioLoginRedirect},
		{"g_proxy_health_rebind", scenarioProxyHealth},
		{"f_breaker_open_half_open_close", scenarioBreaker},
	}
	for _, sc := range scenarios {
		began := time.Now()
		ok := t.Run(sc.name, func(t *testing.T) {
			defer f.mock.clearRules(ctx, t)
			sc.run(ctx, t, f)
		})
		t.Logf("scenario %s: ok=%t in %s", sc.name, ok, time.Since(began).Round(time.Millisecond))
	}
	f.sink.requireValidSignatures(t)
}

// tryAcquireFor leases the given identity for uri: it takes a batch of every available identity, keeps
// the lease of the wanted one and releases the others, retrying until timeout. Between attempts it
// waits out the reuse interval the released leases applied, so every attempt sees the whole pool.
func (f *fixture) tryAcquireFor(ctx context.Context, t *testing.T, client, uri, identityID string, timeout time.Duration) (*spinneretv1.AcquireResponse, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "no attempt"
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		if attempt > 0 && !sleepCtx(ctx, reuseInterval+500*time.Millisecond) {
			break
		}
		res, err := f.node.leases.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{
			Site: f.site, Client: client, Uri: uri, Count: 50, WaitMs: 1000,
		}))
		if err != nil {
			last = err.Error()
			continue
		}
		var found *spinneretv1.AcquireResponse
		for _, l := range res.Msg.GetLeases() {
			if l.GetLease().GetIdentityId() == identityID && found == nil {
				found = l
				continue
			}
			f.release(ctx, t, l.GetLease().GetLeaseId())
		}
		if found != nil {
			return found, ""
		}
		last = describe("%d leases without %s", len(res.Msg.GetLeases()), identityID)
	}
	return nil, describe("identity %s not leasable for %s within %s: %s", identityID, uri, timeout, last)
}

// acquireFor is tryAcquireFor, failing the test when the identity stays unavailable.
func (f *fixture) acquireFor(ctx context.Context, t *testing.T, client, uri, identityID string, timeout time.Duration) *spinneretv1.AcquireResponse {
	t.Helper()
	lease, why := f.tryAcquireFor(ctx, t, client, uri, identityID, timeout)
	if lease == nil {
		t.Fatal(why)
	}
	return lease
}

// release ends a lease without a report.
func (f *fixture) release(ctx context.Context, t *testing.T, leaseID string) {
	t.Helper()
	_, err := f.node.leases.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: leaseID}))
	require.NoErrorf(t, err, "release %s", leaseID)
}

// visit performs the target request of a lease and reports it with release=true.
func (f *fixture) visit(ctx context.Context, t *testing.T, lease *spinneretv1.AcquireResponse, uri string) fetchResult {
	t.Helper()
	res := f.crawler.fetch(ctx, lease, uri)
	f.sendReport(ctx, t, res.report(lease.GetLease().GetLeaseId(), uri, reportID(f.runID), true))
	return res
}

// sendReport sends one report and requires it to be accepted.
func (f *fixture) sendReport(ctx context.Context, t *testing.T, r *spinneretv1.Report) {
	t.Helper()
	out, err := f.node.reports.Report(ctx, connect.NewRequest(&spinneretv1.ReportRequest{Reports: []*spinneretv1.Report{r}}))
	require.NoError(t, err, "Report")
	require.Emptyf(t, out.Msg.GetRejected(), "report %s rejected", r.GetReportId())
	require.EqualValues(t, 1, out.Msg.GetAccepted())
}

// reportID returns a unique report id.
func reportID(runID string) string {
	return fmt.Sprintf("r-%s-%d", runID, reportSeq.Add(1))
}
