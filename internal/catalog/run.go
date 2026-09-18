package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/events"
)

// Run keeps the catalog fresh until ctx is done: it loads every namespace when
// nothing was loaded yet (retrying with backoff until a full load succeeds),
// reloads namespaces invalidated by peers (debounced per namespace) and
// reloads everything periodically as a safety net against lost events. It
// returns nil when ctx is done, after in-flight full reloads finished.
func (s *Store) Run(ctx context.Context) error {
	l := &runLoop{
		store:    s,
		queue:    make(chan string, eventQueueSize),
		fullDone: make(chan error, 1),
		due:      map[string]time.Time{},
		backoff:  s.initialRetryBackoff(),
	}
	unsubscribe := func() {}
	if s.bus != nil {
		unsubscribe = s.bus.Subscribe(events.ChannelCatalog, l.onEvent)
	}
	defer unsubscribe()
	defer l.wg.Wait()

	if !s.loaded.Load() {
		l.startFull(ctx)
	}
	ticker := time.NewTicker(s.fullInterval)
	defer ticker.Stop()
	l.debounce = stoppedTimer()
	defer l.debounce.Stop()
	l.retry = stoppedTimer()
	defer l.retry.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case namespaceID := <-l.queue:
			l.schedule(namespaceID)
			l.fullAfterOverflow(ctx)
		case <-l.debounce.C:
			l.fireDue(ctx)
		case <-ticker.C:
			l.fullAfterOverflow(ctx)
			l.startFull(ctx)
		case err := <-l.fullDone:
			l.onFullDone(ctx, err)
		case <-l.retry.C:
			l.startFull(ctx)
		}
	}
}

// runLoop is the state of one Run call. Its fields other than overflow,
// fullRunning, fullDone, queue and wg are only used by the Run goroutine.
type runLoop struct {
	store *Store

	// queue receives invalidated namespace IDs from the bus handler; overflow
	// records that an invalidation was dropped because the queue was full.
	queue    chan string
	overflow atomic.Bool

	// due maps namespaces to the time their debounced reload starts.
	due      map[string]time.Time
	debounce *time.Timer

	// fullRunning guards against concurrent full reloads; fullDone receives
	// the result of each one.
	fullRunning atomic.Bool
	fullDone    chan error
	wg          sync.WaitGroup

	// retry schedules another full reload after a failure while nothing was
	// loaded yet; backoff is the next delay.
	retry   *time.Timer
	backoff time.Duration
}

func stoppedTimer() *time.Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return t
}

// onEvent is the bus handler: it queues the namespace without blocking.
func (l *runLoop) onEvent(_ context.Context, _ string, ev events.Event) {
	namespaceID, ok := l.store.parseInvalidation(ev)
	if !ok {
		return
	}
	select {
	case l.queue <- namespaceID:
	default:
		l.overflow.Store(true)
	}
}

// schedule debounces a reload of the namespace.
func (l *runLoop) schedule(namespaceID string) {
	now := l.store.now()
	if _, pending := l.due[namespaceID]; !pending {
		l.due[namespaceID] = now.Add(l.store.debounce)
	}
	l.debounce.Reset(earliest(l.due, now))
}

// fireDue starts the reloads whose debounce delay elapsed.
func (l *runLoop) fireDue(ctx context.Context) {
	now := l.store.now()
	for namespaceID, at := range l.due {
		if !at.After(now) {
			delete(l.due, namespaceID)
			l.store.reloadAsync(ctx, namespaceID)
		}
	}
	if len(l.due) > 0 {
		l.debounce.Reset(earliest(l.due, now))
	}
}

// startFull starts a full reload in the background unless one is running and
// reports whether it started one.
func (l *runLoop) startFull(ctx context.Context) bool {
	if !l.fullRunning.CompareAndSwap(false, true) {
		return false
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		err := l.store.ReloadAll(ctx)
		if err != nil && ctx.Err() == nil {
			l.store.logger.Error("catalog: full reload failed", slog.Any("error", err))
		}
		l.fullRunning.Store(false)
		select {
		case l.fullDone <- err:
		default:
			// The loop has not consumed the previous result yet (or has
			// stopped); the periodic reload covers a missed retry.
		}
	}()
	return true
}

// fullAfterOverflow starts a full reload when invalidations were dropped. When
// a full reload is already running (it may have listed namespaces before the
// drop) the flag is kept, so the next event, result or tick retries.
func (l *runLoop) fullAfterOverflow(ctx context.Context) {
	if l.overflow.Swap(false) && !l.startFull(ctx) {
		l.overflow.Store(true)
	}
}

// onFullDone schedules a retry with exponential backoff while no full reload
// has succeeded yet, and handles invalidations dropped during the reload.
func (l *runLoop) onFullDone(ctx context.Context, err error) {
	switch {
	case err == nil:
		l.backoff = l.store.initialRetryBackoff()
	case ctx.Err() == nil && !l.store.loaded.Load():
		l.retry.Reset(l.backoff)
		l.backoff = min(2*l.backoff, max(l.store.fullInterval, l.backoff))
	}
	l.fullAfterOverflow(ctx)
}

// initialRetryBackoff returns the first retry delay (DefaultRetryBackoff when
// the configured value is not positive).
func (s *Store) initialRetryBackoff() time.Duration {
	if s.retryBackoff <= 0 {
		return DefaultRetryBackoff
	}
	return s.retryBackoff
}

// reloadAsync schedules a reload and logs its failure without blocking.
func (s *Store) reloadAsync(ctx context.Context, namespaceID string) {
	f := s.request(ctx, namespaceID)
	go func() {
		select {
		case <-f.done:
			if f.err != nil {
				s.logger.Warn("catalog: reload after invalidation failed",
					slog.String("namespace_id", namespaceID), slog.Any("error", f.err))
			}
		case <-ctx.Done():
		}
	}()
}

// parseInvalidation extracts the namespace of a catalog event, ignoring events
// published by this store (they were already applied by Invalidate).
func (s *Store) parseInvalidation(ev events.Event) (string, bool) {
	var inv invalidation
	if len(ev.Data) > 0 {
		if err := json.Unmarshal(ev.Data, &inv); err != nil {
			s.logger.Warn("catalog: ignoring malformed invalidation event", slog.Any("error", err))
			return "", false
		}
	}
	if inv.Origin != "" && inv.Origin == s.origin {
		return "", false
	}
	if inv.NS == "" {
		inv.NS = ev.NamespaceID
	}
	return inv.NS, inv.NS != ""
}

func earliest(due map[string]time.Time, now time.Time) time.Duration {
	first := time.Time{}
	for _, at := range due {
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	if d := first.Sub(now); d > 0 {
		return d
	}
	return 0
}
