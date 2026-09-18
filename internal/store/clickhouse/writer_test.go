package clickhouse

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/stretchr/testify/require"
)

func TestNewWriterDefaults(t *testing.T) {
	w := NewWriter(nil, nil, 0, -1)
	require.Equal(t, DefaultFlushEvery, w.flushEvery)
	require.Equal(t, DefaultMaxBatch, w.maxBatch)
	require.NotNil(t, w.logger)

	w = newWriter(nil, nil, time.Second, 5, 0)
	require.Equal(t, 5, w.maxBatch)
}

func TestNilConnWriterIsNoop(t *testing.T) {
	w := NewWriter(nil, nil, 10*time.Millisecond, 10)
	w.AddReport(ReportEvent{ReportID: "r1"})
	w.AddLease(LeaseEvent{LeaseID: "l1"})
	require.Zero(t, w.Dropped())
	require.Zero(t, w.Written())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-done:
		t.Fatal("Run returned before the context was canceled")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// fakeConn is a non-nil connection used where no server round trip happens.
type fakeConn struct {
	chdriver.Conn
}

func TestWriterDropsWhenBufferFull(t *testing.T) {
	w := newWriter(fakeConn{}, nil, time.Hour, 10, 2)
	for range 5 {
		w.AddReport(ReportEvent{})
		w.AddLease(LeaseEvent{})
	}
	require.Equal(t, int64(6), w.Dropped())
	require.Len(t, w.reports, 2)
	require.Len(t, w.leases, 2)
}

func TestAddReportCopiesMarkers(t *testing.T) {
	w := newWriter(fakeConn{}, nil, time.Hour, 10, 4)
	markers := make([]string, 2, 8)
	markers[0], markers[1] = "captcha_page", "empty_list"
	w.AddReport(ReportEvent{ReportID: "r1", Markers: markers})

	// The caller reuses its buffer after queueing the event.
	markers[0] = "mutated"
	_ = append(markers, "appended")
	w.AddReport(ReportEvent{ReportID: "r2"})
	w.AddReport(ReportEvent{ReportID: "r3", Markers: []string{}})

	first := <-w.reports
	require.Equal(t, []string{"captcha_page", "empty_list"}, first.Markers)
	require.NotSame(t, &markers[0], &first.Markers[0], "the queued event owns its backing array")
	require.Nil(t, (<-w.reports).Markers)
	third := <-w.reports
	require.NotNil(t, third.Markers)
	require.Empty(t, third.Markers)
}

func TestNilWriterIsNoop(t *testing.T) {
	var w *Writer
	require.NotPanics(t, func() {
		w.AddReport(ReportEvent{ReportID: "r1"})
		w.AddLease(LeaseEvent{LeaseID: "l1"})
	})
	require.Zero(t, w.Dropped())
	require.Zero(t, w.Written())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, w.Run(ctx))
}

func TestWriterRejectsConcurrentRun(t *testing.T) {
	w := newWriter(fakeConn{}, nil, time.Hour, 10, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, w.running.Load, 5*time.Second, time.Millisecond)

	require.ErrorIs(t, w.Run(context.Background()), ErrWriterRunning)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	require.False(t, w.running.Load(), "a stopped writer can be run again")
}

func TestTimeAndBoolHelpers(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	require.Equal(t, epoch, utc(time.Time{}))
	require.Equal(t, epoch, utc(time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, maxEventTime, utc(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)), "far future is clamped")
	require.Positive(t, utc(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)).UnixNano(), "clamped time does not wrap")
	require.Equal(t, maxEventTime, utc(maxEventTime))
	shanghai := time.FixedZone("CST", 8*3600)
	in := time.Date(2025, 9, 16, 16, 30, 11, 962_000_000, shanghai)
	require.Equal(t, time.UTC, utc(in).Location())
	require.True(t, in.Equal(utc(in)))
	require.Equal(t, uint8(1), boolUint8(true))
	require.Equal(t, uint8(0), boolUint8(false))
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func runWriter(t *testing.T, w *Writer) (cancel func() error) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(stop)
	return func() error {
		stop()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			t.Fatal("writer did not stop")
			return nil
		}
	}
}

func countRows(t *testing.T, conn chdriver.Conn, table string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+table).Scan(&n))
	return n
}

func TestWriterWritesBatches(t *testing.T) {
	conn := testConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, Migrate(ctx, conn, 90))

	w := NewWriter(conn, testLogger(t), 50*time.Millisecond, 3)
	stop := runWriter(t, w)

	// Rows older than the table TTL are discarded by ClickHouse on insert, so use a recent timestamp.
	eventTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	full := ReportEvent{
		EventTime: eventTime, ReceivedAt: eventTime.Add(5 * time.Millisecond), StartedAt: eventTime.Add(-842 * time.Millisecond),
		TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", Site: "shop", EndpointGroup: "feed", Client: "web",
		IdentityID: "idt_1", IdentityType: "cookie", ProxyID: "pxy_1", LeaseID: "lse_1", ReportID: "rep-full", Node: "crawler-hk-03",
		TokenID: "tok_1", URI: "/a/b", Method: "GET", HTTPStatus: 200, BusinessCode: "0", ErrorKind: "",
		Markers: []string{"empty_list", "slow"}, Outcome: "success", OutcomeHint: "success", Blame: "none", Rule: "ok",
		LatencyMs: 842, ResponseBytes: 48213, Suppressed: true, Late: false, Probe: true,
	}
	w.AddReport(full)
	for i := range 6 {
		w.AddReport(ReportEvent{ReportID: "rep-" + string(rune('a'+i)), Outcome: "captcha"})
	}
	// A client-supplied timestamp beyond the driver's range is clamped instead of wrapping.
	w.AddReport(ReportEvent{ReportID: "rep-future", StartedAt: time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)})
	w.AddLease(LeaseEvent{
		EventTime: eventTime, TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", Site: "shop", EndpointGroup: "feed",
		Client: "web", IdentityID: "idt_1", ProxyID: "pxy_1", LeaseID: "lse_1", Node: "crawler-hk-03", TokenID: "tok_1",
		Event: "acquired", Result: "ok", DurationUs: 1234, Probe: true, Sticky: true,
	})
	w.AddLease(LeaseEvent{LeaseID: "lse_2", Event: "released"})

	require.Eventually(t, func() bool {
		return countRows(t, conn, ReportEventsTable) == 8 && countRows(t, conn, LeaseEventsTable) == 2
	}, 20*time.Second, 50*time.Millisecond)
	require.NoError(t, stop())
	require.Equal(t, int64(10), w.Written())
	require.Zero(t, w.Dropped())

	var (
		gotTime, gotStarted     time.Time
		markers                 []string
		status                  uint16
		latency                 uint32
		bytes                   uint64
		suppressed, late, probe uint8
		site, uri               string
	)
	require.NoError(t, conn.QueryRow(ctx, `SELECT event_time, started_at, markers, http_status, latency_ms, response_bytes,
		suppressed, late, probe, site, uri FROM report_events WHERE report_id = 'rep-full'`).
		Scan(&gotTime, &gotStarted, &markers, &status, &latency, &bytes, &suppressed, &late, &probe, &site, &uri))
	require.True(t, eventTime.Equal(gotTime))
	require.True(t, full.StartedAt.Equal(gotStarted))
	require.Equal(t, []string{"empty_list", "slow"}, markers)
	require.Equal(t, uint16(200), status)
	require.Equal(t, uint32(842), latency)
	require.Equal(t, uint64(48213), bytes)
	require.Equal(t, []uint8{1, 0, 1}, []uint8{suppressed, late, probe})
	require.Equal(t, "shop", site)
	require.Equal(t, "/a/b", uri)

	var (
		defaultTime  time.Time
		emptyMarkers []string
	)
	require.NoError(t, conn.QueryRow(ctx, "SELECT started_at, markers FROM report_events WHERE report_id = 'rep-a'").
		Scan(&defaultTime, &emptyMarkers))
	require.True(t, time.Unix(0, 0).Equal(defaultTime), "zero timestamps are stored as the epoch")
	require.Empty(t, emptyMarkers)

	var future time.Time
	require.NoError(t, conn.QueryRow(ctx, "SELECT started_at FROM report_events WHERE report_id = 'rep-future'").Scan(&future))
	require.True(t, maxEventTime.Equal(future), "got %s", future)

	var (
		duration      uint32
		leaseProbe    uint8
		sticky        uint8
		leaseEvent    string
		leaseDefaults time.Time
	)
	require.NoError(t, conn.QueryRow(ctx, "SELECT duration_us, probe, sticky, event FROM lease_events WHERE lease_id = 'lse_1'").
		Scan(&duration, &leaseProbe, &sticky, &leaseEvent))
	require.Equal(t, uint32(1234), duration)
	require.Equal(t, []uint8{1, 1}, []uint8{leaseProbe, sticky})
	require.Equal(t, "acquired", leaseEvent)
	require.NoError(t, conn.QueryRow(ctx, "SELECT event_time FROM lease_events WHERE lease_id = 'lse_2'").Scan(&leaseDefaults))
	require.WithinDuration(t, time.Now(), leaseDefaults, time.Minute, "zero EventTime defaults to the queue time")
}

func TestWriterFinalFlushOnShutdown(t *testing.T) {
	conn := testConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, Migrate(ctx, conn, 90))

	// Neither the ticker nor the batch size triggers a flush before shutdown.
	w := NewWriter(conn, nil, time.Hour, 2)
	for range 5 {
		w.AddReport(ReportEvent{Outcome: "success"})
	}
	w.AddLease(LeaseEvent{Event: "expired"})

	stop := runWriter(t, w)
	require.NoError(t, stop())
	require.Equal(t, uint64(5), countRows(t, conn, ReportEventsTable))
	require.Equal(t, uint64(1), countRows(t, conn, LeaseEventsTable))
	require.Equal(t, int64(6), w.Written())
	require.Zero(t, w.Dropped())
}

func TestWriterDropsFailedBatches(t *testing.T) {
	// The database exists but the tables do not, so every insert fails.
	conn := testConn(t)

	w := NewWriter(conn, nil, 20*time.Millisecond, 100)
	w.backoff = time.Millisecond
	stop := runWriter(t, w)
	w.AddReport(ReportEvent{ReportID: "r1"})
	w.AddReport(ReportEvent{ReportID: "r2"})
	w.AddLease(LeaseEvent{LeaseID: "l1"})

	require.Eventually(t, func() bool { return w.Dropped() == 3 }, 20*time.Second, 20*time.Millisecond)
	require.NoError(t, stop())
	require.Zero(t, w.Written())
	require.Equal(t, int64(3), w.Dropped())
}

func TestWriterShutdownDropsUnwritableRows(t *testing.T) {
	conn := testConn(t)

	// A long backoff keeps the batch in its retry loop when the context is canceled;
	// the rows are kept for the final flush, which then runs out of time.
	w := NewWriter(conn, nil, 10*time.Millisecond, 1000)
	w.backoff = time.Hour
	w.shutdownTimeout = 300 * time.Millisecond
	stop := runWriter(t, w)
	w.AddReport(ReportEvent{ReportID: "r1"})
	w.AddLease(LeaseEvent{LeaseID: "l1"})

	// Wait until the rows have left the queue and the first attempt failed.
	require.Eventually(t, func() bool { return len(w.reports) == 0 && len(w.leases) == 0 }, 10*time.Second, 5*time.Millisecond)
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	require.NoError(t, stop())
	require.Less(t, time.Since(start), 10*time.Second)
	require.Equal(t, int64(2), w.Dropped())
	require.Zero(t, w.Written())
}

// fakeBatchConn returns scripted batches so insert error paths can be tested without a server.
type fakeBatchConn struct {
	chdriver.Conn
	batch *fakeBatch
}

func (c fakeBatchConn) PrepareBatch(context.Context, string, ...chdriver.PrepareBatchOption) (chdriver.Batch, error) {
	return c.batch, nil
}

type fakeBatch struct {
	chdriver.Batch
	appendErr error
	sendErr   error
	sent      bool
	aborted   bool
}

func (b *fakeBatch) Append(...any) error { return b.appendErr }
func (b *fakeBatch) Send() error         { return b.sendErr }
func (b *fakeBatch) IsSent() bool        { return b.sent }
func (b *fakeBatch) Abort() error {
	b.aborted = true
	return nil
}

func TestWriterInsertErrors(t *testing.T) {
	tests := []struct {
		name        string
		batch       *fakeBatch
		wantErr     string
		wantAborted bool
	}{
		{name: "append fails", batch: &fakeBatch{appendErr: errors.New("bad row")}, wantErr: "append row", wantAborted: true},
		{name: "send fails", batch: &fakeBatch{sendErr: errors.New("network")}, wantErr: "send batch", wantAborted: true},
		{name: "send fails after sent", batch: &fakeBatch{sendErr: errors.New("late"), sent: true}, wantErr: "send batch"},
		{name: "success", batch: &fakeBatch{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWriter(fakeBatchConn{batch: tt.batch}, nil, time.Hour, 10, 10)
			err := w.insert(context.Background(), w.reportInsert, 2, func(b chdriver.Batch, i int) error {
				return appendReport(b, ReportEvent{})
			})
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			require.Equal(t, tt.wantAborted, tt.batch.aborted)
		})
	}
}
