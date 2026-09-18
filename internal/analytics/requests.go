package analytics

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	chstore "github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
)

// RequestEventQuery filters and pages raw report events. Empty filters match
// everything.
type RequestEventQuery struct {
	SiteID          string
	Client          string
	EndpointGroupID string
	Outcomes        []string
	IdentityID      string
	ProxyID         string
	Node            string
	LeaseID         string
	ReportID        string
	// HTTPStatus filters by status when not nil (0 = no response).
	HTTPStatus *int
	// MinLatencyMs keeps events with at least this latency (0 = no bound).
	MinLatencyMs int64
	// Range defaults to the last hour and may span at most 7 days.
	Range TimeRange
	// PageSize is the maximum number of events (0 = default 50, at most 500).
	PageSize int
	// Cursor continues after the last event of a previous page.
	Cursor *RequestCursor
	// IncludeSummary adds a summary over every matching event.
	IncludeSummary bool
}

// RequestCursor is the keyset position of a request event page.
type RequestCursor struct {
	EventTimeMs int64  `json:"t"`
	ReportID    string `json:"r"`
	LeaseID     string `json:"l"`
}

// RequestEvent is one processed report stored in ClickHouse.
type RequestEvent struct {
	EventTime     time.Time
	ReceivedAt    time.Time
	StartedAt     time.Time
	SiteID        string
	Site          string
	Client        string
	EndpointGroup string
	IdentityID    string
	IdentityType  string
	ProxyID       string
	LeaseID       string
	ReportID      string
	Node          string
	TokenID       string
	URI           string
	Method        string
	HTTPStatus    int32
	BusinessCode  string
	ErrorKind     string
	Markers       []string
	Outcome       string
	OutcomeHint   string
	Blame         string
	Rule          string
	LatencyMs     int64
	ResponseBytes int64
	Suppressed    bool
	Late          bool
	Probe         bool
}

// RequestEventsSummary aggregates the events matching a query.
type RequestEventsSummary struct {
	Total        int64
	Outcomes     map[string]int64
	LatencyAvgMs float64
	LatencyP50Ms float64
	LatencyP95Ms float64
	LatencyP99Ms float64
}

// RequestEventPage is one page of request events, newest first.
type RequestEventPage struct {
	Events []RequestEvent
	// Next is the cursor of the next page; nil when there are no more events.
	Next *RequestCursor
	// Summary is set when the query asked for it.
	Summary *RequestEventsSummary
}

// requestColumns are the report_events columns returned per event, in scan order.
const requestColumns = "event_time, received_at, started_at, site_id, site, client, endpoint_group, identity_id, " +
	"identity_type, proxy_id, lease_id, report_id, node, token_id, uri, method, http_status, business_code, " +
	"error_kind, markers, outcome, outcome_hint, blame, rule, latency_ms, response_bytes, suppressed, late, probe"

// chFilter accumulates WHERE conditions with server-side query parameters.
// Conditions are code constants; every user value travels as a parameter.
type chFilter struct {
	conds []string
	args  []any
}

// param binds value as a new server-side query parameter of type typ and
// returns its "{pN:typ}" placeholder.
func (f *chFilter) param(typ string, value any) string {
	name := "p" + strconv.Itoa(len(f.args))
	f.args = append(f.args, clickhouse.Named(name, value))
	return "{" + name + ":" + typ + "}"
}

// cond appends a condition built from code constants and placeholders.
func (f *chFilter) cond(c string) {
	f.conds = append(f.conds, c)
}

func (f *chFilter) clone() chFilter {
	return chFilter{conds: slices.Clone(f.conds), args: slices.Clone(f.args)}
}

func (f *chFilter) where() string {
	return strings.Join(f.conds, " AND ")
}

// RequestEvents searches the report events of the readable sites in
// ClickHouse, newest first. It fails with unavailable when ClickHouse is not
// configured.
func (s *Service) RequestEvents(ctx context.Context, scope Scope, q RequestEventQuery) (RequestEventPage, error) {
	if s.ch == nil {
		return RequestEventPage{}, apperr.Unavailable(apperr.ReasonFailedPrecondition, 0,
			"request events are unavailable: ClickHouse is not configured")
	}
	rs, err := s.resolve(scope)
	if err != nil {
		return RequestEventPage{}, err
	}
	start, end, err := q.Range.resolve(s.now(), DefaultRequestEventRange, MaxRequestEventRange)
	if err != nil {
		return RequestEventPage{}, err
	}
	base, empty, err := requestFilter(rs, q, start, end)
	if err != nil {
		return RequestEventPage{}, err
	}
	page := RequestEventPage{Events: []RequestEvent{}}
	if q.IncludeSummary {
		page.Summary = &RequestEventsSummary{Outcomes: map[string]int64{}}
	}
	if empty {
		return page, nil
	}
	pageSize := normalizePageSize(q.PageSize)

	ctx, cancel := context.WithTimeout(ctx, s.chTimeout)
	defer cancel()
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
		"max_execution_time": int(math.Ceil(s.chTimeout.Seconds())),
		// Keep one console query well inside the server's memory budget: the
		// explorer reads wide columns, and without a cap a wide time range can
		// exhaust max_server_memory_usage and take the whole node down with it.
		"max_memory_usage": chQueryMemoryBytes,
		"max_threads":      chQueryThreads,
		// Read in sort-key order so the page query streams instead of buffering.
		"optimize_read_in_order": 1,
	}))

	pageFilter := base.clone()
	if q.Cursor != nil {
		if q.Cursor.EventTimeMs <= 0 {
			return RequestEventPage{}, apperr.InvalidArgument("", "invalid page_token")
		}
		cursorAt := pageFilter.param("Int64", q.Cursor.EventTimeMs)
		pageFilter.cond("event_time <= fromUnixTimestamp64Milli(" + cursorAt + ")")
		pageFilter.cond("(event_time, report_id, lease_id) < (fromUnixTimestamp64Milli(" + cursorAt + "), " +
			pageFilter.param("String", q.Cursor.ReportID) + ", " + pageFilter.param("String", q.Cursor.LeaseID) + ")")
	}
	events, err := s.queryRequestEvents(ctx, pageFilter, pageSize+1)
	if err != nil {
		return RequestEventPage{}, err
	}
	if len(events) > pageSize {
		events = events[:pageSize]
		last := events[pageSize-1]
		page.Next = &RequestCursor{EventTimeMs: last.EventTime.UnixMilli(), ReportID: last.ReportID, LeaseID: last.LeaseID}
	}
	page.Events = events
	if q.IncludeSummary {
		if page.Summary, err = s.querySummary(ctx, base); err != nil {
			return RequestEventPage{}, err
		}
	}
	return page, nil
}

// Per-query ClickHouse budget for console analytics. It must stay well below
// max_server_memory_usage so that one explorer query cannot starve the report
// writers; queries that need more fail with query_too_large instead.
const (
	chQueryMemoryBytes = 512 << 20
	chQueryThreads     = 2
)

// requestFilter builds the conditions shared by the page and summary queries.
// empty is true when no event can match (no readable site).
func requestFilter(rs *resolvedScope, q RequestEventQuery, start, end time.Time) (chFilter, bool, error) {
	var f chFilter
	for _, o := range q.Outcomes {
		if !policy.ValidOutcome(o) {
			return f, false, apperr.InvalidArgument("", "unsupported outcome %q", o)
		}
	}
	if q.HTTPStatus != nil && (*q.HTTPStatus < 0 || *q.HTTPStatus > math.MaxUint16) {
		return f, false, apperr.InvalidArgument("", "http_status is out of range")
	}
	if q.MinLatencyMs < 0 {
		return f, false, apperr.InvalidArgument("", "min_latency_ms must not be negative")
	}
	f.cond("namespace_id = " + f.param("String", rs.ns.ID))
	f.cond("event_time >= fromUnixTimestamp64Milli(" + f.param("Int64", start.UnixMilli()) + ")")
	f.cond("event_time < fromUnixTimestamp64Milli(" + f.param("Int64", ceilMillis(end)) + ")")

	switch {
	case q.EndpointGroupID != "":
		g, site, err := rs.group(q.EndpointGroupID)
		if err != nil {
			return f, false, err
		}
		if q.SiteID != "" && q.SiteID != site.ID {
			return f, false, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group does not belong to the site")
		}
		if q.Client != "" && q.Client != g.Client {
			return f, false, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown, "endpoint group does not belong to the client")
		}
		f.cond("site_id = " + f.param("String", site.ID))
		f.cond("client = " + f.param("String", g.Client))
		f.cond("endpoint_group = " + f.param("String", g.Name))
	case q.SiteID != "":
		site, err := rs.site(q.SiteID)
		if err != nil {
			return f, false, err
		}
		f.cond("site_id = " + f.param("String", site.ID))
	case !rs.allSites:
		if len(rs.sites) == 0 {
			return f, true, nil
		}
		f.cond("has(" + f.param("Array(String)", rs.siteIDs()) + ", site_id)")
	}
	if q.Client != "" && q.EndpointGroupID == "" {
		f.cond("client = " + f.param("String", q.Client))
	}
	if len(q.Outcomes) > 0 {
		f.cond("has(" + f.param("Array(String)", slices.Clone(q.Outcomes)) + ", outcome)")
	}
	for _, eq := range []struct{ column, value string }{
		{"identity_id", q.IdentityID}, {"proxy_id", q.ProxyID}, {"node", q.Node},
		{"lease_id", q.LeaseID}, {"report_id", q.ReportID},
	} {
		if eq.value != "" {
			f.cond(eq.column + " = " + f.param("String", eq.value))
		}
	}
	if q.HTTPStatus != nil {
		f.cond("http_status = " + f.param("UInt16", uint16(*q.HTTPStatus)))
	}
	if q.MinLatencyMs > 0 {
		f.cond("latency_ms >= " + f.param("UInt64", uint64(q.MinLatencyMs)))
	}
	return f, false, nil
}

// ceilMillis returns t in Unix milliseconds rounded up.
func ceilMillis(t time.Time) int64 {
	ms := t.UnixMilli()
	if t.Sub(time.UnixMilli(ms)) > 0 {
		ms++
	}
	return ms
}

// queryRequestEvents reads at most limit events matching f, newest first.
func (s *Service) queryRequestEvents(ctx context.Context, f chFilter, limit int) ([]RequestEvent, error) {
	query := "SELECT " + requestColumns + " FROM " + chstore.ReportEventsTable +
		" WHERE " + f.where() +
		" ORDER BY event_time DESC, report_id DESC, lease_id DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.ch.Query(ctx, query, f.args...)
	if err != nil {
		return nil, chError(err, "query request events")
	}
	defer func() { _ = rows.Close() }()
	out := make([]RequestEvent, 0, min(limit, MaxEventPageSize+1))
	for rows.Next() {
		var (
			ev                      RequestEvent
			httpStatus              uint16
			latency                 uint32
			responseBytes           uint64
			suppressed, late, probe uint8
		)
		if err := rows.Scan(&ev.EventTime, &ev.ReceivedAt, &ev.StartedAt, &ev.SiteID, &ev.Site, &ev.Client,
			&ev.EndpointGroup, &ev.IdentityID, &ev.IdentityType, &ev.ProxyID, &ev.LeaseID, &ev.ReportID, &ev.Node,
			&ev.TokenID, &ev.URI, &ev.Method, &httpStatus, &ev.BusinessCode, &ev.ErrorKind, &ev.Markers,
			&ev.Outcome, &ev.OutcomeHint, &ev.Blame, &ev.Rule, &latency, &responseBytes,
			&suppressed, &late, &probe); err != nil {
			return nil, fmt.Errorf("scan request event: %w", err)
		}
		ev.EventTime, ev.ReceivedAt, ev.StartedAt = ev.EventTime.UTC(), ev.ReceivedAt.UTC(), ev.StartedAt.UTC()
		ev.HTTPStatus = int32(httpStatus)
		ev.LatencyMs = int64(latency)
		ev.ResponseBytes = int64(min(responseBytes, math.MaxInt64))
		ev.Suppressed, ev.Late, ev.Probe = suppressed != 0, late != 0, probe != 0
		if ev.Markers == nil {
			ev.Markers = []string{}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, chError(err, "read request events")
	}
	return out, nil
}

// querySummary aggregates every event matching f: counts per outcome, average
// latency and the 50th/95th/99th latency percentiles (approximate quantiles).
func (s *Service) querySummary(ctx context.Context, f chFilter) (*RequestEventsSummary, error) {
	out := &RequestEventsSummary{Outcomes: map[string]int64{}}
	rows, err := s.ch.Query(ctx,
		"SELECT outcome, count(), sum(latency_ms) FROM "+chstore.ReportEventsTable+" WHERE "+f.where()+" GROUP BY outcome",
		f.args...)
	if err != nil {
		return nil, chError(err, "summarize request events")
	}
	defer func() { _ = rows.Close() }()
	var latencySum uint64
	for rows.Next() {
		var (
			outcome string
			count   uint64
			latency uint64
		)
		if err := rows.Scan(&outcome, &count, &latency); err != nil {
			return nil, fmt.Errorf("scan request event summary: %w", err)
		}
		n := int64(min(count, math.MaxInt64))
		out.Outcomes[outcome] += n
		out.Total += n
		latencySum += latency
	}
	if err := rows.Err(); err != nil {
		return nil, chError(err, "read request event summary")
	}
	if out.Total == 0 {
		return out, nil
	}
	out.LatencyAvgMs = float64(latencySum) / float64(out.Total)

	var quantiles []float64
	row := s.ch.QueryRow(ctx,
		"SELECT quantiles(0.5, 0.95, 0.99)(latency_ms) FROM "+chstore.ReportEventsTable+" WHERE "+f.where(),
		f.args...)
	if err := row.Scan(&quantiles); err != nil {
		return nil, chError(err, "compute request latency percentiles")
	}
	if len(quantiles) == 3 {
		out.LatencyP50Ms, out.LatencyP95Ms, out.LatencyP99Ms = finite(quantiles[0]), finite(quantiles[1]), finite(quantiles[2])
	}
	return out, nil
}

// finite maps NaN and infinities to 0.
func finite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
