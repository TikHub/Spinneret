package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProxyRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "empty", body: `[]`},
		{name: "all modes", body: `[{"proxy_id":"p1","mode":"ok"},{"proxy_id":"p2","mode":"refuse"},
			{"proxy_id":"p3","mode":"auth_fail"},{"proxy_id":"p4","mode":"slow","latency_ms":10},{"proxy_id":"*","mode":"ok"}]`},
		{name: "bad json", body: `[`, wantErr: "decode proxy rules"},
		{name: "unknown field", body: `[{"proxy_id":"p1","mode":"ok","x":1}]`, wantErr: "unknown field"},
		{name: "missing id", body: `[{"mode":"ok"}]`, wantErr: "proxy_id is required"},
		{name: "long id", body: `[{"proxy_id":"` + strings.Repeat("p", maxProxyIDLen+1) + `","mode":"ok"}]`, wantErr: "longer than"},
		{name: "duplicate", body: `[{"proxy_id":"p1","mode":"ok"},{"proxy_id":" p1","mode":"refuse"}]`, wantErr: "duplicate proxy_id"},
		{name: "unknown mode", body: `[{"proxy_id":"p1","mode":"boom"}]`, wantErr: "unknown mode"},
		{name: "negative latency", body: `[{"proxy_id":"p1","mode":"slow","latency_ms":-1}]`, wantErr: "latency_ms"},
		{name: "latency too high", body: `[{"proxy_id":"p1","mode":"slow","latency_ms":999999}]`, wantErr: "latency_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set, err := parseProxyRules([]byte(tt.body))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, set.rules)
		})
	}
	_, err := newProxyRuleSet(make([]ProxyRule, maxProxyRules+1))
	require.ErrorContains(t, err, "too many proxy rules")
}

func TestProxyRuleLookup(t *testing.T) {
	t.Parallel()
	set, err := newProxyRuleSet([]ProxyRule{{ProxyID: "p1", Mode: ProxyModeRefuse}, {ProxyID: "slow", Mode: ProxyModeSlow}})
	require.NoError(t, err)
	require.Equal(t, ProxyModeRefuse, set.lookup("p1").Mode)
	require.Equal(t, defaultSlowMs, set.lookup("slow").LatencyMs)
	require.Equal(t, defaultProxyRule, set.lookup("other"))
	require.Equal(t, defaultProxyRule, emptyProxyRuleSet().lookup("p1"))

	withWildcard, err := newProxyRuleSet([]ProxyRule{{ProxyID: "p1", Mode: ProxyModeOK}, {ProxyID: "*", Mode: ProxyModeAuthFail}})
	require.NoError(t, err)
	require.Equal(t, ProxyModeOK, withWildcard.lookup("p1").Mode)
	require.Equal(t, ProxyModeAuthFail, withWildcard.lookup("p2").Mode)
}
