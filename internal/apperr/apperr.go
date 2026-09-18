// Package apperr defines typed application errors that carry a Connect code,
// a machine-readable reason and an optional retry hint. Handlers convert them
// with ToConnect so that plain JSON clients can read the reason from the
// Spinneret-Reason response header.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
)

// Reason is a stable, snake_case machine-readable error reason.
type Reason string

// Header names used to expose structured error details to JSON clients.
const (
	HeaderReason     = "Spinneret-Reason"
	HeaderRetryAfter = "Spinneret-Retry-After-Ms"
)

// Error reasons. The list mirrors section 10 of the implementation spec.
const (
	ReasonTokenInvalid          Reason = "token_invalid"
	ReasonTokenExpired          Reason = "token_expired"
	ReasonTokenRevoked          Reason = "token_revoked"
	ReasonIPNotAllowed          Reason = "ip_not_allowed"
	ReasonSessionInvalid        Reason = "session_invalid"
	ReasonCSRFMissing           Reason = "csrf_missing"
	ReasonLoginThrottled        Reason = "login_throttled"
	ReasonScopeMissing          Reason = "scope_missing"
	ReasonPermissionDenied      Reason = "permission_denied"
	ReasonSiteUnknown           Reason = "site_unknown"
	ReasonClientUnknown         Reason = "client_unknown"
	ReasonEndpointGroupUnknown  Reason = "endpoint_group_unknown"
	ReasonURIInvalid            Reason = "uri_invalid"
	ReasonInvalidArgument       Reason = "invalid_argument"
	ReasonNoIdentityAvailable   Reason = "no_identity_available"
	ReasonNoProxyAvailable      Reason = "no_proxy_available"
	ReasonCircuitOpen           Reason = "circuit_open"
	ReasonSitePaused            Reason = "site_paused"
	ReasonRebuilding            Reason = "rebuilding"
	ReasonLeaseUnknown          Reason = "lease_unknown"
	ReasonLeaseReleased         Reason = "lease_released"
	ReasonLeaseExpired          Reason = "lease_expired"
	ReasonLeaseLifetimeExceeded Reason = "lease_lifetime_exceeded"
	ReasonRateLimited           Reason = "rate_limited"
	ReasonNotFound              Reason = "not_found"
	ReasonAlreadyExists         Reason = "already_exists"
	ReasonFailedPrecondition    Reason = "failed_precondition"
	ReasonConflict              Reason = "conflict"
	ReasonQueryTooLarge         Reason = "query_too_large"
	ReasonQueryTimeout          Reason = "query_timeout"
	ReasonInternal              Reason = "internal"
)

// Error is an application error with a Connect code and a reason.
type Error struct {
	Code         connect.Code
	Reason       Reason
	Message      string
	RetryAfterMs int64
	Err          error
}

// Error implements the error interface. The cause is included for logging
// but never sent to clients (see ToConnect).
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("%s (%s): %s: %v", e.Code, e.Reason, e.Message, e.Err)
	}
	return fmt.Sprintf("%s (%s): %s", e.Code, e.Reason, e.Message)
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// WithRetryAfter returns a copy of e carrying a retry hint in milliseconds.
// A nil receiver yields nil.
func (e *Error) WithRetryAfter(ms int64) *Error {
	if e == nil {
		return nil
	}
	c := *e
	c.RetryAfterMs = ms
	return &c
}

// WithCause returns a copy of e wrapping err. A nil receiver yields nil.
func (e *Error) WithCause(err error) *Error {
	if e == nil {
		return nil
	}
	c := *e
	c.Err = err
	return &c
}

// New creates an Error with a formatted message.
func New(code connect.Code, reason Reason, format string, args ...any) *Error {
	return &Error{Code: code, Reason: reason, Message: sprintf(format, args...)}
}

// InvalidArgument reports a malformed request.
func InvalidArgument(reason Reason, format string, args ...any) *Error {
	if reason == "" {
		reason = ReasonInvalidArgument
	}
	return New(connect.CodeInvalidArgument, reason, format, args...)
}

// NotFound reports a missing resource.
func NotFound(format string, args ...any) *Error {
	return New(connect.CodeNotFound, ReasonNotFound, format, args...)
}

// AlreadyExists reports a uniqueness violation.
func AlreadyExists(format string, args ...any) *Error {
	return New(connect.CodeAlreadyExists, ReasonAlreadyExists, format, args...)
}

// Conflict reports a concurrent modification.
func Conflict(format string, args ...any) *Error {
	return New(connect.CodeAborted, ReasonConflict, format, args...)
}

// FailedPrecondition reports that the system is not in a state required by the operation.
func FailedPrecondition(reason Reason, format string, args ...any) *Error {
	if reason == "" {
		reason = ReasonFailedPrecondition
	}
	return New(connect.CodeFailedPrecondition, reason, format, args...)
}

// PermissionDenied reports an authorization failure.
func PermissionDenied(reason Reason, format string, args ...any) *Error {
	if reason == "" {
		reason = ReasonPermissionDenied
	}
	return New(connect.CodePermissionDenied, reason, format, args...)
}

// Unauthenticated reports missing or invalid credentials.
func Unauthenticated(reason Reason, format string, args ...any) *Error {
	return New(connect.CodeUnauthenticated, reason, format, args...)
}

// ResourceExhausted reports exhaustion with a retry hint.
func ResourceExhausted(reason Reason, retryAfterMs int64, format string, args ...any) *Error {
	e := New(connect.CodeResourceExhausted, reason, format, args...)
	e.RetryAfterMs = retryAfterMs
	return e
}

// Unavailable reports a temporary unavailability with a retry hint.
func Unavailable(reason Reason, retryAfterMs int64, format string, args ...any) *Error {
	e := New(connect.CodeUnavailable, reason, format, args...)
	e.RetryAfterMs = retryAfterMs
	return e
}

// Internal wraps an unexpected error. The client only sees a generic message.
func Internal(err error) *Error {
	return &Error{Code: connect.CodeInternal, Reason: ReasonInternal, Message: "internal error", Err: err}
}

// As extracts an *Error from an error chain. A typed nil *Error in the chain
// is not reported.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}

// ReasonOf returns the reason of an *Error in the chain, or "".
func ReasonOf(err error) Reason {
	if e, ok := As(err); ok {
		return e.Reason
	}
	return ""
}

// IsNotFound reports whether err is a not_found application error.
func IsNotFound(err error) bool {
	e, ok := As(err)
	return ok && e.Code == connect.CodeNotFound
}

// ToConnect converts any error into a *connect.Error suitable for returning
// from a handler. Application errors keep their code, message, reason and
// retry hint (as error metadata, which Connect sends as response headers for
// unary RPCs). Unknown errors become a generic internal error so that no
// implementation details leak to clients. Internal or unknown application
// errors caused by context cancellation or deadline expiry are reported as
// canceled / deadline_exceeded, and application errors carrying an invalid
// code are treated as unknown errors.
func ToConnect(err error) *connect.Error {
	if err == nil {
		return nil
	}
	if e, ok := As(err); ok {
		switch {
		case e.Code == connect.CodeInternal || e.Code == connect.CodeUnknown:
			if ce := contextError(err); ce != nil {
				return ce
			}
		case e.Code < connect.CodeCanceled || e.Code > connect.CodeUnauthenticated:
			return internalConnectError()
		}
		ce := connect.NewError(e.Code, errors.New(e.Message))
		if e.Reason != "" {
			ce.Meta().Set(HeaderReason, string(e.Reason))
		}
		if e.RetryAfterMs > 0 {
			ce.Meta().Set(HeaderRetryAfter, strconv.FormatInt(e.RetryAfterMs, 10))
		}
		return ce
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	if ce := contextError(err); ce != nil {
		return ce
	}
	return internalConnectError()
}

// contextError maps context cancellation and deadline errors, or returns nil.
func contextError(err error) *connect.Error {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, errors.New("request canceled"))
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("deadline exceeded"))
	default:
		return nil
	}
}

func internalConnectError() *connect.Error {
	ce := connect.NewError(connect.CodeInternal, errors.New("internal error"))
	ce.Meta().Set(HeaderReason, string(ReasonInternal))
	return ce
}

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}
