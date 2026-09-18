package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
	"github.com/TikHub/Spinneret/internal/authz"
)

func TestLogin(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")

	cookie, user, err := e.users.Login(e.ctx(), "  ACME-Owner ", testPassword, "203.0.113.7", "browser/1")
	require.NoError(t, err)
	require.Len(t, cookie, sessionIDLen)
	require.Equal(t, w.owner, user.ID)
	require.NotNil(t, user.LastLoginAt)

	var ip string
	var at *time.Time
	require.NoError(t, e.pool.QueryRow(e.ctx(), `SELECT last_login_at, last_login_ip FROM users WHERE id = $1`, w.owner).Scan(&at, &ip))
	require.NotNil(t, at)
	require.Equal(t, "203.0.113.7", ip)

	entry, ok := e.rec.Last(ActionLogin)
	require.True(t, ok)
	require.Equal(t, audit.ResultOK, entry.Result)
	require.Equal(t, w.owner, entry.ActorID)

	p, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)
	require.Equal(t, w.owner, p.ID)
}

func TestLoginFailures(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	authtest.DisableUser(t, e.pool, w.viewer)

	tests := []struct {
		name, username, password, reason string
	}{
		{"wrong password", "acme-owner", "wrong-password-1", "invalid_credentials"},
		{"unknown user", "nobody-here", testPassword, "invalid_credentials"},
		{"disabled user", "acme-viewer", testPassword, "disabled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.users.Login(e.ctx(), tc.username, tc.password, "198.51.100.1", "ua")
			requireReason(t, err, apperr.ReasonSessionInvalid)
			require.Contains(t, err.Error(), "invalid username or password")
			entry, ok := e.rec.Last(ActionLogin)
			require.True(t, ok)
			require.Equal(t, audit.ResultDenied, entry.Result)
			require.Equal(t, tc.reason, entry.Details["reason"])
		})
	}
	for _, bad := range []struct{ u, p string }{{"", "x"}, {"acme-owner", ""}, {string(make([]byte, 65)), "x"}} {
		_, _, err := e.users.Login(e.ctx(), bad.u, bad.p, "", "")
		requireReason(t, err, apperr.ReasonSessionInvalid)
	}

	_, err := e.pool.Exec(e.ctx(), `UPDATE users SET password_hash = 'garbage' WHERE id = $1`, w.owner)
	require.NoError(t, err)
	_, _, err = e.users.Login(e.ctx(), "acme-owner", testPassword, "", "")
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestLoginThrottle(t *testing.T) {
	e := newEnv(t)
	e.world("acme")

	for i := range LoginUserFailureLimit {
		_, _, err := e.users.Login(e.ctx(), "acme-owner", "wrong-password", "198.51.100.1", "")
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_ = i
	}
	_, _, err := e.users.Login(e.ctx(), "acme-owner", testPassword, "198.51.100.2", "")
	requireReason(t, err, apperr.ReasonLoginThrottled)
	ae, ok := apperr.As(err)
	require.True(t, ok)
	require.Greater(t, ae.RetryAfterMs, int64(0))
	entry, _ := e.rec.Last(ActionLogin)
	require.Equal(t, "throttled", entry.Details["reason"])

	// Another user is not affected by the username counter.
	_, _, err = e.users.Login(e.ctx(), "acme-viewer", testPassword, "198.51.100.3", "")
	require.NoError(t, err)

	// An administrative reset clears the username counter.
	require.NoError(t, e.users.throttle.reset(e.ctx(), "acme-owner"))
	_, _, err = e.users.Login(e.ctx(), "acme-owner", testPassword, "198.51.100.1", "")
	require.NoError(t, err)
}

func TestLoginThrottlePerIP(t *testing.T) {
	e := newEnv(t)
	e.world("acme")
	ip := "198.51.100.77"
	for i := range LoginIPFailureLimit {
		_, _, err := e.users.Login(e.ctx(), "someone-"+string(rune('a'+i)), "wrong-password", ip, "")
		requireReason(t, err, apperr.ReasonSessionInvalid)
	}
	_, _, err := e.users.Login(e.ctx(), "acme-owner", testPassword, ip, "")
	requireReason(t, err, apperr.ReasonLoginThrottled)
	_, _, err = e.users.Login(e.ctx(), "acme-owner", testPassword, "198.51.100.78", "")
	require.NoError(t, err)
}

func TestLoginThrottleConcurrent(t *testing.T) {
	e := newEnv(t)
	th := e.users.throttle
	var mu sync.Mutex
	reserved := 0
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if err := th.reserve(e.ctx(), "victim", ""); err == nil {
				mu.Lock()
				reserved++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Equal(t, LoginUserFailureLimit, reserved)

	// A success returns the IP slot and clears the username counter.
	require.NoError(t, th.reserve(e.ctx(), "fresh", "192.0.2.1"))
	require.NoError(t, th.succeeded(e.ctx(), "fresh", "192.0.2.1"))
	n, err := e.rdb.Do(e.ctx(), e.rdb.B().Exists().Key(e.keys.RateLimit(rateKindLoginIP, "192.0.2.1")).Key(e.keys.RateLimit(rateKindLoginUser, "fresh")).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, th.reserve(ctx, "x", "1.2.3.4"))
	require.Error(t, th.succeeded(ctx, "x", "1.2.3.4"))
	require.Error(t, th.reset(ctx, "x"))
}

func TestLogoutAndCookies(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	cookie := e.login("acme-owner")

	ctx := contextWithPrincipal(e.ctx(), w.ownerP())
	require.NoError(t, e.users.Logout(ctx, cookie))
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	entry, ok := e.rec.Last(ActionLogout)
	require.True(t, ok)
	require.Equal(t, w.owner, entry.ResourceID)

	require.NoError(t, e.users.Logout(ctx, "malformed"))
	require.NoError(t, e.users.Logout(ctx, cookie), "logging out twice is a no-op")

	c := e.users.SessionCookie("value", false)
	require.Equal(t, SessionCookieName, c.Name)
	require.True(t, c.HttpOnly)
	require.Equal(t, http.SameSiteStrictMode, c.SameSite)
	require.Equal(t, "/", c.Path)
	require.Equal(t, 3600, c.MaxAge)
	require.False(t, c.Secure)
	require.True(t, e.users.SessionCookie("value", true).Secure)
	cleared := e.users.ClearSessionCookie(true)
	require.Equal(t, -1, cleared.MaxAge)
	require.Empty(t, cleared.Value)

	header := http.Header{}
	header.Set("Cookie", SessionCookieName+"=abc; other=1")
	require.Equal(t, "abc", SessionCookieValue(header))
	require.Empty(t, SessionCookieValue(http.Header{}))

	meta := e.users.RequestMeta(e.ctx(), http.Header{"X-Forwarded-For": {"198.51.100.4"}}, "10.0.0.2:80")
	require.Equal(t, "198.51.100.4", meta.ClientIP)
	stored := e.users.RequestMeta(WithRequestMeta(e.ctx(), RequestMeta{ClientIP: "192.0.2.99"}), nil, "")
	require.Equal(t, "192.0.2.99", stored.ClientIP)
	require.Empty(t, e.users.RequestMeta(e.ctx(), nil, "").ClientIP)
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	current := e.login("acme-owner")
	other := e.login("acme-owner")
	p := w.ownerP()

	tests := []struct {
		name          string
		p             *authz.Principal
		cur, next     string
		wantCode      apperr.Reason
		wantAuditDeny bool
	}{
		{"token principal", &authz.Principal{Kind: authz.KindToken, ID: "tok_1"}, testPassword, "brand-new-password", apperr.ReasonPermissionDenied, false},
		{"nil principal", nil, testPassword, "brand-new-password", apperr.ReasonSessionInvalid, false},
		{"weak password", p, testPassword, "short", apperr.ReasonInvalidArgument, false},
		{"wrong current", p, "not-the-password", "brand-new-password", apperr.ReasonPermissionDenied, true},
		{"same password", p, testPassword, testPassword, apperr.ReasonInvalidArgument, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := e.users.ChangePassword(e.ctx(), tc.p, current, tc.cur, tc.next)
			requireReason(t, err, tc.wantCode)
			if tc.wantAuditDeny {
				entry, ok := e.rec.Last(ActionChangePassword)
				require.True(t, ok)
				require.Equal(t, audit.ResultDenied, entry.Result)
			}
		})
	}

	require.NoError(t, e.users.ChangePassword(e.ctx(), p, current, testPassword, "brand-new-password"))
	_, err := e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", current, nil))
	require.NoError(t, err, "the current session is kept")
	_, err = e.auth.Authenticate(e.ctx(), request(http.MethodGet, "", other, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, _, err = e.users.Login(e.ctx(), "acme-owner", "brand-new-password", "", "")
	require.NoError(t, err)

	missing := authtest.UserPrincipal("usr_missing", w.tenant)
	requireReason(t, e.users.ChangePassword(e.ctx(), missing, "", testPassword, "brand-new-password"), apperr.ReasonSessionInvalid)
	authtest.DisableUser(t, e.pool, w.viewer)
	requireReason(t, e.users.ChangePassword(e.ctx(), w.viewerP(), "", testPassword, "brand-new-password"), apperr.ReasonSessionInvalid)
}

func TestUsersHashingHonoursContext(t *testing.T) {
	e := newEnv(t)
	for range cap(e.users.hashSem) {
		e.users.hashSem <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := e.users.hash(ctx, "long-enough-password")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = e.users.verify(ctx, "x", "y")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func contextWithPrincipal(ctx context.Context, p *authz.Principal) context.Context {
	return authz.WithPrincipal(ctx, p)
}

func TestLoginThrottleRestoresMissingExpiry(t *testing.T) {
	e := newEnv(t)
	th := e.users.throttle
	key := e.keys.RateLimit(rateKindLoginUser, "stuck")
	// A counter at the limit that lost its expiry (INCR succeeded, PEXPIRE failed).
	require.NoError(t, e.rdb.Do(e.ctx(), e.rdb.B().Set().Key(key).Value(strconv.Itoa(LoginUserFailureLimit)).Build()).Error())

	err := th.reserve(e.ctx(), "stuck", "")
	requireReason(t, err, apperr.ReasonLoginThrottled)
	ae, _ := apperr.As(err)
	require.Equal(t, LoginThrottleWindow.Milliseconds(), ae.RetryAfterMs)
	pttl, err := e.rdb.Do(e.ctx(), e.rdb.B().Pttl().Key(key).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, pttl, int64(0), "the lockout expires again")
	require.LessOrEqual(t, pttl, LoginThrottleWindow.Milliseconds())

	// A counter with an expiry keeps it.
	require.NoError(t, e.rdb.Do(e.ctx(), e.rdb.B().Pexpire().Key(key).Milliseconds(60_000).Build()).Error())
	err = th.reserve(e.ctx(), "stuck", "")
	ae, _ = apperr.As(err)
	require.LessOrEqual(t, ae.RetryAfterMs, int64(60_000))
}
