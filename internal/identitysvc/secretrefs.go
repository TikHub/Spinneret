package identitysvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
)

// maxSecretRefMessagePath bounds a secret path quoted in an error message.
const maxSecretRefMessagePath = 128

// secretRefAccess authorizes the secrets referenced by secret_ref fields of
// payloads a principal writes (ImportIdentities, UpdateIdentityPayload).
//
// A secret_ref value is resolved into the credential of every lease of the
// identity without further authorization, so storing a reference is
// equivalent to reading the secret. Writing a payload therefore requires, for
// every referenced path that the same field of the identity's stored payload
// does not already reference, the permission to read that secret — secret:read with a scope
// matching "<namespace name>/<path>" for API tokens, secret:reveal on the
// namespace for users — and that the secret exists in the identity's
// namespace. References kept unchanged in the same field of the stored
// payload were checked when they were written and are not re-authorized. It
// is not safe for concurrent use.
type secretRefAccess struct {
	p       *authz.Principal
	ns      *catalog.Namespace
	results map[string]error // path -> nil (allowed) or the client error
}

func newSecretRefAccess(p *authz.Principal, ns *catalog.Namespace) *secretRefAccess {
	return &secretRefAccess{p: p, ns: ns, results: map[string]error{}}
}

// check authorizes the paths that were not checked yet: the read permission
// first, then (for permitted paths) the existence of the secret. Problems of
// individual paths are recorded (see denied); only infrastructure errors are
// returned.
func (a *secretRefAccess) check(ctx context.Context, q *identitysvcdb.Queries, paths []string) error {
	perm := authz.PermSecretReveal
	if a.p != nil && a.p.Kind == authz.KindToken {
		perm = authz.PermSecretRead
	}
	var lookup []string
	for _, path := range paths {
		if _, done := a.results[path]; done || slices.Contains(lookup, path) {
			continue
		}
		res := authz.Resource{TenantID: a.ns.TenantID, NamespaceID: a.ns.ID, NamespaceName: a.ns.Name, SecretPath: path}
		if err := a.p.Require(perm, res); err != nil {
			a.results[path] = apperr.PermissionDenied(apperr.ReasonOf(err),
				"the payload references secret %q, which requires %s", truncateText(path, maxSecretRefMessagePath), perm)
			continue
		}
		lookup = append(lookup, path)
	}
	if len(lookup) == 0 {
		return nil
	}
	existing, err := q.SecretPathsExisting(ctx, identitysvcdb.SecretPathsExistingParams{NamespaceID: a.ns.ID, Paths: lookup})
	if err != nil {
		return fmt.Errorf("check referenced secrets: %w", err)
	}
	for _, path := range lookup {
		if slices.Contains(existing, path) {
			a.results[path] = nil
			continue
		}
		a.results[path] = invalid("the payload references secret %q, which does not exist in namespace %q",
			truncateText(path, maxSecretRefMessagePath), a.ns.Name)
	}
	return nil
}

// denied returns the checked paths of paths that are not allowed, in order.
func (a *secretRefAccess) denied(paths []string) []string {
	var out []string
	for _, path := range paths {
		if err, ok := a.results[path]; ok && err != nil {
			out = append(out, path)
		}
	}
	return out
}

// err returns the recorded problem of a denied path.
func (a *secretRefAccess) err(path string) error {
	return a.results[path]
}

// allowStored returns the error of the first reference of refs (secret path
// by secret_ref field of the payload being written) whose path is denied and
// that the payload version stored for the identity does not hold in the same
// field (nil when every denied reference is kept unchanged in its field). A
// reference is only kept in its field: moving a path into another field, for
// example from a field that is not delivered into a delivered one, is a new
// reference. storedVersion is the identity's current payload version; the
// stored payload is decrypted only for this check.
func (s *Service) allowStored(ctx context.Context, q *identitysvcdb.Queries, ct *identity.CompiledType, access *secretRefAccess,
	identityID string, storedVersion int32, refs map[string]string) error {
	var fields []string
	for field, path := range refs {
		if access.err(path) != nil {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	slices.Sort(fields)
	var kept map[string]string
	stored, err := openPayload(ctx, q, s.cipher, identityID, storedVersion)
	switch {
	case err == nil:
		kept = ct.SecretRefFields(stored)
		clear(stored)
	case apperr.IsNotFound(err):
		// No stored version: every reference is new.
	default:
		return apperr.Internal(fmt.Errorf("load stored payload of identity %s: %w", identityID, err))
	}
	for _, field := range fields {
		if kept[field] != refs[field] {
			return access.err(refs[field])
		}
	}
	return nil
}
