package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/action"
	"github.com/TikHub/Spinneret/internal/analytics"
	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/breaker"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/hotstate"
	"github.com/TikHub/Spinneret/internal/identitysvc"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/notify"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/peers"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
	"github.com/TikHub/Spinneret/internal/policysvc"
	"github.com/TikHub/Spinneret/internal/proxy"
	"github.com/TikHub/Spinneret/internal/scheduler"
	"github.com/TikHub/Spinneret/internal/settings"
	"github.com/TikHub/Spinneret/internal/signal"
	"github.com/TikHub/Spinneret/internal/sitesvc"
	"github.com/TikHub/Spinneret/internal/stats"
	chstore "github.com/TikHub/Spinneret/internal/store/clickhouse"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/tenancy"
	"github.com/TikHub/Spinneret/internal/updatecheck"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/version"
	"github.com/TikHub/Spinneret/internal/worker"
)

// ClickHouse writer settings (spec §6.3: raw report and lease events).
const (
	clickHouseFlushEvery = time.Second
	clickHouseMaxBatch   = 10_000
	// dedupePepperName is the system key used to hash identity and proxy uniqueness keys.
	dedupePepperName = "dedupe_pepper"
	dedupePepperSize = 32
)

// infra holds the shared infrastructure clients of one instance.
type infra struct {
	pool   *pgxpool.Pool
	rdb    rueidis.Client
	keys   redis.Keys
	chConn chdriver.Conn // nil when ClickHouse is disabled
	cipher *vault.Cipher
	pepper []byte
	// settings is built before the ClickHouse schema is migrated, because the
	// retention that migration applies is one of the settings it holds.
	settings *settings.Store
}

// newSettingsStore builds the deployment settings store. The environment's
// values are the defaults the database may override, except where the variable
// was set explicitly — then it pins the setting and the console shows it
// read-only.
func newSettingsStore(pool *pgxpool.Pool, cfg appconfig.Config) *settings.Store {
	return settings.New(db.New(pool), settings.Config{
		Defaults: settings.Defaults{
			RiskEvents:        cfg.Retention.RiskEvents,
			MinuteStats:       cfg.Retention.MinuteStats,
			HourStats:         cfg.Retention.HourStats,
			StateEvents:       cfg.Retention.StateEvents,
			Audit:             cfg.Retention.Audit,
			AlertEvents:       defaultAlertEventsRetention,
			ClickHouseTTLDays: cfg.ClickHouseTTLDays,
		},
		FromEnv: map[string]bool{
			settings.KeyRiskEvents:  cfg.EnvSet("SPINNERET_RETENTION_RISK_EVENTS"),
			settings.KeyMinuteStats: cfg.EnvSet("SPINNERET_RETENTION_MINUTE_STATS"),
			settings.KeyHourStats:   cfg.EnvSet("SPINNERET_RETENTION_HOUR_STATS"),
			settings.KeyStateEvents: cfg.EnvSet("SPINNERET_RETENTION_STATE_EVENTS"),
			settings.KeyAudit:       cfg.EnvSet("SPINNERET_RETENTION_AUDIT"),
			settings.KeyClickHouse:  cfg.EnvSet("SPINNERET_CLICKHOUSE_TTL_DAYS"),
		},
	})
}

// components holds every domain service of one instance. All services are
// constructed regardless of the role (construction is cheap and has no side
// effects); the role decides which loops, jobs and handlers are started.
type components struct {
	bus         events.Bus
	audit       *audit.Writer
	catalog     *catalog.Store
	hot         *hotstate.Syncer
	chWriter    *chstore.Writer // nil when ClickHouse is disabled
	stats       *stats.Aggregator
	secrets     *vault.SecretStore
	rewrapper   *vault.Rewrapper
	stateWriter *action.StateWriter
	executor    *action.Executor
	operator    *action.Operator
	payloads    *identitysvc.PayloadCache
	identities  *identitysvc.Service
	proxies     *proxy.Service
	resolver    *proxy.Resolver
	checker     *proxy.HealthChecker
	policies    *policysvc.Service
	sites       *sitesvc.Service
	tenancy     *tenancy.Service
	authn       *auth.Authenticator
	users       *auth.Users
	tokens      *auth.Tokens
	auditLogs   *auth.AuditLogs
	breaker     *breaker.Service
	bindings    *proxyBindingWriter
	scheduler   *scheduler.Service
	// peers is nil unless the acquire admission limit is derived from a
	// fleet-wide budget, which is the only reason to heartbeat.
	peers      *peers.Registry
	ingestor   *signal.Ingestor
	worker     *worker.Worker // nil unless the role runs workers
	config     *configcenter.Service
	notify     *notify.Service
	updates    *updatecheck.Checker
	analytics  *analytics.Service
	settings   *settings.Store
	chConn     chdriver.Conn // nil when ClickHouse is disabled
	partitions *partitionMaintainer

	unsubscribeResolver func()
}

// buildComponents wires the domain services following
// the service and wiring contracts.
func buildComponents(cfg appconfig.Config, in *infra, metrics *observability.Metrics, logger *slog.Logger) (*components, error) {
	c := &components{}
	pool, rdb, keys := in.pool, in.rdb, in.keys

	c.bus = events.NewRedisBus(rdb, keys, cfg.InstanceID, logger)
	c.audit = audit.NewWriter(pool, logger.With(slog.String("component", "audit")))
	c.catalog = catalog.NewStore(pool, c.bus, logger)
	c.catalog.SetChangeMarks(catalog.NewRedisChangeMarks(rdb, keys))
	c.hot = hotstate.NewSyncer(pool, rdb, keys, c.catalog, logger)

	if in.chConn != nil {
		c.chWriter = chstore.NewWriter(in.chConn, logger, clickHouseFlushEvery, clickHouseMaxBatch)
	}
	// A nil writer (ClickHouse disabled) makes the aggregator skip raw events.
	c.stats = stats.NewAggregator(pool, c.chWriter, metrics, logger)

	c.secrets = vault.NewSecretStore(pool, in.cipher, c.audit, logger)
	c.rewrapper = vault.NewRewrapper(pool, in.cipher, logger)

	c.stateWriter = action.NewStateWriter(pool, metrics, logger)
	c.executor = action.NewExecutor(action.ExecutorConfig{RecordCooldownEvents: cfg.RecordCooldownEvents},
		rdb, keys, c.stateWriter, c.bus, metrics, logger)
	c.operator = action.NewOperator(pool, rdb, keys, c.catalog, actionHotSyncer{hot: c.hot}, c.stateWriter, c.audit, c.bus, logger)

	c.payloads = identitysvc.NewPayloadCache(pool, in.cipher, c.secrets, cfg.PayloadCacheSize, cfg.PayloadCache, logger)
	c.identities = identitysvc.NewService(pool, in.cipher, in.pepper, c.catalog, identityHotSyncer{hot: c.hot},
		identityOperator{op: c.operator}, c.audit, c.bus, logger)

	c.proxies = proxy.NewService(pool, in.cipher, in.pepper, rdb, keys, c.catalog, c.hot, c.audit, c.bus, logger)
	c.resolver = proxy.NewResolver(pool, in.cipher, logger)
	// Drop cached proxy URLs on proxy.state events (published locally and by peers).
	c.unsubscribeResolver = c.resolver.Subscribe(c.bus)

	c.policies = policysvc.NewService(pool, c.catalog, c.hot, c.audit, logger.With(slog.String("component", "policies")),
		policysvc.WithEventBus(c.bus))
	c.sites = sitesvc.NewService(pool, c.catalog, c.hot, c.audit, rdb, keys, logger)
	c.tenancy = tenancy.NewService(pool, c.catalog, c.policies, c.audit, logger)

	trusted, err := netx.ParsePrefixes(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("parse SPINNERET_TRUSTED_PROXIES: %w", err)
	}
	authCfg := auth.Config{
		TokenCacheTTL:  cfg.TokenCacheTTL,
		SessionTTL:     cfg.SessionTTL,
		CookieSecure:   cfg.CookieSecure,
		TrustedProxies: trusted,
		InstanceID:     cfg.InstanceID,
	}
	c.authn = auth.NewAuthenticator(authCfg, pool, rdb, keys, c.bus, logger)
	c.users = auth.NewUsers(pool, rdb, keys, c.audit, authCfg, logger, auth.WithEventBus(c.bus))
	c.tokens = auth.NewTokens(pool, c.bus, c.audit, logger)
	c.auditLogs = auth.NewAuditLogs(pool)

	c.breaker = breaker.New(breaker.Config{}, pool, rdb, keys, c.catalog, c.bus, c.audit, metrics, logger)

	c.bindings = newProxyBindingWriter(pool, metrics, logger)
	c.scheduler = scheduler.New(c.schedulerConfig(cfg), rdb, keys, c.catalog, c.payloads, schedulerProxyResolver{r: c.resolver},
		schedulerStats{agg: c.stats}, metrics, logger)

	if cfg.AcquireFleetInflight > 0 && cfg.AcquireMaxInflight == 0 {
		c.peers = peers.New(peers.Config{
			InstanceID: cfg.InstanceID,
			OnLive:     c.scheduler.SetAcquirePeers,
		}, rdb, keys, logger)
		// The heartbeat health is what tells a stuck division apart from a real
		// single-instance deployment, so it is exported wherever the registry runs.
		metrics.RegisterAcquirePeerRegistry(c.peers.BeatAge, c.peers.BeatFailures)
	}

	c.ingestor = signal.NewIngestor(signal.Config{
		ReportShards:     cfg.ReportShards,
		DedupTTL:         cfg.ReportDedupTTL,
		StreamMaxLen:     cfg.StreamMaxLen,
		LateReportWindow: cfg.LateReportWindow,
	}, rdb, keys, c.catalog, c.stats, metrics, logger)

	var membership proxy.Membership = noMembership{}
	if cfg.Role.RunsWorkers() {
		c.worker = worker.New(worker.Config{
			ReportShards:     cfg.ReportShards,
			InstanceID:       cfg.InstanceID,
			LateReportWindow: cfg.LateReportWindow,
		}, rdb, keys, c.catalog, workerExecutor{exec: c.executor}, c.scheduler, c.breaker, workerStats{agg: c.stats},
			metrics, logger)
		membership = c.worker
	}
	c.checker = proxy.NewHealthChecker(proxy.HealthConfig{
		CheckURL:  cfg.ProxyCheckURL,
		Interval:  cfg.ProxyCheckInterval,
		Timeout:   cfg.ProxyCheckTimeout,
		ExitIPURL: cfg.ProxyExitIPURL,
		GeoIPDB:   cfg.GeoIPDB,
	}, pool, in.cipher, rdb, keys, c.catalog, c.hot, membership, c.bus, metrics, logger)

	c.config = configcenter.New(configcenter.Config{MaxWatchers: cfg.MaxWatchers}, pool, c.catalog, c.bus,
		configSecrets{store: c.secrets}, c.breaker, c.audit, metrics, logger)
	c.notify = notify.New(notify.Config{ReportShards: cfg.ReportShards, AllowPrivateTargets: cfg.NotifyAllowPrivateTargets},
		pool, in.cipher, rdb, keys, c.catalog, c.bus, c.audit, metrics, logger)

	// in.chConn is a nil interface (not a typed nil) when ClickHouse is disabled.
	c.analytics = analytics.New(pool, in.chConn, rdb, keys, c.catalog, logger, analytics.WithReportShards(cfg.ReportShards))

	c.updates = updatecheck.New(updatecheck.Config{URL: cfg.UpdateCheckURL, Current: version.String()})

	c.settings = in.settings
	c.chConn = in.chConn

	c.partitions = newPartitionMaintainer(pool, c.settings, logger)
	return c, nil
}

// applyClickHouseTTL carries a retention change to the ClickHouse tables. It is
// what makes the console's ClickHouse retention setting mean anything: the store
// records the number, the tables carry the TTL, and only ClickHouse enforces it.
//
// chstore.Migrate is idempotent and issues ALTER TABLE ... MODIFY TTL only when
// the table's current TTL differs, so calling it with an unchanged value costs
// one metadata read. Returns nil when ClickHouse is disabled: there is then
// nothing holding a TTL, and the setting applies the next time one is connected.
func (c *components) applyClickHouseTTL(ctx context.Context, days int) error {
	if c.chConn == nil {
		return nil
	}
	return chstore.Migrate(ctx, c.chConn, days)
}

// registerJobs adds every periodic job of a worker instance (spec §6.8) and
// returns the number of leader jobs.
func (c *components) registerJobs(r *jobs.Runner) (leaders int, err error) {
	for _, j := range []jobs.Job{
		c.scheduler.ReapJob(),
		c.operator.ExpiryJob(),
		c.breaker.EvaluateJob(),
		c.hot.SnapshotJob(),
		c.checker.Job(),
		c.notify.EvaluateJob(),
		c.partitions.job(),
	} {
		if err := r.Add(j); err != nil {
			return 0, fmt.Errorf("register job %s: %w", j.Name, err)
		}
		if j.Mode == jobs.Leader {
			leaders++
		}
	}
	return leaders, nil
}

// minFreeConnections is the number of pooled PostgreSQL connections that must
// remain for requests, loops and non-leader jobs once every leader job holds
// its lock connection.
const minFreeConnections = 2

// checkPoolSize rejects a connection pool that leader jobs would exhaust: the
// jobs runner keeps one pooled connection per leader job for as long as it
// holds the job's advisory lock, so a pool of that size or barely larger
// starves every other database user of the leader instance.
func checkPoolSize(maxConns int32, leaderJobs int) error {
	if need := leaderJobs + minFreeConnections; int(maxConns) < need {
		return fmt.Errorf("SPINNERET_DATABASE_MAX_CONNS=%d is too small for a worker instance: "+
			"%d leader jobs each hold a connection while leading; use at least %d", maxConns, leaderJobs, need)
	}
	return nil
}

// schedulerConfig builds the scheduler configuration. Acquire runs on api
// instances, so the half-open hook is wired on every role; the breaker's
// notification loop that consumes it runs on every role as well (startLoops).
func (c *components) schedulerConfig(cfg appconfig.Config) scheduler.Config {
	return scheduler.Config{
		ReportShards:         cfg.ReportShards,
		LateReportWindow:     cfg.LateReportWindow,
		AcquireFleetInflight: cfg.AcquireFleetInflight,
		AcquireMaxInflight:   cfg.AcquireMaxInflight,
		OnBreakerHalfOpen:    c.breaker.NotifyRisk,
		OnProxyBound:         c.bindings.Record,
	}
}

// loopGroup starts supervised loops (implemented by *tier).
type loopGroup interface {
	Go(name string, fn func(context.Context) error)
}

// startLoops starts the long-running loops of the instance role in tiers:
// services (stopped first), sinks that persist what the services produced,
// and finally the ClickHouse writer that receives rows from the stats sink.
// startWorker is called (supervised) on worker roles and must block until ctx ends.
func (c *components) startLoops(role appconfig.Role, services, sinks, clickhouse loopGroup, startWorker func(ctx context.Context) error) {
	services.Go("events_bus", c.bus.Run)
	services.Go("catalog", c.catalog.Run)
	// Breaker evaluations requested by the worker (risk outcomes) and by
	// acquire (lazy open -> half_open transitions) are served on every role.
	services.Go("breaker", c.breaker.Run)
	if role.ServesAPI() {
		services.Go("auth", c.authn.Run)
		services.Go("configcenter", c.config.Run)
		services.Go("kek_rewrap", c.rewrapper.Run)
		// Acquire runs on API roles only, so only they divide the fleet-wide
		// acquire budget and only they register as acquirers.
		if c.peers != nil {
			services.Go("acquire_peers", c.peers.Run)
		}
	}
	if role.RunsWorkers() {
		services.Go("notify", c.notify.Run)
		services.Go("worker_startup", startWorker)
	}

	sinks.Go("stats", c.stats.Run)
	sinks.Go("state_writer", c.stateWriter.Run)
	sinks.Go("secret_access", c.secrets.Run)
	sinks.Go("audit", c.audit.Run)
	sinks.Go("proxy_bindings", c.bindings.Run)

	if c.chWriter != nil {
		clickhouse.Go("clickhouse_writer", c.chWriter.Run)
	}
}
