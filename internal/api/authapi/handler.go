// Package authapi implements the Connect AuthService: console sign-in and
// sign-out with the session cookie, the current user's access summary and
// password changes.
package authapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/authz"
)

// Handler implements spinneretv1connect.AuthServiceHandler.
type Handler struct {
	users  *auth.Users
	logger *slog.Logger
}

var _ spinneretv1connect.AuthServiceHandler = (*Handler)(nil)

// New creates the AuthService handler.
func New(users *auth.Users, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{users: users, logger: logger.With(slog.String("component", "authapi"))}
}

// Login verifies credentials, sets the session cookie and returns the user
// with its tenant access. It is the only public RPC.
func (h *Handler) Login(ctx context.Context, req *connect.Request[spinneretv1.LoginRequest]) (*connect.Response[spinneretv1.LoginResponse], error) {
	meta := h.users.RequestMeta(ctx, req.Header(), req.Peer().Addr)
	cookie, user, err := h.users.Login(ctx, req.Msg.GetUsername(), req.Msg.GetPassword(), meta.ClientIP, meta.UserAgent)
	if err != nil {
		return nil, err
	}
	me, err := h.users.MeForUser(ctx, user.ID)
	if err != nil {
		// Do not leave a session behind that the client never received.
		if lerr := h.users.Logout(context.WithoutCancel(ctx), cookie); lerr != nil {
			h.logger.Warn("discard session after failed login response", slog.Any("error", lerr))
		}
		return nil, err
	}
	resp := connect.NewResponse(&spinneretv1.LoginResponse{User: UserProto(me.User), Tenants: TenantAccessProtos(me.Tenants)})
	resp.Header().Add("Set-Cookie", h.users.SessionCookie(cookie, meta.Secure).String())
	resp.Header().Set("Cache-Control", "no-store")
	return resp, nil
}

// Logout deletes the current session and clears the cookie.
func (h *Handler) Logout(ctx context.Context, req *connect.Request[spinneretv1.LogoutRequest]) (*connect.Response[spinneretv1.LogoutResponse], error) {
	if _, err := consoleUser(ctx); err != nil {
		return nil, err
	}
	if err := h.users.Logout(ctx, auth.SessionCookieValue(req.Header())); err != nil {
		return nil, err
	}
	meta := h.users.RequestMeta(ctx, req.Header(), req.Peer().Addr)
	resp := connect.NewResponse(&spinneretv1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", h.users.ClearSessionCookie(meta.Secure).String())
	return resp, nil
}

// GetMe returns the current user and the tenants it can access.
func (h *Handler) GetMe(ctx context.Context, _ *connect.Request[spinneretv1.GetMeRequest]) (*connect.Response[spinneretv1.GetMeResponse], error) {
	p, err := consoleUser(ctx)
	if err != nil {
		return nil, err
	}
	me, err := h.users.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	resp := connect.NewResponse(&spinneretv1.GetMeResponse{User: UserProto(me.User), Tenants: TenantAccessProtos(me.Tenants)})
	resp.Header().Set("Cache-Control", "no-store")
	return resp, nil
}

// ChangePassword changes the current user's password and ends its other sessions.
func (h *Handler) ChangePassword(ctx context.Context, req *connect.Request[spinneretv1.ChangePasswordRequest]) (*connect.Response[spinneretv1.ChangePasswordResponse], error) {
	p, err := consoleUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.users.ChangePassword(ctx, p, auth.SessionCookieValue(req.Header()), req.Msg.GetCurrentPassword(), req.Msg.GetNewPassword()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.ChangePasswordResponse{}), nil
}

// consoleUser returns the authenticated console user.
func consoleUser(ctx context.Context) (*authz.Principal, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Kind != authz.KindUser {
		return nil, apperr.PermissionDenied(apperr.ReasonPermissionDenied, "a console user session is required")
	}
	return p, nil
}
