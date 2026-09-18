package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// captureAuthService records the principal and metadata seen by handlers.
type captureAuthService struct {
	spinneretv1connect.UnimplementedAuthServiceHandler
	mu        sync.Mutex
	principal *authz.Principal
	meta      RequestMeta
	hasMeta   bool
}

func (s *captureAuthService) capture(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principal, _ = authz.FromContext(ctx)
	s.meta, s.hasMeta = RequestMetaFrom(ctx)
}

func (s *captureAuthService) Login(ctx context.Context, _ *connect.Request[spinneretv1.LoginRequest]) (*connect.Response[spinneretv1.LoginResponse], error) {
	s.capture(ctx)
	return connect.NewResponse(&spinneretv1.LoginResponse{}), nil
}

func (s *captureAuthService) GetMe(ctx context.Context, _ *connect.Request[spinneretv1.GetMeRequest]) (*connect.Response[spinneretv1.GetMeResponse], error) {
	s.capture(ctx)
	return connect.NewResponse(&spinneretv1.GetMeResponse{}), nil
}

// Logout mimics the real handler: it sets its own (clearing) session cookie.
func (s *captureAuthService) Logout(ctx context.Context, _ *connect.Request[spinneretv1.LogoutRequest]) (*connect.Response[spinneretv1.LogoutResponse], error) {
	s.capture(ctx)
	resp := connect.NewResponse(&spinneretv1.LogoutResponse{})
	c := &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1}
	resp.Header().Add("Set-Cookie", c.String())
	return resp, nil
}

func (e *env) connectServer(svc *captureAuthService) spinneretv1connect.AuthServiceClient {
	e.t.Helper()
	mux := http.NewServeMux()
	path, h := spinneretv1connect.NewAuthServiceHandler(svc,
		connect.WithInterceptors(NewInterceptor(e.auth, PublicProcedures(), nil)))
	mux.Handle(path, h)
	srv := httptest.NewServer(TLSMiddleware(mux))
	e.t.Cleanup(srv.Close)
	return spinneretv1connect.NewAuthServiceClient(srv.Client(), srv.URL, connect.WithProtoJSON())
}

func TestInterceptorPublicAndProtected(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	svc := &captureAuthService{}
	client := e.connectServer(svc)

	t.Run("public procedure without credentials", func(t *testing.T) {
		_, err := client.Login(e.ctx(), connect.NewRequest(&spinneretv1.LoginRequest{Username: "a", Password: "b"}))
		require.NoError(t, err)
		require.Nil(t, svc.principal)
		require.True(t, svc.hasMeta)
		require.Equal(t, "127.0.0.1", svc.meta.ClientIP)
	})
	t.Run("public procedure ignores stale credentials", func(t *testing.T) {
		req := connect.NewRequest(&spinneretv1.LoginRequest{Username: "a", Password: "b"})
		req.Header().Set("Cookie", SessionCookieName+"=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		_, err := client.Login(e.ctx(), req)
		require.NoError(t, err)
	})
	t.Run("protected procedure without credentials", func(t *testing.T) {
		_, err := client.GetMe(e.ctx(), connect.NewRequest(&spinneretv1.GetMeRequest{}))
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		var ce *connect.Error
		require.True(t, errors.As(err, &ce))
		require.Equal(t, string(apperr.ReasonSessionInvalid), ce.Meta().Get(apperr.HeaderReason))
	})
	t.Run("protected procedure with invalid token", func(t *testing.T) {
		req := connect.NewRequest(&spinneretv1.GetMeRequest{})
		req.Header().Set("Authorization", "Bearer spn_invalid")
		_, err := client.GetMe(e.ctx(), req)
		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		var ce *connect.Error
		require.True(t, errors.As(err, &ce))
		require.Equal(t, string(apperr.ReasonTokenInvalid), ce.Meta().Get(apperr.HeaderReason))
	})
	t.Run("session cookie with CSRF and tenant", func(t *testing.T) {
		cookie := e.login("acme-owner")
		req := connect.NewRequest(&spinneretv1.GetMeRequest{})
		req.Header().Set("Cookie", SessionCookieName+"="+cookie)
		req.Header().Set(HeaderCSRF, "1")
		req.Header().Set(HeaderTenant, w.tenant)
		req.Header().Set(HeaderNode, "console")
		_, err := client.GetMe(e.ctx(), req)
		require.NoError(t, err)
		require.Equal(t, w.owner, svc.principal.ID)
		require.Equal(t, w.tenant, svc.principal.TenantID)
		require.Equal(t, "console", svc.principal.Node)

		req.Header().Del(HeaderCSRF)
		_, err = client.GetMe(e.ctx(), req)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

func TestInterceptorRateLimit(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	client := e.connectServer(&captureAuthService{})
	plaintext, _ := e.newToken(w, "limited", CreateTokenInput{RateLimitRPS: 1})
	call := func() error {
		req := connect.NewRequest(&spinneretv1.GetMeRequest{})
		req.Header().Set("Authorization", "Bearer "+plaintext)
		_, err := client.GetMe(e.ctx(), req)
		return err
	}
	require.NoError(t, call())
	err := call()
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	var ce *connect.Error
	require.True(t, errors.As(err, &ce))
	require.Equal(t, string(apperr.ReasonRateLimited), ce.Meta().Get(apperr.HeaderReason))
	require.NotEmpty(t, ce.Meta().Get(apperr.HeaderRetryAfter))
}

// fakeStreamConn is a minimal streaming handler connection.
type fakeStreamConn struct {
	header   http.Header
	response http.Header
}

func (c *fakeStreamConn) Spec() connect.Spec {
	return connect.Spec{Procedure: "/spinneret.v1.X/Stream", StreamType: connect.StreamTypeServer}
}
func (c *fakeStreamConn) Peer() connect.Peer         { return connect.Peer{Addr: "198.51.100.3:1234"} }
func (c *fakeStreamConn) Receive(any) error          { return nil }
func (c *fakeStreamConn) RequestHeader() http.Header { return c.header }
func (c *fakeStreamConn) Send(any) error             { return nil }
func (c *fakeStreamConn) ResponseHeader() http.Header {
	if c.response == nil {
		c.response = http.Header{}
	}
	return c.response
}
func (c *fakeStreamConn) ResponseTrailer() http.Header { return http.Header{} }

func TestInterceptorStreamingAndClient(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	plaintext, id := e.newToken(w, "streamer", CreateTokenInput{})
	ic := NewInterceptor(e.auth, map[string]bool{"/x": true, "/y": false}, nil)

	var seen *authz.Principal
	next := func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		seen, _ = authz.FromContext(ctx)
		return nil
	}
	h := ic.WrapStreamingHandler(next)
	err := h(e.ctx(), &fakeStreamConn{header: http.Header{"Authorization": []string{"Bearer " + plaintext}}})
	require.NoError(t, err)
	require.Equal(t, id, seen.ID)
	require.Equal(t, "198.51.100.3", seen.ClientIP)

	err = h(e.ctx(), &fakeStreamConn{header: http.Header{}})
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	clientFn := func(context.Context, connect.Spec) connect.StreamingClientConn { return nil }
	require.NotNil(t, ic.WrapStreamingClient(clientFn))

	// Unary client-side calls pass through untouched.
	called := false
	unary := ic.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	clientReq := connect.NewRequest(&spinneretv1.GetMeRequest{})
	_, err = unary(e.ctx(), clientSpecRequest{clientReq})
	require.NoError(t, err)
	require.True(t, called)
}

// clientSpecRequest reports a client-side spec.
type clientSpecRequest struct {
	*connect.Request[spinneretv1.GetMeRequest]
}

func (r clientSpecRequest) Spec() connect.Spec { return connect.Spec{IsClient: true} }

func TestHTTPMiddleware(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	var seen *authz.Principal
	h := HTTPMiddleware(e.auth, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = authz.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Run("no credentials", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(http.MethodGet, "", "", nil))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, string(apperr.ReasonSessionInvalid), rec.Header().Get(apperr.HeaderReason))
		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "unauthenticated", body["code"])
	})
	t.Run("session with tenant query", func(t *testing.T) {
		cookie := e.login("acme-viewer")
		r := request(http.MethodGet, "", cookie, nil)
		r.URL.RawQuery = "tenant=" + w.tenant
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, w.viewer, seen.ID)
		require.Equal(t, w.tenant, seen.TenantID)
	})
	t.Run("forbidden tenant", func(t *testing.T) {
		cookie := e.login("acme-viewer")
		r := request(http.MethodGet, "", cookie, map[string]string{HeaderTenant: "ten_other"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
	t.Run("rate limited token", func(t *testing.T) {
		plaintext, _ := e.newToken(w, "sse", CreateTokenInput{RateLimitRPS: 1})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request(http.MethodGet, plaintext, "", nil))
		require.Equal(t, http.StatusNoContent, rec.Code)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, request(http.MethodGet, plaintext, "", nil))
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		require.Equal(t, "1", rec.Header().Get("Retry-After"))
	})
}

func TestHTTPStatus(t *testing.T) {
	tests := map[connect.Code]int{
		connect.CodeUnauthenticated:   http.StatusUnauthorized,
		connect.CodePermissionDenied:  http.StatusForbidden,
		connect.CodeResourceExhausted: http.StatusTooManyRequests,
		connect.CodeInvalidArgument:   http.StatusBadRequest,
		connect.CodeNotFound:          http.StatusNotFound,
		connect.CodeUnavailable:       http.StatusServiceUnavailable,
		connect.CodeInternal:          http.StatusInternalServerError,
	}
	for code, status := range tests {
		require.Equal(t, status, httpStatus(code), code.String())
	}
	rec := httptest.NewRecorder()
	writeHTTPError(rec, errors.New("boom"))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// staleSession creates a session for userID that is past half of its TTL, so
// the next authenticated request slides its expiry.
func (e *env) staleSession(userID string) (cookie string, expiresMs int64) {
	e.t.Helper()
	cookie, hash, err := e.auth.sessions.create(e.ctx(), userID, "0", "203.0.113.7", "ua", time.Now().Add(-40*time.Minute))
	require.NoError(e.t, err)
	return cookie, e.sessionExpiry(hash)
}

func (e *env) sessionExpiry(hash string) int64 {
	e.t.Helper()
	v, err := e.rdb.Do(e.ctx(), e.rdb.B().Hget().Key(e.keys.Session(hash)).Field(sessionFieldExpires).Build()).AsInt64()
	require.NoError(e.t, err)
	return v
}

// sessionCookies returns the session cookies set by a response header.
func sessionCookies(t *testing.T, h http.Header) []*http.Cookie {
	t.Helper()
	var out []*http.Cookie
	for _, v := range h.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(v)
		require.NoError(t, err)
		if c.Name == SessionCookieName {
			out = append(out, c)
		}
	}
	return out
}

func TestInterceptorReissuesRefreshedSessionCookie(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	svc := &captureAuthService{}
	client := e.connectServer(svc)
	withCookie := func(req interface{ Header() http.Header }, cookie string) {
		req.Header().Set("Cookie", SessionCookieName+"="+cookie)
		req.Header().Set(HeaderCSRF, "1")
	}

	t.Run("successful call slides expiry and cookie", func(t *testing.T) {
		cookie, before := e.staleSession(w.owner)
		hash, _ := sessionHashOf(cookie)
		req := connect.NewRequest(&spinneretv1.GetMeRequest{})
		withCookie(req, cookie)
		resp, err := client.GetMe(e.ctx(), req)
		require.NoError(t, err)
		cookies := sessionCookies(t, resp.Header())
		require.Len(t, cookies, 1)
		require.Equal(t, cookie, cookies[0].Value)
		require.Equal(t, int(time.Hour/time.Second), cookies[0].MaxAge)
		require.True(t, cookies[0].HttpOnly)
		require.Equal(t, http.SameSiteStrictMode, cookies[0].SameSite)
		require.False(t, cookies[0].Secure, "plain HTTP in auto mode")
		require.Greater(t, e.sessionExpiry(hash), before)

		// Now more than half of the TTL remains: nothing to re-issue.
		resp, err = client.GetMe(e.ctx(), req)
		require.NoError(t, err)
		require.Empty(t, sessionCookies(t, resp.Header()))
	})
	t.Run("failed call leaves the session for the next request", func(t *testing.T) {
		cookie, before := e.staleSession(w.owner)
		hash, _ := sessionHashOf(cookie)
		req := connect.NewRequest(&spinneretv1.ChangePasswordRequest{CurrentPassword: "x", NewPassword: "yyyyyyyyyy"})
		withCookie(req, cookie)
		_, err := client.ChangePassword(e.ctx(), req)
		require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
		require.Equal(t, before, e.sessionExpiry(hash))
	})
	t.Run("handler setting the session cookie wins", func(t *testing.T) {
		cookie, before := e.staleSession(w.owner)
		hash, _ := sessionHashOf(cookie)
		req := connect.NewRequest(&spinneretv1.LogoutRequest{})
		withCookie(req, cookie)
		resp, err := client.Logout(e.ctx(), req)
		require.NoError(t, err)
		cookies := sessionCookies(t, resp.Header())
		require.Len(t, cookies, 1)
		require.Empty(t, cookies[0].Value)
		require.Equal(t, before, e.sessionExpiry(hash))
	})
	t.Run("token calls never set cookies", func(t *testing.T) {
		plaintext, _ := e.newToken(w, "node", CreateTokenInput{})
		req := connect.NewRequest(&spinneretv1.GetMeRequest{})
		req.Header().Set("Authorization", "Bearer "+plaintext)
		resp, err := client.GetMe(e.ctx(), req)
		require.NoError(t, err)
		require.Empty(t, resp.Header().Values("Set-Cookie"))
	})
}

func TestStreamingAndMiddlewareReissueSessionCookie(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")

	t.Run("streaming handler", func(t *testing.T) {
		cookie, before := e.staleSession(w.viewer)
		hash, _ := sessionHashOf(cookie)
		ic := NewInterceptor(e.auth, nil, nil)
		h := ic.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error { return nil })
		header := http.Header{}
		header.Set("Cookie", SessionCookieName+"="+cookie)
		header.Set(HeaderCSRF, "1")
		conn := &fakeStreamConn{header: header}
		require.NoError(t, h(e.ctx(), conn))
		cookies := sessionCookies(t, conn.ResponseHeader())
		require.Len(t, cookies, 1)
		require.Equal(t, cookie, cookies[0].Value)
		require.Greater(t, e.sessionExpiry(hash), before)
	})
	t.Run("HTTP middleware", func(t *testing.T) {
		cookie, before := e.staleSession(w.viewer)
		hash, _ := sessionHashOf(cookie)
		h := HTTPMiddleware(e.auth, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		r := request(http.MethodGet, "", cookie, map[string]string{"X-Forwarded-Proto": "https"})
		r.RemoteAddr = "10.1.2.3:4000" // trusted proxy
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusNoContent, rec.Code)
		cookies := sessionCookies(t, rec.Header())
		require.Len(t, cookies, 1)
		require.True(t, cookies[0].Secure, "TLS terminated by a trusted proxy")
		require.Greater(t, e.sessionExpiry(hash), before)
	})
	t.Run("deleted session is not re-issued", func(t *testing.T) {
		cookie, _ := e.staleSession(w.viewer)
		hash, _ := sessionHashOf(cookie)
		r := request(http.MethodGet, "", cookie, nil)
		res, err := e.auth.authenticate(e.ctx(), r, e.auth.RequestMeta(r), refreshDeferred)
		require.NoError(t, err)
		require.NotNil(t, res.refresh)
		_, err = e.auth.sessions.delete(e.ctx(), hash)
		require.NoError(t, err)
		require.Nil(t, e.auth.refreshSession(e.ctx(), res, false))
		require.Nil(t, e.auth.refreshSession(e.ctx(), nil, false))
	})
	t.Run("Redis failures are logged and skipped", func(t *testing.T) {
		url := os.Getenv(testutil.RedisURLEnv)
		if url == "" {
			t.Skip(testutil.RedisURLEnv + " is not set")
		}
		rdb, err := redis.Open(e.ctx(), url, nil)
		require.NoError(t, err)
		rdb.Close()
		broken := NewAuthenticator(e.cfg, e.pool, rdb, e.keys, nil, slog.New(slog.DiscardHandler))
		res := &authResult{refresh: &sessionRefresh{sess: session{hash: "abc", userID: w.viewer}, cookie: "c"}}
		require.Nil(t, broken.refreshSession(e.ctx(), res, false))
	})
}
