package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"slices"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"connectrpc.com/validate"

	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/api/accessapi"
	"github.com/Evil0ctal/Spinneret/internal/api/authapi"
	"github.com/Evil0ctal/Spinneret/internal/api/breakerapi"
	"github.com/Evil0ctal/Spinneret/internal/api/configapi"
	"github.com/Evil0ctal/Spinneret/internal/api/dashboardapi"
	"github.com/Evil0ctal/Spinneret/internal/api/identityapi"
	"github.com/Evil0ctal/Spinneret/internal/api/leaseapi"
	"github.com/Evil0ctal/Spinneret/internal/api/notifyapi"
	"github.com/Evil0ctal/Spinneret/internal/api/policyapi"
	"github.com/Evil0ctal/Spinneret/internal/api/proxyapi"
	"github.com/Evil0ctal/Spinneret/internal/api/reportapi"
	"github.com/Evil0ctal/Spinneret/internal/api/secretapi"
	"github.com/Evil0ctal/Spinneret/internal/api/siteapi"
	"github.com/Evil0ctal/Spinneret/internal/api/tenantapi"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/web"
)

// HTTP server limits (spec §12 and the wiring task).
const (
	nodeMaxRequestBytes = 8 << 20
	compressMinBytes    = 4096
	readHeaderTimeout   = 10 * time.Second
	readTimeout         = 6 * time.Minute
	idleTimeout         = 120 * time.Second
	maxHeaderBytes      = 1 << 20
	// EventStreamPath is the console Server-Sent Events endpoint.
	EventStreamPath = "/api/v1/events/stream"
)

// setupHTTP builds the handlers and binds the listeners.
func (s *Server) setupHTTP() error {
	s.health = newHealthHandlers(s.readinessChecks())
	handler, err := s.buildHandler()
	if err != nil {
		return err
	}
	s.httpServer = s.newHTTPServer(handler, s.cfg.TLSCertFile != "")
	if s.cfg.TLSCertFile != "" {
		// Load the key pair now so a broken certificate fails startup instead of Run.
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load TLS certificate %s: %w", s.cfg.TLSCertFile, err)
		}
		s.httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	if s.listener, err = listen(s.cfg.HTTPAddr); err != nil {
		return fmt.Errorf("listen on SPINNERET_HTTP_ADDR %s: %w", s.cfg.HTTPAddr, err)
	}
	if s.cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.metrics.Handler())
		s.metricsServer = s.newHTTPServer(mux, false)
		if s.metricsListener, err = listen(s.cfg.MetricsAddr); err != nil {
			return fmt.Errorf("listen on SPINNERET_METRICS_ADDR %s: %w", s.cfg.MetricsAddr, err)
		}
	}
	if err := s.setupPprof(); err != nil {
		return err
	}
	return nil
}

// setupPprof binds the net/http/pprof listener when SPINNERET_PPROF_ADDR is
// set. It is off by default and never mounted on the API or metrics listener:
// the endpoints are unauthenticated and expose heap contents and goroutine
// stacks, so the address must stay inside the deployment (see
// docs/benchmarks.md for how the load tests use it).
func (s *Server) setupPprof() error {
	if s.cfg.PprofAddr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	s.pprofServer = s.newHTTPServer(mux, false)
	// A CPU profile or an execution trace holds the response open for its whole
	// duration, so this listener must not inherit the API read timeout.
	s.pprofServer.ReadTimeout = 0
	ln, err := listen(s.cfg.PprofAddr)
	if err != nil {
		return fmt.Errorf("listen on SPINNERET_PPROF_ADDR %s: %w", s.cfg.PprofAddr, err)
	}
	s.pprofListener = ln
	s.logger.Warn("pprof debug listener enabled; keep this address unreachable from untrusted networks",
		slog.String("addr", ln.Addr().String()))
	return nil
}

func listen(addr string) (net.Listener, error) {
	var lc net.ListenConfig
	return lc.Listen(context.Background(), "tcp", addr)
}

// newHTTPServer applies the server timeouts. There is no write timeout because
// config long polls and event streams outlive any fixed bound; unary requests
// are bounded by the deadline interceptor instead.
func (s *Server) newHTTPServer(handler http.Handler, tls bool) *http.Server {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	if tls {
		protocols.SetHTTP2(true)
	} else {
		protocols.SetUnencryptedHTTP2(true)
	}
	return &http.Server{
		Handler:           handler,
		Protocols:         &protocols,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.logger.With(slog.String("component", "http")).Handler(), slog.LevelWarn),
	}
}

// buildHandler assembles the routes of the instance role. Worker-only
// instances serve health and metrics endpoints only.
func (s *Server) buildHandler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health.liveness)
	mux.HandleFunc("GET /readyz", s.health.readiness)
	if s.cfg.MetricsAddr == "" {
		mux.Handle("GET /metrics", s.metrics.Handler())
	}
	if !s.cfg.Role.ServesAPI() {
		return mux, nil
	}
	if err := s.mountConnect(mux); err != nil {
		return nil, err
	}
	c := s.c
	sse := newSSEHandler(c.catalog, c.bus, s.logger.With(slog.String("component", "sse")))
	mux.Handle(EventStreamPath, drainMiddleware{next: auth.HTTPMiddleware(c.authn, sse), drain: s.drainCtx})
	if s.cfg.UIEnabled {
		mux.Handle("/", newStaticHandler(web.Assets()))
	}
	return newCORSMiddleware(s.cfg.AllowedOrigins, auth.TLSMiddleware(mux)), nil
}

// connectOptions returns the handler options for node-facing and admin services.
func (s *Server) connectOptions() (node, admin []connect.HandlerOption, err error) {
	var interceptors []connect.Interceptor
	if s.tracingEnabled {
		otel, err := otelconnect.NewInterceptor(otelconnect.WithoutMetrics())
		if err != nil {
			return nil, nil, fmt.Errorf("create tracing interceptor: %w", err)
		}
		interceptors = append(interceptors, otel)
	}
	// Outermost first: metrics see every result (including authentication
	// failures), errors are converted before metrics record the code, the
	// deadline covers authentication, and validation runs on authenticated
	// requests only.
	interceptors = append(interceptors,
		newMetricsInterceptor(s.metrics),
		newErrorInterceptor(s.logger.With(slog.String("component", "rpc"))),
		newDeadlineInterceptor(),
		auth.NewInterceptor(s.c.authn, auth.PublicProcedures(), s.logger),
		validate.NewInterceptor(),
	)
	common := []connect.HandlerOption{
		connect.WithCodec(newJSONCodec()),
		connect.WithCompressMinBytes(compressMinBytes),
	}
	node = slices.Concat(common, []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		connect.WithReadMaxBytes(nodeMaxRequestBytes),
	})
	// Admin requests additionally sync the catalog (innermost, after
	// authentication and validation) so that they observe every catalog change
	// acknowledged on any instance before they started.
	sync := newCatalogSyncInterceptor(s.c.catalog.Sync, s.logger.With(slog.String("component", "catalog_sync")))
	admin = slices.Concat(common, []connect.HandlerOption{
		connect.WithInterceptors(append(slices.Clone(interceptors), sync)...),
		connect.WithReadMaxBytes(int(s.cfg.AdminMaxRequestBytes)),
	})
	return node, admin, nil
}

// mountConnect registers the Connect handlers of every service.
func (s *Server) mountConnect(mux *http.ServeMux) error {
	node, admin, err := s.connectOptions()
	if err != nil {
		return err
	}
	c := s.c
	logger := s.logger
	cfg := configapi.New(c.config, c.catalog)
	mount := func(path string, h http.Handler) { mux.Handle(path, h) }

	// Node-facing services.
	mount(spinneretv1connect.NewLeaseServiceHandler(leaseapi.New(c.scheduler), node...))
	mount(spinneretv1connect.NewReportServiceHandler(reportapi.New(c.ingestor, c.catalog), node...))
	configPath, configHandler := spinneretv1connect.NewConfigServiceHandler(cfg, node...)
	// Only the long poll is canceled at shutdown; short reads finish normally.
	watch := drainMiddleware{next: configHandler, drain: s.drainCtx}
	mount(configPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == spinneretv1connect.ConfigServiceWatchConfigProcedure {
			watch.ServeHTTP(w, r)
			return
		}
		configHandler.ServeHTTP(w, r)
	}))
	mount(spinneretv1connect.NewSecretServiceHandler(secretapi.NewNodeHandler(c.secrets, c.catalog), node...))

	// Console and administration services.
	mount(spinneretv1connect.NewAuthServiceHandler(authapi.New(c.users, logger), admin...))
	mount(spinneretv1connect.NewTenantAdminServiceHandler(tenantapi.New(c.tenancy, logger), admin...))
	mount(spinneretv1connect.NewAccessAdminServiceHandler(accessapi.New(c.tokens, c.users, c.auditLogs, c.catalog, logger), admin...))
	mount(spinneretv1connect.NewSiteAdminServiceHandler(siteapi.New(c.sites, c.catalog, c.audit), admin...))
	mount(spinneretv1connect.NewIdentityAdminServiceHandler(identityapi.New(c.catalog, c.identities, identityHotReader{hot: c.hot}, logger), admin...))
	mount(spinneretv1connect.NewProxyAdminServiceHandler(proxyapi.New(c.catalog, c.proxies, c.checker, c.audit), admin...))
	mount(spinneretv1connect.NewPolicyAdminServiceHandler(policyapi.New(c.policies, c.catalog), admin...))
	mount(spinneretv1connect.NewBreakerAdminServiceHandler(breakerapi.New(c.breaker, c.catalog), admin...))
	mount(spinneretv1connect.NewConfigAdminServiceHandler(cfg, admin...))
	mount(spinneretv1connect.NewSecretAdminServiceHandler(secretapi.New(c.secrets, c.rewrapper, c.catalog, c.audit, logger), admin...))
	mount(spinneretv1connect.NewNotificationAdminServiceHandler(notifyapi.New(c.notify, c.catalog, logger), admin...))
	mount(spinneretv1connect.NewDashboardServiceHandler(dashboardapi.New(c.analytics, c.catalog), admin...))
	return nil
}

// readinessChecks lists the dependencies an instance needs before it accepts traffic.
func (s *Server) readinessChecks() []ReadinessCheck {
	pool, rdb, keys := s.infra.pool, s.infra.rdb, s.infra.keys
	checks := []ReadinessCheck{
		{Name: "postgres", Check: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				s.logger.Warn("readiness: postgresql ping failed", slog.Any("error", err))
				return errors.New("unreachable")
			}
			return nil
		}},
		{Name: "redis", Check: func(ctx context.Context) error {
			if err := rdb.Do(ctx, rdb.B().Ping().Build()).Error(); err != nil {
				s.logger.Warn("readiness: redis ping failed", slog.Any("error", err))
				return errors.New("unreachable")
			}
			return nil
		}},
		{Name: "catalog", Check: func(context.Context) error {
			if !s.c.catalog.Loaded() {
				return errors.New("not loaded")
			}
			return nil
		}},
		{Name: "hotstate", Check: func(ctx context.Context) error {
			if s.cfg.Role.RunsWorkers() && !s.hotStateReady.Load() {
				return errors.New("building")
			}
			n, err := rdb.Do(ctx, rdb.B().Exists().Key(keys.Epoch()).Build()).AsInt64()
			if err != nil {
				s.logger.Warn("readiness: hot state epoch lookup failed", slog.Any("error", err))
				return errors.New("unreachable")
			}
			if n == 0 {
				return errors.New("epoch missing (rebuild pending)")
			}
			return nil
		}},
	}
	return checks
}
