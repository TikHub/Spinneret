package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

// recorder captures audit entries.
type recorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (r *recorder) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *recorder) actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Action)
	}
	return out
}

// env is a service wired to real PostgreSQL and Redis.
type env struct {
	t       *testing.T
	svc     *Service
	pool    *pgxpool.Pool
	rdb     rueidis.Client
	keys    redis.Keys
	cat     *catalogtest.Catalog
	bus     events.Bus
	audit   *recorder
	metrics *observability.Metrics

	tenantID string
	ns       *catalog.Namespace
	site     *catalog.Site
	site2    *catalog.Site
}

var admin = authz.System("test")

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	e := &env{
		t: t, pool: pool, rdb: rdb, keys: keys, cat: catalogtest.New(), bus: events.NewMemoryBus(),
		audit: &recorder{}, metrics: observability.NewMetrics(),
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = 5 * time.Millisecond
	}
	e.svc = New(cfg, pool, vaulttest.NewCipher(t), rdb, keys, e.cat, e.bus, e.audit, e.metrics, slog.New(slog.DiscardHandler))

	e.tenantID = e.seedTenant("acme")
	e.ns = e.seedNamespace(e.tenantID, "prod")
	e.site = e.seedSite(e.ns, "shop")
	e.site2 = e.seedSite(e.ns, "forum")
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

func (e *env) seedTenant(name string) string {
	id := idgen.New(idgen.Tenant)
	e.exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name)
	return id
}

func (e *env) seedNamespace(tenantID, name string) *catalog.Namespace {
	id := idgen.New(idgen.Namespace)
	e.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenantID, name)
	ns := catalogtest.NewNamespace(tenantID, id, name)
	ns.TenantName = "acme"
	e.cat.Put(ns)
	return ns
}

func (e *env) seedSite(ns *catalog.Namespace, name string) *catalog.Site {
	id := idgen.New(idgen.Site)
	var hkey int64
	require.NoError(e.t, e.pool.QueryRow(context.Background(),
		`INSERT INTO sites (id, namespace_id, name) VALUES ($1, $2, $3) RETURNING hkey`, id, ns.ID, name).Scan(&hkey))
	return catalogtest.AddSite(ns, id, name, hkey, "web")
}

// startService runs the service until the test ends.
func (e *env) startService() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.svc.Run(ctx)
	}()
	e.t.Cleanup(func() {
		cancel()
		<-done
	})
	require.Eventually(e.t, func() bool { return e.svc.running.Load() }, time.Second, time.Millisecond)
}

// alerts returns the stored alerts of the test tenant, newest first.
func (e *env) alerts() []AlertEvent {
	e.t.Helper()
	page, err := e.svc.ListAlertEvents(context.Background(), AlertQuery{
		TenantID: e.tenantID, IncludeTenant: true, NamespaceIDs: []string{e.ns.ID}, Limit: 500,
	})
	require.NoError(e.t, err)
	return page.Events
}

// waitDeliveries waits until the alert has n deliveries recorded.
func (e *env) waitDeliveries(alertID string, n int) []Delivery {
	e.t.Helper()
	var got []Delivery
	require.Eventually(e.t, func() bool {
		row, err := e.svc.q.NotifyAlertGet(context.Background(), alertID)
		if err != nil {
			return false
		}
		got = alertEventOf(row).Deliveries
		return len(got) >= n
	}, 10*time.Second, 10*time.Millisecond)
	return got
}

// captured is a request received by a fake provider.
type captured struct {
	Path    string
	Query   string
	Headers http.Header
	Body    []byte
}

// provider is a fake HTTP provider that records requests and answers with
// the configured responses (the last one repeats).
type provider struct {
	*httptest.Server
	mu        sync.Mutex
	requests  []captured
	responses []providerResponse
}

type providerResponse struct {
	status int
	body   string
}

func newProvider(t *testing.T, responses ...providerResponse) *provider {
	t.Helper()
	if len(responses) == 0 {
		responses = []providerResponse{{status: 200, body: `{}`}}
	}
	p := &provider{responses: responses}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.requests = append(p.requests, captured{Path: r.URL.Path, Query: r.URL.RawQuery, Headers: r.Header.Clone(), Body: body})
		resp := p.responses[min(len(p.requests)-1, len(p.responses)-1)]
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *provider) received() []captured {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]captured(nil), p.requests...)
}

func decodeJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

// webhookChannel creates an enabled webhook channel.
func (e *env) webhookChannel(name, namespaceID, url string, kinds []string, siteIDs []string, minSeverity string) Channel {
	e.t.Helper()
	ch, err := e.svc.CreateChannel(context.Background(), admin, ChannelInput{
		TenantID: e.tenantID, NamespaceID: namespaceID, Name: name, Kind: ChannelWebhook,
		Config:     map[string]any{"url": url},
		EventTypes: kinds, SiteIDs: siteIDs, MinSeverity: minSeverity, Enabled: true,
	})
	require.NoError(e.t, err)
	return ch
}
