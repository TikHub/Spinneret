package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	chstore "github.com/TikHub/Spinneret/internal/store/clickhouse"
)

type reportRow struct {
	at                                                     time.Time
	ns, siteID, site, client, group, identity, proxy, node string
	lease, report, outcome                                 string
	status                                                 uint16
	latency                                                uint32
}

func (e *env) reportEvents(rows ...reportRow) {
	e.t.Helper()
	batch, err := e.ch.PrepareBatch(e.ctx, "INSERT INTO "+chstore.ReportEventsTable+" (event_time, received_at, started_at, "+
		"tenant_id, namespace_id, site_id, site, endpoint_group, client, identity_id, identity_type, proxy_id, lease_id, report_id, "+
		"node, token_id, uri, method, http_status, business_code, error_kind, markers, outcome, outcome_hint, blame, rule, "+
		"latency_ms, response_bytes, suppressed, late, probe)")
	require.NoError(e.t, err)
	for _, r := range rows {
		require.NoError(e.t, batch.Append(r.at, r.at.Add(time.Second), r.at.Add(-time.Second),
			tenantID, r.ns, r.siteID, r.site, r.group, r.client, r.identity, "cookie", r.proxy, r.lease, r.report,
			r.node, "tok_1", "/api/x", "GET", r.status, "0", "", []string{"m1"}, r.outcome, "", "identity", "rule-1",
			r.latency, uint64(512), uint8(1), uint8(0), uint8(1)))
	}
	require.NoError(e.t, batch.Send())
}

func reportIDs(events []RequestEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.ReportID
	}
	return out
}

func TestRequestEvents(t *testing.T) {
	e := newEnv(t, true)
	base := e.now.Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.reportEvents(
		reportRow{at: base, ns: namespaceID, siteID: siteA, site: "shop", client: "web", group: "search", identity: "idt_1", proxy: "pxy_1", node: "n1", lease: "lse_a", report: "r1", outcome: "success", status: 200, latency: 100},
		reportRow{at: base, ns: namespaceID, siteID: siteA, site: "shop", client: "web", group: "search", identity: "idt_2", node: "n1", lease: "lse_b", report: "r2", outcome: "rate_limited", status: 429, latency: 200},
		reportRow{at: base, ns: namespaceID, siteID: siteA, site: "shop", client: "web", group: "search", identity: "idt_2", node: "n1", lease: "lse_a", report: "r2", outcome: "success", status: 200, latency: 300},
		reportRow{at: base.Add(time.Minute), ns: namespaceID, siteID: siteA, site: "shop", client: "app", group: "feed", identity: "idt_3", node: "n2", lease: "lse_c", report: "r3", outcome: "captcha", status: 200, latency: 400},
		reportRow{at: base.Add(2 * time.Minute), ns: namespaceID, siteID: siteB, site: "forum", client: "web", group: "search", identity: "idt_4", node: "n2", lease: "lse_d", report: "r4", outcome: "success", status: 0, latency: 1000},
		reportRow{at: e.now.Add(-2 * time.Hour), ns: namespaceID, siteID: siteA, site: "shop", client: "web", group: "search", identity: "idt_1", node: "n1", lease: "lse_e", report: "r5", outcome: "success", status: 200, latency: 50},
		reportRow{at: base, ns: "ns_other", siteID: siteA, site: "shop", client: "web", group: "search", identity: "idt_1", node: "n1", lease: "lse_f", report: "r6", outcome: "success", status: 200, latency: 50},
	)

	page, err := e.svc.RequestEvents(e.ctx, allScope(), RequestEventQuery{PageSize: 2, IncludeSummary: true})
	require.NoError(t, err)
	require.Equal(t, []string{"r4", "r3"}, reportIDs(page.Events))
	require.NotNil(t, page.Next)
	require.Equal(t, RequestCursor{EventTimeMs: base.Add(time.Minute).UnixMilli(), ReportID: "r3", LeaseID: "lse_c"}, *page.Next)
	require.NotNil(t, page.Summary)
	require.Equal(t, int64(5), page.Summary.Total)
	require.Equal(t, map[string]int64{"success": 3, "rate_limited": 1, "captcha": 1}, page.Summary.Outcomes)
	require.InDelta(t, 2000.0/5, page.Summary.LatencyAvgMs, 1e-9)
	require.InDelta(t, 300.0, page.Summary.LatencyP50Ms, 1e-9)
	require.Greater(t, page.Summary.LatencyP95Ms, 400.0)
	require.LessOrEqual(t, page.Summary.LatencyP99Ms, 1000.0)
	require.GreaterOrEqual(t, page.Summary.LatencyP99Ms, page.Summary.LatencyP95Ms)

	ev := page.Events[1]
	require.True(t, base.Add(time.Minute).Equal(ev.EventTime))
	require.True(t, base.Add(time.Minute+time.Second).Equal(ev.ReceivedAt))
	require.True(t, base.Add(time.Minute-time.Second).Equal(ev.StartedAt))
	require.Equal(t, siteA, ev.SiteID)
	require.Equal(t, "shop", ev.Site)
	require.Equal(t, "app", ev.Client)
	require.Equal(t, "feed", ev.EndpointGroup)
	require.Equal(t, "idt_3", ev.IdentityID)
	require.Equal(t, "cookie", ev.IdentityType)
	require.Equal(t, "n2", ev.Node)
	require.Equal(t, "tok_1", ev.TokenID)
	require.Equal(t, "/api/x", ev.URI)
	require.Equal(t, "GET", ev.Method)
	require.Equal(t, int32(200), ev.HTTPStatus)
	require.Equal(t, []string{"m1"}, ev.Markers)
	require.Equal(t, "captcha", ev.Outcome)
	require.Equal(t, "identity", ev.Blame)
	require.Equal(t, "rule-1", ev.Rule)
	require.Equal(t, int64(400), ev.LatencyMs)
	require.Equal(t, int64(512), ev.ResponseBytes)
	require.True(t, ev.Suppressed)
	require.False(t, ev.Late)
	require.True(t, ev.Probe)

	// Second page: ties on event_time are ordered by report_id, lease_id.
	page, err = e.svc.RequestEvents(e.ctx, allScope(), RequestEventQuery{PageSize: 2, Cursor: page.Next})
	require.NoError(t, err)
	require.Equal(t, []string{"r2", "r2"}, reportIDs(page.Events))
	require.Equal(t, []string{"lse_b", "lse_a"}, []string{page.Events[0].LeaseID, page.Events[1].LeaseID})
	require.Nil(t, page.Summary)
	page, err = e.svc.RequestEvents(e.ctx, allScope(), RequestEventQuery{PageSize: 2, Cursor: page.Next})
	require.NoError(t, err)
	require.Equal(t, []string{"r1"}, reportIDs(page.Events))
	require.Nil(t, page.Next)

	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name  string
		scope Scope
		q     RequestEventQuery
		want  []string
	}{
		{"site", allScope(), RequestEventQuery{SiteID: siteB}, []string{"r4"}},
		{"client", allScope(), RequestEventQuery{Client: "app"}, []string{"r3"}},
		{"endpoint group via catalog", allScope(), RequestEventQuery{EndpointGroupID: egSearch}, []string{"r2", "r2", "r1"}},
		{"endpoint group with site and client", allScope(), RequestEventQuery{SiteID: siteA, Client: "web", EndpointGroupID: egSearch}, []string{"r2", "r2", "r1"}},
		{"outcomes", allScope(), RequestEventQuery{Outcomes: []string{"captcha", "rate_limited"}}, []string{"r3", "r2"}},
		{"identity", allScope(), RequestEventQuery{IdentityID: "idt_2"}, []string{"r2", "r2"}},
		{"proxy", allScope(), RequestEventQuery{ProxyID: "pxy_1"}, []string{"r1"}},
		{"node", allScope(), RequestEventQuery{Node: "n2"}, []string{"r4", "r3"}},
		{"lease", allScope(), RequestEventQuery{LeaseID: "lse_a"}, []string{"r2", "r1"}},
		{"report", allScope(), RequestEventQuery{ReportID: "r2"}, []string{"r2", "r2"}},
		{"http status", allScope(), RequestEventQuery{HTTPStatus: ptr(429)}, []string{"r2"}},
		{"no response", allScope(), RequestEventQuery{HTTPStatus: ptr(0)}, []string{"r4"}},
		{"min latency", allScope(), RequestEventQuery{MinLatencyMs: 300}, []string{"r4", "r3", "r2"}},
		{"explicit range", allScope(), RequestEventQuery{Range: TimeRange{Start: ptr(e.now.Add(-3 * time.Hour)), End: ptr(base)}}, []string{"r5"}},
		{"restricted scope", restricted, RequestEventQuery{}, []string{"r4"}},
		{"no readable site", Scope{NamespaceID: namespaceID}, RequestEventQuery{}, []string{}},
		{"injection attempt is a plain value", allScope(), RequestEventQuery{Node: "n1' OR 1=1 --"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := e.svc.RequestEvents(e.ctx, tc.scope, tc.q)
			require.NoError(t, err)
			require.Equal(t, tc.want, reportIDs(page.Events))
			require.Nil(t, page.Next)
		})
	}

	summary, err := e.svc.RequestEvents(e.ctx, Scope{NamespaceID: namespaceID}, RequestEventQuery{IncludeSummary: true})
	require.NoError(t, err)
	require.Equal(t, &RequestEventsSummary{Outcomes: map[string]int64{}}, summary.Summary)
	summary, err = e.svc.RequestEvents(e.ctx, allScope(), RequestEventQuery{IncludeSummary: true, Node: "nobody"})
	require.NoError(t, err)
	require.Zero(t, summary.Summary.Total)
	require.Zero(t, summary.Summary.LatencyP99Ms)
}

func TestRequestEventsErrors(t *testing.T) {
	e := newEnv(t, true)
	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name   string
		scope  Scope
		q      RequestEventQuery
		reason apperr.Reason
	}{
		{"range too long", allScope(), RequestEventQuery{Range: TimeRange{Start: ptr(e.now.Add(-8 * 24 * time.Hour))}}, apperr.ReasonInvalidArgument},
		{"unknown outcome", allScope(), RequestEventQuery{Outcomes: []string{"nope"}}, apperr.ReasonInvalidArgument},
		{"negative status", allScope(), RequestEventQuery{HTTPStatus: ptr(-1)}, apperr.ReasonInvalidArgument},
		{"negative latency", allScope(), RequestEventQuery{MinLatencyMs: -1}, apperr.ReasonInvalidArgument},
		{"unknown group", allScope(), RequestEventQuery{EndpointGroupID: "eg_missing"}, apperr.ReasonEndpointGroupUnknown},
		{"group of another site", allScope(), RequestEventQuery{SiteID: siteB, EndpointGroupID: egSearch}, apperr.ReasonEndpointGroupUnknown},
		{"group of another client", allScope(), RequestEventQuery{Client: "app", EndpointGroupID: egSearch}, apperr.ReasonEndpointGroupUnknown},
		{"unreadable site", restricted, RequestEventQuery{SiteID: siteA}, apperr.ReasonPermissionDenied},
		{"unreadable group", restricted, RequestEventQuery{EndpointGroupID: egSearch}, apperr.ReasonPermissionDenied},
		{"invalid cursor", allScope(), RequestEventQuery{Cursor: &RequestCursor{}}, apperr.ReasonInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.RequestEvents(e.ctx, tc.scope, tc.q)
			require.Error(t, err)
			require.Equal(t, tc.reason, apperr.ReasonOf(err), "error: %v", err)
		})
	}
	require.True(t, e.svc.ClickHouseEnabled())
}

func TestRequestEventsClickHouseDisabled(t *testing.T) {
	svc := New(nil, nil, nil, testKeys(), nil, nil)
	require.False(t, svc.ClickHouseEnabled())
	_, err := svc.RequestEvents(t.Context(), allScope(), RequestEventQuery{})
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok)
	require.Equal(t, "unavailable", e.Code.String())
	require.Equal(t, apperr.ReasonFailedPrecondition, e.Reason)
}

func TestCeilMillis(t *testing.T) {
	ts := time.UnixMilli(1_000).Add(time.Microsecond)
	require.Equal(t, int64(1_001), ceilMillis(ts))
	require.Equal(t, int64(1_000), ceilMillis(time.UnixMilli(1_000)))
}
