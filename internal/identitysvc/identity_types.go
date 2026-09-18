package identitysvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/store/postgres/db"
)

// typeCursor is the pagination cursor of ListIdentityTypes.
type typeCursor struct {
	After string `json:"a"`
}

// ListIdentityTypes lists the identity types of the sites the principal may
// read (site:read or identity:read).
func (s *Service) ListIdentityTypes(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, q TypeQuery) (TypePage, error) {
	if s.pool == nil {
		return TypePage{}, apperr.Internal(errNoPool)
	}
	visible, err := visibleSites(p, ns, authz.PermSiteRead, authz.PermIdentityRead)
	if err != nil {
		return TypePage{}, err
	}
	sites, err := narrowSites(p, ns, visible, q.Site, authz.PermIdentityRead)
	if err != nil {
		return TypePage{}, err
	}
	if err := validateText("client", q.Client, 64); err != nil {
		return TypePage{}, err
	}
	if err := validateText("search", q.Search, 256); err != nil {
		return TypePage{}, err
	}
	var cur typeCursor
	if _, err := decodeCursor(q.PageToken, &cur); err != nil {
		return TypePage{}, err
	}
	if len(sites) == 0 {
		return TypePage{Types: []IdentityType{}}, nil
	}
	limit := pageSize(q.PageSize)
	queries := identitysvcdb.New(s.pool)
	ids := siteIDs(sites)
	rows, err := queries.IdentityTypeList(ctx, identitysvcdb.IdentityTypeListParams{
		SiteIds: ids, Client: q.Client, Search: q.Search, AfterID: cur.After, MaxRows: int32(limit + 1),
	})
	if err != nil {
		return TypePage{}, fmt.Errorf("list identity types: %w", err)
	}
	total, err := queries.IdentityTypeCount(ctx, identitysvcdb.IdentityTypeCountParams{
		SiteIds: ids, Client: q.Client, Search: q.Search,
	})
	if err != nil {
		return TypePage{}, fmt.Errorf("count identity types: %w", err)
	}
	page := TypePage{Types: make([]IdentityType, 0, min(len(rows), limit)), Total: int(total)}
	for i, row := range rows {
		if i == limit {
			page.NextPageToken = encodeCursor(typeCursor{After: rows[limit-1].ID})
			break
		}
		page.Types = append(page.Types, s.typeView(ns, ns.SitesByID[row.SiteID], identitysvcdb.IdentityTypeGetRow(row)))
	}
	return page, nil
}

// GetIdentityType returns one identity type (site:read or identity:read).
func (s *Service) GetIdentityType(ctx context.Context, p *authz.Principal, id string) (IdentityType, error) {
	row, site, ns, err := s.loadType(ctx, p, id, authz.PermSiteRead, authz.PermIdentityRead)
	if err != nil {
		return IdentityType{}, err
	}
	return s.typeView(ns, site, row), nil
}

// loadType loads an identity type and checks that p holds one of perms on its site.
func (s *Service) loadType(ctx context.Context, p *authz.Principal, id string, perms ...authz.Permission) (identitysvcdb.IdentityTypeGetRow, *catalog.Site, *catalog.Namespace, error) {
	if s.pool == nil {
		return identitysvcdb.IdentityTypeGetRow{}, nil, nil, apperr.Internal(errNoPool)
	}
	row, err := identitysvcdb.New(s.pool).IdentityTypeGet(ctx, id)
	if err != nil {
		return row, nil, nil, pgstore.MapError(err, "identity type")
	}
	site, ns, err := s.siteByID(row.SiteID, "identity type")
	if err != nil {
		return row, nil, nil, err
	}
	if err := requireByID(p, ns, site, "identity type", perms...); err != nil {
		return row, nil, nil, err
	}
	return row, site, ns, nil
}

// CreateIdentityType creates an identity type from its YAML spec (site:write).
// The site named by the request overrides the site of the spec.
func (s *Service) CreateIdentityType(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, siteName, specYAML string) (IdentityType, error) {
	if s.pool == nil {
		return IdentityType{}, apperr.Internal(errNoPool)
	}
	site, err := siteByName(ns, siteName)
	if err != nil {
		return IdentityType{}, err
	}
	if err := requireSite(p, authz.PermSiteWrite, ns, site); err != nil {
		return IdentityType{}, err
	}
	spec, yamlOut, err := parseTypeYAMLForSite([]byte(specYAML), site.Name)
	if err != nil {
		return IdentityType{}, err
	}
	if !site.HasClient(spec.Client) {
		return IdentityType{}, apperr.InvalidArgument(apperr.ReasonClientUnknown,
			"client %q is not a client of site %q", spec.Client, site.Name)
	}
	id := idgen.New(idgen.IdentityType)
	ct, err := identity.Compile(id, site.ID, 1, spec)
	if err != nil {
		return IdentityType{}, err
	}
	specJSON, err := identity.MarshalTypeJSON(ct.Spec)
	if err != nil {
		return IdentityType{}, apperr.Internal(err)
	}
	row, err := identitysvcdb.New(s.pool).IdentityTypeInsert(ctx, identitysvcdb.IdentityTypeInsertParams{
		ID: id, SiteID: site.ID, Client: ct.Client, Name: ct.Name, Description: ct.Spec.Description,
		Spec: specJSON, SpecYaml: string(yamlOut), JsonSchema: ct.SchemaJSON(),
	})
	if err != nil {
		return IdentityType{}, pgstore.MapError(err, fmt.Sprintf("identity type %q", ct.Name))
	}
	s.invalidateCatalog(ctx, ns.ID)
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity_type.create", "identity_type", id, ct.Name,
		audit.ResultOK, map[string]any{"site": site.Name, "client": ct.Client, "version": 1}))
	return s.typeView(ns, site, identitysvcdb.IdentityTypeGetRow{
		ID: id, SiteID: site.ID, Client: ct.Client, Name: ct.Name, Description: ct.Spec.Description,
		Spec: specJSON, SpecYaml: string(yamlOut), JsonSchema: ct.SchemaJSON(), Version: 1,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}), nil
}

// UpdateIdentityType replaces the spec of an identity type and bumps its
// version (site:write). The name, client and site cannot change. When the
// effective unique_by changes, the unique hashes of the existing identities
// are recomputed in the same transaction (which decrypts every current
// payload of the type); the update fails with failed_precondition when an
// identity has no value for the new unique_by or two identities would share a
// key.
func (s *Service) UpdateIdentityType(ctx context.Context, p *authz.Principal, id, specYAML string) (IdentityType, error) {
	current, site, ns, err := s.loadType(ctx, p, id, authz.PermSiteWrite)
	if err != nil {
		return IdentityType{}, err
	}
	if len(specYAML) > MaxSpecYAMLBytes {
		return IdentityType{}, invalid("spec_yaml must be at most %d bytes", MaxSpecYAMLBytes)
	}
	src := []byte(specYAML)
	if name := yamlSiteName(src); name != "" && name != site.Name {
		return IdentityType{}, invalid("the site of identity type %q cannot be changed (spec names %q)", current.Name, truncateText(name, 64))
	}
	spec, yamlOut, err := parseTypeYAMLForSite(src, site.Name)
	if err != nil {
		return IdentityType{}, err
	}
	switch {
	case spec.Name != current.Name:
		return IdentityType{}, invalid("the name of identity type %q cannot be changed", current.Name)
	case spec.Client != current.Client:
		return IdentityType{}, invalid("the client of identity type %q cannot be changed", current.Name)
	}
	var (
		version  int32
		rehashed int64
	)
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := identitysvcdb.New(tx)
		locked, err := q.IdentityTypeLockSpec(ctx, id)
		if err != nil {
			return pgstore.MapError(err, "identity type")
		}
		ct, err := identity.Compile(id, site.ID, int(locked.Version)+1, spec)
		if err != nil {
			return err
		}
		if uniqueByChanged(locked.Spec, ct.Spec.UniqueBy) {
			if rehashed, err = s.rehashIdentities(ctx, q, ct); err != nil {
				return err
			}
		}
		specJSON, err := identity.MarshalTypeJSON(ct.Spec)
		if err != nil {
			return apperr.Internal(err)
		}
		updated, err := q.IdentityTypeUpdate(ctx, identitysvcdb.IdentityTypeUpdateParams{
			ID: id, Description: ct.Spec.Description, Spec: specJSON, SpecYaml: string(yamlOut), JsonSchema: ct.SchemaJSON(),
		})
		if err != nil {
			return pgstore.MapError(err, "identity type")
		}
		version = updated.Version
		return nil
	})
	if err != nil {
		return IdentityType{}, err
	}
	s.invalidateCatalog(ctx, ns.ID)
	details := map[string]any{"site": site.Name, "version": version}
	if rehashed > 0 {
		details["rehashed_identities"] = rehashed
	}
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity_type.update", "identity_type", id, current.Name,
		audit.ResultOK, details))
	row, err := identitysvcdb.New(s.pool).IdentityTypeGet(ctx, id)
	if err != nil {
		return IdentityType{}, pgstore.MapError(err, "identity type")
	}
	return s.typeView(ns, site, row), nil
}

// DeleteIdentityType deletes an identity type without identities (site:write).
func (s *Service) DeleteIdentityType(ctx context.Context, p *authz.Principal, id string) error {
	current, site, ns, err := s.loadType(ctx, p, id, authz.PermSiteWrite)
	if err != nil {
		return err
	}
	err = pgstore.InTx(ctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := identitysvcdb.New(tx)
		if _, err := q.IdentityTypeLock(ctx, id); err != nil {
			return pgstore.MapError(err, "identity type")
		}
		n, err := q.IdentityTypeCountIdentities(ctx, id)
		if err != nil {
			return fmt.Errorf("count identities of type %s: %w", id, err)
		}
		if n > 0 {
			return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
				"identity type %q still has %d identities (including retired ones)", current.Name, n)
		}
		if _, err := q.IdentityTypeDelete(ctx, id); err != nil {
			return pgstore.MapError(err, fmt.Sprintf("identity type %q", current.Name))
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.invalidateCatalog(ctx, ns.ID)
	s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity_type.delete", "identity_type", id, current.Name,
		audit.ResultOK, map[string]any{"site": site.Name, "version": current.Version}))
	return nil
}

// typeView converts a stored identity type.
func (s *Service) typeView(ns *catalog.Namespace, site *catalog.Site, row identitysvcdb.IdentityTypeGetRow) IdentityType {
	out := IdentityType{
		ID: row.ID, NamespaceName: ns.Name, SiteID: row.SiteID, Client: row.Client, Name: row.Name,
		Description: row.Description, SpecYAML: row.SpecYaml, JSONSchema: row.JsonSchema, Version: int(row.Version),
		IdentityCount: int(row.IdentityCount), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if site != nil {
		out.SiteName = site.Name
	}
	var spec identity.TypeSpec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		s.logger.Error("stored identity type spec cannot be decoded", slog.String("identity_type_id", row.ID), slog.Any("error", err))
	} else {
		out.Spec = spec.WithDefaults()
	}
	return out
}

// compiledType returns identity type typeID of site from the catalog, or
// compiles it from PostgreSQL when the snapshot does not (yet) contain it.
func (s *Service) compiledType(ctx context.Context, q *identitysvcdb.Queries, site *catalog.Site, typeID string) (*identity.CompiledType, error) {
	if ct, ok := site.IdentityTypesByID[typeID]; ok {
		return ct, nil
	}
	row, err := q.IdentityTypeSpecByID(ctx, typeID)
	if err != nil {
		return nil, pgstore.MapError(err, "identity type")
	}
	if row.SiteID != site.ID {
		return nil, apperr.NotFound("identity type not found")
	}
	return compileStored(row.ID, row.SiteID, row.Spec, row.Version)
}

// currentType returns ct when it still has the stored version of its
// identity type and otherwise compiles the stored spec, so payload writes do
// not normalize and deduplicate with a catalog snapshot that has not reloaded
// yet. The stored version is checked again under the type advisory lock
// (checkTypeVersion) to catch updates that race with the write.
func currentType(ctx context.Context, q *identitysvcdb.Queries, ct *identity.CompiledType) (*identity.CompiledType, error) {
	row, err := q.IdentityTypeSpecByID(ctx, ct.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("identity type %q not found", ct.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("load identity type %s: %w", ct.ID, err)
	}
	if int(row.Version) == ct.Version && row.SiteID == ct.SiteID {
		return ct, nil
	}
	return compileStored(row.ID, row.SiteID, row.Spec, row.Version)
}

// compiledTypeByName returns identity type name of site from the catalog, or
// compiles it from PostgreSQL.
func (s *Service) compiledTypeByName(ctx context.Context, q *identitysvcdb.Queries, site *catalog.Site, name string) (*identity.CompiledType, error) {
	if ct, ok := site.IdentityTypes[name]; ok {
		return ct, nil
	}
	row, err := q.IdentityTypeSpecByName(ctx, identitysvcdb.IdentityTypeSpecByNameParams{SiteID: site.ID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.NotFound("identity type %q not found on site %q", truncateText(name, 64), site.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("load identity type %q: %w", name, err)
	}
	return compileStored(row.ID, row.SiteID, row.Spec, row.Version)
}

func compileStored(id, siteID string, specJSON []byte, version int32) (*identity.CompiledType, error) {
	spec, err := identity.ParseTypeJSON(specJSON)
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("decode stored identity type %s: %w", id, err))
	}
	ct, err := identity.Compile(id, siteID, int(version), spec)
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("compile stored identity type %s: %w", id, err))
	}
	return ct, nil
}
