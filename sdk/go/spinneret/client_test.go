package spinneret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

var protocolCases = []struct {
	name        string
	useGRPC     bool
	contentType string
}{
	{name: "connect-json", useGRPC: false, contentType: "application/json"},
	{name: "grpc", useGRPC: true, contentType: "application/grpc"},
}

func TestClientCallsEveryMethod(t *testing.T) {
	for _, tc := range protocolCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			srv := newFakeServer(t, f, nil)
			c := newTestClient(t, srv.URL+"/", func(o *Options) { o.UseGRPC = tc.useGRPC })
			ctx := context.Background()

			acq, err := c.Acquire(ctx, &AcquireRequest{Site: "shop", Client: "web", Uri: "/api/v1/feed", WaitMs: 10})
			require.NoError(t, err)
			require.Equal(t, "lse_1", acq.GetLease().GetLeaseId())
			require.Equal(t, "a1b2c3", acq.GetCredential().GetCookies()["sessionid"])

			batch, err := c.AcquireBatch(ctx, &AcquireBatchRequest{Site: "shop", Client: "web", Count: 2})
			require.NoError(t, err)
			require.Len(t, batch.GetLeases(), 2)

			renewed, err := c.Renew(ctx, &RenewRequest{LeaseId: "lse_1", ExtendMs: 1000})
			require.NoError(t, err)
			require.Equal(t, int64(2_000_000_000), renewed.GetExpiresAt().GetSeconds())

			released, err := c.Release(ctx, &ReleaseRequest{LeaseId: "lse_1"})
			require.NoError(t, err)
			require.True(t, released.GetReleased())

			rep, err := c.Report(ctx, &ReportRequest{Reports: []*Report{{ReportId: "r1", LeaseId: "lse_1", Uri: "/"}}})
			require.NoError(t, err)
			require.Equal(t, int32(1), rep.GetAccepted())

			item, err := c.GetConfig(ctx, &GetConfigRequest{Group: "crawler", Key: "search.json"})
			require.NoError(t, err)
			require.Equal(t, "search.json", item.GetItem().GetKey())

			items, err := c.BatchGetConfig(ctx, &BatchGetConfigRequest{Items: []*ConfigRef{{Group: "a", Key: "b"}}})
			require.NoError(t, err)
			require.Len(t, items.GetItems(), 1)

			watched, err := c.WatchConfig(ctx, &WatchConfigRequest{Items: []*WatchItem{{Group: "a", Key: "b"}}, TimeoutMs: 1})
			require.NoError(t, err)
			require.Empty(t, watched.GetItems())

			secret, err := c.GetSecret(ctx, &GetSecretRequest{Path: "signing/api_key"})
			require.NoError(t, err)
			require.Equal(t, "s3cr3t", secret.GetValue())

			header := f.lastHeader(spinneretv1connect.LeaseServiceAcquireProcedure)
			require.Equal(t, "Bearer "+testToken, header.Get("Authorization"))
			require.Equal(t, "node-test", header.Get(HeaderNode))
			require.Contains(t, header.Get("User-Agent"), "spinneret-go/"+Version)
			require.True(t, strings.HasPrefix(header.Get("Content-Type"), tc.contentType),
				"content type %q", header.Get("Content-Type"))
			require.Equal(t, "https://x", strings.TrimSuffix("https://x/", "/"))
			require.Equal(t, srv.URL, c.BaseURL())
			require.Equal(t, "node-test", c.Node())
			require.NotNil(t, c.Logger())
			require.NotNil(t, c.LeaseService())
			require.NotNil(t, c.ReportService())
			require.NotNil(t, c.ConfigService())
			require.NotNil(t, c.SecretService())
		})
	}
}

func TestClientNilRequests(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", nil)
	ctx := context.Background()
	checks := []func() error{
		func() error { _, err := c.Acquire(ctx, nil); return err },
		func() error { _, err := c.AcquireBatch(ctx, nil); return err },
		func() error { _, err := c.Renew(ctx, nil); return err },
		func() error { _, err := c.Release(ctx, nil); return err },
		func() error { _, err := c.Report(ctx, nil); return err },
		func() error { _, err := c.GetConfig(ctx, nil); return err },
		func() error { _, err := c.BatchGetConfig(ctx, nil); return err },
		func() error { _, err := c.WatchConfig(ctx, nil); return err },
		func() error { _, err := c.GetSecret(ctx, nil); return err },
	}
	for _, check := range checks {
		err := check()
		require.Equal(t, connect.CodeInvalidArgument, CodeOf(err))
		require.Equal(t, ReasonInvalidArgument, ReasonOf(err))
	}
}

func TestClientErrorMetadata(t *testing.T) {
	for _, tc := range protocolCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			f.acquire = func(context.Context, *AcquireRequest) (*AcquireResponse, error) {
				return nil, apiError(connect.CodeResourceExhausted, ReasonNoIdentityAvailable, 1200, "no identity available for shop/web/search")
			}
			srv := newFakeServer(t, f, nil)
			c := newTestClient(t, srv.URL, func(o *Options) { o.UseGRPC = tc.useGRPC })

			_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "shop", Client: "web"})
			require.Error(t, err)
			e, ok := AsError(err)
			require.True(t, ok)
			require.Equal(t, connect.CodeResourceExhausted, e.Code)
			require.Equal(t, ReasonNoIdentityAvailable, e.Reason)
			require.Equal(t, 1200*time.Millisecond, e.RetryAfter)
			require.Equal(t, "no identity available for shop/web/search", e.Message)
			require.Equal(t, spinneretv1connect.LeaseServiceAcquireProcedure, e.Procedure)
			require.True(t, e.FromServer())
			require.False(t, e.Transport())
			require.True(t, IsNoIdentity(err))
			require.False(t, IsCircuitOpen(err))
			require.Equal(t, 1200*time.Millisecond, RetryAfterOf(err))
			require.Equal(t, 1, f.callCount(spinneretv1connect.LeaseServiceAcquireProcedure), "resource_exhausted is not retried")
			require.Contains(t, err.Error(), "resource_exhausted (no_identity_available)")
		})
	}
}

func TestClientRetries(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		call      func(*Client) error
		procedure string
		wantCalls int
	}{
		{
			name: "renew retries unavailable",
			err:  apiError(connect.CodeUnavailable, ReasonRebuilding, 1, "rebuilding"),
			call: func(c *Client) error {
				_, err := c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
				return err
			},
			procedure: spinneretv1connect.LeaseServiceRenewProcedure,
			wantCalls: 3,
		},
		{
			name: "acquire retries a server unavailable answer",
			err:  apiError(connect.CodeUnavailable, ReasonRebuilding, 0, "rebuilding"),
			call: func(c *Client) error {
				_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
				return err
			},
			procedure: spinneretv1connect.LeaseServiceAcquireProcedure,
			wantCalls: 3,
		},
		{
			name: "circuit open is never retried",
			err:  apiError(connect.CodeUnavailable, ReasonCircuitOpen, 30_000, "open"),
			call: func(c *Client) error {
				_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
				return err
			},
			procedure: spinneretv1connect.LeaseServiceAcquireProcedure,
			wantCalls: 1,
		},
		{
			name: "site paused is never retried",
			err:  apiError(connect.CodeUnavailable, ReasonSitePaused, 0, "paused"),
			call: func(c *Client) error {
				_, err := c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
				return err
			},
			procedure: spinneretv1connect.LeaseServiceRenewProcedure,
			wantCalls: 1,
		},
		{
			name: "retry hint above the limit is returned",
			err:  apiError(connect.CodeUnavailable, ReasonRebuilding, 60_000, "rebuilding"),
			call: func(c *Client) error {
				_, err := c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
				return err
			},
			procedure: spinneretv1connect.LeaseServiceRenewProcedure,
			wantCalls: 1,
		},
		{
			name: "permission denied is not retried",
			err:  apiError(connect.CodePermissionDenied, ReasonScopeMissing, 0, "scope"),
			call: func(c *Client) error {
				_, err := c.GetSecret(context.Background(), &GetSecretRequest{Path: "p"})
				return err
			},
			procedure: spinneretv1connect.SecretServiceGetSecretProcedure,
			wantCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			fail := func() error { return tc.err }
			f.renew = func(context.Context, *RenewRequest) (*RenewResponse, error) { return nil, fail() }
			f.acquire = func(context.Context, *AcquireRequest) (*AcquireResponse, error) { return nil, fail() }
			f.getSecret = func(context.Context, *GetSecretRequest) (*GetSecretResponse, error) { return nil, fail() }
			srv := newFakeServer(t, f, nil)
			c := newTestClient(t, srv.URL, nil)
			require.Error(t, tc.call(c))
			require.Equal(t, tc.wantCalls, f.callCount(tc.procedure))
		})
	}
}

func TestClientRetrySucceeds(t *testing.T) {
	f := newFakeNode()
	var attempts atomic.Int32
	f.renew = func(context.Context, *RenewRequest) (*RenewResponse, error) {
		if attempts.Add(1) < 3 {
			return nil, apiError(connect.CodeUnavailable, "", 0, "try again")
		}
		return &RenewResponse{}, nil
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	_, err := c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
	require.NoError(t, err)
	require.Equal(t, int32(3), attempts.Load())
}

// A load balancer error page (no Connect body) is retried only for idempotent calls.
func TestClientLoadBalancerErrorPage(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>bad gateway</html>")
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL, nil)

	_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
	e, ok := AsError(err)
	require.True(t, ok)
	require.Equal(t, connect.CodeUnavailable, e.Code)
	require.False(t, e.FromServer())
	require.False(t, e.Transport())
	require.Empty(t, e.Reason)
	require.Equal(t, int32(1), hits.Load())

	_, err = c.Release(context.Background(), &ReleaseRequest{LeaseId: "l"})
	require.Error(t, err)
	require.Equal(t, int32(4), hits.Load())
}

func TestClientTransportErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	counter := &countingHTTPClient{inner: &http.Client{Transport: newTransport(false)}}
	c := newTestClient(t, "http://"+addr, func(o *Options) { o.HTTPClient = counter })

	_, err = c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
	e, ok := AsError(err)
	require.True(t, ok)
	require.True(t, e.Transport())
	require.True(t, IsTransport(err))
	require.Equal(t, ReasonTransport, e.Reason)
	require.Equal(t, ErrorKindConnRefused, e.ErrorKind)
	require.True(t, IsRetryable(err))
	require.Equal(t, 3, counter.count(), "connection refused happens before sending, so acquire is retried")
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr)
}

func TestClientPerCallTimeout(t *testing.T) {
	f := newFakeNode()
	slow := func(ctx context.Context) error {
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("slow"))
	}
	f.acquire = func(ctx context.Context, _ *AcquireRequest) (*AcquireResponse, error) { return nil, slow(ctx) }
	f.renew = func(ctx context.Context, _ *RenewRequest) (*RenewResponse, error) { return nil, slow(ctx) }
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, func(o *Options) { o.Timeout = 50 * time.Millisecond })

	_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
	e, ok := AsError(err)
	require.True(t, ok)
	require.True(t, e.Transport(), "error: %v", err)
	require.Equal(t, ErrorKindTimeout, e.ErrorKind)
	require.Equal(t, 1, f.callCount(spinneretv1connect.LeaseServiceAcquireProcedure), "ambiguous acquire failures are not retried")

	_, err = c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
	require.True(t, IsTransport(err))
	require.Equal(t, 3, f.callCount(spinneretv1connect.LeaseServiceRenewProcedure))
}

// An attempt that runs into the SDK timeout is always classified as a
// transport timeout: the deadline sent to the server is later, so the server
// cannot answer deadline_exceeded first (regression test).
func TestClientPerCallTimeoutIsDeterministic(t *testing.T) {
	for _, tc := range protocolCases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNode()
			f.renew = func(ctx context.Context, _ *RenewRequest) (*RenewResponse, error) {
				<-ctx.Done() // the server-side deadline or the cancelled request
				return nil, connect.NewError(connect.CodeDeadlineExceeded, ctx.Err())
			}
			srv := newFakeServer(t, f, nil)
			const timeout = 60 * time.Millisecond
			c := newTestClient(t, srv.URL, func(o *Options) {
				o.UseGRPC = tc.useGRPC
				o.Timeout = timeout
				o.Retry = NoRetry()
			})
			for range 5 {
				_, err := c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
				e, ok := AsError(err)
				require.True(t, ok)
				require.True(t, e.Transport(), "error: %v", err)
				require.Equal(t, ErrorKindTimeout, e.ErrorKind)
				require.Equal(t, connect.CodeDeadlineExceeded, e.Code)
				require.False(t, e.FromServer())
				require.True(t, IsRetryable(err))
			}
			header := f.lastHeader(spinneretv1connect.LeaseServiceRenewProcedure)
			raw := header.Get("Connect-Timeout-Ms") + "m"
			if tc.useGRPC {
				raw = header.Get("Grpc-Timeout") // e.g. "1059985u"
			}
			sent, err := parseProtocolTimeout(raw)
			require.NoError(t, err, "timeout header %q", raw)
			require.Greater(t, sent, timeout, "the server deadline must outlive the client timeout")
		})
	}
}

// parseProtocolTimeout parses a Connect or gRPC timeout header value.
func parseProtocolTimeout(raw string) (time.Duration, error) {
	if len(raw) < 2 {
		return 0, fmt.Errorf("invalid timeout %q", raw)
	}
	value, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil {
		return 0, err
	}
	units := map[byte]time.Duration{
		'n': time.Nanosecond, 'u': time.Microsecond, 'm': time.Millisecond,
		'S': time.Second, 'M': time.Minute, 'H': time.Hour,
	}
	unit, ok := units[raw[len(raw)-1]]
	if !ok {
		return 0, fmt.Errorf("unknown unit in %q", raw)
	}
	return time.Duration(value) * unit, nil
}

func TestClientCallerContext(t *testing.T) {
	f := newFakeNode()
	started := make(chan struct{})
	f.renew = func(ctx context.Context, _ *RenewRequest) (*RenewResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
	}
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := c.Renew(ctx, &RenewRequest{LeaseId: "l"})
	require.Equal(t, connect.CodeCanceled, CodeOf(err))
	require.False(t, IsTransport(err))
	require.Equal(t, 1, f.callCount(spinneretv1connect.LeaseServiceRenewProcedure))

	// A canceled context stops the retry wait too.
	f.set(func(f *fakeNode) {
		f.renew = func(context.Context, *RenewRequest) (*RenewResponse, error) {
			return nil, apiError(connect.CodeUnavailable, "", 900, "later")
		}
	})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	_, err = c.Renew(ctx2, &RenewRequest{LeaseId: "l"})
	require.Equal(t, connect.CodeUnavailable, CodeOf(err))
	require.Equal(t, 2, f.callCount(spinneretv1connect.LeaseServiceRenewProcedure))
}

func TestClientIgnoresUnknownResponseFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"released": true, "added_in_a_newer_server": {"x": 1}}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL, nil)
	resp, err := c.Release(context.Background(), &ReleaseRequest{LeaseId: "l"})
	require.NoError(t, err)
	require.True(t, resp.GetReleased())
}

func TestClientEmptyAndInvalidResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/Release") {
			return // empty body decodes to the zero message
		}
		_, _ = io.WriteString(w, `{not json`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv.URL, nil)
	resp, err := c.Release(context.Background(), &ReleaseRequest{LeaseId: "l"})
	require.NoError(t, err)
	require.False(t, resp.GetReleased())

	_, err = c.Renew(context.Background(), &RenewRequest{LeaseId: "l"})
	require.Error(t, err)
	require.False(t, IsTransport(err))
}

func TestClientClose(t *testing.T) {
	f := newFakeNode()
	srv := newFakeServer(t, f, nil)
	c := newTestClient(t, srv.URL, nil)
	require.NoError(t, c.Reporter().Submit(&Report{LeaseId: "l", Uri: "/"}))
	require.NoError(t, c.Close(context.Background()))
	require.True(t, c.Closed())
	require.NoError(t, c.Close(context.Background()), "close is idempotent")
	require.Len(t, f.receivedReports(), 1, "close delivers queued reports")

	_, err := c.Acquire(context.Background(), &AcquireRequest{Site: "s", Client: "c"})
	require.Equal(t, ReasonClientClosed, ReasonOf(err))
	require.Equal(t, connect.CodeFailedPrecondition, CodeOf(err))

	_, err = c.NewConfigWatcher(WatcherOptions{Items: []ConfigKey{{Group: "a", Key: "b"}}})
	require.Equal(t, ReasonClientClosed, ReasonOf(err))

	// A reporter created after Close is closed.
	c2 := newTestClient(t, srv.URL, nil)
	require.NoError(t, c2.Close(context.Background()))
	require.True(t, c2.Reporter().Closed())
	require.Equal(t, ReasonReporterClosed, ReasonOf(c2.Reporter().Submit(&Report{})))
}

func TestWatchTimeout(t *testing.T) {
	require.Equal(t, 30*time.Second+WatchGrace, watchTimeout(0))
	require.Equal(t, 10*time.Second+WatchGrace, watchTimeout(10_000))
	require.Equal(t, time.Duration(0), waitDuration(-5))
}
