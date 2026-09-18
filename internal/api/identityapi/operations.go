package identityapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/identitysvc"
)

// OperateIdentities implements IdentityAdminServiceHandler.
func (h *Handler) OperateIdentities(ctx context.Context, req *connect.Request[spinneretv1.OperateIdentitiesRequest]) (*connect.Response[spinneretv1.OperateIdentitiesResponse], error) {
	m := req.Msg
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	op, err := operationRequest(m.GetOperation(), m.GetScope(), m.GetEndpointGroupId(), m.GetDuration(), m.GetReason(),
		m.GetResetFailures(), m.GetResetHealth())
	if err != nil {
		return nil, err
	}
	res, err := h.svc.OperateIdentities(ctx, p, m.GetIds(), op)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.OperateIdentitiesResponse{Result: bulkResultProto(res)}), nil
}

// BulkOperateIdentities implements IdentityAdminServiceHandler.
func (h *Handler) BulkOperateIdentities(ctx context.Context, req *connect.Request[spinneretv1.BulkOperateIdentitiesRequest]) (*connect.Response[spinneretv1.BulkOperateIdentitiesResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	if m.GetFilter() == nil {
		return nil, apperr.InvalidArgument("", "filter is required")
	}
	op, err := operationRequest(m.GetOperation(), m.GetScope(), m.GetEndpointGroupId(), m.GetDuration(), m.GetReason(),
		m.GetResetFailures(), m.GetResetHealth())
	if err != nil {
		return nil, err
	}
	res, err := h.svc.BulkOperateIdentities(ctx, p, ns, filterFromProto(m.GetFilter()), op, int(m.GetLimit()), m.GetDryRun())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.BulkOperateIdentitiesResponse{Result: bulkResultProto(res)}), nil
}

// RevertActions implements IdentityAdminServiceHandler.
func (h *Handler) RevertActions(ctx context.Context, req *connect.Request[spinneretv1.RevertActionsRequest]) (*connect.Response[spinneretv1.RevertActionsResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	if m.GetTimeRange().GetStart() == nil {
		return nil, apperr.InvalidArgument("", "time_range.start is required")
	}
	from, to, err := timeRange(m.GetTimeRange())
	if err != nil {
		return nil, err
	}
	res, ids, err := h.svc.RevertActions(ctx, p, ns, identitysvc.RevertInput{
		Site: m.GetSite(),
		Request: identitysvc.RevertRequest{
			PolicyID: m.GetPolicyId(), Rule: m.GetRule(), Actions: m.GetActions(), From: from, To: to,
			ResetFailures: m.GetResetFailures(), ResetHealth: m.GetResetHealth(), DryRun: m.GetDryRun(),
		},
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RevertActionsResponse{Result: bulkResultProto(res), IdentityIds: ids}), nil
}

// ListAccounts implements IdentityAdminServiceHandler.
func (h *Handler) ListAccounts(ctx context.Context, req *connect.Request[spinneretv1.ListAccountsRequest]) (*connect.Response[spinneretv1.ListAccountsResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	page, err := h.svc.ListAccounts(ctx, p, ns, identitysvc.AccountQuery{
		Site: m.GetSite(), Search: m.GetSearch(), State: m.GetState(), PageSize: int(m.GetPageSize()), PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListAccountsResponse{
		Accounts:      make([]*spinneretv1.Account, 0, len(page.Accounts)),
		NextPageToken: page.NextPageToken,
		Total:         clampInt32(page.Total),
	}
	for _, a := range page.Accounts {
		out.Accounts = append(out.Accounts, accountProto(a))
	}
	return connect.NewResponse(out), nil
}

// UpsertAccount implements IdentityAdminServiceHandler.
func (h *Handler) UpsertAccount(ctx context.Context, req *connect.Request[spinneretv1.UpsertAccountRequest]) (*connect.Response[spinneretv1.UpsertAccountResponse], error) {
	m := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, m.GetNamespace())
	if err != nil {
		return nil, err
	}
	a, err := h.svc.UpsertAccount(ctx, p, ns, identitysvc.AccountUpsert{
		Site: m.GetSite(), ExternalRef: m.GetExternalRef(), Region: m.GetRegion(), Tags: m.GetTags(), Notes: m.GetNotes(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.UpsertAccountResponse{Account: accountProto(a)}), nil
}

// OperateAccount implements IdentityAdminServiceHandler.
func (h *Handler) OperateAccount(ctx context.Context, req *connect.Request[spinneretv1.OperateAccountRequest]) (*connect.Response[spinneretv1.OperateAccountResponse], error) {
	m := req.Msg
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	op, err := operationRequest(m.GetOperation(), "", "", m.GetDuration(), m.GetReason(), false, false)
	if err != nil {
		return nil, err
	}
	a, res, err := h.svc.OperateAccount(ctx, p, m.GetId(), op)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.OperateAccountResponse{Account: accountProto(a), Identities: bulkResultProto(res)}), nil
}

// operationRequest builds an operation from request fields.
func operationRequest(operation, scope, groupID, duration, reason string, resetFailures, resetHealth bool) (identitysvc.OperationRequest, error) {
	d, err := apiutil.Duration("duration", duration, true)
	if err != nil {
		return identitysvc.OperationRequest{}, err
	}
	return identitysvc.OperationRequest{
		Operation: operation, Scope: scope, EndpointGroupID: groupID, Duration: d, Reason: reason,
		ResetFailures: resetFailures, ResetHealth: resetHealth,
	}, nil
}
