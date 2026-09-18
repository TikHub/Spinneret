package stats

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/store/clickhouse"
	"github.com/TikHub/Spinneret/internal/store/postgres"
)

// Defaults of an Aggregator.
const (
	// DefaultFlushInterval is the period of the flush loop.
	DefaultFlushInterval = 10 * time.Second
	// DefaultMaxKeys caps the distinct keys per aggregate table per flush
	// interval and the rows kept pending after failed flushes.
	DefaultMaxKeys = 200_000
	// DefaultMaxRiskEvents caps buffered (and pending) risk_events rows.
	DefaultMaxRiskEvents = 50_000
	// DefaultMaxRiskBytes caps the approximate memory of buffered (and,
	// separately, pending) risk_events rows.
	DefaultMaxRiskBytes = 64 << 20
	// MaxRowsPerStatement caps the rows written by one statement.
	MaxRowsPerStatement = 5000

	defaultFlushTimeout     = time.Minute
	defaultShutdownTimeout  = 10 * time.Second
	defaultStatementTimeout = 30 * time.Second
	defaultRetryBackoff     = 200 * time.Millisecond
	defaultMaxRetries       = 3
)

// ErrRunning is returned by Run when another Run call is active.
var ErrRunning = errors.New("stats: aggregator is already running")

// errNoPool is returned by writes of an aggregator without a database pool.
var errNoPool = errors.New("stats: postgres pool is nil")

// Aggregator buffers statistics in memory and flushes them periodically. The
// Record* methods are safe for concurrent use and never block on I/O.
type Aggregator struct {
	pool    *pgxpool.Pool
	ch      *clickhouse.Writer
	metrics *observability.Metrics
	logger  *slog.Logger
	now     func() time.Time

	flushInterval    time.Duration
	flushTimeout     time.Duration
	shutdownTimeout  time.Duration
	statementTimeout time.Duration
	retryBackoff     time.Duration
	maxRetries       int
	chunkSize        int
	ensurePartitions func(ctx context.Context) error

	acquire  *aggTable[acquireKey, acquireValue]
	payload  *aggTable[payloadKey, countValue]
	node     *aggTable[nodeKey, nodeValue]
	outcome  *aggTable[outcomeKey, outcomeValue]
	identity *aggTable[identityKey, identityValue]
	risk     *riskBuffer

	// invalid counts records rejected by validation.
	invalid atomic.Int64

	running atomic.Bool
	flushMu sync.Mutex
	// logged is the counter snapshot of the last drop log (under flushMu).
	logged Counters
}

// NewAggregator returns an aggregator writing to pool and, when ch is not
// nil, queuing raw report and lease events on ch (whose Run loop the caller
// starts). metrics and logger may be nil.
func NewAggregator(pool *pgxpool.Pool, ch *clickhouse.Writer, metrics *observability.Metrics, logger *slog.Logger) *Aggregator {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a := &Aggregator{
		pool:             pool,
		ch:               ch,
		metrics:          metrics,
		logger:           logger.With(slog.String("component", "stats")),
		now:              time.Now,
		flushInterval:    DefaultFlushInterval,
		flushTimeout:     defaultFlushTimeout,
		shutdownTimeout:  defaultShutdownTimeout,
		statementTimeout: defaultStatementTimeout,
		retryBackoff:     defaultRetryBackoff,
		maxRetries:       defaultMaxRetries,
		chunkSize:        MaxRowsPerStatement,
	}
	a.ensurePartitions = func(ctx context.Context) error {
		if a.pool == nil {
			return errNoPool
		}
		return postgres.EnsurePartitions(ctx, a.pool, a.now())
	}
	a.acquire = newAggTable(postgres.TableAcquireStatsMinutely, postgres.GranularityDay, DefaultMaxKeys, a.writeAcquire)
	a.payload = newAggTable(postgres.TablePayloadAccessMinutely, postgres.GranularityDay, DefaultMaxKeys, a.writePayload)
	a.node = newAggTable(postgres.TableNodeStatsMinutely, postgres.GranularityDay, DefaultMaxKeys, a.writeNode)
	a.outcome = newAggTable(postgres.TableOutcomeStatsMinutely, postgres.GranularityDay, DefaultMaxKeys, a.writeOutcome)
	a.identity = newAggTable(postgres.TableIdentityStatsHourly, postgres.GranularityMonth, DefaultMaxKeys, a.writeIdentity)
	a.risk = newRiskBuffer(DefaultMaxRiskEvents, DefaultMaxRiskBytes, func(ctx context.Context, rows []riskRow) error {
		return copyRiskEvents(ctx, a.pool, rows)
	})
	return a
}

// RecordAcquire records one acquire attempt: acquire_stats_minutely for every
// result; payload_access_minutely and node_stats_minutely.acquires for result
// ok; a ClickHouse lease event "acquired". Records without a namespace or with
// an unknown result are counted as invalid and ignored.
func (a *Aggregator) RecordAcquire(r AcquireRecord) {
	if r.NamespaceID == "" || !validAcquireResult(r.Result) {
		a.invalid.Add(1)
		return
	}
	at := r.At
	if at.IsZero() {
		at = a.now()
	}
	bucket := minuteBucket(at)
	ns := cleanText(r.NamespaceID, maxIDBytes)
	a.acquire.add(acquireKey{
		bucket:          bucket,
		namespaceID:     ns,
		siteID:          cleanText(r.SiteID, maxIDBytes),
		endpointGroupID: cleanText(r.EndpointGroupID, maxIDBytes),
		result:          r.Result,
	}, acquireValue{count: 1, durationUs: nonNegative(r.Duration.Microseconds())})
	if r.Result == ResultOK {
		if r.IdentityTypeID != "" {
			a.payload.add(payloadKey{
				bucket:         bucket,
				namespaceID:    ns,
				tokenID:        cleanText(r.TokenID, maxIDBytes),
				identityTypeID: cleanText(r.IdentityTypeID, maxIDBytes),
			}, countValue{count: 1})
		}
		a.node.add(nodeKey{bucket: bucket, namespaceID: ns, node: nodeName(r.Node)}, nodeValue{acquires: 1})
	}
	if a.ch != nil {
		a.ch.AddLease(clickhouse.LeaseEvent{
			EventTime:     at,
			TenantID:      r.TenantID,
			NamespaceID:   r.NamespaceID,
			SiteID:        r.SiteID,
			Site:          r.Site,
			EndpointGroup: r.EndpointGroup,
			Client:        r.Client,
			IdentityID:    r.IdentityID,
			ProxyID:       r.ProxyID,
			LeaseID:       r.LeaseID,
			Node:          r.Node,
			TokenID:       r.TokenID,
			Event:         LeaseEventAcquired,
			Result:        r.Result,
			DurationUs:    clampUint32(r.Duration.Microseconds()),
			Probe:         r.Probe,
			Sticky:        r.Sticky,
		})
	}
}

// RecordReport records one processed report: outcome_stats_minutely,
// identity_stats_hourly (when the identity is known),
// node_stats_minutely.reports, a risk_events row for non-success outcomes and
// a ClickHouse report event. Records without a namespace are counted as
// invalid and ignored.
func (a *Aggregator) RecordReport(r ReportRecord) {
	if r.NamespaceID == "" {
		a.invalid.Add(1)
		return
	}
	received := r.ReceivedAt
	if received.IsZero() {
		received = a.now()
	}
	eventTime := reportEventTime(r.FinishedAt, received)
	outcome := cleanText(r.Outcome, maxEnumBytes)
	if outcome == "" {
		outcome = policy.OutcomeUnknown
	}
	ns := cleanText(r.NamespaceID, maxIDBytes)
	siteID := cleanText(r.SiteID, maxIDBytes)
	egID := cleanText(r.EndpointGroupID, maxIDBytes)

	a.outcome.add(outcomeKey{
		bucket:          minuteBucket(eventTime),
		namespaceID:     ns,
		siteID:          siteID,
		endpointGroupID: egID,
		proxyID:         cleanText(r.ProxyID, maxIDBytes),
		outcome:         outcome,
	}, outcomeValue{count: 1, latencyMs: nonNegative(r.LatencyMs), responseBytes: nonNegative(r.ResponseBytes)})
	if r.IdentityID != "" {
		a.identity.add(identityKey{
			bucket:          hourBucket(eventTime),
			identityID:      cleanText(r.IdentityID, maxIDBytes),
			endpointGroupID: egID,
			outcome:         outcome,
		}, identityValue{siteID: siteID, count: 1})
	}
	a.node.add(nodeKey{bucket: minuteBucket(received), namespaceID: ns, node: nodeName(r.Node)}, nodeValue{reports: 1})
	risk := outcome != policy.OutcomeSuccess
	if !risk && a.ch == nil {
		return
	}
	// Buffered rows outlive the call: copy the caller's markers once and share
	// the (never modified) copy between the risk row and the ClickHouse event.
	markers := cleanMarkers(r.Markers)
	if risk {
		a.risk.add(newRiskRow(&r, eventTime, outcome, markers))
	}
	if a.ch != nil {
		a.ch.AddReport(clickhouse.ReportEvent{
			EventTime:     eventTime,
			ReceivedAt:    received,
			StartedAt:     r.StartedAt,
			TenantID:      r.TenantID,
			NamespaceID:   r.NamespaceID,
			SiteID:        r.SiteID,
			Site:          r.Site,
			EndpointGroup: r.EndpointGroup,
			Client:        r.Client,
			IdentityID:    r.IdentityID,
			IdentityType:  r.IdentityType,
			ProxyID:       r.ProxyID,
			LeaseID:       r.LeaseID,
			ReportID:      r.ReportID,
			Node:          r.Node,
			TokenID:       r.TokenID,
			URI:           r.URI,
			Method:        r.Method,
			HTTPStatus:    clampUint16(r.HTTPStatus),
			BusinessCode:  r.BusinessCode,
			ErrorKind:     r.ErrorKind,
			Markers:       markers,
			Outcome:       outcome,
			OutcomeHint:   r.OutcomeHint,
			Blame:         r.Blame,
			Rule:          r.Rule,
			LatencyMs:     clampUint32(r.LatencyMs),
			ResponseBytes: uint64(nonNegative(r.ResponseBytes)),
			Suppressed:    r.Suppressed,
			Late:          r.Late,
			Probe:         r.Probe,
		})
	}
}

// RecordLeaseEnd records the end (or renewal) of a lease:
// node_stats_minutely.abandoned for kind abandoned and a ClickHouse lease
// event of the kind. Records without a namespace or with an unknown kind are
// counted as invalid and ignored.
func (a *Aggregator) RecordLeaseEnd(r LeaseEndRecord) {
	if r.NamespaceID == "" || !validLeaseEndKind(r.Kind) {
		a.invalid.Add(1)
		return
	}
	at := r.At
	if at.IsZero() {
		at = a.now()
	}
	if r.Kind == LeaseEndAbandoned {
		a.node.add(nodeKey{
			bucket:      minuteBucket(at),
			namespaceID: cleanText(r.NamespaceID, maxIDBytes),
			node:        nodeName(r.Node),
		}, nodeValue{abandoned: 1})
	}
	if a.ch != nil {
		a.ch.AddLease(clickhouse.LeaseEvent{
			EventTime:     at,
			TenantID:      r.TenantID,
			NamespaceID:   r.NamespaceID,
			SiteID:        r.SiteID,
			Site:          r.Site,
			EndpointGroup: r.EndpointGroup,
			Client:        r.Client,
			IdentityID:    r.IdentityID,
			ProxyID:       r.ProxyID,
			LeaseID:       r.LeaseID,
			Node:          r.Node,
			TokenID:       r.TokenID,
			Event:         r.Kind,
			Probe:         r.Probe,
		})
	}
}

// RecordRejectedReports adds n rejected reports of node to the current minute
// of node_stats_minutely. Calls with n <= 0 are ignored; calls without a
// namespace are counted as invalid.
func (a *Aggregator) RecordRejectedReports(namespaceID, node string, n int) {
	if n <= 0 {
		return
	}
	if namespaceID == "" {
		a.invalid.Add(1)
		return
	}
	a.node.add(nodeKey{
		bucket:      minuteBucket(a.now()),
		namespaceID: cleanText(namespaceID, maxIDBytes),
		node:        nodeName(node),
	}, nodeValue{rejected: int64(n)})
}

// Run flushes every flush interval until ctx is done, then performs a final
// flush bounded by a 10 s timeout and returns nil. Flush errors are logged; the
// affected rows are retried on the next flush. A concurrent second call returns
// ErrRunning.
func (a *Aggregator) Run(ctx context.Context) error {
	if !a.running.CompareAndSwap(false, true) {
		return ErrRunning
	}
	defer a.running.Store(false)

	ticker := time.NewTicker(a.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.shutdownTimeout)
			err := a.Flush(fctx)
			cancel()
			if err != nil {
				a.logger.Error("stats: final flush incomplete, statistics lost", slog.Any("error", err))
			}
			return nil
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, a.flushTimeout)
			err := a.Flush(fctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				a.logger.Warn("stats: flush incomplete", slog.Any("error", err))
			}
		}
	}
}

// Flush writes everything buffered so far. It returns an error describing the
// tables whose rows are kept for a later flush; rows that can never be
// written are dropped (see Counters). Flush calls are serialized.
func (a *Aggregator) Flush(ctx context.Context) error {
	a.flushMu.Lock()
	defer a.flushMu.Unlock()

	fw := &flushWriter{
		metrics:          a.metrics,
		logger:           a.logger,
		chunkSize:        max(a.chunkSize, 1),
		maxRetries:       a.maxRetries,
		retryBackoff:     a.retryBackoff,
		statementTimeout: a.statementTimeout,
		ensure:           a.ensurePartitions,
	}
	errs := []error{
		a.node.flush(ctx, fw),
		a.acquire.flush(ctx, fw),
		a.payload.flush(ctx, fw),
		a.outcome.flush(ctx, fw),
		a.identity.flush(ctx, fw),
		a.risk.flush(ctx, fw),
	}
	a.logDrops()
	return errors.Join(errs...)
}

// Counters is a snapshot of the aggregator's loss counters. Map keys are table
// names.
type Counters struct {
	// DroppedRecords counts records not aggregated because a table reached its
	// distinct-key cap (risk_events: the buffer was full).
	DroppedRecords map[string]int64
	// DroppedRows counts aggregated rows discarded while flushing (pending cap
	// exceeded, missing partitions, rows rejected by the database).
	DroppedRows map[string]int64
	// Invalid counts records rejected by validation.
	Invalid int64
}

// Counters returns the cumulative loss counters.
func (a *Aggregator) Counters() Counters {
	c := Counters{
		DroppedRecords: map[string]int64{
			a.acquire.name:           a.acquire.droppedKeys.Load(),
			a.payload.name:           a.payload.droppedKeys.Load(),
			a.node.name:              a.node.droppedKeys.Load(),
			a.outcome.name:           a.outcome.droppedKeys.Load(),
			a.identity.name:          a.identity.droppedKeys.Load(),
			postgres.TableRiskEvents: a.risk.droppedFull.Load(),
		},
		DroppedRows: map[string]int64{
			a.acquire.name:           a.acquire.droppedRows.Load(),
			a.payload.name:           a.payload.droppedRows.Load(),
			a.node.name:              a.node.droppedRows.Load(),
			a.outcome.name:           a.outcome.droppedRows.Load(),
			a.identity.name:          a.identity.droppedRows.Load(),
			postgres.TableRiskEvents: a.risk.droppedRows.Load(),
		},
		Invalid: a.invalid.Load(),
	}
	return c
}

// logDrops logs the loss counters that changed since the previous call, so
// drops are reported at most once per flush. The caller holds flushMu.
func (a *Aggregator) logDrops() {
	cur := a.Counters()
	for table, n := range cur.DroppedRecords {
		if delta := n - a.logged.DroppedRecords[table]; delta > 0 {
			a.logger.Warn("stats: records dropped, too many distinct keys or buffer full",
				slog.String("table", table), slog.Int64("dropped", delta), slog.Int64("dropped_total", n))
		}
	}
	for table, n := range cur.DroppedRows {
		if delta := n - a.logged.DroppedRows[table]; delta > 0 {
			a.logger.Warn("stats: aggregated rows dropped while flushing",
				slog.String("table", table), slog.Int64("dropped", delta), slog.Int64("dropped_total", n))
		}
	}
	if delta := cur.Invalid - a.logged.Invalid; delta > 0 {
		a.logger.Warn("stats: invalid records ignored", slog.Int64("invalid", delta), slog.Int64("invalid_total", cur.Invalid))
	}
	a.logged = cur
}
