package breaker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startRecorder is a start function for notifyLimiter tests.
type startRecorder struct {
	accept  bool
	started []groupRef
}

func (r *startRecorder) start(ref groupRef) bool {
	if !r.accept {
		return false
	}
	r.started = append(r.started, ref)
	return true
}

func TestNotifyLimiter(t *testing.T) {
	t.Parallel()
	a := groupRef{SiteKey: 1, GroupKey: 10}
	b := groupRef{SiteKey: 1, GroupKey: 11}
	t0 := time.UnixMilli(1_758_011_400_000)

	t.Run("burst starts a group once per interval", func(t *testing.T) {
		t.Parallel()
		l := newNotifyLimiter(time.Second)
		rec := &startRecorder{accept: true}

		l.offer(a, t0, rec.start)
		require.Equal(t, []groupRef{a}, rec.started)

		// Inside the interval: deferred until t0+1s.
		l.offer(a, t0.Add(100*time.Millisecond), rec.start)
		require.Len(t, rec.started, 1)
		require.Equal(t, t0.Add(time.Second), l.deferred[a])

		// A notification after the due time but before the tick must not
		// start the deferred group a second time.
		l.offer(a, t0.Add(1100*time.Millisecond), rec.start)
		require.Len(t, rec.started, 1)

		l.tick(t0.Add(1100*time.Millisecond), rec.start)
		require.Equal(t, []groupRef{a, a}, rec.started)
		require.Empty(t, l.deferred)

		// The tick right after does not start it again.
		l.tick(t0.Add(1200*time.Millisecond), rec.start)
		require.Len(t, rec.started, 2)

		// Other groups are independent.
		l.offer(b, t0.Add(1200*time.Millisecond), rec.start)
		require.Equal(t, []groupRef{a, a, b}, rec.started)
	})

	t.Run("busy slots defer and retry", func(t *testing.T) {
		t.Parallel()
		l := newNotifyLimiter(time.Second)
		rec := &startRecorder{accept: false}

		l.offer(a, t0, rec.start)
		require.Empty(t, rec.started)
		require.Contains(t, l.deferred, a)

		l.tick(t0.Add(100*time.Millisecond), rec.start)
		require.Empty(t, rec.started)
		require.Contains(t, l.deferred, a)

		rec.accept = true
		l.tick(t0.Add(200*time.Millisecond), rec.start)
		require.Equal(t, []groupRef{a}, rec.started)
		require.Empty(t, l.deferred)
	})

	t.Run("expired rate-limit entries are forgotten", func(t *testing.T) {
		t.Parallel()
		l := newNotifyLimiter(time.Second)
		rec := &startRecorder{accept: true}
		l.offer(a, t0, rec.start)
		l.offer(b, t0.Add(500*time.Millisecond), rec.start)
		require.Len(t, l.last, 2)

		l.tick(t0.Add(time.Second), rec.start)
		require.NotContains(t, l.last, a)
		require.Contains(t, l.last, b)

		l.tick(t0.Add(2*time.Second), rec.start)
		require.Empty(t, l.last)
	})

	t.Run("deferred set is bounded", func(t *testing.T) {
		t.Parallel()
		l := newNotifyLimiter(time.Hour)
		rec := &startRecorder{accept: false}
		for i := range maxDeferred + 5 {
			l.offer(groupRef{SiteKey: 2, GroupKey: int64(i)}, t0, rec.start)
		}
		require.Len(t, l.deferred, maxDeferred)
	})
}
