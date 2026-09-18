package configapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/configcenter"
)

// GetConfig implements ConfigService.GetConfig (config:read on the group).
func (h *Handler) GetConfig(ctx context.Context, req *connect.Request[spinneretv1.GetConfigRequest]) (*connect.Response[spinneretv1.GetConfigResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	it, err := h.svc.GetConfig(ctx, p, ns, configcenter.Ref{Group: req.Msg.GetGroup(), Key: req.Msg.GetKey()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetConfigResponse{Item: nodeItem(it)}), nil
}

// BatchGetConfig implements ConfigService.BatchGetConfig (config:read on
// every requested group).
func (h *Handler) BatchGetConfig(ctx context.Context, req *connect.Request[spinneretv1.BatchGetConfigRequest]) (*connect.Response[spinneretv1.BatchGetConfigResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	refs := make([]configcenter.Ref, len(req.Msg.GetItems()))
	for i, r := range req.Msg.GetItems() {
		refs[i] = configcenter.Ref{Group: r.GetGroup(), Key: r.GetKey()}
	}
	items, missing, err := h.svc.BatchGetConfig(ctx, p, ns, refs)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.BatchGetConfigResponse{
		Items:   nodeItems(items),
		Missing: make([]*spinneretv1.ConfigRef, len(missing)),
	}
	for i, m := range missing {
		resp.Missing[i] = &spinneretv1.ConfigRef{Group: m.Group, Key: m.Key}
	}
	return connect.NewResponse(resp), nil
}

// WatchConfig implements ConfigService.WatchConfig (config:read on every
// watched group); see configcenter.Service.WatchConfig for the semantics.
func (h *Handler) WatchConfig(ctx context.Context, req *connect.Request[spinneretv1.WatchConfigRequest]) (*connect.Response[spinneretv1.WatchConfigResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	refs := make([]configcenter.WatchRef, len(req.Msg.GetItems()))
	for i, r := range req.Msg.GetItems() {
		refs[i] = configcenter.WatchRef{Group: r.GetGroup(), Key: r.GetKey(), Version: r.GetVersion()}
	}
	items, err := h.svc.WatchConfig(ctx, p, ns, refs, timeoutFromMillis(req.Msg.GetTimeoutMs()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.WatchConfigResponse{Items: nodeItems(items)}), nil
}
