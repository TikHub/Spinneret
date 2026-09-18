package hotstate

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// healthEntry is the decoded packed identity × endpoint-group state
// "score|sts|samples|nfail|lastfail|cd|ru|lu" (spec §5.1).
type healthEntry struct {
	Score    float64
	ScoreTS  int64
	Samples  int64
	NFail    int64
	LastFail int64
	Cooldown int64
	Reuse    int64
	LastUsed int64
}

// parseHealth decodes a packed health value. Missing or malformed fields take
// the defaults of spec §5.1 (score = baseline, sts = now, others 0).
func parseHealth(packed string, baseline float64, nowMs int64) healthEntry {
	parts := strings.Split(packed, "|")
	field := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	return healthEntry{
		Score:    parseFloat(field(0), baseline),
		ScoreTS:  parseInt(field(1), nowMs),
		Samples:  parseInt(field(2), 0),
		NFail:    parseInt(field(3), 0),
		LastFail: parseInt(field(4), 0),
		Cooldown: parseInt(field(5), 0),
		Reuse:    parseInt(field(6), 0),
		LastUsed: parseInt(field(7), 0),
	}
}

// packHealth encodes a health entry exactly like sp_hs_pack in common.lua.
func packHealth(h healthEntry) string {
	var b strings.Builder
	b.Grow(64)
	score := strconv.FormatFloat(h.Score, 'f', 2, 64)
	if score == "-0.00" {
		score = "0.00"
	}
	b.WriteString(score)
	for _, v := range []int64{h.ScoreTS, h.Samples, h.NFail, h.LastFail, h.Cooldown, h.Reuse, h.LastUsed} {
		b.WriteByte('|')
		b.WriteString(strconv.FormatInt(v, 10))
	}
	return b.String()
}

// decayScore applies the lazy regression towards baseline of spec §6.4.
func decayScore(score float64, scoreTS, nowMs int64, baseline float64, tau time.Duration) float64 {
	tauMs := float64(tau.Milliseconds())
	dt := float64(nowMs - scoreTS)
	if tauMs <= 0 || dt <= 0 {
		return score
	}
	return baseline + (score-baseline)*math.Exp(-dt/tauMs)
}

// parseInt parses a base-10 integer field; empty or malformed values yield def.
// Values with a fractional part (for example "12.0") are truncated.
func parseInt(v string, def int64) int64 {
	if v == "" {
		return def
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return int64(f)
}

// parseFloat parses a float field; empty or malformed values yield def.
func parseFloat(v string, def float64) float64 {
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return def
	}
	return f
}

// msToTime converts Unix milliseconds to a UTC time; values <= 0 yield the
// zero time.
func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// maxTimestampMs is 9999-12-31T23:59:59.999Z, the latest instant written to
// PostgreSQL. Larger values can only come from corrupt hot state; clamping them
// keeps a single bad field from failing (and endlessly retrying) a whole batch.
const maxTimestampMs int64 = 253402300799999

// msToTimePtr converts Unix milliseconds to a time pointer; values <= 0 yield
// nil and values beyond maxTimestampMs are clamped.
func msToTimePtr(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(min(ms, maxTimestampMs)).UTC()
	return &t
}

// clampInt32 converts v to int32, saturating at the int32 bounds (the
// PostgreSQL integer columns of hot_state_snapshots).
func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v)
}

// timeMs returns the Unix milliseconds of t, or 0 when t is nil.
func timeMs(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

// itoa formats an int64 as base-10.
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

// csvInt64 joins integers with commas.
func csvInt64(values []int64) string {
	var b strings.Builder
	b.Grow(len(values) * 8)
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(v, 10))
	}
	return b.String()
}

// parseCSVInt64 parses a comma-separated list of integers, skipping malformed items.
func parseCSVInt64(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.ParseInt(p, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// tagList encodes proxy tags as ",tag1,tag2," (spec §5.4); no tags encode as "".
func tagList(tags []string) string {
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	return "," + strings.Join(clean, ",") + ","
}

// sortedInt64 returns a sorted copy of values without duplicates.
func sortedInt64(values []int64) []int64 {
	out := append([]int64(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for i, v := range out {
		if i == 0 || v != out[n-1] {
			out[n] = v
			n++
		}
	}
	return out[:n]
}

// dedupeStrings returns values without duplicates and empty strings, keeping order.
func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// chunkStrings splits values into chunks of at most size elements.
func chunkStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		out = append(out, values[start:min(start+size, len(values))])
	}
	return out
}

// chunkInt64 splits values into chunks of at most size elements.
func chunkInt64(values []int64, size int) [][]int64 {
	if len(values) == 0 {
		return nil
	}
	out := make([][]int64, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		out = append(out, values[start:min(start+size, len(values))])
	}
	return out
}
