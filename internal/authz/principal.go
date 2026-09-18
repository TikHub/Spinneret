package authz

import (
	"slices"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// Binding is a role binding of a user inside one tenant, optionally narrowed
// to a namespace and to a set of sites.
type Binding struct {
	ID       string
	TenantID string
	Role     Role
	// NamespaceID restricts the binding to one namespace; "" means every
	// namespace of the tenant (and tenant-level resources).
	NamespaceID string
	// SiteIDs restricts the binding to the listed sites; empty means all sites.
	SiteIDs []string
	// Extra lists additional permissions; only those accepted by
	// ValidExtraPermission are honoured.
	Extra []Permission
}

// grants reports whether the binding's role or valid extra permissions include
// perm, ignoring the resource. Bindings with an unknown role grant nothing.
func (b Binding) grants(perm Permission) bool {
	if !ValidRole(b.Role) {
		return false
	}
	if RoleHas(b.Role, perm) {
		return true
	}
	return ValidExtraPermission(perm) && slices.Contains(b.Extra, perm)
}

// allows reports whether the binding grants perm on r. The caller has already
// rejected platform-only permissions and malformed resources.
func (b Binding) allows(perm Permission, r Resource) bool {
	if b.TenantID == "" || b.TenantID != r.TenantID {
		return false
	}
	if b.NamespaceID != "" && b.NamespaceID != r.NamespaceID {
		return false
	}
	if !b.grants(perm) {
		return false
	}
	if len(b.SiteIDs) == 0 {
		return true
	}
	if r.SiteID == "" {
		// Namespace-level resource with a site-restricted binding: only the
		// read permissions site-scoped users need to work inside their
		// namespace, and only when the binding is pinned to that namespace
		// (a tenant-wide site list cannot be tied to a namespace without I/O).
		return siteRestrictedNamespacePermission(perm) && b.NamespaceID != ""
	}
	return slices.Contains(b.SiteIDs, r.SiteID)
}

// siteRestrictedNamespacePermission lists the permissions that site-restricted
// bindings also hold on namespace-level resources.
func siteRestrictedNamespacePermission(perm Permission) bool {
	return perm == PermProxyRead || perm == PermNamespaceRead
}

// PrincipalKind identifies the kind of authenticated caller.
type PrincipalKind string

// Principal kinds.
const (
	KindUser   PrincipalKind = "user"
	KindToken  PrincipalKind = "token"
	KindSystem PrincipalKind = "system"
)

// Principal is an authenticated caller.
type Principal struct {
	Kind PrincipalKind
	ID   string
	Name string
	// TenantID is the active tenant for users (may be "" for platform admins
	// outside a tenant; it is a UI selection, not a security boundary — the
	// bindings are) and the fixed tenant for tokens.
	TenantID string
	// NamespaceID and NamespaceName are set for tokens only.
	NamespaceID   string
	NamespaceName string
	// IsPlatformAdmin is honoured for users only.
	IsPlatformAdmin bool
	// Bindings holds the role bindings of a user across all tenants.
	Bindings []Binding
	// Scopes holds the scopes of a token.
	Scopes []Scope

	ClientIP  string
	UserAgent string
	Node      string
}

// Resource identifies what a permission is checked against. Callers set every
// field they know:
//   - TenantID is always required.
//   - NamespaceID (and NamespaceName) for namespace and site resources; it is
//     "" only for tenant-level resources (users, role bindings, tenant-wide
//     notification channels).
//   - SiteID for site resources (sites, endpoint groups, identity types,
//     identities, accounts); SiteName as well for token checks, which compare
//     site-name scope arguments. Namespace-level resources (proxies, configs,
//     secrets, tokens, the namespace itself) leave both empty.
//   - ConfigGroup for config items and SecretPath (namespace-relative) for
//     secrets, used by token scope globs.
type Resource struct {
	TenantID      string
	NamespaceID   string
	NamespaceName string
	SiteID        string
	SiteName      string
	ConfigGroup   string
	SecretPath    string
}

// Can reports whether the principal holds perm on r.
//
//   - System principals and platform-admin users hold every known permission.
//   - tenant:manage and kek:manage are held by nobody else.
//   - Users need a binding in r's tenant whose namespace is unrestricted or
//     equal to r's, whose sites are unrestricted or contain r.SiteID (for
//     namespace-level resources an unrestricted site list is required, except
//     proxy:read and namespace:read for bindings pinned to that namespace),
//     and whose role or extra permissions include perm.
//   - Tokens must be bound to r's tenant and namespace and hold a scope
//     granting perm whose argument matches r (site name exactly, config group
//     glob, or "<namespace name>/<secret path>" glob).
//
// Unknown permissions, unknown principal kinds, a nil principal and malformed
// resources (no tenant, or a site without a namespace) are denied.
func (p *Principal) Can(perm Permission, r Resource) bool {
	if p == nil || !ValidPermission(perm) {
		return false
	}
	switch p.Kind {
	case KindSystem:
		return true
	case KindUser:
		if p.IsPlatformAdmin {
			return true
		}
		if PlatformOnly(perm) || !wellFormed(r) {
			return false
		}
		for _, b := range p.Bindings {
			if b.allows(perm, r) {
				return true
			}
		}
		return false
	case KindToken:
		if PlatformOnly(perm) || !wellFormed(r) || r.NamespaceID == "" || !p.tokenBoundTo(r.TenantID, r.NamespaceID) {
			return false
		}
		for _, s := range p.Scopes {
			if s.allows(perm, r, p.NamespaceName) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// Require returns nil when Can(perm, r) holds. Otherwise it returns an apperr
// PermissionDenied error with reason scope_missing for tokens and
// permission_denied for other principals, or Unauthenticated(session_invalid)
// for a nil principal. Messages name the permission only, never the resource,
// so they do not reveal whether a resource exists.
func (p *Principal) Require(perm Permission, r Resource) error {
	if p == nil {
		return apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	if p.Can(perm, r) {
		return nil
	}
	if p.Kind == KindToken {
		return apperr.PermissionDenied(apperr.ReasonScopeMissing, "token scopes do not grant %s", string(perm))
	}
	return apperr.PermissionDenied(apperr.ReasonPermissionDenied, "permission %s required", string(perm))
}

// SiteFilter describes which sites of a namespace the principal may access
// with perm, for filtering list results.
//
// all is true when perm is held without any site restriction (system,
// platform admins, a matching binding without site IDs, or a token scope
// granting perm without a site-name argument). Otherwise siteIDs lists the
// sites granted by site-restricted user bindings (sorted, deduplicated, nil
// when none).
//
// Tokens whose only matching scopes carry a site-name argument yield
// (false, nil) because scopes name sites rather than IDs: callers must then
// filter candidates individually with Can, setting Resource.SiteName. The
// general pattern — when all is false and siteIDs is empty, fall back to
// per-item Can checks — is correct for every principal.
//
// SiteFilter only describes site restrictions for site-level resources.
// Namespace-level resources (proxies, configs, secrets, tokens, the namespace
// itself) must be checked with Can/Require: the proxy:read and namespace:read
// allowance of site-restricted bindings, config-group globs and secret-path
// globs are not reflected here (a token scope such as "config:read:app*"
// yields all=true although Can still matches each item's ConfigGroup).
func (p *Principal) SiteFilter(tenantID, namespaceID string, perm Permission) (all bool, siteIDs []string) {
	if p == nil || !ValidPermission(perm) {
		return false, nil
	}
	switch p.Kind {
	case KindSystem:
		return true, nil
	case KindUser:
		if p.IsPlatformAdmin {
			return true, nil
		}
		if PlatformOnly(perm) || tenantID == "" {
			return false, nil
		}
		return p.userSiteFilter(tenantID, namespaceID, perm)
	case KindToken:
		if PlatformOnly(perm) || namespaceID == "" || !p.tokenBoundTo(tenantID, namespaceID) {
			return false, nil
		}
		for _, s := range p.Scopes {
			def, ok := s.grantsPermission(perm)
			if !ok || (def.arg == argSite && s.Arg != "") {
				continue
			}
			return true, nil
		}
		return false, nil
	default:
		return false, nil
	}
}

func (p *Principal) userSiteFilter(tenantID, namespaceID string, perm Permission) (bool, []string) {
	var ids []string
	for _, b := range p.Bindings {
		if b.TenantID != tenantID || (b.NamespaceID != "" && b.NamespaceID != namespaceID) || !b.grants(perm) {
			continue
		}
		if len(b.SiteIDs) == 0 {
			return true, nil
		}
		if namespaceID == "" {
			// Site resources always belong to a namespace.
			continue
		}
		for _, id := range b.SiteIDs {
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	slices.Sort(ids)
	return false, slices.Compact(ids)
}

// TenantIDs returns the tenants the principal can act in, sorted and
// deduplicated. It returns nil — meaning every tenant — for system principals
// and platform admins, and a non-nil (possibly empty) slice for everyone else,
// so callers can distinguish "all" from "none".
func (p *Principal) TenantIDs() []string {
	if p == nil {
		return []string{}
	}
	switch p.Kind {
	case KindSystem:
		return nil
	case KindUser:
		if p.IsPlatformAdmin {
			return nil
		}
		out := make([]string, 0, len(p.Bindings))
		for _, b := range p.Bindings {
			if b.TenantID != "" && ValidRole(b.Role) {
				out = append(out, b.TenantID)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	case KindToken:
		if p.TenantID == "" {
			return []string{}
		}
		return []string{p.TenantID}
	default:
		return []string{}
	}
}

// Actor returns the audit actor string: "user:<id>", "token:<id>" or
// "system" ("anonymous" for a nil principal or an unknown kind).
func (p *Principal) Actor() string {
	if p == nil {
		return "anonymous"
	}
	switch p.Kind {
	case KindUser:
		return "user:" + p.ID
	case KindToken:
		return "token:" + p.ID
	case KindSystem:
		return "system"
	default:
		return "anonymous"
	}
}

// System returns an internal principal that passes every permission check,
// for background jobs and bootstrap code. name identifies the component (for
// example "breaker-evaluator") and defaults to "system".
func System(name string) *Principal {
	if name == "" {
		name = "system"
	}
	return &Principal{Kind: KindSystem, ID: "system", Name: name, IsPlatformAdmin: true}
}

// tokenBoundTo reports whether a token principal is bound to the tenant and
// namespace.
func (p *Principal) tokenBoundTo(tenantID, namespaceID string) bool {
	return p.TenantID != "" && p.NamespaceID != "" && p.TenantID == tenantID && p.NamespaceID == namespaceID
}

// wellFormed rejects resources without a tenant and site resources without a
// namespace.
func wellFormed(r Resource) bool {
	return r.TenantID != "" && (r.SiteID == "" || r.NamespaceID != "")
}
