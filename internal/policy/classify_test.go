package policy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func mustSignal(t testing.TB, src string) *SignalSpec {
	t.Helper()
	spec, err := ParseYAML(KindSignal, []byte(src))
	require.NoError(t, err)
	return spec.(*SignalSpec)
}

func compileDefaultSignal(t testing.TB) *CompiledSignal {
	t.Helper()
	c, err := CompileSignal([]*SignalSpec{Default(KindSignal).(*SignalSpec)})
	require.NoError(t, err)
	return c
}

func TestClassifyDefaultSignal(t *testing.T) {
	c := compileDefaultSignal(t)
	require.Equal(t, 11, c.RuleCount())
	require.False(t, c.TrustOutcomeHint())
	tests := []struct {
		name      string
		facts     ReportFacts
		outcome   string
		blame     Blame
		ruleIndex int
		ruleName  string
	}{
		{"proxy auth", ReportFacts{ErrorKind: ErrorKindProxyAuth}, OutcomeProxyError, BlameProxy, 0, "proxy-error"},
		{"conn refused", ReportFacts{ErrorKind: ErrorKindConnRefused}, OutcomeProxyError, BlameProxy, 0, "proxy-error"},
		{"timeout", ReportFacts{ErrorKind: ErrorKindTimeout}, OutcomeNetworkError, BlameProxy, 1, "network-error"},
		{"dns", ReportFacts{ErrorKind: ErrorKindDNS}, OutcomeNetworkError, BlameProxy, 1, "network-error"},
		{"other error kind unmatched", ReportFacts{ErrorKind: ErrorKindOther}, OutcomeUnknown, BlameNone, -1, ""},
		{"captcha marker beats 200", ReportFacts{HTTPStatus: 200, Markers: []string{"x", "captcha_page"}}, OutcomeCaptcha, BlameIdentity, 2, "captcha"},
		{"login redirect", ReportFacts{HTTPStatus: 302, Markers: []string{"login_redirect"}}, OutcomeAuthInvalid, BlameIdentity, 3, "login-redirect"},
		{"429", ReportFacts{HTTPStatus: 429}, OutcomeRateLimited, BlameBoth, 4, "rate-limited"},
		{"500", ReportFacts{HTTPStatus: 500}, OutcomeTargetError, BlameNone, 5, "target-error"},
		{"503", ReportFacts{HTTPStatus: 503}, OutcomeTargetError, BlameNone, 5, "target-error"},
		{"empty list", ReportFacts{HTTPStatus: 200, Markers: []string{"empty_list"}}, OutcomeEmpty, BlameIdentity, 6, "empty-list"},
		{"empty list marker without 200", ReportFacts{HTTPStatus: 206, Markers: []string{"empty_list"}}, OutcomeSuccess, BlameNone, 10, "success"},
		{"400", ReportFacts{HTTPStatus: 400}, OutcomeClientError, BlameNone, 7, "client-error"},
		{"401", ReportFacts{HTTPStatus: 401}, OutcomeAuthInvalid, BlameIdentity, 8, "auth-invalid"},
		{"403", ReportFacts{HTTPStatus: 403}, OutcomeForbidden, BlameIdentity, 9, "forbidden"},
		{"200", ReportFacts{HTTPStatus: 200}, OutcomeSuccess, BlameNone, 10, "success"},
		{"299", ReportFacts{HTTPStatus: 299}, OutcomeSuccess, BlameNone, 10, "success"},
		{"300 unmatched", ReportFacts{HTTPStatus: 300}, OutcomeUnknown, BlameNone, -1, ""},
		{"no status unmatched", ReportFacts{}, OutcomeUnknown, BlameNone, -1, ""},
		{"hint ignored when untrusted", ReportFacts{HTTPStatus: 404, OutcomeHint: OutcomeBanned}, OutcomeUnknown, BlameNone, -1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Classify(tc.facts)
			require.Equal(t, Classification{Outcome: tc.outcome, Blame: tc.blame, RuleIndex: tc.ruleIndex, RuleName: tc.ruleName}, got)
		})
	}
}

func TestClassifyConditions(t *testing.T) {
	spec := mustSignal(t, `
name: conditions
rules:
  - name: no-status
    when: {http_status: [0], error_kind: timeout}
    outcome: network_error
    blame: identity
  - name: business
    when: {http_status: [200], business_code: [10001, "E42"]}
    outcome: forbidden
  - name: uri-prefix
    when: {uri: {prefix: /api/login}, method: [post]}
    outcome: auth_invalid
  - name: uri-regex
    when: {uri: {regex: "^/item/[0-9]+$"}, response_bytes: {lt: 100}}
    outcome: empty
  - name: slow
    when: {latency_ms: {gt: 30000}}
    outcome: network_error
    blame: none
  - name: range-lt
    when: {http_status: {lt: 200}}
    outcome: target_error
  - name: markers-any
    when: {markers: [a, b]}
    outcome: captcha
  - when: {method: [DELETE]}
    outcome: client_error
  - name: other-transport
    when: {error_kind: [other]}
    outcome: network_error
`)
	c, err := CompileSignal([]*SignalSpec{spec})
	require.NoError(t, err)
	tests := []struct {
		name      string
		facts     ReportFacts
		outcome   string
		blame     Blame
		ruleIndex int
		ruleName  string
	}{
		{"status 0 means no status", ReportFacts{ErrorKind: ErrorKindTimeout}, OutcomeNetworkError, BlameIdentity, 0, "no-status"},
		{"status 0 rule needs both conditions", ReportFacts{ErrorKind: ErrorKindDNS}, OutcomeUnknown, BlameNone, -1, ""},
		{"status present does not match 0", ReportFacts{HTTPStatus: 504, ErrorKind: ErrorKindTimeout}, OutcomeUnknown, BlameNone, -1, ""},
		{"business code number", ReportFacts{HTTPStatus: 200, BusinessCode: "10001"}, OutcomeForbidden, BlameIdentity, 1, "business"},
		{"business code string", ReportFacts{HTTPStatus: 200, BusinessCode: "E42"}, OutcomeForbidden, BlameIdentity, 1, "business"},
		{"business code other", ReportFacts{HTTPStatus: 200, BusinessCode: "0"}, OutcomeUnknown, BlameNone, -1, ""},
		{"business code missing", ReportFacts{HTTPStatus: 200}, OutcomeUnknown, BlameNone, -1, ""},
		{"uri prefix and method case-insensitive", ReportFacts{URI: "/api/login/v2", Method: "POST"}, OutcomeAuthInvalid, BlameIdentity, 2, "uri-prefix"},
		{"uri prefix wrong method", ReportFacts{URI: "/api/login", Method: "GET"}, OutcomeUnknown, BlameNone, -1, ""},
		{"uri prefix missing method", ReportFacts{URI: "/api/login"}, OutcomeUnknown, BlameNone, -1, ""},
		{"uri regex and bytes", ReportFacts{URI: "/item/42", ResponseBytes: 99}, OutcomeEmpty, BlameIdentity, 3, "uri-regex"},
		{"uri regex bytes too large", ReportFacts{URI: "/item/42", ResponseBytes: 100}, OutcomeUnknown, BlameNone, -1, ""},
		{"uri regex no match", ReportFacts{URI: "/item/x", ResponseBytes: 1}, OutcomeUnknown, BlameNone, -1, ""},
		{"latency gt", ReportFacts{LatencyMs: 30001}, OutcomeNetworkError, BlameNone, 4, "slow"},
		{"latency boundary", ReportFacts{LatencyMs: 30000}, OutcomeUnknown, BlameNone, -1, ""},
		{"range never matches missing status", ReportFacts{}, OutcomeUnknown, BlameNone, -1, ""},
		{"range lt", ReportFacts{HTTPStatus: 199}, OutcomeTargetError, BlameNone, 5, "range-lt"},
		{"markers any-of second", ReportFacts{Markers: []string{"z", "b"}}, OutcomeCaptcha, BlameIdentity, 6, "markers-any"},
		{"markers none", ReportFacts{Markers: []string{"z", ""}}, OutcomeUnknown, BlameNone, -1, ""},
		{"unnamed rule label", ReportFacts{Method: "delete"}, OutcomeClientError, BlameNone, 7, "conditions.rules[7]"},
		{"out of range status", ReportFacts{HTTPStatus: 5000}, OutcomeUnknown, BlameNone, -1, ""},
		{"negative status", ReportFacts{HTTPStatus: -1}, OutcomeUnknown, BlameNone, -1, ""},
		{"other error kind", ReportFacts{ErrorKind: ErrorKindOther}, OutcomeNetworkError, BlameProxy, 8, "other-transport"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.Classify(tc.facts)
			require.Equal(t, Classification{Outcome: tc.outcome, Blame: tc.blame, RuleIndex: tc.ruleIndex, RuleName: tc.ruleName}, got)
		})
	}
}

func TestClassifyOutcomeHint(t *testing.T) {
	trusting := mustSignal(t, "name: trust\ntrust_outcome_hint: true\nrules:\n  - when: {http_status: [200]}\n    outcome: success\n")
	c, err := CompileSignal([]*SignalSpec{trusting})
	require.NoError(t, err)
	require.True(t, c.TrustOutcomeHint())
	tests := []struct {
		name  string
		facts ReportFacts
		want  Classification
	}{
		{"rule wins over hint", ReportFacts{HTTPStatus: 200, OutcomeHint: OutcomeCaptcha}, Classification{OutcomeSuccess, BlameNone, 0, "trust.rules[0]"}},
		{"valid hint", ReportFacts{HTTPStatus: 418, OutcomeHint: OutcomeCaptcha}, Classification{OutcomeCaptcha, BlameIdentity, -1, ""}},
		{"valid hint rate limited", ReportFacts{OutcomeHint: OutcomeRateLimited}, Classification{OutcomeRateLimited, BlameBoth, -1, ""}},
		{"invalid hint", ReportFacts{OutcomeHint: "meh"}, Classification{OutcomeUnknown, BlameNone, -1, ""}},
		{"empty hint", ReportFacts{}, Classification{OutcomeUnknown, BlameNone, -1, ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, c.Classify(tc.facts))
		})
	}
}

func TestCompileSignalChain(t *testing.T) {
	root := mustSignal(t, "name: root\ntrust_outcome_hint: true\nrules:\n  - {name: r429, when: {http_status: [429]}, outcome: rate_limited}\n")
	leaf := mustSignal(t, "name: leaf\nextends: root\nrules:\n  - {when: {http_status: [429]}, outcome: captcha}\n  - {when: {http_status: [200]}, outcome: success}\n")
	c, err := CompileSignal([]*SignalSpec{root, leaf})
	require.NoError(t, err)
	require.Equal(t, 3, c.RuleCount())
	require.False(t, c.TrustOutcomeHint(), "trust_outcome_hint comes from the leaf")
	require.Equal(t, Classification{OutcomeRateLimited, BlameBoth, 0, "r429"}, c.Classify(ReportFacts{HTTPStatus: 429}))
	require.Equal(t, Classification{OutcomeSuccess, BlameNone, 2, "leaf.rules[1]"}, c.Classify(ReportFacts{HTTPStatus: 200}))
	require.Equal(t, OutcomeUnknown, c.Classify(ReportFacts{OutcomeHint: OutcomeCaptcha}).Outcome)

	// The compiled form does not alias the spec.
	leaf.Rules[1].When.HTTPStatus.Values[0] = 201
	require.Equal(t, OutcomeSuccess, c.Classify(ReportFacts{HTTPStatus: 200}).Outcome)
}

func TestCompileSignalErrors(t *testing.T) {
	_, err := CompileSignal(nil)
	require.ErrorContains(t, err, "chain is empty")
	_, err = CompileSignal([]*SignalSpec{nil})
	require.ErrorContains(t, err, "chain element 0 is nil")
	_, err = CompileSignal([]*SignalSpec{{Name: "bad", Rules: []SignalRule{{Outcome: "nope"}}}})
	require.ErrorContains(t, err, `compile signal policy "bad"`)
	require.ErrorContains(t, err, "rules[0].outcome")

	// A regex that bypassed validation length checks still compiles safely.
	_, err = compileSignalRule("x", 0, &SignalRule{Outcome: OutcomeSuccess, When: SignalWhen{URI: &URIMatcher{Regex: "("}}})
	require.ErrorContains(t, err, "rules[0].when.uri.regex")

	var nilSignal *CompiledSignal
	require.Equal(t, Classification{OutcomeUnknown, BlameNone, -1, ""}, nilSignal.Classify(ReportFacts{HTTPStatus: 200}))
	require.Zero(t, nilSignal.RuleCount())
	require.False(t, nilSignal.TrustOutcomeHint())
}

func TestClassifyZeroAllocations(t *testing.T) {
	c := compileDefaultSignal(t)
	facts := ReportFacts{URI: "/a/b", Method: "GET", HTTPStatus: 200, Markers: []string{"x", "y"}, LatencyMs: 800, ResponseBytes: 4096}
	allocs := testing.AllocsPerRun(1000, func() {
		_ = c.Classify(facts)
	})
	require.Zero(t, allocs)
}

func BenchmarkClassifyDefaultSuccess(b *testing.B) {
	c := compileDefaultSignal(b)
	facts := ReportFacts{URI: "/api/v1/search/single/", Method: "GET", HTTPStatus: 200, Markers: []string{"has_more"}, LatencyMs: 842, ResponseBytes: 48213}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Classify(facts)
	}
}

func BenchmarkClassifyRegexRule(b *testing.B) {
	spec := mustSignal(b, "name: re\nrules:\n  - {when: {uri: {regex: \"^/item/[0-9]+/detail$\"}, http_status: [200]}, outcome: success}\n")
	c, err := CompileSignal([]*SignalSpec{spec})
	require.NoError(b, err)
	facts := ReportFacts{URI: "/item/123456/detail", HTTPStatus: 200}
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Classify(facts)
	}
}
