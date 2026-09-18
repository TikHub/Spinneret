// Package nodeapi verifies API contracts of the generated protobuf code: RPC
// surfaces, protovalidate rules and JSON shapes.
package nodeapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

var (
	marshalOpts   = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}
	unmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}
)

func ts(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

func ptr[T any](v T) *T { return &v }

func packageMessages(t *testing.T) []protoreflect.MessageDescriptor {
	t.Helper()
	var out []protoreflect.MessageDescriptor
	var walk func(protoreflect.MessageDescriptors)
	walk = func(ms protoreflect.MessageDescriptors) {
		for i := 0; i < ms.Len(); i++ {
			md := ms.Get(i)
			if md.IsMapEntry() {
				continue
			}
			out = append(out, md)
			walk(md.Messages())
		}
	}
	protoregistry.GlobalFiles.RangeFilesByPackage("spinneret.v1", func(fd protoreflect.FileDescriptor) bool {
		walk(fd.Messages())
		return true
	})
	require.NotEmpty(t, out)
	return out
}

// TestRulesCompile builds the protovalidate rules of every spinneret.v1 message
// eagerly and evaluates each against an empty message, so CEL typos or rule
// type mismatches surface as compilation/runtime errors.
func TestRulesCompile(t *testing.T) {
	descs := packageMessages(t)
	v, err := protovalidate.New(protovalidate.WithMessageDescriptors(descs...), protovalidate.WithDisableLazy())
	require.NoError(t, err)
	for _, md := range descs {
		err := v.Validate(dynamicpb.NewMessage(md))
		if err == nil {
			continue
		}
		var verr *protovalidate.ValidationError
		require.Truef(t, errors.As(err, &verr), "%s: unexpected non-validation error: %v", md.FullName(), err)
	}
}

// TestServiceMethods pins the RPC sets of spec section 10.
func TestServiceMethods(t *testing.T) {
	want := map[string][]string{
		"LeaseService":             {"Acquire", "AcquireBatch", "Renew", "Release"},
		"ReportService":            {"Report"},
		"ConfigService":            {"GetConfig", "BatchGetConfig", "WatchConfig"},
		"SecretService":            {"GetSecret"},
		"AuthService":              {"Login", "Logout", "GetMe", "ChangePassword"},
		"TenantAdminService":       {"ListTenants", "CreateTenant", "UpdateTenant", "DeleteTenant", "ListNamespaces", "CreateNamespace", "UpdateNamespace", "DeleteNamespace"},
		"AccessAdminService":       {"ListTokens", "CreateToken", "RevokeToken", "ListUsers", "CreateUser", "UpdateUser", "ResetPassword", "ListRoleBindings", "CreateRoleBinding", "DeleteRoleBinding", "ListAuditLogs"},
		"SiteAdminService":         {"ListSites", "GetSite", "CreateSite", "UpdateSite", "DeleteSite", "ListEndpointGroups", "CreateEndpointGroup", "UpdateEndpointGroup", "DeleteEndpointGroup", "ListURIRules", "ReplaceURIRules", "TestURI"},
		"BreakerAdminService":      {"ListBreakers", "GetBreaker", "OpenBreaker", "CloseBreaker", "ListBreakerEvents", "SetSitePaused"},
		"NotificationAdminService": {"ListChannels", "CreateChannel", "UpdateChannel", "DeleteChannel", "TestChannel", "ListAlertEvents"},
		"DashboardService":         {"GetOverview", "GetTimeSeries", "GetHeatmap", "ListRiskEvents", "QueryRequestEvents", "GetNodeStats"},
	}
	for svc, methods := range want {
		t.Run(svc, func(t *testing.T) {
			d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName("spinneret.v1." + svc))
			require.NoError(t, err)
			sd, ok := d.(protoreflect.ServiceDescriptor)
			require.True(t, ok)
			var got []string
			for i := 0; i < sd.Methods().Len(); i++ {
				m := sd.Methods().Get(i)
				got = append(got, string(m.Name()))
				require.Equal(t, string(m.Name())+"Request", string(m.Input().Name()))
				require.Equal(t, string(m.Name())+"Response", string(m.Output().Name()))
				require.False(t, m.IsStreamingClient() || m.IsStreamingServer())
			}
			require.Equal(t, methods, got)
		})
	}
	require.Equal(t, "/spinneret.v1.LeaseService/Acquire", spinneretv1connect.LeaseServiceAcquireProcedure)
	require.Equal(t, "/spinneret.v1.ReportService/Report", spinneretv1connect.ReportServiceReportProcedure)
}

func validReport(now time.Time) *v1.Report {
	return &v1.Report{
		ReportId:      "2c6f0e5a-8d8b-4a52-9f0e-1b7f3c9d2a41",
		LeaseId:       "lse_0192a3f4c1d27b8e9a01f2c3d4e5a6b7_1a_0f",
		Uri:           "/api/v1/search",
		Method:        "GET",
		HttpStatus:    200,
		Markers:       []string{"empty_list"},
		LatencyMs:     842,
		ResponseBytes: 48213,
		StartedAt:     ts(now.Add(-time.Second)),
		FinishedAt:    ts(now),
		Release:       true,
	}
}

type vcase struct {
	name   string
	msg    proto.Message
	valid  bool
	ruleID string // optional expected violation rule id (substring)
}

func withReport(now time.Time, mut func(r *v1.Report)) *v1.Report {
	r := validReport(now)
	mut(r)
	return r
}

func nodeCases(now time.Time) []vcase {
	acq := func(mut func(*v1.AcquireRequest)) *v1.AcquireRequest {
		r := &v1.AcquireRequest{Site: "shop", Client: "web", Uri: "/a", SessionKey: "task-1"}
		mut(r)
		return r
	}
	reports := func(n int) []*v1.Report {
		out := make([]*v1.Report, n)
		for i := range out {
			out[i] = validReport(now)
		}
		return out
	}
	return []vcase{
		{"acquire valid", acq(func(*v1.AcquireRequest) {}), true, ""},
		{"acquire empty site", acq(func(r *v1.AcquireRequest) { r.Site = "" }), false, "string.min_len"},
		{"acquire long site", acq(func(r *v1.AcquireRequest) { r.Site = strings.Repeat("a", 65) }), false, "string.max_len"},
		{"acquire long client", acq(func(r *v1.AcquireRequest) { r.Client = strings.Repeat("a", 33) }), false, "string.max_len"},
		{"acquire long uri", acq(func(r *v1.AcquireRequest) { r.Uri = "/" + strings.Repeat("a", 2048) }), false, "string.max_len"},
		{"acquire long session", acq(func(r *v1.AcquireRequest) { r.SessionKey = strings.Repeat("a", 257) }), false, "string.max_len"},
		{"acquire wait max", acq(func(r *v1.AcquireRequest) { r.WaitMs = 5000 }), true, ""},
		{"acquire wait too long", acq(func(r *v1.AcquireRequest) { r.WaitMs = 5001 }), false, "int32.gte_lte"},
		{"acquire wait negative", acq(func(r *v1.AcquireRequest) { r.WaitMs = -1 }), false, "int32.gte"},
		{"batch count 0", &v1.AcquireBatchRequest{Site: "s", Client: "web"}, false, "int32.gte_lte"},
		{"batch count 50", &v1.AcquireBatchRequest{Site: "s", Client: "web", Count: 50}, true, ""},
		{"batch count 51", &v1.AcquireBatchRequest{Site: "s", Client: "web", Count: 51}, false, "int32.gte_lte"},
		{"renew valid", &v1.RenewRequest{LeaseId: "lse_x", ExtendMs: 1800000}, true, ""},
		{"renew too long", &v1.RenewRequest{LeaseId: "lse_x", ExtendMs: 1800001}, false, "int32.gte_lte"},
		{"renew empty lease", &v1.RenewRequest{}, false, "string.min_len"},
		{"release empty lease", &v1.ReleaseRequest{}, false, "string.min_len"},
		{"report request valid", &v1.ReportRequest{Reports: reports(1)}, true, ""},
		{"report request empty", &v1.ReportRequest{}, false, "repeated.min_items"},
		{"report request 500", &v1.ReportRequest{Reports: reports(500)}, true, ""},
		{"report request 501", &v1.ReportRequest{Reports: reports(501)}, false, "repeated.max_items"},
		{"report request ignores invalid items", &v1.ReportRequest{Reports: []*v1.Report{{ReportId: "bad id!"}}}, true, ""},
		{"report valid", validReport(now), true, ""},
		{"report no status network error", withReport(now, func(r *v1.Report) { r.HttpStatus = 0; r.ErrorKind = "timeout" }), true, ""},
		{"report bad id chars", withReport(now, func(r *v1.Report) { r.ReportId = "a b" }), false, "string.pattern"},
		{"report id too long", withReport(now, func(r *v1.Report) { r.ReportId = strings.Repeat("a", 65) }), false, "string.pattern"},
		{"report empty lease", withReport(now, func(r *v1.Report) { r.LeaseId = "" }), false, "string.min_len"},
		{"report empty uri", withReport(now, func(r *v1.Report) { r.Uri = "" }), false, "string.min_len"},
		{"report status 1000", withReport(now, func(r *v1.Report) { r.HttpStatus = 1000 }), false, "int32.gte_lte"},
		{"report bad error kind", withReport(now, func(r *v1.Report) { r.ErrorKind = "boom" }), false, "string.in"},
		{"report 33 markers", withReport(now, func(r *v1.Report) {
			r.Markers = make([]string, 33)
			for i := range r.Markers {
				r.Markers[i] = "m"
			}
		}), false, "repeated.max_items"},
		{"report empty marker", withReport(now, func(r *v1.Report) { r.Markers = []string{""} }), false, "string.min_len"},
		{"report negative latency", withReport(now, func(r *v1.Report) { r.LatencyMs = -1 }), false, "int32.gte"},
		{"report negative bytes", withReport(now, func(r *v1.Report) { r.ResponseBytes = -1 }), false, "int64.gte"},
		{"report missing started", withReport(now, func(r *v1.Report) { r.StartedAt = nil }), false, "required"},
		{"report finished before started", withReport(now, func(r *v1.Report) { r.FinishedAt = ts(now.Add(-time.Hour)) }), false, "report.finished_after_started"},
		{"get config valid", &v1.GetConfigRequest{Group: "crawler", Key: "search.json"}, true, ""},
		{"get config empty group", &v1.GetConfigRequest{Key: "k"}, false, "string.min_len"},
		{"batch get empty", &v1.BatchGetConfigRequest{}, false, "repeated.min_items"},
		{"batch get nested invalid", &v1.BatchGetConfigRequest{Items: []*v1.ConfigRef{{Group: "g"}}}, false, "string.min_len"},
		{"watch valid", &v1.WatchConfigRequest{Items: []*v1.WatchItem{{Group: "_runtime", Key: "breakers", Version: 348}}, TimeoutMs: 60000}, true, ""},
		{"watch timeout too long", &v1.WatchConfigRequest{Items: []*v1.WatchItem{{Group: "g", Key: "k"}}, TimeoutMs: 60001}, false, "int32.gte_lte"},
		{"watch negative version", &v1.WatchConfigRequest{Items: []*v1.WatchItem{{Group: "g", Key: "k", Version: -1}}}, false, "int32.gte"},
		{"get secret valid", &v1.GetSecretRequest{Path: "signing/api_key"}, true, ""},
		{"get secret upper", &v1.GetSecretRequest{Path: "Signing/api_key"}, false, "string.pattern"},
		{"get secret absolute", &v1.GetSecretRequest{Path: "/signing/api_key"}, false, "string.pattern"},
		{"get secret negative version", &v1.GetSecretRequest{Path: "k", Version: -1}, false, "int32.gte"},
	}
}

func adminCases(now time.Time) []vcase {
	past, future := ts(now.Add(-time.Hour)), ts(now.Add(time.Hour))
	token := func(mut func(*v1.CreateTokenRequest)) *v1.CreateTokenRequest {
		r := &v1.CreateTokenRequest{Namespace: "prod", Name: "crawler", Scopes: []string{"lease:acquire", "report:write"}}
		mut(r)
		return r
	}
	user := func(mut func(*v1.CreateUserRequest)) *v1.CreateUserRequest {
		r := &v1.CreateUserRequest{Username: "alice", Password: "0123456789", Role: "viewer"}
		mut(r)
		return r
	}
	cfg, err := structpb.NewStruct(map[string]any{"url": "https://example.com/hook"})
	if err != nil {
		panic(err)
	}
	channel := func(mut func(*v1.CreateChannelRequest)) *v1.CreateChannelRequest {
		r := &v1.CreateChannelRequest{Name: "ops", Kind: "webhook", Config: cfg, EventTypes: []string{"breaker_opened"}}
		mut(r)
		return r
	}
	inverted := &v1.TimeRange{Start: future, End: past}
	ordered := &v1.TimeRange{Start: past, End: future}
	return []vcase{
		{"login valid", &v1.LoginRequest{Username: "alice", Password: "x"}, true, ""},
		{"login empty username", &v1.LoginRequest{Password: "x"}, false, "string.min_len"},
		{"change password short", &v1.ChangePasswordRequest{CurrentPassword: "x", NewPassword: "012345678"}, false, "string.min_len"},
		{"change password ok", &v1.ChangePasswordRequest{CurrentPassword: "x", NewPassword: "0123456789"}, true, ""},
		{"tenant name valid", &v1.CreateTenantRequest{Name: "prod-1"}, true, ""},
		{"tenant name one char", &v1.CreateTenantRequest{Name: "a"}, false, "string.pattern"},
		{"tenant name upper", &v1.CreateTenantRequest{Name: "Prod"}, false, "string.pattern"},
		{"tenant name leading dash", &v1.CreateTenantRequest{Name: "-prod"}, false, "string.pattern"},
		{"namespace name valid", &v1.CreateNamespaceRequest{Name: "ns1"}, true, ""},
		{"update tenant unset", &v1.UpdateTenantRequest{Id: "ten_1"}, true, ""},
		{"update tenant long name", &v1.UpdateTenantRequest{Id: "ten_1", DisplayName: ptr(strings.Repeat("a", 129))}, false, "string.max_len"},
		{"list tenants page 501", &v1.ListTenantsRequest{PageSize: 501}, false, "int32.gte_lte"},
		{"list tenants page -1", &v1.ListTenantsRequest{PageSize: -1}, false, "int32.gte_lte"},
		{"token valid", token(func(*v1.CreateTokenRequest) {}), true, ""},
		{"token no scopes", token(func(r *v1.CreateTokenRequest) { r.Scopes = nil }), false, "repeated.min_items"},
		{"token duplicate scopes", token(func(r *v1.CreateTokenRequest) { r.Scopes = []string{"admin", "admin"} }), false, "repeated.unique"},
		{"token expired", token(func(r *v1.CreateTokenRequest) { r.ExpiresAt = past }), false, "timestamp.gt_now"},
		{"token future expiry", token(func(r *v1.CreateTokenRequest) { r.ExpiresAt = future }), true, ""},
		{"token allowlist ok", token(func(r *v1.CreateTokenRequest) {
			r.IpAllowlist = []string{"10.0.0.0/8", "10.0.0.1/8", "1.2.3.4", "2001:db8::/32", "::1"}
		}), true, ""},
		{"token allowlist garbage", token(func(r *v1.CreateTokenRequest) { r.IpAllowlist = []string{"not-an-ip"} }), false, "ip_or_cidr"},
		{"user valid", user(func(*v1.CreateUserRequest) {}), true, ""},
		{"user short username", user(func(r *v1.CreateUserRequest) { r.Username = "ab" }), false, "string.pattern"},
		{"user upper username", user(func(r *v1.CreateUserRequest) { r.Username = "Alice" }), false, "string.pattern"},
		{"user short password", user(func(r *v1.CreateUserRequest) { r.Password = "short" }), false, "string.min_len"},
		{"user bad role", user(func(r *v1.CreateUserRequest) { r.Role = "root" }), false, "string.in"},
		{"user sites need namespace", user(func(r *v1.CreateUserRequest) { r.Sites = []string{"shop"} }), false, "create_user.sites_require_namespace"},
		{"user sites with namespace", user(func(r *v1.CreateUserRequest) { r.Sites = []string{"shop"}; r.Namespace = "prod" }), true, ""},
		{"user bad email", user(func(r *v1.CreateUserRequest) { r.Email = "nope" }), false, "string.email"},
		{"user good email", user(func(r *v1.CreateUserRequest) { r.Email = "a@example.com" }), true, ""},
		{"user bad extra permission", user(func(r *v1.CreateUserRequest) { r.ExtraPermissions = []string{"token:write"} }), false, "string.in"},
		{"update user bad email", &v1.UpdateUserRequest{Id: "usr_1", Email: ptr("nope")}, false, "update_user.email"},
		{"update user clear email", &v1.UpdateUserRequest{Id: "usr_1", Email: ptr("")}, true, ""},
		{"update user good email", &v1.UpdateUserRequest{Id: "usr_1", Email: ptr("a@example.com")}, true, ""},
		{"update user locale ok", &v1.UpdateUserRequest{Id: "usr_1", Locale: ptr("zh-CN")}, true, ""},
		{"update user locale bad", &v1.UpdateUserRequest{Id: "usr_1", Locale: ptr("zh_CN")}, false, "string.pattern"},
		{"binding bad role", &v1.CreateRoleBindingRequest{UserId: "usr_1", Role: "god"}, false, "string.in"},
		{"binding sites need namespace", &v1.CreateRoleBindingRequest{UserId: "usr_1", Role: "viewer", Sites: []string{"s"}}, false, "create_role_binding.sites_require_namespace"},
		{"audit bad result", &v1.ListAuditLogsRequest{Result: "maybe"}, false, "string.in"},
		{"audit inverted range", &v1.ListAuditLogsRequest{TimeRange: inverted}, false, "time_range.ordered"},
		{"audit ordered range", &v1.ListAuditLogsRequest{TimeRange: ordered}, true, ""},
		{"audit open range", &v1.ListAuditLogsRequest{TimeRange: &v1.TimeRange{Start: past}}, true, ""},
		{"site valid", &v1.CreateSiteRequest{Namespace: "prod", Name: "shop", Clients: []string{"web", "app"}}, true, ""},
		{"site upper", &v1.CreateSiteRequest{Namespace: "prod", Name: "Shop"}, false, "string.pattern"},
		{"site leading dot", &v1.CreateSiteRequest{Namespace: "prod", Name: ".x"}, false, "string.pattern"},
		{"site too long", &v1.CreateSiteRequest{Namespace: "prod", Name: strings.Repeat("a", 65)}, false, "string.pattern"},
		{"site bad client", &v1.CreateSiteRequest{Namespace: "prod", Name: "d", Clients: []string{"WEB"}}, false, "string.pattern"},
		{"site duplicate client", &v1.CreateSiteRequest{Namespace: "prod", Name: "d", Clients: []string{"web", "web"}}, false, "repeated.unique"},
		{"get site nothing", &v1.GetSiteRequest{}, false, "get_site.address"},
		{"get site by id", &v1.GetSiteRequest{Id: "sit_1"}, true, ""},
		{"get site by name", &v1.GetSiteRequest{Namespace: "prod", Name: "shop"}, true, ""},
		{"get site namespace only", &v1.GetSiteRequest{Namespace: "prod"}, false, "get_site.address"},
		{"delete site force", &v1.DeleteSiteRequest{Id: "sit_1", Force: true}, true, ""},
		{"eg bad rule kind", &v1.CreateEndpointGroupRequest{Namespace: "p", Site: "s", Client: "web", Name: "search", Rules: []*v1.URIRule{{Kind: "glob", Pattern: "/a"}}}, false, "string.in"},
		{"eg empty pattern", &v1.CreateEndpointGroupRequest{Namespace: "p", Site: "s", Client: "web", Name: "search", Rules: []*v1.URIRule{{Kind: "exact"}}}, false, "string.min_len"},
		{"eg valid", &v1.CreateEndpointGroupRequest{Namespace: "p", Site: "s", Client: "web", Name: "search", Rules: []*v1.URIRule{{Kind: "template", Pattern: "/v1/{id}"}}}, true, ""},
		{"replace rules empty", &v1.ReplaceURIRulesRequest{EndpointGroupId: "eg_1"}, true, ""},
		{"test uri empty", &v1.TestURIRequest{Namespace: "p", Site: "s", Client: "web"}, false, "string.min_len"},
		{"breakers bad state", &v1.ListBreakersRequest{Namespace: "p", States: []string{"open", "bogus"}}, false, "string.in"},
		{"open breaker empty eg", &v1.OpenBreakerRequest{}, false, "string.min_len"},
		{"open breaker long duration", &v1.OpenBreakerRequest{EndpointGroupId: "eg_1", Duration: strings.Repeat("1", 33)}, false, "string.max_len"},
		{"breaker events bad trigger", &v1.ListBreakerEventsRequest{Namespace: "p", Trigger: "x"}, false, "string.in"},
		{"breaker events inverted", &v1.ListBreakerEventsRequest{Namespace: "p", TimeRange: inverted}, false, "time_range.ordered"},
		{"site paused empty site", &v1.SetSitePausedRequest{Namespace: "p"}, false, "string.min_len"},
		{"overview bad window", &v1.GetOverviewRequest{Namespace: "p", Window: "2m"}, false, "string.in"},
		{"overview valid", &v1.GetOverviewRequest{Namespace: "p", Window: "5m"}, true, ""},
		{"series empty metric", &v1.GetTimeSeriesRequest{Namespace: "p"}, false, "string.in"},
		{"series valid", &v1.GetTimeSeriesRequest{Namespace: "p", Metric: "outcomes", Site: "s", Client: "web", Step: "5m", TimeRange: ordered}, true, ""},
		{"series bad step", &v1.GetTimeSeriesRequest{Namespace: "p", Metric: "outcomes", Step: "2m"}, false, "string.in"},
		{"series client without site", &v1.GetTimeSeriesRequest{Namespace: "p", Metric: "outcomes", Client: "web"}, false, "get_time_series.client_requires_site"},
		{"series inverted", &v1.GetTimeSeriesRequest{Namespace: "p", Metric: "outcomes", TimeRange: inverted}, false, "time_range.ordered"},
		{"heatmap valid", &v1.GetHeatmapRequest{Namespace: "p", Site: "s", Client: "web", Metric: "cooldown", Limit: 500}, true, ""},
		{"heatmap limit", &v1.GetHeatmapRequest{Namespace: "p", Site: "s", Client: "web", Metric: "score", Limit: 501}, false, "int32.gte_lte"},
		{"heatmap bad state", &v1.GetHeatmapRequest{Namespace: "p", Site: "s", Client: "web", Metric: "score", States: []string{"gone"}}, false, "string.in"},
		{"risk success outcome", &v1.ListRiskEventsRequest{Namespace: "p", Outcome: "success"}, false, "string.in"},
		{"risk inverted", &v1.ListRiskEventsRequest{Namespace: "p", TimeRange: inverted}, false, "time_range.ordered"},
		{"requests status unset", &v1.QueryRequestEventsRequest{Namespace: "p"}, true, ""},
		{"requests status zero", &v1.QueryRequestEventsRequest{Namespace: "p", HttpStatus: ptr[int32](0), IncludeSummary: true}, true, ""},
		{"requests status 1000", &v1.QueryRequestEventsRequest{Namespace: "p", HttpStatus: ptr[int32](1000)}, false, "int32.gte_lte"},
		{"requests duplicate outcomes", &v1.QueryRequestEventsRequest{Namespace: "p", Outcomes: []string{"success", "success"}}, false, "repeated.unique"},
		{"requests inverted", &v1.QueryRequestEventsRequest{Namespace: "p", TimeRange: inverted}, false, "time_range.ordered"},
		{"node stats inverted", &v1.GetNodeStatsRequest{Namespace: "p", TimeRange: inverted}, false, "time_range.ordered"},
		{"node stats ordered", &v1.GetNodeStatsRequest{Namespace: "p", TimeRange: ordered}, true, ""},
		{"channel valid", channel(func(*v1.CreateChannelRequest) {}), true, ""},
		{"channel bad kind", channel(func(r *v1.CreateChannelRequest) { r.Kind = "pager" }), false, "string.in"},
		{"channel no config", channel(func(r *v1.CreateChannelRequest) { r.Config = nil }), false, "required"},
		{"channel no events", channel(func(r *v1.CreateChannelRequest) { r.EventTypes = nil }), false, "repeated.min_items"},
		{"channel bad event", channel(func(r *v1.CreateChannelRequest) { r.EventTypes = []string{"everything"} }), false, "string.in"},
		{"channel sites need namespace", channel(func(r *v1.CreateChannelRequest) { r.Sites = []string{"s"} }), false, "create_channel.sites_require_namespace"},
		{"channel bad severity", channel(func(r *v1.CreateChannelRequest) { r.MinSeverity = "high" }), false, "string.in"},
		{"update channel nil config", &v1.UpdateChannelRequest{Id: "nch_1", Name: "ops", EventTypes: []string{"test"}}, true, ""},
		{"update channel no events", &v1.UpdateChannelRequest{Id: "nch_1", Name: "ops"}, false, "repeated.min_items"},
		{"alerts site without namespace", &v1.ListAlertEventsRequest{Site: "s"}, false, "list_alert_events.site_requires_namespace"},
		{"alerts inverted", &v1.ListAlertEventsRequest{TimeRange: inverted}, false, "time_range.ordered"},
		{"alerts valid", &v1.ListAlertEventsRequest{Namespace: "p", Site: "s", Severity: "critical"}, true, ""},
	}
}

func TestValidationRules(t *testing.T) {
	now := time.Now()
	v, err := protovalidate.New()
	require.NoError(t, err)
	cases := append(nodeCases(now), adminCases(now)...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.msg)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			var verr *protovalidate.ValidationError
			require.ErrorAs(t, err, &verr)
			if tc.ruleID == "" {
				return
			}
			var ids []string
			for _, viol := range verr.Violations {
				ids = append(ids, viol.Proto.GetRuleId())
			}
			require.Truef(t, containsSubstring(ids, tc.ruleID), "rule ids %v do not contain %q", ids, tc.ruleID)
		})
	}
}

func containsSubstring(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestReportPartialTimestamps checks that a report missing one timestamp gets
// only the required violation, not an additional ordering violation.
func TestReportPartialTimestamps(t *testing.T) {
	now := time.Now()
	r := validReport(now)
	r.FinishedAt = nil
	err := protovalidate.Validate(r)
	var verr *protovalidate.ValidationError
	require.ErrorAs(t, err, &verr)
	require.Len(t, verr.Violations, 1)
	require.Equal(t, "required", verr.Violations[0].Proto.GetRuleId())
}

func jsonMap(t *testing.T, m proto.Message) map[string]any {
	t.Helper()
	b, err := marshalOpts.Marshal(m)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// TestAcquireResponseJSON checks the design doc 15.3 response shape.
func TestAcquireResponseJSON(t *testing.T) {
	exp := time.Date(2026, 9, 16, 8, 32, 10, 0, time.UTC)
	resp := &v1.AcquireResponse{
		Lease: &v1.Lease{
			LeaseId: "lse_1", IdentityId: "idt_1", IdentityType: "web_cookie",
			EndpointGroup: "search", ExpiresAt: ts(exp), Sticky: true,
		},
		Credential: &v1.Credential{
			Cookies:      map[string]string{"sessionid": "a1b2c3"},
			CookieHeader: "sessionid=a1b2c3",
		},
		Hints: &v1.Hints{RenewBeforeMs: 30000},
	}
	got := jsonMap(t, resp)
	require.ElementsMatch(t, []string{"lease", "credential", "proxy", "hints"}, keys(got))
	require.Nil(t, got["proxy"])
	lease := got["lease"].(map[string]any)
	require.ElementsMatch(t, []string{"lease_id", "identity_id", "identity_type", "endpoint_group", "expires_at", "sticky", "probe"}, keys(lease))
	require.Equal(t, "2026-09-16T08:32:10Z", lease["expires_at"])
	require.Equal(t, false, lease["probe"])
	cred := got["credential"].(map[string]any)
	require.ElementsMatch(t, []string{"cookies", "cookie_header", "headers", "query", "json", "values"}, keys(cred))
	require.Equal(t, map[string]any{}, cred["query"])
	require.Nil(t, cred["json"])
	require.Equal(t, float64(30000), got["hints"].(map[string]any)["renew_before_ms"])

	resp.Proxy = &v1.ProxyAssignment{ProxyId: "pxy_1", Url: "http://u:p@203.0.113.10:8000", Kind: "residential", Region: "US"}
	resp.Credential.Json = structpb.NewNullValue()
	got = jsonMap(t, resp)
	proxy := got["proxy"].(map[string]any)
	require.ElementsMatch(t, []string{"proxy_id", "url", "kind", "region"}, keys(proxy))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNodeRequestJSON parses the request examples of the design doc.
func TestNodeRequestJSON(t *testing.T) {
	var acq v1.AcquireRequest
	require.NoError(t, unmarshalOpts.Unmarshal([]byte(`{"site":"shop","client":"web","uri":"/api/v1/search","session_key":"task-8842","wait_ms":0,"future_field":1}`), &acq))
	require.Equal(t, "task-8842", acq.GetSessionKey())

	var rep v1.ReportRequest
	require.NoError(t, unmarshalOpts.Unmarshal([]byte(`{"reports":[{"report_id":"2c6f0e5a-8d8b-4a52-9f0e-1b7f3c9d2a41","lease_id":"lse_0192a3f4c1d27b8e9a01","uri":"/x","method":"GET","http_status":200,"markers":[],"latency_ms":842,"response_bytes":48213,"started_at":"2026-09-16T08:30:11.120Z","finished_at":"2026-09-16T08:30:11.962Z","release":true}]}`), &rep))
	require.Len(t, rep.GetReports(), 1)
	require.Equal(t, int64(48213), rep.GetReports()[0].GetResponseBytes())
	require.NoError(t, protovalidate.Validate(rep.GetReports()[0]))

	var watch v1.WatchConfigRequest
	require.NoError(t, unmarshalOpts.Unmarshal([]byte(`{"namespace":"prod","items":[{"group":"crawler","key":"search.json","version":12},{"group":"_runtime","key":"breakers","version":348}],"timeout_ms":30000}`), &watch))
	require.NoError(t, protovalidate.Validate(&watch))

	var renew v1.RenewRequest
	require.NoError(t, unmarshalOpts.Unmarshal([]byte(`{"lease_id":"lse_1","extend_ms":120000}`), &renew))
	require.NoError(t, protovalidate.Validate(&renew))

	require.Equal(t, `{"accepted":1,"duplicated":0,"rejected":[]}`, compact(t, &v1.ReportResponse{Accepted: 1}))
}

func compact(t *testing.T, m proto.Message) string {
	t.Helper()
	b, err := json.Marshal(jsonMap(t, m))
	require.NoError(t, err)
	return string(b)
}

// TestInt64AsStrings documents the proto3 JSON mapping of int64 counters.
func TestInt64AsStrings(t *testing.T) {
	got := jsonMap(t, &v1.NodeStats{Node: "n1", Acquires: 5})
	require.Equal(t, "5", got["acquires"])
	summary := jsonMap(t, &v1.QueryRequestEventsResponse{Summary: &v1.RequestEventsSummary{Total: 7, Outcomes: map[string]int64{"success": 7}}})
	s := summary["summary"].(map[string]any)
	require.Equal(t, "7", s["total"])
	require.Equal(t, map[string]any{"success": "7"}, s["outcomes"])
}
