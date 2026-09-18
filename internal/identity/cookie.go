package identity

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Cookie map limits.
const (
	// MaxCookies is the maximum number of cookies in one cookie_map value.
	MaxCookies = 1024
	// MaxCookieNameBytes is the maximum length of a cookie name.
	MaxCookieNameBytes = 1024
	// MaxCookieValueBytes is the maximum length of a cookie value.
	MaxCookieValueBytes = 16 << 10
)

// ParseCookieMap converts the accepted cookie_map representations into a map
// of cookie names to values:
//
//   - an object of strings: {"sessionid": "a1b2c3"} (map[string]any or
//     map[string]string);
//   - a Cookie header string: "sessionid=a1b2c3; csrf_token=1%7Cabc" (split on
//     ';', pairs trimmed, '=' inside values kept, an optional leading
//     "Cookie:" prefix and empty pairs ignored);
//   - a browser export array of objects with string "name" and "value" keys
//     (other keys such as domain or path are ignored).
//
// When a name occurs more than once the last occurrence wins. Names must be
// non-empty valid UTF-8 and must not contain control characters, whitespace,
// '=', ';', ',' or '"'; values must be valid UTF-8 without control characters
// (tab excepted) or ';'. Errors never include cookie values; names parsed out
// of header strings and browser exports are identified by position only,
// because a malformed pair can carry value text in its name part.
func ParseCookieMap(v any) (map[string]string, error) {
	switch t := v.(type) {
	case map[string]string:
		if err := checkCookieCount(len(t)); err != nil {
			return nil, err
		}
		out := make(map[string]string, len(t))
		for _, name := range sortedKeys(t) {
			if err := addNamedCookie(out, name, t[name]); err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		if err := checkCookieCount(len(t)); err != nil {
			return nil, err
		}
		out := make(map[string]string, len(t))
		for _, name := range sortedKeys(t) {
			value, ok := t[name].(string)
			if !ok {
				return nil, invalidf("cookie %q: value must be a string", truncate(name))
			}
			if err := addNamedCookie(out, name, value); err != nil {
				return nil, err
			}
		}
		return out, nil
	case string:
		return parseCookieHeader(t)
	case []any:
		return parseCookieArray(t)
	case []map[string]any:
		items := make([]any, len(t))
		for i, item := range t {
			items[i] = item
		}
		return parseCookieArray(items)
	case nil:
		return nil, invalidf("cookie map is null")
	default:
		return nil, invalidf("cookie map must be an object, a Cookie header string or an array of {name, value} objects")
	}
}

func checkCookieCount(n int) error {
	if n > MaxCookies {
		return invalidf("cookie map has %d cookies, the maximum is %d", n, MaxCookies)
	}
	return nil
}

func parseCookieHeader(header string) (map[string]string, error) {
	header = strings.TrimSpace(header)
	if len(header) >= len("cookie:") && strings.EqualFold(header[:len("cookie:")], "cookie:") {
		header = header[len("cookie:"):]
	}
	out := make(map[string]string)
	pair := 0
	for part := range strings.SplitSeq(header, ";") {
		part = strings.Trim(part, " \t")
		if part == "" {
			continue
		}
		pair++
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, invalidf("cookie pair %d has no '='", pair)
		}
		if err := addCookieAt(out, "cookie pair ", pair, strings.Trim(name, " \t"), strings.Trim(value, " \t")); err != nil {
			return nil, err
		}
		if len(out) > MaxCookies {
			return nil, checkCookieCount(len(out))
		}
	}
	return out, nil
}

func parseCookieArray(items []any) (map[string]string, error) {
	if err := checkCookieCount(len(items)); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, invalidf("cookie #%d must be an object with name and value", i+1)
		}
		name, ok := obj["name"].(string)
		if !ok {
			return nil, invalidf("cookie #%d: name must be a string", i+1)
		}
		value, ok := obj["value"].(string)
		if !ok {
			return nil, invalidf("cookie #%d: value must be a string", i+1)
		}
		if err := addCookieAt(out, "cookie #", i+1, name, value); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addNamedCookie validates a cookie given as an object entry, whose key is
// unambiguously a name, and stores it into dst.
func addNamedCookie(dst map[string]string, name, value string) error {
	if reason := cookieNameProblem(name); reason != "" {
		return invalidf("cookie name %q %s", truncate(name), reason)
	}
	if !validCookieValue(value) {
		return invalidf("cookie %q: value contains invalid characters or is too long", truncate(name))
	}
	dst[name] = value
	return nil
}

// addCookieAt validates a cookie parsed from a header pair or a browser
// export entry and stores it into dst. Messages identify the cookie by label
// and 1-based position ("cookie pair 2", "cookie #3") and never quote the
// name or value.
func addCookieAt(dst map[string]string, label string, pos int, name, value string) error {
	if reason := cookieNameProblem(name); reason != "" {
		return invalidf("%s%d: cookie name %s", label, pos, reason)
	}
	if !validCookieValue(value) {
		return invalidf("%s%d: value contains invalid characters or is too long", label, pos)
	}
	dst[name] = value
	return nil
}

// validateCookieName returns an error when name is not a valid cookie name.
func validateCookieName(name string) error {
	if reason := cookieNameProblem(name); reason != "" {
		return invalidf("cookie name %s", reason)
	}
	return nil
}

// cookieNameProblem describes why name is not a valid cookie name, or returns
// "" when it is valid. The description never includes the name.
func cookieNameProblem(name string) string {
	switch {
	case name == "":
		return "is empty"
	case len(name) > MaxCookieNameBytes:
		return "is too long"
	case !utf8.ValidString(name):
		return "is not valid UTF-8"
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c <= ' ' || c == 0x7f:
			return "contains a control or whitespace character"
		case c == '=' || c == ';' || c == ',' || c == '"':
			return "contains " + strconv.Quote(string(c))
		}
	}
	return ""
}

func validCookieValue(value string) bool {
	if len(value) > MaxCookieValueBytes || !utf8.ValidString(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < ' ' && c != '\t') || c == 0x7f || c == ';' {
			return false
		}
	}
	return true
}

// cookieHeader joins cookies as "k1=v1; k2=v2" with names sorted.
func cookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	names := sortedKeys(cookies)
	size := 0
	for _, name := range names {
		size += len(name) + len(cookies[name]) + 3
	}
	var b strings.Builder
	b.Grow(size)
	for i, name := range names {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(cookies[name])
	}
	return b.String()
}

// truncate shortens user supplied identifiers (never values) for messages.
func truncate(s string) string {
	const limit = 64
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
