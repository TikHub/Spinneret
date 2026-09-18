package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// app wires the shared state, the target site and the forward proxy.
type app struct {
	cfg    config
	logger *slog.Logger
	state  *state
	site   *siteHandler
	proxy  *proxyHandler
}

// newApp creates the application from its configuration.
func newApp(cfg config, logger *slog.Logger) *app {
	st := newState(cfg.StatsMaxKeys, cfg.Seed)
	return &app{
		cfg:    cfg,
		logger: logger,
		state:  st,
		site:   newSiteHandler(st, logger),
		proxy: newProxyHandler(st, logger, proxyOptions{
			Password:          cfg.ProxyPassword,
			DialTimeout:       cfg.DialTimeout,
			TunnelIdleTimeout: cfg.TunnelIdleTimeout,
			UpstreamTimeout:   upstreamTimeout,
		}),
	}
}

// run listens on the configured addresses and serves until ctx is done or a listener fails.
// ready, when non-nil, receives the bound addresses before serving starts.
func run(ctx context.Context, cfg config, logger *slog.Logger, ready func(site, proxy net.Addr)) error {
	var lc net.ListenConfig
	siteLn, err := lc.Listen(ctx, "tcp", cfg.SiteAddr)
	if err != nil {
		return fmt.Errorf("listen site %s: %w", cfg.SiteAddr, err)
	}
	proxyLn, err := lc.Listen(ctx, "tcp", cfg.ProxyAddr)
	if err != nil {
		_ = siteLn.Close()
		return fmt.Errorf("listen proxy %s: %w", cfg.ProxyAddr, err)
	}
	a := newApp(cfg, logger)
	// The effective seed is logged so a run with a random seed can be reproduced via SPINNERET_MOCK_SEED.
	logger.Info("mocktarget listening",
		slog.String("site_addr", siteLn.Addr().String()), slog.String("proxy_addr", proxyLn.Addr().String()),
		slog.Uint64("seed", a.state.rng.seed), slog.Int("stats_max_keys", cfg.StatsMaxKeys))
	if ready != nil {
		ready(siteLn.Addr(), proxyLn.Addr())
	}
	return a.serve(ctx, siteLn, proxyLn)
}

// serve runs both listeners until ctx is done or one of them fails, then shuts down gracefully.
func (a *app) serve(ctx context.Context, siteLn, proxyLn net.Listener) error {
	servers := []*http.Server{
		a.httpServer(a.site, siteWriteTimeout),
		a.httpServer(a.proxy, proxyWriteTimeout),
	}
	listeners := []net.Listener{siteLn, proxyLn}
	names := []string{"site", "proxy"}
	errCh := make(chan error, len(servers))
	for i, srv := range servers {
		go func() {
			if err := srv.Serve(listeners[i]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("serve %s: %w", names[i], err)
				return
			}
			errCh <- nil
		}()
	}

	pending := len(servers)
	var errs []error
	select {
	case <-ctx.Done():
		a.logger.Info("mocktarget shutting down")
	case err := <-errCh:
		pending--
		errs = append(errs, err)
		a.logger.Error("mocktarget listener stopped unexpectedly", slog.Any("error", err))
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.ShutdownTimeout)
	defer cancel()
	// Hijacked tunnels are invisible to Shutdown; close them so their goroutines finish.
	a.proxy.conns.closeAll()
	// Both listeners drain concurrently, so neither keeps accepting new work while the other waits
	// for its in-flight requests (forwarded proxy requests are themselves served by the site).
	var wg sync.WaitGroup
	for i, srv := range servers {
		wg.Go(func() {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				a.logger.Warn("graceful shutdown incomplete, closing connections",
					slog.String("listener", names[i]), slog.Any("error", err))
				_ = srv.Close()
			}
		})
	}
	wg.Wait()
	a.proxy.transport.CloseIdleConnections()
	for ; pending > 0; pending-- {
		errs = append(errs, <-errCh)
	}
	return errors.Join(errs...)
}

// httpServer builds an http.Server with the timeouts shared by both listeners.
func (a *app) httpServer(h http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// Server-level errors (accept failures such as fd exhaustion under load) must stay visible.
		ErrorLog: slog.NewLogLogger(a.logger.Handler(), slog.LevelWarn),
	}
}
