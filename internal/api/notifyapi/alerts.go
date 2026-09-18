package notifyapi

import (
	"context"
	"time"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/notify"
)

// alertCursor is the page token of ListAlertEvents.
type alertCursor struct {
	At time.Time `json:"t"`
	ID string    `json:"i"`
}

// ListAlertEvents lists alert events of the active tenant the caller may
// read, newest first. Callers with notify:read on a namespace see all of its
// alerts; callers restricted to sites see the alerts of those sites.
func (h *Handler) ListAlertEvents(ctx context.Context, req *connect.Request[spinneretv1.ListAlertEventsRequest]) (*connect.Response[spinneretv1.ListAlertEventsResponse], error) {
	p, tenantID, err := principalTenant(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	query := notify.AlertQuery{
		TenantID: tenantID,
		Kind:     msg.GetKind(),
		Severity: msg.GetSeverity(),
		Limit:    apiutil.PageSize(msg.GetPageSize()),
	}
	if tr := msg.GetTimeRange(); tr != nil {
		query.From = apiutil.Time(tr.GetStart())
		query.To = apiutil.Time(tr.GetEnd())
	}
	if err := h.alertVisibility(ctx, p, tenantID, msg, &query); err != nil {
		return nil, err
	}
	var cursor alertCursor
	ok, err := apiutil.DecodeCursor(msg.GetPageToken(), &cursor)
	if err != nil {
		return nil, err
	}
	if ok {
		if cursor.ID == "" || cursor.At.IsZero() {
			return nil, apperr.InvalidArgument("", "invalid page_token")
		}
		query.AfterAt, query.AfterID = &cursor.At, cursor.ID
	}
	page, err := h.svc.ListAlertEvents(ctx, query)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListAlertEventsResponse{Events: make([]*spinneretv1.AlertEvent, 0, len(page.Events))}
	for _, ev := range page.Events {
		pe, err := h.alertProto(ev)
		if err != nil {
			return nil, err
		}
		resp.Events = append(resp.Events, pe)
	}
	if page.More && len(page.Events) > 0 {
		last := page.Events[len(page.Events)-1]
		token, err := apiutil.EncodeCursor(alertCursor{At: last.CreatedAt, ID: last.ID})
		if err != nil {
			return nil, apperr.Internal(err)
		}
		resp.NextPageToken = token
	}
	return connect.NewResponse(resp), nil
}

// alertVisibility fills the visibility and namespace/site filters of query.
func (h *Handler) alertVisibility(ctx context.Context, p *authz.Principal, tenantID string,
	msg *spinneretv1.ListAlertEventsRequest, query *notify.AlertQuery,
) error {
	if msg.GetNamespace() == "" {
		if msg.GetSite() != "" {
			return apperr.InvalidArgument("", "site requires a namespace")
		}
		query.IncludeTenant = p.Can(authz.PermNotifyRead, tenantResource(tenantID))
		for _, ns := range h.cat.Namespaces(tenantID) {
			h.addNamespaceVisibility(p, ns, query)
		}
		if !query.IncludeTenant && len(query.NamespaceIDs) == 0 && len(query.SiteIDs) == 0 {
			return p.Require(authz.PermNotifyRead, tenantResource(tenantID))
		}
		return nil
	}
	_, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return err
	}
	query.NamespaceID = ns.ID
	if msg.GetSite() != "" {
		site, err := apiutil.Site(ns, msg.GetSite())
		if err != nil {
			return err
		}
		if err := p.Require(authz.PermNotifyRead, apiutil.SiteResource(ns, site)); err != nil {
			return err
		}
		query.SiteID = site.ID
		query.SiteIDs = []string{site.ID}
		return nil
	}
	h.addNamespaceVisibility(p, ns, query)
	if len(query.NamespaceIDs) == 0 && len(query.SiteIDs) == 0 {
		return p.Require(authz.PermNotifyRead, apiutil.NamespaceResource(ns))
	}
	return nil
}

// addNamespaceVisibility adds a namespace (namespace-wide access) or the
// accessible sites of it to the query.
func (h *Handler) addNamespaceVisibility(p *authz.Principal, ns *catalog.Namespace, query *notify.AlertQuery) {
	if p.Can(authz.PermNotifyRead, apiutil.NamespaceResource(ns)) {
		query.NamespaceIDs = append(query.NamespaceIDs, ns.ID)
		return
	}
	all, siteIDs := p.SiteFilter(ns.TenantID, ns.ID, authz.PermNotifyRead)
	if all {
		query.NamespaceIDs = append(query.NamespaceIDs, ns.ID)
		return
	}
	for _, id := range siteIDs {
		if _, ok := ns.SitesByID[id]; ok {
			query.SiteIDs = append(query.SiteIDs, id)
		}
	}
}

// alertProto converts an alert event, resolving namespace and site names.
func (h *Handler) alertProto(ev notify.AlertEvent) (*spinneretv1.AlertEvent, error) {
	details, err := apiutil.Struct(ev.Details)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.AlertEvent{
		Id:         ev.ID,
		CreatedAt:  apiutil.Timestamp(ev.CreatedAt),
		Kind:       ev.Kind,
		Severity:   ev.Severity,
		Title:      ev.Title,
		Message:    ev.Message,
		Details:    details,
		Deliveries: make([]*spinneretv1.AlertDelivery, 0, len(ev.Deliveries)),
	}
	if ns, ok := h.cat.Namespace(ev.NamespaceID); ok && ev.NamespaceID != "" {
		out.Namespace = ns.Name
		if site, ok := ns.SitesByID[ev.SiteID]; ok && ev.SiteID != "" {
			out.Site = site.Name
		}
	}
	for _, d := range ev.Deliveries {
		out.Deliveries = append(out.Deliveries, deliveryProto(d))
	}
	return out, nil
}
