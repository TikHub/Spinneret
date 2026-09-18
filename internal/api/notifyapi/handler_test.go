package notifyapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/notify"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

type fixture struct {
	t        *testing.T
	h        *Handler
	svc      *notify.Service
	tenantID string
	other    string
	prod     *catalog.Namespace
	staging  *catalog.Namespace
	shop     *catalog.Site
	forum    *catalog.Site
	audit    *auditLog
}

type auditLog struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *auditLog) Record(_ context.Context, e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	cat := catalogtest.New()
	f := &fixture{t: t, audit: &auditLog{}}
	exec := func(sql string, args ...any) {
		_, err := pool.Exec(context.Background(), sql, args...)
		require.NoError(t, err)
	}
	f.tenantID, f.other = idgen.New(idgen.Tenant), idgen.New(idgen.Tenant)
	exec(`INSERT INTO tenants (id, name) VALUES ($1, 'acme'), ($2, 'globex')`, f.tenantID, f.other)
	addNamespace := func(tenantID, name string) *catalog.Namespace {
		id := idgen.New(idgen.Namespace)
		exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, id, tenantID, name)
		ns := catalogtest.NewNamespace(tenantID, id, name)
		cat.Put(ns)
		return ns
	}
	addSite := func(ns *catalog.Namespace, name string, key int64) *catalog.Site {
		id := idgen.New(idgen.Site)
		exec(`INSERT INTO sites (id, namespace_id, name) VALUES ($1, $2, $3)`, id, ns.ID, name)
		return catalogtest.AddSite(ns, id, name, key, "web")
	}
	f.prod = addNamespace(f.tenantID, "prod")
	f.staging = addNamespace(f.tenantID, "staging")
	addNamespace(f.other, "prod")
	f.shop = addSite(f.prod, "shop", 1)
	f.forum = addSite(f.prod, "forum", 2)

	f.svc = notify.New(notify.Config{RetryDelay: time.Millisecond}, pool, vaulttest.NewCipher(t), rdb, keys, cat,
		events.NewMemoryBus(), f.audit, nil, slog.New(slog.DiscardHandler))
	f.h = New(f.svc, cat, nil)
	return f
}

// Principals.

func (f *fixture) user(role authz.Role, namespaceID string, siteIDs ...string) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindUser, ID: "usr_" + string(role), Name: string(role), TenantID: f.tenantID,
		Bindings: []authz.Binding{{ID: "rb", TenantID: f.tenantID, Role: role, NamespaceID: namespaceID, SiteIDs: siteIDs}},
	})
}

func (f *fixture) owner() context.Context { return f.user(authz.RoleOwner, "") }

func (f *fixture) token(ns *catalog.Namespace, scopes ...string) context.Context {
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(f.t, err)
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindToken, ID: "tok_1", Name: "node", TenantID: ns.TenantID,
		NamespaceID: ns.ID, NamespaceName: ns.Name, Scopes: parsed,
	})
}

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	require.NoError(t, err)
	return s
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), err.Error())
}

func (f *fixture) create(ctx context.Context, req *spinneretv1.CreateChannelRequest) (*spinneretv1.Channel, error) {
	resp, err := f.h.CreateChannel(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetChannel(), nil
}

func webhookRequest(t *testing.T, namespace, name, url string, sites ...string) *spinneretv1.CreateChannelRequest {
	return &spinneretv1.CreateChannelRequest{
		Namespace: namespace, Name: name, Kind: notify.ChannelWebhook,
		Config:     mustStruct(t, map[string]any{"url": url, "secret": "hook-secret-1234", "headers": map[string]any{"X-Api-Key": "key-abcd"}}),
		EventTypes: []string{notify.KindBanSpike, notify.KindTest},
		Sites:      sites,
	}
}

type hookServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	sigs   []string
}

func newHookServer(t *testing.T) *hookServer {
	t.Helper()
	hs := &hookServer{}
	hs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hs.mu.Lock()
		hs.bodies = append(hs.bodies, body)
		hs.sigs = append(hs.sigs, r.Header.Get(notify.HeaderSignature)+"|"+r.Header.Get(notify.HeaderTimestamp)+"|"+r.Header.Get("X-Api-Key"))
		hs.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(hs.Close)
	return hs
}

func TestChannelLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hook := newHookServer(t)

	ch, err := f.create(f.owner(), webhookRequest(t, "prod", "ops", hook.URL+"/hook", "forum", "shop"))
	require.NoError(t, err)
	require.Equal(t, "prod", ch.GetNamespace())
	require.Equal(t, []string{"forum", "shop"}, ch.GetSites())
	require.True(t, ch.GetEnabled(), "enabled defaults to true")
	require.Equal(t, notify.SeverityWarning, ch.GetMinSeverity())
	cfg := ch.GetConfig().AsMap()
	require.Equal(t, "••••1234", cfg["secret"])
	require.Equal(t, map[string]any{"X-Api-Key": "••••abcd"}, cfg["headers"])
	require.NotContains(t, cfg["url"], "/hook")
	require.NotNil(t, ch.GetCreatedAt())

	disabled := false
	tenantWide, err := f.create(f.owner(), &spinneretv1.CreateChannelRequest{
		Name: "tenant-tg", Kind: notify.ChannelTelegram, Enabled: &disabled, MinSeverity: notify.SeverityCritical,
		Config:     mustStruct(t, map[string]any{"bot_token": "123:secret-token", "chat_id": "-100"}),
		EventTypes: []string{notify.KindReportBacklog},
	})
	require.NoError(t, err)
	require.Empty(t, tenantWide.GetNamespace())
	require.False(t, tenantWide.GetEnabled())
	require.Equal(t, "••••oken", tenantWide.GetConfig().AsMap()["bot_token"])

	// Update with the masked config keeps the secrets (verified by a signed test delivery).
	upd, err := f.h.UpdateChannel(f.owner(), connect.NewRequest(&spinneretv1.UpdateChannelRequest{
		Id: ch.GetId(), Name: "ops2", Config: ch.GetConfig(), EventTypes: []string{notify.KindTest},
		Sites: []string{"shop"}, MinSeverity: notify.SeverityInfo, Enabled: true,
	}))
	require.NoError(t, err)
	require.Equal(t, "ops2", upd.Msg.GetChannel().GetName())
	require.Equal(t, []string{"shop"}, upd.Msg.GetChannel().GetSites())
	require.Equal(t, cfg, upd.Msg.GetChannel().GetConfig().AsMap())

	test, err := f.h.TestChannel(f.owner(), connect.NewRequest(&spinneretv1.TestChannelRequest{Id: ch.GetId()}))
	require.NoError(t, err)
	d := test.Msg.GetDelivery()
	require.True(t, d.GetOk(), d.GetError())
	require.Equal(t, "ops2", d.GetChannelName())
	require.NotNil(t, d.GetAt())
	hook.mu.Lock()
	require.Len(t, hook.bodies, 1)
	parts := hook.sigs[0]
	body := hook.bodies[0]
	hook.mu.Unlock()
	fields := strings.Split(parts, "|")
	require.Len(t, fields, 3)
	require.Equal(t, "key-abcd", fields[2], "masked headers keep their stored value")
	require.Equal(t, notify.WebhookSignature("hook-secret-1234", fields[1], body), fields[0])

	// Update without config keeps it; tenant-wide channels cannot take sites.
	_, err = f.h.UpdateChannel(f.owner(), connect.NewRequest(&spinneretv1.UpdateChannelRequest{
		Id: tenantWide.GetId(), Name: "tenant-tg", EventTypes: []string{notify.KindTest}, Sites: []string{"shop"},
	}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.h.UpdateChannel(f.owner(), connect.NewRequest(&spinneretv1.UpdateChannelRequest{
		Id: ch.GetId(), Name: "ops2", EventTypes: []string{notify.KindTest}, Sites: []string{"nope"},
	}))
	requireReason(t, err, apperr.ReasonSiteUnknown)

	// List with pagination.
	list, err := f.h.ListChannels(f.owner(), connect.NewRequest(&spinneretv1.ListChannelsRequest{PageSize: 1}))
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Msg.GetTotal())
	require.Len(t, list.Msg.GetChannels(), 1)
	require.Equal(t, "ops2", list.Msg.GetChannels()[0].GetName())
	require.NotEmpty(t, list.Msg.GetNextPageToken())
	list, err = f.h.ListChannels(f.owner(), connect.NewRequest(&spinneretv1.ListChannelsRequest{PageSize: 1, PageToken: list.Msg.GetNextPageToken()}))
	require.NoError(t, err)
	require.Equal(t, "tenant-tg", list.Msg.GetChannels()[0].GetName())
	require.Empty(t, list.Msg.GetNextPageToken())
	_, err = f.h.ListChannels(f.owner(), connect.NewRequest(&spinneretv1.ListChannelsRequest{PageToken: "!!"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	list, err = f.h.ListChannels(f.owner(), connect.NewRequest(&spinneretv1.ListChannelsRequest{Namespace: "staging"}))
	require.NoError(t, err)
	require.Empty(t, list.Msg.GetChannels())

	// Delete.
	_, err = f.h.DeleteChannel(f.owner(), connect.NewRequest(&spinneretv1.DeleteChannelRequest{Id: ch.GetId()}))
	require.NoError(t, err)
	_, err = f.h.DeleteChannel(f.owner(), connect.NewRequest(&spinneretv1.DeleteChannelRequest{Id: ch.GetId()}))
	requireReason(t, err, apperr.ReasonNotFound)

	f.audit.mu.Lock()
	actions := make([]string, 0, len(f.audit.entries))
	for _, e := range f.audit.entries {
		actions = append(actions, e.Action)
	}
	f.audit.mu.Unlock()
	require.Equal(t, []string{notify.AuditChannelCreate, notify.AuditChannelCreate, notify.AuditChannelUpdate,
		notify.AuditChannelTest, notify.AuditChannelDelete}, actions)
}

func TestCreateChannelValidation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.create(f.owner(), webhookRequest(t, "missing", "x", "https://x"))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.create(f.owner(), webhookRequest(t, "prod", "x", "https://x", "nope"))
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = f.create(f.owner(), webhookRequest(t, "", "x", "https://x", "shop"))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	req := webhookRequest(t, "prod", "x", "https://x")
	req.Config = nil
	_, err = f.create(f.owner(), req)
	requireReason(t, err, apperr.ReasonInvalidArgument)
	req = webhookRequest(t, "prod", "x", "ftp://x")
	_, err = f.create(f.owner(), req)
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.create(f.owner(), webhookRequest(t, "prod", "dup", "https://x"))
	require.NoError(t, err)
	_, err = f.create(f.owner(), webhookRequest(t, "staging", "dup", "https://x"))
	requireReason(t, err, apperr.ReasonAlreadyExists)
}

func TestChannelPermissions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	prodCh, err := f.create(f.owner(), webhookRequest(t, "prod", "prod-hook", "https://x/prod"))
	require.NoError(t, err)
	stagingCh, err := f.create(f.owner(), webhookRequest(t, "staging", "staging-hook", "https://x/staging"))
	require.NoError(t, err)
	tenantCh, err := f.create(f.owner(), webhookRequest(t, "", "tenant-hook", "https://x/tenant"))
	require.NoError(t, err)

	viewer := f.user(authz.RoleViewer, "")
	prodAdmin := f.user(authz.RoleAdmin, f.prod.ID)
	siteViewer := f.user(authz.RoleViewer, f.prod.ID, f.shop.ID)
	adminToken := f.token(f.prod, "admin")
	leaseToken := f.token(f.prod, "lease:acquire")
	noTenant := authz.WithPrincipal(context.Background(), &authz.Principal{Kind: authz.KindUser, ID: "usr_x", IsPlatformAdmin: true})
	foreign := authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindUser, ID: "usr_f", TenantID: f.other,
		Bindings: []authz.Binding{{TenantID: f.other, Role: authz.RoleOwner}},
	})
	outsider := authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindUser, ID: "usr_o", TenantID: f.tenantID,
		Bindings: []authz.Binding{{TenantID: f.other, Role: authz.RoleOwner}},
	})

	// Create.
	for name, tc := range map[string]struct {
		ctx    context.Context
		req    *spinneretv1.CreateChannelRequest
		reason apperr.Reason
	}{
		"viewer":                   {viewer, webhookRequest(t, "prod", "v", "https://x"), apperr.ReasonPermissionDenied},
		"ns admin tenant-wide":     {prodAdmin, webhookRequest(t, "", "a", "https://x"), apperr.ReasonPermissionDenied},
		"ns admin other namespace": {prodAdmin, webhookRequest(t, "staging", "a", "https://x"), apperr.ReasonPermissionDenied},
		"token tenant-wide":        {adminToken, webhookRequest(t, "", "t", "https://x"), apperr.ReasonScopeMissing},
		"token other namespace":    {adminToken, webhookRequest(t, "staging", "t", "https://x"), apperr.ReasonScopeMissing},
		"token without scope":      {leaseToken, webhookRequest(t, "prod", "t", "https://x"), apperr.ReasonScopeMissing},
		"no active tenant":         {noTenant, webhookRequest(t, "prod", "t", "https://x"), apperr.ReasonInvalidArgument},
		"unauthenticated":          {context.Background(), webhookRequest(t, "prod", "t", "https://x"), apperr.ReasonSessionInvalid},
		"outsider":                 {outsider, webhookRequest(t, "prod", "t", "https://x"), apperr.ReasonPermissionDenied},
	} {
		_, err := f.create(tc.ctx, tc.req)
		require.Equal(t, tc.reason, apperr.ReasonOf(err), name)
	}
	_, err = f.create(prodAdmin, webhookRequest(t, "prod", "by-ns-admin", "https://x"))
	require.NoError(t, err)
	_, err = f.create(adminToken, webhookRequest(t, "prod", "by-token", "https://x"))
	require.NoError(t, err)

	// Mutations by ID.
	mutations := map[string]func(ctx context.Context, id string) error{
		"update": func(ctx context.Context, id string) error {
			_, err := f.h.UpdateChannel(ctx, connect.NewRequest(&spinneretv1.UpdateChannelRequest{Id: id, Name: "renamed", EventTypes: []string{notify.KindTest}}))
			return err
		},
		"test": func(ctx context.Context, id string) error {
			_, err := f.h.TestChannel(ctx, connect.NewRequest(&spinneretv1.TestChannelRequest{Id: id}))
			return err
		},
		"delete": func(ctx context.Context, id string) error {
			_, err := f.h.DeleteChannel(ctx, connect.NewRequest(&spinneretv1.DeleteChannelRequest{Id: id}))
			return err
		},
	}
	for _, mutate := range mutations {
		requireReason(t, mutate(viewer, prodCh.GetId()), apperr.ReasonPermissionDenied)
		requireReason(t, mutate(prodAdmin, stagingCh.GetId()), apperr.ReasonPermissionDenied)
		requireReason(t, mutate(prodAdmin, tenantCh.GetId()), apperr.ReasonPermissionDenied)
		requireReason(t, mutate(adminToken, tenantCh.GetId()), apperr.ReasonScopeMissing)
		requireReason(t, mutate(leaseToken, prodCh.GetId()), apperr.ReasonScopeMissing)
		requireReason(t, mutate(foreign, prodCh.GetId()), apperr.ReasonNotFound)
		requireReason(t, mutate(f.owner(), "nch_missing"), apperr.ReasonNotFound)
		requireReason(t, mutate(noTenant, prodCh.GetId()), apperr.ReasonInvalidArgument)
		requireReason(t, mutate(context.Background(), prodCh.GetId()), apperr.ReasonSessionInvalid)
	}
	require.NoError(t, mutations["update"](prodAdmin, prodCh.GetId()))
	require.NoError(t, mutations["update"](adminToken, prodCh.GetId()))

	// List visibility.
	names := func(ctx context.Context, namespace string) ([]string, error) {
		resp, err := f.h.ListChannels(ctx, connect.NewRequest(&spinneretv1.ListChannelsRequest{Namespace: namespace}))
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, ch := range resp.Msg.GetChannels() {
			out = append(out, ch.GetName())
		}
		return out, nil
	}
	got, err := names(viewer, "")
	require.NoError(t, err)
	require.Equal(t, []string{"by-ns-admin", "by-token", "renamed", "staging-hook", "tenant-hook"}, got)
	got, err = names(prodAdmin, "")
	require.NoError(t, err)
	require.Equal(t, []string{"by-ns-admin", "by-token", "renamed"}, got)
	got, err = names(adminToken, "")
	require.NoError(t, err)
	require.Equal(t, []string{"by-ns-admin", "by-token", "renamed"}, got)
	_, err = names(prodAdmin, "staging")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = names(siteViewer, "")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = names(siteViewer, "prod")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = names(leaseToken, "")
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = names(adminToken, "staging")
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = names(noTenant, "")
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = names(context.Background(), "")
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestListAlertEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	emit := func(a notify.Alert) {
		a.TenantID = f.tenantID
		a.Details = map[string]any{"n": 1, "who": a.Title}
		require.NoError(t, f.svc.Emit(ctx, a))
		time.Sleep(2 * time.Millisecond) // distinct creation times
	}
	emit(notify.Alert{Kind: notify.KindReportBacklog, Severity: notify.SeverityCritical, Title: "tenant"})
	emit(notify.Alert{Kind: notify.KindProxyLowWatermark, Severity: notify.SeverityWarning, NamespaceID: f.prod.ID, Title: "prod"})
	emit(notify.Alert{Kind: notify.KindBanSpike, Severity: notify.SeverityWarning, NamespaceID: f.prod.ID, SiteID: f.shop.ID, Title: "shop"})
	emit(notify.Alert{Kind: notify.KindBreakerOpened, Severity: notify.SeverityCritical, NamespaceID: f.prod.ID, SiteID: f.forum.ID, Title: "forum"})
	emit(notify.Alert{Kind: notify.KindSecretExpiring, Severity: notify.SeverityWarning, NamespaceID: f.staging.ID, Title: "staging"})
	require.NoError(t, f.svc.Emit(ctx, notify.Alert{Kind: notify.KindReportBacklog, Severity: notify.SeverityCritical, TenantID: f.other, Title: "other"}))

	titles := func(c context.Context, req *spinneretv1.ListAlertEventsRequest) ([]string, string, error) {
		resp, err := f.h.ListAlertEvents(c, connect.NewRequest(req))
		if err != nil {
			return nil, "", err
		}
		out := []string{}
		for _, ev := range resp.Msg.GetEvents() {
			out = append(out, ev.GetTitle())
		}
		return out, resp.Msg.GetNextPageToken(), nil
	}

	got, _, err := titles(f.owner(), &spinneretv1.ListAlertEventsRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"staging", "forum", "shop", "prod", "tenant"}, got)

	resp, err := f.h.ListAlertEvents(f.owner(), connect.NewRequest(&spinneretv1.ListAlertEventsRequest{Namespace: "prod", Site: "shop"}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetEvents(), 1)
	ev := resp.Msg.GetEvents()[0]
	require.Equal(t, "prod", ev.GetNamespace())
	require.Equal(t, "shop", ev.GetSite())
	require.Equal(t, notify.KindBanSpike, ev.GetKind())
	require.Equal(t, map[string]any{"n": 1.0, "who": "shop"}, ev.GetDetails().AsMap())
	require.NotNil(t, ev.GetCreatedAt())

	cases := []struct {
		name   string
		ctx    context.Context
		req    *spinneretv1.ListAlertEventsRequest
		want   []string
		reason apperr.Reason
	}{
		{"namespace filter", f.owner(), &spinneretv1.ListAlertEventsRequest{Namespace: "prod"}, []string{"forum", "shop", "prod"}, ""},
		{"kind filter", f.owner(), &spinneretv1.ListAlertEventsRequest{Kind: notify.KindReportBacklog}, []string{"tenant"}, ""},
		{"severity filter", f.owner(), &spinneretv1.ListAlertEventsRequest{Severity: notify.SeverityCritical}, []string{"forum", "tenant"}, ""},
		{"future range", f.owner(), &spinneretv1.ListAlertEventsRequest{TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(time.Now().Add(time.Hour))}}, []string{}, ""},
		{"ns admin", f.user(authz.RoleAdmin, f.prod.ID), &spinneretv1.ListAlertEventsRequest{}, []string{"forum", "shop", "prod"}, ""},
		{"site viewer", f.user(authz.RoleViewer, f.prod.ID, f.shop.ID), &spinneretv1.ListAlertEventsRequest{}, []string{"shop"}, ""},
		{"site viewer namespace", f.user(authz.RoleViewer, f.prod.ID, f.shop.ID), &spinneretv1.ListAlertEventsRequest{Namespace: "prod"}, []string{"shop"}, ""},
		{"site viewer own site", f.user(authz.RoleViewer, f.prod.ID, f.shop.ID), &spinneretv1.ListAlertEventsRequest{Namespace: "prod", Site: "shop"}, []string{"shop"}, ""},
		{"site viewer other site", f.user(authz.RoleViewer, f.prod.ID, f.shop.ID), &spinneretv1.ListAlertEventsRequest{Namespace: "prod", Site: "forum"}, nil, apperr.ReasonPermissionDenied},
		{"site viewer other namespace", f.user(authz.RoleViewer, f.prod.ID, f.shop.ID), &spinneretv1.ListAlertEventsRequest{Namespace: "staging"}, nil, apperr.ReasonPermissionDenied},
		{"admin token", f.token(f.prod, "admin"), &spinneretv1.ListAlertEventsRequest{}, []string{"forum", "shop", "prod"}, ""},
		{"lease token", f.token(f.prod, "lease:acquire"), &spinneretv1.ListAlertEventsRequest{}, nil, apperr.ReasonScopeMissing},
		{"site without namespace", f.owner(), &spinneretv1.ListAlertEventsRequest{Site: "shop"}, nil, apperr.ReasonInvalidArgument},
		{"unknown site", f.owner(), &spinneretv1.ListAlertEventsRequest{Namespace: "prod", Site: "nope"}, nil, apperr.ReasonSiteUnknown},
		{"unknown namespace", f.owner(), &spinneretv1.ListAlertEventsRequest{Namespace: "nope"}, nil, apperr.ReasonNotFound},
		{"bad token", f.owner(), &spinneretv1.ListAlertEventsRequest{PageToken: "e30"}, nil, apperr.ReasonInvalidArgument},
		{"garbage token", f.owner(), &spinneretv1.ListAlertEventsRequest{PageToken: "%%%"}, nil, apperr.ReasonInvalidArgument},
		{"no tenant", authz.WithPrincipal(ctx, &authz.Principal{Kind: authz.KindUser, ID: "u", IsPlatformAdmin: true}), &spinneretv1.ListAlertEventsRequest{}, nil, apperr.ReasonInvalidArgument},
		{"unauthenticated", ctx, &spinneretv1.ListAlertEventsRequest{}, nil, apperr.ReasonSessionInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := titles(tc.ctx, tc.req)
			if tc.reason != "" {
				requireReason(t, err, tc.reason)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	// Keyset pagination.
	var all []string
	token := ""
	for {
		page, next, err := titles(f.owner(), &spinneretv1.ListAlertEventsRequest{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		all = append(all, page...)
		if next == "" {
			break
		}
		token = next
	}
	require.Equal(t, []string{"staging", "forum", "shop", "prod", "tenant"}, all)
}

// A caller who can edit a channel but not see its secrets cannot redirect
// masked credentials (sent verbatim) to another destination.
func TestUpdateChannelDestinationChangeRequiresCredentials(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hook := newHookServer(t)
	attacker := newHookServer(t)
	ch, err := f.create(f.owner(), &spinneretv1.CreateChannelRequest{
		Namespace: "prod", Name: "auth-hook", Kind: notify.ChannelWebhook,
		Config: mustStruct(t, map[string]any{
			"url": hook.URL + "/hook", "headers": map[string]any{"Authorization": "Bearer very-secret-token"},
		}),
		EventTypes: []string{notify.KindTest},
	})
	require.NoError(t, err)
	masked := ch.GetConfig().AsMap()
	require.Equal(t, map[string]any{"Authorization": "••••oken"}, masked["headers"])

	redirected := map[string]any{"url": attacker.URL + "/collect", "headers": masked["headers"]}
	_, err = f.h.UpdateChannel(f.user(authz.RoleAdmin, f.prod.ID), connect.NewRequest(&spinneretv1.UpdateChannelRequest{
		Id: ch.GetId(), Name: "auth-hook", Config: mustStruct(t, redirected), EventTypes: []string{notify.KindTest}, Enabled: true,
	}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	require.ErrorContains(t, err, "masked")

	// The stored destination and credential are unchanged: a test still reaches the original hook.
	test, err := f.h.TestChannel(f.owner(), connect.NewRequest(&spinneretv1.TestChannelRequest{Id: ch.GetId()}))
	require.NoError(t, err)
	require.True(t, test.Msg.GetDelivery().GetOk(), test.Msg.GetDelivery().GetError())
	hook.mu.Lock()
	require.Len(t, hook.bodies, 1)
	hook.mu.Unlock()
	attacker.mu.Lock()
	require.Empty(t, attacker.bodies)
	attacker.mu.Unlock()

	// Alert history shows the test delivery.
	resp, err := f.h.ListAlertEvents(f.owner(), connect.NewRequest(&spinneretv1.ListAlertEventsRequest{Kind: notify.KindTest}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetEvents(), 1)
	require.Len(t, resp.Msg.GetEvents()[0].GetDeliveries(), 1)
	require.True(t, resp.Msg.GetEvents()[0].GetDeliveries()[0].GetOk())
	require.Equal(t, "auth-hook", resp.Msg.GetEvents()[0].GetDeliveries()[0].GetChannelName())
}
