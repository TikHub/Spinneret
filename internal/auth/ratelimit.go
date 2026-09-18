package auth

import (
	"math"
	"sync"
	"time"
)

const (
	// limiterIdleTTL is how long an unused bucket is kept.
	limiterIdleTTL = 5 * time.Minute
	// limiterSweepInterval is the minimum time between idle-bucket sweeps.
	limiterSweepInterval = time.Minute
	// maxLimiterBuckets bounds memory; when reached, idle buckets are swept
	// immediately and, if still full, new keys are not limited.
	maxLimiterBuckets = 100_000
)

// bucket is a token bucket with capacity equal to its rate (one second of burst).
type bucket struct {
	rate   float64
	tokens float64
	last   time.Time
}

// rateLimiter is an in-process token-bucket limiter keyed by token ID
// (spec §3.3: per-token rate_limit_rps, per instance).
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket)}
}

// allow consumes one token for key at rate rps. It returns false and the
// delay until a token becomes available when the bucket is empty. rps <= 0
// means unlimited.
func (l *rateLimiter) allow(key string, rps int, now time.Time) (bool, time.Duration) {
	if rps <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sinceSweep := now.Sub(l.lastSweep)
	if sinceSweep >= limiterSweepInterval || (len(l.buckets) >= maxLimiterBuckets && sinceSweep >= time.Second) {
		l.sweep(now)
	}
	rate := float64(rps)
	b, ok := l.buckets[key]
	if !ok || b.rate != rate {
		if !ok && len(l.buckets) >= maxLimiterBuckets {
			return true, 0
		}
		b = &bucket{rate: rate, tokens: rate, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(b.rate, b.tokens+elapsed.Seconds()*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration(math.Ceil((1 - b.tokens) / b.rate * float64(time.Second)))
	return false, max(wait, time.Millisecond)
}

// forget drops the bucket of key.
func (l *rateLimiter) forget(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}

// sweep removes idle buckets; the caller holds l.mu.
func (l *rateLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) >= limiterIdleTTL {
			delete(l.buckets, k)
		}
	}
}
