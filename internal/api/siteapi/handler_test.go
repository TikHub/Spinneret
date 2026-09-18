package siteapi

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/sitesvc"
	"github.com/TikHub/Spinneret/internal/testutil"
)

type memAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (m *memAudit) Record(_ context.Context, e audit.Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func (m *memAudit) find(action, result string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if e.Action == action && e.Result == result {
			return true
		}
	}
	return false
}

type nopHot struct{}

func (nopHot) SyncSite(context.Context, string) error  { return nil }
func (nopHot) RemoveSite(context.Context, int64) error { return nil }

type env struct {
	t        *testing.T
	h        *Handler
	cat      *catalog.Store
	audit    *memAudit
	tenantID string
	otherTen string
	ns       *catalog.Namespace
	otherNS  *catalog.Namespace
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	ctx := context.Background()
	e := &env{t: t, audit: &memAudit{}}
	e.tenantID = idgen.New(idgen.Tenant)
	e.otherTen = idgen.New(idgen.Tenant)
	nsID, otherNS := idgen.New(idgen.Namespace), idgen.New(idgen.Namespace)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, name) VALUES ($1, 'acme'), ($2, 'other')`, []any{e.tenantID, e.otherTen}},
		{`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod'), ($3, $4, 'prod')`, []any{nsID, e.tenantID, otherNS, e.otherTen}},
	} {
		_, err := pool.Exec(ctx, q.sql, q.args...)
		require.NoError(t, err)
	}
	e.cat = catalog.NewStore(pool, nil, nil)
	require.NoError(t, e.cat.ReloadAll(ctx))
	e.ns, _ = e.cat.Namespace(nsID)
	e.otherNS, _ = e.cat.Namespace(otherNS)
	svc := sitesvc.NewService(pool, e.cat, nopHot{}, e.audit, rdb, keys, nil)
	e.h = New(svc, e.cat, e.audit)
	return e
}

func (e *env) user(role authz.Role, siteIDs ...string) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: "usr_" + string(role), Name: string(role), TenantID: e.tenantID,
		Bindings: []authz.Binding{{ID: "rb_1", TenantID: e.tenantID, Role: role, NamespaceID: e.ns.ID, SiteIDs: siteIDs}},
	}
}

func (e *env) token(scopes ...string) *authz.Principal {
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(e.t, err)
	return &authz.Principal{Kind: authz.KindToken, ID: "tok_1", Name: "node", TenantID: e.tenantID,
		NamespaceID: e.ns.ID, NamespaceName: e.ns.Name, Scopes: parsed}
}

func as(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestSiteAdminLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := as(e.user(authz.RoleAdmin))

	created, err := e.h.CreateSite(admin, connect.NewRequest(&spinneretv1.CreateSiteRequest{
		Namespace: "prod", Name: "shop", DisplayName: "Shop (example site)", Clients: []string{"web", "app"},
	}))
	require.NoError(t, err)
	s := created.Msg.GetSite()
	require.Equal(t, "prod", s.GetNamespace())
	require.Equal(t, []string{"web", "app"}, s.GetClients())
	require.Equal(t, int32(2), s.GetEndpointGroupCount())
	require.NotNil(t, s.GetCreatedAt())
	require.Nil(t, s.GetPausedAt())
	require.True(t, e.audit.find(sitesvc.ActionSiteCreate, audit.ResultOK))

	byID, err := e.h.GetSite(admin, connect.NewRequest(&spinneretv1.GetSiteRequest{Id: s.GetId()}))
	require.NoError(t, err)
	require.True(t, proto.Equal(s, byID.Msg.GetSite()))
	byName, err := e.h.GetSite(admin, connect.NewRequest(&spinneretv1.GetSiteRequest{Namespace: "prod", Name: "shop"}))
	require.NoError(t, err)
	require.Equal(t, s.GetId(), byName.Msg.GetSite().GetId())

	display := "示例站点"
	updated, err := e.h.UpdateSite(admin, connect.NewRequest(&spinneretv1.UpdateSiteRequest{Id: s.GetId(), DisplayName: &display, Clients: []string{"web"}}))
	require.NoError(t, err)
	require.Equal(t, display, updated.Msg.GetSite().GetDisplayName())
	require.Equal(t, []string{"web"}, updated.Msg.GetSite().GetClients())

	group, err := e.h.CreateEndpointGroup(admin, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{
		Namespace: "prod", Site: "shop", Client: "web", Name: "search", LowWatermark: 3,
		Rules: []*spinneretv1.URIRule{{Kind: "prefix", Pattern: "/search/"}, {Kind: "exact", Pattern: "/s"}},
	}))
	require.NoError(t, err)
	g := group.Msg.GetEndpointGroup()
	require.Equal(t, "shop", g.GetSite())
	require.Equal(t, s.GetId(), g.GetSiteId())
	require.Equal(t, "closed", g.GetBreakerState())
	require.Len(t, g.GetRules(), 2)

	list, err := e.h.ListEndpointGroups(admin, connect.NewRequest(&spinneretv1.ListEndpointGroupsRequest{Namespace: "prod", Site: "shop", PageSize: 1}))
	require.NoError(t, err)
	require.Equal(t, int32(2), list.Msg.GetTotal())
	require.Len(t, list.Msg.GetEndpointGroups(), 1)
	require.Equal(t, site.DefaultGroup, list.Msg.GetEndpointGroups()[0].GetName())
	require.NotEmpty(t, list.Msg.GetNextPageToken())
	list, err = e.h.ListEndpointGroups(admin, connect.NewRequest(&spinneretv1.ListEndpointGroupsRequest{
		Namespace: "prod", Site: "shop", PageSize: 1, PageToken: list.Msg.GetNextPageToken()}))
	require.NoError(t, err)
	require.Equal(t, "search", list.Msg.GetEndpointGroups()[0].GetName())
	require.Empty(t, list.Msg.GetNextPageToken())

	low := int32(9)
	desc := "search endpoints"
	upd, err := e.h.UpdateEndpointGroup(admin, connect.NewRequest(&spinneretv1.UpdateEndpointGroupRequest{Id: g.GetId(), LowWatermark: &low, Description: &desc}))
	require.NoError(t, err)
	require.Equal(t, int32(9), upd.Msg.GetEndpointGroup().GetLowWatermark())
	require.Equal(t, desc, upd.Msg.GetEndpointGroup().GetDescription())

	replaced, err := e.h.ReplaceURIRules(admin, connect.NewRequest(&spinneretv1.ReplaceURIRulesRequest{
		EndpointGroupId: g.GetId(),
		Rules: []*spinneretv1.URIRule{
			{Kind: "template", Pattern: "/item/{id}"}, {Kind: "regex", Pattern: "^/x/[0-9]+$"}, {Kind: "prefix", Pattern: "/search/"},
		},
	}))
	require.NoError(t, err)
	require.Len(t, replaced.Msg.GetRules(), 3)

	rules, err := e.h.ListURIRules(admin, connect.NewRequest(&spinneretv1.ListURIRulesRequest{EndpointGroupId: g.GetId(), PageSize: 2}))
	require.NoError(t, err)
	require.Equal(t, int32(3), rules.Msg.GetTotal())
	require.Len(t, rules.Msg.GetRules(), 2)
	rules, err = e.h.ListURIRules(admin, connect.NewRequest(&spinneretv1.ListURIRulesRequest{EndpointGroupId: g.GetId(), PageToken: rules.Msg.GetNextPageToken()}))
	require.NoError(t, err)
	require.Len(t, rules.Msg.GetRules(), 1)
	require.Equal(t, "/search/", rules.Msg.GetRules()[0].GetPattern())
	require.Equal(t, int32(2), rules.Msg.GetRules()[0].GetPosition())

	tested, err := e.h.TestURI(admin, connect.NewRequest(&spinneretv1.TestURIRequest{Namespace: "prod", Site: "shop", Client: "web", Uri: "/item/42?x=1"}))
	require.NoError(t, err)
	require.Equal(t, "search", tested.Msg.GetEndpointGroup())
	require.Equal(t, g.GetId(), tested.Msg.GetEndpointGroupId())
	require.Equal(t, "template", tested.Msg.GetKind())
	require.Equal(t, replaced.Msg.GetRules()[0].GetId(), tested.Msg.GetRuleId())
	require.False(t, tested.Msg.GetIsDefault())
	require.Len(t, tested.Msg.GetPolicies(), 4)
	require.Equal(t, "rotation", tested.Msg.GetPolicies()[0].GetKind())
	require.Equal(t, "builtin", tested.Msg.GetPolicies()[0].GetLevel())
	require.Equal(t, "default-rotation", tested.Msg.GetPolicies()[0].GetName())

	sites, err := e.h.ListSites(admin, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Equal(t, int32(1), sites.Msg.GetTotal())
	require.Empty(t, sites.Msg.GetNextPageToken())

	_, err = e.h.DeleteEndpointGroup(admin, connect.NewRequest(&spinneretv1.DeleteEndpointGroupRequest{Id: g.GetId()}))
	require.NoError(t, err)
	_, err = e.h.DeleteSite(admin, connect.NewRequest(&spinneretv1.DeleteSiteRequest{Id: s.GetId()}))
	require.NoError(t, err)
	_, err = e.h.GetSite(admin, connect.NewRequest(&spinneretv1.GetSiteRequest{Id: s.GetId()}))
	requireReason(t, err, apperr.ReasonNotFound)

	// Error paths shared by every call.
	_, err = e.h.ListSites(admin, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "prod", PageToken: "!!"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.ListEndpointGroups(admin, connect.NewRequest(&spinneretv1.ListEndpointGroupsRequest{Namespace: "prod", Site: "missing"}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.h.ListSites(context.Background(), connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = e.h.GetSite(context.Background(), connect.NewRequest(&spinneretv1.GetSiteRequest{Id: "sit_x"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}
