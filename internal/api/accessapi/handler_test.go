package accessapi

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/testutil"
)

var fastArgon2 = auth.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

type fixture struct {
	h       *Handler
	ctx     context.Context
	tenant  string
	ns      string
	siteA   string
	owner   *authz.Principal
	viewer  *authz.Principal
	ownerID string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	logger := slog.New(slog.DiscardHandler)
	rec := &authtest.Recorder{}
	bus := events.NewMemoryBus()
	cfg := auth.Config{SessionTTL: time.Hour, Argon2: fastArgon2}

	f := &fixture{}
	f.tenant = authtest.Tenant(t, pool, "acme")
	f.ns = authtest.Namespace(t, pool, f.tenant, "prod")
	f.siteA = authtest.Site(t, pool, f.ns, "shop")
	f.ownerID = authtest.User(t, pool, "owner", "x", false)
	f.owner = authtest.UserPrincipal(f.ownerID, f.tenant, authtest.Binding(t, pool, f.ownerID, f.tenant, authz.RoleOwner, "", nil))
	viewerID := authtest.User(t, pool, "viewer", "x", false)
	f.viewer = authtest.UserPrincipal(viewerID, f.tenant, authtest.Binding(t, pool, viewerID, f.tenant, authz.RoleViewer, "", nil))
	authtest.AuditRow(t, pool, audit.Entry{TenantID: f.tenant, NamespaceID: f.ns, Action: "identity.ban", ActorName: "owner"})

	snap := catalogtest.NewNamespace(f.tenant, f.ns, "prod")
	catalogtest.AddSite(snap, f.siteA, "shop", 1)
	cat := catalogtest.New(snap)

	f.h = New(auth.NewTokens(pool, bus, rec, logger), auth.NewUsers(pool, rdb, keys, rec, cfg, logger, auth.WithEventBus(bus)),
		auth.NewAuditLogs(pool), cat, logger)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f.ctx = ctx
	return f
}

func (f *fixture) as(p *authz.Principal) context.Context { return authz.WithPrincipal(f.ctx, p) }

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestTokensRPCs(t *testing.T) {
	f := newFixture(t)
	expires := timestamppb.New(time.Now().Add(time.Hour))

	_, err := f.h.CreateToken(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateTokenRequest{Name: "x", Scopes: []string{"admin"}}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.h.CreateToken(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateTokenRequest{Namespace: "missing", Name: "x", Scopes: []string{"admin"}}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.h.CreateToken(f.as(f.viewer), connect.NewRequest(&spinneretv1.CreateTokenRequest{Namespace: "prod", Name: "x", Scopes: []string{"admin"}}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.CreateToken(f.ctx, connect.NewRequest(&spinneretv1.CreateTokenRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	created, err := f.h.CreateToken(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateTokenRequest{
		Namespace: "prod", Name: "crawler", Scopes: []string{"lease:acquire:shop"}, IpAllowlist: []string{"10.0.0.0/8"},
		RateLimitRps: 5, ExpiresAt: expires,
	}))
	require.NoError(t, err)
	tok := created.Msg.GetToken()
	require.True(t, auth.WellFormedToken(created.Msg.GetPlaintext()))
	require.Equal(t, "prod", tok.GetNamespace())
	require.Equal(t, expires.AsTime().UnixMicro(), tok.GetExpiresAt().AsTime().UnixMicro())
	require.Nil(t, tok.GetRevokedAt())

	list, err := f.h.ListTokens(f.as(f.owner), connect.NewRequest(&spinneretv1.ListTokensRequest{}))
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Msg.GetTotal())
	list, err = f.h.ListTokens(f.as(f.owner), connect.NewRequest(&spinneretv1.ListTokensRequest{Namespace: "prod"}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetTokens(), 1)
	_, err = f.h.ListTokens(f.as(f.viewer), connect.NewRequest(&spinneretv1.ListTokensRequest{}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.ListTokens(f.as(f.owner), connect.NewRequest(&spinneretv1.ListTokensRequest{Namespace: "nope"}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.h.ListTokens(f.ctx, connect.NewRequest(&spinneretv1.ListTokensRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	// API tokens always act in their own namespace.
	adminToken := authtest.TokenPrincipal("tok_admin", f.tenant, f.ns, "prod", "admin")
	list, err = f.h.ListTokens(f.as(adminToken), connect.NewRequest(&spinneretv1.ListTokensRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetTokens(), 1)
	_, err = f.h.ListTokens(f.as(adminToken), connect.NewRequest(&spinneretv1.ListTokensRequest{Namespace: "other"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = f.h.ListTokens(f.as(f.owner), connect.NewRequest(&spinneretv1.ListTokensRequest{PageToken: "!"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	_, err = f.h.RevokeToken(f.as(f.viewer), connect.NewRequest(&spinneretv1.RevokeTokenRequest{Id: tok.GetId()}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.RevokeToken(f.ctx, connect.NewRequest(&spinneretv1.RevokeTokenRequest{Id: tok.GetId()}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	revoked, err := f.h.RevokeToken(f.as(f.owner), connect.NewRequest(&spinneretv1.RevokeTokenRequest{Id: tok.GetId()}))
	require.NoError(t, err)
	require.NotNil(t, revoked.Msg.GetToken().GetRevokedAt())
}

func TestUsersAndBindingsRPCs(t *testing.T) {
	f := newFixture(t)

	t.Run("create user", func(t *testing.T) {
		req := &spinneretv1.CreateUserRequest{
			Username: "carol", Password: "a-long-password", Role: "operator", Namespace: "prod", Sites: []string{"shop"},
			ExtraPermissions: []string{"identity:reveal"},
		}
		_, err := f.h.CreateUser(f.as(f.viewer), connect.NewRequest(&spinneretv1.CreateUserRequest{
			Username: "carol", Password: "a-long-password", Role: "viewer", Namespace: "does-not-exist",
		}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.CreateUser(f.as(authtest.UserPrincipal(f.ownerID, "")), connect.NewRequest(req))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = f.h.CreateUser(f.ctx, connect.NewRequest(req))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = f.h.CreateUser(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateUserRequest{
			Username: "carol", Password: "a-long-password", Role: "viewer", Sites: []string{"shop"},
		}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = f.h.CreateUser(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateUserRequest{
			Username: "carol", Password: "a-long-password", Role: "viewer", Namespace: "prod", Sites: []string{"unknown"},
		}))
		requireReason(t, err, apperr.ReasonSiteUnknown)
		_, err = f.h.CreateUser(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateUserRequest{
			Username: "carol", Password: "a-long-password", Role: "viewer", Namespace: "nope",
		}))
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = f.h.CreateUser(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateUserRequest{Username: "Bad Name", Password: "a-long-password", Role: "viewer"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)

		resp, err := f.h.CreateUser(f.as(f.owner), connect.NewRequest(req))
		require.NoError(t, err)
		require.Equal(t, "carol", resp.Msg.GetUser().GetUsername())
		b := resp.Msg.GetBinding()
		require.Equal(t, "prod", b.GetNamespace())
		require.Equal(t, []string{f.siteA}, b.GetSiteIds())
		require.Equal(t, []string{"shop"}, b.GetSites())
		require.Equal(t, []string{"identity:reveal"}, b.GetExtraPermissions())
	})

	var carolID string
	t.Run("list users", func(t *testing.T) {
		resp, err := f.h.ListUsers(f.as(f.owner), connect.NewRequest(&spinneretv1.ListUsersRequest{Query: "car"}))
		require.NoError(t, err)
		require.EqualValues(t, 1, resp.Msg.GetTotal())
		carolID = resp.Msg.GetUsers()[0].GetUser().GetId()
		require.Len(t, resp.Msg.GetUsers()[0].GetBindings(), 1)
		_, err = f.h.ListUsers(f.as(f.viewer), connect.NewRequest(&spinneretv1.ListUsersRequest{}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.ListUsers(f.ctx, connect.NewRequest(&spinneretv1.ListUsersRequest{}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("update and reset", func(t *testing.T) {
		dn, locale := "Carol", "en"
		resp, err := f.h.UpdateUser(f.as(f.owner), connect.NewRequest(&spinneretv1.UpdateUserRequest{Id: carolID, DisplayName: &dn, Locale: &locale}))
		require.NoError(t, err)
		require.Equal(t, dn, resp.Msg.GetUser().GetDisplayName())
		_, err = f.h.UpdateUser(f.as(f.viewer), connect.NewRequest(&spinneretv1.UpdateUserRequest{Id: carolID, DisplayName: &dn}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.UpdateUser(f.ctx, connect.NewRequest(&spinneretv1.UpdateUserRequest{Id: carolID}))
		requireReason(t, err, apperr.ReasonSessionInvalid)

		_, err = f.h.ResetPassword(f.as(f.owner), connect.NewRequest(&spinneretv1.ResetPasswordRequest{UserId: carolID, NewPassword: "another-long-password"}))
		require.NoError(t, err)
		_, err = f.h.ResetPassword(f.as(f.viewer), connect.NewRequest(&spinneretv1.ResetPasswordRequest{UserId: carolID, NewPassword: "another-long-password"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.ResetPassword(f.ctx, connect.NewRequest(&spinneretv1.ResetPasswordRequest{UserId: carolID}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("role bindings", func(t *testing.T) {
		created, err := f.h.CreateRoleBinding(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateRoleBindingRequest{UserId: carolID, Role: "viewer"}))
		require.NoError(t, err)
		require.Empty(t, created.Msg.GetBinding().GetNamespaceId())
		_, err = f.h.CreateRoleBinding(f.as(f.viewer), connect.NewRequest(&spinneretv1.CreateRoleBindingRequest{UserId: carolID, Role: "viewer"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.CreateRoleBinding(f.as(f.owner), connect.NewRequest(&spinneretv1.CreateRoleBindingRequest{UserId: carolID, Role: "viewer", Namespace: "prod", Sites: []string{"nope"}}))
		requireReason(t, err, apperr.ReasonSiteUnknown)
		_, err = f.h.CreateRoleBinding(f.ctx, connect.NewRequest(&spinneretv1.CreateRoleBindingRequest{UserId: carolID}))
		requireReason(t, err, apperr.ReasonSessionInvalid)

		list, err := f.h.ListRoleBindings(f.as(f.owner), connect.NewRequest(&spinneretv1.ListRoleBindingsRequest{UserId: carolID}))
		require.NoError(t, err)
		require.EqualValues(t, 2, list.Msg.GetTotal())
		_, err = f.h.ListRoleBindings(f.as(f.viewer), connect.NewRequest(&spinneretv1.ListRoleBindingsRequest{}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.ListRoleBindings(f.ctx, connect.NewRequest(&spinneretv1.ListRoleBindingsRequest{}))
		requireReason(t, err, apperr.ReasonSessionInvalid)

		_, err = f.h.DeleteRoleBinding(f.as(f.viewer), connect.NewRequest(&spinneretv1.DeleteRoleBindingRequest{Id: created.Msg.GetBinding().GetId()}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = f.h.DeleteRoleBinding(f.as(f.owner), connect.NewRequest(&spinneretv1.DeleteRoleBindingRequest{Id: created.Msg.GetBinding().GetId()}))
		require.NoError(t, err)
		_, err = f.h.DeleteRoleBinding(f.ctx, connect.NewRequest(&spinneretv1.DeleteRoleBindingRequest{Id: "rb_1"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})
}

func TestListAuditLogsRPC(t *testing.T) {
	f := newFixture(t)
	resp, err := f.h.ListAuditLogs(f.as(f.viewer), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{
		Namespace: "prod",
		TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(time.Now().Add(-time.Hour)), End: timestamppb.New(time.Now().Add(time.Hour))},
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetLogs(), 1)
	l := resp.Msg.GetLogs()[0]
	require.Equal(t, "prod", l.GetNamespace())
	require.Equal(t, "identity.ban", l.GetAction())
	require.Equal(t, "v", l.GetDetails().GetFields()["k"].GetStringValue())

	resp, err = f.h.ListAuditLogs(f.as(f.owner), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{Action: "nothing"}))
	require.NoError(t, err)
	require.Empty(t, resp.Msg.GetLogs())

	_, err = f.h.ListAuditLogs(f.as(f.owner), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{Namespace: "nope"}))
	requireReason(t, err, apperr.ReasonNotFound)
	nsScoped := authtest.UserPrincipal("usr_x", f.tenant, authz.Binding{TenantID: f.tenant, Role: authz.RoleViewer, NamespaceID: f.ns})
	resp, err = f.h.ListAuditLogs(f.as(nsScoped), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{}))
	require.NoError(t, err, "namespace-scoped readers get the entries of their namespaces")
	require.Len(t, resp.Msg.GetLogs(), 1)
	siteScoped := authtest.UserPrincipal("usr_y", f.tenant,
		authz.Binding{TenantID: f.tenant, Role: authz.RoleViewer, NamespaceID: f.ns, SiteIDs: []string{f.siteA}})
	_, err = f.h.ListAuditLogs(f.as(siteScoped), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.ListAuditLogs(f.as(siteScoped), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{Namespace: "prod"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = f.h.ListAuditLogs(f.ctx, connect.NewRequest(&spinneretv1.ListAuditLogsRequest{}))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	nodeToken := authtest.TokenPrincipal("tok_node", f.tenant, f.ns, "prod", "lease:acquire")
	_, err = f.h.ListAuditLogs(f.as(nodeToken), connect.NewRequest(&spinneretv1.ListAuditLogsRequest{}))
	requireReason(t, err, apperr.ReasonScopeMissing)
}
