package notifyapi

import (
	"context"
	"math"
	"sort"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/notify"
)

// channelCursor is the page token of ListChannels.
type channelCursor struct {
	Name string `json:"n"`
}

// ListChannels lists the channels of the active tenant the caller may read.
func (h *Handler) ListChannels(ctx context.Context, req *connect.Request[spinneretv1.ListChannelsRequest]) (*connect.Response[spinneretv1.ListChannelsResponse], error) {
	p, tenantID, err := principalTenant(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	query := notify.ChannelQuery{TenantID: tenantID, Limit: apiutil.PageSize(msg.GetPageSize())}
	if msg.GetNamespace() != "" {
		_, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
		if err != nil {
			return nil, err
		}
		if err := p.Require(authz.PermNotifyRead, apiutil.NamespaceResource(ns)); err != nil {
			return nil, err
		}
		query.NamespaceIDs = []string{ns.ID}
	} else {
		query.IncludeTenant = p.Can(authz.PermNotifyRead, tenantResource(tenantID))
		for _, ns := range h.cat.Namespaces(tenantID) {
			if p.Can(authz.PermNotifyRead, apiutil.NamespaceResource(ns)) {
				query.NamespaceIDs = append(query.NamespaceIDs, ns.ID)
			}
		}
		if !query.IncludeTenant && len(query.NamespaceIDs) == 0 {
			return nil, p.Require(authz.PermNotifyRead, tenantResource(tenantID))
		}
	}
	var cursor channelCursor
	if _, err := apiutil.DecodeCursor(msg.GetPageToken(), &cursor); err != nil {
		return nil, err
	}
	query.AfterName = cursor.Name
	page, err := h.svc.ListChannels(ctx, query)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListChannelsResponse{
		Channels: make([]*spinneretv1.Channel, 0, len(page.Channels)),
		Total:    int32(min(page.Total, math.MaxInt32)), //nolint:gosec // bounded by min
	}
	for _, ch := range page.Channels {
		pc, err := h.channelProto(ch)
		if err != nil {
			return nil, err
		}
		resp.Channels = append(resp.Channels, pc)
	}
	if page.More && len(page.Channels) > 0 {
		token, err := apiutil.EncodeCursor(channelCursor{Name: page.Channels[len(page.Channels)-1].Name})
		if err != nil {
			return nil, apperr.Internal(err)
		}
		resp.NextPageToken = token
	}
	return connect.NewResponse(resp), nil
}

// CreateChannel creates a channel in the active tenant.
func (h *Handler) CreateChannel(ctx context.Context, req *connect.Request[spinneretv1.CreateChannelRequest]) (*connect.Response[spinneretv1.CreateChannelResponse], error) {
	p, tenantID, err := principalTenant(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	namespaceID := ""
	resource := tenantResource(tenantID)
	if msg.GetNamespace() != "" {
		_, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
		if err != nil {
			return nil, err
		}
		namespaceID = ns.ID
		resource = apiutil.NamespaceResource(ns)
	}
	if err := p.Require(authz.PermNotifyWrite, resource); err != nil {
		return nil, err
	}
	if msg.GetConfig() == nil {
		return nil, apperr.InvalidArgument("", "config is required")
	}
	siteIDs, err := h.siteIDs(namespaceID, msg.GetSites())
	if err != nil {
		return nil, err
	}
	enabled := true
	if msg.Enabled != nil {
		enabled = msg.GetEnabled()
	}
	ch, err := h.svc.CreateChannel(ctx, p, notify.ChannelInput{
		TenantID:    tenantID,
		NamespaceID: namespaceID,
		Name:        msg.GetName(),
		Kind:        msg.GetKind(),
		Config:      msg.GetConfig().AsMap(),
		EventTypes:  msg.GetEventTypes(),
		SiteIDs:     siteIDs,
		MinSeverity: msg.GetMinSeverity(),
		Enabled:     enabled,
	})
	if err != nil {
		return nil, err
	}
	pc, err := h.channelProto(ch)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateChannelResponse{Channel: pc}), nil
}

// UpdateChannel replaces the mutable fields of a channel.
func (h *Handler) UpdateChannel(ctx context.Context, req *connect.Request[spinneretv1.UpdateChannelRequest]) (*connect.Response[spinneretv1.UpdateChannelResponse], error) {
	msg := req.Msg
	p, ch, err := h.authorizedChannel(ctx, msg.GetId(), authz.PermNotifyWrite)
	if err != nil {
		return nil, err
	}
	siteIDs, err := h.siteIDs(ch.NamespaceID, msg.GetSites())
	if err != nil {
		return nil, err
	}
	upd := notify.ChannelUpdate{
		Name:        msg.GetName(),
		EventTypes:  msg.GetEventTypes(),
		SiteIDs:     siteIDs,
		MinSeverity: msg.GetMinSeverity(),
		Enabled:     msg.GetEnabled(),
	}
	if msg.GetConfig() != nil {
		upd.Config = msg.GetConfig().AsMap()
	}
	updated, err := h.svc.UpdateChannel(ctx, p, ch.ID, upd)
	if err != nil {
		return nil, err
	}
	pc, err := h.channelProto(updated)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateChannelResponse{Channel: pc}), nil
}

// DeleteChannel deletes a channel.
func (h *Handler) DeleteChannel(ctx context.Context, req *connect.Request[spinneretv1.DeleteChannelRequest]) (*connect.Response[spinneretv1.DeleteChannelResponse], error) {
	p, ch, err := h.authorizedChannel(ctx, req.Msg.GetId(), authz.PermNotifyWrite)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteChannel(ctx, p, ch.ID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteChannelResponse{}), nil
}

// TestChannel sends a test alert through a channel.
func (h *Handler) TestChannel(ctx context.Context, req *connect.Request[spinneretv1.TestChannelRequest]) (*connect.Response[spinneretv1.TestChannelResponse], error) {
	p, ch, err := h.authorizedChannel(ctx, req.Msg.GetId(), authz.PermNotifyWrite)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.TestChannel(ctx, p, ch.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.TestChannelResponse{Delivery: deliveryProto(d)}), nil
}

// channelProto converts a channel (config already masked by the service).
func (h *Handler) channelProto(ch notify.Channel) (*spinneretv1.Channel, error) {
	cfg, err := apiutil.Struct(ch.Config)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.Channel{
		Id:                 ch.ID,
		Name:               ch.Name,
		Kind:               ch.Kind,
		Config:             cfg,
		EventTypes:         ch.EventTypes,
		Sites:              []string{},
		MinSeverity:        ch.MinSeverity,
		Enabled:            ch.Enabled,
		LastDeliveryAt:     apiutil.TimestampPtr(ch.LastDeliveryAt),
		LastDeliveryStatus: ch.LastDeliveryStatus,
		CreatedAt:          apiutil.Timestamp(ch.CreatedAt),
		UpdatedAt:          apiutil.Timestamp(ch.UpdatedAt),
	}
	if ns, ok := h.cat.Namespace(ch.NamespaceID); ok && ch.NamespaceID != "" {
		out.Namespace = ns.Name
		for _, id := range ch.SiteIDs {
			if site, ok := ns.SitesByID[id]; ok {
				out.Sites = append(out.Sites, site.Name)
			}
		}
		sort.Strings(out.Sites)
	}
	return out, nil
}

// deliveryProto converts a delivery result.
func deliveryProto(d notify.Delivery) *spinneretv1.AlertDelivery {
	return &spinneretv1.AlertDelivery{
		ChannelId:   d.ChannelID,
		ChannelName: d.ChannelName,
		Ok:          d.OK,
		Error:       d.Error,
		At:          apiutil.Timestamp(d.At),
	}
}
