package proxy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    ParsedURL
		norm    string
		display string
		hint    string
		errPart string
	}{
		{
			name: "http with credentials", raw: "http://user:pass@1.2.3.4:8080",
			want: ParsedURL{Scheme: "http", Host: "1.2.3.4", Port: 8080, Username: "user", Password: "pass", HasUser: true},
			norm: "http://user:pass@1.2.3.4:8080", display: "http://1.2.3.4:8080", hint: "user***",
		},
		{
			name: "upper case scheme and host, trailing slash", raw: "  HTTPS://Proxy.Example.COM:443/  ",
			want: ParsedURL{Scheme: "https", Host: "proxy.example.com", Port: 443},
			norm: "https://proxy.example.com:443", display: "https://proxy.example.com:443",
		},
		{
			name: "socks5 percent-encoded credentials", raw: "socks5://us%40er:p%3Ass%2Fw@gw_1.provider.example.com:1080",
			want: ParsedURL{Scheme: "socks5", Host: "gw_1.provider.example.com", Port: 1080, Username: "us@er", Password: "p:ss/w", HasUser: true},
			norm: "socks5://us%40er:p%3Ass%2Fw@gw_1.provider.example.com:1080", display: "socks5://gw_1.provider.example.com:1080", hint: "us@e***",
		},
		{
			name: "ipv6", raw: "http://[2001:DB8::1]:3128",
			want: ParsedURL{Scheme: "http", Host: "2001:db8::1", Port: 3128},
			norm: "http://[2001:db8::1]:3128", display: "http://[2001:db8::1]:3128",
		},
		{
			name: "user without password", raw: "http://tokenuser@host.example:80",
			want: ParsedURL{Scheme: "http", Host: "host.example", Port: 80, Username: "tokenuser", HasUser: true},
			norm: "http://tokenuser@host.example:80", display: "http://host.example:80", hint: "toke***",
		},
		{
			name: "empty password normalizes", raw: "http://abc:@host.example:80",
			want: ParsedURL{Scheme: "http", Host: "host.example", Port: 80, Username: "abc", HasUser: true},
			norm: "http://abc@host.example:80", display: "http://host.example:80", hint: "abc***",
		},
		{name: "empty", raw: " ", errPart: "empty"},
		{name: "no scheme", raw: "1.2.3.4:8080", errPart: "must start with"},
		{name: "bad scheme", raw: "ftp://1.2.3.4:21", errPart: "scheme"},
		{name: "socks4", raw: "socks4://1.2.3.4:1080", errPart: "scheme"},
		{name: "no port", raw: "http://1.2.3.4", errPart: "port"},
		{name: "port zero", raw: "http://1.2.3.4:0", errPart: "port"},
		{name: "port too large", raw: "http://1.2.3.4:65536", errPart: "port"},
		{name: "non numeric port", raw: "http://user:secret@1.2.3.4:abc", errPart: "port"},
		{name: "path", raw: "http://1.2.3.4:80/path", errPart: "path"},
		{name: "query", raw: "http://1.2.3.4:80?x=1", errPart: "path"},
		{name: "fragment", raw: "http://1.2.3.4:80#frag", errPart: "path"},
		{name: "empty host", raw: "http://:8080", errPart: "host"},
		{name: "bad hostname", raw: "http://-bad-.example:80", errPart: "host"},
		{name: "invalid ipv4", raw: "http://999.1.1.1:80", errPart: "IPv4"},
		{name: "host with space", raw: "http://bad host:80", errPart: "invalid"},
		{name: "ipv6 zone", raw: "http://[fe80::1%25en0]:80", errPart: "IPv6"},
		{name: "empty user with password", raw: "http://:secret@1.2.3.4:80", errPart: "user name"},
		{name: "control characters", raw: "http://us%0Aer:pw@1.2.3.4:80", errPart: "invalid characters"},
		{name: "too long", raw: "http://" + strings.Repeat("a", MaxURLLength) + ":80", errPart: "too long"},
		{name: "bad escape", raw: "http://us%zzer:pw@1.2.3.4:80", errPart: "percent-encoding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProxyURL(tt.raw)
			if tt.errPart != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errPart)
				require.NotContains(t, err.Error(), "secret", "errors must not echo credentials")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.norm, got.String())
			require.Equal(t, tt.norm, got.URL().String())
			require.Equal(t, tt.display, got.DisplayURL())
			require.Equal(t, tt.hint, got.UsernameHint())
			again, err := ParseProxyURL(got.String())
			require.NoError(t, err)
			require.Equal(t, got, again, "normalization must be idempotent")
		})
	}
}

func TestUsernameHintRunes(t *testing.T) {
	u := ParsedURL{HasUser: true, Username: "日本語ユーザー"}
	require.Equal(t, "日本語ユ***", u.UsernameHint())
}

func TestURLHash(t *testing.T) {
	a, err := ParseProxyURL("HTTP://User:Pass@Host.Example:80")
	require.NoError(t, err)
	b, err := ParseProxyURL("http://User:Pass@host.example:80/")
	require.NoError(t, err)
	c, err := ParseProxyURL("http://User:Other@host.example:80")
	require.NoError(t, err)
	pepper := []byte("pepper")
	require.Equal(t, URLHash(pepper, a), URLHash(pepper, b))
	require.NotEqual(t, URLHash(pepper, a), URLHash(pepper, c))
	require.NotEqual(t, URLHash(pepper, a), URLHash([]byte("other"), a))
	require.Len(t, URLHash(pepper, a), 32)
}

func TestSealOpenURL(t *testing.T) {
	c := vaulttest.NewCipher(t)
	u, err := ParseProxyURL("socks5://alice:s3cr%40t@10.0.0.1:1080")
	require.NoError(t, err)
	sealed, err := SealURL(c, "pxy_1", u)
	require.NoError(t, err)
	require.NotContains(t, string(sealed.Ciphertext), "s3cr@t")

	got, err := OpenURL(c, "pxy_1", sealed)
	require.NoError(t, err)
	require.Equal(t, u, got)

	_, err = OpenURL(c, "pxy_2", sealed)
	require.ErrorIs(t, err, vault.ErrDecrypt, "AAD binds the ciphertext to the proxy id")

	bogus, err := c.Seal([]byte("not a url"), vault.AAD("pxy_3", urlField))
	require.NoError(t, err)
	_, err = OpenURL(c, "pxy_3", bogus)
	require.ErrorContains(t, err, "stored proxy url is invalid")
}
