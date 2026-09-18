package configcenter

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

func TestParseSecretRefs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    []SecretRef
		wantErr string
	}{
		{name: "none", content: `{"a": 1}`, want: nil},
		{name: "latest", content: `key=${secret:signing/api_key}`, want: []SecretRef{{Path: "signing/api_key"}}},
		{name: "pinned", content: `${secret:a.b-c_d/e#12}`, want: []SecretRef{{Path: "a.b-c_d/e", Version: 12}}},
		{
			name:    "distinct in order",
			content: `${secret:b} ${secret:a} ${secret:b} ${secret:a#2} ${secret:a}`,
			want:    []SecretRef{{Path: "b"}, {Path: "a"}, {Path: "a", Version: 2}},
		},
		{name: "adjacent", content: `${secret:x}${secret:y}`, want: []SecretRef{{Path: "x"}, {Path: "y"}}},
		{name: "dollar without secret", content: `${env:HOME} $secret:x {secret:y}`, want: nil},
		{name: "unterminated", content: `${secret:abc`, wantErr: "unterminated"},
		{name: "too long", content: "${secret:" + strings.Repeat("a", 300) + "}", wantErr: "unterminated or too long"},
		{name: "uppercase path", content: `${secret:Abc}`, wantErr: "invalid secret reference"},
		{name: "empty path", content: `${secret:}`, wantErr: "invalid secret reference"},
		{name: "leading slash", content: `${secret:/a}`, wantErr: "invalid secret reference"},
		{name: "trailing slash", content: `${secret:a/}`, wantErr: "invalid secret reference"},
		{name: "double slash", content: `${secret:a//b}`, wantErr: "invalid secret reference"},
		{name: "dot segment", content: `${secret:a/./b}`, wantErr: "invalid secret reference"},
		{name: "dotdot segment", content: `${secret:a/../b}`, wantErr: "invalid secret reference"},
		{name: "empty version", content: `${secret:a#}`, wantErr: "positive integer"},
		{name: "zero version", content: `${secret:a#0}`, wantErr: "positive integer"},
		{name: "leading zero version", content: `${secret:a#01}`, wantErr: "positive integer"},
		{name: "negative version", content: `${secret:a#-1}`, wantErr: "positive integer"},
		{name: "non numeric version", content: `${secret:a#v1}`, wantErr: "positive integer"},
		{name: "version overflow", content: `${secret:a#9999999999}`, wantErr: "out of range"},
		{name: "two hashes", content: `${secret:a#1#2}`, wantErr: "positive integer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSecretRefs(tc.content)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParseSecretRefsLimit(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for i := range MaxSecretRefs {
		fmt.Fprintf(&sb, "${secret:p%d}", i)
	}
	refs, err := ParseSecretRefs(sb.String())
	require.NoError(t, err)
	require.Len(t, refs, MaxSecretRefs)

	sb.WriteString("${secret:one-more}")
	_, err = ParseSecretRefs(sb.String())
	require.ErrorContains(t, err, "more than")
}

func TestValidSecretPath(t *testing.T) {
	t.Parallel()
	require.True(t, ValidSecretPath("a"))
	require.True(t, ValidSecretPath("signing/api.key-1"))
	require.True(t, ValidSecretPath(strings.Repeat("a", 256)))
	require.False(t, ValidSecretPath(strings.Repeat("a", 257)))
	require.False(t, ValidSecretPath(""))
	require.False(t, ValidSecretPath("_a"))
	require.False(t, ValidSecretPath("a/.."))
}

func TestSubstituteSecretRefs(t *testing.T) {
	t.Parallel()
	values := map[SecretRef]string{
		{Path: "k"}:             `va"l\ue<&>` + "\n",
		{Path: "k", Version: 2}: "old",
	}
	tests := []struct {
		name, format, content, want string
	}{
		{name: "json escapes", format: FormatJSON, content: `{"a":"${secret:k}","b":"x-${secret:k#2}-y"}`,
			want: `{"a":"va\"l\\ue<&>\n","b":"x-old-y"}`},
		{name: "yaml raw", format: FormatYAML, content: "a: ${secret:k#2}\n", want: "a: old\n"},
		{name: "text raw", format: FormatText, content: "${secret:k}|${secret:k}", want: "va\"l\\ue<&>\n|va\"l\\ue<&>\n"},
		{name: "no refs", format: FormatText, content: "plain", want: "plain"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spans, err := scanSecretRefs(tc.content)
			require.NoError(t, err)
			got, err := substituteSecretRefs(tc.format, tc.content, spans, values)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			if tc.format == FormatJSON {
				var decoded map[string]string
				require.NoError(t, json.Unmarshal([]byte(got), &decoded))
				require.Equal(t, values[SecretRef{Path: "k"}], decoded["a"])
			}
		})
	}

	spans, err := scanSecretRefs("${secret:missing}")
	require.NoError(t, err)
	_, err = substituteSecretRefs(FormatText, "${secret:missing}", spans, values)
	require.Error(t, err)
	require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))
}

func TestSecretRefString(t *testing.T) {
	t.Parallel()
	require.Equal(t, "a/b", SecretRef{Path: "a/b"}.String())
	require.Equal(t, "a/b#3", SecretRef{Path: "a/b", Version: 3}.String())
}
