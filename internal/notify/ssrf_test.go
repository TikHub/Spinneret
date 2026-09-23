package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBlockedIP(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1",        // loopback
		"::1",              // loopback v6
		"0.0.0.0",          // unspecified
		"::",               // unspecified v6
		"10.0.0.1",         // private
		"172.16.5.4",       // private
		"192.168.1.1",      // private
		"169.254.169.254",  // link-local / cloud instance metadata
		"fe80::1",          // link-local v6
		"fc00::1",          // ULA (private v6)
		"224.0.0.1",        // multicast
		"::ffff:127.0.0.1", // v4-mapped loopback
		"::ffff:10.0.0.1",  // v4-mapped private
		// Ranges net/netip does not classify. IsPrivate is RFC 1918 + RFC 4193
		// only, so each of these reached infrastructure before they were listed.
		"100.100.100.200", // Alibaba Cloud instance metadata (carrier-grade NAT)
		"100.64.0.1",      // carrier-grade NAT (RFC 6598)
		"0.1.2.3",         // "this network" beyond the unspecified address itself
		"192.0.0.1",       // IETF protocol assignments
		"198.18.0.1",      // benchmarking
		"240.0.0.1",       // reserved
		"255.255.255.255", // broadcast
		// IPv6 addresses that look like global unicast but deliver to IPv4.
		"64:ff9b::a00:1",     // NAT64 well-known prefix carrying 10.0.0.1
		"64:ff9b::6464:64c8", // NAT64 carrying the Alibaba Cloud metadata address
		"64:ff9b:1::1",       // local-use NAT64, refused wholesale
		"2002:0a00:0001::",   // 6to4 carrying 10.0.0.1
		"2002:7f00:0001::",   // 6to4 carrying 127.0.0.1
		"::a00:1",            // deprecated IPv4-compatible IPv6 carrying 10.0.0.1
	}
	for _, s := range blocked {
		require.Truef(t, blockedIP(netip.MustParseAddr(s)), "%s should be blocked", s)
	}
	allowed := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111",
		// The new prefixes must not swallow their public neighbours.
		"100.63.255.255",   // just below carrier-grade NAT
		"100.128.0.1",      // just above carrier-grade NAT
		"192.0.1.1",        // just above the IETF assignments block
		"198.20.0.1",       // just above the benchmarking block
		"223.255.255.255",  // last public address before the multicast block
		"2002:0808:0808::", // 6to4 carrying the public 8.8.8.8
		"64:ff9b::808:808", // NAT64 carrying the public 8.8.8.8
	}
	for _, s := range allowed {
		require.Falsef(t, blockedIP(netip.MustParseAddr(s)), "%s should be allowed", s)
	}
	// An invalid address fails closed.
	require.True(t, blockedIP(netip.Addr{}))
}

// TestDeliveryGuardBlocksInternalTargets is the SSRF regression: the default
// (guarded) delivery client must refuse a loopback target at dial time, while
// a client built with allowPrivateTargets=true reaches the same server. This
// proves a channel URL pointing at internal services / instance metadata
// cannot be reached in the default configuration.
func TestDeliveryGuardBlocksInternalTargets(t *testing.T) {
	t.Parallel()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Production default: loopback is refused, server is never hit.
	guarded := &sender{client: newHTTPClient(2*time.Second, false), now: time.Now}
	_, err := guarded.post(context.Background(), srv.URL, nil, []byte(`{}`))
	require.Error(t, err)
	require.Equal(t, 0, hits, "guarded client must not reach a loopback target")
	require.NotContains(t, err.Error(), srv.URL, "error must not leak the target URL")

	// Opt-in: the same request reaches the server.
	allowed := &sender{client: newHTTPClient(2*time.Second, true), now: time.Now}
	_, err = allowed.post(context.Background(), srv.URL, nil, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, 1, hits)
}
