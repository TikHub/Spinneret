package identitysvc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
)

// MaxEventActions bounds the action filter of ListStateEvents.
const MaxEventActions = 32

// stateEventColumns are the columns scanned into
// identitysvcdb.StateEventsRecentBySubjectRow.
const stateEventColumns = `id, created_at, site_id, subject_kind, subject_id, endpoint_group_id, from_state, to_state,
       action, scope, until, permanent, outcome, policy_id, policy_version, rule, report_id, lease_id, actor, reason, shadow`

// eventCursor is the keyset position of ListStateEvents.
type eventCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

// ListStateEvents lists state events of the namespace, newest first
// (identity:read). Principals restricted to some sites only see events of
// those sites; events without a site (namespace-level subjects such as
// proxies) require unrestricted identity:read. Proxy events additionally
// require proxy:read on the namespace: they are omitted for other principals,
// and requesting subject_kind "proxy" explicitly is permission_denied.
func (s *Service) ListStateEvents(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, q StateEventQuery) (StateEventPage, error) {
	if s.pool == nil {
		return StateEventPage{}, apperr.Internal(errNoPool)
	}
	if err := validateEventQuery(q); err != nil {
		return StateEventPage{}, err
	}
	visible, err := visibleSites(p, ns, authz.PermIdentityRead)
	if err != nil {
		return StateEventPage{}, err
	}
	sites, err := narrowSites(p, ns, visible, q.Site, authz.PermIdentityRead)
	if err != nil {
		return StateEventPage{}, err
	}
	unrestricted, _ := p.SiteFilter(ns.TenantID, ns.ID, authz.PermIdentityRead)
	proxyVisible := p.Can(authz.PermProxyRead, namespaceResource(ns))
	if q.SubjectKind == "proxy" && !proxyVisible {
		return StateEventPage{}, p.Require(authz.PermProxyRead, namespaceResource(ns))
	}
	var cur eventCursor
	hasCursor, err := decodeCursor(q.PageToken, &cur)
	if err != nil {
		return StateEventPage{}, err
	}
	var args sqlArgs
	conds := []string{"namespace_id = " + args.add(ns.ID)}
	switch {
	case q.Site != "":
		if len(sites) != 1 {
			return StateEventPage{Events: []StateEvent{}}, nil
		}
		conds = append(conds, "site_id = "+args.add(sites[0].ID))
	case !unrestricted:
		conds = append(conds, "site_id = ANY("+args.add(siteIDs(sites))+"::text[])")
	}
	switch {
	case q.SubjectKind != "":
		conds = append(conds, "subject_kind = "+args.add(q.SubjectKind))
	case !proxyVisible:
		conds = append(conds, "subject_kind <> 'proxy'")
	}
	if q.SubjectID != "" {
		conds = append(conds, "subject_id = "+args.add(q.SubjectID))
	}
	if len(q.Actions) > 0 {
		conds = append(conds, "action = ANY("+args.add(q.Actions)+"::text[])")
	}
	if q.Shadow != nil {
		conds = append(conds, "shadow = "+args.add(*q.Shadow))
	}
	if !q.From.IsZero() {
		conds = append(conds, "created_at >= "+args.add(q.From))
	}
	if !q.To.IsZero() {
		conds = append(conds, "created_at < "+args.add(q.To))
	}
	if hasCursor {
		conds = append(conds, "(created_at, id) < ("+args.add(cur.CreatedAt)+", "+args.add(cur.ID)+")")
	}
	limit := pageSize(q.PageSize)
	sql := "SELECT " + stateEventColumns + " FROM state_events WHERE " + strings.Join(conds, " AND ") +
		" ORDER BY created_at DESC, id DESC LIMIT " + args.add(limit+1)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return StateEventPage{}, fmt.Errorf("list state events: %w", err)
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[identitysvcdb.StateEventsRecentBySubjectRow])
	if err != nil {
		return StateEventPage{}, fmt.Errorf("list state events: %w", err)
	}
	page := StateEventPage{Events: make([]StateEvent, 0, min(len(list), limit))}
	for i, row := range list {
		if i == limit {
			last := list[limit-1]
			page.NextPageToken = encodeCursor(eventCursor{CreatedAt: last.CreatedAt.UTC(), ID: last.ID})
			break
		}
		page.Events = append(page.Events, stateEventView(ns, row))
	}
	return page, nil
}

// validateEventQuery checks the filters of ListStateEvents.
func validateEventQuery(q StateEventQuery) error {
	if !slices.Contains(subjectKinds, q.SubjectKind) {
		return invalid("subject_kind %q must be one of identity, account, proxy, endpoint_group, site", truncateText(q.SubjectKind, 32))
	}
	if len(q.SubjectID) > 64 {
		return invalid("subject_id must be at most 64 bytes")
	}
	if len(q.Actions) > MaxEventActions {
		return invalid("at most %d actions are allowed", MaxEventActions)
	}
	for _, a := range q.Actions {
		if !actionPattern.MatchString(a) {
			return invalid("action %q must match %s", truncateText(a, 32), actionPattern)
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		return invalid("time_range.start must be before time_range.end")
	}
	return nil
}

// stateEventView converts a stored state event.
func stateEventView(ns *catalog.Namespace, row identitysvcdb.StateEventsRecentBySubjectRow) StateEvent {
	out := StateEvent{
		ID: row.ID, CreatedAt: row.CreatedAt, NamespaceName: ns.Name, SiteID: row.SiteID, SubjectKind: row.SubjectKind,
		SubjectID: row.SubjectID, EndpointGroupID: row.EndpointGroupID, FromState: row.FromState, ToState: row.ToState,
		Action: row.Action, Scope: row.Scope, Until: row.Until, Permanent: row.Permanent, Outcome: row.Outcome,
		PolicyID: row.PolicyID, PolicyVersion: int(row.PolicyVersion), Rule: row.Rule, ReportID: row.ReportID,
		LeaseID: row.LeaseID, Actor: row.Actor, Reason: row.Reason, Shadow: row.Shadow,
	}
	if site, ok := ns.SitesByID[row.SiteID]; ok {
		out.SiteName = site.Name
		if g, ok := site.GroupsByID[row.EndpointGroupID]; ok {
			out.EndpointGroupName = g.Name
		}
	}
	return out
}

// stateEventData is the Data of identity.state console events (the shape
// shared with the action track).
type stateEventData struct {
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	SiteID      string     `json:"site_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Action      string     `json:"action"`
	Until       *time.Time `json:"until"`
	Reason      string     `json:"reason"`
}

// publishTransitions publishes identity.state events for payload-driven state
// changes, at most MaxStateEventsPublished per call. Failures are logged.
func (s *Service) publishTransitions(ctx context.Context, ns *catalog.Namespace, siteID string, ts []transition) {
	if s.bus == nil || len(ts) == 0 {
		return
	}
	cctx, cancel := detached(ctx)
	defer cancel()
	channel := events.NamespaceChannel(ns.ID)
	for i, t := range ts {
		if i == MaxStateEventsPublished {
			s.logger.Debug("identity.state events truncated", slog.Int("transitions", len(ts)))
			return
		}
		data, err := json.Marshal(stateEventData{
			SubjectKind: "identity", SubjectID: t.IdentityID, SiteID: siteID, From: t.From, To: t.To,
			Action: ActionPayloadUpdate, Reason: payloadUpdatedReason,
		})
		if err != nil {
			s.logger.Error("encode identity.state event", slog.Any("error", err))
			return
		}
		ev := events.Event{
			Type: events.TypeIdentityState, TenantID: ns.TenantID, NamespaceID: ns.ID, SiteID: siteID,
			At: s.now().UTC(), Data: data,
		}
		if err := s.bus.Publish(cctx, channel, ev); err != nil {
			s.logger.Warn("publish identity.state event failed", slog.String("identity_id", t.IdentityID), slog.Any("error", err))
		}
	}
}
