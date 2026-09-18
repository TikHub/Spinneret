// Package worker consumes the report streams (spec §6.3): it registers the
// instance in the worker registry, balances stream shard ownership between the
// live instances, and runs one sequential consumer per owned shard. Each report
// is classified with the endpoint group's signal policy, applied to the Redis
// hot state atomically by observe.lua (idempotent per stream entry), evaluated
// against the action policy, executed, released when requested and recorded
// for statistics.
package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	randv2 "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Defaults (spec §6.3, §12).
const (
	DefaultReportShards      = 16
	MaxReportShards          = 256
	DefaultLateReportWindow  = 10 * time.Minute
	DefaultBatchSize         = 100
	MaxBatchSize             = 1000
	DefaultBlock             = 2 * time.Second
	DefaultHeartbeatInterval = 2 * time.Second
	DefaultLiveWindow        = 10 * time.Second
	DefaultOwnerTTL          = 10 * time.Second
	DefaultOwnerRefresh      = 3 * time.Second
	DefaultPendingInterval   = 10 * time.Second

	// ConsumerGroup is the consumer group of every report stream.
	ConsumerGroup = "workers"
)

// Config configures a Worker. Zero values select the defaults above; the
// interval fields exist mainly so tests can run the registry faster.
type Config struct {
	// ReportShards is the number of report stream shards (SPINNERET_REPORT_SHARDS).
	ReportShards int
	// InstanceID identifies this instance in the registry, as shard owner and
	// as stream consumer (SPINNERET_INSTANCE_ID).
	InstanceID string
	// LateReportWindow is SPINNERET_LATE_REPORT_WINDOW.
	LateReportWindow time.Duration
	// BatchSize is the XREADGROUP/XAUTOCLAIM COUNT (default 100).
	BatchSize int
	// Block is the XREADGROUP BLOCK timeout (default 2s).
	Block time.Duration

	// HeartbeatInterval is the registry heartbeat and rebalance period (2s).
	HeartbeatInterval time.Duration
	// LiveWindow is how recent a heartbeat must be to count as live (10s).
	LiveWindow time.Duration
	// OwnerTTL is the shard owner lock TTL (10s).
	OwnerTTL time.Duration
	// OwnerRefresh is the shard owner lock refresh period (3s).
	OwnerRefresh time.Duration
	// PendingInterval is the stream_pending gauge refresh period (10s).
	PendingInterval time.Duration
	// TrimInterval is how often an owned shard stream is trimmed to the
	// consumer group position (DefaultTrimInterval).
	TrimInterval time.Duration
}

// ActionExecutor applies planned actions (provided by the action track).
type ActionExecutor interface {
	Execute(ctx context.Context, in ExecInput) (ExecResult, error)
}

// IdempotentActionExecutor is optionally implemented by an ActionExecutor
// whose Execute is idempotent per report: a repeated call for the same report
// (ReportContext.LeaseID and ReportID; the worker also passes the same Now and
// Planned) returns the results of the call that applied the actions instead
// of applying them again or reporting them as skipped. A failed Execute call
// may have applied its actions before failing (for example a client-side
// timeout after the script ran), so the worker retries a failed call only
// when IdempotentExecute reports true; otherwise the call is made once.
type IdempotentActionExecutor interface {
	ActionExecutor
	IdempotentExecute() bool
}

// LeaseReleaser releases a lease after its last report (provided by the scheduler).
type LeaseReleaser interface {
	ReleaseLease(ctx context.Context, siteKey int64, leaseID string, now time.Time) (bool, error)
}

// BreakerNotifier is told about risk outcomes so the breaker is evaluated
// promptly (provided by the breaker track). It must not block.
type BreakerNotifier interface {
	NotifyRisk(siteKey, egKey int64)
}

// StatsRecorder aggregates processed reports (provided by the stats track). It
// must not block.
type StatsRecorder interface {
	RecordReport(ReportRecord)
}

// ReportContext mirrors action.ReportContext: the report an action set was
// planned for.
type ReportContext struct {
	Namespace   *catalog.Namespace
	Site        *catalog.Site
	Group       *catalog.EndpointGroup
	LeaseID     string
	ReportID    string
	IdentityKey int64
	IdentityID  string
	AccountKey  int64
	ProxyKey    int64
	ProxyID     string
	Outcome     string
	RuleName    string
}

// ExecInput mirrors action.ExecInput.
type ExecInput struct {
	ReportContext
	Planned []policy.PlannedAction
	Shadow  bool
	Now     time.Time
}

// AppliedAction mirrors action.AppliedAction.
type AppliedAction struct {
	Planned     policy.PlannedAction
	SubjectKind policy.SubjectKind
	SubjectID   string
	FromState   string
	ToState     string
	Until       time.Time
	Skipped     bool
	SkipReason  string
}

// ExecResult mirrors action.ExecResult.
type ExecResult struct {
	Applied []AppliedAction
}

// ReportRecord mirrors stats.ReportRecord.
type ReportRecord struct {
	ReceivedAt, StartedAt, FinishedAt time.Time

	TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client      string
	IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID, URI, Method string

	HTTPStatus              int
	BusinessCode, ErrorKind string
	Markers                 []string

	Outcome, OutcomeHint, Blame, Rule string
	LatencyMs, ResponseBytes          int64
	Suppressed, Late, Probe           bool
}

// Worker is the report stream consumer of one instance.
type Worker struct {
	cfg     Config
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	exec    ActionExecutor
	rel     LeaseReleaser
	brk     BreakerNotifier
	rec     StatsRecorder
	metrics *observability.Metrics
	logger  *slog.Logger

	observeScript  *redis.Script
	registryScript *redis.Script
	// fallbackSignal and fallbackAction are the compiled built-in policies used
	// when a snapshot lacks compiled policies.
	fallbackSignal *policy.CompiledSignal
	fallbackAction *policy.CompiledAction

	// now and jitter are replaceable in tests.
	now    func() time.Time
	jitter func() float64

	running atomic.Bool
	// bg tracks goroutines that finalize released or lost shards.
	bg sync.WaitGroup

	mu     sync.Mutex
	shards map[int]*shardState
	// catalogRefresh remembers the last on-demand catalog reload per namespace
	// (see refreshNamespace); guarded by mu.
	catalogRefresh map[string]time.Time
}

// catalogRefreshInterval is the minimum delay between two on-demand catalog
// reloads of the same namespace in the worker.
const catalogRefreshInterval = 2 * time.Second

// New creates a Worker. exec, rel, brk, rec and metrics may be nil (the
// corresponding step is skipped). An empty InstanceID is replaced by a random
// one.
func New(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, exec ActionExecutor, rel LeaseReleaser, brk BreakerNotifier, rec StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	cfg = normalizeConfig(cfg, logger)
	return &Worker{
		cfg:     cfg,
		rdb:     rdb,
		keys:    keys,
		cat:     cat,
		exec:    exec,
		rel:     rel,
		brk:     brk,
		rec:     rec,
		metrics: metrics,
		logger:  logger.With(slog.String("component", "worker"), slog.String("instance", cfg.InstanceID)),

		observeScript:  redis.NewScript("observe", observeLua),
		registryScript: redis.NewScript("worker_registry", registryLua),
		fallbackSignal: compileFallbackSignal(logger),
		fallbackAction: compileFallbackAction(logger),

		now:            time.Now,
		jitter:         randv2.Float64,
		shards:         map[int]*shardState{},
		catalogRefresh: map[string]time.Time{},
	}
}

// compileFallbackSignal compiles the built-in signal policy; a nil result
// classifies every report as unknown.
func compileFallbackSignal(logger *slog.Logger) *policy.CompiledSignal {
	spec, ok := policy.Default(policy.KindSignal).(*policy.SignalSpec)
	if !ok {
		return nil
	}
	c, err := policy.CompileSignal([]*policy.SignalSpec{spec})
	if err != nil {
		logger.Error("compile built-in signal policy", slog.Any("error", err))
		return nil
	}
	return c
}

// compileFallbackAction compiles the built-in action policy; a nil result
// disables health updates and actions for groups without a compiled policy.
func compileFallbackAction(logger *slog.Logger) *policy.CompiledAction {
	spec, ok := policy.Default(policy.KindAction).(*policy.ActionSpec)
	if !ok {
		return nil
	}
	c, err := policy.CompileAction([]*policy.ActionSpec{spec})
	if err != nil {
		logger.Error("compile built-in action policy", slog.Any("error", err))
		return nil
	}
	return c
}

// InstanceID returns the registry / consumer name of this worker.
func (w *Worker) InstanceID() string { return w.cfg.InstanceID }

// OwnedShards returns the shards whose consumer is currently running, sorted.
func (w *Worker) OwnedShards() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]int, 0, len(w.shards))
	for shard := 0; shard < w.cfg.ReportShards; shard++ {
		if st, ok := w.shards[shard]; ok && st.phase == phaseOwned {
			out = append(out, shard)
		}
	}
	return out
}

func normalizeConfig(cfg Config, logger *slog.Logger) Config {
	if cfg.ReportShards <= 0 {
		cfg.ReportShards = DefaultReportShards
	}
	if cfg.ReportShards > MaxReportShards {
		logger.Warn("report shard count clamped", slog.Int("configured", cfg.ReportShards), slog.Int("max", MaxReportShards))
		cfg.ReportShards = MaxReportShards
	}
	if cfg.InstanceID == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		cfg.InstanceID = "worker-" + hex.EncodeToString(b[:])
		logger.Warn("worker instance id not configured; generated one", slog.String("instance", cfg.InstanceID))
	}
	if cfg.LateReportWindow <= 0 {
		cfg.LateReportWindow = DefaultLateReportWindow
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.BatchSize > MaxBatchSize {
		cfg.BatchSize = MaxBatchSize
	}
	if cfg.Block <= 0 {
		cfg.Block = DefaultBlock
	}
	setDuration(&cfg.HeartbeatInterval, DefaultHeartbeatInterval)
	setDuration(&cfg.LiveWindow, DefaultLiveWindow)
	setDuration(&cfg.OwnerTTL, DefaultOwnerTTL)
	setDuration(&cfg.OwnerRefresh, DefaultOwnerRefresh)
	setDuration(&cfg.PendingInterval, DefaultPendingInterval)
	if cfg.OwnerRefresh >= cfg.OwnerTTL {
		cfg.OwnerRefresh = cfg.OwnerTTL / 3
	}
	if cfg.LiveWindow <= cfg.HeartbeatInterval {
		cfg.LiveWindow = 5 * cfg.HeartbeatInterval
	}
	return cfg
}

func setDuration(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}
