package auth

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/pkg/netx"
)

// Limits applied to client-supplied request metadata.
const (
	// MaxNodeLength bounds the sanitized X-Spinneret-Node value.
	MaxNodeLength = 128
	// maxUserAgentLength bounds the stored user agent.
	maxUserAgentLength = 512
)

// RequestMeta describes the transport-level facts of a request that handlers
// and audit records need.
type RequestMeta struct {
	// ClientIP is the originating client address ("" when unknown).
	ClientIP string
	// UserAgent is the (truncated) User-Agent header.
	UserAgent string
	// Node is the sanitized X-Spinneret-Node header.
	Node string
	// Secure reports whether the request arrived over TLS directly or through
	// a trusted proxy that set X-Forwarded-Proto: https.
	Secure bool
}

type requestMetaKey struct{}

type tlsKey struct{}

// WithRequestMeta returns a copy of ctx carrying m.
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaKey{}, m)
}

// RequestMetaFrom returns the request metadata stored by the interceptor or
// HTTP middleware.
func RequestMetaFrom(ctx context.Context) (RequestMeta, bool) {
	m, ok := ctx.Value(requestMetaKey{}).(RequestMeta)
	return m, ok
}

// TLSMiddleware records in the request context whether the connection uses
// TLS. Connect handlers cannot see *http.Request, so the server wraps its mux
// with this middleware to let the interceptor decide the session cookie's
// Secure attribute in "auto" mode.
func TLSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			r = r.WithContext(context.WithValue(r.Context(), tlsKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// requestTLS reports whether r (or its context) is marked as TLS.
func requestTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	v, _ := r.Context().Value(tlsKey{}).(bool)
	return v
}

// ExtractRequestMeta computes request metadata from r, honouring forwarding
// headers only from trusted proxies.
func ExtractRequestMeta(r *http.Request, trusted []netip.Prefix) RequestMeta {
	m := RequestMeta{
		UserAgent: truncateUTF8(r.Header.Get("User-Agent"), maxUserAgentLength),
		Node:      SanitizeNode(r.Header.Get(HeaderNode)),
		Secure:    requestTLS(r),
	}
	if ip := netx.ClientIP(r, trusted); ip.IsValid() {
		m.ClientIP = ip.String()
	}
	if !m.Secure && strings.EqualFold(strings.TrimSpace(r.Header.Get(HeaderForwardedProto)), "https") {
		if peer, ok := peerAddr(r.RemoteAddr); ok && len(trusted) > 0 && netx.AllowedIP(peer, trusted) {
			m.Secure = true
		}
	}
	return m
}

// peerAddr parses the TCP peer of a request.
func peerAddr(remote string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap(), true
	}
	if a, err := netip.ParseAddr(remote); err == nil {
		return a.Unmap(), true
	}
	return netip.Addr{}, false
}

// SanitizeNode normalizes a node name: characters outside
// [A-Za-z0-9._:@/-] become '_', surrounding separators are trimmed and the
// result is truncated to MaxNodeLength bytes.
func SanitizeNode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > MaxNodeLength*4 {
		s = s[:MaxNodeLength*4]
	}
	var b strings.Builder
	b.Grow(min(len(s), MaxNodeLength))
	for _, r := range s {
		if b.Len() >= MaxNodeLength {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == ':', r == '@', r == '/', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune and drops
// invalid UTF-8.
func truncateUTF8(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
