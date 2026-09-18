// Package reportapi implements the node-facing ReportService Connect handler.
package reportapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/signal"
)

// Ingester ingests report batches (provided by *signal.Ingestor).
type Ingester interface {
	Ingest(ctx context.Context, p *authz.Principal, node string, reports []*spinneretv1.Report) (accepted, duplicated int, rejected []signal.Rejected, err error)
}

// Handler implements spinneretv1connect.ReportServiceHandler.
//
// Reports are data-plane traffic and are not written to the audit log (spec
// §10 audits admin mutations); per-node rejection counts go to statistics.
type Handler struct {
	ingest Ingester
	cat    catalog.Catalog
}

var _ spinneretv1connect.ReportServiceHandler = (*Handler)(nil)

// New creates the ReportService handler.
func New(ingest Ingester, cat catalog.Catalog) *Handler {
	return &Handler{ingest: ingest, cat: cat}
}

// Report ingests a batch of reports. The caller needs report:write on at least
// one site; every report is additionally authorized against its lease site
// and rejected individually (scope_missing) when that site is not granted.
func (h *Handler) Report(ctx context.Context, req *connect.Request[spinneretv1.ReportRequest]) (*connect.Response[spinneretv1.ReportResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.authorize(p); err != nil {
		return nil, err
	}
	reports := req.Msg.GetReports()
	if len(reports) == 0 {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at least one report is required")
	}
	if len(reports) > signal.MaxReports {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d reports per request", signal.MaxReports)
	}
	accepted, duplicated, rejected, err := h.ingest.Ingest(ctx, p, p.Node, reports)
	if err != nil {
		return nil, err
	}
	resp := &spinneretv1.ReportResponse{
		Accepted:   int32(accepted),   //nolint:gosec // bounded by MaxReports
		Duplicated: int32(duplicated), //nolint:gosec // bounded by MaxReports
		Rejected:   make([]*spinneretv1.RejectedReport, 0, len(rejected)),
	}
	for _, r := range rejected {
		resp.Rejected = append(resp.Rejected, &spinneretv1.RejectedReport{
			ReportId: r.ReportID,
			Reason:   string(r.Reason),
			Message:  r.Message,
		})
	}
	return connect.NewResponse(resp), nil
}

// authorize requires report:write somewhere: tokens on their namespace or on
// one of its sites; users only as platform admins (report:write is node-only).
func (h *Handler) authorize(p *authz.Principal) error {
	if p.Kind != authz.KindToken {
		return p.Require(authz.PermReportWrite, authz.Resource{TenantID: p.TenantID})
	}
	ns, ok := h.cat.Namespace(p.NamespaceID)
	if !ok {
		return p.Require(authz.PermReportWrite, authz.Resource{
			TenantID: p.TenantID, NamespaceID: p.NamespaceID, NamespaceName: p.NamespaceName,
		})
	}
	res := apiutil.NamespaceResource(ns)
	if p.Can(authz.PermReportWrite, res) {
		return nil
	}
	for _, s := range ns.SitesByID {
		if p.Can(authz.PermReportWrite, apiutil.SiteResource(ns, s)) {
			return nil
		}
	}
	return p.Require(authz.PermReportWrite, res)
}
