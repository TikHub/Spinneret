package notify

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// errBlockedTarget is returned by the delivery dialer when a channel's target
// resolves to a non-public address. Its text never includes the address, so it
// is safe to surface through delivery errors.
var errBlockedTarget = errors.New("delivery target resolves to a disallowed (private, loopback or link-local) address")

// blockedPrefixes are non-public ranges that net/netip does not classify.
// netip.Addr.IsPrivate covers only RFC 1918 and RFC 4193, so without these a
// target in shared address space still reaches infrastructure — most sharply
// on Alibaba Cloud, whose instance-metadata endpoint is 100.100.100.200,
// inside the carrier-grade NAT block rather than the 169.254.0.0/16 the other
// providers use.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network" (RFC 1122); 0.x reaches the local host on Linux
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT (RFC 6598); Alibaba Cloud metadata
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments (RFC 6890)
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking (RFC 2544)
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved (RFC 1112), including 255.255.255.255
	// Local-use NAT64 (RFC 8215). Unlike the well-known prefix its embedding
	// depends on the prefix length, so the whole range is refused rather than
	// decoded: every address in it translates into some network's IPv4 space.
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// embeddedV4Prefixes are IPv6 ranges that carry an IPv4 address inside them.
// They matter because the outer address looks like ordinary global unicast:
// 64:ff9b::a00:1 and 2002:0a00:0001:: both pass every IsPrivate-style check
// while the packet is delivered to 10.0.0.1.
var embeddedV4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 well-known prefix (RFC 6052)
	netip.MustParsePrefix("2002::/16"),    // 6to4 (RFC 3056)
	netip.MustParsePrefix("::/96"),        // deprecated IPv4-compatible IPv6 (RFC 4291)
}

// blockedIP reports whether ip must not be used as a notification delivery
// target. It blocks loopback, unspecified, private (RFC 1918 / RFC 4193 ULA),
// link-local (which covers the cloud instance-metadata address
// 169.254.169.254 and fe80::/10), every multicast address, the reserved and
// shared ranges in blockedPrefixes, and any IPv6 address that tunnels a
// blocked IPv4 one. This prevents SSRF from a channel URL to internal services
// or instance metadata.
func blockedIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	ip = ip.Unmap()
	if ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return true
	}
	// Prefix.Contains is false across address families, so one pass covers both.
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if v4, ok := embeddedV4(ip); ok {
		return blockedIP(v4)
	}
	return false
}

// embeddedV4 returns the IPv4 address a tunnelling IPv6 address carries. The
// recursion in blockedIP terminates after one step: the result is always an
// IPv4 address, and every prefix here is IPv6, so the second call finds none.
func embeddedV4(ip netip.Addr) (netip.Addr, bool) {
	for _, p := range embeddedV4Prefixes {
		if !p.Contains(ip) {
			continue
		}
		b := ip.As16()
		// 6to4 carries the address in bytes 2..6, the others in the low 32 bits.
		if p.Bits() == 16 {
			return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
		}
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	}
	return netip.Addr{}, false
}

// guardDialControl is a net.Dialer Control hook that refuses connections whose
// resolved address is blockedIP. Because Control runs after DNS resolution on
// the concrete address being dialed, it also defeats DNS-rebinding: a hostname
// that resolves to a public address on validation but a private one at dial
// time is still blocked here.
func guardDialControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		// At Control time address is a resolved ip:port; anything else is
		// unexpected, so fail closed.
		return errBlockedTarget
	}
	if blockedIP(ap.Addr()) {
		return errBlockedTarget
	}
	return nil
}

// newGuardedDialer returns a dialer that refuses non-public targets, mirroring
// the timeouts of http.DefaultTransport's dialer.
func newGuardedDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control:   guardDialControl,
	}
}
