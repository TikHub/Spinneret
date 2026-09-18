package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/sdk/go/spinneret"
)

// fakeSpinneret serves the node API calls the example makes.
type fakeSpinneret struct {
	spinneretv1connect.UnimplementedLeaseServiceHandler
	spinneretv1connect.UnimplementedReportServiceHandler
	spinneretv1connect.UnimplementedConfigServiceHandler

	mu       sync.Mutex
	acquires int
	reports  []*spinneretv1.Report
	releases int
	failNext error
}

func (f *fakeSpinneret) Acquire(context.Context, *connect.Request[spinneretv1.AcquireRequest]) (*connect.Response[spinneretv1.AcquireResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	if err := f.failNext; err != nil {
		f.failNext = nil
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.AcquireResponse{
		Lease:      &spinneretv1.Lease{LeaseId: "lse_example", IdentityId: "idt_1", ExpiresAt: timestamppb.Now()},
		Credential: &spinneretv1.Credential{Cookies: map[string]string{"sessionid": "abc"}},
	}), nil
}

func (f *fakeSpinneret) Release(context.Context, *connect.Request[spinneretv1.ReleaseRequest]) (*connect.Response[spinneretv1.ReleaseResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return connect.NewResponse(&spinneretv1.ReleaseResponse{Released: true}), nil
}

func (f *fakeSpinneret) Report(_ context.Context, req *connect.Request[spinneretv1.ReportRequest]) (*connect.Response[spinneretv1.ReportResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req.Msg.GetReports()...)
	return connect.NewResponse(&spinneretv1.ReportResponse{Accepted: int32(len(req.Msg.GetReports()))}), nil
}

func (f *fakeSpinneret) BatchGetConfig(_ context.Context, req *connect.Request[spinneretv1.BatchGetConfigRequest]) (*connect.Response[spinneretv1.BatchGetConfigResponse], error) {
	ref := req.Msg.GetItems()[0]
	return connect.NewResponse(&spinneretv1.BatchGetConfigResponse{Items: []*spinneretv1.ConfigItem{{
		Group: ref.GetGroup(), Key: ref.GetKey(), Version: 1, Content: `{"requests":2}`,
	}}}), nil
}

func (f *fakeSpinneret) WatchConfig(ctx context.Context, _ *connect.Request[spinneretv1.WatchConfigRequest]) (*connect.Response[spinneretv1.WatchConfigResponse], error) {
	<-ctx.Done()
	return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
}

func startFake(t *testing.T, f *fakeSpinneret) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(spinneretv1connect.NewLeaseServiceHandler(f))
	mux.Handle(spinneretv1connect.NewReportServiceHandler(f))
	mux.Handle(spinneretv1connect.NewConfigServiceHandler(f))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRunCrawlsAndReports(t *testing.T) {
	f := &fakeSpinneret{}
	var cookies []string
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(target.Close)
	t.Setenv(spinneret.EnvURL, startFake(t, f))
	t.Setenv(spinneret.EnvToken, "spn_example")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	cfg := config{site: "shop", client: "web", target: target.URL + "/api/v1/feed?cursor=0", requests: 2, configID: "crawler/search.json"}
	require.NoError(t, run(cfg, slog.New(slog.DiscardHandler)))

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, 2, f.acquires)
	require.Len(t, f.reports, 2)
	for _, r := range f.reports {
		require.True(t, r.GetRelease())
		require.Equal(t, "/api/v1/feed", r.GetUri())
		require.Equal(t, []string{"empty_list"}, r.GetMarkers())
		require.Equal(t, int32(200), r.GetHttpStatus())
	}
	mu.Lock()
	require.Equal(t, []string{"sessionid=abc", "sessionid=abc"}, cookies)
	mu.Unlock()
}

func TestRunBacksOffAndFails(t *testing.T) {
	f := &fakeSpinneret{}
	url := startFake(t, f)
	t.Setenv(spinneret.EnvURL, url)
	t.Setenv(spinneret.EnvToken, "spn_example")
	logger := slog.New(slog.DiscardHandler)

	// Capacity errors are waited out.
	busy := connect.NewError(connect.CodeResourceExhausted, errors.New("none"))
	busy.Meta().Set(spinneret.HeaderReason, spinneret.ReasonNoIdentityAvailable)
	busy.Meta().Set(spinneret.HeaderRetryAfterMs, "5")
	f.failNext = busy
	unreachable := "http://127.0.0.1:1/"
	require.NoError(t, run(config{site: "s", client: "c", target: unreachable, requests: 1}, logger))

	// Pauses are waited out too (bounded by the retry hint).
	paused := connect.NewError(connect.CodeUnavailable, errors.New("paused"))
	paused.Meta().Set(spinneret.HeaderReason, spinneret.ReasonSitePaused)
	paused.Meta().Set(spinneret.HeaderRetryAfterMs, "5")
	f.mu.Lock()
	f.failNext = paused
	f.mu.Unlock()
	require.NoError(t, run(config{site: "s", client: "c", target: unreachable, requests: 1}, logger))

	// Other errors stop the node.
	f.mu.Lock()
	f.failNext = connect.NewError(connect.CodePermissionDenied, errors.New("scope"))
	f.mu.Unlock()
	require.Error(t, run(config{site: "s", client: "c", target: unreachable, requests: 1}, logger))

	// Transport failures of the target are reported with their error kind.
	require.NoError(t, run(config{site: "s", client: "c", target: unreachable, requests: 1}, logger))
	f.mu.Lock()
	last := f.reports[len(f.reports)-1]
	f.mu.Unlock()
	require.Equal(t, spinneret.ErrorKindConnRefused, last.GetErrorKind())

	// Invalid configuration.
	t.Setenv(spinneret.EnvToken, "")
	require.Error(t, run(config{requests: 1}, logger))
	t.Setenv(spinneret.EnvToken, "spn_example")
	require.Error(t, run(config{requests: 1, configID: "no-slash"}, logger))
}

func TestHelpers(t *testing.T) {
	require.Equal(t, []string{"captcha_page", "empty_list"}, detectMarkers([]byte(`{"captcha":1,"data":null}`)))
	require.Empty(t, detectMarkers(nil))
	require.Equal(t, time.Second, waitFor(errors.New("x"), time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	sleep(ctx, time.Hour)
	require.Less(t, time.Since(start), time.Second)
	sleep(context.Background(), time.Millisecond)
}
