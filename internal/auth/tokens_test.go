package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/events"
)

func TestCreateToken(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	expires := time.Now().Add(24 * time.Hour)

	view, plaintext, err := e.tokens.Create(e.ctx(), w.ownerP(), w.nsRef(), CreateTokenInput{
		Name: "crawler hk", Description: "HK nodes", Scopes: []string{"lease:acquire:shop", "report:write", "secret:read:prod/signing/*"},
		IPAllowlist: []string{"10.0.0.1", "192.168.0.0/16"}, RateLimitRPS: 50, ExpiresAt: &expires,
	})
	require.NoError(t, err)
	require.True(t, WellFormedToken(plaintext))
	require.Equal(t, plaintext[:12], view.TokenPrefix)
	require.Equal(t, "prod", view.Namespace)
	require.Equal(t, []string{"10.0.0.1/32", "192.168.0.0/16"}, view.IPAllowlist)
	require.EqualValues(t, 50, view.RateLimitRPS)
	require.Equal(t, "user:"+w.owner, view.CreatedBy)
	entry, ok := e.rec.Last(ActionTokenCreate)
	require.True(t, ok)
	require.NotContains(t, strings.Join(func() []string {
		var s []string
		for _, v := range entry.Details {
			if str, ok := v.(string); ok {
				s = append(s, str)
			}
		}
		return s
	}(), " "), plaintext)

	var stored []byte
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT token_hash FROM api_tokens WHERE id = $1`, view.ID).Scan(&stored))
	require.Equal(t, HashToken(plaintext), stored)

	past := time.Now().Add(-time.Minute)
	tests := []struct {
		name   string
		p      *authz.Principal
		in     CreateTokenInput
		reason apperr.Reason
	}{
		{"viewer denied", w.viewerP(), CreateTokenInput{Name: "x", Scopes: []string{"admin"}}, apperr.ReasonPermissionDenied},
		{"duplicate name", w.ownerP(), CreateTokenInput{Name: "crawler hk", Scopes: []string{"admin"}}, apperr.ReasonAlreadyExists},
		{"no scopes", w.ownerP(), CreateTokenInput{Name: "x"}, apperr.ReasonInvalidArgument},
		{"too many scopes", w.ownerP(), CreateTokenInput{Name: "x", Scopes: make([]string, maxTokenScopes+1)}, apperr.ReasonInvalidArgument},
		{"unknown scope", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"root"}}, apperr.ReasonInvalidArgument},
		{"invalid site argument", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"lease:acquire:Shop"}}, apperr.ReasonInvalidArgument},
		{"invalid allowlist", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"admin"}, IPAllowlist: []string{"999.1.1.1"}}, apperr.ReasonInvalidArgument},
		{"too many allowlist entries", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"admin"}, IPAllowlist: make([]string, maxTokenAllowlist+1)}, apperr.ReasonInvalidArgument},
		{"negative rate", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"admin"}, RateLimitRPS: -1}, apperr.ReasonInvalidArgument},
		{"expired", w.ownerP(), CreateTokenInput{Name: "x", Scopes: []string{"admin"}, ExpiresAt: &past}, apperr.ReasonInvalidArgument},
		{"empty name", w.ownerP(), CreateTokenInput{Name: " ", Scopes: []string{"admin"}}, apperr.ReasonInvalidArgument},
		{"bad description", w.ownerP(), CreateTokenInput{Name: "x", Description: "\x00", Scopes: []string{"admin"}}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.tokens.Create(e.ctx(), tc.p, w.nsRef(), tc.in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("token with admin scope creates tokens in its namespace", func(t *testing.T) {
		_, parentID := e.newToken(w, "parent-admin", CreateTokenInput{Scopes: []string{"admin"}})
		tp := authtest.TokenPrincipal(parentID, w.tenant, w.ns, "prod", "admin")
		view, _, err := e.tokens.Create(e.ctx(), tp, w.nsRef(), CreateTokenInput{Name: "child", Scopes: []string{"config:read:app*"}})
		require.NoError(t, err)
		require.Equal(t, "token:"+parentID, view.CreatedBy)
		limited := authtest.TokenPrincipal("tok_ro", w.tenant, w.ns, "prod", "config:read")
		_, _, err = e.tokens.Create(e.ctx(), limited, w.nsRef(), CreateTokenInput{Name: "child2", Scopes: []string{"admin"}})
		requireReason(t, err, apperr.ReasonScopeMissing)
	})
}

func TestRevokeToken(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	_, id := e.newToken(w, "crawler", CreateTokenInput{})
	_, otherID := e.newToken(other, "crawler", CreateTokenInput{})

	var received []events.Event
	unsub := e.bus.Subscribe(events.ChannelTokens, func(_ context.Context, _ string, ev events.Event) { received = append(received, ev) })
	defer unsub()

	_, err := e.tokens.Revoke(e.ctx(), w.viewerP(), id)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = e.tokens.Revoke(e.ctx(), w.ownerP(), otherID)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.tokens.Revoke(e.ctx(), w.ownerP(), "tok_missing")
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.tokens.Revoke(e.ctx(), nil, id)
	requireReason(t, err, apperr.ReasonSessionInvalid)

	view, err := e.tokens.Revoke(e.ctx(), w.ownerP(), id)
	require.NoError(t, err)
	require.NotNil(t, view.RevokedAt)
	require.Len(t, received, 1)
	require.Equal(t, EventTokenRevoked, received[0].Type)
	require.JSONEq(t, `{"token_id":"`+id+`"}`, string(received[0].Data))
	require.Len(t, e.rec.Entries(), 1)

	again, err := e.tokens.Revoke(e.ctx(), w.ownerP(), id)
	require.NoError(t, err)
	require.Equal(t, view.RevokedAt.UnixMicro(), again.RevokedAt.UnixMicro(), "revocation time is kept")
	require.Len(t, e.rec.Entries(), 1, "no second audit entry")

	// Platform admins without an active tenant can revoke any token.
	_, err = e.tokens.Revoke(e.ctx(), authtest.AdminPrincipal(w.platformAdmin, ""), otherID)
	require.NoError(t, err)
}

func TestListTokens(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	staging := authtest.Namespace(t, e.pool, w.tenant, "staging")
	stagingRef := NamespaceRef{ID: staging, TenantID: w.tenant, Name: "staging"}
	for _, name := range []string{"a", "b", "c"} {
		e.newToken(w, name, CreateTokenInput{})
	}
	prep, err := prepareToken(CreateTokenInput{Name: "s", Scopes: []string{"admin"}}, time.Now())
	require.NoError(t, err)
	_, _, err = insertToken(e.ctx(), e.tokens.q, stagingRef, CreateTokenInput{Name: "s"}, prep, "test")
	require.NoError(t, err)
	_, revokedID := e.newToken(w, "revoked", CreateTokenInput{})
	_, err = e.tokens.Revoke(e.ctx(), w.ownerP(), revokedID)
	require.NoError(t, err)

	ref := w.nsRef()
	var got []TokenView
	token := ""
	for {
		page, next, total, err := e.tokens.List(e.ctx(), w.ownerP(), ListTokensInput{Namespace: &ref, PageSize: 2, PageToken: token})
		require.NoError(t, err)
		require.EqualValues(t, 3, total)
		got = append(got, page...)
		if next == "" {
			break
		}
		token = next
	}
	require.Len(t, got, 3)
	require.Equal(t, "c", got[0].Name, "newest first")

	_, _, total, err := e.tokens.List(e.ctx(), w.ownerP(), ListTokensInput{Namespace: &ref, IncludeRevoked: true})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)

	_, _, total, err = e.tokens.List(e.ctx(), w.ownerP(), ListTokensInput{})
	require.NoError(t, err)
	require.EqualValues(t, 4, total, "every readable namespace of the tenant")

	// An admin narrowed to the prod namespace only sees prod tokens.
	nsAdmin := authtest.UserPrincipal("usr_nsadmin", w.tenant, authz.Binding{TenantID: w.tenant, Role: authz.RoleAdmin, NamespaceID: w.ns})
	_, _, total, err = e.tokens.List(e.ctx(), nsAdmin, ListTokensInput{})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	_, _, _, err = e.tokens.List(e.ctx(), nsAdmin, ListTokensInput{Namespace: &stagingRef})
	requireReason(t, err, apperr.ReasonPermissionDenied)

	_, _, _, err = e.tokens.List(e.ctx(), w.viewerP(), ListTokensInput{})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, _, _, err = e.tokens.List(e.ctx(), w.ownerP(), ListTokensInput{PageToken: "!"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, _, err = e.tokens.List(e.ctx(), nil, ListTokensInput{})
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, _, _, err = e.tokens.List(e.ctx(), authtest.UserPrincipal(w.owner, ""), ListTokensInput{})
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestListTokensEmptyTenant(t *testing.T) {
	e := newEnv(t)
	tenant := authtest.Tenant(t, e.pool, "empty")
	owner := authtest.UserPrincipal("usr_owner", tenant, authz.Binding{TenantID: tenant, Role: authz.RoleOwner})
	page, next, total, err := e.tokens.List(e.ctx(), owner, ListTokensInput{})
	require.NoError(t, err)
	require.Empty(t, page)
	require.Empty(t, next)
	require.Zero(t, total)

	viewer := authtest.UserPrincipal("usr_viewer", tenant, authz.Binding{TenantID: tenant, Role: authz.RoleViewer})
	_, _, _, err = e.tokens.List(e.ctx(), viewer, ListTokensInput{})
	requireReason(t, err, apperr.ReasonPermissionDenied)
}

func TestCreateTokenDirect(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")

	plaintext, id, err := CreateTokenDirect(e.ctx(), e.pool, w.tenant, w.ns, " bootstrap ", []string{"lease:acquire", "report:write"}, nil)
	require.NoError(t, err)
	p, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, "", nil))
	require.NoError(t, err)
	require.Equal(t, id, p.ID)
	require.Equal(t, "bootstrap", p.Name)

	_, _, err = CreateTokenDirect(e.ctx(), e.pool, w.tenant, other.ns, "x", []string{"admin"}, nil)
	requireReason(t, err, apperr.ReasonNotFound)
	_, _, err = CreateTokenDirect(e.ctx(), e.pool, w.tenant, w.ns, "x", []string{"nope"}, nil)
	requireReason(t, err, apperr.ReasonInvalidArgument)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = CreateTokenDirect(ctx, e.pool, w.tenant, w.ns, "x", []string{"admin"}, nil)
	require.Error(t, err)
}

// TestTokenNameIsReusableAfterRevocation covers token rotation: there is no
// RPC that deletes a token, so a revoked token must release its name.
func TestTokenNameIsReusableAfterRevocation(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")

	first, _, err := e.tokens.Create(e.ctx(), w.ownerP(), w.nsRef(),
		CreateTokenInput{Name: "crawler", Scopes: []string{"lease:acquire"}})
	require.NoError(t, err)

	_, _, err = e.tokens.Create(e.ctx(), w.ownerP(), w.nsRef(),
		CreateTokenInput{Name: "crawler", Scopes: []string{"lease:acquire"}})
	requireReason(t, err, apperr.ReasonAlreadyExists)

	_, err = e.tokens.Revoke(e.ctx(), w.ownerP(), first.ID)
	require.NoError(t, err)

	second, _, err := e.tokens.Create(e.ctx(), w.ownerP(), w.nsRef(),
		CreateTokenInput{Name: "crawler", Scopes: []string{"lease:acquire"}})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)

	// The revoked row is still listed under its own name (audit trail).
	ns := w.nsRef()
	items, _, total, err := e.tokens.List(e.ctx(), w.ownerP(), ListTokensInput{Namespace: &ns, IncludeRevoked: true})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	names := make([]string, 0, len(items))
	for _, v := range items {
		names = append(names, v.Name)
	}
	require.Equal(t, []string{"crawler", "crawler"}, names)
}
