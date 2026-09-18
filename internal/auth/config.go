// Package auth implements authentication for Spinneret (spec §3.3): API token
// verification with an in-process cache, console users with Argon2id
// passwords, Redis-backed sessions with sliding expiry, login throttling, CSRF
// and active-tenant checks, the Connect interceptor and HTTP middleware that
// attach an authz.Principal to every request, API token management, role
// bindings and audit log queries.
//
// Every SQL query of the package lives in queries/*.sql and is compiled by a
// private sqlc configuration into the authdb package.
package auth

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Well-known names shared with the console and SDKs.
const (
	// SessionCookieName is the console session cookie.
	SessionCookieName = "spinneret_session"
	// HeaderCSRF must be "1" on cookie-authenticated unsafe requests.
	HeaderCSRF = "X-Spinneret-CSRF"
	// HeaderTenant selects the active tenant of a console request.
	HeaderTenant = "X-Spinneret-Tenant"
	// HeaderNode names the calling node instance.
	HeaderNode = "X-Spinneret-Node"
	// HeaderForwardedProto is honoured from trusted proxies for cookie security.
	HeaderForwardedProto = "X-Forwarded-Proto"
	// QueryTenant is the query parameter selecting the active tenant of GET
	// requests (the SSE endpoint cannot set headers).
	QueryTenant = "tenant"
)

// Cookie security modes of Config.CookieSecure.
const (
	CookieSecureAuto  = "auto"
	CookieSecureTrue  = "true"
	CookieSecureFalse = "false"
)

// Event types published on events.ChannelTokens.
const (
	// EventTokenRevoked drops a token from every verification cache; data {"token_id":"…"}.
	EventTokenRevoked = "token.revoked"
	// EventUserChanged drops cached users; data {"user_id":"…"} or {"all":true}.
	EventUserChanged = "user.changed"
)

// Defaults applied by Config.normalized.
const (
	DefaultTokenCacheTTL = 30 * time.Second
	DefaultSessionTTL    = 12 * time.Hour

	// lastUsedFlushInterval is how often token last-used data is written.
	lastUsedFlushInterval = 30 * time.Second
	// negativeTokenCacheTTL caches unknown token hashes.
	negativeTokenCacheTTL = 5 * time.Second
	// userCacheTTL bounds the staleness of cached users and role bindings.
	userCacheTTL = 5 * time.Second
	// dbTimeout bounds a single database round trip issued by authentication.
	dbTimeout = 5 * time.Second
	// redisTimeout bounds a single Redis round trip issued by authentication.
	redisTimeout = 2 * time.Second
)

// Config configures authentication.
type Config struct {
	// TokenCacheTTL is how long a token verification result is cached.
	TokenCacheTTL time.Duration
	// SessionTTL is the console session lifetime (sliding).
	SessionTTL time.Duration
	// CookieSecure is "auto" (Secure when the request arrived over TLS or
	// through a trusted proxy with X-Forwarded-Proto: https), "true" or "false".
	CookieSecure string
	// TrustedProxies lists the reverse proxies whose forwarding headers are honoured.
	TrustedProxies []netip.Prefix
	// InstanceID identifies this server instance in logs.
	InstanceID string
	// Argon2 overrides the password hashing parameters of new hashes. The zero
	// value selects DefaultArgon2Params; only tests should lower it.
	Argon2 Argon2Params
}

// normalized returns a copy of c with defaults applied.
func (c Config) normalized() Config {
	if c.TokenCacheTTL <= 0 {
		c.TokenCacheTTL = DefaultTokenCacheTTL
	}
	if c.SessionTTL <= 0 {
		c.SessionTTL = DefaultSessionTTL
	}
	switch strings.ToLower(strings.TrimSpace(c.CookieSecure)) {
	case CookieSecureTrue:
		c.CookieSecure = CookieSecureTrue
	case CookieSecureFalse:
		c.CookieSecure = CookieSecureFalse
	default:
		c.CookieSecure = CookieSecureAuto
	}
	if c.Argon2 == (Argon2Params{}) {
		c.Argon2 = DefaultArgon2Params()
	}
	c.TrustedProxies = append([]netip.Prefix(nil), c.TrustedProxies...)
	return c
}

// cookieSecure decides the Secure attribute for a request whose transport
// security is requestSecure.
func (c Config) cookieSecure(requestSecure bool) bool {
	switch c.CookieSecure {
	case CookieSecureTrue:
		return true
	case CookieSecureFalse:
		return false
	default:
		return requestSecure
	}
}

// sessionCookie builds the session cookie (HttpOnly, SameSite=Strict, Path=/,
// Max-Age = SessionTTL, Secure per CookieSecure).
func (c Config) sessionCookie(value string, requestSecure bool) *http.Cookie {
	//nolint:gosec // G124: HttpOnly and SameSite=Strict are always set; Secure follows CookieSecure ("auto" = the request's transport security).
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   int(c.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   c.cookieSecure(requestSecure),
		SameSite: http.SameSiteStrictMode,
	}
}

// setsSessionCookie reports whether h already sets the session cookie (for
// example a sign-in or sign-out response).
func setsSessionCookie(h http.Header) bool {
	for _, v := range h.Values("Set-Cookie") {
		if strings.HasPrefix(strings.TrimSpace(v), SessionCookieName+"=") {
			return true
		}
	}
	return false
}
