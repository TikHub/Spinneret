package policy

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestIntRangeBounds(t *testing.T) {
	tests := []struct {
		name   string
		r      IntRange
		lo, hi int64
		in     []int64
		out    []int64
	}{
		{"open", IntRange{}, math.MinInt64, math.MaxInt64, []int64{0, math.MaxInt64}, nil},
		{"gte lt", IntRange{Gte: i64(200), Lt: i64(300)}, 200, 299, []int64{200, 299}, []int64{199, 300}},
		{"gt lte", IntRange{Gt: i64(1), Lte: i64(3)}, 2, 3, []int64{2, 3}, []int64{1, 4}},
		{"gt max is empty", IntRange{Gt: i64(math.MaxInt64)}, math.MaxInt64, math.MinInt64, nil, []int64{math.MinInt64, 0, math.MaxInt64}},
		{"lt min is empty", IntRange{Lt: i64(math.MinInt64)}, math.MaxInt64, math.MinInt64, nil, []int64{math.MinInt64, 0, math.MaxInt64}},
		{"gte max", IntRange{Gte: i64(math.MaxInt64)}, math.MaxInt64, math.MaxInt64, []int64{math.MaxInt64}, []int64{math.MaxInt64 - 1}},
		{"lte min", IntRange{Lte: i64(math.MinInt64)}, math.MinInt64, math.MinInt64, []int64{math.MinInt64}, []int64{math.MinInt64 + 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := tc.r.Bounds()
			require.Equal(t, tc.lo, lo)
			require.Equal(t, tc.hi, hi)
			for _, v := range tc.in {
				require.True(t, tc.r.Contains(v), v)
			}
			for _, v := range tc.out {
				require.False(t, tc.r.Contains(v), v)
			}
		})
	}
}

func TestIntMatcherCodecs(t *testing.T) {
	tests := []struct {
		name     string
		yamlSrc  string
		jsonSrc  string
		want     IntMatcher
		wantJSON string
	}{
		{"list", "[200, 204]", "[200, 204]", IntMatcher{Values: []int{200, 204}}, "[200,204]"},
		{"scalar", "429", "429", IntMatcher{Values: []int{429}}, "[429]"},
		{"empty list", "[]", "[]", IntMatcher{Values: []int{}}, "[]"},
		{"range", "{gte: 200, lt: 300}", `{"gte":200,"lt":300}`, IntMatcher{Range: &IntRange{Gte: i64(200), Lt: i64(300)}}, `{"gte":200,"lt":300}`},
		{"hex yaml", "[0x1F4]", "[500]", IntMatcher{Values: []int{500}}, "[500]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var y IntMatcher
			require.NoError(t, yaml.Unmarshal([]byte(tc.yamlSrc), &y))
			require.Equal(t, tc.want, y)
			var j IntMatcher
			require.NoError(t, json.Unmarshal([]byte(tc.jsonSrc), &j))
			require.Equal(t, tc.want, j)
			out, err := json.Marshal(tc.want)
			require.NoError(t, err)
			require.JSONEq(t, tc.wantJSON, string(out))
			yout, err := yaml.Marshal(tc.want)
			require.NoError(t, err)
			var back IntMatcher
			require.NoError(t, yaml.Unmarshal(yout, &back))
			require.Equal(t, tc.want, back)
		})
	}

	var nilValues IntMatcher
	out, err := json.Marshal(nilValues)
	require.NoError(t, err)
	require.Equal(t, "[]", string(out))
	yout, err := yaml.Marshal(nilValues)
	require.NoError(t, err)
	require.Equal(t, "[]\n", string(yout))

	var m IntMatcher
	require.NoError(t, m.UnmarshalJSON([]byte("null")))
	require.Equal(t, IntMatcher{}, m)
	require.Error(t, m.UnmarshalJSON([]byte("  ")))
	require.ErrorContains(t, m.UnmarshalJSON([]byte(`{"gte":1} x`)), "invalid range")
	require.Error(t, yaml.Unmarshal([]byte("!!binary aGVsbG8="), &m))
	require.ErrorContains(t, yaml.Unmarshal([]byte("[9223372036854775808]"), &m), "invalid integer")
	for _, src := range []string{"[010]", "0200", "{gte: 0500}", "[-01]", "[+01]"} {
		require.ErrorContains(t, yaml.Unmarshal([]byte(src), &m), "must not have leading zeros", src)
	}
	require.NoError(t, yaml.Unmarshal([]byte("[0, 0o17]"), &m))
	require.Equal(t, IntMatcher{Values: []int{0, 15}}, m)
	require.True(t, hasLeadingZero("-007"))
	require.False(t, hasLeadingZero("0"))
	require.False(t, hasLeadingZero("0x10"))
	require.False(t, isDecimalLiteral("-"))
	require.False(t, isDecimalLiteral(""))

	require.ErrorContains(t, m.UnmarshalYAML(&yaml.Node{Kind: yaml.DocumentNode, Line: 3}), "line 3")
}

func TestStringListCodecs(t *testing.T) {
	tests := []struct {
		name    string
		yamlSrc string
		jsonSrc string
		want    StringList
	}{
		{"scalar string", "abc", `"abc"`, StringList{"abc"}},
		{"scalar number", "10001", "10001", StringList{"10001"}},
		{"negative number", "[-5]", "[-5]", StringList{"-5"}},
		{"float", "[2.50]", "[2.50]", StringList{"2.5"}},
		{"mixed", "[a, 1, \"2\"]", `["a", 1, "2"]`, StringList{"a", "1", "2"}},
		{"empty", "[]", "[]", StringList{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var y StringList
			require.NoError(t, yaml.Unmarshal([]byte(tc.yamlSrc), &y))
			require.Equal(t, tc.want, y)
			var j StringList
			require.NoError(t, json.Unmarshal([]byte(tc.jsonSrc), &j))
			require.Equal(t, tc.want, j)
		})
	}
	// YAML decimal literals are kept verbatim: leading zeros survive and are
	// never reinterpreted as octal; other numeric forms are normalized.
	var zeros StringList
	require.NoError(t, yaml.Unmarshal([]byte("[0000, 0010, 007, 09, -01, 0, -0, +5, 1_000, 0o17, 0x1F, 1e3, 10001]"), &zeros))
	require.Equal(t, StringList{"0000", "0010", "007", "09", "-01", "0", "-0", "5", "1000", "15", "31", "1000", "10001"}, zeros)
	var zero StringList
	require.NoError(t, yaml.Unmarshal([]byte("0000"), &zero))
	require.Equal(t, StringList{"0000"}, zero)
	var big StringList
	require.NoError(t, yaml.Unmarshal([]byte("[0xFFFFFFFFFFFFFFFF, !!int 42, !!float 7]"), &big))
	require.Equal(t, StringList{"18446744073709551615", "42", "7"}, big)
	require.ErrorContains(t, yaml.Unmarshal([]byte("[!!int abc]"), &big), `invalid integer "abc"`)
	var l StringList
	require.NoError(t, json.Unmarshal([]byte("null"), &l))
	require.Nil(t, l)
	out, err := json.Marshal(StringList(nil))
	require.NoError(t, err)
	require.Equal(t, "[]", string(out))
	yout, err := yaml.Marshal(StringList(nil))
	require.NoError(t, err)
	require.Equal(t, "[]\n", string(yout))
	require.True(t, StringList{"a", "b"}.Contains("b"))
	require.False(t, StringList{"a"}.Contains("c"))
	require.Error(t, json.Unmarshal([]byte(`"unterminated`), &l))
	require.Error(t, l.UnmarshalJSON([]byte(`"bad \q escape"`)))
	require.Error(t, l.UnmarshalJSON([]byte(``)))
	require.Error(t, l.UnmarshalJSON([]byte(`[1, 2`)))
	require.Error(t, l.UnmarshalJSON([]byte(`true`)))
	require.Error(t, yaml.Unmarshal([]byte("!!float x"), &l))
}

func TestOutcomeListCodecs(t *testing.T) {
	var y OutcomeList
	require.NoError(t, yaml.Unmarshal([]byte("captcha"), &y))
	require.Equal(t, OutcomeList{"captcha"}, y)
	require.NoError(t, yaml.Unmarshal([]byte("[captcha, banned]"), &y))
	require.Equal(t, OutcomeList{"captcha", "banned"}, y)
	require.True(t, y.Contains(OutcomeBanned))
	require.False(t, y.Contains(OutcomeEmpty))

	var j OutcomeList
	require.NoError(t, json.Unmarshal([]byte(`"captcha"`), &j))
	require.Equal(t, OutcomeList{"captcha"}, j)
	require.NoError(t, json.Unmarshal([]byte("null"), &j))
	require.Equal(t, OutcomeList{"captcha"}, j)
	require.Error(t, json.Unmarshal([]byte(`{"a":1}`), &j))
	require.Error(t, yaml.Unmarshal([]byte("{a: 1}"), &j))

	for _, tc := range []struct {
		list    OutcomeList
		yamlOut string
		jsonOut string
	}{
		{nil, "[]\n", "[]"},
		{OutcomeList{"captcha"}, "captcha\n", `["captcha"]`},
		{OutcomeList{"captcha", "banned"}, "- captcha\n- banned\n", `["captcha","banned"]`},
	} {
		yout, err := yaml.Marshal(tc.list)
		require.NoError(t, err)
		require.Equal(t, tc.yamlOut, string(yout))
		jout, err := json.Marshal(tc.list)
		require.NoError(t, err)
		require.Equal(t, tc.jsonOut, string(jout))
	}
}

func TestRevertModeCodecs(t *testing.T) {
	tests := []struct {
		src     string
		want    RevertMode
		wantErr bool
	}{
		{"true", RevertEndpoint, false},
		{"false", RevertNone, false},
		{"none", RevertNone, false},
		{"endpoint", RevertEndpoint, false},
		{"all", RevertAll, false},
		{"\"true\"", "true", false},
		{"1", "", true},
		{"[a]", "", true},
	}
	for _, tc := range tests {
		t.Run("yaml "+tc.src, func(t *testing.T) {
			var m RevertMode
			err := yaml.Unmarshal([]byte(tc.src), &m)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, m)
		})
	}
	jsonTests := []struct {
		src     string
		want    RevertMode
		wantErr bool
	}{
		{"true", RevertEndpoint, false},
		{"false", RevertNone, false},
		{`"all"`, RevertAll, false},
		{"null", "", false},
		{"3", "", true},
	}
	for _, tc := range jsonTests {
		t.Run("json "+tc.src, func(t *testing.T) {
			var m RevertMode
			err := json.Unmarshal([]byte(tc.src), &m)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, m)
		})
	}
	require.True(t, ValidRevertMode(RevertAll))
	require.False(t, ValidRevertMode("true"))
	var m RevertMode
	require.Error(t, m.UnmarshalYAML(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "maybe"}))
}

func TestStrictJSONTarget(t *testing.T) {
	var r IntRange
	require.NoError(t, strictJSON([]byte(`{"gte":1}`), &r))
	require.EqualValues(t, 1, *r.Gte)
	require.Error(t, strictJSON([]byte(`{}`), map[string]any{}), "non-pointer targets are rejected")
	require.Error(t, strictJSON([]byte(`{}`), nil))
}

func TestResolveAliasNil(t *testing.T) {
	require.Nil(t, resolveAlias(nil))
	n := &yaml.Node{Kind: yaml.AliasNode}
	require.Same(t, n, resolveAlias(n))
}
