package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/observability"
)

func TestJSONCodecUsesProtoNamesAndZeroValues(t *testing.T) {
	c := newJSONCodec()
	require.Equal(t, "json", c.Name())
	b, err := c.Marshal(&spinneretv1.Lease{LeaseId: "lse_1", ExpiresAt: timestamppb.New(time.Unix(0, 0))})
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, "lse_1", m["lease_id"])
	require.Contains(t, m, "sticky")

	out := &spinneretv1.Lease{}
	require.NoError(t, c.Unmarshal([]byte(`{"lease_id":"x","unknown_field":1}`), out))
	require.Equal(t, "x", out.GetLeaseId())

	appended, err := c.MarshalAppend([]byte("prefix"), &spinneretv1.Lease{})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(appended), "prefix{"))

	_, err = c.Marshal("not a message")
	require.Error(t, err)
	_, err = c.MarshalAppend(nil, 1)
	require.Error(t, err)
	require.Error(t, c.Unmarshal([]byte(`{}`), "nope"))
	require.Error(t, c.Unmarshal(nil, &spinneretv1.Lease{}))
}

type fakeRequest struct {
	connect.AnyRequest
	spec connect.Spec
}

func (r fakeRequest) Spec() connect.Spec { return r.spec }

func TestErrorInterceptorConvertsAndRecovers(t *testing.T) {
	i := newErrorInterceptor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := fakeRequest{spec: connect.Spec{Procedure: "/spinneret.v1.LeaseService/Acquire"}}

	_, err := i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, apperr.ResourceExhausted(apperr.ReasonNoIdentityAvailable, 1200, "no identity")
	})(context.Background(), req)
	var ce *connect.Error
	require.ErrorAs(t, err, &ce)
	require.Equal(t, connect.CodeResourceExhausted, ce.Code())
	require.Equal(t, "no_identity_available", ce.Meta().Get(apperr.HeaderReason))
	require.Equal(t, "1200", ce.Meta().Get(apperr.HeaderRetryAfter))

	_, err = i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errors.New("database exploded")
	})(authz.WithPrincipal(context.Background(), authz.System("test")), req)
	require.ErrorAs(t, err, &ce)
	require.Equal(t, connect.CodeInternal, ce.Code())
	require.NotContains(t, ce.Message(), "exploded")

	_, err = i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		panic("boom")
	})(context.Background(), req)
	require.ErrorAs(t, err, &ce)
	require.Equal(t, connect.CodeInternal, ce.Code())

	resp, err := i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return connect.NewResponse(&spinneretv1.Lease{}), nil
	})(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestMetricsInterceptorCountsRequests(t *testing.T) {
	m := observability.NewMetrics()
	i := newMetricsInterceptor(m)
	req := fakeRequest{spec: connect.Spec{Procedure: "/p"}}
	_, _ = i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("x"))
	})(context.Background(), req)
	_, _ = i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})(context.Background(), req)
	families, err := m.Registry.Gather()
	require.NoError(t, err)
	var found int
	for _, f := range families {
		if f.GetName() == "spinneret_http_requests_total" {
			for _, metric := range f.GetMetric() {
				found += int(metric.GetCounter().GetValue())
			}
		}
	}
	require.Equal(t, 2, found)
	require.NotPanics(t, func() { newMetricsInterceptor(nil).observe("/p", nil, time.Millisecond) })
}

func TestStaticHandlerServesAssetsAndFallsBack(t *testing.T) {
	files := fstest.MapFS{
		"index.html":       {Data: []byte("<html><script>document.documentElement.dataset.theme='dark'</script>console<script type=\"module\" src=\"/assets/app-1.js\"></script></html>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/nested/.x": {Data: []byte("")},
	}
	h := newStaticHandler(files)

	get := func(p string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec
	}
	rec := get("/assets/app-1.js")
	require.Equal(t, http.StatusOK, rec.Code)
	csp := rec.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "script-src 'self' 'sha256-")
	require.Equal(t, 1, strings.Count(csp, "'sha256-"))
	require.Contains(t, rec.Header().Get("Cache-Control"), "immutable")
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	rec = get("/identities/idt_123")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "console")
	require.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))

	require.Equal(t, http.StatusNotFound, get("/assets/missing.js").Code)
	require.Equal(t, http.StatusOK, get("/favicon.svg").Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	empty := newStaticHandler(fstest.MapFS{})
	rec = httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHealthHandlers(t *testing.T) {
	h := newHealthHandlers([]ReadinessCheck{
		{Name: "ok", Check: func(context.Context) error { return nil }},
	})
	rec := httptest.NewRecorder()
	h.liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	failing := newHealthHandlers([]ReadinessCheck{
		{Name: "ok", Check: func(context.Context) error { return nil }},
		{Name: "redis", Check: func(context.Context) error { return errors.New("down") }},
	})
	rec = httptest.NewRecorder()
	failing.readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "down")

	h.setDraining()
	rec = httptest.NewRecorder()
	h.readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "draining")
}

func TestSSEHandlerFiltersByPermission(t *testing.T) {
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "prod")
	s1 := catalogtest.AddSite(ns, "sit_a", "alpha", 1)
	catalogtest.AddSite(ns, "sit_b", "beta", 2)
	cat := catalogtest.New(ns)
	bus := events.NewMemoryBus()
	h := newSSEHandler(cat, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.heartbeat = 50 * time.Millisecond

	// Operator restricted to site alpha.
	p := &authz.Principal{
		Kind: authz.KindUser, ID: "usr_1", Name: "op", TenantID: "ten_1",
		Bindings: []authz.Binding{{ID: "rb_1", TenantID: "ten_1", Role: authz.RoleOperator, NamespaceID: "ns_1", SiteIDs: []string{s1.ID}}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(authz.WithPrincipal(r.Context(), p)))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream?namespace=prod", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "retry: 5000\n", line)

	publish := func(typ, siteID string) {
		require.NoError(t, bus.Publish(ctx, events.NamespaceChannel("ns_1"), events.Event{Type: typ, NamespaceID: "ns_1", SiteID: siteID, At: time.Now(), Data: json.RawMessage(`{}`)}))
	}
	// Wait until the subscription is registered (first heartbeat proves the loop is running).
	for {
		l, err := reader.ReadString('\n')
		require.NoError(t, err)
		if strings.HasPrefix(l, ": ping") {
			break
		}
	}
	publish("breaker.transition", "sit_b") // other site: filtered
	publish("unknown.type", "sit_a")       // unknown type: filtered
	publish("breaker.transition", "sit_a") // visible
	for {
		l, err := reader.ReadString('\n')
		require.NoError(t, err)
		if strings.HasPrefix(l, "event: ") {
			require.Equal(t, "event: breaker.transition\n", l)
			data, err := reader.ReadString('\n')
			require.NoError(t, err)
			require.Contains(t, data, `"site_id":"sit_a"`)
			break
		}
	}
}

func TestSSEHandlerRejectsBadRequests(t *testing.T) {
	cat := catalogtest.New(catalogtest.NewNamespace("ten_1", "ns_1", "prod"))
	h := newSSEHandler(cat, events.NewMemoryBus(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	do := func(method, target string, p *authz.Principal) int {
		r := httptest.NewRequest(method, target, nil)
		if p != nil {
			r = r.WithContext(authz.WithPrincipal(r.Context(), p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	viewer := &authz.Principal{Kind: authz.KindUser, ID: "usr_1", TenantID: "ten_1",
		Bindings: []authz.Binding{{TenantID: "ten_1", Role: authz.RoleViewer}}}
	require.Equal(t, http.StatusMethodNotAllowed, do(http.MethodPost, "/s?namespace=prod", viewer))
	require.Equal(t, http.StatusUnauthorized, do(http.MethodGet, "/s?namespace=prod", nil))
	require.Equal(t, http.StatusBadRequest, do(http.MethodGet, "/s", viewer))
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/s?namespace=missing", viewer))

	h.maxConns = 0
	require.Equal(t, http.StatusServiceUnavailable, do(http.MethodGet, "/s?namespace=prod", viewer))
}
