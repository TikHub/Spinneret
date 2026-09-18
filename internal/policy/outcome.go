package policy

// Report outcomes (spec §7).
const (
	OutcomeSuccess      = "success"
	OutcomeEmpty        = "empty"
	OutcomeRateLimited  = "rate_limited"
	OutcomeCaptcha      = "captcha"
	OutcomeAuthInvalid  = "auth_invalid"
	OutcomeForbidden    = "forbidden"
	OutcomeBanned       = "banned"
	OutcomeProxyError   = "proxy_error"
	OutcomeNetworkError = "network_error"
	OutcomeTargetError  = "target_error"
	OutcomeClientError  = "client_error"
	OutcomeUnknown      = "unknown"
)

// outcomeCount is the number of known outcomes; outcomeIndex maps each one to
// [0, outcomeCount).
const outcomeCount = 12

// outcomeIndex returns the ordinal of a known outcome or -1.
func outcomeIndex(s string) int {
	switch s {
	case OutcomeSuccess:
		return 0
	case OutcomeEmpty:
		return 1
	case OutcomeRateLimited:
		return 2
	case OutcomeCaptcha:
		return 3
	case OutcomeAuthInvalid:
		return 4
	case OutcomeForbidden:
		return 5
	case OutcomeBanned:
		return 6
	case OutcomeProxyError:
		return 7
	case OutcomeNetworkError:
		return 8
	case OutcomeTargetError:
		return 9
	case OutcomeClientError:
		return 10
	case OutcomeUnknown:
		return 11
	}
	return -1
}

// Outcomes returns every known outcome in a stable order.
func Outcomes() []string {
	return []string{
		OutcomeSuccess, OutcomeEmpty, OutcomeRateLimited, OutcomeCaptcha, OutcomeAuthInvalid, OutcomeForbidden,
		OutcomeBanned, OutcomeProxyError, OutcomeNetworkError, OutcomeTargetError, OutcomeClientError, OutcomeUnknown,
	}
}

// ValidOutcome reports whether s is a known outcome.
func ValidOutcome(s string) bool { return outcomeIndex(s) >= 0 }

// IsRiskOutcome reports whether s is a risk outcome
// (rate_limited, captcha, forbidden, banned).
func IsRiskOutcome(s string) bool {
	switch s {
	case OutcomeRateLimited, OutcomeCaptcha, OutcomeForbidden, OutcomeBanned:
		return true
	}
	return false
}

// IsFailureOutcome reports whether s always increments failure streaks
// (empty, rate_limited, captcha, auth_invalid, forbidden, banned). network_error
// is not included: it only counts as a failure when blamed on the identity,
// which callers check separately.
func IsFailureOutcome(s string) bool {
	switch s {
	case OutcomeEmpty, OutcomeRateLimited, OutcomeCaptcha, OutcomeAuthInvalid, OutcomeForbidden, OutcomeBanned:
		return true
	}
	return false
}

// Blame attributes an outcome to the identity, the proxy, both or neither.
type Blame string

// Blame values.
const (
	BlameNone     Blame = "none"
	BlameIdentity Blame = "identity"
	BlameProxy    Blame = "proxy"
	BlameBoth     Blame = "both"
)

// ValidBlame reports whether b is a known blame value.
func ValidBlame(b Blame) bool {
	switch b {
	case BlameNone, BlameIdentity, BlameProxy, BlameBoth:
		return true
	}
	return false
}

// DefaultBlame returns the default attribution of an outcome (spec §6.5).
func DefaultBlame(outcome string) Blame {
	switch outcome {
	case OutcomeEmpty, OutcomeCaptcha, OutcomeAuthInvalid, OutcomeForbidden, OutcomeBanned:
		return BlameIdentity
	case OutcomeRateLimited:
		return BlameBoth
	case OutcomeProxyError, OutcomeNetworkError:
		return BlameProxy
	}
	return BlameNone
}

// Identity reports whether the blame includes the identity.
func (b Blame) Identity() bool { return b == BlameIdentity || b == BlameBoth }

// Proxy reports whether the blame includes the proxy.
func (b Blame) Proxy() bool { return b == BlameProxy || b == BlameBoth }

// Node-side error kinds accepted in reports and signal rules. They mirror the
// values allowed by the Report.error_kind field of the node API.
const (
	ErrorKindTimeout     = "timeout"
	ErrorKindConnReset   = "conn_reset"
	ErrorKindConnRefused = "conn_refused"
	ErrorKindProxyAuth   = "proxy_auth"
	ErrorKindTLS         = "tls"
	ErrorKindDNS         = "dns"
	// ErrorKindOther is any transport error the node could not classify.
	ErrorKindOther = "other"
)

// ErrorKinds returns every known error kind in a stable order.
func ErrorKinds() []string {
	return []string{
		ErrorKindTimeout, ErrorKindConnReset, ErrorKindConnRefused,
		ErrorKindProxyAuth, ErrorKindTLS, ErrorKindDNS, ErrorKindOther,
	}
}

// ValidErrorKind reports whether s is a known error kind. The empty string
// (no transport error) is not a kind.
func ValidErrorKind(s string) bool {
	switch s {
	case ErrorKindTimeout, ErrorKindConnReset, ErrorKindConnRefused,
		ErrorKindProxyAuth, ErrorKindTLS, ErrorKindDNS, ErrorKindOther:
		return true
	}
	return false
}
