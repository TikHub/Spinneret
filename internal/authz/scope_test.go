package authz

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestParseScopeValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		name string
		arg  string
	}{
		{in: "lease:acquire", name: ScopeLeaseAcquire},
		{in: "lease:acquire:shop", name: ScopeLeaseAcquire, arg: "shop"},
		{in: "report:write", name: ScopeReportWrite},
		{in: "report:write:market-web", name: ScopeReportWrite, arg: "market-web"},
		{in: "config:read", name: ScopeConfigRead},
		{in: "config:read:crawler*", name: ScopeConfigRead, arg: "crawler*"},
		{in: "config:read:a:b", name: ScopeConfigRead, arg: "a:b"},
		{in: "config:publish", name: ScopeConfigPublish},
		{in: "config:publish:app-?", name: ScopeConfigPublish, arg: "app-?"},
		{in: "secret:read:prod/*", name: ScopeSecretRead, arg: "prod/*"},
		{in: "secret:read:prod/db/password", name: ScopeSecretRead, arg: "prod/db/password"},
		{in: "identity:write", name: ScopeIdentityWrite},
		{in: "identity:write:shop", name: ScopeIdentityWrite, arg: "shop"},
		{in: "proxy:write", name: ScopeProxyWrite},
		{in: "admin", name: ScopeAdmin},
		{in: "config:read:配置", name: ScopeConfigRead, arg: "配置"},
		{in: "config:read:" + strings.Repeat("g", MaxScopeArgLen), name: ScopeConfigRead, arg: strings.Repeat("g", MaxScopeArgLen)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseScope(tt.in)
			require.NoError(t, err)
			require.Equal(t, Scope{Raw: tt.in, Name: tt.name, Arg: tt.arg}, got)
			require.Equal(t, tt.in, got.String())
		})
	}
}

func TestParseScopeInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		wantMsg string
	}{
		{name: "empty", in: "", wantMsg: "must not be empty"},
		{name: "too long", in: "config:read:" + strings.Repeat("x", MaxScopeLen), wantMsg: "exceeds 512 bytes"},
		{name: "arg too long", in: "config:read:" + strings.Repeat("x", MaxScopeArgLen+1), wantMsg: "argument is too long"},
		{name: "leading space", in: " lease:acquire", wantMsg: "whitespace"},
		{name: "trailing space", in: "lease:acquire ", wantMsg: "whitespace"},
		{name: "space in arg", in: "config:read:my group", wantMsg: "whitespace"},
		{name: "tab", in: "config:read:\tx", wantMsg: "whitespace"},
		{name: "newline", in: "admin\n", wantMsg: "whitespace"},
		{name: "control char", in: "config:read:a\x00b", wantMsg: "control"},
		{name: "invalid utf8", in: "config:read:\xff", wantMsg: "invalid UTF-8"},
		{name: "replacement rune", in: "config:read:�", wantMsg: "invalid UTF-8"},
		{name: "unicode space", in: "config:read:a b", wantMsg: "whitespace"},
		{name: "unknown name", in: "lease:release", wantMsg: "unknown scope name"},
		{name: "unknown single word", in: "root", wantMsg: "unknown scope name"},
		{name: "partial name", in: "lease", wantMsg: "unknown scope name"},
		{name: "uppercase", in: "LEASE:ACQUIRE", wantMsg: "unknown scope name"},
		{name: "wildcard name", in: "*", wantMsg: "unknown scope name"},
		{name: "permission not scope", in: "secret:reveal", wantMsg: "unknown scope name"},
		{name: "role not scope", in: "owner", wantMsg: "unknown scope name"},
		{name: "empty site arg", in: "lease:acquire:", wantMsg: "argument must not be empty"},
		{name: "empty glob arg", in: "config:read:", wantMsg: "argument must not be empty"},
		{name: "secret read without arg", in: "secret:read", wantMsg: "is required"},
		{name: "secret read empty arg", in: "secret:read:", wantMsg: "argument must not be empty"},
		{name: "proxy write with arg", in: "proxy:write:shop", wantMsg: "takes no argument"},
		{name: "admin with arg", in: "admin:ns", wantMsg: "takes no argument"},
		{name: "admin with empty arg", in: "admin:", wantMsg: "argument must not be empty"},
		{name: "site arg star", in: "lease:acquire:*", wantMsg: "must not contain wildcards"},
		{name: "site arg question", in: "report:write:douyi?", wantMsg: "must not contain wildcards"},
		{name: "identity site arg glob", in: "identity:write:dou*", wantMsg: "must not contain wildcards"},
		{name: "leading colon", in: ":lease:acquire", wantMsg: "unknown scope name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseScope(tt.in)
			require.Error(t, err)
			require.Equal(t, Scope{}, got)
			ae, ok := apperr.As(err)
			require.True(t, ok, "error must be an apperr.Error")
			require.Equal(t, connect.CodeInvalidArgument, ae.Code)
			require.Equal(t, apperr.ReasonInvalidArgument, ae.Reason)
			require.Contains(t, ae.Message, tt.wantMsg)
			require.LessOrEqual(t, len(ae.Message), MaxScopeLen+200, "messages must stay bounded")
		})
	}
}

func TestParseScopes(t *testing.T) {
	t.Parallel()
	got, err := ParseScopes(nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	got, err = ParseScopes([]string{"lease:acquire", "report:write", "config:read:app*", "config:read"})
	require.NoError(t, err)
	require.Equal(t, []Scope{
		{Raw: "lease:acquire", Name: ScopeLeaseAcquire},
		{Raw: "report:write", Name: ScopeReportWrite},
		{Raw: "config:read:app*", Name: ScopeConfigRead, Arg: "app*"},
		{Raw: "config:read", Name: ScopeConfigRead},
	}, got)

	_, err = ParseScopes([]string{"lease:acquire", "admin", "lease:acquire"})
	require.Error(t, err)
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
	require.Contains(t, err.Error(), `invalid scope "lease:acquire": duplicate scope`)

	_, err = ParseScopes([]string{"admin", "bogus"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown scope name")
}

func TestScopePermissions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scope string
		want  []Permission
	}{
		{scope: "lease:acquire", want: []Permission{PermLeaseAcquire}},
		{scope: "lease:acquire:shop", want: []Permission{PermLeaseAcquire}},
		{scope: "report:write:x", want: []Permission{PermReportWrite}},
		{scope: "config:read", want: []Permission{PermConfigRead}},
		{scope: "config:publish:app*", want: []Permission{PermConfigRead, PermConfigWrite, PermConfigPublish}},
		{scope: "secret:read:prod/*", want: []Permission{PermSecretRead}},
		{scope: "identity:write", want: []Permission{PermIdentityRead, PermIdentityWrite, PermIdentityOperate}},
		{scope: "proxy:write", want: []Permission{PermProxyRead, PermProxyWrite, PermProxyOperate}},
		{scope: "admin", want: RolePermissions(RoleAdmin)},
	}
	for _, tt := range tests {
		sc, err := ParseScope(tt.scope)
		require.NoError(t, err)
		got := sc.Permissions()
		require.Equal(t, tt.want, got, tt.scope)
		if len(got) > 0 {
			got[0] = PermTenantManage
			require.NotEqual(t, PermTenantManage, sc.Permissions()[0], "Permissions must return a copy")
		}
	}
	require.Nil(t, Scope{Name: "bogus"}.Permissions())
}

func TestScopeNames(t *testing.T) {
	t.Parallel()
	names := ScopeNames()
	require.Len(t, names, 8)
	for _, n := range names {
		_, ok := scopeDefs[n]
		require.True(t, ok, n)
	}
	require.Len(t, scopeDefs, len(names))
}

func TestCleanSecretPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{path: "db/password", want: true},
		{path: "a", want: true},
		{path: "a.b/c-d_e", want: true},
		{path: "..hidden/x", want: true},
		{path: "", want: false},
		{path: "/db", want: false},
		{path: "db/", want: false},
		{path: "db//password", want: false},
		{path: "db/./password", want: false},
		{path: "public/../private", want: false},
		{path: "..", want: false},
		{path: ".", want: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, cleanSecretPath(tt.path), tt.path)
	}
}
