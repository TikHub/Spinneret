package accessapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/api/authapi"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// ListUsers lists the members of the active tenant (user:read).
func (h *Handler) ListUsers(ctx context.Context, req *connect.Request[spinneretv1.ListUsersRequest]) (*connect.Response[spinneretv1.ListUsersResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	users, next, total, err := h.users.ListUsers(ctx, p, auth.ListUsersInput{
		Query: req.Msg.GetQuery(), AllUsers: req.Msg.GetAllUsers(), PageSize: req.Msg.GetPageSize(), PageToken: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*spinneretv1.TenantUser, len(users))
	for i, u := range users {
		out[i] = &spinneretv1.TenantUser{User: authapi.UserProto(u.User), Bindings: authapi.RoleBindingProtos(u.Bindings)}
	}
	return connect.NewResponse(&spinneretv1.ListUsersResponse{Users: out, NextPageToken: next, Total: total}), nil
}

// CreateUser creates a user and its initial binding in the active tenant (user:write).
func (h *Handler) CreateUser(ctx context.Context, req *connect.Request[spinneretv1.CreateUserRequest]) (*connect.Response[spinneretv1.CreateUserResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	spec, err := h.bindingSpec(ctx, p, msg.GetRole(), msg.GetNamespace(), msg.GetSites(), msg.GetExtraPermissions())
	if err != nil {
		return nil, err
	}
	res, err := h.users.CreateUser(ctx, p, auth.CreateUserInput{
		Username: msg.GetUsername(), DisplayName: msg.GetDisplayName(), Email: msg.GetEmail(), Password: msg.GetPassword(), Binding: spec,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateUserResponse{
		User: authapi.UserProto(res.User), Binding: authapi.RoleBindingProto(res.Binding),
	}), nil
}

// UpdateUser changes a user's profile or disabled flag (user:write).
func (h *Handler) UpdateUser(ctx context.Context, req *connect.Request[spinneretv1.UpdateUserRequest]) (*connect.Response[spinneretv1.UpdateUserResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	user, err := h.users.UpdateUser(ctx, p, msg.GetId(), auth.UpdateUserInput{
		DisplayName: msg.DisplayName, Email: msg.Email, Locale: msg.Locale, Disabled: msg.Disabled,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateUserResponse{User: authapi.UserProto(*user)}), nil
}

// ResetPassword sets a user's password and ends its sessions (user:write).
func (h *Handler) ResetPassword(ctx context.Context, req *connect.Request[spinneretv1.ResetPasswordRequest]) (*connect.Response[spinneretv1.ResetPasswordResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.users.ResetPassword(ctx, p, req.Msg.GetUserId(), req.Msg.GetNewPassword()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.ResetPasswordResponse{}), nil
}

// ListRoleBindings lists role bindings of the active tenant (user:read).
func (h *Handler) ListRoleBindings(ctx context.Context, req *connect.Request[spinneretv1.ListRoleBindingsRequest]) (*connect.Response[spinneretv1.ListRoleBindingsResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	bindings, next, total, err := h.users.ListRoleBindings(ctx, p, req.Msg.GetUserId(), req.Msg.GetPageSize(), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.ListRoleBindingsResponse{
		Bindings: authapi.RoleBindingProtos(bindings), NextPageToken: next, Total: total,
	}), nil
}

// CreateRoleBinding grants a role in the active tenant (user:write).
func (h *Handler) CreateRoleBinding(ctx context.Context, req *connect.Request[spinneretv1.CreateRoleBindingRequest]) (*connect.Response[spinneretv1.CreateRoleBindingResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	spec, err := h.bindingSpec(ctx, p, msg.GetRole(), msg.GetNamespace(), msg.GetSites(), msg.GetExtraPermissions())
	if err != nil {
		return nil, err
	}
	binding, err := h.users.CreateRoleBinding(ctx, p, msg.GetUserId(), spec)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateRoleBindingResponse{Binding: authapi.RoleBindingProto(*binding)}), nil
}

// DeleteRoleBinding removes a role binding (user:write).
func (h *Handler) DeleteRoleBinding(ctx context.Context, req *connect.Request[spinneretv1.DeleteRoleBindingRequest]) (*connect.Response[spinneretv1.DeleteRoleBindingResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.users.DeleteRoleBinding(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteRoleBindingResponse{}), nil
}

// bindingSpec checks user:write on the active tenant and resolves namespace
// and site names in the catalog.
func (h *Handler) bindingSpec(ctx context.Context, p *authz.Principal, role, namespace string, sites, extra []string) (auth.BindingSpec, error) {
	if err := requireTenant(p, authz.PermUserWrite); err != nil {
		return auth.BindingSpec{}, err
	}
	spec := auth.BindingSpec{Role: role, ExtraPermissions: extra}
	if len(sites) > 0 && namespace == "" {
		return auth.BindingSpec{}, apperr.InvalidArgument("", "sites require a namespace")
	}
	if namespace == "" {
		return spec, nil
	}
	_, ns, err := apiutil.Namespace(ctx, h.cat, namespace)
	if err != nil {
		return auth.BindingSpec{}, err
	}
	spec.NamespaceID = ns.ID
	spec.SiteIDs = make([]string, 0, len(sites))
	for _, name := range sites {
		site, err := apiutil.Site(ns, name)
		if err != nil {
			return auth.BindingSpec{}, err
		}
		spec.SiteIDs = append(spec.SiteIDs, site.ID)
	}
	return spec, nil
}
