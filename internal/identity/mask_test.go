package identity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaskString(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "••••"},
		{"abc", "••••"},
		{"abcd", "••••"},
		{"abcde", "••••bcde"},
		{"x9y8z7", "••••y8z7"},
		{"密码是中文字符", "••••中文字符"},
		{"ab中文字符", "••••中文字符"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, MaskString(tt.in))
		})
	}
}

func TestMaskWebCookie(t *testing.T) {
	ct := compileTestType(t, "web_cookie.yaml")
	payload := map[string]any{
		"cookies":    map[string]string{"sessionid": "a1b2c3", "csrf_token": "1%7Cabc", "x": "1"},
		"user_agent": "Mozilla/5.0 ...",
		"signature":  "x9y8z7",
	}
	got := ct.Mask(payload)
	require.Equal(t, map[string]any{
		"cookies":    map[string]any{"sessionid": "••••b2c3", "csrf_token": "••••Cabc", "x": "••••"},
		"user_agent": "Mozilla/5.0 ...",
		"signature":  "••••y8z7",
	}, got)
	// The input is untouched.
	require.Equal(t, "a1b2c3", payload["cookies"].(map[string]string)["sessionid"])
	require.Equal(t, "x9y8z7", payload["signature"])
}

func TestMaskAllTypes(t *testing.T) {
	spec := allTypesSpec()
	spec.Fields["secret_json"] = FieldSpec{Type: FieldJSON, Sensitive: true}
	spec.Fields["secret_num"] = FieldSpec{Type: FieldNumber, Sensitive: true}
	spec.Fields["secret_ref"] = FieldSpec{Type: FieldSecretRef, Sensitive: true}
	spec.Fields["plain_cookies"] = FieldSpec{Type: FieldCookieMap}
	ct := compileSpec(t, spec)

	extra := map[string]any{"nested": []any{"x"}}
	got := ct.Mask(map[string]any{
		"device_id":     "d1",
		"aid":           1001.0,
		"debug":         nil,
		"cookies":       "sid=abcdefgh; t=12",
		"extra":         extra,
		"secret_json":   map[string]any{"k": "v"},
		"secret_num":    42.0,
		"secret_ref":    "team/token",
		"plain_cookies": map[string]string{"a": "1"},
		"undeclared":    "value",
	})
	require.Equal(t, map[string]any{
		"device_id":     "d1",
		"aid":           1001.0,
		"debug":         nil,
		"cookies":       map[string]any{"sid": "••••efgh", "t": "••••"},
		"extra":         map[string]any{"nested": []any{"x"}},
		"secret_json":   "••••",
		"secret_num":    "••••",
		"secret_ref":    "••••oken",
		"plain_cookies": map[string]any{"a": "1"},
		"undeclared":    "••••",
	}, got)

	// Non-sensitive values are deep copies.
	got["extra"].(map[string]any)["nested"].([]any)[0] = "changed"
	require.Equal(t, "x", extra["nested"].([]any)[0])

	require.Nil(t, ct.Mask(nil))
	masked := ct.Mask(map[string]any{
		"cookies":       5.0,        // sensitive, unparsable
		"plain_cookies": "broken",   // not sensitive, unparsable: copied as is
		"extra":         struct{}{}, // not a JSON value
	})
	require.Equal(t, map[string]any{"cookies": "••••", "plain_cookies": "broken", "extra": "••••"}, masked)
}
