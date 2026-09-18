package stats

import "time"

// Acquire results accepted by RecordAcquire (acquire_stats_minutely.result).
const (
	ResultOK          = "ok"
	ResultExhausted   = "exhausted"
	ResultCircuitOpen = "circuit_open"
	ResultSitePaused  = "site_paused"
	ResultNoProxy     = "no_proxy"
	ResultError       = "error"
)

// Lease end kinds accepted by RecordLeaseEnd.
const (
	LeaseEndReleased  = "released"
	LeaseEndExpired   = "expired"
	LeaseEndAbandoned = "abandoned"
	LeaseEndRenewed   = "renewed"
)

// LeaseEventAcquired is the ClickHouse lease_events.event value of RecordAcquire.
const LeaseEventAcquired = "acquired"

// EmptyNode is the node name stored in node_stats_minutely for requests
// without an X-Spinneret-Node header.
const EmptyNode = "_"

// AcquireRecord describes one lease acquisition attempt (one per issued lease,
// or one per failed attempt).
type AcquireRecord struct {
	// At is when the attempt happened (server time); zero means now.
	At time.Time
	// Tenant, namespace, site, endpoint group, client and identity type of the attempt.
	TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client, IdentityTypeID string
	// Subjects and correlation identifiers of the issued lease (empty on failure).
	IdentityID, ProxyID, LeaseID, Node, TokenID string
	// Result is one of ok, exhausted, circuit_open, site_paused, no_proxy, error.
	Result string
	// Duration is the server-side acquire latency.
	Duration time.Duration
	// Probe marks half-open probe leases; Sticky leases reused through a session key.
	Probe, Sticky bool
}

// ReportRecord describes one processed report after classification.
type ReportRecord struct {
	// ReceivedAt is when the report was ingested (server time), StartedAt and
	// FinishedAt the node-reported request start and end.
	ReceivedAt, StartedAt, FinishedAt time.Time
	// Tenant, namespace, site, endpoint group and client of the leased request.
	TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client string
	// Subjects and correlation identifiers.
	IdentityID, IdentityType, ProxyID, LeaseID, ReportID, Node, TokenID string
	// URI (path) and HTTP method of the request.
	URI, Method string
	// HTTPStatus is the response status (0 = no response).
	HTTPStatus int
	// BusinessCode and ErrorKind are the raw node-reported signals.
	BusinessCode, ErrorKind string
	// Markers are the node-reported content markers.
	Markers []string
	// Outcome is the classified outcome, OutcomeHint the node hint, Blame the
	// blamed subject and Rule the matching signal rule.
	Outcome, OutcomeHint, Blame, Rule string
	// LatencyMs and ResponseBytes are the node-reported latency and body size.
	LatencyMs, ResponseBytes int64
	// Suppressed reports skipped health/actions, Late reports arrived after
	// lease end, Probe reports belong to half-open probe leases.
	Suppressed, Late, Probe bool
}

// LeaseEndRecord describes the end (or renewal) of a lease.
type LeaseEndRecord struct {
	// At is when the lease ended (server time); zero means now.
	At time.Time
	// Tenant, namespace, site, endpoint group and client of the lease.
	TenantID, NamespaceID, SiteID, Site, EndpointGroupID, EndpointGroup, Client string
	// Subjects and correlation identifiers of the lease.
	IdentityID, ProxyID, LeaseID, Node, TokenID string
	// Kind is one of released, expired, abandoned, renewed.
	Kind string
	// Probe marks half-open probe leases.
	Probe bool
}

// validAcquireResult reports whether r satisfies the acquire_stats_minutely
// result check constraint.
func validAcquireResult(r string) bool {
	switch r {
	case ResultOK, ResultExhausted, ResultCircuitOpen, ResultSitePaused, ResultNoProxy, ResultError:
		return true
	default:
		return false
	}
}

// validLeaseEndKind reports whether k is a known lease end kind.
func validLeaseEndKind(k string) bool {
	switch k {
	case LeaseEndReleased, LeaseEndExpired, LeaseEndAbandoned, LeaseEndRenewed:
		return true
	default:
		return false
	}
}
