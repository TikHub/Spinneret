// Command basic is a minimal crawler node built with the Spinneret Go SDK.
//
// It optionally watches a config item, then repeatedly leases an identity,
// sends the request with the leased credential and proxy, and reports the
// outcome. The lease is released by the last report.
//
//	export SPINNERET_URL=http://localhost:8080
//	export SPINNERET_TOKEN=spn_xxx
//	go run ./sdk/go/examples/basic -site shop -client web \
//	    -target https://target.example.com/api/v1/search -config crawler/search.json
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Evil0ctal/Spinneret/sdk/go/spinneret"
)

const (
	maxBodyBytes   = 8 << 20
	requestTimeout = 15 * time.Second
	closeTimeout   = 5 * time.Second
)

type config struct {
	site     string
	client   string
	target   string
	requests int
	configID string
	useGRPC  bool
}

func main() {
	var cfg config
	flag.StringVar(&cfg.site, "site", "shop", "site name")
	flag.StringVar(&cfg.client, "client", "web", "client type of the site")
	flag.StringVar(&cfg.target, "target", "https://target.example.com/api/v1/search", "URL to request")
	flag.IntVar(&cfg.requests, "requests", 3, "number of requests")
	flag.StringVar(&cfg.configID, "config", "", "config item to watch (group/key), optional")
	flag.BoolVar(&cfg.useGRPC, "grpc", false, "use gRPC instead of Connect JSON (HTTP/2 end to end)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(cfg, logger); err != nil {
		logger.Error("node failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(cfg config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// URL and token come from SPINNERET_URL and SPINNERET_TOKEN.
	client, err := spinneret.New(spinneret.Options{UseGRPC: cfg.useGRPC, Logger: logger})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		if err := client.Close(closeCtx); err != nil {
			logger.Warn("close", slog.String("error", err.Error()))
		}
	}()

	if cfg.configID != "" {
		watcher, err := startWatcher(ctx, client, cfg.configID, logger)
		if err != nil {
			return err
		}
		defer watcher.Stop()
	}

	for i := 0; i < cfg.requests && ctx.Err() == nil; i++ {
		if err := crawlOnce(ctx, client, cfg, logger); err != nil {
			return err
		}
	}
	stats := client.Reporter().Stats()
	logger.Info("reporter", slog.Int64("submitted", stats.Submitted), slog.Int64("sent", stats.Sent),
		slog.Int64("dropped", stats.Dropped))
	return nil
}

func startWatcher(ctx context.Context, client *spinneret.Client, id string, logger *slog.Logger) (*spinneret.ConfigWatcher, error) {
	key, err := spinneret.ParseConfigKey(id)
	if err != nil {
		return nil, err
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
		Items:       []spinneret.ConfigKey{key},
		SnapshotDir: filepath.Join(cacheDir, "spinneret"),
		// Never log config contents: they may contain resolved secrets.
		OnChange: func(item *spinneret.ConfigItem) {
			logger.Info("config changed", slog.String("item", item.GetGroup()+"/"+item.GetKey()),
				slog.Int("version", int(item.GetVersion())))
		},
	})
	if err != nil {
		return nil, err
	}
	if err := watcher.Start(ctx); err != nil {
		return nil, err
	}
	if watcher.FromSnapshot() {
		logger.Warn("config server unavailable, running from local snapshots")
	}
	return watcher, nil
}

// crawlOnce leases an identity, sends one request and reports what happened.
// Capacity and pause decisions of the server are waited out, not returned.
func crawlOnce(ctx context.Context, client *spinneret.Client, cfg config, logger *slog.Logger) error {
	lease, err := client.Lease(ctx, &spinneret.AcquireRequest{
		Site:   cfg.site,
		Client: cfg.client,
		Uri:    cfg.target,
		WaitMs: 500,
	})
	switch {
	case spinneret.IsNoIdentity(err), spinneret.IsNoProxy(err):
		wait := waitFor(err, time.Second)
		logger.Info("no capacity, backing off", slog.String("reason", spinneret.ReasonOf(err)), slog.Duration("wait", wait))
		sleep(ctx, wait)
		return nil
	case spinneret.IsCircuitOpen(err), spinneret.IsSitePaused(err):
		wait := waitFor(err, 30*time.Second)
		logger.Warn("paused by Spinneret", slog.String("reason", spinneret.ReasonOf(err)), slog.Duration("wait", wait))
		sleep(ctx, wait)
		return nil
	case err != nil:
		return fmt.Errorf("acquire: %w", err)
	}
	defer func() {
		// Release even when ctx was cancelled by a signal.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if err := lease.Close(closeCtx); err != nil {
			logger.Warn("release", slog.String("lease_id", lease.ID()), slog.String("error", err.Error()))
		}
	}()

	transport, err := lease.Transport(nil)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: requestTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.target, nil)
	if err != nil {
		return err
	}
	lease.Apply(req)
	started := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Info("request failed", slog.String("error_kind", spinneret.ClassifyError(err)))
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started})
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return lease.ReportError(err, spinneret.ReportInput{StartedAt: started, HTTPStatus: resp.StatusCode})
	}
	logger.Info("request done", slog.Int("status", resp.StatusCode), slog.String("identity", lease.IdentityID()))
	return lease.ReportResponse(resp, spinneret.ReportInput{
		StartedAt:     started,
		ResponseBytes: int64(len(body)),
		Markers:       detectMarkers(body),
	})
}

// detectMarkers recognizes response features the server cannot see (bodies
// are never uploaded).
func detectMarkers(body []byte) []string {
	var markers []string
	head := body[:min(len(body), 4096)]
	if bytes.Contains(head, []byte("captcha")) || bytes.Contains(head, []byte("verify")) {
		markers = append(markers, "captcha_page")
	}
	if bytes.Contains(head, []byte(`"data":[]`)) || bytes.Contains(head, []byte(`"data":null`)) {
		markers = append(markers, "empty_list")
	}
	return markers
}

func waitFor(err error, fallback time.Duration) time.Duration {
	if d := spinneret.RetryAfterOf(err); d > 0 {
		return d
	}
	return fallback
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
