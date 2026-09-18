package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/redis/rueidis"
	"golang.org/x/sync/errgroup"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/jobs"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Health checker defaults (spec §12).
const (
	DefaultCheckURL         = "http://example.com/"
	DefaultCheckInterval    = 60 * time.Second
	DefaultCheckTimeout     = 10 * time.Second
	DefaultCheckConcurrency = 64
	// DeadAfterFailures is the number of consecutive failed checks that marks
	// an active proxy dead.
	DeadAfterFailures = 3
	// MaxDeadBackoff caps the check interval of dead proxies.
	MaxDeadBackoff = time.Hour
	// HealthJobName is the jobs.Job name of the periodic checker.
	HealthJobName = "proxy_health_check"
	checkPageSize = 500
)

// HealthConfig configures the health checker.
type HealthConfig struct {
	// CheckURL is fetched through every proxy (DefaultCheckURL when empty).
	CheckURL string
	// Interval between checks of an active proxy (DefaultCheckInterval when <= 0).
	Interval time.Duration
	// Timeout of one check request (DefaultCheckTimeout when <= 0).
	Timeout time.Duration
	// ExitIPURL optionally returns the caller's IP as JSON {"ip": "..."} or plain text.
	ExitIPURL string
	// GeoIPDB optionally points to a MaxMind DB used to fill empty regions.
	GeoIPDB string
	// Concurrency bounds simultaneous checks (DefaultCheckConcurrency when <= 0).
	Concurrency int
}

// withDefaults fills unset fields.
func (c HealthConfig) withDefaults() HealthConfig {
	if c.CheckURL == "" {
		c.CheckURL = DefaultCheckURL
	}
	if c.Interval <= 0 {
		c.Interval = DefaultCheckInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultCheckTimeout
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultCheckConcurrency
	}
	return c
}

// Membership reports this instance's position among live workers (provided
// by the worker registry).
type Membership interface {
	Membership(ctx context.Context) (index, total int, err error)
}

// CheckResult is the outcome of one health check.
type CheckResult struct {
	OK        bool
	LatencyMs int
	ExitIP    string
	// Region is the country code resolved from ExitIP (GeoIP configured).
	Region string
	// Error describes a failure; it never contains credentials.
	Error string
	// TenantID and NamespaceID locate the checked proxy (for audit entries).
	TenantID    string
	NamespaceID string

	// city is the English city name resolved from ExitIP.
	city string
}

// HealthChecker periodically checks proxies through a real request and keeps
// their state (active ↔ dead), check statistics and proxy x site health up to date.
type HealthChecker struct {
	cfg     HealthConfig
	q       *proxydb.Queries
	pool    *pgxpool.Pool
	cipher  *vault.Cipher
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	hot     HotSyncer
	member  Membership
	bus     events.Bus
	metrics *observability.Metrics
	logger  *slog.Logger
	now     func() time.Time

	// tlsConfig is used for TLS to https proxies and check targets (nil = system roots).
	tlsConfig *tls.Config

	geoOnce sync.Once
	geo     *maxminddb.Reader
	geoMu   sync.RWMutex
}

// NewHealthChecker creates a health checker. metrics may be nil.
func NewHealthChecker(cfg HealthConfig, pool *pgxpool.Pool, cipher *vault.Cipher, rdb rueidis.Client, keys redis.Keys,
	cat catalog.Catalog, hot HotSyncer, member Membership, bus events.Bus, metrics *observability.Metrics, logger *slog.Logger,
) *HealthChecker {
	if logger == nil {
		logger = slog.Default()
	}
	return &HealthChecker{
		cfg: cfg.withDefaults(), q: proxydb.New(pool), pool: pool, cipher: cipher, rdb: rdb, keys: keys,
		cat: cat, hot: hot, member: member, bus: bus, metrics: metrics,
		logger: logger.With(slog.String("component", "proxy_health")),
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Job returns the periodic check job (each instance; proxies are sharded by
// ShardOf(proxyID, live workers)).
func (h *HealthChecker) Job() jobs.Job {
	return jobs.Job{
		Name:     HealthJobName,
		Interval: h.cfg.Interval,
		Mode:     jobs.EachInstance,
		Timeout:  h.cfg.Interval + h.cfg.Timeout,
		Run: func(ctx context.Context) error {
			_, err := h.RunOnce(ctx)
			return err
		},
	}
}

// Close releases the GeoIP database.
func (h *HealthChecker) Close() error {
	h.geoMu.Lock()
	defer h.geoMu.Unlock()
	if h.geo == nil {
		return nil
	}
	err := h.geo.Close()
	h.geo = nil
	return err
}

// ShardOf returns the worker index responsible for a proxy: xxhash(proxyID) % total.
func ShardOf(proxyID string, total int) int {
	if total <= 1 {
		return 0
	}
	return int(xxhash.Sum64String(proxyID) % uint64(total))
}

// RunOnce checks every due proxy (states active and dead) owned by this
// instance and returns the number of checks performed.
func (h *HealthChecker) RunOnce(ctx context.Context) (int, error) {
	if h.member == nil {
		return 0, fmt.Errorf("proxy health: membership provider is not configured")
	}
	index, total, err := h.member.Membership(ctx)
	if err != nil {
		return 0, fmt.Errorf("proxy health: membership: %w", err)
	}
	if total <= 0 || index < 0 || index >= total {
		h.logger.Debug("proxy health: instance is not a live worker, skipping", slog.Int("index", index), slog.Int("total", total))
		return 0, nil
	}
	now := h.now()
	work := make(chan proxydb.Proxy, h.cfg.Concurrency)
	var checked atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	for range h.cfg.Concurrency {
		g.Go(func() error {
			for row := range work {
				if gctx.Err() != nil {
					continue
				}
				if err := h.checkRow(gctx, row, now); err != nil && gctx.Err() == nil {
					h.logger.Warn("proxy health check failed to record", slog.String("proxy_id", row.ID), slog.Any("error", err))
				}
				checked.Add(1)
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(work)
		return h.produce(gctx, now, index, total, work)
	})
	err = g.Wait()
	h.updateGauges(ctx)
	return int(checked.Load()), err
}

// produce pages through due proxies and sends the ones of this shard.
func (h *HealthChecker) produce(ctx context.Context, now time.Time, index, total int, work chan<- proxydb.Proxy) error {
	cursor := proxydb.ProxyDueForCheckParams{Now: now, AfterAt: time.Unix(0, 0).UTC(), PageLimit: checkPageSize}
	for {
		rows, err := h.q.ProxyDueForCheck(ctx, cursor)
		if err != nil {
			if ctx.Err() != nil {
				// The run was canceled (shutdown or run timeout): not a failure.
				return nil //nolint:nilerr // cancellation ends the run without an error
			}
			return fmt.Errorf("proxy health: list due proxies: %w", err)
		}
		for _, row := range rows {
			if ShardOf(row.ID, total) != index {
				continue
			}
			select {
			case work <- row:
			case <-ctx.Done():
				return nil
			}
		}
		if len(rows) < checkPageSize {
			return nil
		}
		last := rows[len(rows)-1]
		cursor.AfterAt, cursor.AfterID = last.NextCheckAt, last.ID
	}
}

// checkRow runs and records the scheduled check of one proxy that was due at
// runAt.
func (h *HealthChecker) checkRow(ctx context.Context, row proxydb.Proxy, runAt time.Time) error {
	u, err := OpenURL(h.cipher, row.ID, sealedURL(row))
	if err != nil {
		return fmt.Errorf("decrypt proxy url: %w", err)
	}
	res := h.probe(ctx, u)
	if ctx.Err() != nil {
		// A probe interrupted by cancellation says nothing about the proxy:
		// it is not recorded and the canceled run is not a failure.
		return nil //nolint:nilerr // cancellation discards the probe result
	}
	return h.record(ctx, row.ID, res, &runAt)
}

// CheckProxy runs a health check of one proxy immediately and records it like
// a scheduled check (proxy:operate).
func (h *HealthChecker) CheckProxy(ctx context.Context, p *authz.Principal, id string) (CheckResult, error) {
	if err := requireTokenScope(h.cat, p, authz.PermProxyOperate); err != nil {
		return CheckResult{}, err
	}
	row, err := h.q.ProxyGet(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return CheckResult{}, apperr.NotFound("proxy not found")
		}
		return CheckResult{}, fmt.Errorf("load proxy: %w", err)
	}
	ns, err := authorizeRow(h.cat, p, row, authz.PermProxyOperate)
	if err != nil {
		return CheckResult{}, err
	}
	u, err := OpenURL(h.cipher, row.ID, sealedURL(row))
	if err != nil {
		h.logger.Error("decrypt proxy url failed", slog.String("proxy_id", row.ID), slog.Any("error", err))
		return CheckResult{}, apperr.Internal(fmt.Errorf("decrypt proxy url: %w", err))
	}
	res := h.probe(ctx, u)
	res.TenantID, res.NamespaceID = ns.TenantID, ns.ID
	if err := ctx.Err(); err != nil {
		return CheckResult{}, err
	}
	if err := h.record(ctx, row.ID, res, nil); err != nil {
		return res, apperr.Internal(err)
	}
	return res, nil
}

// updateGauges publishes proxy counts per site and state.
func (h *HealthChecker) updateGauges(ctx context.Context) {
	if h.metrics == nil || h.metrics.Proxies == nil {
		return
	}
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.cfg.Timeout)
	defer cancel()
	rows, err := h.q.ProxyCountByNamespaceState(gctx)
	if err != nil {
		h.logger.Warn("proxy health: count proxies for metrics failed", slog.Any("error", err))
		return
	}
	counts := map[string]map[string]int64{}
	for _, r := range rows {
		if counts[r.NamespaceID] == nil {
			counts[r.NamespaceID] = map[string]int64{}
		}
		counts[r.NamespaceID][r.State] = r.Proxies
	}
	sums := map[[2]string]float64{}
	for _, ns := range h.cat.Namespaces("") {
		for _, site := range ns.SitesByID {
			label := observability.Label(site.Name)
			for _, st := range allStates {
				sums[[2]string{label, st}] += float64(counts[ns.ID][st])
			}
		}
	}
	for k, v := range sums {
		h.metrics.Proxies.WithLabelValues(k[0], k[1]).Set(v)
	}
}
