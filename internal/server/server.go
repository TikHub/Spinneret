// Package server assembles a Spinneret instance: infrastructure clients, domain
// services, background loops and jobs, and the HTTP server with every Connect
// handler, health endpoints, metrics, the SSE event stream and the embedded
// console.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/observability"
	chstore "github.com/TikHub/Spinneret/internal/store/clickhouse"
	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Shutdown and startup tuning.
const (
	maxDrainDelay      = 5 * time.Second
	closeTimeout       = 10 * time.Second
	tracingStopTimeout = 5 * time.Second
	serviceName        = "spinneret"
)

// Options configures New.
type Options struct {
	// Logger receives every log record of the instance. When nil a logger is
	// built from SPINNERET_LOG_LEVEL / SPINNERET_LOG_FORMAT writing to stdout.
	Logger *slog.Logger
	// Version is the build version reported in logs and traces.
	Version string
	// Migrate applies pending PostgreSQL migrations at startup instead of
	// failing when the schema is behind the binary.
	Migrate bool
}

// Server is one Spinneret instance.
type Server struct {
	cfg     appconfig.Config
	opts    Options
	logger  *slog.Logger
	metrics *observability.Metrics

	infra           *infra
	c               *components
	runner          *jobs.Runner // nil unless the role runs workers
	loopRestarts    *prometheus.CounterVec
	shutdownTracing func(context.Context) error
	tracingEnabled  bool

	health          *healthHandlers
	httpServer      *http.Server
	listener        net.Listener
	metricsServer   *http.Server // nil unless SPINNERET_METRICS_ADDR is set
	metricsListener net.Listener
	pprofServer     *http.Server // nil unless SPINNERET_PPROF_ADDR is set
	pprofListener   net.Listener
	drainCtx        context.Context
	startDrain      context.CancelFunc

	hotStateReady atomic.Bool
	running       atomic.Bool
	closeOnce     sync.Once
	closeErr      error
}

// New connects every dependency, verifies the database schema, wires all
// services and binds the HTTP listener(s). It does not start serving: call
// Run. On error every resource opened so far is released.
func New(ctx context.Context, cfg appconfig.Config, opts Options) (_ *Server, err error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	logger := opts.Logger
	if logger == nil {
		if logger, err = observability.NewLogger(cfg.LogLevel, cfg.LogFormat, os.Stdout); err != nil {
			return nil, fmt.Errorf("create logger: %w", err)
		}
	}
	logger = logger.With(slog.String("instance", cfg.InstanceID))
	if opts.Version == "" {
		opts.Version = "dev"
	}
	s := &Server{cfg: cfg, opts: opts, logger: logger, metrics: observability.NewMetrics(), infra: &infra{}}
	s.drainCtx, s.startDrain = context.WithCancel(context.Background())
	defer func() {
		if err != nil {
			if cerr := s.Close(); cerr != nil {
				logger.Warn("release resources after failed startup", slog.Any("error", cerr))
			}
		}
	}()
	logger.Info("starting spinneret", slog.String("version", opts.Version), slog.Any("config", cfg.Redacted()))

	if s.shutdownTracing, err = observability.SetupTracing(ctx, cfg.OTLPEndpoint, serviceName, opts.Version); err != nil {
		return nil, fmt.Errorf("set up tracing: %w", err)
	}
	s.tracingEnabled = cfg.OTLPEndpoint != ""
	if err := s.openInfra(ctx); err != nil {
		return nil, err
	}
	if s.loopRestarts, err = newLoopRestartCounter(s.metrics.Registry); err != nil {
		return nil, err
	}
	if s.c, err = buildComponents(cfg, s.infra, s.metrics, logger); err != nil {
		return nil, err
	}
	if err := s.c.catalog.ReloadAll(ctx); err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	if cfg.Role.RunsWorkers() {
		jm, err := newJobMetrics(s.metrics.Registry)
		if err != nil {
			return nil, err
		}
		s.runner = jobs.NewRunner(jobs.NewPGLocks(s.infra.pool), logger.With(slog.String("component", "jobs")), jm)
		leaders, err := s.c.registerJobs(s.runner)
		if err != nil {
			return nil, err
		}
		if err := checkPoolSize(cfg.DatabaseMaxConns, leaders); err != nil {
			return nil, err
		}
	}
	if err := s.setupHTTP(); err != nil {
		return nil, err
	}
	return s, nil
}

// openInfra connects PostgreSQL (checking or migrating the schema), Redis,
// ClickHouse (optional) and the vault.
func (s *Server) openInfra(ctx context.Context) error {
	cfg := s.cfg
	pool, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return fmt.Errorf("connect postgresql: %w", err)
	}
	s.infra.pool = pool
	if err := ensureSchema(ctx, pool, s.opts.Migrate, s.logger); err != nil {
		return err
	}
	if err := postgres.EnsurePartitions(ctx, pool, time.Now()); err != nil {
		return fmt.Errorf("ensure partitions: %w", err)
	}

	rdb, err := redis.Open(ctx, cfg.RedisURL, cfg.RedisAddrs)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	s.infra.rdb = rdb
	s.infra.keys = redis.NewKeys(cfg.RedisPrefix)

	if cfg.ClickHouseURL != "" {
		conn, err := chstore.Open(ctx, cfg.ClickHouseURL)
		if err != nil {
			return fmt.Errorf("connect clickhouse: %w", err)
		}
		s.infra.chConn = conn
		if err := chstore.Migrate(ctx, conn, cfg.ClickHouseTTLDays); err != nil {
			return fmt.Errorf("migrate clickhouse: %w", err)
		}
	} else {
		s.logger.Info("clickhouse disabled: raw report events are not stored")
	}

	provider, err := vault.LoadLocalKEKProvider(cfg.KEKFile, cfg.KEKs, cfg.KEKCurrent)
	if err != nil {
		return fmt.Errorf("load key-encryption keys: %w", err)
	}
	s.infra.cipher = vault.NewCipher(provider, cfg.DEKCacheSize, cfg.DEKCacheTTL)
	if s.infra.pepper, err = vault.SystemKey(ctx, pool, s.infra.cipher, dedupePepperName, dedupePepperSize); err != nil {
		return fmt.Errorf("load %s system key (is the KEK the one used to initialize this database?): %w", dedupePepperName, err)
	}
	return nil
}

// ensureSchema compares the applied migration version with the migrations
// embedded in the binary. A database behind the binary is migrated when
// migrate is set and rejected otherwise; a database ahead of the binary (a
// rolling upgrade in progress) is accepted with a warning.
func ensureSchema(ctx context.Context, pool *pgxpool.Pool, migrate bool, logger *slog.Logger) error {
	latest, err := postgres.LatestMigrationVersion()
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	current, err := postgres.MigrationVersion(ctx, pool)
	if err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	switch {
	case current == latest:
		return nil
	case current > latest:
		logger.Warn("database schema is newer than this binary",
			slog.Int64("database_version", current), slog.Int64("binary_version", latest))
		return nil
	case !migrate:
		return fmt.Errorf("database schema is at version %d, binary expects %d: run spnr migrate up", current, latest)
	}
	logger.Info("applying database migrations", slog.Int64("from", current), slog.Int64("to", latest))
	if err := postgres.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}

// Addr returns the address of the main HTTP listener.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

// MetricsAddr returns the address of the dedicated metrics listener, or nil
// when metrics are served on the main listener.
func (s *Server) MetricsAddr() net.Addr {
	if s.metricsListener == nil {
		return nil
	}
	return s.metricsListener.Addr()
}

// PprofAddr returns the address of the pprof debug listener, or nil when
// SPINNERET_PPROF_ADDR is not set.
func (s *Server) PprofAddr() net.Addr {
	if s.pprofListener == nil {
		return nil
	}
	return s.pprofListener.Addr()
}

// Run serves HTTP and runs the background loops and jobs of the instance role
// until ctx is canceled, then shuts down gracefully (see shutdown) and
// releases every resource. It returns nil after a graceful shutdown and an
// error when a listener failed. Run may be called only once.
func (s *Server) Run(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return errors.New("server: Run called more than once")
	}
	defer func() {
		if err := s.Close(); err != nil {
			s.logger.Warn("release resources", slog.Any("error", err))
		}
	}()

	services := newTier("services", s.logger, s.loopRestarts)
	sinks := newTier("sinks", s.logger, s.loopRestarts)
	clickhouse := newTier("clickhouse", s.logger, s.loopRestarts)
	s.c.startLoops(s.cfg.Role, services, sinks, clickhouse, func(ctx context.Context) error {
		return s.startWorker(ctx, services)
	})

	serveErr := make(chan error, 2)
	serve := func(name string, srv *http.Server, ln net.Listener, tls bool) {
		var err error
		if tls {
			// The certificate was loaded into TLSConfig by setupHTTP.
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("%s listener %s: %w", name, ln.Addr(), err)
		}
	}
	go serve("http", s.httpServer, s.listener, s.cfg.TLSCertFile != "")
	if s.metricsServer != nil {
		go serve("metrics", s.metricsServer, s.metricsListener, false)
	}
	if s.pprofServer != nil {
		go serve("pprof", s.pprofServer, s.pprofListener, false)
	}
	s.logger.Info("spinneret started", slog.String("role", string(s.cfg.Role)),
		slog.String("addr", s.listener.Addr().String()), slog.String("version", s.opts.Version))

	var runErr error
	select {
	case <-ctx.Done():
		s.logger.Info("shutdown requested")
	case runErr = <-serveErr:
		s.logger.Error("listener failed, shutting down", slog.Any("error", runErr))
	}
	s.shutdown(runErr == nil, services, sinks, clickhouse)
	return runErr
}

// startWorker ensures the Redis hot state is built, then starts the report
// consumers and the job runner. It blocks until ctx ends so that the
// supervisor restarts it only when the hot state could not be ensured.
func (s *Server) startWorker(ctx context.Context, services *tier) error {
	rebuilt, err := s.c.hot.EnsureBuilt(ctx)
	if err != nil {
		return fmt.Errorf("ensure hot state: %w", err)
	}
	if rebuilt {
		s.logger.Info("hot state rebuilt from postgresql")
	}
	s.hotStateReady.Store(true)
	services.Go("worker", s.c.worker.Run)
	services.Go("jobs", s.runner.Run)
	<-ctx.Done()
	return nil
}

// shutdown drains the instance: readiness turns unavailable, load balancers
// get time to stop routing, HTTP requests finish (long polls and event streams
// are canceled), then loops stop tier by tier so that writers flush what the
// services produced. Everything is bounded by SPINNERET_SHUTDOWN_TIMEOUT.
func (s *Server) shutdown(drainDelay bool, tiers ...*tier) {
	start := time.Now()
	deadline := start.Add(s.cfg.ShutdownTimeout)
	s.health.setDraining()
	// Stop keep-alives before the listener closes: idle connections of load balancers and SDK clients
	// are closed now and every response of the drain window carries "Connection: close", so no peer
	// sends a request on a pooled connection that this instance is about to close (which would fail
	// requests a proxy cannot safely retry).
	s.httpServer.SetKeepAlivesEnabled(false)
	if drainDelay {
		sleepContext(context.Background(), min(maxDrainDelay, s.cfg.ShutdownTimeout/4))
	}
	s.startDrain()

	// HTTP gets half of the remaining budget, loops the rest.
	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), time.Until(deadline)/2)
	if err := s.httpServer.Shutdown(httpCtx); err != nil {
		s.logger.Warn("http shutdown timed out, closing connections", slog.Any("error", err))
		_ = s.httpServer.Close()
	}
	cancelHTTP()

	loopCtx, cancelLoops := context.WithDeadline(context.Background(), deadline)
	defer cancelLoops()
	for _, t := range tiers {
		if !t.stop(loopCtx) {
			s.logger.Error("timed out waiting for background loops", slog.String("tier", t.name))
		}
	}
	for _, t := range tiers {
		t.cancel()
	}
	for _, srv := range []*http.Server{s.metricsServer, s.pprofServer} {
		if srv == nil {
			continue
		}
		mctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := srv.Shutdown(mctx); err != nil {
			_ = srv.Close()
		}
		cancel()
	}
	s.logger.Info("shutdown complete", slog.Duration("took", time.Since(start)))
}

// Close releases every resource held by the server. Run calls it on return;
// call it directly only for a server that is never run. It is idempotent.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		var errs []error
		if s.startDrain != nil {
			s.startDrain()
		}
		if !s.running.Load() {
			for _, ln := range []net.Listener{s.listener, s.metricsListener, s.pprofListener} {
				if ln != nil {
					_ = ln.Close()
				}
			}
		}
		if s.c != nil {
			if s.c.unsubscribeResolver != nil {
				s.c.unsubscribeResolver()
			}
			if s.c.checker != nil {
				if err := s.c.checker.Close(); err != nil {
					errs = append(errs, fmt.Errorf("close proxy health checker: %w", err))
				}
			}
		}
		if s.infra.chConn != nil {
			if err := s.infra.chConn.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close clickhouse: %w", err))
			}
		}
		if s.infra.rdb != nil {
			s.infra.rdb.Close()
		}
		if s.infra.pool != nil {
			if !closeWithTimeout(s.infra.pool.Close, closeTimeout) {
				errs = append(errs, errors.New("close postgresql pool: timed out waiting for connections"))
			}
		}
		if s.shutdownTracing != nil {
			tctx, cancel := context.WithTimeout(context.Background(), tracingStopTimeout)
			if err := s.shutdownTracing(tctx); err != nil {
				errs = append(errs, fmt.Errorf("stop tracing: %w", err))
			}
			cancel()
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// closeWithTimeout runs fn and reports whether it returned within timeout.
func closeWithTimeout(fn func(), timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}
