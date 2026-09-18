package stats

import (
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestCleanText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{name: "clean", in: "ns_abc", limit: 16, want: "ns_abc"},
		{name: "empty", in: "", limit: 16, want: ""},
		{name: "nul removed", in: "a\x00b\x00", limit: 16, want: "ab"},
		{name: "invalid utf8 replaced", in: "a\xffb", limit: 16, want: "a�b"},
		{name: "truncated", in: "abcdef", limit: 4, want: "abcd"},
		{name: "truncated on rune boundary", in: "abéé", limit: 3, want: "ab"},
		{name: "all fixes", in: "\x00\xffxyz", limit: 4, want: "�x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cleanText(tt.in, tt.limit)
			require.Equal(t, tt.want, got)
			require.True(t, utf8.ValidString(got))
			require.LessOrEqual(t, len(got), tt.limit)
		})
	}
}

func TestCleanMarkers(t *testing.T) {
	t.Parallel()
	require.Empty(t, cleanMarkers(nil))
	require.NotNil(t, cleanMarkers(nil), "markers must never be nil (NOT NULL column)")

	in := []string{"empty_list", "bad\x00"}
	out := cleanMarkers(in)
	require.Equal(t, []string{"empty_list", "bad"}, out)
	out[0] = "changed"
	require.Equal(t, "empty_list", in[0], "result must not alias the input")

	many := make([]string, maxMarkers+10)
	for i := range many {
		many[i] = strings.Repeat("m", maxMarkerBytes+5)
	}
	out = cleanMarkers(many)
	require.Len(t, out, maxMarkers)
	require.Len(t, out[0], maxMarkerBytes)
}

func TestNodeName(t *testing.T) {
	t.Parallel()
	require.Equal(t, EmptyNode, nodeName(""))
	require.Equal(t, "crawler-1", nodeName("crawler-1"))
	require.Len(t, nodeName(strings.Repeat("n", 500)), maxNodeBytes)
}

func TestBuckets(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 9, 17, 13, 45, 31, 999, time.FixedZone("x", 3600))
	require.Equal(t, time.Date(2026, 9, 17, 12, 45, 0, 0, time.UTC).Unix(), minuteBucket(ts))
	require.Equal(t, time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC).Unix(), hourBucket(ts))
	require.Equal(t, time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC).Unix(), dayStart(ts.Unix()))
	require.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), monthStart(ts.Unix()))
	require.Equal(t, time.Date(2026, 9, 17, 12, 45, 0, 0, time.UTC), unixTime(minuteBucket(ts)))

	before := time.Date(1969, 12, 31, 23, 59, 30, 0, time.UTC)
	require.Equal(t, int64(-60), minuteBucket(before))
	require.Equal(t, int64(-3600), hourBucket(before))
	require.Equal(t, int64(-86400), dayStart(before.Unix()))
	require.Equal(t, int64(-3), floorDiv(-5, 2))
	require.Equal(t, int64(2), floorDiv(5, 2))
	require.Equal(t, int64(-2), floorDiv(-4, 2))
}

func TestReportEventTime(t *testing.T) {
	t.Parallel()
	received := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		finished time.Time
		want     time.Time
	}{
		{name: "zero finish uses receive", finished: time.Time{}, want: received},
		{name: "normal finish", finished: received.Add(-2 * time.Second), want: received.Add(-2 * time.Second)},
		{name: "small skew accepted", finished: received.Add(time.Minute), want: received.Add(time.Minute)},
		{name: "future beyond skew", finished: received.Add(maxReportClockSkew + time.Second), want: received},
		{name: "old within age", finished: received.Add(-23 * time.Hour), want: received.Add(-23 * time.Hour)},
		{name: "too old", finished: received.Add(-maxReportAge - time.Second), want: received},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, reportEventTime(tt.finished, received))
		})
	}
}

func TestClamps(t *testing.T) {
	t.Parallel()
	require.Equal(t, int64(0), nonNegative(-5))
	require.Equal(t, int64(5), nonNegative(5))
	require.Equal(t, int32(0), clampInt32(-1))
	require.Equal(t, int32(math.MaxInt32), clampInt32(math.MaxInt64))
	require.Equal(t, int32(42), clampInt32(42))
	require.Equal(t, uint16(0), clampUint16(-1))
	require.Equal(t, uint16(math.MaxUint16), clampUint16(70000))
	require.Equal(t, uint16(429), clampUint16(429))
	require.Equal(t, uint32(0), clampUint32(-1))
	require.Equal(t, uint32(math.MaxUint32), clampUint32(math.MaxInt64))

	require.Nil(t, optionalTime(time.Time{}))
	local := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 7200))
	got := optionalTime(local)
	require.NotNil(t, got)
	require.Equal(t, time.UTC, got.Location())
	require.True(t, got.Equal(local))
}

func TestValidators(t *testing.T) {
	t.Parallel()
	for _, r := range []string{ResultOK, ResultExhausted, ResultCircuitOpen, ResultSitePaused, ResultNoProxy, ResultError} {
		require.True(t, validAcquireResult(r), r)
	}
	require.False(t, validAcquireResult(""))
	require.False(t, validAcquireResult("rate_limited"))
	for _, k := range []string{LeaseEndReleased, LeaseEndExpired, LeaseEndAbandoned, LeaseEndRenewed} {
		require.True(t, validLeaseEndKind(k), k)
	}
	require.False(t, validLeaseEndKind("acquired"))
}
