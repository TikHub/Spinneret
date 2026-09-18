package tenancy

import (
	"context"
	"errors"

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

type nameCursor struct {
	Name string `json:"n"`
}

// ListTenants lists the tenants the principal can see, ordered by name:
// every tenant for platform administrators, the tenants with role bindings
// for users and the token's tenant for API tokens.
func (s *Service) ListTenants(ctx context.Context, p *authz.Principal, pageSize int32, pageToken string) ([]Tenant, string, int32, error) {
	if err := requirePrincipal(p); err != nil {
		return nil, "", 0, err
	}
	ids := p.TenantIDs()
	all := ids == nil
	var cur nameCursor
	if _, err := apiutil.DecodeCursor(pageToken, &cur); err != nil {
		return nil, "", 0, err
	}
	size := apiutil.PageSize(pageSize)
	rows, err := s.q.TenancyTenantList(ctx, tenancydb.TenancyTenantListParams{
		AllTenants: all, Ids: ids, AfterName: cur.Name, PageLimit: int32(size + 1),
	})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	total, err := s.q.TenancyTenantCount(ctx, tenancydb.TenancyTenantCountParams{AllTenants: all, Ids: ids})
	if err != nil {
		return nil, "", 0, apperr.Internal(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		if next, err = apiutil.EncodeCursor(nameCursor{Name: rows[len(rows)-1].Name}); err != nil {
			return nil, "", 0, apperr.Internal(err)
		}
	}
	out := make([]Tenant, len(rows))
	for i, r := range rows {
		out[i] = tenantFromModel(r)
	}
	return out, next, total, nil
}

// CreateTenantInput describes a new tenant.
type CreateTenantInput struct {
	Name        string
	DisplayName string
	Description string
	// OwnerUserID optionally receives a tenant-wide owner binding.
	OwnerUserID string
}

// CreateTenant creates a tenant (tenant:manage).
func (s *Service) CreateTenant(ctx context.Context, p *authz.Principal, in CreateTenantInput) (*Tenant, error) {
	if err := requirePrincipal(p); err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermTenantManage, authz.Resource{}); err != nil {
		return nil, err
	}
	if err := auth.ValidateSlug("name", in.Name); err != nil {
		return nil, err
	}
	if err := validateDescriptive(&in.DisplayName, &in.Description); err != nil {
		return nil, err
	}
	var created Tenant
	err := pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := tenancydb.New(tx)
		row, err := q.TenancyTenantInsert(ctx, tenancydb.TenancyTenantInsertParams{
			ID: idgen.New(idgen.Tenant), Name: in.Name, DisplayName: in.DisplayName, Description: in.Description,
		})
		if err != nil {
			return pgstore.MapError(err, "tenant "+quote(in.Name))
		}
		if in.OwnerUserID != "" {
			exists, err := q.TenancyUserExists(ctx, in.OwnerUserID)
			if err != nil {
				return err
			}
			if !exists {
				return apperr.NotFound("owner user not found")
			}
			if err := q.TenancyOwnerBindingInsert(ctx, tenancydb.TenancyOwnerBindingInsertParams{
				ID: idgen.New(idgen.RoleBinding), UserID: in.OwnerUserID, TenantID: row.ID, CreatedBy: p.Actor(),
			}); err != nil {
				return pgstore.MapError(err, "role binding")
			}
		}
		created = tenantFromModel(row)
		return nil
	})
	if err != nil {
		return nil, internalUnlessApp(err)
	}
	details := map[string]any{"name": created.Name}
	if in.OwnerUserID != "" {
		details["owner_user_id"] = in.OwnerUserID
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, created.ID, "", ActionTenantCreate, ResourceTenant, created.ID,
		created.Name, audit.ResultOK, details))
	return &created, nil
}

// UpdateTenant changes the descriptive fields of a tenant (tenant:manage).
func (s *Service) UpdateTenant(ctx context.Context, p *authz.Principal, id string, displayName, description *string) (*Tenant, error) {
	if err := requirePrincipal(p); err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermTenantManage, authz.Resource{}); err != nil {
		return nil, err
	}
	if err := validateDescriptive(displayName, description); err != nil {
		return nil, err
	}
	row, err := s.q.TenancyTenantUpdate(ctx, tenancydb.TenancyTenantUpdateParams{ID: id, DisplayName: displayName, Description: description})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("tenant not found")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	t := tenantFromModel(row)
	s.audit.Record(ctx, audit.FromPrincipal(p, t.ID, "", ActionTenantUpdate, ResourceTenant, t.ID, t.Name, audit.ResultOK, nil))
	return &t, nil
}

// DeleteTenant deletes a tenant that has no namespaces (tenant:manage). Its
// role bindings and notification channels are removed with it.
func (s *Service) DeleteTenant(ctx context.Context, p *authz.Principal, id string) error {
	if err := requirePrincipal(p); err != nil {
		return err
	}
	if err := p.Require(authz.PermTenantManage, authz.Resource{}); err != nil {
		return err
	}
	var deleted tenancydb.Tenant
	err := pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := tenancydb.New(tx)
		t, err := q.TenancyTenantLock(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("tenant not found")
		}
		if err != nil {
			return err
		}
		n, err := q.TenancyTenantNamespaceCount(ctx, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.FailedPrecondition("", "tenant %q still has %d namespace(s); delete them first", t.Name, n)
		}
		if _, err := q.TenancyTenantDelete(ctx, id); err != nil {
			return pgstore.MapError(err, "tenant")
		}
		deleted = t
		return nil
	})
	if err != nil {
		return internalUnlessApp(err)
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, deleted.ID, "", ActionTenantDelete, ResourceTenant, deleted.ID,
		deleted.Name, audit.ResultOK, nil))
	return nil
}
