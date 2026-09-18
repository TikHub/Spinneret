// Package notifyapi implements the NotificationAdminService Connect handler:
// notification channel CRUD, test deliveries and the alert history.
package notifyapi

import (
	"context"
	"log/slog"

	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/notify"
)

// Service is the part of notify.Service the handler uses.
type Service interface {
	CreateChannel(ctx context.Context, p *authz.Principal, in notify.ChannelInput) (notify.Channel, error)
	UpdateChannel(ctx context.Context, p *authz.Principal, id string, upd notify.ChannelUpdate) (notify.Channel, error)
	DeleteChannel(ctx context.Context, p *authz.Principal, id string) error
	GetChannel(ctx context.Context, id string) (notify.Channel, error)
	ListChannels(ctx context.Context, q notify.ChannelQuery) (notify.ChannelPage, error)
	TestChannel(ctx context.Context, p *authz.Principal, id string) (notify.Delivery, error)
	ListAlertEvents(ctx context.Context, q notify.AlertQuery) (notify.AlertPage, error)
}

// Handler implements spinneretv1connect.NotificationAdminServiceHandler.
type Handler struct {
	svc    Service
	cat    catalog.Catalog
	logger *slog.Logger
}

var _ spinneretv1connect.NotificationAdminServiceHandler = (*Handler)(nil)

// New creates the handler. A nil logger discards logs.
func New(svc Service, cat catalog.Catalog, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Handler{svc: svc, cat: cat, logger: logger}
}

// principalTenant returns the caller and its active tenant.
func principalTenant(ctx context.Context) (*authz.Principal, string, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, "", err
	}
	if p.TenantID == "" {
		return nil, "", apperr.InvalidArgument("", "active tenant is required (X-Spinneret-Tenant header)")
	}
	return p, p.TenantID, nil
}

// tenantResource is the authorization resource of tenant-wide channels and alerts.
func tenantResource(tenantID string) authz.Resource {
	return authz.Resource{TenantID: tenantID}
}

// channelResource is the authorization resource of a channel.
func (h *Handler) channelResource(ch notify.Channel) authz.Resource {
	r := authz.Resource{TenantID: ch.TenantID, NamespaceID: ch.NamespaceID}
	if ns, ok := h.cat.Namespace(ch.NamespaceID); ok && ch.NamespaceID != "" {
		r.NamespaceName = ns.Name
	}
	return r
}

// authorizedChannel loads a channel of the caller's active tenant and checks perm on it.
func (h *Handler) authorizedChannel(ctx context.Context, id string, perm authz.Permission) (*authz.Principal, notify.Channel, error) {
	p, tenantID, err := principalTenant(ctx)
	if err != nil {
		return nil, notify.Channel{}, err
	}
	ch, err := h.svc.GetChannel(ctx, id)
	if err != nil {
		return nil, notify.Channel{}, err
	}
	if ch.TenantID != tenantID {
		// Channels of other tenants are reported as missing.
		return nil, notify.Channel{}, apperr.NotFound("notification channel not found")
	}
	if err := p.Require(perm, h.channelResource(ch)); err != nil {
		return nil, notify.Channel{}, err
	}
	return p, ch, nil
}

// siteIDs resolves site names inside the channel's namespace.
func (h *Handler) siteIDs(namespaceID string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if namespaceID == "" {
		return nil, apperr.InvalidArgument("", "sites require a namespace-bound channel")
	}
	ns, ok := h.cat.Namespace(namespaceID)
	if !ok {
		return nil, apperr.NotFound("namespace not found")
	}
	ids := make([]string, 0, len(names))
	for _, name := range names {
		site, ok := ns.Sites[name]
		if !ok {
			return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", name)
		}
		ids = append(ids, site.ID)
	}
	return ids, nil
}
