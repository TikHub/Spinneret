package sitesvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/sitesvc/sitesvcdb"
)

// GroupCursor is the position after which a group listing continues.
type GroupCursor struct {
	Client string `json:"c"`
	Name   string `json:"n"`
}

// GroupPage is one page of endpoint groups ordered by client, then name.
type GroupPage struct {
	Groups []EndpointGroup
	// Total counts the endpoint groups matching the filter.
	Total int
	// Next is the cursor of the next page; nil when there are no more pages.
	Next *GroupCursor
}

// CreateGroupInput describes a new endpoint group.
type CreateGroupInput struct {
	Client       string
	Name         string
	Description  string
	LowWatermark int
	// Rules are the initial URI rules in evaluation order (positions are
	// assigned from list order).
	Rules []URIRule
}

// UpdateGroupInput changes an endpoint group; nil fields keep their value.
type UpdateGroupInput struct {
	Description  *string
	LowWatermark *int
}

// GroupRef returns the reference of an endpoint group (not_found when missing).
func (s *Service) GroupRef(ctx context.Context, groupID string) (GroupRef, error) {
	row, err := sitesvcdb.New(s.pool).EndpointGroupGet(ctx, groupID)
	if err != nil {
		return GroupRef{}, mapDBError(err, "endpoint group")
	}
	return groupRefFromRow(row), nil
}

// GetEndpointGroup returns an endpoint group with its rules and hot-state figures.
func (s *Service) GetEndpointGroup(ctx context.Context, groupID string) (EndpointGroup, error) {
	q := sitesvcdb.New(s.pool)
	row, err := q.EndpointGroupGet(ctx, groupID)
	if err != nil {
		return EndpointGroup{}, mapDBError(err, "endpoint group")
	}
	g := EndpointGroup{
		GroupRef:     groupRefFromRow(row),
		Description:  row.Description,
		LowWatermark: int(row.LowWatermark),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
	groups := []EndpointGroup{g}
	if err := s.attachRules(ctx, q, groups); err != nil {
		return EndpointGroup{}, err
	}
	if err := s.attachHotState(ctx, g.Site.Key, groups); err != nil {
		return EndpointGroup{}, err
	}
	return groups[0], nil
}

// ListEndpointGroups returns one page of the endpoint groups of a site,
// optionally of one client, with their rules and hot-state figures.
func (s *Service) ListEndpointGroups(ctx context.Context, ref SiteRef, client string, pageSize int, after GroupCursor) (GroupPage, error) {
	limit := clampPageSize(pageSize)
	q := sitesvcdb.New(s.pool)
	rows, err := q.EndpointGroupList(ctx, sitesvcdb.EndpointGroupListParams{
		SiteID:      ref.ID,
		Client:      client,
		AfterClient: after.Client,
		AfterName:   after.Name,
		MaxRows:     int32(limit + 1),
	})
	if err != nil {
		return GroupPage{}, mapDBError(err, "list endpoint groups")
	}
	total, err := q.EndpointGroupCount(ctx, sitesvcdb.EndpointGroupCountParams{SiteID: ref.ID, Client: client})
	if err != nil {
		return GroupPage{}, mapDBError(err, "count endpoint groups")
	}
	page := GroupPage{Groups: make([]EndpointGroup, 0, min(len(rows), limit)), Total: int(total)}
	for i, row := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = &GroupCursor{Client: last.Client, Name: last.Name}
			break
		}
		page.Groups = append(page.Groups, EndpointGroup{
			GroupRef:     GroupRef{ID: row.ID, Key: row.Hkey, Name: row.Name, Client: row.Client, Site: ref},
			Description:  row.Description,
			LowWatermark: int(row.LowWatermark),
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	if err := s.attachRules(ctx, q, page.Groups); err != nil {
		return GroupPage{}, err
	}
	if err := s.attachHotState(ctx, ref.Key, page.Groups); err != nil {
		return GroupPage{}, err
	}
	return page, nil
}

// attachRules loads the URI rules of the groups in one query.
func (s *Service) attachRules(ctx context.Context, q *sitesvcdb.Queries, groups []EndpointGroup) error {
	if len(groups) == 0 {
		return nil
	}
	ids := make([]string, len(groups))
	index := make(map[string]int, len(groups))
	for i := range groups {
		ids[i] = groups[i].ID
		index[groups[i].ID] = i
		groups[i].Rules = []URIRule{}
	}
	rows, err := q.URIRuleListByGroups(ctx, ids)
	if err != nil {
		return mapDBError(err, "list uri rules")
	}
	for _, r := range rows {
		i := index[r.EndpointGroupID]
		groups[i].Rules = append(groups[i].Rules, URIRule{
			ID: r.ID, Kind: site.RuleKind(r.Kind), Pattern: r.Pattern, Position: int(r.Position),
		})
	}
	return nil
}

// CreateEndpointGroup creates an endpoint group of a site client with
// optional initial URI rules.
func (s *Service) CreateEndpointGroup(ctx context.Context, p *authz.Principal, siteID string, in CreateGroupInput) (EndpointGroup, error) {
	if err := validateCreateGroup(in); err != nil {
		return EndpointGroup{}, err
	}
	ref, err := s.SiteRef(ctx, siteID)
	if err != nil {
		return EndpointGroup{}, err
	}
	id := idgen.New(idgen.EndpointGroup)
	var ruleCount int
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		locked, err := q.SiteLock(ctx, siteID)
		if err != nil {
			return mapDBError(err, "site")
		}
		if !slices.Contains(locked.Clients, in.Client) {
			return apperr.InvalidArgument(apperr.ReasonClientUnknown, "client %q is not a client of site %q", in.Client, locked.Name)
		}
		if _, err := q.EndpointGroupInsert(ctx, sitesvcdb.EndpointGroupInsertParams{
			ID:           id,
			SiteID:       siteID,
			Client:       in.Client,
			Name:         in.Name,
			Description:  in.Description,
			LowWatermark: int32(in.LowWatermark),
		}); err != nil {
			return mapDBError(err, fmt.Sprintf("endpoint group %q", in.Name))
		}
		stored, err := writeRules(ctx, q, siteID, in.Client, id, in.Name, in.Rules)
		ruleCount = len(stored)
		return err
	})
	if err != nil {
		return EndpointGroup{}, mapDBError(err, "create endpoint group")
	}
	s.record(ctx, p, ref, ActionEndpointGroupCreate, ResourceEndpointGroup, id, in.Name,
		map[string]any{"site": ref.Name, "client": in.Client, "rules": ruleCount, "low_watermark": in.LowWatermark})
	perr := s.propagate(ctx, change{tenantID: ref.TenantID, namespaceID: ref.NamespaceID, siteID: siteID, structural: true})
	created, err := s.GetEndpointGroup(ctx, id)
	if err != nil {
		return EndpointGroup{}, err
	}
	return created, perr
}

func validateCreateGroup(in CreateGroupInput) error {
	if err := validateName("name", in.Name); err != nil {
		return err
	}
	if in.Name == site.DefaultGroup {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "the endpoint group name %q is reserved", site.DefaultGroup)
	}
	if !clientPattern.MatchString(in.Client) {
		return apperr.InvalidArgument(apperr.ReasonClientUnknown, "client must match %s", clientPattern.String())
	}
	if err := validateText("description", in.Description, MaxDescriptionLength); err != nil {
		return err
	}
	if err := validateLowWatermark(in.LowWatermark); err != nil {
		return err
	}
	return validateRuleList(in.Rules)
}

// UpdateEndpointGroup changes the description and low watermark of an
// endpoint group ("_default" groups included).
func (s *Service) UpdateEndpointGroup(ctx context.Context, p *authz.Principal, groupID string, in UpdateGroupInput) (EndpointGroup, error) {
	if err := validateOptionalText("description", in.Description, MaxDescriptionLength); err != nil {
		return EndpointGroup{}, err
	}
	if in.LowWatermark != nil {
		if err := validateLowWatermark(*in.LowWatermark); err != nil {
			return EndpointGroup{}, err
		}
	}
	ref, err := s.GroupRef(ctx, groupID)
	if err != nil {
		return EndpointGroup{}, err
	}
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		if _, err := q.SiteLock(ctx, ref.Site.ID); err != nil {
			return mapDBError(err, "site")
		}
		cur, err := q.EndpointGroupGet(ctx, groupID)
		if err != nil {
			return mapDBError(err, "endpoint group")
		}
		params := sitesvcdb.EndpointGroupUpdateParams{ID: groupID, Description: cur.Description, LowWatermark: cur.LowWatermark}
		if in.Description != nil {
			params.Description = *in.Description
		}
		if in.LowWatermark != nil {
			params.LowWatermark = int32(*in.LowWatermark)
		}
		return mapDBError(q.EndpointGroupUpdate(ctx, params), "endpoint group")
	})
	if err != nil {
		return EndpointGroup{}, mapDBError(err, "update endpoint group")
	}
	details := map[string]any{"site": ref.Site.Name, "client": ref.Client}
	if in.LowWatermark != nil {
		details["low_watermark"] = *in.LowWatermark
	}
	s.record(ctx, p, ref.Site, ActionEndpointGroupUpdate, ResourceEndpointGroup, ref.ID, ref.Name, details)
	perr := s.propagate(ctx, change{tenantID: ref.Site.TenantID, namespaceID: ref.Site.NamespaceID, siteID: ref.Site.ID})
	updated, err := s.GetEndpointGroup(ctx, groupID)
	if err != nil {
		return EndpointGroup{}, err
	}
	return updated, perr
}

// DeleteEndpointGroup deletes an endpoint group with its URI rules and policy
// bindings. "_default" groups cannot be deleted.
func (s *Service) DeleteEndpointGroup(ctx context.Context, p *authz.Principal, groupID string) error {
	ref, err := s.GroupRef(ctx, groupID)
	if err != nil {
		return err
	}
	if ref.Name == site.DefaultGroup {
		return apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "the %q endpoint group cannot be deleted", site.DefaultGroup)
	}
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		if _, err := q.SiteLock(ctx, ref.Site.ID); err != nil {
			return mapDBError(err, "site")
		}
		n, err := q.EndpointGroupDelete(ctx, groupID)
		if err != nil {
			return mapDBError(err, "endpoint group")
		}
		if n == 0 {
			return apperr.NotFound("endpoint group not found")
		}
		return nil
	})
	if err != nil {
		return mapDBError(err, "delete endpoint group")
	}
	s.record(ctx, p, ref.Site, ActionEndpointGroupDelete, ResourceEndpointGroup, ref.ID, ref.Name,
		map[string]any{"site": ref.Site.Name, "client": ref.Client})
	return s.propagate(ctx, change{tenantID: ref.Site.TenantID, namespaceID: ref.Site.NamespaceID, siteID: ref.Site.ID,
		structural: true})
}

func groupRefFromRow(row sitesvcdb.EndpointGroupGetRow) GroupRef {
	return GroupRef{
		ID:     row.ID,
		Key:    row.Hkey,
		Name:   row.Name,
		Client: row.Client,
		Site: SiteRef{
			ID:            row.SiteID,
			Key:           row.SiteHkey,
			Name:          row.SiteName,
			NamespaceID:   row.NamespaceID,
			NamespaceName: row.NamespaceName,
			TenantID:      row.TenantID,
		},
	}
}
