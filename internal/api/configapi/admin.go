package configapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/configcenter"
)

// ListConfigItems implements ConfigAdminService.ListConfigItems (config:read).
func (h *Handler) ListConfigItems(ctx context.Context, req *connect.Request[spinneretv1.ListConfigItemsRequest]) (*connect.Response[spinneretv1.ListConfigItemsResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListItems(ctx, p, ns, configcenter.ListOptions{
		Group:     req.Msg.GetGroup(),
		Search:    req.Msg.GetSearch(),
		PageSize:  int(req.Msg.GetPageSize()),
		PageToken: req.Msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListConfigItemsResponse{
		Items:         make([]*spinneretv1.ConfigItemInfo, len(page.Items)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(page.Total),
	}
	for i, it := range page.Items {
		resp.Items[i] = itemInfo(it)
	}
	return connect.NewResponse(resp), nil
}

// GetConfigItem implements ConfigAdminService.GetConfigItem (config:read).
func (h *Handler) GetConfigItem(ctx context.Context, req *connect.Request[spinneretv1.GetConfigItemRequest]) (*connect.Response[spinneretv1.GetConfigItemResponse], error) {
	var (
		it  configcenter.Item
		err error
	)
	switch sel := req.Msg.GetSelector().(type) {
	case *spinneretv1.GetConfigItemRequest_Id:
		p, perr := apiutil.Principal(ctx)
		if perr != nil {
			return nil, perr
		}
		it, err = h.svc.GetItem(ctx, p, sel.Id)
	case *spinneretv1.GetConfigItemRequest_Locator:
		loc := sel.Locator
		if loc == nil {
			return nil, apperr.InvalidArgument("", "locator is required")
		}
		p, ns, nerr := apiutil.Namespace(ctx, h.cat, loc.GetNamespace())
		if nerr != nil {
			return nil, nerr
		}
		it, err = h.svc.GetItemByLocator(ctx, p, ns, loc.GetGroup(), loc.GetKey())
	default:
		return nil, apperr.InvalidArgument("", "id or locator is required")
	}
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetConfigItemResponse{Item: itemInfo(it)}), nil
}

// CreateConfigItem implements ConfigAdminService.CreateConfigItem
// (config:write, plus config:publish when publishing).
func (h *Handler) CreateConfigItem(ctx context.Context, req *connect.Request[spinneretv1.CreateConfigItemRequest]) (*connect.Response[spinneretv1.CreateConfigItemResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	it, err := h.svc.CreateItem(ctx, p, ns, configcenter.CreateRequest{
		Group:       req.Msg.GetGroup(),
		Key:         req.Msg.GetKey(),
		Format:      req.Msg.GetFormat(),
		SchemaJSON:  req.Msg.GetSchemaJson(),
		Description: req.Msg.GetDescription(),
		Content:     req.Msg.GetContent(),
		Publish:     req.Msg.GetPublish(),
		Comment:     req.Msg.GetComment(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateConfigItemResponse{Item: itemInfo(it)}), nil
}

// SaveConfigDraft implements ConfigAdminService.SaveConfigDraft (config:write).
func (h *Handler) SaveConfigDraft(ctx context.Context, req *connect.Request[spinneretv1.SaveConfigDraftRequest]) (*connect.Response[spinneretv1.SaveConfigDraftResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	it, err := h.svc.SaveDraft(ctx, p, configcenter.DraftRequest{
		ID:          req.Msg.GetId(),
		Content:     req.Msg.GetContent(),
		SchemaJSON:  req.Msg.SchemaJson,
		Description: req.Msg.Description,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.SaveConfigDraftResponse{Item: itemInfo(it)}), nil
}

// PublishConfig implements ConfigAdminService.PublishConfig (config:publish).
func (h *Handler) PublishConfig(ctx context.Context, req *connect.Request[spinneretv1.PublishConfigRequest]) (*connect.Response[spinneretv1.PublishConfigResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	it, version, err := h.svc.Publish(ctx, p, configcenter.PublishRequest{
		ID:              req.Msg.GetId(),
		Comment:         req.Msg.GetComment(),
		ExpectedVersion: req.Msg.GetExpectedVersion(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.PublishConfigResponse{Item: itemInfo(it), Version: version}), nil
}

// RollbackConfig implements ConfigAdminService.RollbackConfig (config:publish).
func (h *Handler) RollbackConfig(ctx context.Context, req *connect.Request[spinneretv1.RollbackConfigRequest]) (*connect.Response[spinneretv1.RollbackConfigResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	it, version, err := h.svc.Rollback(ctx, p, configcenter.RollbackRequest{
		ID:      req.Msg.GetId(),
		Version: req.Msg.GetVersion(),
		Comment: req.Msg.GetComment(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RollbackConfigResponse{Item: itemInfo(it), Version: version}), nil
}

// DeleteConfigItem implements ConfigAdminService.DeleteConfigItem (config:publish).
func (h *Handler) DeleteConfigItem(ctx context.Context, req *connect.Request[spinneretv1.DeleteConfigItemRequest]) (*connect.Response[spinneretv1.DeleteConfigItemResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteItem(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteConfigItemResponse{}), nil
}

// ListConfigVersions implements ConfigAdminService.ListConfigVersions (config:read).
func (h *Handler) ListConfigVersions(ctx context.Context, req *connect.Request[spinneretv1.ListConfigVersionsRequest]) (*connect.Response[spinneretv1.ListConfigVersionsResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListVersions(ctx, p, req.Msg.GetId(), int(req.Msg.GetPageSize()), req.Msg.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ListConfigVersionsResponse{
		Versions:      make([]*spinneretv1.ConfigVersion, len(page.Versions)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(page.Total),
	}
	for i, v := range page.Versions {
		resp.Versions[i] = versionInfo(v)
	}
	return connect.NewResponse(resp), nil
}

// DiffConfigVersions implements ConfigAdminService.DiffConfigVersions (config:read).
func (h *Handler) DiffConfigVersions(ctx context.Context, req *connect.Request[spinneretv1.DiffConfigVersionsRequest]) (*connect.Response[spinneretv1.DiffConfigVersionsResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	d, err := h.svc.DiffVersions(ctx, p, req.Msg.GetId(), req.Msg.GetFromVersion(), req.Msg.GetToVersion())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DiffConfigVersionsResponse{
		FromContent: d.FromContent,
		ToContent:   d.ToContent,
		UnifiedDiff: d.Unified,
	}), nil
}

func clampInt32(n int) int32 {
	const maxInt32 = 1<<31 - 1
	if n > maxInt32 {
		return maxInt32
	}
	return int32(n)
}
