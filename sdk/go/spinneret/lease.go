package spinneret

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
)

// Lease is an acquired lease with helpers to use its credential and proxy,
// report the requests made with it and release it. It is safe for concurrent
// use.
//
// Reports are queued on the client's background [Reporter]. The most recent
// report is held back until the next report or Close, so that it can carry
// release=true: Close marks the last report as the releasing one, or calls
// LeaseService/Release when nothing was reported. A report with Release set
// releases the lease immediately. One lease can serve several requests
// (pagination): report every request, and Close releases the lease.
type Lease struct {
	client *Client
	resp   *AcquireResponse
	uri    string

	mu        sync.Mutex
	pending   *Report
	released  bool
	expiresAt time.Time
}

// Lease acquires a lease (see [Client.Acquire]) and wraps it in a [Lease].
// Always call Close on the returned lease.
func (c *Client) Lease(ctx context.Context, req *AcquireRequest) (*Lease, error) {
	resp, err := c.Acquire(ctx, req)
	if err != nil {
		return nil, err
	}
	return c.wrapLease(resp, req.GetUri())
}

// LeaseBatch acquires up to req.Count leases (see [Client.AcquireBatch]).
// Always call Close on every returned lease.
func (c *Client) LeaseBatch(ctx context.Context, req *AcquireBatchRequest) ([]*Lease, error) {
	resp, err := c.AcquireBatch(ctx, req)
	if err != nil {
		return nil, err
	}
	leases := make([]*Lease, 0, len(resp.GetLeases()))
	for _, item := range resp.GetLeases() {
		lease, err := c.wrapLease(item, req.GetUri())
		if err != nil {
			for _, l := range leases {
				_ = l.Close(ctx)
			}
			return nil, err
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

// NewLease wraps a lease acquired with the generated client or
// [Client.AcquireBatch]; uri is the default URI of its reports.
func (c *Client) NewLease(resp *AcquireResponse, uri string) (*Lease, error) {
	return c.wrapLease(resp, uri)
}

func (c *Client) wrapLease(resp *AcquireResponse, uri string) (*Lease, error) {
	if resp.GetLease().GetLeaseId() == "" {
		return nil, &Error{
			Code:      connect.CodeInternal,
			Reason:    ReasonInternal,
			Message:   "acquire response carries no lease",
			Procedure: spinneretv1connect.LeaseServiceAcquireProcedure,
		}
	}
	return &Lease{client: c, resp: resp, uri: uri}, nil
}

// ID returns the lease ID.
func (l *Lease) ID() string { return l.resp.GetLease().GetLeaseId() }

// IdentityID returns the ID of the leased identity.
func (l *Lease) IdentityID() string { return l.resp.GetLease().GetIdentityId() }

// Info returns the lease metadata (identity type, endpoint group, sticky, probe).
func (l *Lease) Info() *LeaseInfo { return l.resp.GetLease() }

// Credential returns the rendered credential of the identity.
func (l *Lease) Credential() *Credential { return l.resp.GetCredential() }

// Proxy returns the assigned proxy, or nil.
func (l *Lease) Proxy() *ProxyAssignment { return l.resp.GetProxy() }

// Hints returns the lease handling hints.
func (l *Lease) Hints() *Hints { return l.resp.GetHints() }

// Response returns the full acquire response.
func (l *Lease) Response() *AcquireResponse { return l.resp }

// URI returns the URI the lease was acquired for.
func (l *Lease) URI() string { return l.uri }

// ExpiresAt returns the lease expiry, updated by Renew.
func (l *Lease) ExpiresAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.expiresAt.IsZero() {
		return l.expiresAt
	}
	if ts := l.resp.GetLease().GetExpiresAt(); ts != nil {
		return ts.AsTime()
	}
	return time.Time{}
}

// Released reports whether the lease has been released (or its release queued).
func (l *Lease) Released() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released
}

// Apply merges the credential into req (see [ApplyCredential]).
func (l *Lease) Apply(req *http.Request) { ApplyCredential(req, l.Credential()) }

// ProxyURL returns the proxy URL to use for requests of this lease, or nil
// when no proxy is assigned. Assign it to http.Transport.Proxy with
// http.ProxyURL. The URL carries credentials: do not log it.
func (l *Lease) ProxyURL() (*url.URL, error) { return ProxyURL(l.Proxy()) }

// Transport returns a clone of base (http.DefaultTransport when nil) that
// sends requests through the assigned proxy. Without an assigned proxy the
// clone keeps the proxy setting of base. Call CloseIdleConnections on the
// transport when the lease is done.
func (l *Lease) Transport(base *http.Transport) (*http.Transport, error) {
	proxy, err := l.ProxyURL()
	if err != nil {
		return nil, err
	}
	if base == nil {
		if def, ok := http.DefaultTransport.(*http.Transport); ok {
			base = def
		} else {
			base = &http.Transport{}
		}
	}
	t := base.Clone()
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return t, nil
}

// Report queues a report of one request made with this lease. It fails when
// the input is invalid, when the lease has already been released
// ([IsLeaseGone]) and when the reporter is closed.
func (l *Lease) Report(in ReportInput) error {
	report, err := buildReport(l.ID(), l.uri, in, time.Now())
	if err != nil {
		return err
	}
	reporter := l.client.Reporter()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return errLeaseReleased()
	}
	if reporter.Closed() {
		return errReporterClosed()
	}
	// Submit under the lease lock to keep the reports of a lease in order.
	if l.pending != nil {
		if err := reporter.Submit(l.pending); err != nil {
			return err
		}
		l.pending = nil
	}
	if report.GetRelease() {
		l.released = true
		return reporter.Submit(report)
	}
	l.pending = report
	return nil
}

// ReportResponse queues a report built from resp: status, method, request
// path and Content-Length (when known) fill the fields left empty in in. Set
// in.StartedAt (or in.Latency) to report the latency. A 407 response is
// reported with error kind "proxy_auth".
func (l *Lease) ReportResponse(resp *http.Response, in ReportInput) error {
	if resp == nil {
		return newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "response must not be nil")
	}
	if in.HTTPStatus == 0 {
		in.HTTPStatus = resp.StatusCode
	}
	if resp.StatusCode == http.StatusProxyAuthRequired && in.ErrorKind == "" {
		in.ErrorKind = ErrorKindProxyAuth
	}
	if req := resp.Request; req != nil {
		if in.Method == "" {
			in.Method = req.Method
		}
		if in.URI == "" && req.URL != nil {
			in.URI = firstNonEmpty(req.URL.EscapedPath(), "/")
		}
	}
	if in.ResponseBytes == 0 && resp.ContentLength > 0 {
		in.ResponseBytes = resp.ContentLength
	}
	return l.Report(in)
}

// ReportError queues a report of a request that failed without a response:
// the error kind is classified from err (see [ClassifyError]) unless
// in.ErrorKind is set, and the method and path are taken from a *url.Error.
func (l *Lease) ReportError(err error, in ReportInput) error {
	if in.ErrorKind == "" {
		in.ErrorKind = ClassifyError(err)
		if in.ErrorKind == ErrorKindNone {
			in.ErrorKind = ErrorKindOther
		}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if in.Method == "" {
			in.Method = strings.ToUpper(ue.Op)
		}
		if in.URI == "" {
			in.URI = ue.URL
		}
	}
	return l.Report(in)
}

// Renew extends the lease by extend (0 = the policy lease TTL) and returns the
// new expiry.
func (l *Lease) Renew(ctx context.Context, extend time.Duration) (time.Time, error) {
	if l.Released() {
		return time.Time{}, errLeaseReleased()
	}
	ms := min(max(extend.Milliseconds(), 0), math.MaxInt32)
	resp, err := l.client.Renew(ctx, &RenewRequest{LeaseId: l.ID(), ExtendMs: int32(ms)})
	if err != nil {
		return time.Time{}, err
	}
	expires := time.Time{}
	if ts := resp.GetExpiresAt(); ts != nil {
		expires = ts.AsTime()
	}
	l.mu.Lock()
	if !expires.IsZero() {
		l.expiresAt = expires
	}
	l.mu.Unlock()
	return l.ExpiresAt(), nil
}

// Close releases the lease: the last queued report is sent with release=true,
// or LeaseService/Release is called when nothing was reported. It returns the
// error of the Release call, except when the lease had already ended
// ([IsLeaseGone]). When the reporter is closed the last report is delivered
// directly. Close is idempotent; later reports fail.
func (l *Lease) Close(ctx context.Context) error {
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return nil
	}
	l.released = true
	pending := l.pending
	l.pending = nil
	if pending != nil {
		pending.Release = true
		err := l.client.Reporter().Submit(pending)
		l.mu.Unlock()
		if err == nil {
			return nil
		}
		return l.deliverDirectly(ctx, pending)
	}
	l.mu.Unlock()
	resp, err := l.client.Release(ctx, &ReleaseRequest{LeaseId: l.ID()})
	logger := l.client.s.logger
	switch {
	case err == nil:
		if !resp.GetReleased() {
			logger.DebugContext(ctx, "spinneret lease had already ended before release", slog.String("lease_id", l.ID()))
		}
		return nil
	case IsLeaseGone(err):
		logger.DebugContext(ctx, "spinneret lease had already ended before release",
			slog.String("lease_id", l.ID()), slog.String("reason", ReasonOf(err)))
		return nil
	default:
		logger.WarnContext(ctx, "spinneret lease release failed",
			slog.String("lease_id", l.ID()), slog.String("error", err.Error()))
		return err
	}
}

func (l *Lease) deliverDirectly(ctx context.Context, report *Report) error {
	resp, err := l.client.sendReports(ctx, []*Report{report})
	if err != nil {
		l.client.s.logger.WarnContext(ctx, "spinneret direct report delivery failed",
			slog.String("lease_id", l.ID()), slog.String("error", err.Error()))
		return err
	}
	if rejected := resp.GetRejected(); len(rejected) > 0 {
		return &Error{
			Code:      connect.CodeInvalidArgument,
			Reason:    rejected[0].GetReason(),
			Message:   "report rejected: " + rejected[0].GetMessage(),
			Procedure: spinneretv1connect.ReportServiceReportProcedure,
		}
	}
	return nil
}

func errLeaseReleased() *Error {
	return newError(connect.CodeFailedPrecondition, ReasonLeaseReleased, "lease has already been released")
}
