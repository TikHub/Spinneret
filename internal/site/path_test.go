package site_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/site"
)

func requireAppErr(t *testing.T, err error, reason apperr.Reason, contains string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.As(err)
	require.True(t, ok, "want *apperr.Error, got %T", err)
	require.Equal(t, connect.CodeInvalidArgument, ae.Code)
	require.Equal(t, reason, ae.Reason)
	if contains != "" {
		require.Contains(t, ae.Message, contains)
	}
}

func TestNormalizePath(t *testing.T) {
	maxPath := "/" + strings.Repeat("a", site.MaxPathLength-1)
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"plain path", "/api/v1/search/", "/api/v1/search/"},
		{"root", "/", "/"},
		{"query stripped", "/a/b?x=1&y=2", "/a/b"},
		{"fragment stripped", "/a/b#frag", "/a/b"},
		{"fragment before question mark", "/a/b#frag?x", "/a/b"},
		{"query with fragment", "/a?x=1#f", "/a"},
		{"root with query", "/?q=1", "/"},
		{"query is not validated", "/s?q=hello world\t", "/s"},
		{"absolute https", "https://target.example.com/api/v1/search/single/?keyword=x", "/api/v1/search/single/"},
		{"absolute http mixed-case scheme", "HTTP://Example.COM/A/b", "/A/b"},
		{"absolute with userinfo and port", "http://user:pass@host:8080/p/q?x", "/p/q"},
		{"absolute without path", "https://example.com", "/"},
		{"absolute without path with query", "https://example.com?x=1", "/"},
		{"absolute without path with fragment", "https://example.com#top", "/"},
		{"absolute with unicode host", "https://例子.测试/p", "/p"},
		{"double slash is a path", "//host/x", "//host/x"},
		{"percent encoding kept", "/a%20b/%E4%B8%AD", "/a%20b/%E4%B8%AD"},
		{"dot segments kept", "/a/../b/./c", "/a/../b/./c"},
		{"repeated slashes kept", "/a//b///", "/a//b///"},
		{"unicode path", "/路径/中文", "/路径/中文"},
		{"max length", maxPath, maxPath},
		{"max length then query", maxPath + "?x=" + strings.Repeat("q", 5000), maxPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := site.NormalizePath(tt.uri)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizePathErrors(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		contains string
	}{
		{"empty", "", "empty"},
		{"relative", "api/v1?token=secret", "must be a path"},
		{"other scheme", "ftp://host/secret", "must be a path"},
		{"malformed scheme", "http:/secret", "must be a path"},
		{"scheme only", "https://", "host is empty"},
		{"empty host with path", "http:///secret", "host is empty"},
		{"empty host with query", "http://?secret", "host is empty"},
		{"host too long", "http://" + strings.Repeat("h", 1025) + "/secret", "host is too long"},
		{"space in host", "http://ho st/secret", "control or whitespace"},
		{"invalid utf8 in host", "http://h\xff/secret", "not valid UTF-8"},
		{"nbsp in host", "http://h\u00a0/secret", "control or whitespace"},
		{"space in path", "/a b/secret", "control or whitespace"},
		{"tab in path", "/a\tsecret", "control or whitespace"},
		{"newline in path", "/a\nsecret", "control or whitespace"},
		{"nul in path", "/a\x00secret", "control or whitespace"},
		{"del in path", "/a\x7fsecret", "control or whitespace"},
		{"c1 control in path", "/a\u0085secret", "control or whitespace"},
		{"nbsp in path", "/a\u00a0secret", "control or whitespace"},
		{"line separator in path", "/a\u2028secret", "control or whitespace"},
		{"ideographic space in path", "/a\u3000secret", "control or whitespace"},
		{"invalid utf8 in path", "/a\xffsecret", "not valid UTF-8"},
		{"truncated utf8 in path", "/a\xe4\xb8secret", "not valid UTF-8"},
		{"path too long", "/" + strings.Repeat("a", site.MaxPathLength), "exceeds 2048 bytes"},
		{"absolute path too long", "https://h/" + strings.Repeat("a", site.MaxPathLength), "exceeds 2048 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := site.NormalizePath(tt.uri)
			require.Empty(t, got)
			requireAppErr(t, err, apperr.ReasonURIInvalid, tt.contains)
			require.NotContains(t, err.Error(), "secret", "error must not echo the uri")
		})
	}
}

func TestNormalizePathDoesNotAllocate(t *testing.T) {
	uris := []string{"/api/v1/search/item/?keyword=x", "https://www.example.com/a/b?c=d", "https://example.com"}
	for _, uri := range uris {
		allocs := testing.AllocsPerRun(100, func() {
			if _, err := site.NormalizePath(uri); err != nil {
				t.Fatal(err)
			}
		})
		require.Zero(t, allocs, uri)
	}
}

func TestValidatePattern(t *testing.T) {
	long := "/" + strings.Repeat("a", site.MaxPathLength)
	tests := []struct {
		name     string
		kind     site.RuleKind
		pattern  string
		contains string // "" means valid
	}{
		{"exact ok", site.RuleExact, "/api/v1/item/detail/", ""},
		{"exact unicode ok", site.RuleExact, "/路径", ""},
		{"exact braces are literal", site.RuleExact, "/a/{id}", ""},
		{"exact max length", site.RuleExact, long[:site.MaxPathLength], ""},
		{"exact empty", site.RuleExact, "", "empty"},
		{"exact relative", site.RuleExact, "a/b", `must start with "/"`},
		{"exact too long", site.RuleExact, long, "exceeds 2048 bytes"},
		{"exact query", site.RuleExact, "/a?b=1", `must not contain "?" or "#"`},
		{"exact fragment", site.RuleExact, "/a#b", `must not contain "?" or "#"`},
		{"exact whitespace", site.RuleExact, "/a b", "control or whitespace"},
		{"exact control", site.RuleExact, "/a\x01", "control or whitespace"},
		{"exact invalid utf8", site.RuleExact, "/a\xff", "not valid UTF-8"},
		{"prefix ok", site.RulePrefix, "/api/v1/", ""},
		{"prefix root ok", site.RulePrefix, "/", ""},
		{"prefix relative", site.RulePrefix, "api", `must start with "/"`},
		{"prefix newline", site.RulePrefix, "/a\n", "control or whitespace"},
		{"template ok", site.RuleTemplate, "/api/item/{id}/", ""},
		{"template several params", site.RuleTemplate, "/{a}/x/{b_2}/{_C}", ""},
		{"template without params", site.RuleTemplate, "/a//b/", ""},
		{"template root", site.RuleTemplate, "/", ""},
		{"template partial segment prefix", site.RuleTemplate, "/item{id}", "must occupy a whole segment"},
		{"template partial segment suffix", site.RuleTemplate, "/{id}.json", "must occupy a whole segment"},
		{"template nested braces", site.RuleTemplate, "/{a{b}}", "must occupy a whole segment"},
		{"template stray close", site.RuleTemplate, "/a}", "must occupy a whole segment"},
		{"template stray open", site.RuleTemplate, "/{", "must occupy a whole segment"},
		{"template empty name", site.RuleTemplate, "/{}", "parameter name must match"},
		{"template digit first", site.RuleTemplate, "/{1a}", "parameter name must match"},
		{"template dash in name", site.RuleTemplate, "/x/{a-b}", "template segment 2: parameter name must match"},
		{"template duplicate names", site.RuleTemplate, "/{id}/x/{id}", `duplicate parameter name "id"`},
		{"template relative", site.RuleTemplate, "{id}", `must start with "/"`},
		{"template query", site.RuleTemplate, "/{id}?x", `must not contain "?" or "#"`},
		{"regex ok", site.RuleRegex, `^/api/v[0-9]+/(search|feed)/`, ""},
		{"regex unanchored ok", site.RuleRegex, `detail`, ""},
		{"regex max length", site.RuleRegex, strings.Repeat("a", site.MaxRegexLength), ""},
		{"regex empty", site.RuleRegex, "", "empty"},
		{"regex too long", site.RuleRegex, strings.Repeat("a", site.MaxRegexLength+1), "exceeds 1024 bytes"},
		{"regex invalid", site.RuleRegex, `(unclosed`, "invalid regex"},
		{"regex backreference unsupported", site.RuleRegex, `(a)\1`, "invalid regex"},
		{"regex bounded repetition ok", site.RuleRegex, `^/item/.{0,100}x$`, ""},
		{"regex open repetitions ok", site.RuleRegex, `^/a{2,}/b{0,}/c{1,3}/(?:d|ef)*$`, ""},
		{"regex repetition within program limit", site.RuleRegex, `(?:ab){1000}`, ""},
		{"regex repetition over program limit", site.RuleRegex, `(?:abc){700}`, "regex is too complex"},
		{"regex nested optional repetition", site.RuleRegex, `(?:.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?.?){1000}$`, "regex is too complex"},
		{"regex optional class repetition", site.RuleRegex, `(?:[a-z/]?[a-z/]?[a-z/]?){1000}`, "regex is too complex"},
		{"regex unbounded repetition of large group", site.RuleRegex, `(?:` + strings.Repeat("a", 700) + `){3,}`, "regex is too complex"},
		{"regex star of large alternation ok", site.RuleRegex, `(?:` + strings.Repeat("ab|", 300) + `c)*`, ""},
		{"unknown kind", site.RuleKind("glob"), "/a/*", `unknown uri rule kind "glob"`},
		{"empty kind", site.RuleKind(""), "/a", "unknown uri rule kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := site.ValidatePattern(tt.kind, tt.pattern)
			if tt.contains == "" {
				require.NoError(t, err)
				return
			}
			requireAppErr(t, err, apperr.ReasonInvalidArgument, tt.contains)
		})
	}
}

func FuzzNormalizePath(f *testing.F) {
	for _, seed := range []string{
		"/", "/a/b?c#d", "https://h/p?q", "http://h", "//x", "/\xff", "HTTPS://H#f", "/a\u00a0b", "ftp://x/",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, uri string) {
		got, err := site.NormalizePath(uri)
		if err != nil {
			require.Empty(t, got)
			require.Equal(t, apperr.ReasonURIInvalid, apperr.ReasonOf(err))
			return
		}
		require.True(t, strings.HasPrefix(got, "/"))
		require.LessOrEqual(t, len(got), site.MaxPathLength)
		require.False(t, strings.ContainsAny(got, "?# \t\r\n"))
		require.True(t, utf8.ValidString(got))
	})
}
