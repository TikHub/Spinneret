package identitysvc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
)

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestCreateIdentityType(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	owner := env.Owner(env.NS)

	t.Run("site from the request overrides the spec", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.WebCookieYAML, "site: shop", "site: somewhere_else", 1)
		it, err := env.Service.CreateIdentityType(ctx, owner, env.NS, env.SiteB.Name, strings.Replace(spec, "web_cookie", "alt_cookie", 1))
		require.NoError(t, err)
		require.Equal(t, env.SiteB.ID, it.SiteID)
		require.Equal(t, "market", it.SiteName)
		require.Equal(t, "default", it.NamespaceName)
		require.Equal(t, 1, it.Version)
		require.Contains(t, it.SpecYAML, "site: market")
		require.NotContains(t, it.SpecYAML, "somewhere_else")
		require.NotNil(t, it.Spec)
		require.Equal(t, identity.ActivationProbe, it.Spec.Activation)
		require.Contains(t, string(it.JSONSchema), `"cookies"`)
		require.Contains(t, env.Catalog.Calls(), "invalidate:"+env.NS.ID)
		entry, ok := env.Audit.Last("identity_type.create")
		require.True(t, ok)
		require.Equal(t, it.ID, entry.ResourceID)
		require.Equal(t, owner.ID, entry.ActorID)
	})

	t.Run("site missing in the spec is inserted", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.AppDeviceYAML, "site: shop\n", "", 1)
		it, err := env.Service.CreateIdentityType(ctx, owner, env.NS, env.SiteA.Name, spec)
		require.NoError(t, err)
		require.Contains(t, it.SpecYAML, "site: shop")
		_, err = identity.ParseTypeYAML([]byte(it.SpecYAML))
		require.NoError(t, err)
		require.Equal(t, identity.ActivationImmediate, it.Spec.Activation)
	})

	t.Run("spec that already names the site is stored verbatim", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "verbatim_cookie", 1)
		it, err := env.Service.CreateIdentityType(ctx, owner, env.NS, env.SiteA.Name, spec)
		require.NoError(t, err)
		require.Equal(t, spec, it.SpecYAML)
	})

	tests := []struct {
		name   string
		site   string
		spec   string
		p      *authz.Principal
		reason apperr.Reason
	}{
		{"duplicate name", "shop", identitysvctest.AppDeviceYAML, owner, apperr.ReasonAlreadyExists},
		{"client of another site", "market", strings.Replace(identitysvctest.AppDeviceYAML, "app_device", "x", 1), owner, apperr.ReasonClientUnknown},
		{"invalid spec", "shop", "name: bad\nclient: web\nfields: {}\n", owner, apperr.ReasonInvalidArgument},
		{"not yaml mapping", "shop", "- a\n- b\n", owner, apperr.ReasonInvalidArgument},
		{"two documents", "shop", "name: a\n---\nname: b\n", owner, apperr.ReasonInvalidArgument},
		{"empty spec", "shop", "  \n", owner, apperr.ReasonInvalidArgument},
		{"unknown site", "nope", identitysvctest.WebCookieYAML, owner, apperr.ReasonSiteUnknown},
		{"operator lacks site:write", "shop", strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "op", 1), env.Role(authz.RoleOperator), apperr.ReasonPermissionDenied},
		{"token lacks site:write", "shop", strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "tok", 1), env.Token(t, "identity:write"), apperr.ReasonScopeMissing},
		{"oversized spec", "shop", identitysvctest.WebCookieYAML + "#" + strings.Repeat("x", identitysvc.MaxSpecYAMLBytes), owner, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.CreateIdentityType(ctx, tc.p, env.NS, tc.site, tc.spec)
			requireReason(t, err, tc.reason)
		})
	}
}

func TestGetListIdentityTypes(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	app := env.CreateType(t, env.SiteA, identitysvctest.AppDeviceYAML)
	alt := env.CreateType(t, env.SiteB, strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "alt_cookie", 1))
	env.CreateType(t, env.OtherSite, identitysvctest.WebCookieYAML)

	t.Run("get", func(t *testing.T) {
		got, err := env.Service.GetIdentityType(ctx, env.Role(authz.RoleViewer), web.ID)
		require.NoError(t, err)
		require.Equal(t, web.ID, got.ID)
		require.Equal(t, "shop", got.SiteName)
		require.Equal(t, 0, got.IdentityCount)
		require.Equal(t, []string{"cookies.sessionid"}, got.Spec.UniqueBy)
	})

	t.Run("get denials", func(t *testing.T) {
		_, err := env.Service.GetIdentityType(ctx, env.Stranger(), web.ID)
		requireReason(t, err, apperr.ReasonNotFound)
		// Principals without any binding in the namespace cannot probe IDs.
		_, err = env.Service.GetIdentityType(ctx, env.NoRole(), web.ID)
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = env.Service.GetIdentityType(ctx, env.SiteRole(authz.RoleViewer, env.SiteB), web.ID)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.GetIdentityType(ctx, env.Role(authz.RoleViewer), "ity_missing")
		requireReason(t, err, apperr.ReasonNotFound)
	})

	t.Run("list all with pagination", func(t *testing.T) {
		var ids []string
		token := ""
		for {
			page, err := env.Service.ListIdentityTypes(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.TypeQuery{PageSize: 2, PageToken: token})
			require.NoError(t, err)
			require.Equal(t, 3, page.Total)
			for _, it := range page.Types {
				ids = append(ids, it.ID)
			}
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		}
		require.ElementsMatch(t, []string{web.ID, app.ID, alt.ID}, ids)
	})

	tests := []struct {
		name  string
		p     *authz.Principal
		query identitysvc.TypeQuery
		want  []string
	}{
		{"site filter", env.Role(authz.RoleViewer), identitysvc.TypeQuery{Site: "market"}, []string{alt.ID}},
		{"client filter", env.Role(authz.RoleViewer), identitysvc.TypeQuery{Client: "app"}, []string{app.ID}},
		{"search", env.Role(authz.RoleViewer), identitysvc.TypeQuery{Search: "COOKIE"}, []string{web.ID, alt.ID}},
		{"site restricted principal", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.TypeQuery{}, []string{alt.ID}},
		{"site scoped token", env.Token(t, "identity:write:shop"), identitysvc.TypeQuery{}, []string{web.ID, app.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := env.Service.ListIdentityTypes(ctx, tc.p, env.NS, tc.query)
			require.NoError(t, err)
			var got []string
			for _, it := range page.Types {
				got = append(got, it.ID)
			}
			require.ElementsMatch(t, tc.want, got)
			require.Equal(t, len(tc.want), page.Total)
		})
	}

	t.Run("list denials and errors", func(t *testing.T) {
		_, err := env.Service.ListIdentityTypes(ctx, env.NoRole(), env.NS, identitysvc.TypeQuery{})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.ListIdentityTypes(ctx, env.SiteRole(authz.RoleViewer, env.SiteB), env.NS, identitysvc.TypeQuery{Site: "shop"})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.ListIdentityTypes(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.TypeQuery{PageToken: "!!"})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = env.Service.ListIdentityTypes(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.TypeQuery{Site: "nope"})
		requireReason(t, err, apperr.ReasonSiteUnknown)
	})
}

func TestUpdateDeleteIdentityType(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	owner := env.Owner(env.NS)
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)

	t.Run("update bumps the version", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.WebCookieYAML, "activation: probe", "activation: immediate", 1)
		spec = strings.Replace(spec, "site: shop\n", "", 1)
		got, err := env.Service.UpdateIdentityType(ctx, owner, web.ID, spec)
		require.NoError(t, err)
		require.Equal(t, 2, got.Version)
		require.Equal(t, identity.ActivationImmediate, got.Spec.Activation)
		require.Contains(t, got.SpecYAML, "site: shop")
		entry, ok := env.Audit.Last("identity_type.update")
		require.True(t, ok)
		require.EqualValues(t, 2, entry.Details["version"])
	})

	tests := []struct {
		name   string
		spec   string
		p      *authz.Principal
		reason apperr.Reason
	}{
		{"rename", strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "renamed", 1), owner, apperr.ReasonInvalidArgument},
		{"change client", strings.Replace(identitysvctest.WebCookieYAML, "client: web", "client: app", 1), owner, apperr.ReasonInvalidArgument},
		{"change site", strings.Replace(identitysvctest.WebCookieYAML, "site: shop", "site: market", 1), owner, apperr.ReasonInvalidArgument},
		{"invalid spec", "name: web_cookie\nclient: web\nfields: {}\n", owner, apperr.ReasonInvalidArgument},
		{"viewer", identitysvctest.WebCookieYAML, env.Role(authz.RoleViewer), apperr.ReasonPermissionDenied},
		{"stranger", identitysvctest.WebCookieYAML, env.Stranger(), apperr.ReasonNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.UpdateIdentityType(ctx, tc.p, web.ID, tc.spec)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("delete fails while identities exist", func(t *testing.T) {
		env.RegisterType(t, env.SiteA, mustGetType(t, env, web.ID))
		_, err := env.Service.ImportIdentities(ctx, owner, env.NS, identitysvc.ImportInput{
			Site: "shop", Type: "web_cookie", Format: "jsonl", Data: `{"cookies":"sessionid=abc"}`,
		})
		require.NoError(t, err)
		err = env.Service.DeleteIdentityType(ctx, owner, web.ID)
		requireReason(t, err, apperr.ReasonFailedPrecondition)
	})

	t.Run("delete", func(t *testing.T) {
		app := env.CreateType(t, env.SiteA, identitysvctest.AppDeviceYAML)
		requireReason(t, env.Service.DeleteIdentityType(ctx, env.Role(authz.RoleOperator), app.ID), apperr.ReasonPermissionDenied)
		require.NoError(t, env.Service.DeleteIdentityType(ctx, owner, app.ID))
		_, err := env.Service.GetIdentityType(ctx, owner, app.ID)
		requireReason(t, err, apperr.ReasonNotFound)
		_, ok := env.Audit.Last("identity_type.delete")
		require.True(t, ok)
		requireReason(t, env.Service.DeleteIdentityType(ctx, owner, app.ID), apperr.ReasonNotFound)
	})
}

func mustGetType(t *testing.T, env *identitysvctest.Env, id string) identitysvc.IdentityType {
	t.Helper()
	it, err := env.Service.GetIdentityType(context.Background(), env.Owner(env.NS), id)
	require.NoError(t, err)
	return it
}

func TestPreviewDelivery(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	viewer := env.Role(authz.RoleViewer)

	t.Run("existing type renders secrets as placeholders", func(t *testing.T) {
		res, err := env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{
			TypeID:      web.ID,
			PayloadJSON: `{"cookies":"sessionid=abc; tt=1","user_agent":"UA/1","signature":"tok","api_key":"keys/api"}`,
		})
		require.NoError(t, err)
		require.Empty(t, res.Errors)
		require.NotNil(t, res.Credential)
		require.Equal(t, "sessionid=abc; tt=1", res.Credential.CookieHeader)
		require.Equal(t, "<secret:keys/api>", res.Credential.Headers["X-Api-Key"])
		require.Equal(t, "UA/1", res.Credential.Headers["User-Agent"])
		require.Equal(t, "tok", res.Credential.Values["signature"])
		require.Equal(t, map[string]string{"sessionid": "abc", "tt": "1"}, res.Normalized["cookies"])
	})

	t.Run("draft spec", func(t *testing.T) {
		res, err := env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{
			SpecYAML:    identitysvctest.AppDeviceYAML,
			PayloadJSON: `{"device_id":"d1","install_id":"i1","extra":{"n":12345678901234567890}}`,
		})
		require.NoError(t, err)
		require.Empty(t, res.Errors)
		require.Equal(t, map[string]string{"device_id": "d1", "iid": "i1"}, res.Credential.Query)
		require.Equal(t, map[string]any{"n": 12345678901234567890.0}, res.Credential.JSON)
	})

	tests := []struct {
		name    string
		in      identitysvc.PreviewInput
		wantErr string
	}{
		{"invalid payload json", identitysvc.PreviewInput{TypeID: web.ID, PayloadJSON: `{`}, "not valid JSON"},
		{"payload not an object", identitysvc.PreviewInput{TypeID: web.ID, PayloadJSON: `[1]`}, "JSON object"},
		{"trailing data", identitysvc.PreviewInput{TypeID: web.ID, PayloadJSON: `{} {}`}, "unexpected data"},
		{"normalize failure", identitysvc.PreviewInput{TypeID: web.ID, PayloadJSON: `{"user_agent":"x"}`}, "required field is missing"},
		{"draft spec error", identitysvc.PreviewInput{Site: "shop", SpecYAML: "name: x\nclient: web\nfields: {}\n", PayloadJSON: `{}`}, "at least one field"},
		{"draft client error", identitysvc.PreviewInput{Site: "market", SpecYAML: identitysvctest.AppDeviceYAML, PayloadJSON: `{}`}, "is not a client"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := env.Service.PreviewDelivery(ctx, viewer, env.NS, tc.in)
			require.NoError(t, err)
			require.Nil(t, res.Credential)
			require.Len(t, res.Errors, 1)
			require.Contains(t, res.Errors[0], tc.wantErr)
		})
	}

	t.Run("request errors", func(t *testing.T) {
		_, err := env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{PayloadJSON: `{}`})
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{
			SpecYAML: strings.Replace(identitysvctest.AppDeviceYAML, "site: shop\n", "", 1), PayloadJSON: `{}`,
		})
		requireReason(t, err, apperr.ReasonSiteUnknown)
		_, err = env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{TypeID: "ity_missing", PayloadJSON: `{}`})
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = env.Service.PreviewDelivery(ctx, env.NoRole(), env.NS, identitysvc.PreviewInput{TypeID: web.ID, PayloadJSON: `{}`})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.PreviewDelivery(ctx, env.SiteRole(authz.RoleViewer, env.SiteB), env.NS, identitysvc.PreviewInput{
			SpecYAML: identitysvctest.AppDeviceYAML, PayloadJSON: `{}`,
		})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{
			TypeID: web.ID, PayloadJSON: strings.Repeat(" ", identitysvc.MaxPreviewPayloadBytes+1),
		})
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("type missing from the snapshot is compiled from the database", func(t *testing.T) {
		delete(env.SiteA.IdentityTypesByID, web.ID)
		delete(env.SiteA.IdentityTypes, web.Name)
		res, err := env.Service.PreviewDelivery(ctx, viewer, env.NS, identitysvc.PreviewInput{
			TypeID: web.ID, PayloadJSON: `{"cookies":{"sessionid":"x"}}`,
		})
		require.NoError(t, err)
		require.Empty(t, res.Errors)
		require.Equal(t, "sessionid=x", res.Credential.CookieHeader)
	})
}
