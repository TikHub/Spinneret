package events

import (
	"context"
	"time"
)

// memoryBus is a process-local Bus.
type memoryBus struct {
	*dispatcher
}

// NewMemoryBus returns a Bus that only delivers to subscribers in this process
// (single-instance deployments and tests).
func NewMemoryBus() Bus {
	return &memoryBus{dispatcher: newDispatcher(nil)}
}

// Publish validates the event and delivers it to local subscribers.
func (b *memoryBus) Publish(ctx context.Context, channel string, ev Event) error {
	ev, err := prepare(channel, ev, time.Now)
	if err != nil {
		return err
	}
	b.dispatch(ctx, channel, ev)
	return nil
}

// Run blocks until ctx is done; a memory bus has nothing to receive.
func (b *memoryBus) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
