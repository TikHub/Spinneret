// Package siteapi implements the SiteAdminService Connect handler: sites,
// endpoint groups, URI rules and the URI tester.
//
// Permissions: reads need site:read on the site (site listings are filtered to
// the sites the caller can read); creating, updating and deleting sites need
// site:write at namespace level (a binding without site restriction);
// endpoint group and URI rule mutations need site:write on their site.
// Sites and groups of other tenants (or, for tokens, other namespaces) are
// reported as not found; a missing site name is reported like an inaccessible
// site to principals restricted to some sites, so names cannot be probed.
package siteapi

import (
	"context"
	"math"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/sitesvc"
)

// Handler implements spinneretv1connect.SiteAdminServiceHandler.
type Handler struct {
	svc   *sitesvc.Service
	cat   catalog.Catalog
	audit audit.Recorder
}

var _ spinneretv1connect.SiteAdminServiceHandler = (*Handler)(nil)

// New creates the handler. rec records denied mutation attempts (successful
// mutations are audited by the service); nil discards them.
func New(svc *sitesvc.Service, cat catalog.Catalog, rec audit.Recorder) *Handler {
	if rec == nil {
		rec = audit.Nop{}
	}
	return &Handler{svc: svc, cat: cat, audit: rec}
}

// siteCursor is the page token of ListSites.
type siteCursor struct {
	After string `json:"a"`
}

// ListSites implements SiteAdminServiceHandler.
func (h *Handler) ListSites(ctx context.Context, req *connect.Request[spinneretv1.ListSitesRequest]) (*connect.Response[spinneretv1.ListSitesResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	access, err := siteAccess(p, ns)
	if err != nil {
		return nil, err
	}
	var cur siteCursor
	if _, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	page, err := h.svc.ListSites(ctx, ns.ID, access, apiutil.PageSize(req.Msg.GetPageSize()), cur.After)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListSitesResponse{Sites: make([]*spinneretv1.Site, 0, len(page.Sites)), Total: clampInt32(int64(page.Total))}
	for _, s := range page.Sites {
		resp.Sites = append(resp.Sites, toProtoSite(s))
	}
	if page.NextAfterName != "" {
		if resp.NextPageToken, err = apiutil.EncodeCursor(siteCursor{After: page.NextAfterName}); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	return connect.NewResponse(resp), nil
}

// siteAccess determines which sites of ns the principal may read.
func siteAccess(p *authz.Principal, ns *catalog.Namespace) (sitesvc.SiteAccess, error) {
	all, ids := p.SiteFilter(ns.TenantID, ns.ID, authz.PermSiteRead)
	if all {
		return sitesvc.SiteAccess{All: true}, nil
	}
	if len(ids) == 0 {
		// Principals whose grants name sites rather than IDs (and every other
		// principal, as a safe fallback) are checked site by site.
		for _, s := range ns.SitesByID {
			if p.Can(authz.PermSiteRead, apiutil.SiteResource(ns, s)) {
				ids = append(ids, s.ID)
			}
		}
	}
	if len(ids) == 0 {
		if err := p.Require(authz.PermSiteRead, apiutil.NamespaceResource(ns)); err != nil {
			return sitesvc.SiteAccess{}, err
		}
		return sitesvc.SiteAccess{All: true}, nil
	}
	return sitesvc.SiteAccess{SiteIDs: ids}, nil
}

// GetSite implements SiteAdminServiceHandler.
func (h *Handler) GetSite(ctx context.Context, req *connect.Request[spinneretv1.GetSiteRequest]) (*connect.Response[spinneretv1.GetSiteResponse], error) {
	p, ref, err := h.resolveSite(ctx, authz.PermSiteRead, req.Msg.GetId(), req.Msg.GetNamespace(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteRead, siteResource(ref)); err != nil {
		return nil, err
	}
	s, err := h.svc.GetSite(ctx, ref.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetSiteResponse{Site: toProtoSite(s)}), nil
}

// CreateSite implements SiteAdminServiceHandler.
func (h *Handler) CreateSite(ctx context.Context, req *connect.Request[spinneretv1.CreateSiteRequest]) (*connect.Response[spinneretv1.CreateSiteResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, apiutil.NamespaceResource(ns)); err != nil {
		h.denied(ctx, p, ns.TenantID, ns.ID, sitesvc.ActionSiteCreate, sitesvc.ResourceSite, "", req.Msg.GetName())
		return nil, err
	}
	s, err := h.svc.CreateSite(ctx, p, ns, sitesvc.CreateSiteInput{
		Name:        req.Msg.GetName(),
		DisplayName: req.Msg.GetDisplayName(),
		Description: req.Msg.GetDescription(),
		Clients:     req.Msg.GetClients(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateSiteResponse{Site: toProtoSite(s)}), nil
}

// UpdateSite implements SiteAdminServiceHandler.
func (h *Handler) UpdateSite(ctx context.Context, req *connect.Request[spinneretv1.UpdateSiteRequest]) (*connect.Response[spinneretv1.UpdateSiteResponse], error) {
	p, ref, err := h.resolveSite(ctx, authz.PermSiteWrite, req.Msg.GetId(), "", "")
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, namespaceResource(ref)); err != nil {
		h.denied(ctx, p, ref.TenantID, ref.NamespaceID, sitesvc.ActionSiteUpdate, sitesvc.ResourceSite, ref.ID, ref.Name)
		return nil, err
	}
	s, err := h.svc.UpdateSite(ctx, p, ref.ID, sitesvc.UpdateSiteInput{
		DisplayName: req.Msg.DisplayName,
		Description: req.Msg.Description,
		Clients:     req.Msg.GetClients(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateSiteResponse{Site: toProtoSite(s)}), nil
}

// DeleteSite implements SiteAdminServiceHandler.
func (h *Handler) DeleteSite(ctx context.Context, req *connect.Request[spinneretv1.DeleteSiteRequest]) (*connect.Response[spinneretv1.DeleteSiteResponse], error) {
	p, ref, err := h.resolveSite(ctx, authz.PermSiteWrite, req.Msg.GetId(), "", "")
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, namespaceResource(ref)); err != nil {
		h.denied(ctx, p, ref.TenantID, ref.NamespaceID, sitesvc.ActionSiteDelete, sitesvc.ResourceSite, ref.ID, ref.Name)
		return nil, err
	}
	if err := h.svc.DeleteSite(ctx, p, ref.ID, req.Msg.GetForce()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteSiteResponse{}), nil
}

// TestURI implements SiteAdminServiceHandler. It resolves the URI against the
// catalog snapshot, exactly like Acquire does.
func (h *Handler) TestURI(ctx context.Context, req *connect.Request[spinneretv1.TestURIRequest]) (*connect.Response[spinneretv1.TestURIResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	st, err := apiutil.Site(ns, req.Msg.GetSite())
	if err != nil {
		return nil, concealMissingSite(p, ns, authz.PermSiteRead, err)
	}
	if err := p.Require(authz.PermSiteRead, apiutil.SiteResource(ns, st)); err != nil {
		return nil, err
	}
	res, err := h.svc.TestURI(st, req.Msg.GetClient(), req.Msg.GetUri())
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.TestURIResponse{
		EndpointGroup:   res.GroupName,
		EndpointGroupId: res.GroupID,
		RuleId:          res.RuleID,
		Kind:            string(res.Kind),
		IsDefault:       res.Default,
		Policies:        make([]*spinneretv1.ResolvedPolicyRef, 0, len(res.Policies)),
	}
	for _, rp := range res.Policies {
		resp.Policies = append(resp.Policies, &spinneretv1.ResolvedPolicyRef{
			Kind:     string(rp.Kind),
			PolicyId: rp.Ref.PolicyID,
			Name:     rp.Ref.Name,
			Version:  clampInt32(int64(rp.Ref.Version)),
			Level:    string(rp.Ref.Level),
		})
	}
	return connect.NewResponse(resp), nil
}

// resolveSite loads a site reference by ID, or by namespace and name, and
// hides sites outside the principal's tenant (and, for tokens, namespace).
// perm is the permission the caller is about to check; a site name that does
// not exist is reported like an inaccessible site to principals that do not
// hold perm on every site of the namespace (see concealMissingSite).
func (h *Handler) resolveSite(ctx context.Context, perm authz.Permission, id, namespace, name string) (*authz.Principal, sitesvc.SiteRef, error) {
	if id == "" {
		p, ns, err := apiutil.Namespace(ctx, h.cat, namespace)
		if err != nil {
			return nil, sitesvc.SiteRef{}, err
		}
		if name == "" {
			return nil, sitesvc.SiteRef{}, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "site id or name is required")
		}
		ref, err := h.svc.SiteRefByName(ctx, ns.ID, name)
		if apperr.IsNotFound(err) {
			return nil, sitesvc.SiteRef{}, concealMissingSite(p, ns, perm, err)
		}
		if err != nil {
			return nil, sitesvc.SiteRef{}, err
		}
		return p, ref, nil
	}
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, sitesvc.SiteRef{}, err
	}
	if err := requireTenant(p); err != nil {
		return nil, sitesvc.SiteRef{}, err
	}
	ref, err := h.svc.SiteRef(ctx, id)
	if err != nil {
		return nil, sitesvc.SiteRef{}, err
	}
	if !visible(p, ref) {
		return nil, sitesvc.SiteRef{}, apperr.NotFound("site not found")
	}
	return p, ref, nil
}

// resolveGroup loads an endpoint group reference by ID with the same
// visibility rules as resolveSite.
func (h *Handler) resolveGroup(ctx context.Context, id string) (*authz.Principal, sitesvc.GroupRef, error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, sitesvc.GroupRef{}, err
	}
	if err := requireTenant(p); err != nil {
		return nil, sitesvc.GroupRef{}, err
	}
	ref, err := h.svc.GroupRef(ctx, id)
	if err != nil {
		return nil, sitesvc.GroupRef{}, err
	}
	if !visible(p, ref.Site) {
		return nil, sitesvc.GroupRef{}, apperr.NotFound("endpoint group not found")
	}
	return p, ref, nil
}

// concealMissingSite returns err (a missing-site error) unchanged for
// principals that hold perm on every site of ns. Everyone else gets the same
// permission error as for an existing site outside their grants, so that
// principals restricted to some sites cannot probe which site names exist.
func concealMissingSite(p *authz.Principal, ns *catalog.Namespace, perm authz.Permission, err error) error {
	if all, _ := p.SiteFilter(ns.TenantID, ns.ID, perm); all {
		return err
	}
	if denied := p.Require(perm, apiutil.NamespaceResource(ns)); denied != nil {
		return denied
	}
	return err
}

// requireTenant rejects users (other than platform admins) without an active tenant.
func requireTenant(p *authz.Principal) error {
	if p.Kind == authz.KindUser && !p.IsPlatformAdmin && p.TenantID == "" {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "active tenant is required (X-Spinneret-Tenant header)")
	}
	return nil
}

// visible reports whether a site belongs to the principal's active tenant (and
// the token's namespace). Platform admins without an active tenant see every
// site; permissions are checked separately.
func visible(p *authz.Principal, ref sitesvc.SiteRef) bool {
	switch p.Kind {
	case authz.KindToken:
		return p.TenantID == ref.TenantID && p.NamespaceID == ref.NamespaceID
	case authz.KindUser:
		return p.TenantID == ref.TenantID || (p.IsPlatformAdmin && p.TenantID == "")
	case authz.KindSystem:
		return true
	default:
		return false
	}
}

func namespaceResource(ref sitesvc.SiteRef) authz.Resource {
	return authz.Resource{TenantID: ref.TenantID, NamespaceID: ref.NamespaceID, NamespaceName: ref.NamespaceName}
}

func siteResource(ref sitesvc.SiteRef) authz.Resource {
	r := namespaceResource(ref)
	r.SiteID = ref.ID
	r.SiteName = ref.Name
	return r
}

// denied records a mutation attempt rejected by authorization.
func (h *Handler) denied(ctx context.Context, p *authz.Principal, tenantID, namespaceID, action, kind, id, name string) {
	h.audit.Record(ctx, audit.FromPrincipal(p, tenantID, namespaceID, action, kind, id, name, audit.ResultDenied, nil))
}

func toProtoSite(s sitesvc.Site) *spinneretv1.Site {
	return &spinneretv1.Site{
		Id:                 s.ID,
		Namespace:          s.NamespaceName,
		Name:               s.Name,
		DisplayName:        s.DisplayName,
		Description:        s.Description,
		Clients:            s.Clients,
		Paused:             s.Paused,
		PausedReason:       s.PausedReason,
		PausedAt:           apiutil.TimestampPtr(s.PausedAt),
		EndpointGroupCount: clampInt32(int64(s.EndpointGroupCount)),
		IdentityCount:      clampInt32(int64(s.IdentityCount)),
		CreatedAt:          apiutil.Timestamp(s.CreatedAt),
		UpdatedAt:          apiutil.Timestamp(s.UpdatedAt),
	}
}

func toProtoGroup(g sitesvc.EndpointGroup) *spinneretv1.EndpointGroup {
	rules := make([]*spinneretv1.URIRule, 0, len(g.Rules))
	for _, r := range g.Rules {
		rules = append(rules, toProtoRule(r))
	}
	return &spinneretv1.EndpointGroup{
		Id:                  g.ID,
		Site:                g.Site.Name,
		SiteId:              g.Site.ID,
		Client:              g.Client,
		Name:                g.Name,
		Description:         g.Description,
		LowWatermark:        clampInt32(int64(g.LowWatermark)),
		Rules:               rules,
		AvailableIdentities: clampInt32(g.AvailableIdentities),
		BreakerState:        g.BreakerState,
		CreatedAt:           apiutil.Timestamp(g.CreatedAt),
		UpdatedAt:           apiutil.Timestamp(g.UpdatedAt),
	}
}

func toProtoRule(r sitesvc.URIRule) *spinneretv1.URIRule {
	return &spinneretv1.URIRule{Id: r.ID, Kind: string(r.Kind), Pattern: r.Pattern, Position: clampInt32(int64(r.Position))}
}

// fromProtoRules converts request rules; IDs and positions are ignored (the
// service assigns them from list order).
func fromProtoRules(in []*spinneretv1.URIRule) []sitesvc.URIRule {
	out := make([]sitesvc.URIRule, 0, len(in))
	for _, r := range in {
		out = append(out, sitesvc.URIRule{Kind: site.RuleKind(r.GetKind()), Pattern: r.GetPattern()})
	}
	return out
}

func clampInt32(n int64) int32 {
	switch {
	case n > math.MaxInt32:
		return math.MaxInt32
	case n < math.MinInt32:
		return math.MinInt32
	default:
		return int32(n)
	}
}
