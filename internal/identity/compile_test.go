package identity

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allTypesSpec declares one field of every type.
func allTypesSpec() *TypeSpec {
	return &TypeSpec{
		Name: "all_types", Site: "example", Client: "app",
		Fields: map[string]FieldSpec{
			"device_id": {Type: FieldString, Required: true},
			"aid":       {Type: FieldNumber},
			"debug":     {Type: FieldBool},
			"cookies":   {Type: FieldCookieMap, Sensitive: true},
			"extra":     {Type: FieldJSON},
			"token":     {Type: FieldSecretRef},
		},
		UniqueBy: []string{"device_id"},
	}
}

func compileSpec(t testing.TB, spec *TypeSpec) *CompiledType {
	t.Helper()
	ct, err := Compile("ity_x", "sit_x", 3, spec)
	require.NoError(t, err)
	return ct
}

func TestCompile(t *testing.T) {
	spec := loadTestSpec(t, "web_cookie.yaml")
	ct, err := Compile("ity_1", "sit_1", 2, spec)
	require.NoError(t, err)
	require.Equal(t, "ity_1", ct.ID)
	require.Equal(t, "web_cookie", ct.Name)
	require.Equal(t, "sit_1", ct.SiteID)
	require.Equal(t, "web", ct.Client)
	require.Equal(t, 2, ct.Version)
	require.True(t, ct.HasSensitive())
	require.Equal(t, []string{"cookies"}, ct.required)

	// The compiled type does not alias the caller's spec.
	spec.Fields["late"] = FieldSpec{Type: FieldString}
	spec.Deliver["headers"].(map[string]any)["X-Late"] = "x"
	require.NotContains(t, ct.Spec.Fields, "late")
	require.NotContains(t, ct.Spec.Deliver["headers"], "X-Late")

	schemaCopy := ct.SchemaJSON()
	schemaCopy[0] = 'X'
	require.True(t, json.Valid(ct.SchemaJSON()))

	app := compileTestType(t, "app_device.yaml")
	require.True(t, app.HasSensitive())
	noSensitive := compileSpec(t, &TypeSpec{Name: "n", Site: "s", Client: "web", Fields: map[string]FieldSpec{"a": {Type: FieldString, Required: true}}})
	require.False(t, noSensitive.HasSensitive())
	require.Equal(t, ActivationProbe, noSensitive.Spec.Activation)
	require.Equal(t, []string{"a"}, noSensitive.Spec.UniqueBy)
}

func TestCompileErrors(t *testing.T) {
	_, err := Compile("ity_1", "sit_1", 1, nil)
	requireInvalid(t, err, "spec is nil")
	_, err = Compile("ity_1", "sit_1", -1, allTypesSpec())
	requireInvalid(t, err, "version -1 is negative")
	_, err = Compile("ity_1", "sit_1", 1, &TypeSpec{Name: "x"})
	requireInvalid(t, err, "site is required")
}

func TestNormalize(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{
			name: "typed values",
			in: map[string]any{
				"device_id": "d1", "aid": 1001.0, "debug": true,
				"cookies": map[string]any{"sid": "x"}, "extra": map[string]any{"a": []any{1.0, "b"}}, "token": "signing/token",
			},
			want: map[string]any{
				"device_id": "d1", "aid": 1001.0, "debug": true,
				"cookies": map[string]string{"sid": "x"}, "extra": map[string]any{"a": []any{1.0, "b"}}, "token": "signing/token",
			},
		},
		{
			name: "string coercion from csv",
			in:   map[string]any{"device_id": "d1", "aid": " 1001 ", "debug": "false", "cookies": "sid=x; tt=1%7Cabc", "extra": "plain"},
			want: map[string]any{"device_id": "d1", "aid": 1001.0, "debug": false, "cookies": map[string]string{"sid": "x", "tt": "1%7Cabc"}, "extra": "plain"},
		},
		{
			name: "json numbers and ints",
			in:   map[string]any{"device_id": "d1", "aid": json.Number("1.5e3"), "extra": map[string]any{"n": json.Number("7"), "i": int64(3)}},
			want: map[string]any{"device_id": "d1", "aid": 1500.0, "extra": map[string]any{"n": 7.0, "i": 3.0}},
		},
		{
			name: "numeric strings",
			in:   map[string]any{"device_id": "d1", "aid": "-.5e-2", "debug": "1"},
			want: map[string]any{"device_id": "d1", "aid": -0.005, "debug": true},
		},
		{
			name: "browser export cookies and null optional values",
			in:   map[string]any{"device_id": "d1", "aid": nil, "extra": nil, "cookies": []any{map[string]any{"name": "sid", "value": "v"}}},
			want: map[string]any{"device_id": "d1", "cookies": map[string]string{"sid": "v"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ct.Normalize(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	first, err := ct.Normalize(map[string]any{
		"device_id": "d1", "aid": "7", "debug": "1", "token": "a/b",
		"cookies": "b=2; a=1", "extra": map[string]any{"n": json.Number("3"), "l": []any{"x"}},
	})
	require.NoError(t, err)
	second, err := ct.Normalize(first)
	require.NoError(t, err)
	require.Equal(t, first, second)

	// A payload decoded back from stored JSON normalizes to the same value.
	stored, err := json.Marshal(first)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(stored, &decoded))
	third, err := ct.Normalize(decoded)
	require.NoError(t, err)
	require.Equal(t, first, third)
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	extra := map[string]any{"a": json.Number("1")}
	in := map[string]any{"device_id": "d1", "cookies": "a=1", "extra": extra}
	got, err := ct.Normalize(in)
	require.NoError(t, err)
	require.Equal(t, "a=1", in["cookies"])
	require.Equal(t, json.Number("1"), extra["a"])
	got["extra"].(map[string]any)["a"] = "changed"
	require.Equal(t, json.Number("1"), extra["a"])
}

func TestNormalizeErrors(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	tests := []struct {
		name     string
		in       map[string]any
		contains []string
	}{
		{"nil payload", nil, []string{"device_id: required field is missing"}},
		{"missing required", map[string]any{"aid": 1.0}, []string{"device_id: required field is missing"}},
		{"null required", map[string]any{"device_id": nil}, []string{"device_id: required field is missing"}},
		{"unknown field", map[string]any{"device_id": "d", "bogus": 1.0}, []string{`"bogus": unknown field`}},
		{"string type", map[string]any{"device_id": 12.0}, []string{"device_id: expected a string"}},
		{"number type", map[string]any{"device_id": "d", "aid": "12abc"}, []string{"aid: expected a number"}},
		{"number bool", map[string]any{"device_id": "d", "aid": true}, []string{"aid: expected a number"}},
		{"number out of range", map[string]any{"device_id": "d", "aid": "1e400"}, []string{"aid: number is out of range"}},
		{"number nan", map[string]any{"device_id": "d", "aid": math.NaN()}, []string{"aid: number is not finite"}},
		{"bool type", map[string]any{"device_id": "d", "debug": "maybe"}, []string{"debug: expected a boolean"}},
		{"bool number", map[string]any{"device_id": "d", "debug": 1.0}, []string{"debug: expected a boolean"}},
		{"cookie map", map[string]any{"device_id": "d", "cookies": "broken"}, []string{"cookies: cookie pair 1 has no '='"}},
		{"secret ref pattern", map[string]any{"device_id": "d", "token": "../Etc"}, []string{"token: expected a secret path"}},
		{"secret ref type", map[string]any{"device_id": "d", "token": 1.0}, []string{"token: expected a secret path"}},
		{"json unsupported", map[string]any{"device_id": "d", "extra": struct{}{}}, []string{"extra: unsupported value"}},
		{"string invalid utf8", map[string]any{"device_id": "d\xff"}, []string{"device_id: string is not valid UTF-8"}},
		{"json invalid utf8", map[string]any{"device_id": "d", "extra": map[string]any{"k": []any{"\xc3"}}}, []string{"extra: string is not valid UTF-8"}},
		{"json key invalid utf8", map[string]any{"device_id": "d", "extra": map[string]any{"k\xff": 1.0}}, []string{"extra: string is not valid UTF-8"}},
		{"cookie invalid utf8", map[string]any{"device_id": "d", "cookies": "a=\xff"}, []string{"cookies: cookie pair 1: value contains invalid characters"}},
		{"all problems reported", map[string]any{"aid": "x", "debug": "y", "zzz": 1.0}, []string{"aid: expected a number", "debug: expected a boolean", `"zzz": unknown field`, "device_id: required field is missing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ct.Normalize(tt.in)
			require.Nil(t, got)
			requireInvalid(t, err, append([]string{`invalid payload for identity type "all_types"`}, tt.contains...)...)
		})
	}
}

func TestNormalizeProblemsAreCapped(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	payload := map[string]any{"device_id": "d"}
	for i := range 100 {
		payload[fmt.Sprintf("unknown_%03d", i)] = 1.0
	}
	_, err := ct.Normalize(payload)
	requireInvalid(t, err, `"unknown_000": unknown field`, "and 68 more problems")
	require.NotContains(t, err.Error(), "unknown_099")
}

func TestNormalizeRequiredFieldFailureNotReportedTwice(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	_, err := ct.Normalize(map[string]any{"device_id": 5.0})
	requireInvalid(t, err, "device_id: expected a string")
	require.NotContains(t, err.Error(), "required field is missing")
}

func TestNormalizeErrorsNeverLeakValues(t *testing.T) {
	ct := compileSpec(t, allTypesSpec())
	const secret = "SUPERSECRETVALUE"
	_, err := ct.Normalize(map[string]any{
		"device_id": "d",
		"aid":       secret,
		"debug":     secret,
		"cookies":   "a=1; " + secret,
		"token":     "UPPER" + secret,
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), secret)
}

func TestUniqueKey(t *testing.T) {
	web := compileTestType(t, "web_cookie.yaml")
	key, err := web.UniqueKey(map[string]any{"cookies": map[string]string{"sessionid": "a1b2c3", "csrf_token": "x"}})
	require.NoError(t, err)
	require.Equal(t, `["a1b2c3"]`, string(key))

	// The same cookie in another representation yields the same key.
	for _, cookies := range []any{
		"csrf_token=other; sessionid=a1b2c3",
		map[string]any{"sessionid": "a1b2c3"},
		[]any{map[string]any{"name": "sessionid", "value": "a1b2c3"}},
	} {
		other, err := web.UniqueKey(map[string]any{"cookies": cookies})
		require.NoError(t, err)
		require.Equal(t, key, other)
	}

	spec := allTypesSpec()
	spec.UniqueBy = []string{"extra.ids.1", "aid", "debug", "device_id", "cookies", "token"}
	ct := compileSpec(t, spec)
	payload := map[string]any{
		"device_id": "d1", "aid": "42", "debug": "true", "token": "a/b",
		"cookies": map[string]string{"b": "2", "a": "1"},
		"extra":   map[string]any{"ids": []any{"x", map[string]any{"z": 1.0, "k": "v"}}},
	}
	key1, err := ct.UniqueKey(payload)
	require.NoError(t, err)
	require.Equal(t, `[{"k":"v","z":1},42,true,"d1",{"a":"1","b":"2"},"a/b"]`, string(key1))
	for range 10 {
		again, err := ct.UniqueKey(map[string]any{
			"token": "a/b", "debug": true, "aid": 42.0, "device_id": "d1",
			"cookies": "b=2; a=1",
			"extra":   map[string]any{"ids": []any{"x", map[string]any{"k": "v", "z": json.Number("1")}}},
		})
		require.NoError(t, err)
		require.Equal(t, key1, again)
	}
}

func TestUniqueKeyErrors(t *testing.T) {
	web := compileTestType(t, "web_cookie.yaml")
	spec := allTypesSpec()
	spec.UniqueBy = []string{"aid", "extra.a"}
	ct := compileSpec(t, spec)
	tests := []struct {
		name     string
		ct       *CompiledType
		in       map[string]any
		contains string
	}{
		{"missing field", web, map[string]any{}, `unique_by path "cookies.sessionid" is missing`},
		{"missing cookie", web, map[string]any{"cookies": map[string]string{"csrf_token": "x"}}, `unique_by path "cookies.sessionid" is missing`},
		{"empty cookie", web, map[string]any{"cookies": "sessionid="}, `unique_by path "cookies.sessionid" is missing`},
		{"invalid cookies", web, map[string]any{"cookies": 5.0}, `unique_by path "cookies.sessionid": cookie map must be`},
		{"bad number", ct, map[string]any{"aid": "abc", "extra": map[string]any{"a": 1.0}}, `unique_by path "aid": expected a number`},
		{"missing json path", ct, map[string]any{"aid": 1.0, "extra": map[string]any{"b": 1.0}}, `unique_by path "extra.a" is missing`},
		{"empty json object", ct, map[string]any{"aid": 1.0, "extra": map[string]any{"a": map[string]any{}}}, `unique_by path "extra.a" is missing`},
		{"unsupported json", ct, map[string]any{"aid": 1.0, "extra": map[string]any{"a": struct{}{}}}, `unique_by path "extra.a": unsupported value`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := tt.ct.UniqueKey(tt.in)
			require.Nil(t, key)
			requireInvalid(t, err, tt.contains)
		})
	}

	spec = allTypesSpec()
	spec.UniqueBy = []string{"cookies", "extra"}
	whole := compileSpec(t, spec)
	_, err := whole.UniqueKey(map[string]any{"cookies": map[string]string{}, "extra": []any{}})
	requireInvalid(t, err, `unique_by path "cookies" is missing`)
	_, err = whole.UniqueKey(map[string]any{"cookies": "a=1", "extra": []any{}})
	requireInvalid(t, err, `unique_by path "extra" is missing`)
}

func TestCoerceHelpers(t *testing.T) {
	for _, s := range []string{"1", "-1", "+1.", ".5", "1e3", "1.5E-3"} {
		_, err := coerceNumber(s)
		require.NoError(t, err, s)
	}
	for _, s := range []string{"", "0x10", "1_000", "NaN", "Inf", "1e", "--1", "1 2"} {
		_, err := coerceNumber(s)
		require.Error(t, err, s)
	}
	_, err := coerceNumber(json.Number("bad"))
	require.Error(t, err)
	b, err := coerceBool(" TRUE ")
	require.NoError(t, err)
	require.True(t, b)
	require.True(t, isEmptyValue([]any{}))
	require.False(t, isEmptyValue(0.0))
	require.Equal(t, "x", errorMessage(invalidf("x")))
	require.Equal(t, strings.Repeat("y", 3), errorMessage(errString("yyy")))
}

type errString string

func (e errString) Error() string { return string(e) }
