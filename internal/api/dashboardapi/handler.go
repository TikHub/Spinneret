// Package dashboardapi implements the DashboardService Connect handler on top
// of internal/analytics.
//
// Permissions: every RPC requires dashboard:read in the namespace. Callers
// whose grants are restricted to sites only see those sites: overview, time
// series, heatmap, risk events and request events are limited to them, and
// naming another site or one of its endpoint groups is denied. Node
// statistics are not site-scoped and therefore require dashboard:read without
// site restriction. All RPCs are read-only, so nothing is audited.
package dashboardapi

import (
	"context"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/analytics"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// Handler implements spinneretv1connect.DashboardServiceHandler.
type Handler struct {
	svc *analytics.Service
	cat catalog.Catalog
}

var _ spinneretv1connect.DashboardServiceHandler = (*Handler)(nil)

// New creates the handler.
func New(svc *analytics.Service, cat catalog.Catalog) *Handler {
	return &Handler{svc: svc, cat: cat}
}

// access is the resolved caller, namespace and readable scope of a request.
type access struct {
	p     *authz.Principal
	ns    *catalog.Namespace
	scope analytics.Scope
}

// resolve authenticates the caller, resolves the namespace and determines the
// sites readable with dashboard:read. It fails with a permission error when
// the caller can read no site and has no namespace-wide grant.
func (h *Handler) resolve(ctx context.Context, namespace string) (access, error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, namespace)
	if err != nil {
		return access{}, err
	}
	a := access{p: p, ns: ns, scope: analytics.Scope{NamespaceID: ns.ID}}
	if all, _ := p.SiteFilter(ns.TenantID, ns.ID, authz.PermDashboardRead); all {
		a.scope.AllSites = true
		return a, nil
	}
	// Site grants are checked site by site: this covers user bindings and
	// token scopes alike and ignores site IDs of other namespaces.
	for _, site := range ns.SitesByID {
		if p.Can(authz.PermDashboardRead, apiutil.SiteResource(ns, site)) {
			a.scope.SiteIDs = append(a.scope.SiteIDs, site.ID)
		}
	}
	if len(a.scope.SiteIDs) == 0 {
		if err := p.Require(authz.PermDashboardRead, apiutil.NamespaceResource(ns)); err != nil {
			return access{}, err
		}
		a.scope.AllSites = true
	}
	sort.Strings(a.scope.SiteIDs)
	return a, nil
}

// site resolves a site by name and requires dashboard:read on it.
func (a access) site(name string) (*catalog.Site, error) {
	site, err := apiutil.Site(a.ns, name)
	if err != nil {
		return nil, err
	}
	if err := a.p.Require(authz.PermDashboardRead, apiutil.SiteResource(a.ns, site)); err != nil {
		return nil, err
	}
	return site, nil
}

// optionalSite resolves an optional site name ("" yields nil).
func (a access) optionalSite(name string) (*catalog.Site, error) {
	if name == "" {
		return nil, nil
	}
	return a.site(name)
}

// requireGroup requires dashboard:read on the site of an endpoint group.
// Unknown groups are left to the service, which reports them as unknown.
func (a access) requireGroup(groupID string) error {
	if groupID == "" {
		return nil
	}
	for _, site := range a.ns.SitesByID {
		if _, ok := site.GroupsByID[groupID]; ok {
			return a.p.Require(authz.PermDashboardRead, apiutil.SiteResource(a.ns, site))
		}
	}
	return nil
}

// requireNamespace requires dashboard:read without site restriction.
func (a access) requireNamespace() error {
	return a.p.Require(authz.PermDashboardRead, apiutil.NamespaceResource(a.ns))
}

// siteID returns the ID of an optional site.
func siteID(site *catalog.Site) string {
	if site == nil {
		return ""
	}
	return site.ID
}

// timeRange converts a protobuf time range. Timestamps outside the range a
// google.protobuf.Timestamp may represent (years 1..9999, normalized nanos)
// are rejected so they never reach the databases.
func timeRange(r *spinneretv1.TimeRange) (analytics.TimeRange, error) {
	if r == nil {
		return analytics.TimeRange{}, nil
	}
	for _, f := range []struct {
		name string
		ts   *timestamppb.Timestamp
	}{{"time_range.start", r.GetStart()}, {"time_range.end", r.GetEnd()}} {
		if f.ts != nil && f.ts.CheckValid() != nil {
			return analytics.TimeRange{}, apperr.InvalidArgument("", "%s is not a valid timestamp", f.name)
		}
	}
	return analytics.TimeRange{Start: apiutil.Time(r.GetStart()), End: apiutil.Time(r.GetEnd())}, nil
}

// clampInt32 converts a count to the int32 used by the API, saturating.
func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}

// counts converts a count map.
func counts(m map[string]int64) map[string]int32 {
	out := make(map[string]int32, len(m))
	for k, v := range m {
		out[k] = clampInt32(v)
	}
	return out
}

// eventTimestamp converts a ClickHouse timestamp; the zero time and the Unix
// epoch (stored for missing values) map to nil.
func eventTimestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() || t.UnixNano() == 0 {
		return nil
	}
	return timestamppb.New(t)
}

// encodeCursor serializes a page cursor, mapping failures to internal errors.
func encodeCursor(v any) (string, error) {
	token, err := apiutil.EncodeCursor(v)
	if err != nil {
		return "", apperr.Internal(err)
	}
	return token, nil
}
