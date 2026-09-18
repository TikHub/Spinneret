package breaker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// notifyTick is the resolution of the rate-limited evaluation loop.
const notifyTick = 100 * time.Millisecond

// maxDeferred bounds the groups waiting for their rate-limit slot.
const maxDeferred = 100_000

// NotifyRisk asks for a prompt evaluation of an endpoint group after the
// worker observed a risk outcome. It never blocks: repeated notifications of a
// group that is already queued are merged, and notifications are dropped when
// the queue is full (the periodic sweep still evaluates the group).
func (s *Service) NotifyRisk(siteKey, egKey int64) {
	ref := groupRef{SiteKey: siteKey, GroupKey: egKey}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if _, queued := s.pending[ref]; queued {
		return
	}
	select {
	case s.notify <- ref:
		s.pending[ref] = struct{}{}
	default:
	}
}

// Run evaluates notified groups until ctx is canceled, at most once per
// NotifyMinInterval per group and with at most Concurrency evaluations in
// flight. It returns nil after in-flight evaluations finished.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(notifyTick)
	defer ticker.Stop()

	var wg sync.WaitGroup
	defer wg.Wait()
	sem := make(chan struct{}, s.cfg.Concurrency)
	limiter := newNotifyLimiter(s.cfg.NotifyMinInterval)

	// start launches an evaluation when a slot is free; it reports false when
	// all slots are busy.
	start := func(ref groupRef) bool {
		select {
		case sem <- struct{}{}:
		default:
			return false
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.evaluateRef(ctx, ref)
		}()
		return true
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case ref := <-s.notify:
			s.pendingMu.Lock()
			delete(s.pending, ref)
			s.pendingMu.Unlock()
			limiter.offer(ref, s.now(), start)
		case <-ticker.C:
			limiter.tick(s.now(), start)
		}
	}
}

// notifyLimiter enforces the per-group minimum interval between notified
// evaluations. It is used by a single goroutine (Run) and is not safe for
// concurrent use.
type notifyLimiter struct {
	interval time.Duration
	// last is the start time of the latest evaluation of a group, kept while
	// it still limits the group.
	last map[groupRef]time.Time
	// deferred maps groups waiting for their slot to the earliest start time.
	deferred map[groupRef]time.Time
}

func newNotifyLimiter(interval time.Duration) *notifyLimiter {
	return &notifyLimiter{
		interval: interval,
		last:     make(map[groupRef]time.Time),
		deferred: make(map[groupRef]time.Time),
	}
}

// offer handles one notification: the group starts at once when its interval
// elapsed and start accepts it; otherwise it is deferred (once) until tick
// starts it. A group that is already deferred is left to tick, so a burst of
// notifications never starts a group twice within the interval.
func (l *notifyLimiter) offer(ref groupRef, now time.Time, start func(groupRef) bool) {
	if _, waiting := l.deferred[ref]; waiting {
		return
	}
	due := l.last[ref].Add(l.interval)
	if !now.Before(due) && start(ref) {
		l.last[ref] = now
		return
	}
	if len(l.deferred) < maxDeferred {
		l.deferred[ref] = due
	}
}

// tick starts deferred groups whose time came while start accepts them and
// forgets rate-limit entries that no longer limit anything.
func (l *notifyLimiter) tick(now time.Time, start func(groupRef) bool) {
	for ref, due := range l.deferred {
		if now.Before(due) {
			continue
		}
		if !start(ref) {
			break
		}
		l.last[ref] = now
		delete(l.deferred, ref)
	}
	for ref, at := range l.last {
		if now.Sub(at) < l.interval {
			continue
		}
		if _, waiting := l.deferred[ref]; !waiting {
			delete(l.last, ref)
		}
	}
}

// evaluateRef resolves a notified group through the catalog and evaluates it.
func (s *Service) evaluateRef(ctx context.Context, ref groupRef) {
	site, ns, ok := s.cat.SiteByKey(ref.SiteKey)
	if !ok {
		return
	}
	g, ok := site.GroupsByKey[ref.GroupKey]
	if !ok {
		return
	}
	if _, err := s.evaluateGroup(ctx, ns, site, g); err != nil && ctx.Err() == nil {
		s.logger.Error("breaker evaluation failed",
			slog.String("site", site.Name), slog.String("endpoint_group_id", g.ID), slog.Any("error", err))
	}
}
