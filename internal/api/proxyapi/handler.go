// Package proxyapi implements the Connect ProxyAdminService on top of
// internal/proxy. Responses never carry proxy credentials: only the display
// URL and the user name hint are returned.
package proxyapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/proxy"
)

// Service is the proxy domain service used by the handler (*proxy.Service).
type Service interface {
	ListProxies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f proxy.ListFilter) (proxy.ListResult, error)
	GetProxy(ctx context.Context, p *authz.Principal, id string) (*proxy.Proxy, error)
	ImportProxies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req proxy.ImportRequest) (proxy.ImportResult, error)
	UpdateProxy(ctx context.Context, p *authz.Principal, id string, req proxy.UpdateRequest) (*proxy.Proxy, error)
	OperateProxies(ctx context.Context, p *authz.Principal, ids []string, req proxy.OperationRequest) (proxy.BulkResult, error)
	DeleteProxies(ctx context.Context, p *authz.Principal, ids []string) (proxy.BulkResult, error)
	GetProviderStats(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req proxy.StatsRequest) ([]proxy.ProviderStats, error)
}

// Checker runs manual health checks (*proxy.HealthChecker).
type Checker interface {
	CheckProxy(ctx context.Context, p *authz.Principal, id string) (proxy.CheckResult, error)
}

// Handler implements spinneretv1connect.ProxyAdminServiceHandler. Permission
// checks happen here for namespace-scoped RPCs and inside the service for
// RPCs addressing proxies by ID (their namespace is only known after loading).
type Handler struct {
	cat     catalog.Catalog
	svc     Service
	checker Checker
	audit   audit.Recorder
}

var _ spinneretv1connect.ProxyAdminServiceHandler = (*Handler)(nil)

// New creates the handler. rec records manual health checks (other mutations
// are audited by the service); nil discards entries.
func New(cat catalog.Catalog, svc Service, checker Checker, rec audit.Recorder) *Handler {
	if rec == nil {
		rec = audit.Nop{}
	}
	return &Handler{cat: cat, svc: svc, checker: checker, audit: rec}
}

// namespace resolves the request namespace and checks perm on it.
func (h *Handler) namespace(ctx context.Context, name string, perm authz.Permission) (*authz.Principal, *catalog.Namespace, error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, name)
	if err != nil {
		return nil, nil, err
	}
	if err := p.Require(perm, apiutil.NamespaceResource(ns)); err != nil {
		return nil, nil, err
	}
	return p, ns, nil
}

// ListProxies implements ProxyAdminServiceHandler (proxy:read).
func (h *Handler) ListProxies(ctx context.Context, req *connect.Request[spinneretv1.ListProxiesRequest]) (*connect.Response[spinneretv1.ListProxiesResponse], error) {
	m := req.Msg
	p, ns, err := h.namespace(ctx, m.GetNamespace(), authz.PermProxyRead)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.ListProxies(ctx, p, ns, proxy.ListFilter{
		States: m.GetStates(), Kinds: m.GetKinds(), Providers: m.GetProviders(), Regions: m.GetRegions(),
		Tags: m.GetTags(), Search: m.GetSearch(), PageSize: apiutil.PageSize(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListProxiesResponse{
		Proxies:       make([]*spinneretv1.Proxy, len(res.Proxies)),
		NextPageToken: res.NextPageToken,
		Total:         int32(res.Total),
	}
	for i, v := range res.Proxies {
		out.Proxies[i] = toProto(v)
	}
	return connect.NewResponse(out), nil
}

// GetProxy implements ProxyAdminServiceHandler (proxy:read).
func (h *Handler) GetProxy(ctx context.Context, req *connect.Request[spinneretv1.GetProxyRequest]) (*connect.Response[spinneretv1.GetProxyResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.GetProxy(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetProxyResponse{Proxy: toProto(v)}), nil
}

// ImportProxies implements ProxyAdminServiceHandler (proxy:write).
func (h *Handler) ImportProxies(ctx context.Context, req *connect.Request[spinneretv1.ImportProxiesRequest]) (*connect.Response[spinneretv1.ImportProxiesResponse], error) {
	m := req.Msg
	p, ns, err := h.namespace(ctx, m.GetNamespace(), authz.PermProxyWrite)
	if err != nil {
		return nil, err
	}
	d := m.GetDefaults()
	res, err := h.svc.ImportProxies(ctx, p, ns, proxy.ImportRequest{
		Format: m.GetFormat(),
		Data:   m.GetData(),
		DryRun: m.GetDryRun(),
		Defaults: proxy.Defaults{
			Kind: d.GetKind(), Region: d.GetRegion(), City: d.GetCity(), Provider: d.GetProvider(),
			Tags: d.GetTags(), MaxConcurrency: int(d.GetMaxConcurrency()), SessionTemplate: d.GetSessionTemplate(),
		},
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ImportProxiesResponse{
		Created:   int32(res.Created),
		Updated:   int32(res.Updated),
		Unchanged: int32(res.Unchanged),
		Failed:    make([]*spinneretv1.ProxyImportFailure, len(res.Failed)),
	}
	for i, f := range res.Failed {
		out.Failed[i] = &spinneretv1.ProxyImportFailure{Line: int32(f.Line), Message: f.Message}
	}
	return connect.NewResponse(out), nil
}

// UpdateProxy implements ProxyAdminServiceHandler (proxy:write).
func (h *Handler) UpdateProxy(ctx context.Context, req *connect.Request[spinneretv1.UpdateProxyRequest]) (*connect.Response[spinneretv1.UpdateProxyResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	upd := proxy.UpdateRequest{
		URL: m.Url, Kind: m.Kind, Region: m.Region, City: m.City, Provider: m.Provider,
		SessionTemplate: m.SessionTemplate, SetTags: m.GetSetTags(),
	}
	if m.MaxConcurrency != nil {
		n := int(m.GetMaxConcurrency())
		upd.MaxConcurrency = &n
	}
	if upd.SetTags {
		upd.Tags = m.GetTags()
	}
	v, err := h.svc.UpdateProxy(ctx, p, m.GetId(), upd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateProxyResponse{Proxy: toProto(v)}), nil
}

// OperateProxies implements ProxyAdminServiceHandler (proxy:operate).
func (h *Handler) OperateProxies(ctx context.Context, req *connect.Request[spinneretv1.OperateProxiesRequest]) (*connect.Response[spinneretv1.OperateProxiesResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	var d durationx.Duration
	if m.GetDuration() != "" {
		if d, err = apiutil.Duration("duration", m.GetDuration(), m.GetOperation() == proxy.OpBan); err != nil {
			return nil, err
		}
	}
	res, err := h.svc.OperateProxies(ctx, p, m.GetIds(), proxy.OperationRequest{
		Operation: m.GetOperation(), Site: m.GetSite(), Duration: d, Reason: m.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.OperateProxiesResponse{Result: bulkToProto(res)}), nil
}

// DeleteProxies implements ProxyAdminServiceHandler (proxy:write).
func (h *Handler) DeleteProxies(ctx context.Context, req *connect.Request[spinneretv1.DeleteProxiesRequest]) (*connect.Response[spinneretv1.DeleteProxiesResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.DeleteProxies(ctx, p, req.Msg.GetIds())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteProxiesResponse{Result: bulkToProto(res)}), nil
}

// CheckProxy implements ProxyAdminServiceHandler (proxy:operate).
func (h *Handler) CheckProxy(ctx context.Context, req *connect.Request[spinneretv1.CheckProxyRequest]) (*connect.Response[spinneretv1.CheckProxyResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if h.checker == nil {
		return nil, apperr.FailedPrecondition(apperr.ReasonFailedPrecondition, "proxy health checks are not configured")
	}
	id := req.Msg.GetId()
	res, err := h.checker.CheckProxy(ctx, p, id)
	if err != nil {
		return nil, err
	}
	h.audit.Record(ctx, audit.FromPrincipal(p, res.TenantID, res.NamespaceID, "proxy.check", "proxy", id, "", audit.ResultOK,
		map[string]any{"ok": res.OK, "latency_ms": res.LatencyMs}))
	return connect.NewResponse(&spinneretv1.CheckProxyResponse{
		Ok: res.OK, LatencyMs: int32(res.LatencyMs), ExitIp: res.ExitIP, Region: res.Region, Error: res.Error,
	}), nil
}

// GetProviderStats implements ProxyAdminServiceHandler (proxy:read).
func (h *Handler) GetProviderStats(ctx context.Context, req *connect.Request[spinneretv1.GetProviderStatsRequest]) (*connect.Response[spinneretv1.GetProviderStatsResponse], error) {
	m := req.Msg
	p, ns, err := h.namespace(ctx, m.GetNamespace(), authz.PermProxyRead)
	if err != nil {
		return nil, err
	}
	stats, err := h.svc.GetProviderStats(ctx, p, ns, proxy.StatsRequest{
		Site:  m.GetSite(),
		Start: apiutil.Time(m.GetTimeRange().GetStart()),
		End:   apiutil.Time(m.GetTimeRange().GetEnd()),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.GetProviderStatsResponse{Providers: make([]*spinneretv1.ProviderStats, len(stats))}
	for i, st := range stats {
		out.Providers[i] = &spinneretv1.ProviderStats{
			Provider: st.Provider, Proxies: int32(st.Proxies), Active: int32(st.Active), Dead: int32(st.Dead),
			Requests: st.Requests, SuccessRatio: st.SuccessRatio, RiskRatio: st.RiskRatio, AvgLatencyMs: st.AvgLatencyMs,
		}
	}
	return connect.NewResponse(out), nil
}
