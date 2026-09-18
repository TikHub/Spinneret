package notify

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/notify/notifydb"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Defaults of Config.
const (
	DefaultWorkers         = 4
	DefaultQueueSize       = 10_000
	DefaultReportShards    = 16
	DefaultAttempts        = 3
	DefaultRetryDelay      = 2 * time.Second
	DefaultChannelCacheTTL = 10 * time.Second
	defaultBusQueueSize    = 4096
	maxWorkers             = 64
	maxQueueSize           = 1_000_000
	maxAttempts            = 10
)

// ErrRunning is returned by Run when the service is already running.
var ErrRunning = errors.New("notify: service is already running")

// Config configures the service. Zero values select the defaults.
type Config struct {
	// Workers is the number of concurrent delivery workers (default 4).
	Workers int
	// QueueSize bounds pending deliveries (default 10000); deliveries that do
	// not fit are recorded as failed ("delivery queue full").
	QueueSize int
	// ReportShards is the number of report stream shards inspected by the
	// report_backlog rule (SPINNERET_REPORT_SHARDS, default 16).
	ReportShards int
	// Attempts per delivery (default 3) with exponential backoff starting at
	// RetryDelay (default 2s). A channel whose previous delivery failed within
	// the last 5 minutes gets a single attempt until a delivery succeeds.
	Attempts   int
	RetryDelay time.Duration
	// HTTPTimeout bounds one delivery request (default 10s).
	HTTPTimeout time.Duration
	// ChannelCacheTTL is how long enabled channels of a tenant are cached for
	// alert matching (default 10s).
	ChannelCacheTTL time.Duration
}

func (c Config) withDefaults() Config {
	if c.Workers <= 0 {
		c.Workers = DefaultWorkers
	}
	c.Workers = min(c.Workers, maxWorkers)
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultQueueSize
	}
	c.QueueSize = min(c.QueueSize, maxQueueSize)
	if c.ReportShards <= 0 {
		c.ReportShards = DefaultReportShards
	}
	if c.Attempts <= 0 {
		c.Attempts = DefaultAttempts
	}
	c.Attempts = min(c.Attempts, maxAttempts)
	if c.RetryDelay <= 0 {
		c.RetryDelay = DefaultRetryDelay
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = DefaultHTTPTimeout
	}
	if c.ChannelCacheTTL <= 0 {
		c.ChannelCacheTTL = DefaultChannelCacheTTL
	}
	return c
}

// Service manages notification channels, emits and delivers alerts and
// evaluates alert rules. It is safe for concurrent use.
type Service struct {
	cfg     Config
	pool    *pgxpool.Pool
	q       *notifydb.Queries
	cipher  *vault.Cipher
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	bus     events.Bus
	audit   audit.Recorder
	metrics *observability.Metrics
	logger  *slog.Logger
	now     func() time.Time
	sender  *sender

	queue    chan deliveryJob
	busQueue chan busItem
	running  atomic.Bool

	channels *channelCache
	failing  *failingChannels

	droppedDeliveries atomic.Int64
	droppedBusEvents  atomic.Int64

	// expiredRecoveryBudget reports whether a recovery run may emit another
	// alert; nil selects expiredRecoveryTimeBudget (tests override it).
	expiredRecoveryBudget func() bool

	banMu       sync.Mutex
	banBaseline banBaseline
}

// New creates the notification service. audit, metrics, bus and logger may be
// nil.
func New(cfg Config, pool *pgxpool.Pool, cipher *vault.Cipher, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog,
	bus events.Bus, rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger,
) *Service {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if rec == nil {
		rec = audit.Nop{}
	}
	s := &Service{
		cfg:      cfg,
		pool:     pool,
		q:        notifydb.New(pool),
		cipher:   cipher,
		rdb:      rdb,
		keys:     keys,
		cat:      cat,
		bus:      bus,
		audit:    rec,
		metrics:  metrics,
		logger:   logger.With(slog.String("component", "notify")),
		now:      time.Now,
		queue:    make(chan deliveryJob, cfg.QueueSize),
		busQueue: make(chan busItem, defaultBusQueueSize),
		channels: newChannelCache(cfg.ChannelCacheTTL),
		failing:  newFailingChannels(),
	}
	s.sender = &sender{client: newHTTPClient(cfg.HTTPTimeout), now: func() time.Time { return s.now() }}
	return s
}

// Run starts the delivery workers and the bus subscription (breaker
// transitions and identity expiry become alerts) and blocks until ctx is
// canceled. Deliveries still queued at shutdown are dropped. Identity expiry
// alerts lost by the bus path are recovered by the evaluation job (see
// recoverExpiredIdentities).
func (s *Service) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer s.running.Store(false)

	var wg sync.WaitGroup
	for range s.cfg.Workers {
		wg.Go(func() { s.deliveryWorker(ctx) })
	}
	if s.bus != nil {
		unsubscribe := s.bus.Subscribe(events.ChannelAll, s.onBusEvent)
		defer unsubscribe()
		wg.Go(func() { s.busLoop(ctx) })
	}
	<-ctx.Done()
	wg.Wait()
	if n := len(s.queue); n > 0 {
		s.logger.Warn("notify stopped with pending deliveries", slog.Int("pending", n))
	}
	return nil
}

// countDelivery records a delivery outcome metric.
func (s *Service) countDelivery(kind, result string) {
	if s.metrics == nil {
		return
	}
	s.metrics.NotifyDeliveries.WithLabelValues(observability.Label(kind), result).Inc()
}

// Delivery metric results.
const (
	resultOK      = "ok"
	resultError   = "error"
	resultDropped = "dropped"
)
