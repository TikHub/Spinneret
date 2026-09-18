package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSessionTemplate(t *testing.T) {
	tests := []struct {
		tpl     string
		errPart string
	}{
		{tpl: ""},
		{tpl: "plain-user"},
		{tpl: "user-{username}-session-{identity_hash}"},
		{tpl: "{username}{password}{identity_id}{identity_hash}{lease_id}{random}"},
		{tpl: "user-{unknown}", errPart: "unknown placeholder {unknown}"},
		{tpl: "user-{}", errPart: "unknown placeholder"},
		{tpl: "user-{username", errPart: "unterminated"},
		{tpl: "user-{user{name}}", errPart: "unterminated"},
		{tpl: "user-}", errPart: "unmatched"},
		{tpl: strings.Repeat("a", MaxSessionTemplateLength+1), errPart: "at most"},
		{tpl: "user\n", errPart: "invalid characters"},
	}
	for _, tt := range tests {
		t.Run(tt.tpl, func(t *testing.T) {
			err := ValidateSessionTemplate(tt.tpl)
			if tt.errPart == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.errPart)
		})
	}
}

func TestRenderTemplate(t *testing.T) {
	segs, err := parseTemplate("u-{username}-p-{password}-i-{identity_id}-h-{identity_hash}-l-{lease_id}-r-{random}")
	require.NoError(t, err)
	out, err := renderTemplate(segs, templateVars{username: "bob", password: "pw", identityID: "idt_1", leaseID: "lse_9"})
	require.NoError(t, err)
	sum := sha256.Sum256([]byte("idt_1"))
	hash := hex.EncodeToString(sum[:])[:12]
	require.Regexp(t, regexp.MustCompile(`^u-bob-p-pw-i-idt_1-h-`+hash+`-l-lse_9-r-[0-9a-f]{8}$`), out)

	require.Equal(t, hash, IdentityHash("idt_1"))
	require.Empty(t, IdentityHash(""))

	other, err := renderTemplate(segs, templateVars{username: "bob", password: "pw", identityID: "idt_1", leaseID: "lse_9"})
	require.NoError(t, err)
	require.NotEqual(t, out, other, "{random} differs per rendering")
}

func TestTruncate(t *testing.T) {
	require.Equal(t, "abc", truncate("abc", 5))
	require.Equal(t, "ab", truncate("abcdef", 2))
	require.Equal(t, "日", truncate("日本", 4), "never splits a UTF-8 sequence")
}
