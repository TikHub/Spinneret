package apperr

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestConstructors(t *testing.T) {
	t.Parallel()
	cause := errors.New("db down")
	tests := []struct {
		name       string
		err        *Error
		wantCode   connect.Code
		wantReason Reason
		wantMsg    string
		wantRetry  int64
	}{
		{name: "new", err: New(connect.CodeAborted, ReasonConflict, "x %d", 1), wantCode: connect.CodeAborted, wantReason: ReasonConflict, wantMsg: "x 1"},
		{name: "invalid argument default reason", err: InvalidArgument("", "bad %s", "field"), wantCode: connect.CodeInvalidArgument, wantReason: ReasonInvalidArgument, wantMsg: "bad field"},
		{name: "invalid argument custom reason", err: InvalidArgument(ReasonURIInvalid, "uri"), wantCode: connect.CodeInvalidArgument, wantReason: ReasonURIInvalid, wantMsg: "uri"},
		{name: "not found", err: NotFound("site %q not found", "a"), wantCode: connect.CodeNotFound, wantReason: ReasonNotFound, wantMsg: `site "a" not found`},
		{name: "already exists", err: AlreadyExists("dup"), wantCode: connect.CodeAlreadyExists, wantReason: ReasonAlreadyExists, wantMsg: "dup"},
		{name: "conflict", err: Conflict("changed"), wantCode: connect.CodeAborted, wantReason: ReasonConflict, wantMsg: "changed"},
		{name: "failed precondition default", err: FailedPrecondition("", "state"), wantCode: connect.CodeFailedPrecondition, wantReason: ReasonFailedPrecondition, wantMsg: "state"},
		{name: "failed precondition custom", err: FailedPrecondition(ReasonLeaseReleased, "released"), wantCode: connect.CodeFailedPrecondition, wantReason: ReasonLeaseReleased, wantMsg: "released"},
		{name: "permission denied default", err: PermissionDenied("", "no"), wantCode: connect.CodePermissionDenied, wantReason: ReasonPermissionDenied, wantMsg: "no"},
		{name: "permission denied scope", err: PermissionDenied(ReasonScopeMissing, "scope"), wantCode: connect.CodePermissionDenied, wantReason: ReasonScopeMissing, wantMsg: "scope"},
		{name: "unauthenticated", err: Unauthenticated(ReasonTokenInvalid, "token"), wantCode: connect.CodeUnauthenticated, wantReason: ReasonTokenInvalid, wantMsg: "token"},
		{name: "resource exhausted", err: ResourceExhausted(ReasonNoIdentityAvailable, 250, "none"), wantCode: connect.CodeResourceExhausted, wantReason: ReasonNoIdentityAvailable, wantMsg: "none", wantRetry: 250},
		{name: "unavailable", err: Unavailable(ReasonSitePaused, 1000, "paused"), wantCode: connect.CodeUnavailable, wantReason: ReasonSitePaused, wantMsg: "paused", wantRetry: 1000},
		{name: "internal", err: Internal(cause), wantCode: connect.CodeInternal, wantReason: ReasonInternal, wantMsg: "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantCode, tt.err.Code)
			require.Equal(t, tt.wantReason, tt.err.Reason)
			require.Equal(t, tt.wantMsg, tt.err.Message)
			require.Equal(t, tt.wantRetry, tt.err.RetryAfterMs)
		})
	}
	require.ErrorIs(t, Internal(cause), cause)
}

func TestErrorString(t *testing.T) {
	t.Parallel()
	require.Equal(t, "not_found (not_found): gone", NotFound("gone").Error())
	require.Equal(t, "internal (internal): internal error: boom", Internal(errors.New("boom")).Error())
	var nilErr *Error
	require.Equal(t, "<nil>", nilErr.Error())
	require.NoError(t, nilErr.Unwrap())
}

func TestWithRetryAfterAndCauseCopy(t *testing.T) {
	t.Parallel()
	base := Unavailable(ReasonRebuilding, 0, "rebuilding")
	withRetry := base.WithRetryAfter(500)
	require.Equal(t, int64(500), withRetry.RetryAfterMs)
	require.Equal(t, int64(0), base.RetryAfterMs, "original must not be mutated")
	require.NotSame(t, base, withRetry)

	cause := errors.New("redis")
	withCause := base.WithCause(cause)
	require.ErrorIs(t, withCause, cause)
	require.NoError(t, base.Unwrap(), "original must not be mutated")

	var nilErr *Error
	require.Nil(t, nilErr.WithRetryAfter(1))
	require.Nil(t, nilErr.WithCause(cause))
}

func TestAsReasonOfIsNotFound(t *testing.T) {
	t.Parallel()
	nf := NotFound("x")
	wrapped := fmt.Errorf("load site: %w", nf)
	e, ok := As(wrapped)
	require.True(t, ok)
	require.Same(t, nf, e)
	require.Equal(t, ReasonNotFound, ReasonOf(wrapped))
	require.True(t, IsNotFound(wrapped))

	_, ok = As(errors.New("plain"))
	require.False(t, ok)
	_, ok = As(nil)
	require.False(t, ok)
	require.Equal(t, Reason(""), ReasonOf(errors.New("plain")))
	require.Equal(t, Reason(""), ReasonOf(nil))
	require.False(t, IsNotFound(AlreadyExists("x")))
	require.False(t, IsNotFound(nil))

	var typedNil *Error
	var asErr error = typedNil
	_, ok = As(asErr)
	require.False(t, ok, "typed nil *Error is not an application error")
	require.Equal(t, Reason(""), ReasonOf(asErr))
}

func TestToConnect(t *testing.T) {
	t.Parallel()
	passthrough := connect.NewError(connect.CodeUnimplemented, errors.New("nope"))
	passthrough.Meta().Set("X-Custom", "1")
	var typedNil *Error

	tests := []struct {
		name       string
		err        error
		wantCode   connect.Code
		wantMsg    string
		wantReason string
		wantRetry  string
		same       *connect.Error
	}{
		{name: "not found", err: NotFound("site %s not found", "shop"),
			wantCode: connect.CodeNotFound, wantMsg: "site shop not found", wantReason: "not_found"},
		{name: "wrapped app error", err: fmt.Errorf("handler: %w", PermissionDenied(ReasonScopeMissing, "scope missing")),
			wantCode: connect.CodePermissionDenied, wantMsg: "scope missing", wantReason: "scope_missing"},
		{name: "retry header", err: ResourceExhausted(ReasonNoIdentityAvailable, 1234, "exhausted"),
			wantCode: connect.CodeResourceExhausted, wantMsg: "exhausted", wantReason: "no_identity_available", wantRetry: "1234"},
		{name: "zero retry omitted", err: Unavailable(ReasonCircuitOpen, 0, "open"),
			wantCode: connect.CodeUnavailable, wantMsg: "open", wantReason: "circuit_open"},
		{name: "negative retry omitted", err: Unavailable(ReasonCircuitOpen, -5, "open"),
			wantCode: connect.CodeUnavailable, wantMsg: "open", wantReason: "circuit_open"},
		{name: "empty reason omitted", err: New(connect.CodeAborted, "", "conflict"),
			wantCode: connect.CodeAborted, wantMsg: "conflict"},
		{name: "internal hides cause", err: Internal(errors.New("password=hunter2 in dsn")),
			wantCode: connect.CodeInternal, wantMsg: "internal error", wantReason: "internal"},
		{name: "internal wrapping canceled", err: Internal(fmt.Errorf("query: %w", context.Canceled)),
			wantCode: connect.CodeCanceled, wantMsg: "request canceled"},
		{name: "internal wrapping deadline", err: Internal(context.DeadlineExceeded),
			wantCode: connect.CodeDeadlineExceeded, wantMsg: "deadline exceeded"},
		{name: "non-internal app error keeps code despite context cause", err: Unavailable(ReasonRebuilding, 10, "rebuilding").WithCause(context.Canceled),
			wantCode: connect.CodeUnavailable, wantMsg: "rebuilding", wantReason: "rebuilding", wantRetry: "10"},
		{name: "invalid zero code", err: &Error{Reason: ReasonConflict, Message: "leaky detail"},
			wantCode: connect.CodeInternal, wantMsg: "internal error", wantReason: "internal"},
		{name: "invalid large code", err: &Error{Code: connect.Code(99), Message: "leaky detail"},
			wantCode: connect.CodeInternal, wantMsg: "internal error", wantReason: "internal"},
		{name: "context canceled", err: context.Canceled,
			wantCode: connect.CodeCanceled, wantMsg: "request canceled"},
		{name: "wrapped deadline", err: fmt.Errorf("acquire: %w", context.DeadlineExceeded),
			wantCode: connect.CodeDeadlineExceeded, wantMsg: "deadline exceeded"},
		{name: "connect passthrough", err: fmt.Errorf("wrap: %w", passthrough), same: passthrough},
		{name: "plain error generic", err: errors.New("pq: relation secrets does not exist"),
			wantCode: connect.CodeInternal, wantMsg: "internal error", wantReason: "internal"},
		{name: "typed nil app error", err: typedNil,
			wantCode: connect.CodeInternal, wantMsg: "internal error", wantReason: "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ce := ToConnect(tt.err)
			require.NotNil(t, ce)
			if tt.same != nil {
				require.Same(t, tt.same, ce)
				require.Equal(t, "1", ce.Meta().Get("X-Custom"))
				return
			}
			require.Equal(t, tt.wantCode, ce.Code())
			require.Equal(t, tt.wantMsg, ce.Message())
			require.Equal(t, tt.wantReason, ce.Meta().Get(HeaderReason))
			require.Equal(t, tt.wantRetry, ce.Meta().Get(HeaderRetryAfter))
			require.NotContains(t, ce.Error(), "hunter2")
			require.NotContains(t, ce.Error(), "leaky")
			require.NotContains(t, ce.Error(), "relation")
		})
	}
	require.Nil(t, ToConnect(nil))
}

func TestReasonsAreSnakeCase(t *testing.T) {
	t.Parallel()
	reasons := []Reason{
		ReasonTokenInvalid, ReasonTokenExpired, ReasonTokenRevoked, ReasonIPNotAllowed, ReasonSessionInvalid,
		ReasonCSRFMissing, ReasonLoginThrottled, ReasonScopeMissing, ReasonPermissionDenied, ReasonSiteUnknown,
		ReasonClientUnknown, ReasonEndpointGroupUnknown, ReasonURIInvalid, ReasonInvalidArgument,
		ReasonNoIdentityAvailable, ReasonNoProxyAvailable, ReasonCircuitOpen, ReasonSitePaused, ReasonRebuilding,
		ReasonLeaseUnknown, ReasonLeaseReleased, ReasonLeaseExpired, ReasonLeaseLifetimeExceeded, ReasonRateLimited,
		ReasonNotFound, ReasonAlreadyExists, ReasonFailedPrecondition, ReasonConflict, ReasonInternal,
	}
	require.Len(t, reasons, 29, "spec §10 lists 29 reasons")
	seen := map[Reason]bool{}
	for _, r := range reasons {
		require.False(t, seen[r], "duplicate reason %s", r)
		seen[r] = true
		require.Regexp(t, `^[a-z]+(_[a-z]+)*$`, string(r))
	}
}
