package spinneret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// Error is the error returned by every [Client] call.
//
// Server errors carry the Connect code and message of the response plus the
// reason and retry hint of the Spinneret-Reason and Spinneret-Retry-After-Ms
// headers (response trailers with gRPC). Errors without a usable response
// (connection failures, TLS, DNS, per-call timeouts) have [ReasonTransport]
// and a non-empty ErrorKind.
type Error struct {
	// Code is the Connect error code, e.g. connect.CodeResourceExhausted.
	Code connect.Code
	// Reason is the Spinneret reason, e.g. "no_identity_available"; empty
	// when the response carried none.
	Reason string
	// Message is the human-readable message.
	Message string
	// RetryAfter is the server retry hint; zero when absent.
	RetryAfter time.Duration
	// Procedure is the RPC that failed, e.g. "/spinneret.v1.LeaseService/Acquire".
	Procedure string
	// ErrorKind classifies transport failures (see [ClassifyError]); empty
	// for errors returned by the server.
	ErrorKind string

	// wire is true when the server sent the error.
	wire bool
	// preSend is true for transport failures that happened before the request
	// could reach the server (dial, DNS), so retrying cannot duplicate it.
	preSend bool
	// permanent is true for transport failures that retrying cannot fix.
	permanent bool
	cause     error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("spinneret: ")
	if e.Procedure != "" {
		b.WriteString(e.Procedure)
		b.WriteString(": ")
	}
	b.WriteString(e.Code.String())
	if e.Reason != "" {
		b.WriteString(" (")
		b.WriteString(e.Reason)
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

// Unwrap returns the underlying cause (for example a *url.Error or a
// *connect.Error), or nil.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// FromServer reports whether the server sent the error (as opposed to an
// error synthesized by the client from a transport failure or an HTTP status).
func (e *Error) FromServer() bool { return e != nil && e.wire }

// Transport reports whether no usable response was received.
func (e *Error) Transport() bool { return e != nil && e.ErrorKind != "" }

// newError builds an SDK-side error.
func newError(code connect.Code, reason, format string, args ...any) *Error {
	return &Error{Code: code, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// errInvalidOptions builds the error returned for invalid options.
func errInvalidOptions(format string, args ...any) *Error {
	return newError(connect.CodeInvalidArgument, ReasonInvalidConfiguration, format, args...)
}

// AsError returns the *Error in err's chain.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}

// CodeOf returns the Connect code of err (connect.CodeUnknown for foreign
// errors; 0 for nil).
func CodeOf(err error) connect.Code {
	if err == nil {
		return 0
	}
	if e, ok := AsError(err); ok {
		return e.Code
	}
	return connect.CodeOf(err)
}

// ReasonOf returns the Spinneret reason of err, or "".
func ReasonOf(err error) string {
	if e, ok := AsError(err); ok {
		return e.Reason
	}
	return ""
}

// RetryAfterOf returns the server retry hint of err, or zero.
func RetryAfterOf(err error) time.Duration {
	if e, ok := AsError(err); ok {
		return e.RetryAfter
	}
	return 0
}

func hasReason(err error, reasons ...string) bool {
	reason := ReasonOf(err)
	if reason == "" {
		return false
	}
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// IsNoIdentity reports whether no identity was available (wait RetryAfter and retry).
func IsNoIdentity(err error) bool { return hasReason(err, ReasonNoIdentityAvailable) }

// IsNoProxy reports whether no proxy matching the rotation policy was available.
func IsNoProxy(err error) bool { return hasReason(err, ReasonNoProxyAvailable) }

// IsCircuitOpen reports whether the endpoint group breaker is open (pause the endpoint group).
func IsCircuitOpen(err error) bool { return hasReason(err, ReasonCircuitOpen) }

// IsSitePaused reports whether the site is paused by an operator (pause the site).
func IsSitePaused(err error) bool { return hasReason(err, ReasonSitePaused) }

// IsLeaseGone reports whether the lease no longer exists: unknown, released,
// expired or past its lifetime cap. Acquire a new lease.
func IsLeaseGone(err error) bool {
	return hasReason(err, ReasonLeaseUnknown, ReasonLeaseReleased, ReasonLeaseExpired, ReasonLeaseLifetimeExceeded)
}

// IsUnauthenticated reports whether the token is missing, invalid, expired or revoked.
func IsUnauthenticated(err error) bool {
	return err != nil && CodeOf(err) == connect.CodeUnauthenticated
}

// IsPermissionDenied reports whether the token lacks a required scope.
func IsPermissionDenied(err error) bool {
	return err != nil && CodeOf(err) == connect.CodePermissionDenied
}

// IsTransport reports whether err is a transport failure (no usable response).
func IsTransport(err error) bool {
	e, ok := AsError(err)
	return ok && e.Transport()
}

// IsRetryable reports whether a background delivery failing with err should
// be retried later: transport failures, unavailable (except circuit_open and
// site_paused), internal, unknown, data_loss, deadline_exceeded, aborted and
// resource_exhausted.
func IsRetryable(err error) bool {
	e, ok := AsError(err)
	if !ok {
		return false
	}
	if e.Transport() {
		return true
	}
	switch e.Code {
	case connect.CodeUnavailable:
		return e.Reason != ReasonCircuitOpen && e.Reason != ReasonSitePaused
	case connect.CodeInternal, connect.CodeUnknown, connect.CodeDataLoss,
		connect.CodeDeadlineExceeded, connect.CodeAborted, connect.CodeResourceExhausted:
		return true
	default:
		return false
	}
}

// fromCallError converts an error returned by a generated Connect client.
// perCallTimeout tells whether the per-call deadline of the SDK (and not the
// caller's context) expired.
func fromCallError(procedure string, err error, perCallTimeout bool) *Error {
	if e, ok := AsError(err); ok {
		return e
	}
	out := &Error{Procedure: procedure, cause: err}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		out.Code = connect.CodeOf(err)
		out.Message = err.Error()
		if perCallTimeout {
			markTransport(out, context.DeadlineExceeded)
		}
		return out
	}
	out.Code = ce.Code()
	out.Message = ce.Message()
	out.wire = connect.IsWireError(err)
	meta := ce.Meta()
	out.Reason = strings.TrimSpace(meta.Get(HeaderReason))
	out.RetryAfter = parseRetryAfter(meta.Get(HeaderRetryAfterMs))
	if out.wire {
		return out
	}
	switch cause := ce.Unwrap(); {
	case perCallTimeout && (out.Code == connect.CodeDeadlineExceeded || out.Code == connect.CodeCanceled):
		// The SDK cancels an attempt that ran into its own timeout, which
		// Connect reports as canceled: report the timeout instead.
		out.Code = connect.CodeDeadlineExceeded
		out.Message = "the call did not finish within the per-call timeout"
		markTransport(out, context.DeadlineExceeded)
	case out.Code == connect.CodeCanceled || out.Code == connect.CodeDeadlineExceeded:
		// The caller's context ended: not a transport failure.
	case isTransportCause(cause):
		markTransport(out, cause)
	}
	return out
}

func markTransport(e *Error, cause error) {
	e.Reason = ReasonTransport
	e.ErrorKind = ClassifyError(cause)
	if e.ErrorKind == ErrorKindNone {
		e.ErrorKind = ErrorKindOther
	}
	e.preSend = isPreSend(cause)
	e.permanent = isPermanentTransport(cause)
}

// parseRetryAfter parses a Spinneret-Retry-After-Ms header value.
func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	const maxMs = int64(time.Duration(1<<62) / time.Millisecond)
	return time.Duration(min(ms, maxMs)) * time.Millisecond
}

// isTransportCause reports whether cause is a network-level failure.
func isTransportCause(cause error) bool {
	if cause == nil {
		return false
	}
	var ue *url.Error
	var ne net.Error
	return errors.As(cause, &ue) || errors.As(cause, &ne) ||
		errors.Is(cause, io.ErrUnexpectedEOF) || errors.Is(cause, io.EOF) ||
		classifyErrno(cause) != ""
}

// isPreSend reports whether the failure happened before any request byte
// reached the server: dial errors (including DNS and proxy dials).
func isPreSend(cause error) bool {
	var dns *net.DNSError
	if errors.As(cause, &dns) {
		return true
	}
	var op *net.OpError
	return errors.As(cause, &op) && (op.Op == "dial" || op.Op == "proxyconnect")
}

// isPermanentTransport reports transport failures caused by configuration
// (certificate verification), which retrying cannot fix.
func isPermanentTransport(cause error) bool {
	return isCertificateError(cause)
}
