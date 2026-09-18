package spinneret

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

func TestLeaseReportsAndReleasesWithLastReport(t *testing.T) {
	for _, tc := range protocolCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			srv := newFakeServer(t, f, nil)
			c := newTestClient(t, srv.URL, func(o *Options) { o.UseGRPC = tc.useGRPC })
			ctx := context.Background()

			// The target site sees the leased credential.
			var seen http.Header
			var seenQuery string
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Header.Clone()
				seenQuery = r.URL.RawQuery
				_, _ = io.WriteString(w, `{"data":[]}`)
			}))
			defer target.Close()

			lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/api/v1/feed?cursor=0"})
			require.NoError(t, err)
			require.Equal(t, "lse_1", lease.ID())
			require.Equal(t, "idt_1", lease.IdentityID())
			require.True(t, lease.Info().GetSticky())
			require.Equal(t, "residential", lease.Proxy().GetKind())
			require.Equal(t, int32(30_000), lease.Hints().GetRenewBeforeMs())
			require.Equal(t, "/api/v1/feed?cursor=0", lease.URI())
			require.NotNil(t, lease.Response())
			require.Equal(t, time.Unix(1_900_000_000, 0).UTC(), lease.ExpiresAt())

			for page := range 2 {
				req := newRequest(t, target.URL+"/api/v1/feed?cursor="+string(rune('0'+page)))
				lease.Apply(req)
				started := time.Now()
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				_, _ = io.Copy(io.Discard, resp.Body)
				require.NoError(t, resp.Body.Close())
				require.NoError(t, lease.ReportResponse(resp, ReportInput{StartedAt: started, Markers: []string{"empty_list"}}))
			}
			require.Equal(t, "sessionid=a1b2c3", seen.Get("Cookie"))
			require.Equal(t, "Mozilla/5.0", seen.Get("User-Agent"))
			require.Contains(t, seenQuery, "csrf_token=x9y8z7")

			require.NoError(t, lease.Close(ctx))
			require.True(t, lease.Released())
			require.NoError(t, lease.Close(ctx), "close is idempotent")
			require.NoError(t, c.Reporter().Flush(ctx))

			reports := f.receivedReports()
			require.Len(t, reports, 2)
			require.False(t, reports[0].GetRelease())
			require.True(t, reports[1].GetRelease(), "the last report releases the lease")
			for _, r := range reports {
				require.Equal(t, "lse_1", r.GetLeaseId())
				require.Equal(t, "/api/v1/feed", r.GetUri(), "query removed")
				require.Equal(t, "GET", r.GetMethod())
				require.Equal(t, int32(200), r.GetHttpStatus())
				require.Equal(t, []string{"empty_list"}, r.GetMarkers())
				require.Equal(t, int64(len(`{"data":[]}`)), r.GetResponseBytes())
			}
			require.Empty(t, f.releasedLeases(), "no Release RPC when a report released the lease")

			err = lease.Report(ReportInput{HTTPStatus: 200})
			require.True(t, IsLeaseGone(err))
			_, err = lease.Renew(ctx, time.Minute)
			require.True(t, IsLeaseGone(err))
		})
	}
}

func TestLeaseReleaseRPCWhenNothingReported(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()

	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.NoError(t, lease.Close(ctx))
	require.Equal(t, []string{"lse_1"}, f.releasedLeases())

	// Released=false (already ended) is not an error.
	f.set(func(f *fakeNode) {
		f.release = func(context.Context, *ReleaseRequest) (*ReleaseResponse, error) { return &ReleaseResponse{}, nil }
	})
	lease, err = c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.NoError(t, lease.Close(ctx))

	// Reasons saying the lease has ended are swallowed.
	f.set(func(f *fakeNode) {
		f.release = func(context.Context, *ReleaseRequest) (*ReleaseResponse, error) {
			return nil, apiError(connect.CodeFailedPrecondition, ReasonLeaseExpired, 0, "expired")
		}
	})
	lease, err = c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.NoError(t, lease.Close(ctx))

	// Other failures are returned.
	f.set(func(f *fakeNode) {
		f.release = func(context.Context, *ReleaseRequest) (*ReleaseResponse, error) {
			return nil, apiError(connect.CodePermissionDenied, ReasonScopeMissing, 0, "scope")
		}
	})
	lease, err = c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.True(t, IsPermissionDenied(lease.Close(ctx)))
}

func TestLeaseReportWithRelease(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()

	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/a"})
	require.NoError(t, err)
	require.NoError(t, lease.Report(ReportInput{HTTPStatus: 429}))
	require.NoError(t, lease.Report(ReportInput{HTTPStatus: 200, Release: true}))
	require.True(t, lease.Released())
	require.True(t, IsLeaseGone(lease.Report(ReportInput{HTTPStatus: 200})))
	require.NoError(t, lease.Close(ctx))
	require.NoError(t, c.Reporter().Flush(ctx))
	reports := f.receivedReports()
	require.Len(t, reports, 2)
	require.Equal(t, int32(429), reports[0].GetHttpStatus())
	require.True(t, reports[1].GetRelease())
	require.Empty(t, f.releasedLeases())

	// Invalid input is rejected synchronously and does not release.
	lease, err = c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.ErrorContains(t, lease.Report(ReportInput{HTTPStatus: 200}), "uri is required")
	require.ErrorContains(t, lease.Report(ReportInput{URI: "/x", ErrorKind: "bogus"}), "error_kind")
	require.False(t, lease.Released())
	require.NoError(t, lease.Close(ctx))
}

func TestLeaseReportResponseAndError(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()
	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/default"})
	require.NoError(t, err)

	require.ErrorContains(t, lease.ReportResponse(nil, ReportInput{}), "response must not be nil")

	u, _ := url.Parse("https://x.example/proxy/path?token=secret")
	proxyResp := &http.Response{
		StatusCode:    http.StatusProxyAuthRequired,
		ContentLength: -1,
		Request:       &http.Request{Method: http.MethodPost, URL: u},
	}
	require.NoError(t, lease.ReportResponse(proxyResp, ReportInput{Latency: 15 * time.Millisecond, BusinessCode: "0"}))

	urlErr := &url.Error{Op: "Get", URL: "https://x.example/err/path?sig=1", Err: context.DeadlineExceeded}
	require.NoError(t, lease.ReportError(urlErr, ReportInput{Latency: time.Second}))
	require.NoError(t, lease.ReportError(errors.New("mystery"), ReportInput{}))
	require.NoError(t, lease.ReportError(nil, ReportInput{ErrorKind: ErrorKindDNS}))
	require.NoError(t, lease.Close(ctx))
	require.NoError(t, c.Reporter().Flush(ctx))

	reports := f.receivedReports()
	require.Len(t, reports, 4)
	require.Equal(t, int32(407), reports[0].GetHttpStatus())
	require.Equal(t, ErrorKindProxyAuth, reports[0].GetErrorKind())
	require.Equal(t, "POST", reports[0].GetMethod())
	require.Equal(t, "/proxy/path", reports[0].GetUri())
	require.Equal(t, int32(15), reports[0].GetLatencyMs())
	require.Zero(t, reports[0].GetResponseBytes())
	require.Equal(t, "0", reports[0].GetBusinessCode())

	require.Equal(t, ErrorKindTimeout, reports[1].GetErrorKind())
	require.Equal(t, "GET", reports[1].GetMethod())
	require.Equal(t, "/err/path", reports[1].GetUri())
	require.Zero(t, reports[1].GetHttpStatus())

	require.Equal(t, ErrorKindOther, reports[2].GetErrorKind())
	require.Equal(t, "/default", reports[2].GetUri())
	require.Equal(t, ErrorKindDNS, reports[3].GetErrorKind())
	require.True(t, reports[3].GetRelease())
}

func TestLeaseRenew(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()
	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.NoError(t, err)

	expires, err := lease.Renew(ctx, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, time.Unix(2_000_000_000, 0).UTC(), expires)
	require.Equal(t, expires, lease.ExpiresAt())
	f.mu.Lock()
	require.Equal(t, int32(120_000), f.renewals[0].GetExtendMs())
	f.mu.Unlock()

	// A response without expiry keeps the known one.
	f.set(func(f *fakeNode) {
		f.renew = func(context.Context, *RenewRequest) (*RenewResponse, error) { return &RenewResponse{}, nil }
	})
	expires, err = lease.Renew(ctx, -time.Second)
	require.NoError(t, err)
	require.Equal(t, time.Unix(2_000_000_000, 0).UTC(), expires)

	f.set(func(f *fakeNode) {
		f.renew = func(context.Context, *RenewRequest) (*RenewResponse, error) {
			return nil, apiError(connect.CodeFailedPrecondition, ReasonLeaseLifetimeExceeded, 0, "cap")
		}
	})
	_, err = lease.Renew(ctx, time.Minute)
	require.True(t, IsLeaseGone(err))
	require.NoError(t, lease.Close(ctx))

	noExpiry, err := c.NewLease(&AcquireResponse{Lease: &LeaseInfo{LeaseId: "lse_x"}}, "")
	require.NoError(t, err)
	require.True(t, noExpiry.ExpiresAt().IsZero())
}

func TestLeaseAcquireErrorsAndBatch(t *testing.T) {
	f := newFakeNode()
	f.acquire = func(context.Context, *AcquireRequest) (*AcquireResponse, error) {
		return nil, apiError(connect.CodeUnavailable, ReasonCircuitOpen, 30_000, "open")
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()

	_, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web"})
	require.True(t, IsCircuitOpen(err))
	require.Equal(t, 30*time.Second, RetryAfterOf(err))

	leases, err := c.LeaseBatch(ctx, &AcquireBatchRequest{Site: "shop", Client: "web", Count: 3, Uri: "/b"})
	require.NoError(t, err)
	require.Len(t, leases, 3)
	for _, l := range leases {
		require.Equal(t, "/b", l.URI())
		require.NoError(t, l.Close(ctx))
	}
	require.Len(t, f.releasedLeases(), 3)

	f.set(func(f *fakeNode) {
		f.acquireBatch = func(context.Context, *AcquireBatchRequest) (*AcquireBatchResponse, error) {
			return &AcquireBatchResponse{Leases: []*AcquireResponse{testAcquireResponse("lse_ok", "s"), {}}}, nil
		}
	})
	_, err = c.LeaseBatch(ctx, &AcquireBatchRequest{Site: "shop", Client: "web", Count: 2})
	require.Equal(t, connect.CodeInternal, CodeOf(err))
	require.Contains(t, f.releasedLeases(), "lse_ok", "leases of a failed batch are released")

	f.set(func(f *fakeNode) {
		f.acquireBatch = func(context.Context, *AcquireBatchRequest) (*AcquireBatchResponse, error) {
			return nil, apiError(connect.CodeResourceExhausted, ReasonNoIdentityAvailable, 10, "none")
		}
	})
	_, err = c.LeaseBatch(ctx, &AcquireBatchRequest{Site: "shop", Client: "web", Count: 2})
	require.True(t, IsNoIdentity(err))

	_, err = c.NewLease(nil, "")
	require.Equal(t, connect.CodeInternal, CodeOf(err))
}

func TestLeaseTransport(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", nil)
	lease, err := c.NewLease(testAcquireResponse("lse_1", "s"), "/")
	require.NoError(t, err)
	proxyURL, err := lease.ProxyURL()
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:1", proxyURL.Host)

	transport, err := lease.Transport(nil)
	require.NoError(t, err)
	got, err := transport.Proxy(newRequest(t, "https://example.com/"))
	require.NoError(t, err)
	require.Equal(t, proxyURL.String(), got.String())

	direct, err := c.NewLease(&AcquireResponse{Lease: &LeaseInfo{LeaseId: "lse_2"}}, "/")
	require.NoError(t, err)
	base := &http.Transport{Proxy: nil}
	transport, err = direct.Transport(base)
	require.NoError(t, err)
	require.Nil(t, transport.Proxy)
	require.NotSame(t, base, transport)

	bad, err := c.NewLease(&AcquireResponse{Lease: &LeaseInfo{LeaseId: "lse_3"}, Proxy: &ProxyAssignment{Url: "::bad"}}, "/")
	require.NoError(t, err)
	_, err = bad.Transport(nil)
	require.Error(t, err)
}

// When the reporter is closed, Close delivers the releasing report directly.
func TestLeaseCloseAfterReporterClosed(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()
	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/a"})
	require.NoError(t, err)
	require.NoError(t, lease.Report(ReportInput{HTTPStatus: 200}))
	require.NoError(t, c.Reporter().Close(ctx))
	require.Equal(t, ReasonReporterClosed, ReasonOf(lease.Report(ReportInput{HTTPStatus: 200})))
	require.NoError(t, lease.Close(ctx))
	reports := f.receivedReports()
	require.Len(t, reports, 1)
	require.True(t, reports[0].GetRelease())

	// Direct delivery surfaces rejections and failures.
	lease2, err := c.NewLease(testAcquireResponse("lse_2", "s"), "/a")
	require.NoError(t, err)
	lease2.pending = &Report{ReportId: "r", LeaseId: "lse_2", Uri: "/a"}
	f.set(func(f *fakeNode) {
		f.report = func(context.Context, *ReportRequest) (*ReportResponse, error) {
			return &ReportResponse{Rejected: []*RejectedReport{{ReportId: "r", Reason: ReasonLeaseUnknown, Message: "gone"}}}, nil
		}
	})
	require.True(t, IsLeaseGone(lease2.Close(ctx)))

	lease3, err := c.NewLease(testAcquireResponse("lse_3", "s"), "/a")
	require.NoError(t, err)
	lease3.pending = &Report{ReportId: "r3", LeaseId: "lse_3", Uri: "/a"}
	f.set(func(f *fakeNode) {
		f.report = func(context.Context, *ReportRequest) (*ReportResponse, error) {
			return nil, apiError(connect.CodeInvalidArgument, ReasonInvalidArgument, 0, "bad")
		}
	})
	require.Equal(t, connect.CodeInvalidArgument, CodeOf(lease3.Close(ctx)))
	require.Equal(t, 3, f.callCount(spinneretv1connect.ReportServiceReportProcedure))
}

func TestLeaseConcurrentReports(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx := context.Background()
	lease, err := c.Lease(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/c"})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 25 {
				if err := lease.Report(ReportInput{HTTPStatus: 200}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	require.NoError(t, lease.Close(ctx))
	require.NoError(t, c.Reporter().Flush(ctx))
	reports := f.receivedReports()
	require.Len(t, reports, 400)
	releases := 0
	for _, r := range reports {
		if r.GetRelease() {
			releases++
		}
	}
	require.Equal(t, 1, releases)
	require.True(t, reports[len(reports)-1].GetRelease())
}
