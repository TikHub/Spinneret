package glob

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		s       string
		want    bool
	}{
		{name: "empty both", pattern: "", s: "", want: true},
		{name: "empty pattern non-empty input", pattern: "", s: "a", want: false},
		{name: "star matches empty", pattern: "*", s: "", want: true},
		{name: "star matches anything", pattern: "*", s: "prod/db/password", want: true},
		{name: "multiple stars match empty", pattern: "***", s: "", want: true},
		{name: "literal equal", pattern: "app", s: "app", want: true},
		{name: "literal differs", pattern: "app", s: "apq", want: false},
		{name: "literal prefix only", pattern: "app", s: "apple", want: false},
		{name: "literal longer than input", pattern: "apple", s: "app", want: false},
		{name: "case sensitive", pattern: "App", s: "app", want: false},
		{name: "trailing star", pattern: "app*", s: "app", want: true},
		{name: "trailing star longer", pattern: "app*", s: "application", want: true},
		{name: "leading star", pattern: "*.json", s: "config.json", want: true},
		{name: "leading star mismatch", pattern: "*.json", s: "config.yaml", want: false},
		{name: "star crosses slash", pattern: "prod/*", s: "prod/db/password", want: true},
		{name: "star in middle crosses slash", pattern: "prod/*/password", s: "prod/a/b/password", want: true},
		{name: "star in middle needs suffix", pattern: "prod/*/password", s: "prod/a/b/token", want: false},
		{name: "namespace prefix enforced", pattern: "prod/*", s: "staging/db", want: false},
		{name: "question one char", pattern: "a?c", s: "abc", want: true},
		{name: "question requires a char", pattern: "a?c", s: "ac", want: false},
		{name: "question does not match two", pattern: "a?c", s: "abbc", want: false},
		{name: "question matches slash", pattern: "a?c", s: "a/c", want: true},
		{name: "question matches multibyte rune", pattern: "a?c", s: "a世c", want: true},
		{name: "question on multibyte input end", pattern: "??", s: "世界", want: true},
		{name: "question count mismatch multibyte", pattern: "???", s: "世界", want: false},
		{name: "multibyte literal", pattern: "配置*", s: "配置中心", want: true},
		{name: "multibyte literal mismatch", pattern: "配置*", s: "配额", want: false},
		{name: "backtracking", pattern: "*ab*ab", s: "xabyabab", want: true},
		{name: "backtracking failure", pattern: "*ab*abc", s: "xabyabab", want: false},
		{name: "star question combo", pattern: "*?", s: "", want: false},
		{name: "star question combo one", pattern: "*?", s: "x", want: true},
		{name: "brackets are literal", pattern: "[ab]", s: "[ab]", want: true},
		{name: "brackets do not form a class", pattern: "[ab]", s: "a", want: false},
		{name: "backslash is literal", pattern: `a\*`, s: `a\zzz`, want: true},
		{name: "backslash does not escape", pattern: `a\*`, s: `a*`, want: false},
		{name: "invalid utf8 literal", pattern: "a\xffb", s: "a\xffb", want: true},
		{name: "invalid utf8 question", pattern: "a?b", s: "a\xffb", want: true},
		{name: "invalid utf8 mismatch", pattern: "a\xfeb", s: "a\xffb", want: false},
		{name: "pattern with trailing literal after star", pattern: "a*z", s: "abcdefz", want: true},
		{name: "pattern with trailing literal after star mismatch", pattern: "a*z", s: "abcdefzy", want: false},
		{name: "consecutive stars", pattern: "a**b", s: "ab", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, Match(tt.pattern, tt.s))
		})
	}
}

func TestMatchPathologicalInputIsFast(t *testing.T) {
	t.Parallel()
	pattern := strings.Repeat("*a", 64) + "b"
	s := strings.Repeat("a", 4096)
	start := time.Now()
	require.False(t, Match(pattern, s))
	require.Less(t, time.Since(start), 2*time.Second)
}

func TestHasWildcard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		want    bool
	}{
		{pattern: "", want: false},
		{pattern: "app", want: false},
		{pattern: "[x]", want: false},
		{pattern: "app*", want: true},
		{pattern: "a?c", want: true},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, HasWildcard(tt.pattern), tt.pattern)
	}
}

// referenceMatch is a straightforward exponential-time matcher over runes used
// as an oracle for Match on short inputs.
func referenceMatch(p, s []rune) bool {
	if len(p) == 0 {
		return len(s) == 0
	}
	switch p[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if referenceMatch(p[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return len(s) > 0 && referenceMatch(p[1:], s[1:])
	default:
		return len(s) > 0 && s[0] == p[0] && referenceMatch(p[1:], s[1:])
	}
}

func TestMatchAgreesWithReference(t *testing.T) {
	t.Parallel()
	alphabet := []rune{'a', 'b', '/', '.', '世', '*', '?'}
	rng := rand.New(rand.NewPCG(3, 4))
	randomString := func(maxLen int, wildcards bool) string {
		n := rng.IntN(maxLen + 1)
		out := make([]rune, 0, n)
		for range n {
			r := alphabet[rng.IntN(len(alphabet))]
			if !wildcards && (r == '*' || r == '?') {
				r = 'a'
			}
			out = append(out, r)
		}
		return string(out)
	}
	for range 100_000 {
		pattern, s := randomString(8, true), randomString(10, false)
		want := referenceMatch([]rune(pattern), []rune(s))
		require.Equal(t, want, Match(pattern, s), "Match(%q, %q)", pattern, s)
	}
}

func FuzzMatchStarMatchesEverything(f *testing.F) {
	f.Add("prod/db")
	f.Add("")
	f.Add("\xff\xfe")
	f.Fuzz(func(t *testing.T, s string) {
		if !Match("*", s) {
			t.Fatalf("'*' must match %q", s)
		}
		if !Match(s, s) && !HasWildcard(s) {
			t.Fatalf("literal pattern must match itself: %q", s)
		}
	})
}
