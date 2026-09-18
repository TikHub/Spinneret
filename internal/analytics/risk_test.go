package analytics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

type riskRow struct {
	id, ns, site, eg, identity, proxy, node, outcome string
	at                                               time.Time
}

func (e *env) riskEvent(r riskRow) {
	e.t.Helper()
	e.exec(`INSERT INTO risk_events (id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, identity_id, proxy_id,
		lease_id, report_id, node, token_id, uri, method, http_status, business_code, error_kind, markers, outcome, blame, rule,
		latency_ms, response_bytes, started_at, finished_at)
		VALUES ($1::text, $2::timestamptz, $3, $4, $5, $6, $7, $8, 'lse_1', 'rep_' || $1::text, $9, 'tok_1', '/api/search', 'GET', 429, '0', '',
		'{empty_list}', $10, 'both', 'http-429', 812, 2048, $2::timestamptz - interval '1 second', $2::timestamptz)`,
		r.id, r.at, tenantID, r.ns, r.site, r.eg, r.identity, r.proxy, r.node, r.outcome)
}

func ids(events []RiskEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.ID
	}
	return out
}

func TestRiskEvents(t *testing.T) {
	e := newEnv(t, false)
	e.exec(`SELECT spinneret_ensure_partitions('risk_events', 'day', $1::timestamptz - interval '3 days', $1::timestamptz)`, e.now)
	tie := e.now.Add(-10 * time.Minute).Truncate(time.Millisecond).Add(123 * time.Microsecond)
	for _, r := range []riskRow{
		{id: "rsk_01", ns: namespaceID, site: siteA, eg: egSearch, identity: "idt_1", proxy: "pxy_1", node: "n1", outcome: "rate_limited", at: tie},
		{id: "rsk_02", ns: namespaceID, site: siteA, eg: egSearch, identity: "idt_2", node: "n1", outcome: "rate_limited", at: tie},
		{id: "rsk_03", ns: namespaceID, site: siteB, eg: egForumSearch, identity: "idt_3", node: "n2", outcome: "captcha", at: e.now.Add(-5 * time.Minute)},
		{id: "rsk_04", ns: namespaceID, site: siteA, eg: egFeed, identity: "idt_1", node: "n1", outcome: "banned", at: e.now.Add(-2 * time.Hour)},
		{id: "rsk_05", ns: namespaceID, site: siteA, eg: "eg_gone", node: "n1", outcome: "unknown", at: e.now.Add(-30 * time.Hour)},
		{id: "rsk_06", ns: "ns_other", site: siteA, eg: egSearch, node: "n1", outcome: "captcha", at: e.now.Add(-time.Minute)},
		{id: "rsk_07", ns: namespaceID, site: siteA, eg: egSearch, node: "n1", outcome: "captcha", at: e.now.Add(time.Minute)},
	} {
		e.riskEvent(r)
	}

	page, err := e.svc.RiskEvents(e.ctx, allScope(), RiskEventQuery{PageSize: 2})
	require.NoError(t, err)
	require.Equal(t, []string{"rsk_03", "rsk_02"}, ids(page.Events))
	require.NotNil(t, page.Next)
	require.Equal(t, "rsk_02", page.Next.ID)
	require.Equal(t, tie.UnixMicro(), page.Next.CreatedAtUs)

	ev := page.Events[1]
	require.Equal(t, "shop", ev.Site)
	require.Equal(t, "search", ev.EndpointGroup)
	require.Equal(t, "web", ev.Client)
	require.True(t, tie.Equal(ev.CreatedAt))
	require.Equal(t, "rep_rsk_02", ev.ReportID)
	require.Equal(t, int32(429), ev.HTTPStatus)
	require.Equal(t, []string{"empty_list"}, ev.Markers)
	require.Equal(t, "both", ev.Blame)
	require.Equal(t, "http-429", ev.Rule)
	require.Equal(t, int32(812), ev.LatencyMs)
	require.Equal(t, int64(2048), ev.ResponseBytes)
	require.NotNil(t, ev.StartedAt)
	require.True(t, tie.Equal(*ev.FinishedAt))

	page, err = e.svc.RiskEvents(e.ctx, allScope(), RiskEventQuery{PageSize: 2, Cursor: page.Next})
	require.NoError(t, err)
	require.Equal(t, []string{"rsk_01", "rsk_04"}, ids(page.Events))
	require.Nil(t, page.Next)

	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name  string
		scope Scope
		q     RiskEventQuery
		want  []string
	}{
		{"outcome", allScope(), RiskEventQuery{Outcome: "captcha"}, []string{"rsk_03"}},
		{"endpoint group", allScope(), RiskEventQuery{EndpointGroupID: egSearch}, []string{"rsk_02", "rsk_01"}},
		{"site", allScope(), RiskEventQuery{SiteID: siteA}, []string{"rsk_02", "rsk_01", "rsk_04"}},
		{"site and group", allScope(), RiskEventQuery{SiteID: siteA, EndpointGroupID: egFeed}, []string{"rsk_04"}},
		{"identity", allScope(), RiskEventQuery{IdentityID: "idt_1"}, []string{"rsk_01", "rsk_04"}},
		{"proxy", allScope(), RiskEventQuery{ProxyID: "pxy_1"}, []string{"rsk_01"}},
		{"node", allScope(), RiskEventQuery{Node: "n2"}, []string{"rsk_03"}},
		{"explicit range", allScope(), RiskEventQuery{SiteID: siteA, Range: TimeRange{Start: ptr(e.now.Add(-31 * time.Hour)), End: ptr(e.now.Add(-time.Hour))}}, []string{"rsk_04", "rsk_05"}},
		{"restricted scope", restricted, RiskEventQuery{}, []string{"rsk_03"}},
		{"no readable site", Scope{NamespaceID: namespaceID}, RiskEventQuery{}, []string{}},
		{"cursor after range end", allScope(), RiskEventQuery{Cursor: &RiskCursor{CreatedAtUs: e.now.Add(time.Hour).UnixMicro(), ID: "rsk_99"}}, []string{"rsk_03", "rsk_02", "rsk_01", "rsk_04"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := e.svc.RiskEvents(e.ctx, tc.scope, tc.q)
			require.NoError(t, err)
			require.Equal(t, tc.want, ids(page.Events))
			require.Nil(t, page.Next)
		})
	}

	old, err := e.svc.RiskEvents(e.ctx, allScope(), RiskEventQuery{Range: TimeRange{Start: ptr(e.now.Add(-31 * time.Hour))}})
	require.NoError(t, err)
	gone := old.Events[len(old.Events)-1]
	require.Equal(t, "rsk_05", gone.ID)
	require.Equal(t, "shop", gone.Site)
	require.Empty(t, gone.EndpointGroup)
	require.Empty(t, gone.Client)
}

func TestRiskEventsErrors(t *testing.T) {
	e := newEnv(t, false)
	restricted := Scope{NamespaceID: namespaceID, SiteIDs: []string{siteB}}
	for _, tc := range []struct {
		name   string
		scope  Scope
		q      RiskEventQuery
		reason apperr.Reason
	}{
		{"success outcome", allScope(), RiskEventQuery{Outcome: "success"}, apperr.ReasonInvalidArgument},
		{"unknown outcome", allScope(), RiskEventQuery{Outcome: "weird"}, apperr.ReasonInvalidArgument},
		{"range too long", allScope(), RiskEventQuery{Range: TimeRange{Start: ptr(e.now.Add(-32 * 24 * time.Hour))}}, apperr.ReasonInvalidArgument},
		{"unknown group", allScope(), RiskEventQuery{EndpointGroupID: "eg_missing"}, apperr.ReasonEndpointGroupUnknown},
		{"group of another site", allScope(), RiskEventQuery{SiteID: siteB, EndpointGroupID: egSearch}, apperr.ReasonEndpointGroupUnknown},
		{"unreadable site", restricted, RiskEventQuery{SiteID: siteA}, apperr.ReasonPermissionDenied},
		{"unreadable group", restricted, RiskEventQuery{EndpointGroupID: egFeed}, apperr.ReasonPermissionDenied},
		{"invalid cursor", allScope(), RiskEventQuery{Cursor: &RiskCursor{CreatedAtUs: 1}}, apperr.ReasonInvalidArgument},
		{"unknown namespace", Scope{NamespaceID: "ns_missing"}, RiskEventQuery{}, apperr.ReasonNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.RiskEvents(e.ctx, tc.scope, tc.q)
			require.Error(t, err)
			require.Equal(t, tc.reason, apperr.ReasonOf(err), "error: %v", err)
		})
	}
	require.Equal(t, DefaultEventPageSize, normalizePageSize(0))
	require.Equal(t, MaxEventPageSize, normalizePageSize(10_000))
	require.Equal(t, 7, normalizePageSize(7))
}
