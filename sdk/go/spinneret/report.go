package spinneret

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Report field limits enforced by the server.
const (
	maxReportIDLength     = 64
	maxLeaseIDLength      = 128
	maxMethodLength       = 16
	maxBusinessCodeLength = 64
	maxMarkers            = 32
	maxMarkerLength       = 64
	maxOutcomeHintLength  = 32
)

// ReportInput describes the observed result of one request made with a lease.
// Only the fields that apply need to be set.
type ReportInput struct {
	// HTTPStatus is the response status; 0 when no response was received.
	HTTPStatus int
	// Method is the HTTP method, e.g. "GET".
	Method string
	// URI is the requested path or URL; defaults to the URI the lease was
	// acquired for. The query string and fragment are removed before sending.
	URI string
	// BusinessCode is the business status code extracted from the response body.
	BusinessCode string
	// ErrorKind is the transport error kind (see [ClassifyError]).
	ErrorKind string
	// Markers are response features, e.g. "captcha_page" or "empty_list".
	Markers []string
	// OutcomeHint is the outcome proposed by the node.
	OutcomeHint string
	// Latency is the request latency; derived from StartedAt when zero.
	Latency time.Duration
	// ResponseBytes is the response body size.
	ResponseBytes int64
	// StartedAt is the start of the request; defaults to FinishedAt - Latency.
	StartedAt time.Time
	// FinishedAt is the end of the request; defaults to now.
	FinishedAt time.Time
	// ReportID is the idempotency key; defaults to a random UUID.
	ReportID string
	// Release releases the lease with this report.
	Release bool
}

// NewReportID returns a random UUID (version 4) for Report.report_id.
func NewReportID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}

// ReportURI reduces a request URI or absolute URL to the path reported to the
// server. The query string and fragment are removed (endpoint groups match on
// the path, and query parameters often carry signed credential values) and
// the result is cut to 2048 characters. An empty input stays empty.
func ReportURI(uri string) string {
	if uri == "" {
		return ""
	}
	var path string
	lower := strings.ToLower(uri)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if u, err := url.Parse(uri); err == nil {
			path = u.EscapedPath()
		} else {
			rest := uri[strings.Index(uri, "//")+2:]
			if i := strings.IndexAny(rest, "/?#"); i >= 0 && rest[i] == '/' {
				path = rest[i:]
			}
			path = cutQueryAndFragment(path)
		}
	} else {
		path = cutQueryAndFragment(uri)
	}
	if path == "" {
		path = "/"
	}
	return truncateRunes(path, MaxReportURILength)
}

func cutQueryAndFragment(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// buildReport converts an input into a Report for leaseID, defaulting the URI
// to defaultURI, and validates it against the server rules.
func buildReport(leaseID, defaultURI string, in ReportInput, now time.Time) (*Report, error) {
	uri := in.URI
	if uri == "" {
		uri = defaultURI
	}
	finished := in.FinishedAt
	if finished.IsZero() {
		finished = now
	}
	started := in.StartedAt
	latency := in.Latency
	switch {
	case started.IsZero() && latency > 0:
		started = finished.Add(-latency)
	case started.IsZero():
		started = finished
	case latency == 0:
		latency = max(0, finished.Sub(started))
	}
	reportID := in.ReportID
	if reportID == "" {
		reportID = NewReportID()
	}
	markers := make([]string, len(in.Markers))
	copy(markers, in.Markers)
	report := &Report{
		ReportId:      reportID,
		LeaseId:       leaseID,
		Uri:           ReportURI(uri),
		Method:        strings.ToUpper(strings.TrimSpace(in.Method)),
		HttpStatus:    int32(min(max(in.HTTPStatus, math.MinInt32), math.MaxInt32)),
		BusinessCode:  in.BusinessCode,
		ErrorKind:     in.ErrorKind,
		Markers:       markers,
		OutcomeHint:   in.OutcomeHint,
		LatencyMs:     int32(min(latency.Milliseconds(), math.MaxInt32)),
		ResponseBytes: in.ResponseBytes,
		StartedAt:     timestamppb.New(started),
		FinishedAt:    timestamppb.New(finished),
		Release:       in.Release,
	}
	if err := validateReport(report, started, finished, latency); err != nil {
		return nil, err
	}
	return report, nil
}

func invalidReport(format string, args ...any) *Error {
	return newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "invalid report: "+format, args...)
}

// validateReport applies the protovalidate rules of spinneret.v1.Report.
func validateReport(r *Report, started, finished time.Time, latency time.Duration) error {
	if !validReportID(r.GetReportId()) {
		return invalidReport("report_id must be 1..64 characters of [A-Za-z0-9_.:-]")
	}
	if n := utf8.RuneCountInString(r.GetLeaseId()); n == 0 || n > maxLeaseIDLength {
		return invalidReport("lease_id must be 1..%d characters", maxLeaseIDLength)
	}
	if r.GetUri() == "" {
		return invalidReport("uri is required (the lease was acquired without a uri)")
	}
	if utf8.RuneCountInString(r.GetMethod()) > maxMethodLength {
		return invalidReport("method must be at most %d characters", maxMethodLength)
	}
	if r.GetHttpStatus() < 0 || r.GetHttpStatus() > 999 {
		return invalidReport("http_status must be within 0..999")
	}
	if utf8.RuneCountInString(r.GetBusinessCode()) > maxBusinessCodeLength {
		return invalidReport("business_code must be at most %d characters", maxBusinessCodeLength)
	}
	if !validErrorKind(r.GetErrorKind()) {
		return invalidReport("unknown error_kind %q", r.GetErrorKind())
	}
	if len(r.GetMarkers()) > maxMarkers {
		return invalidReport("at most %d markers are allowed", maxMarkers)
	}
	for _, m := range r.GetMarkers() {
		if n := utf8.RuneCountInString(m); n == 0 || n > maxMarkerLength {
			return invalidReport("markers must be 1..%d characters", maxMarkerLength)
		}
	}
	if utf8.RuneCountInString(r.GetOutcomeHint()) > maxOutcomeHintLength {
		return invalidReport("outcome_hint must be at most %d characters", maxOutcomeHintLength)
	}
	if latency < 0 {
		return invalidReport("latency must not be negative")
	}
	if r.GetResponseBytes() < 0 {
		return invalidReport("response_bytes must not be negative")
	}
	if finished.Before(started) {
		return invalidReport("finished_at must not be before started_at")
	}
	return nil
}

func validReportID(id string) bool {
	if id == "" || len(id) > maxReportIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == ':' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func validErrorKind(kind string) bool {
	switch kind {
	case ErrorKindNone, ErrorKindTimeout, ErrorKindConnReset, ErrorKindConnRefused,
		ErrorKindProxyAuth, ErrorKindTLS, ErrorKindDNS, ErrorKindOther:
		return true
	default:
		return false
	}
}
