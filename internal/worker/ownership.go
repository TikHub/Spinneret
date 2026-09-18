package worker

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

//go:embed lua/registry.lua
var registryLua string

// Registry script modes.
const (
	registryBeat = "beat"
	registryList = "list"
)

// shutdownWait bounds how long Run waits for consumers to finish their current
// event on shutdown; shards whose consumer is still busy are not unlocked and
// their locks simply expire.
const shutdownWait = 15 * time.Second

// shardPhase is the lifecycle phase of a locally tracked shard.
type shardPhase int

const (
	// phaseOwned: the lock is held and refreshed, the consumer runs.
	phaseOwned shardPhase = iota
	// phaseReleasing: the consumer is stopping; the lock is still refreshed and
	// is deleted once the consumer has exited.
	phaseReleasing
	// phaseLost: the lock was lost; the consumer is stopping and the lock is
	// neither refreshed nor deleted.
	phaseLost
)

// shardState tracks one shard this instance owns or is giving up.
type shardState struct {
	phase       shardPhase
	cancel      context.CancelFunc
	done        chan struct{}
	lastRefresh time.Time
}

// Run registers the instance, balances shard ownership and runs the shard
// consumers until ctx is cancelled. On return every consumer has stopped
// (bounded by shutdownWait), owned shard locks are released and the instance
// is removed from the registry. Redis errors are logged and retried; Run only
// returns an error when it is misconfigured or already running.
func (w *Worker) Run(ctx context.Context) error {
	if w.rdb == nil || w.cat == nil {
		return errors.New("worker: redis client and catalog are required")
	}
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("worker: already running")
	}
	defer w.running.Store(false)
	defer w.shutdown(context.WithoutCancel(ctx))

	w.tick(ctx)
	heartbeat := time.NewTicker(w.cfg.HeartbeatInterval)
	defer heartbeat.Stop()
	refresh := time.NewTicker(w.cfg.OwnerRefresh)
	defer refresh.Stop()
	pending := time.NewTicker(w.cfg.PendingInterval)
	defer pending.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			w.tick(ctx)
		case <-refresh.C:
			w.refreshOwnership(ctx)
		case <-pending.C:
			w.updatePending(ctx)
		}
	}
}

// Membership returns the position of this instance in the sorted list of live
// registry members and the number of live members, for work partitioning
// (e.g. proxy health checks: hkey % total == index). When this instance has no
// live heartbeat (Run is not running, has not sent its first heartbeat yet, or
// cannot reach Redis) index is -1 and err is nil: the instance simply owns no
// partition, which consumers such as the proxy health checker skip quietly.
// err is non-nil only when the registry cannot be read.
func (w *Worker) Membership(ctx context.Context) (index, total int, err error) {
	live, err := w.registry(ctx, registryList)
	if err != nil {
		return -1, 0, fmt.Errorf("worker membership: %w", err)
	}
	return slices.Index(live, w.cfg.InstanceID), len(live), nil
}

// registry runs registry.lua and returns the sorted live members.
func (w *Worker) registry(ctx context.Context, mode string) ([]string, error) {
	live, err := w.registryScript.Exec(ctx, w.rdb, []string{w.keys.Workers()},
		[]string{mode, w.cfg.InstanceID, strconv.FormatInt(w.cfg.LiveWindow.Milliseconds(), 10)}).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("worker registry %s: %w", mode, err)
	}
	slices.Sort(live)
	return live, nil
}

// tick sends the heartbeat and rebalances shard ownership.
func (w *Worker) tick(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	live, err := w.registry(opCtx, registryBeat)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("worker heartbeat failed", slog.Any("error", err))
		}
		return
	}
	w.rebalance(ctx, live)
}

// rebalance releases shards above the target share ceil(shards/live) and
// acquires free shards below it.
func (w *Worker) rebalance(ctx context.Context, live []string) {
	shards := w.cfg.ReportShards
	n := len(live)
	pos := slices.Index(live, w.cfg.InstanceID)
	if pos < 0 {
		pos = n
		n++
	}
	target := (shards + n - 1) / n

	w.mu.Lock()
	owned := make([]int, 0, len(w.shards))
	busy := make(map[int]bool, len(w.shards))
	for s, st := range w.shards {
		busy[s] = true
		if st.phase == phaseOwned {
			owned = append(owned, s)
		}
	}
	w.mu.Unlock()
	slices.Sort(owned)

	switch {
	case len(owned) > target:
		for _, s := range owned[target:] {
			w.releaseShard(ctx, s)
		}
	case len(owned) < target:
		need := target - len(owned)
		start := pos * shards / n
		for k := 0; k < shards && need > 0 && ctx.Err() == nil; k++ {
			s := (start + k) % shards
			if busy[s] {
				continue
			}
			ok, err := w.acquireShard(ctx, s)
			if err != nil {
				w.logger.Warn("acquire shard failed", slog.Int("shard", s), slog.Any("error", err))
				break
			}
			if ok {
				w.startShard(ctx, s)
				need--
			}
		}
	}
	w.updateOwnedGauge()
}

// acquireShard takes the owner lock of a free shard, or adopts a lock that
// already names this instance (e.g. after a quick restart).
func (w *Worker) acquireShard(ctx context.Context, shard int) (bool, error) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	key := w.keys.ShardOwner(shard)
	ok, err := redis.TryLock(opCtx, w.rdb, key, w.cfg.InstanceID, w.cfg.OwnerTTL)
	if err != nil || ok {
		return ok, err
	}
	return redis.RefreshLock(opCtx, w.rdb, key, w.cfg.InstanceID, w.cfg.OwnerTTL)
}

// startShard starts the consumer of a freshly acquired shard.
func (w *Worker) startShard(ctx context.Context, shard int) {
	cctx, cancel := context.WithCancel(ctx)
	st := &shardState{phase: phaseOwned, cancel: cancel, done: make(chan struct{}), lastRefresh: time.Now()}
	w.mu.Lock()
	w.shards[shard] = st
	w.mu.Unlock()
	w.logger.Info("acquired report shard", slog.Int("shard", shard))
	go w.consume(cctx, shard, st.done)
}

// releaseShard stops a shard consumer and deletes the owner lock once the
// consumer has exited, so the next owner never overlaps with it. The lock is
// deleted with a context detached from ctx so that shutdown still releases it.
func (w *Worker) releaseShard(ctx context.Context, shard int) {
	w.mu.Lock()
	st := w.shards[shard]
	if st == nil || st.phase != phaseOwned {
		w.mu.Unlock()
		return
	}
	st.phase = phaseReleasing
	w.mu.Unlock()
	st.cancel()
	w.logger.Info("releasing report shard", slog.Int("shard", shard))
	w.bg.Add(1)
	go func() {
		defer w.bg.Done()
		<-st.done
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
		defer cancel()
		if err := redis.Unlock(opCtx, w.rdb, w.keys.ShardOwner(shard), w.cfg.InstanceID); err != nil {
			w.logger.Warn("release shard lock failed", slog.Int("shard", shard), slog.Any("error", err))
		}
		w.forgetShard(shard, st)
	}()
}

// loseShard stops the consumer of a shard whose lock is no longer held.
func (w *Worker) loseShard(shard int, st *shardState) {
	w.mu.Lock()
	if w.shards[shard] != st || st.phase != phaseOwned {
		w.mu.Unlock()
		return
	}
	st.phase = phaseLost
	w.mu.Unlock()
	st.cancel()
	w.logger.Warn("lost report shard ownership", slog.Int("shard", shard))
	w.bg.Add(1)
	go func() {
		defer w.bg.Done()
		<-st.done
		w.forgetShard(shard, st)
	}()
	w.updateOwnedGauge()
}

// forgetShard removes a stopped shard from the local table.
func (w *Worker) forgetShard(shard int, st *shardState) {
	w.mu.Lock()
	if w.shards[shard] == st {
		delete(w.shards, shard)
	}
	w.mu.Unlock()
	if w.metrics != nil {
		w.metrics.StreamPending.DeleteLabelValues(strconv.Itoa(shard))
	}
}

// refreshOwnership extends the owner locks of owned and releasing shards. A
// shard whose lock is gone — or could not be refreshed for longer than the
// lock can survive — is stopped.
func (w *Worker) refreshOwnership(ctx context.Context) {
	type item struct {
		shard int
		st    *shardState
		last  time.Time
	}
	w.mu.Lock()
	items := make([]item, 0, len(w.shards))
	for s, st := range w.shards {
		if st.phase != phaseLost {
			items = append(items, item{shard: s, st: st, last: st.lastRefresh})
		}
	}
	w.mu.Unlock()

	// After the first failure in a round the remaining locks are not retried
	// (Redis is most likely unreachable, and every attempt could block for
	// opTimeout); only the staleness rule is applied to them.
	var roundErr error
	stale := w.cfg.OwnerTTL - w.cfg.OwnerRefresh
	for _, it := range items {
		if ctx.Err() != nil {
			return
		}
		if roundErr == nil {
			opCtx, cancel := context.WithTimeout(ctx, opTimeout)
			ok, err := redis.RefreshLock(opCtx, w.rdb, w.keys.ShardOwner(it.shard), w.cfg.InstanceID, w.cfg.OwnerTTL)
			cancel()
			switch {
			case err == nil && ok:
				w.mu.Lock()
				it.st.lastRefresh = time.Now()
				w.mu.Unlock()
				continue
			case err == nil:
				w.loseShard(it.shard, it.st)
				continue
			default:
				roundErr = err
			}
		}
		if time.Since(it.last) > stale {
			w.logger.Warn("shard lock refresh failing; stopping consumer", slog.Int("shard", it.shard), slog.Any("error", roundErr))
			w.loseShard(it.shard, it.st)
			continue
		}
		w.logger.Debug("shard lock refresh failed", slog.Int("shard", it.shard), slog.Any("error", roundErr))
	}
}

// updatePending refreshes the stream_pending gauge of owned shards with one
// pipelined XPENDING summary per shard, bounded by a single timeout so that a
// slow Redis cannot delay the lock refreshes sharing the Run loop.
func (w *Worker) updatePending(ctx context.Context) {
	if w.metrics == nil {
		return
	}
	shards := w.OwnedShards()
	if len(shards) == 0 {
		return
	}
	cmds := make(rueidis.Commands, len(shards))
	for i, shard := range shards {
		cmds[i] = w.pendingCommand(shard)
	}
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	for i, res := range w.rdb.DoMulti(opCtx, cmds...) {
		n, err := parsePending(res, shards[i])
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Debug("read stream pending count", slog.Int("shard", shards[i]), slog.Any("error", err))
			}
			continue
		}
		w.metrics.StreamPending.WithLabelValues(strconv.Itoa(shards[i])).Set(float64(n))
	}
}

func (w *Worker) updateOwnedGauge() {
	if w.metrics != nil {
		w.metrics.StreamOwnedShards.Set(float64(len(w.OwnedShards())))
	}
}

// shutdown stops every consumer, releases the locks this instance still holds
// and leaves the registry. ctx must not be cancelled (Run passes a context
// detached from its own).
func (w *Worker) shutdown(ctx context.Context) {
	w.mu.Lock()
	states := make(map[int]*shardState, len(w.shards))
	for s, st := range w.shards {
		states[s] = st
	}
	w.mu.Unlock()
	for _, st := range states {
		st.cancel()
	}

	deadline := time.NewTimer(shutdownWait)
	defer deadline.Stop()
	expired := false
	stopped := make(map[int]*shardState, len(states))
	for shard, st := range states {
		if !waitDone(st.done, deadline.C, &expired) {
			w.logger.Warn("report shard consumer did not stop in time; leaving its lock to expire", slog.Int("shard", shard))
			continue
		}
		stopped[shard] = st
	}
	if !expired {
		w.bg.Wait()
	}

	// The Redis timeout starts only now: waiting for consumers must not eat
	// into the time left to release the locks and leave the registry.
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	for shard, st := range stopped {
		if st.phase != phaseLost {
			if err := redis.Unlock(opCtx, w.rdb, w.keys.ShardOwner(shard), w.cfg.InstanceID); err != nil {
				w.logger.Warn("release shard lock failed", slog.Int("shard", shard), slog.Any("error", err))
			}
		}
		w.forgetShard(shard, st)
	}
	if err := w.rdb.Do(opCtx, w.rdb.B().Zrem().Key(w.keys.Workers()).Member(w.cfg.InstanceID).Build()).Error(); err != nil {
		w.logger.Warn("leave worker registry failed", slog.Any("error", err))
	}
	w.updateOwnedGauge()
}

// waitDone waits for done until the shared deadline fires; once it has fired
// (*expired) it only polls. It reports whether done is closed.
func waitDone(done <-chan struct{}, deadline <-chan time.Time, expired *bool) bool {
	if *expired {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	select {
	case <-done:
		return true
	case <-deadline:
		*expired = true
		return false
	}
}
