# Spinneret v0.1 — Foundation Package Contracts

These exported Go signatures are **frozen**: later packages are written against them in parallel. Implementations may
add exported helpers, but must not rename, remove or change the signatures below. Read together with
`1_implementation_spec.md`.

Import aliases used below: `connect "connectrpc.com/connect"`, `rueidis "github.com/redis/rueidis"`,
`pgxpool "github.com/jackc/pgx/v5/pgxpool"`, `chdriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"`.

---

## internal/pkg/idgen
```go
const (
    Tenant = "ten"; Namespace = "ns"; Site = "sit"; EndpointGroup = "eg"; URIRule = "uri"; IdentityType = "ity"
    Identity = "idt"; Account = "acc"; Proxy = "pxy"; Policy = "pol"; PolicyBinding = "pbd"; ConfigItem = "cfg"
    Secret = "sec"; Token = "tok"; User = "usr"; RoleBinding = "rb"; StateEvent = "evt"; Audit = "aud"
    BreakerEvent = "brk"; Channel = "nch"; Alert = "alt"; RiskEvent = "rsk"; Lease = "lse"
)
func New(prefix string) string                 // "<prefix>_<32 hex uuidv7>"
func Valid(id, prefix string) bool
func Time(id string) (time.Time, bool)          // timestamp embedded in the UUIDv7
type LeaseRef struct { SiteKey int64; Shard int }
func LeasePrefix(siteKey int64) string          // "lse_<32hex>_<siteKey base36>_" (fresh uuid each call)
func ParseLeaseID(id string) (LeaseRef, error)
func FormatShard(shard int) string              // "%02x"
func ValidReportID(id string) bool              // 1..64 of [A-Za-z0-9_.:-]
```

## internal/pkg/durationx
```go
type Duration time.Duration                     // Permanent == -1
const Permanent Duration = -1
func Parse(s string) (Duration, error)          // "", "0" → 0; "500ms" "30s" "10m" "24h" "7d" "1h30m" "permanent"
func MustParse(s string) Duration
func (d Duration) String() string               // canonical, e.g. "30s", "10m", "7d", "1h30m", "permanent", "0s"
func (d Duration) Std() time.Duration
func (d Duration) Milliseconds() int64
func (d Duration) IsPermanent() bool
func (d Duration) IsZero() bool
// JSON: marshals as canonical string; unmarshals from string or integer milliseconds.
// YAML (go.yaml.in/yaml/v3): MarshalYAML/UnmarshalYAML with the same rules.
```

## internal/pkg/glob
```go
func Match(pattern, s string) bool              // '*' any sequence (including '/'), '?' one rune; no other metacharacters
```

## internal/pkg/netx
```go
func ParsePrefixes(values []string) ([]netip.Prefix, error)   // accepts CIDRs and bare IPs
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr // X-Forwarded-For / X-Real-IP honored only from trusted peers
func AllowedIP(ip netip.Addr, allow []netip.Prefix) bool        // empty allowlist → true
```

## internal/apperr
```go
type Reason string   // every reason listed in spec §10 as a constant: ReasonTokenInvalid = "token_invalid", ...
const HeaderReason = "Spinneret-Reason"; const HeaderRetryAfter = "Spinneret-Retry-After-Ms"
type Error struct { Code connect.Code; Reason Reason; Message string; RetryAfterMs int64; Err error }
func (e *Error) Error() string
func (e *Error) Unwrap() error
func (e *Error) WithRetryAfter(ms int64) *Error
func (e *Error) WithCause(err error) *Error
func New(code connect.Code, reason Reason, format string, args ...any) *Error
func InvalidArgument(reason Reason, format string, args ...any) *Error
func NotFound(format string, args ...any) *Error                   // reason not_found
func AlreadyExists(format string, args ...any) *Error              // reason already_exists
func Conflict(format string, args ...any) *Error                   // code aborted, reason conflict
func FailedPrecondition(reason Reason, format string, args ...any) *Error
func PermissionDenied(reason Reason, format string, args ...any) *Error
func Unauthenticated(reason Reason, format string, args ...any) *Error
func ResourceExhausted(reason Reason, retryAfterMs int64, format string, args ...any) *Error
func Unavailable(reason Reason, retryAfterMs int64, format string, args ...any) *Error
func Internal(err error) *Error                                     // message "internal error", cause kept for logs
func As(err error) (*Error, bool)
func ReasonOf(err error) Reason                                     // "" when not an *Error
func IsNotFound(err error) bool
func ToConnect(err error) *connect.Error  // *Error → code/message + metadata headers; context.Canceled → canceled;
                                          // DeadlineExceeded → deadline_exceeded; *connect.Error passthrough; others → internal (generic message)
```

## internal/appconfig
```go
type Role string
const (RoleAll Role = "all"; RoleAPI Role = "api"; RoleWorker Role = "worker")
func (r Role) ServesAPI() bool
func (r Role) RunsWorkers() bool
type Retention struct { RiskEvents, MinuteStats, HourStats, StateEvents, Audit time.Duration }
type Config struct {
    HTTPAddr string; Role Role; InstanceID string
    DatabaseURL string; DatabaseMaxConns int32
    RedisURL string; RedisAddrs []string; RedisPrefix string
    ClickHouseURL string; ClickHouseTTLDays int
    KEKFile, KEKs, KEKCurrent string
    ReportShards int; ReportDedupTTL, LateReportWindow time.Duration; StreamMaxLen int64
    PayloadCache bool; PayloadCacheSize int
    DEKCacheSize int; DEKCacheTTL time.Duration
    TokenCacheTTL, SessionTTL time.Duration; CookieSecure string // auto|true|false
    TLSCertFile, TLSKeyFile string; TrustedProxies []string; MetricsAddr string
    LogLevel, LogFormat string
    ProxyCheckURL string; ProxyCheckInterval, ProxyCheckTimeout time.Duration; ProxyExitIPURL, GeoIPDB string
    Retention Retention; RecordCooldownEvents bool; MaxWatchers int; UIEnabled bool; AllowedOrigins []string
    ShutdownTimeout time.Duration; AdminMaxRequestBytes int64; OTLPEndpoint string
}
func Load() (Config, error)                                        // os.LookupEnv
func LoadFrom(lookup func(string) (string, bool)) (Config, error)  // applies defaults of spec §12, then Validate
func (c Config) Validate() error
func (c Config) Redacted() map[string]string                       // for startup logging, URLs with passwords masked
```

## internal/observability
```go
func NewLogger(level, format string, w io.Writer) (*slog.Logger, error) // format json|text
type Metrics struct {
    Registry *prometheus.Registry
    AcquireTotal *prometheus.CounterVec          // site, group, result
    AcquireDuration *prometheus.HistogramVec     // site
    ReportIngestTotal *prometheus.CounterVec     // result
    ReportTotal *prometheus.CounterVec           // site, group, outcome
    ReportLag prometheus.Histogram
    ReportProcessDuration prometheus.Histogram
    Identities *prometheus.GaugeVec              // site, type, state
    IdentitiesAvailable *prometheus.GaugeVec     // site, group
    ActionsTotal *prometheus.CounterVec          // site, action, scope, mode
    BreakerState *prometheus.GaugeVec            // site, group
    BreakerTransitions *prometheus.CounterVec    // site, group, to
    Proxies *prometheus.GaugeVec                 // site, state
    StreamPending *prometheus.GaugeVec           // shard
    StreamOwnedShards prometheus.Gauge
    ConfigWatchers prometheus.Gauge
    LeaseReaped *prometheus.CounterVec           // site, kind
    HTTPRequests *prometheus.CounterVec          // procedure, code
    HTTPRequestDuration *prometheus.HistogramVec // procedure
    NotifyDeliveries *prometheus.CounterVec      // kind, result
    DBWriteBatches *prometheus.CounterVec        // writer, result
}
func NewMetrics() *Metrics                       // new registry incl. Go and process collectors
func (m *Metrics) Handler() http.Handler
func Label(v string) string                      // "" → "_"
func SetupTracing(ctx context.Context, endpoint, serviceName, version string) (func(context.Context) error, error) // endpoint "" → no-op
```

## internal/authz
```go
type Permission string   // constants for every permission in spec §3.2, e.g. PermLeaseAcquire = "lease:acquire"
type Role string
const (RoleOwner Role = "owner"; RoleAdmin Role = "admin"; RoleOperator Role = "operator"; RoleViewer Role = "viewer")
func ValidRole(r Role) bool
func RolePermissions(r Role) []Permission
func ValidExtraPermission(p Permission) bool
type Binding struct { ID, TenantID string; Role Role; NamespaceID string /* "" = all */; SiteIDs []string; Extra []Permission }
type Scope struct { Raw, Name, Arg string }          // "lease:acquire:shop" → Name "lease:acquire", Arg "shop"
func ParseScope(s string) (Scope, error)
func ParseScopes(ss []string) ([]Scope, error)
type PrincipalKind string
const (KindUser PrincipalKind = "user"; KindToken PrincipalKind = "token"; KindSystem PrincipalKind = "system")
type Principal struct {
    Kind PrincipalKind; ID, Name string
    TenantID string                  // user: active tenant (may be "" for platform admins outside a tenant); token: fixed
    NamespaceID, NamespaceName string // token only
    IsPlatformAdmin bool
    Bindings []Binding               // user only (bindings of all tenants)
    Scopes []Scope                   // token only
    ClientIP, UserAgent, Node string
}
type Resource struct { TenantID, NamespaceID, NamespaceName, SiteID, SiteName, ConfigGroup, SecretPath string }
func (p *Principal) Can(perm Permission, r Resource) bool
func (p *Principal) Require(perm Permission, r Resource) error      // apperr PermissionDenied (reason scope_missing for tokens, permission_denied for users)
func (p *Principal) SiteFilter(tenantID, namespaceID string, perm Permission) (all bool, siteIDs []string)
func (p *Principal) TenantIDs() []string                            // tenants with any binding (platform admin: nil = all)
func (p *Principal) Actor() string                                  // "user:<id>" | "token:<id>" | "system"
func System(name string) *Principal                                 // platform-admin-equivalent internal principal
func WithPrincipal(ctx context.Context, p *Principal) context.Context
func FromContext(ctx context.Context) (*Principal, bool)
func MustPrincipal(ctx context.Context) (*Principal, error)          // apperr Unauthenticated(session_invalid) when absent
```

## internal/store/postgres
```go
//go:embed migrations/*.sql
var Migrations embed.FS
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) // pings, sets application_name=spinneret
func Migrate(ctx context.Context, pool *pgxpool.Pool) error                        // goose up, serialized by advisory lock
func MigrationVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error)
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx, q *db.Queries) error) error
func MapError(err error, what string) error      // ErrNoRows→apperr.NotFound, 23505→AlreadyExists, 23503/23514→FailedPrecondition, else wrapped
func EnsurePartitions(ctx context.Context, pool *pgxpool.Pool, now time.Time) error
func DropExpiredPartitions(ctx context.Context, pool *pgxpool.Pool, r appconfig.Retention, now time.Time) error
func Listen?  (not provided)
// package db: sqlc output, `db.New(pgx DBTX) *Queries`, `db.Querier` interface
```

## internal/store/redis
```go
func Open(ctx context.Context, url string, clusterAddrs []string) (rueidis.Client, error)
type Keys struct { Prefix string }
func NewKeys(prefix string) Keys
func (k Keys) SiteTag(siteKey int64) string                       // "{s12}"
func (k Keys) SiteBase(siteKey int64) string                      // "sp:{s12}:"
func (k Keys) SiteMeta(siteKey int64) string
func (k Keys) Ready(siteKey, egKey int64) string
func (k Keys) Health(siteKey, egKey int64) string
func (k Keys) Identity(siteKey, identityKey int64) string
func (k Keys) Account(siteKey, accountKey int64) string
func (k Keys) AccountMembers(siteKey, accountKey int64) string
func (k Keys) Lease(siteKey int64, leaseID string) string
func (k Keys) LeaseExpiry(siteKey int64) string
func (k Keys) Sticky(siteKey, egKey int64, normalizedSession string) string
func (k Keys) Quota(siteKey, egKey, identityKey int64) string
func (k Keys) ProxySite(siteKey, proxyKey int64) string
func (k Keys) ProxyReady(siteKey int64) string
func (k Keys) Window(siteKey, egKey, bucket int64) string
func (k Keys) WindowHLL(siteKey, egKey, bucket int64) string
func (k Keys) Breaker(siteKey, egKey int64) string
func (k Keys) Counter(siteKey int64, subject, outcome string, windowMs int64) string // subject "i12" | "a3" | "p9"
func (k Keys) CrossProxy(siteKey, proxyKey int64) string
func (k Keys) CrossIdentity(siteKey, identityKey int64) string
func (k Keys) Bans(siteKey, identityKey int64) string
func (k Keys) RecentCooldowns(siteKey, egKey int64) string
func (k Keys) Checkpoint(siteKey int64) string
func (k Keys) Dirty(siteKey int64) string
func (k Keys) RoundRobin(siteKey, egKey int64) string
func (k Keys) ActiveGroups(siteKey int64) string
func (k Keys) SiteLock(siteKey int64, name string) string
func (k Keys) ShardTag(shard int) string                           // "{r3}"
func (k Keys) Stream(shard int) string
func (k Keys) Dedup(shard int, reportID string) string
func (k Keys) ShardOwner(shard int) string
func (k Keys) Epoch() string
func (k Keys) Workers() string
func (k Keys) RuntimeVersions(namespaceID string) string
func (k Keys) Session(hash string) string
func (k Keys) RateLimit(kind, key string) string
func (k Keys) AlertDedup(key string) string
func (k Keys) Lock(name string) string
func (k Keys) Channel(name string) string                          // "sp:ch:<name>"
func (k Keys) ChannelPattern() string                              // "sp:ch:*"
func NormalizeSessionKey(s string) string                          // spec §5 rule
//go:embed lua/common.lua
var CommonLua string
type Script struct { Name string /* unexported fields */ }
func NewScript(name, body string) *Script                          // body is prefixed with CommonLua
func (s *Script) Exec(ctx context.Context, c rueidis.Client, keys, args []string) rueidis.RedisResult
func (s *Script) ExecMulti(ctx context.Context, c rueidis.Client, calls ...rueidis.LuaExec) []rueidis.RedisResult
func (s *Script) Source() string
// Lock helpers (SET NX PX + compare-and-delete / compare-and-pexpire via Lua)
func TryLock(ctx context.Context, c rueidis.Client, key, owner string, ttl time.Duration) (bool, error)
func RefreshLock(ctx context.Context, c rueidis.Client, key, owner string, ttl time.Duration) (bool, error)
func Unlock(ctx context.Context, c rueidis.Client, key, owner string) error
```
`lua/common.lua` defines these global functions (all scripts may call them):
```
sp_base()                                   -- "P:T:" derived from KEYS[1] (strip trailing "meta")
sp_num(v, default)                          -- tonumber with default (handles false/nil/"")
sp_split(s, sep)                            -- array of strings
sp_hs_unpack(s, baseline, now)              -- {score,sts,samples,nfail,lastfail,cd,ru,lu}
sp_hs_pack(t)                               -- "score|sts|samples|nfail|lastfail|cd|ru|lu" (score "%.2f", others integers)
sp_hs_get(base, eg, i, baseline, now)       -- reads HGET base.."hs:"..eg i
sp_hs_set(base, eg, i, t)                   -- HSET
sp_decay(score, sts, now, baseline, tau_ms) -- exponential regression to baseline
sp_ewma(score, v, alpha)
sp_avail(base, eg, i)                       -- spec §5.6 (reads hs, id scd/sru/al/xl/acc, acc cd)
sp_push_all(base, egs, i, score)            -- ZADD XX GT on every rdy:<eg> of egs (array of eg keys)
sp_rescore(base, eg, i)                     -- ZADD XX rdy:<eg> sp_avail(...)
sp_quota_est(p, n, window_idx_start_ms, w, now) -- sliding-window estimate
sp_hmget_map(key, fields)                   -- returns table field -> value (nil when missing)
```

## internal/store/clickhouse
```go
func Open(ctx context.Context, url string) (chdriver.Conn, error)  // error when url == ""
func Migrate(ctx context.Context, conn chdriver.Conn, ttlDays int) error
type ReportEvent struct {
    EventTime, ReceivedAt, StartedAt time.Time
    TenantID, NamespaceID, SiteID, Site, EndpointGroup, Client string
    IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID string
    URI, Method string; HTTPStatus uint16; BusinessCode, ErrorKind string; Markers []string
    Outcome, OutcomeHint, Blame, Rule string; LatencyMs uint32; ResponseBytes uint64
    Suppressed, Late, Probe bool
}
type LeaseEvent struct {
    EventTime time.Time; TenantID, NamespaceID, SiteID, Site, EndpointGroup, Client string
    IdentityID, ProxyID, LeaseID, Node, TokenID string
    Event string /* acquired|renewed|released|expired|abandoned|rejected */; Result string; DurationUs uint32; Probe, Sticky bool
}
type Writer struct { /* unexported */ }
func NewWriter(conn chdriver.Conn, logger *slog.Logger, flushEvery time.Duration, maxBatch int) *Writer // conn may be nil → no-op writer
func (w *Writer) AddReport(ev ReportEvent)       // non-blocking; drops (and counts) when the buffer is full
func (w *Writer) AddLease(ev LeaseEvent)
func (w *Writer) Run(ctx context.Context) error  // flush loop until ctx done, final flush
func (w *Writer) Dropped() int64
```
Tables `report_events` and `lease_events` (MergeTree, partition by day, TTL `ttlDays`).

## internal/events
```go
const (ChannelCatalog = "catalog"; ChannelTokens = "tokens"; ChannelConfig = "config"; ChannelRuntime = "runtime")
func NamespaceChannel(namespaceID string) string               // "ns:<id>"
type Event struct {
    Type string `json:"type"`; TenantID string `json:"tenant_id,omitempty"`; NamespaceID string `json:"namespace_id,omitempty"`
    SiteID string `json:"site_id,omitempty"`; At time.Time `json:"at"`; Data json.RawMessage `json:"data,omitempty"`
}
type Handler func(ctx context.Context, channel string, ev Event)
type Bus interface {
    Publish(ctx context.Context, channel string, ev Event) error // delivers locally at once + Redis Pub/Sub for peers
    Subscribe(channel string, h Handler) (unsubscribe func())    // channel "*" receives everything
    Run(ctx context.Context) error                               // Redis PSUBSCRIBE loop with reconnect; returns on ctx done
}
func NewRedisBus(client rueidis.Client, keys redis.Keys, instanceID string, logger *slog.Logger) Bus // ignores own echoes
func NewMemoryBus() Bus
```

## internal/vault (crypto core)
```go
type KEKProvider interface {
    CurrentID() string
    IDs() []string
    Wrap(dek []byte) (wrapped []byte, kekID string, err error)
    Unwrap(kekID string, wrapped []byte) ([]byte, error)
}
func NewLocalKEKProvider(keys map[string][]byte, current string) (*LocalKEKProvider, error)
func LoadLocalKEKProvider(file, keys, current string) (*LocalKEKProvider, error) // spec §9 formats
func GenerateKEK() (string, error)                                             // base64 of 32 random bytes
type Sealed struct { Ciphertext, WrappedDEK []byte; KEKID string }
type Cipher struct { /* unexported */ }
func NewCipher(p KEKProvider, cacheSize int, cacheTTL time.Duration) *Cipher
func (c *Cipher) Seal(plaintext, aad []byte) (Sealed, error)
func (c *Cipher) Open(s Sealed, aad []byte) ([]byte, error)
func (c *Cipher) Rewrap(s Sealed) (Sealed, bool, error) // bool=false when already on current KEK
func (c *Cipher) Provider() KEKProvider
func AAD(recordID, field string) []byte
var ErrDecrypt = errors.New("vault: decryption failed")
// package vault/vaulttest: func NewCipher(t testing.TB) *vault.Cipher
```

## internal/site (matcher)
```go
type RuleKind string
const (RuleExact RuleKind = "exact"; RuleTemplate RuleKind = "template"; RulePrefix RuleKind = "prefix"; RuleRegex RuleKind = "regex")
const DefaultGroup = "_default"
type Rule struct { ID, GroupID, GroupName string; Kind RuleKind; Pattern string; Position int }
func ValidatePattern(kind RuleKind, pattern string) error
func NormalizePath(uri string) (string, error)      // accepts "/p?q" or absolute URL; returns path; apperr uri_invalid
type MatchResult struct { GroupID, GroupName, RuleID string; Kind RuleKind; Default bool }
type Matcher struct { /* immutable */ }
func NewMatcher(rules []Rule, defaultGroupID string) (*Matcher, error)
func (m *Matcher) Match(path string) MatchResult    // exact > template (more literal segments, then fewer params, then position) > prefix (longest) > regex (position) > default
```

## internal/policy (pure)
```go
type Kind string
const (KindRotation Kind = "rotation"; KindSignal Kind = "signal"; KindAction Kind = "action"; KindBreaker Kind = "breaker")
type Binding struct { Site, Client, EndpointGroup string }  // yaml:"site" "client" "endpoint_group"
type Spec interface { Kind() Kind; PolicyName() string; PolicyBinding() *Binding; Validate() error; ApplyDefaults() }
type RotationSpec struct {...}  // spec §7, yaml/json snake_case tags, durationx.Duration for durations
type SignalSpec struct {...}
type ActionSpec struct {...}
type BreakerSpec struct {...}
func ParseYAML(kind Kind, data []byte) (Spec, error)       // strict (unknown fields rejected), ApplyDefaults, Validate
func ParseJSON(kind Kind, data []byte) (Spec, error)
func MarshalYAML(spec Spec) ([]byte, error)
func MarshalJSON(spec Spec) ([]byte, error)                 // canonical JSON stored in policy_versions.spec
func Default(kind Kind) Spec
func DefaultYAML(kind Kind) string
func DefaultPolicyName(kind Kind) string                    // "default-rotation" ...

const (OutcomeSuccess = "success"; OutcomeEmpty = "empty"; OutcomeRateLimited = "rate_limited"; OutcomeCaptcha = "captcha"
    OutcomeAuthInvalid = "auth_invalid"; OutcomeForbidden = "forbidden"; OutcomeBanned = "banned"; OutcomeProxyError = "proxy_error"
    OutcomeNetworkError = "network_error"; OutcomeTargetError = "target_error"; OutcomeClientError = "client_error"; OutcomeUnknown = "unknown")
func ValidOutcome(s string) bool
func IsRiskOutcome(s string) bool                           // rate_limited captcha forbidden banned
func IsFailureOutcome(s string) bool                        // streak-affecting (spec §6.4, without the network_error blame condition)
type Blame string
const (BlameNone Blame = "none"; BlameIdentity Blame = "identity"; BlameProxy Blame = "proxy"; BlameBoth Blame = "both")
func DefaultBlame(outcome string) Blame
func (b Blame) Identity() bool
func (b Blame) Proxy() bool

type ReportFacts struct { URI, Method string; HTTPStatus int; BusinessCode, ErrorKind string; Markers []string; OutcomeHint string; LatencyMs, ResponseBytes int64 }
type Classification struct { Outcome string; Blame Blame; RuleIndex int; RuleName string } // RuleIndex -1 when unmatched
type CompiledSignal struct { /* unexported */ }
func CompileSignal(chain []*SignalSpec) (*CompiledSignal, error)   // chain ordered root → leaf (extends)
func (c *CompiledSignal) Classify(f ReportFacts) Classification

type SubjectKind string
const (SubjectIdentity SubjectKind = "identity"; SubjectAccount SubjectKind = "account"; SubjectProxy SubjectKind = "proxy")
type ActionKind string
const (ActionCooldown ActionKind = "cooldown"; ActionExpire ActionKind = "expire"; ActionQuarantine ActionKind = "quarantine"
    ActionBan ActionKind = "ban"; ActionActivate ActionKind = "activate")
type ActionScope string
const (ScopeIdentityEndpoint ActionScope = "identity_endpoint"; ScopeIdentitySite ActionScope = "identity_site"; ScopeIdentity ActionScope = "identity"
    ScopeAccount ActionScope = "account"; ScopeProxySite ActionScope = "proxy_site"; ScopeProxy ActionScope = "proxy")
func (s ActionScope) Subject() SubjectKind
type CounterRequest struct { Subject SubjectKind; Outcome string; Window time.Duration }
type EvalInput struct {
    Outcome string; Blame Blame; IdentityState string; HasAccount, HasProxy bool
    Counts map[CounterRequest]int64          // values include the current report
    BanCounts map[time.Duration]int64        // bans within window, excluding the ban being evaluated
    EndpointStreak, SiteStreak, ProxyStreak int
    EndpointScore float64; EndpointSamples int; GlobalScore float64; GlobalSamples int
    EndpointCooldownRemaining time.Duration  // avoid re-applying the low-score cooldown
    Jitter func() float64                    // returns [0,1); nil → no jitter (factor 1)
}
type PlannedAction struct {
    Action ActionKind; Scope ActionScope; Duration time.Duration; Permanent bool
    Severity int; RuleIndex int; RuleName string; Source string // rule|health|lifecycle|escalation
}
type CompiledAction struct { Mode string; Health HealthSpec; CrossAttribution CrossAttributionSpec; BanExpiryState string /* unexported rest */ }
func CompileAction(chain []*ActionSpec) (*CompiledAction, error)
func (c *CompiledAction) CounterRequests(outcome string) []CounterRequest
func (c *CompiledAction) EscalationWindows() []time.Duration
func (c *CompiledAction) Observation(outcome string, blame Blame) (value float64, affectsIdentity bool)
func (c *CompiledAction) ProxyObservation(outcome string, blame Blame) (value float64, affectsProxy bool)
func (c *CompiledAction) Evaluate(in EvalInput) []PlannedAction         // already reduced to the most severe per subject
func Severity(a PlannedAction) int
func MostSevere(actions []PlannedAction) []PlannedAction
func CooldownDuration(base, max time.Duration, multiplier float64, maxExponent, streak int, jitter float64) time.Duration // jitter factor in [0.8,1.2]

type BindingRow struct { PolicyID string; Kind Kind; SiteID, Client, EndpointGroupID string }
type Level string
const (LevelEndpointGroup Level = "endpoint_group"; LevelClient Level = "client"; LevelSite Level = "site"; LevelNamespace Level = "namespace"; LevelBuiltin Level = "builtin")
func Resolve(kind Kind, bindings []BindingRow, siteID, client, endpointGroupID string) (policyID string, level Level)
func ResolveExtends(kind Kind, leaf Spec, lookup func(name string) (Spec, bool)) ([]Spec, error) // root → leaf
```

## internal/identity (pure type logic)
```go
type FieldType string
const (FieldString FieldType = "string"; FieldNumber FieldType = "number"; FieldBool FieldType = "bool"
    FieldCookieMap FieldType = "cookie_map"; FieldJSON FieldType = "json"; FieldSecretRef FieldType = "secret_ref")
type FieldSpec struct { Type FieldType; Required, Sensitive bool; Description string }
type TypeSpec struct {
    Name, Site, Client, Description string
    Fields map[string]FieldSpec; UniqueBy []string; Activation string // probe|immediate
    Deliver map[string]any  // segments: cookies cookie_header headers query json values
}
func ParseTypeYAML(data []byte) (*TypeSpec, error)
func ParseTypeJSON(data []byte) (*TypeSpec, error)
func (s *TypeSpec) Validate() error
func (s *TypeSpec) JSONSchema() ([]byte, error)
func MarshalTypeYAML(s *TypeSpec) ([]byte, error)
type Credential struct {
    Cookies map[string]string; CookieHeader string; Headers map[string]string; Query map[string]string
    JSON any; Values map[string]any
}
type SecretResolver func(ctx context.Context, path string) (string, error)
type CompiledType struct { ID, Name, SiteID, Client string; Version int; Spec *TypeSpec /* unexported rest */ }
func Compile(id, siteID string, version int, spec *TypeSpec) (*CompiledType, error)
func (c *CompiledType) Normalize(payload map[string]any) (map[string]any, error)   // coerce cookie_map/number/bool, validate against schema
func (c *CompiledType) UniqueKey(payload map[string]any) ([]byte, error)          // canonical bytes of unique_by values
func (c *CompiledType) Render(ctx context.Context, payload map[string]any, resolve SecretResolver) (*Credential, bool, error) // bool: used secrets
func (c *CompiledType) Mask(payload map[string]any) map[string]any
func (c *CompiledType) HasSensitive() bool
func CanonicalJSON(v any) ([]byte, error)                                          // sorted keys
func ParseCookieMap(v any) (map[string]string, error)
type ImportRow struct { Line int; Payload map[string]any; Account, Region string; Tags []string; Labels map[string]string }
type RowError struct { Line int; Message string }
type ImportLimits struct { MaxRows int; MaxBytes int64 }
func ParseImport(format string, r io.Reader, limits ImportLimits) ([]ImportRow, []RowError, error) // jsonl|csv
```
