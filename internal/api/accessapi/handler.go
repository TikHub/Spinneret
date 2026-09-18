// Package accessapi implements the Connect AccessAdminService: API tokens,
// tenant members, role bindings and the audit log of the active tenant.
package accessapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// Handler implements spinneretv1connect.AccessAdminServiceHandler.
type Handler struct {
	tokens *auth.Tokens
	users  *auth.Users
	audit  *auth.AuditLogs
	cat    catalog.Catalog
	logger *slog.Logger
}

var _ spinneretv1connect.AccessAdminServiceHandler = (*Handler)(nil)

// New creates the AccessAdminService handler.
func New(tokens *auth.Tokens, users *auth.Users, auditLogs *auth.AuditLogs, cat catalog.Catalog, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{tokens: tokens, users: users, audit: auditLogs, cat: cat, logger: logger.With(slog.String("component", "accessapi"))}
}

// namespaceRef resolves a namespace name for the caller. An empty name yields
// nil for users (meaning "every namespace") and the token's own namespace for
// API tokens.
func (h *Handler) namespaceRef(ctx context.Context, p *authz.Principal, name string) (*auth.NamespaceRef, error) {
	if name == "" && p.Kind != authz.KindToken {
		return nil, nil
	}
	_, ns, err := apiutil.Namespace(ctx, h.cat, name)
	if err != nil {
		return nil, err
	}
	return &auth.NamespaceRef{ID: ns.ID, TenantID: ns.TenantID, Name: ns.Name}, nil
}

// requireTenant checks perm on the caller's active tenant before any name is
// resolved, so unauthorized callers cannot probe for namespaces or sites.
func requireTenant(p *authz.Principal, perm authz.Permission) error {
	if p.TenantID == "" {
		return apperr.InvalidArgument("", "active tenant is required (%s header)", auth.HeaderTenant)
	}
	return p.Require(perm, authz.Resource{TenantID: p.TenantID})
}

// ListTokens lists API tokens (token:read).
func (h *Handler) ListTokens(ctx context.Context, req *connect.Request[spinneretv1.ListTokensRequest]) (*connect.Response[spinneretv1.ListTokensResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := h.namespaceRef(ctx, p, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	tokens, next, total, err := h.tokens.List(ctx, p, auth.ListTokensInput{
		Namespace: ns, IncludeRevoked: req.Msg.GetIncludeRevoked(), PageSize: req.Msg.GetPageSize(), PageToken: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*spinneretv1.ApiToken, len(tokens))
	for i, t := range tokens {
		out[i] = tokenProto(t)
	}
	return connect.NewResponse(&spinneretv1.ListTokensResponse{Tokens: out, NextPageToken: next, Total: total}), nil
}

// CreateToken creates an API token (token:write); the plaintext is returned once.
func (h *Handler) CreateToken(ctx context.Context, req *connect.Request[spinneretv1.CreateTokenRequest]) (*connect.Response[spinneretv1.CreateTokenResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetNamespace() == "" {
		return nil, apperr.InvalidArgument("", "namespace is required")
	}
	ns, err := h.namespaceRef(ctx, p, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	view, plaintext, err := h.tokens.Create(ctx, p, *ns, auth.CreateTokenInput{
		Name: req.Msg.GetName(), Description: req.Msg.GetDescription(), Scopes: req.Msg.GetScopes(),
		IPAllowlist: req.Msg.GetIpAllowlist(), RateLimitRPS: req.Msg.GetRateLimitRps(), ExpiresAt: apiutil.Time(req.Msg.GetExpiresAt()),
	})
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&spinneretv1.CreateTokenResponse{Token: tokenProto(*view), Plaintext: plaintext})
	resp.Header().Set("Cache-Control", "no-store")
	return resp, nil
}

// RevokeToken revokes an API token (token:write).
func (h *Handler) RevokeToken(ctx context.Context, req *connect.Request[spinneretv1.RevokeTokenRequest]) (*connect.Response[spinneretv1.RevokeTokenResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	view, err := h.tokens.Revoke(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RevokeTokenResponse{Token: tokenProto(*view)}), nil
}

// ListAuditLogs queries the audit log, newest first (audit:read).
func (h *Handler) ListAuditLogs(ctx context.Context, req *connect.Request[spinneretv1.ListAuditLogsRequest]) (*connect.Response[spinneretv1.ListAuditLogsResponse], error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	ns, err := h.namespaceRef(ctx, p, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	logs, next, err := h.audit.List(ctx, p, auth.AuditQuery{
		Namespace: ns, Actor: msg.GetActor(), Action: msg.GetAction(), ResourceKind: msg.GetResourceKind(),
		ResourceID: msg.GetResourceId(), Result: msg.GetResult(),
		Start: apiutil.Time(msg.GetTimeRange().GetStart()), End: apiutil.Time(msg.GetTimeRange().GetEnd()),
		PageSize: msg.GetPageSize(), PageToken: msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*spinneretv1.AuditLog, len(logs))
	for i, l := range logs {
		details, err := apiutil.Struct(l.Details)
		if err != nil {
			return nil, err
		}
		out[i] = &spinneretv1.AuditLog{
			Id: l.ID, CreatedAt: apiutil.Timestamp(l.CreatedAt), Namespace: l.Namespace, ActorKind: l.ActorKind,
			ActorId: l.ActorID, ActorName: l.ActorName, Action: l.Action, ResourceKind: l.ResourceKind,
			ResourceId: l.ResourceID, ResourceName: l.ResourceName, Result: l.Result, Ip: l.IP, UserAgent: l.UserAgent,
			Details: details,
		}
	}
	return connect.NewResponse(&spinneretv1.ListAuditLogsResponse{Logs: out, NextPageToken: next}), nil
}

func tokenProto(t auth.TokenView) *spinneretv1.ApiToken {
	return &spinneretv1.ApiToken{
		Id: t.ID, Namespace: t.Namespace, Name: t.Name, Description: t.Description, TokenPrefix: t.TokenPrefix,
		Scopes: t.Scopes, IpAllowlist: t.IPAllowlist, RateLimitRps: t.RateLimitRPS, ExpiresAt: apiutil.TimestampPtr(t.ExpiresAt),
		RevokedAt: apiutil.TimestampPtr(t.RevokedAt), LastUsedAt: apiutil.TimestampPtr(t.LastUsedAt), LastUsedIp: t.LastUsedIP,
		CreatedBy: t.CreatedBy, CreatedAt: apiutil.Timestamp(t.CreatedAt),
	}
}
