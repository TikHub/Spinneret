package proxyapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/proxy"
	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

const (
	tenantID      = "ten_api"
	otherTenantID = "ten_api_other"
	secretPass    = "sup3r-s3cret-pass"
)

type nopHot struct{}

func (nopHot) SyncProxies(context.Context, string, []string) error   { return nil }
func (nopHot) RemoveProxies(context.Context, string, []string) error { return nil }

type member struct{}

func (member) Membership(context.Context) (int, int, error) { return 0, 1, nil }

type recorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (r *recorder) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

func (r *recorder) has(action string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

func newHandler(t *testing.T) (*Handler, *recorder) {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ('ten_api', 'api'), ('ten_api_other', 'other');
		INSERT INTO namespaces (id, tenant_id, name) VALUES ('ns_api', 'ten_api', 'main'), ('ns_api_other', 'ten_api_other', 'main')`)
	require.NoError(t, err)
	ns := catalogtest.NewNamespace(tenantID, "ns_api", "main")
	catalogtest.AddSite(ns, "sit_api", "alpha", 501)
	other := catalogtest.NewNamespace(otherTenantID, "ns_api_other", "main")
	cat := catalogtest.New(ns, other)
	cipher := vaulttest.NewCipher(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := &recorder{}
	bus := events.NewMemoryBus()
	svc := proxy.NewService(pool, cipher, []byte("pepper-pepper-pepper-pepper-1234"), rdb, keys, cat, nopHot{}, rec, bus, logger)
	checker := proxy.NewHealthChecker(proxy.HealthConfig{CheckURL: "http://127.0.0.1:9/check", Timeout: time.Second},
		pool, cipher, rdb, keys, cat, nopHot{}, member{}, bus, nil, logger)
	return New(cat, svc, checker, rec), rec
}

func userCtx(role authz.Role, tenant string) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindUser, ID: "usr_" + string(role), TenantID: tenant,
		Bindings: []authz.Binding{{TenantID: tenant, Role: role}},
	})
}

func tokenCtx(t *testing.T, scopes ...string) context.Context {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindToken, ID: "tok_api", TenantID: tenantID, NamespaceID: "ns_api", NamespaceName: "main", Scopes: parsed,
	})
}

// requireNoSecret fails when a response leaks the proxy password.
func requireNoSecret(t *testing.T, msg proto.Message) {
	t.Helper()
	b, err := protojson.Marshal(msg)
	require.NoError(t, err)
	require.NotContains(t, string(b), secretPass)
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestHandlerLifecycle(t *testing.T) {
	h, rec := newHandler(t)
	ctx := userCtx(authz.RoleAdmin, tenantID)
	addr := closedAddr(t)

	imp, err := h.ImportProxies(ctx, connect.NewRequest(&spinneretv1.ImportProxiesRequest{
		Namespace: "main", Format: "lines",
		Data:     "http://carol:" + secretPass + "@" + addr + " provider=acme\nbad-line\n",
		Defaults: &spinneretv1.ProxyDefaults{Kind: "residential", Tags: []string{"t1"}, MaxConcurrency: 3},
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, imp.Msg.GetCreated())
	require.Len(t, imp.Msg.GetFailed(), 1)
	require.EqualValues(t, 2, imp.Msg.GetFailed()[0].GetLine())
	requireNoSecret(t, imp.Msg)

	list, err := h.ListProxies(ctx, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "main", Kinds: []string{"residential"}}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetProxies(), 1)
	require.EqualValues(t, 1, list.Msg.GetTotal())
	pb := list.Msg.GetProxies()[0]
	require.Equal(t, "main", pb.GetNamespace())
	require.Equal(t, "http://"+addr, pb.GetDisplayUrl())
	require.Equal(t, "caro***", pb.GetUsernameHint())
	require.Equal(t, []string{"t1"}, pb.GetTags())
	require.EqualValues(t, 3, pb.GetMaxConcurrency())
	require.Equal(t, "acme", pb.GetProvider())
	require.NotNil(t, pb.GetCreatedAt())
	requireNoSecret(t, list.Msg)
	id := pb.GetId()

	got, err := h.GetProxy(ctx, connect.NewRequest(&spinneretv1.GetProxyRequest{Id: id}))
	require.NoError(t, err)
	require.Equal(t, id, got.Msg.GetProxy().GetId())
	requireNoSecret(t, got.Msg)

	region, mc := "jp", int32(9)
	upd, err := h.UpdateProxy(ctx, connect.NewRequest(&spinneretv1.UpdateProxyRequest{
		Id: id, Region: &region, MaxConcurrency: &mc, SetTags: true, Tags: []string{"t2"},
	}))
	require.NoError(t, err)
	require.Equal(t, "jp", upd.Msg.GetProxy().GetRegion())
	require.EqualValues(t, 9, upd.Msg.GetProxy().GetMaxConcurrency())
	require.Equal(t, []string{"t2"}, upd.Msg.GetProxy().GetTags())
	requireNoSecret(t, upd.Msg)

	op, err := h.OperateProxies(ctx, connect.NewRequest(&spinneretv1.OperateProxiesRequest{
		Ids: []string{id, "pxy_missing"}, Operation: "ban", Duration: "permanent", Reason: "abuse",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, op.Msg.GetResult().GetSucceeded())
	require.Equal(t, "not_found", op.Msg.GetResult().GetFailed()[0].GetReason())
	got, err = h.GetProxy(ctx, connect.NewRequest(&spinneretv1.GetProxyRequest{Id: id}))
	require.NoError(t, err)
	require.Equal(t, "banned", got.Msg.GetProxy().GetState())
	require.Nil(t, got.Msg.GetProxy().GetBanUntil(), "permanent ban")

	op, err = h.OperateProxies(ctx, connect.NewRequest(&spinneretv1.OperateProxiesRequest{
		Ids: []string{id}, Operation: "cooldown", Site: "alpha", Duration: "10m",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, op.Msg.GetResult().GetSucceeded())

	check, err := h.CheckProxy(ctx, connect.NewRequest(&spinneretv1.CheckProxyRequest{Id: id}))
	require.NoError(t, err)
	require.False(t, check.Msg.GetOk())
	require.NotEmpty(t, check.Msg.GetError())
	requireNoSecret(t, check.Msg)
	require.True(t, rec.has("proxy.check"))

	now := time.Now()
	stats, err := h.GetProviderStats(ctx, connect.NewRequest(&spinneretv1.GetProviderStatsRequest{
		Namespace: "main", TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(now.Add(-time.Hour)), End: timestamppb.New(now)},
	}))
	require.NoError(t, err)
	require.Len(t, stats.Msg.GetProviders(), 1)
	require.Equal(t, "acme", stats.Msg.GetProviders()[0].GetProvider())
	require.EqualValues(t, 1, stats.Msg.GetProviders()[0].GetProxies())

	del, err := h.DeleteProxies(ctx, connect.NewRequest(&spinneretv1.DeleteProxiesRequest{Ids: []string{id}}))
	require.NoError(t, err)
	require.EqualValues(t, 1, del.Msg.GetResult().GetSucceeded())
	_, err = h.GetProxy(ctx, connect.NewRequest(&spinneretv1.GetProxyRequest{Id: id}))
	require.Equal(t, apperr.ReasonNotFound, apperr.ReasonOf(err))
	for _, action := range []string{"proxy.import", "proxy.update", "proxy.ban", "proxy.cooldown", "proxy.delete"} {
		require.True(t, rec.has(action), action)
	}
}

func TestHandlerPermissions(t *testing.T) {
	h, _ := newHandler(t)
	admin := userCtx(authz.RoleAdmin, tenantID)
	imp, err := h.ImportProxies(admin, connect.NewRequest(&spinneretv1.ImportProxiesRequest{
		Namespace: "main", Format: "lines", Data: "http://10.0.0.1:80",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, imp.Msg.GetCreated())
	list, err := h.ListProxies(admin, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "main"}))
	require.NoError(t, err)
	id := list.Msg.GetProxies()[0].GetId()

	viewer := userCtx(authz.RoleViewer, tenantID)
	foreign := userCtx(authz.RoleOwner, otherTenantID)
	leaseToken := tokenCtx(t, "lease:acquire")
	noTenant := authz.WithPrincipal(context.Background(), &authz.Principal{Kind: authz.KindUser, ID: "usr_x",
		Bindings: []authz.Binding{{TenantID: tenantID, Role: authz.RoleAdmin}}})

	calls := map[string]func(ctx context.Context) error{
		"ListProxies": func(ctx context.Context) error {
			_, err := h.ListProxies(ctx, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "main"}))
			return err
		},
		"GetProxy": func(ctx context.Context) error {
			_, err := h.GetProxy(ctx, connect.NewRequest(&spinneretv1.GetProxyRequest{Id: id}))
			return err
		},
		"ImportProxies": func(ctx context.Context) error {
			_, err := h.ImportProxies(ctx, connect.NewRequest(&spinneretv1.ImportProxiesRequest{Namespace: "main", Format: "lines", Data: "http://10.0.0.2:80"}))
			return err
		},
		"UpdateProxy": func(ctx context.Context) error {
			city := "x"
			_, err := h.UpdateProxy(ctx, connect.NewRequest(&spinneretv1.UpdateProxyRequest{Id: id, City: &city}))
			return err
		},
		"OperateProxies": func(ctx context.Context) error {
			_, err := h.OperateProxies(ctx, connect.NewRequest(&spinneretv1.OperateProxiesRequest{Ids: []string{id}, Operation: "disable"}))
			return err
		},
		"DeleteProxies": func(ctx context.Context) error {
			_, err := h.DeleteProxies(ctx, connect.NewRequest(&spinneretv1.DeleteProxiesRequest{Ids: []string{id}}))
			return err
		},
		"CheckProxy": func(ctx context.Context) error {
			_, err := h.CheckProxy(ctx, connect.NewRequest(&spinneretv1.CheckProxyRequest{Id: id}))
			return err
		},
		"GetProviderStats": func(ctx context.Context) error {
			_, err := h.GetProviderStats(ctx, connect.NewRequest(&spinneretv1.GetProviderStatsRequest{Namespace: "main"}))
			return err
		},
	}
	tests := []struct {
		principal string
		ctx       context.Context
		want      map[string]apperr.Reason // RPC → reason ("" = allowed)
	}{
		{principal: "unauthenticated", ctx: context.Background(), want: map[string]apperr.Reason{
			"ListProxies": apperr.ReasonSessionInvalid, "GetProxy": apperr.ReasonSessionInvalid, "ImportProxies": apperr.ReasonSessionInvalid,
			"UpdateProxy": apperr.ReasonSessionInvalid, "OperateProxies": apperr.ReasonSessionInvalid, "DeleteProxies": apperr.ReasonSessionInvalid,
			"CheckProxy": apperr.ReasonSessionInvalid, "GetProviderStats": apperr.ReasonSessionInvalid,
		}},
		{principal: "viewer", ctx: viewer, want: map[string]apperr.Reason{
			"ListProxies": "", "GetProxy": "", "GetProviderStats": "",
			"ImportProxies": apperr.ReasonPermissionDenied, "UpdateProxy": apperr.ReasonPermissionDenied,
			"OperateProxies": apperr.ReasonPermissionDenied, "DeleteProxies": apperr.ReasonPermissionDenied,
			"CheckProxy": apperr.ReasonPermissionDenied,
		}},
		{principal: "token without proxy scopes", ctx: leaseToken, want: map[string]apperr.Reason{
			"ListProxies": apperr.ReasonScopeMissing, "GetProxy": apperr.ReasonScopeMissing, "ImportProxies": apperr.ReasonScopeMissing,
			"UpdateProxy": apperr.ReasonScopeMissing, "OperateProxies": apperr.ReasonScopeMissing, "DeleteProxies": apperr.ReasonScopeMissing,
			"CheckProxy": apperr.ReasonScopeMissing, "GetProviderStats": apperr.ReasonScopeMissing,
		}},
		{principal: "user of another tenant", ctx: foreign, want: map[string]apperr.Reason{
			"ListProxies": "", "ImportProxies": "", "GetProviderStats": "",
			"GetProxy": apperr.ReasonNotFound, "UpdateProxy": apperr.ReasonNotFound, "CheckProxy": apperr.ReasonNotFound,
		}},
		{principal: "user without active tenant", ctx: noTenant, want: map[string]apperr.Reason{
			"ListProxies": apperr.ReasonInvalidArgument, "ImportProxies": apperr.ReasonInvalidArgument,
			"GetProviderStats": apperr.ReasonInvalidArgument,
		}},
	}
	for _, tt := range tests {
		for rpc, reason := range tt.want {
			t.Run(tt.principal+"/"+rpc, func(t *testing.T) {
				err := calls[rpc](tt.ctx)
				if reason == "" {
					require.NoError(t, err)
					return
				}
				require.Error(t, err)
				require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
			})
		}
	}

	t.Run("foreign user bulk operations do not touch other tenants", func(t *testing.T) {
		op, err := h.OperateProxies(foreign, connect.NewRequest(&spinneretv1.OperateProxiesRequest{Ids: []string{id}, Operation: "disable"}))
		require.NoError(t, err)
		require.Zero(t, op.Msg.GetResult().GetSucceeded())
		del, err := h.DeleteProxies(foreign, connect.NewRequest(&spinneretv1.DeleteProxiesRequest{Ids: []string{id}}))
		require.NoError(t, err)
		require.Zero(t, del.Msg.GetResult().GetSucceeded())
		list, err := h.ListProxies(foreign, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "main"}))
		require.NoError(t, err)
		for _, p := range list.Msg.GetProxies() {
			require.NotEqual(t, id, p.GetId(), "the other tenant's namespace \"main\" is a different namespace")
		}
	})

	t.Run("token namespace mismatch", func(t *testing.T) {
		_, err := h.ListProxies(tokenCtx(t, "proxy:write"), connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "other"}))
		require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err))
		res, err := h.ListProxies(tokenCtx(t, "proxy:write"), connect.NewRequest(&spinneretv1.ListProxiesRequest{}))
		require.NoError(t, err)
		require.Len(t, res.Msg.GetProxies(), 1)
	})

	t.Run("invalid durations", func(t *testing.T) {
		for _, d := range []struct{ op, duration string }{{"cooldown", "soon"}, {"cooldown", "permanent"}, {"cooldown", ""}, {"ban", ""}} {
			_, err := h.OperateProxies(admin, connect.NewRequest(&spinneretv1.OperateProxiesRequest{Ids: []string{id}, Operation: d.op, Duration: d.duration}))
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err), "%s %q", d.op, d.duration)
		}
	})

	t.Run("unknown namespace", func(t *testing.T) {
		_, err := h.ListProxies(admin, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: "nope"}))
		require.Equal(t, apperr.ReasonNotFound, apperr.ReasonOf(err))
	})
}

func TestHandlerWithoutChecker(t *testing.T) {
	full, _ := newHandler(t)
	h := New(full.cat, full.svc, nil, nil)
	_, err := h.CheckProxy(userCtx(authz.RoleAdmin, tenantID), connect.NewRequest(&spinneretv1.CheckProxyRequest{Id: "pxy_1"}))
	require.Equal(t, apperr.ReasonFailedPrecondition, apperr.ReasonOf(err))
	_, err = h.CheckProxy(context.Background(), connect.NewRequest(&spinneretv1.CheckProxyRequest{Id: "pxy_1"}))
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))
}

func TestConverters(t *testing.T) {
	require.Nil(t, toProto(nil))
	now := time.Now().UTC()
	v := &proxy.Proxy{
		ID: "pxy_1", NamespaceName: "main", Attributes: proxy.Attributes{Tags: []string{"a"}},
		Sites: []proxy.SiteState{{SiteID: "sit_1", Site: "alpha", State: "active", Score: 55.5, Samples: 3, ActiveLeases: 1, CooldownUntil: &now}},
	}
	pb := toProto(v)
	require.Len(t, pb.GetSites(), 1)
	require.Equal(t, 55.5, pb.GetSites()[0].GetScore())
	require.True(t, pb.GetSites()[0].GetCooldownUntil().AsTime().Equal(now))
	res := bulkToProto(proxy.BulkResult{Matched: 2, Succeeded: 1, Failed: []proxy.BulkFailure{{ID: "x", Reason: "not_found", Message: "m"}}})
	require.EqualValues(t, 2, res.GetMatched())
	require.Equal(t, "x", res.GetFailed()[0].GetId())
}
