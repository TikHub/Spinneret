// Package tenancy manages tenants and namespaces (spec §3.1): CRUD with
// permission checks, deletion constraints, default policy installation for
// new namespaces and catalog invalidation.
//
// SQL lives in queries/*.sql and is compiled by a private sqlc configuration
// into the tenancydb package.
package tenancy

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/tenancy/tenancydb"
)

// Audit actions and resource kinds.
const (
	ActionTenantCreate    = "tenant.create"
	ActionTenantUpdate    = "tenant.update"
	ActionTenantDelete    = "tenant.delete"
	ActionNamespaceCreate = "namespace.create"
	ActionNamespaceUpdate = "namespace.update"
	ActionNamespaceDelete = "namespace.delete"

	ResourceTenant    = "tenant"
	ResourceNamespace = "namespace"
)

const (
	maxDisplayNameLength = 128
	maxDescriptionLength = 1024
	// maxNamespacesPerListing bounds the namespaces loaded for one listing.
	maxNamespacesPerListing = 10_000
	// catalogTimeout bounds catalog invalidation after a committed change.
	catalogTimeout = 10 * time.Second
)

// NamespaceInstaller installs the default policies of a new namespace inside
// the creating transaction (provided by policysvc).
type NamespaceInstaller interface {
	InstallNamespaceDefaults(ctx context.Context, tx pgx.Tx, namespaceID, actor string) error
}

// Tenant describes a tenant.
type Tenant struct {
	ID          string
	Name        string
	DisplayName string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Namespace describes a namespace.
type Namespace struct {
	ID          string
	TenantID    string
	Name        string
	DisplayName string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Service manages tenants and namespaces.
type Service struct {
	pool      *pgxpool.Pool
	q         *tenancydb.Queries
	cat       catalog.Catalog
	installer NamespaceInstaller
	audit     audit.Recorder
	logger    *slog.Logger
}

// NewService creates the tenancy service.
func NewService(pool *pgxpool.Pool, cat catalog.Catalog, installer NamespaceInstaller, rec audit.Recorder, logger *slog.Logger) *Service {
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		pool:      pool,
		q:         tenancydb.New(pool),
		cat:       cat,
		installer: installer,
		audit:     rec,
		logger:    logger.With(slog.String("component", "tenancy")),
	}
}

// BootstrapPlatformAdmin creates the first platform administrator, a tenant,
// a namespace with default policies and a tenant-wide owner binding in one
// transaction (see auth.BootstrapPlatformAdmin). Used by `spnr admin init`.
func BootstrapPlatformAdmin(ctx context.Context, pool *pgxpool.Pool, username, password, tenantName, namespaceName string, installDefaults NamespaceInstaller) (string, error) {
	return auth.BootstrapPlatformAdmin(ctx, pool, username, password, tenantName, namespaceName, installDefaults)
}

func tenantFromModel(t tenancydb.Tenant) Tenant {
	return Tenant{ID: t.ID, Name: t.Name, DisplayName: t.DisplayName, Description: t.Description, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func namespaceFromModel(n tenancydb.Namespace) Namespace {
	return Namespace{
		ID: n.ID, TenantID: n.TenantID, Name: n.Name, DisplayName: n.DisplayName, Description: n.Description,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
}

// superuser reports whether p passes every check.
func superuser(p *authz.Principal) bool {
	return p != nil && (p.Kind == authz.KindSystem || (p.Kind == authz.KindUser && p.IsPlatformAdmin))
}

func requirePrincipal(p *authz.Principal) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	return nil
}

// activeTenant returns the principal's active tenant.
func activeTenant(p *authz.Principal) (string, error) {
	if err := requirePrincipal(p); err != nil {
		return "", err
	}
	if p.TenantID == "" {
		return "", apperr.InvalidArgument("", "active tenant is required (%s header)", auth.HeaderTenant)
	}
	return p.TenantID, nil
}

// validateDescriptive checks optional display name and description updates.
func validateDescriptive(displayName, description *string) error {
	if displayName != nil {
		if err := validateText("display_name", *displayName, maxDisplayNameLength, false); err != nil {
			return err
		}
	}
	if description != nil {
		if err := validateText("description", *description, maxDescriptionLength, true); err != nil {
			return err
		}
	}
	return nil
}

func validateText(field, s string, maxLen int, multiline bool) error {
	if !utf8.ValidString(s) {
		return apperr.InvalidArgument("", "%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(s) > maxLen {
		return apperr.InvalidArgument("", "%s must be at most %d characters", field, maxLen)
	}
	for _, r := range s {
		allowedWhitespace := multiline && (r == '\n' || r == '\t')
		if unicode.IsControl(r) && !allowedWhitespace {
			return apperr.InvalidArgument("", "%s must not contain control characters", field)
		}
	}
	return nil
}

// invalidate reloads a namespace snapshot after a committed change. Failures
// are logged: the catalog also reloads periodically.
func (s *Service) invalidate(ctx context.Context, namespaceID string) {
	if s.cat == nil {
		return
	}
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogTimeout)
	defer cancel()
	if err := s.cat.Invalidate(ictx, namespaceID); err != nil {
		s.logger.Warn("catalog invalidation failed", slog.String("namespace_id", namespaceID), slog.Any("error", err))
	}
}

// internalUnlessApp keeps application and context errors and wraps the rest.
func internalUnlessApp(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := apperr.As(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return apperr.Internal(err)
}

func quote(s string) string { return strconv.Quote(s) }
