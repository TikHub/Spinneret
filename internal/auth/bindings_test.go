package auth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth/authtest"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

func TestRoleBindingsCRUD(t *testing.T) {
	e := newEnv(t)
	e.runAuthenticator()
	w := e.world("acme")
	other := e.world("globex")
	member := authtest.User(t, e.pool, "member", e.hash(testPassword), false)

	// Warm the authenticator cache so the change event is observable.
	_, err := e.auth.users.user(e.ctx(), member)
	require.NoError(t, err)

	b, err := e.users.CreateRoleBinding(e.ctx(), w.ownerP(), member, BindingSpec{
		Role: "operator", NamespaceID: w.ns, SiteIDs: []string{w.siteA}, ExtraPermissions: []string{"identity:reveal"},
	})
	require.NoError(t, err)
	require.Equal(t, "member", b.Username)
	require.Equal(t, []string{"shop"}, b.Sites)
	require.Equal(t, []string{"identity:reveal"}, b.ExtraPermissions)
	require.Equal(t, 0, e.auth.users.users.Len(), "user.changed event dropped the cached user")
	_, ok := e.rec.Last(ActionBindingCreate)
	require.True(t, ok)

	_, err = e.users.CreateRoleBinding(e.ctx(), w.ownerP(), member, BindingSpec{
		Role: "operator", NamespaceID: w.ns, SiteIDs: []string{w.siteA}, ExtraPermissions: []string{"identity:reveal"},
	})
	requireReason(t, err, apperr.ReasonAlreadyExists)
	_, err = e.users.CreateRoleBinding(e.ctx(), w.ownerP(), "usr_missing", BindingSpec{Role: "viewer"})
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.users.CreateRoleBinding(e.ctx(), w.viewerP(), member, BindingSpec{Role: "viewer"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = e.users.CreateRoleBinding(e.ctx(), w.ownerP(), member, BindingSpec{Role: "viewer", NamespaceID: other.ns})
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.users.CreateRoleBinding(e.ctx(), w.ownerP(), member, BindingSpec{Role: "bogus"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.users.CreateRoleBinding(e.ctx(), authtest.UserPrincipal(w.owner, ""), member, BindingSpec{Role: "viewer"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	owner2, err := e.users.CreateRoleBinding(e.ctx(), w.ownerP(), member, BindingSpec{Role: "owner"})
	require.NoError(t, err)

	t.Run("list with pagination and filter", func(t *testing.T) {
		var got []BindingView
		token := ""
		for {
			page, next, total, err := e.users.ListRoleBindings(e.ctx(), w.ownerP(), "", 2, token)
			require.NoError(t, err)
			require.EqualValues(t, 4, total)
			got = append(got, page...)
			if next == "" {
				break
			}
			token = next
		}
		require.Len(t, got, 4)
		require.Equal(t, "acme-owner", got[0].Username)
		require.Equal(t, "member", got[2].Username)

		page, _, total, err := e.users.ListRoleBindings(e.ctx(), w.ownerP(), member, 0, "")
		require.NoError(t, err)
		require.EqualValues(t, 2, total)
		require.Len(t, page, 2)

		_, _, _, err = e.users.ListRoleBindings(e.ctx(), w.viewerP(), "", 0, "")
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, _, _, err = e.users.ListRoleBindings(e.ctx(), w.ownerP(), "", 0, "bad!")
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, _, _, err = e.users.ListRoleBindings(e.ctx(), authtest.UserPrincipal(w.owner, ""), "", 0, "")
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("delete", func(t *testing.T) {
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), w.ownerP(), other.ownerBinding.ID), apperr.ReasonNotFound)
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), w.ownerP(), "rb_missing"), apperr.ReasonNotFound)
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), w.viewerP(), b.ID), apperr.ReasonPermissionDenied)
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), authtest.UserPrincipal(w.owner, ""), b.ID), apperr.ReasonInvalidArgument)
		require.NoError(t, e.users.DeleteRoleBinding(e.ctx(), w.ownerP(), b.ID))
		_, ok := e.rec.Last(ActionBindingDelete)
		require.True(t, ok)

		// Two tenant-wide owners: one can go, the last cannot.
		require.NoError(t, e.users.DeleteRoleBinding(e.ctx(), w.ownerP(), owner2.ID))
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), w.ownerP(), w.ownerBinding.ID), apperr.ReasonFailedPrecondition)

		// A non-owner principal with user:write (platform-independent check)
		// cannot remove owner bindings.
		restricted := authtest.UserPrincipal("usr_x", w.tenant, authz.Binding{TenantID: w.tenant, Role: authz.RoleOwner, NamespaceID: w.ns})
		requireReason(t, e.users.DeleteRoleBinding(e.ctx(), restricted, w.ownerBinding.ID), apperr.ReasonPermissionDenied)
	})
}

func TestNormalizeBindingSpec(t *testing.T) {
	many := make([]string, maxBindingSites+1)
	for i := range many {
		many[i] = "sit_" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
	}
	_, err := normalizeBindingSpec(BindingSpec{Role: "viewer", NamespaceID: "ns_1", SiteIDs: many})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	spec, err := normalizeBindingSpec(BindingSpec{Role: "admin", ExtraPermissions: []string{"secret:reveal", "config:publish", "secret:reveal"}})
	require.NoError(t, err)
	require.Equal(t, []string{"config:publish", "secret:reveal"}, spec.ExtraPermissions)
	require.Equal(t, []string{}, spec.SiteIDs)
}
