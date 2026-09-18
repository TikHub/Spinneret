package stats

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// captured collects the rows written through injected table writers.
type captured struct {
	mu       sync.Mutex
	acquire  map[acquireKey]acquireValue
	payload  map[payloadKey]countValue
	node     map[nodeKey]nodeValue
	outcome  map[outcomeKey]outcomeValue
	identity map[identityKey]identityValue
	risk     []riskRow
}

func captureInto[K comparable, V any](mu *sync.Mutex, dst map[K]V) func(context.Context, []entry[K, V]) error {
	return func(_ context.Context, rows []entry[K, V]) error {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range rows {
			if _, dup := dst[r.key]; dup {
				return errors.New("duplicate key written")
			}
			dst[r.key] = r.val
		}
		return nil
	}
}

// newUnitAggregator returns an aggregator whose writers capture rows in memory.
func newUnitAggregator(t *testing.T, now time.Time) (*Aggregator, *captured) {
	t.Helper()
	a := NewAggregator(nil, nil, observability.NewMetrics(), nil)
	a.now = func() time.Time { return now }
	a.retryBackoff = time.Millisecond
	c := &captured{
		acquire:  map[acquireKey]acquireValue{},
		payload:  map[payloadKey]countValue{},
		node:     map[nodeKey]nodeValue{},
		outcome:  map[outcomeKey]outcomeValue{},
		identity: map[identityKey]identityValue{},
	}
	a.acquire.exec = captureInto(&c.mu, c.acquire)
	a.payload.exec = captureInto(&c.mu, c.payload)
	a.node.exec = captureInto(&c.mu, c.node)
	a.outcome.exec = captureInto(&c.mu, c.outcome)
	a.identity.exec = captureInto(&c.mu, c.identity)
	a.risk.exec = func(_ context.Context, rows []riskRow) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.risk = append(c.risk, rows...)
		return nil
	}
	return a, c
}

func TestRecordAcquire(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	at := time.Date(2026, 9, 17, 10, 29, 5, 0, time.UTC)

	base := AcquireRecord{At: at, NamespaceID: "ns_1", SiteID: "sit_1", EndpointGroupID: "eg_1",
		IdentityTypeID: "ity_1", TokenID: "tok_1", Node: "node-a", Result: ResultOK, Duration: 1500 * time.Microsecond}
	a.RecordAcquire(base)
	a.RecordAcquire(base)
	noType := base
	noType.IdentityTypeID = ""
	a.RecordAcquire(noType)
	exhausted := base
	exhausted.Result = ResultExhausted
	exhausted.At = time.Time{} // defaults to now
	exhausted.Node = ""
	a.RecordAcquire(exhausted)
	negative := base
	negative.Result = ResultError
	negative.Duration = -time.Second
	a.RecordAcquire(negative)

	bad := base
	bad.Result = "bogus"
	a.RecordAcquire(bad)
	noNS := base
	noNS.NamespaceID = ""
	a.RecordAcquire(noNS)

	require.NoError(t, a.Flush(context.Background()))

	atBucket := minuteBucket(at)
	nowBucket := minuteBucket(now)
	require.Equal(t, map[acquireKey]acquireValue{
		{bucket: atBucket, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", result: ResultOK}:         {count: 3, durationUs: 4500},
		{bucket: atBucket, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", result: ResultError}:      {count: 1, durationUs: 0},
		{bucket: nowBucket, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", result: ResultExhausted}: {count: 1, durationUs: 1500},
	}, c.acquire)
	require.Equal(t, map[payloadKey]countValue{
		{bucket: atBucket, namespaceID: "ns_1", tokenID: "tok_1", identityTypeID: "ity_1"}: {count: 2},
	}, c.payload)
	require.Equal(t, map[nodeKey]nodeValue{
		{bucket: atBucket, namespaceID: "ns_1", node: "node-a"}: {acquires: 3},
	}, c.node)
	require.Equal(t, int64(2), a.Counters().Invalid)
}

func TestRecordReport(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	received := time.Date(2026, 9, 17, 11, 0, 2, 0, time.UTC)
	finished := time.Date(2026, 9, 17, 10, 59, 58, 0, time.UTC)

	success := ReportRecord{ReceivedAt: received, StartedAt: finished.Add(-time.Second), FinishedAt: finished,
		TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", EndpointGroupID: "eg_1", IdentityID: "idt_1",
		ProxyID: "pxy_1", LeaseID: "lse_1", ReportID: "r1", Node: "node-a", TokenID: "tok_1", URI: "/a", Method: "GET",
		HTTPStatus: 200, Outcome: "success", LatencyMs: 100, ResponseBytes: 1000}
	a.RecordReport(success)
	a.RecordReport(success)

	captcha := success
	captcha.ReportID = "r2"
	captcha.Outcome = "captcha"
	captcha.HTTPStatus = 403
	captcha.Markers = []string{"captcha_page"}
	captcha.BusinessCode = "10001"
	captcha.Rule = "captcha-marker"
	captcha.Blame = "identity"
	captcha.LatencyMs = -5
	captcha.ResponseBytes = -1
	a.RecordReport(captcha)

	// Unknown identity, no finish time, empty outcome and node.
	late := ReportRecord{ReceivedAt: received, NamespaceID: "ns_1", SiteID: "sit_1", LeaseID: "lse_2", ReportID: "r3"}
	a.RecordReport(late)

	// Skewed node clock: the receive time is used.
	skewed := success
	skewed.ReportID = "r4"
	skewed.Outcome = "rate_limited"
	skewed.FinishedAt = received.Add(time.Hour)
	skewed.ReceivedAt = time.Time{} // defaults to now
	a.RecordReport(skewed)

	a.RecordReport(ReportRecord{Outcome: "success"}) // no namespace

	require.NoError(t, a.Flush(context.Background()))

	fb := minuteBucket(finished)
	rb := minuteBucket(received)
	nb := minuteBucket(now)
	require.Equal(t, map[outcomeKey]outcomeValue{
		{bucket: fb, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", proxyID: "pxy_1", outcome: "success"}:      {count: 2, latencyMs: 200, responseBytes: 2000},
		{bucket: fb, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", proxyID: "pxy_1", outcome: "captcha"}:      {count: 1},
		{bucket: rb, namespaceID: "ns_1", siteID: "sit_1", outcome: "unknown"}:                                                 {count: 1},
		{bucket: nb, namespaceID: "ns_1", siteID: "sit_1", endpointGroupID: "eg_1", proxyID: "pxy_1", outcome: "rate_limited"}: {count: 1, latencyMs: 100, responseBytes: 1000},
	}, c.outcome)
	require.Equal(t, map[identityKey]identityValue{
		{bucket: hourBucket(finished), identityID: "idt_1", endpointGroupID: "eg_1", outcome: "success"}: {siteID: "sit_1", count: 2},
		{bucket: hourBucket(finished), identityID: "idt_1", endpointGroupID: "eg_1", outcome: "captcha"}: {siteID: "sit_1", count: 1},
		{bucket: hourBucket(now), identityID: "idt_1", endpointGroupID: "eg_1", outcome: "rate_limited"}: {siteID: "sit_1", count: 1},
	}, c.identity)
	require.Equal(t, map[nodeKey]nodeValue{
		{bucket: rb, namespaceID: "ns_1", node: "node-a"}:  {reports: 3},
		{bucket: rb, namespaceID: "ns_1", node: EmptyNode}: {reports: 1},
		{bucket: nb, namespaceID: "ns_1", node: "node-a"}:  {reports: 1},
	}, c.node)
	require.Equal(t, int64(1), a.Counters().Invalid)

	require.Len(t, c.risk, 3)
	byReport := map[string]riskRow{}
	for _, r := range c.risk {
		require.True(t, len(r.id) > 4 && r.id[:4] == "rsk_", r.id)
		byReport[r.reportID] = r
	}
	cr := byReport["r2"]
	require.Equal(t, finished, cr.createdAt)
	require.Equal(t, "captcha", cr.outcome)
	require.Equal(t, []string{"captcha_page"}, cr.markers)
	require.Equal(t, int32(403), cr.httpStatus)
	require.Equal(t, "10001", cr.businessCode)
	require.Equal(t, "captcha-marker", cr.rule)
	require.Equal(t, "identity", cr.blame)
	require.Equal(t, "ten_1", cr.tenantID)
	require.Equal(t, "tok_1", cr.tokenID)
	require.Equal(t, int32(0), cr.latencyMs)
	require.Equal(t, int64(0), cr.responseBytes)
	require.NotNil(t, cr.finishedAt)
	require.True(t, cr.finishedAt.Equal(finished))

	lr := byReport["r3"]
	require.Equal(t, received, lr.createdAt)
	require.Equal(t, "unknown", lr.outcome)
	require.Nil(t, lr.finishedAt)
	require.Nil(t, lr.startedAt)
	require.NotNil(t, lr.markers)

	require.Equal(t, now, byReport["r4"].createdAt)
	require.Len(t, byReport["r4"].values(), len(riskColumns))
}

func TestRecordLeaseEndAndRejected(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	at := now.Add(-2 * time.Minute)

	a.RecordLeaseEnd(LeaseEndRecord{At: at, NamespaceID: "ns_1", Node: "n1", Kind: LeaseEndAbandoned})
	a.RecordLeaseEnd(LeaseEndRecord{NamespaceID: "ns_1", Node: "n1", Kind: LeaseEndAbandoned})
	a.RecordLeaseEnd(LeaseEndRecord{At: at, NamespaceID: "ns_1", Node: "n1", Kind: LeaseEndReleased})
	a.RecordLeaseEnd(LeaseEndRecord{At: at, NamespaceID: "ns_1", Node: "n1", Kind: "bogus"})
	a.RecordLeaseEnd(LeaseEndRecord{At: at, Node: "n1", Kind: LeaseEndExpired})

	a.RecordRejectedReports("ns_1", "n1", 3)
	a.RecordRejectedReports("ns_1", "", 2)
	a.RecordRejectedReports("ns_1", "n1", 0)
	a.RecordRejectedReports("ns_1", "n1", -1)
	a.RecordRejectedReports("", "n1", 4)

	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, map[nodeKey]nodeValue{
		{bucket: minuteBucket(at), namespaceID: "ns_1", node: "n1"}:  {abandoned: 1},
		{bucket: minuteBucket(now), namespaceID: "ns_1", node: "n1"}: {abandoned: 1, rejected: 3},
		{bucket: minuteBucket(now), namespaceID: "ns_1", node: "_"}:  {rejected: 2},
	}, c.node)
	require.Equal(t, int64(3), a.Counters().Invalid)
}

func TestRecordWithNoopClickHouseWriter(t *testing.T) {
	t.Parallel()
	a, c := newUnitAggregator(t, time.Now())
	a.ch = clickhouse.NewWriter(nil, nil, time.Second, 10)
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", Result: ResultOK, Duration: time.Millisecond})
	a.RecordReport(ReportRecord{NamespaceID: "ns", Outcome: "banned", HTTPStatus: 99999, LatencyMs: 5})
	a.RecordLeaseEnd(LeaseEndRecord{NamespaceID: "ns", Kind: LeaseEndRenewed})
	require.NoError(t, a.Flush(context.Background()))
	require.Len(t, c.risk, 1)
	require.Len(t, c.acquire, 1)
}

func TestRiskBufferCaps(t *testing.T) {
	t.Parallel()
	b := newRiskBuffer(3, DefaultMaxRiskBytes, func(context.Context, []riskRow) error { return nil })
	for i := range 5 {
		b.add(riskRow{reportID: string(rune('a' + i))})
	}
	require.Equal(t, int64(2), b.droppedFull.Load())
	b.drain()
	require.Len(t, b.pending, 3)
	for _, r := range b.pending {
		require.NotEmpty(t, r.id)
	}
	b.add(riskRow{reportID: "x"})
	b.add(riskRow{reportID: "y"})
	b.drain()
	// Pending is capped at 3: the two oldest rows are dropped.
	require.Len(t, b.pending, 3)
	require.Equal(t, int64(2), b.droppedRows.Load())
	require.Equal(t, []string{"c", "x", "y"}, []string{b.pending[0].reportID, b.pending[1].reportID, b.pending[2].reportID})
}

func TestRiskBufferByteBudget(t *testing.T) {
	t.Parallel()
	row := func(id string) riskRow {
		return riskRow{reportID: id, uri: strings.Repeat("u", 1000), markers: []string{"m"}}
	}
	sample := row("a")
	size := sample.size()
	require.Greater(t, size, 1000+riskRowOverhead)
	// Room for exactly three rows by size, ten by count.
	b := newRiskBuffer(10, 3*size+size/2, func(context.Context, []riskRow) error { return nil })
	for _, id := range []string{"a", "b", "c", "d"} {
		b.add(row(id))
	}
	require.Equal(t, int64(1), b.droppedFull.Load())
	b.drain()
	require.Len(t, b.pending, 3)
	// The buffer accepts rows again after draining; pending is trimmed by
	// size, oldest first (ids add bytes, so only three rows fit).
	b.add(row("x"))
	b.add(row("y"))
	b.drain()
	require.Len(t, b.pending, 3)
	require.Equal(t, int64(2), b.droppedRows.Load())
	require.Equal(t, []string{"c", "x", "y"}, []string{b.pending[0].reportID, b.pending[1].reportID, b.pending[2].reportID})
}

func TestRecordReportSharesSanitizedMarkers(t *testing.T) {
	t.Parallel()
	a, c := newUnitAggregator(t, time.Now())
	markers := []string{"captcha_page", "x\x00"}
	a.RecordReport(ReportRecord{NamespaceID: "ns", Outcome: "captcha", Markers: markers})
	markers[0] = "mutated after the call"
	require.NoError(t, a.Flush(context.Background()))
	require.Len(t, c.risk, 1)
	require.Equal(t, []string{"captcha_page", "x"}, c.risk[0].markers)
}

func TestFlushKeepsPendingOnTransientFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	a.maxRetries = 1
	capture := a.node.exec
	failures := 0
	a.node.exec = func(context.Context, []entry[nodeKey, nodeValue]) error {
		failures++
		return errTransient
	}

	a.RecordRejectedReports("ns_1", "n1", 2)
	a.RecordAcquire(AcquireRecord{At: now, NamespaceID: "ns_1", Result: ResultExhausted})
	a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", Node: "n1", Outcome: "captcha"})
	err := a.Flush(context.Background())
	require.Error(t, err)
	require.Equal(t, 2, failures)
	// The first failure stops the flush: later tables keep their rows too.
	require.Empty(t, c.acquire)
	require.Empty(t, c.risk)
	require.Equal(t, 1, a.node.pendingLen())
	require.Equal(t, 1, a.acquire.pendingLen())
	require.Len(t, a.risk.pending, 1)
	require.Equal(t, 1.0, counterValue(a.metrics, postgres.TableNodeStatsMinutely, batchError))

	a.node.exec = capture
	a.RecordRejectedReports("ns_1", "n1", 5)
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, map[nodeKey]nodeValue{
		{bucket: minuteBucket(now), namespaceID: "ns_1", node: "n1"}: {rejected: 7, reports: 1},
	}, c.node)
	require.Len(t, c.acquire, 1)
	require.Len(t, c.risk, 1)
	require.Zero(t, a.node.pendingLen())
	require.Empty(t, a.risk.pending)
}

func TestFlushServerErrorDoesNotStarveOtherTables(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	a.chunkSize = 2
	capture := a.node.exec
	calls := 0
	a.node.exec = func(context.Context, []entry[nodeKey, nodeValue]) error {
		calls++
		return &pgconn.PgError{Code: "42501", Message: "permission denied for table node_stats_minutely"}
	}
	for _, node := range []string{"n1", "n2", "n3", "n4", "n5"} {
		a.RecordRejectedReports("ns_1", node, 1)
	}
	a.RecordAcquire(AcquireRecord{At: now, NamespaceID: "ns_1", Result: ResultExhausted})
	a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", Outcome: "captcha"})

	err := a.Flush(context.Background())
	require.ErrorContains(t, err, postgres.TableNodeStatsMinutely)
	require.Equal(t, 1, calls, "a permanent error is not retried and halts the table's remaining chunks")
	// Five rejected rows plus the report's row of node "_".
	require.Equal(t, 6, a.node.pendingLen())
	require.Len(t, c.acquire, 1, "other tables are still written")
	require.Len(t, c.outcome, 1)
	require.Len(t, c.risk, 1)

	a.node.exec = capture
	require.NoError(t, a.Flush(context.Background()))
	require.Len(t, c.node, 6)
	require.Zero(t, a.node.pendingLen())
}

func TestFlushChunksStatements(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 30, 40, 0, time.UTC)
	a, c := newUnitAggregator(t, now)
	a.chunkSize = 7
	statements := 0
	capture := a.node.exec
	a.node.exec = func(ctx context.Context, rows []entry[nodeKey, nodeValue]) error {
		statements++
		require.LessOrEqual(t, len(rows), 7)
		return capture(ctx, rows)
	}
	riskStatements := 0
	riskCapture := a.risk.exec
	a.risk.exec = func(ctx context.Context, rows []riskRow) error {
		riskStatements++
		require.LessOrEqual(t, len(rows), 7)
		return riskCapture(ctx, rows)
	}
	for i := range 50 {
		node := string(rune('A' + i))
		a.RecordRejectedReports("ns_1", node, 1)
		a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", Node: node, Outcome: "empty"})
	}
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, 8, statements)
	require.Equal(t, 8, riskStatements)
	require.Len(t, c.node, 50)
	require.Len(t, c.risk, 50)
}

func TestNilPoolKeepsRows(t *testing.T) {
	t.Parallel()
	a := NewAggregator(nil, nil, nil, nil)
	a.maxRetries = 0
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", Result: ResultOK, IdentityTypeID: "ity"})
	a.RecordReport(ReportRecord{NamespaceID: "ns", IdentityID: "idt", Outcome: "banned"})
	err := a.Flush(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, a.node.pendingLen())
	require.ErrorIs(t, a.ensurePartitions(context.Background()), errNoPool)
	for _, exec := range []func() error{
		func() error { return a.writeAcquire(context.Background(), nil) },
		func() error { return a.writePayload(context.Background(), nil) },
		func() error { return a.writeNode(context.Background(), nil) },
		func() error { return a.writeOutcome(context.Background(), nil) },
		func() error { return a.writeIdentity(context.Background(), nil) },
		func() error { return copyRiskEvents(context.Background(), nil, nil) },
	} {
		require.ErrorIs(t, exec(), errNoPool)
	}
}

func TestCountersAndDropLogging(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var bufMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil))
	a := NewAggregator(nil, nil, nil, logger)
	a.acquire.maxKeys = 1
	a.acquire.exec = func(context.Context, []entry[acquireKey, acquireValue]) error { return nil }
	a.node.exec = func(context.Context, []entry[nodeKey, nodeValue]) error { return nil }
	a.payload.exec = func(context.Context, []entry[payloadKey, countValue]) error { return nil }

	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", SiteID: "s1", Result: ResultOK})
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", SiteID: "s2", Result: ResultOK})
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", SiteID: "s3", Result: ResultOK})
	a.RecordAcquire(AcquireRecord{Result: ResultOK})
	require.NoError(t, a.Flush(context.Background()))

	c := a.Counters()
	require.Equal(t, int64(2), c.DroppedRecords[postgres.TableAcquireStatsMinutely])
	require.Equal(t, int64(1), c.Invalid)
	require.Len(t, c.DroppedRecords, 6)
	require.Len(t, c.DroppedRows, 6)

	bufMu.Lock()
	logged := buf.String()
	buf.Reset()
	bufMu.Unlock()
	require.Contains(t, logged, "records dropped")
	require.Contains(t, logged, "invalid records ignored")

	// Unchanged counters are not logged again.
	require.NoError(t, a.Flush(context.Background()))
	bufMu.Lock()
	defer bufMu.Unlock()
	require.NotContains(t, buf.String(), "records dropped")
}

func TestFlushLogsDroppedRows(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var bufMu sync.Mutex
	a := NewAggregator(nil, nil, nil, slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil)))
	a.node.exec = func(context.Context, []entry[nodeKey, nodeValue]) error { return errBadRow }
	a.RecordRejectedReports("ns", "n", 1)
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, int64(1), a.Counters().DroppedRows[postgres.TableNodeStatsMinutely])
	bufMu.Lock()
	defer bufMu.Unlock()
	require.Contains(t, buf.String(), "aggregated rows dropped while flushing")
}

func TestRunFlushesPeriodicallyAndOnShutdown(t *testing.T) {
	t.Parallel()
	a, c := newUnitAggregator(t, time.Now())
	a.flushInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	require.Eventually(t, func() bool { return a.running.Load() }, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, a.Run(ctx), ErrRunning)

	a.RecordRejectedReports("ns", "periodic", 1)
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.node) == 1
	}, 5*time.Second, 5*time.Millisecond)

	// A record made right before cancellation is written by the final flush.
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns", Result: ResultNoProxy})
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.acquire, 1)
	require.False(t, a.running.Load())
}

func TestRunLogsFlushFailures(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var bufMu sync.Mutex
	a := NewAggregator(nil, nil, nil, slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, nil)))
	a.flushInterval = 5 * time.Millisecond
	a.maxRetries = 0
	a.shutdownTimeout = time.Second
	a.RecordRejectedReports("ns", "n", 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	require.Eventually(t, func() bool {
		bufMu.Lock()
		defer bufMu.Unlock()
		return bytes.Contains(buf.Bytes(), []byte("flush incomplete"))
	}, 5*time.Second, 5*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	bufMu.Lock()
	defer bufMu.Unlock()
	require.Contains(t, buf.String(), "final flush incomplete")
}

// lockedWriter serializes writes to a shared buffer.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
