package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Environment variables read by mocktarget.
const (
	envSiteAddr          = "SPINNERET_MOCK_ADDR"
	envProxyAddr         = "SPINNERET_MOCK_PROXY_ADDR"
	envProxyPassword     = "SPINNERET_MOCK_PROXY_PASSWORD"
	envLogLevel          = "SPINNERET_MOCK_LOG_LEVEL"
	envLogFormat         = "SPINNERET_MOCK_LOG_FORMAT"
	envSeed              = "SPINNERET_MOCK_SEED"
	envStatsMaxKeys      = "SPINNERET_MOCK_STATS_MAX_KEYS"
	envShutdownTimeout   = "SPINNERET_MOCK_SHUTDOWN_TIMEOUT"
	envDialTimeout       = "SPINNERET_MOCK_DIAL_TIMEOUT"
	envTunnelIdleTimeout = "SPINNERET_MOCK_TUNNEL_IDLE_TIMEOUT"
)

// Server timeouts. Write timeouts leave room for the maximum scripted latency.
const (
	readHeaderTimeout  = 10 * time.Second
	readTimeout        = 60 * time.Second
	idleTimeout        = 120 * time.Second
	siteWriteTimeout   = maxLatencyMs*time.Millisecond + 30*time.Second
	proxyWriteTimeout  = 2*maxLatencyMs*time.Millisecond + 60*time.Second
	upstreamTimeout    = maxLatencyMs*time.Millisecond + 30*time.Second
	maxHeaderBytes     = 1 << 20
	maxStatsKeysLimit  = 10_000_000
	defaultStatsKeys   = 100_000
	defaultLogFormat   = "json"
	defaultPassword    = "secret"
	defaultSiteAddr    = ":9090"
	defaultProxyAddr   = ":9091"
	defaultShutdown    = 10 * time.Second
	defaultDialTimeout = 10 * time.Second
	defaultTunnelIdle  = 5 * time.Minute
)

// config is the runtime configuration of mocktarget.
type config struct {
	SiteAddr          string
	ProxyAddr         string
	ProxyPassword     string
	LogLevel          slog.Level
	LogFormat         string
	Seed              uint64
	StatsMaxKeys      int
	ShutdownTimeout   time.Duration
	DialTimeout       time.Duration
	TunnelIdleTimeout time.Duration
}

// loadConfig reads the configuration from lookup; unset or empty variables take their defaults.
func loadConfig(lookup func(string) (string, bool)) (config, error) {
	get := func(name, def string) string {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	cfg := config{
		SiteAddr:      get(envSiteAddr, defaultSiteAddr),
		ProxyAddr:     get(envProxyAddr, defaultProxyAddr),
		ProxyPassword: defaultPassword,
		LogFormat:     strings.ToLower(get(envLogFormat, defaultLogFormat)),
	}
	// The password is taken verbatim: surrounding spaces are significant.
	if v, ok := lookup(envProxyPassword); ok && v != "" {
		cfg.ProxyPassword = v
	}
	var errs []error
	if err := cfg.LogLevel.UnmarshalText([]byte(get(envLogLevel, "info"))); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", envLogLevel, err))
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("%s: must be json or text, got %q", envLogFormat, cfg.LogFormat))
	}
	var err error
	if cfg.Seed, err = strconv.ParseUint(get(envSeed, "0"), 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", envSeed, err))
	}
	if cfg.StatsMaxKeys, err = strconv.Atoi(get(envStatsMaxKeys, strconv.Itoa(defaultStatsKeys))); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", envStatsMaxKeys, err))
	} else if cfg.StatsMaxKeys < 1 || cfg.StatsMaxKeys > maxStatsKeysLimit {
		errs = append(errs, fmt.Errorf("%s: must be in [1,%d], got %d", envStatsMaxKeys, maxStatsKeysLimit, cfg.StatsMaxKeys))
	}
	cfg.ShutdownTimeout, err = parsePositiveDuration(envShutdownTimeout, get(envShutdownTimeout, defaultShutdown.String()))
	errs = append(errs, err)
	cfg.DialTimeout, err = parsePositiveDuration(envDialTimeout, get(envDialTimeout, defaultDialTimeout.String()))
	errs = append(errs, err)
	cfg.TunnelIdleTimeout, err = parsePositiveDuration(envTunnelIdleTimeout, get(envTunnelIdleTimeout, defaultTunnelIdle.String()))
	errs = append(errs, err)
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// parsePositiveDuration parses a strictly positive Go duration.
func parsePositiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be positive, got %s", name, value)
	}
	return d, nil
}

// newLogger creates the structured logger.
func newLogger(cfg config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
