package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
)

func (e *env) login(username string) string {
	e.t.Helper()
	cookie, _, err := e.users.Login(e.ctx(), username, testPassword, "203.0.113.7", "browser/1")
	require.NoError(e.t, err)
	return cookie
}

func TestSessionAuthentication(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	other := e.world("globex")
	cookie := e.login("acme-owner")
	csrf := map[string]string{HeaderCSRF: "1"}

	t.Run("unsafe method with CSRF header", func(t *testing.T) {
		p, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, "", cookie, csrf))
		require.NoError(t, err)
		require.Equal(t, authz.KindUser, p.Kind)
		require.Equal(t, w.owner, p.ID)
		require.Equal(t, "acme-owner", p.Name)
		require.Empty(t, p.TenantID)
		require.Len(t, p.Bindings, 1)
		require.Equal(t, "203.0.113.7", p.ClientIP)
	})
	t.Run("unsafe method without CSRF header", func(t *testing.T) {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			_, err := e.auth.Authenticate(e.ctx(), request(m, "", cookie, nil))
			requireReason(t, err, apperr.ReasonCSRFMissing)
		}
		_, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, "", cookie, map[string]string{HeaderCSRF: "yes"}))
		requireReason(t, err, apperr.ReasonCSRFMissing)
	})
	t.Run("safe methods need no CSRF header", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
			_, err := e.auth.Authenticate(e.ctx(), request(m, "", cookie, nil))
			require.NoError(t, err, m)
		}
	})
	t.Run("active tenant header", func(t *testing.T) {
		p, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, "", cookie, map[string]string{HeaderCSRF: "1", HeaderTenant: w.tenant}))
		require.NoError(t, err)
		require.Equal(t, w.tenant, p.TenantID)

		_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, "", cookie, map[string]string{HeaderCSRF: "1", HeaderTenant: other.tenant}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = e.auth.Authenticate(e.ctx(), request(http.MethodPost, "", cookie, map[string]string{HeaderCSRF: "1", HeaderTenant: string(make([]byte, 65))}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
	})
	t.Run("tenant query parameter for GET only", func(t *testing.T) {
		r := request(http.MethodGet, "", cookie, nil)
		r.URL.RawQuery = "tenant=" + w.tenant + "&namespace=prod"
		p, err := e.auth.Authenticate(e.ctx(), r)
		require.NoError(t, err)
		require.Equal(t, w.tenant, p.TenantID)

		r = request(http.MethodPost, "", cookie, csrf)
		r.URL.RawQuery = "tenant=" + w.tenant
		p, err = e.auth.Authenticate(e.ctx(), r)
		require.NoError(t, err)
		require.Empty(t, p.TenantID)
	})
	t.Run("bearer token takes precedence", func(t *testing.T) {
		plaintext, id := e.newToken(w, "node", CreateTokenInput{})
		p, err := e.auth.Authenticate(e.ctx(), request(http.MethodPost, plaintext, cookie, nil))
		require.NoError(t, err)
		require.Equal(t, id, p.ID)
	})
	t.Run("malformed and unknown cookies", func(t *testing.T) {
		for _, c := range []string{"short", "!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
			_, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", c, nil))
			requireReason(t, err, apperr.ReasonSessionInvalid)
		}
	})
}

func TestSessionPlatformAdminTenant(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	cookie := e.login("acme-root")
	p, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, map[string]string{HeaderTenant: w.tenant}))
	require.NoError(t, err)
	require.True(t, p.IsPlatformAdmin)
	require.Equal(t, w.tenant, p.TenantID)

	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, map[string]string{HeaderTenant: "ten_missing"}))
	requireReason(t, err, apperr.ReasonPermissionDenied)
}

func TestSessionDisabledAndDeletedUser(t *testing.T) {
	e := newEnv(t)
	e.runAuthenticator()
	e.world("acme")
	cookie := e.login("acme-viewer")
	p, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)

	authtest.DisableUser(t, e.pool, p.ID)
	e.users.publishUserChanged(e.ctx(), p.ID)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	_, err = e.pool.Exec(e.ctx(), `DELETE FROM users WHERE id = $1`, p.ID)
	require.NoError(t, err)
	e.auth.InvalidateUser(p.ID)
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestSessionSlidingRefresh(t *testing.T) {
	e := newEnv(t)
	e.world("acme")
	cookie := e.login("acme-owner")
	hash, ok := sessionHashOf(cookie)
	require.True(t, ok)
	key := e.keys.Session(hash)

	readExpiry := func() int64 {
		v, err := e.rdb.Do(e.ctx(), e.rdb.B().Hget().Key(key).Field(sessionFieldExpires).Build()).AsInt64()
		require.NoError(t, err)
		return v
	}
	initial := readExpiry()

	// More than half of the TTL remains: no refresh.
	e.auth.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)
	require.Equal(t, initial, readExpiry())

	// Less than half remains: the expiry slides forward.
	future := time.Now().Add(40 * time.Minute)
	e.auth.now = func() time.Time { return future }
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)
	require.Equal(t, future.Add(time.Hour).UnixMilli(), readExpiry())
	pttl, err := e.rdb.Do(e.ctx(), e.rdb.B().Pttl().Key(key).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, pttl, int64(50*time.Minute/time.Millisecond))

	// Past the recorded expiry the session is invalid even if the key survives.
	e.auth.now = func() time.Time { return future.Add(2 * time.Hour) }
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestSessionStoreRevokeAndPrune(t *testing.T) {
	e := newEnv(t)
	s := e.users.sessions
	now := time.Now()
	var cookies []string
	for range sessionIndexPruneThreshold + 2 {
		c, _, err := s.create(e.ctx(), "usr_x", "0", "192.0.2.1", "ua", now)
		require.NoError(t, err)
		cookies = append(cookies, c)
	}
	// Remove a few session hashes directly so the index has stale members.
	for _, c := range cookies[:3] {
		h, _ := sessionHashOf(c)
		require.NoError(t, e.rdb.Do(e.ctx(), e.rdb.B().Del().Key(e.keys.Session(h)).Build()).Error())
	}
	_, _, err := s.create(e.ctx(), "usr_x", "0", "192.0.2.1", "ua", now)
	require.NoError(t, err)
	n, err := e.rdb.Do(e.ctx(), e.rdb.B().Scard().Key(s.userIndexKey("usr_x")).Build()).AsInt64()
	require.NoError(t, err)
	require.EqualValues(t, len(cookies)-3+1, n)

	keep, _ := sessionHashOf(cookies[5])
	deleted, err := s.revokeUser(e.ctx(), "usr_x", keep)
	require.NoError(t, err)
	require.Equal(t, len(cookies)-3, deleted)
	_, err = s.get(e.ctx(), keep, now)
	require.NoError(t, err)
	gone, _ := sessionHashOf(cookies[6])
	_, err = s.get(e.ctx(), gone, now)
	require.ErrorIs(t, err, errSessionUnknown)

	deleted, err = s.revokeUser(e.ctx(), "usr_nobody", "")
	require.NoError(t, err)
	require.Zero(t, deleted)

	userID, err := s.delete(e.ctx(), keep)
	require.NoError(t, err)
	require.Equal(t, "usr_x", userID)
	userID, err = s.delete(e.ctx(), keep)
	require.NoError(t, err)
	require.Empty(t, userID)

	// A refresh racing with a logout does not resurrect the session.
	extended, err := s.refresh(e.ctx(), session{hash: keep, userID: "usr_x"}, now)
	require.NoError(t, err)
	require.False(t, extended)
	exists, err := e.rdb.Do(e.ctx(), e.rdb.B().Exists().Key(e.keys.Session(keep)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, exists)
}

func TestSessionStoreRedisErrors(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := e.users.sessions
	_, _, err := s.create(ctx, "usr_x", "0", "", "", time.Now())
	require.Error(t, err)
	_, err = s.get(ctx, "abc", time.Now())
	require.Error(t, err)
	require.NotErrorIs(t, err, errSessionUnknown)
	_, err = s.delete(ctx, "abc")
	require.Error(t, err)
	_, err = s.revokeUser(ctx, "usr_x", "")
	require.Error(t, err)
	_, err = s.refresh(ctx, session{hash: "abc", userID: "u"}, time.Now())
	require.Error(t, err)
	require.Error(t, s.pruneIndex(ctx, "idx"))

	// Authentication surfaces infrastructure failures as internal errors.
	cookie := e.login2(t)
	_, err = e.auth.Authenticate(ctx, request(http.MethodGet, "", cookie, nil))
	require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))
}

// login2 creates a user and a session without the world fixture.
func (e *env) login2(t *testing.T) string {
	t.Helper()
	authtest.User(t, e.pool, "solo-user", e.hash(testPassword), false)
	return e.login("solo-user")
}
