package events

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// subscription is one registered handler.
type subscription struct {
	id uint64
	h  Handler
}

// dispatcher fans events out to in-process subscribers. Subscriber lists are
// copy-on-write: dispatch reads an immutable slice snapshot and never holds
// the lock while handlers run, so handlers may (un)subscribe or publish.
type dispatcher struct {
	logger *slog.Logger

	mu     sync.RWMutex
	nextID uint64
	subs   map[string][]subscription
}

func newDispatcher(logger *slog.Logger) *dispatcher {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &dispatcher{logger: logger, subs: make(map[string][]subscription)}
}

// Subscribe registers h for channel. An empty channel or a nil handler yields
// a no-op subscription.
func (d *dispatcher) Subscribe(channel string, h Handler) func() {
	if channel == "" || h == nil {
		return func() {}
	}
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	cur := d.subs[channel]
	next := make([]subscription, len(cur), len(cur)+1)
	copy(next, cur)
	d.subs[channel] = append(next, subscription{id: id, h: h})
	d.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { d.remove(channel, id) })
	}
}

func (d *dispatcher) remove(channel string, id uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cur := d.subs[channel]
	next := make([]subscription, 0, len(cur))
	for _, s := range cur {
		if s.id != id {
			next = append(next, s)
		}
	}
	if len(next) == 0 {
		delete(d.subs, channel)
		return
	}
	d.subs[channel] = next
}

// dispatch delivers ev to the subscribers of channel, then to wildcard subscribers.
func (d *dispatcher) dispatch(ctx context.Context, channel string, ev Event) {
	d.mu.RLock()
	direct := d.subs[channel]
	wildcard := d.subs[ChannelAll]
	d.mu.RUnlock()

	for _, s := range direct {
		d.call(ctx, s.h, channel, ev)
	}
	for _, s := range wildcard {
		d.call(ctx, s.h, channel, ev)
	}
}

// call runs one handler, recovering and logging panics.
func (d *dispatcher) call(ctx context.Context, h Handler, channel string, ev Event) {
	defer func() {
		if r := recover(); r != nil {
			d.logger.Error("event handler panicked",
				slog.String("channel", channel),
				slog.String("event_type", ev.Type),
				slog.Any("panic", r),
				slog.String("stack", string(debug.Stack())))
		}
	}()
	h(ctx, channel, ev)
}
