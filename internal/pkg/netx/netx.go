// Package netx provides client IP extraction that honours reverse-proxy
// headers only from trusted peers, and CIDR allowlist helpers.
//
// All addresses are normalized before comparison: IPv4-mapped IPv6 addresses
// (::ffff:a.b.c.d) are unmapped to IPv4 and IPv6 zones are removed, so an
// allowlist entry "10.0.0.0/8" matches a peer reported as "[::ffff:10.1.2.3]".
package netx

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Header names consulted by ClientIP.
const (
	HeaderForwardedFor = "X-Forwarded-For"
	HeaderRealIP       = "X-Real-Ip"
)

// maxHopLen bounds the length of a single forwarded hop. Longer values cannot
// be a valid address (with port and brackets) and are treated as malformed.
const maxHopLen = 128

// ParsePrefixes parses CIDR prefixes ("10.0.0.0/8", "2001:db8::/32") and bare
// addresses ("192.0.2.1", "::1", which become single-address prefixes).
// Surrounding whitespace is ignored and empty values are skipped. Prefixes are
// masked to their network address, IPv4-mapped IPv6 prefixes of at least /96
// are converted to their IPv4 equivalent and zones are rejected.
func ParsePrefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for i, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		p, err := parsePrefix(v)
		if err != nil {
			return nil, fmt.Errorf("netx: entry %d: %w", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parsePrefix(v string) (netip.Prefix, error) {
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", v)
		}
		addr, bits := p.Addr(), p.Bits()
		if addr.Is4In6() && bits >= 96 {
			return netip.PrefixFrom(addr.Unmap(), bits-96).Masked(), nil
		}
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid IP address %q", v)
	}
	if addr.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("IP address %q must not have a zone", v)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// ClientIP returns the originating client address of r.
//
// Forwarding headers are honoured only when the TCP peer (r.RemoteAddr) lies
// within trusted; otherwise the peer address is returned. For a trusted peer
// the X-Forwarded-For hops (all header lines, in order) are walked from the
// right, skipping hops that are themselves trusted proxies, and the first
// untrusted hop is returned. When every hop is trusted, the leftmost hop (the
// originating client inside a trusted network) is returned. A malformed hop
// stops the walk: nothing to its left is trusted, so the closest valid hop
// to its right is returned. When X-Forwarded-For yields no valid address, a
// valid X-Real-IP is used, and finally the peer address.
//
// The result is always a valid, normalized address, or the zero netip.Addr
// when r is nil or RemoteAddr cannot be parsed.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	if r == nil {
		return netip.Addr{}
	}
	peer, ok := parseHostAddr(r.RemoteAddr)
	if !ok {
		return netip.Addr{}
	}
	if !contains(trusted, peer) {
		return peer
	}
	if addr, ok := fromForwardedFor(r.Header.Values(HeaderForwardedFor), trusted); ok {
		return addr
	}
	if vals := r.Header.Values(HeaderRealIP); len(vals) == 1 {
		if addr, ok := parseHostAddr(vals[0]); ok {
			return addr
		}
	}
	return peer
}

// fromForwardedFor walks X-Forwarded-For hops right to left.
func fromForwardedFor(lines []string, trusted []netip.Prefix) (netip.Addr, bool) {
	var last netip.Addr
	found := false
	for li := len(lines) - 1; li >= 0; li-- {
		rest := lines[li]
		for {
			hop := rest
			more := false
			if i := strings.LastIndexByte(rest, ','); i >= 0 {
				hop, rest, more = rest[i+1:], rest[:i], true
			}
			hop = strings.TrimSpace(hop)
			if hop != "" || more {
				addr, ok := parseHostAddr(hop)
				if !ok {
					return last, found
				}
				if !contains(trusted, addr) {
					return addr, true
				}
				last, found = addr, true
			}
			if !more {
				break
			}
		}
	}
	return last, found
}

// AllowedIP reports whether ip is permitted by allow. An empty allowlist
// permits every address (including the zero address); a non-empty allowlist
// never permits an invalid address.
func AllowedIP(ip netip.Addr, allow []netip.Prefix) bool {
	if len(allow) == 0 {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	return contains(allow, normalize(ip))
}

// contains reports whether the normalized address addr is inside any prefix.
func contains(prefixes []netip.Prefix, addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func normalize(addr netip.Addr) netip.Addr {
	return addr.WithZone("").Unmap()
}

// parseHostAddr parses "ip", "ip:port", "[ipv6]" and "[ipv6]:port", optionally
// wrapped in double quotes, and returns the normalized address.
func parseHostAddr(v string) (netip.Addr, bool) {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	if v == "" || len(v) > maxHopLen {
		return netip.Addr{}, false
	}
	if v[0] == '[' {
		end := strings.IndexByte(v, ']')
		if end < 0 {
			return netip.Addr{}, false
		}
		tail := v[end+1:]
		if tail != "" {
			if _, err := netip.ParseAddrPort(v); err != nil {
				return netip.Addr{}, false
			}
		}
		addr, err := netip.ParseAddr(v[1:end])
		if err != nil || !addr.Is6() {
			return netip.Addr{}, false
		}
		return normalize(addr), true
	}
	if addr, err := netip.ParseAddr(v); err == nil {
		return normalize(addr), true
	}
	if ap, err := netip.ParseAddrPort(v); err == nil && ap.Addr().Is4() {
		return normalize(ap.Addr()), true
	}
	return netip.Addr{}, false
}
