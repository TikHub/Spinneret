package scheduler

import (
	"context"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/identity"
)

// Limits of the lease API (spec §6.1, proto LeaseService).
const (
	// MaxBatch is the largest AcquireBatch count.
	MaxBatch = 50
	// MaxWait is the longest server-side wait for an identity.
	MaxWait = 5 * time.Second
	// MaxRenewExtension is the largest Renew extension.
	MaxRenewExtension = 30 * time.Minute
	// DefaultReportShards is used when Config.ReportShards is not positive.
	DefaultReportShards = 16
	// DefaultLateReportWindow is used when Config.LateReportWindow is not positive.
	DefaultLateReportWindow = 10 * time.Minute
	// maxReportShards is the largest shard count a two-hex-digit lease shard can encode.
	maxReportShards = 256
)

// Config configures the scheduler.
type Config struct {
	// ReportShards is SPINNERET_REPORT_SHARDS (1..256); lease IDs embed
	// identityHkey % ReportShards.
	ReportShards int
	// LateReportWindow is SPINNERET_LATE_REPORT_WINDOW: how long an ended
	// lease hash is kept for late reports.
	LateReportWindow time.Duration

	// OnBreakerHalfOpen, when set, is called after an acquire lazily moved
	// the breaker of an endpoint group from open to half_open (spec §6.1
	// step 1), so the breaker service can record the transition. It runs on
	// the request path and must not block.
	OnBreakerHalfOpen func(siteKey, groupKey int64)
	// OnProxyBound, when set, is called after an acquire bound an identity
	// to a proxy (bind_identity mode), either for the first time or as a
	// rebind, so the binding can be persisted. It must not block.
	OnProxyBound func(ProxyBinding)
}

// ProxyBinding describes a binding created by acquire in bind_identity mode.
type ProxyBinding struct {
	At          time.Time
	NamespaceID string
	SiteID      string
	SiteKey     int64
	IdentityID  string
	IdentityKey int64
	ProxyID     string
	ProxyKey    int64
	// Rebound is true when the identity was previously bound to another proxy.
	Rebound bool
	// RebindDay (YYYYMMDD, UTC) and RebindsToday mirror the identity hot state.
	RebindDay    string
	RebindsToday int
}

// CredentialSource renders the credential of an identity payload version
// (provided by identitysvc.PayloadCache).
type CredentialSource interface {
	Credential(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error)
}

// ProxyAssignment is the proxy handed to a node. It mirrors proxy.Assignment
// (the server adapts the proxy package to ProxyResolver).
type ProxyAssignment struct {
	ID     string
	URL    string
	Kind   string
	Region string
}

// ProxyResolver resolves the connection URL of a proxy for a lease (provided
// by proxy.Resolver through a server-side adapter).
type ProxyResolver interface {
	Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*ProxyAssignment, error)
}

// Acquire results recorded in statistics (acquire_stats_minutely.result).
const (
	ResultOK          = "ok"
	ResultExhausted   = "exhausted"
	ResultCircuitOpen = "circuit_open"
	ResultSitePaused  = "site_paused"
	ResultNoProxy     = "no_proxy"
	ResultError       = "error"
)

// Lease end kinds recorded in statistics.
const (
	EndReleased  = "released"
	EndExpired   = "expired"
	EndAbandoned = "abandoned"
	EndRenewed   = "renewed"
)

// AcquireRecord mirrors stats.AcquireRecord (the server adapts it).
type AcquireRecord struct {
	At              time.Time
	TenantID        string
	NamespaceID     string
	SiteID          string
	Site            string
	EndpointGroupID string
	EndpointGroup   string
	Client          string
	IdentityTypeID  string
	IdentityID      string
	ProxyID         string
	LeaseID         string
	Node            string
	TokenID         string
	Result          string
	Duration        time.Duration
	Probe           bool
	Sticky          bool
}

// LeaseEndRecord mirrors stats.LeaseEndRecord (the server adapts it).
type LeaseEndRecord struct {
	At              time.Time
	TenantID        string
	NamespaceID     string
	SiteID          string
	Site            string
	EndpointGroupID string
	EndpointGroup   string
	Client          string
	IdentityID      string
	ProxyID         string
	LeaseID         string
	Node            string
	TokenID         string
	Kind            string // released | expired | abandoned | renewed
	Probe           bool
}

// StatsRecorder receives acquire and lease-end statistics. Implementations
// must not block.
type StatsRecorder interface {
	RecordAcquire(AcquireRecord)
	RecordLeaseEnd(LeaseEndRecord)
}

// AcquireRequest selects the endpoint group to lease identities for.
type AcquireRequest struct {
	// Site is the site name inside the principal's namespace.
	Site string
	// Client is the client type of the site.
	Client string
	// URI (path or absolute URL) is matched against the client's URI rules
	// when EndpointGroup is empty; both empty selects "_default".
	URI string
	// EndpointGroup explicitly names the endpoint group.
	EndpointGroup string
	// SessionKey is the optional sticky-session key.
	SessionKey string
	// Wait is how long to wait for an identity (0..MaxWait).
	Wait time.Duration
	// Count is the number of distinct identities for AcquireBatch (1..MaxBatch);
	// Acquire ignores it.
	Count int
}

// Lease describes an issued lease.
type Lease struct {
	ID            string
	IdentityID    string
	IdentityType  string
	EndpointGroup string
	ExpiresAt     time.Time
	// Sticky is true when the identity was reused through the session key.
	Sticky bool
	// Probe is true for half-open breaker probes and for leases of pending
	// identities, whose reports decide state transitions.
	Probe bool
}

// Grant is an issued lease with its rendered credential and proxy.
type Grant struct {
	Lease      Lease
	Credential *identity.Credential
	// Proxy is nil when the rotation policy assigns no proxy.
	Proxy *ProxyAssignment
	// RenewBefore is the advisory renew margin (a quarter of the lease TTL).
	RenewBefore time.Duration

	// breakerProbe is true for half-open breaker probes only (lease hash
	// pr=1); statistics record this flag rather than Lease.Probe.
	breakerProbe bool
}
