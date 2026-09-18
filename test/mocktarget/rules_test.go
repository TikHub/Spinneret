package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestParseRulesValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "empty array", body: `[]`},
		{name: "null", body: `null`},
		{name: "all modes", body: `[
			{"prefix":"/a","mode":"ok"},{"prefix":"/b","mode":"rate_limit","status":403},
			{"prefix":"/c","mode":"captcha"},{"prefix":"/d","mode":"login_redirect","status":307},
			{"prefix":"/e","mode":"server_error","status":500},{"prefix":"/f","mode":"empty"},
			{"prefix":"/g","mode":"business_error","business_code":10001},{"prefix":"/h","mode":"slow","latency_ms":5},
			{"prefix":"/i","mode":"flaky","probability":0.3}]`},
		{name: "not json", body: `{`, wantErr: "decode rules"},
		{name: "object instead of array", body: `{"prefix":"/a"}`, wantErr: "decode rules"},
		{name: "unknown field", body: `[{"prefix":"/a","mode":"ok","latency":5}]`, wantErr: "unknown field"},
		{name: "trailing data", body: `[] []`, wantErr: "unexpected data"},
		{name: "trailing bracket", body: `[]]`, wantErr: "unexpected data"},
		{name: "missing prefix", body: `[{"mode":"ok"}]`, wantErr: "must start with '/'"},
		{name: "relative prefix", body: `[{"prefix":"site","mode":"ok"}]`, wantErr: "must start with '/'"},
		{name: "long prefix", body: `[{"prefix":"/` + strings.Repeat("a", maxRulePrefixLen) + `","mode":"ok"}]`, wantErr: "prefix longer"},
		{name: "unknown mode", body: `[{"prefix":"/a","mode":"explode"}]`, wantErr: "unknown mode"},
		{name: "missing mode", body: `[{"prefix":"/a"}]`, wantErr: "unknown mode"},
		{name: "rate limit 2xx", body: `[{"prefix":"/a","mode":"rate_limit","status":200}]`, wantErr: "status 200 is not allowed"},
		{name: "server error 4xx", body: `[{"prefix":"/a","mode":"server_error","status":429}]`, wantErr: "not allowed"},
		{name: "ok 204", body: `[{"prefix":"/a","mode":"ok","status":204}]`, wantErr: "not allowed"},
		{name: "redirect 200", body: `[{"prefix":"/a","mode":"login_redirect","status":200}]`, wantErr: "not allowed"},
		{name: "probability above one", body: `[{"prefix":"/a","mode":"ok","probability":1.5}]`, wantErr: "probability"},
		{name: "probability negative", body: `[{"prefix":"/a","mode":"ok","probability":-0.1}]`, wantErr: "probability"},
		{name: "latency negative", body: `[{"prefix":"/a","mode":"slow","latency_ms":-1}]`, wantErr: "latency_ms"},
		{name: "latency too high", body: `[{"prefix":"/a","mode":"slow","latency_ms":120001}]`, wantErr: "latency_ms"},
		{name: "item count negative", body: `[{"prefix":"/a","mode":"ok","item_count":-1}]`, wantErr: "item_count"},
		{name: "item count too high", body: `[{"prefix":"/a","mode":"ok","item_count":1001}]`, wantErr: "item_count"},
		{name: "business code on other mode", body: `[{"prefix":"/a","mode":"ok","business_code":"1"}]`, wantErr: "only valid for mode business_error"},
		{name: "business code bool", body: `[{"prefix":"/a","mode":"business_error","business_code":true}]`, wantErr: "business_code"},
		{name: "business code too long", body: `[{"prefix":"/a","mode":"business_error","business_code":"` + strings.Repeat("9", 65) + `"}]`, wantErr: "business_code longer"},
		{name: "empty identity", body: `[{"prefix":"/a","mode":"ok","identities":[" "]}]`, wantErr: "identities: empty entry"},
		{name: "empty proxy", body: `[{"prefix":"/a","mode":"ok","proxies":[""]}]`, wantErr: "proxies: empty entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set, err := parseRules([]byte(tt.body))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, set)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, set.rules)
		})
	}
}

func TestNewRuleSetLimits(t *testing.T) {
	t.Parallel()
	_, err := newRuleSet(make([]Rule, maxRules+1))
	require.ErrorContains(t, err, "too many rules")

	_, err = newRuleSet([]Rule{{Prefix: "/a", Mode: ModeOK, Identities: make([]string, maxRuleFilterLen+1)}})
	require.ErrorContains(t, err, "too many entries")
}

func TestRuleDefaults(t *testing.T) {
	t.Parallel()
	set, err := parseRules([]byte(`[
		{"prefix":"/ok","mode":"ok"},
		{"prefix":"/rl","mode":"rate_limit"},
		{"prefix":"/se","mode":"server_error"},
		{"prefix":"/lr","mode":"login_redirect"},
		{"prefix":"/biz","mode":"business_error"},
		{"prefix":"/slow","mode":"slow"},
		{"prefix":"/flaky","mode":"flaky"},
		{"prefix":"/dup","mode":"ok","identities":[" a ","a","b"],"proxies":["p1","p1"]}
	]`))
	require.NoError(t, err)
	byPrefix := map[string]Rule{}
	for _, r := range set.rules {
		byPrefix[r.Prefix] = r
	}
	tests := []struct {
		prefix  string
		status  int
		prob    float64
		latency int
		code    BusinessCode
	}{
		{prefix: "/ok", status: 200, prob: 1},
		{prefix: "/rl", status: 429, prob: 1},
		{prefix: "/se", status: 503, prob: 1},
		{prefix: "/lr", status: 302, prob: 1},
		{prefix: "/biz", status: 200, prob: 1, code: "10001"},
		{prefix: "/slow", status: 200, prob: 1, latency: defaultSlowMs},
		{prefix: "/flaky", status: 429, prob: defaultFlakyProb},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			r := byPrefix[tt.prefix]
			require.Equal(t, tt.status, r.Status)
			require.NotNil(t, r.Probability)
			require.InDelta(t, tt.prob, *r.Probability, 1e-9)
			require.Equal(t, tt.latency, r.LatencyMs)
			require.Equal(t, tt.code, r.BusinessCode)
			require.NotNil(t, r.ItemCount)
			require.Equal(t, defaultItemCount, *r.ItemCount)
		})
	}
	require.Equal(t, []string{"a", "b"}, byPrefix["/dup"].Identities)
	require.Equal(t, []string{"p1"}, byPrefix["/dup"].Proxies)
}

func TestRuleSetMatch(t *testing.T) {
	t.Parallel()
	set, err := newRuleSet([]Rule{
		{Prefix: "/site", Mode: ModeServerError},
		{Prefix: "/site/search", Mode: ModeRateLimit},
		{Prefix: "/site/search", Mode: ModeCaptcha, Identities: []string{"alice"}},
		{Prefix: "/site/search", Mode: ModeEmpty, Identities: []string{"alice"}, Proxies: []string{"p1"}},
		{Prefix: "/site/feed", Mode: ModeSlow, Proxies: []string{"direct"}},
		{Prefix: "/site/feed", Mode: ModeBusinessError},
		{Prefix: "/site/feed", Mode: ModeLoginRedirect},
	})
	require.NoError(t, err)
	tests := []struct {
		name     string
		path     string
		identity string
		proxy    string
		want     Mode // "" = no rule
	}{
		{name: "no match", path: "/other", identity: "x", proxy: "direct"},
		{name: "short prefix", path: "/site/items", identity: "x", proxy: "direct", want: ModeServerError},
		{name: "longest prefix", path: "/site/search/v2", identity: "bob", proxy: "p1", want: ModeRateLimit},
		{name: "identity filter", path: "/site/search", identity: "alice", proxy: "p2", want: ModeCaptcha},
		{name: "identity and proxy filter", path: "/site/search", identity: "alice", proxy: "p1", want: ModeEmpty},
		{name: "proxy filter direct", path: "/site/feed", identity: "bob", proxy: "direct", want: ModeSlow},
		{name: "first unfiltered in order", path: "/site/feed", identity: "bob", proxy: "p9", want: ModeBusinessError},
		{name: "plain string prefix", path: "/site/searchable", identity: "bob", proxy: "p1", want: ModeRateLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := set.match(tt.path, tt.identity, tt.proxy)
			if tt.want == "" {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.want, got.Mode)
		})
	}
	require.Nil(t, emptyRuleSet().match("/site/a", "x", "direct"))
}

func TestFilteredRuleFallsBackToShorterPrefix(t *testing.T) {
	t.Parallel()
	set, err := newRuleSet([]Rule{
		{Prefix: "/site/", Mode: ModeRateLimit},
		{Prefix: "/site/search", Mode: ModeCaptcha, Identities: []string{"alice"}},
	})
	require.NoError(t, err)
	require.Equal(t, ModeCaptcha, set.match("/site/search", "alice", "direct").Mode)
	require.Equal(t, ModeRateLimit, set.match("/site/search", "bob", "direct").Mode)
}

func TestBusinessCodeUnmarshal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    BusinessCode
		wantErr bool
	}{
		{in: `"10001"`, want: "10001"},
		{in: `10001`, want: "10001"},
		{in: `-5`, want: "-5"},
		{in: ` null `, want: ""},
		{in: `"abc"`, want: "abc"},
		{in: `true`, wantErr: true},
		{in: `"unterminated`, wantErr: true},
		{in: `{}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			var c BusinessCode = "prev"
			err := c.UnmarshalJSON([]byte(tt.in))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, c)
		})
	}
}

func TestCompileRuleDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []Rule{{Prefix: "/a", Mode: ModeFlaky, Identities: []string{" x "}}}
	_, err := newRuleSet(in)
	require.NoError(t, err)
	require.Nil(t, in[0].Probability)
	require.Equal(t, 0, in[0].Status)
	require.Equal(t, []string{" x "}, in[0].Identities)
	require.Nil(t, in[0].ItemCount)
}
