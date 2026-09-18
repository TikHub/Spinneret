package sitesvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/sitesvc/sitesvcdb"
)

// SiteAccess restricts listings to the sites a caller may see. All grants
// every site of the namespace; otherwise only SiteIDs are visible.
type SiteAccess struct {
	All     bool
	SiteIDs []string
}

// SitePage is one page of sites ordered by name.
type SitePage struct {
	Sites []Site
	// Total counts every visible site of the namespace.
	Total int
	// NextAfterName is the name to continue after; "" when there are no more pages.
	NextAfterName string
}

// CreateSiteInput describes a new site. Empty Clients default to ["web"].
type CreateSiteInput struct {
	Name        string
	DisplayName string
	Description string
	Clients     []string
}

// UpdateSiteInput changes a site. Nil fields and an empty Clients list keep
// the current values; Clients otherwise is the complete new list.
type UpdateSiteInput struct {
	DisplayName *string
	Description *string
	Clients     []string
}

// SiteRef returns the reference of a site by ID (not_found when missing).
func (s *Service) SiteRef(ctx context.Context, siteID string) (SiteRef, error) {
	row, err := sitesvcdb.New(s.pool).SiteGetRef(ctx, siteID)
	if err != nil {
		return SiteRef{}, mapDBError(err, "site")
	}
	return SiteRef{ID: row.ID, Key: row.Hkey, Name: row.Name, NamespaceID: row.NamespaceID,
		NamespaceName: row.NamespaceName, TenantID: row.TenantID}, nil
}

// SiteRefByName returns the reference of a site by namespace and name.
func (s *Service) SiteRefByName(ctx context.Context, namespaceID, name string) (SiteRef, error) {
	row, err := sitesvcdb.New(s.pool).SiteGetRefByName(ctx, sitesvcdb.SiteGetRefByNameParams{NamespaceID: namespaceID, Name: name})
	if err != nil {
		return SiteRef{}, mapDBError(err, fmt.Sprintf("site %q", name))
	}
	return SiteRef{ID: row.ID, Key: row.Hkey, Name: row.Name, NamespaceID: row.NamespaceID,
		NamespaceName: row.NamespaceName, TenantID: row.TenantID}, nil
}

// GetSite returns a site with its counts.
func (s *Service) GetSite(ctx context.Context, siteID string) (Site, error) {
	row, err := sitesvcdb.New(s.pool).SiteGet(ctx, siteID)
	if err != nil {
		return Site{}, mapDBError(err, "site")
	}
	return siteFromRow(sitesvcdb.SiteListRow(row)), nil
}

// ListSites returns one page of the visible sites of a namespace, ordered by
// name, starting after afterName.
func (s *Service) ListSites(ctx context.Context, namespaceID string, access SiteAccess, pageSize int, afterName string) (SitePage, error) {
	if !access.All && len(access.SiteIDs) == 0 {
		return SitePage{Sites: []Site{}}, nil
	}
	limit := clampPageSize(pageSize)
	ids := access.SiteIDs
	if ids == nil {
		ids = []string{}
	}
	q := sitesvcdb.New(s.pool)
	rows, err := q.SiteList(ctx, sitesvcdb.SiteListParams{
		NamespaceID: namespaceID,
		AllSites:    access.All,
		SiteIds:     ids,
		AfterName:   afterName,
		MaxRows:     int32(limit + 1),
	})
	if err != nil {
		return SitePage{}, mapDBError(err, "list sites")
	}
	total, err := q.SiteCount(ctx, sitesvcdb.SiteCountParams{NamespaceID: namespaceID, AllSites: access.All, SiteIds: ids})
	if err != nil {
		return SitePage{}, mapDBError(err, "count sites")
	}
	page := SitePage{Sites: make([]Site, 0, min(len(rows), limit)), Total: int(total)}
	for i, row := range rows {
		if i == limit {
			page.NextAfterName = rows[limit-1].Name
			break
		}
		page.Sites = append(page.Sites, siteFromRow(row))
	}
	return page, nil
}

// CreateSite creates a site with a "_default" endpoint group per client.
func (s *Service) CreateSite(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in CreateSiteInput) (Site, error) {
	if ns == nil {
		return Site{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "namespace is required")
	}
	if len(in.Clients) == 0 {
		in.Clients = []string{DefaultClient}
	}
	if err := validateCreateSite(in); err != nil {
		return Site{}, err
	}
	id := idgen.New(idgen.Site)
	err := s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		if _, err := q.SiteInsert(ctx, sitesvcdb.SiteInsertParams{
			ID:          id,
			NamespaceID: ns.ID,
			Name:        in.Name,
			DisplayName: in.DisplayName,
			Description: in.Description,
			Clients:     in.Clients,
		}); err != nil {
			return mapDBError(err, fmt.Sprintf("site %q", in.Name))
		}
		return insertDefaultGroups(ctx, q, id, in.Clients)
	})
	if err != nil {
		return Site{}, mapDBError(err, "create site")
	}
	ref := SiteRef{ID: id, Name: in.Name, NamespaceID: ns.ID, NamespaceName: ns.Name, TenantID: ns.TenantID}
	s.record(ctx, p, ref, ActionSiteCreate, ResourceSite, id, in.Name, map[string]any{"clients": in.Clients})
	perr := s.propagate(ctx, change{tenantID: ns.TenantID, namespaceID: ns.ID, siteID: id, structural: true})
	created, err := s.GetSite(ctx, id)
	if err != nil {
		return Site{}, err
	}
	return created, perr
}

func validateCreateSite(in CreateSiteInput) error {
	if err := validateName("name", in.Name); err != nil {
		return err
	}
	if err := validateText("display_name", in.DisplayName, MaxDisplayNameLength); err != nil {
		return err
	}
	if err := validateText("description", in.Description, MaxDescriptionLength); err != nil {
		return err
	}
	return validateClients(in.Clients)
}

func insertDefaultGroups(ctx context.Context, q *sitesvcdb.Queries, siteID string, clients []string) error {
	if len(clients) == 0 {
		return nil
	}
	ids := make([]string, len(clients))
	for i := range clients {
		ids[i] = idgen.New(idgen.EndpointGroup)
	}
	if _, err := q.EndpointGroupInsertDefaults(ctx, sitesvcdb.EndpointGroupInsertDefaultsParams{
		Ids: ids, SiteID: siteID, Clients: clients,
	}); err != nil {
		return mapDBError(err, "default endpoint groups")
	}
	return nil
}

// UpdateSite changes a site. Added clients get a "_default" endpoint group;
// removed clients must not have identity types or identities, and their
// endpoint groups (with URI rules and bindings) are deleted.
func (s *Service) UpdateSite(ctx context.Context, p *authz.Principal, siteID string, in UpdateSiteInput) (Site, error) {
	if err := validateOptionalText("display_name", in.DisplayName, MaxDisplayNameLength); err != nil {
		return Site{}, err
	}
	if err := validateOptionalText("description", in.Description, MaxDescriptionLength); err != nil {
		return Site{}, err
	}
	if len(in.Clients) > 0 {
		if err := validateClients(in.Clients); err != nil {
			return Site{}, err
		}
	}
	ref, err := s.SiteRef(ctx, siteID)
	if err != nil {
		return Site{}, err
	}
	var added, removed []string
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		locked, err := q.SiteLock(ctx, siteID)
		if err != nil {
			return mapDBError(err, "site")
		}
		cur, err := q.SiteGet(ctx, siteID)
		if err != nil {
			return mapDBError(err, "site")
		}
		clients := locked.Clients
		if len(in.Clients) > 0 {
			clients = in.Clients
			added, removed = diffClients(locked.Clients, in.Clients)
		}
		if err := removeClients(ctx, q, siteID, removed); err != nil {
			return err
		}
		params := sitesvcdb.SiteUpdateParams{ID: siteID, DisplayName: cur.DisplayName, Description: cur.Description, Clients: clients}
		if in.DisplayName != nil {
			params.DisplayName = *in.DisplayName
		}
		if in.Description != nil {
			params.Description = *in.Description
		}
		if err := q.SiteUpdate(ctx, params); err != nil {
			return mapDBError(err, "site")
		}
		return insertDefaultGroups(ctx, q, siteID, added)
	})
	if err != nil {
		return Site{}, mapDBError(err, "update site")
	}
	details := map[string]any{}
	if in.DisplayName != nil {
		details["display_name"] = *in.DisplayName
	}
	if len(added) > 0 {
		details["added_clients"] = added
	}
	if len(removed) > 0 {
		details["removed_clients"] = removed
	}
	s.record(ctx, p, ref, ActionSiteUpdate, ResourceSite, ref.ID, ref.Name, details)
	perr := s.propagate(ctx, change{tenantID: ref.TenantID, namespaceID: ref.NamespaceID, siteID: siteID,
		structural: len(added) > 0 || len(removed) > 0})
	updated, err := s.GetSite(ctx, siteID)
	if err != nil {
		return Site{}, err
	}
	return updated, perr
}

// removeClients deletes the endpoint groups and client-level policy bindings
// of removed clients after checking that they have no identity types or
// identities.
func removeClients(ctx context.Context, q *sitesvcdb.Queries, siteID string, removed []string) error {
	if len(removed) == 0 {
		return nil
	}
	usage, err := q.SiteCountClientUsage(ctx, sitesvcdb.SiteCountClientUsageParams{SiteID: siteID, Clients: removed})
	if err != nil {
		return mapDBError(err, "client usage")
	}
	if usage.IdentityTypes > 0 || usage.Identities > 0 {
		return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
			"clients %v still have %d identity types and %d identities; delete them before removing the clients",
			removed, usage.IdentityTypes, usage.Identities)
	}
	if _, err := q.EndpointGroupDeleteByClients(ctx, sitesvcdb.EndpointGroupDeleteByClientsParams{SiteID: siteID, Clients: removed}); err != nil {
		return mapDBError(err, "endpoint groups of removed clients")
	}
	if _, err := q.SiteDeleteClientBindings(ctx, sitesvcdb.SiteDeleteClientBindingsParams{SiteID: siteID, Clients: removed}); err != nil {
		return mapDBError(err, "policy bindings of removed clients")
	}
	return nil
}

// DeleteSite deletes a site with its endpoint groups, URI rules, identity
// types, accounts and policy bindings. A site with identities is only deleted
// when force is true (failed_precondition otherwise); the identities are
// deleted with it. The site's hot state is removed afterwards.
func (s *Service) DeleteSite(ctx context.Context, p *authz.Principal, siteID string, force bool) error {
	ref, err := s.SiteRef(ctx, siteID)
	if err != nil {
		return err
	}
	var identities int32
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		if _, err := q.SiteLock(ctx, siteID); err != nil {
			return mapDBError(err, "site")
		}
		n, err := q.SiteCountIdentities(ctx, siteID)
		if err != nil {
			return mapDBError(err, "count identities")
		}
		identities = n
		if n > 0 && !force {
			return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
				"site %q still has %d identities; delete them first or use force", ref.Name, n)
		}
		if n > 0 {
			if _, err := q.SiteDeleteIdentities(ctx, siteID); err != nil {
				return mapDBError(err, "delete identities")
			}
		}
		if _, err := q.SiteDelete(ctx, siteID); err != nil {
			return mapDBError(err, "site")
		}
		return nil
	})
	if err != nil {
		return mapDBError(err, "delete site")
	}
	s.record(ctx, p, ref, ActionSiteDelete, ResourceSite, ref.ID, ref.Name,
		map[string]any{"force": force, "deleted_identities": identities})
	return s.propagate(ctx, change{tenantID: ref.TenantID, namespaceID: ref.NamespaceID, siteID: siteID,
		removedKey: ref.Key, structural: true})
}

func siteFromRow(row sitesvcdb.SiteListRow) Site {
	clients := slices.Clone(row.Clients)
	if clients == nil {
		clients = []string{}
	}
	return Site{
		SiteRef: SiteRef{
			ID:            row.ID,
			Key:           row.Hkey,
			Name:          row.Name,
			NamespaceID:   row.NamespaceID,
			NamespaceName: row.NamespaceName,
			TenantID:      row.TenantID,
		},
		DisplayName:        row.DisplayName,
		Description:        row.Description,
		Clients:            clients,
		Paused:             row.Paused,
		PausedReason:       row.PausedReason,
		PausedAt:           row.PausedAt,
		EndpointGroupCount: int(row.EndpointGroupCount),
		IdentityCount:      int(row.IdentityCount),
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}
