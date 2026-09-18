// Package breakerapi implements the Connect handler of BreakerAdminService.
// Authorization (breaker:read / breaker:operate on the site of the affected
// endpoint group, list filtering to accessible sites) and audit logging are
// enforced by the breaker service for every call; the handler resolves the
// caller and namespace and converts between protobuf messages and service
// types.
package breakerapi

import (
	"context"
	"math"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/breaker"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// Service is the breaker functionality used by the handler (implemented by
// *breaker.Service).
type Service interface {
	List(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f breaker.ListFilter) (breaker.ListPage, error)
	Get(ctx context.Context, p *authz.Principal, groupID string) (*breaker.Status, error)
	Open(ctx context.Context, p *authz.Principal, groupID, duration, reason string) (*breaker.Status, error)
	Close(ctx context.Context, p *authz.Principal, groupID, reason string) (*breaker.Status, error)
	ListEvents(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f breaker.EventFilter) (breaker.EventPage, error)
	SetSitePaused(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, site string, paused bool, reason string) (breaker.SiteSwitch, error)
}

// Handler implements spinneretv1connect.BreakerAdminServiceHandler.
type Handler struct {
	svc Service
	cat catalog.Catalog
}

var _ spinneretv1connect.BreakerAdminServiceHandler = (*Handler)(nil)

// New creates the BreakerAdminService handler.
func New(svc Service, cat catalog.Catalog) *Handler {
	return &Handler{svc: svc, cat: cat}
}

// ListBreakers implements BreakerAdminService.ListBreakers (breaker:read).
func (h *Handler) ListBreakers(ctx context.Context, req *connect.Request[spinneretv1.ListBreakersRequest]) (*connect.Response[spinneretv1.ListBreakersResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.List(ctx, p, ns, breaker.ListFilter{
		Site:      msg.GetSite(),
		Client:    msg.GetClient(),
		States:    msg.GetStates(),
		PageSize:  msg.GetPageSize(),
		PageToken: msg.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListBreakersResponse{
		Breakers:      make([]*spinneretv1.BreakerStatus, 0, len(page.Breakers)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(int64(page.Total)),
	}
	for i := range page.Breakers {
		out.Breakers = append(out.Breakers, statusProto(&page.Breakers[i]))
	}
	return connect.NewResponse(out), nil
}

// GetBreaker implements BreakerAdminService.GetBreaker (breaker:read).
func (h *Handler) GetBreaker(ctx context.Context, req *connect.Request[spinneretv1.GetBreakerRequest]) (*connect.Response[spinneretv1.GetBreakerResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	st, err := h.svc.Get(ctx, p, req.Msg.GetEndpointGroupId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetBreakerResponse{Breaker: statusProto(st)}), nil
}

// OpenBreaker implements BreakerAdminService.OpenBreaker (breaker:operate, audited).
func (h *Handler) OpenBreaker(ctx context.Context, req *connect.Request[spinneretv1.OpenBreakerRequest]) (*connect.Response[spinneretv1.OpenBreakerResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	st, err := h.svc.Open(ctx, p, msg.GetEndpointGroupId(), msg.GetDuration(), msg.GetReason())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.OpenBreakerResponse{Breaker: statusProto(st)}), nil
}

// CloseBreaker implements BreakerAdminService.CloseBreaker (breaker:operate, audited).
func (h *Handler) CloseBreaker(ctx context.Context, req *connect.Request[spinneretv1.CloseBreakerRequest]) (*connect.Response[spinneretv1.CloseBreakerResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	st, err := h.svc.Close(ctx, p, req.Msg.GetEndpointGroupId(), req.Msg.GetReason())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CloseBreakerResponse{Breaker: statusProto(st)}), nil
}

// ListBreakerEvents implements BreakerAdminService.ListBreakerEvents (breaker:read).
func (h *Handler) ListBreakerEvents(ctx context.Context, req *connect.Request[spinneretv1.ListBreakerEventsRequest]) (*connect.Response[spinneretv1.ListBreakerEventsResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	f := breaker.EventFilter{
		Site:            msg.GetSite(),
		EndpointGroupID: msg.GetEndpointGroupId(),
		Trigger:         msg.GetTrigger(),
		PageSize:        msg.GetPageSize(),
		PageToken:       msg.GetPageToken(),
	}
	if tr := msg.GetTimeRange(); tr != nil {
		f.Start = apiutil.Time(tr.GetStart())
		f.End = apiutil.Time(tr.GetEnd())
	}
	page, err := h.svc.ListEvents(ctx, p, ns, f)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListBreakerEventsResponse{
		Events:        make([]*spinneretv1.BreakerEvent, 0, len(page.Events)),
		NextPageToken: page.NextPageToken,
	}
	for i := range page.Events {
		ev, err := eventProto(&page.Events[i])
		if err != nil {
			return nil, err
		}
		out.Events = append(out.Events, ev)
	}
	return connect.NewResponse(out), nil
}

// SetSitePaused implements BreakerAdminService.SetSitePaused (breaker:operate on the site, audited).
func (h *Handler) SetSitePaused(ctx context.Context, req *connect.Request[spinneretv1.SetSitePausedRequest]) (*connect.Response[spinneretv1.SetSitePausedResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	sw, err := h.svc.SetSitePaused(ctx, p, ns, msg.GetSite(), msg.GetPaused(), msg.GetReason())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.SetSitePausedResponse{
		Site:         sw.Site,
		SiteId:       sw.SiteID,
		Paused:       sw.Paused,
		PausedReason: sw.Reason,
		PausedAt:     apiutil.TimestampPtr(sw.PausedAt),
	}), nil
}

// statusProto converts a breaker status.
func statusProto(st *breaker.Status) *spinneretv1.BreakerStatus {
	return &spinneretv1.BreakerStatus{
		Namespace:        st.Namespace,
		Site:             st.Site,
		SiteId:           st.SiteID,
		Client:           st.Client,
		EndpointGroup:    st.EndpointGroup,
		EndpointGroupId:  st.EndpointGroupID,
		State:            st.State,
		OpenUntil:        apiutil.Timestamp(st.OpenUntil),
		ConsecutiveOpens: clampInt32(st.ConsecutiveOpens),
		Manual:           st.Manual,
		Reason:           st.Reason,
		Window: &spinneretv1.WindowMetrics{
			Total:             clampInt32(st.Window.Total),
			Success:           clampInt32(st.Window.Success),
			Risk:              clampInt32(st.Window.Risk),
			CaptchaIdentities: clampInt32(st.Window.CaptchaIdentities),
			RiskRatio:         st.Window.RiskRatio,
			SuccessRatio:      st.Window.SuccessRatio,
		},
		Probe: &spinneretv1.ProbeMetrics{
			Samples:   clampInt32(st.Probe.Samples),
			Successes: clampInt32(st.Probe.Successes),
			Issued:    clampInt32(st.Probe.Issued),
		},
		LastOpenedAt: apiutil.Timestamp(st.LastOpenedAt),
		LastClosedAt: apiutil.Timestamp(st.LastClosedAt),
		SitePaused:   st.SitePaused,
		PolicyId:     st.PolicyID,
		PolicyName:   st.PolicyName,
	}
}

// eventProto converts a stored breaker event.
func eventProto(ev *breaker.Event) (*spinneretv1.BreakerEvent, error) {
	metrics, err := apiutil.Struct(ev.Metrics)
	if err != nil {
		return nil, err
	}
	return &spinneretv1.BreakerEvent{
		Id:              ev.ID,
		CreatedAt:       apiutil.Timestamp(ev.CreatedAt),
		Site:            ev.Site,
		EndpointGroup:   ev.EndpointGroup,
		EndpointGroupId: ev.EndpointGroupID,
		Client:          ev.Client,
		FromState:       ev.FromState,
		ToState:         ev.ToState,
		Trigger:         ev.Trigger,
		Reason:          ev.Reason,
		OpenUntil:       apiutil.TimestampPtr(ev.OpenUntil),
		Metrics:         metrics,
		Actor:           ev.Actor,
	}, nil
}

// clampInt32 converts a count to int32, saturating at the int32 range.
func clampInt32(v int64) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	default:
		return int32(v)
	}
}
