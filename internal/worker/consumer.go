package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/rueidis"
)

// Consumer tuning.
const (
	// streamStart is the id a new consumer group starts at: the beginning of the
	// stream, so reports enqueued before the first worker started are processed.
	streamStart = "0"
	// claimStart is the XAUTOCLAIM cursor that starts (and ends) a PEL scan.
	claimStart = "0-0"
	// staleConsumerIdle is how long a consumer without pending entries must be
	// idle before it is removed from the group.
	staleConsumerIdle = 5 * time.Minute
	minBackoff        = 100 * time.Millisecond
	maxBackoff        = 5 * time.Second
)

// DefaultTrimInterval is how often a shard stream is trimmed to the consumer
// group position (Config.TrimInterval).
const DefaultTrimInterval = 5 * time.Second

// consume is the goroutine of one owned shard. It stops (after finishing the
// event in progress) when ctx is cancelled; events are processed with a
// context detached from ctx so that an event is never abandoned half way.
func (w *Worker) consume(ctx context.Context, shard int, done chan<- struct{}) {
	defer close(done)
	stream := w.keys.Stream(shard)
	proc := context.WithoutCancel(ctx)
	log := w.logger.With(slog.Int("shard", shard))
	backoff := minBackoff

	wait := func(err error, what string) bool {
		log.Warn("report stream consumer error", slog.String("op", what), slog.Any("error", err))
		ok := sleepCtx(ctx, backoff)
		backoff = min(2*backoff, maxBackoff)
		return ok
	}

	for ctx.Err() == nil {
		if err := w.ensureGroup(ctx, stream); err != nil {
			if !wait(err, "create group") {
				return
			}
			continue
		}
		break
	}
	tr := &trimmer{interval: w.trimInterval(), last: time.Now()}
	claimed, acked, lastID := w.claimPending(ctx, proc, shard, stream, wait)
	if !claimed {
		return
	}
	tr.record(lastID)
	w.removeStaleConsumers(ctx, stream, log)

	for ctx.Err() == nil {
		if !acked {
			// Entries whose XACK failed stay pending and are never returned by
			// XREADGROUP ">" again: claim them back (they are duplicates for
			// observe.lua, so this only acknowledges them).
			if claimed, acked, lastID = w.claimPending(ctx, proc, shard, stream, wait); !claimed {
				return
			}
			tr.record(lastID)
			continue
		}
		entries, err := w.readGroup(ctx, stream)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if isNoGroup(err) {
				if gerr := w.ensureGroup(ctx, stream); gerr != nil && !wait(gerr, "recreate group") {
					return
				}
				continue
			}
			if !wait(err, "read") {
				return
			}
			continue
		}
		backoff = minBackoff
		if len(entries) > 0 {
			var complete bool
			if complete, acked, lastID = w.processBatch(ctx, proc, shard, stream, entries); !complete {
				return
			}
			tr.record(lastID)
		}
		if acked && tr.due(time.Now()) {
			w.trimProcessed(ctx, shard, stream, tr.lastAcked)
		}
	}
}

// trimmer paces the stream trimming of one shard consumer: it remembers the
// last acknowledged entry id and when the stream was last trimmed.
type trimmer struct {
	interval  time.Duration
	last      time.Time
	lastAcked string
}

// record notes the last acknowledged entry id of a batch ("" when none).
func (t *trimmer) record(id string) {
	if id != "" {
		t.lastAcked = id
	}
}

// due reports whether the stream should be trimmed now, and starts a new
// interval when it does.
func (t *trimmer) due(now time.Time) bool {
	if now.Sub(t.last) < t.interval {
		return false
	}
	t.last = now
	return true
}

// claimPending takes over every pending entry of the shard (entries delivered
// to a previous owner, or to this instance before a restart, but never
// acknowledged) and processes them in id order. claimed is false when ctx was
// cancelled before the scan finished; acked is false when acknowledging a
// batch failed (its entries are still pending). lastID is the last entry id
// acknowledged by the scan ("" when it processed nothing).
func (w *Worker) claimPending(ctx, proc context.Context, shard int, stream string, wait func(error, string) bool) (claimed, acked bool, lastID string) {
	cursor := claimStart
	acked = true
	for ctx.Err() == nil {
		next, entries, err := w.autoClaim(ctx, stream, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return false, acked, lastID
			}
			if isNoGroup(err) {
				if gerr := w.ensureGroup(ctx, stream); gerr != nil && !wait(gerr, "recreate group") {
					return false, acked, lastID
				}
				continue
			}
			if !wait(err, "autoclaim") {
				return false, acked, lastID
			}
			continue
		}
		if len(entries) > 0 {
			complete, ok, id := w.processBatch(ctx, proc, shard, stream, entries)
			acked = acked && ok
			if id != "" {
				lastID = id
			}
			if !complete {
				return false, acked, lastID
			}
		}
		if next == claimStart || next == "" {
			return true, acked, lastID
		}
		cursor = next
	}
	return false, acked, lastID
}

// processBatch processes entries sequentially and acknowledges the processed
// ones. complete is false when ctx was cancelled mid-batch (the remaining
// entries stay pending for the next owner); acked is false when the XACK
// failed after retries. lastID is the id of the last acknowledged entry ("" when
// the batch processed nothing or the acknowledgement failed).
func (w *Worker) processBatch(ctx, proc context.Context, shard int, stream string, entries []rueidis.XRangeEntry) (complete, acked bool, lastID string) {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		w.processEntry(proc, shard, e)
		ids = append(ids, e.ID)
	}
	acked = true
	if len(ids) > 0 {
		if err := w.retry(proc, func(opCtx context.Context) error {
			return w.rdb.Do(opCtx, w.rdb.B().Xack().Key(stream).Group(ConsumerGroup).Id(ids...).Build()).Error()
		}); err != nil {
			acked = false
			w.logger.Error("acknowledge report events failed; they will be claimed again",
				slog.Int("shard", shard), slog.Int("count", len(ids)), slog.Any("error", err))
		} else {
			lastID = ids[len(ids)-1]
		}
	}
	return len(ids) == len(entries), acked, lastID
}

// trimProcessed removes acknowledged entries from a shard stream.
//
// ingest.lua appends with XADD ... MAXLEN ~ SPINNERET_STREAM_MAXLEN, which
// bounds a backlog but never shrinks a stream the workers have drained: the
// entries stay until the cap evicts them, so a deployment that once reached
// the cap keeps SPINNERET_STREAM_MAXLEN entries per shard (≈ 440 B each) in
// Redis for its whole life — 7 GiB with the defaults. Trimming to the consumer
// group position keeps the memory proportional to the real backlog instead.
//
// The floor is the oldest entry the group still has pending — an entry a
// previous owner claimed but never acknowledged. With nothing pending it is the
// last entry this consumer acknowledged, or, before this consumer has
// acknowledged anything (a shard adopted after a rebalance), the group's
// last-delivered id: every entry up to it has been delivered and, since nothing
// is pending, acknowledged. An unprocessed event is therefore never removed.
// MINID with "~" only drops whole macro nodes, which bounds the cost to the
// nodes actually removed.
func (w *Worker) trimProcessed(ctx context.Context, shard int, stream, lastAcked string) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	floor, err := w.trimFloor(opCtx, stream, lastAcked)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Debug("read the trim floor of the report stream",
				slog.Int("shard", shard), slog.Any("error", err))
		}
		return
	}
	if floor == "" || floor == streamStart || floor == claimStart {
		return
	}
	cmd := w.rdb.B().Xtrim().Key(stream).Minid().Almost().Threshold(floor).Build()
	if err := w.rdb.Do(opCtx, cmd).Error(); err != nil && ctx.Err() == nil {
		w.logger.Debug("trim report stream", slog.Int("shard", shard), slog.Any("error", err))
	}
}

// trimFloor returns the oldest entry id that must survive a trim: the oldest
// pending entry of the group, else lastAcked, else the group's last-delivered
// id. It returns "" when none of them is known (nothing to trim).
func (w *Worker) trimFloor(ctx context.Context, stream, lastAcked string) (string, error) {
	arr, err := w.rdb.Do(ctx, w.rdb.B().Xpending().Key(stream).Group(ConsumerGroup).Build()).ToArray()
	if err != nil {
		return "", fmt.Errorf("xpending %s: %w", stream, err)
	}
	if len(arr) < 2 {
		return "", fmt.Errorf("xpending %s: unexpected reply with %d elements", stream, len(arr))
	}
	n, err := arr[0].AsInt64()
	if err != nil {
		return "", fmt.Errorf("xpending %s count: %w", stream, err)
	}
	if n > 0 && !arr[1].IsNil() {
		oldest, err := arr[1].ToString()
		if err != nil {
			return "", fmt.Errorf("xpending %s smallest id: %w", stream, err)
		}
		return oldest, nil
	}
	if lastAcked != "" {
		return lastAcked, nil
	}
	return w.lastDelivered(ctx, stream)
}

// lastDelivered returns the last-delivered id of the worker consumer group, or
// "" when the group is missing from the XINFO GROUPS reply.
func (w *Worker) lastDelivered(ctx context.Context, stream string) (string, error) {
	groups, err := w.rdb.Do(ctx, w.rdb.B().XinfoGroups().Key(stream).Build()).ToArray()
	if err != nil {
		return "", fmt.Errorf("xinfo groups %s: %w", stream, err)
	}
	for _, g := range groups {
		info, err := g.AsMap()
		if err != nil {
			return "", fmt.Errorf("xinfo groups %s: %w", stream, err)
		}
		nameMsg := info["name"]
		if name, _ := nameMsg.ToString(); name != ConsumerGroup {
			continue
		}
		last, ok := info["last-delivered-id"]
		if !ok || last.IsNil() {
			return "", nil
		}
		id, err := last.ToString()
		if err != nil {
			return "", fmt.Errorf("xinfo groups %s last-delivered-id: %w", stream, err)
		}
		return id, nil
	}
	return "", nil
}

// trimInterval is how often a shard stream is trimmed to the consumer position.
func (w *Worker) trimInterval() time.Duration {
	if w.cfg.TrimInterval > 0 {
		return w.cfg.TrimInterval
	}
	return DefaultTrimInterval
}

// ensureGroup creates the consumer group (and the stream) when missing.
func (w *Worker) ensureGroup(ctx context.Context, stream string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	err := w.rdb.Do(ctx, w.rdb.B().XgroupCreate().Key(stream).Group(ConsumerGroup).Id(streamStart).Mkstream().Build()).Error()
	if err != nil && !isBusyGroup(err) {
		return fmt.Errorf("create consumer group on %s: %w", stream, err)
	}
	return nil
}

// readGroup reads new entries for this consumer, blocking up to cfg.Block.
func (w *Worker) readGroup(ctx context.Context, stream string) ([]rueidis.XRangeEntry, error) {
	cmd := w.rdb.B().Xreadgroup().Group(ConsumerGroup, w.cfg.InstanceID).
		Count(int64(w.cfg.BatchSize)).Block(w.cfg.Block.Milliseconds()).
		Streams().Key(stream).Id(">").Build()
	res, err := w.rdb.Do(ctx, cmd).AsXRead()
	if err != nil {
		if rueidis.IsRedisNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("xreadgroup %s: %w", stream, err)
	}
	return res[stream], nil
}

// autoClaim claims up to cfg.BatchSize pending entries starting at cursor.
func (w *Worker) autoClaim(ctx context.Context, stream, cursor string) (string, []rueidis.XRangeEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd := w.rdb.B().Xautoclaim().Key(stream).Group(ConsumerGroup).Consumer(w.cfg.InstanceID).
		MinIdleTime("0").Start(cursor).Count(int64(w.cfg.BatchSize)).Build()
	arr, err := w.rdb.Do(ctx, cmd).ToArray()
	if err != nil {
		return "", nil, fmt.Errorf("xautoclaim %s: %w", stream, err)
	}
	if len(arr) < 2 {
		return "", nil, fmt.Errorf("xautoclaim %s: unexpected reply with %d elements", stream, len(arr))
	}
	next, err := arr[0].ToString()
	if err != nil {
		return "", nil, fmt.Errorf("xautoclaim %s cursor: %w", stream, err)
	}
	entries, err := arr[1].AsXRange()
	if err != nil {
		return "", nil, fmt.Errorf("xautoclaim %s entries: %w", stream, err)
	}
	return next, entries, nil
}

// removeStaleConsumers deletes consumers of other instances that have no
// pending entries and have been idle for a long time, so restarted instances
// (new instance ids) do not accumulate in the group. Failures are only logged.
func (w *Worker) removeStaleConsumers(ctx context.Context, stream string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	arr, err := w.rdb.Do(ctx, w.rdb.B().XinfoConsumers().Key(stream).Group(ConsumerGroup).Build()).ToArray()
	if err != nil {
		if ctx.Err() == nil {
			log.Debug("list stream consumers", slog.Any("error", err))
		}
		return
	}
	for _, c := range arr {
		info, err := c.AsMap()
		if err != nil {
			continue
		}
		nameMsg, pendingMsg, idleMsg := info["name"], info["pending"], info["idle"]
		name, _ := nameMsg.ToString()
		pending, _ := pendingMsg.AsInt64()
		idle, _ := idleMsg.AsInt64()
		if name == "" || name == w.cfg.InstanceID || pending > 0 || idle < staleConsumerIdle.Milliseconds() {
			continue
		}
		if err := w.rdb.Do(ctx, w.rdb.B().XgroupDelconsumer().Key(stream).Group(ConsumerGroup).Consumername(name).Build()).Error(); err != nil {
			log.Debug("remove stale stream consumer", slog.String("consumer", name), slog.Any("error", err))
		}
	}
}

// pendingCount returns the number of pending entries of a shard's group.
func (w *Worker) pendingCount(ctx context.Context, shard int) (int64, error) {
	return parsePending(w.rdb.Do(ctx, w.pendingCommand(shard)), shard)
}

// pendingCommand builds the XPENDING summary command of a shard.
func (w *Worker) pendingCommand(shard int) rueidis.Completed {
	return w.rdb.B().Xpending().Key(w.keys.Stream(shard)).Group(ConsumerGroup).Build()
}

// parsePending extracts the pending count from an XPENDING summary reply.
func parsePending(res rueidis.RedisResult, shard int) (int64, error) {
	arr, err := res.ToArray()
	if err != nil {
		return 0, fmt.Errorf("xpending shard %d: %w", shard, err)
	}
	if len(arr) == 0 {
		return 0, fmt.Errorf("xpending shard %d: empty reply", shard)
	}
	n, err := arr[0].AsInt64()
	if err != nil {
		return 0, fmt.Errorf("xpending shard %d count: %w", shard, err)
	}
	return n, nil
}

func isBusyGroup(err error) bool { return redisErrPrefix(err, "BUSYGROUP") }

func isNoGroup(err error) bool { return redisErrPrefix(err, "NOGROUP") }

// redisErrPrefix reports whether err wraps a Redis error reply starting with prefix.
func redisErrPrefix(err error, prefix string) bool {
	var rerr *rueidis.RedisError
	return errors.As(err, &rerr) && !rerr.IsNil() && strings.HasPrefix(rerr.Error(), prefix)
}
