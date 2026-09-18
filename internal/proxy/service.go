package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/proxy/proxydb"
	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/vault"
)

// HotSyncer materializes proxies into the Redis hot state of every site of a
// namespace (provided by hotstate.Syncer).
type HotSyncer interface {
	SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error
	RemoveProxies(ctx context.Context, namespaceID string, proxyIDs []string) error
}

// Audit resource kind and action prefix of proxy entries.
const auditResourceKind = "proxy"

// sideEffectTimeout bounds one step (a Redis pipeline, a hot-state sync
// chunk, event publishing) of the side effects that run after a commit.
const sideEffectTimeout = 10 * time.Second

// hotSyncChunk bounds the proxy IDs passed to one HotSyncer call.
const hotSyncChunk = 1000

// Service implements proxy management (ProxyAdminService).
type Service struct {
	pool   *pgxpool.Pool
	q      *proxydb.Queries
	cipher *vault.Cipher
	pepper []byte
	rdb    rueidis.Client
	keys   redis.Keys
	cat    catalog.Catalog
	hot    HotSyncer
	audit  audit.Recorder
	bus    events.Bus
	logger *slog.Logger
	now    func() time.Time
}

// NewService creates the proxy service. pepper is the dedupe pepper
// (vault.SystemKey "dedupe_pepper"). audit may be nil (entries are discarded).
func NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, rdb rueidis.Client, keys redis.Keys,
	cat catalog.Catalog, hot HotSyncer, rec audit.Recorder, bus events.Bus, logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if rec == nil {
		rec = audit.Nop{}
	}
	return &Service{
		pool: pool, q: proxydb.New(pool), cipher: cipher, pepper: append([]byte(nil), pepper...),
		rdb: rdb, keys: keys, cat: cat, hot: hot, audit: rec, bus: bus,
		logger: logger.With(slog.String("component", "proxy")),
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// Proxy is the console view of a proxy. It never carries credentials.
type Proxy struct {
	ID                       string
	Key                      int64
	NamespaceID              string
	NamespaceName            string
	DisplayURL               string
	Scheme                   string
	Host                     string
	Port                     int
	UsernameHint             string
	Attributes               Attributes
	URLVersion               int
	State                    string
	StateReason              string
	StateChangedAt           time.Time
	BanUntil                 *time.Time
	CooldownUntil            *time.Time
	LastCheckAt              *time.Time
	LastCheckOK              bool
	LastLatencyMs            int
	ExitIP                   string
	ConsecutiveCheckFailures int
	NextCheckAt              time.Time
	BoundIdentities          int
	Sites                    []SiteState
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// SiteState is the hot state of a proxy on one site.
type SiteState struct {
	SiteID        string
	Site          string
	State         string
	Score         float64
	Samples       int
	ActiveLeases  int
	CooldownUntil *time.Time
}

// newProxyView converts a row into a view without site state.
func newProxyView(row proxydb.Proxy, ns *catalog.Namespace) *Proxy {
	p := &Proxy{
		ID:          row.ID,
		Key:         row.Hkey,
		NamespaceID: row.NamespaceID,
		DisplayURL:  row.DisplayUrl,
		Scheme:      row.Scheme,
		Host:        row.Host,
		Port:        int(row.Port),
		Attributes: Attributes{
			Kind: row.Kind, Region: row.Region, City: row.City, Provider: row.Provider,
			MaxConcurrency: int(row.MaxConcurrency), Tags: row.Tags, SessionTemplate: row.SessionTemplate,
		},
		UsernameHint:             row.UsernameHint,
		URLVersion:               int(row.UrlVersion),
		State:                    row.State,
		StateReason:              row.StateReason,
		StateChangedAt:           row.StateChangedAt,
		BanUntil:                 row.BanUntil,
		CooldownUntil:            row.CooldownUntil,
		LastCheckAt:              row.LastCheckAt,
		ExitIP:                   row.ExitIp,
		ConsecutiveCheckFailures: int(row.ConsecutiveCheckFailures),
		NextCheckAt:              row.NextCheckAt,
		CreatedAt:                row.CreatedAt,
		UpdatedAt:                row.UpdatedAt,
	}
	if row.LastCheckOk != nil {
		p.LastCheckOK = *row.LastCheckOk
	}
	if row.LastLatencyMs != nil {
		p.LastLatencyMs = int(*row.LastLatencyMs)
	}
	if p.Attributes.Tags == nil {
		p.Attributes.Tags = []string{}
	}
	if ns != nil {
		p.NamespaceName = ns.Name
	}
	return p
}

// sealedURL returns the sealed URL of a row.
func sealedURL(row proxydb.Proxy) vault.Sealed {
	return vault.Sealed{Ciphertext: row.UrlCiphertext, WrappedDEK: row.UrlWrappedDek, KEKID: row.UrlKekID}
}

// namespaceOf resolves the catalog snapshot of a proxy row.
func namespaceOf(cat catalog.Catalog, row proxydb.Proxy) (*catalog.Namespace, bool) {
	return cat.Namespace(row.NamespaceID)
}

// authorizeRow checks perm on the namespace of row. Callers without proxy:read
// get NotFound so that proxy IDs of other tenants are not disclosed.
func authorizeRow(cat catalog.Catalog, p *authz.Principal, row proxydb.Proxy, perm authz.Permission) (*catalog.Namespace, error) {
	ns, ok := namespaceOf(cat, row)
	if !ok {
		return nil, apperr.NotFound("proxy not found")
	}
	res := namespaceResource(ns)
	if !p.Can(authz.PermProxyRead, res) {
		return nil, apperr.NotFound("proxy not found")
	}
	if err := p.Require(perm, res); err != nil {
		return nil, err
	}
	return ns, nil
}

// namespaceResource is the authorization resource of namespace-level proxy objects.
func namespaceResource(ns *catalog.Namespace) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
}

// siteResource is the authorization resource of a site of ns.
func siteResource(ns *catalog.Namespace, s *catalog.Site) authz.Resource {
	r := namespaceResource(ns)
	r.SiteID, r.SiteName = s.ID, s.Name
	return r
}

// requireNamespace checks perm on a namespace-level proxy resource.
func requireNamespace(p *authz.Principal, ns *catalog.Namespace, perm authz.Permission) error {
	if ns == nil {
		return apperr.NotFound("namespace not found")
	}
	return p.Require(perm, namespaceResource(ns))
}

// requireTokenScope fails early for API tokens lacking perm on their own
// namespace, so that they get scope_missing instead of not_found for proxies
// addressed by ID. Other principals are checked per proxy.
func requireTokenScope(cat catalog.Catalog, p *authz.Principal, perm authz.Permission) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if p.Kind != authz.KindToken {
		return nil
	}
	ns, ok := cat.Namespace(p.NamespaceID)
	if !ok {
		return apperr.NotFound("namespace not found")
	}
	return p.Require(perm, namespaceResource(ns))
}

// loadAuthorized loads one proxy and checks perm on its namespace.
func (s *Service) loadAuthorized(ctx context.Context, p *authz.Principal, id string, perm authz.Permission) (proxydb.Proxy, *catalog.Namespace, error) {
	if err := requireTokenScope(s.cat, p, perm); err != nil {
		return proxydb.Proxy{}, nil, err
	}
	row, err := s.q.ProxyGet(ctx, id)
	if err != nil {
		return proxydb.Proxy{}, nil, postgres.MapError(err, "proxy")
	}
	ns, err := authorizeRow(s.cat, p, row, perm)
	if err != nil {
		return proxydb.Proxy{}, nil, err
	}
	return row, ns, nil
}

// GetProxy returns one proxy with its bound identity count and the hot state
// of every site the principal can read (proxy:read).
func (s *Service) GetProxy(ctx context.Context, p *authz.Principal, id string) (*Proxy, error) {
	row, ns, err := s.loadAuthorized(ctx, p, id, authz.PermProxyRead)
	if err != nil {
		return nil, err
	}
	views := []*Proxy{newProxyView(row, ns)}
	if err := s.decorate(ctx, p, ns, views); err != nil {
		return nil, err
	}
	return views[0], nil
}

// decorate fills bound identity counts and per-site hot state.
func (s *Service) decorate(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, views []*Proxy) error {
	if len(views) == 0 {
		return nil
	}
	ids := make([]string, len(views))
	for i, v := range views {
		ids[i] = v.ID
	}
	counts, err := s.q.ProxyBoundCounts(ctx, ids)
	if err != nil {
		return fmt.Errorf("count proxy bindings: %w", err)
	}
	byID := make(map[string]int, len(counts))
	for _, c := range counts {
		byID[c.ProxyID] = int(c.Bound)
	}
	for _, v := range views {
		v.BoundIdentities = byID[v.ID]
	}
	return s.readSiteStates(ctx, visibleSites(p, ns), views)
}

// syncProxies propagates committed proxy changes to the hot state in bounded
// chunks. Each chunk gets its own sideEffectTimeout, detached from ctx
// cancellation, so that large imports are not cut off by one shared deadline.
func (s *Service) syncProxies(ctx context.Context, namespaceID string, ids []string) error {
	if s.hot == nil || len(ids) == 0 {
		return nil
	}
	for start := 0; start < len(ids); start += hotSyncChunk {
		end := min(start+hotSyncChunk, len(ids))
		cctx, cancel := detached(ctx)
		err := s.hot.SyncProxies(cctx, namespaceID, ids[start:end])
		cancel()
		if err != nil {
			return fmt.Errorf("sync proxies to hot state: %w", err)
		}
	}
	return nil
}

// record writes an audit entry for principal p.
func (s *Service) record(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, action, resourceID, resourceName, result string, details map[string]any) {
	tenantID, nsID := "", ""
	if ns != nil {
		tenantID, nsID = ns.TenantID, ns.ID
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, tenantID, nsID, action, auditResourceKind, resourceID, resourceName, result, details))
}

// detached returns a context for side effects that must complete even when
// the request context was canceled after the database commit.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sideEffectTimeout)
}

// isNoRows reports whether err is pgx.ErrNoRows.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// inTx runs fn in a READ COMMITTED transaction with the package queries. The
// transaction is committed when fn returns nil and rolled back otherwise
// (including panics).
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *proxydb.Queries) error) error {
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return fn(proxydb.New(tx))
	})
}
