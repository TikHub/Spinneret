package spinneret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestErrorString(t *testing.T) {
	e := &Error{Code: connect.CodeUnavailable, Reason: ReasonCircuitOpen, Message: "breaker open", Procedure: "/p"}
	require.Equal(t, "spinneret: /p: unavailable (circuit_open): breaker open", e.Error())
	require.Equal(t, "spinneret: not_found", (&Error{Code: connect.CodeNotFound}).Error())
	var nilErr *Error
	require.Equal(t, "<nil>", nilErr.Error())
	require.NoError(t, nilErr.Unwrap())
	require.False(t, nilErr.FromServer())
	require.False(t, nilErr.Transport())

	cause := errors.New("root")
	wrapped := &Error{Code: connect.CodeInternal, cause: cause}
	require.ErrorIs(t, fmt.Errorf("outer: %w", wrapped), cause)
}

func TestErrorHelpers(t *testing.T) {
	mk := func(code connect.Code, reason string) error {
		return fmt.Errorf("wrapped: %w", &Error{Code: code, Reason: reason, RetryAfter: time.Second})
	}
	require.True(t, IsNoIdentity(mk(connect.CodeResourceExhausted, ReasonNoIdentityAvailable)))
	require.True(t, IsNoProxy(mk(connect.CodeResourceExhausted, ReasonNoProxyAvailable)))
	require.True(t, IsCircuitOpen(mk(connect.CodeUnavailable, ReasonCircuitOpen)))
	require.True(t, IsSitePaused(mk(connect.CodeUnavailable, ReasonSitePaused)))
	for _, reason := range []string{ReasonLeaseUnknown, ReasonLeaseReleased, ReasonLeaseExpired, ReasonLeaseLifetimeExceeded} {
		require.True(t, IsLeaseGone(mk(connect.CodeFailedPrecondition, reason)), reason)
	}
	require.False(t, IsLeaseGone(mk(connect.CodeNotFound, ReasonNotFound)))
	require.True(t, IsUnauthenticated(mk(connect.CodeUnauthenticated, ReasonTokenInvalid)))
	require.True(t, IsPermissionDenied(mk(connect.CodePermissionDenied, ReasonScopeMissing)))
	require.Equal(t, time.Second, RetryAfterOf(mk(connect.CodeUnavailable, "")))

	for _, fn := range []func(error) bool{IsNoIdentity, IsCircuitOpen, IsUnauthenticated, IsPermissionDenied, IsTransport, IsRetryable, IsLeaseGone} {
		require.False(t, fn(nil))
		require.False(t, fn(errors.New("plain")))
	}
	require.Equal(t, connect.Code(0), CodeOf(nil))
	require.Equal(t, connect.CodeUnknown, CodeOf(errors.New("plain")))
	require.Equal(t, connect.CodeNotFound, CodeOf(connect.NewError(connect.CodeNotFound, errors.New("x"))))
	require.Empty(t, ReasonOf(errors.New("plain")))
	require.Zero(t, RetryAfterOf(errors.New("plain")))
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		err  *Error
		want bool
	}{
		{&Error{Code: connect.CodeUnavailable}, true},
		{&Error{Code: connect.CodeUnavailable, Reason: ReasonCircuitOpen}, false},
		{&Error{Code: connect.CodeUnavailable, Reason: ReasonSitePaused}, false},
		{&Error{Code: connect.CodeInternal}, true},
		{&Error{Code: connect.CodeUnknown}, true},
		{&Error{Code: connect.CodeDataLoss}, true},
		{&Error{Code: connect.CodeDeadlineExceeded}, true},
		{&Error{Code: connect.CodeAborted}, true},
		{&Error{Code: connect.CodeResourceExhausted}, true},
		{&Error{Code: connect.CodeInvalidArgument}, false},
		{&Error{Code: connect.CodeUnauthenticated}, false},
		{&Error{Code: connect.CodeCanceled}, false},
		{&Error{Code: connect.CodeCanceled, ErrorKind: ErrorKindTimeout}, true},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, IsRetryable(tc.err), "%+v", tc.err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	require.Equal(t, 1500*time.Millisecond, parseRetryAfter(" 1500 "))
	require.Zero(t, parseRetryAfter(""))
	require.Zero(t, parseRetryAfter("soon"))
	require.Zero(t, parseRetryAfter("-5"))
	require.Positive(t, parseRetryAfter("9223372036854775807"))
}

func TestFromCallError(t *testing.T) {
	// Already converted errors pass through.
	orig := &Error{Code: connect.CodeNotFound}
	require.Same(t, orig, fromCallError("/p", orig, false))

	// Foreign errors keep their code.
	plain := fromCallError("/p", errors.New("boom"), false)
	require.Equal(t, connect.CodeUnknown, plain.Code)
	require.False(t, plain.Transport())
	timeout := fromCallError("/p", errors.New("boom"), true)
	require.Equal(t, ErrorKindTimeout, timeout.ErrorKind)

	// Transport causes are classified; pre-send detection covers dial errors.
	dial := &url.Error{Op: "Post", URL: "http://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}
	e := fromCallError("/p", connect.NewError(connect.CodeUnavailable, dial), false)
	require.True(t, e.Transport())
	require.True(t, e.preSend)
	require.Equal(t, ErrorKindConnRefused, e.ErrorKind)

	reset := fromCallError("/p", connect.NewError(connect.CodeUnavailable, io.ErrUnexpectedEOF), false)
	require.True(t, reset.Transport())
	require.False(t, reset.preSend)
	require.Equal(t, ErrorKindConnReset, reset.ErrorKind)

	dns := fromCallError("/p", connect.NewError(connect.CodeUnavailable, &net.DNSError{Err: "no such host", Name: "x"}), false)
	require.True(t, dns.preSend)
	require.Equal(t, ErrorKindDNS, dns.ErrorKind)

	// The caller's own cancellation is not a transport failure.
	canceled := fromCallError("/p", connect.NewError(connect.CodeCanceled, context.Canceled), false)
	require.False(t, canceled.Transport())
	perCall := fromCallError("/p", connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded), true)
	require.True(t, perCall.Transport())

	// An error synthesized from an HTTP status is neither wire nor transport.
	status := fromCallError("/p", connect.NewError(connect.CodeUnavailable, errors.New("503 Service Unavailable")), false)
	require.False(t, status.Transport())
	require.False(t, status.FromServer())

	// Unknown transport failures still get a kind.
	other := &Error{}
	markTransport(other, errors.New("weird"))
	require.Equal(t, ErrorKindOther, other.ErrorKind)
	markTransport(other, nil)
	require.Equal(t, ErrorKindOther, other.ErrorKind)
	require.False(t, isTransportCause(nil))
}

func TestShouldRetryCall(t *testing.T) {
	tests := []struct {
		name       string
		err        *Error
		idempotent bool
		want       bool
	}{
		{"pre-send transport", &Error{ErrorKind: ErrorKindConnRefused, preSend: true}, false, true},
		{"ambiguous transport, idempotent", &Error{ErrorKind: ErrorKindConnReset}, true, true},
		{"ambiguous transport, not idempotent", &Error{ErrorKind: ErrorKindConnReset}, false, false},
		{"permanent transport", &Error{ErrorKind: ErrorKindTLS, permanent: true, preSend: true}, true, false},
		{"wire unavailable", &Error{Code: connect.CodeUnavailable, wire: true}, false, true},
		{"status unavailable, idempotent", &Error{Code: connect.CodeUnavailable}, true, true},
		{"status unavailable, not idempotent", &Error{Code: connect.CodeUnavailable}, false, false},
		{"circuit open", &Error{Code: connect.CodeUnavailable, Reason: ReasonCircuitOpen, wire: true}, true, false},
		{"internal", &Error{Code: connect.CodeInternal, wire: true}, true, false},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, shouldRetryCall(tc.err, tc.idempotent), tc.name)
	}
}
