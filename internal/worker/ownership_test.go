package worker

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/signal"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// fastConfig runs the registry and ownership loops quickly for tests.
func fastConfig() Config {
	return Config{
		HeartbeatInterval: 50 * time.Millisecond,
		LiveWindow:        time.Second,
		OwnerTTL:          2 * time.Second,
		OwnerRefresh:      200 * time.Millisecond,
		Block:             100 * time.Millisecond,
		PendingInterval:   100 * time.Millisecond,
		BatchSize:         7,
	}
}

type running struct {
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, w *Worker) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- w.Run(ctx) }()
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case err, ok := <-r.done:
		if ok {
			require.NoError(t, err)
			close(r.done)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not stop")
	}
}

// ingester sends reports through the real signal ingest path.
type ingester struct {
	f   *fixture
	ing *signal.Ingestor
	p   *authz.Principal
	n   int
}

func newIngester(t *testing.T, f *fixture) *ingester {
	t.Helper()
	scopes, err := authz.ParseScopes([]string{"report:write"})
	require.NoError(t, err)
	return &ingester{
		f:   f,
		ing: signal.NewIngestor(signal.Config{ReportShards: testShards}, f.rdb, f.keys, f.cat, nil, nil, nil),
		p:   &authz.Principal{Kind: authz.KindToken, ID: "tok_1", TenantID: testTenant, NamespaceID: testNS, NamespaceName: "prod", Scopes: scopes},
	}
}

func (in *ingester) send(t *testing.T, leases []string, perLease int) []string {
	t.Helper()
	reports := make([]*spinneretv1.Report, 0, perLease*len(leases))
	ids := make([]string, 0, perLease*len(leases))
	now := time.Now()
	for _, lease := range leases {
		for range perLease {
			in.n++
			id := fmt.Sprintf("rep-%d", in.n)
			ids = append(ids, id)
			reports = append(reports, &spinneretv1.Report{
				ReportId: id, LeaseId: lease, Uri: "/search", Method: "GET", HttpStatus: 200,
				StartedAt: timestamppb.New(now), FinishedAt: timestamppb.New(now),
			})
		}
	}
	accepted, duplicated, rejected, err := in.ing.Ingest(context.Background(), in.p, "node-1", reports)
	require.NoError(t, err)
	require.Empty(t, rejected)
	require.Zero(t, duplicated)
	require.Equal(t, len(reports), accepted)
	return ids
}

func (f *fixture) leases(t *testing.T, identities ...int64) []string {
	t.Helper()
	out := make([]string, 0, len(identities))
	for _, i := range identities {
		f.identity(t, i)
		out = append(out, f.lease(t, i, 0))
	}
	return out
}

func requireExactlyOnce(t *testing.T, f *fixture, ids []string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(f.rec.countByReport()) >= len(ids)
	}, 15*time.Second, 20*time.Millisecond, "all reports processed")
	counts := f.rec.countByReport()
	for _, id := range ids {
		require.Equal(t, 1, counts[id], "report %s processed exactly once", id)
	}
	require.Len(t, counts, len(ids))
}

func TestMembership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.newWorker("inst-a", fastConfig())
	b := f.newWorker("inst-b", fastConfig())

	idx, total, err := b.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, [2]int{-1, 0}, [2]int{idx, total}, "not a live member before the first heartbeat")

	// A stale member is pruned by the next heartbeat.
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Zadd().Key(f.keys.Workers()).ScoreMember().ScoreMember(1, "inst-old").Build()).Error())
	live, err := b.registry(ctx, registryBeat)
	require.NoError(t, err)
	require.Equal(t, []string{"inst-b"}, live)
	_, err = a.registry(ctx, registryBeat)
	require.NoError(t, err)

	idx, total, err = a.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, [2]int{0, 2}, [2]int{idx, total})
	idx, total, err = b.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, [2]int{1, 2}, [2]int{idx, total})
	n, err := f.rdb.Do(ctx, f.rdb.B().Zscore().Key(f.keys.Workers()).Member("inst-old").Build()).AsFloat64()
	require.True(t, rueidis.IsRedisNil(err), "stale member removed (score %v)", n)
}

func TestRunProcessesExistingAndNewReports(t *testing.T) {
	f := newFixture(t)
	in := newIngester(t, f)
	leases := f.leases(t, 1, 2, 3, 4)
	before := in.send(t, leases, 3) // enqueued before any consumer group exists

	w := f.newWorker("inst-a", fastConfig())
	r := start(t, w)
	// OwnedShards() grows as each shard starts, but the gauge is published once at
	// the end of the rebalance pass (updateOwnedGauge in ownership.go), so it
	// converges just behind the set. Waiting on the set and then asserting the
	// gauge is a race that a loaded runner loses; wait on the pair.
	require.Eventually(t, func() bool {
		return len(w.OwnedShards()) == testShards &&
			metricValue(f.metrics.StreamOwnedShards) == float64(testShards)
	}, 5*time.Second, 10*time.Millisecond)
	idx, total, err := w.Membership(context.Background())
	require.NoError(t, err)
	require.Equal(t, [2]int{0, 1}, [2]int{idx, total})

	after := in.send(t, leases, 2)
	requireExactlyOnce(t, f, append(slices.Clone(before), after...))
	for _, lease := range leases {
		require.Equal(t, "5", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))
	}

	// Deleting a stream drops its consumer group; the consumer recreates it.
	ctx := context.Background()
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Del().Key(f.keys.Stream(1)).Build()).Error())
	more := in.send(t, leases[:1], 2) // identity 1 -> shard 1
	require.Eventually(t, func() bool { return f.rec.countByReport()[more[1]] == 1 }, 10*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		n, err := w.pendingCount(ctx, 2)
		return err == nil && n == 0
	}, 5*time.Second, 20*time.Millisecond)

	r.stop(t)
	require.Empty(t, w.OwnedShards())
	for shard := range testShards {
		n, err := f.rdb.Do(ctx, f.rdb.B().Exists().Key(f.keys.ShardOwner(shard)).Build()).AsInt64()
		require.NoError(t, err)
		require.Zero(t, n, "shard %d lock released", shard)
	}
	idx, total, err = w.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, [2]int{-1, 0}, [2]int{idx, total}, "left the registry")
	require.Zero(t, counterValue(t, f.metrics.StreamOwnedShards))
}

func TestRebalanceBetweenTwoWorkers(t *testing.T) {
	f := newFixture(t)
	in := newIngester(t, f)
	leases := f.leases(t, 1, 2, 3, 4, 5, 6, 7, 8)
	var ids []string

	a := f.newWorker("inst-a", fastConfig())
	start(t, a)
	require.Eventually(t, func() bool { return len(a.OwnedShards()) == testShards }, 5*time.Second, 10*time.Millisecond)
	ids = append(ids, in.send(t, leases, 3)...)

	b := f.newWorker("inst-b", fastConfig())
	rb := start(t, b)
	for range 5 {
		ids = append(ids, in.send(t, leases, 1)...)
		time.Sleep(30 * time.Millisecond)
	}
	require.Eventually(t, func() bool {
		oa, ob := a.OwnedShards(), b.OwnedShards()
		if len(oa) != 2 || len(ob) != 2 {
			return false
		}
		for _, s := range oa {
			if slices.Contains(ob, s) {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "shards split between the workers")
	ctx := context.Background()
	for shard := range testShards {
		owner, err := f.rdb.Do(ctx, f.rdb.B().Get().Key(f.keys.ShardOwner(shard)).Build()).ToString()
		require.NoError(t, err)
		if slices.Contains(a.OwnedShards(), shard) {
			require.Equal(t, "inst-a", owner)
		} else {
			require.Equal(t, "inst-b", owner)
		}
	}
	ia, total, err := a.Membership(ctx)
	require.NoError(t, err)
	ib, _, err := b.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.ElementsMatch(t, []int{0, 1}, []int{ia, ib})

	ids = append(ids, in.send(t, leases, 2)...)
	rb.stop(t)
	require.Eventually(t, func() bool { return len(a.OwnedShards()) == testShards }, 10*time.Second, 10*time.Millisecond,
		"the remaining worker takes over every shard")
	ib, total, err = b.Membership(ctx)
	require.NoError(t, err)
	require.Equal(t, [2]int{-1, 1}, [2]int{ib, total}, "a stopped worker leaves the registry")
	ids = append(ids, in.send(t, leases, 2)...)

	requireExactlyOnce(t, f, ids)
	for _, lease := range leases {
		require.Equal(t, "12", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"), "every report observed exactly once")
	}
}

func TestPendingEntriesTakenOverExactlyOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	leases := f.leases(t, 4) // identity 4 -> shard 0
	stream := f.keys.Stream(0)
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().XgroupCreate().Key(stream).Group(ConsumerGroup).Id("0").Mkstream().Build()).Error())

	ids := make([]string, 0, 5)
	for range 5 {
		ev := f.event(leases[0])
		ids = append(ids, ev.ReportID)
		data, err := signal.EncodeEvent(ev)
		require.NoError(t, err)
		require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Xadd().Key(stream).Id("*").FieldValue().
			FieldValue(signal.StreamFieldVersion, signal.StreamVersion).FieldValue(signal.StreamFieldData, string(data)).Build()).Error())
	}

	// A consumer read every entry and crashed after applying the first two.
	crashed := f.newWorker("inst-crashed", fastConfig())
	entries, err := crashed.readGroup(ctx, stream)
	require.NoError(t, err)
	require.Len(t, entries, 5)
	crashed.processEntry(ctx, 0, entries[0])
	crashed.processEntry(ctx, 0, entries[1])
	n, err := crashed.pendingCount(ctx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 5, n)

	w := f.newWorker("inst-a", fastConfig())
	start(t, w)
	requireExactlyOnce(t, f, ids)
	require.Equal(t, "5", f.hget(t, f.keys.Lease(testSiteKey, leases[0]), "rc"))
	_, _, samples, _, _ := f.hs(t, 4)
	require.EqualValues(t, 5, samples)
	require.Eventually(t, func() bool {
		n, err := w.pendingCount(ctx, 0)
		return err == nil && n == 0
	}, 5*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		return counterValue(t, f.metrics.StreamPending.WithLabelValues("0")) == 0
	}, 5*time.Second, 20*time.Millisecond)
}

func TestLostOwnershipStopsConsumer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	w := f.newWorker("inst-a", fastConfig())
	start(t, w)
	require.Eventually(t, func() bool { return len(w.OwnedShards()) == testShards }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Set().Key(f.keys.ShardOwner(0)).Value("intruder").Build()).Error())
	require.Eventually(t, func() bool { return !slices.Contains(w.OwnedShards(), 0) }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // several heartbeats and refreshes
	require.Equal(t, []int{1, 2, 3}, w.OwnedShards())
	owner, err := f.rdb.Do(ctx, f.rdb.B().Get().Key(f.keys.ShardOwner(0)).Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "intruder", owner, "a lost lock is never deleted")

	// Once the intruder is gone the shard is acquired again.
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Del().Key(f.keys.ShardOwner(0)).Build()).Error())
	require.Eventually(t, func() bool { return len(w.OwnedShards()) == testShards }, 5*time.Second, 10*time.Millisecond)
}

func TestAdoptOwnLockAfterRestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for shard := range testShards {
		require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Set().Key(f.keys.ShardOwner(shard)).Value("inst-a").PxMilliseconds(60000).Build()).Error())
	}
	w := f.newWorker("inst-a", fastConfig())
	start(t, w)
	require.Eventually(t, func() bool { return len(w.OwnedShards()) == testShards }, 5*time.Second, 10*time.Millisecond)
	ttl, err := f.rdb.Do(ctx, f.rdb.B().Pttl().Key(f.keys.ShardOwner(0)).Build()).AsInt64()
	require.NoError(t, err)
	require.LessOrEqual(t, ttl, (2 * time.Second).Milliseconds(), "adopted lock uses the configured TTL")
}

func TestRunErrors(t *testing.T) {
	f := newFixture(t)
	w := f.newWorker("inst-a", fastConfig())
	start(t, w)
	require.Eventually(t, func() bool { return w.running.Load() }, time.Second, 5*time.Millisecond)
	require.ErrorContains(t, w.Run(context.Background()), "already running")

	orphan := New(Config{}, nil, f.keys, nil, nil, nil, nil, nil, nil, nil)
	require.ErrorContains(t, orphan.Run(context.Background()), "required")
}

func TestNormalizeConfig(t *testing.T) {
	w := New(Config{ReportShards: 1000, BatchSize: 5000, OwnerTTL: time.Second, OwnerRefresh: 2 * time.Second,
		HeartbeatInterval: 3 * time.Second, LiveWindow: time.Second}, nil, f0Keys(), nil, nil, nil, nil, nil, nil, nil)
	cfg := w.cfg
	require.Equal(t, MaxReportShards, cfg.ReportShards)
	require.Equal(t, MaxBatchSize, cfg.BatchSize)
	require.NotEmpty(t, cfg.InstanceID)
	require.Equal(t, w.InstanceID(), cfg.InstanceID)
	require.Equal(t, time.Second/3, cfg.OwnerRefresh)
	require.Equal(t, 15*time.Second, cfg.LiveWindow)
	require.Equal(t, DefaultLateReportWindow, cfg.LateReportWindow)
	require.Equal(t, DefaultBlock, cfg.Block)
	require.Equal(t, DefaultPendingInterval, cfg.PendingInterval)

	d := New(Config{InstanceID: "x"}, nil, f0Keys(), nil, nil, nil, nil, nil, nil, nil).cfg
	require.Equal(t, Config{
		ReportShards: DefaultReportShards, InstanceID: "x", LateReportWindow: DefaultLateReportWindow,
		BatchSize: DefaultBatchSize, Block: DefaultBlock, HeartbeatInterval: DefaultHeartbeatInterval,
		LiveWindow: DefaultLiveWindow, OwnerTTL: DefaultOwnerTTL, OwnerRefresh: DefaultOwnerRefresh,
		PendingInterval: DefaultPendingInterval,
	}, d)
	require.NotNil(t, New(Config{InstanceID: "x"}, nil, f0Keys(), nil, nil, nil, nil, nil, nil, nil).fallbackAction)
}

func f0Keys() redis.Keys { return redis.NewKeys("") }
