package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

const (
	// inboundQueueSize bounds messages received from Redis but not yet handled.
	inboundQueueSize = 4096
	minBackoff       = 100 * time.Millisecond
	maxBackoff       = 5 * time.Second
	// healthySubscription resets the reconnect backoff when a subscription lasted this long.
	healthySubscription = 30 * time.Second
)

// ErrBusRunning is returned by Run when another Run call is already active on
// the same bus; a second subscription would deliver every peer event twice.
var ErrBusRunning = errors.New("events: bus is already running")

// envelope is the wire format of a bus message.
type envelope struct {
	Origin  string `json:"origin"`
	Channel string `json:"channel"`
	Event   Event  `json:"event"`
}

// redisBus is a Bus backed by Redis Pub/Sub.
type redisBus struct {
	*dispatcher
	client     rueidis.Client
	keys       redis.Keys
	instanceID string
	logger     *slog.Logger
	prefix     string

	running    atomic.Bool
	dropped    atomic.Int64
	readyOnce  sync.Once
	ready      chan struct{} // closed once the first PSUBSCRIBE is confirmed
	queueSize  int
	backoffMin time.Duration
	backoffMax time.Duration
}

// NewRedisBus returns a Bus that delivers events locally and to every peer
// subscribed to keys.ChannelPattern(). Messages whose origin equals
// instanceID are ignored by Run, because Publish already delivered them
// locally; every instance must therefore use a unique instanceID (a random one
// is generated when empty). A nil logger discards logs.
func NewRedisBus(client rueidis.Client, keys redis.Keys, instanceID string, logger *slog.Logger) Bus {
	return newRedisBus(client, keys, instanceID, logger)
}

func newRedisBus(client rueidis.Client, keys redis.Keys, instanceID string, logger *slog.Logger) *redisBus {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if instanceID == "" {
		instanceID = randomInstanceID()
	}
	logger = logger.With(slog.String("component", "event_bus"))
	return &redisBus{
		dispatcher: newDispatcher(logger),
		client:     client,
		keys:       keys,
		instanceID: instanceID,
		logger:     logger,
		prefix:     keys.Channel(""),
		ready:      make(chan struct{}),
		queueSize:  inboundQueueSize,
		backoffMin: minBackoff,
		backoffMax: maxBackoff,
	}
}

// Publish delivers ev locally, then PUBLISHes it to "P:ch:<channel>".
func (b *redisBus) Publish(ctx context.Context, channel string, ev Event) error {
	ev, err := prepare(channel, ev, time.Now)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope{Origin: b.instanceID, Channel: channel, Event: ev})
	if err != nil {
		return fmt.Errorf("events: encode %s event: %w", channel, err)
	}
	b.dispatch(ctx, channel, ev)
	cmd := b.client.B().Publish().Channel(b.keys.Channel(channel)).Message(string(payload)).Build()
	if err := b.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("events: publish %s: %w", channel, err)
	}
	return nil
}

// Run subscribes to every bus channel and dispatches peer events until ctx is
// done. Subscription failures are retried with exponential backoff. Messages
// are handed to a single consumer goroutine through a bounded queue so that
// slow handlers never stall the Redis connection; when the queue is full the
// message is dropped and logged. A concurrent second call returns ErrBusRunning.
func (b *redisBus) Run(ctx context.Context) error {
	if !b.running.CompareAndSwap(false, true) {
		return ErrBusRunning
	}
	defer b.running.Store(false)

	queue := make(chan rueidis.PubSubMessage, b.queueSize)
	var wg sync.WaitGroup
	wg.Go(func() {
		for msg := range queue {
			if ctx.Err() == nil {
				b.handle(ctx, msg)
			}
		}
	})
	defer func() {
		close(queue)
		wg.Wait()
	}()

	subCtx := rueidis.WithOnSubscriptionHook(ctx, func(s rueidis.PubSubSubscription) {
		if s.Kind == "psubscribe" {
			b.readyOnce.Do(func() { close(b.ready) })
		}
	})
	onMessage := func(msg rueidis.PubSubMessage) {
		select {
		case queue <- msg:
		default:
			n := b.dropped.Add(1)
			b.logger.Warn("event bus queue full; dropping peer event",
				slog.String("channel", msg.Channel), slog.Int64("dropped_total", n))
		}
	}

	backoff := b.backoffMin
	for {
		started := time.Now()
		cmd := b.client.B().Psubscribe().Pattern(b.keys.ChannelPattern()).Build()
		err := b.client.Receive(subCtx, cmd, onMessage)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, rueidis.ErrClosing) {
			return fmt.Errorf("events: redis client closed: %w", err)
		}
		if time.Since(started) >= healthySubscription {
			backoff = b.backoffMin
		}
		b.logger.Warn("event bus subscription ended; resubscribing",
			slog.Any("error", err), slog.Duration("backoff", backoff))
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, b.backoffMax)
	}
}

// handle decodes one peer message and dispatches it locally.
func (b *redisBus) handle(ctx context.Context, msg rueidis.PubSubMessage) {
	channel, ok := strings.CutPrefix(msg.Channel, b.prefix)
	if !ok || channel == "" || channel == ChannelAll {
		return
	}
	var env envelope
	if err := json.Unmarshal([]byte(msg.Message), &env); err != nil {
		b.logger.Warn("event bus ignored malformed message", slog.String("channel", channel), slog.Any("error", err))
		return
	}
	if env.Origin == b.instanceID {
		return
	}
	if env.Channel != channel {
		b.logger.Warn("event bus ignored message with mismatched channel",
			slog.String("channel", channel), slog.String("envelope_channel", env.Channel))
		return
	}
	b.dispatch(ctx, channel, env.Event)
}

// randomInstanceID returns "bus-" + 12 random hex characters.
func randomInstanceID() string {
	var buf [6]byte
	_, _ = rand.Read(buf[:]) // crypto/rand.Read never returns an error.
	return "bus-" + hex.EncodeToString(buf[:])
}
