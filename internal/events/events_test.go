package events

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recorder collects delivered events.
type recorder struct {
	mu     sync.Mutex
	events []delivered
}

type delivered struct {
	channel string
	ev      Event
}

func (r *recorder) handler(_ context.Context, channel string, ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, delivered{channel: channel, ev: ev})
}

func (r *recorder) snapshot() []delivered {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]delivered(nil), r.events...)
}

func (r *recorder) count() int {
	return len(r.snapshot())
}

func TestNamespaceChannel(t *testing.T) {
	require.Equal(t, "ns:ns_0192", NamespaceChannel("ns_0192"))
}

func TestPrepare(t *testing.T) {
	fixed := time.Date(2025, 9, 16, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	now := func() time.Time { return fixed }
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		channel string
		ev      Event
		wantErr error
		wantAt  time.Time
	}{
		{name: "empty channel", channel: "", wantErr: ErrInvalidChannel},
		{name: "wildcard channel", channel: ChannelAll, wantErr: ErrInvalidChannel},
		{name: "oversized channel", channel: strings.Repeat("c", maxChannelLen+1), wantErr: ErrInvalidChannel},
		{name: "invalid data", channel: ChannelCatalog, ev: Event{Data: json.RawMessage(`{"ns":`)}, wantErr: ErrInvalidData},
		{name: "zero time filled in UTC", channel: ChannelCatalog, ev: Event{Type: "invalidate"}, wantAt: fixed.UTC()},
		{name: "explicit time kept", channel: ChannelTokens, ev: Event{At: at, Data: json.RawMessage(`{"token_id":"tok_1"}`)}, wantAt: at},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prepare(tt.channel, tt.ev, now)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.True(t, tt.wantAt.Equal(got.At))
			require.Equal(t, time.UTC, got.At.Location())
			require.Equal(t, tt.ev.Data, got.Data)
		})
	}
}

func TestMemoryBusDelivery(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var catalog, all, tokens recorder
	unsubCatalog := bus.Subscribe(ChannelCatalog, catalog.handler)
	unsubAll := bus.Subscribe(ChannelAll, all.handler)
	bus.Subscribe(ChannelTokens, tokens.handler)

	data := json.RawMessage(`{"ns":"ns_1"}`)
	require.NoError(t, bus.Publish(ctx, ChannelCatalog, Event{Type: "invalidate", NamespaceID: "ns_1", Data: data}))
	require.NoError(t, bus.Publish(ctx, NamespaceChannel("ns_1"), Event{Type: TypeAlert}))

	// Delivery is synchronous.
	got := catalog.snapshot()
	require.Len(t, got, 1)
	require.Equal(t, ChannelCatalog, got[0].channel)
	require.Equal(t, "invalidate", got[0].ev.Type)
	require.Equal(t, "ns_1", got[0].ev.NamespaceID)
	require.JSONEq(t, string(data), string(got[0].ev.Data))
	require.False(t, got[0].ev.At.IsZero())

	gotAll := all.snapshot()
	require.Len(t, gotAll, 2)
	require.Equal(t, ChannelCatalog, gotAll[0].channel)
	require.Equal(t, "ns:ns_1", gotAll[1].channel)
	require.Zero(t, tokens.count())

	// Unsubscribe is idempotent and stops delivery.
	unsubCatalog()
	unsubCatalog()
	unsubAll()
	require.NoError(t, bus.Publish(ctx, ChannelCatalog, Event{Type: "invalidate"}))
	require.Equal(t, 1, catalog.count())
	require.Equal(t, 2, all.count())

	require.ErrorIs(t, bus.Publish(ctx, "", Event{}), ErrInvalidChannel)
	require.ErrorIs(t, bus.Publish(ctx, ChannelAll, Event{}), ErrInvalidChannel)
}

func TestSubscribeNoop(t *testing.T) {
	bus := NewMemoryBus()
	unsub := bus.Subscribe("", func(context.Context, string, Event) {})
	unsub()
	unsub = bus.Subscribe(ChannelCatalog, nil)
	unsub()
	require.NoError(t, bus.Publish(context.Background(), ChannelCatalog, Event{}))
}

func TestMultipleSubscribersOrderAndPartialUnsubscribe(t *testing.T) {
	bus := NewMemoryBus()
	var mu sync.Mutex
	var order []string
	mk := func(name string) Handler {
		return func(context.Context, string, Event) {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, name)
		}
	}
	bus.Subscribe(ChannelConfig, mk("a"))
	unsubB := bus.Subscribe(ChannelConfig, mk("b"))
	bus.Subscribe(ChannelAll, mk("all"))
	bus.Subscribe(ChannelConfig, mk("c"))

	require.NoError(t, bus.Publish(context.Background(), ChannelConfig, Event{}))
	unsubB()
	require.NoError(t, bus.Publish(context.Background(), ChannelConfig, Event{}))
	require.Equal(t, []string{"a", "b", "c", "all", "a", "c", "all"}, order)
}

func TestHandlerPanicIsRecovered(t *testing.T) {
	var logs bytes.Buffer
	d := newDispatcher(slog.New(slog.NewTextHandler(&logs, nil)))
	var after recorder
	d.Subscribe(ChannelRuntime, func(context.Context, string, Event) { panic("boom") })
	d.Subscribe(ChannelRuntime, after.handler)

	require.NotPanics(t, func() {
		d.dispatch(context.Background(), ChannelRuntime, Event{Type: "bump"})
	})
	require.Equal(t, 1, after.count(), "later handlers still run")
	require.Contains(t, logs.String(), "event handler panicked")
	require.Contains(t, logs.String(), "boom")
}

func TestHandlersMaySubscribeDuringDispatch(t *testing.T) {
	bus := NewMemoryBus()
	var late recorder
	var once sync.Once
	bus.Subscribe(ChannelCatalog, func(context.Context, string, Event) {
		once.Do(func() { bus.Subscribe(ChannelCatalog, late.handler) })
	})
	require.NoError(t, bus.Publish(context.Background(), ChannelCatalog, Event{}))
	require.Zero(t, late.count(), "a subscription added during dispatch applies to later events")
	require.NoError(t, bus.Publish(context.Background(), ChannelCatalog, Event{}))
	require.Equal(t, 1, late.count())
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	bus := NewMemoryBus()
	var total recorder
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				unsub := bus.Subscribe(ChannelAll, func(context.Context, string, Event) {})
				require.NoError(t, bus.Publish(context.Background(), ChannelTokens, Event{}))
				unsub()
			}
		})
	}
	bus.Subscribe(ChannelTokens, total.handler)
	wg.Wait()
	require.LessOrEqual(t, total.count(), 400)
}

func TestMemoryBusRun(t *testing.T) {
	bus := NewMemoryBus()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bus.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}
