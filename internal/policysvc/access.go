package policysvc

import (
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// Policies are namespace-level objects. Principals holding a permission on
// the namespace see and manage every policy. Site-restricted principals (role
// bindings narrowed to sites) see the policies that apply to their sites:
// those bound at namespace level or on one of their sites. Mutating a policy
// always requires the namespace-level permission because a policy may be bound
// anywhere in the namespace; binding changes require policy:publish on the
// binding target (the site, or the namespace for namespace-level bindings).

// nsResource is the authorization resource of a namespace-level object.
func nsResource(ns *catalog.Namespace) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
}

// siteResource is the authorization resource of a site-level object.
func siteResource(ns *catalog.Namespace, siteID string) authz.Resource {
	r := nsResource(ns)
	r.SiteID = siteID
	if s, ok := ns.SitesByID[siteID]; ok {
		r.SiteName = s.Name
	}
	return r
}

// access describes where a principal holds a permission inside a namespace.
type access struct {
	all   bool
	sites map[string]struct{}
}

// accessFor computes the namespace-wide or per-site access of p.
func accessFor(p *authz.Principal, ns *catalog.Namespace, perm authz.Permission) access {
	if p.Can(perm, nsResource(ns)) {
		return access{all: true}
	}
	a := access{sites: map[string]struct{}{}}
	for id := range ns.SitesByID {
		if p.Can(perm, siteResource(ns, id)) {
			a.sites[id] = struct{}{}
		}
	}
	return a
}

// any reports whether the principal holds the permission anywhere.
func (a access) any() bool { return a.all || len(a.sites) > 0 }

// site reports whether the principal holds the permission on the site.
func (a access) site(id string) bool {
	if a.all {
		return true
	}
	_, ok := a.sites[id]
	return ok
}

// siteIDs returns the accessible site IDs (nil when all).
func (a access) siteIDs() []string {
	if a.all {
		return nil
	}
	out := make([]string, 0, len(a.sites))
	for id := range a.sites {
		out = append(out, id)
	}
	return out
}

// bindingVisible reports whether a binding on siteID ("" = namespace level)
// is visible.
func (a access) bindingVisible(siteID string) bool {
	if siteID == "" {
		return a.any()
	}
	return a.site(siteID)
}

// policyVisible reports whether a policy with the given binding sites is
// visible: always with namespace access, otherwise when it applies to one of
// the accessible sites.
func (a access) policyVisible(bindingSites []string) bool {
	if a.all {
		return true
	}
	if !a.any() {
		return false
	}
	for _, id := range bindingSites {
		if id == "" || a.site(id) {
			return true
		}
	}
	return false
}

// denied returns the permission error for a namespace-level check.
func denied(p *authz.Principal, ns *catalog.Namespace, perm authz.Permission) error {
	if err := p.Require(perm, nsResource(ns)); err != nil {
		return err
	}
	return apperr.PermissionDenied(apperr.ReasonPermissionDenied, "permission %s required", string(perm))
}

// validKindFilter accepts "" or a valid kind.
func validKindFilter(k policy.Kind) error {
	if k != "" && !policy.ValidKind(k) {
		return apperr.InvalidArgument("", "kind must be one of rotation|signal|action|breaker")
	}
	return nil
}

// requireKind accepts a valid kind only.
func requireKind(k policy.Kind) error {
	if !policy.ValidKind(k) {
		return apperr.InvalidArgument("", "kind must be one of rotation|signal|action|breaker")
	}
	return nil
}
