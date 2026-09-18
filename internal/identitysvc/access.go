package identitysvc

import (
	"slices"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// namespaceResource builds the authorization resource of a namespace-level object.
func namespaceResource(ns *catalog.Namespace) authz.Resource {
	return authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
}

// siteResource builds the authorization resource of a site-level object.
func siteResource(ns *catalog.Namespace, site *catalog.Site) authz.Resource {
	r := namespaceResource(ns)
	r.SiteID = site.ID
	r.SiteName = site.Name
	return r
}

// requireSite checks perm on a site the caller named explicitly.
func requireSite(p *authz.Principal, perm authz.Permission, ns *catalog.Namespace, site *catalog.Site) error {
	return p.Require(perm, siteResource(ns, site))
}

// requireAny succeeds when p holds at least one of perms on site.
func requireAny(p *authz.Principal, ns *catalog.Namespace, site *catalog.Site, perms ...authz.Permission) error {
	res := siteResource(ns, site)
	for _, perm := range perms {
		if p.Can(perm, res) {
			return nil
		}
	}
	return p.Require(perms[0], res)
}

// requireByID checks perm on an object that was looked up by ID. Principals
// without any relation to the object's namespace get not_found (what names
// the object), so IDs cannot be probed across tenants.
func requireByID(p *authz.Principal, ns *catalog.Namespace, site *catalog.Site, what string, perms ...authz.Permission) error {
	res := siteResource(ns, site)
	for _, perm := range perms {
		if p.Can(perm, res) {
			return nil
		}
	}
	if !relatedToNamespace(p, ns) {
		return apperr.NotFound("%s not found", what)
	}
	return p.Require(perms[0], res)
}

// relatedToNamespace reports whether p could hold any permission in ns.
func relatedToNamespace(p *authz.Principal, ns *catalog.Namespace) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case authz.KindSystem:
		return true
	case authz.KindUser:
		if p.IsPlatformAdmin {
			return true
		}
		for _, b := range p.Bindings {
			if b.TenantID == ns.TenantID && (b.NamespaceID == "" || b.NamespaceID == ns.ID) {
				return true
			}
		}
		return false
	case authz.KindToken:
		return p.TenantID == ns.TenantID && p.NamespaceID == ns.ID
	default:
		return false
	}
}

// visibleSites returns the sites of ns (sorted by ID) on which p holds at
// least one of perms. It fails with permission_denied when there is none and
// p does not hold any of perms without site restriction (so an empty
// namespace still lists as empty for unrestricted principals).
func visibleSites(p *authz.Principal, ns *catalog.Namespace, perms ...authz.Permission) ([]*catalog.Site, error) {
	if p == nil {
		return nil, apperr.Unauthenticated(apperr.ReasonSessionInvalid, "authentication required")
	}
	unrestricted := false
	for _, perm := range perms {
		if all, _ := p.SiteFilter(ns.TenantID, ns.ID, perm); all {
			unrestricted = true
			break
		}
	}
	out := make([]*catalog.Site, 0, len(ns.SitesByID))
	for _, site := range ns.SitesByID {
		res := siteResource(ns, site)
		for _, perm := range perms {
			if p.Can(perm, res) {
				out = append(out, site)
				break
			}
		}
	}
	if len(out) == 0 && !unrestricted {
		return nil, p.Require(perms[0], namespaceResource(ns))
	}
	slices.SortFunc(out, func(a, b *catalog.Site) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}

// siteIDs returns the IDs of sites.
func siteIDs(sites []*catalog.Site) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.ID
	}
	return out
}

// narrowSites applies an optional site name filter to the visible sites.
// Naming an unknown site is invalid_argument (site_unknown); naming a site the
// principal may not access is permission_denied.
func narrowSites(p *authz.Principal, ns *catalog.Namespace, visible []*catalog.Site, name string, perm authz.Permission) ([]*catalog.Site, error) {
	if name == "" {
		return visible, nil
	}
	site, err := siteByName(ns, name)
	if err != nil {
		return nil, err
	}
	for _, s := range visible {
		if s.ID == site.ID {
			return []*catalog.Site{s}, nil
		}
	}
	return nil, requireSite(p, perm, ns, site)
}

// siteByName resolves a site of the namespace snapshot by name.
func siteByName(ns *catalog.Namespace, name string) (*catalog.Site, error) {
	if name == "" {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site is required")
	}
	site, ok := ns.Sites[name]
	if !ok {
		return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", name)
	}
	return site, nil
}

// siteByID resolves a site and its namespace from the catalog. A site missing
// from the catalog is reported as not_found for the object named by what.
func (s *Service) siteByID(siteID, what string) (*catalog.Site, *catalog.Namespace, error) {
	if s.cat == nil {
		return nil, nil, apperr.Internal(errNoCatalog)
	}
	site, ns, ok := s.cat.Site(siteID)
	if !ok {
		return nil, nil, apperr.NotFound("%s not found", what)
	}
	return site, ns, nil
}
