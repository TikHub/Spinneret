package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/signal"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// Retry policy of the per-event steps: every Redis or executor step is retried
// up to len(retryBackoff) times; afterwards the failure is logged and the event
// moves on, so a shard is never blocked forever.
var retryBackoff = [...]time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond}

// opTimeout bounds one attempt of a per-event step.
const opTimeout = 3 * time.Second

// leaseFields are the lease hash fields loaded per event, in this order.
var leaseFields = []string{"i", "iid", "e", "p", "pid", "ns", "st", "end"}

// leaseInfo is the subset of the lease hash the worker needs.
type leaseInfo struct {
	identityKey int64
	identityID  string
	egKey       int64
	proxyKey    int64
	proxyID     string
	namespaceID string
	state       string
	endedMs     int64
}

// loadLease reads the lease hash; it returns nil when the lease is gone.
func (w *Worker) loadLease(ctx context.Context, siteKey int64, leaseID string) (*leaseInfo, error) {
	vals, err := w.rdb.Do(ctx, w.rdb.B().Hmget().Key(w.keys.Lease(siteKey, leaseID)).Field(leaseFields...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("load lease: %w", err)
	}
	if len(vals) != len(leaseFields) {
		return nil, fmt.Errorf("load lease: got %d fields, want %d", len(vals), len(leaseFields))
	}
	str := func(i int) string {
		if vals[i].IsNil() {
			return ""
		}
		s, _ := vals[i].ToString()
		return s
	}
	num := func(i int) int64 {
		n, _ := strconv.ParseInt(str(i), 10, 64)
		return n
	}
	info := &leaseInfo{
		identityKey: num(0),
		identityID:  str(1),
		egKey:       num(2),
		proxyKey:    num(3),
		proxyID:     str(4),
		namespaceID: str(5),
		state:       str(6),
		endedMs:     num(7),
	}
	if info.namespaceID == "" && info.identityKey == 0 {
		return nil, nil
	}
	return info, nil
}

// fallbackGroupSignal returns the signal policy used when the endpoint group
// of a report is unknown: that of the first client's default group.
func (w *Worker) fallbackGroupSignal(st *catalog.Site) *policy.CompiledSignal {
	for _, client := range st.Clients {
		if g, ok := st.Group(client, site.DefaultGroup); ok && g.Signal != nil {
			return g.Signal
		}
	}
	return w.fallbackSignal
}

// classification classifies an event (a nil policy yields unknown).
func classification(sig *policy.CompiledSignal, ev signal.Event) policy.Classification {
	return sig.Classify(policy.ReportFacts{
		URI:           ev.URI,
		Method:        ev.Method,
		HTTPStatus:    ev.HTTPStatus,
		BusinessCode:  ev.BusinessCode,
		ErrorKind:     ev.ErrorKind,
		Markers:       ev.Markers,
		OutcomeHint:   ev.OutcomeHint,
		LatencyMs:     ev.LatencyMs,
		ResponseBytes: ev.ResponseBytes,
	})
}

// baseRecord fills the statistics record from the event and resolved context.
func (w *Worker) baseRecord(r *report) ReportRecord {
	ev := r.ev
	rec := ReportRecord{
		ReceivedAt:    ev.Received(),
		StartedAt:     ev.Started(),
		FinishedAt:    ev.Finished(),
		TenantID:      ev.TenantID,
		NamespaceID:   r.ns.ID,
		SiteID:        r.site.ID,
		Site:          r.site.Name,
		LeaseID:       ev.LeaseID,
		ReportID:      ev.ReportID,
		Node:          ev.Node,
		TokenID:       ev.TokenID,
		URI:           ev.URI,
		Method:        ev.Method,
		HTTPStatus:    ev.HTTPStatus,
		BusinessCode:  ev.BusinessCode,
		ErrorKind:     ev.ErrorKind,
		Markers:       ev.Markers,
		OutcomeHint:   ev.OutcomeHint,
		LatencyMs:     ev.LatencyMs,
		ResponseBytes: ev.ResponseBytes,
	}
	if rec.TenantID == "" {
		rec.TenantID = r.ns.TenantID
	}
	if r.group != nil {
		rec.EndpointGroupID = r.group.ID
		rec.EndpointGroup = r.group.Name
		rec.Client = r.group.Client
	}
	if r.lease != nil {
		rec.IdentityID = r.lease.identityID
		rec.ProxyID = r.lease.proxyID
	}
	return rec
}

// record finalizes the record with the classification and emits statistics
// and metrics.
func (w *Worker) record(r *report, rec ReportRecord, cls policy.Classification) {
	rec.Outcome = cls.Outcome
	rec.Blame = string(cls.Blame)
	rec.Rule = cls.RuleName
	if w.rec != nil {
		w.rec.RecordReport(rec)
	}
	if w.metrics == nil {
		return
	}
	w.metrics.ReportTotal.WithLabelValues(observability.Label(rec.Site), observability.Label(rec.EndpointGroup), cls.Outcome).Inc()
	if !rec.ReceivedAt.IsZero() {
		if lag := r.now.Sub(rec.ReceivedAt); lag >= 0 {
			w.metrics.ReportLag.Observe(lag.Seconds())
		}
	}
}

// retry runs fn with a per-attempt timeout, retrying failures with backoff.
// It stops early when ctx is done.
func (w *Worker) retry(ctx context.Context, fn func(ctx context.Context) error) error {
	var err error
	for attempt := 0; ; attempt++ {
		opCtx, cancel := context.WithTimeout(ctx, opTimeout)
		err = fn(opCtx)
		cancel()
		if err == nil || attempt >= len(retryBackoff) || ctx.Err() != nil {
			return err
		}
		if !sleepCtx(ctx, retryBackoff[attempt]) {
			return errors.Join(err, ctx.Err())
		}
	}
}

// sleepCtx waits for d or until ctx is done; it reports whether d elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
