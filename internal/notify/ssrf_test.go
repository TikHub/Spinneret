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
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"0.0.0.0",         // unspecified
		"::",              // unspecified v6
		"10.0.0.1",        // private
		"172.16.5.4",      // private
		"192.168.1.1",     // private
		"169.254.169.254", // link-local / cloud instance metadata
		"fe80::1",         // link-local v6
		"fc00::1",         // ULA (private v6)
		"224.0.0.1",       // multicast
		"::ffff:127.0.0.1", // v4-mapped loopback
		"::ffff:10.0.0.1",  // v4-mapped private
	}
	for _, s := range blocked {
		require.Truef(t, blockedIP(netip.MustParseAddr(s)), "%s should be blocked", s)
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
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
