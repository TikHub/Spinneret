package dashboardapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/analytics"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// Heatmap metrics.
const (
	heatmapMetricCooldown = "cooldown"
	heatmapMetricScore    = "score"
)

// heatmapCursor is the page token of GetHeatmap.
type heatmapCursor struct {
	After string `json:"a"`
}

// GetTimeSeries implements DashboardServiceHandler.
func (h *Handler) GetTimeSeries(ctx context.Context, req *connect.Request[spinneretv1.GetTimeSeriesRequest]) (*connect.Response[spinneretv1.GetTimeSeriesResponse], error) {
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
	step, err := analytics.ParseStep(msg.GetStep())
	if err != nil {
		return nil, err
	}
	rng, err := timeRange(msg.GetTimeRange())
	if err != nil {
		return nil, err
	}
	ts, err := h.svc.TimeSeries(ctx, a.scope, analytics.TimeSeriesQuery{
		Metric:          msg.GetMetric(),
		SiteID:          siteID(site),
		Client:          msg.GetClient(),
		EndpointGroupID: msg.GetEndpointGroupId(),
		Range:           rng,
		Step:            step,
	})
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.GetTimeSeriesResponse{
		Metric: ts.Metric,
		Step:   analytics.StepName(ts.Step),
		Series: make([]*spinneretv1.Series, 0, len(ts.Series)),
	}
	for _, s := range ts.Series {
		series := &spinneretv1.Series{Name: s.Name, Labels: s.Labels, Points: make([]*spinneretv1.Point, len(s.Points))}
		for i, p := range s.Points {
			series.Points[i] = &spinneretv1.Point{Ts: timestamppb.New(p.TS), Value: p.Value}
		}
		resp.Series = append(resp.Series, series)
	}
	return connect.NewResponse(resp), nil
}

// GetHeatmap implements DashboardServiceHandler.
func (h *Handler) GetHeatmap(ctx context.Context, req *connect.Request[spinneretv1.GetHeatmapRequest]) (*connect.Response[spinneretv1.GetHeatmapResponse], error) {
	msg := req.Msg
	a, err := h.resolve(ctx, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	site, err := a.site(msg.GetSite())
	if err != nil {
		return nil, err
	}
	metric := msg.GetMetric()
	if metric != heatmapMetricCooldown && metric != heatmapMetricScore {
		return nil, apperr.InvalidArgument("", "metric must be cooldown or score")
	}
	var cur heatmapCursor
	if _, err := apiutil.DecodeCursor(msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	hm, err := h.svc.Heatmap(ctx, a.scope, analytics.HeatmapQuery{
		SiteID:  site.ID,
		Client:  msg.GetClient(),
		States:  msg.GetStates(),
		Limit:   int(msg.GetLimit()),
		AfterID: cur.After,
	})
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.GetHeatmapResponse{
		Metric:      metric,
		Columns:     make([]string, len(hm.Columns)),
		ColumnIds:   make([]string, len(hm.Columns)),
		Rows:        make([]*spinneretv1.HeatmapRow, len(hm.Rows)),
		Cells:       make([]*spinneretv1.HeatmapCell, len(hm.Cells)),
		Total:       clampInt32(hm.Total),
		GeneratedAt: apiutil.Timestamp(hm.GeneratedAt),
	}
	for i, c := range hm.Columns {
		resp.Columns[i], resp.ColumnIds[i] = c.EndpointGroup, c.EndpointGroupID
	}
	for i, r := range hm.Rows {
		resp.Rows[i] = &spinneretv1.HeatmapRow{IdentityId: r.IdentityID, Label: r.Label, State: r.State}
	}
	for i, c := range hm.Cells {
		resp.Cells[i] = &spinneretv1.HeatmapCell{
			Row:                 clampInt32(int64(c.Row)),
			Col:                 clampInt32(int64(c.Col)),
			Score:               c.Score,
			CooldownRemainingMs: c.CooldownRemainingMs,
			Available:           c.Available,
		}
	}
	if hm.NextAfterID != "" {
		if resp.NextPageToken, err = encodeCursor(heatmapCursor{After: hm.NextAfterID}); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(resp), nil
}
