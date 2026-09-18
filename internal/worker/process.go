package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/signal"
)

// Lease states (spec §5.3).
const leaseStateActive = "active"

// report is the resolved context of one stream event as it moves through the
// pipeline.
type report struct {
	base     *slog.Logger
	log      *slog.Logger // built lazily by logger()
	shard    int
	streamID string
	ev       signal.Event
	ns       *catalog.Namespace
	site     *catalog.Site
	group    *catalog.EndpointGroup // nil when the lease or its group is unknown
	lease    *leaseInfo             // nil when the lease is gone
	now      time.Time
}

// logger returns the event logger, creating it on first use so that events
// processed without logging do not pay for the attributes.
func (r *report) logger() *slog.Logger {
	if r.log == nil {
		r.log = r.base.With(slog.Int("shard", r.shard), slog.String("stream_id", r.streamID), slog.String("report_id", r.ev.ReportID))
	}
	return r.log
}

// observeStatus summarizes an observe.lua call made with retries.
type observeStatus int

const (
	// observeApplied: the report was applied now; continue the pipeline.
	observeApplied observeStatus = iota
	// observeSkipped: the stream entry had already been applied (redelivery).
	observeSkipped
	// observeUnknown: the call failed, or a failed attempt may have applied
	// it; only statistics are recorded.
	observeUnknown
)

// processEntry decodes and processes one stream entry. Undecodable entries are
// logged and skipped (the caller acknowledges them).
func (w *Worker) processEntry(ctx context.Context, shard int, entry rueidis.XRangeEntry) {
	if entry.FieldValues == nil {
		return // trimmed before it could be claimed
	}
	started := time.Now()
	if v := entry.FieldValues[signal.StreamFieldVersion]; v != signal.StreamVersion {
		w.logger.Warn("skipping report event with unsupported version",
			slog.Int("shard", shard), slog.String("stream_id", entry.ID), slog.String("version", v))
		return
	}
	ev, err := signal.DecodeEvent([]byte(entry.FieldValues[signal.StreamFieldData]))
	if err != nil {
		w.logger.Warn("skipping undecodable report event",
			slog.Int("shard", shard), slog.String("stream_id", entry.ID), slog.Any("error", err))
		return
	}
	w.processEvent(ctx, shard, entry.ID, ev)
	if w.metrics != nil {
		w.metrics.ReportProcessDuration.Observe(time.Since(started).Seconds())
	}
}

// processEvent runs the pipeline of spec §6.3 for one decoded event.
func (w *Worker) processEvent(ctx context.Context, shard int, streamID string, ev signal.Event) {
	r := &report{
		base:     w.logger,
		shard:    shard,
		streamID: streamID,
		ev:       ev,
		now:      w.now(),
	}
	ref, err := idgen.ParseLeaseID(ev.LeaseID)
	if err != nil {
		r.logger().Warn("dropping report with malformed lease id")
		return
	}
	var ok bool
	if r.site, r.ns, ok = w.cat.SiteByKey(ref.SiteKey); !ok {
		// Catalog snapshots propagate between instances asynchronously, so a site
		// created moments ago can still be missing here while its leases are
		// already being reported. Reload the namespace once (rate limited) before
		// dropping the report, otherwise the first reports of a new site are lost.
		if w.refreshNamespace(ctx, ev.NamespaceID) {
			r.site, r.ns, ok = w.cat.SiteByKey(ref.SiteKey)
		}
		if !ok {
			r.logger().Warn("dropping report of unknown site", slog.Int64("site_key", ref.SiteKey))
			return
		}
	}
	if r.ns.ID != ev.NamespaceID {
		r.logger().Warn("dropping report: site namespace does not match event namespace", slog.String("site_id", r.site.ID))
		return
	}
	if err := w.retry(ctx, func(ctx context.Context) error {
		var lerr error
		r.lease, lerr = w.loadLease(ctx, r.site.Key, ev.LeaseID)
		return lerr
	}); err != nil {
		r.logger().Error("load lease failed; recording statistics only", slog.Any("error", err))
		r.lease = nil
		w.record(r, w.baseRecord(r), classification(w.fallbackGroupSignal(r.site), ev))
		return
	}
	if r.lease != nil && r.lease.namespaceID != ev.NamespaceID {
		r.logger().Warn("dropping report: lease namespace does not match event namespace", slog.String("site_id", r.site.ID))
		return
	}
	if r.lease != nil {
		r.group = r.site.GroupsByKey[r.lease.egKey]
	}
	if r.group == nil {
		w.statsOnly(ctx, r)
		return
	}
	w.processLeased(ctx, r)
}

// refreshNamespace reloads one namespace from PostgreSQL and reports whether
// the snapshot was refreshed. Reloads are rate limited per namespace so a flood
// of reports for a deleted site cannot hammer the database.
func (w *Worker) refreshNamespace(ctx context.Context, namespaceID string) bool {
	if namespaceID == "" {
		return false
	}
	now := w.now()
	w.mu.Lock()
	if last, seen := w.catalogRefresh[namespaceID]; seen && now.Sub(last) < catalogRefreshInterval {
		w.mu.Unlock()
		return false
	}
	w.catalogRefresh[namespaceID] = now
	w.mu.Unlock()

	if err := w.cat.Reload(ctx, namespaceID); err != nil {
		w.logger.Warn("catalog reload for unknown site failed",
			slog.String("namespace_id", namespaceID), slog.Any("error", err))
		return false
	}
	return true
}

// statsOnly records a report whose lease (or endpoint group) is gone: the
// checkpoint keeps redeliveries from being counted twice, the outcome is
// classified with the site's fallback signal policy and the record carries no
// endpoint group (spec §6.3).
func (w *Worker) statsOnly(ctx context.Context, r *report) {
	var dup bool
	attempts := 0
	if err := w.retry(ctx, func(ctx context.Context) error {
		var cerr error
		attempts++
		dup, cerr = w.checkpoint(ctx, r.site.Key, r.shard, r.streamID)
		return cerr
	}); err != nil {
		r.logger().Error("checkpoint failed; recording statistics anyway", slog.Any("error", err))
	}
	if dup && attempts == 1 {
		return
	}
	w.record(r, w.baseRecord(r), classification(w.fallbackGroupSignal(r.site), r.ev))
}

// processLeased handles a report whose lease and endpoint group are known.
func (w *Worker) processLeased(ctx context.Context, r *report) {
	sig := r.group.Signal
	if sig == nil {
		sig = w.fallbackSignal
	}
	cls := classification(sig, r.ev)
	act := r.group.Action
	if act == nil {
		act = w.fallbackAction
	}
	rec := w.baseRecord(r)
	rec.Late = w.isLate(r)
	if act == nil {
		r.logger().Error("endpoint group has no action policy; recording statistics only", slog.String("group_id", r.group.ID))
		w.record(r, rec, cls)
		return
	}

	params := observeParams{
		shard: r.shard, streamID: r.streamID, now: r.now, leaseID: r.ev.LeaseID, group: r.group, action: act,
		identityKey: r.lease.identityKey, proxyKey: r.lease.proxyKey, outcome: cls.Outcome, blame: cls.Blame,
		late: rec.Late, counters: act.CounterRequests(cls.Outcome), banWindows: act.EscalationWindows(),
	}
	obs, status := w.observeWithRetry(ctx, r, params)
	switch status {
	case observeSkipped:
		// A redelivered entry may have been interrupted before its release;
		// releasing is idempotent (only active leases are released).
		w.releaseIfRequested(ctx, r)
		return
	case observeUnknown:
		w.releaseIfRequested(ctx, r)
		w.notifyRisk(r, cls.Outcome)
		w.record(r, rec, cls)
		return
	}
	cls.Blame = obs.blame
	rec.Suppressed, rec.Probe, rec.IdentityType = obs.suppressed, obs.probe, obs.identityType

	if !obs.suppressed && !rec.Late && obs.identityState != "" && w.exec != nil {
		if planned := act.Evaluate(evalInput(cls, obs, params, r, w.jitter)); len(planned) > 0 {
			w.execute(ctx, r, act, cls, obs, planned)
		}
	}
	w.releaseIfRequested(ctx, r)
	w.notifyRisk(r, cls.Outcome)
	w.record(r, rec, cls)
}

// isLate reports whether the report arrived more than the late window after
// its lease ended (spec §6.3, design doc §7.5). Lateness is judged by the
// ingest time so that a stream backlog does not turn timely reports into late
// ones; the processing time is used when the event carries no receive time.
func (w *Worker) isLate(r *report) bool {
	if r.lease.state == leaseStateActive || r.lease.endedMs <= 0 {
		return false
	}
	at := r.ev.ReceivedAt
	if at <= 0 {
		at = r.now.UnixMilli()
	}
	return at-r.lease.endedMs > w.cfg.LateReportWindow.Milliseconds()
}

// notifyRisk tells the breaker about a risk outcome of the report's group.
func (w *Worker) notifyRisk(r *report, outcome string) {
	if w.brk != nil && policy.IsRiskOutcome(outcome) {
		w.brk.NotifyRisk(r.site.Key, r.group.Key)
	}
}

// releaseIfRequested releases an active lease when the report asks for it.
func (w *Worker) releaseIfRequested(ctx context.Context, r *report) {
	if !r.ev.Release || r.lease.state != leaseStateActive || w.rel == nil {
		return
	}
	if err := w.retry(ctx, func(ctx context.Context) error {
		_, rerr := w.rel.ReleaseLease(ctx, r.site.Key, r.ev.LeaseID, r.now)
		return rerr
	}); err != nil {
		r.logger().Error("release lease failed", slog.Any("error", err))
	}
}

// observeWithRetry runs observe.lua with retries and classifies the result.
func (w *Worker) observeWithRetry(ctx context.Context, r *report, params observeParams) (observeResult, observeStatus) {
	var obs observeResult
	attempts := 0
	if err := w.retry(ctx, func(ctx context.Context) error {
		var oerr error
		attempts++
		obs, oerr = w.observe(ctx, r.site.Key, params)
		return oerr
	}); err != nil {
		r.logger().Error("observe failed; recording statistics only", slog.Any("error", err))
		return observeResult{}, observeUnknown
	}
	if !obs.dup {
		return obs, observeApplied
	}
	if attempts > 1 {
		// A failed attempt of this very call advanced the checkpoint: its
		// effects are unknown, so skip actions but keep the statistics.
		r.logger().Warn("observe outcome unknown after a failed attempt; recording statistics only")
		return obs, observeUnknown
	}
	r.logger().Debug("skipping already applied report")
	return obs, observeSkipped
}

// evalInput builds the action policy input from the observed hot state.
func evalInput(cls policy.Classification, obs observeResult, params observeParams, r *report, jitter func() float64) policy.EvalInput {
	in := policy.EvalInput{
		Outcome:         cls.Outcome,
		Blame:           cls.Blame,
		IdentityState:   obs.identityState,
		HasAccount:      obs.accountKey > 0,
		HasProxy:        r.lease.proxyKey > 0,
		Counts:          make(map[policy.CounterRequest]int64, len(params.counters)),
		BanCounts:       make(map[time.Duration]int64, len(params.banWindows)),
		EndpointStreak:  obs.endpointStreak,
		SiteStreak:      obs.globalStreak,
		ProxyStreak:     obs.proxyStreak,
		EndpointScore:   obs.endpointScore,
		EndpointSamples: obs.endpointSamples,
		GlobalScore:     obs.globalScore,
		GlobalSamples:   obs.globalSamples,
		Jitter:          jitter,
	}
	for i, c := range params.counters {
		in.Counts[c] = obs.counts[i]
	}
	for i, bw := range params.banWindows {
		in.BanCounts[bw] = obs.banCounts[i]
	}
	if remaining := obs.endpointCooldownUntil - r.now.UnixMilli(); remaining > 0 {
		in.EndpointCooldownRemaining = time.Duration(remaining) * time.Millisecond
	}
	return in
}

// execute hands planned actions to the executor. The input is built once, so
// every attempt carries the same report identity, time and plan. Failed calls
// are retried only for an idempotent executor (see IdempotentActionExecutor):
// retrying any other executor after an attempt with an unknown outcome would
// report already applied actions as skipped and lose their state changes.
func (w *Worker) execute(ctx context.Context, r *report, act *policy.CompiledAction, cls policy.Classification, obs observeResult, planned []policy.PlannedAction) {
	in := ExecInput{
		ReportContext: ReportContext{
			Namespace:   r.ns,
			Site:        r.site,
			Group:       r.group,
			LeaseID:     r.ev.LeaseID,
			ReportID:    r.ev.ReportID,
			IdentityKey: r.lease.identityKey,
			IdentityID:  r.lease.identityID,
			AccountKey:  obs.accountKey,
			ProxyKey:    r.lease.proxyKey,
			ProxyID:     r.lease.proxyID,
			Outcome:     cls.Outcome,
			RuleName:    cls.RuleName,
		},
		Planned: planned,
		Shadow:  act.Shadow(),
		Now:     r.now,
	}
	call := func(ctx context.Context) error {
		_, eerr := w.exec.Execute(ctx, in)
		return eerr
	}
	if ie, ok := w.exec.(IdempotentActionExecutor); ok && ie.IdempotentExecute() {
		if err := w.retry(ctx, call); err != nil {
			r.logger().Error("execute actions failed", slog.Int("planned", len(planned)), slog.Any("error", err))
		}
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	err := call(opCtx)
	cancel()
	if err != nil {
		r.logger().Error("execute actions failed; outcome unknown, not retried",
			slog.Int("planned", len(planned)), slog.Any("error", err))
	}
}
