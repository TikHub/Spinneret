package authz

import (
	"context"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

type principalKey struct{}

// WithPrincipal returns a copy of ctx carrying p. A nil ctx is treated as
// context.Background().
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal stored in ctx; ok is false when there is
// none (or it is nil).
func FromContext(ctx context.Context) (*Principal, bool) {
	if ctx == nil {
		return nil, false
	}
	p, ok := ctx.Value(principalKey{}).(*Principal)
	if !ok || p == nil {
		return nil, false
	}
	return p, true
}

// MustPrincipal returns the principal stored in ctx or an apperr
// Unauthenticated error with reason session_invalid when absent.
func MustPrincipal(ctx context.Context) (*Principal, error) {
	p, ok := FromContext(ctx)
	if !ok {
		return nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	return p, nil
}
