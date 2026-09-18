package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// maxTenantIDLength bounds the active tenant header.
const maxTenantIDLength = 64

// Authenticator verifies API tokens and console sessions (spec §3.3). It is
// safe for concurrent use. Run must be started to receive revocations from
// peer instances and to persist token last-used data.
type Authenticator struct {
	cfg      Config
	q        *authdb.Queries
	bus      events.Bus
	logger   *slog.Logger
	tokens   *tokenCache
	users    *userCache
	sessions *sessionStore
	lastUsed *lastUsedTracker
	limiter  *rateLimiter

	now           func() time.Time
	flushInterval time.Duration
	// started is closed once Run has subscribed to the bus.
	started   chan struct{}
	startOnce sync.Once
}

// NewAuthenticator creates an authenticator. bus may be nil (single instance
// without revocation fan-out).
func NewAuthenticator(cfg Config, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, bus events.Bus, logger *slog.Logger) *Authenticator {
	cfg = cfg.normalized()
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(slog.String("component", "auth"))
	q := authdb.New(pool)
	return &Authenticator{
		cfg:           cfg,
		q:             q,
		bus:           bus,
		logger:        logger,
		tokens:        newTokenCache(pool, cfg.TokenCacheTTL, logger),
		users:         newUserCache(q),
		sessions:      &sessionStore{rdb: rdb, keys: keys, ttl: cfg.SessionTTL},
		lastUsed:      newLastUsedTracker(),
		limiter:       newRateLimiter(),
		now:           time.Now,
		flushInterval: lastUsedFlushInterval,
		started:       make(chan struct{}),
	}
}

// authResult is the outcome of a successful authentication.
type authResult struct {
	principal    *authz.Principal
	rateLimitRPS int
	// refresh is set when the session needs its sliding expiry extended and
	// the caller deferred the refresh (see refreshSession).
	refresh *sessionRefresh
}

// sessionRefresh is a pending sliding-expiry extension of a console session.
type sessionRefresh struct {
	sess   session
	cookie string
}

// refreshMode selects whether authenticateSession extends a session inline or
// leaves it to the caller, which can then re-issue the cookie.
type refreshMode int

const (
	refreshInline refreshMode = iota
	refreshDeferred
)

// Authenticate authenticates r with (in order of precedence) an
// "Authorization: Bearer spn_…" token or the session cookie. It returns
// nil, nil when the request carries no credentials, and an apperr error when
// credentials are present but invalid (token_invalid, token_expired,
// token_revoked, ip_not_allowed, session_invalid, csrf_missing,
// permission_denied for an inaccessible active tenant).
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (*authz.Principal, error) {
	res, err := a.authenticate(ctx, r, a.RequestMeta(r), refreshInline)
	if err != nil || res == nil {
		return nil, err
	}
	return res.principal, nil
}

// RequestMeta extracts request metadata using the configured trusted proxies.
func (a *Authenticator) RequestMeta(r *http.Request) RequestMeta {
	return ExtractRequestMeta(r, a.cfg.TrustedProxies)
}

// Config returns the normalized configuration.
func (a *Authenticator) Config() Config {
	return a.cfg
}

func (a *Authenticator) authenticate(ctx context.Context, r *http.Request, meta RequestMeta, mode refreshMode) (*authResult, error) {
	if token, ok := bearerToken(r.Header); ok {
		return a.authenticateToken(ctx, token, meta)
	}
	// A missing or empty session cookie means the request carries no credentials.
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return a.authenticateSession(ctx, r, c.Value, meta, mode)
	}
	return nil, nil
}

// bearerToken extracts a Bearer credential. Other Authorization schemes are
// ignored so that deployments behind proxies using Basic auth keep working
// with session cookies.
func bearerToken(h http.Header) (string, bool) {
	v := strings.TrimSpace(h.Get("Authorization"))
	scheme, rest, _ := strings.Cut(v, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func (a *Authenticator) authenticateToken(ctx context.Context, token string, meta RequestMeta) (*authResult, error) {
	if !WellFormedToken(token) {
		return nil, apperr.Unauthenticated(apperr.ReasonTokenInvalid, "invalid API token")
	}
	rec, err := a.tokens.lookup(ctx, HashToken(token))
	if errors.Is(err, errTokenUnknown) {
		return nil, apperr.Unauthenticated(apperr.ReasonTokenInvalid, "invalid API token")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	now := a.now()
	switch {
	case rec.corrupt:
		return nil, apperr.Unauthenticated(apperr.ReasonTokenInvalid, "invalid API token")
	case rec.revoked:
		return nil, apperr.Unauthenticated(apperr.ReasonTokenRevoked, "API token has been revoked")
	case !rec.expiresAt.IsZero() && !now.Before(rec.expiresAt):
		return nil, apperr.Unauthenticated(apperr.ReasonTokenExpired, "API token has expired")
	}
	if len(rec.allow) > 0 {
		ip, err := netip.ParseAddr(meta.ClientIP)
		if err != nil || !netx.AllowedIP(ip, rec.allow) {
			return nil, apperr.PermissionDenied(apperr.ReasonIPNotAllowed, "client address is not allowed for this API token")
		}
	}
	a.lastUsed.record(rec.id, now, meta.ClientIP)
	return &authResult{
		principal: &authz.Principal{
			Kind:          authz.KindToken,
			ID:            rec.id,
			Name:          rec.name,
			TenantID:      rec.tenantID,
			NamespaceID:   rec.namespaceID,
			NamespaceName: rec.namespaceName,
			Scopes:        rec.scopes,
			ClientIP:      meta.ClientIP,
			UserAgent:     meta.UserAgent,
			Node:          meta.Node,
		},
		rateLimitRPS: rec.rateLimitRPS,
	}, nil
}

func (a *Authenticator) authenticateSession(ctx context.Context, r *http.Request, cookie string, meta RequestMeta, mode refreshMode) (*authResult, error) {
	hash, ok := sessionHashOf(cookie)
	if !ok {
		return nil, errSessionInvalid()
	}
	now := a.now()
	rctx, cancel := context.WithTimeout(ctx, redisTimeout)
	sess, err := a.sessions.get(rctx, hash, now)
	cancel()
	if errors.Is(err, errSessionUnknown) {
		return nil, errSessionInvalid()
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	user, err := a.sessionUser(ctx, sess)
	if errors.Is(err, errUserUnknown) || errors.Is(err, errSessionCredential) {
		return nil, errSessionInvalid()
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if user.disabled {
		return nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "account is disabled")
	}
	if !safeMethod(r.Method) && strings.TrimSpace(r.Header.Get(HeaderCSRF)) != "1" {
		return nil, apperr.PermissionDenied(apperr.ReasonCSRFMissing, "missing %s header", HeaderCSRF)
	}
	tenantID, err := a.activeTenant(ctx, r, user)
	if err != nil {
		return nil, err
	}
	res := &authResult{principal: user.principal(tenantID, meta)}
	if a.sessions.needsRefresh(sess, now) {
		res.refresh = &sessionRefresh{sess: sess, cookie: cookie}
		if mode == refreshInline {
			a.refreshSession(ctx, res, meta.Secure)
			res.refresh = nil
		}
	}
	return res, nil
}

// errSessionCredential reports a session created with another password
// generation than the user's current one.
var errSessionCredential = errors.New("auth: session password generation does not match")

// sessionUser loads the user of a session and checks that the session was
// created with the user's current password generation. A session newer than
// the cached user record (a password change whose user.changed event has not
// reached this instance yet) reloads the record once instead of rejecting
// the session.
func (a *Authenticator) sessionUser(ctx context.Context, sess session) (*userRecord, error) {
	user, err := a.users.user(ctx, sess.userID)
	if err != nil {
		return nil, err
	}
	if sess.credential == user.credential {
		return user, nil
	}
	if !credentialNewer(sess.credential, user.credential) {
		return nil, errSessionCredential
	}
	a.users.dropUser(sess.userID)
	if user, err = a.users.user(ctx, sess.userID); err != nil {
		return nil, err
	}
	if sess.credential != user.credential {
		return nil, errSessionCredential
	}
	return user, nil
}

// credentialNewer reports whether password generation a is later than b.
func credentialNewer(a, b string) bool {
	x, errA := strconv.ParseInt(a, 10, 64)
	y, errB := strconv.ParseInt(b, 10, 64)
	return errA == nil && errB == nil && x > y
}

// refreshSession extends the session of a deferred refresh (sliding expiry)
// and returns the cookie to send again, so the browser's Max-Age slides with
// the server-side expiry. It returns nil when there is nothing to refresh, the
// session no longer exists or Redis fails (the next request retries).
func (a *Authenticator) refreshSession(ctx context.Context, res *authResult, requestSecure bool) *http.Cookie {
	if res == nil || res.refresh == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), redisTimeout)
	defer cancel()
	extended, err := a.sessions.refresh(rctx, res.refresh.sess, a.now())
	if err != nil {
		a.logger.Warn("session refresh failed", slog.String("user_id", res.refresh.sess.userID), slog.Any("error", err))
		return nil
	}
	if !extended {
		return nil
	}
	return a.cfg.sessionCookie(res.refresh.cookie, requestSecure)
}

// activeTenant resolves and checks the tenant selected by the request.
func (a *Authenticator) activeTenant(ctx context.Context, r *http.Request, user *userRecord) (string, error) {
	tenantID := strings.TrimSpace(r.Header.Get(HeaderTenant))
	if tenantID == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL != nil {
		tenantID = strings.TrimSpace(r.URL.Query().Get(QueryTenant))
	}
	if tenantID == "" {
		return "", nil
	}
	denied := apperr.PermissionDenied(apperr.ReasonPermissionDenied, "tenant %q is not accessible", truncateUTF8(tenantID, maxTenantIDLength))
	if len(tenantID) > maxTenantIDLength {
		return "", denied
	}
	if user.platformAdmin {
		exists, err := a.users.tenantExists(ctx, tenantID)
		if err != nil {
			return "", apperr.Internal(err)
		}
		if !exists {
			return "", denied
		}
		return tenantID, nil
	}
	for _, b := range user.bindings {
		if b.TenantID == tenantID && authz.ValidRole(b.Role) {
			return tenantID, nil
		}
	}
	return "", denied
}

func errSessionInvalid() error {
	return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "session is invalid or has expired")
}

// safeMethod reports whether an HTTP method is exempt from the CSRF header.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// allowRate applies the per-token rate limit of an authentication result.
func (a *Authenticator) allowRate(res *authResult) error {
	if res == nil || res.principal.Kind != authz.KindToken || res.rateLimitRPS <= 0 {
		return nil
	}
	ok, wait := a.limiter.allow(res.principal.ID, res.rateLimitRPS, a.now())
	if ok {
		return nil
	}
	return apperr.ResourceExhausted(apperr.ReasonRateLimited, wait.Milliseconds(), "API token rate limit of %d requests per second exceeded", res.rateLimitRPS)
}

// InvalidateToken drops a token from this instance's caches.
func (a *Authenticator) InvalidateToken(tokenID string) {
	a.tokens.dropToken(tokenID)
	a.limiter.forget(tokenID)
}

// InvalidateUser drops a user from this instance's caches; "" drops every user.
func (a *Authenticator) InvalidateUser(userID string) {
	if userID == "" {
		a.users.purge()
		return
	}
	a.users.dropUser(userID)
}

// tokensEventData is the payload of events on events.ChannelTokens.
type tokensEventData struct {
	TokenID string `json:"token_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	All     bool   `json:"all,omitempty"`
}

// onEvent handles cache invalidations from the bus.
func (a *Authenticator) onEvent(_ context.Context, _ string, ev events.Event) {
	var data tokensEventData
	if len(ev.Data) > 0 {
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			a.logger.Warn("ignoring malformed tokens event", slog.String("type", ev.Type), slog.Any("error", err))
			return
		}
	}
	switch {
	case ev.Type == EventUserChanged && (data.All || data.UserID == ""):
		a.InvalidateUser("")
	case ev.Type == EventUserChanged:
		a.InvalidateUser(data.UserID)
	case data.TokenID != "":
		a.InvalidateToken(data.TokenID)
	case data.All:
		a.tokens.purge()
	}
}

// Run subscribes to token revocations and user changes and flushes token
// last-used data every 30 s until ctx is canceled (with a final flush).
func (a *Authenticator) Run(ctx context.Context) error {
	if a.bus != nil {
		unsubscribe := a.bus.Subscribe(events.ChannelTokens, a.onEvent)
		defer unsubscribe()
	}
	a.startOnce.Do(func() { close(a.started) })
	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
			a.flushLastUsed(fctx)
			cancel()
			return nil
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, dbTimeout)
			a.flushLastUsed(fctx)
			cancel()
		}
	}
}

func (a *Authenticator) flushLastUsed(ctx context.Context) {
	if err := a.lastUsed.flush(ctx, a.q); err != nil {
		a.logger.Warn("token last-used flush failed", slog.Any("error", err))
	}
}
