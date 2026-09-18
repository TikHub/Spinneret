package policy

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

func TestDefaultsRoundTrip(t *testing.T) {
	for _, kind := range Kinds() {
		t.Run(string(kind), func(t *testing.T) {
			def := Default(kind)
			require.NotNil(t, def)
			require.NoError(t, def.Validate())
			require.Equal(t, DefaultPolicyName(kind), def.PolicyName())

			src := DefaultYAML(kind)
			require.NotEmpty(t, src)
			parsed, err := ParseYAML(kind, []byte(src))
			require.NoError(t, err)
			require.Equal(t, def, parsed, "embedded YAML must parse into Default(kind)")

			out, err := MarshalYAML(def)
			require.NoError(t, err)
			again, err := ParseYAML(kind, out)
			require.NoError(t, err)
			require.Equal(t, def, again, "MarshalYAML -> ParseYAML must be idempotent:\n%s", out)

			js, err := MarshalJSON(def)
			require.NoError(t, err)
			fromJSON, err := ParseJSON(kind, js)
			require.NoError(t, err)
			require.Equal(t, def, fromJSON)
			js2, err := MarshalJSON(fromJSON)
			require.NoError(t, err)
			require.Equal(t, string(js), string(js2), "canonical JSON must be stable")

			// Default returns independent copies.
			other := Default(kind)
			require.Equal(t, def, other)
			require.NotSame(t, def, other)
		})
	}
}

func TestDefaultUnknownKind(t *testing.T) {
	require.Nil(t, Default("nope"))
	require.Empty(t, DefaultYAML("nope"))
	require.Empty(t, DefaultPolicyName("nope"))
	require.False(t, ValidKind("nope"))
	_, err := ParseYAML("nope", []byte("name: x"))
	require.ErrorContains(t, err, `unknown kind "nope"`)
	_, err = ParseJSON("nope", []byte(`{"name":"x"}`))
	require.ErrorContains(t, err, `unknown kind "nope"`)
}

func TestDefaultBuiltinValues(t *testing.T) {
	rot := Default(KindRotation).(*RotationSpec)
	require.Equal(t, StrategyWeightedRandom, rot.Rotation.Strategy)
	require.Equal(t, 32, rot.Rotation.CandidateSample)
	require.Equal(t, 120*time.Second, rot.Rotation.LeaseTTL.Std())
	require.Equal(t, 30*time.Minute, rot.Rotation.MaxLeaseLifetime.Std())
	require.Equal(t, ReuseAnchorReleased, rot.Rotation.ReuseAnchor)
	require.Equal(t, ReuseScopeEndpointGroup, rot.Rotation.ReuseScope)
	require.Equal(t, 5*time.Minute, rot.Proxy.RebindTolerance.Std())
	require.Equal(t, 3, rot.Proxy.MaxRebindsPerDay)
	require.Equal(t, ProxyModeNone, rot.Proxy.Mode)

	sig := Default(KindSignal).(*SignalSpec)
	require.Len(t, sig.Rules, 11)
	require.Equal(t, OutcomeAuthInvalid, sig.Rules[8].Outcome)
	require.Equal(t, OutcomeForbidden, sig.Rules[9].Outcome)
	require.Equal(t, OutcomeSuccess, sig.Rules[10].Outcome)

	act := Default(KindAction).(*ActionSpec)
	require.Len(t, act.Rules, 7)
	require.Equal(t, 1.0, act.Rules[1].Multiplier)
	require.Equal(t, 24*time.Hour, act.Rules[1].Max.Std())
	require.Equal(t, 10, act.Rules[1].MaxExponent)
	require.Equal(t, time.Hour, act.Rules[1].FailureResetAfter.Std())
	require.Zero(t, act.Rules[3].Multiplier, "ban rules carry no cooldown parameters")
	require.True(t, act.Rules[5].Duration.IsPermanent())
	require.Len(t, act.Escalation, 2)
	require.True(t, act.CrossAttribution.Enabled)
	require.Equal(t, 70.0, act.Health.Baseline)

	brk := Default(KindBreaker).(*BreakerSpec)
	require.True(t, brk.Enabled)
	require.Equal(t, RevertEndpoint, brk.RevertRecentCooldowns)
	require.Equal(t, 5*time.Second, brk.BucketDuration())
	require.Equal(t, 0.4, brk.Trip.RiskRatioGte)
}

func TestParseYAMLDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		src  string
		want string
	}{
		{"empty", KindRotation, "", "document is empty"},
		{"comment only", KindSignal, "# nothing\n", "document is empty"},
		{"unknown top-level field", KindRotation, "name: a\nfoo: 1\n", "field foo not found"},
		{"unknown nested field", KindRotation, "name: a\nrotation:\n  strategyy: x\n", "field strategyy not found"},
		{"unknown rule field", KindSignal, "name: a\nrules:\n  - outcome: success\n    colour: red\n", "field colour not found"},
		{"unknown when field", KindAction, "name: a\nrules:\n  - when: {outcome: captcha, cnt: 1}\n", "field cnt not found"},
		{"multiple documents", KindBreaker, "name: a\n---\nname: b\n", "multiple documents"},
		{"broken second document", KindBreaker, "name: a\n---\n[\n", "parse breaker policy YAML"},
		{"not a mapping", KindBreaker, "- a\n- b\n", "cannot unmarshal"},
		{"bad duration", KindRotation, "name: a\nrotation: {lease_ttl: soon}\n", "invalid duration"},
		{"duplicate key", KindRotation, "name: a\nname: b\n", "already"},
		{"status string", KindSignal, "name: a\nrules:\n  - when: {http_status: [\"429\"]}\n    outcome: rate_limited\n", "expected an integer"},
		{"status nested list", KindSignal, "name: a\nrules:\n  - when: {http_status: [[1]]}\n    outcome: rate_limited\n", "expected an integer"},
		{"status unknown range key", KindSignal, "name: a\nrules:\n  - when: {http_status: {ge: 1}}\n    outcome: rate_limited\n", "field ge not found in range"},
		{"status duplicate range key", KindSignal, "name: a\nrules:\n  - when: {http_status: {gte: 1, gte: 2}}\n    outcome: rate_limited\n", "already set"},
		{"status range bad value", KindSignal, "name: a\nrules:\n  - when: {http_status: {gte: x}}\n    outcome: rate_limited\n", "expected an integer"},
		{"status float", KindSignal, "name: a\nrules:\n  - when: {http_status: 4.5}\n    outcome: rate_limited\n", "expected an integer"},
		{"latency unknown key", KindSignal, "name: a\nrules:\n  - when: {latency_ms: {above: 1}}\n    outcome: rate_limited\n", "field above not found"},
		{"markers mapping", KindSignal, "name: a\nrules:\n  - when: {markers: {a: b}}\n    outcome: captcha\n", "expected a string or a list"},
		{"markers nested", KindSignal, "name: a\nrules:\n  - when: {markers: [[a]]}\n    outcome: captcha\n", "expected a string or number"},
		{"business code bool", KindSignal, "name: a\nrules:\n  - when: {business_code: [true]}\n    outcome: captcha\n", "expected a string or number"},
		{"business code inf", KindSignal, "name: a\nrules:\n  - when: {business_code: [.inf]}\n    outcome: captcha\n", "invalid number"},
		{"outcome mapping", KindAction, "name: a\nrules:\n  - when: {outcome: {a: 1}}\n", "expected a string or a list"},
		{"revert mode list", KindBreaker, "name: a\nrevert_recent_cooldowns: [a]\n", "revert mode must be"},
		{"revert mode int", KindBreaker, "name: a\nrevert_recent_cooldowns: 1\n", "revert mode must be"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseYAML(tc.kind, []byte(tc.src))
			require.Error(t, err)
			require.Nil(t, spec)
			require.ErrorContains(t, err, tc.want)
			var ve *ValidationError
			require.False(t, errors.As(err, &ve), "decode errors are not validation errors")
		})
	}
}

func TestParseJSONDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		kind Kind
		src  string
		want string
	}{
		{"empty", KindRotation, "  ", "document is empty"},
		{"array", KindRotation, "[]", "must be an object"},
		{"unknown field", KindRotation, `{"name":"a","foo":1}`, `unknown field "foo"`},
		{"unknown nested field", KindBreaker, `{"name":"a","trip":{"x":1}}`, `unknown field "x"`},
		{"trailing data", KindBreaker, `{"name":"a"} {"name":"b"}`, "trailing data"},
		{"bad json", KindSignal, `{"name":`, "parse signal policy JSON"},
		{"status object unknown", KindSignal, `{"name":"a","rules":[{"when":{"http_status":{"ge":1}},"outcome":"success"}]}`, "invalid range"},
		{"status bad list", KindSignal, `{"name":"a","rules":[{"when":{"http_status":["x"]},"outcome":"success"}]}`, "expected a list of integers"},
		{"status bad scalar", KindSignal, `{"name":"a","rules":[{"when":{"http_status":"x"}, "outcome":"success"}]}`, "expected a list of integers"},
		{"markers bool", KindSignal, `{"name":"a","rules":[{"when":{"markers":[true]},"outcome":"captcha"}]}`, "expected a string or number"},
		{"markers null element", KindSignal, `{"name":"a","rules":[{"when":{"markers":[null]},"outcome":"captcha"}]}`, "got null"},
		{"markers object", KindSignal, `{"name":"a","rules":[{"when":{"markers":{"a":1}},"outcome":"captcha"}]}`, "expected a string or number"},
		{"markers bad list", KindSignal, `{"name":"a","rules":[{"when":{"markers":[1,}},"outcome":"captcha"}]}`, "parse signal policy JSON"},
		{"revert mode number", KindBreaker, `{"name":"a","revert_recent_cooldowns":1}`, "revert mode must be"},
		{"duration bool", KindRotation, `{"name":"a","rotation":{"lease_ttl":true}}`, "duration"},
		{"member name case", KindRotation, `{"NAME":"a"}`, `unknown object member name "NAME"`},
		{"nested member name case", KindRotation, `{"name":"a","Rotation":{"Lease_TTL":"10s"}}`, `unknown object member name "Rotation"`},
		{"range member name case", KindSignal, `{"name":"a","rules":[{"when":{"http_status":{"GTE":1}},"outcome":"success"}]}`, `unknown object member name "GTE"`},
		{"duplicate member", KindBreaker, `{"name":"a","enabled":true,"enabled":false}`, `duplicate object member name "enabled"`},
		{"invalid utf8", KindSignal, "{\"name\":\"a\",\"description\":\"\xff\"}", "invalid UTF-8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseJSON(tc.kind, []byte(tc.src))
			require.Error(t, err)
			require.Nil(t, spec)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestParseKeepsExplicitZeroValues(t *testing.T) {
	brk, err := ParseYAML(KindBreaker, []byte(`
name: quiet
enabled: false
trip: {risk_ratio_gte: 0, distinct_captcha_identities_gte: 0, success_ratio_lte: 0}
revert_recent_cooldowns: false
`))
	require.NoError(t, err)
	b := brk.(*BreakerSpec)
	require.False(t, b.Enabled)
	require.Zero(t, b.Trip)
	require.Equal(t, RevertNone, b.RevertRecentCooldowns)
	require.Equal(t, DefaultBreakerBuckets, b.Buckets)

	partial, err := ParseYAML(KindBreaker, []byte("name: p\ntrip: {risk_ratio_gte: 0.5}\nrevert_recent_cooldowns: true\n"))
	require.NoError(t, err)
	p := partial.(*BreakerSpec)
	require.True(t, p.Enabled)
	require.Equal(t, TripSpec{RiskRatioGte: 0.5, DistinctCaptchaIdentitiesGte: 10, SuccessRatioLte: 0.2}, p.Trip)
	require.Equal(t, RevertEndpoint, p.RevertRecentCooldowns)

	rot, err := ParseJSON(KindRotation, []byte(`{"name":"r","proxy":{"rebind_tolerance":0,"max_rebinds_per_day":0}}`))
	require.NoError(t, err)
	r := rot.(*RotationSpec)
	require.Zero(t, r.Proxy.RebindTolerance)
	require.Zero(t, r.Proxy.MaxRebindsPerDay)

	act, err := ParseYAML(KindAction, []byte("name: a\ncross_attribution: {enabled: false}\nhealth: {baseline: 0, quarantine_score: 0}\n"))
	require.NoError(t, err)
	a := act.(*ActionSpec)
	require.False(t, a.CrossAttribution.Enabled)
	require.Equal(t, DefaultCrossAttributionWindow, a.CrossAttribution.Window.Std())
	require.Zero(t, a.Health.Baseline)
	require.Zero(t, a.Health.QuarantineScore)
	require.Equal(t, DefaultEndpointLowScore, a.Health.EndpointLowScore)

	// JSON null leaves the default in place.
	nulls, err := ParseJSON(KindBreaker, []byte(`{"name":"n","enabled":null,"revert_recent_cooldowns":null}`))
	require.NoError(t, err)
	require.True(t, nulls.(*BreakerSpec).Enabled)
	require.Equal(t, RevertEndpoint, nulls.(*BreakerSpec).RevertRecentCooldowns)
}

func TestParseNormalizesScalarsAndNumbers(t *testing.T) {
	src := `
name: web-signals
extends: base
rules:
  - when:
      http_status: 200
      business_code: [10001, "abc", 1.50, 0x10, 18446744073709551615]
      markers: captcha_page
      method: get
      latency_ms: {gt: 100, lte: 2000}
      uri: {prefix: /api/}
    outcome: forbidden
    blame: both
`
	spec, err := ParseYAML(KindSignal, []byte(src))
	require.NoError(t, err)
	s := spec.(*SignalSpec)
	w := s.Rules[0].When
	require.Equal(t, []int{200}, w.HTTPStatus.Values)
	require.Equal(t, StringList{"10001", "abc", "1.5", "16", "18446744073709551615"}, w.BusinessCode)
	require.Equal(t, StringList{"captcha_page"}, w.Markers)
	require.EqualValues(t, 100, *w.LatencyMs.Gt)
	require.EqualValues(t, 2000, *w.LatencyMs.Lte)
	require.Equal(t, "base", ParentName(s))

	js, err := MarshalJSON(s)
	require.NoError(t, err)
	require.Contains(t, string(js), `"business_code":["10001","abc","1.5","16","18446744073709551615"]`)
	require.Contains(t, string(js), `"http_status":[200]`)
	require.Contains(t, string(js), `"latency_ms":{"gt":100,"lte":2000}`)

	zeros, err := ParseYAML(KindSignal, []byte("name: z\nrules:\n  - when: {business_code: [0000, 0010]}\n    outcome: success\n"))
	require.NoError(t, err)
	zs := zeros.(*SignalSpec)
	require.Equal(t, StringList{"0000", "0010"}, zs.Rules[0].When.BusinessCode)
	zc, err := CompileSignal([]*SignalSpec{zs})
	require.NoError(t, err)
	require.Equal(t, OutcomeSuccess, zc.Classify(ReportFacts{BusinessCode: "0000"}).Outcome)
	require.Equal(t, OutcomeUnknown, zc.Classify(ReportFacts{BusinessCode: "0"}).Outcome)
	require.Equal(t, OutcomeUnknown, zc.Classify(ReportFacts{BusinessCode: "8"}).Outcome)
	zyaml, err := MarshalYAML(zs)
	require.NoError(t, err)
	zback, err := ParseYAML(KindSignal, zyaml)
	require.NoError(t, err)
	require.Equal(t, zs, zback, "leading zeros survive a YAML round trip:\n%s", zyaml)

	fromJSON, err := ParseJSON(KindSignal, []byte(`{"name":"web-signals","extends":"base","rules":[{"when":{"http_status":200,"business_code":[10001,"abc",1.50,1e3,1e400],"markers":"captcha_page","method":["get"],"latency_ms":{"gt":100,"lte":2000},"uri":{"prefix":"/api/"}},"outcome":"forbidden","blame":"both"}]}`))
	require.NoError(t, err)
	require.Equal(t, StringList{"10001", "abc", "1.5", "1000", "1e400"}, fromJSON.(*SignalSpec).Rules[0].When.BusinessCode)
}

func TestAliasesAreResolved(t *testing.T) {
	src := `
name: aliases
x-codes: &codes [429, 430]
`
	_, err := ParseYAML(KindSignal, []byte(src))
	require.ErrorContains(t, err, "field x-codes not found")

	src = `
name: aliases
rules:
  - when: {http_status: &codes [429, 430], markers: &m [a]}
    outcome: rate_limited
  - when: {http_status: *codes, markers: *m}
    outcome: captcha
`
	spec, err := ParseYAML(KindSignal, []byte(src))
	require.NoError(t, err)
	s := spec.(*SignalSpec)
	require.Equal(t, s.Rules[0].When.HTTPStatus, s.Rules[1].When.HTTPStatus)
	require.Equal(t, s.Rules[0].When.Markers, s.Rules[1].When.Markers)
}

func TestMarshalDoesNotMutateAndAppliesDefaults(t *testing.T) {
	spec := &ActionSpec{
		Name: "manual",
		Rules: []ActionRule{{
			When: ActionWhen{Outcome: OutcomeList{OutcomeRateLimited}}, Action: ActionCooldown,
			Scope: ScopeIdentityEndpoint, Base: durationx.Duration(time.Minute),
		}},
	}
	out, err := MarshalJSON(spec)
	require.NoError(t, err)
	require.Zero(t, spec.Rules[0].Multiplier, "input must not be modified")
	require.Empty(t, spec.Mode)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out, &decoded))
	require.Equal(t, "enforce", decoded["mode"])
	rule := decoded["rules"].([]any)[0].(map[string]any)
	require.Equal(t, 1.0, rule["multiplier"])
	require.Equal(t, "1d", rule["max"])
	require.Equal(t, []any{"rate_limited"}, rule["when"].(map[string]any)["outcome"])
}

func TestMarshalYAMLShapes(t *testing.T) {
	out, err := MarshalYAML(Default(KindAction))
	require.NoError(t, err)
	text := string(out)
	require.Contains(t, text, "outcome: rate_limited\n", "single outcome is a scalar")
	require.Contains(t, text, "duration: permanent")

	out, err = MarshalYAML(Default(KindSignal))
	require.NoError(t, err)
	require.Contains(t, string(out), "gte: 500")
	require.NotContains(t, string(out), "blame")

	multi := &ActionSpec{Name: "m", Rules: []ActionRule{{
		When: ActionWhen{Outcome: OutcomeList{OutcomeCaptcha, OutcomeForbidden}}, Action: ActionExpire, Scope: ScopeIdentity,
	}}}
	out, err = MarshalYAML(multi)
	require.NoError(t, err)
	require.Contains(t, string(out), "- captcha\n")
}

func TestMarshalErrors(t *testing.T) {
	var nilRotation *RotationSpec
	for _, spec := range []Spec{nil, nilRotation, (*SignalSpec)(nil), (*ActionSpec)(nil), (*BreakerSpec)(nil)} {
		_, err := MarshalJSON(spec)
		require.ErrorContains(t, err, "spec is nil")
		_, err = MarshalYAML(spec)
		require.ErrorContains(t, err, "spec is nil")
		_, err = Clone(spec)
		require.ErrorContains(t, err, "spec is nil")
	}
	bad := &RotationSpec{Name: "neg", Rotation: RotationParams{LeaseTTL: durationx.Duration(-5 * time.Second)}}
	_, err := MarshalJSON(bad)
	require.ErrorContains(t, err, "copy rotation policy")
	_, err = MarshalYAML(bad)
	require.ErrorContains(t, err, "copy rotation policy")
}

func TestCloneIsDeep(t *testing.T) {
	orig := Default(KindSignal).(*SignalSpec)
	c, err := Clone(orig)
	require.NoError(t, err)
	cp := c.(*SignalSpec)
	require.Equal(t, orig, cp)
	cp.Rules[0].When.ErrorKind[0] = "changed"
	cp.Rules[5].When.HTTPStatus.Range.Gte = i64(1)
	require.Equal(t, ErrorKindProxyAuth, orig.Rules[0].When.ErrorKind[0])
	require.EqualValues(t, 500, *orig.Rules[5].When.HTTPStatus.Range.Gte)
}

func TestValidationErrorFormatting(t *testing.T) {
	_, err := ParseYAML(KindRotation, []byte("name: Bad Name\nrotation: {candidate_sample: 1000}\n"))
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, KindRotation, ve.Kind)
	require.Equal(t, "Bad Name", ve.Name)
	require.Len(t, ve.Problems, 2)
	msg := err.Error()
	require.True(t, strings.HasPrefix(msg, `invalid rotation policy "Bad Name": 2 problems:`), msg)
	require.Contains(t, msg, "\n  - name: must match")
	require.Contains(t, msg, "\n  - rotation.candidate_sample: must be between 1 and 256 (got 1000)")

	_, err = ParseYAML(KindBreaker, []byte("buckets: 12\n"))
	require.EqualError(t, err, "invalid breaker policy: name: is required")
	require.Equal(t, "plain", Problem{Message: "plain"}.String())
}
