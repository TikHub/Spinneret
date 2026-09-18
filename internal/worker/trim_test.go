package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/signal"
)

// addEntries appends n report events of a lease to a shard stream and returns
// their stream ids.
func (f *fixture) addEntries(t *testing.T, stream, lease string, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for range n {
		data, err := signal.EncodeEvent(f.event(lease))
		require.NoError(t, err)
		id, err := f.rdb.Do(ctx, f.rdb.B().Xadd().Key(stream).Id("*").FieldValue().
			FieldValue(signal.StreamFieldVersion, signal.StreamVersion).
			FieldValue(signal.StreamFieldData, string(data)).Build()).ToString()
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

func (f *fixture) xlen(t *testing.T, stream string) int64 {
	t.Helper()
	n, err := f.rdb.Do(context.Background(), f.rdb.B().Xlen().Key(stream).Build()).AsInt64()
	require.NoError(t, err)
	return n
}

// TestConsumerTrimsProcessedEntries is the regression test for the report
// streams growing without bound: XADD only caps a stream at
// SPINNERET_STREAM_MAXLEN, so before the fix a drained shard kept every
// processed entry (up to the cap, ~7 GiB over 16 shards with the defaults)
// for the life of the deployment.
func TestConsumerTrimsProcessedEntries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// Identity 4 hashes to shard 0, the shard the consumer below owns.
	lease := f.leases(t, 4)[0]
	stream := f.keys.Stream(0)
	// XTRIM MINID "~" only drops whole macro nodes (100 entries by default),
	// so the stream needs more than one node for the trim to be observable.
	const entries = 600
	f.addEntries(t, stream, lease, entries)
	require.EqualValues(t, entries, f.xlen(t, stream))

	w := f.newWorker("inst-trim", Config{BatchSize: 100, Block: 10 * time.Millisecond, TrimInterval: time.Millisecond})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go w.consume(ctx, 0, done)

	require.Eventually(t, func() bool {
		return len(f.rec.countByReport()) == entries && f.xlen(t, stream) < entries/2
	}, 15*time.Second, 20*time.Millisecond, "acknowledged entries must be trimmed from the stream")
	cancel()
	<-done

	// Every entry was processed exactly once, and the backlog of the consumer
	// group is empty: the trim removed acknowledged entries only.
	for id, n := range f.rec.countByReport() {
		require.Equal(t, 1, n, "report %s processed more than once", id)
	}
	n, err := w.pendingCount(context.Background(), 0)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestTrimKeepsPendingEntries checks the trim floor: entries a previous owner
// claimed but never acknowledged must survive, because the next owner claims
// and processes them.
func TestTrimKeepsPendingEntries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lease := f.leases(t, 4)[0]
	stream := f.keys.Stream(0)
	require.NoError(t, f.w.ensureGroup(ctx, stream))

	// 400 entries delivered to a consumer that never acknowledges them, then
	// 400 more that this worker processes and acknowledges.
	pending := f.addEntries(t, stream, lease, 400)
	other := New(Config{InstanceID: "inst-dead", ReportShards: testShards}, f.rdb, f.keys, f.cat,
		f.exec, f.rel, f.brk, f.rec, f.metrics, nil)
	delivered, err := other.readGroup(ctx, stream)
	require.NoError(t, err)
	require.Len(t, delivered, DefaultBatchSize)

	f.addEntries(t, stream, lease, 400)
	entries, err := f.w.readGroup(ctx, stream)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	complete, acked, lastID := f.w.processBatch(ctx, ctx, 0, stream, entries)
	require.True(t, complete)
	require.True(t, acked)
	require.NotEmpty(t, lastID)

	f.w.trimProcessed(ctx, 0, stream, lastID)

	// The floor is the oldest pending entry, so nothing was removed.
	remaining, err := f.rdb.Do(ctx, f.rdb.B().Xrange().Key(stream).Start("-").End("+").Build()).AsXRange()
	require.NoError(t, err)
	require.NotEmpty(t, remaining)
	require.Equal(t, pending[0], remaining[0].ID, "the oldest pending entry must survive the trim")

	// Once the stuck entries have been claimed and acknowledged, the floor
	// moves past them and they are removed.
	claimed, acked, lastID := f.w.claimPending(ctx, ctx, 0, stream, func(error, string) bool { return false })
	require.True(t, claimed)
	require.True(t, acked)
	require.Equal(t, pending[len(delivered)-1], lastID, "claimPending drains the pending entries only")
	f.w.trimProcessed(ctx, 0, stream, lastID)

	remaining, err = f.rdb.Do(ctx, f.rdb.B().Xrange().Key(stream).Start("-").End("+").Build()).AsXRange()
	require.NoError(t, err)
	require.Less(t, f.xlen(t, stream), int64(800))
	require.Greater(t, remaining[0].ID, pending[0], "entries acknowledged by both consumers are removed")
}

// TestTrimAdoptedIdleShard covers a shard adopted after a rebalance that gets
// no new reports: the new owner has acknowledged nothing, so the floor comes
// from the group's last-delivered id and the drained entries still go away.
func TestTrimAdoptedIdleShard(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lease := f.leases(t, 4)[0]
	stream := f.keys.Stream(0)
	require.NoError(t, f.w.ensureGroup(ctx, stream))
	f.addEntries(t, stream, lease, 600)

	// The previous owner read and acknowledged everything.
	for {
		entries, err := f.w.readGroup(ctx, stream)
		require.NoError(t, err)
		if len(entries) == 0 {
			break
		}
		_, acked, _ := f.w.processBatch(ctx, ctx, 0, stream, entries)
		require.True(t, acked)
	}

	// The new owner trims before acknowledging anything of its own.
	adopted := f.newWorker("inst-adopted", Config{})
	adopted.trimProcessed(ctx, 0, stream, "")
	require.Less(t, f.xlen(t, stream), int64(300))
}

func TestTrimmerPacing(t *testing.T) {
	start := time.Now()
	tr := &trimmer{interval: time.Second, last: start}
	require.False(t, tr.due(start.Add(500*time.Millisecond)), "interval not elapsed")
	require.True(t, tr.due(start.Add(2*time.Second)))
	require.False(t, tr.due(start.Add(2*time.Second)), "the interval restarts after a trim")
	require.True(t, tr.due(start.Add(4*time.Second)))
	tr.record("1-1")
	tr.record("")
	require.Equal(t, "1-1", tr.lastAcked, "an empty batch keeps the last acknowledged id")
}
