package stats

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/postgres"
)

func newTestNodeTable(maxKeys int) *aggTable[nodeKey, nodeValue] {
	return newAggTable(postgres.TableNodeStatsMinutely, postgres.GranularityDay, maxKeys,
		func(context.Context, []entry[nodeKey, nodeValue]) error { return nil })
}

func TestKeyCompareAndMerge(t *testing.T) {
	t.Parallel()

	acq := []acquireKey{
		{bucket: 120, namespaceID: "a", siteID: "s", endpointGroupID: "e", result: "ok"},
		{bucket: 60, namespaceID: "b", siteID: "s", endpointGroupID: "e", result: "ok"},
		{bucket: 60, namespaceID: "a", siteID: "s", endpointGroupID: "e", result: "ok"},
		{bucket: 60, namespaceID: "a", siteID: "s", endpointGroupID: "e", result: "error"},
	}
	slices.SortFunc(acq, acquireKey.compare)
	require.Equal(t, "error", acq[0].result)
	require.Equal(t, "ok", acq[1].result)
	require.Equal(t, "b", acq[2].namespaceID)
	require.Equal(t, int64(120), acq[3].bucket)
	require.Equal(t, 0, acq[0].compare(acq[0]))
	require.Equal(t, acq[0].shardHash(), acq[0].shardHash())
	require.Equal(t, int64(60), acq[0].bucketUnix())
	require.Equal(t, acquireValue{count: 3, durationUs: 30}, acquireValue{1, 10}.merge(acquireValue{2, 20}))

	pk := []payloadKey{{bucket: 1, tokenID: "b"}, {bucket: 1, tokenID: "a"}, {bucket: 0, tokenID: "z"}}
	slices.SortFunc(pk, payloadKey.compare)
	require.Equal(t, []string{"z", "a", "b"}, []string{pk[0].tokenID, pk[1].tokenID, pk[2].tokenID})
	require.Equal(t, int64(1), pk[1].bucketUnix())
	require.NotZero(t, pk[0].shardHash()^pk[1].shardHash())
	require.Equal(t, countValue{count: 5}, countValue{2}.merge(countValue{3}))

	nk := []nodeKey{{bucket: 1, node: "b"}, {bucket: 1, node: "a"}}
	slices.SortFunc(nk, nodeKey.compare)
	require.Equal(t, "a", nk[0].node)
	require.Equal(t, int64(1), nk[0].bucketUnix())
	require.Equal(t, nk[0].shardHash(), nodeKey{bucket: 1, node: "a"}.shardHash())
	require.Equal(t, nodeValue{1, 2, 3, 4}, nodeValue{acquires: 1, reports: 1}.merge(nodeValue{reports: 1, abandoned: 3, rejected: 4}))

	ok := []outcomeKey{{bucket: 1, proxyID: "p2"}, {bucket: 1, proxyID: "p1"}, {bucket: 1, proxyID: "p1", outcome: "a"}}
	slices.SortFunc(ok, outcomeKey.compare)
	require.Equal(t, outcomeKey{bucket: 1, proxyID: "p1"}, ok[0])
	require.Equal(t, "a", ok[1].outcome)
	require.Equal(t, int64(1), ok[0].bucketUnix())
	require.Equal(t, ok[0].shardHash(), outcomeKey{bucket: 1, proxyID: "p1"}.shardHash())
	require.Equal(t, outcomeValue{2, 30, 300}, outcomeValue{1, 10, 100}.merge(outcomeValue{1, 20, 200}))

	ik := []identityKey{{bucket: 3600, identityID: "i2"}, {bucket: 3600, identityID: "i1"}}
	slices.SortFunc(ik, identityKey.compare)
	require.Equal(t, "i1", ik[0].identityID)
	require.Equal(t, int64(3600), ik[0].bucketUnix())
	require.Equal(t, ik[0].shardHash(), identityKey{bucket: 3600, identityID: "i1"}.shardHash())
	require.Equal(t, identityValue{siteID: "s1", count: 3}, identityValue{siteID: "s1", count: 1}.merge(identityValue{siteID: "s2", count: 2}))
	require.Equal(t, identityValue{siteID: "s2", count: 3}, identityValue{count: 1}.merge(identityValue{siteID: "s2", count: 2}))
}

func TestAggTableAddMergesAndCaps(t *testing.T) {
	t.Parallel()
	tbl := newTestNodeTable(3)
	for i := range 5 {
		tbl.add(nodeKey{bucket: 60, namespaceID: "ns", node: "n"}, nodeValue{acquires: int64(i)})
	}
	tbl.add(nodeKey{bucket: 60, namespaceID: "ns", node: "m"}, nodeValue{reports: 1})
	tbl.add(nodeKey{bucket: 120, namespaceID: "ns", node: "n"}, nodeValue{reports: 1})
	require.Equal(t, int64(3), tbl.size.Load())
	require.Zero(t, tbl.droppedKeys.Load())

	// A fourth distinct key is rejected, existing keys still merge.
	tbl.add(nodeKey{bucket: 180, namespaceID: "ns", node: "n"}, nodeValue{reports: 1})
	tbl.add(nodeKey{bucket: 60, namespaceID: "ns", node: "m"}, nodeValue{reports: 1})
	require.Equal(t, int64(1), tbl.droppedKeys.Load())

	tbl.drain()
	require.Zero(t, tbl.size.Load())
	require.Equal(t, 3, tbl.pendingLen())
	require.Equal(t, nodeValue{acquires: 10}, tbl.pending[nodeKey{bucket: 60, namespaceID: "ns", node: "n"}])
	require.Equal(t, nodeValue{reports: 2}, tbl.pending[nodeKey{bucket: 60, namespaceID: "ns", node: "m"}])

	// After draining the shards accept new keys again and merge into pending.
	tbl.add(nodeKey{bucket: 60, namespaceID: "ns", node: "m"}, nodeValue{rejected: 7})
	tbl.drain()
	require.Equal(t, nodeValue{reports: 2, rejected: 7}, tbl.pending[nodeKey{bucket: 60, namespaceID: "ns", node: "m"}])

	rows := tbl.sortedPending()
	require.Len(t, rows, 3)
	require.True(t, slices.IsSortedFunc(rows, func(a, b entry[nodeKey, nodeValue]) int { return a.key.compare(b.key) }))
}

func TestAggTableTrimDropsOldestBuckets(t *testing.T) {
	t.Parallel()
	tbl := newTestNodeTable(100)
	tbl.maxPending = 5
	for bucket := int64(1); bucket <= 4; bucket++ {
		for _, node := range []string{"a", "b"} {
			tbl.pending[nodeKey{bucket: bucket * 60, namespaceID: "ns", node: node}] = nodeValue{reports: 1}
		}
	}
	tbl.trim()
	// 8 rows, cap 5: bucket 60 (2 rows) is dropped and bucket 120 thinned by one row.
	require.Equal(t, 5, tbl.pendingLen())
	require.Equal(t, int64(3), tbl.droppedRows.Load())
	perBucket := map[int64]int{}
	for k := range tbl.pending {
		perBucket[k.bucket]++
	}
	require.Equal(t, map[int64]int{120: 1, 180: 2, 240: 2}, perBucket)

	tbl.trim()
	require.Equal(t, 5, tbl.pendingLen(), "within the cap nothing is dropped")
}

func TestAggTableTrimSingleOversizedBucket(t *testing.T) {
	t.Parallel()
	tbl := newTestNodeTable(100)
	tbl.maxPending = 3
	for i := range 10 {
		tbl.pending[nodeKey{bucket: 60, namespaceID: "ns", node: string(rune('a' + i))}] = nodeValue{reports: 1}
	}
	tbl.trim()
	require.Equal(t, 3, tbl.pendingLen(), "a bucket larger than the cap is thinned, not emptied")
	require.Equal(t, int64(7), tbl.droppedRows.Load())
}

func TestAggTableConcurrentAdd(t *testing.T) {
	t.Parallel()
	tbl := newTestNodeTable(DefaultMaxKeys)
	const goroutines, perGoroutine = 16, 5000
	var wg sync.WaitGroup
	stop := make(chan struct{})
	drained := make(chan int64)
	// A concurrent drainer must not lose or duplicate counts.
	go func() {
		var total int64
		for {
			select {
			case <-stop:
				tbl.drain()
				for _, v := range tbl.pending {
					total += v.reports
				}
				drained <- total
				return
			default:
				tbl.drain()
			}
		}
	}()
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				tbl.add(nodeKey{bucket: int64(i%7) * 60, namespaceID: "ns", node: string(rune('a' + g%5))}, nodeValue{reports: 1})
			}
		}()
	}
	wg.Wait()
	close(stop)
	require.Equal(t, int64(goroutines*perGoroutine), <-drained)
	require.Equal(t, 35, tbl.pendingLen())
	require.Zero(t, tbl.droppedKeys.Load())
}
