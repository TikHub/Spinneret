package identityapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/identitysvc"
)

// ListIdentities implements IdentityAdminServiceHandler.
func (h *Handler) ListIdentities(ctx context.Context, req *connect.Request[spinneretv1.ListIdentitiesRequest]) (*connect.Response[spinneretv1.ListIdentitiesResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListIdentities(ctx, p, ns, identitysvc.IdentityQuery{
		Filter: filterFromProto(m.GetFilter()), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
		OrderBy: m.GetOrderBy(), Descending: m.GetDescending(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListIdentitiesResponse{
		Identities:    make([]*spinneretv1.Identity, 0, len(page.Identities)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(page.Total),
	}
	for _, i := range page.Identities {
		out.Identities = append(out.Identities, identityProto(i))
	}
	return connect.NewResponse(out), nil
}

// GetIdentity implements IdentityAdminServiceHandler. Hot state is read on a
// best-effort basis: failures are logged and leave hot_state unset.
func (h *Handler) GetIdentity(ctx context.Context, req *connect.Request[spinneretv1.GetIdentityRequest]) (*connect.Response[spinneretv1.GetIdentityResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	detail, err := h.svc.GetIdentity(ctx, p, req.Msg.GetId(), req.Msg.GetReveal())
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.GetIdentityResponse{
		Identity:     identityProto(detail.Identity),
		Revealed:     detail.Revealed,
		RecentEvents: make([]*spinneretv1.StateEvent, 0, len(detail.RecentEvents)),
	}
	if detail.Payload != nil {
		if out.Payload, err = apiutil.Struct(detail.Payload); err != nil {
			return nil, err
		}
	}
	for _, ev := range detail.RecentEvents {
		out.RecentEvents = append(out.RecentEvents, stateEventProto(ev))
	}
	if h.hot != nil {
		hs, err := h.hot.IdentityHotState(ctx, detail.Site, detail.Identity.ID)
		if err != nil {
			h.logger.Warn("identity hot state unavailable", slog.String("identity_id", detail.Identity.ID), slog.Any("error", err))
		} else {
			out.HotState = hotStateProto(hs)
			if hs.Present {
				out.Identity.ActiveLeases = clampInt32(hs.ActiveLeases)
				out.Identity.GlobalScore = hs.GlobalScore
				out.Identity.GlobalSamples = clampInt32(hs.GlobalSamples)
				out.Identity.BoundProxyId = hs.BoundProxyID
			}
		}
	}
	return connect.NewResponse(out), nil
}

// GetIdentityHotState implements IdentityAdminServiceHandler.
func (h *Handler) GetIdentityHotState(ctx context.Context, req *connect.Request[spinneretv1.GetIdentityHotStateRequest]) (*connect.Response[spinneretv1.GetIdentityHotStateResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := h.svc.ResolveIdentity(ctx, p, req.Msg.GetId(), authz.PermIdentityRead)
	if err != nil {
		return nil, err
	}
	if h.hot == nil {
		return nil, apperr.Unavailable(apperr.ReasonRebuilding, 1000, "hot state is not available")
	}
	hs, err := h.hot.IdentityHotState(ctx, ref.Site, ref.ID)
	if err != nil {
		if _, ok := apperr.As(err); ok {
			return nil, err
		}
		return nil, apperr.Internal(err)
	}
	return connect.NewResponse(&spinneretv1.GetIdentityHotStateResponse{HotState: hotStateProto(hs)}), nil
}

// ImportIdentities implements IdentityAdminServiceHandler.
func (h *Handler) ImportIdentities(ctx context.Context, req *connect.Request[spinneretv1.ImportIdentitiesRequest]) (*connect.Response[spinneretv1.ImportIdentitiesResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	res, err := h.svc.ImportIdentities(ctx, p, ns, identitysvc.ImportInput{
		Site: m.GetSite(), Type: m.GetType(), Format: m.GetFormat(), Data: m.GetData(), Mode: m.GetMode(), DryRun: m.GetDryRun(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ImportIdentitiesResponse{
		Created:   clampInt32(res.Created),
		Updated:   clampInt32(res.Updated),
		Unchanged: clampInt32(res.Unchanged),
		Failed:    make([]*spinneretv1.ImportFailure, 0, len(res.Failed)),
	}
	for _, f := range res.Failed {
		out.Failed = append(out.Failed, &spinneretv1.ImportFailure{Line: clampInt32(f.Line), Message: f.Message})
	}
	return connect.NewResponse(out), nil
}

// UpdateIdentityPayload implements IdentityAdminServiceHandler.
func (h *Handler) UpdateIdentityPayload(ctx context.Context, req *connect.Request[spinneretv1.UpdateIdentityPayloadRequest]) (*connect.Response[spinneretv1.UpdateIdentityPayloadResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetPayload() == nil {
		return nil, apperr.InvalidArgument("", "payload is required")
	}
	i, err := h.svc.UpdateIdentityPayload(ctx, p, req.Msg.GetId(), req.Msg.GetPayload().AsMap())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateIdentityPayloadResponse{Identity: identityProto(i)}), nil
}

// UpdateIdentity implements IdentityAdminServiceHandler.
func (h *Handler) UpdateIdentity(ctx context.Context, req *connect.Request[spinneretv1.UpdateIdentityRequest]) (*connect.Response[spinneretv1.UpdateIdentityResponse], error) {
	m := req.Msg
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	i, err := h.svc.UpdateIdentity(ctx, p, m.GetId(), identitysvc.IdentityUpdate{
		Region: m.Region, Tags: m.GetTags(), SetTags: m.GetSetTags(), Labels: m.GetLabels(), SetLabels: m.GetSetLabels(),
		AccountRef: m.AccountRef,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateIdentityResponse{Identity: identityProto(i)}), nil
}

// ListStateEvents implements IdentityAdminServiceHandler.
func (h *Handler) ListStateEvents(ctx context.Context, req *connect.Request[spinneretv1.ListStateEventsRequest]) (*connect.Response[spinneretv1.ListStateEventsResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	q := identitysvc.StateEventQuery{
		SubjectKind: m.GetSubjectKind(), SubjectID: m.GetSubjectId(), Site: m.GetSite(), Actions: m.GetActions(),
		Shadow: m.Shadow, PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	}
	if q.From, q.To, err = timeRange(m.GetTimeRange()); err != nil {
		return nil, err
	}
	page, err := h.svc.ListStateEvents(ctx, p, ns, q)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListStateEventsResponse{
		Events:        make([]*spinneretv1.StateEvent, 0, len(page.Events)),
		NextPageToken: page.NextPageToken,
	}
	for _, ev := range page.Events {
		out.Events = append(out.Events, stateEventProto(ev))
	}
	return connect.NewResponse(out), nil
}
