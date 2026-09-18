package policyapi_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/policyapi"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvctest"
)

const signalYAML = `name: shop-signals
extends: default-signal
rules:
  - name: account-banned
    when: {markers: [account_banned]}
    outcome: banned
`

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equalf(t, reason, apperr.ReasonOf(err), "unexpected error: %v", err)
}

func newHandler(t *testing.T) (*policysvctest.Fixture, *policyapi.Handler) {
	t.Helper()
	f := policysvctest.New(t)
	return f, policyapi.New(f.Service, f.Catalog)
}

func TestHandlerPolicyLifecycle(t *testing.T) {
	t.Parallel()
	f, h := newHandler(t)
	op := policysvctest.Ctx(f.Operator)
	admin := policysvctest.Ctx(f.Admin)
	viewer := policysvctest.Ctx(f.Viewer)

	created, err := h.CreatePolicy(op, connect.NewRequest(&spinneretv1.CreatePolicyRequest{
		Namespace: policysvctest.NamespaceName, Kind: "signal", Yaml: signalYAML,
	}))
	require.NoError(t, err)
	pol := created.Msg.GetPolicy()
	require.Equal(t, "shop-signals", pol.GetName())
	require.Equal(t, policysvctest.NamespaceName, pol.GetNamespace())
	require.True(t, pol.GetHasDraft())
	require.Equal(t, signalYAML, pol.GetDraftYaml())
	require.NotNil(t, pol.GetCreatedAt())
	require.NotNil(t, pol.GetDraftUpdatedAt())

	// Operators may save drafts but not publish.
	_, err = h.SaveDraft(op, connect.NewRequest(&spinneretv1.SaveDraftRequest{Id: pol.GetId(), Yaml: signalYAML + "trust_outcome_hint: true\n"}))
	require.NoError(t, err)
	_, err = h.PublishPolicy(op, connect.NewRequest(&spinneretv1.PublishPolicyRequest{Id: pol.GetId()}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.CreatePolicy(op, connect.NewRequest(&spinneretv1.CreatePolicyRequest{
		Namespace: policysvctest.NamespaceName, Kind: "breaker", Yaml: "name: b\n", Publish: true,
	}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	pub, err := h.PublishPolicy(admin, connect.NewRequest(&spinneretv1.PublishPolicyRequest{Id: pol.GetId(), Comment: "v1", ExpectedVersion: 0}))
	require.NoError(t, err)
	require.Equal(t, int32(1), pub.Msg.GetPolicy().GetCurrentVersion())
	require.Nil(t, pub.Msg.GetPolicy().GetDraftUpdatedAt())

	_, err = h.SaveDraft(op, connect.NewRequest(&spinneretv1.SaveDraftRequest{Id: pol.GetId(), Yaml: signalYAML}))
	require.NoError(t, err)
	_, err = h.PublishPolicy(admin, connect.NewRequest(&spinneretv1.PublishPolicyRequest{Id: pol.GetId(), ExpectedVersion: 1}))
	require.NoError(t, err)

	// Versions with page tokens.
	var versions []int32
	token := ""
	for {
		resp, err := h.ListPolicyVersions(viewer, connect.NewRequest(&spinneretv1.ListPolicyVersionsRequest{Id: pol.GetId(), PageSize: 1, PageToken: token}))
		require.NoError(t, err)
		require.Equal(t, int32(2), resp.Msg.GetTotal())
		for _, v := range resp.Msg.GetVersions() {
			versions = append(versions, v.GetVersion())
			require.NotNil(t, v.GetCreatedAt())
		}
		token = resp.Msg.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Equal(t, []int32{2, 1}, versions)
	_, err = h.ListPolicyVersions(viewer, connect.NewRequest(&spinneretv1.ListPolicyVersionsRequest{Id: pol.GetId(), PageToken: "!!"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	diff, err := h.DiffPolicyVersions(viewer, connect.NewRequest(&spinneretv1.DiffPolicyVersionsRequest{Id: pol.GetId(), FromVersion: 1, ToVersion: 2}))
	require.NoError(t, err)
	require.Contains(t, diff.Msg.GetUnifiedDiff(), "-trust_outcome_hint: true")
	require.Contains(t, diff.Msg.GetFromYaml(), "trust_outcome_hint: true")

	rb, err := h.RollbackPolicy(admin, connect.NewRequest(&spinneretv1.RollbackPolicyRequest{Id: pol.GetId(), Version: 1, Comment: "back"}))
	require.NoError(t, err)
	require.Equal(t, int32(3), rb.Msg.GetPolicy().GetCurrentVersion())
	_, err = h.RollbackPolicy(op, connect.NewRequest(&spinneretv1.RollbackPolicyRequest{Id: pol.GetId(), Version: 2}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	got, err := h.GetPolicy(viewer, connect.NewRequest(&spinneretv1.GetPolicyRequest{Id: pol.GetId()}))
	require.NoError(t, err)
	require.Contains(t, got.Msg.GetPolicy().GetPublishedYaml(), "trust_outcome_hint: true")

	// Bindings.
	set, err := h.SetBinding(admin, connect.NewRequest(&spinneretv1.SetBindingRequest{
		PolicyId: pol.GetId(), Site: policysvctest.SiteShop, Client: "web", EndpointGroup: policysvctest.GroupSearch,
	}))
	require.NoError(t, err)
	b := set.Msg.GetBinding()
	require.Equal(t, "endpoint_group", b.GetLevel())
	require.Equal(t, "shop", b.GetSite())
	require.Equal(t, "search", b.GetEndpointGroup())
	require.Equal(t, f.Search.ID, b.GetEndpointGroupId())
	require.Equal(t, "shop-signals", b.GetPolicyName())
	require.Equal(t, "signal", b.GetKind())
	require.Equal(t, policysvctest.NamespaceName, b.GetNamespace())
	_, err = h.SetBinding(op, connect.NewRequest(&spinneretv1.SetBindingRequest{PolicyId: pol.GetId(), Site: policysvctest.SiteShop}))
	requireReason(t, err, apperr.ReasonPermissionDenied)

	list, err := h.ListBindings(viewer, connect.NewRequest(&spinneretv1.ListBindingsRequest{Namespace: policysvctest.NamespaceName, Kind: "signal"}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetBindings(), 2)
	require.Equal(t, "namespace", list.Msg.GetBindings()[0].GetLevel())

	res, err := h.ResolvePolicies(viewer, connect.NewRequest(&spinneretv1.ResolvePoliciesRequest{
		Namespace: policysvctest.NamespaceName, Site: policysvctest.SiteShop, Client: "web",
	}))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetPolicies(), 4)
	require.Equal(t, "rotation", res.Msg.GetPolicies()[0].GetKind())
	require.Equal(t, "default-signal", res.Msg.GetPolicies()[1].GetName())
	require.Equal(t, "namespace", res.Msg.GetPolicies()[1].GetLevel())
	require.Equal(t, int32(1), res.Msg.GetPolicies()[1].GetVersion())
	require.NotEmpty(t, res.Msg.GetPolicies()[1].GetPolicyId())

	_, err = h.DeleteBinding(op, connect.NewRequest(&spinneretv1.DeleteBindingRequest{Id: b.GetId()}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.DeleteBinding(admin, connect.NewRequest(&spinneretv1.DeleteBindingRequest{Id: b.GetId()}))
	require.NoError(t, err)

	// Listing with page tokens.
	var names []string
	token = ""
	for {
		resp, err := h.ListPolicies(viewer, connect.NewRequest(&spinneretv1.ListPoliciesRequest{
			Namespace: policysvctest.NamespaceName, PageSize: 2, PageToken: token,
		}))
		require.NoError(t, err)
		require.Equal(t, int32(5), resp.Msg.GetTotal())
		for _, p := range resp.Msg.GetPolicies() {
			names = append(names, p.GetName())
		}
		token = resp.Msg.GetNextPageToken()
		if token == "" {
			break
		}
	}
	require.Equal(t, []string{"default-action", "default-breaker", "default-rotation", "default-signal", "shop-signals"}, names)
	_, err = h.ListPolicies(viewer, connect.NewRequest(&spinneretv1.ListPoliciesRequest{Namespace: policysvctest.NamespaceName, PageToken: "%%%"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	_, err = h.DeletePolicy(op, connect.NewRequest(&spinneretv1.DeletePolicyRequest{Id: pol.GetId()}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.DeletePolicy(admin, connect.NewRequest(&spinneretv1.DeletePolicyRequest{Id: pol.GetId()}))
	require.NoError(t, err)
	_, err = h.GetPolicy(viewer, connect.NewRequest(&spinneretv1.GetPolicyRequest{Id: pol.GetId()}))
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestHandlerDebugAndValidate(t *testing.T) {
	t.Parallel()
	f, h := newHandler(t)
	viewer := policysvctest.Ctx(f.Viewer)

	draft, err := h.CreatePolicy(policysvctest.Ctx(f.Operator), connect.NewRequest(&spinneretv1.CreatePolicyRequest{
		Namespace: policysvctest.NamespaceName, Kind: "signal", Yaml: signalYAML,
	}))
	require.NoError(t, err)

	req := &spinneretv1.DebugReportRequest{
		Namespace: policysvctest.NamespaceName, Site: policysvctest.SiteShop, Client: "web",
		Target: &spinneretv1.DebugReportRequest_Uri{Uri: policysvctest.SearchPrefix + "/item"},
		Report: &spinneretv1.Report{Uri: "/x", Markers: []string{"captcha_page"}, HttpStatus: 200, LatencyMs: 10},
		Counts: map[string]int64{"identity:captcha:24h": 3},
	}
	resp, err := h.DebugReport(viewer, connect.NewRequest(req))
	require.NoError(t, err)
	msg := resp.Msg
	require.Equal(t, "captcha", msg.GetOutcome())
	require.Equal(t, "identity", msg.GetBlame())
	require.Equal(t, int32(2), msg.GetMatchedRuleIndex())
	require.Equal(t, "captcha", msg.GetMatchedRuleName())
	require.Equal(t, "enforce", msg.GetMode())
	require.Len(t, msg.GetActions(), 1)
	require.Equal(t, "ban", msg.GetActions()[0].GetAction())
	require.Equal(t, "identity", msg.GetActions()[0].GetScope())
	require.Equal(t, "12h", msg.GetActions()[0].GetDuration())
	require.Equal(t, int32(4), msg.GetActions()[0].GetSeverity())
	require.Equal(t, int32(3), msg.GetActions()[0].GetRuleIndex())
	require.Equal(t, "rule", msg.GetActions()[0].GetSource())
	require.Len(t, msg.GetCounters(), 1)
	require.Equal(t, "identity", msg.GetCounters()[0].GetSubject())
	require.Equal(t, "1d", msg.GetCounters()[0].GetWindow())

	// Draft signal policy + account ban with an explicit endpoint group.
	req = &spinneretv1.DebugReportRequest{
		Namespace: policysvctest.NamespaceName, Site: policysvctest.SiteShop, Client: "web",
		Target:        &spinneretv1.DebugReportRequest_EndpointGroup{EndpointGroup: "_default"},
		Report:        &spinneretv1.Report{Uri: "/feed", Markers: []string{"account_banned"}},
		HasAccount:    true,
		DraftPolicyId: draft.Msg.GetPolicy().GetId(),
	}
	resp, err = h.DebugReport(viewer, connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, "banned", resp.Msg.GetOutcome())
	require.Len(t, resp.Msg.GetActions(), 1)
	require.Equal(t, "account", resp.Msg.GetActions()[0].GetScope())
	require.True(t, resp.Msg.GetActions()[0].GetPermanent())
	require.Equal(t, "permanent", resp.Msg.GetActions()[0].GetDuration())

	// Errors: missing report, other namespace for a token, lease token scopes.
	_, err = h.DebugReport(viewer, connect.NewRequest(&spinneretv1.DebugReportRequest{
		Namespace: policysvctest.NamespaceName, Site: policysvctest.SiteShop, Client: "web",
	}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = h.DebugReport(policysvctest.Ctx(f.AdminToken), connect.NewRequest(&spinneretv1.DebugReportRequest{
		Namespace: "elsewhere", Site: policysvctest.SiteShop, Client: "web", Report: &spinneretv1.Report{Uri: "/"},
	}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.DebugReport(policysvctest.Ctx(f.LeaseToken), connect.NewRequest(&spinneretv1.DebugReportRequest{
		Site: policysvctest.SiteShop, Client: "web", Report: &spinneretv1.Report{Uri: "/"},
	}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.DebugReport(viewer, connect.NewRequest(&spinneretv1.DebugReportRequest{
		Namespace: "missing", Site: policysvctest.SiteShop, Client: "web", Report: &spinneretv1.Report{Uri: "/"},
	}))
	requireReason(t, err, apperr.ReasonNotFound)

	val, err := h.ValidatePolicy(viewer, connect.NewRequest(&spinneretv1.ValidatePolicyRequest{Kind: "breaker", Yaml: "name: b\nmin_requests: 5\n"}))
	require.NoError(t, err)
	require.True(t, val.Msg.GetValid())
	require.Contains(t, val.Msg.GetNormalizedYaml(), "min_requests: 5")
	val, err = h.ValidatePolicy(viewer, connect.NewRequest(&spinneretv1.ValidatePolicyRequest{Kind: "breaker", Yaml: "name: B\n"}))
	require.NoError(t, err)
	require.False(t, val.Msg.GetValid())
	require.NotEmpty(t, val.Msg.GetErrors())
	_, err = h.ValidatePolicy(policysvctest.Ctx(f.LeaseToken), connect.NewRequest(&spinneretv1.ValidatePolicyRequest{Kind: "breaker", Yaml: "name: b\n"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
}

func TestHandlerRequiresPrincipal(t *testing.T) {
	t.Parallel()
	f, h := newHandler(t)
	_ = f
	ctx := t.Context()
	ns := policysvctest.NamespaceName
	calls := map[string]func() error{
		"ListPolicies": func() error {
			_, err := h.ListPolicies(ctx, connect.NewRequest(&spinneretv1.ListPoliciesRequest{Namespace: ns}))
			return err
		},
		"GetPolicy": func() error {
			_, err := h.GetPolicy(ctx, connect.NewRequest(&spinneretv1.GetPolicyRequest{Id: "pol_x"}))
			return err
		},
		"CreatePolicy": func() error {
			_, err := h.CreatePolicy(ctx, connect.NewRequest(&spinneretv1.CreatePolicyRequest{Namespace: ns, Kind: "breaker", Yaml: "name: b\n"}))
			return err
		},
		"SaveDraft": func() error {
			_, err := h.SaveDraft(ctx, connect.NewRequest(&spinneretv1.SaveDraftRequest{Id: "pol_x", Yaml: "name: b\n"}))
			return err
		},
		"PublishPolicy": func() error {
			_, err := h.PublishPolicy(ctx, connect.NewRequest(&spinneretv1.PublishPolicyRequest{Id: "pol_x"}))
			return err
		},
		"RollbackPolicy": func() error {
			_, err := h.RollbackPolicy(ctx, connect.NewRequest(&spinneretv1.RollbackPolicyRequest{Id: "pol_x", Version: 1}))
			return err
		},
		"DeletePolicy": func() error {
			_, err := h.DeletePolicy(ctx, connect.NewRequest(&spinneretv1.DeletePolicyRequest{Id: "pol_x"}))
			return err
		},
		"ListPolicyVersions": func() error {
			_, err := h.ListPolicyVersions(ctx, connect.NewRequest(&spinneretv1.ListPolicyVersionsRequest{Id: "pol_x"}))
			return err
		},
		"DiffPolicyVersions": func() error {
			_, err := h.DiffPolicyVersions(ctx, connect.NewRequest(&spinneretv1.DiffPolicyVersionsRequest{Id: "pol_x"}))
			return err
		},
		"ListBindings": func() error {
			_, err := h.ListBindings(ctx, connect.NewRequest(&spinneretv1.ListBindingsRequest{Namespace: ns}))
			return err
		},
		"SetBinding": func() error {
			_, err := h.SetBinding(ctx, connect.NewRequest(&spinneretv1.SetBindingRequest{PolicyId: "pol_x"}))
			return err
		},
		"DeleteBinding": func() error {
			_, err := h.DeleteBinding(ctx, connect.NewRequest(&spinneretv1.DeleteBindingRequest{Id: "pbd_x"}))
			return err
		},
		"ResolvePolicies": func() error {
			_, err := h.ResolvePolicies(ctx, connect.NewRequest(&spinneretv1.ResolvePoliciesRequest{Namespace: ns}))
			return err
		},
		"DebugReport": func() error {
			_, err := h.DebugReport(ctx, connect.NewRequest(&spinneretv1.DebugReportRequest{Namespace: ns}))
			return err
		},
		"ValidatePolicy": func() error {
			_, err := h.ValidatePolicy(ctx, connect.NewRequest(&spinneretv1.ValidatePolicyRequest{Kind: "breaker", Yaml: "name: b\n"}))
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireReason(t, call(), apperr.ReasonSessionInvalid)
		})
	}

	// A viewer of another tenant is denied on ID-based and namespace-based calls.
	other := policysvctest.Ctx(policysvctest.User("usr_x", authz.Binding{TenantID: f.Namespace.TenantID, Role: authz.RoleViewer, NamespaceID: "ns_other"}))
	_, err := h.ListPolicies(other, connect.NewRequest(&spinneretv1.ListPoliciesRequest{Namespace: ns}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.CreatePolicy(other, connect.NewRequest(&spinneretv1.CreatePolicyRequest{Namespace: ns, Kind: "breaker", Yaml: "name: b\n"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.ListBindings(other, connect.NewRequest(&spinneretv1.ListBindingsRequest{Namespace: "nope"}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = h.ResolvePolicies(other, connect.NewRequest(&spinneretv1.ResolvePoliciesRequest{Namespace: ns}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
}
