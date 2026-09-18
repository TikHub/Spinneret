package dashboardapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/analytics"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
)

// ListRiskEvents implements DashboardServiceHandler.
func (h *Handler) ListRiskEvents(ctx context.Context, req *connect.Request[spinneretv1.ListRiskEventsRequest]) (*connect.Response[spinneretv1.ListRiskEventsResponse], error) {
	msg := req.Msg
	a, err := h.resolve(ctx, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	site, err := a.optionalSite(msg.GetSite())
	if err != nil {
		return nil, err
	}
	if err := a.requireGroup(msg.GetEndpointGroupId()); err != nil {
		return nil, err
	}
	rng, err := timeRange(msg.GetTimeRange())
	if err != nil {
		return nil, err
	}
	q := analytics.RiskEventQuery{
		SiteID:          siteID(site),
		EndpointGroupID: msg.GetEndpointGroupId(),
		Outcome:         msg.GetOutcome(),
		IdentityID:      msg.GetIdentityId(),
		ProxyID:         msg.GetProxyId(),
		Node:            msg.GetNode(),
		Range:           rng,
		PageSize:        int(msg.GetPageSize()),
	}
	var cur analytics.RiskCursor
	if ok, err := apiutil.DecodeCursor(msg.GetPageToken(), &cur); err != nil {
		return nil, err
	} else if ok {
		q.Cursor = &cur
	}
	page, err := h.svc.RiskEvents(ctx, a.scope, q)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListRiskEventsResponse{Events: make([]*spinneretv1.RiskEvent, len(page.Events))}
	for i, ev := range page.Events {
		resp.Events[i] = toProtoRiskEvent(a.ns.Name, ev)
	}
	if page.Next != nil {
		if resp.NextPageToken, err = encodeCursor(page.Next); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}

func toProtoRiskEvent(namespace string, ev analytics.RiskEvent) *spinneretv1.RiskEvent {
	return &spinneretv1.RiskEvent{
		Id:              ev.ID,
		CreatedAt:       apiutil.Timestamp(ev.CreatedAt),
		Namespace:       namespace,
		Site:            ev.Site,
		SiteId:          ev.SiteID,
		Client:          ev.Client,
		EndpointGroup:   ev.EndpointGroup,
		EndpointGroupId: ev.EndpointGroupID,
		IdentityId:      ev.IdentityID,
		ProxyId:         ev.ProxyID,
		LeaseId:         ev.LeaseID,
		ReportId:        ev.ReportID,
		Node:            ev.Node,
		TokenId:         ev.TokenID,
		Uri:             ev.URI,
		Method:          ev.Method,
		HttpStatus:      ev.HTTPStatus,
		BusinessCode:    ev.BusinessCode,
		ErrorKind:       ev.ErrorKind,
		Markers:         ev.Markers,
		Outcome:         ev.Outcome,
		Blame:           ev.Blame,
		Rule:            ev.Rule,
		LatencyMs:       ev.LatencyMs,
		ResponseBytes:   ev.ResponseBytes,
		StartedAt:       apiutil.TimestampPtr(ev.StartedAt),
		FinishedAt:      apiutil.TimestampPtr(ev.FinishedAt),
	}
}

// QueryRequestEvents implements DashboardServiceHandler.
func (h *Handler) QueryRequestEvents(ctx context.Context, req *connect.Request[spinneretv1.QueryRequestEventsRequest]) (*connect.Response[spinneretv1.QueryRequestEventsResponse], error) {
	msg := req.Msg
	a, err := h.resolve(ctx, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	site, err := a.optionalSite(msg.GetSite())
	if err != nil {
		return nil, err
	}
	if err := a.requireGroup(msg.GetEndpointGroupId()); err != nil {
		return nil, err
	}
	rng, err := timeRange(msg.GetTimeRange())
	if err != nil {
		return nil, err
	}
	q := analytics.RequestEventQuery{
		SiteID:          siteID(site),
		Client:          msg.GetClient(),
		EndpointGroupID: msg.GetEndpointGroupId(),
		Outcomes:        msg.GetOutcomes(),
		IdentityID:      msg.GetIdentityId(),
		ProxyID:         msg.GetProxyId(),
		Node:            msg.GetNode(),
		LeaseID:         msg.GetLeaseId(),
		ReportID:        msg.GetReportId(),
		MinLatencyMs:    int64(msg.GetMinLatencyMs()),
		Range:           rng,
		PageSize:        int(msg.GetPageSize()),
		IncludeSummary:  msg.GetIncludeSummary(),
	}
	if msg.HttpStatus != nil {
		status := int(msg.GetHttpStatus())
		q.HTTPStatus = &status
	}
	var cur analytics.RequestCursor
	if ok, err := apiutil.DecodeCursor(msg.GetPageToken(), &cur); err != nil {
		return nil, err
	} else if ok {
		q.Cursor = &cur
	}
	page, err := h.svc.RequestEvents(ctx, a.scope, q)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.QueryRequestEventsResponse{Events: make([]*spinneretv1.RequestEvent, len(page.Events))}
	for i, ev := range page.Events {
		resp.Events[i] = toProtoRequestEvent(a.ns.Name, ev)
	}
	if page.Next != nil {
		if resp.NextPageToken, err = encodeCursor(page.Next); err != nil {
			return nil, err
		}
	}
	if s := page.Summary; s != nil {
		resp.Summary = &spinneretv1.RequestEventsSummary{
			Total:        s.Total,
			Outcomes:     s.Outcomes,
			LatencyAvgMs: s.LatencyAvgMs,
			LatencyP50Ms: s.LatencyP50Ms,
			LatencyP95Ms: s.LatencyP95Ms,
			LatencyP99Ms: s.LatencyP99Ms,
		}
	}
	return connect.NewResponse(resp), nil
}

func toProtoRequestEvent(namespace string, ev analytics.RequestEvent) *spinneretv1.RequestEvent {
	return &spinneretv1.RequestEvent{
		EventTime:     eventTimestamp(ev.EventTime),
		ReceivedAt:    eventTimestamp(ev.ReceivedAt),
		StartedAt:     eventTimestamp(ev.StartedAt),
		Namespace:     namespace,
		Site:          ev.Site,
		SiteId:        ev.SiteID,
		Client:        ev.Client,
		EndpointGroup: ev.EndpointGroup,
		IdentityId:    ev.IdentityID,
		IdentityType:  ev.IdentityType,
		ProxyId:       ev.ProxyID,
		LeaseId:       ev.LeaseID,
		ReportId:      ev.ReportID,
		Node:          ev.Node,
		TokenId:       ev.TokenID,
		Uri:           ev.URI,
		Method:        ev.Method,
		HttpStatus:    ev.HTTPStatus,
		BusinessCode:  ev.BusinessCode,
		ErrorKind:     ev.ErrorKind,
		Markers:       ev.Markers,
		Outcome:       ev.Outcome,
		OutcomeHint:   ev.OutcomeHint,
		Blame:         ev.Blame,
		Rule:          ev.Rule,
		LatencyMs:     clampInt32(ev.LatencyMs),
		ResponseBytes: ev.ResponseBytes,
		Suppressed:    ev.Suppressed,
		Late:          ev.Late,
		Probe:         ev.Probe,
	}
}

// GetNodeStats implements DashboardServiceHandler.
func (h *Handler) GetNodeStats(ctx context.Context, req *connect.Request[spinneretv1.GetNodeStatsRequest]) (*connect.Response[spinneretv1.GetNodeStatsResponse], error) {
	a, err := h.resolve(ctx, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := a.requireNamespace(); err != nil {
		return nil, err
	}
	rng, err := timeRange(req.Msg.GetTimeRange())
	if err != nil {
		return nil, err
	}
	nodes, err := h.svc.NodeStats(ctx, a.ns.ID, rng)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.GetNodeStatsResponse{Nodes: make([]*spinneretv1.NodeStats, len(nodes))}
	for i, n := range nodes {
		resp.Nodes[i] = &spinneretv1.NodeStats{
			Node:            n.Node,
			Acquires:        n.Acquires,
			Reports:         n.Reports,
			Abandoned:       n.Abandoned,
			Rejected:        n.Rejected,
			UnreportedRatio: n.UnreportedRatio,
		}
	}
	return connect.NewResponse(resp), nil
}
