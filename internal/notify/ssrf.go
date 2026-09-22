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

// blockedIP reports whether ip must not be used as a notification delivery
// target. It blocks loopback, unspecified, private (RFC 1918 / RFC 4193 ULA),
// link-local (which covers the cloud instance-metadata address
// 169.254.169.254 and fe80::/10) and every multicast address. This prevents
// SSRF from a channel URL to internal services or instance metadata.
func blockedIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return true
	}
	ip = ip.Unmap()
	return ip.IsLoopback() ||
		ip.IsUnspecified() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast()
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
