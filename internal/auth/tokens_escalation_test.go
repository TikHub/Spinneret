package auth

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/auth/authtest"
)

func TestPrefixWithinAny(t *testing.T) {
	parents := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("2001:db8::/32"),
	}
	tests := []struct {
		child string
		want  bool
	}{
		{"10.0.0.0/8", true},
		{"10.1.0.0/16", true},
		{"10.255.255.255/32", true},
		{"192.0.2.7/32", true},
		{"2001:db8:1::/48", true},
		{"0.0.0.0/0", false},
		{"8.0.0.0/7", false},
		{"11.0.0.0/8", false},
		{"192.0.2.0/24", false},
		{"192.0.2.8/32", false},
		{"2001:db8::/31", false},
		{"::ffff:10.0.0.1/128", false},
	}
	for _, tc := range tests {
		t.Run(tc.child, func(t *testing.T) {
			require.Equal(t, tc.want, prefixWithinAny(netip.MustParsePrefix(tc.child), parents))
		})
	}
	require.False(t, prefixWithinAny(netip.MustParsePrefix("10.0.0.1/32"), nil))
}

func TestValidateChildToken(t *testing.T) {
	now := time.Date(2031, 5, 1, 12, 0, 0, 0, time.UTC)
	parentExpiry := now.Add(48 * time.Hour)
	before := parentExpiry.Add(-time.Hour)
	after := parentExpiry.Add(time.Microsecond)
	past := now.Add(-time.Second)

	unrestricted := authdb.AuthTokenGetRow{ID: "tok_parent", Scopes: []string{"admin"}, IpAllowlist: []string{}}
	limited := authdb.AuthTokenGetRow{
		ID: "tok_parent", Scopes: []string{"admin"}, ExpiresAt: &parentExpiry,
		IpAllowlist: []string{"10.0.0.0/8", "2001:db8::/32"}, RateLimitRps: 100,
	}
	revoked := unrestricted
	revoked.RevokedAt = &past
	expired := unrestricted
	expired.ExpiresAt = &now
	corrupt := unrestricted
	corrupt.IpAllowlist = []string{"not-an-ip"}

	valid := CreateTokenInput{
		Name: "child", Scopes: []string{"lease:acquire", "report:write"},
		IPAllowlist: []string{"10.1.2.0/24", "2001:db8:ff::1"}, RateLimitRPS: 100, ExpiresAt: &parentExpiry,
	}
	with := func(mut func(in *CreateTokenInput)) CreateTokenInput {
		in := valid
		mut(&in)
		return in
	}

	tests := []struct {
		name   string
		parent authdb.AuthTokenGetRow
		in     CreateTokenInput
		reason apperr.Reason
	}{
		{"unrestricted parent allows unrestricted child", unrestricted, CreateTokenInput{Name: "c", Scopes: []string{"proxy:write"}}, ""},
		{"unrestricted parent allows any limits", unrestricted, valid, ""},
		{"limited parent, child at the limits", limited, valid, ""},
		{"limited parent, tighter child", limited, with(func(in *CreateTokenInput) {
			in.ExpiresAt, in.RateLimitRPS, in.IPAllowlist = &before, 1, []string{"10.0.0.1"}
		}), ""},
		{"admin scope from unrestricted parent", unrestricted, CreateTokenInput{Name: "c", Scopes: []string{"lease:acquire", "admin"}}, apperr.ReasonPermissionDenied},
		{"admin scope from limited parent", limited, with(func(in *CreateTokenInput) { in.Scopes = []string{"admin"} }), apperr.ReasonPermissionDenied},
		{"missing expiry", limited, with(func(in *CreateTokenInput) { in.ExpiresAt = nil }), apperr.ReasonPermissionDenied},
		{"expiry after parent", limited, with(func(in *CreateTokenInput) { in.ExpiresAt = &after }), apperr.ReasonPermissionDenied},
		{"missing allowlist", limited, with(func(in *CreateTokenInput) { in.IPAllowlist = nil }), apperr.ReasonPermissionDenied},
		{"allowlist wider than parent", limited, with(func(in *CreateTokenInput) { in.IPAllowlist = []string{"10.0.0.0/7"} }), apperr.ReasonPermissionDenied},
		{"allowlist outside parent", limited, with(func(in *CreateTokenInput) { in.IPAllowlist = []string{"10.0.0.1", "192.0.2.1"} }), apperr.ReasonPermissionDenied},
		{"allowlist other family", limited, with(func(in *CreateTokenInput) { in.IPAllowlist = []string{"2001:db9::1"} }), apperr.ReasonPermissionDenied},
		{"unlimited rate", limited, with(func(in *CreateTokenInput) { in.RateLimitRPS = 0 }), apperr.ReasonPermissionDenied},
		{"rate above parent", limited, with(func(in *CreateTokenInput) { in.RateLimitRPS = 101 }), apperr.ReasonPermissionDenied},
		{"revoked parent", revoked, CreateTokenInput{Name: "c", Scopes: []string{"proxy:write"}}, apperr.ReasonTokenRevoked},
		{"expired parent", expired, CreateTokenInput{Name: "c", Scopes: []string{"proxy:write"}}, apperr.ReasonTokenExpired},
		{"corrupt parent allowlist", corrupt, CreateTokenInput{Name: "c", Scopes: []string{"proxy:write"}}, apperr.ReasonInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prep, err := prepareToken(tc.in, now)
			require.NoError(t, err)
			err = validateChildToken(tc.parent, tc.in, prep, now)
			if tc.reason == "" {
				require.NoError(t, err)
				return
			}
			requireReason(t, err, tc.reason)
		})
	}
}

func TestCreateTokenByTokenDoesNotEscalate(t *testing.T) {
	e := newEnv(t)
	w := e.world("acme")
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	_, parentID := e.newToken(w, "parent", CreateTokenInput{
		Scopes: []string{"admin"}, IPAllowlist: []string{"10.0.0.0/8"}, RateLimitRPS: 20, ExpiresAt: &expires,
	})
	parent := authtest.TokenPrincipal(parentID, w.tenant, w.ns, "prod", "admin")
	later := expires.Add(time.Hour)
	sooner := expires.Add(-time.Hour)

	tests := []struct {
		name   string
		in     CreateTokenInput
		reason apperr.Reason
	}{
		{"admin scope", CreateTokenInput{Name: "c1", Scopes: []string{"admin"}, IPAllowlist: []string{"10.0.0.1"}, RateLimitRPS: 1, ExpiresAt: &sooner}, apperr.ReasonPermissionDenied},
		{"no expiry", CreateTokenInput{Name: "c2", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"10.0.0.1"}, RateLimitRPS: 1}, apperr.ReasonPermissionDenied},
		{"later expiry", CreateTokenInput{Name: "c3", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"10.0.0.1"}, RateLimitRPS: 1, ExpiresAt: &later}, apperr.ReasonPermissionDenied},
		{"no allowlist", CreateTokenInput{Name: "c4", Scopes: []string{"lease:acquire"}, RateLimitRPS: 1, ExpiresAt: &sooner}, apperr.ReasonPermissionDenied},
		{"wider allowlist", CreateTokenInput{Name: "c5", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"0.0.0.0/0"}, RateLimitRPS: 1, ExpiresAt: &sooner}, apperr.ReasonPermissionDenied},
		{"no rate limit", CreateTokenInput{Name: "c6", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"10.0.0.1"}, ExpiresAt: &sooner}, apperr.ReasonPermissionDenied},
		{"higher rate limit", CreateTokenInput{Name: "c7", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"10.0.0.1"}, RateLimitRPS: 21, ExpiresAt: &sooner}, apperr.ReasonPermissionDenied},
		{"malformed input is still invalid", CreateTokenInput{Name: "c8", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"999.0.0.1"}}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := e.tokens.Create(e.ctx(), parent, w.nsRef(), tc.in)
			requireReason(t, err, tc.reason)
		})
	}
	require.Empty(t, e.rec.Entries(), "rejected creations are not audited")

	t.Run("child within the parent's limits", func(t *testing.T) {
		view, _, err := e.tokens.Create(e.ctx(), parent, w.nsRef(), CreateTokenInput{
			Name: "child", Scopes: []string{"lease:acquire", "report:write"},
			IPAllowlist: []string{"10.1.0.0/16", "10.2.3.4"}, RateLimitRPS: 20, ExpiresAt: &expires,
		})
		require.NoError(t, err)
		require.Equal(t, "token:"+parentID, view.CreatedBy)
		require.Equal(t, []string{"10.1.0.0/16", "10.2.3.4/32"}, view.IPAllowlist)
	})

	t.Run("users are not restricted", func(t *testing.T) {
		_, _, err := e.tokens.Create(e.ctx(), w.ownerP(), w.nsRef(), CreateTokenInput{Name: "user-admin", Scopes: []string{"admin"}})
		require.NoError(t, err)
	})

	t.Run("revoked parent", func(t *testing.T) {
		_, err := e.tokens.Revoke(e.ctx(), w.ownerP(), parentID)
		require.NoError(t, err)
		_, _, err = e.tokens.Create(e.ctx(), parent, w.nsRef(), CreateTokenInput{
			Name: "after-revoke", Scopes: []string{"lease:acquire"}, IPAllowlist: []string{"10.0.0.1"}, RateLimitRPS: 1, ExpiresAt: &sooner,
		})
		requireReason(t, err, apperr.ReasonTokenRevoked)
	})

	t.Run("unknown parent", func(t *testing.T) {
		ghost := authtest.TokenPrincipal("tok_ghost", w.tenant, w.ns, "prod", "admin")
		_, _, err := e.tokens.Create(e.ctx(), ghost, w.nsRef(), CreateTokenInput{Name: "ghost-child", Scopes: []string{"lease:acquire"}})
		requireReason(t, err, apperr.ReasonTokenInvalid)
	})
}
