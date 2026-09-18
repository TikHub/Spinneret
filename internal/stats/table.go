package stats

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/TikHub/Spinneret/internal/store/postgres"
)

// shardCount is the number of mutex-protected maps per aggregate table (a
// power of two).
const shardCount = 32

// entry is one aggregate row: primary key plus counters.
type entry[K any, V any] struct {
	key K
	val V
}

// tableShard is one mutex-protected map of an aggregate table.
type tableShard[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]V
	// Padding keeps neighboring shard locks off the same cache line.
	_ [48]byte
}

// aggTable accumulates the rows of one aggregate table. add is safe for
// concurrent use; drain, trim and flush are called only by the flusher (under
// Aggregator.flushMu).
type aggTable[K tableKey[K], V tableValue[V]] struct {
	name        string
	granularity postgres.Granularity
	maxKeys     int64
	maxPending  int

	// size is the number of distinct keys currently held by the shards.
	size atomic.Int64
	// droppedKeys counts records rejected because maxKeys was reached.
	droppedKeys atomic.Int64
	// droppedRows counts aggregate rows discarded while flushing.
	droppedRows atomic.Int64

	shards [shardCount]tableShard[K, V]

	// pending holds rows not yet written (flusher only).
	pending map[K]V
	// exec writes one chunk of rows (one statement).
	exec func(ctx context.Context, rows []entry[K, V]) error
}

func newAggTable[K tableKey[K], V tableValue[V]](name string, g postgres.Granularity, maxKeys int,
	exec func(context.Context, []entry[K, V]) error) *aggTable[K, V] {
	t := &aggTable[K, V]{
		name:        name,
		granularity: g,
		maxKeys:     int64(maxKeys),
		maxPending:  maxKeys,
		pending:     make(map[K]V),
		exec:        exec,
	}
	for i := range t.shards {
		t.shards[i].m = make(map[K]V)
	}
	return t
}

// add merges v into the row with key k. A new key is rejected (and counted)
// when the table already holds maxKeys distinct keys.
func (t *aggTable[K, V]) add(k K, v V) {
	s := &t.shards[k.shardHash()&(shardCount-1)]
	s.mu.Lock()
	if cur, ok := s.m[k]; ok {
		s.m[k] = cur.merge(v)
		s.mu.Unlock()
		return
	}
	if t.size.Add(1) > t.maxKeys {
		t.size.Add(-1)
		s.mu.Unlock()
		t.droppedKeys.Add(1)
		return
	}
	s.m[k] = v
	s.mu.Unlock()
}

// drain moves every shard's rows into pending and enforces the pending cap.
func (t *aggTable[K, V]) drain() {
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		m := s.m
		if len(m) == 0 {
			s.mu.Unlock()
			continue
		}
		s.m = make(map[K]V, len(m))
		t.size.Add(-int64(len(m)))
		s.mu.Unlock()

		if len(t.pending) == 0 {
			t.pending = m
			continue
		}
		for k, v := range m {
			if cur, ok := t.pending[k]; ok {
				t.pending[k] = cur.merge(v)
			} else {
				t.pending[k] = v
			}
		}
	}
	t.trim()
}

// trim drops the oldest buckets until pending fits maxPending. The newest
// bucket touched is only thinned to the number of excess rows, so a single
// bucket larger than the cap never empties pending.
func (t *aggTable[K, V]) trim() {
	excess := len(t.pending) - t.maxPending
	if excess <= 0 {
		return
	}
	perBucket := make(map[int64]int)
	for k := range t.pending {
		perBucket[k.bucketUnix()]++
	}
	buckets := make([]int64, 0, len(perBucket))
	for b := range perBucket {
		buckets = append(buckets, b)
	}
	slices.Sort(buckets)
	// quota maps each affected bucket to the number of its rows to drop.
	quota := make(map[int64]int)
	for _, b := range buckets {
		if excess <= 0 {
			break
		}
		n := min(perBucket[b], excess)
		quota[b] = n
		excess -= n
	}
	var dropped int64
	for k := range t.pending {
		b := k.bucketUnix()
		if quota[b] > 0 {
			quota[b]--
			delete(t.pending, k)
			dropped++
		}
	}
	t.droppedRows.Add(dropped)
}

// sortedPending returns the pending rows ordered by primary key.
func (t *aggTable[K, V]) sortedPending() []entry[K, V] {
	rows := make([]entry[K, V], 0, len(t.pending))
	for k, v := range t.pending {
		rows = append(rows, entry[K, V]{key: k, val: v})
	}
	slices.SortFunc(rows, func(a, b entry[K, V]) int { return a.key.compare(b.key) })
	return rows
}

// flush drains the shards and writes pending rows in chunks. Rows that could
// not be written because of transient errors stay pending; rows that can never
// be written are dropped and counted.
func (t *aggTable[K, V]) flush(ctx context.Context, fw *flushWriter) error {
	t.drain()
	if len(t.pending) == 0 {
		return nil
	}
	rows := t.sortedPending()
	w := newRowWriter(fw, t.name, t.granularity, func(e entry[K, V]) int64 { return e.key.bucketUnix() }, t.exec)
	var kept int
	for start := 0; start < len(rows); start += fw.chunkSize {
		chunk := rows[start:min(start+fw.chunkSize, len(rows))]
		if w.stopped(ctx) {
			kept += len(rows) - start
			break
		}
		failed, dropped := w.write(ctx, chunk)
		for _, e := range chunk {
			delete(t.pending, e.key)
		}
		for _, e := range failed {
			t.pending[e.key] = e.val
		}
		kept += len(failed)
		t.droppedRows.Add(int64(dropped))
	}
	if kept > 0 {
		return fmt.Errorf("flush %s: %d rows kept for the next flush", t.name, kept)
	}
	return nil
}

// pendingLen returns the number of pending rows (flusher only).
func (t *aggTable[K, V]) pendingLen() int {
	return len(t.pending)
}
