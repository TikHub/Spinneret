package tenancy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
	"github.com/TikHub/Spinneret/internal/tenancy/tenancydb"
)

func namespaceResource(n tenancydb.Namespace) authz.Resource {
	return authz.Resource{TenantID: n.TenantID, NamespaceID: n.ID, NamespaceName: n.Name}
}

// ListNamespaces lists the namespaces of the active tenant the principal can
// read (namespace:read), ordered by name.
func (s *Service) ListNamespaces(ctx context.Context, p *authz.Principal, pageSize int32, pageToken string) ([]Namespace, string, int32, error) {
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, "", 0, err
	}
	var cur nameCursor
	if _, err := apiutil.DecodeCursor(pageToken, &cur); err != nil {
		return nil, "", 0, err
	}
	rows, err := s.q.TenancyNamespacesOfTenant(ctx, tenancydb.TenancyNamespacesOfTenantParams{
		TenantID: tenantID, PageLimit: maxNamespacesPerListing,
	})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	visible := make([]Namespace, 0, len(rows))
	for _, r := range rows {
		if p.Can(authz.PermNamespaceRead, namespaceResource(r)) {
			visible = append(visible, namespaceFromModel(r))
		}
	}
	// The database orders by its collation, which may differ from byte order
	// (glibc locales ignore '-'); the cursor search below needs byte order.
	slices.SortFunc(visible, func(a, b Namespace) int { return strings.Compare(a.Name, b.Name) })
	total := int32(len(visible))
	start := sort.Search(len(visible), func(i int) bool { return visible[i].Name > cur.Name })
	size := apiutil.PageSize(pageSize)
	page := visible[start:]
	next := ""
	if len(page) > size {
		page = page[:size]
		if next, err = apiutil.EncodeCursor(nameCursor{Name: page[len(page)-1].Name}); err != nil {
			return nil, "", 0, apperr.Internal(err)
		}
	}
	return page, next, total, nil
}

// CreateNamespaceInput describes a new namespace in the active tenant.
type CreateNamespaceInput struct {
	Name        string
	DisplayName string
	Description string
}

// CreateNamespace creates a namespace in the active tenant (namespace:write on
// the tenant), installs the default policies in the same transaction and
// invalidates the catalog.
func (s *Service) CreateNamespace(ctx context.Context, p *authz.Principal, in CreateNamespaceInput) (*Namespace, error) {
	tenantID, err := activeTenant(p)
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermNamespaceWrite, authz.Resource{TenantID: tenantID}); err != nil {
		return nil, err
	}
	if err := auth.ValidateSlug("name", in.Name); err != nil {
		return nil, err
	}
	if err := validateDescriptive(&in.DisplayName, &in.Description); err != nil {
		return nil, err
	}
	if s.installer == nil {
		return nil, apperr.Internal(errors.New("tenancy: namespace installer is not configured"))
	}
	var created Namespace
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := tenancydb.New(tx)
		if _, err := q.TenancyTenantGet(ctx, tenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.NotFound("tenant not found")
			}
			return err
		}
		row, err := q.TenancyNamespaceInsert(ctx, tenancydb.TenancyNamespaceInsertParams{
			ID: idgen.New(idgen.Namespace), TenantID: tenantID, Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
		})
		if err != nil {
			return pgstore.MapError(err, "namespace "+quote(in.Name))
		}
		if err := s.installer.InstallNamespaceDefaults(ctx, tx, row.ID, p.Actor()); err != nil {
			return fmt.Errorf("install namespace defaults: %w", err)
		}
		created = namespaceFromModel(row)
		return nil
	})
	if err != nil {
		return nil, internalUnlessApp(err)
	}
	s.invalidate(ctx, created.ID)
	s.audit.Record(ctx, audit.FromPrincipal(p, created.TenantID, created.ID, ActionNamespaceCreate, ResourceNamespace,
		created.ID, created.Name, audit.ResultOK, nil))
	return &created, nil
}

// visibleNamespace loads a namespace of the principal's active tenant
// (platform administrators without an active tenant may address any).
func (s *Service) visibleNamespace(ctx context.Context, p *authz.Principal, id string) (tenancydb.Namespace, error) {
	if err := requirePrincipal(p); err != nil {
		return tenancydb.Namespace{}, err
	}
	row, err := s.q.TenancyNamespaceGet(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return tenancydb.Namespace{}, apperr.NotFound("namespace not found")
	}
	if err != nil {
		return tenancydb.Namespace{}, apperr.Internal(err)
	}
	if row.TenantID != p.TenantID && (!superuser(p) || p.TenantID != "") {
		return tenancydb.Namespace{}, apperr.NotFound("namespace not found")
	}
	return row, nil
}

// UpdateNamespace changes the descriptive fields of a namespace (namespace:write).
func (s *Service) UpdateNamespace(ctx context.Context, p *authz.Principal, id string, displayName, description *string) (*Namespace, error) {
	current, err := s.visibleNamespace(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermNamespaceWrite, namespaceResource(current)); err != nil {
		return nil, err
	}
	if err := validateDescriptive(displayName, description); err != nil {
		return nil, err
	}
	row, err := s.q.TenancyNamespaceUpdate(ctx, tenancydb.TenancyNamespaceUpdateParams{ID: id, DisplayName: displayName, Description: description})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("namespace not found")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.invalidate(ctx, row.ID)
	ns := namespaceFromModel(row)
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, ActionNamespaceUpdate, ResourceNamespace, ns.ID,
		ns.Name, audit.ResultOK, nil))
	return &ns, nil
}

// DeleteNamespace deletes a namespace that has no sites, proxies, config
// items, secrets or API tokens (namespace:write). Policies, bindings and
// notification channels of the namespace are removed with it.
func (s *Service) DeleteNamespace(ctx context.Context, p *authz.Principal, id string) error {
	current, err := s.visibleNamespace(ctx, p, id)
	if err != nil {
		return err
	}
	if err := p.Require(authz.PermNamespaceWrite, namespaceResource(current)); err != nil {
		return err
	}
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := tenancydb.New(tx)
		// The row lock blocks concurrent inserts referencing the namespace
		// (foreign key checks take KEY SHARE locks) until this transaction ends.
		if _, err := q.TenancyNamespaceLock(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.NotFound("namespace not found")
			}
			return err
		}
		usage, err := q.TenancyNamespaceUsage(ctx, id)
		if err != nil {
			return err
		}
		if remaining := usageSummary(usage); remaining != "" {
			return apperr.FailedPrecondition("", "namespace %q is not empty: %s", current.Name, remaining)
		}
		if _, err := q.TenancyNamespaceDelete(ctx, id); err != nil {
			return pgstore.MapError(err, "namespace")
		}
		return nil
	})
	if err != nil {
		return internalUnlessApp(err)
	}
	s.invalidate(ctx, id)
	s.audit.Record(ctx, audit.FromPrincipal(p, current.TenantID, current.ID, ActionNamespaceDelete, ResourceNamespace,
		current.ID, current.Name, audit.ResultOK, nil))
	return nil
}

// usageSummary describes the objects that block deletion ("" when none).
func usageSummary(u tenancydb.TenancyNamespaceUsageRow) string {
	var parts []string
	for _, c := range []struct {
		n    int32
		what string
	}{
		{u.Sites, "sites"}, {u.Proxies, "proxies"}, {u.ConfigItems, "config items"}, {u.Secrets, "secrets"}, {u.Tokens, "API tokens"},
	} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	return strings.Join(parts, ", ")
}
