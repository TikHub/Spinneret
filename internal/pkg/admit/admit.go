// Package admit bounds the number of concurrent operations a process may have
// in flight against a shared external resource. Callers that cannot be
// admitted immediately wait in a bounded FIFO wait room for no longer than
// their own budget; callers beyond the wait room, and callers whose budget
// expires, are shed. The limit can be changed at runtime, which is how a
// fleet-wide budget is divided among a changing number of instances.
//
// A Gate is safe for concurrent use. A nil *Gate admits everything and costs
// one predictable branch per call, which is how admission control is disabled.
package admit

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Timer abstracts time.Timer so tests can control the wait deadline.
type Timer interface {
	// C returns the channel the deadline is delivered on.
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it did.
	Stop() bool
}

// Config configures a Gate. Zero values take the documented defaults.
type Config struct {
	// Limit is the initial number of weight units that may be in flight.
	// Values below 1 are raised to 1.
	Limit int
	// MaxWait caps how long a caller may wait for a permit. The effective
	// wait is min(MaxWait, the caller's own budget). Zero means DefaultMaxWait.
	MaxWait time.Duration
	// QueueDepth caps the number of parked callers. Zero derives it as
	// QueueFactor*Limit with a floor of MinQueueDepth, and it is re-derived
	// whenever SetLimit changes the limit.
	QueueDepth int
	// Now and NewTimer are replaced in tests. Zero values use the real clock.
	Now      func() time.Time
	NewTimer func(time.Duration) Timer
}

const (
	// DefaultMaxWait is the wait-room cap used when Config.MaxWait is zero.
	DefaultMaxWait = 50 * time.Millisecond
	// QueueFactor multiplies the limit to derive the wait-room depth.
	QueueFactor = 4
	// MinQueueDepth floors the derived wait-room depth.
	MinQueueDepth = 32
)

var (
	// ErrQueueFull is returned when no permit was free and the caller could
	// not be parked: it had no wait budget, or the wait room was full.
	ErrQueueFull = errors.New("admit: wait room full")
	// ErrNoBudget is returned when no permit was free and the caller passed no
	// wait budget, so it was shed without being parked. It wraps ErrQueueFull
	// because it is the same outcome — not admitted, nothing attempted — but it
	// is a distinct value so that a caller reporting sheds can tell "the callers
	// declined to wait" from "the wait room genuinely overflowed". The two say
	// very different things about how deep the overload is.
	ErrNoBudget = fmt.Errorf("admit: no wait budget: %w", ErrQueueFull)
	// ErrQueueTimeout is returned when a parked caller's wait elapsed.
	ErrQueueTimeout = errors.New("admit: wait timed out")
)

// Result describes how a caller was admitted.
type Result struct {
	// Waited is the time spent in the wait room (zero on the fast path and
	// on ErrQueueFull).
	Waited time.Duration
	// Queued is true when the caller was parked before being admitted.
	Queued bool
}

// Gate is a resizable, weighted, FIFO admission gate.
//
// A channel semaphore of fixed capacity cannot be resized and cannot tell
// "shed because the wait room is full" from "shed because the budget expired",
// so the state is guarded by a mutex instead. The critical section is a handful
// of field updates and, at most, one non-blocking send per freed permit.
type Gate struct {
	mu       sync.Mutex
	limit    int        // weight units that may be in flight
	depth    int        // derived wait-room cap
	cfgDepth int        // Config.QueueDepth, 0 when the depth is derived
	inFlight int        // weight units currently held
	queue    *list.List // FIFO of *waiter; container/list gives O(1) removal
	maxWait  time.Duration
	now      func() time.Time
	newTimer func(time.Duration) Timer
	// entered is a test-only hook signalled just before a caller parks. It is
	// nil in production, where it costs one branch on the slow path only.
	entered chan struct{}
}

// waiter is one parked caller.
type waiter struct {
	// want is the number of units the caller asked for, clamped to the limit in
	// force when it parked. It never changes, so a limit that dips and recovers
	// while the caller is parked still admits it with the weight its work costs.
	want int
	// weight is the number of units the caller is admitted with: want clamped to
	// the limit in force at the moment of admission. It is only written by
	// drainLocked, under the gate lock and before the handoff, so the admitted
	// caller reads the final value through the channel.
	weight int
	// ch carries the handoff. It is buffered with capacity 1 and receives at
	// most one send, so the handoff never blocks the gate lock.
	ch chan struct{}
	// claimed resolves the race between a handoff and an abandoning caller:
	// exactly one of them wins the compare-and-swap.
	claimed atomic.Bool
}

// New creates a Gate.
func New(cfg Config) *Gate {
	g := &Gate{
		limit:    max(cfg.Limit, 1),
		cfgDepth: max(cfg.QueueDepth, 0),
		queue:    list.New(),
		maxWait:  cfg.MaxWait,
		now:      cfg.Now,
		newTimer: cfg.NewTimer,
	}
	if g.maxWait <= 0 {
		g.maxWait = DefaultMaxWait
	}
	if g.now == nil {
		g.now = time.Now
	}
	if g.newTimer == nil {
		g.newTimer = newRealTimer
	}
	g.depth = g.deriveDepth()
	return g
}

// Acquire reserves weight units, waiting at most min(budget, MaxWait) for
// them. weight is clamped to [1, current limit] so a request larger than the
// limit can still be served. A budget of zero or less means the caller does
// not wait at all: it is admitted only if a permit is free right now, and is
// otherwise shed with ErrNoBudget rather than ErrQueueFull.
//
// On success the returned Permit must be released exactly once; the returned
// Permit is always safe to release, including after an error, so callers may
// unconditionally defer it.
func (g *Gate) Acquire(ctx context.Context, weight int, budget time.Duration) (Permit, Result, error) {
	if g == nil {
		return Permit{}, Result{}, nil
	}

	g.mu.Lock()
	w := min(max(weight, 1), g.limit)
	// The empty-queue term is the anti-barge rule: without it a fresh arrival
	// can step over parked callers indefinitely. The fit test subtracts rather
	// than adds because inFlight <= limit holds by construction, so the
	// subtraction cannot overflow while inFlight+w could.
	if g.queue.Len() == 0 && w <= g.limit-g.inFlight {
		g.inFlight += w
		g.mu.Unlock()
		return Permit{g: g, n: w}, Result{}, nil
	}
	// No budget means no timer, no channel and no allocation: shed in one
	// mutex round trip. This is the contract callers that pass wait_ms = 0
	// rely on, and they are the common case.
	if budget <= 0 {
		g.mu.Unlock()
		return Permit{}, Result{}, ErrNoBudget
	}
	if g.queue.Len() >= g.depth {
		g.mu.Unlock()
		return Permit{}, Result{}, ErrQueueFull
	}
	wt := &waiter{want: w, weight: w, ch: make(chan struct{}, 1)}
	el := g.queue.PushBack(wt)
	start := g.now()
	g.mu.Unlock()

	t := g.newTimer(min(budget, g.maxWait))
	defer t.Stop()
	g.signalEntered()

	select {
	case <-wt.ch:
		return Permit{g: g, n: wt.weight}, Result{Waited: g.since(start), Queued: true}, nil
	case <-t.C():
		if wt.claimed.CompareAndSwap(false, true) {
			g.abandon(el)
			return Permit{}, Result{Waited: g.since(start), Queued: true}, ErrQueueTimeout
		}
		// The handoff won the race, so the permit is already charged to this
		// caller: report success, merely late.
		<-wt.ch
		return Permit{g: g, n: wt.weight}, Result{Waited: g.since(start), Queued: true}, nil
	case <-ctx.Done():
		if wt.claimed.CompareAndSwap(false, true) {
			g.abandon(el)
			return Permit{}, Result{Waited: g.since(start), Queued: true}, ctx.Err()
		}
		// The handoff won the race but the caller is gone, so the permit must
		// be given back here or it would be lost for the life of the process.
		<-wt.ch
		p := Permit{g: g, n: wt.weight}
		p.Release()
		return Permit{}, Result{Waited: g.since(start), Queued: true}, ctx.Err()
	}
}

// SetLimit changes the limit and wakes waiters that now fit. Values below 1
// are raised to 1.
func (g *Gate) SetLimit(n int) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.limit = max(n, 1)
	g.depth = g.deriveDepth()
	// Callers already holding permits are never revoked; lowering the limit
	// only stops new admissions until enough weight is returned.
	g.drainLocked()
	g.mu.Unlock()
}

// Limit reports the current limit. A nil *Gate reports zero.
func (g *Gate) Limit() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

// InFlight reports the weight units currently held. A nil *Gate reports zero.
func (g *Gate) InFlight() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight
}

// Queued reports the number of parked callers. A nil *Gate reports zero.
func (g *Gate) Queued() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.queue.Len()
}

// Permit is a held reservation. Its zero value releases nothing.
type Permit struct {
	g *Gate
	n int
}

// Release returns the reserved units. It is idempotent when called from a
// single goroutine — which is what a deferred release is — but a Permit must
// not be released concurrently with itself: the two calls would both see a
// non-nil gate and return the units twice, shrinking the effective capacity for
// the life of the process. Ownership of a Permit is single-goroutine, like the
// ownership of the work it authorises.
func (p *Permit) Release() {
	if p == nil || p.g == nil {
		return
	}
	g := p.g
	p.g = nil
	g.mu.Lock()
	g.inFlight -= p.n
	g.drainLocked()
	g.mu.Unlock()
}

// abandon removes a waiter that will never be admitted and re-runs the drain,
// because the departing waiter may have been standing in front of a smaller
// one that now fits.
func (g *Gate) abandon(el *list.Element) {
	g.mu.Lock()
	// The element may already be out of the list: drainLocked pops a waiter
	// before it claims it, so a lost claim leaves nothing to remove. Removing
	// an element that is no longer in a list is a no-op.
	g.queue.Remove(el)
	g.drainLocked()
	g.mu.Unlock()
}

// drainLocked hands permits to the waiters at the head of the queue for as
// long as they fit. The handoff is direct, so there is no window in which an
// arriving caller can steal capacity a parked caller was already promised.
//
// It must be called with g.mu held.
func (g *Gate) drainLocked() {
	for {
		el := g.queue.Front()
		if el == nil {
			return
		}
		wt := el.Value.(*waiter)
		// A caller parked before SetLimit lowered the limit may ask for more
		// than the limit now allows. Re-clamping keeps it servable instead of
		// letting it block the queue until its budget expires. The clamp is
		// re-derived from want on every drain, and written only on the admitting
		// branch, so a limit that recovers before the caller is admitted charges
		// it for the work it actually does instead of the shrunken figure.
		// The fit test subtracts because inFlight <= limit holds by construction.
		n := min(wt.want, g.limit)
		if n > g.limit-g.inFlight {
			return
		}
		wt.weight = n
		g.queue.Remove(el)
		if !wt.claimed.CompareAndSwap(false, true) {
			// The caller already gave up. Drop it and consider the next one.
			continue
		}
		g.inFlight += wt.weight
		wt.ch <- struct{}{}
	}
}

// deriveDepth returns the wait-room cap: the configured depth when one was
// given, otherwise QueueFactor*limit floored at MinQueueDepth. The
// multiplication is guarded so an absurd limit cannot overflow.
func (g *Gate) deriveDepth() int {
	if g.cfgDepth > 0 {
		return g.cfgDepth
	}
	if g.limit > math.MaxInt/QueueFactor {
		return math.MaxInt
	}
	return max(QueueFactor*g.limit, MinQueueDepth)
}

// since returns the time elapsed since start on the gate's clock.
func (g *Gate) since(start time.Time) time.Duration {
	return g.now().Sub(start)
}

// signalEntered notifies the test hook, when one is installed, that a caller
// is parked and about to wait.
func (g *Gate) signalEntered() {
	if g.entered == nil {
		return
	}
	g.entered <- struct{}{}
}

// realTimer adapts time.Timer to Timer.
type realTimer struct {
	t *time.Timer
}

func (r realTimer) C() <-chan time.Time { return r.t.C }

func (r realTimer) Stop() bool { return r.t.Stop() }

// newRealTimer is the default Config.NewTimer.
func newRealTimer(d time.Duration) Timer {
	return realTimer{t: time.NewTimer(d)}
}
