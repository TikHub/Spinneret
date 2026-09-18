package spinneret

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func newRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	require.NoError(t, err)
	return req
}

func TestApplyCredentialHeadersAndQuery(t *testing.T) {
	req := newRequest(t, "https://target.example.com/api/v1/feed?b=2&a=1&signature=x%2By")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("user-agent", "explicit/1.0")
	ApplyCredential(req, &Credential{
		Headers: map[string]string{
			"user-agent": "Mozilla/5.0",
			"Referer":    "https://target.example.com/",
			"Host":       "evil.example",
			"":           "ignored",
		},
		Query: map[string]string{"csrf_token": "x9 y8", "a": "credential", "": "ignored"},
	})
	require.Equal(t, "explicit/1.0", req.Header.Get("User-Agent"), "request values win")
	require.Equal(t, "https://target.example.com/", req.Header.Get("Referer"))
	require.Empty(t, req.Header.Get("Host"))
	require.Equal(t, "target.example.com", req.Host)
	require.Equal(t, "b=2&a=1&signature=x%2By&csrf_token=x9+y8", req.URL.RawQuery, "existing query kept byte for byte")

	empty := newRequest(t, "https://x/")
	empty.Header = nil
	ApplyCredential(empty, &Credential{Query: map[string]string{"k": "v"}})
	require.Equal(t, "k=v", empty.URL.RawQuery)
	require.NotNil(t, empty.Header)

	ApplyCredential(nil, &Credential{})
	ApplyCredential(empty, nil)
}

func TestApplyCredentialCookies(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		cred     *Credential
		want     string
	}{
		{
			name: "cookie header verbatim",
			cred: &Credential{CookieHeader: `sessionid=a1b2c3; csrf_token=1%7Cabc; odd="quoted value"`},
			want: `sessionid=a1b2c3; csrf_token=1%7Cabc; odd="quoted value"`,
		},
		{
			name: "cookie map sorted",
			cred: &Credential{Cookies: map[string]string{"b": "2", "a": "1"}, CookieHeader: "a=1; b=2"},
			want: "a=1; b=2",
		},
		{
			name:     "merge with request cookies",
			existing: "a=mine",
			cred:     &Credential{Cookies: map[string]string{"a": "cred", "c": "3"}},
			want:     "a=mine; c=3",
		},
		{
			name:     "merge cookie header with request cookies",
			existing: "a=mine",
			cred:     &Credential{CookieHeader: "a=cred; d=4"},
			want:     "a=mine; d=4",
		},
		{
			name: "cookie from credential headers",
			cred: &Credential{Headers: map[string]string{"cookie": "h=1"}, CookieHeader: "ignored=1"},
			want: "h=1",
		},
		{
			name: "cookie map plus header cookie",
			cred: &Credential{Cookies: map[string]string{"m": "1"}, Headers: map[string]string{"Cookie": "h=2; m=9"}},
			want: "m=1; h=2",
		},
		{
			name: "no cookies",
			cred: &Credential{},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(t, "https://x/")
			if tc.existing != "" {
				req.Header.Set("Cookie", tc.existing)
			}
			ApplyCredential(req, tc.cred)
			require.Equal(t, tc.want, req.Header.Get("Cookie"))
			require.LessOrEqual(t, len(req.Header.Values("Cookie")), 1)
		})
	}
}

func TestParseCookieHeader(t *testing.T) {
	require.Equal(t, map[string]string{"a": "1", "b": "x=y", "c": ""},
		ParseCookieHeader(" a=1; b=x=y;; novalue; =skip; c="))
}

func TestAppendQueryLenient(t *testing.T) {
	require.Equal(t, "a=%zz", appendQuery("a=%zz", map[string]string{"a": "1"}))
	got := appendQuery("x=%zz;y", map[string]string{"x": "dup", "z": "1"})
	require.Equal(t, "x=%zz;y&z=1", got)
	require.Equal(t, "q=1", appendQuery("q=1", nil))
}

func TestProxyURL(t *testing.T) {
	u, err := ProxyURL(nil)
	require.NoError(t, err)
	require.Nil(t, u)

	u, err = ProxyURL(&ProxyAssignment{Url: "socks5://user:pass@10.0.0.1:1080"})
	require.NoError(t, err)
	require.Equal(t, "socks5", u.Scheme)
	require.Equal(t, "pass", func() string { p, _ := u.User.Password(); return p }())

	_, err = ProxyURL(&ProxyAssignment{ProxyId: "pxy_9", Url: "not a url with secret"})
	require.ErrorContains(t, err, "pxy_9")
	require.NotContains(t, err.Error(), "secret")
	_, err = ProxyURL(&ProxyAssignment{ProxyId: "pxy_9", Url: "http://[::1"})
	require.Error(t, err)
}
