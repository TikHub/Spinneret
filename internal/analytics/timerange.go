package analytics

import (
	"strconv"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// Time range limits and defaults.
const (
	// MaxAggregateRange is the longest range of aggregate and risk event queries.
	MaxAggregateRange = 31 * 24 * time.Hour
	// MaxRequestEventRange is the longest range of ClickHouse request event queries.
	MaxRequestEventRange = 7 * 24 * time.Hour
	// DefaultTimeSeriesRange is used when a time series query has no start.
	DefaultTimeSeriesRange = time.Hour
	// DefaultRiskEventRange is used when a risk event query has no start.
	DefaultRiskEventRange = 24 * time.Hour
	// DefaultRequestEventRange is used when a request event query has no start.
	DefaultRequestEventRange = time.Hour
	// DefaultNodeStatsRange is used when a node statistics query has no start.
	DefaultNodeStatsRange = time.Hour
	// MaxSeriesPoints is the largest number of buckets of one series.
	MaxSeriesPoints = 720
)

// TimeRange is a half-open interval [Start, End). Nil bounds take defaults:
// End defaults to now and Start to End minus the query's default length.
type TimeRange struct {
	Start *time.Time
	End   *time.Time
}

// resolve applies the defaults and validates ordering and length.
func (r TimeRange) resolve(now time.Time, def, maxLen time.Duration) (time.Time, time.Time, error) {
	end := now
	if r.End != nil {
		end = *r.End
	}
	start := end.Add(-def)
	if r.Start != nil {
		start = *r.Start
	}
	start, end = start.UTC(), end.UTC()
	if !end.After(start) {
		return time.Time{}, time.Time{}, apperr.InvalidArgument("", "time_range.end must be after time_range.start")
	}
	if end.Sub(start) > maxLen {
		return time.Time{}, time.Time{}, apperr.InvalidArgument("", "time range must not exceed %s", formatRangeLimit(maxLen))
	}
	return start, end, nil
}

// formatRangeLimit renders a range limit in days when it is a whole number of days.
func formatRangeLimit(d time.Duration) string {
	const day = 24 * time.Hour
	if d%day == 0 {
		days := int64(d / day)
		if days == 1 {
			return "1 day"
		}
		return strconv.FormatInt(days, 10) + " days"
	}
	return d.String()
}

// Steps are the bucket sizes of time series, smallest first.
var steps = []struct {
	name string
	d    time.Duration
}{
	{"1m", time.Minute},
	{"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
	{"6h", 6 * time.Hour},
	{"1d", 24 * time.Hour},
}

// ParseStep parses a time series step name ("1m", "5m", "15m", "1h", "6h",
// "1d"); the empty string yields 0 (automatic).
func ParseStep(name string) (time.Duration, error) {
	if name == "" {
		return 0, nil
	}
	for _, st := range steps {
		if st.name == name {
			return st.d, nil
		}
	}
	return 0, apperr.InvalidArgument("", "unsupported step %q", name)
}

// StepName returns the name of a supported step, or the duration string.
func StepName(d time.Duration) string {
	for _, st := range steps {
		if st.d == d {
			return st.name
		}
	}
	return d.String()
}

// alignDown returns the start of the step-aligned bucket containing t
// (buckets are aligned to the Unix epoch).
func alignDown(t time.Time, step time.Duration) time.Time {
	sec := int64(step / time.Second)
	unix := t.Unix()
	q := unix / sec
	if unix%sec != 0 && unix < 0 {
		q--
	}
	return time.Unix(q*sec, 0).UTC()
}

// alignUp returns the smallest step-aligned instant not before t.
func alignUp(t time.Time, step time.Duration) time.Time {
	down := alignDown(t, step)
	if down.Equal(t) {
		return down
	}
	return down.Add(step)
}

// bucketCount returns the number of step buckets covering [start, end).
func bucketCount(start, end time.Time, step time.Duration) int64 {
	return int64(alignUp(end, step).Sub(alignDown(start, step)) / step)
}

// chooseStep returns the smallest supported step, not smaller than requested
// (0 = automatic), whose bucket count over [start, end) is at most
// MaxSeriesPoints.
func chooseStep(start, end time.Time, requested time.Duration) (time.Duration, error) {
	for _, st := range steps {
		if st.d < requested {
			continue
		}
		if bucketCount(start, end, st.d) <= MaxSeriesPoints {
			return st.d, nil
		}
	}
	return 0, apperr.InvalidArgument("", "time range is too long for the requested step")
}

// Overview windows accepted by Overview.
var windows = []struct {
	name string
	d    time.Duration
}{
	{"1m", time.Minute},
	{"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
}

// DefaultWindow is the overview window used when none is requested.
const DefaultWindow = 5 * time.Minute

// ParseWindow parses an overview window name ("1m", "5m", "15m", "1h"); the
// empty string yields DefaultWindow.
func ParseWindow(name string) (time.Duration, error) {
	if name == "" {
		return DefaultWindow, nil
	}
	for _, w := range windows {
		if w.name == name {
			return w.d, nil
		}
	}
	return 0, apperr.InvalidArgument("", "unsupported window %q", name)
}

// WindowName returns the name of a supported overview window, or the duration string.
func WindowName(d time.Duration) string {
	for _, w := range windows {
		if w.d == d {
			return w.name
		}
	}
	return d.String()
}

// minuteFloor truncates t to the start of its UTC minute.
func minuteFloor(t time.Time) time.Time {
	return alignDown(t, time.Minute)
}
