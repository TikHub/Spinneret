package identitysvc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
)

// webCookieUniqueBy returns the web_cookie spec deduplicated by uniqueBy.
func webCookieUniqueBy(uniqueBy string) string {
	return strings.Replace(identitysvctest.WebCookieYAML, "unique_by: [cookies.sessionid]", "unique_by: ["+uniqueBy+"]", 1)
}

func TestUpdateIdentityTypeRehash(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	owner := env.Owner(env.NS)
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	// A's new key (uid "b") equals B's old key (sessionid "b"): a naive
	// in-place rehash would report a false collision.
	res := importJSONL(t, env, "web_cookie", "{\"cookies\":\"sessionid=a; uid=b\"}\n{\"cookies\":\"sessionid=b; uid=c\"}")
	require.Equal(t, 2, res.Created)

	update := func(t *testing.T, spec string) (identitysvc.IdentityType, error) {
		t.Helper()
		it, err := env.Service.UpdateIdentityType(ctx, owner, web.ID, spec)
		if err == nil {
			env.RegisterType(t, env.SiteA, it)
		}
		return it, err
	}

	t.Run("unique_by change rehashes existing identities", func(t *testing.T) {
		it, err := update(t, webCookieUniqueBy("cookies.uid"))
		require.NoError(t, err)
		require.Equal(t, []string{"cookies.uid"}, it.Spec.UniqueBy)
		entry, ok := env.Audit.Last("identity_type.update")
		require.True(t, ok)
		require.EqualValues(t, 2, entry.Details["rehashed_identities"])

		res := importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=a2; uid=b"}`)
		require.Equal(t, 1, res.Updated, "the identity is found by its new unique key")
		require.Zero(t, res.Created)
		res = importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=b; uid=c"}`)
		require.Equal(t, 1, res.Unchanged)
	})

	t.Run("spec change without unique_by change does not rehash", func(t *testing.T) {
		_, err := update(t, strings.Replace(webCookieUniqueBy("cookies.uid"), "activation: probe", "activation: immediate", 1))
		require.NoError(t, err)
		entry, ok := env.Audit.Last("identity_type.update")
		require.True(t, ok)
		require.NotContains(t, entry.Details, "rehashed_identities")
	})

	t.Run("keys shared under the new unique_by fail", func(t *testing.T) {
		importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=a2; uid=z"}`)
		before := mustGetType(t, env, web.ID)
		_, err := update(t, webCookieUniqueBy("cookies.sessionid"))
		requireReason(t, err, apperr.ReasonFailedPrecondition)
		after := mustGetType(t, env, web.ID)
		require.Equal(t, before.Version, after.Version, "the failed update is rolled back")
		res := importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=b; uid=c"}`)
		require.Equal(t, 1, res.Unchanged, "hashes are rolled back too")
	})

	t.Run("identities without a value for the new unique_by fail", func(t *testing.T) {
		_, err := update(t, webCookieUniqueBy("cookies.missing"))
		requireReason(t, err, apperr.ReasonFailedPrecondition)
		require.Contains(t, err.Error(), "no unique key")
	})

	t.Run("stale catalog snapshots are reloaded or conflict", func(t *testing.T) {
		stale := env.SiteA.IdentityTypesByID[web.ID]
		// Register nothing: the catalog keeps the stale compiled type.
		updated, err := env.Service.UpdateIdentityType(ctx, owner, web.ID, strings.Replace(webCookieUniqueBy("cookies.uid"), "activation: probe", "activation: immediate", 1))
		require.NoError(t, err)
		require.Same(t, stale, env.SiteA.IdentityTypesByID[web.ID])
		requireReason(t, env.Service.CheckTypeVersion(ctx, stale), apperr.ReasonConflict)

		res := importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=new; uid=new","_labels":{"k":"new"}}`)
		require.Equal(t, 1, res.Created, "the import compiles the stored spec")
		ids := loadIdentities(t, env, web.ID)
		require.Equal(t, identitysvc.StateActive, ids[identityBySession(t, env, "new")].State)

		fresh := env.RegisterType(t, env.SiteA, updated)
		require.NoError(t, env.Service.CheckTypeVersion(ctx, fresh))
		env.Exec(t, `DELETE FROM identities WHERE type_id = $1`, web.ID)
		env.Exec(t, `DELETE FROM identity_types WHERE id = $1`, web.ID)
		requireReason(t, env.Service.CheckTypeVersion(ctx, fresh), apperr.ReasonNotFound)
	})

	t.Run("types without identities update without crypto", func(t *testing.T) {
		app := env.CreateType(t, env.SiteA, identitysvctest.AppDeviceYAML)
		bare := identitysvc.NewService(env.Pool, nil, nil, env.Catalog, env.Hot, env.Ops, env.Audit, env.Bus, nil)
		_, err := bare.UpdateIdentityType(ctx, owner, app.ID, strings.Replace(identitysvctest.AppDeviceYAML, "unique_by: [device_id]", "unique_by: [install_id]", 1))
		require.NoError(t, err)
	})
}
