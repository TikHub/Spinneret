package tenantapi

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/tenancy"
	"github.com/TikHub/Spinneret/internal/testutil"
)

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestTenantAdminHandler(t *testing.T) {
	pool := testutil.Postgres(t)
	cat := catalogtest.New()
	installer := &authtest.Installer{}
	h := New(tenancy.NewService(pool, cat, installer, &authtest.Recorder{}, slog.New(slog.DiscardHandler)), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenantID := authtest.Tenant(t, pool, "acme")
	authtest.Namespace(t, pool, tenantID, "prod")
	ownerID := authtest.User(t, pool, "owner", "x", false)
	owner := authtest.UserPrincipal(ownerID, tenantID, authtest.Binding(t, pool, ownerID, tenantID, authz.RoleOwner, "", nil))
	viewerID := authtest.User(t, pool, "viewer", "x", false)
	viewer := authtest.UserPrincipal(viewerID, tenantID, authtest.Binding(t, pool, viewerID, tenantID, authz.RoleViewer, "", nil))
	admin := authtest.AdminPrincipal(authtest.User(t, pool, "root", "x", true), "")
	as := func(p *authz.Principal) context.Context { return authz.WithPrincipal(ctx, p) }

	t.Run("unauthenticated", func(t *testing.T) {
		_, err := h.ListTenants(ctx, connect.NewRequest(&spinneretv1.ListTenantsRequest{}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.CreateTenant(ctx, connect.NewRequest(&spinneretv1.CreateTenantRequest{Name: "x1"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.UpdateTenant(ctx, connect.NewRequest(&spinneretv1.UpdateTenantRequest{Id: tenantID}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.DeleteTenant(ctx, connect.NewRequest(&spinneretv1.DeleteTenantRequest{Id: tenantID}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.ListNamespaces(ctx, connect.NewRequest(&spinneretv1.ListNamespacesRequest{}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.CreateNamespace(ctx, connect.NewRequest(&spinneretv1.CreateNamespaceRequest{Name: "qa"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.UpdateNamespace(ctx, connect.NewRequest(&spinneretv1.UpdateNamespaceRequest{Id: "ns_1"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = h.DeleteNamespace(ctx, connect.NewRequest(&spinneretv1.DeleteNamespaceRequest{Id: "ns_1"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	var globexID string
	t.Run("tenants", func(t *testing.T) {
		_, err := h.CreateTenant(as(owner), connect.NewRequest(&spinneretv1.CreateTenantRequest{Name: "globex"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		created, err := h.CreateTenant(as(admin), connect.NewRequest(&spinneretv1.CreateTenantRequest{Name: "globex", DisplayName: "Globex"}))
		require.NoError(t, err)
		globexID = created.Msg.GetTenant().GetId()
		require.NotNil(t, created.Msg.GetTenant().GetCreatedAt())

		list, err := h.ListTenants(as(admin), connect.NewRequest(&spinneretv1.ListTenantsRequest{}))
		require.NoError(t, err)
		require.EqualValues(t, 2, list.Msg.GetTotal())
		mine, err := h.ListTenants(as(viewer), connect.NewRequest(&spinneretv1.ListTenantsRequest{}))
		require.NoError(t, err)
		require.Len(t, mine.Msg.GetTenants(), 1)
		_, err = h.ListTenants(as(viewer), connect.NewRequest(&spinneretv1.ListTenantsRequest{PageToken: "!"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)

		dn := "Globex Corp"
		updated, err := h.UpdateTenant(as(admin), connect.NewRequest(&spinneretv1.UpdateTenantRequest{Id: globexID, DisplayName: &dn}))
		require.NoError(t, err)
		require.Equal(t, dn, updated.Msg.GetTenant().GetDisplayName())
		_, err = h.UpdateTenant(as(viewer), connect.NewRequest(&spinneretv1.UpdateTenantRequest{Id: globexID, DisplayName: &dn}))
		requireReason(t, err, apperr.ReasonPermissionDenied)

		_, err = h.DeleteTenant(as(owner), connect.NewRequest(&spinneretv1.DeleteTenantRequest{Id: globexID}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.DeleteTenant(as(admin), connect.NewRequest(&spinneretv1.DeleteTenantRequest{Id: globexID}))
		require.NoError(t, err)
	})

	t.Run("namespaces", func(t *testing.T) {
		_, err := h.CreateNamespace(as(viewer), connect.NewRequest(&spinneretv1.CreateNamespaceRequest{Name: "staging"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		created, err := h.CreateNamespace(as(owner), connect.NewRequest(&spinneretv1.CreateNamespaceRequest{Name: "staging", Description: "d"}))
		require.NoError(t, err)
		nsID := created.Msg.GetNamespace().GetId()
		require.Equal(t, tenantID, created.Msg.GetNamespace().GetTenantId())
		require.Len(t, installer.Calls(), 1)

		list, err := h.ListNamespaces(as(viewer), connect.NewRequest(&spinneretv1.ListNamespacesRequest{PageSize: 1}))
		require.NoError(t, err)
		require.EqualValues(t, 2, list.Msg.GetTotal())
		require.Len(t, list.Msg.GetNamespaces(), 1)
		require.NotEmpty(t, list.Msg.GetNextPageToken())
		_, err = h.ListNamespaces(as(authtest.UserPrincipal(ownerID, "")), connect.NewRequest(&spinneretv1.ListNamespacesRequest{}))
		requireReason(t, err, apperr.ReasonInvalidArgument)

		dn := "Staging"
		_, err = h.UpdateNamespace(as(viewer), connect.NewRequest(&spinneretv1.UpdateNamespaceRequest{Id: nsID, DisplayName: &dn}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		updated, err := h.UpdateNamespace(as(owner), connect.NewRequest(&spinneretv1.UpdateNamespaceRequest{Id: nsID, DisplayName: &dn}))
		require.NoError(t, err)
		require.Equal(t, dn, updated.Msg.GetNamespace().GetDisplayName())

		_, err = h.DeleteNamespace(as(viewer), connect.NewRequest(&spinneretv1.DeleteNamespaceRequest{Id: nsID}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.DeleteNamespace(as(owner), connect.NewRequest(&spinneretv1.DeleteNamespaceRequest{Id: nsID}))
		require.NoError(t, err)
		require.Contains(t, cat.Calls(), "invalidate:"+nsID)
	})
}
