package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/store/postgres"
)

// Partition manager settings (spec §6.8).
const (
	partitionJobName      = "partition_manager"
	partitionJobInterval  = time.Hour
	partitionJobTimeout   = 15 * time.Minute
	alertEventsRetention  = 90 * 24 * time.Hour
	alertEventsPurgeBatch = 5000
)

// partitionMaintainer abstracts the PostgreSQL maintenance calls so the job
// logic can be tested without a database.
type partitionMaintainer struct {
	ensure    func(ctx context.Context, now time.Time) error
	drop      func(ctx context.Context, now time.Time) error
	purge     func(ctx context.Context, before time.Time) (int64, error)
	now       func() time.Time
	logger    *slog.Logger
	retention time.Duration
}

func newPartitionMaintainer(pool *pgxpool.Pool, r appconfig.Retention, logger *slog.Logger) *partitionMaintainer {
	return &partitionMaintainer{
		ensure: func(ctx context.Context, now time.Time) error { return postgres.EnsurePartitions(ctx, pool, now) },
		drop: func(ctx context.Context, now time.Time) error {
			return postgres.DropExpiredPartitions(ctx, pool, r, now)
		},
		purge: func(ctx context.Context, before time.Time) (int64, error) {
			return postgres.PurgeAlertEvents(ctx, pool, before, alertEventsPurgeBatch)
		},
		now:       time.Now,
		logger:    logger.With(slog.String("component", partitionJobName)),
		retention: alertEventsRetention,
	}
}

// job returns the leader job that keeps partitions rolling, drops expired
// partitions and purges old alert events. Its first run happens right after
// leadership is acquired at startup.
func (m *partitionMaintainer) job() jobs.Job {
	return jobs.Job{
		Name:         partitionJobName,
		Interval:     partitionJobInterval,
		Mode:         jobs.Leader,
		Timeout:      partitionJobTimeout,
		InitialDelay: time.Millisecond,
		Run:          m.run,
	}
}

// run performs one maintenance pass. Every step is attempted; failures are joined.
func (m *partitionMaintainer) run(ctx context.Context) error {
	now := m.now().UTC()
	var errs []error
	if err := m.ensure(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("ensure partitions: %w", err))
	}
	if err := m.drop(ctx, now); err != nil {
		errs = append(errs, fmt.Errorf("drop expired partitions: %w", err))
	}
	deleted, err := m.purge(ctx, now.Add(-m.retention))
	if err != nil {
		errs = append(errs, fmt.Errorf("purge alert events: %w", err))
	}
	if deleted > 0 {
		m.logger.Info("purged expired alert events", slog.Int64("deleted", deleted))
	}
	return errors.Join(errs...)
}

// jobMetrics implements jobs.Metrics with Prometheus collectors registered on
// the server registry.
type jobMetrics struct {
	runs     *prometheus.CounterVec   // job, result
	duration *prometheus.HistogramVec // job
}

var _ jobs.Metrics = (*jobMetrics)(nil)

func newJobMetrics(reg prometheus.Registerer) (*jobMetrics, error) {
	m := &jobMetrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "spinneret",
			Name:      "job_runs_total",
			Help:      "Background job iterations by result (ok or error).",
		}, []string{"job", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "spinneret",
			Name:      "job_duration_seconds",
			Help:      "Duration of background job iterations.",
			Buckets:   []float64{0.001, 0.005, 0.025, 0.1, 0.5, 2.5, 10, 60, 300},
		}, []string{"job"}),
	}
	for _, c := range []prometheus.Collector{m.runs, m.duration} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register job metrics: %w", err)
		}
	}
	return m, nil
}

// ObserveJob implements jobs.Metrics.
func (m *jobMetrics) ObserveJob(name string, d time.Duration, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	m.runs.WithLabelValues(name, result).Inc()
	m.duration.WithLabelValues(name).Observe(d.Seconds())
}

// newLoopRestartCounter creates the counter of supervised loop restarts.
func newLoopRestartCounter(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "spinneret",
		Name:      "loop_restarts_total",
		Help:      "Restarts of background loops that failed or exited before shutdown.",
	}, []string{"loop"})
	if err := reg.Register(c); err != nil {
		return nil, fmt.Errorf("register loop restart metric: %w", err)
	}
	return c, nil
}
