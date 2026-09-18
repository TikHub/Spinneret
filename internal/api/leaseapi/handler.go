// Package leaseapi implements the node-facing LeaseService Connect handler on
// top of the scheduler (spec §6.1). Requests are authenticated by the server
// interceptor with an API token; the token namespace is implied and the node
// name comes from the X-Spinneret-Node header (Principal.Node).
//
// Lease operations are not written to the audit log: they are the hot path of
// every crawler request and are recorded in acquire/lease statistics instead.
package leaseapi

import (
	"context"
	"math"
	"time"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/scheduler"
)

// Service is the lease logic the handler needs (provided by *scheduler.Service).
type Service interface {
	Acquire(ctx context.Context, req scheduler.AcquireRequest) (*scheduler.Grant, error)
	AcquireBatch(ctx context.Context, req scheduler.AcquireRequest) ([]*scheduler.Grant, error)
	Renew(ctx context.Context, leaseID string, extend time.Duration) (time.Time, error)
	Release(ctx context.Context, leaseID string) (bool, error)
}

// Handler implements spinneretv1connect.LeaseServiceHandler.
type Handler struct {
	svc Service
}

var _ spinneretv1connect.LeaseServiceHandler = (*Handler)(nil)

// New creates the LeaseService handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// Acquire leases one identity.
func (h *Handler) Acquire(ctx context.Context, req *connect.Request[spinneretv1.AcquireRequest]) (*connect.Response[spinneretv1.AcquireResponse], error) {
	if _, err := apiutil.Principal(ctx); err != nil {
		return nil, err
	}
	m := req.Msg
	g, err := h.svc.Acquire(ctx, scheduler.AcquireRequest{
		Site:          m.GetSite(),
		Client:        m.GetClient(),
		URI:           m.GetUri(),
		EndpointGroup: m.GetEndpointGroup(),
		SessionKey:    m.GetSessionKey(),
		Wait:          millis(m.GetWaitMs()),
	})
	if err != nil {
		return nil, err
	}
	out, err := grantProto(g)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// AcquireBatch leases up to count distinct identities.
func (h *Handler) AcquireBatch(ctx context.Context, req *connect.Request[spinneretv1.AcquireBatchRequest]) (*connect.Response[spinneretv1.AcquireBatchResponse], error) {
	if _, err := apiutil.Principal(ctx); err != nil {
		return nil, err
	}
	m := req.Msg
	grants, err := h.svc.AcquireBatch(ctx, scheduler.AcquireRequest{
		Site:          m.GetSite(),
		Client:        m.GetClient(),
		URI:           m.GetUri(),
		EndpointGroup: m.GetEndpointGroup(),
		SessionKey:    m.GetSessionKey(),
		Wait:          millis(m.GetWaitMs()),
		Count:         int(m.GetCount()),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.AcquireBatchResponse{
		Leases:    make([]*spinneretv1.AcquireResponse, 0, len(grants)),
		Requested: m.GetCount(),
	}
	for _, g := range grants {
		p, err := grantProto(g)
		if err != nil {
			return nil, err
		}
		out.Leases = append(out.Leases, p)
	}
	return connect.NewResponse(out), nil
}

// Renew extends an active lease.
func (h *Handler) Renew(ctx context.Context, req *connect.Request[spinneretv1.RenewRequest]) (*connect.Response[spinneretv1.RenewResponse], error) {
	if _, err := apiutil.Principal(ctx); err != nil {
		return nil, err
	}
	expires, err := h.svc.Renew(ctx, req.Msg.GetLeaseId(), millis(req.Msg.GetExtendMs()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RenewResponse{ExpiresAt: apiutil.Timestamp(expires)}), nil
}

// Release ends a lease; releasing an ended lease reports released=false.
func (h *Handler) Release(ctx context.Context, req *connect.Request[spinneretv1.ReleaseRequest]) (*connect.Response[spinneretv1.ReleaseResponse], error) {
	if _, err := apiutil.Principal(ctx); err != nil {
		return nil, err
	}
	released, err := h.svc.Release(ctx, req.Msg.GetLeaseId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.ReleaseResponse{Released: released}), nil
}

// millis converts a millisecond request field; negative values (rejected by
// protovalidate) map to a negative duration so the service rejects them too.
func millis(ms int32) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// clampInt32 converts a millisecond count for int32 response fields.
func clampInt32(ms int64) int32 {
	switch {
	case ms > math.MaxInt32:
		return math.MaxInt32
	case ms < 0:
		return 0
	default:
		return int32(ms)
	}
}

// internalError wraps a conversion failure.
func internalError(err error) error {
	return apperr.Internal(err)
}
