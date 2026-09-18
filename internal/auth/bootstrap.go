package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// bootstrapActor is recorded as the creator of bootstrap objects.
const bootstrapActor = "system:bootstrap"

// NamespaceInstaller installs the default policies of a new namespace inside
// the creating transaction (implemented by policysvc.Service). It has the same
// method set as tenancy.NamespaceInstaller.
type NamespaceInstaller interface {
	InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error
}

// BootstrapPlatformAdmin creates the first platform administrator together
// with a tenant, a namespace (with default policies) and a tenant-wide owner
// binding, all in one transaction. Existing tenants and namespaces with the
// given names are reused (defaults are only installed for a new namespace).
// It fails with failed_precondition when a platform administrator already
// exists. Used by `spnr admin init`.
func BootstrapPlatformAdmin(ctx context.Context, pool *pgxpool.Pool, username, password, tenantName, namespaceName string, installDefaults NamespaceInstaller) (string, error) {
	if pool == nil || installDefaults == nil {
		return "", errors.New("bootstrap platform admin: pool and namespace installer are required")
	}
	username = NormalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return "", err
	}
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if err := ValidateSlug("tenant name", tenantName); err != nil {
		return "", err
	}
	if err := ValidateSlug("namespace name", namespaceName); err != nil {
		return "", err
	}
	hashed, err := HashPassword(password)
	if err != nil {
		return "", apperr.Internal(err)
	}
	var userID string
	err = pgstore.InTx(ctx, pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := authdb.New(tx)
		if err := q.AuthBootstrapLock(ctx); err != nil {
			return err
		}
		exists, err := q.AuthPlatformAdminExists(ctx)
		if err != nil {
			return err
		}
		if exists {
			return apperr.FailedPrecondition("", "a platform administrator already exists")
		}
		tenantID, err := bootstrapTenant(ctx, q, tenantName)
		if err != nil {
			return err
		}
		if err := bootstrapNamespace(ctx, tx, q, tenantID, namespaceName, installDefaults); err != nil {
			return err
		}
		user, err := q.AuthUserInsert(ctx, authdb.AuthUserInsertParams{
			ID: newUserID(), Username: username, DisplayName: username, PasswordHash: hashed, IsPlatformAdmin: true,
		})
		if err != nil {
			return pgstore.MapError(err, "user "+strconvQuote(username))
		}
		if _, err := q.AuthBindingInsert(ctx, authdb.AuthBindingInsertParams{
			ID: idgen.New(idgen.RoleBinding), UserID: user.ID, TenantID: tenantID, Role: string(authz.RoleOwner),
			SiteIds: []string{}, ExtraPermissions: []string{}, CreatedBy: bootstrapActor,
		}); err != nil {
			return pgstore.MapError(err, "role binding")
		}
		userID = user.ID
		return nil
	})
	if err != nil {
		return "", internalUnlessApp(err)
	}
	return userID, nil
}

func bootstrapTenant(ctx context.Context, q *authdb.Queries, name string) (string, error) {
	t, err := q.AuthTenantByName(ctx, name)
	if err == nil {
		return t.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	t, err = q.AuthTenantInsert(ctx, authdb.AuthTenantInsertParams{ID: idgen.New(idgen.Tenant), Name: name, DisplayName: name})
	if err != nil {
		return "", pgstore.MapError(err, "tenant "+strconvQuote(name))
	}
	return t.ID, nil
}

func bootstrapNamespace(ctx context.Context, tx pgx.Tx, q *authdb.Queries, tenantID, name string, installer NamespaceInstaller) error {
	_, err := q.AuthNamespaceByName(ctx, authdb.AuthNamespaceByNameParams{TenantID: tenantID, Name: name})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	created, err := q.AuthNamespaceInsert(ctx, authdb.AuthNamespaceInsertParams{
		ID: idgen.New(idgen.Namespace), TenantID: tenantID, Name: name, DisplayName: name,
	})
	if err != nil {
		return pgstore.MapError(err, "namespace "+strconvQuote(name))
	}
	if err := installer.InstallNamespaceDefaults(ctx, tx, created.ID, bootstrapActor); err != nil {
		return fmt.Errorf("install namespace defaults: %w", err)
	}
	return nil
}
