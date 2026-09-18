package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// CORS settings for the console development server (SPINNERET_ALLOWED_ORIGINS).
var (
	corsAllowMethods = "GET, POST, OPTIONS"
	corsAllowHeaders = strings.Join([]string{
		"Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms",
		"X-Spinneret-CSRF", "X-Spinneret-Tenant", "X-Spinneret-Node", "Authorization",
	}, ", ")
	corsExposeHeaders = strings.Join([]string{apperr.HeaderReason, apperr.HeaderRetryAfter}, ", ")
)

const corsMaxAge = "7200"

// corsMiddleware answers CORS preflight requests (before any authentication)
// and decorates responses for the configured origins. Credentials are allowed
// only for explicitly listed origins; "*" allows any origin without credentials.
type corsMiddleware struct {
	next     http.Handler
	origins  map[string]bool
	wildcard bool
}

// newCORSMiddleware wraps next; it returns next unchanged when no origin is configured.
func newCORSMiddleware(origins []string, next http.Handler) http.Handler {
	m := &corsMiddleware{next: next, origins: make(map[string]bool, len(origins))}
	for _, o := range origins {
		switch o = strings.TrimSpace(o); o {
		case "":
		case "*":
			m.wildcard = true
		default:
			if n, ok := normalizeOrigin(o); ok {
				m.origins[n] = true
			}
		}
	}
	if !m.wildcard && len(m.origins) == 0 {
		return next
	}
	return m
}

// normalizeOrigin lower-cases the scheme and host of an origin and strips any
// trailing slash, so configured values compare equal to browser Origin headers.
func normalizeOrigin(o string) (string, bool) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(o), "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), true
}

func (m *corsMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		m.next.ServeHTTP(w, r)
		return
	}
	h := w.Header()
	h.Add("Vary", "Origin")
	normalized, _ := normalizeOrigin(origin)
	listed := normalized != "" && m.origins[normalized]
	allowed := listed || m.wildcard
	preflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
	if preflight {
		h.Add("Vary", "Access-Control-Request-Method")
		h.Add("Vary", "Access-Control-Request-Headers")
		if !allowed {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		m.setAllowOrigin(h, origin, listed)
		h.Set("Access-Control-Allow-Methods", corsAllowMethods)
		h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
		h.Set("Access-Control-Max-Age", corsMaxAge)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if allowed {
		m.setAllowOrigin(h, origin, listed)
		h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
	}
	m.next.ServeHTTP(w, r)
}

func (m *corsMiddleware) setAllowOrigin(h http.Header, origin string, listed bool) {
	if listed {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		return
	}
	h.Set("Access-Control-Allow-Origin", "*")
}

// Request deadlines applied by the deadline interceptor. A shorter deadline
// sent by the client (Connect-Timeout-Ms) always wins.
const (
	defaultRequestTimeout = 60 * time.Second
	// longRequestTimeout bounds imports and bulk administrative operations
	// whose duration grows with the number of identities or proxies.
	longRequestTimeout  = 5 * time.Minute
	watchDefaultTimeout = 30 * time.Second
	watchMaxTimeout     = 60 * time.Second
	watchTimeoutSlack   = 10 * time.Second
)

// timeoutRequest is implemented by requests that carry their own long-poll timeout.
type timeoutRequest interface {
	GetTimeoutMs() int32
}

// deadlineInterceptor bounds every unary handler with a per-procedure
// deadline, because the HTTP server has no write timeout (long polls and SSE
// need unbounded responses).
type deadlineInterceptor struct {
	defaultTimeout time.Duration
	overrides      map[string]time.Duration
}

func newDeadlineInterceptor() *deadlineInterceptor {
	return &deadlineInterceptor{
		defaultTimeout: defaultRequestTimeout,
		overrides: map[string]time.Duration{
			// Imports.
			spinneretv1connect.IdentityAdminServiceImportIdentitiesProcedure: longRequestTimeout,
			spinneretv1connect.ProxyAdminServiceImportProxiesProcedure:       longRequestTimeout,
			// Bulk operations that scale with the number of identities or
			// proxies: a 60 s deadline would roll back their transactions on
			// large sites (for example re-hashing every identity of a type or
			// force-deleting a site) so they could never complete.
			spinneretv1connect.IdentityAdminServiceUpdateIdentityTypeProcedure:    longRequestTimeout,
			spinneretv1connect.IdentityAdminServiceDeleteIdentityTypeProcedure:    longRequestTimeout,
			spinneretv1connect.IdentityAdminServiceOperateIdentitiesProcedure:     longRequestTimeout,
			spinneretv1connect.IdentityAdminServiceBulkOperateIdentitiesProcedure: longRequestTimeout,
			spinneretv1connect.IdentityAdminServiceOperateAccountProcedure:        longRequestTimeout,
			spinneretv1connect.IdentityAdminServiceRevertActionsProcedure:         longRequestTimeout,
			spinneretv1connect.ProxyAdminServiceOperateProxiesProcedure:           longRequestTimeout,
			spinneretv1connect.ProxyAdminServiceDeleteProxiesProcedure:            longRequestTimeout,
			spinneretv1connect.SiteAdminServiceDeleteSiteProcedure:                longRequestTimeout,
			spinneretv1connect.TenantAdminServiceDeleteNamespaceProcedure:         longRequestTimeout,
			spinneretv1connect.TenantAdminServiceDeleteTenantProcedure:            longRequestTimeout,
		},
	}
}

// timeout returns the deadline for one request.
func (d *deadlineInterceptor) timeout(procedure string, msg any) time.Duration {
	if procedure == spinneretv1connect.ConfigServiceWatchConfigProcedure {
		wait := watchDefaultTimeout
		if tr, ok := msg.(timeoutRequest); ok && tr.GetTimeoutMs() > 0 {
			wait = time.Duration(tr.GetTimeoutMs()) * time.Millisecond
		}
		return min(wait, watchMaxTimeout) + watchTimeoutSlack
	}
	if t, ok := d.overrides[procedure]; ok {
		return t
	}
	return d.defaultTimeout
}

func (d *deadlineInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		ctx, cancel := context.WithTimeout(ctx, d.timeout(req.Spec().Procedure, req.Any()))
		defer cancel()
		return next(ctx, req)
	}
}

func (d *deadlineInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (d *deadlineInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// drainMiddleware cancels long-lived requests (SSE streams, config long polls)
// once shutdown begins, so http.Server.Shutdown does not wait for them to time
// out. Clients reconnect to another instance.
type drainMiddleware struct {
	next  http.Handler
	drain context.Context // canceled when the server starts draining
}

func (m drainMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if m.drain.Err() != nil {
		// AfterFunc runs asynchronously for an already canceled context.
		cancel()
	}
	stop := context.AfterFunc(m.drain, cancel)
	defer stop()
	m.next.ServeHTTP(w, r.WithContext(ctx))
}
