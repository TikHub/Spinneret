package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
)

// checkTokenCreatedByToken enforces that a token created by an API token
// principal is never broader than the creating (parent) token:
//
//   - the admin scope cannot be granted (it would let the child mint tokens
//     and hold every namespace permission);
//   - when the parent expires, the child must expire no later than the parent;
//   - when the parent has an IP allowlist, the child allowlist must be
//     non-empty and every child prefix must lie inside a parent prefix;
//   - when the parent is rate limited, the child must be rate limited to at
//     most the parent's rate.
//
// The parent row is read from the database so that the checks use its
// current limits and a token revoked or expired since authentication cannot
// create tokens. Principals other than tokens are not restricted.
func (t *Tokens) checkTokenCreatedByToken(ctx context.Context, p *authz.Principal, in CreateTokenInput, prep preparedToken) error {
	if p == nil || p.Kind != authz.KindToken {
		return nil
	}
	parent, err := t.q.AuthTokenGet(ctx, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.Unauthenticated(apperr.ReasonTokenInvalid, "API token is not valid")
	}
	if err != nil {
		return apperr.Internal(fmt.Errorf("load creating token %s: %w", p.ID, err))
	}
	return validateChildToken(parent, in, prep, t.now())
}

// validateChildToken applies the non-escalation rules of
// checkTokenCreatedByToken to the prepared input of a child token.
func validateChildToken(parent authdb.AuthTokenGetRow, in CreateTokenInput, prep preparedToken, now time.Time) error {
	switch {
	case parent.RevokedAt != nil:
		return apperr.Unauthenticated(apperr.ReasonTokenRevoked, "API token has been revoked")
	case parent.ExpiresAt != nil && !parent.ExpiresAt.After(now):
		return apperr.Unauthenticated(apperr.ReasonTokenExpired, "API token has expired")
	}
	for _, s := range prep.parsed {
		if s.Name == authz.ScopeAdmin {
			return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
				"an API token cannot create tokens with the %q scope", authz.ScopeAdmin)
		}
	}
	if parent.ExpiresAt != nil {
		if in.ExpiresAt == nil {
			return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
				"expires_at is required and must not be after the creating token's expiry (%s)",
				parent.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if in.ExpiresAt.After(*parent.ExpiresAt) {
			return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
				"expires_at must not be after the creating token's expiry (%s)",
				parent.ExpiresAt.UTC().Format(time.RFC3339))
		}
	}
	if err := validateChildAllowlist(parent.IpAllowlist, prep.prefixes); err != nil {
		return err
	}
	if parent.RateLimitRps > 0 && (in.RateLimitRPS <= 0 || in.RateLimitRPS > parent.RateLimitRps) {
		return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
			"rate_limit_rps must be between 1 and the creating token's limit (%d)", parent.RateLimitRps)
	}
	return nil
}

// validateChildAllowlist checks that child prefixes are all contained in the
// parent allowlist. An empty parent allowlist allows every address, so any
// child allowlist is accepted.
func validateChildAllowlist(parentAllow []string, child []netip.Prefix) error {
	parent, err := netx.ParsePrefixes(parentAllow)
	if err != nil {
		return apperr.Internal(fmt.Errorf("parse creating token ip_allowlist: %w", err))
	}
	if len(parent) == 0 {
		return nil
	}
	if len(child) == 0 {
		return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
			"ip_allowlist is required because the creating token has one")
	}
	for _, c := range child {
		if !prefixWithinAny(c, parent) {
			return apperr.PermissionDenied(apperr.ReasonPermissionDenied,
				"ip_allowlist entry %s is not within the creating token's allowlist", c)
		}
	}
	return nil
}

// prefixWithinAny reports whether child is a sub-network of (or equal to) one
// of parents. Prefixes of different address families never contain each
// other.
func prefixWithinAny(child netip.Prefix, parents []netip.Prefix) bool {
	for _, p := range parents {
		if p.Bits() <= child.Bits() && p.Contains(child.Addr()) {
			return true
		}
	}
	return false
}
