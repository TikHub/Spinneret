package spinneret

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

const testToken = "spn_test_token"

// fakeNode implements the four node services with overridable behaviors and
// records what it received.
type fakeNode struct {
	mu        sync.Mutex
	calls     map[string]int
	headers   map[string]http.Header
	reports   []*Report
	releases  []string
	renewals  []*RenewRequest
	watches   []*WatchConfigRequest
	protocols map[string]string // procedure -> content type of the last request

	acquire        func(context.Context, *AcquireRequest) (*AcquireResponse, error)
	acquireBatch   func(context.Context, *AcquireBatchRequest) (*AcquireBatchResponse, error)
	renew          func(context.Context, *RenewRequest) (*RenewResponse, error)
	release        func(context.Context, *ReleaseRequest) (*ReleaseResponse, error)
	report         func(context.Context, *ReportRequest) (*ReportResponse, error)
	getConfig      func(context.Context, *GetConfigRequest) (*GetConfigResponse, error)
	batchGetConfig func(context.Context, *BatchGetConfigRequest) (*BatchGetConfigResponse, error)
	watchConfig    func(context.Context, *WatchConfigRequest) (*WatchConfigResponse, error)
	getSecret      func(context.Context, *GetSecretRequest) (*GetSecretResponse, error)
}

func newFakeNode() *fakeNode {
	return &fakeNode{
		calls:     map[string]int{},
		headers:   map[string]http.Header{},
		protocols: map[string]string{},
	}
}

// set runs fn under the lock (to change behaviors while a client is running).
func (f *fakeNode) set(fn func(f *fakeNode)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeNode) record(procedure string, h http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[procedure]++
	f.headers[procedure] = h.Clone()
	f.protocols[procedure] = h.Get("Content-Type")
}

func (f *fakeNode) callCount(procedure string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[procedure]
}

func (f *fakeNode) lastHeader(procedure string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headers[procedure]
}

func (f *fakeNode) receivedReports() []*Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*Report, len(f.reports))
	copy(out, f.reports)
	return out
}

func (f *fakeNode) releasedLeases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.releases...)
}

func handlerOf[Req, Res any](f *fakeNode, fn func() func(context.Context, *Req) (*Res, error), def func(*Req) (*Res, error)) func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
	return func(ctx context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
		f.record(req.Spec().Procedure, req.Header())
		f.mu.Lock()
		custom := fn()
		f.mu.Unlock()
		var (
			res *Res
			err error
		)
		if custom != nil {
			res, err = custom(ctx, req.Msg)
		} else {
			res, err = def(req.Msg)
		}
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(res), nil
	}
}

func (f *fakeNode) Acquire(ctx context.Context, req *connect.Request[AcquireRequest]) (*connect.Response[AcquireResponse], error) {
	return handlerOf(f, func() func(context.Context, *AcquireRequest) (*AcquireResponse, error) { return f.acquire },
		func(r *AcquireRequest) (*AcquireResponse, error) {
			return testAcquireResponse("lse_1", r.GetSite()), nil
		})(ctx, req)
}

func (f *fakeNode) AcquireBatch(ctx context.Context, req *connect.Request[AcquireBatchRequest]) (*connect.Response[AcquireBatchResponse], error) {
	return handlerOf(f, func() func(context.Context, *AcquireBatchRequest) (*AcquireBatchResponse, error) {
		return f.acquireBatch
	},
		func(r *AcquireBatchRequest) (*AcquireBatchResponse, error) {
			out := &AcquireBatchResponse{Requested: r.GetCount()}
			for i := range r.GetCount() {
				out.Leases = append(out.Leases, testAcquireResponse("lse_b"+strconv.Itoa(int(i)), r.GetSite()))
			}
			return out, nil
		})(ctx, req)
}

func (f *fakeNode) Renew(ctx context.Context, req *connect.Request[RenewRequest]) (*connect.Response[RenewResponse], error) {
	f.mu.Lock()
	f.renewals = append(f.renewals, proto.CloneOf(req.Msg))
	f.mu.Unlock()
	return handlerOf(f, func() func(context.Context, *RenewRequest) (*RenewResponse, error) { return f.renew },
		func(r *RenewRequest) (*RenewResponse, error) {
			return &RenewResponse{ExpiresAt: timestamppb.New(time.Unix(2_000_000_000, 0))}, nil
		})(ctx, req)
}

func (f *fakeNode) Release(ctx context.Context, req *connect.Request[ReleaseRequest]) (*connect.Response[ReleaseResponse], error) {
	f.mu.Lock()
	f.releases = append(f.releases, req.Msg.GetLeaseId())
	f.mu.Unlock()
	return handlerOf(f, func() func(context.Context, *ReleaseRequest) (*ReleaseResponse, error) { return f.release },
		func(*ReleaseRequest) (*ReleaseResponse, error) { return &ReleaseResponse{Released: true}, nil })(ctx, req)
}

func (f *fakeNode) Report(ctx context.Context, req *connect.Request[ReportRequest]) (*connect.Response[ReportResponse], error) {
	return handlerOf(f, func() func(context.Context, *ReportRequest) (*ReportResponse, error) { return f.report },
		func(r *ReportRequest) (*ReportResponse, error) {
			f.mu.Lock()
			f.reports = append(f.reports, r.GetReports()...)
			f.mu.Unlock()
			return &ReportResponse{Accepted: int32(len(r.GetReports()))}, nil
		})(ctx, req)
}

func (f *fakeNode) GetConfig(ctx context.Context, req *connect.Request[GetConfigRequest]) (*connect.Response[GetConfigResponse], error) {
	return handlerOf(f, func() func(context.Context, *GetConfigRequest) (*GetConfigResponse, error) { return f.getConfig },
		func(r *GetConfigRequest) (*GetConfigResponse, error) {
			return &GetConfigResponse{Item: testConfigItem(r.GetGroup(), r.GetKey(), 1, "{}")}, nil
		})(ctx, req)
}

func (f *fakeNode) BatchGetConfig(ctx context.Context, req *connect.Request[BatchGetConfigRequest]) (*connect.Response[BatchGetConfigResponse], error) {
	return handlerOf(f, func() func(context.Context, *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
		return f.batchGetConfig
	}, func(r *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
		out := &BatchGetConfigResponse{}
		for _, ref := range r.GetItems() {
			out.Items = append(out.Items, testConfigItem(ref.GetGroup(), ref.GetKey(), 1, "{}"))
		}
		return out, nil
	})(ctx, req)
}

func (f *fakeNode) WatchConfig(ctx context.Context, req *connect.Request[WatchConfigRequest]) (*connect.Response[WatchConfigResponse], error) {
	f.mu.Lock()
	f.watches = append(f.watches, proto.CloneOf(req.Msg))
	f.mu.Unlock()
	return handlerOf(f, func() func(context.Context, *WatchConfigRequest) (*WatchConfigResponse, error) { return f.watchConfig },
		func(r *WatchConfigRequest) (*WatchConfigResponse, error) {
			wait := time.Duration(r.GetTimeoutMs()) * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
			case <-time.After(wait):
				return &WatchConfigResponse{}, nil
			}
		})(ctx, req)
}

func (f *fakeNode) GetSecret(ctx context.Context, req *connect.Request[GetSecretRequest]) (*connect.Response[GetSecretResponse], error) {
	return handlerOf(f, func() func(context.Context, *GetSecretRequest) (*GetSecretResponse, error) { return f.getSecret },
		func(r *GetSecretRequest) (*GetSecretResponse, error) {
			return &GetSecretResponse{Path: r.GetPath(), Version: 3, Value: "s3cr3t"}, nil
		})(ctx, req)
}

// serverCodec mirrors the JSON codec of the Spinneret server.
func serverCodec() connect.Codec {
	return &jsonCodec{
		marshal:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
		unmarshal: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// newFakeServer serves the fake over HTTP/1.1 and h2c. wrap may decorate the handler.
func newFakeServer(t testing.TB, f *fakeNode, wrap func(http.Handler) http.Handler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	opts := []connect.HandlerOption{connect.WithCodec(serverCodec())}
	mux.Handle(spinneretv1connect.NewLeaseServiceHandler(f, opts...))
	mux.Handle(spinneretv1connect.NewReportServiceHandler(f, opts...))
	mux.Handle(spinneretv1connect.NewConfigServiceHandler(f, opts...))
	mux.Handle(spinneretv1connect.NewSecretServiceHandler(f, opts...))
	var handler http.Handler = mux
	if wrap != nil {
		handler = wrap(mux)
	}
	srv := httptest.NewUnstartedServer(handler)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = protocols
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newTestClient creates a client for srv with fast retries.
func newTestClient(t testing.TB, baseURL string, mutate func(*Options)) *Client {
	t.Helper()
	opts := Options{
		BaseURL: baseURL,
		Token:   testToken,
		Node:    "node-test",
		Timeout: 2 * time.Second,
		Retry: &RetryPolicy{
			MaxRetries:     2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     5 * time.Millisecond,
			MaxRetryAfter:  time.Second,
		},
		Reporter: ReporterOptions{
			FlushInterval:  10 * time.Millisecond,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     10 * time.Millisecond,
			CloseTimeout:   2 * time.Second,
		},
		Logger: discardLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	c, err := New(opts)
	require.NoError(t, err)
	c.rnd = func() float64 { return 0.5 }
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func testAcquireResponse(leaseID, site string) *AcquireResponse {
	return &AcquireResponse{
		Lease: &LeaseInfo{
			LeaseId:       leaseID,
			IdentityId:    "idt_1",
			IdentityType:  site + "_cookie",
			EndpointGroup: "search",
			ExpiresAt:     timestamppb.New(time.Unix(1_900_000_000, 0)),
			Sticky:        true,
		},
		Credential: &Credential{
			Cookies:      map[string]string{"sessionid": "a1b2c3"},
			CookieHeader: "sessionid=a1b2c3",
			Headers:      map[string]string{"User-Agent": "Mozilla/5.0"},
			Query:        map[string]string{"csrf_token": "x9y8z7"},
		},
		Proxy: &ProxyAssignment{ProxyId: "pxy_1", Url: "http://user:pass@127.0.0.1:1", Kind: "residential", Region: "US"},
		Hints: &Hints{RenewBeforeMs: 30_000},
	}
}

func testConfigItem(group, key string, version int32, content string) *ConfigItem {
	return &ConfigItem{
		Namespace: "default",
		Group:     group,
		Key:       key,
		Format:    "json",
		Version:   version,
		Content:   content,
		UpdatedAt: timestamppb.New(time.Unix(1_800_000_000, 0)),
	}
}

// apiError builds a server error carrying the Spinneret metadata headers.
func apiError(code connect.Code, reason string, retryAfterMs int64, message string) error {
	e := connect.NewError(code, errors.New(message))
	if reason != "" {
		e.Meta().Set(HeaderReason, reason)
	}
	if retryAfterMs > 0 {
		e.Meta().Set(HeaderRetryAfterMs, strconv.FormatInt(retryAfterMs, 10))
	}
	return e
}

// countingHTTPClient counts the requests sent through it.
type countingHTTPClient struct {
	mu    sync.Mutex
	n     int
	inner connect.HTTPClient
}

func (c *countingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.inner.Do(req)
}

func (c *countingHTTPClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// eventually polls cond until it holds or the timeout expires.
func eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %s", timeout, msg)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
