package leaseapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/scheduler"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

const typeYAML = `
name: demo_cookie
site: demo
client: web
fields:
  cookies: { type: cookie_map, required: true, sensitive: true }
unique_by: [cookies.sessionid]
deliver:
  cookie_header: "{{ cookies }}"
`

type creds struct{}

func (creds) Credential(_ context.Context, _ *identity.CompiledType, _, identityID string, _ int) (*identity.Credential, error) {
	return &identity.Credential{
		Cookies:      map[string]string{"sid": identityID},
		CookieHeader: "sid=" + identityID,
		JSON:         map[string]any{"list": []string{"a", "b"}},
		Values:       map[string]any{"device": map[string]string{"os": "android"}, "n": 3},
	}, nil
}

type proxies struct{}

func (proxies) Resolve(_ context.Context, _, proxyID, _, _ string) (*scheduler.ProxyAssignment, error) {
	return &scheduler.ProxyAssignment{ID: proxyID, URL: "http://u:p@203.0.113.10:8000", Kind: "residential", Region: "US"}, nil
}

type env struct {
	rdb  rueidis.Client
	keys redis.Keys
	ns   *catalog.Namespace
	site *catalog.Site
	web  *catalog.EndpointGroup
	h    *Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "prod")
	st := catalogtest.AddSite(ns, "sit_1", "demo", 9, "web")
	catalogtest.AddGroup(st, "eg_search", "web", "search", 91, site.Rule{Kind: site.RulePrefix, Pattern: "/search/"})
	catalogtest.AddIdentityType(st, catalogtest.MustCompileType("ity_1", st.ID, 1, typeYAML))
	web, _ := st.Group("web", site.DefaultGroup)
	svc := scheduler.New(scheduler.Config{ReportShards: 16}, rdb, keys, catalogtest.New(ns), creds{}, proxies{}, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &env{rdb: rdb, keys: keys, ns: ns, site: st, web: web, h: New(svc)}
}

func (e *env) identity(t *testing.T, i int64, groups ...int64) {
	t.Helper()
	ctx := context.Background()
	id := strconv.FormatInt(i, 10)
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Hset().Key(e.keys.Identity(e.site.Key, i)).FieldValue().
		FieldValue("iid", "idt_"+id).FieldValue("st", "active").FieldValue("ty", "demo_cookie").
		FieldValue("tv", "1").FieldValue("pv", "1").FieldValue("al", "0").Build()).Error())
	for _, g := range groups {
		require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Zadd().Key(e.keys.Ready(e.site.Key, g)).ScoreMember().ScoreMember(0, id).Build()).Error())
	}
}

func (e *env) token(t *testing.T, scopes ...string) context.Context {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindToken, ID: "tok_1", TenantID: e.ns.TenantID, NamespaceID: e.ns.ID,
		NamespaceName: e.ns.Name, Scopes: parsed, Node: "crawler-hk-03",
	})
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestLeaseLifecycle(t *testing.T) {
	e := newEnv(t)
	e.identity(t, 1, e.web.Key, 91)
	e.identity(t, 2, e.web.Key, 91)
	ctx := e.token(t, "lease:acquire")

	resp, err := e.h.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Site: "demo", Client: "web", Uri: "/search/q?x=1"}))
	require.NoError(t, err)
	msg := resp.Msg
	require.Equal(t, "search", msg.GetLease().GetEndpointGroup())
	require.Equal(t, "demo_cookie", msg.GetLease().GetIdentityType())
	require.NotNil(t, msg.GetLease().GetExpiresAt())
	require.Equal(t, int32(30000), msg.GetHints().GetRenewBeforeMs())
	require.Nil(t, msg.GetProxy())
	require.Equal(t, "sid="+msg.GetLease().GetIdentityId(), msg.GetCredential().GetCookieHeader())
	require.Equal(t, "android", msg.GetCredential().GetValues().GetFields()["device"].GetStructValue().GetFields()["os"].GetStringValue())
	require.Len(t, msg.GetCredential().GetJson().GetStructValue().GetFields()["list"].GetListValue().GetValues(), 2)

	body, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(msg)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Contains(t, decoded, "proxy")
	require.Nil(t, decoded["proxy"], "proxy is null when none is assigned")
	ls, err := e.rdb.Do(context.Background(), e.rdb.B().Hget().Key(e.keys.Lease(e.site.Key, msg.GetLease().GetLeaseId())).Field("n").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "crawler-hk-03", ls)

	renew, err := e.h.Renew(ctx, connect.NewRequest(&spinneretv1.RenewRequest{LeaseId: msg.GetLease().GetLeaseId(), ExtendMs: 60000}))
	require.NoError(t, err)
	require.False(t, renew.Msg.GetExpiresAt().AsTime().Before(msg.GetLease().GetExpiresAt().AsTime()))

	rel, err := e.h.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: msg.GetLease().GetLeaseId()}))
	require.NoError(t, err)
	require.True(t, rel.Msg.GetReleased())
	rel, err = e.h.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: msg.GetLease().GetLeaseId()}))
	require.NoError(t, err)
	require.False(t, rel.Msg.GetReleased())

	_, err = e.h.Renew(ctx, connect.NewRequest(&spinneretv1.RenewRequest{LeaseId: msg.GetLease().GetLeaseId()}))
	requireReason(t, err, apperr.ReasonLeaseReleased)

	batch, err := e.h.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{Site: "demo", Client: "web", Count: 5}))
	require.NoError(t, err)
	require.Equal(t, int32(5), batch.Msg.GetRequested())
	require.Len(t, batch.Msg.GetLeases(), 2)
	require.NotEqual(t, batch.Msg.GetLeases()[0].GetLease().GetIdentityId(), batch.Msg.GetLeases()[1].GetLease().GetIdentityId())

	_, err = e.h.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{Site: "demo", Client: "web", Count: 1}))
	requireReason(t, err, apperr.ReasonNoIdentityAvailable)
}

func TestLeaseProxyAssignment(t *testing.T) {
	e := newEnv(t)
	rot := policy.Default(policy.KindRotation).(*policy.RotationSpec)
	rot.Proxy.Mode = policy.ProxyModePool
	e.web.Rotation = rot
	e.identity(t, 1, e.web.Key)
	ctx := context.Background()
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Hset().Key(e.keys.ProxySite(e.site.Key, 4)).FieldValue().
		FieldValue("pid", "pxy_4").FieldValue("st", "active").FieldValue("kd", "residential").FieldValue("mc", "2").Build()).Error())
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Zadd().Key(e.keys.ProxyReady(e.site.Key)).ScoreMember().ScoreMember(0, "4").Build()).Error())

	resp, err := e.h.Acquire(e.token(t, "lease:acquire:demo"), connect.NewRequest(&spinneretv1.AcquireRequest{Site: "demo", Client: "web"}))
	require.NoError(t, err)
	require.Equal(t, "pxy_4", resp.Msg.GetProxy().GetProxyId())
	require.Equal(t, "http://u:p@203.0.113.10:8000", resp.Msg.GetProxy().GetUrl())
	require.Equal(t, "residential", resp.Msg.GetProxy().GetKind())
	require.Equal(t, "US", resp.Msg.GetProxy().GetRegion())
}

func TestLeasePermissionDenials(t *testing.T) {
	e := newEnv(t)
	e.identity(t, 1, e.web.Key)
	acquire := connect.NewRequest(&spinneretv1.AcquireRequest{Site: "demo", Client: "web"})
	user := authz.WithPrincipal(context.Background(), &authz.Principal{
		Kind: authz.KindUser, ID: "usr_1", TenantID: "ten_1",
		Bindings: []authz.Binding{{TenantID: "ten_1", Role: authz.RoleOwner}},
	})
	tests := []struct {
		name   string
		ctx    context.Context
		reason apperr.Reason
	}{
		{name: "unauthenticated", ctx: context.Background(), reason: apperr.ReasonSessionInvalid},
		{name: "token without scope", ctx: e.token(t, "report:write"), reason: apperr.ReasonScopeMissing},
		{name: "token for another site", ctx: e.token(t, "lease:acquire:other"), reason: apperr.ReasonScopeMissing},
		{name: "console user", ctx: user, reason: apperr.ReasonPermissionDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.h.Acquire(tc.ctx, acquire)
			requireReason(t, err, tc.reason)
			_, err = e.h.AcquireBatch(tc.ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{Site: "demo", Client: "web", Count: 2}))
			requireReason(t, err, tc.reason)
		})
	}

	g, err := e.h.Acquire(e.token(t, "lease:acquire"), acquire)
	require.NoError(t, err)
	id := g.Msg.GetLease().GetLeaseId()
	for _, tc := range tests {
		t.Run("lease "+tc.name, func(t *testing.T) {
			want := tc.reason
			if want == apperr.ReasonScopeMissing {
				want = apperr.ReasonLeaseUnknown
			}
			_, err := e.h.Renew(tc.ctx, connect.NewRequest(&spinneretv1.RenewRequest{LeaseId: id}))
			requireReason(t, err, want)
			_, err = e.h.Release(tc.ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: id}))
			requireReason(t, err, want)
		})
	}

	_, err = e.h.Acquire(e.token(t, "lease:acquire"), connect.NewRequest(&spinneretv1.AcquireRequest{Site: "nope", Client: "web"}))
	requireReason(t, err, apperr.ReasonSiteUnknown)
}

// fakeService returns canned results to exercise conversion edge cases.
type fakeService struct {
	grant  *scheduler.Grant
	grants []*scheduler.Grant
	err    error
}

func (f fakeService) Acquire(context.Context, scheduler.AcquireRequest) (*scheduler.Grant, error) {
	return f.grant, f.err
}

func (f fakeService) AcquireBatch(context.Context, scheduler.AcquireRequest) ([]*scheduler.Grant, error) {
	return f.grants, f.err
}

func (f fakeService) Renew(context.Context, string, time.Duration) (time.Time, error) {
	return time.Time{}, f.err
}

func (f fakeService) Release(context.Context, string) (bool, error) { return false, f.err }

func TestConversionsAndErrors(t *testing.T) {
	ctx := authz.WithPrincipal(context.Background(), &authz.Principal{Kind: authz.KindToken, ID: "tok"})

	bad := &scheduler.Grant{Lease: scheduler.Lease{ID: "lse"}, Credential: &identity.Credential{JSON: map[string]any{"ch": make(chan int)}}}
	_, err := New(fakeService{grant: bad}).Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{}))
	requireReason(t, err, apperr.ReasonInternal)
	_, err = New(fakeService{grants: []*scheduler.Grant{bad}}).AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{Count: 1}))
	requireReason(t, err, apperr.ReasonInternal)
	badValues := &scheduler.Grant{Credential: &identity.Credential{Values: map[string]any{"f": func() {}}}}
	_, err = grantProto(badValues)
	requireReason(t, err, apperr.ReasonInternal)
	_, err = grantProto(nil)
	requireReason(t, err, apperr.ReasonInternal)

	boom := apperr.Unavailable(apperr.ReasonCircuitOpen, 100, "open")
	h := New(fakeService{err: boom})
	_, err = h.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{}))
	require.True(t, errors.Is(err, boom))
	_, err = h.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{}))
	require.True(t, errors.Is(err, boom))
	_, err = h.Renew(ctx, connect.NewRequest(&spinneretv1.RenewRequest{}))
	require.True(t, errors.Is(err, boom))
	_, err = h.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{}))
	require.True(t, errors.Is(err, boom))

	empty, err := grantProto(&scheduler.Grant{RenewBefore: time.Duration(math.MaxInt64)})
	require.NoError(t, err)
	require.NotNil(t, empty.GetCredential().GetCookies())
	require.Nil(t, empty.GetCredential().GetJson())
	require.Nil(t, empty.GetLease().GetExpiresAt())
	require.Equal(t, int32(math.MaxInt32), empty.GetHints().GetRenewBeforeMs())
	require.Equal(t, int32(0), clampInt32(-5))

	v, err := toValue(json.Number("12"))
	require.NoError(t, err)
	require.Equal(t, 12.0, v.GetNumberValue())
	s, err := toStruct(map[string]any{"tags": []string{"x"}})
	require.NoError(t, err)
	require.Len(t, s.GetFields()["tags"].GetListValue().GetValues(), 1)
}
