package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Supervisor defaults.
const (
	defaultMinRestartBackoff = time.Second
	defaultMaxRestartBackoff = 30 * time.Second
	// healthyRunDuration resets the restart backoff: a loop that ran this long
	// before failing is considered to have been healthy.
	healthyRunDuration = time.Minute
)

// supervisor runs long-lived loops (Run(ctx) error functions) until their
// context is canceled. A loop that returns before cancellation, with or
// without an error, or panics is logged and restarted with exponential
// backoff, so a transient failure never stops a subsystem silently and a
// persistent one never busy-loops.
type supervisor struct {
	logger   *slog.Logger
	restarts *prometheus.CounterVec // loop; may be nil
	min, max time.Duration

	wg sync.WaitGroup
}

func newSupervisor(logger *slog.Logger, restarts *prometheus.CounterVec) *supervisor {
	return &supervisor{logger: logger, restarts: restarts, min: defaultMinRestartBackoff, max: defaultMaxRestartBackoff}
}

// Go starts fn under supervision. It may be called from a goroutine that is
// itself supervised by the same supervisor (the wait group is non-zero then).
func (s *supervisor) Go(ctx context.Context, name string, fn func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.supervise(ctx, name, fn)
	}()
}

// Wait blocks until every supervised loop returned or ctx is done. It reports
// whether all loops finished.
func (s *supervisor) Wait(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *supervisor) supervise(ctx context.Context, name string, fn func(context.Context) error) {
	backoff := s.min
	for {
		started := time.Now()
		err := runRecovered(ctx, fn)
		if ctx.Err() != nil {
			if err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("background loop stopped with error during shutdown",
					slog.String("loop", name), slog.Any("error", err))
			}
			return
		}
		if time.Since(started) >= healthyRunDuration {
			backoff = s.min
		}
		if err == nil {
			err = errors.New("returned before shutdown")
		}
		s.logger.Error("background loop failed, restarting",
			slog.String("loop", name), slog.Any("error", err), slog.Duration("backoff", backoff))
		if s.restarts != nil {
			s.restarts.WithLabelValues(name).Inc()
		}
		if !sleepContext(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, s.max)
	}
}

// runRecovered calls fn and converts a panic into an error that carries the stack.
func runRecovered(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("panic: %v\n%s", rec, debug.Stack())
		}
	}()
	return fn(ctx)
}

// sleepContext sleeps for d and reports false when ctx ended first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// tier is a group of loops that is started together and stopped together.
// Tiers are stopped in order so that producers finish before the sinks they
// write to (for example the stats aggregator before the ClickHouse writer).
type tier struct {
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	sup    *supervisor
}

func newTier(name string, logger *slog.Logger, restarts *prometheus.CounterVec) *tier {
	// Tier contexts are deliberately detached from the Run context: the
	// shutdown sequence cancels them one by one.
	ctx, cancel := context.WithCancel(context.Background())
	return &tier{name: name, ctx: ctx, cancel: cancel, sup: newSupervisor(logger, restarts)}
}

// Go starts a supervised loop in the tier.
func (t *tier) Go(name string, fn func(context.Context) error) { t.sup.Go(t.ctx, name, fn) }

// stop cancels the tier and waits for its loops until deadline ctx ends.
func (t *tier) stop(deadline context.Context) bool {
	t.cancel()
	return t.sup.Wait(deadline)
}
