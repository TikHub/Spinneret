package spinneret

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

// Client calls the Spinneret node API. It is safe for concurrent use; create
// one per process and close it on shutdown to deliver queued reports.
type Client struct {
	s       settings
	leases  spinneretv1connect.LeaseServiceClient
	reports spinneretv1connect.ReportServiceClient
	configs spinneretv1connect.ConfigServiceClient
	secrets spinneretv1connect.SecretServiceClient
	rnd     func() float64

	closed atomic.Bool

	mu       sync.Mutex
	reporter *Reporter
	watchers map[*ConfigWatcher]struct{}
}

// New creates a client. It does not contact the server.
func New(opts Options) (*Client, error) {
	s, err := opts.resolve(os.Getenv)
	if err != nil {
		return nil, err
	}
	c := &Client{s: s, rnd: jitter, watchers: make(map[*ConfigWatcher]struct{})}
	clientOpts := []connect.ClientOption{
		connect.WithInterceptors(&headerInterceptor{
			authorization: "Bearer " + s.token,
			node:          s.node,
			userAgent:     s.userAgent,
		}),
	}
	if s.useGRPC {
		clientOpts = append(clientOpts, connect.WithGRPC())
	} else {
		clientOpts = append(clientOpts, connect.WithCodec(newJSONCodec()))
	}
	c.leases = spinneretv1connect.NewLeaseServiceClient(s.httpClient, s.baseURL, clientOpts...)
	c.reports = spinneretv1connect.NewReportServiceClient(s.httpClient, s.baseURL, clientOpts...)
	c.configs = spinneretv1connect.NewConfigServiceClient(s.httpClient, s.baseURL, clientOpts...)
	c.secrets = spinneretv1connect.NewSecretServiceClient(s.httpClient, s.baseURL, clientOpts...)
	return c, nil
}

// BaseURL returns the normalized server URL.
func (c *Client) BaseURL() string { return c.s.baseURL }

// Node returns the node name sent as X-Spinneret-Node.
func (c *Client) Node() string { return c.s.node }

// Logger returns the logger of the client.
func (c *Client) Logger() *slog.Logger { return c.s.logger }

// Closed reports whether Close has been called.
func (c *Client) Closed() bool { return c.closed.Load() }

// LeaseService returns the underlying generated client (authenticated, no
// retries, no error conversion).
func (c *Client) LeaseService() spinneretv1connect.LeaseServiceClient { return c.leases }

// ReportService returns the underlying generated client.
func (c *Client) ReportService() spinneretv1connect.ReportServiceClient { return c.reports }

// ConfigService returns the underlying generated client.
func (c *Client) ConfigService() spinneretv1connect.ConfigServiceClient { return c.configs }

// SecretService returns the underlying generated client.
func (c *Client) SecretService() spinneretv1connect.SecretServiceClient { return c.secrets }

// Acquire leases one identity (and proxy) for a request. See [Client.Lease]
// for a helper that also reports and releases.
//
// Typical errors: [IsNoIdentity] and [IsNoProxy] (wait [RetryAfterOf] and
// retry), [IsCircuitOpen] and [IsSitePaused] (pause the endpoint group or site).
func (c *Client) Acquire(ctx context.Context, req *AcquireRequest) (*AcquireResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.LeaseServiceAcquireProcedure)
	}
	return invoke(ctx, c, call{
		procedure: spinneretv1connect.LeaseServiceAcquireProcedure,
		timeout:   c.s.timeout + waitDuration(req.GetWaitMs()),
	}, req, c.leases.Acquire)
}

// AcquireBatch leases up to req.Count (1..50) distinct identities. Fewer
// leases are returned when fewer identities are available.
func (c *Client) AcquireBatch(ctx context.Context, req *AcquireBatchRequest) (*AcquireBatchResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.LeaseServiceAcquireBatchProcedure)
	}
	return invoke(ctx, c, call{
		procedure: spinneretv1connect.LeaseServiceAcquireBatchProcedure,
		timeout:   c.s.timeout + waitDuration(req.GetWaitMs()),
	}, req, c.leases.AcquireBatch)
}

// Renew extends a lease (extend_ms 0 uses the policy lease TTL).
func (c *Client) Renew(ctx context.Context, req *RenewRequest) (*RenewResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.LeaseServiceRenewProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.LeaseServiceRenewProcedure,
		timeout:    c.s.timeout,
		idempotent: true,
	}, req, c.leases.Renew)
}

// Release ends a lease (idempotent: Released is false when it had already ended).
func (c *Client) Release(ctx context.Context, req *ReleaseRequest) (*ReleaseResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.LeaseServiceReleaseProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.LeaseServiceReleaseProcedure,
		timeout:    c.s.timeout,
		idempotent: true,
	}, req, c.leases.Release)
}

// Report sends 1..500 reports synchronously. Use [Client.Reporter] or
// [Lease.Report] for batched background delivery. Invalid reports are listed
// in ReportResponse.Rejected instead of failing the call.
func (c *Client) Report(ctx context.Context, req *ReportRequest) (*ReportResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.ReportServiceReportProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.ReportServiceReportProcedure,
		timeout:    c.s.timeout,
		idempotent: true, // deduplicated by report_id
	}, req, c.reports.Report)
}

// sendReports is the delivery function of the background reporter: no
// retries (the reporter retries with its own backoff) and allowed while the
// client is closing.
func (c *Client) sendReports(ctx context.Context, reports []*Report) (*ReportResponse, error) {
	return invoke(ctx, c, call{
		procedure:   spinneretv1connect.ReportServiceReportProcedure,
		timeout:     c.s.timeout,
		idempotent:  true,
		noRetry:     true,
		allowClosed: true,
	}, &ReportRequest{Reports: reports}, c.reports.Report)
}

// GetConfig fetches the published version of one config item (not_found when
// it does not exist or is unpublished).
func (c *Client) GetConfig(ctx context.Context, req *GetConfigRequest) (*GetConfigResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.ConfigServiceGetConfigProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.ConfigServiceGetConfigProcedure,
		timeout:    c.s.timeout,
		idempotent: true,
	}, req, c.configs.GetConfig)
}

// BatchGetConfig fetches several config items; unknown ones are listed in Missing.
func (c *Client) BatchGetConfig(ctx context.Context, req *BatchGetConfigRequest) (*BatchGetConfigResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.ConfigServiceBatchGetConfigProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.ConfigServiceBatchGetConfigProcedure,
		timeout:    c.s.timeout,
		idempotent: true,
	}, req, c.configs.BatchGetConfig)
}

// WatchConfig long-polls until a watched item differs from the given version
// or timeout_ms (0 = 30s, max 60s) elapses; it returns the changed items
// (empty on timeout). Prefer [Client.NewConfigWatcher] for a managed loop.
func (c *Client) WatchConfig(ctx context.Context, req *WatchConfigRequest) (*WatchConfigResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.ConfigServiceWatchConfigProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.ConfigServiceWatchConfigProcedure,
		timeout:    watchTimeout(req.GetTimeoutMs()),
		idempotent: true,
	}, req, c.configs.WatchConfig)
}

// watchOnce is the long poll of the config watcher (no retries: the watcher
// retries with its own backoff).
func (c *Client) watchOnce(ctx context.Context, req *WatchConfigRequest) (*WatchConfigResponse, error) {
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.ConfigServiceWatchConfigProcedure,
		timeout:    watchTimeout(req.GetTimeoutMs()),
		idempotent: true,
		noRetry:    true,
	}, req, c.configs.WatchConfig)
}

// GetSecret reads a secret value (version 0 reads the current version).
func (c *Client) GetSecret(ctx context.Context, req *GetSecretRequest) (*GetSecretResponse, error) {
	if req == nil {
		return nil, errNilRequest(spinneretv1connect.SecretServiceGetSecretProcedure)
	}
	return invoke(ctx, c, call{
		procedure:  spinneretv1connect.SecretServiceGetSecretProcedure,
		timeout:    c.s.timeout,
		idempotent: true,
	}, req, c.secrets.GetSecret)
}

// Reporter returns the background reporter of the client, created on first use.
func (c *Client) Reporter() *Reporter {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reporter == nil {
		// Options were validated by New, so the reporter cannot fail here.
		c.reporter = newReporter(c.sendReports, c.s.reporter, c.s.logger)
		if c.closed.Load() {
			c.reporter.closeNow()
		}
	}
	return c.reporter
}

// Close stops the config watchers, delivers the queued reports and releases
// idle connections of the default HTTP client. Report delivery is bounded by
// ctx, or by ReporterOptions.CloseTimeout when ctx has no deadline. Later
// calls fail with reason client_closed. Close is idempotent.
func (c *Client) Close(ctx context.Context) error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	c.mu.Lock()
	reporter := c.reporter
	watchers := make([]*ConfigWatcher, 0, len(c.watchers))
	for w := range c.watchers {
		watchers = append(watchers, w)
	}
	c.watchers = map[*ConfigWatcher]struct{}{}
	c.mu.Unlock()
	for _, w := range watchers {
		w.Stop()
	}
	var err error
	if reporter != nil {
		err = reporter.Close(ctx)
	}
	if c.s.transport != nil {
		c.s.transport.CloseIdleConnections()
	}
	return err
}

func (c *Client) forgetWatcher(w *ConfigWatcher) {
	c.mu.Lock()
	delete(c.watchers, w)
	c.mu.Unlock()
}

// deadlineSlack is how much longer than the client-side timeout the deadline
// sent to the server is.
const deadlineSlack = time.Second

// call describes one unary invocation.
type call struct {
	procedure   string
	timeout     time.Duration
	idempotent  bool
	noRetry     bool
	allowClosed bool
}

// invoke runs a generated unary method with per-attempt timeouts, retries
// and error conversion.
func invoke[Req, Res any](
	ctx context.Context,
	c *Client,
	spec call,
	msg *Req,
	method func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
) (*Res, error) {
	if !spec.allowClosed && c.closed.Load() {
		return nil, &Error{
			Code:      connect.CodeFailedPrecondition,
			Reason:    ReasonClientClosed,
			Message:   "the client has been closed",
			Procedure: spec.procedure,
		}
	}
	policy := c.s.retry
	if spec.noRetry {
		policy.MaxRetries = 0
	}
	for attempt := 0; ; attempt++ {
		resp, err := attemptCall(ctx, spec, msg, method)
		if err == nil {
			return resp, nil
		}
		if attempt >= policy.MaxRetries || ctx.Err() != nil || !shouldRetryCall(err, spec.idempotent) {
			return nil, err
		}
		delay, ok := policy.delay(attempt, err.RetryAfter, c.rnd)
		if !ok {
			return nil, err
		}
		c.s.logger.DebugContext(ctx, "spinneret retrying call",
			slog.String("procedure", spec.procedure),
			slog.Int("attempt", attempt+2),
			slog.Duration("delay", delay),
			slog.String("code", err.Code.String()),
			slog.String("reason", err.Reason))
		if !sleepContext(ctx, delay) {
			return nil, err
		}
	}
}

func attemptCall[Req, Res any](
	ctx context.Context,
	spec call,
	msg *Req,
	method func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error),
) (*Res, *Error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	if spec.timeout > 0 {
		// Connect sends the deadline of the context to the server. Giving the
		// server deadlineSlack more than the client-side timeout keeps the
		// classification of an expired attempt deterministic: the client
		// always gives up first, instead of sometimes racing a
		// deadline_exceeded answer caused by its own deadline.
		var cancelDeadline context.CancelFunc
		callCtx, cancelDeadline = context.WithTimeout(callCtx, spec.timeout+deadlineSlack)
		defer cancelDeadline()
		timer := time.AfterFunc(spec.timeout, func() {
			timedOut.Store(true)
			cancel()
		})
		defer timer.Stop()
	}
	resp, err := method(callCtx, connect.NewRequest(msg))
	if err != nil {
		perCallTimeout := ctx.Err() == nil &&
			(timedOut.Load() || errors.Is(callCtx.Err(), context.DeadlineExceeded))
		return nil, fromCallError(spec.procedure, err, perCallTimeout)
	}
	if resp == nil || resp.Msg == nil {
		return nil, &Error{
			Code:      connect.CodeInternal,
			Reason:    ReasonInternal,
			Message:   "empty response",
			Procedure: spec.procedure,
		}
	}
	return resp.Msg, nil
}

func errNilRequest(procedure string) *Error {
	return &Error{
		Code:      connect.CodeInvalidArgument,
		Reason:    ReasonInvalidArgument,
		Message:   "request must not be nil",
		Procedure: procedure,
	}
}

func waitDuration(waitMs int32) time.Duration {
	return time.Duration(max(waitMs, 0)) * time.Millisecond
}

func watchTimeout(timeoutMs int32) time.Duration {
	wait := waitDuration(timeoutMs)
	if wait == 0 {
		wait = DefaultWatchTimeoutMs * time.Millisecond
	}
	return wait + WatchGrace
}

// headerInterceptor adds the authentication, node and user agent headers to
// every outgoing request.
type headerInterceptor struct {
	authorization string
	node          string
	userAgent     string
}

func (h *headerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			h.apply(req.Header())
		}
		return next(ctx, req)
	}
}

func (h *headerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		h.apply(conn.RequestHeader())
		return conn
	}
}

func (h *headerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (h *headerInterceptor) apply(header http.Header) {
	header.Set("Authorization", h.authorization)
	header.Set(HeaderNode, h.node)
	header.Set("User-Agent", h.userAgent)
}
