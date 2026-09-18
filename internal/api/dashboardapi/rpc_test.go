package dashboardapi

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
)

func TestDashboardFullAccess(t *testing.T) {
	e := newEnv(t)
	ctx := as(user(authz.RoleViewer))

	ov, err := e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Equal(t, "5m", ov.Msg.GetWindow())
	require.Zero(t, ov.Msg.GetStreamsPending())
	require.True(t, e.now.Equal(ov.Msg.GetGeneratedAt().AsTime()))
	require.Len(t, ov.Msg.GetSites(), 2)
	alpha := ov.Msg.GetSites()[0]
	require.Equal(t, "alpha", alpha.GetSite())
	require.Equal(t, siteA, alpha.GetSiteId())
	require.Equal(t, int32(1), alpha.GetAvailableIdentities())
	require.Equal(t, map[string]int32{"active": 1}, alpha.GetIdentitiesByState())
	require.InDelta(t, 60.0/300, alpha.GetAcquireQps(), 1e-9)
	require.InDelta(t, 30.0/300, alpha.GetReportQps(), 1e-9)
	require.InDelta(t, 1.0, alpha.GetSuccessRatio(), 1e-9)
	require.NotNil(t, alpha.GetProxiesByState())
	require.Empty(t, alpha.GetLowWatermarkWarnings())
	require.Equal(t, map[string]int32{"active": 2}, ov.Msg.GetTotals().GetIdentitiesByState())
	require.InDelta(t, 120.0/300, ov.Msg.GetTotals().GetAcquireQps(), 1e-9)

	ov, err = e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{
		Namespace: "prod", Sites: []string{"beta", "beta"}, Window: "15m",
	}))
	require.NoError(t, err)
	require.Equal(t, "15m", ov.Msg.GetWindow())
	require.Len(t, ov.Msg.GetSites(), 1)
	require.Equal(t, "beta", ov.Msg.GetSites()[0].GetSite())

	rng := &spinneretv1.TimeRange{Start: timestamppb.New(e.bucket), End: timestamppb.New(e.bucket.Add(time.Minute))}
	ts, err := e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{
		Namespace: "prod", Metric: "acquire_rate", TimeRange: rng, Step: "1m",
	}))
	require.NoError(t, err)
	require.Equal(t, "acquire_rate", ts.Msg.GetMetric())
	require.Equal(t, "1m", ts.Msg.GetStep())
	require.Len(t, ts.Msg.GetSeries(), 1)
	require.Len(t, ts.Msg.GetSeries()[0].GetPoints(), 1)
	require.InDelta(t, 2.0, ts.Msg.GetSeries()[0].GetPoints()[0].GetValue(), 1e-9)
	require.True(t, e.bucket.Equal(ts.Msg.GetSeries()[0].GetPoints()[0].GetTs().AsTime()))

	ts, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{
		Namespace: "prod", Site: "alpha", Client: "web", EndpointGroupId: egA, Metric: "outcomes", TimeRange: rng,
	}))
	require.NoError(t, err)
	require.Len(t, ts.Msg.GetSeries(), 1)
	require.Equal(t, "success", ts.Msg.GetSeries()[0].GetName())
	require.Equal(t, map[string]string{"outcome": "success"}, ts.Msg.GetSeries()[0].GetLabels())
	require.InDelta(t, 30.0, ts.Msg.GetSeries()[0].GetPoints()[0].GetValue(), 1e-9)

	hm, err := e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{
		Namespace: "prod", Site: "alpha", Client: "web", Metric: "score",
	}))
	require.NoError(t, err)
	require.Equal(t, "score", hm.Msg.GetMetric())
	require.Equal(t, []string{"_default", "search"}, hm.Msg.GetColumns())
	require.Equal(t, egA, hm.Msg.GetColumnIds()[1])
	require.Len(t, hm.Msg.GetRows(), 1)
	require.Equal(t, e.idtA, hm.Msg.GetRows()[0].GetIdentityId())
	require.Equal(t, "active", hm.Msg.GetRows()[0].GetState())
	require.Equal(t, int32(1), hm.Msg.GetTotal())
	require.Empty(t, hm.Msg.GetNextPageToken())
	require.Len(t, hm.Msg.GetCells(), 2)
	for _, c := range hm.Msg.GetCells() {
		if c.GetCol() == 1 {
			require.InDelta(t, 40.0, c.GetScore(), 1e-9)
			require.True(t, c.GetAvailable())
		} else {
			require.False(t, c.GetAvailable())
		}
	}

	risk, err := e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", PageSize: 2}))
	require.NoError(t, err)
	require.Len(t, risk.Msg.GetEvents(), 2)
	require.NotEmpty(t, risk.Msg.GetNextPageToken())
	first := risk.Msg.GetEvents()[0]
	require.Equal(t, "prod", first.GetNamespace())
	require.Equal(t, "captcha", first.GetOutcome())
	require.Equal(t, "search", first.GetEndpointGroup())
	require.Equal(t, "web", first.GetClient())
	require.Equal(t, []string{"m"}, first.GetMarkers())
	require.NotNil(t, first.GetStartedAt())
	require.Nil(t, first.GetFinishedAt())
	risk, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{
		Namespace: "prod", PageSize: 2, PageToken: risk.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	require.Len(t, risk.Msg.GetEvents(), 1)
	require.Equal(t, "rsk_second", risk.Msg.GetEvents()[0].GetId())
	require.Empty(t, risk.Msg.GetNextPageToken())
	risk, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{
		Namespace: "prod", Site: "alpha", EndpointGroupId: egA, Outcome: "banned",
	}))
	require.NoError(t, err)
	require.Len(t, risk.Msg.GetEvents(), 1)

	req, err := e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{
		Namespace: "prod", PageSize: 2, IncludeSummary: true,
	}))
	require.NoError(t, err)
	require.Len(t, req.Msg.GetEvents(), 2)
	require.NotEmpty(t, req.Msg.GetNextPageToken())
	require.Equal(t, int64(3), req.Msg.GetSummary().GetTotal())
	require.Equal(t, map[string]int64{"success": 3}, req.Msg.GetSummary().GetOutcomes())
	require.InDelta(t, 200.0, req.Msg.GetSummary().GetLatencyAvgMs(), 1e-9)
	ev := req.Msg.GetEvents()[0]
	require.Equal(t, "prod", ev.GetNamespace())
	require.Equal(t, "beta", ev.GetSite())
	require.Equal(t, int32(300), ev.GetLatencyMs())
	require.NotNil(t, ev.GetEventTime())
	require.Nil(t, ev.GetStartedAt())
	req, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{
		Namespace: "prod", PageSize: 2, PageToken: req.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	require.Len(t, req.Msg.GetEvents(), 1)
	require.Nil(t, req.Msg.GetSummary())
	status := int32(200)
	req, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{
		Namespace: "prod", Site: "alpha", EndpointGroupId: egA, HttpStatus: &status, MinLatencyMs: 150,
	}))
	require.NoError(t, err)
	require.Len(t, req.Msg.GetEvents(), 1)

	nodes, err := e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Len(t, nodes.Msg.GetNodes(), 1)
	n := nodes.Msg.GetNodes()[0]
	require.Equal(t, "n1", n.GetNode())
	require.Equal(t, int64(10), n.GetAcquires())
	require.Equal(t, int64(8), n.GetReports())
	require.Equal(t, int64(2), n.GetAbandoned())
	require.Equal(t, int64(1), n.GetRejected())
	require.InDelta(t, 0.2, n.GetUnreportedRatio(), 1e-9)
}

func TestDashboardSiteRestricted(t *testing.T) {
	e := newEnv(t)
	ctx := as(user(authz.RoleViewer, siteB))

	ov, err := e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Len(t, ov.Msg.GetSites(), 1)
	require.Equal(t, "beta", ov.Msg.GetSites()[0].GetSite())
	require.Equal(t, map[string]int32{"active": 1}, ov.Msg.GetTotals().GetIdentitiesByState())
	_, err = e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod", Sites: []string{"alpha"}}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	rng := &spinneretv1.TimeRange{Start: timestamppb.New(e.bucket), End: timestamppb.New(e.bucket.Add(time.Minute))}
	ts, err := e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "acquire_rate", TimeRange: rng}))
	require.NoError(t, err)
	require.InDelta(t, 1.0, ts.Msg.GetSeries()[0].GetPoints()[0].GetValue(), 1e-9)
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", Site: "alpha"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", EndpointGroupId: egA}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "alpha", Client: "web", Metric: "cooldown"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	hm, err := e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "beta", Client: "web", Metric: "cooldown"}))
	require.NoError(t, err)
	require.Len(t, hm.Msg.GetRows(), 1)

	risk, err := e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Len(t, risk.Msg.GetEvents(), 1)
	require.Equal(t, "beta", risk.Msg.GetEvents()[0].GetSite())
	_, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", Site: "alpha"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	req, err := e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", IncludeSummary: true}))
	require.NoError(t, err)
	require.Len(t, req.Msg.GetEvents(), 1)
	require.Equal(t, int64(1), req.Msg.GetSummary().GetTotal())
	_, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", EndpointGroupId: egA}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	_, err = e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
}

func TestDashboardPrincipals(t *testing.T) {
	e := newEnv(t)
	overview := func(p *authz.Principal, namespace string) error {
		ctx := as(p)
		if p == nil {
			ctx = t.Context()
		}
		_, err := e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: namespace}))
		return err
	}
	require.NoError(t, overview(token(t, "admin"), ""))
	_, err := e.h.GetNodeStats(as(token(t, "admin")), connect.NewRequest(&spinneretv1.GetNodeStatsRequest{}))
	require.NoError(t, err)
	requireReason(t, overview(token(t, "lease:acquire"), "prod"), apperr.ReasonScopeMissing)
	requireReason(t, overview(token(t, "admin"), "other"), apperr.ReasonScopeMissing)
	_, err = e.h.GetNodeStats(as(token(t, "report:write")), connect.NewRequest(&spinneretv1.GetNodeStatsRequest{}))
	requireReason(t, err, apperr.ReasonScopeMissing)

	requireReason(t, overview(nil, "prod"), apperr.ReasonSessionInvalid)
	require.NoError(t, overview(user(authz.RoleOperator), "prod"))
	require.NoError(t, overview(&authz.Principal{Kind: authz.KindUser, ID: "usr_pa", TenantID: tenantID, IsPlatformAdmin: true}, "prod"))

	noBinding := &authz.Principal{Kind: authz.KindUser, ID: "usr_none", TenantID: tenantID}
	requireReason(t, overview(noBinding, "prod"), apperr.ReasonPermissionDenied)
	otherTenantUser := &authz.Principal{Kind: authz.KindUser, ID: "usr_other", TenantID: otherTenant,
		Bindings: []authz.Binding{{TenantID: otherTenant, Role: authz.RoleOwner}}}
	requireReason(t, overview(otherTenantUser, "prod"), apperr.ReasonNotFound)
	// A binding restricted to a site of another namespace grants nothing here.
	foreignSite := user(authz.RoleViewer, "sit_elsewhere")
	requireReason(t, overview(foreignSite, "prod"), apperr.ReasonPermissionDenied)
}

func TestDashboardValidation(t *testing.T) {
	e := newEnv(t)
	ctx := as(user(authz.RoleAdmin))
	_, err := e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod", Window: "2m"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod", Sites: []string{"nope"}}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", Step: "2m"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", Site: "nope"}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", EndpointGroupId: "eg_nope"}))
	requireReason(t, err, apperr.ReasonEndpointGroupUnknown)
	long := &spinneretv1.TimeRange{Start: timestamppb.New(e.now.Add(-40 * 24 * time.Hour))}
	_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", TimeRange: long}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "alpha", Client: "web", Metric: "heat"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "alpha", Client: "web", Metric: "score", PageToken: "%%%"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "", Client: "web", Metric: "score"}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", PageToken: "not-a-token"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", Site: "nope"}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", PageToken: "not-a-token"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", Site: "nope"}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod", TimeRange: long}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "missing"}))
	requireReason(t, err, apperr.ReasonNotFound)

	_, err = e.noCH.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod"}))
	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, apperr.ToConnect(err).Code())
	// Permission checks run before the availability check.
	_, err = e.noCH.QueryRequestEvents(as(token(t, "lease:acquire")), connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
}

func TestConversionHelpers(t *testing.T) {
	require.Equal(t, int32(2147483647), clampInt32(1<<40))
	require.Equal(t, int32(-2147483648), clampInt32(-(1 << 40)))
	require.Nil(t, eventTimestamp(time.Unix(0, 0)))
	require.Nil(t, eventTimestamp(time.Time{}))
	require.NotNil(t, eventTimestamp(time.Unix(10, 0)))
	require.Empty(t, siteID(nil))
	require.Equal(t, map[string]int32{"a": 1}, counts(map[string]int64{"a": 1}))
	_, err := encodeCursor(func() {})
	requireReason(t, err, apperr.ReasonInternal)
}
