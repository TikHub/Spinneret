package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/events"
)

// setPasswordBehindTheBack changes a password the way ChangePassword and
// ResetPassword do, without revoking sessions or invalidating caches.
func (e *env) setPasswordBehindTheBack(ctx context.Context, userID, password string) {
	e.t.Helper()
	_, err := e.pool.Exec(ctx, `UPDATE users SET password_hash = $1, password_changed_at = now(), updated_at = now() WHERE id = $2`,
		e.hash(password), userID)
	require.NoError(e.t, err)
}

// sessionCount returns the number of live sessions indexed for a user.
func (e *env) sessionCount(ctx context.Context, userID string) int {
	e.t.Helper()
	members, err := e.rdb.Do(ctx, e.rdb.B().Smembers().Key(e.users.sessions.userIndexKey(userID)).Build()).AsStrSlice()
	require.NoError(e.t, err)
	live := 0
	for _, m := range members {
		n, err := e.rdb.Do(ctx, e.rdb.B().Exists().Key(e.keys.Session(m)).Build()).AsInt64()
		require.NoError(e.t, err)
		live += int(n)
	}
	return live
}

// TestSessionBoundToPasswordGeneration checks that a session created before a
// password change is rejected afterwards even when it escaped revocation (a
// login that verified the old password and stored its session after the
// revocation listed the user's sessions).
func TestSessionBoundToPasswordGeneration(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	ctx := e.ctx()
	cookie := e.login("acme-owner")
	_, err := e.auth.Authenticate(ctx, request(http.MethodGet, "", cookie, nil))
	require.NoError(t, err)

	e.setPasswordBehindTheBack(ctx, w.owner, "a-brand-new-password")
	e.auth.InvalidateUser(w.owner) // what the user.changed event does on every instance

	_, err = e.auth.Authenticate(ctx, request(http.MethodGet, "", cookie, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)

	// A session created with the new password works.
	fresh, _, err := e.users.Login(ctx, "acme-owner", "a-brand-new-password", "203.0.113.7", "ua")
	require.NoError(t, err)
	_, err = e.auth.Authenticate(ctx, request(http.MethodGet, "", fresh, nil))
	require.NoError(t, err)
}

// TestSessionNewerThanCachedUser checks that a session created after a
// password change is accepted by an instance whose cached user record still
// has the previous password generation (for example a lost user.changed
// event).
func TestSessionNewerThanCachedUser(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	ctx := e.ctx()
	old := e.login("acme-owner")
	_, err := e.auth.Authenticate(ctx, request(http.MethodGet, "", old, nil)) // caches the user record
	require.NoError(t, err)

	e.setPasswordBehindTheBack(ctx, w.owner, "a-brand-new-password")
	fresh, _, err := e.users.Login(ctx, "acme-owner", "a-brand-new-password", "203.0.113.7", "ua")
	require.NoError(t, err)
	_, err = e.auth.Authenticate(ctx, request(http.MethodGet, "", fresh, nil))
	require.NoError(t, err, "a stale cached user must be reloaded, not reject the newer session")
	_, err = e.auth.Authenticate(ctx, request(http.MethodGet, "", old, nil))
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

// TestLoginRacingPasswordReset reproduces a login that read the user before a
// password reset committed and revoked the sessions, and stored its session
// afterwards: the login must fail and leave no session behind.
func TestLoginRacingPasswordReset(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	ctx := e.ctx()
	var once sync.Once
	// Users.now runs after the password was verified and before the session
	// is stored, which is exactly the window of the race.
	e.users.now = func() time.Time {
		once.Do(func() {
			e.setPasswordBehindTheBack(ctx, w.owner, "a-brand-new-password")
			_, err := e.users.sessions.revokeUser(ctx, w.owner, "")
			require.NoError(t, err)
		})
		return time.Now()
	}

	cookie, user, err := e.users.Login(ctx, "acme-owner", testPassword, "203.0.113.7", "ua")
	requireReason(t, err, apperr.ReasonSessionInvalid)
	require.Empty(t, cookie)
	require.Nil(t, user)
	require.Zero(t, e.sessionCount(ctx, w.owner), "the session stored by the racing login must be deleted")
	entry, ok := e.rec.Last(ActionLogin)
	require.True(t, ok)
	require.Equal(t, audit.ResultDenied, entry.Result)
	require.Equal(t, "credentials_changed", entry.Details["reason"])
}

// TestLoginRacingDisable is the disable counterpart of TestLoginRacingPasswordReset.
func TestLoginRacingDisable(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	ctx := e.ctx()
	var once sync.Once
	e.users.now = func() time.Time {
		once.Do(func() {
			disabled := true
			_, err := e.users.UpdateUser(ctx, w.ownerP(), w.viewer, UpdateUserInput{Disabled: &disabled})
			require.NoError(t, err)
		})
		return time.Now()
	}
	_, _, err := e.users.Login(ctx, "acme-viewer", testPassword, "203.0.113.7", "ua")
	requireReason(t, err, apperr.ReasonSessionInvalid)
	require.Zero(t, e.sessionCount(ctx, w.viewer))
}

// TestPasswordChangesPublishUserChanged checks that password changes and
// resets drop the cached user records of every instance at once.
func TestPasswordChangesPublishUserChanged(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	ctx := e.ctx()
	var mu sync.Mutex
	var changed []string
	unsubscribe := e.bus.Subscribe(events.ChannelTokens, func(_ context.Context, _ string, ev events.Event) {
		if ev.Type != EventUserChanged {
			return
		}
		var data tokensEventData
		require.NoError(t, json.Unmarshal(ev.Data, &data))
		mu.Lock()
		defer mu.Unlock()
		changed = append(changed, data.UserID)
	})
	defer unsubscribe()
	seen := func(userID string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range changed {
			if id == userID {
				return true
			}
		}
		return false
	}

	current := e.login("acme-owner")
	require.NoError(t, e.users.ChangePassword(ctx, w.ownerP(), current, testPassword, "a-brand-new-password"))
	require.Eventually(t, func() bool { return seen(w.owner) }, 5*time.Second, 5*time.Millisecond)

	require.NoError(t, e.users.ResetPassword(ctx, authz.System("test"), w.viewer, "another-new-password"))
	require.Eventually(t, func() bool { return seen(w.viewer) }, 5*time.Second, 5*time.Millisecond)
}

// TestLoginRejectsMalformedUsernames checks that usernames no account can have
// (control characters, invalid UTF-8) are refused before the throttle and the
// database: they neither fail with an internal error nor use up the client's
// failure budget, and they never reach the audit log.
func TestLoginRejectsMalformedUsernames(t *testing.T) {
	e := newEnv(t)
	e.world("acme")
	ctx := e.ctx()
	ip := "198.51.100.90"
	for i := range LoginIPFailureLimit + 2 {
		for _, name := range []string{"acme-owner\x00", "evil\x00" + string(rune('a'+i)), "bad\xffname", "tab\tname"} {
			_, _, err := e.users.Login(ctx, name, testPassword, ip, "ua")
			requireReason(t, err, apperr.ReasonSessionInvalid)
		}
	}
	_, ok := e.rec.Last(ActionLogin)
	require.False(t, ok, "malformed usernames are not audited")
	_, _, err := e.users.Login(ctx, "acme-owner", testPassword, ip, "ua")
	require.NoError(t, err, "malformed attempts must not throttle the client")
}

// TestLoginTrimsSurroundingWhitespace checks that the malformed-username check
// does not reject names that only differ by the surrounding white space
// (including tabs and line breaks) that NormalizeUsername removes.
func TestLoginTrimsSurroundingWhitespace(t *testing.T) {
	e := newEnv(t)
	e.world("acme")
	ctx := e.ctx()
	for _, name := range []string{" ACME-Owner ", "acme-owner\n", "\tacme-owner\r\n", "acme-owner\u0085"} {
		cookie, user, err := e.users.Login(ctx, name, testPassword, "203.0.113.7", "ua")
		require.NoError(t, err, "%q", name)
		require.NotEmpty(t, cookie)
		require.Equal(t, "acme-owner", user.Username)
	}
	_, _, err := e.users.Login(ctx, "acme\n-owner", testPassword, "203.0.113.7", "ua")
	requireReason(t, err, apperr.ReasonSessionInvalid)
}
