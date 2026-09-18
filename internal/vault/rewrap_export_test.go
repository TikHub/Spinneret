package vault

import (
	"context"
	"time"
)

// Test hooks for the external vault_test package.

// SetRewrapBatchSize overrides the keyset batch size. Call before Start.
func SetRewrapBatchSize(r *Rewrapper, n int) { r.batchSize = n }

// SetRewrapAfterBatch installs a hook called after each batch. Call before Start.
func SetRewrapAfterBatch(r *Rewrapper, fn func(table string)) { r.afterBatch = fn }

// SetRewrapClock overrides the clock. Call before Start.
func SetRewrapClock(r *Rewrapper, now func() time.Time) { r.now = now }

// PersistRewrapState stores a raw rewrap status document.
func PersistRewrapState(ctx context.Context, r *Rewrapper, running bool, done, total int64, heartbeat time.Time, lastError string) error {
	return r.persist(ctx, rewrapState{
		Running: running, Done: done, Total: total, HeartbeatAt: &heartbeat, LastError: lastError,
	})
}

// RewrapStateTruncate exposes truncateUTF8.
func RewrapStateTruncate(s string, n int) string { return truncateUTF8(s, n) }

// SetRewrapHeartbeatInterval overrides the progress heartbeat period. Call before Start.
func SetRewrapHeartbeatInterval(r *Rewrapper, d time.Duration) { r.heartbeatEvery = d }
