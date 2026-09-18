package identity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCookieMap(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want map[string]string
	}{
		{"object", map[string]any{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc"}, map[string]string{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc"}},
		{"string map", map[string]string{"a": "1"}, map[string]string{"a": "1"}},
		{"empty object", map[string]any{}, map[string]string{}},
		{"header", "sessionid=a1b2c3; csrf_token=1%7Cabc", map[string]string{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc"}},
		{"header with spaces and empty pairs", "  a=1 ;; b = 2 ;\t", map[string]string{"a": "1", "b": "2"}},
		{"header keeps '=' in values", "token=abc==; x=a=b", map[string]string{"token": "abc==", "x": "a=b"}},
		{"header prefix", "Cookie: a=1; b=", map[string]string{"a": "1", "b": ""}},
		{"header lower-case prefix", "cookie:a=1", map[string]string{"a": "1"}},
		{"empty header", "", map[string]string{}},
		{"header duplicates last wins", "a=1; a=2", map[string]string{"a": "2"}},
		{"header quoted value", `a="x y, z"`, map[string]string{"a": `"x y, z"`}},
		{"browser export", []any{
			map[string]any{"name": "sessionid", "value": "a1b2c3", "domain": ".target.example.com", "httpOnly": true},
			map[string]any{"name": "csrf_token", "value": "1%7Cabc", "path": "/"},
		}, map[string]string{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc"}},
		{"typed browser export", []map[string]any{{"name": "a", "value": "1"}}, map[string]string{"a": "1"}},
		{"unicode value", map[string]any{"a": "中文"}, map[string]string{"a": "中文"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCookieMap(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseCookieMapErrors(t *testing.T) {
	manyHeader := strings.Repeat("a=1;", 1) + func() string {
		var b strings.Builder
		for i := 0; i <= MaxCookies; i++ {
			b.WriteString("c")
			b.WriteString(strings.Repeat("x", i%7))
			b.WriteString(string(rune('a' + i%26)))
			b.WriteString(strings.Repeat("y", i/26))
			b.WriteString("=1;")
		}
		return b.String()
	}()
	manyMap := make(map[string]any, MaxCookies+1)
	manyArray := make([]any, MaxCookies+1)
	for i := 0; i <= MaxCookies; i++ {
		manyMap["c"+strings.Repeat("x", i)] = "1"
		manyArray[i] = map[string]any{"name": "c", "value": "1"}
	}
	tests := []struct {
		name string
		in   any
		err  string
	}{
		{"nil", nil, "cookie map is null"},
		{"number", 12.0, "cookie map must be an object"},
		{"object non-string value", map[string]any{"a": 1.0}, `cookie "a": value must be a string`},
		{"object bad value", map[string]any{"a": "x\ny"}, `cookie "a": value contains invalid characters`},
		{"string map bad name", map[string]string{"a b": "1"}, "contains a control or whitespace character"},
		{"string map too many", func() map[string]string {
			m := make(map[string]string, MaxCookies+1)
			for k := range manyMap {
				m[k] = "1"
			}
			return m
		}(), "maximum is 1024"},
		{"header missing equals", "a=1; broken", "cookie pair 2 has no '='"},
		{"header empty name", "=1", "cookie name is empty"},
		{"header control char", "a=\x01", "invalid characters"},
		{"header too many", manyHeader, "maximum is 1024"},
		{"name with equals in map", map[string]any{"a=b": "1"}, `contains "="`},
		{"name with semicolon", map[string]string{"a;b": "1"}, `contains ";"`},
		{"name with comma", map[string]string{"a,b": "1"}, `contains ","`},
		{"name with quote", map[string]string{`a"b`: "1"}, `contains "\""`},
		{"name with DEL", map[string]string{"a\x7f": "1"}, "control or whitespace"},
		{"name too long", map[string]string{strings.Repeat("n", MaxCookieNameBytes+1): "1"}, "is too long"},
		{"value too long", map[string]string{"a": strings.Repeat("v", MaxCookieValueBytes+1)}, "invalid characters or is too long"},
		{"value with semicolon", map[string]string{"a": "x;y"}, "invalid characters"},
		{"map too many", manyMap, "maximum is 1024"},
		{"array too many", manyArray, "maximum is 1024"},
		{"array element not object", []any{"a=1"}, "cookie #1 must be an object"},
		{"array missing name", []any{map[string]any{"value": "1"}}, "cookie #1: name must be a string"},
		{"array value not string", []any{map[string]any{"name": "a", "value": 1.0}}, "cookie #1: value must be a string"},
		{"array bad name", []any{map[string]any{"name": "", "value": "1"}}, "cookie #1: cookie name is empty"},
		{"array bad value", []any{map[string]any{"name": "a", "value": "x"}, map[string]any{"name": "b", "value": "\x00"}}, "cookie #2: value contains invalid characters"},
		{"header bad name position", "a=1; b c=2", "cookie pair 2: cookie name contains a control or whitespace character"},
		{"header bad value position", "a=1; b=\x7f", "cookie pair 2: value contains invalid characters"},
		{"invalid utf8 name", map[string]string{"a\xff": "1"}, "is not valid UTF-8"},
		{"invalid utf8 value", map[string]any{"a": "\xff"}, `cookie "a": value contains invalid characters`},
		{"invalid utf8 header value", "a=\xc3", "cookie pair 1: value contains invalid characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCookieMap(tt.in)
			require.Nil(t, got)
			requireInvalid(t, err, tt.err)
		})
	}
}

func TestParseCookieMapErrorsNeverLeakValues(t *testing.T) {
	const secret = "TOPSECRETVALUE"
	for _, in := range []any{
		"a=1; " + secret,
		map[string]any{"a": secret + "\n"},
		map[string]any{"a": secret + ";"},
		[]any{map[string]any{"name": "a", "value": secret + "\x00"}},
		// Malformed pairs whose name part carries value text.
		"sessionid " + secret + "==",
		"sessionid:" + secret + ",x=1",
		`"` + secret + `"=1`,
		[]any{map[string]any{"name": "sid " + secret, "value": "1"}},
	} {
		_, err := ParseCookieMap(in)
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	}
}

func TestCookieHeader(t *testing.T) {
	require.Equal(t, "", cookieHeader(nil))
	require.Equal(t, "a=; b=2; sessionid=a1b2c3", cookieHeader(map[string]string{"sessionid": "a1b2c3", "b": "2", "a": ""}))
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "short", truncate("short"))
	long := strings.Repeat("a", 63) + "中文"
	got := truncate(long)
	require.True(t, strings.HasSuffix(got, "…"))
	require.Equal(t, strings.Repeat("a", 63)+"…", got)
	require.Equal(t, strings.Repeat("b", 64)+"…", truncate(strings.Repeat("b", 100)))
}
