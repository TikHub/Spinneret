package spinneret

import (
	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

// Version is the SDK version sent in the User-Agent header.
const Version = "0.1.0"

// Header names of the Spinneret node protocol.
const (
	// HeaderReason carries the machine-readable reason of an error response.
	HeaderReason = "Spinneret-Reason"
	// HeaderRetryAfterMs carries the server retry hint in milliseconds.
	HeaderRetryAfterMs = "Spinneret-Retry-After-Ms"
	// HeaderNode names the calling node instance.
	HeaderNode = "X-Spinneret-Node"
)

// Environment variables read by [New] when the matching option is empty.
const (
	EnvURL   = "SPINNERET_URL"
	EnvToken = "SPINNERET_TOKEN" //nolint:gosec // environment variable name, not a credential
	EnvNode  = "SPINNERET_NODE"
)

// Limits enforced by the server.
const (
	// MaxReportsPerCall is the maximum number of reports of one Report call.
	MaxReportsPerCall = 500
	// MaxConfigItems is the maximum number of items of one BatchGetConfig or WatchConfig call.
	MaxConfigItems = 200
	// MaxNodeNameLength is the maximum length of the X-Spinneret-Node header.
	MaxNodeNameLength = 128
	// MaxReportURILength is the maximum length of Report.uri in characters.
	MaxReportURILength = 2048
	// MaxWatchTimeoutMs is the maximum long-poll wait of WatchConfig.
	MaxWatchTimeoutMs = 60_000
	// DefaultWatchTimeoutMs is the server-side wait applied when WatchConfigRequest.timeout_ms is 0.
	DefaultWatchTimeoutMs = 30_000
)

// Error reasons sent by the server in the Spinneret-Reason header, plus the
// reasons of errors synthesized by the SDK.
const (
	ReasonTokenInvalid          = "token_invalid"
	ReasonTokenExpired          = "token_expired"
	ReasonTokenRevoked          = "token_revoked"
	ReasonIPNotAllowed          = "ip_not_allowed"
	ReasonScopeMissing          = "scope_missing"
	ReasonPermissionDenied      = "permission_denied"
	ReasonSiteUnknown           = "site_unknown"
	ReasonClientUnknown         = "client_unknown"
	ReasonEndpointGroupUnknown  = "endpoint_group_unknown"
	ReasonURIInvalid            = "uri_invalid"
	ReasonInvalidArgument       = "invalid_argument"
	ReasonNoIdentityAvailable   = "no_identity_available"
	ReasonNoProxyAvailable      = "no_proxy_available"
	ReasonCircuitOpen           = "circuit_open"
	ReasonSitePaused            = "site_paused"
	ReasonRebuilding            = "rebuilding"
	ReasonLeaseUnknown          = "lease_unknown"
	ReasonLeaseReleased         = "lease_released"
	ReasonLeaseExpired          = "lease_expired"
	ReasonLeaseLifetimeExceeded = "lease_lifetime_exceeded"
	ReasonRateLimited           = "rate_limited"
	ReasonNotFound              = "not_found"
	ReasonAlreadyExists         = "already_exists"
	ReasonFailedPrecondition    = "failed_precondition"
	ReasonConflict              = "conflict"
	ReasonInternal              = "internal"

	// ReasonTransport marks errors where no usable response was received.
	ReasonTransport = "transport"
	// ReasonClientClosed marks calls made on a closed [Client].
	ReasonClientClosed = "client_closed"
	// ReasonReporterClosed marks reports submitted to a closed [Reporter].
	ReasonReporterClosed = "reporter_closed"
	// ReasonInvalidConfiguration marks invalid SDK options.
	ReasonInvalidConfiguration = "invalid_configuration"
)

// Report error kinds (Report.error_kind) returned by [ClassifyError].
const (
	ErrorKindNone        = ""
	ErrorKindTimeout     = "timeout"
	ErrorKindConnReset   = "conn_reset"
	ErrorKindConnRefused = "conn_refused"
	ErrorKindProxyAuth   = "proxy_auth"
	ErrorKindTLS         = "tls"
	ErrorKindDNS         = "dns"
	ErrorKindOther       = "other"
)

// Aliases of the generated node API messages, so that callers do not need to
// import the generated package.
type (
	AcquireRequest         = spinneretv1.AcquireRequest
	AcquireResponse        = spinneretv1.AcquireResponse
	AcquireBatchRequest    = spinneretv1.AcquireBatchRequest
	AcquireBatchResponse   = spinneretv1.AcquireBatchResponse
	LeaseInfo              = spinneretv1.Lease
	Credential             = spinneretv1.Credential
	ProxyAssignment        = spinneretv1.ProxyAssignment
	Hints                  = spinneretv1.Hints
	RenewRequest           = spinneretv1.RenewRequest
	RenewResponse          = spinneretv1.RenewResponse
	ReleaseRequest         = spinneretv1.ReleaseRequest
	ReleaseResponse        = spinneretv1.ReleaseResponse
	Report                 = spinneretv1.Report
	ReportRequest          = spinneretv1.ReportRequest
	ReportResponse         = spinneretv1.ReportResponse
	RejectedReport         = spinneretv1.RejectedReport
	ConfigItem             = spinneretv1.ConfigItem
	ConfigRef              = spinneretv1.ConfigRef
	GetConfigRequest       = spinneretv1.GetConfigRequest
	GetConfigResponse      = spinneretv1.GetConfigResponse
	BatchGetConfigRequest  = spinneretv1.BatchGetConfigRequest
	BatchGetConfigResponse = spinneretv1.BatchGetConfigResponse
	WatchItem              = spinneretv1.WatchItem
	WatchConfigRequest     = spinneretv1.WatchConfigRequest
	WatchConfigResponse    = spinneretv1.WatchConfigResponse
	GetSecretRequest       = spinneretv1.GetSecretRequest
	GetSecretResponse      = spinneretv1.GetSecretResponse
)
