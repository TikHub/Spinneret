// Package identityapi implements the IdentityAdminService Connect handler on
// top of identitysvc. Handlers resolve the namespace of the request for the
// calling principal, convert messages and delegate to the service, which
// enforces authorization on every call (identity types: site:write to
// mutate, site:read or identity:read to read; identities and accounts:
// identity:read / identity:write / identity:operate / identity:reveal on the
// object's site).
package identityapi

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/identitysvc"
)

// Handler implements spinneretv1connect.IdentityAdminServiceHandler.
type Handler struct {
	cat    catalog.Catalog
	svc    *identitysvc.Service
	hot    identitysvc.HotReader
	logger *slog.Logger
}

var _ spinneretv1connect.IdentityAdminServiceHandler = (*Handler)(nil)

// New creates the identity admin handler. hot may be nil, in which case hot
// state is reported as unavailable (GetIdentityHotState) or omitted
// (GetIdentity).
func New(cat catalog.Catalog, svc *identitysvc.Service, hot identitysvc.HotReader, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{cat: cat, svc: svc, hot: hot, logger: logger.With(slog.String("component", "identityapi"))}
}

// ListIdentityTypes implements IdentityAdminServiceHandler.
func (h *Handler) ListIdentityTypes(ctx context.Context, req *connect.Request[spinneretv1.ListIdentityTypesRequest]) (*connect.Response[spinneretv1.ListIdentityTypesResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListIdentityTypes(ctx, p, ns, identitysvc.TypeQuery{
		Site: m.GetSite(), Client: m.GetClient(), Search: m.GetSearch(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListIdentityTypesResponse{
		IdentityTypes: make([]*spinneretv1.IdentityType, 0, len(page.Types)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(page.Total),
	}
	for _, t := range page.Types {
		out.IdentityTypes = append(out.IdentityTypes, identityTypeProto(t))
	}
	return connect.NewResponse(out), nil
}

// GetIdentityType implements IdentityAdminServiceHandler.
func (h *Handler) GetIdentityType(ctx context.Context, req *connect.Request[spinneretv1.GetIdentityTypeRequest]) (*connect.Response[spinneretv1.GetIdentityTypeResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.GetIdentityType(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetIdentityTypeResponse{IdentityType: identityTypeProto(t)}), nil
}

// CreateIdentityType implements IdentityAdminServiceHandler.
func (h *Handler) CreateIdentityType(ctx context.Context, req *connect.Request[spinneretv1.CreateIdentityTypeRequest]) (*connect.Response[spinneretv1.CreateIdentityTypeResponse], error) {
	p, ns, err := apiutil.Namespace(ctx, h.cat, req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	t, err := h.svc.CreateIdentityType(ctx, p, ns, req.Msg.GetSite(), req.Msg.GetSpecYaml())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreateIdentityTypeResponse{IdentityType: identityTypeProto(t)}), nil
}

// UpdateIdentityType implements IdentityAdminServiceHandler.
func (h *Handler) UpdateIdentityType(ctx context.Context, req *connect.Request[spinneretv1.UpdateIdentityTypeRequest]) (*connect.Response[spinneretv1.UpdateIdentityTypeResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	t, err := h.svc.UpdateIdentityType(ctx, p, req.Msg.GetId(), req.Msg.GetSpecYaml())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpdateIdentityTypeResponse{IdentityType: identityTypeProto(t)}), nil
}

// DeleteIdentityType implements IdentityAdminServiceHandler.
func (h *Handler) DeleteIdentityType(ctx context.Context, req *connect.Request[spinneretv1.DeleteIdentityTypeRequest]) (*connect.Response[spinneretv1.DeleteIdentityTypeResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteIdentityType(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteIdentityTypeResponse{}), nil
}

// PreviewDelivery implements IdentityAdminServiceHandler.
func (h *Handler) PreviewDelivery(ctx context.Context, req *connect.Request[spinneretv1.PreviewDeliveryRequest]) (*connect.Response[spinneretv1.PreviewDeliveryResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	in := identitysvc.PreviewInput{PayloadJSON: m.GetPayloadJson()}
	switch src := m.GetSource().(type) {
	case *spinneretv1.PreviewDeliveryRequest_TypeId:
		in.TypeID = src.TypeId
	case *spinneretv1.PreviewDeliveryRequest_SpecYaml:
		in.SpecYAML = src.SpecYaml
		in.Site = m.GetSite()
	default:
		return nil, apperr.InvalidArgument("", "one of type_id and spec_yaml is required")
	}
	res, err := h.svc.PreviewDelivery(ctx, p, ns, in)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.PreviewDeliveryResponse{Errors: res.Errors}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	if res.Normalized != nil {
		if out.NormalizedPayload, err = apiutil.Struct(res.Normalized); err != nil {
			return nil, err
		}
	}
	if res.Credential != nil {
		cred, err := credentialProto(res.Credential)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
		} else {
			out.Credential = cred
		}
	}
	return connect.NewResponse(out), nil
}

// clampInt32 converts a count to int32 without overflow.
func clampInt32(n int) int32 {
	const maxInt32 = 1<<31 - 1
	switch {
	case n > maxInt32:
		return maxInt32
	case n < 0:
		return 0
	default:
		return int32(n)
	}
}
