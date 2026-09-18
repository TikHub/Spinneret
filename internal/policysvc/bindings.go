package policysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
)

// resolvedTarget is a binding or resolution target resolved against the
// catalog snapshot.
type resolvedTarget struct {
	level  policy.Level
	site   *catalog.Site
	client string
	group  *catalog.EndpointGroup
}

func (t resolvedTarget) siteID() string {
	if t.site == nil {
		return ""
	}
	return t.site.ID
}

func (t resolvedTarget) groupID() string {
	if t.group == nil {
		return ""
	}
	return t.group.ID
}

// resource returns the authorization resource of the target level.
func (t resolvedTarget) resource(ns *catalog.Namespace) authz.Resource {
	if t.site == nil {
		return nsResource(ns)
	}
	return siteResource(ns, t.site.ID)
}

// resolveTarget resolves site, client and endpoint group names.
func resolveTarget(ns *catalog.Namespace, t Target) (resolvedTarget, error) {
	switch {
	case t.Client != "" && t.Site == "":
		return resolvedTarget{}, apperr.InvalidArgument("", "client requires site")
	case t.EndpointGroup != "" && t.Client == "":
		return resolvedTarget{}, apperr.InvalidArgument("", "endpoint_group requires client")
	case t.Site == "":
		return resolvedTarget{level: policy.LevelNamespace}, nil
	}
	s, ok := ns.Sites[t.Site]
	if !ok {
		return resolvedTarget{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", t.Site)
	}
	out := resolvedTarget{level: policy.LevelSite, site: s}
	if t.Client == "" {
		return out, nil
	}
	if !s.HasClient(t.Client) {
		return resolvedTarget{}, apperr.InvalidArgument(apperr.ReasonClientUnknown, "site %q has no client %q", s.Name, t.Client)
	}
	out.level, out.client = policy.LevelClient, t.Client
	if t.EndpointGroup == "" {
		return out, nil
	}
	g, ok := s.Group(t.Client, t.EndpointGroup)
	if !ok {
		return resolvedTarget{}, apperr.InvalidArgument(apperr.ReasonEndpointGroupUnknown,
			"endpoint group %q not found for site %q client %q", t.EndpointGroup, s.Name, t.Client)
	}
	out.level, out.group = policy.LevelEndpointGroup, g
	return out, nil
}

// upsertBinding binds row at target, replacing a binding of the same kind at
// that target. changed is false when the target was already bound to row.
func upsertBinding(ctx context.Context, q *policysvcdb.Queries, p *authz.Principal, row policysvcdb.Policy, t resolvedTarget) (policysvcdb.PolicyBinding, bool, error) {
	prev, err := q.BindingGetByTarget(ctx, policysvcdb.BindingGetByTargetParams{
		NamespaceID: row.NamespaceID, Kind: row.Kind, SiteID: t.siteID(), Client: t.client, EndpointGroupID: t.groupID(),
	})
	exists := true
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		exists = false
	case err != nil:
		return policysvcdb.PolicyBinding{}, false, fmt.Errorf("load binding target: %w", err)
	}
	stored, err := q.BindingUpsert(ctx, policysvcdb.BindingUpsertParams{
		ID: idgen.New(idgen.PolicyBinding), PolicyID: row.ID, Kind: row.Kind, NamespaceID: row.NamespaceID,
		SiteID: ptr(t.siteID()), Client: ptr(t.client), EndpointGroupID: ptr(t.groupID()), CreatedBy: p.Actor(),
	})
	if err != nil {
		return policysvcdb.PolicyBinding{}, false, pgstore.MapError(err, "policy binding")
	}
	return stored, !exists || prev.PolicyID != row.ID, nil
}

// ListBindings lists the bindings of a namespace visible to the caller
// (policy:read). kind "" matches every kind; site "" matches every accessible
// site, otherwise the namespace-level bindings and those of that site.
func (s *Service) ListBindings(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, kind policy.Kind, siteName string) ([]Binding, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return nil, err
	}
	if err := validKindFilter(kind); err != nil {
		return nil, err
	}
	read := accessFor(p, ns, authz.PermPolicyRead)
	if !read.any() {
		return nil, denied(p, ns, authz.PermPolicyRead)
	}
	siteID := ""
	if siteName != "" {
		site, ok := ns.Sites[siteName]
		if !ok {
			return nil, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site %q not found", siteName)
		}
		if !read.site(site.ID) {
			return nil, p.Require(authz.PermPolicyRead, siteResource(ns, site.ID))
		}
		siteID = site.ID
	}
	rows, err := policysvcdb.New(s.pool).BindingListByNamespace(ctx, policysvcdb.BindingListByNamespaceParams{
		NamespaceID: ns.ID, Kind: string(kind), SiteID: siteID, RowLimit: maxBindingRows + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list policy bindings: %w", err)
	}
	if len(rows) > maxBindingRows {
		s.logger.Warn("policy: binding list truncated",
			slog.String("namespace_id", ns.ID), slog.Int("limit", maxBindingRows))
		rows = rows[:maxBindingRows]
	}
	return visibleBindings(ns, read, fromNamespaceRows(rows)), nil
}

// SetBinding binds a published policy at the target level, replacing the
// binding of the same kind at that level (policy:publish on the target, and
// the policy must be visible to the caller).
func (s *Service) SetBinding(ctx context.Context, p *authz.Principal, policyID string, t Target) (Binding, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return Binding{}, err
	}
	l, err := s.loadVisiblePolicy(ctx, p, policyID)
	if err != nil {
		if l.ns != nil {
			s.record(ctx, p, l.ns, AuditBindingSet, resourcePolicy, l.row.ID, l.row.Name, audit.ResultDenied,
				map[string]any{"permission": string(authz.PermPolicyRead)})
		}
		return Binding{}, err
	}
	target, err := resolveTarget(l.ns, t)
	if err != nil {
		return Binding{}, err
	}
	if err := s.authorize(ctx, p, l.ns, authz.PermPolicyPublish, target.resource(l.ns), AuditBindingSet, resourcePolicy, l.row.ID, l.row.Name); err != nil {
		return Binding{}, err
	}
	if l.row.CurrentVersion == 0 {
		return Binding{}, apperr.FailedPrecondition("", "policy %q has no published version and cannot be bound", l.row.Name)
	}
	eff := effects{ns: l.ns, kind: policy.Kind(l.row.Kind)}
	var stored policysvcdb.PolicyBinding
	var changed bool
	err = s.inTx(ctx, func(q *policysvcdb.Queries) error {
		// Lock the policy so it cannot be deleted while it is being bound.
		locked, err := lockPolicy(ctx, q, policyID)
		if err != nil {
			return err
		}
		l.row = locked
		stored, changed, err = upsertBinding(ctx, q, p, l.row, target)
		return err
	})
	if err != nil {
		return Binding{}, err
	}
	if changed {
		eff.addSite(stored.SiteID)
	}
	b := toBinding(l.ns, fromStoredBinding(stored, l.row.Name))
	s.record(ctx, p, l.ns, AuditBindingSet, resourcePolicyBinding, b.ID, l.row.Name, audit.ResultOK, bindingDetails(b))
	eff.event = &PublishedEvent{
		PolicyID: l.row.ID, Name: l.row.Name, Kind: l.row.Kind, Version: int(l.row.CurrentVersion),
		Action: EventActionBind, BindingID: b.ID, SiteID: b.SiteID, Actor: p.Actor(),
	}
	s.apply(ctx, eff)
	return b, nil
}

// DeleteBinding deletes a binding (policy:publish on its target). Deleting
// the namespace-level binding of a kind makes the built-in default apply.
func (s *Service) DeleteBinding(ctx context.Context, p *authz.Principal, id string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return err
	}
	if id == "" {
		return apperr.InvalidArgument("", "binding id is required")
	}
	q := policysvcdb.New(s.pool)
	row, err := q.BindingGet(ctx, id)
	if err != nil {
		return pgstore.MapError(err, "policy binding")
	}
	ns, err := s.namespace(row.NamespaceID, "policy binding")
	if err != nil {
		return err
	}
	br := fromGetRow(row)
	res := nsResource(ns)
	if br.siteID() != "" {
		res = siteResource(ns, br.siteID())
	}
	if err := s.authorize(ctx, p, ns, authz.PermPolicyPublish, res, AuditBindingDelete, resourcePolicyBinding, row.ID, row.PolicyName); err != nil {
		return err
	}
	n, err := q.BindingDelete(ctx, policysvcdb.BindingDeleteParams{ID: id, PolicyID: row.PolicyID})
	if err != nil {
		return fmt.Errorf("delete policy binding %s: %w", id, err)
	}
	if n == 0 {
		// Gone, or its target was rebound to another policy meanwhile: the
		// caller saw a different binding than the one that exists now.
		if _, err := q.BindingGet(ctx, id); err != nil {
			return pgstore.MapError(err, "policy binding")
		}
		return apperr.Conflict("policy binding %s changed concurrently, reload and retry", id)
	}
	b := toBinding(ns, br)
	s.record(ctx, p, ns, AuditBindingDelete, resourcePolicyBinding, b.ID, b.PolicyName, audit.ResultOK, bindingDetails(b))
	eff := effects{ns: ns, kind: b.Kind, event: &PublishedEvent{
		PolicyID: b.PolicyID, Name: b.PolicyName, Kind: string(b.Kind), Action: EventActionUnbind,
		BindingID: b.ID, SiteID: b.SiteID, Actor: p.Actor(),
	}}
	eff.addSite(br.SiteID)
	s.apply(ctx, eff)
	return nil
}

func bindingDetails(b Binding) map[string]any {
	return map[string]any{
		"policy_id":         b.PolicyID,
		"kind":              string(b.Kind),
		"level":             string(b.Level),
		"site_id":           b.SiteID,
		"client":            b.Client,
		"endpoint_group_id": b.EndpointGroupID,
	}
}
