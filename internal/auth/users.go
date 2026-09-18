package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Audit actions and resource kinds written by the auth services.
const (
	ActionLogin             = "auth.login"
	ActionLogout            = "auth.logout"
	ActionChangePassword    = "user.change_password"
	ActionUserCreate        = "user.create"
	ActionUserUpdate        = "user.update"
	ActionUserResetPassword = "user.reset_password"
	ActionBindingCreate     = "role_binding.create"
	ActionBindingDelete     = "role_binding.delete"
	ActionTokenCreate       = "token.create"
	ActionTokenRevoke       = "token.revoke"

	ResourceUser        = "user"
	ResourceRoleBinding = "role_binding"
	ResourceToken       = "token"
)

// UsersOption configures Users.
type UsersOption func(*Users)

// WithEventBus makes Users publish "user.changed" events on
// events.ChannelTokens after user and role binding changes, so every
// Authenticator drops its cached copy immediately instead of after the 5 s
// cache TTL.
func WithEventBus(bus events.Bus) UsersOption {
	return func(u *Users) { u.bus = bus }
}

// Users implements console sign-in, sessions, user administration and role
// bindings.
type Users struct {
	cfg      Config
	pool     *pgxpool.Pool
	q        *authdb.Queries
	sessions *sessionStore
	throttle *loginThrottle
	audit    audit.Recorder
	bus      events.Bus
	logger   *slog.Logger
	now      func() time.Time

	// hashSem bounds concurrent Argon2id computations (64 MiB each).
	hashSem   chan struct{}
	dummyOnce sync.Once
	dummyHash string
}

// NewUsers creates the user service.
func NewUsers(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, rec audit.Recorder, cfg Config, logger *slog.Logger, opts ...UsersOption) *Users {
	cfg = cfg.normalized()
	if rec == nil {
		rec = audit.Nop{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	u := &Users{
		cfg:      cfg,
		pool:     pool,
		q:        authdb.New(pool),
		sessions: &sessionStore{rdb: rdb, keys: keys, ttl: cfg.SessionTTL},
		throttle: &loginThrottle{rdb: rdb, keys: keys},
		audit:    rec,
		logger:   logger.With(slog.String("component", "auth")),
		now:      time.Now,
		hashSem:  make(chan struct{}, min(max(runtime.GOMAXPROCS(0)/2, 2), 4)),
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// invalidCredentials is the single error returned for every failed sign-in.
func invalidCredentials() error {
	return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "invalid username or password")
}

// Login verifies credentials and creates a session. It returns the session
// cookie value and the signed-in user. Failures are throttled per username
// and per client IP and always report "invalid username or password".
// Usernames that no account can have (control characters, invalid UTF-8) are
// refused before the throttle, the database and the audit log.
//
// The session is bound to the password generation read with the user, and
// the user is read again once the session is stored: a password change,
// reset or disable that committed while the password was being verified has
// revoked the user's sessions before this one existed, so the login deletes
// its session and fails.
func (u *Users) Login(ctx context.Context, username, password, ip, ua string) (string, *UserView, error) {
	// Validity is checked after trimming the surrounding white space (which
	// NormalizeUsername removes, line breaks and tabs included) but before
	// lower-casing, which replaces invalid UTF-8 with U+FFFD.
	plausible := plausibleUsername(strings.TrimSpace(username))
	username = NormalizeUsername(username)
	if !plausible || username == "" || len(username) > maxUsernameLength || password == "" ||
		utf8.RuneCountInString(password) > MaxPasswordLength {
		return "", nil, invalidCredentials()
	}
	attempt := audit.Entry{
		ActorKind: string(authz.KindUser), ActorName: username,
		Action: ActionLogin, ResourceKind: ResourceUser, ResourceName: username,
		IP: ip, UserAgent: truncateUTF8(ua, maxUserAgentLength),
	}
	if err := u.throttle.reserve(ctx, username, ip); err != nil {
		if apperr.ReasonOf(err) == apperr.ReasonLoginThrottled {
			u.recordDenied(ctx, attempt, "throttled")
			return "", nil, err
		}
		return "", nil, apperr.Internal(err)
	}
	user, err := u.q.AuthUserByUsername(ctx, username)
	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, apperr.Internal(err)
	}
	encoded := user.PasswordHash
	if !found {
		encoded = u.dummy()
	}
	ok, err := u.verify(ctx, password, encoded)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		u.logger.Error("stored password hash is malformed", slog.String("user_id", user.ID), slog.Any("error", err))
		ok = false
	}
	if found {
		attempt.ActorID, attempt.ResourceID = user.ID, user.ID
	}
	switch {
	case !found || !ok:
		u.recordDenied(ctx, attempt, "invalid_credentials")
		return "", nil, invalidCredentials()
	case user.Disabled:
		u.recordDenied(ctx, attempt, "disabled")
		return "", nil, invalidCredentials()
	}
	if err := u.throttle.succeeded(ctx, username, ip); err != nil {
		u.logger.Warn("login throttle release failed", slog.Any("error", err))
	}
	now := u.now()
	cookie, sessHash, err := u.sessions.create(ctx, user.ID, credentialOf(user.PasswordChangedAt), ip, ua, now)
	if err != nil {
		return "", nil, apperr.Internal(err)
	}
	if err := u.confirmSignIn(ctx, user, sessHash); err != nil {
		if apperr.ReasonOf(err) == apperr.ReasonSessionInvalid {
			u.recordDenied(ctx, attempt, "credentials_changed")
		}
		return "", nil, err
	}
	if err := u.q.AuthUserRecordLogin(ctx, authdb.AuthUserRecordLoginParams{ID: user.ID, Ip: ip}); err != nil {
		u.logger.Warn("record last login failed", slog.String("user_id", user.ID), slog.Any("error", err))
	}
	attempt.Result = audit.ResultOK
	u.audit.Record(ctx, attempt)
	view := userViewFromModel(user)
	loginAt := now.UTC()
	view.LastLoginAt = &loginAt
	return cookie, &view, nil
}

// confirmSignIn re-reads the user after its session was stored and deletes
// the session when the password or the disabled flag changed since the user
// was read for verification.
func (u *Users) confirmSignIn(ctx context.Context, verified authdb.User, sessHash string) error {
	current, err := u.q.AuthUserByID(ctx, verified.ID)
	stale := err != nil || current.Disabled || current.PasswordHash != verified.PasswordHash ||
		credentialOf(current.PasswordChangedAt) != credentialOf(verified.PasswordChangedAt)
	if !stale {
		return nil
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTimeout)
	defer cancel()
	if _, derr := u.sessions.delete(dctx, sessHash); derr != nil {
		// Best effort: a session of a changed user is rejected anyway by its
		// password generation or the disabled flag, and its cookie was never
		// handed out.
		u.logger.Error("delete session of a changed user failed", slog.String("user_id", verified.ID), slog.Any("error", derr))
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return apperr.Internal(fmt.Errorf("confirm sign-in: %w", err))
	}
	return invalidCredentials()
}

// plausibleUsername reports whether s could be the username of an account:
// valid UTF-8 without control characters (ValidateUsername admits far less).
func plausibleUsername(s string) bool {
	return utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (u *Users) recordDenied(ctx context.Context, e audit.Entry, reason string) {
	e.Result = audit.ResultDenied
	e.Details = map[string]any{"reason": reason}
	u.audit.Record(ctx, e)
}

// dummy returns a hash verified for unknown usernames so that timing does not
// reveal whether an account exists.
func (u *Users) dummy() string {
	u.dummyOnce.Do(func() {
		h, err := HashPasswordWithParams("spinneret-dummy-password", u.cfg.Argon2)
		if err != nil {
			u.logger.Error("compute dummy password hash", slog.Any("error", err))
			return
		}
		u.dummyHash = h
	})
	return u.dummyHash
}

// verify checks a password under the hashing concurrency limit.
func (u *Users) verify(ctx context.Context, password, encoded string) (bool, error) {
	select {
	case u.hashSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-u.hashSem }()
	return VerifyPassword(password, encoded)
}

// hash computes a new password hash under the hashing concurrency limit.
func (u *Users) hash(ctx context.Context, password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	select {
	case u.hashSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-u.hashSem }()
	h, err := HashPasswordWithParams(password, u.cfg.Argon2)
	if err != nil {
		return "", apperr.Internal(err)
	}
	return h, nil
}

// Logout deletes the session identified by the cookie value. Unknown or
// malformed sessions are ignored.
func (u *Users) Logout(ctx context.Context, cookie string) error {
	hash, ok := sessionHashOf(cookie)
	if !ok {
		return nil
	}
	userID, err := u.sessions.delete(ctx, hash)
	if err != nil {
		return apperr.Internal(err)
	}
	if userID != "" {
		p, _ := authz.FromContext(ctx)
		e := audit.FromPrincipal(p, "", "", ActionLogout, ResourceUser, userID, "", audit.ResultOK, nil)
		e.TenantID = ""
		u.audit.Record(ctx, e)
	}
	return nil
}

// ChangePassword changes the password of the signed-in user after verifying
// the current one, and ends every other session of the user (the session of
// currentCookie is kept).
func (u *Users) ChangePassword(ctx context.Context, p *authz.Principal, currentCookie, current, next string) error {
	if err := requireUser(p); err != nil {
		return err
	}
	if err := ValidatePassword(next); err != nil {
		return err
	}
	user, err := u.q.AuthUserByID(ctx, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSessionInvalid()
	}
	if err != nil {
		return apperr.Internal(err)
	}
	if user.Disabled {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "account is disabled")
	}
	if err := u.throttle.reserve(ctx, user.Username, p.ClientIP); err != nil {
		if apperr.ReasonOf(err) == apperr.ReasonLoginThrottled {
			return err
		}
		return apperr.Internal(err)
	}
	ok, err := u.verify(ctx, current, user.PasswordHash)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	entry := audit.FromPrincipal(p, "", "", ActionChangePassword, ResourceUser, user.ID, user.Username, audit.ResultOK, nil)
	entry.TenantID = ""
	if err != nil || !ok {
		entry.Result = audit.ResultDenied
		u.audit.Record(ctx, entry)
		return apperr.PermissionDenied(apperr.ReasonPermissionDenied, "current password is incorrect")
	}
	if err := u.throttle.succeeded(ctx, user.Username, p.ClientIP); err != nil {
		u.logger.Warn("login throttle release failed", slog.Any("error", err))
	}
	if current == next {
		return apperr.InvalidArgument("", "the new password must differ from the current password")
	}
	hashed, err := u.hash(ctx, next)
	if err != nil {
		return err
	}
	changedAt, err := u.q.AuthUserSetPassword(ctx, authdb.AuthUserSetPasswordParams{ID: user.ID, PasswordHash: hashed})
	if errors.Is(err, pgx.ErrNoRows) {
		return errSessionInvalid()
	}
	if err != nil {
		return apperr.Internal(err)
	}
	keep, _ := sessionHashOf(currentCookie)
	if keep != "" {
		// The current session moves to the new password generation; every
		// other session keeps the old one and is no longer accepted.
		kept, err := u.sessions.setCredential(ctx, keep, user.ID, credentialOf(changedAt))
		if err != nil {
			u.logger.Warn("keep current session after password change failed", slog.String("user_id", user.ID), slog.Any("error", err))
		}
		if !kept {
			keep = ""
		}
	}
	if _, err := u.sessions.revokeUser(ctx, user.ID, keep); err != nil {
		return apperr.Internal(err)
	}
	u.publishUserChanged(ctx, user.ID)
	u.audit.Record(ctx, entry)
	return nil
}

// SessionCookie builds the Set-Cookie value for a new session. requestSecure
// is RequestMeta.Secure of the sign-in request.
func (u *Users) SessionCookie(value string, requestSecure bool) *http.Cookie {
	return u.cfg.sessionCookie(value, requestSecure)
}

// ClearSessionCookie builds the Set-Cookie value that removes the session cookie.
func (u *Users) ClearSessionCookie(requestSecure bool) *http.Cookie {
	c := u.SessionCookie("", requestSecure) //nolint:gosec // G124: attributes set by Config.sessionCookie (HttpOnly, SameSite=Strict, Secure per CookieSecure).
	c.MaxAge = -1
	return c
}

// RequestMeta returns the metadata stored by the interceptor, or computes it
// from the given header and peer address when absent.
func (u *Users) RequestMeta(ctx context.Context, header http.Header, peerAddr string) RequestMeta {
	if m, ok := RequestMetaFrom(ctx); ok {
		return m
	}
	r := (&http.Request{Method: http.MethodPost, Header: header, RemoteAddr: peerAddr}).WithContext(ctx)
	if r.Header == nil {
		r.Header = http.Header{}
	}
	return ExtractRequestMeta(r, u.cfg.TrustedProxies)
}

// SessionCookieValue returns the session cookie value carried by header ("" when absent).
func SessionCookieValue(header http.Header) string {
	c, err := (&http.Request{Header: header}).Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// publishUserChanged notifies authenticators that a user's data changed
// ("" = every user).
func (u *Users) publishUserChanged(ctx context.Context, userID string) {
	if u.bus == nil {
		return
	}
	data := tokensEventData{UserID: userID, All: userID == ""}
	raw, err := json.Marshal(data)
	if err != nil {
		u.logger.Error("encode user change event", slog.Any("error", err))
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTimeout)
	defer cancel()
	if err := u.bus.Publish(pctx, events.ChannelTokens, events.Event{Type: EventUserChanged, Data: raw}); err != nil {
		u.logger.Warn("publish user change event", slog.String("user_id", userID), slog.Any("error", err))
	}
}
