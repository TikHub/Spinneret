# Spinneret v0.1 — Service Contracts (Phase B)

Phase B builds the domain services and Connect handlers in parallel vertical tracks. This document fixes package
boundaries, constructors and the methods other packages call. **Consumers declare the minimal interface they need in
their own package** (Go idiom), with exactly the method signatures listed here, so tracks compile independently; the
server wiring (`internal/server`, Phase C) passes the concrete providers.

Already available (do not re-implement): `apperr`, `appconfig`, `authz`, `observability`, `pkg/*`, `store/postgres`
(+ `db` sqlc package), `store/redis` (+ `lua/common.lua`), `store/clickhouse`, `events`, `vault` (crypto core),
`site` (matcher), `policy` (pure), `identity` (pure types), `catalog/types.go` (types + `Catalog` interface),
`jobs` (runner), `audit` (Recorder + Writer), `api/apiutil`, `testutil`, generated protos in
`gen/go/spinneret/v1` (`spinneretv1`) and `gen/go/spinneret/v1/spinneretv1connect`.

## Import rules (no cycles)
- `catalog` imports `site`, `identity`, `policy`. Therefore **site/identity/policy must never import catalog**.
  DB-backed services for them live in `internal/sitesvc`, `internal/identitysvc`, `internal/policysvc`.
- Handlers live in one package per service group under `internal/api/<name>api` (e.g. `leaseapi`, `siteapi`), each
  exposing `New(...) *Handler` that implements the generated `spinneretv1connect.<Service>Handler` interface(s).
  Handlers return `apperr` errors; the server installs an interceptor that converts them with `apperr.ToConnect`
  and authenticates requests (principal available through `authz.FromContext`).
- `jobs.Job` values are returned by providers (e.g. `ReapJob()`); the server registers them.
- Long-running loops expose `Run(ctx context.Context) error` and are started by the server.

## sqlc query files (one owner each)
`auth.sql` (auth track) · `tenancy.sql` (auth track) · `audit.sql` (auth track, reads only) · `site.sql` + `catalog.sql` (site-catalog) ·
`identity.sql` (identity) · `proxy.sql` (proxy) · `policy.sql` (policy) · `hotstate.sql` (hotstate) · `action.sql` (action) ·
`breaker.sql` (breaker) · `config.sql` (configcenter) · `secret.sql` (vault-secrets) · `notify.sql` (notify) ·
`stats.sql` (stats, writes) · `analytics.sql` (analytics, reads). Query names must be globally unique — prefix them
with the domain (`SiteGet…`, `IdentityList…`). Regenerate only via `./scripts/sqlc-generate.sh`.
Batch inserts may use `pgx.CopyFrom` directly.

---

## auth track — `internal/auth`, `internal/tenancy`, `internal/api/{authapi,tenantapi,accessapi}`
```go
// auth
type Config struct { TokenCacheTTL, SessionTTL time.Duration; CookieSecure string; TrustedProxies []netip.Prefix; InstanceID string }
func NewAuthenticator(cfg Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, bus events.Bus, logger *slog.Logger) *Authenticator
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*authz.Principal, error) // token or session (+CSRF); nil,nil when no credentials
func (a *Authenticator) Run(ctx context.Context) error                                             // bus subscription (token revocation), last-used flush loop
func NewInterceptor(a *Authenticator, public map[string]bool /* procedure → public */, logger *slog.Logger) connect.Interceptor
  // unary + streaming-handler: authenticate, set principal (with X-Spinneret-Node, client IP, user agent),
  // reject unauthenticated for non-public procedures, per-token rate limit → resource_exhausted/rate_limited.
func HTTPMiddleware(a *Authenticator, next http.Handler) http.Handler // for non-Connect endpoints (SSE): principal in context, 401 when absent
type Users struct; func NewUsers(pool, rdb, keys, audit audit.Recorder, cfg Config, logger) *Users
  // Login(ctx, username, password, ip, ua) (session cookie value, *UserView, error); Logout; Me; ChangePassword; Create/Update/ResetPassword/List; role bindings CRUD
func BootstrapPlatformAdmin(ctx context.Context, pool *pgxpool.Pool, username, password, tenantName, namespaceName string, installDefaults NamespaceInstaller) (userID string, err error) // used by `spnr admin init`
type Tokens struct; func NewTokens(pool, bus, audit, logger) *Tokens // Create (returns plaintext once), Revoke, List
func CreateTokenDirect(ctx context.Context, pool *pgxpool.Pool, tenantID, namespaceID, name string, scopes []string, expiresAt *time.Time) (plaintext string, id string, err error) // CLI bootstrap
// tenancy
type NamespaceInstaller interface { InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error } // provided by policysvc
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, installer NamespaceInstaller, audit audit.Recorder, logger *slog.Logger) *Service
  // tenants & namespaces CRUD; namespace creation runs installer in the same tx then cat.Invalidate
```
Handlers: `authapi` (AuthService — Login sets `Set-Cookie`, Logout clears it), `tenantapi` (TenantAdminService),
`accessapi` (AccessAdminService incl. ListAuditLogs).

## site-catalog track — `internal/catalog` (implementation), `internal/sitesvc`, `internal/api/siteapi`
```go
// catalog
func NewStore(pool *pgxpool.Pool, bus events.Bus, logger *slog.Logger) *Store // implements catalog.Catalog
func (s *Store) Run(ctx context.Context) error  // bus subscription (ChannelCatalog) + periodic full reload (60 s)
// sitesvc
type HotSyncer interface { SyncSite(ctx context.Context, siteID string) error; RemoveSite(ctx context.Context, siteKey int64) error }
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot HotSyncer, audit audit.Recorder, logger *slog.Logger) *Service
```
Site creation auto-creates `_default` endpoint groups per client (also when clients are added); deleting a site
requires it to have no identities unless `force`. Every mutation: PG tx → `cat.Invalidate(ns)` → `hot.SyncSite`.

## identity track — `internal/identitysvc`, `internal/api/identityapi`
```go
type HotSyncer interface {
    SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts hotstate.SyncOptions) error
    RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error
    SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error
}
type HotReader interface { IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (hotstate.IdentityHot, error) }
type Operator interface { // provided by action
    OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req action.OperationRequest) (action.BulkResult, error)
    OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req action.OperationRequest) (action.BulkResult, error)
    RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req action.RevertRequest) (action.BulkResult, []string, error)
}
type SecretResolver interface { ResolveForIdentity(ctx context.Context, namespaceID, path string) (string, error) } // provided by vault.SecretStore
func NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, cat catalog.Catalog, hot HotSyncer, audit audit.Recorder, bus events.Bus, logger *slog.Logger) *Service
func NewPayloadCache(pool *pgxpool.Pool, cipher *vault.Cipher, secrets SecretResolver, size int, enabled bool, logger *slog.Logger) *PayloadCache
func (c *PayloadCache) Credential(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error)
func (c *PayloadCache) Invalidate(identityID string)
```
Filter-based bulk operations resolve IDs in identitysvc (max 100 000) and call `Operator.OperateIdentities` in chunks.
The payload pepper comes from `vault.SystemKey(ctx, pool, cipher, "dedupe_pepper", 32)`.

## proxy track — `internal/proxy`, `internal/api/proxyapi`
```go
type Assignment struct { ID, URL, Kind, Region string }
type HotSyncer interface { SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error; RemoveProxies(ctx context.Context, namespaceID string, proxyIDs []string) error }
type Membership interface { Membership(ctx context.Context) (index, total int, err error) } // provided by worker registry
func NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot HotSyncer, audit audit.Recorder, bus events.Bus, logger *slog.Logger) *Service
func NewResolver(pool *pgxpool.Pool, cipher *vault.Cipher, logger *slog.Logger) *Resolver
func (r *Resolver) Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*Assignment, error) // cached decrypted URL + session template rendering
func NewHealthChecker(cfg HealthConfig, pool, cipher, rdb, keys, cat, hot HotSyncer, member Membership, bus, metrics, logger) *HealthChecker
func (h *HealthChecker) Job() jobs.Job
```

## policy track — `internal/policysvc`, `internal/api/policyapi`
```go
type HotSyncer interface { SyncSite(ctx context.Context, siteID string) error }
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot HotSyncer, audit audit.Recorder, logger *slog.Logger) *Service
func (s *Service) InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error // tenancy.NamespaceInstaller
```
Publishing/rollback/binding changes invalidate the catalog and resync sites whose eligible identity types changed.
`DebugReport` compiles the resolved policies (or the draft of a given policy when requested) and runs classify +
evaluate without touching Redis.

## hotstate track — `internal/hotstate`
```go
type SyncOptions struct { ResetHealth, ResetFailures bool }
type IdentityHot struct { /* mirrors proto IdentityHotState */ }
func NewSyncer(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger) *Syncer
// methods: SyncIdentities, RemoveIdentities, SyncAccounts, SyncProxies, RemoveProxies, SyncSite, RemoveSite, RebuildAll, EnsureBuilt(ctx) (rebuilt bool, err error),
//          IdentityHotState(ctx, site, identityID) (IdentityHot, error), ReadyCounts(ctx, site, now) (map[int64]int64, error)
func (s *Syncer) SnapshotJob() jobs.Job
```

## scheduler track — `internal/scheduler`, `internal/api/leaseapi`
```go
type CredentialSource interface { Credential(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error) }
type ProxyResolver interface { Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*proxy.Assignment, error) }
type StatsRecorder interface { RecordAcquire(stats.AcquireRecord); RecordLeaseEnd(stats.LeaseEndRecord) }
type Config struct { ReportShards int; LateReportWindow time.Duration }
func New(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, creds CredentialSource, proxies ProxyResolver, rec StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *Service
func (s *Service) ReleaseLease(ctx context.Context, siteKey int64, leaseID string, now time.Time) (bool, error) // used by worker
func (s *Service) ReapJob() jobs.Job
```

## signal + worker track — `internal/signal`, `internal/worker`, `internal/api/reportapi`
```go
// signal
type Event struct { /* spec 6.2 JSON */ }
func EncodeEvent(e Event) ([]byte, error); func DecodeEvent(b []byte) (Event, error)
type RejectRecorder interface { RecordRejectedReports(namespaceID, node string, n int) }
func NewIngestor(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, rec RejectRecorder, metrics *observability.Metrics, logger *slog.Logger) *Ingestor
// worker
type ActionExecutor interface { Execute(ctx context.Context, in action.ExecInput) (action.ExecResult, error) }
type LeaseReleaser interface { ReleaseLease(ctx context.Context, siteKey int64, leaseID string, now time.Time) (bool, error) }
type BreakerNotifier interface { NotifyRisk(siteKey, egKey int64) }
type StatsRecorder interface { RecordReport(stats.ReportRecord) }
func New(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, exec ActionExecutor, rel LeaseReleaser, brk BreakerNotifier, rec StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *Worker
func (w *Worker) Run(ctx context.Context) error               // registry heartbeat + shard ownership + consumers
func (w *Worker) Membership(ctx context.Context) (index, total int, err error)
```

## stats track — `internal/stats`
```go
type AcquireRecord struct { At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityTypeID, IdentityID, ProxyID, LeaseID, Node, TokenID, Result string; Duration time.Duration; Probe, Sticky bool }
type ReportRecord struct { ReceivedAt, StartedAt, FinishedAt time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID, URI, Method string; HTTPStatus int; BusinessCode, ErrorKind string; Markers []string; Outcome, OutcomeHint, Blame, Rule string; LatencyMs, ResponseBytes int64; Suppressed, Late, Probe bool }
type LeaseEndRecord struct { At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, ProxyID, LeaseID, Node, TokenID, Kind string /* released|expired|abandoned|renewed */; Probe bool }
func NewAggregator(pool *pgxpool.Pool, ch *clickhouse.Writer, metrics *observability.Metrics, logger *slog.Logger) *Aggregator
// RecordAcquire, RecordReport, RecordLeaseEnd, RecordRejectedReports (non-blocking), Run(ctx) (flush every 10 s + on shutdown)
```

## action track — `internal/action`
```go
type ReportContext struct { Namespace *catalog.Namespace; Site *catalog.Site; Group *catalog.EndpointGroup; LeaseID, ReportID string; IdentityKey int64; IdentityID string; AccountKey int64; ProxyKey int64; ProxyID string; Outcome string; RuleName string }
type ExecInput struct { ReportContext; Planned []policy.PlannedAction; Shadow bool; Now time.Time }
type AppliedAction struct { Planned policy.PlannedAction; SubjectKind policy.SubjectKind; SubjectID string; FromState, ToState string; Until time.Time; Skipped bool; SkipReason string }
type ExecResult struct { Applied []AppliedAction }
type OperationRequest struct { Operation string; Scope string; EndpointGroupID string; Duration durationx.Duration; Reason string; ResetFailures, ResetHealth bool }
type RevertRequest struct { SiteID, PolicyID, Rule string; Actions []string; From, To time.Time; ResetFailures, ResetHealth, DryRun bool }
type BulkResult struct { Matched, Succeeded int; Failed []BulkFailure }; type BulkFailure struct { ID, Reason, Message string }
type HotSyncer interface { SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts hotstate.SyncOptions) error; SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error; SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error }
func NewStateWriter(pool *pgxpool.Pool, metrics *observability.Metrics, logger *slog.Logger) *StateWriter // Enqueue(StateChange), EnqueueContext(ctx, StateChange) error, Run(ctx), Flush(ctx); never drops lifecycle changes while PostgreSQL is unavailable (spec §6.6)
func NewExecutor(cfg ExecutorConfig, rdb rueidis.Client, keys redis.Keys, writer *StateWriter, bus events.Bus, metrics *observability.Metrics, logger *slog.Logger) *Executor
func NewOperator(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot HotSyncer, writer *StateWriter, audit audit.Recorder, bus events.Bus, logger *slog.Logger) *Operator
func (o *Operator) ExpiryJob() jobs.Job
```
State changes publish `identity.state` / `proxy.state` events on `events.NamespaceChannel(ns)` with data
`{"subject_kind","subject_id","site_id","from","to","action","until","reason"}` (notify subscribes for `identity_expired`;
the bus path is lossy under bursts, so the notify evaluation job also scans `state_events` rows with
`action='expire'`, `to_state='expired'` from a watermark in `system_settings` and emits the alerts not yet
de-duplicated — every transition to `expired` must therefore be recorded with action `expire`).

## breaker track — `internal/breaker`, `internal/api/breakerapi`
```go
type Reverter interface{} // internal
func New(cfg Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, bus events.Bus, audit audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *Service
func (s *Service) NotifyRisk(siteKey, egKey int64)                 // non-blocking
func (s *Service) Run(ctx context.Context) error                    // fast-evaluation loop for notified groups
func (s *Service) EvaluateJob() jobs.Job                            // 5 s sweep, each instance, per-eg lock
func (s *Service) RuntimeContent(ctx context.Context, namespaceID, kind string) (content string, version int64, err error) // kind breakers|site_switches
func (s *Service) RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error)
```
Transitions publish `breaker.transition` on the namespace channel and `runtime` channel events
`{"ns","kind":"breakers","version"}`.

## configcenter track — `internal/configcenter`, `internal/api/configapi`
```go
type SecretReader interface { ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (vault.SecretValue, error) }
type RuntimeProvider interface { RuntimeContent(ctx context.Context, namespaceID, kind string) (string, int64, error); RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error) }
func New(cfg Config, pool *pgxpool.Pool, cat catalog.Catalog, bus events.Bus, secrets SecretReader, runtime RuntimeProvider, audit audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *Service
func (s *Service) Run(ctx context.Context) error // bus subscriptions (config, runtime) → wake watchers
```
Versions of a (namespace, group, key) never repeat: `DeleteItem` records the item's last version in
`config_version_floors` and the next publish (create with publish, publish, rollback) uses `max(current, floor) + 1`.

## vault-secrets track — `internal/vault` (secrets.go, systemkeys.go, rewrap.go), `internal/api/secretapi`
```go
type SecretValue struct { Path string; Version int; Value string; ExpiresAt *time.Time }
func SystemKey(ctx context.Context, pool *pgxpool.Pool, c *Cipher, name string, size int) ([]byte, error) // load or create (race-safe)
func NewSecretStore(pool *pgxpool.Pool, c *Cipher, audit audit.Recorder, logger *slog.Logger) *SecretStore
func (s *SecretStore) ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error) // authz secret:read (tokens) or secret:reveal (users) + audit
func (s *SecretStore) ResolveForIdentity(ctx context.Context, namespaceID, path string) (string, error) // no per-read audit (aggregated elsewhere)
func NewRewrapper(pool *pgxpool.Pool, c *Cipher, logger *slog.Logger) *Rewrapper // Start(ctx) (bool, error), Status(ctx) (KEKStatus, error)
```
**Import note:** `vault` may import `catalog` and `authz` (catalog does not import vault).

## notify track — `internal/notify`, `internal/api/notifyapi`
```go
func New(cfg Config, pool *pgxpool.Pool, cipher *vault.Cipher, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, bus events.Bus, audit audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *Service
func (s *Service) Run(ctx context.Context) error   // bus subscriptions → alerts (breaker transitions, identity expired) + delivery workers
func (s *Service) EvaluateJob() jobs.Job           // 30 s leader: identity_expired recovery from state_events, low watermark, ban spike, report backlog, unknown ratio, client_error spike, secret expiring
func (s *Service) Emit(ctx context.Context, a Alert) error
```

## analytics track — `internal/analytics`, `internal/api/dashboardapi`
```go
func New(pool *pgxpool.Pool, ch chdriver.Conn /* may be nil */, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger) *Service
```
Reads `outcome_stats_minutely`, `acquire_stats_minutely`, `node_stats_minutely`, `risk_events`, `identities`, `proxies`,
Redis ready queues / health hashes / breakers, and ClickHouse `report_events` (QueryRequestEvents returns
`unavailable` when ClickHouse is disabled).
