package notify

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailingChannels(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	f := newFailingChannels()

	require.False(t, f.failing("nch_1", now))
	f.observe("nch_1", false, now)
	require.True(t, f.failing("nch_1", now.Add(failingWindow-time.Second)))
	require.False(t, f.failing("nch_1", now.Add(failingWindow)), "the window expires")
	require.False(t, f.failing("nch_1", now.Add(failingWindow-time.Second)), "expired entries are removed")

	f.observe("nch_1", false, now)
	f.observe("nch_1", true, now)
	require.False(t, f.failing("nch_1", now), "a success clears the failure")

	// The tracked set is bounded: expired entries are pruned first, then the set is reset.
	for i := range maxFailingChannels {
		f.observe("old_"+strconv.Itoa(i), false, now)
	}
	later := now.Add(failingWindow)
	f.observe("new", false, later)
	require.True(t, f.failing("new", later))
	f.mu.Lock()
	require.Len(t, f.until, 1)
	f.mu.Unlock()
	// "new" plus 9 999 live entries fill the set; the next insertion resets it.
	for i := range maxFailingChannels - 1 {
		f.observe("fresh_"+strconv.Itoa(i), false, later)
	}
	f.mu.Lock()
	require.Len(t, f.until, maxFailingChannels)
	f.mu.Unlock()
	f.observe("overflow", false, later)
	f.mu.Lock()
	require.Len(t, f.until, 1)
	f.mu.Unlock()
	require.True(t, f.failing("overflow", later))
}

// A channel whose delivery failed gets a single attempt for the following
// deliveries, so dead endpoints do not block the shared workers with retries.
func TestFailingChannelSkipsRetries(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{Attempts: 3, RetryDelay: time.Millisecond})
	ctx := context.Background()
	flaky := newProvider(t,
		providerResponse{status: 500}, providerResponse{status: 500}, providerResponse{status: 500},
		providerResponse{status: 500},
		providerResponse{status: 200},
		providerResponse{status: 500}, providerResponse{status: 200},
	)
	e.webhookChannel("flaky", e.ns.ID, flaky.URL, []string{KindBanSpike}, nil, "")
	e.startService()

	attempts := func() int {
		id, err := e.svc.emit(ctx, siteAlert(e, KindBanSpike, SeverityWarning, ""))
		require.NoError(t, err)
		return e.waitDeliveries(id, 1)[0].Attempts
	}
	require.Equal(t, 3, attempts(), "healthy channels are retried")
	require.Equal(t, 1, attempts(), "failing channels get one attempt")
	require.Equal(t, 1, attempts(), "the single attempt succeeds and clears the failure")
	require.Equal(t, 2, attempts(), "retries resume after a success")
	require.Len(t, flaky.received(), 7)
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()
	require.True(t, sleepCtx(context.Background(), 0))
	require.True(t, sleepCtx(context.Background(), time.Millisecond))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, sleepCtx(canceled, 0))
	require.False(t, sleepCtx(canceled, time.Hour))
}
