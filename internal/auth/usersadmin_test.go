package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
)

func TestCreateUser(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	admin := authtest.AdminPrincipal(w.platformAdmin, w.tenant)

	valid := CreateUserInput{
		Username: "New.User", DisplayName: "New User", Email: "new@example.com", Password: "a-long-password",
		Binding: BindingSpec{Role: "operator", NamespaceID: w.ns, SiteIDs: []string{w.siteB, w.siteA, w.siteA}, ExtraPermissions: []string{"config:publish"}},
	}
	res, err := e.users.CreateUser(e.ctx(), w.ownerP(), valid)
	require.NoError(t, err)
	require.False(t, res.Existing)
	require.Equal(t, "new.user", res.User.Username)
	require.Equal(t, "new@example.com", res.User.Email)
	require.Equal(t, "operator", res.Binding.Role)
	require.Equal(t, "prod", res.Binding.Namespace)
	require.Len(t, res.Binding.SiteIDs, 2, "sites are de-duplicated")
	require.ElementsMatch(t, []string{"shop", "market"}, res.Binding.Sites)
	require.Equal(t, "new.user", res.Binding.Username)
	entry, ok := e.rec.Last(ActionUserCreate)
	require.True(t, ok)
	require.Equal(t, w.tenant, entry.TenantID)
	_, _, err = e.users.Login(e.ctx(), "new.user", "a-long-password", "", "")
	require.NoError(t, err)

	t.Run("existing username", func(t *testing.T) {
		_, err := e.users.CreateUser(e.ctx(), w.ownerP(), valid)
		requireReason(t, err, apperr.ReasonAlreadyExists)

		// Platform admins add a binding to the existing account instead.
		adminOther := authtest.AdminPrincipal(w.platformAdmin, other.tenant)
		in := valid
		in.Password = ""
		in.Binding = BindingSpec{Role: "viewer"}
		res, err := e.users.CreateUser(e.ctx(), adminOther, in)
		require.NoError(t, err)
		require.True(t, res.Existing)
		require.Equal(t, other.tenant, res.Binding.TenantID)
		_, err = e.users.CreateUser(e.ctx(), adminOther, in)
		requireReason(t, err, apperr.ReasonAlreadyExists)
		_, ok := e.rec.Last(ActionBindingCreate)
		require.True(t, ok)
	})

	tests := []struct {
		name   string
		p      *authz.Principal
		mutate func(*CreateUserInput)
		reason apperr.Reason
	}{
		{"viewer denied", w.viewerP(), nil, apperr.ReasonPermissionDenied},
		{"no active tenant", authtest.UserPrincipal(w.owner, "", w.ownerBinding), nil, apperr.ReasonInvalidArgument},
		{"token denied", authtest.TokenPrincipal("tok_1", w.tenant, w.ns, "prod", "admin"), nil, apperr.ReasonScopeMissing},
		{"invalid username", admin, func(in *CreateUserInput) { in.Username = "x" }, apperr.ReasonInvalidArgument},
		{"weak password", admin, func(in *CreateUserInput) { in.Username = "weak-pw"; in.Password = "123" }, apperr.ReasonInvalidArgument},
		{"invalid email", admin, func(in *CreateUserInput) { in.Username = "bad-email"; in.Email = "nope" }, apperr.ReasonInvalidArgument},
		{"invalid display name", admin, func(in *CreateUserInput) { in.Username = "bad-dn"; in.DisplayName = "a\x00b" }, apperr.ReasonInvalidArgument},
		{"invalid role", admin, func(in *CreateUserInput) { in.Username = "bad-role"; in.Binding.Role = "god" }, apperr.ReasonInvalidArgument},
		{"sites require namespace", admin, func(in *CreateUserInput) { in.Username = "no-ns"; in.Binding.NamespaceID = "" }, apperr.ReasonInvalidArgument},
		{"invalid extra permission", admin, func(in *CreateUserInput) {
			in.Username = "bad-extra"
			in.Binding.ExtraPermissions = []string{"user:write"}
		}, apperr.ReasonInvalidArgument},
		{"namespace of another tenant", admin, func(in *CreateUserInput) {
			in.Username = "foreign-ns"
			in.Binding.NamespaceID = other.ns
			in.Binding.SiteIDs = nil
		}, apperr.ReasonNotFound},
		{"site outside namespace", admin, func(in *CreateUserInput) {
			in.Username = "foreign-site"
			in.Binding.SiteIDs = []string{other.siteA}
		}, apperr.ReasonSiteUnknown},
		{"empty site id", admin, func(in *CreateUserInput) { in.Username = "empty-site"; in.Binding.SiteIDs = []string{""} }, apperr.ReasonSiteUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			in.Username = "fresh-user"
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			_, err := e.users.CreateUser(e.ctx(), tc.p, in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("owner role requires a tenant owner", func(t *testing.T) {
		in := valid
		in.Username = "second-owner"
		in.Binding = BindingSpec{Role: "owner"}
		res, err := e.users.CreateUser(e.ctx(), w.ownerP(), in)
		require.NoError(t, err)
		require.Equal(t, "owner", res.Binding.Role)
		require.False(t, tenantOwner(authtest.UserPrincipal("u", w.tenant, authz.Binding{TenantID: w.tenant, Role: authz.RoleOwner, NamespaceID: w.ns}), w.tenant))
		require.False(t, tenantOwner(nil, w.tenant))
	})
}

func TestUpdateUserAndResetPassword(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	member := authtest.User(t, e.pool, "member", e.hash(testPassword), false)
	authtest.Binding(t, e.pool, member, w.tenant, authz.RoleViewer, "", nil)
	crossTenant := authtest.User(t, e.pool, "cross", e.hash(testPassword), false)
	authtest.Binding(t, e.pool, crossTenant, w.tenant, authz.RoleViewer, "", nil)
	authtest.Binding(t, e.pool, crossTenant, other.tenant, authz.RoleViewer, "", nil)
	adminMember := authtest.User(t, e.pool, "root-member", e.hash(testPassword), true)
	authtest.Binding(t, e.pool, adminMember, w.tenant, authz.RoleViewer, "", nil)

	name, email, locale := "Member One", "member@example.com", "zh-CN"
	disabled := true
	cookie := e.login("member")

	updated, err := e.users.UpdateUser(e.ctx(), w.ownerP(), member, UpdateUserInput{DisplayName: &name, Email: &email, Locale: &locale})
	require.NoError(t, err)
	require.Equal(t, name, updated.DisplayName)
	require.Equal(t, locale, updated.Locale)
	require.False(t, updated.Disabled)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)

	updated, err = e.users.UpdateUser(e.ctx(), w.ownerP(), member, UpdateUserInput{Disabled: &disabled})
	require.NoError(t, err)
	require.True(t, updated.Disabled)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	entry, ok := e.rec.Last(ActionUserUpdate)
	require.True(t, ok)
	require.Equal(t, true, entry.Details["disabled"])

	bad := "not an email"
	badLocale := "zh_CN"
	badName := "x\x01"
	tests := []struct {
		name   string
		p      *authz.Principal
		id     string
		in     UpdateUserInput
		reason apperr.Reason
	}{
		{"viewer denied", w.viewerP(), member, UpdateUserInput{DisplayName: &name}, apperr.ReasonPermissionDenied},
		{"non member hidden", w.ownerP(), other.viewer, UpdateUserInput{DisplayName: &name}, apperr.ReasonNotFound},
		{"unknown user", w.ownerP(), "usr_missing", UpdateUserInput{DisplayName: &name}, apperr.ReasonNotFound},
		{"member of unmanaged tenant", w.ownerP(), crossTenant, UpdateUserInput{DisplayName: &name}, apperr.ReasonPermissionDenied},
		{"platform admin target", w.ownerP(), adminMember, UpdateUserInput{DisplayName: &name}, apperr.ReasonPermissionDenied},
		{"disable yourself", w.ownerP(), w.owner, UpdateUserInput{Disabled: &disabled}, apperr.ReasonFailedPrecondition},
		{"invalid email", w.ownerP(), member, UpdateUserInput{Email: &bad}, apperr.ReasonInvalidArgument},
		{"invalid locale", w.ownerP(), member, UpdateUserInput{Locale: &badLocale}, apperr.ReasonInvalidArgument},
		{"invalid name", w.ownerP(), member, UpdateUserInput{DisplayName: &badName}, apperr.ReasonInvalidArgument},
		{"nil principal", nil, member, UpdateUserInput{}, apperr.ReasonSessionInvalid},
		{"no active tenant", authtest.UserPrincipal(w.owner, ""), member, UpdateUserInput{}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.users.UpdateUser(e.ctx(), tc.p, tc.id, tc.in)
			requireReason(t, err, tc.reason)
		})
	}

	// Platform admins manage anyone, including cross-tenant members.
	_, err = e.users.UpdateUser(e.ctx(), authtest.AdminPrincipal(w.platformAdmin, ""), crossTenant, UpdateUserInput{DisplayName: &name})
	require.NoError(t, err)

	t.Run("reset password", func(t *testing.T) {
		enabled := false
		_, err := e.users.UpdateUser(e.ctx(), w.ownerP(), member, UpdateUserInput{Disabled: &enabled})
		require.NoError(t, err)
		session := e.login("member")
		requireReason(t, e.users.ResetPassword(e.ctx(), w.ownerP(), member, "short"), apperr.ReasonInvalidArgument)
		requireReason(t, e.users.ResetPassword(e.ctx(), w.viewerP(), member, "another-password"), apperr.ReasonPermissionDenied)
		requireReason(t, e.users.ResetPassword(e.ctx(), w.ownerP(), other.owner, "another-password"), apperr.ReasonNotFound)

		require.NoError(t, e.users.ResetPassword(e.ctx(), w.ownerP(), member, "another-password"))
		_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", session, nil))
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, _, err = e.users.Login(e.ctx(), "member", "another-password", "", "")
		require.NoError(t, err)
		_, ok := e.rec.Last(ActionUserResetPassword)
		require.True(t, ok)
	})
}

func TestListUsers(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		id := authtest.User(t, e.pool, name, e.hash(testPassword), false)
		authtest.Binding(t, e.pool, id, w.tenant, authz.RoleOperator, w.ns, []string{w.siteA})
	}

	var all []TenantUser
	token := ""
	for {
		page, next, total, err := e.users.ListUsers(e.ctx(), w.ownerP(), ListUsersInput{PageSize: 2, PageToken: token})
		require.NoError(t, err)
		require.EqualValues(t, 5, total, "alpha, bravo, charlie, owner, viewer")
		all = append(all, page...)
		if next == "" {
			break
		}
		token = next
	}
	require.Len(t, all, 5)
	require.Equal(t, "acme-owner", all[0].User.Username)
	require.Equal(t, "alpha", all[2].User.Username)
	require.Len(t, all[2].Bindings, 1)
	require.Equal(t, []string{"shop"}, all[2].Bindings[0].Sites)

	page, _, total, err := e.users.ListUsers(e.ctx(), w.ownerP(), ListUsersInput{Query: "RAV"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "bravo", page[0].User.Username)

	_, _, total, err = e.users.ListUsers(e.ctx(), authtest.AdminPrincipal(w.platformAdmin, ""), ListUsersInput{AllUsers: true})
	require.NoError(t, err)
	require.EqualValues(t, 9, total)

	_, _, _, err = e.users.ListUsers(e.ctx(), w.ownerP(), ListUsersInput{AllUsers: true})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, _, _, err = e.users.ListUsers(e.ctx(), w.viewerP(), ListUsersInput{})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, _, _, err = e.users.ListUsers(e.ctx(), other.ownerP(), ListUsersInput{PageToken: "%%%"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, _, err = e.users.ListUsers(e.ctx(), w.ownerP(), ListUsersInput{Query: string(make([]byte, 300))})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, _, err = e.users.ListUsers(e.ctx(), authtest.UserPrincipal(w.owner, ""), ListUsersInput{})
	requireReason(t, err, apperr.ReasonInvalidArgument)
}
