package dashboardapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/analytics"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
)

// GetOverview implements DashboardServiceHandler.
func (h *Handler) GetOverview(ctx context.Context, req *connect.Request[spinneretv1.GetOverviewRequest]) (*connect.Response[spinneretv1.GetOverviewResponse], error) {
	a, err := h.resolve(ctx, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	window, err := analytics.ParseWindow(req.Msg.GetWindow())
	if err != nil {
		return nil, err
	}
	scope := a.scope
	if names := req.Msg.GetSites(); len(names) > 0 {
		scope = analytics.Scope{NamespaceID: a.ns.ID, SiteIDs: make([]string, 0, len(names))}
		seen := make(map[string]bool, len(names))
		for _, name := range names {
			site, err := a.site(name)
			if err != nil {
				return nil, err
			}
			if !seen[site.ID] {
				seen[site.ID] = true
				scope.SiteIDs = append(scope.SiteIDs, site.ID)
			}
		}
	}
	ov, err := h.svc.Overview(ctx, scope, window)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.GetOverviewResponse{
		Sites:          make([]*spinneretv1.SiteOverview, 0, len(ov.Sites)),
		Totals:         toProtoSiteOverview(ov.Totals),
		StreamsPending: clampInt32(ov.StreamsPending),
		Window:         analytics.WindowName(ov.Window),
		GeneratedAt:    apiutil.Timestamp(ov.GeneratedAt),
	}
	for _, s := range ov.Sites {
		resp.Sites = append(resp.Sites, toProtoSiteOverview(s))
	}
	return connect.NewResponse(resp), nil
}

func toProtoSiteOverview(s analytics.SiteOverview) *spinneretv1.SiteOverview {
	out := &spinneretv1.SiteOverview{
		Site:                 s.Site,
		SiteId:               s.SiteID,
		DisplayName:          s.DisplayName,
		Paused:               s.Paused,
		IdentitiesByState:    counts(s.IdentitiesByState),
		AvailableIdentities:  clampInt32(s.AvailableIdentities),
		ProxiesByState:       counts(s.ProxiesByState),
		AcquireQps:           s.AcquireQPS,
		ReportQps:            s.ReportQPS,
		SuccessRatio:         s.SuccessRatio,
		RiskRatio:            s.RiskRatio,
		UnknownRatio:         s.UnknownRatio,
		ClientErrorRatio:     s.ClientErrorRatio,
		AcquireFailureRatio:  s.AcquireFailureRatio,
		OpenBreakers:         clampInt32(int64(s.OpenBreakers)),
		HalfOpenBreakers:     clampInt32(int64(s.HalfOpenBreakers)),
		LowWatermarkWarnings: make([]*spinneretv1.LowWatermarkWarning, 0, len(s.LowWatermarkWarnings)),
	}
	for _, w := range s.LowWatermarkWarnings {
		out.LowWatermarkWarnings = append(out.LowWatermarkWarnings, &spinneretv1.LowWatermarkWarning{
			Client:          w.Client,
			EndpointGroup:   w.EndpointGroup,
			EndpointGroupId: w.EndpointGroupID,
			Available:       clampInt32(w.Available),
			LowWatermark:    clampInt32(int64(w.LowWatermark)),
		})
	}
	return out
}
