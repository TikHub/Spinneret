package authz

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/pkg/glob"
)

// API token scope names (spec §3.2).
const (
	ScopeLeaseAcquire  = "lease:acquire"  // [:<site name>]
	ScopeReportWrite   = "report:write"   // [:<site name>]
	ScopeConfigRead    = "config:read"    // [:<group glob>]
	ScopeConfigPublish = "config:publish" // [:<group glob>]
	ScopeSecretRead    = "secret:read"    // :<glob over "<namespace name>/<path>"> (required)
	ScopeIdentityWrite = "identity:write" // [:<site name>]
	ScopeProxyWrite    = "proxy:write"    // no argument
	ScopeAdmin         = "admin"          // no argument
)

// Scope length limits.
const (
	MaxScopeLen    = 512
	MaxScopeArgLen = 256
)

// Scope is a parsed API token scope, e.g. "lease:acquire:shop" has Name
// "lease:acquire" and Arg "shop".
type Scope struct {
	Raw  string
	Name string
	Arg  string
}

// String returns the raw scope string.
func (s Scope) String() string { return s.Raw }

// argKind describes how a scope argument restricts the resource.
type argKind int

const (
	argNone       argKind = iota // no argument allowed
	argSite                      // optional exact site name
	argGroupGlob                 // optional glob over the config group
	argSecretGlob                // required glob over "<namespace name>/<secret path>"
)

type scopeDef struct {
	arg   argKind
	perms []Permission // nil for admin (role admin permissions)
}

// scopeDefs is read-only after initialization.
var scopeDefs = map[string]scopeDef{
	ScopeLeaseAcquire:  {arg: argSite, perms: []Permission{PermLeaseAcquire}},
	ScopeReportWrite:   {arg: argSite, perms: []Permission{PermReportWrite}},
	ScopeConfigRead:    {arg: argGroupGlob, perms: []Permission{PermConfigRead}},
	ScopeConfigPublish: {arg: argGroupGlob, perms: []Permission{PermConfigRead, PermConfigWrite, PermConfigPublish}},
	ScopeSecretRead:    {arg: argSecretGlob, perms: []Permission{PermSecretRead}},
	ScopeIdentityWrite: {arg: argSite, perms: []Permission{PermIdentityRead, PermIdentityWrite, PermIdentityOperate}},
	ScopeProxyWrite:    {arg: argNone, perms: []Permission{PermProxyRead, PermProxyWrite, PermProxyOperate}},
	ScopeAdmin:         {arg: argNone},
}

// ScopeNames returns every scope name.
func ScopeNames() []string {
	return []string{
		ScopeLeaseAcquire, ScopeReportWrite, ScopeConfigRead, ScopeConfigPublish,
		ScopeSecretRead, ScopeIdentityWrite, ScopeProxyWrite, ScopeAdmin,
	}
}

// ParseScope parses and validates a token scope string. Scope names are
// lease:acquire, report:write, config:read, config:publish, secret:read,
// identity:write, proxy:write and admin. Arguments follow the name after a
// colon, must be non-empty, at most 256 bytes of valid UTF-8 without
// whitespace or control characters; secret:read requires an argument,
// proxy:write and admin take none, and site-name arguments must not contain
// the wildcards '*' or '?' (site names are compared exactly).
// Errors are apperr InvalidArgument errors.
func ParseScope(s string) (Scope, error) {
	if s == "" {
		return Scope{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "scope must not be empty")
	}
	if len(s) > MaxScopeLen {
		return Scope{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "scope exceeds %d bytes", MaxScopeLen)
	}
	if !utf8.ValidString(s) || strings.IndexFunc(s, invalidScopeRune) >= 0 {
		return Scope{}, invalidScope(s, "contains whitespace, control characters or invalid UTF-8")
	}
	name, arg, hasArg := splitScope(s)
	def, ok := scopeDefs[name]
	if !ok {
		return Scope{}, invalidScope(s, "unknown scope name")
	}
	if hasArg {
		if arg == "" {
			return Scope{}, invalidScope(s, "argument must not be empty")
		}
		if len(arg) > MaxScopeArgLen {
			return Scope{}, invalidScope(s, "argument is too long")
		}
	}
	switch def.arg {
	case argNone:
		if hasArg {
			return Scope{}, invalidScope(s, "scope takes no argument")
		}
	case argSite:
		if hasArg && glob.HasWildcard(arg) {
			return Scope{}, invalidScope(s, "site name argument must not contain wildcards; omit it to allow all sites")
		}
	case argSecretGlob:
		if !hasArg {
			return Scope{}, invalidScope(s, `argument "<namespace>/<path glob>" is required`)
		}
	case argGroupGlob:
	}
	return Scope{Raw: s, Name: name, Arg: arg}, nil
}

// ParseScopes parses every scope and rejects duplicates. The result is never
// nil.
func ParseScopes(ss []string) ([]Scope, error) {
	out := make([]Scope, 0, len(ss))
	seen := make(map[string]struct{}, len(ss))
	for _, raw := range ss {
		sc, err := ParseScope(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[sc.Raw]; dup {
			return nil, invalidScope(raw, "duplicate scope")
		}
		seen[sc.Raw] = struct{}{}
		out = append(out, sc)
	}
	return out, nil
}

// Permissions returns the permissions the scope grants, ignoring any argument
// restriction. It returns nil for an unknown scope name.
func (s Scope) Permissions() []Permission {
	def, ok := scopeDefs[s.Name]
	if !ok {
		return nil
	}
	if s.Name == ScopeAdmin {
		return RolePermissions(RoleAdmin)
	}
	out := make([]Permission, len(def.perms))
	copy(out, def.perms)
	return out
}

// grantsPermission reports whether the scope, ignoring its argument, grants
// perm, together with its definition. Structurally invalid scopes (for
// example built by hand rather than by ParseScope) grant nothing.
func (s Scope) grantsPermission(perm Permission) (scopeDef, bool) {
	def, ok := scopeDefs[s.Name]
	if !ok {
		return scopeDef{}, false
	}
	switch def.arg {
	case argNone:
		if s.Arg != "" {
			return scopeDef{}, false
		}
	case argSecretGlob:
		if s.Arg == "" {
			return scopeDef{}, false
		}
	case argSite, argGroupGlob:
	}
	if s.Name == ScopeAdmin {
		return def, RoleHas(RoleAdmin, perm)
	}
	for _, p := range def.perms {
		if p == perm {
			return def, true
		}
	}
	return scopeDef{}, false
}

// allows reports whether the scope grants perm on r for a token bound to the
// namespace named namespaceName.
func (s Scope) allows(perm Permission, r Resource, namespaceName string) bool {
	def, ok := s.grantsPermission(perm)
	if !ok {
		return false
	}
	switch def.arg {
	case argSite:
		return s.Arg == "" || (r.SiteName != "" && r.SiteName == s.Arg)
	case argGroupGlob:
		return s.Arg == "" || glob.Match(s.Arg, r.ConfigGroup)
	case argSecretGlob:
		name := r.NamespaceName
		if name == "" {
			name = namespaceName
		}
		if name == "" || (namespaceName != "" && name != namespaceName) || !cleanSecretPath(r.SecretPath) {
			return false
		}
		return glob.Match(s.Arg, name+"/"+r.SecretPath)
	default:
		return true
	}
}

// cleanSecretPath rejects empty paths and paths that are not in canonical
// relative form (leading or trailing '/', empty, "." or ".." segments), so a
// path glob can never be satisfied through path aliasing.
func cleanSecretPath(p string) bool {
	if p == "" {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// splitScope splits "<name>[:<arg>]" where name is "admin" or "<a>:<b>".
func splitScope(s string) (name, arg string, hasArg bool) {
	first := strings.IndexByte(s, ':')
	if first < 0 {
		return s, "", false
	}
	if s[:first] == ScopeAdmin {
		return ScopeAdmin, s[first+1:], true
	}
	second := strings.IndexByte(s[first+1:], ':')
	if second < 0 {
		return s, "", false
	}
	cut := first + 1 + second
	return s[:cut], s[cut+1:], true
}

func invalidScopeRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == utf8.RuneError
}

// invalidScope builds the validation error; callers guarantee len(s) <= MaxScopeLen.
func invalidScope(s, why string) *apperr.Error {
	return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "invalid scope %q: %s", s, why)
}
