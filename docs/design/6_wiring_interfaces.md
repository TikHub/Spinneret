# Phase B wiring interfaces (from track reviewers)

Generated from the Phase B review results; used to write internal/server wiring.

## auth

CONSTRUCTORS (package auth):
- auth.Config{TokenCacheTTL, SessionTTL time.Duration; CookieSecure string ("auto"|"true"|"false"); TrustedProxies []netip.Prefix (convert appconfig strings with netx.ParsePrefixes); InstanceID string; Argon2 auth.Argon2Params (zero value = production m=64MiB,t=3,p=2)}.
- auth.NewAuthenticator(cfg Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, bus events.Bus /*nil ok*/, logger *slog.Logger) *Authenticator
  - Authenticate(ctx, *http.Request) (*authz.Principal, error)
  - Run(ctx) error: must be started; subscribes to events.ChannelTokens and flushes token last-used every 30 s.
  - InvalidateToken(id), InvalidateUser(id /* "" = all */), RequestMeta(*http.Request) RequestMeta, Config() Config.
- auth.NewInterceptor(a *Authenticator, public map[string]bool, logger *slog.Logger) connect.Interceptor
  - Pass auth.PublicProcedures() ({"/spinneret.v1.AuthService/Login": true}).
  - Install it before the protovalidate and error interceptors.
  - It returns *connect.Error itself, and also re-issues the session cookie (Set-Cookie) on successful responses.
- auth.HTTPMiddleware(a *Authenticator, next http.Handler) http.Handler: for the SSE endpoint (also re-issues refreshed cookies).
- auth.TLSMiddleware(next http.Handler) http.Handler: wrap the whole mux so CookieSecure=auto detects direct TLS inside Connect handlers.
- auth.NewUsers(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, rec audit.Recorder, cfg Config, logger *slog.Logger, opts ...auth.UsersOption) *Users
  - Pass auth.WithEventBus(bus) so user and binding changes invalidate caches on every instance.
- auth.NewTokens(pool *pgxpool.Pool, bus events.Bus, rec audit.Recorder, logger *slog.Logger) *Tokens
- auth.NewAuditLogs(pool *pgxpool.Pool) *AuditLogs
- auth.BootstrapPlatformAdmin(ctx, pool *pgxpool.Pool, username, password, tenantName, namespaceName string, installDefaults auth.NamespaceInstaller) (userID string, err error)
- auth.CreateTokenDirect(ctx, pool *pgxpool.Pool, tenantID, namespaceID, name string, scopes []string, expiresAt *time.Time) (plaintext, id string, err error)

CONSTRUCTORS (package tenancy):
- tenancy.NewService(pool *pgxpool.Pool, cat catalog.Catalog, installer tenancy.NamespaceInstaller, rec audit.Recorder, logger *slog.Logger) *Service
- tenancy.BootstrapPlatformAdmin(...): same signature as the auth one; delegates to it.

HANDLERS:
- authapi.New(users *auth.Users, logger *slog.Logger) *authapi.Handler implements spinneretv1connect.AuthServiceHandler.
- tenantapi.New(svc *tenancy.Service, logger *slog.Logger) *tenantapi.Handler implements TenantAdminServiceHandler.
- accessapi.New(tokens *auth.Tokens, users *auth.Users, auditLogs *auth.AuditLogs, cat catalog.Catalog, logger *slog.Logger) *accessapi.Handler implements AccessAdminServiceHandler.

CONSUMER INTERFACES (identical method sets):
- auth.NamespaceInstaller and tenancy.NamespaceInstaller: `InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error`.
  - Provider: *policysvc.Service (verified that internal/policysvc/defaults.go has this method).
  - tenancy.Service also consumes catalog.Catalog (only Invalidate(ctx, namespaceID) error is used) and audit.Recorder.
- Import rule: tenancy imports auth, so auth must never import tenancy.

EVENTS: published only on events.ChannelTokens, never on namespace channels.
- Type "token.revoked", data {"token_id"}, with TenantID and NamespaceID set.
- Type "user.changed", data {"user_id"} or {"all":true}.

REDIS KEYS:
- P:sess:<sha256 hex of cookie> HASH {user_id, cred, created_ms, expires_ms, ip, ua} with PX=SessionTTL (sliding). cred is the user's password generation at login (password_changed_at in Unix microseconds, "0" when unset); a session whose cred differs from the user's is invalid, and a session without cred is not accepted.
- P:sess:user:<userID> SET of session hashes, PX=SessionTTL.
- P:rl:login_user:<lower username> and P:rl:login_ip:<ip>: counters, 15 m expiry.
- Lua scripts auth_session_refresh, auth_session_credential (moves the session kept by ChangePassword to the new generation) and auth_login_release (single key each, cluster-safe).

HEADERS AND COOKIES:
- Request headers: X-Spinneret-Tenant (or ?tenant= for GET/HEAD), X-Spinneret-CSRF: 1 (cookie auth on unsafe methods), X-Spinneret-Node.
- Cookie: spinneret_session, HttpOnly, SameSite=Strict, Path=/, Max-Age=SessionTTL.

## site-catalog

catalog (package github.com/Evil0ctal/Spinneret/internal/catalog):
- `func NewStore(pool *pgxpool.Pool, bus events.Bus, logger *slog.Logger) *Store`. It implements `catalog.Catalog`. `bus` may be nil; a nil logger discards logs.
- `(*Store) Run(ctx context.Context) error`: long-running loop; start it in the server. It subscribes to `events.ChannelCatalog` (debounced 100ms per namespace), reloads everything every 60s, and when nothing is loaded yet does a full load with retries (1s backoff doubling up to 60s). It returns nil when ctx is done.
- `(*Store) Loaded() bool`: true once a ReloadAll succeeded; use it for /readyz. The server may also call `store.ReloadAll(ctx)` synchronously at startup before serving.
- Exported consts: `DefaultDebounce`, `DefaultFullReloadInterval`, `DefaultLoadTimeout`, `DefaultRetryBackoff`, and `InvalidateEventType = "invalidate"`.
- Events: `Invalidate` publishes on `events.ChannelCatalog` an `events.Event{Type: "invalidate", NamespaceID: ns, Data: {"ns": ns, "origin": <random per-Store id>}}`. The store ignores its own origin; events without an origin, or with only NamespaceID set, are honoured. Nothing is published on namespace channels.

sitesvc (package github.com/Evil0ctal/Spinneret/internal/sitesvc):
- `func NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot HotSyncer, rec audit.Recorder, rdb rueidis.Client, keys redis.Keys, logger *slog.Logger) *Service`.
  - Deviation from 3_service_contracts.md: it adds `rdb` and `keys` before `logger`. They are used read-only for available_identities (`ZCOUNT Keys.Ready(siteKey, egKey) -inf nowMs`) and breaker_state (`HGET Keys.Breaker(siteKey, egKey) st`, missing means closed).
  - `hot`, `rec` and `rdb` may be nil (no sync, no audit, and 0/closed figures respectively).
- Consumer interface declared: `type HotSyncer interface { SyncSite(ctx context.Context, siteID string) error; RemoveSite(ctx context.Context, siteKey int64) error }`. Provider: `*hotstate.Syncer`; both methods were verified in internal/hotstate/site.go.
- Exported methods:
  - `SiteRef(ctx, siteID) (SiteRef, error)`
  - `SiteRefByName(ctx, namespaceID, name) (SiteRef, error)`
  - `GetSite(ctx, siteID) (Site, error)`
  - `ListSites(ctx, namespaceID string, access SiteAccess{All bool; SiteIDs []string}, pageSize int, afterName string) (SitePage, error)`
  - `CreateSite(ctx, p *authz.Principal, ns *catalog.Namespace, CreateSiteInput{Name, DisplayName, Description string; Clients []string}) (Site, error)`
  - `UpdateSite(ctx, p, siteID, UpdateSiteInput{DisplayName, Description *string; Clients []string}) (Site, error)`
  - `DeleteSite(ctx, p, siteID string, force bool) error`
  - `GroupRef(ctx, groupID) (GroupRef, error)`
  - `GetEndpointGroup(ctx, groupID) (EndpointGroup, error)`
  - `ListEndpointGroups(ctx, SiteRef, client string, pageSize int, after GroupCursor) (GroupPage, error)`
  - `CreateEndpointGroup(ctx, p, siteID, CreateGroupInput{Client, Name, Description string; LowWatermark int; Rules []URIRule}) (EndpointGroup, error)`
  - `UpdateEndpointGroup(ctx, p, groupID, UpdateGroupInput{Description *string; LowWatermark *int}) (EndpointGroup, error)`
  - `DeleteEndpointGroup(ctx, p, groupID) error`
  - `ListURIRules(ctx, groupID string, pageSize int, after *RuleCursor) (RulePage, error)`
  - `ReplaceURIRules(ctx, p, groupID string, rules []URIRule) ([]URIRule, error)`
  - `TestURI(site *catalog.Site, client, uri string) (TestURIResult, error)`
- Every mutation runs the PostgreSQL transaction, then records audit, then `cat.Invalidate(ns)`, then `hot.SyncSite(siteID)` (or `hot.RemoveSite(siteKey)` for DeleteSite). A failure after commit is returned as internal and PostgreSQL is not rolled back.
- Audit actions: site.create, site.update, site.delete, endpoint_group.create, endpoint_group.update, endpoint_group.delete, uri_rules.replace.
- Nothing is written to Redis.

siteapi (package github.com/Evil0ctal/Spinneret/internal/api/siteapi):
- `func New(svc *sitesvc.Service, cat catalog.Catalog, rec audit.Recorder) *Handler`. It implements `spinneretv1connect.SiteAdminServiceHandler`; mount it with `spinneretv1connect.NewSiteAdminServiceHandler(h, opts...)`. `rec` records denied mutations and may be nil.
- Permissions:
  - site:read on the site for GetSite, ListEndpointGroups, ListURIRules and TestURI. ListSites is filtered through SiteFilter, with a per-site Can fallback.
  - site:write at namespace level for CreateSite, UpdateSite and DeleteSite.
  - site:write on the site for endpoint-group and URI-rule mutations.
- Isolation: other tenants' (or, for tokens, other namespaces') sites and groups return not_found.
- Dependencies: the principal must be set by the auth interceptor, and protovalidate must run before the handler (no IGNORE_ALWAYS fields in site_admin.proto).

## identity

CONSTRUCTORS
- `identitysvc.NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, cat catalog.Catalog, hot identitysvc.HotSyncer, ops identitysvc.Operator, audit audit.Recorder, bus events.Bus, logger *slog.Logger) *identitysvc.Service`
  - pepper = vault.SystemKey(ctx, pool, cipher, "dedupe_pepper", 32). Payload writes return internal if it is shorter than 16 bytes.
  - audit, bus and logger may be nil.
- `identitysvc.NewPayloadCache(pool *pgxpool.Pool, cipher *vault.Cipher, secrets identitysvc.SecretResolver, size int, enabled bool, logger *slog.Logger) *identitysvc.PayloadCache`
  - Pass cfg.PayloadCacheSize and cfg.PayloadCache. size <= 0 means 200000.
  - Methods:
    - `Credential(ctx, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error)`: satisfies scheduler.CredentialSource directly.
    - `Invalidate(identityID string)`
    - `Stats() identitysvc.PayloadCacheStats{Hits, Misses uint64; Entries int}`
  - No goroutines, nothing to Run.
- `identityapi.New(cat catalog.Catalog, svc *identitysvc.Service, hot identitysvc.HotReader, logger *slog.Logger) *identityapi.Handler`
  - Implements spinneretv1connect.IdentityAdminServiceHandler, all 19 RPCs.
  - hot may be nil: GetIdentityHotState then returns unavailable/rebuilding and GetIdentity omits hot_state.
  - Requests need a principal in the context and rely on the server's protovalidate and apperr interceptors.

CONSUMER INTERFACES (declared in package identitysvc)
- `type SyncOptions struct { ResetHealth bool; ResetFailures bool }`
- `type HotSyncer interface`:
  - `SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts SyncOptions) error`
  - `RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error`
  - `SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error`
  - Provider: adapter over *hotstate.Syncer.
    - SyncIdentities calls `syncer.SyncIdentities(ctx, siteID, ids, hotstate.SyncOptions(opts))`; direct struct conversion works (same fields, same order).
    - RemoveIdentities and SyncAccounts have identical signatures and pass straight through.
- `type HotReader interface`: `IdentityHotState(ctx context.Context, site *catalog.Site, identityID string) (HotState, error)`
  - Provider: adapter over `(*hotstate.Syncer).IdentityHotState` returning hotstate.IdentityHot.
  - Copy fields by name; the field order differs, so no direct conversion.
  - Pass apperr errors (e.g. NotFound) through unchanged.
- `type HotState struct`:
  - `Present bool; State string; ActiveLeases int`
  - `SiteCooldownUntil, SiteReuseUntil, ExclusiveUntil time.Time`
  - `BoundProxyID string; GlobalScore float64; GlobalSamples int`
  - `Groups []EndpointHotState; AccountCooldownUntil time.Time`
  - Zero times mean unset.
- `type EndpointHotState struct`:
  - `EndpointGroup, EndpointGroupID, Client string`
  - `Score float64; Samples, ConsecutiveFailures int`
  - `CooldownUntil, ReuseUntil, LastUsedAt, AvailableAt time.Time`
  - `InReadyQueue bool`
  - Maps from hotstate.EndpointHot, which has the same field names.
- `type Operator interface`:
  - `OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req OperationRequest) (BulkResult, error)`
  - `OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req OperationRequest) (BulkResult, error)`
  - `RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req RevertRequest) (BulkResult, []string, error)`
  - Provider: adapter over *action.Operator (action.NewOperator).
    - `action.OperationRequest(req)` and `action.RevertRequest(req)` are direct conversions (identical field order and types).
    - BulkResult must be copied with a loop over Failed, because action.BulkFailure and identitysvc.BulkFailure are distinct types.
- Local operation types:
  - `type OperationRequest struct { Operation, Scope, EndpointGroupID string; Duration durationx.Duration; Reason string; ResetFailures, ResetHealth bool }`
  - `type RevertRequest struct { SiteID, PolicyID, Rule string; Actions []string; From, To time.Time; ResetFailures, ResetHealth, DryRun bool }`
  - `type BulkResult struct { Matched, Succeeded int; Failed []BulkFailure }`
  - `type BulkFailure struct { ID, Reason, Message string }`
- `type SecretResolver interface`: `ResolveForIdentity(ctx context.Context, namespaceID, path string) (string, error)`
  - *vault.SecretStore satisfies it directly (internal/vault/secrets_read.go).

SERVICE METHODS (all enforce authorization themselves)
- Identity types:
  - `ListIdentityTypes(ctx, p, ns, TypeQuery) (TypePage, error)`
  - `GetIdentityType(ctx, p, id) (IdentityType, error)`
  - `CreateIdentityType(ctx, p, ns, siteName, specYAML string) (IdentityType, error)`
  - `UpdateIdentityType(ctx, p, id, specYAML string) (IdentityType, error)`: rehashes identities when unique_by changes.
  - `DeleteIdentityType(ctx, p, id) error`
  - `PreviewDelivery(ctx, p, ns, PreviewInput) (PreviewResult, error)`
- Identities:
  - `ListIdentities(ctx, p, ns, IdentityQuery) (IdentityPage, error)`
  - `GetIdentity(ctx, p, id string, reveal bool) (IdentityDetail, error)`
  - `ResolveIdentity(ctx, p, id string, perm authz.Permission) (IdentityRef, error)`
  - `ImportIdentities(ctx, p, ns, ImportInput) (ImportResult, error)`: rows referencing (secret_ref) secrets the caller may not read (tokens: secret:read on `<ns>/<path>`; users: secret:reveal) or that do not exist are rejected, unless the same field of the existing identity's stored payload already references them.
  - `UpdateIdentityPayload(ctx, p, id string, payload map[string]any) (Identity, error)`: same secret_ref rule (permission_denied / invalid_argument).
  - `UpdateIdentity(ctx, p, id string, IdentityUpdate) (Identity, error)`
- Operations:
  - `OperateIdentities(ctx, p, ids []string, OperationRequest) (BulkResult, error)`: no ns argument; the service groups IDs by namespace.
  - `BulkOperateIdentities(ctx, p, ns, IdentityFilter, OperationRequest, limit int, dryRun bool) (BulkResult, error)`
  - `RevertActions(ctx, p, ns, RevertInput{Site string; Request RevertRequest}) (BulkResult, []string, error)`
- Other:
  - `ListStateEvents(ctx, p, ns, StateEventQuery) (StateEventPage, error)`
  - `ListAccounts(ctx, p, ns, AccountQuery) (AccountPage, error)`
  - `UpsertAccount(ctx, p, ns, AccountUpsert) (Account, error)`
  - `OperateAccount(ctx, p, accountID string, OperationRequest) (Account, BulkResult, error)`

EVENTS
- `identity.state` on events.NamespaceChannel(nsID), only for lifecycle changes caused by payload updates, at most 100 per request.
  - Data: `{"subject_kind":"identity","subject_id","site_id","from","to","action":"payload_update","until":null,"reason":"payload updated"}`
- `cat.Invalidate(ctx, nsID)` after identity type create, update and delete.

REDIS
- None written directly; everything goes through HotSyncer.

AUDIT ACTIONS
- identity_type.create, identity_type.update (details include rehashed_identities when > 0), identity_type.delete
- identity.import, identity.update_payload, identity.update, identity.reveal, identity.bulk_operate
- account.upsert

## proxy

CONSTRUCTORS (package internal/proxy)
- proxy.NewService(pool *pgxpool.Pool, cipher *vault.Cipher, pepper []byte, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot proxy.HotSyncer, rec audit.Recorder, bus events.Bus, logger *slog.Logger) *proxy.Service
  - pepper = vault.SystemKey(ctx, pool, cipher, "dedupe_pepper", 32).
  - rec nil → audit.Nop; bus nil → no events; logger nil → slog.Default.
  - Methods (each takes the principal and enforces permissions itself):
    - ImportProxies(ctx, p *authz.Principal, ns *catalog.Namespace, req proxy.ImportRequest{Format, Data string; Defaults proxy.Defaults{Kind, Region, City, Provider string; Tags []string; MaxConcurrency int; SessionTemplate string}; DryRun bool}) (proxy.ImportResult{Created, Updated, Unchanged int; Failed []proxy.ImportFailure{Line int; Message string}}, error)
    - ListProxies(ctx, p, ns, proxy.ListFilter{States, Kinds, Providers, Regions, Tags []string; Search string; PageSize int; PageToken string}) (proxy.ListResult{Proxies []*proxy.Proxy; NextPageToken string; Total int}, error)
    - GetProxy(ctx, p, id string) (*proxy.Proxy, error)
    - UpdateProxy(ctx, p, id string, proxy.UpdateRequest{URL, Kind, Region, City, Provider *string; MaxConcurrency *int; Tags []string; SetTags bool; SessionTemplate *string}) (*proxy.Proxy, error)
    - OperateProxies(ctx, p, ids []string, proxy.OperationRequest{Operation, Site string; Duration durationx.Duration; Reason string}) (proxy.BulkResult{Matched, Succeeded int; Failed []proxy.BulkFailure{ID, Reason, Message string}}, error)
    - DeleteProxies(ctx, p, ids []string) (proxy.BulkResult, error)
    - GetProviderStats(ctx, p, ns, proxy.StatsRequest{Site string; Start, End *time.Time}) ([]proxy.ProviderStats{Provider string; Proxies, Active, Dead int; Requests int64; SuccessRatio, RiskRatio, AvgLatencyMs float64}, error)

- proxy.NewResolver(pool *pgxpool.Pool, cipher *vault.Cipher, logger *slog.Logger) *proxy.Resolver
  - Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*proxy.Assignment, error), where proxy.Assignment = struct{ID, URL, Kind, Region string}. Returns apperr NotFound for a missing proxy or a namespace mismatch. This satisfies scheduler.ProxyResolver.
  - Invalidate(proxyID string)
  - Subscribe(bus events.Bus) (unsubscribe func()): the wiring should call this once at startup (it subscribes to events.ChannelAll for proxy.state) and call unsubscribe on shutdown. Without it, changes show up within 30s.

- proxy.NewHealthChecker(cfg proxy.HealthConfig, pool *pgxpool.Pool, cipher *vault.Cipher, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot proxy.HotSyncer, member proxy.Membership, bus events.Bus, metrics *observability.Metrics, logger *slog.Logger) *proxy.HealthChecker
  - HealthConfig{CheckURL string; Interval, Timeout time.Duration; ExitIPURL, GeoIPDB string; Concurrency int}. Map from appconfig ProxyCheckURL, ProxyCheckInterval, ProxyCheckTimeout, ProxyExitIPURL, GeoIPDB. Defaults: http://example.com/, 60s, 10s, concurrency 64. metrics may be nil.
  - Job() jobs.Job: name "proxy_health_check", jobs.EachInstance, Interval = cfg.Interval, Timeout = Interval + Timeout. Register it on instances that run workers.
  - RunOnce(ctx) (int, error)
  - CheckProxy(ctx, p *authz.Principal, id string) (proxy.CheckResult{OK bool; LatencyMs int; ExitIP, Region, Error, TenantID, NamespaceID string}, error)
  - Close() error: releases the GeoIP database; call on shutdown.

CONSUMER INTERFACES DECLARED IN internal/proxy
- proxy.HotSyncer { SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error; RemoveProxies(ctx context.Context, namespaceID string, proxyIDs []string) error }
  - Provider: *hotstate.Syncer.
  - Chunks are at most 1000 IDs.
  - RemoveProxies is called inside the delete transaction, while the proxies rows still exist and are locked FOR UPDATE by that transaction. It must not take row locks on proxies or proxy_bindings; the current hotstate queries are plain SELECTs, which is fine.
  - SyncProxies is called with detached contexts, 10s per chunk.
- proxy.Membership { Membership(ctx context.Context) (index, total int, err error) }
  - Provider: *worker.Worker.
  - total <= 0 or index outside [0, total) means this instance skips the run.
  - Sharding: xxhash64(proxyID) % total == index (the task text; spec §6.8 says hkey % live workers).

HANDLER (package internal/api/proxyapi)
- proxyapi.New(cat catalog.Catalog, svc proxyapi.Service, checker proxyapi.Checker, rec audit.Recorder) *proxyapi.Handler
  - Implements spinneretv1connect.ProxyAdminServiceHandler.
  - Pass *proxy.Service as svc and *proxy.HealthChecker as checker. A nil checker makes CheckProxy return failed_precondition.
  - rec records proxy.check entries; nil → Nop. The service audits all other mutations.
- proxyapi.Service interface: exactly the 7 *proxy.Service methods above, with identical signatures.
- proxyapi.Checker { CheckProxy(ctx context.Context, p *authz.Principal, id string) (proxy.CheckResult, error) }

EVENTS
- Only proxy.state, on events.NamespaceChannel(nsID). Envelope carries TenantID and NamespaceID, plus SiteID for site-scoped operations.
- Data: {"subject_kind":"proxy","subject_id","site_id","from","to","action","until" (RFC3339 or null),"reason"}
- Actions: disable, enable, ban, unban, cooldown, quarantine, activate (from unquarantine), archive, restore, reset_stats, health_check (automatic active↔dead), update (UpdateProxy, or health check filling region/city), delete (to is empty).

POSTGRESQL WRITES
- proxies; state_events rows with subject_kind proxy, scope proxy or proxy_site, actor user:/token:/system.
- ban_until holds the end of a ban or a quarantine (the action and hotstate convention).
- Audit actions: proxy.import, proxy.update, proxy.<operation>, proxy.delete, proxy.check.

REDIS WRITES (every write also runs SADD P:T:dirty p<p>)
- cooldown.lua on P:T:px:<p>: cd (site) or gcd (global, every site of the namespace) plus pid. pxrdy uses ZADD XX to max(cd, gcd), moving earlier only when al < mc.
- health.lua: sc ("%.2f"), sts, sn, nf, lf, on existing hashes only.
- reset.lua: HDEL sc sts sn nf lf.
- Reads: pid st sc sts sn al cd.

## policy

Constructors:
- `policysvc.NewService(pool *pgxpool.Pool, cat catalog.Catalog, hot policysvc.HotSyncer, rec audit.Recorder, logger *slog.Logger, opts ...policysvc.Option) *policysvc.Service`
  - `hot` may be nil (no hot-state syncs), `rec` nil means `audit.Nop`, `logger` nil means `slog.Default()`.
  - Deviation from docs/design/3_service_contracts.md: the variadic `opts` was added. Calls written to the contract still compile.
- `policysvc.WithEventBus(bus events.Bus) policysvc.Option`: without it, no policy.published events are published. Wiring should pass the Redis bus.
- `(*policysvc.Service).InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error`: satisfies `tenancy.NamespaceInstaller` and the auth bootstrap `NamespaceInstaller`. It runs inside the caller's transaction and is idempotent; the caller invalidates the catalog after commit.
- `policyapi.New(svc *policysvc.Service, cat catalog.Catalog) *policyapi.Handler`: implements `spinneretv1connect.PolicyAdminServiceHandler`. Mount it with `spinneretv1connect.NewPolicyAdminServiceHandler(h, ...)` behind the auth + apperr + protovalidate interceptors. The handler validates `DebugReportRequest.report` itself.

Consumer interface declared (the only one):
- `policysvc.HotSyncer interface { SyncSite(ctx context.Context, siteID string) error }`. Concrete provider: `*hotstate.Syncer` (its `SyncSite` matches). It must be safe for concurrent calls on different sites: up to 4 run in parallel.

Exported service methods (all take a principal; handlers pass `authz.MustPrincipal` / `apiutil.Namespace` results):
- `ListPolicies(ctx, p, ns *catalog.Namespace, ListPoliciesInput{Kind, Search, PageSize, AfterKind, AfterName}) (PolicyPage{Policies, More, Total}, error)`
- `GetPolicy(ctx, p, id) (Policy, error)`
- `CreatePolicy(ctx, p, ns, CreateInput{Kind, YAML, Publish, Comment}) (Policy, error)`
- `SaveDraft(ctx, p, id, yaml) (Policy, error)`
- `PublishPolicy(ctx, p, PublishInput{ID, Comment, ExpectedVersion}) (Policy, error)`
- `RollbackPolicy(ctx, p, id, version int, comment) (Policy, error)`
- `DeletePolicy(ctx, p, id) error`
- `ListPolicyVersions(ctx, p, id, pageSize, beforeVersion int) (VersionPage, error)`
- `DiffPolicyVersions(ctx, p, id, from, to int) (Diff{FromYAML, ToYAML, UnifiedDiff}, error)`
- `ListBindings(ctx, p, ns, kind policy.Kind, siteName string) ([]Binding, error)`
- `SetBinding(ctx, p, policyID, Target{Site, Client, EndpointGroup}) (Binding, error)`
- `DeleteBinding(ctx, p, id) error`
- `ResolvePolicies(ctx, p, ns, Target) ([]ResolvedPolicy{Kind, PolicyID, Name, Version, Level, YAML}, error)`
- `DebugReport(ctx, p, ns, DebugInput) (DebugResult, error)`
- `ValidatePolicy(p, kind, yaml) (ValidationResult{Valid, Errors, NormalizedYAML}, error)`

Local types: `Policy`, `Binding`, `Version`, `ListPoliciesInput`, `PolicyPage`, `CreateInput`, `PublishInput`, `VersionPage`, `Diff`, `Target`, `ResolvedPolicy`, `DebugInput`, `DebugReport`, `DebugResult`, `PlannedAction`, `CounterRequirement`, `ValidationResult`, `PublishedEvent`, `Option`.

Events:
- `events.TypePolicyPublished` ("policy.published") on `events.NamespaceChannel(nsID)`.
- `Event.TenantID` and `NamespaceID` are set. `Event.SiteID` is set for site-level bind/unbind and for publishes that applied a site bind block.
- Data = `PublishedEvent` JSON: `{"policy_id","name","kind","version","action": publish|rollback|delete|bind|unbind, "binding_id"?, "site_id"?, "actor"}`.
- `internal/server/sse.go` already maps it to `policy:read`.

Audit actions: `policy.create`, `policy.save_draft`, `policy.publish`, `policy.rollback`, `policy.delete`, `policy.bind`, `policy.unbind`, with result `ok` or `denied`.

Redis: nothing written or read. PostgreSQL: private sqlc package `internal/policysvc/policysvcdb`, regenerated via `sqlc generate -f internal/policysvc/sqlc.yaml`.

Test fixture for other packages: `policysvctest.New(t)`, returning a `*Fixture` with Pool, Catalog, Namespace, Shop, Forum, Search, Hot, Audit, Events, Service and principals.

## hotstate

Constructor: `hotstate.NewSyncer(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger) *hotstate.Syncer`. The logger may be nil. The Syncer is safe for concurrent use.

Exported types:
- `type SyncOptions struct{ ResetHealth, ResetFailures bool }`
- `type IdentityHot struct{ Present bool; State string; ActiveLeases int; SiteCooldownUntil, SiteReuseUntil, ExclusiveUntil time.Time; BoundProxyID string; GlobalScore float64; GlobalSamples int; AccountCooldownUntil time.Time; Groups []EndpointHot }`
- `type EndpointHot struct{ EndpointGroupID, EndpointGroup, Client string; Score float64; Samples, ConsecutiveFailures int; CooldownUntil, ReuseUntil, LastUsedAt, AvailableAt time.Time; InReadyQueue bool }`
- Zero times mean unset. Counts are int; convert to proto int32. Groups are sorted by name.

Methods on *Syncer:
- `SyncIdentities(ctx, siteID string, identityIDs []string, opts SyncOptions) error`
- `RemoveIdentities(ctx, siteID string, identityIDs []string) error`
- `SyncAccounts(ctx, siteID string, accountIDs []string) error`
- `SyncProxies(ctx, namespaceID string, proxyIDs []string) error`
- `RemoveProxies(ctx, namespaceID string, proxyIDs []string) error`
- `SyncSite(ctx, siteID string) error`: returns apperr NotFound when the site is gone.
- `RemoveSite(ctx, siteKey int64) error`
- `RebuildAll(ctx) error`: returns apperr Unavailable with reason `rebuilding` when another instance holds the lock.
- `EnsureBuilt(ctx) (rebuilt bool, err error)`
- `IdentityHotState(ctx, site *catalog.Site, identityID string) (IdentityHot, error)`: NotFound for an unknown identity, InvalidArgument for a nil site.
- `ReadyCounts(ctx, site *catalog.Site, now time.Time) (map[int64]int64, error)`: keyed by endpoint group hkey.
- `SnapshotJob() jobs.Job`: name `hotstate_snapshot`, mode Leader, interval 60 s, timeout 50 s. Needs a runner created with jobs.NewPGLocks.

Consumer interfaces declared by this track: none. It uses catalog.Catalog directly and publishes no events.

Providers the wiring must adapt:
1. sitesvc.HotSyncer{SyncSite(ctx, siteID string) error; RemoveSite(ctx, siteKey int64) error}: pass *Syncer directly.
2. policysvc.HotSyncer{SyncSite}: pass directly.
3. proxy.HotSyncer{SyncProxies; RemoveProxies}: pass directly. The health checker takes the same interface.
4. identitysvc.HotSyncer{SyncIdentities(ctx, siteID, ids, identitysvc.SyncOptions) error; RemoveIdentities; SyncAccounts}: needs a small adapter converting identitysvc.SyncOptions{ResetHealth, ResetFailures} to hotstate.SyncOptions.
5. identitysvc.HotReader{IdentityHotState(ctx, *catalog.Site, id) (identitysvc.HotState, error)}: needs an adapter copying IdentityHot to HotState field by field. Groups map EndpointHot to identitysvc.EndpointHotState with identical field names and types.
6. action.HotSyncer{SyncIdentities(ctx, siteID, ids, action.SyncOptions) error; SyncAccounts; SyncProxies}: needs an adapter converting action.SyncOptions.

Startup and CLI:
- Call EnsureBuilt(ctx) at startup, before serving acquire. It blocks while another instance rebuilds, bounded by ctx.
- Register SnapshotJob() with the jobs runner.
- `spnr rebuild` calls RebuildAll.
- Analytics can use ReadyCounts.

Redis fields written:
- `meta`: ns, site, paused (from PostgreSQL), built, plus the additive egs.
- `id`: iid st ty tv pv acc rg bu qu act px sct. `px` is Redis-first. gs/gts/gn are deleted only on health reset; gnf/glf on health or failure reset. Health resets rewrite existing `hs` entries to the group baseline and keep cd/ru/lu.
- `acc`: st bu cd sct.
- `accm`.
- `rdy`, `hs`, `pxrdy`, `dirty`, `P:meta:epoch`.
- `px`: pid st kd rg pv tg mc uv gcd sct, plus bu and qu (new; mirror proxies.ban_until). gcd = max(Redis, PostgreSQL).
- `sct` is the lifecycle change time: authoritative syncs apply PostgreSQL lifecycle fields unless Redis holds a newer change still in force (implementation spec §5.8).
- On restore (HSETNX): gs/gts/gn only when samples > 0, and scd/sru/lu; sc/sts/sn only when samples > 0, and nf/lf/cd.

## scheduler

CONSTRUCTORS
- `scheduler.New(cfg scheduler.Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, creds scheduler.CredentialSource, proxies scheduler.ProxyResolver, rec scheduler.StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *scheduler.Service`
  - rec, metrics and logger may be nil.
  - If creds or proxies is nil, every render fails and the lease is released.
- `scheduler.Config{ReportShards int; LateReportWindow time.Duration; OnBreakerHalfOpen func(siteKey, groupKey int64); OnProxyBound func(scheduler.ProxyBinding)}`
  - ReportShards: pass appconfig.ReportShards (1..256, default 16).
  - LateReportWindow: pass appconfig.LateReportWindow (default 10m).
  - OnBreakerHalfOpen: wire to breakerService.NotifyRisk (same signature, non-blocking). Breaker detects lazy transitions itself (half_open with ou>0); the hook only speeds that up.
  - OnProxyBound: optional; runs on the request path and must not block. No provider exists yet.
- Methods on *scheduler.Service:
  - `Acquire(ctx, scheduler.AcquireRequest) (*scheduler.Grant, error)`
  - `AcquireBatch(ctx, scheduler.AcquireRequest) ([]*scheduler.Grant, error)`
  - `Renew(ctx, leaseID string, extend time.Duration) (time.Time, error)`
  - `Release(ctx, leaseID string) (bool, error)`
  - `ReleaseLease(ctx, siteKey int64, leaseID string, now time.Time) (bool, error)`: satisfies worker.LeaseReleaser as-is.
  - `ReapJob() jobs.Job`: name lease_reaper, jobs.EachInstance, Interval 1s, Timeout 5s. Register it on instances that should reap.
  - Lease RPCs require an authz.KindToken principal from the context. Node name comes from Principal.Node.
- `leaseapi.New(svc leaseapi.Service) *leaseapi.Handler`: implements spinneretv1connect.LeaseServiceHandler. Pass *scheduler.Service.

CONSUMER INTERFACES (declared in scheduler)
- `CredentialSource { Credential(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error) }`
  - Satisfied directly by *identitysvc.PayloadCache.
- `ProxyResolver { Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*scheduler.ProxyAssignment, error) }`
  - Needs a server adapter over *proxy.Resolver (whose Resolve returns *proxy.Assignment): copy ID, URL, Kind, Region.
  - A nil assignment with a nil error is treated as a failure.
- `StatsRecorder { RecordAcquire(scheduler.AcquireRecord); RecordLeaseEnd(scheduler.LeaseEndRecord) }`
  - Needs an adapter over *stats.Aggregator. Field names and types are identical, so copy field by field.
  - Must not block.
- leaseapi declares `Service { Acquire(ctx, scheduler.AcquireRequest) (*scheduler.Grant, error); AcquireBatch(ctx, scheduler.AcquireRequest) ([]*scheduler.Grant, error); Renew(ctx, string, time.Duration) (time.Time, error); Release(ctx, string) (bool, error) }`.

LOCAL TYPES
- `ProxyAssignment{ID, URL, Kind, Region string}`
- `AcquireRecord{At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityTypeID, IdentityID, ProxyID, LeaseID, Node, TokenID, Result string; Duration time.Duration; Probe, Sticky bool}`
  - Result is one of ok, exhausted, circuit_open, site_paused, no_proxy, error.
  - Probe is true for half-open breaker probes only.
- `LeaseEndRecord{At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, ProxyID, LeaseID, Node, TokenID, Kind string; Probe bool}`
  - Kind is one of released, expired, abandoned, renewed.
- `ProxyBinding{At time.Time; NamespaceID, SiteID string; SiteKey int64; IdentityID string; IdentityKey int64; ProxyID string; ProxyKey int64; Rebound bool; RebindDay string (YYYYMMDD UTC); RebindsToday int}`
- `AcquireRequest{Site, Client, URI, EndpointGroup, SessionKey string; Wait time.Duration (0..5s); Count int (AcquireBatch only, 1..50)}`
- `Grant{Lease Lease; Credential *identity.Credential; Proxy *ProxyAssignment (nil = none); RenewBefore time.Duration}`
- `Lease{ID, IdentityID, IdentityType, EndpointGroup string; ExpiresAt time.Time; Sticky, Probe bool}`
- Constants: MaxBatch=50, MaxWait=5s, MaxRenewExtension=30m, Result*, End*.

EVENTS AND REDIS
- Events published: none.
- Redis keys follow spec §5 as in the implementer's report, with one addition: acquire now also runs ZREM on inactive or missing proxies in pxrdy and pushes full proxies by min(ttl, 5s).

## signal-worker

PACKAGE signal (internal/signal)
- func NewIngestor(cfg signal.Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, rec signal.RejectRecorder, metrics *observability.Metrics, logger *slog.Logger) *signal.Ingestor
  - Config{ReportShards int; DedupTTL time.Duration; StreamMaxLen int64}. Non-positive values use defaults 16 / 1h / 1_000_000; ReportShards is capped at 256.
  - rec, metrics and logger may be nil.
- func (*Ingestor) Ingest(ctx, p *authz.Principal, node string, reports []*spinneretv1.Report) (accepted, duplicated int, rejected []signal.Rejected, err error)
- type Rejected struct{ ReportID string; Reason apperr.Reason; Message string }
- Consumer interface: RejectRecorder{ RecordRejectedReports(namespaceID, node string, n int) }. *stats.Aggregator satisfies it directly (compile-verified).
- Other exports: type Event (spec 6.2 JSON; methods Received/Started/Finished), EncodeEvent(Event) ([]byte, error), DecodeEvent([]byte) (Event, error), ErrInvalidEvent, StreamFieldVersion="v", StreamFieldData="d", StreamVersion="1", MaxReports=500, DefaultReportShards, MaxReportShards, DefaultDedupTTL, DefaultStreamMaxLen.

PACKAGE reportapi (internal/api/reportapi)
- func New(ingest reportapi.Ingester, cat catalog.Catalog) *reportapi.Handler. Implements spinneretv1connect.ReportServiceHandler.
- Consumer interface: Ingester{ Ingest(ctx context.Context, p *authz.Principal, node string, reports []*spinneretv1.Report) (accepted, duplicated int, rejected []signal.Rejected, err error) }. Pass *signal.Ingestor.
- The node name comes from principal.Node, set by the auth interceptor from X-Spinneret-Node.
- The RPC requires report:write on the token namespace or on at least one of its sites (users: platform admins only); each report is also checked against its lease site.
- No audit entry: the RPC is data-plane traffic.

PACKAGE worker (internal/worker)
- func New(cfg worker.Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, exec worker.ActionExecutor, rel worker.LeaseReleaser, brk worker.BreakerNotifier, rec worker.StatsRecorder, metrics *observability.Metrics, logger *slog.Logger) *worker.Worker
  - exec, rel, brk, rec, metrics and logger may be nil.
- Config fields:
  - ReportShards int (default 16, max 256)
  - InstanceID string (empty generates a random id)
  - LateReportWindow time.Duration (default 10m; must equal the scheduler's)
  - BatchSize int (default 100, max 1000)
  - Block time.Duration (default 2s)
  - Optional: HeartbeatInterval 2s, LiveWindow 10s, OwnerTTL 10s, OwnerRefresh 3s, PendingInterval 10s
- Methods:
  - (*Worker) Run(ctx) error: heartbeat, shard ownership and consumers. Returns nil on ctx cancel; an error only if already running or rdb/cat is nil.
  - (*Worker) Membership(ctx) (index, total int, err error): index = position in the sorted live registry, -1 with nil err when not a live member; err only when Redis is unreadable. Satisfies proxy.Membership directly (compile-verified).
  - (*Worker) InstanceID() string
  - (*Worker) OwnedShards() []int
- Other exports: ConsumerGroup="workers" and Default* constants.
- Consumer interfaces:
  - ActionExecutor{ Execute(ctx context.Context, in worker.ExecInput) (worker.ExecResult, error) }. Needs an adapter over *action.Executor: `a.e.Execute(ctx, action.ExecInput{ReportContext: action.ReportContext(in.ReportContext), Planned: in.Planned, Shadow: in.Shadow, Now: in.Now})`, returning worker.ExecResult{} and the error. action.ExecResult is not convertible (action.AppliedAction has an extra SubjectKey field); the worker ignores the result.
  - LeaseReleaser{ ReleaseLease(ctx context.Context, siteKey int64, leaseID string, now time.Time) (bool, error) }. *scheduler.Service satisfies it directly.
  - BreakerNotifier{ NotifyRisk(siteKey, egKey int64) }: must not block. *breaker.Service satisfies it directly.
  - StatsRecorder{ RecordReport(worker.ReportRecord) }: must not block. Adapter: `agg.RecordReport(stats.ReportRecord(rec))`; direct conversion compiles.
- Local mirror types (field order identical to action/stats):
  - ReportContext{Namespace *catalog.Namespace; Site *catalog.Site; Group *catalog.EndpointGroup; LeaseID, ReportID string; IdentityKey int64; IdentityID string; AccountKey int64; ProxyKey int64; ProxyID, Outcome, RuleName string}
  - ExecInput{ReportContext; Planned []policy.PlannedAction; Shadow bool; Now time.Time}
  - AppliedAction{Planned policy.PlannedAction; SubjectKind policy.SubjectKind; SubjectID, FromState, ToState string; Until time.Time; Skipped bool; SkipReason string}
  - ExecResult{Applied []AppliedAction}
  - ReportRecord{ReceivedAt, StartedAt, FinishedAt time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID, URI, Method string; HTTPStatus int; BusinessCode, ErrorKind string; Markers []string; Outcome, OutcomeHint, Blame, Rule string; LatencyMs, ResponseBytes int64; Suppressed, Late, Probe bool}
- Events published: none.
- Redis keys:
  - Written by signal: P:R:dd:<rid> (SET NX PX), P:R:stream (XADD MAXLEN ~ with fields v 1 d <json>).
  - Written by worker: P:workers (ZADD/ZREMRANGEBYSCORE using the Redis TIME clock), P:R:owner (TryLock/RefreshLock/Unlock), and the stream group `workers` (starts at id 0).
  - Written by observe.lua:
    - P:T:ckpt[shard]; lease rc; P:T:q:<eg>:<i> <w>:c/n/p
    - P:T:win:<eg>:<b> t/s/r, P:T:winh:<eg>:<b> (PFADD on captcha), P:T:aeg
    - P:T:brk:<eg> ps/pk (probe lease on a half_open breaker only; reads st/ou/man)
    - P:T:xa:p:<p> and P:T:xa:i:<i>; P:T:hs:<eg> packed value (lu unchanged)
    - P:T:id:<i> gs/gts/gn plus worker-owned gnf/glf; P:T:px:<p> sc/sts/sn/nf/lf
    - P:T:cnt:<i|a|p><hkey>:<outcome>:<w>; P:T:dirty entries e<eg>:<i>, g<i>, p<p>
    - Reads P:T:bans:<i>.
- Metrics set: ReportIngestTotal, ReportTotal, ReportLag, ReportProcessDuration, StreamPending{shard}, StreamOwnedShards.

## stats

Package internal/stats (import github.com/Evil0ctal/Spinneret/internal/stats). It declares no consumer interfaces, publishes no events and writes no Redis keys.

Constructor:
  func NewAggregator(pool *pgxpool.Pool, ch *clickhouse.Writer, metrics *observability.Metrics, logger *slog.Logger) *Aggregator
  ch, metrics and logger may be nil. With a nil ch no ClickHouse rows are queued. Aggregator.Run does NOT start the ClickHouse writer: the server must also run ch.Run(ctx), created with clickhouse.NewWriter(conn, logger, flushEvery, maxBatch).

Methods (all Record* calls are non-blocking, O(1) and safe for concurrent use):
  func (a *Aggregator) RecordAcquire(r AcquireRecord)
  func (a *Aggregator) RecordReport(r ReportRecord)
  func (a *Aggregator) RecordLeaseEnd(r LeaseEndRecord)
  func (a *Aggregator) RecordRejectedReports(namespaceID, node string, n int)
  func (a *Aggregator) Run(ctx context.Context) error  // flushes every 10 s, then a final flush (10 s timeout); returns nil; ErrRunning if already running
  func (a *Aggregator) Flush(ctx context.Context) error // serialized; the error names tables whose rows were kept for the next flush
  func (a *Aggregator) Counters() Counters             // DroppedRecords, DroppedRows map[table]int64; Invalid int64

Record types, with field names, types and order exactly as in 3_service_contracts.md:
  AcquireRecord{At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityTypeID, IdentityID, ProxyID, LeaseID, Node, TokenID, Result string; Duration time.Duration; Probe, Sticky bool}
  ReportRecord{ReceivedAt, StartedAt, FinishedAt time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID, URI, Method string; HTTPStatus int; BusinessCode, ErrorKind string; Markers []string; Outcome, OutcomeHint, Blame, Rule string; LatencyMs, ResponseBytes int64; Suppressed, Late, Probe bool}
  LeaseEndRecord{At time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityID, ProxyID, LeaseID, Node, TokenID, Kind string; Probe bool}

Constants: ResultOK/ResultExhausted/ResultCircuitOpen/ResultSitePaused/ResultNoProxy/ResultError, LeaseEndReleased/Expired/Abandoned/Renewed, LeaseEventAcquired="acquired", EmptyNode="_", DefaultFlushInterval=10s, DefaultMaxKeys=200000, DefaultMaxRiskEvents=50000, DefaultMaxRiskBytes=64MiB, MaxRowsPerStatement=5000. Error: ErrRunning.

Wiring adapters (conversions compile-verified against the current consumer packages):
- scheduler.StatsRecorder {RecordAcquire(scheduler.AcquireRecord); RecordLeaseEnd(scheduler.LeaseEndRecord)}: the mirror structs are identical, so an adapter can call agg.RecordAcquire(stats.AcquireRecord(r)) and agg.RecordLeaseEnd(stats.LeaseEndRecord(r)).
- worker.StatsRecorder {RecordReport(worker.ReportRecord)}: adapter calls agg.RecordReport(stats.ReportRecord(r)).
- signal.RejectRecorder {RecordRejectedReports(namespaceID, node string, n int)}: *stats.Aggregator satisfies it directly.
- Run agg.Run(ctx) once per instance (spec 6.8, stats flush on each instance).

Tables written: acquire_stats_minutely, payload_access_minutely, node_stats_minutely, outcome_stats_minutely, identity_stats_hourly (additive upserts), and risk_events (COPY). ClickHouse report_events and lease_events go through ch.AddReport and ch.AddLease. Metrics: DBWriteBatches{writer=<table name>, result=ok|retry|error|dropped}. On a missing partition it calls postgres.EnsurePartitions(ctx, pool, now) at most once per flush.

## action

Package github.com/Evil0ctal/Spinneret/internal/action. Files: types.go, applier.go, executor.go, events.go, stateevents.go, statewriter.go, statewriter_sql.go, operator.go, opspec.go, operator_identity.go, operator_account.go, revert.go, revert_cooldown.go, expiry.go, lua/apply.lua (script name "action.apply"), a private sqlc setup (sqlc.yaml, queries/*.sql, actiondb/), and tests.

CONSTRUCTORS AND METHODS
- `func NewStateWriter(pool *pgxpool.Pool, metrics *observability.Metrics, logger *slog.Logger) *StateWriter`
  - `Enqueue(c StateChange)`: queue of 100k. Non-lifecycle changes (cooldown/shadow events) never block; when the queue is full the oldest of them are spilled (counted). A lifecycle change (UpdateSubject, not shadow) waits for space for at most 1 minute, then is dropped and counted.
  - `EnqueueContext(ctx context.Context, c StateChange) error`: like Enqueue, waiting for space until ctx is done; returns an error when the change was dropped. The executor uses it (backpressure on the worker; the wait is bounded to 1 minute for all changes of one report).
  - `Run(ctx context.Context) error`: flush every 200ms or 500 changes; while PostgreSQL is unavailable the unwritten batch is kept and retried with a backoff of 200ms doubling to 5s (nothing is dropped); data errors drop only the rejected changes; on ctx done, drains with a 10s timeout; returns nil. **Start it on every instance.**
  - `Flush(ctx context.Context) error`, `Dropped() int64`, `Spilled() int64`, `Written() int64`, `Pending() int`.
- `type ExecutorConfig struct{ RecordCooldownEvents bool }`: set from appconfig.Config.RecordCooldownEvents.
- `func NewExecutor(cfg ExecutorConfig, rdb rueidis.Client, keys redis.Keys, writer *StateWriter, bus events.Bus, metrics *observability.Metrics, logger *slog.Logger) *Executor`
  - `(*Executor).Execute(ctx context.Context, in ExecInput) (ExecResult, error)`: safe for concurrent use; errors only when apply.lua fails on the report's site. Idempotent per report (lease and report ID): a retry with the same input replays the recorded apply.lua results, so the worker may retry after a timeout; state events of a report have deterministic IDs and are stored once.
  - `(*Executor).IdempotentExecute() bool`: always true; declares the per-report idempotency (worker.IdempotentActionExecutor, forwarded by the server's worker executor adapter), without which the worker does not retry a failed Execute.
  - Shadow runs when in.Shadow is set OR in.Group.Action.Shadow().
- `func NewOperator(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, hot HotSyncer, writer *StateWriter, auditRec audit.Recorder, bus events.Bus, logger *slog.Logger) *Operator` (hot, writer, auditRec, bus and logger may be nil)
  - `OperateIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req OperationRequest) (BulkResult, error)`: max 10000 ids per call; identity:operate checked per site.
  - `OperateAccount(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req OperationRequest) (BulkResult, error)`: operations ban|unban|cooldown|disable|enable; the BulkResult describes member identities.
  - `RevertActions(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req RevertRequest) (BulkResult, []string, error)`: returns at most 1000 affected identity IDs.
  - `ExpiryJob() jobs.Job`: Name "action.expiry", Interval 10s, Mode jobs.Leader, Timeout 60s. **Register with the jobs runner.**
  - `RunExpiry(ctx context.Context) error`: skips the pass (error) while Redis does not answer PING; re-synchronizes subjects whose hot-state push failed on earlier passes until that succeeds, and on the first pass of an instance (or after missed passes) the expiry releases of the last 10 minutes.
  - PostgreSQL-first pushes (apply.lua "set") run at the commit time and never overwrite a newer Redis-first change (skip reason newer_state).

TYPES (identitysvc and worker declare mirrors; the server adapts them field by field)
- `ReportContext{ Namespace *catalog.Namespace; Site *catalog.Site; Group *catalog.EndpointGroup; LeaseID, ReportID string; IdentityKey int64; IdentityID string; AccountKey int64; ProxyKey int64; ProxyID string; Outcome string; RuleName string }`
- `ExecInput{ ReportContext; Planned []policy.PlannedAction; Shadow bool; Now time.Time }`
- `AppliedAction{ Planned policy.PlannedAction; SubjectKind policy.SubjectKind; SubjectID string; SubjectKey int64; FromState, ToState string; Until time.Time; Skipped bool; SkipReason string }`
  - SubjectKey is an addition; worker.AppliedAction lacks it, so the adapter drops it.
  - SubjectID is empty for account subjects.
- `ExecResult{ Applied []AppliedAction }`
- `OperationRequest{ Operation, Scope, EndpointGroupID string; Duration durationx.Duration; Reason string; ResetFailures, ResetHealth bool }`
- `RevertRequest{ SiteID, PolicyID, Rule string; Actions []string; From, To time.Time; ResetFailures, ResetHealth, DryRun bool }`
- `BulkResult{ Matched, Succeeded int; Failed []BulkFailure }`, `BulkFailure{ ID, Reason, Message string }`
  - Failure reasons: not_found, permission_denied, invalid_transition, invalid_argument, not_in_hot_state, state_changed, internal.
- `StateChange` (exported; used by Enqueue): the state_events columns plus SubjectKey, UpdateSubject and StateReason.

CONSUMER INTERFACE AND LOCAL TYPE
```go
type SyncOptions struct{ ResetHealth, ResetFailures bool }
type HotSyncer interface {
    SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts SyncOptions) error
    SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error
    SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error
}
```
Provider is *hotstate.Syncer through a small adapter: SyncIdentities converts action.SyncOptions to hotstate.SyncOptions{ResetHealth, ResetFailures}; SyncAccounts and SyncProxies already match.

Other providers:
- `audit.Recorder` → *audit.Writer
- `events.Bus` → events.NewRedisBus
- `catalog.Catalog` → *catalog.Store
- `worker.ActionExecutor` → *action.Executor through a type adapter
- `identitysvc.Operator` → *action.Operator through a type adapter (mirrors have identical fields)

EVENTS
Published on events.NamespaceChannel(nsID) with type `identity.state` (subject_kind identity or account) or `proxy.state`. Data JSON (StateEventData):
`{"subject_kind","subject_id","subject_key"(only when id unknown),"site_id","from","to","action","scope"(omitempty),"until"(RFC3339 or null),"permanent"(omitempty),"reason","actor"(omitempty)}`
Published only for state transitions and ban extensions; never for cooldowns, stat resets, cooldown reverts or shadow mode. Actions include ban, expire, quarantine, activate, unban, unquarantine, disable, enable, archive, restore, revert.

REDIS WRITES (apply.lua, site slot, KEYS[1]=P:T:meta)
- `id:<i>`: st, bu, qu, scd, act, sct (lifecycle change time); HDEL gs/gts/gn on health reset.
- `hs:<eg>`: cd; score/sts/samples/nfail/lastfail on resets.
- `rdy:<eg>`: ZADD XX GT pushes, sp_rescore, ZREM, ZADD for eligible groups on 'set'.
- `acc:<a>`: st, bu, cd, sct.
- `px:<p>`: st, cd, gcd, sct, plus non-spec bu and qu.
- `axr:<token>`: recorded results of an executor call (ARGV[2] idempotency token), PX 15 min. ARGV is now `[now, token, op...]`.
- `pxrdy`: ZADD XX GT or ZREM.
- `rcd:<eg>`: `<i>|<prevCd>|<prevNfail>`, retention max(2×breaker window, 10m).
- `rcds`: `<i>|<prevScd>`, 10m.
- `bans:<i>`: now-ms members, 30d.
- `dirty`: e<eg>:<i>, g<i>, p<p>.
- `cnt:i<hkey>:<outcome>:<windowMs>`: DEL on reset_stats.

POSTGRESQL WRITES
- identities, accounts and proxies state columns.
- state_events via COPY (actor "system" or principal.Actor()); the StateWriter inserts with ON CONFLICT DO NOTHING (idempotent per event ID).
- Accounts: the StateWriter guard and automatic-ban revert candidates compare with the account's latest lifecycle state_events row, not updated_at.
- Metric db_write_batches_total{writer="state_events",result=ok|error|dropped}.
- Metrics spinneret_state_writer_pending_changes, spinneret_state_writer_dropped_changes_total, spinneret_state_writer_spilled_changes_total (registered on the metrics registry).
- Metric actions_total{site,action,scope,mode}.

## breaker

Package internal/breaker (import "github.com/Evil0ctal/Spinneret/internal/breaker")

Constructor:
  func New(cfg breaker.Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, bus events.Bus, rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *breaker.Service
  - audit, metrics and logger may be nil.
  - Config zero values select defaults. Fields: EvalInterval (5s), ActiveWindow (2m), NotifyMinInterval (1s), LockTTL (4s), NotifyBuffer (4096), Concurrency (16), IOTimeout (5s), Now func() time.Time (nil = time.Now).

Contract methods:
  func (s *Service) NotifyRisk(siteKey, egKey int64)
  - Non-blocking. Satisfies worker.BreakerNotifier.
  func (s *Service) Run(ctx context.Context) error
  - Loop that evaluates notified groups. Start it on instances that run workers. Returns nil when ctx is done.
  func (s *Service) EvaluateJob() jobs.Job
  - Name "breaker_evaluate", Interval 5s, Mode jobs.EachInstance, Timeout 9s. Register with the jobs runner.
  func (s *Service) RuntimeContent(ctx context.Context, namespaceID, kind string) (string, int64, error)
  func (s *Service) RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error)
  - Together these satisfy configcenter.RuntimeProvider. kind is "breakers" or "site_switches"; anything else returns invalid_argument.

Additional exported methods (used by breakerapi):
  Sweep(ctx) error
  Get(ctx, *authz.Principal, groupID string) (*breaker.Status, error)
  List(ctx, *authz.Principal, *catalog.Namespace, breaker.ListFilter) (breaker.ListPage, error)
  Open(ctx, *authz.Principal, groupID, duration, reason string) (*breaker.Status, error)
  - duration "" or "permanent" means indefinite; the maximum is 365d.
  Close(ctx, *authz.Principal, groupID, reason string) (*breaker.Status, error)
  ListEvents(ctx, *authz.Principal, *catalog.Namespace, breaker.EventFilter) (breaker.EventPage, error)
  SetSitePaused(ctx, *authz.Principal, *catalog.Namespace, site string, paused bool, reason string) (breaker.SiteSwitch, error)

Exported helpers:
  func ParseOpenDuration(string) (time.Duration, error)

Local types:
  Status{TenantID, NamespaceID, Namespace, SiteID, Site, Client, EndpointGroup, EndpointGroupID, State string; OpenUntil time.Time; ConsecutiveOpens int64; Manual bool; Reason string; Version int64; LastOpenedAt, LastClosedAt time.Time; Window WindowMetrics; Probe ProbeMetrics; SitePaused bool; PolicyID, PolicyName string}
  WindowMetrics{Total, Success, Risk, CaptchaIdentities int64; RiskRatio, SuccessRatio float64}
  ProbeMetrics{Samples, Successes, Issued int64}
  ListFilter{Site, Client string; States []string; PageSize int32; PageToken string}
  ListPage{Breakers []Status; NextPageToken string; Total int}
  EventFilter{Site, EndpointGroupID, Trigger string; Start, End *time.Time; PageSize int32; PageToken string}
  EventPage{Events []Event; NextPageToken string}
  Event{ID string; CreatedAt time.Time; SiteID, Site, Client, EndpointGroup, EndpointGroupID, FromState, ToState, Trigger, Reason string; OpenUntil *time.Time; Metrics map[string]any; Actor string}
  SiteSwitch{SiteID, Site string; Paused bool; Reason string; PausedAt *time.Time; PausedBy string; Changed bool}
  TransitionData, RuntimeEventData{ns,kind,version}, EventMetrics, BreakersContent, SiteSwitchesContent

Constants:
  StateClosed/StateOpen/StateHalfOpen
  TriggerAuto/Manual/Probe/SiteSwitch
  SiteRunning/SitePaused
  KindBreakers/KindSiteSwitches
  RuntimeEventType = "runtime"
  EvaluateJobName
  AuditBreakerOpen "breaker.open", AuditBreakerClose "breaker.close", AuditSitePause "site.pause", AuditSiteResume "site.resume"

Events published:
  - events.NamespaceChannel(ns), Type events.TypeBreakerTransition ("breaker.transition"), with SiteID set. Data is TransitionData {namespace, site, site_id, client, endpoint_group, endpoint_group_id, from, to, trigger, reason, open_until (RFC 3339 | null), consecutive_opens, manual, actor, metrics{total, success, risk, captcha_identities, risk_ratio, success_ratio, probe_samples, probe_successes, reverted_endpoint_cooldowns?, reverted_site_cooldowns?}}.
  - Site switches send the same event with trigger "site_switch", from/to "running"/"paused", and empty group/client fields.
  - events.ChannelRuntime, Type "runtime", data {"ns","kind","version"}.
  - catalog.Invalidate(ns) on a site switch.

Redis writes:
  - P:T:brk:<eg> (all 12 fields); P:T:brko via SADD/SREM (the evaluation also heals it).
  - revert.lua: hs:<eg> cd/nfail (lowered only), id:<i> scd (mode all), rdy:<eg> rescore, dirty "e<eg>:<i>" and "g<i>"; consumes rcd:<eg> and rcds members inside the window.
  - meta "paused" (only when the meta hash exists).
  - P:rtv:<ns> HINCRBY "breakers" / "site_switches".
  - P:T:lock:brk:<eg>.
  - Reads: win, winh, aeg.

PostgreSQL:
  - Private sqlc package internal/breaker/breakerdb.
  - Queries BreakerInsertEvent, BreakerListEvents, BreakerLockSite, BreakerUpdateSitePaused, BreakerNamespace, BreakerNamespaceSites, BreakerGroupsByKeys.
  - Writes breaker_events and sites.paused, paused_reason, paused_at, paused_by.

Package internal/api/breakerapi:
  func New(svc breakerapi.Service, cat catalog.Catalog) *breakerapi.Handler
  - Implements spinneretv1connect.BreakerAdminServiceHandler. Pass the *breaker.Service as svc.
  Consumer interface breakerapi.Service (satisfied by *breaker.Service):
    List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f breaker.ListFilter) (breaker.ListPage, error)
    Get(ctx context.Context, p *authz.Principal, groupID string) (*breaker.Status, error)
    Open(ctx context.Context, p *authz.Principal, groupID, duration, reason string) (*breaker.Status, error)
    Close(ctx context.Context, p *authz.Principal, groupID, reason string) (*breaker.Status, error)
    ListEvents(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f breaker.EventFilter) (breaker.EventPage, error)
    SetSitePaused(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site string, paused bool, reason string) (breaker.SiteSwitch, error)

Wiring summary:
  - Handler: breakerapi.New(brk, cat) mounted with spinneretv1connect.NewBreakerAdminServiceHandler.
  - Jobs: jobs.Runner.Add(brk.EvaluateJob()).
  - Long-running loop: go brk.Run(ctx).
  - Worker: worker.New(..., brk /*BreakerNotifier*/, ...).
  - Config center: configcenter.New(..., runtime: brk, ...).

Test fixture (tests only): internal/breaker/breakertest.New(t) *Env.

## configcenter

**Constructor**
```go
configcenter.New(cfg configcenter.Config, pool *pgxpool.Pool, cat catalog.Catalog, bus events.Bus,
    secrets configcenter.SecretReader, runtime configcenter.RuntimeProvider,
    rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *configcenter.Service
```
Zero values fall back to defaults.
- `bus`, `rec`, `logger` and `metrics` may be nil.
- `secrets == nil`: items with secret references fail node reads with failed_precondition.
- `runtime == nil`: the `_runtime` items do not exist.
- Pass a literal nil, not a typed nil pointer, when a provider is absent.

**Config fields**
- `MaxWatchers int`: set from `appconfig.Config.MaxWatchers` (SPINNERET_MAX_WATCHERS); default 20000.
- `MaxWatchTimeout`: default 60s.
- `DefaultWatchTimeout`: default 30s.
- `ResyncInterval`: default 30s.
- `MaxCachedVersions int`: default 100000.
- `ContentCacheBytes int64`: default 64 MiB.
- `MaxResponseBytes int64`: default 32 MiB.
- `OperationTimeout`: default 15s.
- All durations are `time.Duration`.

**Run**
`func (s *Service) Run(ctx context.Context) error` subscribes to `events.ChannelConfig` and `events.ChannelRuntime` and runs the watch-hub refresher. It returns nil when ctx is done. Start it on every instance that serves the API.

**Handlers**
`configapi.New(svc *configcenter.Service, cat catalog.Catalog) *configapi.Handler`
- The one `*Handler` implements both `spinneretv1connect.ConfigServiceHandler` and `spinneretv1connect.ConfigAdminServiceHandler`. Mount it with `NewConfigServiceHandler(h, opts...)` and `NewConfigAdminServiceHandler(h, opts...)`.
- Handlers expect the principal in ctx (auth interceptor) and return apperr errors (error interceptor converts them).

**Consumer interfaces and local types (package configcenter)**
```go
type SecretValue struct { Path string; Version int; Value string; ExpiresAt *time.Time }
type SecretReader interface {
    ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error)
}
type RuntimeProvider interface {
    RuntimeContent(ctx context.Context, namespaceID, kind string) (string, int64, error)
    RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error)
}
```

**SecretReader provider**
- `*vault.SecretStore` has the same method but returns `vault.SecretValue`. The fields are identical and in the same order, so a direct struct conversion works:
```go
type vaultSecrets struct{ s *vault.SecretStore }
func (a vaultSecrets) ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (configcenter.SecretValue, error) {
    v, err := a.s.ReadSecret(ctx, p, ns, path, version, purpose)
    return configcenter.SecretValue(v), err
}
```
- Contract the provider must keep:
  - version 0 means the current version.
  - Permission failures are apperr errors with code PermissionDenied: tokens need secret:read, users need secret:reveal.
  - A missing secret or version is apperr NotFound.
  - The provider writes the secret.read audit entry.
  - Purpose passed in is `config:<group>/<key>`.

**RuntimeProvider provider**
- `*breaker.Service` satisfies it directly.
- kind is `breakers` or `site_switches`; the version is the `rtv:<ns>` counter (0 when missing).
- The API exposes version provider + 1.

**Events published**
- On `events.ChannelConfig`:
  - Type `config.published`, data `{"ns","group","key","version"}`.
  - Type `config.deleted`, same data with version 0.
- On `events.NamespaceChannel(nsID)`: Type `events.TypeConfigPublished`, data `{"item_id","group","key","version","source_version"(rollbacks only),"actor"}`.

**Events consumed**
- `ChannelConfig` (above).
- `ChannelRuntime`: `{"ns","kind","version"}`, which matches `breaker.RuntimeEventData`.

**Audit actions**
- `config.create`, `config.draft.save`, `config.publish`, `config.rollback`, `config.delete` (result ok, or denied on permission failure).
- `config.read` (denied, when a referenced secret cannot be read).
- Resource kind: `config_item`.

**Redis**
The package reads and writes no Redis keys.

**Exported service methods**
- Admin: `ListItems`, `GetItem`, `GetItemByLocator`, `CreateItem`, `SaveDraft`, `Publish`, `Rollback`, `DeleteItem`, `ListVersions`, `DiffVersions`.
- Node: `GetConfig`, `BatchGetConfig`, `WatchConfig`.
- Helpers: `ParseSecretRefs`, `ValidSecretPath`.
- Handlers already call these; the server needs nothing else.

## vault-secrets

PACKAGE internal/vault (concrete providers)
- func SystemKey(ctx context.Context, pool *pgxpool.Pool, c *Cipher, name string, size int) ([]byte, error)
  - Use SystemKey(ctx, pool, cipher, "dedupe_pepper", 32) for the identitysvc and proxy pepper.
  - Returns an error if the stored key has a different size.
- func NewSecretStore(pool *pgxpool.Pool, c *Cipher, rec audit.Recorder, logger *slog.Logger) *SecretStore
  - rec and logger may be nil.
  - The server must run `go store.Run(ctx)` on every role that reads secrets: API (admin, SecretService, configcenter) and the scheduler payload cache. Run flushes last_accessed_at every 10 s, does a final flush on cancel, and always returns nil.
  - Methods:
    - Create(ctx, *authz.Principal, *catalog.Namespace, CreateSecretInput{Path, Value string; Description string; Tags []string; ExpiresAt *time.Time}) (Secret, error)
    - Update(ctx, *authz.Principal, id string, UpdateSecretInput{Value *string; Description *string; Tags []string; SetTags bool; ExpiresAt *time.Time; ClearExpiresAt bool}) (Secret, error)
    - Delete(ctx, *authz.Principal, id string) error
    - Get(ctx, *authz.Principal, id string) (Secret, error)
    - List(ctx, *authz.Principal, *catalog.Namespace, ListSecretsOptions{Prefix, Search string; Tags []string; Limit int; AfterPath string}) (SecretPage{Secrets []Secret; Total int64; HasMore bool}, error)
    - Reveal(ctx, *authz.Principal, id string, version int) (SecretValue, error)
    - ListVersions(ctx, *authz.Principal, id string, ListVersionsOptions{Limit, BeforeVersion int}) (SecretVersionPage{Versions []SecretVersionInfo{Version int; KEKID, CreatedBy string; CreatedAt time.Time}; Total int64; HasMore bool}, error)
    - ListAccessLogs(ctx, *authz.Principal, id string, ListAccessLogsOptions{Limit int; After *AccessLogCursor{CreatedAt time.Time; ID string}}) (SecretAccessLogPage{Logs []SecretAccessLog{ID string; CreatedAt time.Time; ActorKind, ActorID, ActorName, Action, Result, IP string; Version int}; HasMore bool}, error)
    - ReadSecret(ctx, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error)
    - ResolveForIdentity(ctx, namespaceID, path string) (string, error)
    - Run(ctx) error
    - FlushAccessTimes(ctx) error
  - Types:
    - SecretValue{Path string; Version int; Value string; ExpiresAt *time.Time}
    - Secret{ID, TenantID, NamespaceID, NamespaceName, Path, Description string; Tags []string; CurrentVersion int; MaskedValue string; ExpiresAt, LastAccessedAt *time.Time; CreatedBy string; CreatedAt, UpdatedAt time.Time}
- func NewRewrapper(pool *pgxpool.Pool, c *Cipher, logger *slog.Logger) *Rewrapper
  - Methods:
    - Start(ctx) (started bool, err error): runs in the background under advisory lock jobs.LockKey("spinneret:kek:rewrap").
    - Status(ctx) (KEKStatus{CurrentKEKID string; KEKs []KEKInfo{ID string; Current, Configured bool; WrappedRecords int64}; Running bool; Done, Total int64; LastError string; LastFinishedAt *time.Time}, error)
    - Run(ctx) error: blocks until ctx is done, then calls Close.
    - Close()
    - Wait(ctx) error
  - The server must run `go rewrapper.Run(ctx)` (or call Close on shutdown) on API instances.
  - Status document: system_settings key "kek_rewrap_status", JSON {running, instance, target_kek_id, done, total, failed, last_error, started_at, finished_at, heartbeat_at}.

PACKAGE internal/api/secretapi (consumer interfaces declared here; *vault.SecretStore and *vault.Rewrapper satisfy them directly)
- type SecretManager interface:
  - Create(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in vault.CreateSecretInput) (vault.Secret, error)
  - Update(ctx context.Context, p *authz.Principal, id string, in vault.UpdateSecretInput) (vault.Secret, error)
  - Delete(ctx context.Context, p *authz.Principal, id string) error
  - Get(ctx context.Context, p *authz.Principal, id string) (vault.Secret, error)
  - List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, opts vault.ListSecretsOptions) (vault.SecretPage, error)
  - Reveal(ctx context.Context, p *authz.Principal, id string, version int) (vault.SecretValue, error)
  - ListVersions(ctx context.Context, p *authz.Principal, id string, opts vault.ListVersionsOptions) (vault.SecretVersionPage, error)
  - ListAccessLogs(ctx context.Context, p *authz.Principal, id string, opts vault.ListAccessLogsOptions) (vault.SecretAccessLogPage, error)
- type SecretReader interface:
  - ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (vault.SecretValue, error)
- type KEKManager interface:
  - Start(ctx context.Context) (bool, error)
  - Status(ctx context.Context) (vault.KEKStatus, error)
- Constructors:
  - func New(secrets SecretManager, kek KEKManager, cat catalog.Catalog, rec audit.Recorder, logger *slog.Logger) *Handler
    - Implements spinneretv1connect.SecretAdminServiceHandler.
    - secrets, kek and cat must be non-nil; rec and logger may be nil.
    - StartKEKRewrap is audited as action "kek.rewrap_start", resource kind "kek".
  - func NewNodeHandler(secrets SecretReader, cat catalog.Catalog) *NodeHandler
    - Implements spinneretv1connect.SecretServiceHandler: token principals only, purpose "api".
  - Mount both: spinneretv1connect.NewSecretAdminServiceHandler(h) and spinneretv1connect.NewSecretServiceHandler(nodeH).

CROSS-TRACK ADAPTERS
- identitysvc.SecretResolver{ResolveForIdentity(ctx, namespaceID, path string) (string, error)}: pass *vault.SecretStore directly.
- configcenter.SecretReader returns configcenter.SecretValue, which has the same fields as vault.SecretValue but is a distinct type. The server needs a small adapter: `func (a adapter) ReadSecret(ctx, p, ns, path, version, purpose) (configcenter.SecretValue, error) { v, err := a.store.ReadSecret(...); return configcenter.SecretValue{Path: v.Path, Version: v.Version, Value: v.Value, ExpiresAt: v.ExpiresAt}, err }`. configcenter passes purpose "config:<name>"; the store trims it to 64 bytes.

AUDIT (resource_kind "secret", resource_id = secret ID, resource_name = path)
- secret.create {version}
- secret.update {version, value_changed, description_changed, tags_changed, expiry_changed}
- secret.delete {version}
- secret.reveal ok/denied/error {version}
- secret.read ok/denied/error {version, purpose, error?}

No events published. No Redis keys written.

## notify

PACKAGE internal/notify (declares no consumer interfaces; depends only on concrete foundation types)
- func New(cfg notify.Config, pool *pgxpool.Pool, cipher *vault.Cipher, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, bus events.Bus, rec audit.Recorder, metrics *observability.Metrics, logger *slog.Logger) *notify.Service
  audit, metrics, bus and logger may be nil. cat is the real catalog.Store.
- type Config struct {
    Workers int                    // default 4, max 64
    QueueSize int                  // default 10000
    ReportShards int               // default 16; pass appconfig.Config.ReportShards
    Attempts int                   // default 3, max 10
    RetryDelay time.Duration       // default 2s, exponential
    HTTPTimeout time.Duration      // default 10s
    ChannelCacheTTL time.Duration  // default 10s
  }
- func (s *Service) Run(ctx context.Context) error
  Starts the delivery workers and subscribes to bus channel "*" (breaker.transition and identity.state on ns:<id>). Blocks until ctx is done and returns nil; returns notify.ErrRunning if already running. Start it on every instance that registers EvaluateJob.
- func (s *Service) EvaluateJob() jobs.Job — Name "notify_alert_evaluation", Mode jobs.Leader, Interval 30s, Timeout 25s.
- func (s *Service) Emit(ctx context.Context, a notify.Alert) error
  type Alert struct { Kind, Severity, TenantID, NamespaceID, SiteID, Title, Message string; Details map[string]any; DedupKey string; DedupTTL time.Duration }
- Methods used by the handler (all return masked config):
  - CreateChannel(ctx, p *authz.Principal, in notify.ChannelInput) (notify.Channel, error)
  - UpdateChannel(ctx, p *authz.Principal, id string, upd notify.ChannelUpdate) (notify.Channel, error)
  - DeleteChannel(ctx, p *authz.Principal, id string) error
  - GetChannel(ctx, id string) (notify.Channel, error)
  - ListChannels(ctx, q notify.ChannelQuery) (notify.ChannelPage, error)
  - TestChannel(ctx, p *authz.Principal, id string) (notify.Delivery, error)
  - ListAlertEvents(ctx, q notify.AlertQuery) (notify.AlertPage, error)
- Exported constants and helpers:
  - kinds Kind*, severities Severity*, channel kinds Channel*; validators ValidKind, ValidSeverity, ValidChannelKind, Kinds()
  - WebhookSignature(secret, ts string, body []byte) string, HeaderTimestamp, HeaderSignature
  - ReportConsumerGroup = "workers" (must equal worker.ConsumerGroup), DefaultTelegramAPIBase
  - audit actions AuditChannelCreate/Update/Delete/Test ("notify.channel.*"), AuditResourceKind "notification_channel"
- Events:
  - Publishes type "alert" on events.NamespaceChannel(ns) with data {id, kind, severity, title, message, namespace_id, site_id, created_at}. Tenant-level alerts are published on every namespace of the tenant.
  - Consumes breaker.transition (breaker.TransitionData; from/to or from_state/to_state).
  - Consumes identity.state (action.StateEventData; to=expired, subject_kind identity).
- Redis:
  - Writes SET P:alert:<tenant>:<kind>:<key> NX PX (10m default, 24h for secret_expiring).
  - Reads ZCOUNT P:{s<site>}:rdy:<eg> -inf now, and XINFO GROUPS / XLEN P:{r<shard>}:stream.
  - No dirty-set writes.
- Metrics: NotifyDeliveries{kind=channel kind, result=ok|error|dropped}.

PACKAGE internal/api/notifyapi
- func New(svc notifyapi.Service, cat catalog.Catalog, logger *slog.Logger) *notifyapi.Handler — implements spinneretv1connect.NotificationAdminServiceHandler. Register with spinneretv1connect.NewNotificationAdminServiceHandler(h, opts...).
- Consumer interface (satisfied by *notify.Service):
  type Service interface {
    CreateChannel(ctx context.Context, p *authz.Principal, in notify.ChannelInput) (notify.Channel, error)
    UpdateChannel(ctx context.Context, p *authz.Principal, id string, upd notify.ChannelUpdate) (notify.Channel, error)
    DeleteChannel(ctx context.Context, p *authz.Principal, id string) error
    GetChannel(ctx context.Context, id string) (notify.Channel, error)
    ListChannels(ctx context.Context, q notify.ChannelQuery) (notify.ChannelPage, error)
    TestChannel(ctx context.Context, p *authz.Principal, id string) (notify.Delivery, error)
    ListAlertEvents(ctx context.Context, q notify.AlertQuery) (notify.AlertPage, error)
  }
- Local types come from package notify:
  - ChannelInput{TenantID, NamespaceID, Name, Kind string; Config map[string]any; EventTypes, SiteIDs []string; MinSeverity string; Enabled bool}
  - ChannelUpdate{Name string; Config map[string]any /* nil keeps */; EventTypes, SiteIDs []string; MinSeverity string; Enabled bool}
  - ChannelQuery{TenantID string; IncludeTenant bool; NamespaceIDs []string; AfterName string; Limit int}
  - ChannelPage{Channels []Channel; More bool; Total int}
  - Channel{ID, TenantID, NamespaceID, Name, Kind string; Config map[string]any; EventTypes, SiteIDs []string; MinSeverity string; Enabled bool; LastDeliveryAt *time.Time; LastDeliveryStatus, CreatedBy string; CreatedAt, UpdatedAt time.Time}
  - Delivery{ChannelID, ChannelName string; OK bool; Error string; Attempts int; At time.Time}
  - AlertQuery{TenantID string; IncludeTenant bool; NamespaceIDs, SiteIDs []string; NamespaceID, SiteID, Kind, Severity string; From, To, AfterAt *time.Time; AfterID string; Limit int}
  - AlertPage{Events []AlertEvent; More bool}
  - AlertEvent{ID string; CreatedAt time.Time; TenantID, NamespaceID, SiteID, Kind, Severity, Title, Message string; Details map[string]any; DedupKey string; Deliveries []Delivery}
- Requirements on the server:
  - Auth interceptor puts the principal in context; users need an active tenant (X-Spinneret-Tenant).
  - The apperr→Connect converter and protovalidate run before the handler.

## analytics

Constructors:
- `analytics.New(pool *pgxpool.Pool, ch chdriver.Conn, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger, opts ...analytics.Option) *analytics.Service`
  - `ch` is the result of `store/clickhouse.Open`, or nil when `SPINNERET_CLICKHOUSE_URL` is empty.
  - `logger` may be nil.
  - `keys` must use the same prefix as every other track.
  - This is compatible with the contract signature `New(pool, ch, rdb, keys, cat, logger)`.
- Options:
  - `analytics.WithReportShards(n int)`: pass `cfg.ReportShards`. Default 16; values outside 1..4096 are ignored.
  - `analytics.WithTimeouts(query, clickhouse time.Duration)`: defaults 15s and 30s; non-positive values keep the defaults.
  - `analytics.WithClock(func() time.Time)`: tests only.
- `dashboardapi.New(svc *analytics.Service, cat catalog.Catalog) *dashboardapi.Handler`
  - Implements `spinneretv1connect.DashboardServiceHandler`.
  - Mount with `spinneretv1connect.NewDashboardServiceHandler(h, handlerOpts...)`.
  - Relies on the server's auth interceptor (principal via `authz.WithPrincipal`), the `apperr.ToConnect` error conversion and protovalidate running before the handler.
  - The handler also validates timestamps and cursors itself.

Exported `analytics.Service` methods, all read-only:
- `ClickHouseEnabled() bool`
- `Overview(ctx, Scope, window time.Duration) (Overview, error)`
- `TimeSeries(ctx, Scope, TimeSeriesQuery) (TimeSeries, error)`
- `Heatmap(ctx, Scope, HeatmapQuery) (Heatmap, error)`
- `RiskEvents(ctx, Scope, RiskEventQuery) (RiskEventPage, error)`
- `RequestEvents(ctx, Scope, RequestEventQuery) (RequestEventPage, error)`
- `NodeStats(ctx, namespaceID string, TimeRange) ([]NodeStat, error)`

Local types:
- `Scope{NamespaceID string; AllSites bool; SiteIDs []string}`
- `TimeRange{Start, End *time.Time}`
- Helpers `ParseStep`, `StepName`, `ParseWindow`, `WindowName`, and the `Metric*` constants.

Nothing else to wire:
- Consumer interfaces declared: none. The track needs no providers beyond pool, ClickHouse conn, rueidis client, keys and a `catalog.Catalog`.
- No jobs, no Run loops, no goroutines outliving a call.
- No events published, no Redis keys written (reads only), no audit entries (read-only RPCs).

Redis keys read:
- `P:T:rdy:<eg>` (ZCOUNT/ZMSCORE)
- `P:T:hs:<eg>` (HMGET packed `score|sts|samples|nfail|lastfail|cd|ru|lu`)
- `P:T:id:<i>` (st, bu, scd)
- `P:T:acc:<a>` (st, bu, cd)
- `P:T:brko` (SMEMBERS)
- `P:T:brk:<eg>` (HGET st)
- `P:{r<shard>}:stream` (XLEN, XINFO GROUPS, group 'workers')

PostgreSQL tables read:
- identities (+ accounts)
- proxies
- acquire_stats_minutely
- outcome_stats_minutely
- node_stats_minutely
- risk_events

ClickHouse: `report_events`, server-side `{pN:Type}` parameters only.

Handler permission rules:
- `dashboard:read` on every RPC.
- Namespace-wide principals get all sites. Site-restricted users are limited, site by site through `Principal.Can`, to their readable sites.
- Explicit site or endpoint-group filters require `dashboard:read` on that site.
- GetNodeStats requires namespace-wide `dashboard:read`.

