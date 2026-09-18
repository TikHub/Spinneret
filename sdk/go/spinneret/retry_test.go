package spinneret

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoffDelay(t *testing.T) {
	b := backoff{initial: 100 * time.Millisecond, max: time.Second}
	zero := func() float64 { return 0 }
	one := func() float64 { return 1 }
	require.Equal(t, 50*time.Millisecond, b.delay(0, zero))
	require.Equal(t, 100*time.Millisecond, b.delay(0, one))
	require.Equal(t, 200*time.Millisecond, b.delay(2, zero))
	require.Equal(t, 500*time.Millisecond, b.delay(10, zero), "capped at max")
	require.Equal(t, time.Second, b.delay(1000, one))

	huge := backoff{initial: time.Duration(math.MaxInt64 / 3), max: time.Duration(math.MaxInt64)}
	require.Positive(t, huge.delay(5, one), "no overflow")
	require.NotZero(t, jitter()+1)
}

func TestRetryPolicyDelay(t *testing.T) {
	p := DefaultRetryPolicy()
	half := func() float64 { return 0.5 }
	d, ok := p.delay(0, 0, half)
	require.True(t, ok)
	require.Equal(t, 75*time.Millisecond, d)
	d, ok = p.delay(0, 3*time.Second, half)
	require.True(t, ok)
	require.Equal(t, 3*time.Second, d, "server hint wins when longer")
	_, ok = p.delay(0, 6*time.Second, half)
	require.False(t, ok, "hint above MaxRetryAfter")
	require.Equal(t, 0, NoRetry().MaxRetries)
}

func TestSleepContext(t *testing.T) {
	require.True(t, sleepContext(context.Background(), 0))
	require.True(t, sleepContext(context.Background(), time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, sleepContext(ctx, 0))
	require.False(t, sleepContext(ctx, time.Hour))
}
