//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

var reportSeq atomic.Int64

// Crawler simulation parameters (scenario b).
const (
	crawlWorkers = 8
	// crawlCallTimeout bounds one RPC or target request of the crawl loop, independently of
	// the run deadline.
	crawlCallTimeout = 20 * time.Second
	// reuseTolerance absorbs timer granularity between the client's and the server's view of time.
	reuseTolerance = 25 * time.Millisecond
)

// crawlTracker checks lease invariants across the simulated crawler goroutines.
type crawlTracker struct {
	mu sync.Mutex
	// holder is the lease currently holding an identity (as seen by the crawler).
	holder map[string]string
	// lastReport is when the report releasing an identity was sent, per "<identity>|<endpoint group>".
	lastReport map[string]time.Time
	// proxyOf is the proxy of every lease of an identity.
	proxyOf map[string]map[string]int

	violations []string
	acquired   int
	reported   int
	statuses   map[int]int
	exhausted  int
}

func newCrawlTracker() *crawlTracker {
	return &crawlTracker{
		holder: map[string]string{}, lastReport: map[string]time.Time{},
		proxyOf: map[string]map[string]int{}, statuses: map[int]int{},
	}
}

func (c *crawlTracker) violation(format string, args ...any) {
	if len(c.violations) < 50 {
		c.violations = append(c.violations, fmt.Sprintf(format, args...))
	}
}

// onAcquire records an issued lease at time at (when the response was received).
func (c *crawlTracker) onAcquire(l *spinneretv1.AcquireResponse, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquired++
	id, lid, eg := l.GetLease().GetIdentityId(), l.GetLease().GetLeaseId(), l.GetLease().GetEndpointGroup()
	if prev, busy := c.holder[id]; busy {
		c.violation("identity %s leased by %s while still held by %s", id, lid, prev)
	}
	c.holder[id] = lid
	if last, ok := c.lastReport[id+"|"+eg]; ok {
		if gap := at.Sub(last); gap+reuseTolerance < reuseInterval {
			c.violation("identity %s reacquired for %s %s after its release report (reuse interval %s)", id, eg, gap, reuseInterval)
		}
	}
	if c.proxyOf[id] == nil {
		c.proxyOf[id] = map[string]int{}
	}
	c.proxyOf[id][l.GetProxy().GetProxyId()]++
}

// onReport records the release report of a lease right before it is sent at time at: once the server
// has the report, the identity may be leased again (immediately for another endpoint group).
func (c *crawlTracker) onReport(l *spinneretv1.AcquireResponse, at time.Time, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reported++
	c.statuses[status]++
	id := l.GetLease().GetIdentityId()
	if c.holder[id] == l.GetLease().GetLeaseId() {
		delete(c.holder, id)
	}
	c.lastReport[id+"|"+l.GetLease().GetEndpointGroup()] = at
}

// scenarioCrawl (b) runs crawlWorkers goroutines that acquire, request the mock target through the
// leased proxy with the credential and report with release=true, then checks the lease invariants, the
// proxy binding and the ClickHouse request events.
func scenarioCrawl(ctx context.Context, t *testing.T, f *fixture) {
	// Every replica must serve the site before the workers start (the previous scenario paused and
	// resumed it, and catalog invalidations reach the other replicas asynchronously).
	f.waitLeasable(ctx, t, "web", "/site/search?q=warmup")
	tracker := newCrawlTracker()
	runCtx, cancel := context.WithTimeout(ctx, f.cfg.CrawlDuration)
	defer cancel()
	start := time.Now()
	var errMu sync.Mutex
	var errs []string
	var wg sync.WaitGroup
	for w := range crawlWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; runCtx.Err() == nil; i++ {
				if err := crawlOnce(runCtx, f, tracker, w, i); err != nil && runCtx.Err() == nil {
					errMu.Lock()
					if len(errs) < 20 {
						errs = append(errs, err.Error())
					}
					errMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	t.Logf("crawl: %d acquired, %d reported in %s (%.1f req/s), statuses %v, exhausted waits %d",
		tracker.acquired, tracker.reported, elapsed.Round(time.Millisecond),
		float64(tracker.reported)/elapsed.Seconds(), tracker.statuses, tracker.exhausted)
	require.Empty(t, errs, "crawler errors")
	require.Empty(t, tracker.violations, "lease invariant violations")
	require.Greater(t, tracker.reported, 100, "the simulation must exercise the scheduler")
	require.Equal(t, tracker.reported, tracker.statuses[200], "the mock target answers ok without rules")

	// bind_identity: every identity used exactly one proxy.
	used := 0
	for id, proxies := range tracker.proxyOf {
		used++
		require.Lenf(t, proxies, 1, "identity %s used several proxies: %v", id, proxies)
		for pid := range proxies {
			_, known := f.proxyByID[pid]
			require.Truef(t, known, "identity %s leased with unknown proxy %q", id, pid)
		}
	}
	// Rotation spreads over the pool; `pending` identities carry the probe weight factor (0.1), so a
	// few of them can stay unused in a short run.
	require.GreaterOrEqualf(t, used, webIdentities*3/4, "the rotation must use most of the %d identities", webIdentities)

	// The mock target saw each session id through exactly the proxy the scheduler assigned.
	stats := f.mock.stats(ctx, t, url.Values{"path_prefix": {"/site/"}})
	seen := map[string]map[string]int64{}
	for _, e := range stats.Entries {
		if !strings.HasPrefix(e.Identity, "s"+f.runID+"-") {
			continue
		}
		if seen[e.Identity] == nil {
			seen[e.Identity] = map[string]int64{}
		}
		seen[e.Identity][e.Proxy] += e.Count
	}
	var mockRequests int64
	for _, idt := range f.web {
		proxies := seen[idt.SessionID]
		if _, leased := tracker.proxyOf[idt.ID]; !leased {
			require.Emptyf(t, proxies, "unused identity %s reached the target", idt.SessionID)
			continue
		}
		require.Lenf(t, proxies, 1, "mock target saw session %s through proxies %v", idt.SessionID, proxies)
		var assigned string
		for pid := range tracker.proxyOf[idt.ID] {
			assigned = f.proxyByID[pid].Label
		}
		for label, n := range proxies {
			require.Equalf(t, assigned, label, "session %s went through %s instead of %s", idt.SessionID, label, assigned)
			mockRequests += n
		}
	}
	require.EqualValues(t, tracker.reported, mockRequests, "every report corresponds to one mock target request")

	// Probe activation: successful reports activate pending identities.
	eventually(t, 30*time.Second, 500*time.Millisecond, "web identities activated", func() (bool, string) {
		res, err := f.console.ids.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{
			Namespace: f.namespace, PageSize: 500,
			Filter: &spinneretv1.IdentityFilter{Site: f.site, Type: webTypeName, States: []string{"active"}},
		}))
		if err != nil {
			return false, err.Error()
		}
		n := len(res.Msg.GetIdentities())
		// Every identity that served a successful request is activated; unused ones stay pending.
		return n >= used, describe("%d active, %d identities used", n, used)
	})

	// Raw request events reach ClickHouse (batched writer).
	eventually(t, 90*time.Second, 2*time.Second, "ClickHouse request events", func() (bool, string) {
		res, err := f.console.dash.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{
			Namespace: f.namespace, Site: f.site, PageSize: 10, IncludeSummary: true,
			TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(start.Add(-time.Minute)), End: timestamppb.New(time.Now().Add(time.Minute))},
		}))
		if err != nil {
			return false, err.Error()
		}
		total := res.Msg.GetSummary().GetTotal()
		outcomes := res.Msg.GetSummary().GetOutcomes()
		if total < int64(tracker.reported) {
			return false, describe("total %d of %d, outcomes %v", total, tracker.reported, outcomes)
		}
		for _, ev := range res.Msg.GetEvents() {
			if ev.GetSite() != f.site || ev.GetIdentityId() == "" || ev.GetProxyId() == "" || ev.GetNode() != f.node.name {
				return false, describe("unexpected event %v", ev)
			}
		}
		return outcomes["success"] >= int64(tracker.statuses[200]), describe("total %d, outcomes %v", total, outcomes)
	})
}

// crawlOnce performs one acquire → request → report cycle of worker w.
func crawlOnce(ctx context.Context, f *fixture, tracker *crawlTracker, w, i int) error {
	uri := fmt.Sprintf("/site/search?q=w%d-%d", w, i)
	if i%2 == 1 {
		uri = fmt.Sprintf("/site/item/%d", w*1000+i)
	}
	// The acquire waits up to WaitMs for a free identity, so it must not inherit the run
	// deadline: Connect forwards that deadline to the server, and near the end of the run the
	// server answers deadline_exceeded a moment before the client's own timer fires — a race
	// that turns the last in-flight acquire of each worker into a spurious "crawler error".
	// The loop below stops the run; this call only needs its own upper bound.
	actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), crawlCallTimeout)
	defer acancel()
	res, err := f.node.leases.Acquire(actx, connect.NewRequest(&spinneretv1.AcquireRequest{
		Site: f.site, Client: "web", Uri: uri, WaitMs: 5000,
	}))
	if err != nil {
		if ae, ok := asAPIError(err); ok && ae.Code == connect.CodeResourceExhausted {
			tracker.mu.Lock()
			tracker.exhausted++
			tracker.mu.Unlock()
			sleepCtx(ctx, min(max(ae.RetryAfter, 50*time.Millisecond), time.Second))
			return nil
		}
		return fmt.Errorf("acquire %s: %w", uri, err)
	}
	lease := res.Msg
	tracker.onAcquire(lease, time.Now())
	wantGroup := "search"
	if i%2 == 1 {
		wantGroup = "detail"
	}
	if lease.GetLease().GetEndpointGroup() != wantGroup {
		return fmt.Errorf("uri %s matched endpoint group %q", uri, lease.GetLease().GetEndpointGroup())
	}
	if lease.GetProxy() == nil || lease.GetCredential().GetCookieHeader() == "" || lease.GetCredential().GetCookies()["sessionid"] == "" {
		return fmt.Errorf("lease %s without proxy or cookie credential", lease.GetLease().GetLeaseId())
	}
	// A started cycle always completes: the request and its release report must not be cut off by the end
	// of the run, otherwise the lease would stay active until its TTL and disturb the next scenarios.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), crawlCallTimeout)
	defer cancel()
	fetched := f.crawler.fetch(wctx, lease, uri)
	if fetched.ErrorKind != "" {
		_, _ = f.node.leases.Release(wctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: lease.GetLease().GetLeaseId()}))
		return fmt.Errorf("request %s through %s: %s", uri, lease.GetProxy().GetProxyId(), fetched.ErrorKind)
	}
	if fetched.Identity != lease.GetCredential().GetCookies()["sessionid"] {
		return fmt.Errorf("mock target attributed %s to %q", uri, fetched.Identity)
	}
	tracker.onReport(lease, time.Now(), fetched.Status)
	out, err := f.node.reports.Report(wctx, connect.NewRequest(&spinneretv1.ReportRequest{Reports: []*spinneretv1.Report{
		fetched.report(lease.GetLease().GetLeaseId(), uri, reportID(f.runID), true),
	}}))
	if err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if out.Msg.GetAccepted() != 1 || len(out.Msg.GetRejected()) > 0 {
		return fmt.Errorf("report not accepted: %v", out.Msg)
	}
	return nil
}
