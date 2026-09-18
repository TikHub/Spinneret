package siteapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/sitesvc"
)

// ListEndpointGroups implements SiteAdminServiceHandler.
func (h *Handler) ListEndpointGroups(ctx context.Context, req *connect.Request[spinneretv1.ListEndpointGroupsRequest]) (*connect.Response[spinneretv1.ListEndpointGroupsResponse], error) {
	p, ref, err := h.resolveSite(ctx, authz.PermSiteRead, "", req.Msg.GetNamespace(), req.Msg.GetSite())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteRead, siteResource(ref)); err != nil {
		return nil, err
	}
	var cur sitesvc.GroupCursor
	if _, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	page, err := h.svc.ListEndpointGroups(ctx, ref, req.Msg.GetClient(), apiutil.PageSize(req.Msg.GetPageSize()), cur)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListEndpointGroupsResponse{
		EndpointGroups: make([]*spinneretv1.EndpointGroup, 0, len(page.Groups)),
		Total:          clampInt32(int64(page.Total)),
	}
	for _, g := range page.Groups {
		resp.EndpointGroups = append(resp.EndpointGroups, toProtoGroup(g))
	}
	if page.Next != nil {
		if resp.NextPageToken, err = apiutil.EncodeCursor(page.Next); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	return connect.NewResponse(resp), nil
}

// CreateEndpointGroup implements SiteAdminServiceHandler.
func (h *Handler) CreateEndpointGroup(ctx context.Context, req *connect.Request[spinneretv1.CreateEndpointGroupRequest]) (*connect.Response[spinneretv1.CreateEndpointGroupResponse], error) {
	p, ref, err := h.resolveSite(ctx, authz.PermSiteWrite, "", req.Msg.GetNamespace(), req.Msg.GetSite())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, siteResource(ref)); err != nil {
		h.denied(ctx, p, ref.TenantID, ref.NamespaceID, sitesvc.ActionEndpointGroupCreate, sitesvc.ResourceEndpointGroup, "", req.Msg.GetName())
		return nil, err
	}
	g, err := h.svc.CreateEndpointGroup(ctx, p, ref.ID, sitesvc.CreateGroupInput{
		Client:       req.Msg.GetClient(),
		Name:         req.Msg.GetName(),
		Description:  req.Msg.GetDescription(),
		LowWatermark: int(req.Msg.GetLowWatermark()),
		Rules:        fromProtoRules(req.Msg.GetRules()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateEndpointGroupResponse{EndpointGroup: toProtoGroup(g)}), nil
}

// UpdateEndpointGroup implements SiteAdminServiceHandler.
func (h *Handler) UpdateEndpointGroup(ctx context.Context, req *connect.Request[spinneretv1.UpdateEndpointGroupRequest]) (*connect.Response[spinneretv1.UpdateEndpointGroupResponse], error) {
	p, ref, err := h.resolveGroup(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, siteResource(ref.Site)); err != nil {
		h.denied(ctx, p, ref.Site.TenantID, ref.Site.NamespaceID, sitesvc.ActionEndpointGroupUpdate, sitesvc.ResourceEndpointGroup, ref.ID, ref.Name)
		return nil, err
	}
	in := sitesvc.UpdateGroupInput{Description: req.Msg.Description}
	if req.Msg.LowWatermark != nil {
		v := int(*req.Msg.LowWatermark)
		in.LowWatermark = &v
	}
	g, err := h.svc.UpdateEndpointGroup(ctx, p, ref.ID, in)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateEndpointGroupResponse{EndpointGroup: toProtoGroup(g)}), nil
}

// DeleteEndpointGroup implements SiteAdminServiceHandler.
func (h *Handler) DeleteEndpointGroup(ctx context.Context, req *connect.Request[spinneretv1.DeleteEndpointGroupRequest]) (*connect.Response[spinneretv1.DeleteEndpointGroupResponse], error) {
	p, ref, err := h.resolveGroup(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, siteResource(ref.Site)); err != nil {
		h.denied(ctx, p, ref.Site.TenantID, ref.Site.NamespaceID, sitesvc.ActionEndpointGroupDelete, sitesvc.ResourceEndpointGroup, ref.ID, ref.Name)
		return nil, err
	}
	if err := h.svc.DeleteEndpointGroup(ctx, p, ref.ID); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteEndpointGroupResponse{}), nil
}

// ListURIRules implements SiteAdminServiceHandler.
func (h *Handler) ListURIRules(ctx context.Context, req *connect.Request[spinneretv1.ListURIRulesRequest]) (*connect.Response[spinneretv1.ListURIRulesResponse], error) {
	p, ref, err := h.resolveGroup(ctx, req.Msg.GetEndpointGroupId())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteRead, siteResource(ref.Site)); err != nil {
		return nil, err
	}
	var cur sitesvc.RuleCursor
	var after *sitesvc.RuleCursor
	if ok, err := apiutil.DecodeCursor(req.Msg.GetPageToken(), &cur); err != nil {
		return nil, err
	} else if ok {
		after = &cur
	}
	page, err := h.svc.ListURIRules(ctx, ref.ID, apiutil.PageSize(req.Msg.GetPageSize()), after)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListURIRulesResponse{
		Rules: make([]*spinneretv1.URIRule, 0, len(page.Rules)),
		Total: clampInt32(int64(page.Total)),
	}
	for _, r := range page.Rules {
		resp.Rules = append(resp.Rules, toProtoRule(r))
	}
	if page.Next != nil {
		if resp.NextPageToken, err = apiutil.EncodeCursor(page.Next); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	return connect.NewResponse(resp), nil
}

// ReplaceURIRules implements SiteAdminServiceHandler.
func (h *Handler) ReplaceURIRules(ctx context.Context, req *connect.Request[spinneretv1.ReplaceURIRulesRequest]) (*connect.Response[spinneretv1.ReplaceURIRulesResponse], error) {
	p, ref, err := h.resolveGroup(ctx, req.Msg.GetEndpointGroupId())
	if err != nil {
		return nil, err
	}
	if err := p.Require(authz.PermSiteWrite, siteResource(ref.Site)); err != nil {
		h.denied(ctx, p, ref.Site.TenantID, ref.Site.NamespaceID, sitesvc.ActionURIRulesReplace, sitesvc.ResourceEndpointGroup, ref.ID, ref.Name)
		return nil, err
	}
	stored, err := h.svc.ReplaceURIRules(ctx, p, ref.ID, fromProtoRules(req.Msg.GetRules()))
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ReplaceURIRulesResponse{Rules: make([]*spinneretv1.URIRule, 0, len(stored))}
	for _, r := range stored {
		resp.Rules = append(resp.Rules, toProtoRule(r))
	}
	return connect.NewResponse(resp), nil
}
