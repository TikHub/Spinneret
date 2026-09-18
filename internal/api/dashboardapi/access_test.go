package dashboardapi

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// rpc invokes one DashboardService method and returns its error.
type rpc struct {
	name string
	call func(ctx context.Context, h *Handler) error
}

// allRPCs calls every RPC with a minimal valid request for namespace "prod".
func allRPCs() []rpc {
	return []rpc{
		{"GetOverview", func(ctx context.Context, h *Handler) error {
			_, err := h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod"}))
			return err
		}},
		{"GetTimeSeries", func(ctx context.Context, h *Handler) error {
			_, err := h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes"}))
			return err
		}},
		{"GetHeatmap", func(ctx context.Context, h *Handler) error {
			_, err := h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "alpha", Client: "web", Metric: "score"}))
			return err
		}},
		{"ListRiskEvents", func(ctx context.Context, h *Handler) error {
			_, err := h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod"}))
			return err
		}},
		{"QueryRequestEvents", func(ctx context.Context, h *Handler) error {
			_, err := h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod"}))
			return err
		}},
		{"GetNodeStats", func(ctx context.Context, h *Handler) error {
			_, err := h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod"}))
			return err
		}},
	}
}

// Every RPC denies callers without dashboard:read in the namespace.
func TestDashboardPermissionMatrix(t *testing.T) {
	e := newEnv(t)
	otherNamespace := &authz.Principal{Kind: authz.KindUser, ID: "usr_ons", TenantID: tenantID,
		Bindings: []authz.Binding{{TenantID: tenantID, Role: authz.RoleOwner, NamespaceID: "ns_elsewhere"}}}
	otherTenantBinding := &authz.Principal{Kind: authz.KindUser, ID: "usr_otb", TenantID: tenantID,
		Bindings: []authz.Binding{{TenantID: otherTenant, Role: authz.RoleOwner}}}
	unknownRole := &authz.Principal{Kind: authz.KindUser, ID: "usr_role", TenantID: tenantID,
		Bindings: []authz.Binding{{TenantID: tenantID, Role: "superuser"}}}
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		reason apperr.Reason
	}{
		{"unauthenticated", context.Background(), apperr.ReasonSessionInvalid},
		{"user without binding", as(&authz.Principal{Kind: authz.KindUser, ID: "usr_none", TenantID: tenantID}), apperr.ReasonPermissionDenied},
		{"user bound to another namespace", as(otherNamespace), apperr.ReasonPermissionDenied},
		{"user bound in another tenant only", as(otherTenantBinding), apperr.ReasonPermissionDenied},
		{"user with an unknown role", as(unknownRole), apperr.ReasonPermissionDenied},
		{"user restricted to a foreign site", as(user(authz.RoleOwner, "sit_elsewhere")), apperr.ReasonPermissionDenied},
		{"node token", as(token(t, "lease:acquire", "report:write", "config:read")), apperr.ReasonScopeMissing},
		{"identity token of a site", as(token(t, "identity:write:alpha", "proxy:write")), apperr.ReasonScopeMissing},
		{"token of another namespace", as(&authz.Principal{Kind: authz.KindToken, ID: "tok_o", TenantID: tenantID,
			NamespaceID: "ns_elsewhere", NamespaceName: "elsewhere", Scopes: []authz.Scope{{Raw: "admin", Name: "admin"}}}), apperr.ReasonScopeMissing},
	} {
		for _, r := range allRPCs() {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				requireReason(t, r.call(tc.ctx, e.h), tc.reason)
				requireReason(t, r.call(tc.ctx, e.noCH), tc.reason)
			})
		}
	}

	// Callers with namespace-wide dashboard:read pass every RPC.
	for _, ctx := range []context.Context{
		as(user(authz.RoleViewer)),
		as(user(authz.RoleOwner)),
		as(token(t, "admin")),
		as(&authz.Principal{Kind: authz.KindUser, ID: "usr_pa", TenantID: tenantID, IsPlatformAdmin: true}),
	} {
		for _, r := range allRPCs() {
			require.NoError(t, r.call(ctx, e.h), r.name)
		}
	}
}

// Site-restricted callers pass every site-scoped RPC for their site but are
// denied node statistics.
func TestDashboardSiteRestrictedMatrix(t *testing.T) {
	e := newEnv(t)
	alphaOnly := as(user(authz.RoleViewer, siteA))
	tenantWideAlpha := as(&authz.Principal{Kind: authz.KindUser, ID: "usr_tw", TenantID: tenantID,
		Bindings: []authz.Binding{{TenantID: tenantID, Role: authz.RoleViewer, SiteIDs: []string{siteA}}}})
	for _, ctx := range []context.Context{alphaOnly, tenantWideAlpha} {
		for _, r := range allRPCs() {
			err := r.call(ctx, e.h)
			if r.name == "GetNodeStats" {
				requireReason(t, err, apperr.ReasonPermissionDenied)
				continue
			}
			require.NoError(t, err, r.name)
		}
		ov, err := e.h.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: "prod"}))
		require.NoError(t, err)
		require.Len(t, ov.Msg.GetSites(), 1)
		require.Equal(t, "alpha", ov.Msg.GetSites()[0].GetSite())
		_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{Namespace: "prod", Site: "beta", Client: "web", Metric: "score"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", Site: "beta"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", EndpointGroupId: egB}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", Site: "alpha", EndpointGroupId: egB}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
	}
}

// Timestamps a google.protobuf.Timestamp cannot represent are rejected before
// reaching the databases.
func TestDashboardInvalidTimestamps(t *testing.T) {
	e := newEnv(t)
	ctx := as(user(authz.RoleViewer))
	for _, rng := range []*spinneretv1.TimeRange{
		{Start: &timestamppb.Timestamp{Seconds: -70_000_000_000}},
		{End: &timestamppb.Timestamp{Seconds: 300_000_000_000}},
		{Start: &timestamppb.Timestamp{Seconds: 10, Nanos: -1}},
	} {
		_, err := e.h.GetTimeSeries(ctx, connect.NewRequest(&spinneretv1.GetTimeSeriesRequest{Namespace: "prod", Metric: "outcomes", TimeRange: rng}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = e.h.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: "prod", TimeRange: rng}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = e.h.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: "prod", TimeRange: rng}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod", TimeRange: rng}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
	}
	// A valid explicit range is accepted.
	rng := &spinneretv1.TimeRange{Start: timestamppb.New(e.now.Add(-time.Hour)), End: timestamppb.New(e.now)}
	_, err := e.h.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: "prod", TimeRange: rng}))
	require.NoError(t, err)
}

// Heatmap rows page through next_page_token.
func TestDashboardHeatmapPaging(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"idt_dash_a2", "idt_dash_a3"} {
		sum := sha256.Sum256([]byte(id))
		_, err := e.pool.Exec(e.ctx, `INSERT INTO identities (id, site_id, client, type_id, state, unique_hash, payload_hash)
			VALUES ($1, $2, 'web', 'ity_a', 'pending', $3, $3)`, id, siteA, sum[:])
		require.NoError(t, err)
	}
	ctx := as(user(authz.RoleViewer, siteA))
	var (
		seen  []string
		token string
	)
	for page := 0; page < 5; page++ {
		hm, err := e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{
			Namespace: "prod", Site: "alpha", Client: "web", Metric: "cooldown", Limit: 2, PageToken: token,
		}))
		require.NoError(t, err)
		require.Equal(t, int32(3), hm.Msg.GetTotal())
		for _, r := range hm.Msg.GetRows() {
			seen = append(seen, r.GetIdentityId())
		}
		for _, c := range hm.Msg.GetCells() {
			require.Less(t, int(c.GetRow()), len(hm.Msg.GetRows()))
			require.Less(t, int(c.GetCol()), len(hm.Msg.GetColumns()))
		}
		token = hm.Msg.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Equal(t, []string{e.idtA, "idt_dash_a2", "idt_dash_a3"}, seen)

	states, err := e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{
		Namespace: "prod", Site: "alpha", Client: "web", Metric: "score", States: []string{"pending"},
	}))
	require.NoError(t, err)
	require.Len(t, states.Msg.GetRows(), 2)
	_, err = e.h.GetHeatmap(ctx, connect.NewRequest(&spinneretv1.GetHeatmapRequest{
		Namespace: "prod", Site: "alpha", Client: "app", Metric: "score",
	}))
	requireReason(t, err, apperr.ReasonClientUnknown)
}
