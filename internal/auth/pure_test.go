package auth

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// fastArgon2 keeps password hashing cheap in tests.
var fastArgon2 = Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestHashPasswordDefaultParams(t *testing.T) {
	encoded, err := HashPassword("correct horse battery")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$"), encoded)

	ok, err := VerifyPassword("correct horse battery", encoded)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = VerifyPassword("wrong horse battery", encoded)
	require.NoError(t, err)
	require.False(t, ok)

	other, err := HashPassword("correct horse battery")
	require.NoError(t, err)
	require.NotEqual(t, encoded, other, "salts must differ")
}

func TestVerifyPasswordMalformed(t *testing.T) {
	valid, err := HashPasswordWithParams("0123456789", fastArgon2)
	require.NoError(t, err)
	parts := strings.Split(valid, "$")
	tests := []struct {
		name    string
		encoded string
	}{
		{"empty", ""},
		{"bcrypt", "$2a$10$abcdefghijklmnopqrstuv"},
		{"wrong version", strings.Replace(valid, "v=19", "v=16", 1)},
		{"missing params", "$argon2id$v=19$$" + parts[4] + "$" + parts[5]},
		{"unknown param", strings.Replace(valid, "p=1", "x=1", 1)},
		{"non-numeric", strings.Replace(valid, "t=1", "t=a", 1)},
		{"bad salt", "$argon2id$v=19$m=64,t=1,p=1$!!$" + parts[5]},
		{"bad key", "$argon2id$v=19$m=64,t=1,p=1$" + parts[4] + "$!!"},
		{"huge memory", strings.Replace(valid, "m=64", "m=99999999", 1)},
		{"huge parallelism", strings.Replace(valid, "p=1", "p=200", 1)},
		{"short key", "$argon2id$v=19$m=64,t=1,p=1$" + parts[4] + "$AAAA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := VerifyPassword("0123456789", tc.encoded)
			require.ErrorIs(t, err, ErrMalformedHash)
			require.False(t, ok)
		})
	}
}

func TestHashPasswordRejectsInvalidParams(t *testing.T) {
	tests := []Argon2Params{
		{Memory: 1, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{Memory: 64, Iterations: 0, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{Memory: 64, Iterations: 1, Parallelism: 0, SaltLength: 16, KeyLength: 32},
		{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 4, KeyLength: 32},
		{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 8},
	}
	for _, p := range tests {
		_, err := HashPasswordWithParams("0123456789", p)
		require.Error(t, err)
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		pw string
		ok bool
	}{
		{"short", false},
		{"123456789", false},
		{"1234567890", true},
		{"密码密码密码密码密码", true},
		{strings.Repeat("a", MaxPasswordLength), true},
		{strings.Repeat("a", MaxPasswordLength+1), false},
		{"\xff\xfe12345678901", false},
	}
	for _, tc := range tests {
		err := ValidatePassword(tc.pw)
		if tc.ok {
			require.NoError(t, err, tc.pw)
		} else {
			require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
		}
	}
}

func TestGenerateToken(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tok, err := GenerateToken()
		require.NoError(t, err)
		require.Len(t, tok, TokenLength)
		require.True(t, WellFormedToken(tok), tok)
		require.False(t, seen[tok])
		seen[tok] = true
	}
	require.Len(t, HashToken("spn_x"), 32)
}

func TestWellFormedToken(t *testing.T) {
	valid := "spn_" + strings.Repeat("aZ9", 14) + "b"
	tests := []struct {
		in string
		ok bool
	}{
		{valid, true},
		{"", false},
		{"spn_short", false},
		{"xyz_" + valid[4:], false},
		{valid[:len(valid)-1] + "-", false},
		{valid + "a", false},
	}
	for _, tc := range tests {
		require.Equal(t, tc.ok, WellFormedToken(tc.in), tc.in)
	}
}

func TestSanitizeNode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"  crawler-hk-03 ", "crawler-hk-03"},
		{"pod/ns@host:1.2", "pod/ns@host:1.2"},
		{"bad node\n<x>", "bad_node__x"},
		{"节点", ""},
		{strings.Repeat("a", 300), strings.Repeat("a", MaxNodeLength)},
	}
	for _, tc := range tests {
		require.Equal(t, tc.want, SanitizeNode(tc.in), tc.in)
	}
}

func TestTruncateUTF8(t *testing.T) {
	require.Equal(t, "ab", truncateUTF8("abc", 2))
	require.Equal(t, "a", truncateUTF8("a你", 3))
	require.Equal(t, "ok", truncateUTF8("o\xffk", 10))
}

func TestExtractRequestMeta(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name       string
		remote     string
		headers    map[string]string
		tls        bool
		wantIP     string
		wantSecure bool
	}{
		{name: "direct", remote: "203.0.113.5:4000", wantIP: "203.0.113.5"},
		{name: "untrusted forwarded ignored", remote: "203.0.113.5:4000",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.1", HeaderForwardedProto: "https"}, wantIP: "203.0.113.5"},
		{name: "trusted forwarded", remote: "10.1.2.3:4000",
			headers: map[string]string{"X-Forwarded-For": "198.51.100.1", HeaderForwardedProto: "https"}, wantIP: "198.51.100.1", wantSecure: true},
		{name: "tls", remote: "203.0.113.5:4000", tls: true, wantIP: "203.0.113.5", wantSecure: true},
		{name: "unparseable remote", remote: "garbage", wantIP: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("User-Agent", "agent/1.0")
			r.Header.Set(HeaderNode, "node-1")
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if tc.tls {
				r.TLS = &tls.ConnectionState{}
			}
			m := ExtractRequestMeta(r, trusted)
			require.Equal(t, tc.wantIP, m.ClientIP)
			require.Equal(t, tc.wantSecure, m.Secure)
			require.Equal(t, "agent/1.0", m.UserAgent)
			require.Equal(t, "node-1", m.Node)
		})
	}
}

func TestTLSMiddlewareMarksContext(t *testing.T) {
	var secure bool
	h := TLSMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Simulate what Connect exposes: no TLS state, only the context.
		clone := r.Clone(r.Context())
		clone.TLS = nil
		secure = ExtractRequestMeta(clone, nil).Secure
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.TLS = &tls.ConnectionState{}
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.True(t, secure)

	ctx := WithRequestMeta(context.Background(), RequestMeta{ClientIP: "192.0.2.1"})
	m, ok := RequestMetaFrom(ctx)
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", m.ClientIP)
	_, ok = RequestMetaFrom(context.Background())
	require.False(t, ok)
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter()
	now := time.Unix(1_700_000_000, 0)

	ok, _ := l.allow("unlimited", 0, now)
	require.True(t, ok)

	for i := range 3 {
		ok, _ := l.allow("tok", 3, now)
		require.True(t, ok, "request %d within burst", i)
	}
	ok, wait := l.allow("tok", 3, now)
	require.False(t, ok)
	require.InDelta(t, float64(time.Second/3), float64(wait), float64(2*time.Millisecond))

	ok, _ = l.allow("tok", 3, now.Add(400*time.Millisecond))
	require.True(t, ok, "refilled")

	// A changed rate resets the bucket.
	ok, _ = l.allow("tok", 10, now.Add(400*time.Millisecond))
	require.True(t, ok)

	l.forget("tok")
	require.NotContains(t, l.buckets, "tok")

	// Idle buckets are swept.
	l.allow("idle", 1, now)
	l.allow("other", 1, now.Add(limiterIdleTTL+limiterSweepInterval))
	require.NotContains(t, l.buckets, "idle")
	require.Contains(t, l.buckets, "other")
}

func TestRateLimiterConcurrent(t *testing.T) {
	l := newRateLimiter()
	now := time.Now()
	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if ok, _ := l.allow("tok", 10, now); ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Equal(t, 10, allowed)
}

func TestConfigNormalized(t *testing.T) {
	c := Config{CookieSecure: " TRUE "}.normalized()
	require.Equal(t, DefaultTokenCacheTTL, c.TokenCacheTTL)
	require.Equal(t, DefaultSessionTTL, c.SessionTTL)
	require.Equal(t, CookieSecureTrue, c.CookieSecure)
	require.Equal(t, DefaultArgon2Params(), c.Argon2)
	require.True(t, c.cookieSecure(false))

	c = Config{CookieSecure: "false"}.normalized()
	require.False(t, c.cookieSecure(true))
	c = Config{CookieSecure: "bogus"}.normalized()
	require.Equal(t, CookieSecureAuto, c.CookieSecure)
	require.True(t, c.cookieSecure(true))
	require.False(t, c.cookieSecure(false))
}

func TestValidators(t *testing.T) {
	require.NoError(t, ValidateUsername("alice.b_c-1"))
	for _, bad := range []string{"", "ab", "Alice", "-bob", strings.Repeat("a", 65), "a b c"} {
		require.Error(t, ValidateUsername(bad), bad)
	}
	require.NoError(t, ValidateSlug("name", "prod-1"))
	for _, bad := range []string{"a", "Prod", "-x", "a_b", strings.Repeat("a", 64)} {
		require.Error(t, ValidateSlug("name", bad), bad)
	}
	require.True(t, ValidSiteName("shop_v2.web"))
	require.False(t, ValidSiteName("Shop"))
	require.NoError(t, validateEmail(""))
	require.NoError(t, validateEmail("a@example.com"))
	for _, bad := range []string{"nope", "Alice <a@example.com>", strings.Repeat("a", 250) + "@x.io"} {
		require.Error(t, validateEmail(bad), bad)
	}
	require.NoError(t, validateLocale(""))
	require.NoError(t, validateLocale("zh-CN"))
	require.Error(t, validateLocale("zh_CN"))
	require.NoError(t, validateText("d", "line1\nline2\tx", 100))
	require.Error(t, validateText("d", "bell\a", 100))
	require.Error(t, validateText("d", "abc", 2))
	require.Error(t, validateText("d", "\xff", 2))
	require.Error(t, validateName("n", "  ", 10))
	require.Error(t, validateName("n", "a\nb", 10))
	require.NoError(t, validateName("n", "node token", 10))
	require.Equal(t, "alice", NormalizeUsername("  ALICE "))
}
