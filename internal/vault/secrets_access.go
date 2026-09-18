package vault

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/vault/vaultdb"
)

// accessTracker collects last-access times of secrets in memory so reads do
// not write to PostgreSQL; SecretStore.Run persists them in batches.
type accessTracker struct {
	mu      sync.Mutex
	pending map[string]time.Time
	max     int
	dropped atomic.Int64
}

func newAccessTracker(maxEntries int) *accessTracker {
	return &accessTracker{pending: make(map[string]time.Time), max: maxEntries}
}

// touch records an access, keeping the latest time per secret. When the
// buffer is full, accesses of secrets not yet buffered are dropped (and
// counted): last_accessed_at is informational.
func (t *accessTracker) touch(secretID string, at time.Time) {
	if secretID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.pending[secretID]; ok {
		if at.After(prev) {
			t.pending[secretID] = at
		}
		return
	}
	if len(t.pending) >= t.max {
		t.dropped.Add(1)
		return
	}
	t.pending[secretID] = at
}

// drain removes and returns every buffered access.
func (t *accessTracker) drain() map[string]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pending) == 0 {
		return nil
	}
	out := t.pending
	t.pending = make(map[string]time.Time, len(out))
	return out
}

// restore puts back accesses that could not be persisted, without exceeding
// the buffer limit and without overwriting newer accesses.
func (t *accessTracker) restore(entries map[string]time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, at := range entries {
		if prev, ok := t.pending[id]; ok {
			if at.After(prev) {
				t.pending[id] = at
			}
			continue
		}
		if len(t.pending) >= t.max {
			t.dropped.Add(1)
			continue
		}
		t.pending[id] = at
	}
}

func (t *accessTracker) len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pending)
}

// Run persists buffered last-access times every 10 seconds until ctx is
// canceled, then flushes one last time. It always returns nil.
func (s *SecretStore) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessFlushTimeout)
			if err := s.FlushAccessTimes(fctx); err != nil {
				s.logger.Warn("final flush of secret access times failed", slog.Any("error", err))
			}
			cancel()
			return nil
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, accessFlushTimeout)
			if err := s.FlushAccessTimes(fctx); err != nil && ctx.Err() == nil {
				s.logger.Warn("flush of secret access times failed", slog.Any("error", err))
			}
			cancel()
		}
	}
}

// FlushAccessTimes writes buffered last-access times to PostgreSQL. Entries
// that could not be written are kept for the next flush.
func (s *SecretStore) FlushAccessTimes(ctx context.Context) error {
	pending := s.access.drain()
	if len(pending) == 0 {
		return nil
	}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	// A stable order keeps concurrent flushes of several instances from
	// locking rows in conflicting orders.
	slices.Sort(ids)
	for start := 0; start < len(ids); start += accessFlushChunk {
		chunk := ids[start:min(start+accessFlushChunk, len(ids))]
		times := make([]time.Time, len(chunk))
		for i, id := range chunk {
			times[i] = pending[id]
		}
		if _, err := s.q.VaultSecretTouch(ctx, vaultdb.VaultSecretTouchParams{Ids: chunk, AccessedAt: times}); err != nil {
			rest := make(map[string]time.Time, len(ids)-start)
			for _, id := range ids[start:] {
				rest[id] = pending[id]
			}
			s.access.restore(rest)
			return errorf(err, "update secret access times")
		}
	}
	return nil
}
