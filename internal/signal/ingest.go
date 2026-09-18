package signal

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"github.com/redis/rueidis"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/store/redis"
)

// Ingest limits and defaults (spec §6.2, §12).
const (
	// MaxReports is the largest batch accepted by one Ingest call.
	MaxReports = 500
	// DefaultReportShards is used when Config.ReportShards is not positive.
	DefaultReportShards = 16
	// MaxReportShards is the largest shard count a two-hex-digit lease suffix can address.
	MaxReportShards = 256
	// DefaultDedupTTL is used when Config.DedupTTL is not positive.
	DefaultDedupTTL = time.Hour
	// DefaultStreamMaxLen is used when Config.StreamMaxLen is not positive.
	DefaultStreamMaxLen int64 = 1_000_000
	// DefaultLateReportWindow is used when Config.LateReportWindow is not
	// positive (the worker applies the same default).
	DefaultLateReportWindow = 10 * time.Minute

	maxNodeLen           = 128
	maxRejectMessageLen  = 256
	ingestUnavailableMs  = 1000
	metricResultAccepted = "accepted"
	metricResultDup      = "duplicated"
	metricResultRejected = "rejected"
)

var (
	//go:embed lua/ingest.lua
	ingestLua string
	//go:embed lua/lease_retain.lua
	leaseRetainLua string
)

// Config configures an Ingestor.
type Config struct {
	// ReportShards is the number of report stream shards (SPINNERET_REPORT_SHARDS).
	ReportShards int
	// DedupTTL is how long report IDs are remembered (SPINNERET_REPORT_DEDUP_TTL).
	// It also bounds the report processing backlog: the lease hash of an
	// accepted report stays readable for at least DedupTTL after ingest so
	// that the worker can still resolve the lease (spec §6.2).
	DedupTTL time.Duration
	// StreamMaxLen is the approximate cap of each stream (SPINNERET_STREAM_MAXLEN).
	StreamMaxLen int64
	// LateReportWindow is SPINNERET_LATE_REPORT_WINDOW. Only reports that are
	// timely at ingest (the lease is active or ended at most this long ago)
	// extend the retention of the lease hash, so a report after the window
	// never keeps an ended lease alive.
	LateReportWindow time.Duration
}

// Rejected describes a report that was not accepted.
type Rejected struct {
	ReportID string
	// Reason is invalid_argument, lease_unknown or scope_missing.
	Reason  apperr.Reason
	Message string
}

// RejectRecorder receives the number of rejected reports per node (stats
// aggregation, node_stats_minutely.rejected). Implementations must not block.
type RejectRecorder interface {
	RecordRejectedReports(namespaceID, node string, n int)
}

// Ingestor validates reports and appends them to the report streams. It is
// safe for concurrent use.
type Ingestor struct {
	cfg     Config
	rdb     rueidis.Client
	keys    redis.Keys
	cat     catalog.Catalog
	rec     RejectRecorder
	metrics *observability.Metrics
	logger  *slog.Logger
	script  *redis.Script
	retain  *redis.Script
	now     func() time.Time
}

// NewIngestor creates an Ingestor. Non-positive config values select the
// defaults; a shard count above MaxReportShards is clamped. rec and metrics
// may be nil.
func NewIngestor(cfg Config, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, rec RejectRecorder, metrics *observability.Metrics, logger *slog.Logger) *Ingestor {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ReportShards <= 0 {
		cfg.ReportShards = DefaultReportShards
	}
	if cfg.ReportShards > MaxReportShards {
		logger.Warn("report shard count clamped", slog.Int("configured", cfg.ReportShards), slog.Int("max", MaxReportShards))
		cfg.ReportShards = MaxReportShards
	}
	if cfg.DedupTTL <= 0 {
		cfg.DedupTTL = DefaultDedupTTL
	}
	if cfg.DedupTTL < time.Millisecond {
		cfg.DedupTTL = time.Millisecond
	}
	if cfg.StreamMaxLen <= 0 {
		cfg.StreamMaxLen = DefaultStreamMaxLen
	}
	if cfg.LateReportWindow <= 0 {
		cfg.LateReportWindow = DefaultLateReportWindow
	}
	return &Ingestor{
		cfg:     cfg,
		rdb:     rdb,
		keys:    keys,
		cat:     cat,
		rec:     rec,
		metrics: metrics,
		logger:  logger.With(slog.String("component", "report_ingest")),
		script:  redis.NewScript("ingest", ingestLua),
		retain:  redis.NewScript("lease_retain", leaseRetainLua),
		now:     time.Now,
	}
}

// candidate is a report that passed the stateless checks.
type candidate struct {
	report *spinneretv1.Report
	ref    idgen.LeaseRef
	site   *catalog.Site
	ns     *catalog.Namespace
}

// Ingest validates, authorizes, de-duplicates and enqueues reports. Reports are
// judged individually: failures are returned in rejected (in input order)
// while the other reports are accepted or counted as duplicated. err is
// non-nil only when the batch as a whole cannot be processed (unauthenticated,
// oversized, or Redis unavailable); a retried batch is safe because accepted
// reports are then counted as duplicated.
func (g *Ingestor) Ingest(ctx context.Context, p *authz.Principal, node string, reports []*spinneretv1.Report) (accepted, duplicated int, rejected []Rejected, err error) {
	if p == nil {
		return 0, 0, nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if len(reports) > MaxReports {
		return 0, 0, nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d reports per request", MaxReports)
	}
	if len(reports) == 0 {
		return 0, 0, nil, nil
	}
	node = sanitizeNode(node)
	rejected = []Rejected{}
	candidates := make([]candidate, 0, len(reports))
	for _, r := range reports {
		c, rej, ok := g.check(p, r)
		if !ok {
			rejected = append(rejected, rej)
			continue
		}
		candidates = append(candidates, c)
	}
	defer func() { g.finish(p, node, accepted, duplicated, len(rejected)) }()

	if len(candidates) == 0 {
		return 0, 0, rejected, nil
	}
	now := g.now()
	known, err := g.lookupLeases(ctx, candidates, now)
	if err != nil {
		return 0, 0, nil, err
	}
	order, shards, rejected := g.groupByShard(p, node, candidates, known, rejected, now)
	if len(order) == 0 {
		return 0, 0, rejected, nil
	}
	accepted, duplicated, err = g.enqueue(ctx, order, shards)
	if err != nil {
		return accepted, duplicated, nil, err
	}
	return accepted, duplicated, rejected, nil
}

// check runs the stateless per-report checks: protovalidate rules, report and
// lease ID syntax, lease site and namespace, and report:write on the site.
func (g *Ingestor) check(p *authz.Principal, r *spinneretv1.Report) (candidate, Rejected, bool) {
	if r == nil {
		return candidate{}, Rejected{Reason: apperr.ReasonInvalidArgument, Message: "report is empty"}, false
	}
	if err := protovalidate.Validate(r); err != nil {
		return candidate{}, reject(r, apperr.ReasonInvalidArgument, validationMessage(err)), false
	}
	if !idgen.ValidReportID(r.GetReportId()) {
		return candidate{}, reject(r, apperr.ReasonInvalidArgument, "report_id must be 1-64 characters of [A-Za-z0-9_.:-]"), false
	}
	// protovalidate only checks presence and order; out-of-range timestamps
	// would overflow the millisecond fields of the stream event.
	if r.GetStartedAt().CheckValid() != nil || r.GetFinishedAt().CheckValid() != nil {
		return candidate{}, reject(r, apperr.ReasonInvalidArgument, "started_at and finished_at must be valid timestamps"), false
	}
	ref, err := idgen.ParseLeaseID(r.GetLeaseId())
	if err != nil || ref.Shard >= g.cfg.ReportShards {
		return candidate{}, reject(r, apperr.ReasonLeaseUnknown, "lease is unknown or expired"), false
	}
	s, ns, ok := g.cat.SiteByKey(ref.SiteKey)
	if !ok || (p.Kind == authz.KindToken && ns.ID != p.NamespaceID) {
		return candidate{}, reject(r, apperr.ReasonLeaseUnknown, "lease is unknown or expired"), false
	}
	res := authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name, SiteID: s.ID, SiteName: s.Name}
	if !p.Can(authz.PermReportWrite, res) {
		return candidate{}, reject(r, apperr.ReasonScopeMissing, "report:write is not granted for the lease site"), false
	}
	return candidate{report: r, ref: ref, site: s, ns: ns}, Rejected{}, true
}

// lookupLeases runs lease_retain.lua for every distinct lease in one pipeline
// and returns the leases that exist in the namespace of their site. Ended
// leases ("st" released/expired) are still accepted: the lease hash survives
// for the late-report window after the lease ended, and the worker decides
// whether a report is late. For the leases found, the script counts the
// ingested reports and, for reports that are timely (the lease is active or
// ended at most LateReportWindow ago), keeps the hash readable for DedupTTL,
// so that a processing backlog does not lose the lease of a report accepted
// in time. Late reports never extend the retention.
func (g *Ingestor) lookupLeases(ctx context.Context, candidates []candidate, at time.Time) (map[string]bool, error) {
	type leaseRef struct {
		ns      string
		key     string
		reports int
	}
	ids := make([]string, 0, len(candidates))
	refs := make(map[string]*leaseRef, len(candidates))
	bySite := make(map[int64][]string, 2)
	siteOrder := make([]int64, 0, 2)
	for _, c := range candidates {
		id := c.report.GetLeaseId()
		if ref, dup := refs[id]; dup {
			ref.reports++
			continue
		}
		refs[id] = &leaseRef{ns: c.ns.ID, key: g.keys.Lease(c.ref.SiteKey, id), reports: 1}
		ids = append(ids, id)
		if _, seen := bySite[c.ref.SiteKey]; !seen {
			siteOrder = append(siteOrder, c.ref.SiteKey)
		}
		bySite[c.ref.SiteKey] = append(bySite[c.ref.SiteKey], id)
	}
	now := strconv.FormatInt(at.UnixMilli(), 10)
	retention := strconv.FormatInt(g.cfg.DedupTTL.Milliseconds(), 10)
	late := strconv.FormatInt(g.cfg.LateReportWindow.Milliseconds(), 10)

	// One call per site (all its lease keys share the site hash tag, so the
	// batch is single-slot under Redis Cluster), chunked so that no single
	// script occupies the server for longer than a reap batch does.
	var calls []rueidis.LuaExec
	var batches [][]string
	for _, siteKey := range siteOrder {
		siteIDs := bySite[siteKey]
		for start := 0; start < len(siteIDs); start += maxRetainBatch {
			chunk := siteIDs[start:min(start+maxRetainBatch, len(siteIDs))]
			keys := make([]string, len(chunk))
			args := make([]string, 0, len(chunk)+4)
			args = append(args, refs[chunk[0]].ns, now, retention, late)
			for i, id := range chunk {
				keys[i] = refs[id].key
				args = append(args, strconv.Itoa(refs[id].reports))
			}
			calls = append(calls, rueidis.LuaExec{Keys: keys, Args: args})
			batches = append(batches, chunk)
		}
	}

	known := make(map[string]bool, len(ids))
	for i, res := range g.retain.ExecMulti(ctx, g.rdb, calls...) {
		found, err := res.AsStrSlice()
		if err != nil {
			return nil, g.unavailable("read leases", err)
		}
		if len(found) != len(batches[i]) {
			return nil, g.unavailable("read leases",
				fmt.Errorf("lease_retain returned %d results for %d leases", len(found), len(batches[i])))
		}
		for j, ns := range found {
			if ns != "" {
				known[batches[i][j]] = ns == refs[batches[i][j]].ns
			}
		}
	}
	return known, nil
}

// maxRetainBatch bounds the lease hashes one lease_retain.lua call touches.
// Batching removes about 4 us of per-lease dispatch and prelude, but a script
// holds the single Valkey thread for its whole run, so a 500-report request is
// split rather than blocking every other client for the length of 500 leases.
const maxRetainBatch = 100

// groupByShard encodes the reports whose lease exists, grouped by stream
// shard in first-seen order; the others are appended to rejected.
func (g *Ingestor) groupByShard(p *authz.Principal, node string, candidates []candidate, known map[string]bool, rejected []Rejected, now time.Time) ([]int, map[int][]shardItem, []Rejected) {
	shards := make(map[int][]shardItem, 4)
	order := make([]int, 0, 4)
	for _, c := range candidates {
		if !known[c.report.GetLeaseId()] {
			rejected = append(rejected, reject(c.report, apperr.ReasonLeaseUnknown, "lease is unknown or expired"))
			continue
		}
		data, err := EncodeEvent(buildEvent(p, node, c, now))
		if err != nil {
			rejected = append(rejected, reject(c.report, apperr.ReasonInvalidArgument, err.Error()))
			continue
		}
		if _, seen := shards[c.ref.Shard]; !seen {
			order = append(order, c.ref.Shard)
		}
		shards[c.ref.Shard] = append(shards[c.ref.Shard], shardItem{reportID: c.report.GetReportId(), data: data})
	}
	return order, shards, rejected
}

// shardItem is one encoded report bound for a shard.
type shardItem struct {
	reportID string
	data     []byte
}

// enqueue runs ingest.lua once per shard in a single pipeline.
func (g *Ingestor) enqueue(ctx context.Context, order []int, shards map[int][]shardItem) (accepted, duplicated int, err error) {
	ttl := strconv.FormatInt(g.cfg.DedupTTL.Milliseconds(), 10)
	maxLen := strconv.FormatInt(g.cfg.StreamMaxLen, 10)
	calls := make([]rueidis.LuaExec, len(order))
	for i, shard := range order {
		items := shards[shard]
		keys := make([]string, 0, len(items)+1)
		args := make([]string, 0, len(items)+2)
		keys = append(keys, g.keys.Stream(shard))
		args = append(args, ttl, maxLen)
		for _, it := range items {
			keys = append(keys, g.keys.Dedup(shard, it.reportID))
			args = append(args, string(it.data))
		}
		calls[i] = rueidis.LuaExec{Keys: keys, Args: args}
	}
	var firstErr error
	for i, res := range g.script.ExecMulti(ctx, g.rdb, calls...) {
		flags, execErr := res.AsIntSlice()
		if execErr == nil && len(flags) != len(shards[order[i]]) {
			execErr = fmt.Errorf("ingest script returned %d results for %d reports", len(flags), len(shards[order[i]]))
		}
		if execErr != nil {
			if firstErr == nil {
				firstErr = execErr
			}
			continue
		}
		for _, f := range flags {
			if f == 1 {
				accepted++
			} else {
				duplicated++
			}
		}
	}
	if firstErr != nil {
		return accepted, duplicated, g.unavailable("enqueue reports", firstErr)
	}
	return accepted, duplicated, nil
}

// finish records metrics and rejected counts.
func (g *Ingestor) finish(p *authz.Principal, node string, accepted, duplicated, rejected int) {
	if g.metrics != nil {
		if accepted > 0 {
			g.metrics.ReportIngestTotal.WithLabelValues(metricResultAccepted).Add(float64(accepted))
		}
		if duplicated > 0 {
			g.metrics.ReportIngestTotal.WithLabelValues(metricResultDup).Add(float64(duplicated))
		}
		if rejected > 0 {
			g.metrics.ReportIngestTotal.WithLabelValues(metricResultRejected).Add(float64(rejected))
		}
	}
	if rejected > 0 && g.rec != nil && p.NamespaceID != "" {
		g.rec.RecordRejectedReports(p.NamespaceID, node, rejected)
	}
}

func (g *Ingestor) unavailable(what string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	g.logger.Error("report ingest failed", slog.String("op", what), slog.Any("error", err))
	return apperr.Unavailable(apperr.ReasonInternal, ingestUnavailableMs, "report queue temporarily unavailable").
		WithCause(fmt.Errorf("%s: %w", what, err))
}

// buildEvent converts a validated report into its stream event.
func buildEvent(p *authz.Principal, node string, c candidate, now time.Time) Event {
	r := c.report
	e := Event{
		ReportID:      r.GetReportId(),
		LeaseID:       r.GetLeaseId(),
		NamespaceID:   c.ns.ID,
		TenantID:      c.ns.TenantID,
		Node:          node,
		ReceivedAt:    now.UnixMilli(),
		URI:           r.GetUri(),
		Method:        r.GetMethod(),
		HTTPStatus:    int(r.GetHttpStatus()),
		BusinessCode:  r.GetBusinessCode(),
		ErrorKind:     r.GetErrorKind(),
		Markers:       append(make([]string, 0, len(r.GetMarkers())), r.GetMarkers()...),
		OutcomeHint:   r.GetOutcomeHint(),
		LatencyMs:     int64(r.GetLatencyMs()),
		ResponseBytes: r.GetResponseBytes(),
		Release:       r.GetRelease(),
	}
	if p.Kind == authz.KindToken {
		e.TokenID = p.ID
	}
	if ts := r.GetStartedAt(); ts != nil {
		e.StartedAt = ts.AsTime().UnixMilli()
	}
	if ts := r.GetFinishedAt(); ts != nil {
		e.FinishedAt = ts.AsTime().UnixMilli()
	}
	return e
}

func reject(r *spinneretv1.Report, reason apperr.Reason, msg string) Rejected {
	return Rejected{ReportID: r.GetReportId(), Reason: reason, Message: msg}
}

// validationMessage renders a protovalidate error compactly.
func validationMessage(err error) string {
	var verr *protovalidate.ValidationError
	msg := err.Error()
	if errors.As(err, &verr) && len(verr.Violations) > 0 {
		parts := make([]string, 0, len(verr.Violations))
		for _, v := range verr.Violations {
			parts = append(parts, v.String())
		}
		msg = strings.Join(parts, "; ")
	}
	return truncate(msg, maxRejectMessageLen)
}

// sanitizeNode keeps printable ASCII and caps the length.
func sanitizeNode(node string) string {
	node = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, strings.TrimSpace(node))
	return truncate(node, maxNodeLen)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
