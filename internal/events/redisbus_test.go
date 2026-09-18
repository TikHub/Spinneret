package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

var (
	containerOnce sync.Once
	containerURL  string
	containerErr  error
)

func testRedisURL(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires Redis; skipped in -short mode")
	}
	if u := os.Getenv("SPINNERET_TEST_REDIS_URL"); u != "" {
		return u
	}
	containerOnce.Do(func() {
		ctx := context.Background()
		c, err := tcredis.Run(ctx, "valkey/valkey:8-alpine")
		if err != nil {
			containerErr = err
			return
		}
		containerURL, containerErr = c.ConnectionString(ctx)
	})
	require.NoError(t, containerErr)
	return containerURL
}

func newClient(t *testing.T) rueidis.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := redis.Open(ctx, testRedisURL(t), nil)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

func uniqueKeys(t *testing.T) redis.Keys {
	t.Helper()
	var b [5]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	return redis.NewKeys("t" + hex.EncodeToString(b[:]))
}

// startBus runs bus until the test ends and waits for its subscription.
func startBus(t *testing.T, bus *redisBus) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bus.Run(ctx) }()
	var once sync.Once
	var runErr error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case runErr = <-done:
			case <-time.After(10 * time.Second):
				t.Error("bus did not stop")
			}
		})
		return runErr
	}
	t.Cleanup(func() { _ = stop() })
	select {
	case <-bus.ready:
	case err := <-done:
		t.Fatalf("bus stopped before subscribing: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("bus did not subscribe")
	}
	return stop
}

func logBuffer() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, nil)), buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRedisBusDeliversToPeersExactlyOnce(t *testing.T) {
	keys := uniqueKeys(t)
	a := newRedisBus(newClient(t), keys, "instance-a", nil)
	b := newRedisBus(newClient(t), keys, "instance-b", nil)
	other := newRedisBus(newClient(t), uniqueKeys(t), "instance-other", nil)
	startBus(t, a)
	startBus(t, b)
	startBus(t, other)

	var aCatalog, aAll, bCatalog, bAll, otherAll recorder
	a.Subscribe(ChannelCatalog, aCatalog.handler)
	a.Subscribe(ChannelAll, aAll.handler)
	b.Subscribe(ChannelCatalog, bCatalog.handler)
	b.Subscribe(ChannelAll, bAll.handler)
	other.Subscribe(ChannelAll, otherAll.handler)

	ctx := context.Background()
	at := time.Date(2025, 9, 16, 8, 30, 11, 962_000_000, time.UTC)
	ev := Event{Type: "invalidate", TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", At: at, Data: json.RawMessage(`{"ns":"ns_1"}`)}
	require.NoError(t, a.Publish(ctx, ChannelCatalog, ev))

	// Local delivery is synchronous.
	require.Equal(t, 1, aCatalog.count())
	require.Equal(t, 1, aAll.count())

	require.Eventually(t, func() bool { return bCatalog.count() == 1 && bAll.count() == 1 }, 5*time.Second, 10*time.Millisecond)
	got := bCatalog.snapshot()[0]
	require.Equal(t, ChannelCatalog, got.channel)
	require.Equal(t, ev.Type, got.ev.Type)
	require.Equal(t, ev.TenantID, got.ev.TenantID)
	require.Equal(t, ev.NamespaceID, got.ev.NamespaceID)
	require.Equal(t, ev.SiteID, got.ev.SiteID)
	require.True(t, ev.At.Equal(got.ev.At))
	require.JSONEq(t, string(ev.Data), string(got.ev.Data))

	// Peer to peer in the other direction on a namespace channel.
	require.NoError(t, b.Publish(ctx, NamespaceChannel("ns_1"), Event{Type: TypeBreakerTransition}))
	require.Eventually(t, func() bool { return aAll.count() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, "ns:ns_1", aAll.snapshot()[1].channel)

	// Give echoes a chance to arrive, then verify nothing was delivered twice
	// and nothing crossed prefixes.
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, 1, aCatalog.count())
	require.Equal(t, 2, aAll.count())
	require.Equal(t, 1, bCatalog.count())
	require.Equal(t, 2, bAll.count())
	require.Zero(t, otherAll.count())
}

func TestRedisBusUnsubscribe(t *testing.T) {
	keys := uniqueKeys(t)
	a := newRedisBus(newClient(t), keys, "a", nil)
	b := newRedisBus(newClient(t), keys, "b", nil)
	startBus(t, a)
	startBus(t, b)

	var first, second recorder
	unsub := b.Subscribe(ChannelTokens, first.handler)
	b.Subscribe(ChannelTokens, second.handler)

	ctx := context.Background()
	require.NoError(t, a.Publish(ctx, ChannelTokens, Event{Type: "revoked"}))
	require.Eventually(t, func() bool { return first.count() == 1 && second.count() == 1 }, 5*time.Second, 10*time.Millisecond)

	unsub()
	require.NoError(t, a.Publish(ctx, ChannelTokens, Event{Type: "revoked"}))
	require.Eventually(t, func() bool { return second.count() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, first.count())
}

func TestRedisBusIgnoresInvalidMessages(t *testing.T) {
	keys := uniqueKeys(t)
	client := newClient(t)
	logger, logs := logBuffer()
	bus := newRedisBus(client, keys, "self", logger)
	startBus(t, bus)
	var all recorder
	bus.Subscribe(ChannelAll, all.handler)

	ctx := context.Background()
	publish := func(channel, payload string) {
		require.NoError(t, client.Do(ctx, client.B().Publish().Channel(channel).Message(payload).Build()).Error())
	}
	encode := func(env envelope) string {
		raw, err := json.Marshal(env)
		require.NoError(t, err)
		return string(raw)
	}

	publish(keys.Channel(ChannelConfig), "not json")
	publish(keys.Channel(ChannelConfig), encode(envelope{Origin: "self", Channel: ChannelConfig, Event: Event{Type: "echo"}}))
	publish(keys.Channel(ChannelConfig), encode(envelope{Origin: "peer", Channel: ChannelCatalog, Event: Event{Type: "mismatch"}}))
	publish(keys.Channel(""), encode(envelope{Origin: "peer", Channel: "", Event: Event{Type: "empty"}}))
	publish(keys.Channel(ChannelAll), encode(envelope{Origin: "peer", Channel: ChannelAll, Event: Event{Type: "wildcard"}}))
	publish(keys.Channel(ChannelConfig), encode(envelope{Origin: "peer", Channel: ChannelConfig, Event: Event{Type: "valid"}}))

	require.Eventually(t, func() bool { return all.count() == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	got := all.snapshot()
	require.Len(t, got, 1)
	require.Equal(t, "valid", got[0].ev.Type)
	require.Contains(t, logs.String(), "malformed message")
	require.Contains(t, logs.String(), "mismatched channel")
	require.NotContains(t, logs.String(), "not json", "payloads are never logged")

	// Messages outside the prefix are ignored by handle directly.
	bus.handle(ctx, rueidis.PubSubMessage{Channel: "elsewhere:ch:config", Message: "{}"})
	require.Equal(t, 1, all.count())
}

func TestRedisBusPublishErrors(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	bus := newRedisBus(c, uniqueKeys(t), "", nil)
	require.NotEmpty(t, bus.instanceID, "random instance id generated")

	require.ErrorIs(t, bus.Publish(ctx, "", Event{}), ErrInvalidChannel)
	require.ErrorIs(t, bus.Publish(ctx, ChannelCatalog, Event{Data: json.RawMessage("{")}), ErrInvalidData)

	var local recorder
	bus.Subscribe(ChannelCatalog, local.handler)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	err := bus.Publish(canceled, ChannelCatalog, Event{Type: "x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "events: publish catalog")
	require.Equal(t, 1, local.count(), "local delivery happens even when the broadcast fails")
}

func TestRedisBusRejectsConcurrentRun(t *testing.T) {
	bus := newRedisBus(newClient(t), uniqueKeys(t), "single", nil)
	stop := startBus(t, bus)
	require.ErrorIs(t, bus.Run(context.Background()), ErrBusRunning)
	require.NoError(t, stop())

	// After Run returned the bus can be started again.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, bus.Run(ctx))
}

func TestNewRedisBus(t *testing.T) {
	bus := NewRedisBus(newClient(t), uniqueKeys(t), "public", nil)
	var got recorder
	bus.Subscribe(ChannelConfig, got.handler)
	require.NoError(t, bus.Publish(context.Background(), ChannelConfig, Event{Type: "published"}))
	require.Equal(t, 1, got.count())
}

func TestRedisBusRunStopsWhenClientCloses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := redis.Open(ctx, testRedisURL(t), nil)
	require.NoError(t, err)
	bus := newRedisBus(c, uniqueKeys(t), "closing", nil)

	done := make(chan error, 1)
	go func() { done <- bus.Run(ctx) }()
	select {
	case <-bus.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("bus did not subscribe")
	}
	c.Close()
	select {
	case err := <-done:
		require.ErrorIs(t, err, rueidis.ErrClosing)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the client closed")
	}
}

// flakyClient fails the first Receive calls to exercise the resubscribe loop.
type flakyClient struct {
	rueidis.Client
	failures atomic.Int32
}

func (c *flakyClient) Receive(ctx context.Context, subscribe rueidis.Completed, fn func(rueidis.PubSubMessage)) error {
	if c.failures.Add(-1) >= 0 {
		return errors.New("connection reset")
	}
	return c.Client.Receive(ctx, subscribe, fn)
}

func TestRedisBusResubscribesAfterFailures(t *testing.T) {
	keys := uniqueKeys(t)
	flaky := &flakyClient{Client: newClient(t)}
	flaky.failures.Store(3)
	logger, logs := logBuffer()
	sub := newRedisBus(flaky, keys, "sub", logger)
	sub.backoffMin = time.Millisecond
	sub.backoffMax = 4 * time.Millisecond
	startBus(t, sub)
	require.Contains(t, logs.String(), "resubscribing")

	var got recorder
	sub.Subscribe(ChannelRuntime, got.handler)
	pub := newRedisBus(newClient(t), keys, "pub", nil)
	require.NoError(t, pub.Publish(context.Background(), ChannelRuntime, Event{Type: "bump"}))
	require.Eventually(t, func() bool { return got.count() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func TestRedisBusRunReturnsOnCancelDuringBackoff(t *testing.T) {
	flaky := &flakyClient{Client: newClient(t)}
	flaky.failures.Store(1 << 30)
	bus := newRedisBus(flaky, uniqueKeys(t), "x", nil)
	bus.backoffMin = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bus.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRedisBusDropsWhenQueueFull(t *testing.T) {
	keys := uniqueKeys(t)
	logger, logs := logBuffer()
	sub := newRedisBus(newClient(t), keys, "slow", logger)
	sub.queueSize = 1
	release := make(chan struct{})
	var handled atomic.Int32
	sub.Subscribe(ChannelCatalog, func(context.Context, string, Event) {
		handled.Add(1)
		<-release
	})
	stop := startBus(t, sub)

	pub := newRedisBus(newClient(t), keys, "fast", nil)
	for range 50 {
		require.NoError(t, pub.Publish(context.Background(), ChannelCatalog, Event{Type: "invalidate"}))
	}
	require.Eventually(t, func() bool { return sub.dropped.Load() > 0 }, 5*time.Second, 10*time.Millisecond)
	close(release)
	require.NoError(t, stop())
	require.Contains(t, logs.String(), "queue full")
	require.Less(t, int(handled.Load()), 50)
}
