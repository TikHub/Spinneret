// Package peers maintains the registry of live API instances. The acquire
// admission gate divides a fleet-wide concurrency budget by this count, so the
// total concurrency the fleet puts on Redis does not grow with the replica
// count.
//
// The registry is a heartbeat, not a controller: every instance records its own
// timestamp in a sorted set scored by the Redis server clock, prunes the
// members whose heartbeat fell out of the live window and reads back how many
// are left. There is no feedback signal and nothing to oscillate. It is also
// best-effort by design: a Redis failure degrades to the last observed count
// and is logged, never returned to the request path, because a registry problem
// must not be able to take an API instance down or to widen the gate.
package peers

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/store/redis"
)

//go:embed lua/registry.lua
var registryLua string

const (
	// BeatInterval is how often an instance refreshes its heartbeat.
	BeatInterval = 2 * time.Second
	// LiveWindow is how recent a heartbeat must be to count as live. It is
	// deliberately 30 beats, not two or three.
	//
	// A graceful shutdown deregisters the instance itself (see Run), so this
	// window governs only the detection of a *crashed* instance — and a crashed
	// instance's stale membership makes the survivors' share narrower, which is
	// the safe direction. A tight window is the unsafe direction: a beat needs
	// Redis, and the moment admission control matters is the moment Redis is
	// saturated, so beats are exactly then at risk of exceeding opTimeout.
	// Measured under a two-replica overload with a 10 s window: one instance
	// missed enough beats to be pruned by the other, which then divided the
	// fleet budget by 1 and admitted all of it — widening the gate precisely
	// when it should have held, which is a positive feedback loop into the
	// collapse the gate exists to prevent. Thirty beats of slack costs a
	// crashed instance's share for up to a minute and removes that loop.
	LiveWindow = 60 * time.Second
	// opTimeout bounds one registry call.
	opTimeout = 2 * time.Second
)

// Config configures a Registry.
type Config struct {
	// InstanceID identifies this instance in the registry
	// (SPINNERET_INSTANCE_ID). An empty id is replaced by a random one, so two
	// misconfigured instances never collapse into one registry member and
	// widen the gate.
	InstanceID string
	// OnLive is called after every successful beat with the live member count
	// (always >= 1). It must not block.
	OnLive func(live int)
	// Now is replaced in tests. A zero value uses the real clock. It is only
	// used to report heartbeat staleness: liveness itself is decided by the
	// Redis server clock.
	Now func() time.Time
}

// Registry is the peer registry of one API instance.
type Registry struct {
	cfg    Config
	rdb    rueidis.Client
	keys   redis.Keys
	logger *slog.Logger
	script *redis.Script

	// live is the last observed member count; it starts at 1 so that the
	// divisor is never zero before the first beat.
	live atomic.Int64
	// lastBeat is the Unix nanosecond stamp of the last successful beat. It is
	// seeded at construction so that BeatAge grows from the start: a registry
	// whose beats have never succeeded must not be able to look fresh.
	lastBeat atomic.Int64
	// beatFailures counts failed beats. A permanently failing heartbeat would
	// otherwise be indistinguishable from a genuine single-instance deployment,
	// while the fleet quietly runs at instances times the configured budget.
	beatFailures atomic.Int64
	running      atomic.Bool
}

// New creates a Registry. A nil logger uses the default logger.
func New(cfg Config, rdb rueidis.Client, keys redis.Keys, logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = randomInstanceID()
		logger.Warn("api instance id not configured; generated one", slog.String("instance", cfg.InstanceID))
	}
	r := &Registry{
		cfg:    cfg,
		rdb:    rdb,
		keys:   keys,
		logger: logger.With(slog.String("component", "peers"), slog.String("instance", cfg.InstanceID)),
		script: redis.NewScript("peers_registry", registryLua),
	}
	r.live.Store(1)
	r.lastBeat.Store(cfg.Now().UnixNano())
	return r
}

// randomInstanceID returns a fallback instance id for an unconfigured instance.
func randomInstanceID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "api-" + hex.EncodeToString(b[:])
}

// InstanceID returns the registry member name of this instance.
func (r *Registry) InstanceID() string {
	if r == nil {
		return ""
	}
	return r.cfg.InstanceID
}

// Run beats immediately and then every BeatInterval until ctx is done, and
// deregisters this instance on the way out. It matches the loop signature used
// to start background services: a failed beat is logged and retried, so Run
// only returns an error when it is misconfigured or already running.
func (r *Registry) Run(ctx context.Context) error {
	if r.rdb == nil {
		return errors.New("peers: redis client is required")
	}
	if !r.running.CompareAndSwap(false, true) {
		return errors.New("peers: already running")
	}
	defer r.running.Store(false)
	// The departure is issued with a context detached from ctx: shutdown must
	// still remove this instance from the registry so the surviving instances
	// widen their share on their next beat instead of after the live window.
	defer r.leave(context.WithoutCancel(ctx))

	r.tick(ctx)
	beat := time.NewTicker(BeatInterval)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-beat.C:
			r.tick(ctx)
		}
	}
}

// tick performs one heartbeat and logs a failure instead of propagating it: a
// Redis problem keeps this instance's last known count, so its own gate can
// only stay as it is or narrow. Widening can still happen through the other
// side of the registry — a peer that stops beating is eventually pruned by the
// instances that are still beating — which is why LiveWindow is generous.
func (r *Registry) tick(ctx context.Context) {
	if _, err := r.Beat(ctx); err != nil && ctx.Err() == nil {
		r.logger.Warn("peer registry heartbeat failed",
			slog.Any("error", err),
			slog.Int("live", r.Live()),
			slog.Int64("failures", r.BeatFailures()),
			slog.Duration("stale_for", r.BeatAge()))
	}
}

// Beat performs one heartbeat and returns the live member count, which is
// always at least 1. It is exported so that callers and tests can drive the
// registry deterministically instead of waiting on the ticker. On failure the
// last observed count is returned together with the error, and OnLive is not
// called.
func (r *Registry) Beat(ctx context.Context) (int, error) {
	if r.rdb == nil {
		r.beatFailures.Add(1)
		return r.Live(), errors.New("peers: redis client is required")
	}
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	n, err := r.script.Exec(opCtx, r.rdb, []string{r.keys.Acquirers()},
		[]string{r.cfg.InstanceID, strconv.FormatInt(LiveWindow.Milliseconds(), 10)}).AsInt64()
	if err != nil {
		r.beatFailures.Add(1)
		return r.Live(), fmt.Errorf("peers registry beat: %w", err)
	}
	// The script counts this instance too, so a successful beat can only see
	// one member or more; the floor guards against a truncated reply.
	live := max(int(n), 1)
	r.live.Store(int64(live))
	r.lastBeat.Store(r.cfg.Now().UnixNano())
	if r.cfg.OnLive != nil {
		r.cfg.OnLive(live)
	}
	return live, nil
}

// Live returns the last observed member count, or 1 when no beat has succeeded
// yet. It never returns less than 1, so a caller dividing a budget by it can
// neither divide by zero nor derive an empty budget from a Redis outage.
func (r *Registry) Live() int {
	if r == nil {
		return 1
	}
	return max(int(r.live.Load()), 1)
}

// BeatAge returns how long ago the last successful beat was, measured from
// construction when none has succeeded yet. Exported so that it can be exposed
// as a gauge: a heartbeat that never succeeds keeps the member count at its
// initial 1, which is indistinguishable from a real single-instance deployment
// unless the age of the count is visible next to it.
func (r *Registry) BeatAge() time.Duration {
	if r == nil {
		return 0
	}
	return max(r.cfg.Now().Sub(time.Unix(0, r.lastBeat.Load())), 0)
}

// BeatFailures returns the number of beats that have failed. It only grows, so
// it is exposed as a counter.
func (r *Registry) BeatFailures() int64 {
	if r == nil {
		return 0
	}
	return r.beatFailures.Load()
}

// leave removes this instance from the registry. ctx must not be cancelled.
func (r *Registry) leave(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd := r.rdb.B().Zrem().Key(r.keys.Acquirers()).Member(r.cfg.InstanceID).Build()
	if err := r.rdb.Do(opCtx, cmd).Error(); err != nil {
		r.logger.Warn("leave peer registry failed", slog.Any("error", err))
	}
}
