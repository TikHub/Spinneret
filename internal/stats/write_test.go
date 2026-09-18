package stats

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

var (
	errTransient        = errors.New("connection reset by peer")
	errMissingPartition = &pgconn.PgError{Code: postgres.SQLStateCheckViolation,
		Message: `no partition of relation "node_stats_minutely" found for row`, TableName: "node_stats_minutely"}
	errBadRow = &pgconn.PgError{Code: "22021", Message: "invalid byte sequence for encoding \"UTF8\""}
)

// fakeExec is a scripted statement executor for rowWriter tests.
type fakeExec struct {
	mu      sync.Mutex
	calls   [][]int64
	written []int64
	fail    func(call int, rows []int64) error
}

func (f *fakeExec) exec(_ context.Context, rows []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(rows))
	if f.fail != nil {
		if err := f.fail(len(f.calls), rows); err != nil {
			return err
		}
	}
	f.written = append(f.written, rows...)
	return nil
}

func newTestFlushWriter(ensure func(context.Context) error) (*flushWriter, *observability.Metrics) {
	m := observability.NewMetrics()
	if ensure == nil {
		ensure = func(context.Context) error { return nil }
	}
	return &flushWriter{
		metrics:          m,
		logger:           slog.New(slog.DiscardHandler),
		chunkSize:        MaxRowsPerStatement,
		maxRetries:       3,
		retryBackoff:     time.Millisecond,
		statementTimeout: time.Second,
		ensure:           ensure,
	}, m
}

func newTestRowWriter(fw *flushWriter, f *fakeExec) rowWriter[int64] {
	return newRowWriter(fw, "test_table", postgres.GranularityDay, func(v int64) int64 { return v }, f.exec)
}

func batches(m *observability.Metrics, result string) float64 {
	return counterValue(m, "test_table", result)
}

// counterValue reads spinneret_db_write_batches_total{writer,result}.
func counterValue(m *observability.Metrics, writer, result string) float64 {
	var out dto.Metric
	if err := m.DBWriteBatches.WithLabelValues(writer, result).Write(&out); err != nil {
		return -1
	}
	return out.GetCounter().GetValue()
}

func TestRowWriterSuccess(t *testing.T) {
	t.Parallel()
	fw, m := newTestFlushWriter(nil)
	f := &fakeExec{}
	kept, dropped := newTestRowWriter(fw, f).write(context.Background(), []int64{1, 2, 3})
	require.Empty(t, kept)
	require.Zero(t, dropped)
	require.Equal(t, []int64{1, 2, 3}, f.written)
	require.Equal(t, 1.0, batches(m, batchOK))

	kept, dropped = newTestRowWriter(fw, f).write(context.Background(), nil)
	require.Empty(t, kept)
	require.Zero(t, dropped)
	require.Len(t, f.calls, 1)
}

func TestRowWriterRetriesTransientErrors(t *testing.T) {
	t.Parallel()
	fw, m := newTestFlushWriter(nil)
	f := &fakeExec{fail: func(call int, _ []int64) error {
		if call <= 2 {
			return errTransient
		}
		return nil
	}}
	kept, dropped := newTestRowWriter(fw, f).write(context.Background(), []int64{1})
	require.Empty(t, kept)
	require.Zero(t, dropped)
	require.Len(t, f.calls, 3)
	require.Equal(t, 2.0, batches(m, batchRetry))
	require.Equal(t, 1.0, batches(m, batchOK))
	require.False(t, fw.failed)
}

func TestRowWriterKeepsRowsAfterRetriesExhausted(t *testing.T) {
	t.Parallel()
	fw, m := newTestFlushWriter(nil)
	f := &fakeExec{fail: func(int, []int64) error { return errTransient }}
	w := newTestRowWriter(fw, f)
	rows := []int64{1, 2}
	kept, dropped := w.write(context.Background(), rows)
	require.Equal(t, rows, kept)
	require.Zero(t, dropped)
	require.Len(t, f.calls, 1+fw.maxRetries)
	require.Equal(t, 1.0, batches(m, batchError))
	require.True(t, fw.failed)

	// Once failed, later writes of the same flush are skipped.
	kept, _ = w.write(context.Background(), []int64{3})
	require.Equal(t, []int64{3}, kept)
	require.Len(t, f.calls, 1+fw.maxRetries)
}

func TestRowWriterServerErrorHaltsOnlyTheTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   error
		calls int
	}{
		{name: "retryable deadlock", err: &pgconn.PgError{Code: postgres.SQLStateDeadlockDetected}, calls: 4},
		{name: "permanent undefined table", err: &pgconn.PgError{Code: "42P01"}, calls: 1},
		{name: "retryable statement cancel", err: &pgconn.PgError{Code: sqlStateQueryCanceled}, calls: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fw, m := newTestFlushWriter(nil)
			f := &fakeExec{fail: func(int, []int64) error { return tt.err }}
			w := newTestRowWriter(fw, f)
			kept, dropped := w.write(context.Background(), []int64{1, 2})
			require.Equal(t, []int64{1, 2}, kept)
			require.Zero(t, dropped)
			require.Len(t, f.calls, tt.calls)
			require.Equal(t, 1.0, batches(m, batchError))
			require.False(t, fw.failed, "a server error must not stop other tables")
			require.True(t, w.stopped(context.Background()))

			// The table's remaining writes are skipped...
			kept, _ = w.write(context.Background(), []int64{3})
			require.Equal(t, []int64{3}, kept)
			require.Len(t, f.calls, tt.calls)

			// ...while another table of the same flush is still written.
			other := &fakeExec{}
			kept, _ = newTestRowWriter(fw, other).write(context.Background(), []int64{4})
			require.Empty(t, kept)
			require.Equal(t, []int64{4}, other.written)
		})
	}
}

func TestRowWriterUnavailableServerStopsFlush(t *testing.T) {
	t.Parallel()
	fw, _ := newTestFlushWriter(nil)
	f := &fakeExec{fail: func(int, []int64) error {
		return &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}
	}}
	kept, _ := newTestRowWriter(fw, f).write(context.Background(), []int64{1})
	require.Equal(t, []int64{1}, kept)
	require.Len(t, f.calls, 1+fw.maxRetries)
	require.True(t, fw.failed, "a server-wide error stops the whole flush")

	other := &fakeExec{}
	kept, _ = newTestRowWriter(fw, other).write(context.Background(), []int64{2})
	require.Equal(t, []int64{2}, kept)
	require.Empty(t, other.calls)
}

func TestRowWriterContextCanceled(t *testing.T) {
	t.Parallel()
	fw, _ := newTestFlushWriter(nil)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeExec{fail: func(int, []int64) error {
		cancel()
		return context.Canceled
	}}
	kept, dropped := newTestRowWriter(fw, f).write(ctx, []int64{1})
	require.Equal(t, []int64{1}, kept)
	require.Zero(t, dropped)
	require.Len(t, f.calls, 1)

	// A canceled context during backoff stops retrying.
	fw2, _ := newTestFlushWriter(nil)
	fw2.retryBackoff = time.Hour
	ctx2, cancel2 := context.WithCancel(context.Background())
	f2 := &fakeExec{fail: func(int, []int64) error {
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel2()
		}()
		return errTransient
	}}
	kept, _ = newTestRowWriter(fw2, f2).write(ctx2, []int64{1})
	require.Equal(t, []int64{1}, kept)
	require.Len(t, f2.calls, 1)
}

func TestRowWriterRepairsMissingPartition(t *testing.T) {
	t.Parallel()
	ensured := 0
	fw, _ := newTestFlushWriter(func(context.Context) error {
		ensured++
		return nil
	})
	f := &fakeExec{fail: func(call int, _ []int64) error {
		if call == 1 {
			return errMissingPartition
		}
		return nil
	}}
	kept, dropped := newTestRowWriter(fw, f).write(context.Background(), []int64{10, 20})
	require.Empty(t, kept)
	require.Zero(t, dropped)
	require.Equal(t, 1, ensured)
	require.Equal(t, []int64{10, 20}, f.written)
	require.Equal(t, ensureOK, fw.ensureState)
}

func TestRowWriterDropsRowsWithoutPartition(t *testing.T) {
	t.Parallel()
	ensured := 0
	fw, m := newTestFlushWriter(func(context.Context) error {
		ensured++
		return nil
	})
	day := int64(secondsPerDay)
	farFuture := 1000 * day
	// Rows of the far-future partition always fail; every statement containing
	// one fails as a whole.
	f := &fakeExec{fail: func(_ int, rows []int64) error {
		for _, r := range rows {
			if r >= farFuture {
				return errMissingPartition
			}
		}
		return nil
	}}
	w := newTestRowWriter(fw, f)
	rows := []int64{day + 1, day + 2, 2*day + 5, farFuture + 1, farFuture + 60}
	kept, dropped := w.write(context.Background(), rows)
	require.Empty(t, kept)
	require.Equal(t, 2, dropped)
	require.Equal(t, 1, ensured, "partitions are repaired once per flush")
	require.ElementsMatch(t, []int64{day + 1, day + 2, 2*day + 5}, f.written)
	require.Equal(t, 1.0, batches(m, batchDropped))

	// A later statement of the same flush with a missing partition goes
	// straight to per-partition isolation.
	kept, dropped = w.write(context.Background(), []int64{farFuture + 5})
	require.Empty(t, kept)
	require.Equal(t, 1, dropped)
	require.Equal(t, 1, ensured)
}

func TestRowWriterEnsureFailureKeepsRows(t *testing.T) {
	t.Parallel()
	fw, m := newTestFlushWriter(func(context.Context) error { return errors.New("lock timeout") })
	f := &fakeExec{fail: func(int, []int64) error { return errMissingPartition }}
	w := newTestRowWriter(fw, f)
	kept, dropped := w.write(context.Background(), []int64{1, 2})
	require.Equal(t, []int64{1, 2}, kept)
	require.Zero(t, dropped)
	require.Equal(t, ensureFailed, fw.ensureState)
	require.True(t, fw.failed)
	require.Equal(t, 1.0, batches(m, batchError))

	// Without the failed flag (e.g. a different flush writer state), a missing
	// partition after a failed repair keeps the rows as well.
	fw.failed = false
	kept, dropped = w.write(context.Background(), []int64{3})
	require.Equal(t, []int64{3}, kept)
	require.Zero(t, dropped)
}

func TestRowWriterBisectsDataErrors(t *testing.T) {
	t.Parallel()
	fw, m := newTestFlushWriter(nil)
	bad := map[int64]bool{3: true, 6: true}
	f := &fakeExec{fail: func(_ int, rows []int64) error {
		for _, r := range rows {
			if bad[r] {
				return errBadRow
			}
		}
		return nil
	}}
	rows := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	original := slices.Clone(rows)
	kept, dropped := newTestRowWriter(fw, f).write(context.Background(), rows)
	require.Empty(t, kept)
	require.Equal(t, 2, dropped)
	require.ElementsMatch(t, []int64{1, 2, 4, 5, 7, 8}, f.written)
	require.Equal(t, original, rows, "the caller's slice must not be modified")
	require.Equal(t, 2.0, batches(m, batchDropped))
}

func TestRowWriterBisectKeepsTransientHalves(t *testing.T) {
	t.Parallel()
	fw, _ := newTestFlushWriter(nil)
	fw.maxRetries = 0
	f := &fakeExec{fail: func(call int, rows []int64) error {
		switch {
		case call == 1:
			return errBadRow
		case slices.Contains(rows, 1):
			return nil
		default:
			return errTransient
		}
	}}
	rows := []int64{1, 2, 3, 4}
	original := slices.Clone(rows)
	kept, dropped := newTestRowWriter(fw, f).write(context.Background(), rows)
	require.Equal(t, []int64{3, 4}, kept)
	require.Zero(t, dropped)
	require.Equal(t, original, rows)
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()
	checkWithConstraint := &pgconn.PgError{Code: postgres.SQLStateCheckViolation, ConstraintName: "acquire_stats_minutely_result_check",
		Message: "new row violates check constraint"}
	localizedPartition := &pgconn.PgError{Code: postgres.SQLStateCheckViolation, Message: "keine Partition gefunden"}
	unique := &pgconn.PgError{Code: postgres.SQLStateUniqueViolation}
	limit := &pgconn.PgError{Code: sqlStateProgramLimitExceeded}
	deadlock := &pgconn.PgError{Code: postgres.SQLStateDeadlockDetected}
	server := func(code string) error { return &pgconn.PgError{Code: code} }

	tests := []struct {
		name      string
		err       error
		partition bool
		data      bool
		retryable bool
		unique    bool
		down      bool
	}{
		{name: "missing partition", err: errMissingPartition, partition: true},
		{name: "wrapped missing partition", err: fmt.Errorf("upsert: %w", errMissingPartition), partition: true},
		{name: "localized missing partition", err: localizedPartition, partition: true},
		{name: "check constraint", err: checkWithConstraint, data: true},
		{name: "invalid encoding", err: errBadRow, data: true},
		{name: "unique violation", err: unique, data: true, unique: true},
		{name: "program limit", err: limit, data: true},
		{name: "deadlock", err: deadlock, retryable: true},
		{name: "serialization failure", err: server(postgres.SQLStateSerializationFailure), retryable: true},
		{name: "too many connections", err: server("53300"), retryable: true, down: true},
		{name: "lock not available", err: server("55P03"), retryable: true},
		{name: "statement canceled", err: server(sqlStateQueryCanceled), retryable: true},
		{name: "admin shutdown", err: server("57P01"), retryable: true, down: true},
		{name: "connection failure", err: server("08006"), retryable: true, down: true},
		{name: "io error", err: server("58030"), retryable: true, down: true},
		{name: "undefined table", err: server("42P01")},
		{name: "insufficient privilege", err: server("42501")},
		{name: "empty code", err: server("")},
		{name: "network", err: errTransient, retryable: true, down: true},
		{name: "deadline", err: context.DeadlineExceeded, retryable: true, down: true},
		{name: "canceled", err: context.Canceled, down: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.partition, isMissingPartition(tt.err))
			require.Equal(t, tt.data, isDataError(tt.err))
			require.Equal(t, tt.retryable, isRetryable(tt.err))
			require.Equal(t, tt.unique, isUniqueViolation(tt.err))
			require.Equal(t, tt.down, isDatabaseUnavailable(tt.err))
		})
	}
}

func TestGroupByPartition(t *testing.T) {
	t.Parallel()
	fw, _ := newTestFlushWriter(nil)
	w := newTestRowWriter(fw, &fakeExec{})
	day := int64(secondsPerDay)
	groups := w.groupByPartition([]int64{2*day + 1, day, 2 * day, day + 5})
	require.Equal(t, [][]int64{{2*day + 1, 2 * day}, {day, day + 5}}, groups)

	w.granularity = postgres.GranularityMonth
	sep1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	sep30 := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC).Unix()
	oct1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	groups = w.groupByPartition([]int64{sep1, sep30, oct1})
	require.Equal(t, [][]int64{{sep1, sep30}, {oct1}}, groups)
}
