package siteapi

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/sitesvc"
)

// permFixture has two sites (a, b) in the tenant's namespace, one site in
// another tenant, and one non-default endpoint group per site.
type permFixture struct {
	*env
	siteA, siteB, foreign string
	groupA, groupB        string
	foreignGroup          string
}

func newPermFixture(t *testing.T) permFixture {
	t.Helper()
	e := newEnv(t)
	f := permFixture{env: e}
	admin := as(e.user(authz.RoleAdmin))
	create := func(ctx context.Context, ns, name string) (string, string) {
		s, err := e.h.CreateSite(ctx, connect.NewRequest(&spinneretv1.CreateSiteRequest{Namespace: ns, Name: name}))
		require.NoError(t, err)
		g, err := e.h.CreateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{
			Namespace: ns, Site: name, Client: "web", Name: "search",
			Rules: []*spinneretv1.URIRule{{Kind: "prefix", Pattern: "/search/"}},
		}))
		require.NoError(t, err)
		return s.Msg.GetSite().GetId(), g.Msg.GetEndpointGroup().GetId()
	}
	f.siteA, f.groupA = create(admin, "prod", "a")
	f.siteB, f.groupB = create(admin, "prod", "b")
	otherAdmin := &authz.Principal{Kind: authz.KindUser, ID: "usr_o", TenantID: e.otherTen,
		Bindings: []authz.Binding{{TenantID: e.otherTen, Role: authz.RoleOwner}}}
	f.foreign, f.foreignGroup = create(as(otherAdmin), "prod", "a")
	return f
}

func TestSiteAdminPermissions(t *testing.T) {
	f := newPermFixture(t)
	restrictedAdmin := f.user(authz.RoleAdmin, f.siteA)
	restrictedViewer := f.user(authz.RoleViewer, f.siteA)
	viewer := f.user(authz.RoleViewer)
	operator := f.user(authz.RoleOperator)
	adminToken := f.token("admin")
	nodeToken := f.token("lease:acquire", "identity:write:a")
	noTenant := &authz.Principal{Kind: authz.KindUser, ID: "usr_x", Bindings: viewer.Bindings}
	platformAdmin := &authz.Principal{Kind: authz.KindUser, ID: "usr_p", IsPlatformAdmin: true}

	type call func(ctx context.Context) error
	h := f.h
	getSite := func(id string) call {
		return func(ctx context.Context) error {
			_, err := h.GetSite(ctx, connect.NewRequest(&spinneretv1.GetSiteRequest{Id: id}))
			return err
		}
	}
	getSiteByName := func(name string) call {
		return func(ctx context.Context) error {
			_, err := h.GetSite(ctx, connect.NewRequest(&spinneretv1.GetSiteRequest{Namespace: "prod", Name: name}))
			return err
		}
	}
	createSite := func(ctx context.Context) error {
		_, err := h.CreateSite(ctx, connect.NewRequest(&spinneretv1.CreateSiteRequest{Namespace: "prod", Name: "new"}))
		return err
	}
	updateSite := func(id string) call {
		return func(ctx context.Context) error {
			d := "x"
			_, err := h.UpdateSite(ctx, connect.NewRequest(&spinneretv1.UpdateSiteRequest{Id: id, Description: &d}))
			return err
		}
	}
	deleteSite := func(id string) call {
		return func(ctx context.Context) error {
			_, err := h.DeleteSite(ctx, connect.NewRequest(&spinneretv1.DeleteSiteRequest{Id: id}))
			return err
		}
	}
	listGroups := func(site string) call {
		return func(ctx context.Context) error {
			_, err := h.ListEndpointGroups(ctx, connect.NewRequest(&spinneretv1.ListEndpointGroupsRequest{Namespace: "prod", Site: site}))
			return err
		}
	}
	createGroup := func(site string) call {
		return func(ctx context.Context) error {
			_, err := h.CreateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{Namespace: "prod", Site: site, Client: "web", Name: "detail"}))
			return err
		}
	}
	updateGroup := func(id string) call {
		return func(ctx context.Context) error {
			d := "x"
			_, err := h.UpdateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.UpdateEndpointGroupRequest{Id: id, Description: &d}))
			return err
		}
	}
	deleteGroup := func(id string) call {
		return func(ctx context.Context) error {
			_, err := h.DeleteEndpointGroup(ctx, connect.NewRequest(&spinneretv1.DeleteEndpointGroupRequest{Id: id}))
			return err
		}
	}
	listRules := func(id string) call {
		return func(ctx context.Context) error {
			_, err := h.ListURIRules(ctx, connect.NewRequest(&spinneretv1.ListURIRulesRequest{EndpointGroupId: id}))
			return err
		}
	}
	replaceRules := func(id string) call {
		return func(ctx context.Context) error {
			_, err := h.ReplaceURIRules(ctx, connect.NewRequest(&spinneretv1.ReplaceURIRulesRequest{EndpointGroupId: id,
				Rules: []*spinneretv1.URIRule{{Kind: "prefix", Pattern: "/search/"}}}))
			return err
		}
	}
	testURI := func(site string) call {
		return func(ctx context.Context) error {
			_, err := h.TestURI(ctx, connect.NewRequest(&spinneretv1.TestURIRequest{Namespace: "prod", Site: site, Client: "web", Uri: "/search/x"}))
			return err
		}
	}

	const ok apperr.Reason = ""
	tests := []struct {
		name      string
		principal *authz.Principal
		call      call
		want      apperr.Reason
	}{
		{"viewer reads site", viewer, getSite(f.siteB), ok},
		{"viewer reads site by name", viewer, getSiteByName("b"), ok},
		{"viewer cannot create site", viewer, createSite, apperr.ReasonPermissionDenied},
		{"operator cannot update site", operator, updateSite(f.siteA), apperr.ReasonPermissionDenied},
		{"viewer cannot create group", viewer, createGroup("a"), apperr.ReasonPermissionDenied},
		{"viewer cannot replace rules", viewer, replaceRules(f.groupA), apperr.ReasonPermissionDenied},
		{"viewer reads rules", viewer, listRules(f.groupB), ok},
		{"viewer tests uri", viewer, testURI("b"), ok},

		{"restricted viewer reads own site", restrictedViewer, getSite(f.siteA), ok},
		{"restricted viewer cannot read other site", restrictedViewer, getSite(f.siteB), apperr.ReasonPermissionDenied},
		{"restricted viewer cannot read other site by name", restrictedViewer, getSiteByName("b"), apperr.ReasonPermissionDenied},
		{"restricted viewer cannot list other groups", restrictedViewer, listGroups("b"), apperr.ReasonPermissionDenied},
		{"restricted viewer cannot list other rules", restrictedViewer, listRules(f.groupB), apperr.ReasonPermissionDenied},
		{"restricted viewer cannot test other uri", restrictedViewer, testURI("b"), apperr.ReasonPermissionDenied},
		{"restricted viewer lists own groups", restrictedViewer, listGroups("a"), ok},

		{"restricted admin cannot create site", restrictedAdmin, createSite, apperr.ReasonPermissionDenied},
		{"restricted admin cannot update own site", restrictedAdmin, updateSite(f.siteA), apperr.ReasonPermissionDenied},
		{"restricted admin cannot delete own site", restrictedAdmin, deleteSite(f.siteA), apperr.ReasonPermissionDenied},
		{"restricted admin creates group on own site", restrictedAdmin, createGroup("a"), ok},
		{"restricted admin cannot create group on other site", restrictedAdmin, createGroup("b"), apperr.ReasonPermissionDenied},
		{"restricted admin updates own group", restrictedAdmin, updateGroup(f.groupA), ok},
		{"restricted admin cannot update other group", restrictedAdmin, updateGroup(f.groupB), apperr.ReasonPermissionDenied},
		{"restricted admin replaces own rules", restrictedAdmin, replaceRules(f.groupA), ok},
		{"restricted admin cannot replace other rules", restrictedAdmin, replaceRules(f.groupB), apperr.ReasonPermissionDenied},
		{"restricted admin cannot delete other group", restrictedAdmin, deleteGroup(f.groupB), apperr.ReasonPermissionDenied},

		{"admin token reads", adminToken, listGroups("b"), ok},
		{"admin token updates site", adminToken, updateSite(f.siteB), ok},
		{"node token cannot read sites", nodeToken, getSite(f.siteA), apperr.ReasonScopeMissing},
		{"node token cannot test uri", nodeToken, testURI("a"), apperr.ReasonScopeMissing},
		{"node token cannot replace rules", nodeToken, replaceRules(f.groupA), apperr.ReasonScopeMissing},

		{"foreign site is hidden", viewer, getSite(f.foreign), apperr.ReasonNotFound},
		{"foreign group is hidden", viewer, listRules(f.foreignGroup), apperr.ReasonNotFound},
		{"foreign site hidden from token", adminToken, deleteSite(f.foreign), apperr.ReasonNotFound},
		{"foreign group hidden from token", adminToken, updateGroup(f.foreignGroup), apperr.ReasonNotFound},
		{"user without active tenant", noTenant, getSite(f.siteA), apperr.ReasonInvalidArgument},
		{"user without active tenant by group", noTenant, listRules(f.groupA), apperr.ReasonInvalidArgument},
		{"platform admin without tenant sees every site", platformAdmin, getSite(f.foreign), ok},
		{"missing group", viewer, listRules("eg_missing"), apperr.ReasonNotFound},
		{"missing site for test uri", viewer, testURI("missing"), apperr.ReasonSiteUnknown},
		{"missing site by name", viewer, getSiteByName("missing"), apperr.ReasonNotFound},
		{"missing site groups", viewer, listGroups("missing"), apperr.ReasonNotFound},
		{"admin creates group on missing site", f.user(authz.RoleAdmin), createGroup("missing"), apperr.ReasonNotFound},

		// Principals restricted to some sites cannot tell a missing site name
		// from an existing site they cannot access.
		{"restricted viewer probes missing site", restrictedViewer, getSiteByName("missing"), apperr.ReasonPermissionDenied},
		{"restricted viewer probes missing site groups", restrictedViewer, listGroups("missing"), apperr.ReasonPermissionDenied},
		{"restricted viewer probes missing site uri", restrictedViewer, testURI("missing"), apperr.ReasonPermissionDenied},
		{"restricted admin probes missing site", restrictedAdmin, createGroup("missing"), apperr.ReasonPermissionDenied},
		{"viewer cannot probe with write", viewer, createGroup("missing"), apperr.ReasonPermissionDenied},
		{"node token probes missing site", nodeToken, getSiteByName("missing"), apperr.ReasonScopeMissing},
		{"node token probes missing site uri", nodeToken, testURI("missing"), apperr.ReasonScopeMissing},
		{"admin token sees missing site", adminToken, getSiteByName("missing"), apperr.ReasonNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(as(tc.principal))
			if tc.want == ok {
				require.NoError(t, err)
				return
			}
			requireReason(t, err, tc.want)
		})
	}

	require.True(t, f.audit.find(sitesvc.ActionSiteCreate, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionSiteUpdate, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionSiteDelete, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionEndpointGroupCreate, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionEndpointGroupUpdate, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionEndpointGroupDelete, audit.ResultDenied))
	require.True(t, f.audit.find(sitesvc.ActionURIRulesReplace, audit.ResultDenied))
}

func TestListSitesFiltering(t *testing.T) {
	f := newPermFixture(t)
	list := func(p *authz.Principal) (*spinneretv1.ListSitesResponse, error) {
		resp, err := f.h.ListSites(as(p), connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "prod"}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	names := func(resp *spinneretv1.ListSitesResponse) []string {
		out := []string{}
		for _, s := range resp.GetSites() {
			out = append(out, s.GetName())
		}
		return out
	}

	all, err := list(f.user(authz.RoleViewer))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, names(all))
	require.Equal(t, int32(2), all.GetTotal())

	restricted, err := list(f.user(authz.RoleViewer, f.siteB))
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, names(restricted))
	require.Equal(t, int32(1), restricted.GetTotal())

	token, err := list(f.token("admin"))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, names(token))

	_, err = list(f.token("lease:acquire"))
	requireReason(t, err, apperr.ReasonScopeMissing)

	// A binding restricted to sites of another namespace sees nothing here.
	elsewhere := &authz.Principal{Kind: authz.KindUser, ID: "usr_e", TenantID: f.tenantID,
		Bindings: []authz.Binding{{TenantID: f.tenantID, Role: authz.RoleViewer, SiteIDs: []string{"sit_elsewhere"}}}}
	empty, err := list(elsewhere)
	require.NoError(t, err)
	require.Empty(t, empty.GetSites())

	noBinding := &authz.Principal{Kind: authz.KindUser, ID: "usr_n", TenantID: f.tenantID}
	_, err = list(noBinding)
	requireReason(t, err, apperr.ReasonPermissionDenied)

	// Paging with a page size of one.
	first, err := f.h.ListSites(as(f.user(authz.RoleViewer)), connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "prod", PageSize: 1}))
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, names(first.Msg))
	second, err := f.h.ListSites(as(f.user(authz.RoleViewer)), connect.NewRequest(&spinneretv1.ListSitesRequest{
		Namespace: "prod", PageSize: 1, PageToken: first.Msg.GetNextPageToken()}))
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, names(second.Msg))
	require.Empty(t, second.Msg.GetNextPageToken())

	// Namespaces resolve inside the active tenant only.
	_, err = f.h.ListSites(as(f.user(authz.RoleViewer)), connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "missing"}))
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestClampInt32(t *testing.T) {
	require.Equal(t, int32(2147483647), clampInt32(1<<40))
	require.Equal(t, int32(-2147483648), clampInt32(-(1 << 40)))
	require.Equal(t, int32(5), clampInt32(5))
}
