package identitysvc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// ListIdentities lists the identities matching q on the sites the principal
// may read (identity:read). Pages use keyset pagination on the order column
// and the ID. The score filter and order use the last hot-state snapshot of
// the global score (DefaultBaselineScore when there is none), so they are
// approximate by up to one snapshot interval.
func (s *Service) ListIdentities(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, q IdentityQuery) (IdentityPage, error) {
	if s.pool == nil {
		return IdentityPage{}, apperr.Internal(errNoPool)
	}
	sites, err := s.filterSites(p, ns, &q.Filter, authz.PermIdentityRead)
	if err != nil {
		return IdentityPage{}, err
	}
	orderBy := q.OrderBy
	if orderBy == "" {
		orderBy = OrderCreatedAt
	}
	if _, ok := orderExprs[orderBy]; !ok {
		return IdentityPage{}, invalid("order_by %q is not supported", truncateText(orderBy, 32))
	}
	var cur identityCursor
	hasCursor, err := decodeCursor(q.PageToken, &cur)
	if err != nil {
		return IdentityPage{}, err
	}
	if hasCursor && (cur.Order != orderBy || cur.Desc != q.Descending || cur.ID == "" ||
		(orderBy == OrderScore) != (cur.Score != nil) || (orderBy != OrderScore) != (cur.Time != nil)) {
		return IdentityPage{}, invalid("page_token does not match the requested order")
	}
	page := IdentityPage{Identities: []Identity{}}
	if len(sites) == 0 {
		return page, nil
	}
	ids := siteIDs(sites)
	limit := pageSize(q.PageSize)
	var after *identityCursor
	if hasCursor {
		after = &cur
	}
	sql, args := listIdentitiesSQL(q.Filter, ids, orderBy, q.Descending, after, limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return IdentityPage{}, fmt.Errorf("list identities: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanIdentityRow)
	if err != nil {
		return IdentityPage{}, fmt.Errorf("list identities: %w", err)
	}
	countSQL, countArgs := countIdentitiesSQL(q.Filter, ids)
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return IdentityPage{}, fmt.Errorf("count identities: %w", err)
	}
	page.Total = int(total)
	for i, row := range list {
		if i == limit {
			page.NextPageToken = encodeCursor(cursorFor(list[limit-1], orderBy, q.Descending))
			break
		}
		page.Identities = append(page.Identities, identityView(ns, ns.SitesByID[row.SiteID], row))
	}
	return page, nil
}

// filterSites validates f and returns the sites it covers that p may access
// with perm.
func (s *Service) filterSites(p *authz.Principal, ns *catalog.Namespace, f *IdentityFilter, perm authz.Permission) ([]*catalog.Site, error) {
	if err := validateFilter(f); err != nil {
		return nil, err
	}
	visible, err := visibleSites(p, ns, perm)
	if err != nil {
		return nil, err
	}
	return narrowSites(p, ns, visible, f.Site, perm)
}

// scanIdentityRow scans a row selected with identityColumns.
func scanIdentityRow(row pgx.CollectableRow) (identitysvcdb.IdentityGetRow, error) {
	var i identitysvcdb.IdentityGetRow
	err := row.Scan(
		&i.ID, &i.SiteID, &i.Client, &i.TypeID, &i.TypeName, &i.AccountID, &i.AccountRef,
		&i.State, &i.StateReason, &i.StateChangedAt, &i.BanUntil, &i.QuarantineUntil, &i.Region, &i.Tags, &i.Labels,
		&i.PayloadVersion, &i.ActivatedAt, &i.LastUsedAt, &i.GlobalScore, &i.GlobalSamples, &i.BoundProxyID,
		&i.CreatedAt, &i.UpdatedAt,
	)
	return i, err
}

// GetIdentity returns an identity with its payload and recent state events
// (identity:read). The payload is masked unless reveal is set and the
// principal holds identity:reveal on the site (audited as identity.reveal).
func (s *Service) GetIdentity(ctx context.Context, p *authz.Principal, id string, reveal bool) (IdentityDetail, error) {
	if s.pool == nil {
		return IdentityDetail{}, apperr.Internal(errNoPool)
	}
	q := identitysvcdb.New(s.pool)
	row, err := q.IdentityGet(ctx, id)
	if err != nil {
		return IdentityDetail{}, pgstore.MapError(err, "identity")
	}
	site, ns, err := s.siteByID(row.SiteID, "identity")
	if err != nil {
		return IdentityDetail{}, err
	}
	if err := requireByID(p, ns, site, "identity", authz.PermIdentityRead); err != nil {
		return IdentityDetail{}, err
	}
	detail := IdentityDetail{Identity: identityView(ns, site, row), Namespace: ns, Site: site}
	revealAllowed := reveal && p.Can(authz.PermIdentityReveal, siteResource(ns, site))
	detail.Payload, detail.Revealed = s.displayPayload(ctx, q, site, row, revealAllowed)
	if detail.Revealed {
		s.audit.Record(ctx, audit.FromPrincipal(p, ns.TenantID, ns.ID, "identity.reveal", "identity", id, "",
			audit.ResultOK, map[string]any{"site": site.Name, "payload_version": row.PayloadVersion}))
	}
	events, err := q.StateEventsRecentBySubject(ctx, identitysvcdb.StateEventsRecentBySubjectParams{
		NamespaceID: ns.ID, SubjectKind: "identity", SubjectID: id, MaxRows: RecentEventsLimit,
	})
	if err != nil {
		return IdentityDetail{}, fmt.Errorf("load recent state events of identity %s: %w", id, err)
	}
	detail.RecentEvents = make([]StateEvent, 0, len(events))
	for _, ev := range events {
		detail.RecentEvents = append(detail.RecentEvents, stateEventView(ns, ev))
	}
	return detail, nil
}

// displayPayload loads the current payload, masked unless reveal. Failures
// are logged and yield a nil payload (fail closed).
func (s *Service) displayPayload(ctx context.Context, q *identitysvcdb.Queries, site *catalog.Site,
	row identitysvcdb.IdentityGetRow, reveal bool) (map[string]any, bool) {
	if s.cipher == nil {
		s.logger.Error("identity payload unavailable: cipher is not configured", slog.String("identity_id", row.ID))
		return nil, false
	}
	ct, err := s.compiledType(ctx, q, site, row.TypeID)
	if err != nil {
		s.logger.Error("identity payload unavailable: identity type cannot be loaded",
			slog.String("identity_id", row.ID), slog.Any("error", err))
		return nil, false
	}
	payload, err := openPayload(ctx, q, s.cipher, row.ID, row.PayloadVersion)
	if err != nil {
		s.logger.Error("identity payload unavailable", slog.String("identity_id", row.ID),
			slog.Int("payload_version", int(row.PayloadVersion)), slog.Any("error", err))
		return nil, false
	}
	if reveal {
		return payload, true
	}
	return ct.Mask(payload), false
}

// ResolveIdentity locates an identity and checks perm on its site.
func (s *Service) ResolveIdentity(ctx context.Context, p *authz.Principal, id string, perm authz.Permission) (IdentityRef, error) {
	_, ref, err := s.locateIdentity(ctx, p, id, perm)
	return ref, err
}

// locateIdentity loads the location of an identity and checks perm on its site.
func (s *Service) locateIdentity(ctx context.Context, p *authz.Principal, id string, perm authz.Permission) (identitysvcdb.IdentityLocateRow, IdentityRef, error) {
	if s.pool == nil {
		return identitysvcdb.IdentityLocateRow{}, IdentityRef{}, apperr.Internal(errNoPool)
	}
	loc, err := identitysvcdb.New(s.pool).IdentityLocate(ctx, id)
	if err != nil {
		return loc, IdentityRef{}, pgstore.MapError(err, "identity")
	}
	site, ns, err := s.siteByID(loc.SiteID, "identity")
	if err != nil {
		return loc, IdentityRef{}, err
	}
	if err := requireByID(p, ns, site, "identity", perm); err != nil {
		return loc, IdentityRef{}, err
	}
	return loc, IdentityRef{ID: loc.ID, Namespace: ns, Site: site}, nil
}

// identityByID loads the current view of an identity.
func (s *Service) identityByID(ctx context.Context, ns *catalog.Namespace, site *catalog.Site, id string) (Identity, error) {
	row, err := identitysvcdb.New(s.pool).IdentityGet(ctx, id)
	if err != nil {
		return Identity{}, pgstore.MapError(err, "identity")
	}
	return identityView(ns, site, row), nil
}

// identityView converts a stored identity.
func identityView(ns *catalog.Namespace, site *catalog.Site, row identitysvcdb.IdentityGetRow) Identity {
	out := Identity{
		ID: row.ID, NamespaceName: ns.Name, SiteID: row.SiteID, Client: row.Client, TypeID: row.TypeID,
		TypeName: row.TypeName, AccountRef: row.AccountRef, State: row.State, StateReason: row.StateReason,
		StateChangedAt: row.StateChangedAt, BanUntil: row.BanUntil, QuarantineUntil: row.QuarantineUntil,
		Region: row.Region, Tags: row.Tags, Labels: decodeLabels(row.Labels), PayloadVersion: int(row.PayloadVersion),
		ActivatedAt: row.ActivatedAt, LastUsedAt: row.LastUsedAt, GlobalScore: row.GlobalScore,
		GlobalSamples: int(row.GlobalSamples), BoundProxyID: row.BoundProxyID, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.AccountID != nil {
		out.AccountID = *row.AccountID
	}
	if site != nil {
		out.SiteName = site.Name
	}
	return out
}
