package netx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustPrefixes(t *testing.T, values ...string) []netip.Prefix {
	t.Helper()
	p, err := ParsePrefixes(values)
	require.NoError(t, err)
	return p
}

func TestParsePrefixes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{name: "nil", in: nil, want: []string{}},
		{name: "empty and blank entries skipped", in: []string{"", "  "}, want: []string{}},
		{name: "ipv4 cidr", in: []string{"10.0.0.0/8"}, want: []string{"10.0.0.0/8"}},
		{name: "cidr masked", in: []string{"10.1.2.3/8"}, want: []string{"10.0.0.0/8"}},
		{name: "bare ipv4", in: []string{" 192.0.2.1 "}, want: []string{"192.0.2.1/32"}},
		{name: "bare ipv6", in: []string{"::1"}, want: []string{"::1/128"}},
		{name: "ipv6 cidr", in: []string{"2001:db8::1/32"}, want: []string{"2001:db8::/32"}},
		{name: "mapped bare address unmapped", in: []string{"::ffff:10.1.2.3"}, want: []string{"10.1.2.3/32"}},
		{name: "mapped prefix unmapped", in: []string{"::ffff:10.0.0.0/104"}, want: []string{"10.0.0.0/8"}},
		{name: "multiple", in: []string{"10.0.0.0/8", "fd00::/8"}, want: []string{"10.0.0.0/8", "fd00::/8"}},
		{name: "invalid ip", in: []string{"10.0.0.256"}, wantErr: `entry 0: invalid IP address "10.0.0.256"`},
		{name: "invalid cidr bits", in: []string{"10.0.0.0/33"}, wantErr: `invalid CIDR "10.0.0.0/33"`},
		{name: "invalid cidr text", in: []string{"ok/8"}, wantErr: `invalid CIDR "ok/8"`},
		{name: "hostname rejected", in: []string{"localhost"}, wantErr: "invalid IP address"},
		{name: "zone rejected", in: []string{"fe80::1%eth0"}, wantErr: "must not have a zone"},
		{name: "zone in cidr rejected", in: []string{"fe80::%eth0/64"}, wantErr: "invalid CIDR"},
		{name: "error index", in: []string{"10.0.0.0/8", "bad"}, wantErr: "entry 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParsePrefixes(tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			gotS := make([]string, 0, len(got))
			for _, p := range got {
				gotS = append(gotS, p.String())
			}
			require.Equal(t, tt.want, gotS)
		})
	}
}

func TestAllowedIP(t *testing.T) {
	t.Parallel()
	allow := mustPrefixes(t, "10.0.0.0/8", "2001:db8::/32", "192.0.2.7")
	tests := []struct {
		name  string
		ip    netip.Addr
		allow []netip.Prefix
		want  bool
	}{
		{name: "empty allowlist allows", ip: netip.MustParseAddr("203.0.113.1"), want: true},
		{name: "empty allowlist allows zero addr", ip: netip.Addr{}, want: true},
		{name: "ipv4 inside", ip: netip.MustParseAddr("10.20.30.40"), allow: allow, want: true},
		{name: "ipv4 outside", ip: netip.MustParseAddr("11.0.0.1"), allow: allow, want: false},
		{name: "single address", ip: netip.MustParseAddr("192.0.2.7"), allow: allow, want: true},
		{name: "single address neighbour", ip: netip.MustParseAddr("192.0.2.8"), allow: allow, want: false},
		{name: "ipv6 inside", ip: netip.MustParseAddr("2001:db8:1::5"), allow: allow, want: true},
		{name: "ipv6 outside", ip: netip.MustParseAddr("2001:db9::5"), allow: allow, want: false},
		{name: "mapped ipv4 normalized", ip: netip.MustParseAddr("::ffff:10.1.1.1"), allow: allow, want: true},
		{name: "zone ignored", ip: netip.MustParseAddr("2001:db8::1%eth0"), allow: allow, want: true},
		{name: "invalid addr denied", ip: netip.Addr{}, allow: allow, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, AllowedIP(tt.ip, tt.allow))
		})
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()
	trusted := mustPrefixes(t, "10.0.0.0/8", "fd00::/8", "127.0.0.1")
	tests := []struct {
		name    string
		remote  string
		xff     []string
		realIP  []string
		trusted []netip.Prefix
		want    string
	}{
		{name: "untrusted peer ignores headers", remote: "203.0.113.5:4000", xff: []string{"1.1.1.1"}, realIP: []string{"2.2.2.2"}, trusted: trusted, want: "203.0.113.5"},
		{name: "no trusted prefixes ignores headers", remote: "10.0.0.1:4000", xff: []string{"1.1.1.1"}, want: "10.0.0.1"},
		{name: "trusted peer without headers", remote: "10.0.0.1:4000", trusted: trusted, want: "10.0.0.1"},
		{name: "single hop", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "spoofed left entries ignored", remote: "10.0.0.1:4000", xff: []string{"6.6.6.6, 198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "trusted hops skipped", remote: "10.0.0.1:4000", xff: []string{"6.6.6.6, 198.51.100.9, 10.0.0.2, 10.0.0.3"}, trusted: trusted, want: "198.51.100.9"},
		{name: "all hops trusted returns leftmost", remote: "10.0.0.1:4000", xff: []string{"10.0.0.5, 10.0.0.2"}, trusted: trusted, want: "10.0.0.5"},
		{name: "multiple header lines", remote: "10.0.0.1:4000", xff: []string{"6.6.6.6", "198.51.100.9, 10.0.0.2"}, trusted: trusted, want: "198.51.100.9"},
		{name: "multiple header lines last trusted", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9", "10.0.0.2"}, trusted: trusted, want: "198.51.100.9"},
		{name: "hop with port", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9:5555"}, trusted: trusted, want: "198.51.100.9"},
		{name: "ipv6 hop", remote: "10.0.0.1:4000", xff: []string{"2001:db8::1"}, trusted: trusted, want: "2001:db8::1"},
		{name: "bracketed ipv6 hop with port", remote: "10.0.0.1:4000", xff: []string{"[2001:db8::1]:443"}, trusted: trusted, want: "2001:db8::1"},
		{name: "bracketed ipv6 hop without port", remote: "10.0.0.1:4000", xff: []string{"[2001:db8::1]"}, trusted: trusted, want: "2001:db8::1"},
		{name: "quoted hop", remote: "10.0.0.1:4000", xff: []string{`"[2001:db8::1]:443"`}, trusted: trusted, want: "2001:db8::1"},
		{name: "mapped hop unmapped", remote: "10.0.0.1:4000", xff: []string{"::ffff:198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "mapped trusted hop skipped", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9, ::ffff:10.0.0.2"}, trusted: trusted, want: "198.51.100.9"},
		{name: "ipv6 peer trusted", remote: "[fd00::1]:4000", xff: []string{"198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "mapped peer trusted", remote: "[::ffff:10.0.0.1]:4000", xff: []string{"198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "zoned ipv6 peer", remote: "[fe80::1%en0]:4000", trusted: trusted, want: "fe80::1"},
		{name: "peer without port", remote: "10.0.0.1", xff: []string{"198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "malformed rightmost falls back to real ip", remote: "10.0.0.1:4000", xff: []string{"garbage"}, realIP: []string{"198.51.100.20"}, trusted: trusted, want: "198.51.100.20"},
		{name: "malformed rightmost falls back to peer", remote: "10.0.0.1:4000", xff: []string{"unknown"}, trusted: trusted, want: "10.0.0.1"},
		{name: "malformed hop stops walk", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9, garbage, 10.0.0.7, 10.0.0.2"}, trusted: trusted, want: "10.0.0.7"},
		{name: "untrusted hop before malformed", remote: "10.0.0.1:4000", xff: []string{"garbage, 198.51.100.9"}, trusted: trusted, want: "198.51.100.9"},
		{name: "empty middle hop is malformed", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9, , 10.0.0.2"}, trusted: trusted, want: "10.0.0.2"},
		{name: "trailing comma is malformed", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9,"}, realIP: []string{"198.51.100.30"}, trusted: trusted, want: "198.51.100.30"},
		{name: "blank header line ignored", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9", " "}, trusted: trusted, want: "198.51.100.9"},
		{name: "overlong hop malformed", remote: "10.0.0.1:4000", xff: []string{strings.Repeat("1", 200)}, trusted: trusted, want: "10.0.0.1"},
		{name: "bracket without close malformed", remote: "10.0.0.1:4000", xff: []string{"[2001:db8::1"}, trusted: trusted, want: "10.0.0.1"},
		{name: "bracket with bad tail malformed", remote: "10.0.0.1:4000", xff: []string{"[2001:db8::1]x"}, trusted: trusted, want: "10.0.0.1"},
		{name: "bracketed ipv4 malformed", remote: "10.0.0.1:4000", xff: []string{"[198.51.100.9]"}, trusted: trusted, want: "10.0.0.1"},
		{name: "ipv4 hop with out-of-range port malformed", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9:99999"}, trusted: trusted, want: "10.0.0.1"},
		{name: "real ip used without xff", remote: "10.0.0.1:4000", realIP: []string{"198.51.100.20"}, trusted: trusted, want: "198.51.100.20"},
		{name: "real ip with port", remote: "10.0.0.1:4000", realIP: []string{"198.51.100.20:80"}, trusted: trusted, want: "198.51.100.20"},
		{name: "invalid real ip falls back to peer", remote: "10.0.0.1:4000", realIP: []string{"nope"}, trusted: trusted, want: "10.0.0.1"},
		{name: "ambiguous multiple real ip ignored", remote: "10.0.0.1:4000", realIP: []string{"198.51.100.20", "198.51.100.21"}, trusted: trusted, want: "10.0.0.1"},
		{name: "xff wins over real ip", remote: "10.0.0.1:4000", xff: []string{"198.51.100.9"}, realIP: []string{"198.51.100.20"}, trusted: trusted, want: "198.51.100.9"},
		{name: "unparseable peer returns zero", remote: "@", xff: []string{"198.51.100.9"}, trusted: trusted, want: "invalid IP"},
		{name: "empty peer returns zero", remote: "", trusted: trusted, want: "invalid IP"},
		{name: "hostname peer returns zero", remote: "localhost:80", trusted: trusted, want: "invalid IP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			for _, v := range tt.realIP {
				r.Header.Add("X-Real-IP", v)
			}
			got := ClientIP(r, tt.trusted)
			require.Equal(t, tt.want, got.String())
			if got.IsValid() {
				require.Empty(t, got.Zone())
				require.False(t, got.Is4In6())
			}
		})
	}
}

func TestClientIPNilRequest(t *testing.T) {
	t.Parallel()
	require.False(t, ClientIP(nil, nil).IsValid())
}

// FuzzClientIP checks the ClientIP invariants for arbitrary peers and headers:
// the result is the zero address or a normalized one, an unparseable peer
// yields the zero address and an untrusted peer is never overridden by
// forwarding headers.
func FuzzClientIP(f *testing.F) {
	f.Add("10.0.0.1:4000", "198.51.100.9, 10.0.0.2", "198.51.100.20")
	f.Add("[fd00::1]:4000", "[::ffff:198.51.100.9]:5,,", "")
	f.Add("203.0.113.5:1", "10.0.0.7", "10.0.0.8")
	f.Add("@", "", "garbage")
	trusted, err := ParsePrefixes([]string{"10.0.0.0/8", "fd00::/8"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, remote, xff, realIP string) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set(HeaderForwardedFor, xff)
		r.Header.Set(HeaderRealIP, realIP)
		got := ClientIP(r, trusted)
		if got.IsValid() && (got.Zone() != "" || got.Is4In6()) {
			t.Fatalf("result %v is not normalized", got)
		}
		peer, ok := parseHostAddr(remote)
		switch {
		case !ok && got.IsValid():
			t.Fatalf("unparseable peer %q produced %v", remote, got)
		case ok && !contains(trusted, peer) && got != peer:
			t.Fatalf("untrusted peer %v was overridden by headers: %v", peer, got)
		}
	})
}
