// Package policyapi implements the PolicyAdminService Connect handler on top
// of internal/policysvc. Authorization, auditing, catalog invalidation and
// events are handled by the service; the handler resolves the caller and the
// namespace, converts messages and encodes pagination cursors.
package policyapi

import (
	"context"

	"connectrpc.com/connect"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/TikHub/Spinneret/internal/api/apiutil"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc"
)

// Handler implements spinneretv1connect.PolicyAdminServiceHandler.
type Handler struct {
	svc *policysvc.Service
	cat catalog.Catalog
}

var _ spinneretv1connect.PolicyAdminServiceHandler = (*Handler)(nil)

// New creates the handler.
func New(svc *policysvc.Service, cat catalog.Catalog) *Handler {
	return &Handler{svc: svc, cat: cat}
}

// policyCursor is the ListPolicies page token.
type policyCursor struct {
	Kind string `json:"k"`
	Name string `json:"n"`
}

// versionCursor is the ListPolicyVersions page token.
type versionCursor struct {
	Before int `json:"b"`
}

// ListPolicies lists policies of a namespace (policy:read).
func (h *Handler) ListPolicies(ctx context.Context, req *connect.Request[spinneretv1.ListPoliciesRequest]) (*connect.Response[spinneretv1.ListPoliciesResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	var cur policyCursor
	if _, err := apiutil.DecodeCursor(msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	page, err := h.svc.ListPolicies(ctx, p, ns, policysvc.ListPoliciesInput{
		Kind: policy.Kind(msg.GetKind()), Search: msg.GetSearch(), PageSize: apiutil.PageSize(msg.GetPageSize()),
		AfterKind: cur.Kind, AfterName: cur.Name,
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListPoliciesResponse{
		Policies: make([]*spinneretv1.Policy, 0, len(page.Policies)),
		Total:    clampInt32(page.Total),
	}
	for _, pol := range page.Policies {
		out.Policies = append(out.Policies, toPolicy(pol))
	}
	if page.More && len(page.Policies) > 0 {
		last := page.Policies[len(page.Policies)-1]
		if out.NextPageToken, err = apiutil.EncodeCursor(policyCursor{Kind: string(last.Kind), Name: last.Name}); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	return connect.NewResponse(out), nil
}

// GetPolicy returns one policy with its bindings (policy:read).
func (h *Handler) GetPolicy(ctx context.Context, req *connect.Request[spinneretv1.GetPolicyRequest]) (*connect.Response[spinneretv1.GetPolicyResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	pol, err := h.svc.GetPolicy(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.GetPolicyResponse{Policy: toPolicy(pol)}), nil
}

// CreatePolicy creates a policy from YAML (policy:write, policy:publish to publish).
func (h *Handler) CreatePolicy(ctx context.Context, req *connect.Request[spinneretv1.CreatePolicyRequest]) (*connect.Response[spinneretv1.CreatePolicyResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	pol, err := h.svc.CreatePolicy(ctx, p, ns, policysvc.CreateInput{
		Kind: policy.Kind(msg.GetKind()), YAML: msg.GetYaml(), Publish: msg.GetPublish(), Comment: msg.GetComment(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.CreatePolicyResponse{Policy: toPolicy(pol)}), nil
}

// SaveDraft stores draft YAML (policy:write).
func (h *Handler) SaveDraft(ctx context.Context, req *connect.Request[spinneretv1.SaveDraftRequest]) (*connect.Response[spinneretv1.SaveDraftResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	pol, err := h.svc.SaveDraft(ctx, p, req.Msg.GetId(), req.Msg.GetYaml())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.SaveDraftResponse{Policy: toPolicy(pol)}), nil
}

// PublishPolicy publishes the draft as a new version (policy:publish).
func (h *Handler) PublishPolicy(ctx context.Context, req *connect.Request[spinneretv1.PublishPolicyRequest]) (*connect.Response[spinneretv1.PublishPolicyResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	pol, err := h.svc.PublishPolicy(ctx, p, policysvc.PublishInput{
		ID: msg.GetId(), Comment: msg.GetComment(), ExpectedVersion: int(msg.GetExpectedVersion()),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.PublishPolicyResponse{Policy: toPolicy(pol)}), nil
}

// RollbackPolicy publishes an old version as a new version (policy:publish).
func (h *Handler) RollbackPolicy(ctx context.Context, req *connect.Request[spinneretv1.RollbackPolicyRequest]) (*connect.Response[spinneretv1.RollbackPolicyResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	pol, err := h.svc.RollbackPolicy(ctx, p, msg.GetId(), int(msg.GetVersion()), msg.GetComment())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.RollbackPolicyResponse{Policy: toPolicy(pol)}), nil
}

// DeletePolicy deletes a policy and its bindings (policy:write and policy:publish).
func (h *Handler) DeletePolicy(ctx context.Context, req *connect.Request[spinneretv1.DeletePolicyRequest]) (*connect.Response[spinneretv1.DeletePolicyResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeletePolicy(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeletePolicyResponse{}), nil
}

// ListPolicyVersions lists published versions, newest first (policy:read).
func (h *Handler) ListPolicyVersions(ctx context.Context, req *connect.Request[spinneretv1.ListPolicyVersionsRequest]) (*connect.Response[spinneretv1.ListPolicyVersionsResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	var cur versionCursor
	if _, err := apiutil.DecodeCursor(msg.GetPageToken(), &cur); err != nil {
		return nil, err
	}
	page, err := h.svc.ListPolicyVersions(ctx, p, msg.GetId(), apiutil.PageSize(msg.GetPageSize()), cur.Before)
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListPolicyVersionsResponse{
		Versions: make([]*spinneretv1.PolicyVersion, 0, len(page.Versions)),
		Total:    clampInt32(page.Total),
	}
	for _, v := range page.Versions {
		out.Versions = append(out.Versions, toVersion(v))
	}
	if page.More && len(page.Versions) > 0 {
		if out.NextPageToken, err = apiutil.EncodeCursor(versionCursor{Before: page.Versions[len(page.Versions)-1].Version}); err != nil {
			return nil, apperr.Internal(err)
		}
	}
	return connect.NewResponse(out), nil
}

// DiffPolicyVersions compares two versions or a version and the draft (policy:read).
func (h *Handler) DiffPolicyVersions(ctx context.Context, req *connect.Request[spinneretv1.DiffPolicyVersionsRequest]) (*connect.Response[spinneretv1.DiffPolicyVersionsResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	d, err := h.svc.DiffPolicyVersions(ctx, p, msg.GetId(), int(msg.GetFromVersion()), int(msg.GetToVersion()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DiffPolicyVersionsResponse{
		FromYaml: d.FromYAML, ToYaml: d.ToYAML, UnifiedDiff: d.UnifiedDiff,
	}), nil
}

// ListBindings lists policy bindings (policy:read).
func (h *Handler) ListBindings(ctx context.Context, req *connect.Request[spinneretv1.ListBindingsRequest]) (*connect.Response[spinneretv1.ListBindingsResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	bindings, err := h.svc.ListBindings(ctx, p, ns, policy.Kind(msg.GetKind()), msg.GetSite())
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ListBindingsResponse{Bindings: make([]*spinneretv1.PolicyBinding, 0, len(bindings))}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, toBinding(b))
	}
	return connect.NewResponse(out), nil
}

// SetBinding binds a policy at a level, replacing the binding of the same kind there (policy:publish).
func (h *Handler) SetBinding(ctx context.Context, req *connect.Request[spinneretv1.SetBindingRequest]) (*connect.Response[spinneretv1.SetBindingResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	b, err := h.svc.SetBinding(ctx, p, msg.GetPolicyId(), policysvc.Target{
		Site: msg.GetSite(), Client: msg.GetClient(), EndpointGroup: msg.GetEndpointGroup(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.SetBindingResponse{Binding: toBinding(b)}), nil
}

// DeleteBinding deletes a policy binding (policy:publish).
func (h *Handler) DeleteBinding(ctx context.Context, req *connect.Request[spinneretv1.DeleteBindingRequest]) (*connect.Response[spinneretv1.DeleteBindingResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.DeleteBinding(ctx, p, req.Msg.GetId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.DeleteBindingResponse{}), nil
}

// ResolvePolicies returns the effective policy of every kind (policy:read).
func (h *Handler) ResolvePolicies(ctx context.Context, req *connect.Request[spinneretv1.ResolvePoliciesRequest]) (*connect.Response[spinneretv1.ResolvePoliciesResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	resolved, err := h.svc.ResolvePolicies(ctx, p, ns, policysvc.Target{
		Site: msg.GetSite(), Client: msg.GetClient(), EndpointGroup: msg.GetEndpointGroup(),
	})
	if err != nil {
		return nil, err
	}
	out := &spinneretv1.ResolvePoliciesResponse{Policies: make([]*spinneretv1.ResolvedPolicy, 0, len(resolved))}
	for _, r := range resolved {
		out.Policies = append(out.Policies, &spinneretv1.ResolvedPolicy{
			Kind: string(r.Kind), PolicyId: r.PolicyID, Name: r.Name, Version: clampInt32(r.Version),
			Level: string(r.Level), Yaml: r.YAML,
		})
	}
	return connect.NewResponse(out), nil
}

// DebugReport classifies a sample report and plans actions without side effects (policy:read).
func (h *Handler) DebugReport(ctx context.Context, req *connect.Request[spinneretv1.DebugReportRequest]) (*connect.Response[spinneretv1.DebugReportResponse], error) {
	msg := req.Msg
	p, ns, err := apiutil.Namespace(ctx, h.cat, msg.GetNamespace())
	if err != nil {
		return nil, err
	}
	in, err := debugInput(msg)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.DebugReport(ctx, p, ns, in)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toDebugResponse(res)), nil
}

// ValidatePolicy validates policy YAML and returns its normalized form (policy:read).
func (h *Handler) ValidatePolicy(ctx context.Context, req *connect.Request[spinneretv1.ValidatePolicyRequest]) (*connect.Response[spinneretv1.ValidatePolicyResponse], error) {
	p, err := apiutil.Principal(ctx)
	if err != nil {
		return nil, err
	}
	res, err := h.svc.ValidatePolicy(p, policy.Kind(req.Msg.GetKind()), req.Msg.GetYaml())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&spinneretv1.ValidatePolicyResponse{
		Valid: res.Valid, Errors: res.Errors, NormalizedYaml: res.NormalizedYAML,
	}), nil
}
