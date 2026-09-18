package spinneret

import (
	"context"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"
)

// Default retry tuning of unary calls.
const (
	DefaultMaxRetries     = 2
	DefaultInitialBackoff = 100 * time.Millisecond
	DefaultMaxBackoff     = 2 * time.Second
	DefaultMaxRetryAfter  = 5 * time.Second
)

// RetryPolicy controls the retries of unary calls.
//
// Transport failures and unavailable responses (except circuit_open and
// site_paused) are retried with jittered exponential backoff. Acquire and
// AcquireBatch are not idempotent: they are retried only when the failure
// provably happened before the request was sent (dial and DNS errors) or when
// the server itself answered unavailable. Ambiguous failures (per-call
// timeout, connection reset, a bare 502/503/504 from a load balancer) are
// returned to the caller.
type RetryPolicy struct {
	// MaxRetries is the number of retries after the first attempt; 0 disables retries.
	MaxRetries int
	// InitialBackoff is the base delay of the first retry (default 100ms).
	InitialBackoff time.Duration
	// MaxBackoff caps a single delay (default 2s).
	MaxBackoff time.Duration
	// MaxRetryAfter is the longest server retry hint that is waited for; an
	// error with a longer hint is returned instead (default 5s).
	MaxRetryAfter time.Duration
}

// DefaultRetryPolicy returns the policy used when [Options.Retry] is nil.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     DefaultMaxRetries,
		InitialBackoff: DefaultInitialBackoff,
		MaxBackoff:     DefaultMaxBackoff,
		MaxRetryAfter:  DefaultMaxRetryAfter,
	}
}

// NoRetry returns a policy that never retries.
func NoRetry() *RetryPolicy {
	p := DefaultRetryPolicy()
	p.MaxRetries = 0
	return &p
}

func (p RetryPolicy) normalized() (RetryPolicy, error) {
	if p.MaxRetries < 0 || p.InitialBackoff < 0 || p.MaxBackoff < 0 || p.MaxRetryAfter < 0 {
		return p, errInvalidOptions("retry policy values must not be negative")
	}
	if p.InitialBackoff == 0 {
		p.InitialBackoff = DefaultInitialBackoff
	}
	if p.MaxBackoff == 0 {
		p.MaxBackoff = DefaultMaxBackoff
	}
	if p.MaxBackoff < p.InitialBackoff {
		p.MaxBackoff = p.InitialBackoff
	}
	return p, nil
}

// delay returns the wait before retry number attempt (0-based) and false when
// the server hint is longer than MaxRetryAfter.
func (p RetryPolicy) delay(attempt int, retryAfter time.Duration, rnd func() float64) (time.Duration, bool) {
	d := backoff{initial: p.InitialBackoff, max: p.MaxBackoff}.delay(attempt, rnd)
	if retryAfter > 0 {
		if retryAfter > p.MaxRetryAfter {
			return 0, false
		}
		d = max(d, retryAfter)
	}
	return d, true
}

// backoff is exponential backoff with "equal jitter": half of the delay is
// fixed and half is random.
type backoff struct {
	initial time.Duration
	max     time.Duration
}

func (b backoff) delay(attempt int, rnd func() float64) time.Duration {
	base := b.initial
	for i := 0; i < attempt && base < b.max; i++ {
		if base > b.max/2 {
			base = b.max
			break
		}
		base *= 2
	}
	base = min(base, b.max)
	half := base / 2
	return half + time.Duration(float64(half)*rnd())
}

// jitter is the default random source of backoff delays.
func jitter() float64 {
	return rand.Float64() //nolint:gosec // backoff jitter does not need a cryptographic source
}

// shouldRetryCall decides whether a failed unary call may be retried.
func shouldRetryCall(e *Error, idempotent bool) bool {
	switch {
	case e.Transport():
		return !e.permanent && (e.preSend || idempotent)
	case e.Code != connect.CodeUnavailable:
		return false
	case e.Reason == ReasonCircuitOpen || e.Reason == ReasonSitePaused:
		return false
	case e.wire:
		return true
	default:
		// A bare 502/503/504 without a Connect body may come from a proxy
		// after the request reached the server.
		return idempotent
	}
}

// sleepContext waits for d or until ctx ends, reporting whether the full delay elapsed.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
