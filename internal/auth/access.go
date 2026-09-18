package auth

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// maxAccessTenants bounds the tenants listed for platform administrators.
const maxAccessTenants = 1000

// Me returns the principal's user and, for every tenant it can access, its
// bindings and the effective permissions per namespace and site. The
// namespace-wide set lists every permission p with Can(p, namespace); sites
// are listed only when site-restricted bindings grant permissions beyond that
// set.
func (u *Users) Me(ctx context.Context, p *authz.Principal) (*Me, error) {
	if err := requireUser(p); err != nil {
		return nil, err
	}
	user, err := u.q.AuthUserByID(ctx, p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errSessionInvalid()
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	me, err := u.access(ctx, p)
	if err != nil {
		return nil, err
	}
	me.User = userViewFromModel(user)
	return me, nil
}

// MeForUser computes Me for a user ID with freshly loaded bindings (used right
// after sign-in, before a session principal exists).
func (u *Users) MeForUser(ctx context.Context, userID string) (*Me, error) {
	rec, err := loadUserRecord(ctx, u.q, userID)
	if errors.Is(err, errUserUnknown) {
		return nil, errSessionInvalid()
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return u.Me(ctx, rec.principal("", RequestMeta{}))
}

func (u *Users) access(ctx context.Context, p *authz.Principal) (*Me, error) {
	var tenants []authdb.Tenant
	var err error
	if p.IsPlatformAdmin {
		tenants, err = u.q.AuthTenantsAll(ctx, maxAccessTenants)
	} else {
		tenants, err = u.q.AuthTenantsByIDs(ctx, p.TenantIDs())
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	tenantIDs := make([]string, len(tenants))
	for i, t := range tenants {
		tenantIDs[i] = t.ID
	}
	namespaces, err := u.q.AuthNamespacesOfTenants(ctx, tenantIDs)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	bindingRows, err := u.q.AuthBindingsOfUsers(ctx, authdb.AuthBindingsOfUsersParams{UserIds: []string{p.ID}})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	bindings := make([]BindingView, len(bindingRows))
	ptrs := make([]*BindingView, len(bindingRows))
	for i, r := range bindingRows {
		bindings[i] = bindingRow(r).view()
		ptrs[i] = &bindings[i]
	}
	if err := fillSiteNames(ctx, u.q, ptrs); err != nil {
		return nil, apperr.Internal(err)
	}
	sites, err := u.restrictedSites(ctx, p)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	me := &Me{Tenants: make([]TenantAccess, 0, len(tenants))}
	for _, t := range tenants {
		ta := TenantAccess{Tenant: tenantViewFromModel(t), Bindings: []BindingView{}, Namespaces: []NamespaceAccess{}}
		for _, b := range bindings {
			if b.TenantID == t.ID {
				ta.Bindings = append(ta.Bindings, b)
			}
		}
		for _, ns := range namespaces {
			if ns.TenantID != t.ID {
				continue
			}
			if na, ok := namespaceAccess(p, ns, sites); ok {
				ta.Namespaces = append(ta.Namespaces, na)
			}
		}
		me.Tenants = append(me.Tenants, ta)
	}
	return me, nil
}

// siteRef is a site referenced by a site-restricted binding.
type siteRef struct {
	id          string
	name        string
	namespaceID string
}

// restrictedSites loads the sites named by the principal's site-restricted bindings.
func (u *Users) restrictedSites(ctx context.Context, p *authz.Principal) ([]siteRef, error) {
	var ids []string
	for _, b := range p.Bindings {
		ids = append(ids, b.SiteIDs...)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := u.q.AuthSitesByIDs(ctx, sortedUnique(ids))
	if err != nil {
		return nil, err
	}
	out := make([]siteRef, len(rows))
	for i, r := range rows {
		out[i] = siteRef{id: r.ID, name: r.Name, namespaceID: r.NamespaceID}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// namespaceAccess computes the effective permissions of p in one namespace;
// ok is false when the namespace is not readable.
func namespaceAccess(p *authz.Principal, ns authdb.Namespace, sites []siteRef) (NamespaceAccess, bool) {
	res := authz.Resource{TenantID: ns.TenantID, NamespaceID: ns.ID, NamespaceName: ns.Name}
	if !p.Can(authz.PermNamespaceRead, res) {
		return NamespaceAccess{}, false
	}
	all := authz.AllPermissions()
	na := NamespaceAccess{Namespace: namespaceViewFromModel(ns), Permissions: permissionsOn(p, res, all), Sites: []SiteAccess{}}
	if len(na.Permissions) == len(all) {
		return na, true
	}
	for _, s := range sites {
		if s.namespaceID != ns.ID {
			continue
		}
		sres := res
		sres.SiteID, sres.SiteName = s.id, s.name
		perms := permissionsOn(p, sres, all)
		if slices.ContainsFunc(perms, func(perm string) bool { return !slices.Contains(na.Permissions, perm) }) {
			na.Sites = append(na.Sites, SiteAccess{SiteID: s.id, SiteName: s.name, Permissions: perms})
		}
	}
	return na, true
}

// permissionsOn lists, in canonical order, every permission p holds on r.
func permissionsOn(p *authz.Principal, r authz.Resource, all []authz.Permission) []string {
	out := make([]string, 0, len(all))
	for _, perm := range all {
		if p.Can(perm, r) {
			out = append(out, string(perm))
		}
	}
	return out
}
