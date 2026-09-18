package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestRNGSourceDeterministic(t *testing.T) {
	t.Parallel()
	a, b := newRNGSource(7), newRNGSource(7)
	for range 100 {
		va, vb := a.float64(), b.float64()
		require.Equal(t, va, vb)
		require.GreaterOrEqual(t, va, 0.0)
		require.Less(t, va, 1.0)
	}
	require.NotZero(t, newRNGSource(0).seed, "seed 0 selects a random non-zero seed")
}

func TestRNGSourceTrigger(t *testing.T) {
	t.Parallel()
	src := newRNGSource(12345)
	require.True(t, src.trigger(1))
	require.True(t, src.trigger(2))
	require.False(t, src.trigger(0))
	require.False(t, src.trigger(-1))

	const n = 20000
	for _, p := range []float64{0.1, 0.5, 0.9} {
		hits := 0
		for range n {
			if src.trigger(p) {
				hits++
			}
		}
		require.InDelta(t, p, float64(hits)/n, 0.02, "p=%v", p)
	}
}

func TestMix64Decorrelates(t *testing.T) {
	t.Parallel()
	require.NotEqual(t, mix64(1), mix64(2))
	require.NotEqual(t, uint64(0), mix64(1))
}

func TestTunnelRegistry(t *testing.T) {
	t.Parallel()
	var reg tunnelRegistry
	_, ok := reg.lookup("127.0.0.1:1")
	require.False(t, ok)

	unregister := reg.register("127.0.0.1:1", "p1")
	id, ok := reg.lookup("127.0.0.1:1")
	require.True(t, ok)
	require.Equal(t, "p1", id)

	// A stale unregister must not remove a newer registration of a reused address.
	unregisterNew := reg.register("127.0.0.1:1", "p2")
	unregister()
	id, ok = reg.lookup("127.0.0.1:1")
	require.True(t, ok)
	require.Equal(t, "p2", id)
	unregisterNew()
	_, ok = reg.lookup("127.0.0.1:1")
	require.False(t, ok)
}

func TestSleepCtx(t *testing.T) {
	t.Parallel()
	require.NoError(t, sleepCtx(context.Background(), 0))
	require.NoError(t, sleepCtx(context.Background(), time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	require.ErrorIs(t, sleepCtx(ctx, time.Hour), context.Canceled)
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 1500*time.Millisecond, millis(1500))
}

func TestTruncateLabel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "short", truncateLabel("short"))
	long := strings.Repeat("a", maxLabelLen+10)
	require.Len(t, truncateLabel(long), maxLabelLen)

	// A multi-byte rune straddling the limit is dropped entirely.
	multi := strings.Repeat("a", maxLabelLen-1) + "é" + "tail"
	got := truncateLabel(multi)
	require.True(t, utf8.ValidString(got))
	require.Len(t, got, maxLabelLen-1)
}

func TestWriteJSONEncodeFailure(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, math.Inf(1))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "encode response")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
