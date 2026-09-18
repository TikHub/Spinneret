package stats

import (
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Text length limits (bytes) applied at record time. Identifiers are
// server-generated and short; the limits only bound memory and index key sizes
// when a caller passes unexpected input.
const (
	maxIDBytes       = 128
	maxNodeBytes     = 128
	maxEnumBytes     = 64
	maxURIBytes      = 4096
	maxShortBytes    = 256
	maxMarkers       = 64
	maxMarkerBytes   = 128
	secondsPerMinute = 60
	secondsPerHour   = 3600
	secondsPerDay    = 86400
)

// Plausibility window of node-reported finish times relative to the server
// receive time; outside of it the receive time is used as the event time.
const (
	maxReportClockSkew = 5 * time.Minute
	maxReportAge       = 24 * time.Hour
)

// cleanText returns s as valid UTF-8 without NUL bytes, truncated to at most
// limit bytes on a rune boundary. The common case (already clean and short)
// returns s without allocating.
func cleanText(s string, limit int) string {
	if len(s) <= limit && strings.IndexByte(s, 0) < 0 && utf8.ValidString(s) {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// cleanMarkers returns a sanitized copy of markers bounded in count and size,
// so the buffered row never aliases the caller's slice.
func cleanMarkers(markers []string) []string {
	n := min(len(markers), maxMarkers)
	out := make([]string, n)
	for i := range n {
		out[i] = cleanText(markers[i], maxMarkerBytes)
	}
	return out
}

// nodeName normalizes a node name for node_stats_minutely.
func nodeName(node string) string {
	if node == "" {
		return EmptyNode
	}
	return cleanText(node, maxNodeBytes)
}

// floorDiv returns the floor of a/b for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// minuteBucket returns the start of t's UTC minute in Unix seconds.
func minuteBucket(t time.Time) int64 {
	return floorDiv(t.Unix(), secondsPerMinute) * secondsPerMinute
}

// hourBucket returns the start of t's UTC hour in Unix seconds.
func hourBucket(t time.Time) int64 {
	return floorDiv(t.Unix(), secondsPerHour) * secondsPerHour
}

// dayStart returns the start of the UTC day containing unix.
func dayStart(unix int64) int64 {
	return floorDiv(unix, secondsPerDay) * secondsPerDay
}

// monthStart returns the start of the UTC month containing unix.
func monthStart(unix int64) int64 {
	t := time.Unix(unix, 0).UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
}

// unixTime converts a bucket in Unix seconds to a UTC time.
func unixTime(unix int64) time.Time {
	return time.Unix(unix, 0).UTC()
}

// reportEventTime returns the time a report is attributed to: FinishedAt, or
// received when FinishedAt is zero or implausibly far from the receive time.
func reportEventTime(finished, received time.Time) time.Time {
	if finished.IsZero() {
		return received
	}
	if finished.After(received.Add(maxReportClockSkew)) || finished.Before(received.Add(-maxReportAge)) {
		return received
	}
	return finished
}

// nonNegative clamps v to >= 0.
func nonNegative(v int64) int64 {
	return max(v, 0)
}

// clampInt32 clamps v to [0, MaxInt32] for integer columns.
func clampInt32(v int64) int32 {
	return int32(min(nonNegative(v), math.MaxInt32))
}

// clampUint16 clamps v to [0, MaxUint16].
func clampUint16(v int) uint16 {
	return uint16(min(max(v, 0), math.MaxUint16))
}

// clampUint32 clamps v to [0, MaxUint32].
func clampUint32(v int64) uint32 {
	return uint32(min(nonNegative(v), math.MaxUint32))
}

// optionalTime returns nil for the zero time and a pointer to the UTC time otherwise.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}
