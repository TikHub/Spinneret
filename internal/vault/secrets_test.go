package vault_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/vault"
)

func TestValidateSecretPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		path  string
		valid bool
	}{
		{"simple", "key", true},
		{"nested", "signing/api_key", true},
		{"digits first", "0abc/x-y.z", true},
		{"single char", "a", true},
		{"dots inside segment", "a/b.c..d", true},
		{"max length", strings.Repeat("a", vault.MaxSecretPathLength), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", vault.MaxSecretPathLength+1), false},
		{"upper case", "Shop/key", false},
		{"leading slash", "/signing/key", false},
		{"trailing slash", "signing/key/", false},
		{"double slash", "signing//key", false},
		{"dot segment", "signing/./key", false},
		{"dotdot segment", "signing/../key", false},
		{"trailing dotdot", "signing/..", false},
		{"leading dot", ".hidden", false},
		{"leading underscore", "_key", false},
		{"leading dash", "-key", false},
		{"space", "dou yin", false},
		{"star", "signing/*", false},
		{"unicode", "密钥", false},
		{"backslash", "a\\b", false},
		{"nul", "a\x00b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := vault.ValidateSecretPath(tt.path)
			if tt.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
		})
	}
}

func TestMaskSecretValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  string
	}{
		{"", vault.SecretMask},
		{"abc", vault.SecretMask},
		{"12345678", vault.SecretMask},
		{"123456789", vault.SecretMask + "6789"},
		{"sk-live-abcdefghijkl", vault.SecretMask + "ijkl"},
		{"密钥密钥密钥密钥", vault.SecretMask},
		{"密钥密钥密钥密钥值", vault.SecretMask + "钥密钥值"},
		{"abcdefgh😀😀", vault.SecretMask + "gh😀😀"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, vault.MaskSecretValue(tt.value), "value %q", tt.value)
	}
}

func TestSanitizePurpose(t *testing.T) {
	t.Parallel()
	require.Equal(t, "api", vault.SanitizePurpose(""))
	require.Equal(t, "api", vault.SanitizePurpose(" \t\n"))
	require.Equal(t, "config", vault.SanitizePurpose("con fig"))
	require.Equal(t, "ab", vault.SanitizePurpose("a\x00b\xff"))
	long := vault.SanitizePurpose(strings.Repeat("x", 100))
	require.Len(t, long, vault.MaxSecretPurposeLength)
	multi := vault.SanitizePurpose(strings.Repeat("é", 40)) // 2 bytes each
	require.LessOrEqual(t, len(multi), vault.MaxSecretPurposeLength)
	require.True(t, strings.HasPrefix(strings.Repeat("é", 40), multi))
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()
	require.Equal(t, "abc", vault.RewrapStateTruncate("abc", 10))
	require.Equal(t, "ab", vault.RewrapStateTruncate("abcdef", 2))
	require.Equal(t, "é", vault.RewrapStateTruncate("éé", 3))
}
