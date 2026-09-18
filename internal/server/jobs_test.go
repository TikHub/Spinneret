package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/jobs"
)

func TestPartitionMaintainerRunsEveryStep(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	var calls []string
	var purgedBefore time.Time
	m := &partitionMaintainer{
		ensure: func(_ context.Context, at time.Time) error {
			require.Equal(t, now, at)
			calls = append(calls, "ensure")
			return errors.New("ensure failed")
		},
		drop: func(context.Context, time.Time) error {
			calls = append(calls, "drop")
			return nil
		},
		purge: func(_ context.Context, before time.Time) (int64, error) {
			calls = append(calls, "purge")
			purgedBefore = before
			return 12, errors.New("purge failed")
		},
		now:       func() time.Time { return now },
		logger:    discardLogger(),
		retention: alertEventsRetention,
	}
	err := m.run(context.Background())
	require.ErrorContains(t, err, "ensure failed")
	require.ErrorContains(t, err, "purge failed")
	require.Equal(t, []string{"ensure", "drop", "purge"}, calls, "a failing step does not skip the others")
	require.Equal(t, now.Add(-90*24*time.Hour), purgedBefore)

	job := m.job()
	require.NoError(t, job.Validate())
	require.Equal(t, jobs.Leader, job.Mode)
	require.Equal(t, time.Hour, job.Interval)
	require.Positive(t, job.InitialDelay, "the first run happens right after startup")
	require.Less(t, job.InitialDelay, time.Second)
}

func TestCheckPoolSize(t *testing.T) {
	require.NoError(t, checkPoolSize(32, 4))
	require.NoError(t, checkPoolSize(6, 4))
	err := checkPoolSize(5, 4)
	require.ErrorContains(t, err, "SPINNERET_DATABASE_MAX_CONNS=5 is too small")
	require.ErrorContains(t, err, "use at least 6")
}

func TestJobMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := newJobMetrics(reg)
	require.NoError(t, err)
	m.ObserveJob("reaper", 3*time.Millisecond, nil)
	m.ObserveJob("reaper", time.Millisecond, errors.New("x"))
	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]int{}
	for _, f := range families {
		names[f.GetName()] = len(f.GetMetric())
	}
	require.Equal(t, 2, names["spinneret_job_runs_total"], "ok and error series")
	require.Equal(t, 1, names["spinneret_job_duration_seconds"])

	_, err = newJobMetrics(reg)
	require.Error(t, err, "duplicate registration is reported")
	_, err = newLoopRestartCounter(reg)
	require.NoError(t, err)
	_, err = newLoopRestartCounter(reg)
	require.Error(t, err)
}
