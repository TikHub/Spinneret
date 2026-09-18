package site

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// Size limits of paths and patterns.
const (
	// MaxPathLength is the maximum length in bytes of a request path and of
	// exact, template and prefix patterns.
	MaxPathLength = 2048
	// MaxRegexLength is the maximum length in bytes of a regex pattern.
	MaxRegexLength = 1024
	// maxAuthorityLength bounds the host part scanned in absolute URLs.
	maxAuthorityLength = 1024
	// maxRegexProgramSize bounds the estimated size, in RE2 instructions, of
	// a compiled regex rule. RE2 matching costs O(program size x path
	// length), and counted repetitions expand the program: the 51-byte
	// "(?:.?.?...){1000}$" compiles to 40 000 instructions and takes hundreds
	// of milliseconds per match on a 2048-byte path, which would stall the
	// acquire hot path. Patterns without large counted repetitions stay far
	// below this bound (roughly 1.5 instructions per pattern byte).
	maxRegexProgramSize = 2 * MaxRegexLength
)

// NormalizePath returns the raw path of uri, which is either a path starting
// with "/" (optionally followed by "?query" and/or "#fragment") or an absolute
// http or https URL. An absolute URL without a path yields "/". A string
// starting with "/" is always treated as a path, so "//host/x" is the path
// "//host/x".
//
// The query and fragment are discarded without validation. The path must be
// at most MaxPathLength bytes of valid UTF-8 without control or whitespace
// characters. Percent-encoding is not decoded. The result is a substring of
// uri (no allocation). Errors are apperr InvalidArgument with reason
// uri_invalid; messages never echo the input, which may carry credentials in
// its query string.
func NormalizePath(uri string) (string, error) {
	if uri == "" {
		return "", uriInvalid("uri is empty")
	}
	rest := uri
	if uri[0] != '/' {
		afterScheme, ok := cutHTTPScheme(uri)
		if !ok {
			return "", uriInvalid(`uri must be a path starting with "/" or an absolute http(s) URL`)
		}
		n, err := scanAuthority(afterScheme)
		if err != nil {
			return "", err
		}
		rest = afterScheme[n:]
		if rest == "" || rest[0] != '/' {
			return "/", nil
		}
	}
	n, err := scanPath(rest)
	if err != nil {
		return "", err
	}
	return rest[:n], nil
}

// ValidatePattern checks a URI rule pattern of the given kind:
//
//   - exact and prefix: start with "/", at most MaxPathLength bytes, valid
//     UTF-8, no control or whitespace characters, and no "?" or "#" (query
//     and fragment are never part of a matched path);
//   - template: as exact, and every "{name}" parameter occupies a whole
//     segment, name matches ^[a-zA-Z_][a-zA-Z0-9_]*$ and names are unique;
//   - regex: a valid RE2 expression of at most MaxRegexLength bytes whose
//     compiled program stays within 2048 instructions (this rejects large
//     counted repetitions such as "(?:.?.?.?){1000}", which would make
//     matching expensive).
//
// Errors are apperr InvalidArgument with reason invalid_argument.
func ValidatePattern(kind RuleKind, pattern string) error {
	switch kind {
	case RuleExact, RulePrefix:
		return validatePathPattern(pattern)
	case RuleTemplate:
		_, err := parseTemplate(pattern)
		return err
	case RuleRegex:
		_, err := compileRegex(pattern)
		return err
	default:
		return patternInvalid("unknown uri rule kind %q (want exact, template, prefix or regex)", kind)
	}
}

func uriInvalid(msg string) error {
	return apperr.InvalidArgument(apperr.ReasonURIInvalid, "%s", msg)
}

func patternInvalid(format string, args ...any) *apperr.Error {
	return apperr.InvalidArgument(apperr.ReasonInvalidArgument, format, args...)
}

// cutHTTPScheme strips a case-insensitive "http://" or "https://" prefix.
func cutHTTPScheme(s string) (string, bool) {
	for _, scheme := range [...]string{"https://", "http://"} {
		if len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			return s[len(scheme):], true
		}
	}
	return "", false
}

// scanAuthority returns the length of the authority (userinfo, host, port)
// at the start of s, which ends at the first "/", "?" or "#".
func scanAuthority(s string) (int, error) {
	n := len(s)
	nonASCII := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' || c == '?' || c == '#' {
			n = i
			break
		}
		if i == maxAuthorityLength {
			return 0, uriInvalid("uri host is too long")
		}
		if c >= utf8.RuneSelf {
			nonASCII = true
		} else if isBadASCII(c) {
			return 0, uriInvalid("uri contains a control or whitespace character")
		}
	}
	if n == 0 {
		return 0, uriInvalid("uri host is empty")
	}
	if nonASCII {
		if msg := checkRunes(s[:n]); msg != "" {
			return 0, uriInvalid("uri " + msg)
		}
	}
	return n, nil
}

// scanPath returns the length of the path at the start of s, which ends at
// the first "?" or "#", and validates its characters and length.
func scanPath(s string) (int, error) {
	n := len(s)
	nonASCII := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '?' || c == '#' {
			n = i
			break
		}
		if i == MaxPathLength {
			return 0, uriInvalid("uri path exceeds 2048 bytes")
		}
		if c >= utf8.RuneSelf {
			nonASCII = true
		} else if isBadASCII(c) {
			return 0, uriInvalid("uri contains a control or whitespace character")
		}
	}
	if nonASCII {
		if msg := checkRunes(s[:n]); msg != "" {
			return 0, uriInvalid("uri " + msg)
		}
	}
	return n, nil
}

// isBadASCII reports ASCII control characters (including DEL) and space.
func isBadASCII(c byte) bool {
	return c <= ' ' || c == 0x7f
}

// checkRunes validates UTF-8 and rejects Unicode control and whitespace
// characters. It returns the predicate of an error message or "".
func checkRunes(s string) string {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			return "is not valid UTF-8"
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "contains a control or whitespace character"
		}
		i += size
	}
	return ""
}

// validatePathPattern validates exact and prefix patterns (and the overall
// shape of templates).
func validatePathPattern(pattern string) error {
	switch {
	case pattern == "":
		return patternInvalid("pattern is empty")
	case pattern[0] != '/':
		return patternInvalid(`pattern must start with "/"`)
	case len(pattern) > MaxPathLength:
		return patternInvalid("pattern exceeds %d bytes", MaxPathLength)
	case strings.ContainsAny(pattern, "?#"):
		return patternInvalid(`pattern must not contain "?" or "#": query and fragment are never matched`)
	}
	if msg := checkRunes(pattern); msg != "" {
		return patternInvalid("pattern %s", msg)
	}
	return nil
}

// templateSegment is one "/"-separated segment of a template pattern.
type templateSegment struct {
	literal string
	param   bool
}

// parseTemplate validates a template pattern and splits it into segments.
// The leading "/" is dropped, so "/a/{id}/" yields ["a", {id}, ""].
func parseTemplate(pattern string) ([]templateSegment, error) {
	if err := validatePathPattern(pattern); err != nil {
		return nil, err
	}
	parts := strings.Split(pattern[1:], "/")
	segments := make([]templateSegment, len(parts))
	var names map[string]struct{}
	for i, part := range parts {
		if !strings.ContainsAny(part, "{}") {
			segments[i] = templateSegment{literal: part}
			continue
		}
		if len(part) < 2 || part[0] != '{' || part[len(part)-1] != '}' || strings.Count(part, "{") != 1 || strings.Count(part, "}") != 1 {
			return nil, patternInvalid("template segment %d: a parameter must occupy a whole segment, like {name}", i+1)
		}
		name := part[1 : len(part)-1]
		if !validParamName(name) {
			return nil, patternInvalid("template segment %d: parameter name must match ^[a-zA-Z_][a-zA-Z0-9_]*$", i+1)
		}
		if names == nil {
			names = make(map[string]struct{}, len(parts))
		}
		if _, dup := names[name]; dup {
			return nil, patternInvalid("template segment %d: duplicate parameter name %q", i+1, name)
		}
		names[name] = struct{}{}
		segments[i] = templateSegment{param: true}
	}
	return segments, nil
}

func validParamName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// compileRegex validates and compiles a regex pattern with RE2 semantics.
func compileRegex(pattern string) (*regexp.Regexp, error) {
	switch {
	case pattern == "":
		return nil, patternInvalid("pattern is empty")
	case len(pattern) > MaxRegexLength:
		return nil, patternInvalid("regex pattern exceeds %d bytes", MaxRegexLength)
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, patternInvalid("invalid regex: %v", err).WithCause(err)
	}
	if regexProgramSize(parsed) > maxRegexProgramSize {
		return nil, patternInvalid("regex is too complex: its compiled program exceeds %d instructions (reduce counted repetitions such as {n,m})", maxRegexProgramSize)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, patternInvalid("invalid regex: %v", err).WithCause(err)
	}
	return re, nil
}

// regexProgramSize estimates the number of instructions of the program
// compiled from a parsed, not yet simplified, regexp. It mirrors the size
// estimate of regexp/syntax (pessimistic for stars) without expanding counted
// repetitions, so it runs in time linear in the parse tree. Recursion depth is
// bounded by the parser's nesting limit and sizes by its program size limit,
// so the arithmetic cannot overflow.
func regexProgramSize(re *syntax.Regexp) int64 {
	var size int64
	switch re.Op {
	case syntax.OpLiteral:
		size = int64(len(re.Rune))
	case syntax.OpCapture, syntax.OpStar:
		size = 2 + regexProgramSize(re.Sub[0])
	case syntax.OpPlus, syntax.OpQuest:
		size = 1 + regexProgramSize(re.Sub[0])
	case syntax.OpConcat, syntax.OpAlternate:
		for _, sub := range re.Sub {
			size += regexProgramSize(sub)
		}
		if re.Op == syntax.OpAlternate && len(re.Sub) > 1 {
			size += int64(len(re.Sub)) - 1
		}
	case syntax.OpRepeat:
		sub := regexProgramSize(re.Sub[0])
		switch {
		case re.Max == -1 && re.Min == 0:
			size = 2 + sub
		case re.Max == -1:
			size = 1 + int64(re.Min)*sub
		default:
			size = int64(re.Max)*sub + int64(re.Max-re.Min)
		}
	}
	return max(1, size)
}
