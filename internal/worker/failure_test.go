package worker

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/signal"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

func TestObserveSuppressionFollowsBreakerGate(t *testing.T) {
	tests := []struct {
		name       string
		fields     func(now time.Time) []string
		suppressed bool
	}{
		{name: "closed (no hash)", fields: func(time.Time) []string { return nil }},
		{name: "open until later", fields: func(now time.Time) []string {
			return []string{"st", "open", "ou", strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10), "man", "0"}
		}, suppressed: true},
		{name: "open but expired (effectively half-open)", fields: func(now time.Time) []string {
			return []string{"st", "open", "ou", strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10), "man", "0"}
		}},
		{name: "manual open without end", fields: func(time.Time) []string {
			return []string{"st", "open", "ou", "0", "man", "1"}
		}, suppressed: true},
		{name: "manual open with expired end", fields: func(now time.Time) []string {
			return []string{"st", "open", "ou", strconv.FormatInt(now.Add(-time.Second).UnixMilli(), 10), "man", "1"}
		}},
		{name: "half-open", fields: func(time.Time) []string { return []string{"st", "half_open"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.identity(t, 1)
			if kv := tt.fields(f.now); kv != nil {
				f.hset(t, f.keys.Breaker(testSiteKey, testGroupKey), kv...)
			}
			f.process(t, withOutcome(f.event(f.lease(t, 1, 0)), policy.OutcomeRateLimited))
			rec := f.lastRecord(t)
			require.Equal(t, tt.suppressed, rec.Suppressed)
			_, _, _, nfail, _ := f.hs(t, 1)
			if tt.suppressed {
				require.Zero(t, nfail)
				require.Empty(t, f.exec.snapshot())
			} else {
				require.EqualValues(t, 1, nfail)
				require.Len(t, f.exec.snapshot(), 1)
			}
		})
	}
}

func TestBreakerWindowFallbacks(t *testing.T) {
	tests := []struct {
		name             string
		spec             *policy.BreakerSpec
		window, bucketMs int64
	}{
		{name: "nil policy uses built-in defaults", spec: nil, window: 60000, bucketMs: 5000},
		{name: "configured", spec: &policy.BreakerSpec{Window: durationx.MustParse("30s"), Buckets: 6}, window: 30000, bucketMs: 5000},
		{name: "zero window", spec: &policy.BreakerSpec{Buckets: 6}, window: 60000, bucketMs: 10000},
		{name: "buckets out of range", spec: &policy.BreakerSpec{Window: durationx.MustParse("2m"), Buckets: 61}, window: 120000, bucketMs: 10000},
		{name: "bucket at least 1ms", spec: &policy.BreakerSpec{Window: durationx.MustParse("10ms"), Buckets: 60}, window: 10, bucketMs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, b := breakerWindow(tt.spec)
			require.Equal(t, tt.window, w)
			require.Equal(t, tt.bucketMs, b)
		})
	}

	// Groups without a breaker policy still feed the breaker window.
	f := newFixture(t)
	f.group.Breaker = nil
	f.identity(t, 1)
	f.process(t, withOutcome(f.event(f.lease(t, 1, 0)), policy.OutcomeCaptcha))
	require.Equal(t, "1", f.hget(t, f.keys.Window(testSiteKey, testGroupKey, f.now.UnixMilli()/5000), "r"))
}

func TestLateIsJudgedByReceiveTime(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	ended := f.now.Add(-15 * time.Minute)
	lease := f.lease(t, 1, 0, "st", "released", "end", strconv.FormatInt(ended.UnixMilli(), 10))

	// Received one minute after the lease ended, processed 15 minutes later
	// because of a backlog: still in time.
	ev := withOutcome(f.event(lease), policy.OutcomeRateLimited)
	ev.ReceivedAt = ended.Add(time.Minute).UnixMilli()
	f.process(t, ev)
	require.False(t, f.lastRecord(t).Late)
	require.Len(t, f.exec.snapshot(), 1)

	// Without a receive time the processing time decides.
	ev = withOutcome(f.event(lease), policy.OutcomeRateLimited)
	ev.ReceivedAt = 0
	f.process(t, ev)
	require.True(t, f.lastRecord(t).Late)
	require.Len(t, f.exec.snapshot(), 1)
}

func TestReleaseOnRedeliveryAndUnknownObserve(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)

	// The first delivery was interrupted before the release step.
	f.w.rel = nil
	ev := f.event(lease)
	ev.Release = true
	id := f.process(t, ev)
	f.w.rel = f.rel
	f.processWithID(t, ev, id)
	require.Equal(t, []releaseCall{{testSiteKey, lease}}, f.rel.calls, "redelivery releases the still active lease")
	require.Len(t, f.rec.snapshot(), 1, "redelivery is not recorded twice")

	// observe.lua fails (wrong key type): statistics, release and breaker
	// notification still happen.
	require.NoError(t, f.rdb.Do(context.Background(), f.rdb.B().Set().Key(f.keys.Health(testSiteKey, testGroupKey)).Value("x").Build()).Error())
	ev = withOutcome(f.event(lease), policy.OutcomeCaptcha)
	ev.Release = true
	f.process(t, ev)
	require.Len(t, f.rel.calls, 2)
	require.Equal(t, [][2]int64{{testSiteKey, testGroupKey}}, f.brk.calls)
	rec := f.lastRecord(t)
	require.Equal(t, policy.OutcomeCaptcha, rec.Outcome)
	require.Empty(t, f.exec.snapshot())
}

func TestAckFailureClaimsEntriesAgain(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	lease := f.leases(t, 4)[0] // identity 4 -> shard 0
	stream := f.keys.Stream(0)
	require.NoError(t, f.w.ensureGroup(ctx, stream))
	ids := make([]string, 0, 3)
	for range 3 {
		ev := f.event(lease)
		ids = append(ids, ev.ReportID)
		data, err := signal.EncodeEvent(ev)
		require.NoError(t, err)
		require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Xadd().Key(stream).Id("*").FieldValue().
			FieldValue(signal.StreamFieldVersion, signal.StreamVersion).FieldValue(signal.StreamFieldData, string(data)).Build()).Error())
	}
	entries, err := f.w.readGroup(ctx, stream)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// XACK against a key of the wrong type fails: every entry stays pending.
	wrong := f.keys.Stream(3)
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Set().Key(wrong).Value("x").Build()).Error())
	complete, acked, _ := f.w.processBatch(ctx, ctx, 0, wrong, entries)
	require.True(t, complete)
	require.False(t, acked)
	n, err := f.w.pendingCount(ctx, 0)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)

	noWait := func(error, string) bool { return false }
	claimed, acked, _ := f.w.claimPending(ctx, ctx, 0, stream, noWait)
	require.True(t, claimed)
	require.True(t, acked)
	n, err = f.w.pendingCount(ctx, 0)
	require.NoError(t, err)
	require.Zero(t, n)
	for _, id := range ids {
		require.Equal(t, 1, f.rec.countByReport()[id], "claimed duplicates are acknowledged, not reprocessed")
	}
	require.Equal(t, "3", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))
}

func TestClaimPendingRecreatesMissingGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	stream := f.keys.Stream(2)
	claimed, acked, _ := f.w.claimPending(ctx, ctx, 2, stream, func(error, string) bool { return true })
	require.True(t, claimed)
	require.True(t, acked)
	groups, err := f.rdb.Do(ctx, f.rdb.B().XinfoGroups().Key(stream).Build()).ToArray()
	require.NoError(t, err)
	require.Len(t, groups, 1)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	claimed, _, _ = f.w.claimPending(cancelled, ctx, 2, stream, func(error, string) bool { return true })
	require.False(t, claimed)
}

// closedClient returns a rueidis client that fails every command.
func closedClient(t *testing.T) rueidis.Client {
	t.Helper()
	url := os.Getenv(testutil.RedisURLEnv)
	if url == "" || testing.Short() {
		t.Skip("requires " + testutil.RedisURLEnv)
	}
	c, err := redis.Open(context.Background(), url, nil)
	require.NoError(t, err)
	c.Close()
	return c
}

func TestConsumerStopsWhileRedisIsDown(t *testing.T) {
	f := newFixture(t)
	w := New(Config{InstanceID: "inst-down", ReportShards: testShards}, closedClient(t), f.keys, f.cat, nil, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go w.consume(ctx, 0, done)
	time.Sleep(250 * time.Millisecond) // a few failed attempts with backoff
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop")
	}

	_, _, err := w.Membership(context.Background())
	require.Error(t, err)
	w.tick(context.Background()) // heartbeat failure is logged, not fatal
	require.Empty(t, w.OwnedShards())
}

func TestRefreshOwnershipWhileRedisIsDown(t *testing.T) {
	f := newFixture(t)
	w := New(Config{InstanceID: "inst-down", ReportShards: testShards}, closedClient(t), f.keys, f.cat, nil, nil, nil, nil, f.metrics, nil)
	now := time.Now()
	states := map[int]*shardState{}
	for shard, last := range map[int]time.Time{
		0: now,                       // refreshed recently: kept for now
		1: now.Add(-9 * time.Second), // refresh failing for too long: stopped
		2: now.Add(-time.Minute),
	} {
		_, cancel := context.WithCancel(context.Background())
		st := &shardState{phase: phaseOwned, cancel: cancel, done: make(chan struct{}), lastRefresh: last}
		close(st.done)
		states[shard] = st
		w.shards[shard] = st
	}
	started := time.Now()
	w.refreshOwnership(context.Background())
	require.Less(t, time.Since(started), opTimeout, "one failed round does not wait per shard")
	w.bg.Wait()
	require.Equal(t, []int{0}, w.OwnedShards())
	require.Equal(t, phaseLost, states[1].phase)
	require.Equal(t, phaseLost, states[2].phase)

	w.updatePending(context.Background()) // errors are only logged
	w.shutdown(context.Background())
	require.Empty(t, w.OwnedShards())
}

func TestParseObserveReplyErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	eval := func(body string) rueidis.RedisResult {
		return f.rdb.Do(ctx, f.rdb.B().Eval().Script(body).Numkeys(0).Build())
	}
	tests := []struct {
		name string
		body string
	}{
		{name: "error reply", body: "return redis.error_reply('boom')"},
		{name: "empty reply", body: "return {}"},
		{name: "unexpected status", body: "return {'NOPE'}"},
		{name: "short reply", body: "return {'OK', 'none'}"},
		{name: "bad score", body: "return {'OK','none',0,0,'active','','t',0,0,0,'x',0,'1.0',0,'0','closed',{},{}}"},
		{name: "bad account", body: "return {'OK','none',0,0,'active','zz','t',0,0,0,'1.0',0,'1.0',0,'0','closed',{},{}}"},
		{name: "counter count mismatch", body: "return {'OK','none',0,0,'active','','t',0,0,0,'1.0',0,'1.0',0,'0','closed',{1},{}}"},
		{name: "counter not an array", body: "return {'OK','none',0,0,'active','','t',0,0,0,'1.0',0,'1.0',0,'0','closed','x',{}}"},
		{name: "bad integer", body: "return {'OK','none','x',0,'active','','t',0,0,0,'1.0',0,'1.0',0,'0','closed',{},{}}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseObserveReply(eval(tt.body), 0, 0)
			require.Error(t, err)
		})
	}
	res, err := parseObserveReply(eval("return {'OK','proxy',1,1,'active','7','t',1,2,3,'10.5',4,'20.5',5,'99','open',{},{}}"), 0, 0)
	require.NoError(t, err)
	require.Equal(t, observeResult{
		blame: policy.BlameProxy, suppressed: true, probe: true, identityState: "active", accountKey: 7, identityType: "t",
		endpointStreak: 1, globalStreak: 2, proxyStreak: 3, endpointScore: 10.5, endpointSamples: 4, globalScore: 20.5,
		globalSamples: 5, endpointCooldownUntil: 99, counts: []int64{}, banCounts: []int64{},
	}, res)
}
