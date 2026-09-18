package stats

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func TestIntegrationClickHouseEvents(t *testing.T) {
	t.Parallel()
	conn := testutil.ClickHouse(t)
	writer := clickhouse.NewWriter(conn, nil, 50*time.Millisecond, 1000)
	wctx, wcancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- writer.Run(wctx) }()

	now := time.Now().UTC().Truncate(time.Millisecond)
	a := NewAggregator(nil, writer, nil, nil)
	a.now = func() time.Time { return now }

	acquire := AcquireRecord{At: now, TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", Site: "shop",
		EndpointGroupID: "eg_1", EndpointGroup: "feed", Client: "web", IdentityTypeID: "ity_1", IdentityID: "idt_1",
		ProxyID: "pxy_1", LeaseID: "lse_1", Node: "node-a", TokenID: "tok_1", Result: ResultOK,
		Duration: 1234 * time.Microsecond, Probe: true, Sticky: true}
	a.RecordAcquire(acquire)
	failed := acquire
	failed.Result = ResultExhausted
	failed.LeaseID = ""
	a.RecordAcquire(failed)
	a.RecordLeaseEnd(LeaseEndRecord{At: now, TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", Site: "shop",
		EndpointGroup: "feed", Client: "web", IdentityID: "idt_1", LeaseID: "lse_1", Node: "node-a", TokenID: "tok_1",
		Kind: LeaseEndAbandoned})
	finished := now.Add(-time.Second)
	a.RecordReport(ReportRecord{ReceivedAt: now, StartedAt: finished.Add(-500 * time.Millisecond), FinishedAt: finished,
		TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", Site: "shop", EndpointGroup: "feed", Client: "web",
		IdentityID: "idt_1", IdentityType: "cookie", ProxyID: "pxy_1", LeaseID: "lse_1", ReportID: "rep-1",
		Node: "node-a", TokenID: "tok_1", URI: "/feed", Method: "GET", HTTPStatus: 200, Markers: []string{"ok"},
		Outcome: "success", OutcomeHint: "success", Rule: "http-2xx", LatencyMs: 500, ResponseBytes: 2048, Late: true})
	a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", ReportID: "rep-2", Outcome: "captcha", HTTPStatus: 403})
	a.RecordReport(ReportRecord{ReportID: "invalid"}) // no namespace: not written anywhere

	wcancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("clickhouse writer did not stop")
	}
	require.Zero(t, writer.Dropped())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM report_events").Scan(&n))
	require.Equal(t, uint64(2), n)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM lease_events").Scan(&n))
	require.Equal(t, uint64(3), n)

	var (
		eventTime, receivedAt       time.Time
		outcome, site, group, uri   string
		status                      uint16
		latency                     uint32
		size                        uint64
		late                        uint8
		markers                     []string
		identityType, rule, rid, tk string
	)
	require.NoError(t, conn.QueryRow(ctx, `SELECT event_time, received_at, outcome, site, endpoint_group, uri, http_status,
		latency_ms, response_bytes, late, markers, identity_type, rule, report_id, token_id
		FROM report_events WHERE report_id = 'rep-1'`).Scan(&eventTime, &receivedAt, &outcome, &site, &group, &uri,
		&status, &latency, &size, &late, &markers, &identityType, &rule, &rid, &tk))
	require.True(t, eventTime.Equal(finished), "event_time %s want %s", eventTime, finished)
	require.True(t, receivedAt.Equal(now))
	require.Equal(t, []string{"success", "shop", "feed", "/feed", "cookie", "http-2xx", "rep-1", "tok_1"},
		[]string{outcome, site, group, uri, identityType, rule, rid, tk})
	require.Equal(t, uint16(200), status)
	require.Equal(t, uint32(500), latency)
	require.Equal(t, uint64(2048), size)
	require.Equal(t, uint8(1), late)
	require.Equal(t, []string{"ok"}, markers)

	var (
		event, result string
		durationUs    uint32
		probe, sticky uint8
	)
	require.NoError(t, conn.QueryRow(ctx, `SELECT event, result, duration_us, probe, sticky FROM lease_events
		WHERE event = 'acquired' AND result = 'ok'`).Scan(&event, &result, &durationUs, &probe, &sticky))
	require.Equal(t, uint32(1234), durationUs)
	require.Equal(t, uint8(1), probe)
	require.Equal(t, uint8(1), sticky)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM lease_events WHERE event = 'abandoned'").Scan(&n))
	require.Equal(t, uint64(1), n)
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM lease_events WHERE event = 'acquired' AND result = 'exhausted'").Scan(&n))
	require.Equal(t, uint64(1), n)
}
