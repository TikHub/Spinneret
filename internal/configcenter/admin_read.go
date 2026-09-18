package configcenter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/configcenter/configdb"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
)

// Pagination limits.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
	maxSearchLen    = 256
)

func pageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return n
	}
}

func encodeCursor(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(token string, v any) error {
	if token == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(b, v) != nil {
		return apperr.InvalidArgument("", "invalid page_token")
	}
	return nil
}

type itemCursor struct {
	Group string `json:"g"`
	Key   string `json:"k"`
}

// escapeLike escapes LIKE metacharacters (the queries use ESCAPE '\').
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListItems lists the config items of a namespace, ordered by group and key,
// that the principal can read. Token principals restricted to group globs
// only see matching groups. The first page also carries the virtual,
// read-only "_runtime" items (breakers, site_switches) when they match the
// filters and are readable, so it may hold up to two items more than the page
// size; Total counts them. List results omit contents and schemas (use
// GetItem).
func (s *Service) ListItems(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, opts ListOptions) (ItemPage, error) {
	if err := requireCaller(p, ns); err != nil {
		return ItemPage{}, err
	}
	if err := s.requireList(p, ns, opts.Group); err != nil {
		return ItemPage{}, err
	}
	if err := validateText("search", opts.Search, maxSearchLen); err != nil {
		return ItemPage{}, err
	}
	var cur itemCursor
	if err := decodeCursor(opts.PageToken, &cur); err != nil {
		return ItemPage{}, err
	}
	size := pageSize(opts.PageSize)
	ctx, cancel := s.opContext(ctx)
	defer cancel()

	virtual := s.virtualRuntimeItems(ctx, p, ns, opts.Group, opts.Search)
	page := ItemPage{Items: []Item{}, Total: len(virtual)}
	if opts.PageToken == "" {
		page.Items = append(page.Items, virtual...)
	}
	if reservedGroup(opts.Group) {
		return page, nil
	}

	q := s.queries()
	params := configdb.ConfigListItemsParams{NamespaceID: ns.ID, PageLimit: int32(size + 1)}
	count := configdb.ConfigCountItemsParams{NamespaceID: ns.ID}
	if opts.Group != "" {
		// requireList already checked config:read on this group.
		params.GroupName, count.GroupName = &opts.Group, &opts.Group
	} else {
		allowed, all, err := s.allowedGroups(ctx, q, p, ns)
		if err != nil {
			return ItemPage{}, err
		}
		if !all {
			params.AllowedGroups, count.AllowedGroups = allowed, allowed
		}
	}
	if opts.Search != "" {
		pattern := "%" + escapeLike(opts.Search) + "%"
		params.Search, count.Search = &pattern, &pattern
	}
	if opts.PageToken != "" {
		params.AfterGroup, params.AfterKey = &cur.Group, cur.Key
	}
	rows, err := q.ConfigListItems(ctx, params)
	if err != nil {
		return ItemPage{}, wrapErr(err, "list config items")
	}
	total, err := q.ConfigCountItems(ctx, count)
	if err != nil {
		return ItemPage{}, wrapErr(err, "count config items")
	}
	page.Total += int(total)
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		page.NextPageToken = encodeCursor(itemCursor{Group: last.GroupName, Key: last.Key})
	}
	for _, r := range rows {
		page.Items = append(page.Items, itemFromListRow(ns, r))
	}
	return page, nil
}

// requireList checks list permissions: config:read on the filtered group, or
// on at least some group when unfiltered.
func (s *Service) requireList(p *authz.Principal, ns *catalog.Namespace, group string) error {
	if group != "" {
		if !readableGroupPattern.MatchString(group) {
			return apperr.InvalidArgument("", "group must match %s", readableGroupPattern.String())
		}
		return p.Require(authz.PermConfigRead, configResource(ns, group))
	}
	if !holdsAnyConfigRead(p, ns) {
		return p.Require(authz.PermConfigRead, configResource(ns, ""))
	}
	return nil
}

// allowedGroups returns the groups the principal may read (all=true when
// unrestricted). At most maxListGroups groups are considered.
func (s *Service) allowedGroups(ctx context.Context, q *configdb.Queries, p *authz.Principal, ns *catalog.Namespace) ([]string, bool, error) {
	if p.Can(authz.PermConfigRead, configResource(ns, "")) {
		return nil, true, nil
	}
	groups, err := q.ConfigListGroups(ctx, configdb.ConfigListGroupsParams{NamespaceID: ns.ID, MaxGroups: maxListGroups})
	if err != nil {
		return nil, false, wrapErr(err, "list config groups")
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if p.Can(authz.PermConfigRead, configResource(ns, g)) {
			out = append(out, g)
		}
	}
	return out, false, nil
}

// GetItem returns a config item by ID with its draft and current published
// content (raw, secret references unresolved). It requires config:read.
func (s *Service) GetItem(ctx context.Context, p *authz.Principal, id string) (Item, error) {
	h, err := s.loadHeader(ctx, p, id)
	if err != nil {
		return Item{}, err
	}
	if err := p.Require(authz.PermConfigRead, configResource(h.NS, h.Group)); err != nil {
		return Item{}, err
	}
	return s.itemView(ctx, h.NS, h.ID)
}

// GetItemByLocator returns a config item by group and key. The "_runtime"
// items are returned as read-only items with their current content.
func (s *Service) GetItemByLocator(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, group, key string) (Item, error) {
	if err := requireCaller(p, ns); err != nil {
		return Item{}, err
	}
	if err := p.Require(authz.PermConfigRead, configResource(ns, group)); err != nil {
		return Item{}, err
	}
	if !wellFormedRef(group, key) {
		return Item{}, apperr.NotFound(itemNotFoundFormat)
	}
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	if group == RuntimeGroup {
		return s.runtimeItemInfo(ctx, ns, key)
	}
	if reservedGroup(group) {
		return Item{}, apperr.NotFound(itemNotFoundFormat)
	}
	id, err := s.queries().ConfigGetItemIDByLocator(ctx, configdb.ConfigGetItemIDByLocatorParams{
		NamespaceID: ns.ID, GroupName: group, Key: key,
	})
	if err != nil {
		return Item{}, pgstore.MapError(err, "config item")
	}
	return s.itemView(ctx, ns, id)
}

// itemView loads the full admin view of an item.
func (s *Service) itemView(ctx context.Context, ns *catalog.Namespace, id string) (Item, error) {
	ctx, cancel := s.opContext(ctx)
	defer cancel()
	row, err := s.queries().ConfigGetItemView(ctx, id)
	if err != nil {
		return Item{}, pgstore.MapError(err, "config item")
	}
	it := Item{
		ID:             row.ID,
		NamespaceID:    row.NamespaceID,
		NamespaceName:  ns.Name,
		Group:          row.GroupName,
		Key:            row.Key,
		Format:         row.Format,
		Schema:         row.Schema,
		Description:    row.Description,
		CurrentVersion: row.CurrentVersion,
		DraftContent:   row.DraftContent,
		HasDraft:       row.DraftContent != nil,
		DraftUpdatedBy: deref(row.DraftUpdatedBy),
		DraftUpdatedAt: row.DraftUpdatedAt,
		PublishedBy:    deref(row.PublishedBy),
		PublishedAt:    row.PublishedAt,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	it.PublishedContent = deref(row.PublishedContent)
	return it, nil
}

func itemFromListRow(ns *catalog.Namespace, r configdb.ConfigListItemsRow) Item {
	return Item{
		ID:             r.ID,
		NamespaceID:    ns.ID,
		NamespaceName:  ns.Name,
		Group:          r.GroupName,
		Key:            r.Key,
		Format:         r.Format,
		Description:    r.Description,
		CurrentVersion: r.CurrentVersion,
		HasDraft:       r.HasDraft,
		DraftUpdatedBy: deref(r.DraftUpdatedBy),
		DraftUpdatedAt: r.DraftUpdatedAt,
		PublishedBy:    deref(r.PublishedBy),
		PublishedAt:    r.PublishedAt,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
