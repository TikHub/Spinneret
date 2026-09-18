package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// counterValue sums the counter samples of a vector that carry label value v.
func counterValue(t *testing.T, c *prometheus.CounterVec, v string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	families, err := reg.Gather()
	require.NoError(t, err)
	var sum float64
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetValue() == v {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}

func testSupervisor() (*supervisor, *prometheus.CounterVec) {
	restarts := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "restarts"}, []string{"loop"})
	s := newSupervisor(discardLogger(), restarts)
	s.min, s.max = time.Millisecond, 4*time.Millisecond
	return s, restarts
}

func TestSupervisorRestartsFailingLoops(t *testing.T) {
	s, restarts := testSupervisor()
	ctx, cancel := context.WithCancel(context.Background())
	var failing, early, panicking atomic.Int32
	s.Go(ctx, "failing", func(context.Context) error {
		failing.Add(1)
		return errors.New("transient")
	})
	s.Go(ctx, "early", func(context.Context) error {
		early.Add(1)
		return nil // returning before shutdown is unexpected too
	})
	s.Go(ctx, "panicking", func(context.Context) error {
		if panicking.Add(1) == 1 {
			panic("boom")
		}
		<-ctx.Done()
		return ctx.Err()
	})
	require.Eventually(t, func() bool {
		return failing.Load() >= 3 && early.Load() >= 3 && panicking.Load() == 2
	}, 5*time.Second, time.Millisecond)
	cancel()
	require.True(t, s.Wait(context.Background()))
	require.GreaterOrEqual(t, counterValue(t, restarts, "failing"), 2.0)
	require.Equal(t, 1.0, counterValue(t, restarts, "panicking"))
}

func TestSupervisorBackoffGrowsAndIsBounded(t *testing.T) {
	s, _ := testSupervisor()
	s.min, s.max = 10*time.Millisecond, 40*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var starts []time.Time
	done := make(chan struct{})
	s.Go(ctx, "loop", func(context.Context) error {
		starts = append(starts, time.Now())
		if len(starts) == 5 {
			close(done)
		}
		return errors.New("fail")
	})
	<-done
	cancel()
	require.True(t, s.Wait(context.Background()))
	require.GreaterOrEqual(t, starts[1].Sub(starts[0]), 10*time.Millisecond)
	require.GreaterOrEqual(t, starts[2].Sub(starts[1]), 20*time.Millisecond)
	require.GreaterOrEqual(t, starts[4].Sub(starts[3]), 40*time.Millisecond)
	require.Less(t, starts[4].Sub(starts[3]), 2*time.Second)
}

func TestSupervisorStopsOnCancelWithoutRestart(t *testing.T) {
	s, restarts := testSupervisor()
	ctx, cancel := context.WithCancel(context.Background())
	var runs atomic.Int32
	s.Go(ctx, "clean", func(ctx context.Context) error {
		runs.Add(1)
		<-ctx.Done()
		return errors.New("stopped with error")
	})
	require.Eventually(t, func() bool { return runs.Load() == 1 }, time.Second, time.Millisecond)
	cancel()
	require.True(t, s.Wait(context.Background()))
	require.EqualValues(t, 1, runs.Load())
	require.Zero(t, counterValue(t, restarts, "clean"))
}

func TestSupervisorWaitHonoursDeadline(t *testing.T) {
	s, _ := testSupervisor()
	release := make(chan struct{})
	loopCtx, stopLoop := context.WithCancel(context.Background())
	s.Go(loopCtx, "stuck", func(ctx context.Context) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.False(t, s.Wait(deadline))
	// Stop the loop so it is not restarted for the rest of the test binary.
	stopLoop()
	close(release)
	require.True(t, s.Wait(context.Background()))
}

func TestTierStartsLoopsFromWithinTheTier(t *testing.T) {
	tr := newTier("t", discardLogger(), nil)
	started := make(chan struct{})
	tr.Go("parent", func(ctx context.Context) error {
		tr.Go("child", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return nil
		})
		<-ctx.Done()
		return nil
	})
	<-started
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, tr.stop(deadline))
}
