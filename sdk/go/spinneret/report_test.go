package spinneret

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReportURI(t *testing.T) {
	tests := map[string]string{
		"":                                       "",
		"/api/v1/feed":                           "/api/v1/feed",
		"/search?keyword=x&signature=sig":        "/search",
		"/path#frag":                             "/path",
		"https://target.example.com/a/b?sig=1#x": "/a/b",
		"HTTPS://Example.com":                    "/",
		"http://example.com/p%20q?x=1":           "/p%20q",
		"https://exa mple.com/bad/path?q=1":      "/bad/path",
		"https://exa mple.com?q=1":               "/",
		"?only=query":                            "/",
		"relative/path?x":                        "relative/path",
	}
	for in, want := range tests {
		require.Equal(t, want, ReportURI(in), "input %q", in)
	}
	long := "/" + strings.Repeat("é", 3000)
	require.Equal(t, MaxReportURILength, len([]rune(ReportURI(long))))
	require.Equal(t, "ab", truncateRunes("abc", 2))
	require.Equal(t, "abc", truncateRunes("abc", 3))
}

func TestNewReportID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for range 1000 {
		id := NewReportID()
		require.Regexp(t, pattern, id)
		require.True(t, validReportID(id))
		require.False(t, seen[id])
		seen[id] = true
	}
}

func TestBuildReportDefaults(t *testing.T) {
	now := time.Date(2026, 9, 16, 8, 30, 11, 962_000_000, time.UTC)

	r, err := buildReport("lse_1", "/api/v1/feed?cursor=1", ReportInput{HTTPStatus: 200, Latency: 842 * time.Millisecond, Method: " get "}, now)
	require.NoError(t, err)
	require.Equal(t, "lse_1", r.GetLeaseId())
	require.Equal(t, "/api/v1/feed", r.GetUri())
	require.Equal(t, "GET", r.GetMethod())
	require.Equal(t, int32(842), r.GetLatencyMs())
	require.Equal(t, now, r.GetFinishedAt().AsTime())
	require.Equal(t, now.Add(-842*time.Millisecond), r.GetStartedAt().AsTime())
	require.True(t, validReportID(r.GetReportId()))

	started := now.Add(-1500 * time.Millisecond)
	r, err = buildReport("lse_1", "", ReportInput{URI: "/x", StartedAt: started, ReportID: "custom-id:1", Release: true, Markers: []string{"captcha_page"}}, now)
	require.NoError(t, err)
	require.Equal(t, int32(1500), r.GetLatencyMs())
	require.Equal(t, "custom-id:1", r.GetReportId())
	require.True(t, r.GetRelease())
	require.Equal(t, []string{"captcha_page"}, r.GetMarkers())

	r, err = buildReport("lse_1", "/x", ReportInput{}, now)
	require.NoError(t, err)
	require.Equal(t, r.GetStartedAt().AsTime(), r.GetFinishedAt().AsTime())
	require.Zero(t, r.GetLatencyMs())

	r, err = buildReport("lse_1", "/x", ReportInput{Latency: 1000 * time.Hour}, now)
	require.NoError(t, err)
	require.Equal(t, int32(2147483647), r.GetLatencyMs(), "latency is clamped to int32")
}

func TestBuildReportValidation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		leaseID string
		uri     string
		in      ReportInput
		want    string
	}{
		{"bad report id", "l", "/", ReportInput{ReportID: "has space"}, "report_id"},
		{"long report id", "l", "/", ReportInput{ReportID: strings.Repeat("a", 65)}, "report_id"},
		{"no lease", "", "/", ReportInput{}, "lease_id"},
		{"no uri", "l", "", ReportInput{}, "uri is required"},
		{"method", "l", "/", ReportInput{Method: strings.Repeat("M", 17)}, "method"},
		{"status", "l", "/", ReportInput{HTTPStatus: 1000}, "http_status"},
		{"negative status", "l", "/", ReportInput{HTTPStatus: -1}, "http_status"},
		{"business code", "l", "/", ReportInput{BusinessCode: strings.Repeat("9", 65)}, "business_code"},
		{"error kind", "l", "/", ReportInput{ErrorKind: "boom"}, "error_kind"},
		{"too many markers", "l", "/", ReportInput{Markers: make([]string, 33)}, "markers"},
		{"empty marker", "l", "/", ReportInput{Markers: []string{""}}, "markers"},
		{"long marker", "l", "/", ReportInput{Markers: []string{strings.Repeat("m", 65)}}, "markers"},
		{"outcome hint", "l", "/", ReportInput{OutcomeHint: strings.Repeat("o", 33)}, "outcome_hint"},
		{"negative latency", "l", "/", ReportInput{Latency: -time.Second}, "latency"},
		{"negative bytes", "l", "/", ReportInput{ResponseBytes: -1}, "response_bytes"},
		{"order", "l", "/", ReportInput{StartedAt: now, FinishedAt: now.Add(-time.Second)}, "finished_at"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildReport(tc.leaseID, tc.uri, tc.in, now)
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, ReasonInvalidArgument, ReasonOf(err))
		})
	}
	for _, kind := range []string{"", "timeout", "conn_reset", "conn_refused", "proxy_auth", "tls", "dns", "other"} {
		require.True(t, validErrorKind(kind))
	}
}
