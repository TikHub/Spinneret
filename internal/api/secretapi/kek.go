package secretapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/apiutil"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// Audit vocabulary of KEK operations.
const (
	AuditResourceKEK          = "kek"
	AuditActionKEKRewrapStart = "kek.rewrap_start"
)

// GetKEKStatus reports configured KEKs and rewrap progress (kek:manage,
// platform admins only).
func (h *Handler) GetKEKStatus(ctx context.Context, _ *connect.Request[spinneretv1.GetKEKStatusRequest]) (*connect.Response[spinneretv1.GetKEKStatusResponse], error) {
	if _, err := requireKEKManage(ctx); err != nil {
		return nil, err
	}
	st, err := h.kek.Status(ctx)
	if err != nil {
		return nil, asInternal(err)
	}
	resp := &spinneretv1.GetKEKStatusResponse{
		CurrentKekId:         st.CurrentKEKID,
		Keks:                 make([]*spinneretv1.KEKInfo, 0, len(st.KEKs)),
		RewrapRunning:        st.Running,
		RewrapDone:           st.Done,
		RewrapTotal:          st.Total,
		LastRewrapError:      st.LastError,
		LastRewrapFinishedAt: apiutil.TimestampPtr(st.LastFinishedAt),
	}
	for _, k := range st.KEKs {
		resp.Keks = append(resp.Keks, &spinneretv1.KEKInfo{
			Id: k.ID, Current: k.Current, Configured: k.Configured, WrappedRecords: k.WrappedRecords,
		})
	}
	return connect.NewResponse(resp), nil
}

// StartKEKRewrap starts re-wrapping every DEK onto the current KEK (kek:manage,
// platform admins only). started is false when a job is already running.
func (h *Handler) StartKEKRewrap(ctx context.Context, _ *connect.Request[spinneretv1.StartKEKRewrapRequest]) (*connect.Response[spinneretv1.StartKEKRewrapResponse], error) {
	p, err := requireKEKManage(ctx)
	if err != nil {
		return nil, err
	}
	started, err := h.kek.Start(ctx)
	if err != nil {
		h.audit.Record(ctx, audit.FromPrincipal(p, "", "", AuditActionKEKRewrapStart, AuditResourceKEK, "", "",
			audit.ResultError, nil))
		return nil, asInternal(err)
	}
	h.audit.Record(ctx, audit.FromPrincipal(p, "", "", AuditActionKEKRewrapStart, AuditResourceKEK, "", "",
		audit.ResultOK, map[string]any{"started": started}))
	h.logger.Info("kek rewrap requested", slog.String("actor", p.Actor()), slog.Bool("started", started))
	return connect.NewResponse(&spinneretv1.StartKEKRewrapResponse{Started: started}), nil
}

func requireKEKManage(ctx context.Context) (*authz.Principal, error) {
	p, err := authz.MustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	// kek:manage is platform-only: the resource is irrelevant for platform
	// admins and every other principal is denied.
	if err := p.Require(authz.PermKEKManage, authz.Resource{TenantID: p.TenantID}); err != nil {
		return nil, err
	}
	return p, nil
}

// asInternal keeps application errors and hides everything else behind an
// internal error (the cause is kept for server logs).
func asInternal(err error) error {
	if _, ok := apperr.As(err); ok {
		return err
	}
	return apperr.Internal(err)
}
