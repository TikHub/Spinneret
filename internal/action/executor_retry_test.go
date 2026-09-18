package action

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

// The worker retries a failed Execute only for an executor that declares
// itself idempotent per report (worker.IdempotentActionExecutor, forwarded by
// the server adapter). Without the declaration a call that ran in Redis but
// whose reply was lost is never retried and its state changes are lost.
func TestExecutorDeclaresIdempotentExecute(t *testing.T) {
	var exec any = NewExecutor(ExecutorConfig{}, nil, redis.NewKeys("test"), nil, nil, nil, nil)
	ie, ok := exec.(interface{ IdempotentExecute() bool })
	require.True(t, ok, "the executor must declare its per-report idempotency")
	require.True(t, ie.IdempotentExecute())
}

// The wait for queue space applies once to all state changes of a report: a
// report with many lifecycle changes (an account ban and its members) blocks
// the worker for at most enqueueWait, not for enqueueWait per change.
func TestExecutorEnqueueWaitIsBoundedPerReport(t *testing.T) {
	w := NewStateWriter(nil, nil, nil)
	w.capacity = 1
	now := time.Now()
	w.Enqueue(identityChange("idt_queued", now, StateBanned, OpBan))

	const wait = 200 * time.Millisecond
	exec := &Executor{writer: w, enqueueWait: wait, logger: slog.New(slog.DiscardHandler)}
	changes := make([]StateChange, 5)
	for i := range changes {
		changes[i] = identityChange(fmt.Sprintf("idt_%d", i), now, StateBanned, OpBan)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the report's context has ended: the changes are applied in Redis and still wait
	start := time.Now()
	exec.enqueue(ctx, changes)
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, wait, "lifecycle changes wait for queue space")
	require.Less(t, elapsed, 3*wait, "the wait is bounded per report, not per change")
	require.EqualValues(t, len(changes), w.Dropped())
	require.Equal(t, 1, w.Pending())
}
