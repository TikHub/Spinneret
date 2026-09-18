package vault

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/vault/vaultdb"
)

// deniedLookupTimeout bounds the best-effort lookup that attaches the secret
// ID to the audit entry of a denied read.
const deniedLookupTimeout = 2 * time.Second

// ReadSecret returns a version (0 = current) of the secret at path in ns on
// behalf of p. API tokens need secret:read with a scope glob matching
// "<namespace name>/<path>"; other principals need secret:reveal. Every
// attempt is audited as secret.read with the purpose, version and client IP
// (result ok, denied or error). Expired secrets are still returned and logged
// at warn level. A missing secret or version yields not_found.
func (s *SecretStore) ReadSecret(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, path string, version int, purpose string) (SecretValue, error) {
	if ns == nil {
		return SecretValue{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	if err := ValidateSecretPath(path); err != nil {
		return SecretValue{}, err
	}
	if version < 0 || version > math.MaxInt32 {
		return SecretValue{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "version must be >= 0")
	}
	if p == nil {
		return SecretValue{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	purpose = sanitizePurpose(purpose)
	details := map[string]any{"version": version, "purpose": purpose}

	perm := authz.PermSecretReveal
	if p.Kind == authz.KindToken {
		perm = authz.PermSecretRead
	}
	if err := p.Require(perm, secretResource(ns.TenantID, ns.ID, ns.Name, path)); err != nil {
		s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretRead, s.lookupIDForAudit(ctx, ns.ID, path), path,
			audit.ResultDenied, details)
		return SecretValue{}, err
	}

	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	row, err := s.q.VaultSecretReadByPath(ctx, vaultdb.VaultSecretReadByPathParams{
		NamespaceID: ns.ID, Path: path, Version: int32(version),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			details["error"] = "not_found"
			s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretRead, s.lookupIDForAudit(ctx, ns.ID, path), path,
				audit.ResultError, details)
			if version > 0 {
				return SecretValue{}, apperr.NotFound("secret %q version %d not found", path, version)
			}
			return SecretValue{}, apperr.NotFound("secret %q not found", path)
		}
		details["error"] = "unavailable"
		s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretRead, "", path, audit.ResultError, details)
		return SecretValue{}, errorf(err, "read secret")
	}
	value, err := s.openEnvelope(row.ID, envelope{
		version: row.Version, ciphertext: row.Ciphertext, wrapped: row.WrappedDek, kekID: row.KekID,
	})
	details["version"] = int(row.Version)
	if err != nil {
		details["error"] = "decrypt"
		s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretRead, row.ID, path, audit.ResultError, details)
		return SecretValue{}, err
	}
	now := s.now()
	s.warnIfExpired(ns.ID, row.ID, row.ExpiresAt, now)
	s.access.touch(row.ID, now)
	s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretRead, row.ID, path, audit.ResultOK, details)
	return SecretValue{Path: row.Path, Version: int(row.Version), Value: value, ExpiresAt: row.ExpiresAt}, nil
}

// lookupIDForAudit returns the ID of the secret at path, or "" when it does
// not exist or cannot be loaded quickly. It lets denied and failed reads show
// up in the access log of the secret.
func (s *SecretStore) lookupIDForAudit(ctx context.Context, namespaceID, path string) string {
	ctx, cancel := context.WithTimeout(ctx, deniedLookupTimeout)
	defer cancel()
	id, err := s.q.VaultSecretIDByPath(ctx, vaultdb.VaultSecretIDByPathParams{NamespaceID: namespaceID, Path: path})
	if err != nil {
		return ""
	}
	return id
}

func (s *SecretStore) warnIfExpired(namespaceID, secretID string, expiresAt *time.Time, now time.Time) {
	if expiresAt != nil && expiresAt.Before(now) {
		s.logger.Warn("expired secret was read",
			slog.String("namespace_id", namespaceID), slog.String("secret_id", secretID),
			slog.Time("expires_at", *expiresAt))
	}
}

// ResolveForIdentity returns the current value of the secret at path in the
// namespace for identity payload rendering. It performs no authorization
// (rendering is authorized when the lease is acquired) and writes no per-read
// audit entry. Values are cached for 30 seconds per namespace and path, and
// concurrent misses for the same secret share one database read.
func (s *SecretStore) ResolveForIdentity(ctx context.Context, namespaceID, path string) (string, error) {
	if namespaceID == "" {
		return "", apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace id is required")
	}
	if err := ValidateSecretPath(path); err != nil {
		return "", err
	}
	now := s.now()
	if e, ok := s.resolve.get(namespaceID, path, now); ok {
		s.access.touch(e.secretID, now)
		return e.value, nil
	}
	key := resolveKey(namespaceID, path)
	ch := s.flight.DoChan(key, func() (any, error) {
		// Detached from the first caller's cancellation so that one canceled
		// request does not fail every waiter; bounded by the operation timeout.
		lctx, cancel := secretOpContext(context.WithoutCancel(ctx))
		defer cancel()
		return s.loadForResolve(lctx, namespaceID, path)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		e := res.Val.(resolveEntry)
		s.access.touch(e.secretID, s.now())
		return e.value, nil
	}
}

func (s *SecretStore) loadForResolve(ctx context.Context, namespaceID, path string) (resolveEntry, error) {
	// A local change committed while this load runs must not be overwritten
	// by the value read before it: the generation taken before the query
	// guards the cache insert.
	gen := s.resolve.generation()
	row, err := s.q.VaultSecretReadByPath(ctx, vaultdb.VaultSecretReadByPathParams{NamespaceID: namespaceID, Path: path})
	if errors.Is(err, pgx.ErrNoRows) {
		return resolveEntry{}, apperr.NotFound("secret %q not found", path)
	}
	if err != nil {
		return resolveEntry{}, errorf(err, "resolve secret")
	}
	value, err := s.openEnvelope(row.ID, envelope{
		version: row.Version, ciphertext: row.Ciphertext, wrapped: row.WrappedDek, kekID: row.KekID,
	})
	if err != nil {
		return resolveEntry{}, err
	}
	now := s.now()
	s.warnIfExpired(namespaceID, row.ID, row.ExpiresAt, now)
	e := resolveEntry{secretID: row.ID, value: value}
	if s.afterResolveLoad != nil {
		s.afterResolveLoad()
	}
	s.resolve.addIfCurrent(namespaceID, path, e, now, gen)
	return e, nil
}

// invalidateResolved drops the cached value of a secret path after a local
// change and detaches loads already in flight, so later resolutions read the
// committed state instead of joining or caching a stale read.
func (s *SecretStore) invalidateResolved(namespaceID, path string) {
	s.resolve.remove(namespaceID, path)
	s.flight.Forget(resolveKey(namespaceID, path))
}
