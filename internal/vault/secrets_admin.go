package vault

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaultdb"
)

// CreateSecretInput describes a new secret.
type CreateSecretInput struct {
	Path        string
	Value       string
	Description string
	Tags        []string
	ExpiresAt   *time.Time
}

// UpdateSecretInput describes changes to a secret. Nil pointers and false
// flags leave the corresponding attribute unchanged.
type UpdateSecretInput struct {
	// Value, when set, is stored as a new version.
	Value       *string
	Description *string
	// Tags replace the current tags when SetTags is true (an empty slice clears them).
	Tags    []string
	SetTags bool
	// ExpiresAt sets the expiry; ClearExpiresAt removes it. They are mutually exclusive.
	ExpiresAt      *time.Time
	ClearExpiresAt bool
}

// Create stores a new secret with its first version (secret:write).
func (s *SecretStore) Create(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in CreateSecretInput) (Secret, error) {
	if ns == nil {
		return Secret{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	if err := validateCreate(&in); err != nil {
		return Secret{}, err
	}
	res := secretResource(ns.TenantID, ns.ID, ns.Name, in.Path)
	if p == nil {
		return Secret{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if err := p.Require(authz.PermSecretWrite, res); err != nil {
		return Secret{}, err
	}

	id := idgen.New(idgen.Secret)
	sealed, err := s.cipher.Seal([]byte(in.Value), secretVersionAAD(id, 1))
	if err != nil {
		return Secret{}, apperr.Internal(fmt.Errorf("seal secret: %w", err))
	}
	actor := p.Actor()
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	var created vaultdb.VaultSecretInsertRow
	err = s.inTx(ctx, func(q *vaultdb.Queries) error {
		row, err := q.VaultSecretInsert(ctx, vaultdb.VaultSecretInsertParams{
			ID: id, NamespaceID: ns.ID, Path: in.Path, Description: in.Description,
			Tags: in.Tags, ExpiresAt: in.ExpiresAt, CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		created = row
		return q.VaultSecretVersionInsert(ctx, vaultdb.VaultSecretVersionInsertParams{
			SecretID: id, Version: 1, Ciphertext: sealed.Ciphertext, WrappedDek: sealed.WrappedDEK,
			KekID: sealed.KEKID, CreatedBy: actor,
		})
	})
	if err != nil {
		return Secret{}, pgstore.MapError(err, fmt.Sprintf("secret %q", in.Path))
	}
	// A path that was deleted and re-created must not serve the old value.
	s.invalidateResolved(ns.ID, in.Path)
	s.record(ctx, p, ns.TenantID, ns.ID, AuditActionSecretCreate, id, in.Path, audit.ResultOK,
		map[string]any{"version": 1})
	return Secret{
		ID: id, TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, Path: in.Path,
		Description: in.Description, Tags: in.Tags, CurrentVersion: 1, MaskedValue: MaskSecretValue(in.Value),
		ExpiresAt: in.ExpiresAt, CreatedBy: actor, CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt,
	}, nil
}

func validateCreate(in *CreateSecretInput) error {
	if err := ValidateSecretPath(in.Path); err != nil {
		return err
	}
	if err := validateSecretValue(in.Value); err != nil {
		return err
	}
	if err := validateDescription(in.Description); err != nil {
		return err
	}
	tags, err := normalizeTags(in.Tags, MaxSecretTags)
	if err != nil {
		return err
	}
	in.Tags = tags
	return nil
}

// Update changes the metadata of a secret and, when a value is given, stores
// it as a new version (secret:write).
func (s *SecretStore) Update(ctx context.Context, p *authz.Principal, id string, in UpdateSecretInput) (Secret, error) {
	if err := validateUpdate(&in); err != nil {
		return Secret{}, err
	}
	if id == "" {
		return Secret{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret id is required")
	}
	ctx, cancel := secretOpContext(ctx)
	defer cancel()

	var (
		locked     vaultdb.VaultSecretLockRow
		newVersion int32
	)
	err := s.inTx(ctx, func(q *vaultdb.Queries) error {
		row, err := q.VaultSecretLock(ctx, id)
		if err != nil {
			return err
		}
		locked = row
		res := secretResource(row.TenantID, row.NamespaceID, row.NamespaceName, row.Path)
		if err := authorizeSecret(p, res, authz.PermSecretWrite); err != nil {
			return err
		}
		newVersion = row.CurrentVersion
		if in.Value != nil {
			if row.CurrentVersion == math.MaxInt32 {
				return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "secret has too many versions")
			}
			newVersion++
			sealed, err := s.cipher.Seal([]byte(*in.Value), secretVersionAAD(id, int(newVersion)))
			if err != nil {
				return apperr.Internal(fmt.Errorf("seal secret: %w", err))
			}
			if err := q.VaultSecretVersionInsert(ctx, vaultdb.VaultSecretVersionInsertParams{
				SecretID: id, Version: newVersion, Ciphertext: sealed.Ciphertext, WrappedDek: sealed.WrappedDEK,
				KekID: sealed.KEKID, CreatedBy: p.Actor(),
			}); err != nil {
				return err
			}
		}
		params := vaultdb.VaultSecretUpdateParams{
			ID: id, CurrentVersion: newVersion,
			SetTags: in.SetTags, Tags: in.Tags,
			SetExpiresAt: in.ExpiresAt != nil || in.ClearExpiresAt, ExpiresAt: in.ExpiresAt,
		}
		if in.Description != nil {
			params.SetDescription, params.Description = true, *in.Description
		}
		if params.Tags == nil {
			params.Tags = []string{}
		}
		return q.VaultSecretUpdate(ctx, params)
	})
	if err != nil {
		return Secret{}, pgstore.MapError(err, "secret")
	}
	s.invalidateResolved(locked.NamespaceID, locked.Path)
	s.record(ctx, p, locked.TenantID, locked.NamespaceID, AuditActionSecretUpdate, id, locked.Path, audit.ResultOK,
		map[string]any{
			"version":             int(newVersion),
			"value_changed":       in.Value != nil,
			"description_changed": in.Description != nil,
			"tags_changed":        in.SetTags,
			"expiry_changed":      in.ExpiresAt != nil || in.ClearExpiresAt,
		})
	row, err := s.q.VaultSecretGet(ctx, id)
	if err != nil {
		return Secret{}, pgstore.MapError(err, "secret")
	}
	return s.secretFromGetRow(row), nil
}

func validateUpdate(in *UpdateSecretInput) error {
	if in.ExpiresAt != nil && in.ClearExpiresAt {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "expires_at and clear_expires_at are mutually exclusive")
	}
	if in.Value != nil {
		if err := validateSecretValue(*in.Value); err != nil {
			return err
		}
	}
	if in.Description != nil {
		if err := validateDescription(*in.Description); err != nil {
			return err
		}
	}
	if in.SetTags {
		tags, err := normalizeTags(in.Tags, MaxSecretTags)
		if err != nil {
			return err
		}
		in.Tags = tags
	}
	return nil
}

// Delete removes a secret with all of its versions (secret:write).
func (s *SecretStore) Delete(ctx context.Context, p *authz.Principal, id string) error {
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	row, err := s.loadAuthorized(ctx, p, id, authz.PermSecretWrite)
	if err != nil {
		return err
	}
	n, err := s.q.VaultSecretDelete(ctx, id)
	if err != nil {
		return pgstore.MapError(err, "secret")
	}
	if n == 0 {
		return apperr.NotFound("secret not found")
	}
	s.invalidateResolved(row.NamespaceID, row.Path)
	s.record(ctx, p, row.TenantID, row.NamespaceID, AuditActionSecretDelete, id, row.Path, audit.ResultOK,
		map[string]any{"version": int(row.CurrentVersion)})
	return nil
}

// Get returns the metadata of a secret with its masked value (secret:list).
func (s *SecretStore) Get(ctx context.Context, p *authz.Principal, id string) (Secret, error) {
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	row, err := s.loadAuthorized(ctx, p, id, authz.PermSecretList)
	if err != nil {
		return Secret{}, err
	}
	return s.secretFromGetRow(row), nil
}

// Reveal decrypts a version (0 = current) of a secret (secret:reveal). Every
// attempt by a principal that can see the secret is audited.
func (s *SecretStore) Reveal(ctx context.Context, p *authz.Principal, id string, version int) (SecretValue, error) {
	if version < 0 || version > math.MaxInt32 {
		return SecretValue{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "version must be >= 0")
	}
	ctx, cancel := secretOpContext(ctx)
	defer cancel()
	row, err := s.loadAuthorized(ctx, p, id, authz.PermSecretList)
	if err != nil {
		return SecretValue{}, err
	}
	res := secretResource(row.TenantID, row.NamespaceID, row.NamespaceName, row.Path)
	if err := p.Require(authz.PermSecretReveal, res); err != nil {
		s.record(ctx, p, row.TenantID, row.NamespaceID, AuditActionSecretReveal, id, row.Path, audit.ResultDenied,
			map[string]any{"version": version})
		return SecretValue{}, err
	}

	env := envelope{version: row.CurrentVersion, ciphertext: row.Ciphertext, wrapped: row.WrappedDek}
	if row.KekID != nil {
		env.kekID = *row.KekID
	}
	if version != 0 && int32(version) != row.CurrentVersion {
		v, err := s.q.VaultSecretVersionGet(ctx, vaultdb.VaultSecretVersionGetParams{SecretID: id, Version: int32(version)})
		if errors.Is(err, pgx.ErrNoRows) {
			return SecretValue{}, apperr.NotFound("secret version %d not found", version)
		}
		if err != nil {
			return SecretValue{}, errorf(err, "load secret version")
		}
		env = envelope{version: v.Version, ciphertext: v.Ciphertext, wrapped: v.WrappedDek, kekID: v.KekID}
	}
	if len(env.ciphertext) == 0 {
		return SecretValue{}, apperr.NotFound("secret version %d not found", version)
	}
	value, err := s.openEnvelope(id, env)
	if err != nil {
		s.record(ctx, p, row.TenantID, row.NamespaceID, AuditActionSecretReveal, id, row.Path, audit.ResultError,
			map[string]any{"version": int(env.version)})
		return SecretValue{}, err
	}
	s.access.touch(id, s.now())
	s.record(ctx, p, row.TenantID, row.NamespaceID, AuditActionSecretReveal, id, row.Path, audit.ResultOK,
		map[string]any{"version": int(env.version)})
	return SecretValue{Path: row.Path, Version: int(env.version), Value: value, ExpiresAt: row.ExpiresAt}, nil
}

// envelope is one stored secret version.
type envelope struct {
	version    int32
	ciphertext []byte
	wrapped    []byte
	kekID      string
}

// openEnvelope decrypts a secret version. Failures are internal errors: they
// indicate a missing KEK or corrupted data, never a caller mistake.
func (s *SecretStore) openEnvelope(secretID string, env envelope) (string, error) {
	plaintext, err := s.cipher.Open(Sealed{Ciphertext: env.ciphertext, WrappedDEK: env.wrapped, KEKID: env.kekID},
		secretVersionAAD(secretID, int(env.version)))
	if err != nil {
		return "", apperr.Internal(fmt.Errorf("decrypt secret %s version %d: %w", secretID, env.version, err))
	}
	value := string(plaintext)
	clear(plaintext)
	return value, nil
}

// loadAuthorized loads a secret by ID and checks that the principal holds one
// of perms (see authorizeSecret).
func (s *SecretStore) loadAuthorized(ctx context.Context, p *authz.Principal, id string, perms ...authz.Permission) (vaultdb.VaultSecretGetRow, error) {
	if p == nil {
		return vaultdb.VaultSecretGetRow{}, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if id == "" {
		return vaultdb.VaultSecretGetRow{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "secret id is required")
	}
	row, err := s.q.VaultSecretGet(ctx, id)
	if err != nil {
		return vaultdb.VaultSecretGetRow{}, pgstore.MapError(err, "secret")
	}
	res := secretResource(row.TenantID, row.NamespaceID, row.NamespaceName, row.Path)
	if err := authorizeSecret(p, res, perms...); err != nil {
		return vaultdb.VaultSecretGetRow{}, err
	}
	return row, nil
}
