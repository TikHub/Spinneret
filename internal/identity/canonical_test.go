package identity

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalJSON(t *testing.T) {
	type sample struct {
		B string `json:"b"`
		A int    `json:"a"`
	}
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, `null`},
		{"true", true, `true`},
		{"false", false, `false`},
		{"string plain", "abc", `"abc"`},
		{"no html escaping", "<a href='x'>&</a>", `"<a href='x'>&</a>"`},
		{"escapes", "q\"b\\s\b\f\n\r\t\x01\x1f", `"q\"b\\s\b\f\n\r\t\u0001\u001f"`},
		{"unicode", "中文 é", `"中文 é"`},
		{"line separators", "a\u2028b\u2029c", `"a\u2028b\u2029c"`},
		{"invalid utf8", "a\xffb", `"a\ufffdb"`},
		{"integer float", 42.0, `42`},
		{"negative zero", math.Copysign(0, -1), `0`},
		{"fraction", 1.5, `1.5`},
		{"large integer", 123456789012345.0, `123456789012345`},
		{"just below 1e21", 1e20, `100000000000000000000`},
		{"integer at 1e21 has no exponent", 1e21, `1000000000000000000000`},
		{"negative huge integer has no exponent", -1.5e25, `-15000000000000000000000000`},
		{"exponent small", 1e-7, `1e-7`},
		{"exponent small fraction", -1.25e-9, `-1.25e-9`},
		{"small fraction", 0.000001, `0.000001`},
		{"float32", float32(0.1), `0.1`},
		{"int", 7, `7`},
		{"int8", int8(-8), `-8`},
		{"int16", int16(16), `16`},
		{"int32", int32(32), `32`},
		{"int64", int64(-64), `-64`},
		{"uint", uint(1), `1`},
		{"uint8", uint8(8), `8`},
		{"uint16", uint16(16), `16`},
		{"uint32", uint32(32), `32`},
		{"uint64", uint64(64), `64`},
		{"json number integer", json.Number("10"), `10`},
		{"json number float form", json.Number("10.0"), `10`},
		{"json number exponent", json.Number("1.5e2"), `150`},
		{"sorted map", map[string]any{"b": 1.0, "a": []any{"x", nil}, "C": map[string]any{}}, `{"C":{},"a":["x",null],"b":1}`},
		{"string map", map[string]string{"z": "1", "a": "<"}, `{"a":"<","z":"1"}`},
		{"empty array", []any{}, `[]`},
		{"string slice", []string{"b", "a"}, `["b","a"]`},
		{"struct fallback", sample{B: "x", A: 1}, `{"a":1,"b":"x"}`},
		{"nested struct in map", map[string]any{"s": &sample{B: "y"}}, `{"s":{"a":0,"b":"y"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalJSON(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
			require.True(t, json.Valid(got))
		})
	}
}

func TestCanonicalJSONErrors(t *testing.T) {
	var deep any = "x"
	for range maxJSONDepth + 1 {
		deep = []any{deep}
	}
	tests := []struct {
		name string
		in   any
		err  string
	}{
		{"nan", math.NaN(), "not finite"},
		{"inf", map[string]any{"x": math.Inf(1)}, "not finite"},
		{"float32 inf", float32(math.Inf(-1)), "not finite"},
		{"bad json number", json.Number("abc"), "invalid number"},
		{"huge json number", json.Number("1e400"), "invalid number"},
		{"unmarshalable", make(chan int), "marshal chan int"},
		{"too deep", deep, "nested deeper"},
		{"nested error in array", []any{math.NaN()}, "not finite"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CanonicalJSON(tt.in)
			require.ErrorContains(t, err, tt.err)
		})
	}
}

func TestCanonicalJSONDeterministic(t *testing.T) {
	a := map[string]any{"k1": 1.0, "k2": map[string]any{"x": "y", "a": []any{1.0, "2"}}, "k3": true}
	b := map[string]any{"k3": true, "k2": map[string]any{"a": []any{json.Number("1"), "2"}, "x": "y"}, "k1": int64(1)}
	ca, err := CanonicalJSON(a)
	require.NoError(t, err)
	for range 20 {
		cb, err := CanonicalJSON(b)
		require.NoError(t, err)
		require.Equal(t, ca, cb)
	}
}

func TestNormalizeJSONValue(t *testing.T) {
	got, err := normalizeJSONValue(map[any]any{"a": []string{"x"}, "b": map[string]string{"k": "v"}, "c": int32(3), "d": json.Number("2.5")}, 0)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": []any{"x"}, "b": map[string]any{"k": "v"}, "c": 3.0, "d": 2.5}, got)

	for _, bad := range []any{
		"a\xffb",
		map[string]any{"k\xff": "v"},
		map[string]string{"k": "\xff"},
		map[any]any{"k\xff": "v"},
		[]string{"\xff"},
		map[any]any{1: "x"},
		map[any]any{"a": make(chan int)},
		map[string]any{"a": struct{}{}},
		[]any{math.NaN()},
		json.Number("x"),
	} {
		_, err := normalizeJSONValue(bad, 0)
		require.Error(t, err, "%#v", bad)
	}
	_, err = normalizeJSONValue("x", maxJSONDepth+1)
	require.ErrorIs(t, err, errJSONTooDeep)
}
