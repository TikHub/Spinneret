package configapi

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/configcenter"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

type fakeSecrets struct {
	mu     sync.Mutex
	values map[string]string
}

func (f *fakeSecrets) ReadSecret(_ context.Context, p *authz.Principal, ns *catalog.Namespace, path string, _ int, _ string) (configcenter.SecretValue, error) {
	perm := authz.PermSecretReveal
	if p.Kind == authz.KindToken {
		perm = authz.PermSecretRead
	}
	if err := p.Require(perm, authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, SecretPath: path}); err != nil {
		return configcenter.SecretValue{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[path]
	if !ok {
		return configcenter.SecretValue{}, apperr.NotFound("secret not found")
	}
	return configcenter.SecretValue{Path: path, Value: v}, nil
}

type fakeRuntime struct{}

func (fakeRuntime) RuntimeVersion(context.Context, string, string) (int64, error) { return 4, nil }

func (fakeRuntime) RuntimeContent(_ context.Context, _, kind string) (string, int64, error) {
	return fmt.Sprintf(`{"kind":%q}`, kind), 4, nil
}

type testEnv struct {
	h        *Handler
	ns       *catalog.Namespace
	admin    *authz.Principal
	operator *authz.Principal
	viewer   *authz.Principal
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testutil.Postgres(t)
	ns := catalogtest.NewNamespace(idgen.New(idgen.Tenant), idgen.New(idgen.Namespace), "prod")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, ns.TenantID, "tenant")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, ns.ID, ns.TenantID, ns.Name)
	require.NoError(t, err)
	secretID := idgen.New(idgen.Secret)
	_, err = pool.Exec(ctx, `INSERT INTO secrets (id, namespace_id, path) VALUES ($1, $2, 'api/key')`, secretID, ns.ID)
	require.NoError(t, err)

	cat := catalogtest.New(ns)
	secrets := &fakeSecrets{values: map[string]string{"api/key": "s3cret"}}
	svc := configcenter.New(configcenter.Config{}, pool, cat, events.NewMemoryBus(), secrets, fakeRuntime{}, nil, nil, nil)
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-done)
	})
	return &testEnv{
		h:        New(svc, cat),
		ns:       ns,
		admin:    user(ns.TenantID, authz.RoleAdmin),
		operator: user(ns.TenantID, authz.RoleOperator),
		viewer:   user(ns.TenantID, authz.RoleViewer),
	}
}

func user(tenantID string, role authz.Role) *authz.Principal {
	return &authz.Principal{
		Kind: authz.KindUser, ID: idgen.New(idgen.User), TenantID: tenantID,
		Bindings: []authz.Binding{{TenantID: tenantID, Role: role}},
	}
}

func (e *testEnv) token(t *testing.T, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: idgen.New(idgen.Token), TenantID: e.ns.TenantID,
		NamespaceID: e.ns.ID, NamespaceName: e.ns.Name, Scopes: parsed,
	}
}

func as(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestAdminHandlers(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	h := e.h

	created, err := h.CreateConfigItem(as(e.operator), connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
		Namespace: "prod", Group: "crawler", Key: "search.json", Format: "json",
		SchemaJson: `{"type":"object"}`, Description: "d", Content: `{"key":"${secret:api/key}"}`,
	}))
	require.NoError(t, err)
	item := created.Msg.GetItem()
	require.NotEmpty(t, item.GetId())
	require.Equal(t, "prod", item.GetNamespace())
	require.True(t, item.GetHasDraft())
	require.Equal(t, `{"key":"${secret:api/key}"}`, item.GetDraftContent())
	require.JSONEq(t, `{"type":"object"}`, item.GetSchemaJson())
	require.NotNil(t, item.GetCreatedAt())
	require.NotNil(t, item.GetDraftUpdatedAt())
	require.Nil(t, item.GetPublishedAt())
	id := item.GetId()

	_, err = h.CreateConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
		Namespace: "prod", Group: "g", Key: "k", Format: "text",
	}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.CreateConfigItem(as(e.operator), connect.NewRequest(&spinneretv1.CreateConfigItemRequest{Group: "g", Key: "k", Format: "text"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = h.CreateConfigItem(context.Background(), connect.NewRequest(&spinneretv1.CreateConfigItemRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	got, err := h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Id{Id: id},
	}))
	require.NoError(t, err)
	require.Equal(t, id, got.Msg.GetItem().GetId())
	got, err = h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Locator{Locator: &spinneretv1.ConfigLocator{Namespace: "prod", Group: "crawler", Key: "search.json"}},
	}))
	require.NoError(t, err)
	require.Equal(t, id, got.Msg.GetItem().GetId())
	got, err = h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Locator{Locator: &spinneretv1.ConfigLocator{Namespace: "prod", Group: "_runtime", Key: "breakers"}},
	}))
	require.NoError(t, err)
	require.Empty(t, got.Msg.GetItem().GetId())
	require.Equal(t, int32(5), got.Msg.GetItem().GetCurrentVersion())
	require.JSONEq(t, `{"kind":"breakers"}`, got.Msg.GetItem().GetPublishedContent())
	_, err = h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Locator{},
	}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = h.GetConfigItem(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Locator{Locator: &spinneretv1.ConfigLocator{Namespace: "missing", Group: "g", Key: "k"}},
	}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = h.GetConfigItem(context.Background(), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Id{Id: id},
	}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = h.GetConfigItem(as(e.token(t, "lease:acquire")), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Id{Id: id},
	}))
	requireReason(t, err, apperr.ReasonScopeMissing)

	list, err := h.ListConfigItems(as(e.viewer), connect.NewRequest(&spinneretv1.ListConfigItemsRequest{Namespace: "prod", PageSize: 10}))
	require.NoError(t, err)
	require.Equal(t, int32(3), list.Msg.GetTotal())
	require.Len(t, list.Msg.GetItems(), 3)
	require.Equal(t, "search.json", list.Msg.GetItems()[2].GetKey())
	_, err = h.ListConfigItems(as(e.token(t, "report:write")), connect.NewRequest(&spinneretv1.ListConfigItemsRequest{}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.ListConfigItems(as(e.viewer), connect.NewRequest(&spinneretv1.ListConfigItemsRequest{}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	schema := ""
	desc := "updated"
	saved, err := h.SaveConfigDraft(as(e.operator), connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{
		Id: id, Content: `{"key":"${secret:api/key}","n":1}`, SchemaJson: &schema, Description: &desc,
	}))
	require.NoError(t, err)
	require.Empty(t, saved.Msg.GetItem().GetSchemaJson())
	require.Equal(t, "updated", saved.Msg.GetItem().GetDescription())
	_, err = h.SaveConfigDraft(as(e.viewer), connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{Id: id, Content: `{}`}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.SaveConfigDraft(context.Background(), connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{Id: id}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	_, err = h.PublishConfig(as(e.operator), connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: id}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.PublishConfig(context.Background(), connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: id}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	published, err := h.PublishConfig(as(e.admin), connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: id, Comment: "v1"}))
	require.NoError(t, err)
	require.Equal(t, int32(1), published.Msg.GetVersion())
	require.Equal(t, int32(1), published.Msg.GetItem().GetCurrentVersion())
	require.NotNil(t, published.Msg.GetItem().GetPublishedAt())
	require.Equal(t, e.admin.Actor(), published.Msg.GetItem().GetPublishedBy())

	_, err = h.SaveConfigDraft(as(e.operator), connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{Id: id, Content: `{"n":2}`}))
	require.NoError(t, err)
	published, err = h.PublishConfig(as(e.admin), connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: id, ExpectedVersion: 1}))
	require.NoError(t, err)
	require.Equal(t, int32(2), published.Msg.GetVersion())

	rolled, err := h.RollbackConfig(as(e.admin), connect.NewRequest(&spinneretv1.RollbackConfigRequest{Id: id, Version: 1, Comment: "back"}))
	require.NoError(t, err)
	require.Equal(t, int32(3), rolled.Msg.GetVersion())
	_, err = h.RollbackConfig(as(e.operator), connect.NewRequest(&spinneretv1.RollbackConfigRequest{Id: id, Version: 1}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.RollbackConfig(context.Background(), connect.NewRequest(&spinneretv1.RollbackConfigRequest{Id: id, Version: 1}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	versions, err := h.ListConfigVersions(as(e.viewer), connect.NewRequest(&spinneretv1.ListConfigVersionsRequest{Id: id, PageSize: 2}))
	require.NoError(t, err)
	require.Equal(t, int32(3), versions.Msg.GetTotal())
	require.Len(t, versions.Msg.GetVersions(), 2)
	require.Equal(t, int32(3), versions.Msg.GetVersions()[0].GetVersion())
	require.Equal(t, int32(1), versions.Msg.GetVersions()[0].GetSourceVersion())
	require.Equal(t, "back", versions.Msg.GetVersions()[0].GetComment())
	require.NotNil(t, versions.Msg.GetVersions()[0].GetPublishedAt())
	require.NotEmpty(t, versions.Msg.GetNextPageToken())
	_, err = h.ListConfigVersions(as(e.token(t, "config:read:other")), connect.NewRequest(&spinneretv1.ListConfigVersionsRequest{Id: id}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.ListConfigVersions(context.Background(), connect.NewRequest(&spinneretv1.ListConfigVersionsRequest{Id: id}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	diff, err := h.DiffConfigVersions(as(e.viewer), connect.NewRequest(&spinneretv1.DiffConfigVersionsRequest{Id: id, FromVersion: 2, ToVersion: 3}))
	require.NoError(t, err)
	require.Equal(t, `{"n":2}`, diff.Msg.GetFromContent())
	require.Contains(t, diff.Msg.GetUnifiedDiff(), "--- v2\n+++ v3")
	_, err = h.DiffConfigVersions(as(e.token(t, "lease:acquire")), connect.NewRequest(&spinneretv1.DiffConfigVersionsRequest{Id: id}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.DiffConfigVersions(context.Background(), connect.NewRequest(&spinneretv1.DiffConfigVersionsRequest{Id: id}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	_, err = h.DeleteConfigItem(as(e.operator), connect.NewRequest(&spinneretv1.DeleteConfigItemRequest{Id: id}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.DeleteConfigItem(context.Background(), connect.NewRequest(&spinneretv1.DeleteConfigItemRequest{Id: id}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = h.DeleteConfigItem(as(e.token(t, "config:publish:crawl*")), connect.NewRequest(&spinneretv1.DeleteConfigItemRequest{Id: id}))
	require.NoError(t, err)
	_, err = h.GetConfigItem(as(e.admin), connect.NewRequest(&spinneretv1.GetConfigItemRequest{
		Selector: &spinneretv1.GetConfigItemRequest_Id{Id: id},
	}))
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestNodeHandlers(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t)
	h := e.h
	_, err := h.CreateConfigItem(as(e.admin), connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
		Namespace: "prod", Group: "crawler", Key: "a.txt", Format: "text", Content: "key=${secret:api/key}", Publish: true,
	}))
	require.NoError(t, err)
	node := e.token(t, "config:read", "secret:read:prod/api/*")

	got, err := h.GetConfig(as(node), connect.NewRequest(&spinneretv1.GetConfigRequest{Group: "crawler", Key: "a.txt"}))
	require.NoError(t, err)
	item := got.Msg.GetItem()
	require.Equal(t, "key=s3cret", item.GetContent())
	require.True(t, item.GetHasSecretRefs())
	require.Equal(t, int32(1), item.GetVersion())
	require.Equal(t, "prod", item.GetNamespace())
	require.NotNil(t, item.GetUpdatedAt())

	_, err = h.GetConfig(as(node), connect.NewRequest(&spinneretv1.GetConfigRequest{Namespace: "other", Group: "crawler", Key: "a.txt"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.GetConfig(as(e.token(t, "config:read")), connect.NewRequest(&spinneretv1.GetConfigRequest{Group: "crawler", Key: "a.txt"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.GetConfig(as(node), connect.NewRequest(&spinneretv1.GetConfigRequest{Group: "crawler", Key: "none"}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = h.GetConfig(context.Background(), connect.NewRequest(&spinneretv1.GetConfigRequest{Group: "crawler", Key: "a.txt"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	batch, err := h.BatchGetConfig(as(node), connect.NewRequest(&spinneretv1.BatchGetConfigRequest{
		Namespace: "prod",
		Items: []*spinneretv1.ConfigRef{
			{Group: "crawler", Key: "a.txt"}, {Group: "_runtime", Key: "site_switches"}, {Group: "crawler", Key: "none"},
		},
	}))
	require.NoError(t, err)
	require.Len(t, batch.Msg.GetItems(), 2)
	require.Equal(t, int32(5), batch.Msg.GetItems()[1].GetVersion())
	require.Len(t, batch.Msg.GetMissing(), 1)
	require.Equal(t, "none", batch.Msg.GetMissing()[0].GetKey())
	_, err = h.BatchGetConfig(as(e.token(t, "config:read:x")), connect.NewRequest(&spinneretv1.BatchGetConfigRequest{
		Items: []*spinneretv1.ConfigRef{{Group: "crawler", Key: "a.txt"}},
	}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.BatchGetConfig(context.Background(), connect.NewRequest(&spinneretv1.BatchGetConfigRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	watch, err := h.WatchConfig(as(node), connect.NewRequest(&spinneretv1.WatchConfigRequest{
		Items: []*spinneretv1.WatchItem{{Group: "crawler", Key: "a.txt"}}, TimeoutMs: 1000,
	}))
	require.NoError(t, err)
	require.Len(t, watch.Msg.GetItems(), 1)

	start := time.Now()
	watch, err = h.WatchConfig(as(node), connect.NewRequest(&spinneretv1.WatchConfigRequest{
		Items: []*spinneretv1.WatchItem{{Group: "crawler", Key: "a.txt", Version: 1}}, TimeoutMs: 200,
	}))
	require.NoError(t, err)
	require.Empty(t, watch.Msg.GetItems())
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond)

	_, err = h.WatchConfig(as(e.token(t, "report:write")), connect.NewRequest(&spinneretv1.WatchConfigRequest{
		Items: []*spinneretv1.WatchItem{{Group: "crawler", Key: "a.txt"}},
	}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.WatchConfig(context.Background(), connect.NewRequest(&spinneretv1.WatchConfigRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	// Console users resolve the namespace by name and need secret:reveal.
	_, err = h.GetConfig(as(e.viewer), connect.NewRequest(&spinneretv1.GetConfigRequest{Namespace: "prod", Group: "crawler", Key: "a.txt"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	got, err = h.GetConfig(as(e.admin), connect.NewRequest(&spinneretv1.GetConfigRequest{Namespace: "prod", Group: "crawler", Key: "a.txt"}))
	require.NoError(t, err)
	require.Equal(t, "key=s3cret", got.Msg.GetItem().GetContent())
}

func TestTimeoutFromMillis(t *testing.T) {
	t.Parallel()
	require.Zero(t, timeoutFromMillis(0))
	require.Zero(t, timeoutFromMillis(-1))
	require.Equal(t, 1500*time.Millisecond, timeoutFromMillis(1500))
	require.Equal(t, int32(1<<31-1), clampInt32(1<<40))
	require.Equal(t, int32(7), clampInt32(7))
}
