package configcenter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter/configdb"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/observability"
)

// Defaults applied by New to zero Config fields.
const (
	DefaultMaxWatchers         = 20_000
	DefaultMaxWatchTimeout     = 60 * time.Second
	DefaultWatchTimeout        = 30 * time.Second
	DefaultResyncInterval      = 30 * time.Second
	DefaultMaxCachedVersions   = 100_000
	DefaultContentCacheBytes   = 64 << 20
	DefaultMaxResponseBytes    = 32 << 20
	DefaultOperationTimeout    = 15 * time.Second
	defaultContentCacheEntryMx = 256 << 10
)

// Config tunes the config center. Zero values select the defaults above.
type Config struct {
	// MaxWatchers bounds the number of WatchConfig calls blocked at the same
	// time on this instance (SPINNERET_MAX_WATCHERS); further calls fail with
	// resource_exhausted / rate_limited.
	MaxWatchers int
	// MaxWatchTimeout caps the long-poll duration requested by clients.
	MaxWatchTimeout time.Duration
	// DefaultWatchTimeout is used when a client sends timeout_ms = 0.
	DefaultWatchTimeout time.Duration
	// ResyncInterval is how often versions of watched items are re-read from
	// the source of truth (a safety net for lost bus events) and how long
	// versions of unwatched items stay cached.
	ResyncInterval time.Duration
	// MaxCachedVersions bounds the number of cached versions of items that no
	// watcher currently waits on.
	MaxCachedVersions int
	// ContentCacheBytes bounds the memory of the published-content cache.
	ContentCacheBytes int64
	// MaxResponseBytes bounds the combined content size returned by one node
	// read (BatchGetConfig fails beyond it; WatchConfig returns a subset and
	// the remaining changed items on the next poll).
	MaxResponseBytes int64
	// OperationTimeout bounds each database or provider operation.
	OperationTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.MaxWatchers <= 0 {
		c.MaxWatchers = DefaultMaxWatchers
	}
	if c.MaxWatchTimeout <= 0 {
		c.MaxWatchTimeout = DefaultMaxWatchTimeout
	}
	if c.DefaultWatchTimeout <= 0 {
		c.DefaultWatchTimeout = DefaultWatchTimeout
	}
	if c.DefaultWatchTimeout > c.MaxWatchTimeout {
		c.DefaultWatchTimeout = c.MaxWatchTimeout
	}
	if c.ResyncInterval <= 0 {
		c.ResyncInterval = DefaultResyncInterval
	}
	if c.MaxCachedVersions <= 0 {
		c.MaxCachedVersions = DefaultMaxCachedVersions
	}
	if c.ContentCacheBytes <= 0 {
		c.ContentCacheBytes = DefaultContentCacheBytes
	}
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if c.OperationTimeout <= 0 {
		c.OperationTimeout = DefaultOperationTimeout
	}
	return c
}

// SecretValue is a resolved secret as returned by a SecretReader. It mirrors
// vault.SecretValue; the server adapts the vault secret store to SecretReader.
type SecretValue struct {
	Path      string
	Version   int
	Value     string
	ExpiresAt *time.Time
}

// SecretReader reads a secret on behalf of a principal. Implementations
// enforce secret permissions (returning apperr permission errors) and write
// the secret access audit log. version 0 reads the current version.
type SecretReader interface {
	ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error)
}

// RuntimeProvider serves the read-only "_runtime" items (kind "breakers" or
// "site_switches") and their versions. It is provided by the breaker service.
type RuntimeProvider interface {
	RuntimeContent(ctx context.Context, namespaceID, kind string) (string, int64, error)
	RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error)
}

// Service implements the config center: config items, drafts, immutable
// versions, publish/rollback, the node read API with secret reference
// resolution and the long-poll watch hub.
type Service struct {
	cfg      Config
	pool     *pgxpool.Pool
	cat      catalog.Catalog
	bus      events.Bus
	secrets  SecretReader
	runtime  RuntimeProvider
	audit    audit.Recorder
	metrics  *observability.Metrics
	logger   *slog.Logger
	hub      *hub
	contents *contentCache
	runtimes *runtimeCache
}

// New creates the config center service. secrets and runtime may be nil:
// without a SecretReader items with secret references cannot be served to
// nodes (failed_precondition), without a RuntimeProvider the "_runtime" items
// do not exist. Run must be started for WatchConfig to observe changes.
func New(cfg Config, pool *pgxpool.Pool, cat catalog.Catalog, bus events.Bus, secrets SecretReader, runtime RuntimeProvider,
	rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger,
) *Service {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if rec == nil {
		rec = audit.Nop{}
	}
	if bus == nil {
		bus = events.NewMemoryBus()
	}
	s := &Service{
		cfg:      cfg,
		pool:     pool,
		cat:      cat,
		bus:      bus,
		secrets:  secrets,
		runtime:  runtime,
		audit:    rec,
		metrics:  metrics,
		logger:   logger.With(slog.String("component", "configcenter")),
		contents: newContentCache(cfg.ContentCacheBytes, min(defaultContentCacheEntryMx, cfg.ContentCacheBytes)),
		runtimes: newRuntimeCache(),
	}
	hubCfg := hubConfig{
		maxWatchers: cfg.MaxWatchers,
		maxEntries:  cfg.MaxCachedVersions,
		ttl:         cfg.ResyncInterval,
	}
	if metrics != nil {
		hubCfg.gauge = metrics.ConfigWatchers
	}
	s.hub = newHub(hubCfg, s.loadVersions)
	return s
}

// Run subscribes to the config and runtime bus channels and keeps the watch
// hub's version cache fresh (event-driven reloads plus a periodic resync of
// watched items) until ctx is done. It returns nil on cancellation.
func (s *Service) Run(ctx context.Context) error {
	unsubConfig := s.bus.Subscribe(events.ChannelConfig, s.onConfigEvent)
	defer unsubConfig()
	unsubRuntime := s.bus.Subscribe(events.ChannelRuntime, s.onRuntimeEvent)
	defer unsubRuntime()
	s.hub.run(ctx, s.cfg.OperationTimeout, s.logger)
	return nil
}

// queries returns sqlc queries bound to the pool.
func (s *Service) queries() *configdb.Queries {
	return configdb.New(s.pool)
}

// opContext bounds one operation with the configured timeout.
func (s *Service) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.cfg.OperationTimeout)
}

// wrapErr wraps an unexpected error with context while keeping application
// errors and context errors unchanged.
func wrapErr(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	if isAppErr(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}
