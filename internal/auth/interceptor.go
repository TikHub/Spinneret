package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"connectrpc.com/connect"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// PublicProcedures returns the Connect procedures that do not require
// authentication.
func PublicProcedures() map[string]bool {
	return map[string]bool{"/spinneret.v1.AuthService/Login": true}
}

// interceptor authenticates Connect requests.
type interceptor struct {
	auth   *Authenticator
	public map[string]bool
	logger *slog.Logger
}

// NewInterceptor returns a Connect interceptor (unary and streaming handler)
// that authenticates every request, stores the principal (with client IP,
// user agent and X-Spinneret-Node) and RequestMeta in the context, rejects
// unauthenticated calls to procedures not listed in public, and enforces the
// per-token rate limit (resource_exhausted/rate_limited). Credential errors on
// public procedures are ignored so that, for example, a stale session cookie
// cannot block Login. Errors are returned as *connect.Error.
//
// When a console session is past half of its lifetime, its expiry is extended
// and the session cookie is sent again (unary: after the handler succeeded;
// streaming: before the handler runs), unless the handler itself sets the
// session cookie (sign-in, sign-out).
func NewInterceptor(a *Authenticator, public map[string]bool, logger *slog.Logger) connect.Interceptor {
	if logger == nil {
		logger = slog.Default()
	}
	pub := make(map[string]bool, len(public))
	for k, v := range public {
		if v {
			pub[k] = true
		}
	}
	return &interceptor{auth: a, public: pub, logger: logger.With(slog.String("component", "auth"))}
}

// WrapUnary implements connect.Interceptor.
func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		ctx, res, err := i.authenticate(ctx, req.Spec().Procedure, req.HTTPMethod(), req.Header(), req.Peer())
		if err != nil {
			return nil, err
		}
		resp, err := next(ctx, req)
		if err == nil && resp != nil {
			i.reissueSessionCookie(ctx, res, resp.Header())
		}
		return resp, err
	}
}

// WrapStreamingClient implements connect.Interceptor (no-op).
func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements connect.Interceptor.
func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, res, err := i.authenticate(ctx, conn.Spec().Procedure, http.MethodPost, conn.RequestHeader(), conn.Peer())
		if err != nil {
			return err
		}
		i.reissueSessionCookie(ctx, res, conn.ResponseHeader())
		return next(ctx, conn)
	}
}

// authenticate runs authentication for one call and returns the enriched
// context with the authentication result (nil for anonymous public calls).
func (i *interceptor) authenticate(ctx context.Context, procedure, method string, header http.Header, peer connect.Peer) (context.Context, *authResult, error) {
	r := syntheticRequest(ctx, method, header, peer)
	meta := i.auth.RequestMeta(r)
	ctx = WithRequestMeta(ctx, meta)
	res, err := i.auth.authenticate(ctx, r, meta, refreshDeferred)
	public := i.public[procedure]
	if err != nil {
		if public {
			return ctx, nil, nil
		}
		i.logFailure(procedure, meta, err)
		return ctx, nil, apperr.ToConnect(err)
	}
	if res == nil {
		if public {
			return ctx, nil, nil
		}
		return ctx, nil, apperr.ToConnect(apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required"))
	}
	if err := i.auth.allowRate(res); err != nil {
		return ctx, nil, apperr.ToConnect(err)
	}
	return authz.WithPrincipal(ctx, res.principal), res, nil
}

// reissueSessionCookie extends a session that needs a sliding refresh and adds
// the renewed cookie to the response header h.
func (i *interceptor) reissueSessionCookie(ctx context.Context, res *authResult, h http.Header) {
	if res == nil || res.refresh == nil || setsSessionCookie(h) {
		return
	}
	meta, _ := RequestMetaFrom(ctx)
	if c := i.auth.refreshSession(ctx, res, meta.Secure); c != nil {
		h.Add("Set-Cookie", c.String())
	}
}

func (i *interceptor) logFailure(procedure string, meta RequestMeta, err error) {
	e, ok := apperr.As(err)
	if ok && e.Code != connect.CodeInternal {
		i.logger.Debug("authentication rejected", slog.String("procedure", procedure),
			slog.String("reason", string(e.Reason)), slog.String("client_ip", meta.ClientIP))
		return
	}
	i.logger.Error("authentication failed", slog.String("procedure", procedure), slog.Any("error", err))
}

// syntheticRequest builds the *http.Request view that Authenticate needs from
// what Connect exposes to interceptors.
func syntheticRequest(ctx context.Context, method string, header http.Header, peer connect.Peer) *http.Request {
	if method == "" {
		method = http.MethodPost
	}
	u := &url.URL{}
	if peer.Query != nil {
		u.RawQuery = peer.Query.Encode()
	}
	r := &http.Request{Method: method, URL: u, Header: header, RemoteAddr: peer.Addr}
	if r.Header == nil {
		r.Header = http.Header{}
	}
	return r.WithContext(ctx)
}

// HTTPMiddleware authenticates plain HTTP handlers (the SSE endpoint): the
// principal and RequestMeta are stored in the request context; requests
// without valid credentials get an error response (401 when absent) with the
// Spinneret-Reason header and a Connect-style JSON body. A session past half
// of its lifetime is extended and its cookie sent again before next runs.
func HTTPMiddleware(a *Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := a.RequestMeta(r)
		ctx := WithRequestMeta(r.Context(), meta)
		res, err := a.authenticate(ctx, r, meta, refreshDeferred)
		if err == nil && res == nil {
			err = apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
		}
		if err == nil {
			err = a.allowRate(res)
		}
		if err != nil {
			if e, ok := apperr.As(err); !ok || e.Code == connect.CodeInternal {
				a.logger.Error("authentication failed", slog.String("path", r.URL.Path), slog.Any("error", err))
			}
			writeHTTPError(w, err)
			return
		}
		if c := a.refreshSession(ctx, res, meta.Secure); c != nil {
			w.Header().Add("Set-Cookie", c.String())
		}
		next.ServeHTTP(w, r.WithContext(authz.WithPrincipal(ctx, res.principal)))
	})
}

// writeHTTPError writes err in the Connect unary error JSON format.
func writeHTTPError(w http.ResponseWriter, err error) {
	ce := apperr.ToConnect(err)
	for k, vs := range ce.Meta() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if ce.Code() == connect.CodeResourceExhausted {
		if e, ok := apperr.As(err); ok && e.RetryAfterMs > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt((e.RetryAfterMs+999)/1000, 10))
		}
	}
	w.WriteHeader(httpStatus(ce.Code()))
	body, mErr := json.Marshal(map[string]string{"code": ce.Code().String(), "message": ce.Message()})
	if mErr != nil {
		return
	}
	_, _ = w.Write(body)
}

// httpStatus maps Connect codes to HTTP status codes (Connect protocol table).
func httpStatus(code connect.Code) int {
	switch code {
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
