package spinneret

import (
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"

	"connectrpc.com/connect"
)

// ApplyCredential merges a leased credential into req:
//
//   - credential headers are set unless req already has the header;
//   - cookies are added to the Cookie header unless req already sends a
//     cookie of the same name (cookie_header is used verbatim when the
//     credential has no cookie map and req has no cookies);
//   - query parameters are appended to the URL unless already present, keeping
//     the existing query string byte for byte (signatures stay valid).
//
// Values already present on req therefore win over the credential. The
// "Host" header is not applied (net/http takes the host from the URL).
// A nil credential or request is ignored.
func ApplyCredential(req *http.Request, cred *Credential) {
	if req == nil || cred == nil {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	applyHeaders(req.Header, cred.GetHeaders())
	applyCookies(req.Header, cred)
	if req.URL != nil {
		req.URL.RawQuery = appendQuery(req.URL.RawQuery, cred.GetQuery())
	}
}

func applyHeaders(header http.Header, values map[string]string) {
	for _, name := range sortedKeys(values) {
		canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		if canonical == "" || canonical == "Host" || canonical == "Cookie" {
			continue
		}
		if len(header.Values(canonical)) == 0 {
			header.Set(canonical, values[name])
		}
	}
}

// applyCookies merges the credential cookies (and a Cookie entry of the
// credential headers) into the Cookie header of req.
func applyCookies(header http.Header, cred *Credential) {
	existing := strings.Join(header.Values("Cookie"), "; ")
	credHeaderCookie := headerValue(cred.GetHeaders(), "Cookie")
	if existing == "" && len(cred.GetCookies()) == 0 {
		// Nothing to merge with: send the rendered header verbatim.
		value := firstNonEmpty(credHeaderCookie, cred.GetCookieHeader())
		if value != "" {
			header.Set("Cookie", value)
		}
		return
	}
	present := make(map[string]bool)
	for _, pair := range splitCookieHeader(existing) {
		present[pair[0]] = true
	}
	var parts []string
	if existing != "" {
		parts = append(parts, existing)
	}
	add := func(name, value string) {
		if name == "" || present[name] {
			return
		}
		present[name] = true
		parts = append(parts, name+"="+value)
	}
	for _, name := range sortedKeys(cred.GetCookies()) {
		add(name, cred.GetCookies()[name])
	}
	source := credHeaderCookie
	if len(cred.GetCookies()) == 0 && source == "" {
		source = cred.GetCookieHeader()
	}
	for _, pair := range splitCookieHeader(source) {
		add(pair[0], pair[1])
	}
	if len(parts) > 0 {
		header.Set("Cookie", strings.Join(parts, "; "))
	}
}

// ParseCookieHeader parses a Cookie header value ("k1=v1; k2=v2") into a map.
// Values are kept verbatim (no unquoting or unescaping).
func ParseCookieHeader(value string) map[string]string {
	out := make(map[string]string)
	for _, pair := range splitCookieHeader(value) {
		out[pair[0]] = pair[1]
	}
	return out
}

func splitCookieHeader(value string) [][2]string {
	var out [][2]string
	for part := range strings.SplitSeq(value, ";") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		out = append(out, [2]string{name, strings.TrimSpace(val)})
	}
	return out
}

func headerValue(values map[string]string, name string) string {
	for k, v := range values {
		if strings.EqualFold(strings.TrimSpace(k), name) {
			return v
		}
	}
	return ""
}

// appendQuery appends the parameters that rawQuery does not contain yet.
func appendQuery(rawQuery string, params map[string]string) string {
	if len(params) == 0 {
		return rawQuery
	}
	existing, err := url.ParseQuery(rawQuery)
	if err != nil {
		existing = parseQueryLenient(rawQuery)
	}
	var b strings.Builder
	b.WriteString(rawQuery)
	for _, name := range sortedKeys(params) {
		if name == "" || existing.Has(name) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(name))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(params[name]))
	}
	return b.String()
}

// parseQueryLenient collects the parameter names of a query string that
// url.ParseQuery rejects (for example because of a bad escape).
func parseQueryLenient(rawQuery string) url.Values {
	out := url.Values{}
	for part := range strings.SplitSeq(rawQuery, "&") {
		name, _, _ := strings.Cut(part, "=")
		if unescaped, err := url.QueryUnescape(name); err == nil {
			name = unescaped
		}
		out.Add(name, "")
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ProxyURL parses the URL of a proxy assignment; nil (and no error) when the
// assignment is nil or has no URL.
func ProxyURL(proxy *ProxyAssignment) (*url.URL, error) {
	raw := strings.TrimSpace(proxy.GetUrl())
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		// Never include the URL itself: it carries proxy credentials.
		return nil, newError(connect.CodeInvalidArgument, ReasonInvalidArgument, "proxy %q has an invalid URL", proxy.GetProxyId())
	}
	return u, nil
}
