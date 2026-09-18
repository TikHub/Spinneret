package authapi

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/testutil"
)

const password = "correct-horse-battery"

var fastArgon2 = auth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

type fixture struct {
	h      *Handler
	users  *auth.Users
	authn  *auth.Authenticator
	tenant string
	ns     string
	userID string
	p      *authz.Principal
}

func newFixture(t *testing.T) (*fixture, context.Context) {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	cfg := auth.Config{SessionTTL: time.Hour, Argon2: fastArgon2}
	logger := slog.New(slog.DiscardHandler)
	f := &fixture{users: auth.NewUsers(pool, rdb, keys, &authtest.Recorder{}, cfg, logger)}
	f.authn = auth.NewAuthenticator(cfg, pool, rdb, keys, nil, logger)
	f.h = New(f.users, logger)
	hash, err := auth.HashPasswordWithParams(password, fastArgon2)
	require.NoError(t, err)
	f.tenant = authtest.Tenant(t, pool, "acme")
	f.ns = authtest.Namespace(t, pool, f.tenant, "prod")
	f.userID = authtest.User(t, pool, "alice", hash, false)
	f.p = authtest.UserPrincipal(f.userID, f.tenant, authtest.Binding(t, pool, f.userID, f.tenant, authz.RoleAdmin, "", nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return f, ctx
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func (f *fixture) login(t *testing.T, ctx context.Context) (*connect.Response[spinneretv1.LoginResponse], *http.Cookie) {
	t.Helper()
	ctx = auth.WithRequestMeta(ctx, auth.RequestMeta{ClientIP: "203.0.113.9", UserAgent: "browser", Secure: true})
	resp, err := f.h.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: "Alice", Password: password}))
	require.NoError(t, err)
	cookie, err := http.ParseSetCookie(resp.Header().Get("Set-Cookie"))
	require.NoError(t, err)
	return resp, cookie
}

func TestLogin(t *testing.T) {
	f, ctx := newFixture(t)
	resp, cookie := f.login(t, ctx)

	require.Equal(t, auth.SessionCookieName, cookie.Name)
	require.True(t, cookie.HttpOnly)
	require.True(t, cookie.Secure)
	require.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	require.Equal(t, 3600, cookie.MaxAge)
	require.Equal(t, "no-store", resp.Header().Get("Cache-Control"))

	msg := resp.Msg
	require.Equal(t, f.userID, msg.GetUser().GetId())
	require.Equal(t, "alice", msg.GetUser().GetUsername())
	require.NotNil(t, msg.GetUser().GetLastLoginAt())
	require.Len(t, msg.GetTenants(), 1)
	ta := msg.GetTenants()[0]
	require.Equal(t, "acme", ta.GetTenant().GetName())
	require.Len(t, ta.GetBindings(), 1)
	require.Equal(t, "admin", ta.GetBindings()[0].GetRole())
	require.Len(t, ta.GetNamespaces(), 1)
	require.Equal(t, "prod", ta.GetNamespaces()[0].GetNamespace().GetName())
	require.Contains(t, ta.GetNamespaces()[0].GetPermissions(), string(authz.PermTokenWrite))

	r := &http.Request{Method: http.MethodGet, Header: http.Header{}, RemoteAddr: "192.0.2.1:1"}
	r.AddCookie(cookie)
	p, err := f.authn.Authenticate(ctx, r)
	require.NoError(t, err)
	require.Equal(t, f.userID, p.ID)

	// Without request metadata in the context the handler derives it from the request.
	plain, err := f.h.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: "alice", Password: password}))
	require.NoError(t, err)
	c, err := http.ParseSetCookie(plain.Header().Get("Set-Cookie"))
	require.NoError(t, err)
	require.False(t, c.Secure)

	_, err = f.h.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: "alice", Password: "wrong-password"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestLogout(t *testing.T) {
	f, ctx := newFixture(t)
	_, cookie := f.login(t, ctx)

	req := connect.NewRequest(&spinneretv1.LogoutRequest{})
	req.Header().Set("Cookie", cookie.Name+"="+cookie.Value)
	resp, err := f.h.Logout(authz.WithPrincipal(ctx, f.p), req)
	require.NoError(t, err)
	cleared, err := http.ParseSetCookie(resp.Header().Get("Set-Cookie"))
	require.NoError(t, err)
	require.Empty(t, cleared.Value)
	require.Equal(t, -1, cleared.MaxAge)

	r := &http.Request{Method: http.MethodGet, Header: http.Header{}, RemoteAddr: "192.0.2.1:1"}
	r.AddCookie(cookie)
	_, err = f.authn.Authenticate(ctx, r)
	requireReason(t, err, apperr.ReasonSessionInvalid)

	_, err = f.h.Logout(ctx, req)
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = f.h.Logout(authz.WithPrincipal(ctx, authtest.TokenPrincipal("tok_1", f.tenant, f.ns, "prod", "admin")), req)
	requireReason(t, err, apperr.ReasonPermissionDenied)
}

func TestGetMe(t *testing.T) {
	f, ctx := newFixture(t)
	resp, err := f.h.GetMe(authz.WithPrincipal(ctx, f.p), connect.NewRequest(&spinneretv1.GetMeRequest{}))
	require.NoError(t, err)
	require.Equal(t, "alice", resp.Msg.GetUser().GetUsername())
	require.Len(t, resp.Msg.GetTenants(), 1)

	_, err = f.h.GetMe(ctx, connect.NewRequest(&spinneretv1.GetMeRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = f.h.GetMe(authz.WithPrincipal(ctx, authtest.TokenPrincipal("tok_1", f.tenant, f.ns, "prod", "admin")), connect.NewRequest(&spinneretv1.GetMeRequest{}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.GetMe(authz.WithPrincipal(ctx, authtest.UserPrincipal("usr_missing", "")), connect.NewRequest(&spinneretv1.GetMeRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestChangePassword(t *testing.T) {
	f, ctx := newFixture(t)
	_, cookie := f.login(t, ctx)
	pctx := authz.WithPrincipal(ctx, f.p)

	req := connect.NewRequest(&spinneretv1.ChangePasswordRequest{CurrentPassword: "wrong-one", NewPassword: "a-new-password"})
	_, err := f.h.ChangePassword(pctx, req)
	requireReason(t, err, apperr.ReasonPermissionDenied)

	req = connect.NewRequest(&spinneretv1.ChangePasswordRequest{CurrentPassword: password, NewPassword: "a-new-password"})
	req.Header().Set("Cookie", cookie.Name+"="+cookie.Value)
	_, err = f.h.ChangePassword(pctx, req)
	require.NoError(t, err)
	_, _, err = f.users.Login(ctx, "alice", "a-new-password", "", "")
	require.NoError(t, err)

	_, err = f.h.ChangePassword(ctx, req)
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestConverters(t *testing.T) {
	now := time.Now()
	u := UserProto(auth.UserView{ID: "usr_1", Username: "a", LastLoginAt: &now, CreatedAt: now})
	require.Equal(t, "usr_1", u.GetId())
	require.NotNil(t, u.GetLastLoginAt())
	require.Nil(t, UserProto(auth.UserView{}).GetCreatedAt())

	tenants := TenantAccessProtos([]auth.TenantAccess{{
		Tenant:   auth.TenantView{ID: "ten_1", Name: "t"},
		Bindings: []auth.BindingView{{ID: "rb_1", Role: "viewer", SiteIDs: []string{"sit_1"}, Sites: []string{"s"}}},
		Namespaces: []auth.NamespaceAccess{{
			Namespace:   auth.NamespaceView{ID: "ns_1", Name: "prod"},
			Permissions: []string{"site:read"},
			Sites:       []auth.SiteAccess{{SiteID: "sit_1", SiteName: "s", Permissions: []string{"identity:write"}}},
		}},
	}})
	require.Equal(t, "sit_1", tenants[0].GetNamespaces()[0].GetSites()[0].GetSiteId())
	require.Equal(t, []string{"s"}, tenants[0].GetBindings()[0].GetSites())
}
