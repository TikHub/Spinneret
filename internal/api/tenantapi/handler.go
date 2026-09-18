// Package tenantapi implements the Connect TenantAdminService: tenants
// (platform administrators) and the namespaces of the active tenant.
package tenantapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/tenancy"
)

// Handler implements spinneretv1connect.TenantAdminServiceHandler. Permission
// checks (tenant:manage, namespace:read, namespace:write) are enforced by the
// tenancy service for every RPC.
type Handler struct {
	svc    *tenancy.Service
	logger *slog.Logger
}

var _ spinneretv1connect.TenantAdminServiceHandler = (*Handler)(nil)

// New creates the TenantAdminService handler.
func New(svc *tenancy.Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{svc: svc, logger: logger.With(slog.String("component", "tenantapi"))}
}

// ListTenants lists the tenants visible to the caller.
func (h *Handler) ListTenants(ctx context.Context, req *connect.Request[spinneretv1.ListTenantsRequest]) (*connect.Response[spinneretv1.ListTenantsResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	tenants, next, total, err := h.svc.ListTenants(ctx, p, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	out := make([]*spinneretv1.Tenant, len(tenants))
	for i, t := range tenants {
		out[i] = tenantProto(t)
	}
	return connect.NewResponse(&spinneretv1.ListTenantsResponse{Tenants: out, NextPageToken: next, Total: total}), nil
}

// CreateTenant creates a tenant (tenant:manage).
func (h *Handler) CreateTenant(ctx context.Context, req *connect.Request[spinneretv1.CreateTenantRequest]) (*connect.Response[spinneretv1.CreateTenantResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.CreateTenant(ctx, p, tenancy.CreateTenantInput{
		Name: req.Msg.GetName(), DisplayName: req.Msg.GetDisplayName(), Description: req.Msg.GetDescription(),
		OwnerUserID: req.Msg.GetOwnerUserId(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateTenantResponse{Tenant: tenantProto(*t)}), nil
}

// UpdateTenant changes the descriptive fields of a tenant (tenant:manage).
func (h *Handler) UpdateTenant(ctx context.Context, req *connect.Request[spinneretv1.UpdateTenantRequest]) (*connect.Response[spinneretv1.UpdateTenantResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.UpdateTenant(ctx, p, req.Msg.GetId(), req.Msg.DisplayName, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateTenantResponse{Tenant: tenantProto(*t)}), nil
}

// DeleteTenant deletes an empty tenant (tenant:manage).
func (h *Handler) DeleteTenant(ctx context.Context, req *connect.Request[spinneretv1.DeleteTenantRequest]) (*connect.Response[spinneretv1.DeleteTenantResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteTenant(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteTenantResponse{}), nil
}

// ListNamespaces lists the readable namespaces of the active tenant.
func (h *Handler) ListNamespaces(ctx context.Context, req *connect.Request[spinneretv1.ListNamespacesRequest]) (*connect.Response[spinneretv1.ListNamespacesResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	nss, next, total, err := h.svc.ListNamespaces(ctx, p, req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	out := make([]*spinneretv1.Namespace, len(nss))
	for i, n := range nss {
		out[i] = namespaceProto(n)
	}
	return connect.NewResponse(&spinneretv1.ListNamespacesResponse{Namespaces: out, NextPageToken: next, Total: total}), nil
}

// CreateNamespace creates a namespace with default policies (namespace:write).
func (h *Handler) CreateNamespace(ctx context.Context, req *connect.Request[spinneretv1.CreateNamespaceRequest]) (*connect.Response[spinneretv1.CreateNamespaceResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.svc.CreateNamespace(ctx, p, tenancy.CreateNamespaceInput{
		Name: req.Msg.GetName(), DisplayName: req.Msg.GetDisplayName(), Description: req.Msg.GetDescription(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateNamespaceResponse{Namespace: namespaceProto(*ns)}), nil
}

// UpdateNamespace changes the descriptive fields of a namespace (namespace:write).
func (h *Handler) UpdateNamespace(ctx context.Context, req *connect.Request[spinneretv1.UpdateNamespaceRequest]) (*connect.Response[spinneretv1.UpdateNamespaceResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.svc.UpdateNamespace(ctx, p, req.Msg.GetId(), req.Msg.DisplayName, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateNamespaceResponse{Namespace: namespaceProto(*ns)}), nil
}

// DeleteNamespace deletes an empty namespace (namespace:write).
func (h *Handler) DeleteNamespace(ctx context.Context, req *connect.Request[spinneretv1.DeleteNamespaceRequest]) (*connect.Response[spinneretv1.DeleteNamespaceResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteNamespace(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteNamespaceResponse{}), nil
}

func tenantProto(t tenancy.Tenant) *spinneretv1.Tenant {
	return &spinneretv1.Tenant{
		Id: t.ID, Name: t.Name, DisplayName: t.DisplayName, Description: t.Description,
		CreatedAt: apiutil.Timestamp(t.CreatedAt), UpdatedAt: apiutil.Timestamp(t.UpdatedAt),
	}
}

func namespaceProto(n tenancy.Namespace) *spinneretv1.Namespace {
	return &spinneretv1.Namespace{
		Id: n.ID, TenantId: n.TenantID, Name: n.Name, DisplayName: n.DisplayName, Description: n.Description,
		CreatedAt: apiutil.Timestamp(n.CreatedAt), UpdatedAt: apiutil.Timestamp(n.UpdatedAt),
	}
}
