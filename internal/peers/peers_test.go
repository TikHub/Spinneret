package peers

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// discardLogger keeps the expected warnings of the failure tests out of the
// test output.
var discardLogger = slog.New(slog.DiscardHandler)

// liveRecorder records the counts reported through Config.OnLive.
type liveRecorder struct {
	mu     sync.Mutex
	counts []int
	// notify receives every count so tests can wait for a beat without sleeping.
	notify chan int
}

func newLiveRecorder() *liveRecorder {
	return &liveRecorder{notify: make(chan int, 64)}
}

func (l *liveRecorder) record(live int) {
	l.mu.Lock()
	l.counts = append(l.counts, live)
	l.mu.Unlock()
	select {
	case l.notify <- live:
	default:
	}
}

func (l *liveRecorder) snapshot() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.counts...)
}

// fixture is one Redis server and key prefix shared by the registries of a test.
type fixture struct {
	rdb  rueidis.Client
	keys redis.Keys
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	return &fixture{rdb: rdb, keys: keys}
}

// registry builds a registry for instance id on the fixture's server.
func (f *fixture) registry(id string, onLive func(int)) *Registry {
	return New(Config{InstanceID: id, OnLive: onLive}, f.rdb, f.keys, discardLogger)
}

// members returns the instance ids currently in the registry ZSET.
func (f *fixture) members(t *testing.T) []string {
	t.Helper()
	cmd := f.rdb.B().Zrange().Key(f.keys.Acquirers()).Min("0").Max("-1").Build()
	got, err := f.rdb.Do(context.Background(), cmd).AsStrSlice()
	require.NoError(t, err)
	return got
}

// addMemberAgedBy writes a member whose heartbeat is `age` old, which may be
// inside or outside the live window.
func (f *fixture) addMemberAgedBy(t *testing.T, id string, age time.Duration) {
	t.Helper()
	score := float64(time.Now().Add(-age).UnixMilli())
	cmd := f.rdb.B().Zadd().Key(f.keys.Acquirers()).ScoreMember().ScoreMember(score, id).Build()
	require.NoError(t, f.rdb.Do(context.Background(), cmd).Error())
}

// addStaleMember writes a member whose heartbeat is far outside the live window.
func (f *fixture) addStaleMember(t *testing.T, id string) {
	t.Helper()
	stale := float64(time.Now().Add(-time.Hour).UnixMilli())
	cmd := f.rdb.B().Zadd().Key(f.keys.Acquirers()).ScoreMember().ScoreMember(stale, id).Build()
	require.NoError(t, f.rdb.Do(context.Background(), cmd).Error())
}

func TestBeatCountsLiveMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	ids := []string{"api-1", "api-2", "api-3"}
	regs := make([]*Registry, 0, len(ids))
	for i, id := range ids {
		r := f.registry(id, nil)
		regs = append(regs, r)
		live, err := r.Beat(ctx)
		require.NoError(t, err)
		// Each registration is immediately visible, to itself and to the peers
		// that registered before it.
		require.Equal(t, i+1, live)
		require.Equal(t, i+1, r.Live())
	}

	for _, r := range regs {
		live, err := r.Beat(ctx)
		require.NoError(t, err)
		require.Equal(t, len(ids), live)
		require.Equal(t, len(ids), r.Live())
	}
	require.ElementsMatch(t, ids, f.members(t))
}

func TestBeatReportsCountThroughOnLive(t *testing.T) {
	f := newFixture(t)
	rec := newLiveRecorder()
	r := f.registry("api-1", rec.record)

	for range 3 {
		_, err := r.Beat(context.Background())
		require.NoError(t, err)
	}
	require.Equal(t, []int{1, 1, 1}, rec.snapshot())
}

func TestBeatPrunesStale(t *testing.T) {
	f := newFixture(t)
	f.addStaleMember(t, "api-gone")
	require.Equal(t, []string{"api-gone"}, f.members(t))

	r := f.registry("api-1", nil)
	live, err := r.Beat(context.Background())
	require.NoError(t, err)
	// The stale instance is neither counted nor kept.
	require.Equal(t, 1, live)
	require.Equal(t, []string{"api-1"}, f.members(t))
}

func TestBeatFailureKeepsLastCount(t *testing.T) {
	f := newFixture(t)
	rec := newLiveRecorder()
	other := f.registry("api-2", nil)
	_, err := other.Beat(context.Background())
	require.NoError(t, err)

	r := f.registry("api-1", rec.record)
	live, err := r.Beat(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, live)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	live, err = r.Beat(canceled)
	require.Error(t, err)
	// A failed beat reports the last known count and does not notify: a Redis
	// problem must never widen the gate.
	require.Equal(t, 2, live)
	require.Equal(t, 2, r.Live())
	require.Equal(t, []int{2}, rec.snapshot())
}

func TestLiveIsAtLeastOne(t *testing.T) {
	f := newFixture(t)

	// Before the first beat.
	require.Equal(t, 1, f.registry("api-1", nil).Live())
	// Without a client at all, which is how an unreachable Redis looks to the
	// gate: the divisor stays 1 instead of becoming 0.
	unreachable := New(Config{InstanceID: "api-1"}, nil, f.keys, discardLogger)
	live, err := unreachable.Beat(context.Background())
	require.Error(t, err)
	require.Equal(t, 1, live)
	require.Equal(t, 1, unreachable.Live())
	// And on a registry that was never constructed.
	var nilRegistry *Registry
	require.Equal(t, 1, nilRegistry.Live())
	require.Empty(t, nilRegistry.InstanceID())
}

func TestConcurrentBeatsConverge(t *testing.T) {
	f := newFixture(t)
	const (
		instances = 6
		beats     = 5
	)
	ids := make([]string, 0, instances)
	regs := make([]*Registry, 0, instances)
	for i := range instances {
		id := "api-" + strconv.Itoa(i)
		ids = append(ids, id)
		regs = append(regs, f.registry(id, nil))
	}

	// Every instance beats repeatedly at the same time; the counts they observe
	// grow as the peers register, so only their range and the converged state
	// can be asserted. Failures are collected and reported from the test
	// goroutine.
	type beatResult struct {
		live int
		err  error
	}
	results := make(chan beatResult, instances*beats)
	var wg sync.WaitGroup
	for _, r := range regs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range beats {
				live, err := r.Beat(context.Background())
				results <- beatResult{live: live, err: err}
			}
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		require.NoError(t, got.err)
		require.GreaterOrEqual(t, got.live, 1)
		require.LessOrEqual(t, got.live, instances)
	}

	for _, r := range regs {
		live, err := r.Beat(context.Background())
		require.NoError(t, err)
		require.Equal(t, instances, live)
	}
	require.ElementsMatch(t, ids, f.members(t))
}

func TestRunDeregistersOnExit(t *testing.T) {
	f := newFixture(t)
	rec := newLiveRecorder()
	stays := f.registry("api-stays", nil)
	_, err := stays.Beat(context.Background())
	require.NoError(t, err)

	r := f.registry("api-leaves", rec.record)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Run beats once immediately; waiting for that beat instead of sleeping
	// keeps the test deterministic.
	select {
	case live := <-rec.notify:
		require.Equal(t, 2, live)
	case <-time.After(15 * time.Second):
		t.Fatal("registry did not beat")
	}
	require.ElementsMatch(t, []string{"api-stays", "api-leaves"}, f.members(t))

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("registry did not stop")
	}
	// The departure uses a context detached from the cancelled one.
	require.Equal(t, []string{"api-stays"}, f.members(t))

	live, err := stays.Beat(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, live)
}

func TestRunRejectsMisuse(t *testing.T) {
	f := newFixture(t)

	require.Error(t, New(Config{InstanceID: "api-1"}, nil, f.keys, discardLogger).Run(context.Background()))

	rec := newLiveRecorder()
	r := f.registry("api-1", rec.record)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(15 * time.Second):
			t.Fatal("registry did not stop")
		}
	})

	// The first beat proves the loop is up, so a second Run must be refused.
	select {
	case <-rec.notify:
	case <-time.After(15 * time.Second):
		t.Fatal("registry did not beat")
	}
	require.Error(t, r.Run(context.Background()))
}

func TestNewGeneratesInstanceID(t *testing.T) {
	f := newFixture(t)
	first := f.registry("", nil)
	second := f.registry("", nil)
	require.NotEmpty(t, first.InstanceID())
	require.NotEqual(t, first.InstanceID(), second.InstanceID())

	// Two unconfigured instances are two members, not one.
	_, err := first.Beat(context.Background())
	require.NoError(t, err)
	live, err := second.Beat(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, live)
}

func TestBeatHealthIsObservable(t *testing.T) {
	f := newFixture(t)
	// A registry whose beats never succeed keeps the member count at its initial
	// 1, which is indistinguishable from a real single-instance deployment. The
	// failure count and the age of the count are what tell the two apart, so both
	// have to be readable without a successful beat ever having happened.
	clock := &stepClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{InstanceID: "api-1", Now: clock.read}, nil, f.keys, discardLogger)
	require.Zero(t, r.BeatFailures())
	require.Zero(t, r.BeatAge(), "the age is measured from construction, not from zero")

	clock.advance(30 * time.Second)
	_, err := r.Beat(context.Background())
	require.Error(t, err)
	require.Equal(t, int64(1), r.BeatFailures())
	require.Equal(t, 30*time.Second, r.BeatAge(), "a frozen count ages")

	// A successful beat resets the age and leaves the failure count alone.
	live := New(Config{InstanceID: "api-2", Now: clock.read}, f.rdb, f.keys, nil)
	n, err := live.Beat(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Zero(t, live.BeatFailures())
	require.Zero(t, live.BeatAge())

	// And a nil registry — an instance with a pinned limit — reports nothing.
	var none *Registry
	require.Zero(t, none.BeatFailures())
	require.Zero(t, none.BeatAge())
}

// stepClock is a hand-advanced clock; the registry only uses it to report the
// age of its member count, so nothing in the test depends on real time.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// TestLiveWindowToleratesMissedBeats pins the ratio between the heartbeat
// interval and the live window, and the reason for it.
//
// A beat needs Redis, and the moment admission control matters is the moment
// Redis is saturated — so beats are exactly then at risk of exceeding
// opTimeout. Measured under a two-replica overload with a 10 s window, one
// instance missed enough beats to be pruned by the other, which then divided
// the fleet budget by 1 and admitted all of it: the gate widened precisely when
// it should have held. A graceful shutdown deregisters the instance itself, so
// this window only governs crash detection, and a crashed instance's stale
// membership makes the survivors narrower — the safe direction.
//
// Tightening the window is therefore a correctness regression, not a tuning
// choice, and this test fails if someone does it.
func TestLiveWindowToleratesMissedBeats(t *testing.T) {
	const minMissedBeats = 10
	require.GreaterOrEqualf(t, LiveWindow, minMissedBeats*BeatInterval,
		"LiveWindow (%s) must tolerate at least %d missed beats of %s",
		LiveWindow, minMissedBeats, BeatInterval)
	require.Greaterf(t, LiveWindow, minMissedBeats*opTimeout,
		"LiveWindow (%s) must outlast %d beats that each time out after %s",
		LiveWindow, minMissedBeats, opTimeout)
}

// TestPeerSurvivesMissedBeats is the behavioural half of the test above: an
// instance that last beat several intervals ago is still counted, so a peer
// struggling against a saturated Redis does not hand its share to the others.
func TestPeerSurvivesMissedBeats(t *testing.T) {
	f := newFixture(t)
	// api-2 last beat five intervals ago — well inside the live window.
	f.addMemberAgedBy(t, "api-2", 5*BeatInterval)

	r := f.registry("api-1", nil)
	live, err := r.Beat(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, live, "a peer that missed beats must still be counted")
	require.ElementsMatch(t, []string{"api-1", "api-2"}, f.members(t))
}
