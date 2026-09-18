package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

// ChangeMarks is a per-namespace change counter shared by every instance.
//
// Peers learn about catalog changes through Pub/Sub invalidations that are
// debounced and applied asynchronously, so a request routed to another
// instance right after a write could observe the previous snapshot (for
// example "namespace not found" for a namespace that was just created).
// Invalidate therefore bumps the mark of the namespace after the change was
// committed, every load records the mark it observed before reading
// PostgreSQL, and Sync reloads the namespaces whose shared mark is ahead of the
// recorded one. Requests that call Sync observe every change acknowledged
// before they started, on any instance.
type ChangeMarks interface {
	// Bump increments the mark of a namespace.
	Bump(ctx context.Context, namespaceID string) error
	// Get returns the mark of a namespace (0 when it has none).
	Get(ctx context.Context, namespaceID string) (int64, error)
	// All returns every mark.
	All(ctx context.Context) (map[string]int64, error)
	// Delete removes the marks of namespaces that no longer exist.
	Delete(ctx context.Context, namespaceIDs ...string) error
}

// RedisChangeMarks stores change marks in the Redis hash "P:catv"
// (field = namespace ID, value = counter).
type RedisChangeMarks struct {
	rdb rueidis.Client
	key string
}

var _ ChangeMarks = (*RedisChangeMarks)(nil)

// NewRedisChangeMarks returns change marks stored under keys.CatalogMarks().
func NewRedisChangeMarks(rdb rueidis.Client, keys redis.Keys) *RedisChangeMarks {
	return &RedisChangeMarks{rdb: rdb, key: keys.CatalogMarks()}
}

// Bump implements ChangeMarks.
func (m *RedisChangeMarks) Bump(ctx context.Context, namespaceID string) error {
	if err := m.rdb.Do(ctx, m.rdb.B().Hincrby().Key(m.key).Field(namespaceID).Increment(1).Build()).Error(); err != nil {
		return fmt.Errorf("bump catalog mark of namespace %s: %w", namespaceID, err)
	}
	return nil
}

// Get implements ChangeMarks.
func (m *RedisChangeMarks) Get(ctx context.Context, namespaceID string) (int64, error) {
	v, err := m.rdb.Do(ctx, m.rdb.B().Hget().Key(m.key).Field(namespaceID).Build()).AsInt64()
	if rueidis.IsRedisNil(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read catalog mark of namespace %s: %w", namespaceID, err)
	}
	return v, nil
}

// All implements ChangeMarks.
func (m *RedisChangeMarks) All(ctx context.Context) (map[string]int64, error) {
	raw, err := m.rdb.Do(ctx, m.rdb.B().Hgetall().Key(m.key).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("read catalog marks: %w", err)
	}
	out := make(map[string]int64, len(raw))
	for id, v := range raw {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("parse catalog mark of namespace %s: %w", id, perr)
		}
		out[id] = n
	}
	return out, nil
}

// Delete implements ChangeMarks.
func (m *RedisChangeMarks) Delete(ctx context.Context, namespaceIDs ...string) error {
	if len(namespaceIDs) == 0 {
		return nil
	}
	if err := m.rdb.Do(ctx, m.rdb.B().Hdel().Key(m.key).Field(namespaceIDs...).Build()).Error(); err != nil {
		return fmt.Errorf("delete catalog marks: %w", err)
	}
	return nil
}

// SetChangeMarks enables cross-instance read-your-writes consistency through
// Sync. It must be called before the store is used.
func (s *Store) SetChangeMarks(marks ChangeMarks) {
	s.marks = marks
}

// Sync reloads every namespace whose shared change mark is ahead of the mark
// observed by its last load on this instance, and waits for those loads. A
// store without change marks returns immediately. Errors (Redis or PostgreSQL
// unavailable) leave the current snapshots in place.
func (s *Store) Sync(ctx context.Context) error {
	if s.marks == nil {
		return nil
	}
	marks, err := s.marks.All(ctx)
	if err != nil {
		return fmt.Errorf("catalog: sync: %w", err)
	}
	var stale []string
	s.writeMu.Lock()
	for id, mark := range marks {
		if mark > s.applied[id] {
			stale = append(stale, id)
		}
	}
	s.writeMu.Unlock()
	if len(stale) == 0 {
		return nil
	}
	waiting := make(map[string]*flight, len(stale))
	for _, id := range stale {
		waiting[id] = s.request(ctx, id)
	}
	var errs []error
	for id, f := range waiting {
		if err := f.wait(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("catalog: sync: %w", ctx.Err())
			}
			if len(errs) < maxJoinedErrors {
				errs = append(errs, fmt.Errorf("namespace %s: %w", id, err))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("catalog: sync: %w", errors.Join(errs...))
	}
	return nil
}

// bumpMark increments the shared change mark of a namespace (best effort).
func (s *Store) bumpMark(ctx context.Context, namespaceID string) {
	if s.marks == nil {
		return
	}
	if err := s.marks.Bump(ctx, namespaceID); err != nil {
		s.logger.Warn("catalog: bumping the change mark failed; peers catch up on the invalidation event",
			slog.String("namespace_id", namespaceID), slog.Any("error", err))
	}
}

// observeMark reads the change mark of a namespace before a load. ok is false
// when marks are disabled or unavailable; the load then records nothing.
func (s *Store) observeMark(ctx context.Context, namespaceID string) (mark int64, ok bool) {
	if s.marks == nil {
		return 0, false
	}
	mark, err := s.marks.Get(ctx, namespaceID)
	if err != nil {
		s.logger.Debug("catalog: reading the change mark failed", slog.String("namespace_id", namespaceID), slog.Any("error", err))
		return 0, false
	}
	return mark, true
}

// recordMarkLocked records the mark observed by a completed load. The caller
// holds writeMu.
func (s *Store) recordMarkLocked(namespaceID string, mark int64, ok bool) {
	if ok && mark > s.applied[namespaceID] {
		s.applied[namespaceID] = mark
	}
}

// pruneMarks deletes the marks of namespaces that were missing from two
// consecutive full reloads: by then every instance removed them from its
// index, so the marks are no longer needed to detect the deletion. present
// lists the namespaces of the current full reload; marks must be read before
// that listing so that a namespace created after the listing is never taken
// for a deleted one.
func (s *Store) pruneMarks(ctx context.Context, marks map[string]int64, present map[string]struct{}) {
	if s.marks == nil || marks == nil {
		return
	}
	s.writeMu.Lock()
	var prune []string
	missing := make(map[string]struct{})
	for id := range marks {
		if _, ok := present[id]; ok {
			continue
		}
		if _, before := s.missingMarks[id]; before {
			prune = append(prune, id)
			delete(s.applied, id)
			continue
		}
		missing[id] = struct{}{}
	}
	s.missingMarks = missing
	s.writeMu.Unlock()
	if err := s.marks.Delete(ctx, prune...); err != nil {
		s.logger.Warn("catalog: pruning change marks of deleted namespaces failed", slog.Any("error", err))
	}
}
