package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestCORSMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	serve := func(h http.Handler, method, origin string, preflight bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/spinneret.v1.AuthService/GetMe", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if preflight {
			r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	t.Run("disabled without origins", func(t *testing.T) {
		h := newCORSMiddleware(nil, next)
		rec := serve(h, http.MethodOptions, "http://localhost:5173", true)
		require.Equal(t, http.StatusTeapot, rec.Code)
		require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})

	h := newCORSMiddleware([]string{"http://LOCALHOST:5173/", " ", "not a url"}, next)
	t.Run("preflight of a listed origin is answered before auth", func(t *testing.T) {
		rec := serve(h, http.MethodOptions, "http://localhost:5173", true)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
		require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
		require.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "X-Spinneret-CSRF")
		require.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Connect-Protocol-Version")
		require.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "POST")
		require.Contains(t, rec.Header().Values("Vary"), "Origin")
	})
	t.Run("preflight of another origin is rejected", func(t *testing.T) {
		rec := serve(h, http.MethodOptions, "https://evil.example", true)
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
	t.Run("simple request of a listed origin exposes headers", func(t *testing.T) {
		rec := serve(h, http.MethodPost, "http://localhost:5173", false)
		require.Equal(t, http.StatusTeapot, rec.Code)
		require.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
		require.Contains(t, rec.Header().Get("Access-Control-Expose-Headers"), "Spinneret-Reason")
		require.Contains(t, rec.Header().Get("Access-Control-Expose-Headers"), "Spinneret-Retry-After-Ms")
	})
	t.Run("unlisted origin gets no CORS headers", func(t *testing.T) {
		rec := serve(h, http.MethodPost, "https://evil.example", false)
		require.Equal(t, http.StatusTeapot, rec.Code)
		require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	})
	t.Run("requests without origin pass through", func(t *testing.T) {
		rec := serve(h, http.MethodOptions, "", true)
		require.Equal(t, http.StatusTeapot, rec.Code)
	})
	t.Run("wildcard allows any origin without credentials", func(t *testing.T) {
		w := newCORSMiddleware([]string{"*"}, next)
		rec := serve(w, http.MethodOptions, "https://any.example", true)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
		require.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))
	})
}

type fakeUnaryRequest struct {
	connect.AnyRequest
	spec connect.Spec
	msg  any
}

func (r fakeUnaryRequest) Spec() connect.Spec { return r.spec }
func (r fakeUnaryRequest) Any() any           { return r.msg }

func TestDeadlineInterceptor(t *testing.T) {
	d := newDeadlineInterceptor()
	remaining := func(procedure string, msg any) time.Duration {
		var got time.Duration
		_, err := d.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			got = time.Until(deadline)
			return nil, nil
		})(context.Background(), fakeUnaryRequest{spec: connect.Spec{Procedure: procedure}, msg: msg})
		require.NoError(t, err)
		return got
	}
	approx := func(t *testing.T, want, got time.Duration) {
		t.Helper()
		require.InDelta(t, want.Seconds(), got.Seconds(), 1)
	}
	approx(t, 60*time.Second, remaining(spinneretv1connect.LeaseServiceAcquireProcedure, &spinneretv1.AcquireRequest{}))
	approx(t, 5*time.Minute, remaining(spinneretv1connect.IdentityAdminServiceImportIdentitiesProcedure, &spinneretv1.ImportIdentitiesRequest{}))
	approx(t, 5*time.Minute, remaining(spinneretv1connect.ProxyAdminServiceImportProxiesProcedure, &spinneretv1.ImportProxiesRequest{}))
	approx(t, 5*time.Minute, remaining(spinneretv1connect.IdentityAdminServiceUpdateIdentityTypeProcedure, &spinneretv1.UpdateIdentityTypeRequest{}))
	approx(t, 5*time.Minute, remaining(spinneretv1connect.IdentityAdminServiceBulkOperateIdentitiesProcedure, &spinneretv1.BulkOperateIdentitiesRequest{}))
	approx(t, 5*time.Minute, remaining(spinneretv1connect.SiteAdminServiceDeleteSiteProcedure, &spinneretv1.DeleteSiteRequest{}))
	approx(t, 60*time.Second, remaining(spinneretv1connect.SiteAdminServiceGetSiteProcedure, &spinneretv1.GetSiteRequest{}))
	approx(t, 25*time.Second, remaining(spinneretv1connect.ConfigServiceWatchConfigProcedure, &spinneretv1.WatchConfigRequest{TimeoutMs: 15_000}))
	approx(t, 40*time.Second, remaining(spinneretv1connect.ConfigServiceWatchConfigProcedure, &spinneretv1.WatchConfigRequest{}))
	approx(t, 70*time.Second, remaining(spinneretv1connect.ConfigServiceWatchConfigProcedure, &spinneretv1.WatchConfigRequest{TimeoutMs: 600_000}))

	// A shorter client deadline wins.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := d.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		deadline, _ := ctx.Deadline()
		require.LessOrEqual(t, time.Until(deadline), time.Second)
		return nil, nil
	})(ctx, fakeUnaryRequest{spec: connect.Spec{Procedure: "/x"}})
	require.NoError(t, err)

	// Client-side calls are not bounded.
	_, err = d.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		_, ok := ctx.Deadline()
		require.False(t, ok)
		return nil, nil
	})(context.Background(), fakeUnaryRequest{spec: connect.Spec{Procedure: "/x", IsClient: true}})
	require.NoError(t, err)
}

func TestErrorInterceptorAddsReasonToValidationErrors(t *testing.T) {
	i := newErrorInterceptor(discardLogger())
	req := fakeRequest{spec: connect.Spec{Procedure: "/spinneret.v1.LeaseService/Acquire"}}
	_, err := i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("validation error: site: value is required"))
	})(context.Background(), req)
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	require.Equal(t, "invalid_argument", ce.Meta().Get(apperr.HeaderReason))

	_, err = i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, apperr.InvalidArgument(apperr.ReasonURIInvalid, "bad uri")
	})(context.Background(), req)
	require.ErrorAs(t, err, &ce)
	require.Equal(t, "uri_invalid", ce.Meta().Get(apperr.HeaderReason), "application reasons are kept")
}

func TestDrainMiddlewareCancelsLongRequests(t *testing.T) {
	drain, startDrain := context.WithCancel(context.Background())
	entered := make(chan struct{})
	finished := make(chan error, 1)
	h := drainMiddleware{drain: drain, next: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			finished <- r.Context().Err()
		case <-time.After(5 * time.Second):
			finished <- nil
		}
	})}
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil))
	<-entered
	startDrain()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("request was not canceled by the drain")
	}

	// Requests started after the drain are canceled immediately.
	var canceled bool
	h.next = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { canceled = r.Context().Err() != nil })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	require.True(t, canceled)
}
