package identitysvc_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// addSecret inserts the metadata row of a secret of the default namespace.
func addSecret(t *testing.T, env *identitysvctest.Env, path string) {
	t.Helper()
	env.Exec(t, `INSERT INTO secrets (id, namespace_id, path) VALUES ($1, $2, $3)`, idgen.New(idgen.Secret), env.NS.ID, path)
}

func importAs(t *testing.T, env *identitysvctest.Env, p *authz.Principal, data string) identitysvc.ImportResult {
	t.Helper()
	res, err := env.Service.ImportIdentities(context.Background(), p, env.NS,
		identitysvc.ImportInput{Site: "shop", Type: "web_cookie", Format: "jsonl", Data: data})
	require.NoError(t, err)
	return res
}

func failureMessages(res identitysvc.ImportResult) []string {
	out := make([]string, len(res.Failed))
	for i, f := range res.Failed {
		out[i] = f.Message
	}
	return out
}

// A token that may write identities and acquire leases but not read a secret
// cannot make leases deliver that secret by referencing it from a payload.
func TestSecretRefsRequireSecretReadAccess(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	addSecret(t, env, "prod/db_password")
	addSecret(t, env, "public/api")
	node := env.Token(t, "identity:write:shop", "lease:acquire:shop", "secret:read:default/public/*")

	res := importAs(t, env, node, `{"cookies":"sessionid=steal","api_key":"prod/db_password"}
{"cookies":"sessionid=ok","api_key":"public/api"}
{"cookies":"sessionid=missing","api_key":"public/none"}`)
	require.Equal(t, 1, res.Created)
	require.Len(t, res.Failed, 2)
	require.Equal(t, 1, res.Failed[0].Line)
	require.Contains(t, res.Failed[0].Message, `secret "prod/db_password", which requires secret:read`)
	require.Equal(t, 3, res.Failed[1].Line)
	require.Contains(t, res.Failed[1].Message, `secret "public/none", which does not exist`)
	require.Equal(t, 1, env.QueryInt(t, `SELECT count(*) FROM identities`))

	// Dry runs report the same rejections.
	dry, err := env.Service.ImportIdentities(ctx, node, env.NS, identitysvc.ImportInput{Site: "shop", Type: "web_cookie",
		Format: "jsonl", DryRun: true, Data: `{"cookies":"sessionid=steal2","api_key":"prod/db_password"}`})
	require.NoError(t, err)
	require.Zero(t, dry.Created)
	require.Len(t, dry.Failed, 1)

	// Users need secret:reveal.
	res = importAs(t, env, env.Role(authz.RoleOperator), `{"cookies":"sessionid=op","api_key":"public/api"}`)
	require.Zero(t, res.Created)
	require.Contains(t, failureMessages(res)[0], "requires secret:reveal")
	res = importAs(t, env, env.Role(authz.RoleOperator, authz.PermSecretReveal), `{"cookies":"sessionid=op","api_key":"prod/db_password","_labels":{"session":"op"}}`)
	require.Equal(t, 1, res.Created, "failures: %v", res.Failed)
	opID := identityBySession(t, env, "op")

	// Updating the payload of that identity with a new reference is denied...
	_, err = env.Service.UpdateIdentityPayload(ctx, node, opID, map[string]any{
		"cookies": map[string]any{"sessionid": "op"}, "api_key": "prod/other",
	})
	require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err), "err: %v", err)
	// ...while keeping the stored reference is allowed (it was authorized when written).
	_, err = env.Service.UpdateIdentityPayload(ctx, node, opID, map[string]any{
		"cookies": map[string]any{"sessionid": "op", "fresh": "1"}, "api_key": "prod/db_password",
	})
	require.NoError(t, err)
	res = importAs(t, env, node, `{"cookies":"sessionid=op; fresh=2","api_key":"prod/db_password"}`)
	require.Equal(t, 1, res.Updated, "failures: %v", res.Failed)
	res = importAs(t, env, node, `{"cookies":"sessionid=op; fresh=3","api_key":"public/api"}
{"cookies":"sessionid=ok; fresh=1","api_key":"prod/db_password"}`)
	require.Equal(t, 1, res.Updated, "failures: %v", res.Failed)
	require.Len(t, res.Failed, 1)
	require.Equal(t, 2, res.Failed[0].Line)

	// Only namespace-relative paths are accepted.
	_, err = env.Service.UpdateIdentityPayload(ctx, env.Owner(env.NS), opID, map[string]any{
		"cookies": map[string]any{"sessionid": "op"}, "api_key": "public/../prod/db_password",
	})
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
}

// signedCookieYAML has a secret_ref field that is not delivered to nodes
// (vault_note) next to a delivered one (api_key).
const signedCookieYAML = `name: signed_cookie
site: shop
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  vault_note: { type: secret_ref }
  api_key:    { type: secret_ref }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  headers:
    X-Api-Key: "{{ api_key }}"
`

// A stored reference only authorizes keeping it in the same field: moving a
// secret the caller may not read from a field that is not delivered into a
// delivered one would hand its plaintext to leases.
func TestSecretRefsKeptOnlyInTheSameField(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, signedCookieYAML)
	addSecret(t, env, "prod/db_password")
	addSecret(t, env, "public/api")
	node := env.Token(t, "identity:write:shop", "lease:acquire:shop", "secret:read:default/public/*")
	importType := func(p *authz.Principal, data string) identitysvc.ImportResult {
		t.Helper()
		res, err := env.Service.ImportIdentities(ctx, p, env.NS,
			identitysvc.ImportInput{Site: "shop", Type: "signed_cookie", Format: "jsonl", Data: data})
		require.NoError(t, err)
		return res
	}

	admin := env.Role(authz.RoleOperator, authz.PermSecretReveal)
	res := importType(admin, `{"cookies":"sessionid=s1","vault_note":"prod/db_password","api_key":"public/api","_labels":{"session":"s1"}}`)
	require.Equal(t, 1, res.Created, "failures: %v", res.Failed)
	id := identityBySession(t, env, "s1")

	// Swapping the references moves the unreadable secret into the delivered field.
	_, err := env.Service.UpdateIdentityPayload(ctx, node, id, map[string]any{
		"cookies": map[string]any{"sessionid": "s1"}, "vault_note": "public/api", "api_key": "prod/db_password",
	})
	require.Equal(t, apperr.ReasonScopeMissing, apperr.ReasonOf(err), "err: %v", err)
	res = importType(node, `{"cookies":"sessionid=s1","vault_note":"public/api","api_key":"prod/db_password"}`)
	require.Zero(t, res.Updated)
	require.Len(t, res.Failed, 1)
	require.Contains(t, res.Failed[0].Message, `secret "prod/db_password", which requires secret:read`)

	// Keeping every reference in its field is allowed.
	_, err = env.Service.UpdateIdentityPayload(ctx, node, id, map[string]any{
		"cookies": map[string]any{"sessionid": "s1", "fresh": "1"}, "vault_note": "prod/db_password", "api_key": "public/api",
	})
	require.NoError(t, err)
	res = importType(node, `{"cookies":"sessionid=s1; fresh=2","vault_note":"prod/db_password","api_key":"public/api"}`)
	require.Equal(t, 1, res.Updated, "failures: %v", res.Failed)
}
