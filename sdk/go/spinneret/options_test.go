package spinneret

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestOptionsResolve(t *testing.T) {
	s, err := Options{}.resolve(envOf(map[string]string{
		EnvURL:   " https://spinneret.internal/prefix/ ",
		EnvToken: "spn_env",
		EnvNode:  "crawler hk/03",
	}))
	require.NoError(t, err)
	require.Equal(t, "https://spinneret.internal/prefix", s.baseURL)
	require.Equal(t, "spinneret.internal", s.host)
	require.Equal(t, "spn_env", s.token)
	require.Equal(t, "crawler-hk-03", s.node)
	require.Equal(t, DefaultTimeout, s.timeout)
	require.Equal(t, DefaultRetryPolicy(), s.retry)
	require.Equal(t, DefaultFlushInterval, s.reporter.FlushInterval)
	require.NotNil(t, s.logger)
	require.NotNil(t, s.transport)
	require.Equal(t, "spinneret-go/"+Version, s.userAgent)

	explicit, err := Options{
		BaseURL:   "http://127.0.0.1:8080",
		Token:     "spn_explicit",
		Node:      "node-a",
		Timeout:   time.Second,
		Retry:     NoRetry(),
		UserAgent: "my-crawler/1.0",
	}.resolve(envOf(map[string]string{EnvURL: "https://ignored", EnvToken: "ignored"}))
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", explicit.baseURL)
	require.Equal(t, "spn_explicit", explicit.token)
	require.Equal(t, "node-a", explicit.node)
	require.Equal(t, time.Second, explicit.timeout)
	require.Equal(t, 0, explicit.retry.MaxRetries)
	require.Equal(t, "my-crawler/1.0 spinneret-go/"+Version, explicit.userAgent)

	withClient, err := Options{BaseURL: "http://x", Token: "t", HTTPClient: http.DefaultClient}.resolve(envOf(nil))
	require.NoError(t, err)
	require.Nil(t, withClient.transport)
	require.Equal(t, DefaultNodeName(), withClient.node)
}

func TestOptionsResolveErrors(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"missing url", Options{Token: "t"}, "server URL is not configured"},
		{"bad scheme", Options{BaseURL: "ftp://x", Token: "t"}, "invalid server URL"},
		{"no host", Options{BaseURL: "http://", Token: "t"}, "invalid server URL"},
		{"query", Options{BaseURL: "http://x/?a=1", Token: "t"}, "query and fragment"},
		{"fragment", Options{BaseURL: "http://x#frag", Token: "t"}, "query and fragment"},
		{"credentials", Options{BaseURL: "http://u:p@x", Token: "t"}, "credentials"},
		{"missing token", Options{BaseURL: "http://x"}, "token is not configured"},
		{"token whitespace", Options{BaseURL: "http://x", Token: "spn a"}, "whitespace"},
		{"negative timeout", Options{BaseURL: "http://x", Token: "t", Timeout: -1}, "timeout"},
		{"negative retry", Options{BaseURL: "http://x", Token: "t", Retry: &RetryPolicy{MaxRetries: -1}}, "retry policy"},
		{"reporter", Options{BaseURL: "http://x", Token: "t", Reporter: ReporterOptions{MaxBatchSize: 501}}, "MaxBatchSize"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.opts.resolve(envOf(nil))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, ReasonInvalidConfiguration, ReasonOf(err))
			_, err = New(Options{BaseURL: tc.opts.BaseURL, Token: tc.opts.Token, Timeout: tc.opts.Timeout,
				Retry: tc.opts.Retry, Reporter: tc.opts.Reporter, Node: "n"})
			if tc.opts.BaseURL != "" && tc.opts.Token != "" {
				require.Error(t, err)
			}
		})
	}
}

func TestSanitizeNodeName(t *testing.T) {
	tests := map[string]string{
		"crawler-hk-03":          "crawler-hk-03",
		"  pod/abc  ":            "pod-abc",
		"a  b\t\tc":              "a-b-c",
		"--x--":                  "x",
		"":                       "unknown",
		"日本":                     "unknown",
		"user@host:9000.local":   "user@host:9000.local",
		"a--b":                   "a--b",
		strings.Repeat("n", 200): strings.Repeat("n", MaxNodeNameLength),
	}
	for in, want := range tests {
		require.Equal(t, want, SanitizeNodeName(in), "input %q", in)
	}
	require.NotEmpty(t, DefaultNodeName())
}

func TestRetryPolicyNormalized(t *testing.T) {
	p, err := RetryPolicy{MaxRetries: 1, InitialBackoff: time.Second, MaxBackoff: time.Millisecond}.normalized()
	require.NoError(t, err)
	require.Equal(t, time.Second, p.MaxBackoff)
	p, err = RetryPolicy{}.normalized()
	require.NoError(t, err)
	require.Equal(t, DefaultInitialBackoff, p.InitialBackoff)
	require.Equal(t, DefaultMaxBackoff, p.MaxBackoff)
}

func TestReporterOptionsNormalized(t *testing.T) {
	o, err := ReporterOptions{MaxBatchSize: 50}.normalized()
	require.NoError(t, err)
	require.Equal(t, 50, o.BatchSize)
	require.Equal(t, DefaultReportQueueSize, o.MaxQueueSize)
	require.Equal(t, DefaultReporterBackoff, o.InitialBackoff)
	require.Equal(t, DefaultReporterMaxBackoff, o.MaxBackoff)
	require.Equal(t, DefaultReporterCloseTimeout, o.CloseTimeout)

	for _, bad := range []ReporterOptions{
		{FlushInterval: -1},
		{MaxBatchSize: 501},
		{BatchSize: 200, MaxBatchSize: 100},
		{MaxBatchSize: 100, MaxQueueSize: 10},
	} {
		_, err := bad.normalized()
		require.Error(t, err, "%+v", bad)
	}
}
