package policysvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvcdb"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// CreatePolicy creates a policy from YAML (policy:write). The name,
// description and bind block come from the YAML; the YAML is stored as the
// draft. With Publish (policy:publish) the draft is published as version 1 in
// the same transaction and a bind block becomes a binding.
func (s *Service) CreatePolicy(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in CreateInput) (Policy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return Policy{}, err
	}
	if err := validateComment(in.Comment); err != nil {
		return Policy{}, err
	}
	if err := s.authorize(ctx, p, ns, authz.PermPolicyWrite, nsResource(ns), AuditPolicyCreate, resourcePolicy, "", ""); err != nil {
		return Policy{}, err
	}
	if in.Publish {
		if err := s.authorize(ctx, p, ns, authz.PermPolicyPublish, nsResource(ns), AuditPolicyCreate, resourcePolicy, "", ""); err != nil {
			return Policy{}, err
		}
	}
	spec, err := parseSpec(in.Kind, in.YAML)
	if err != nil {
		return Policy{}, err
	}
	name := spec.PolicyName()
	if !policy.ValidName(name) {
		return Policy{}, apperr.InvalidArgument("", "policy name must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	eff := effects{ns: ns, kind: in.Kind}
	var row policysvcdb.Policy
	var res publishResult
	err = s.inTx(ctx, func(q *policysvcdb.Queries) error {
		if in.Publish {
			if err := lockKind(ctx, q, ns.ID, string(in.Kind)); err != nil {
				return err
			}
		}
		created, err := q.PolicyInsert(ctx, policysvcdb.PolicyInsertParams{
			ID: idgen.New(idgen.Policy), NamespaceID: ns.ID, Kind: string(in.Kind), Name: name,
			Description: specDescription(spec), DraftYaml: in.YAML, CreatedBy: p.Actor(),
		})
		if err != nil {
			return pgstore.MapError(err, fmt.Sprintf("%s policy %q", in.Kind, name))
		}
		row = created
		if !in.Publish {
			return nil
		}
		res, err = s.publishTx(ctx, q, publishRequest{
			p: p, ns: ns, row: created, spec: spec, comment: in.Comment, clearDraft: true, action: AuditPolicyCreate,
		}, &eff)
		return err
	})
	if err != nil {
		return Policy{}, err
	}
	details := map[string]any{"kind": string(in.Kind), "published": in.Publish}
	if !in.Publish {
		s.record(ctx, p, ns, AuditPolicyCreate, resourcePolicy, row.ID, row.Name, audit.ResultOK, details)
		return s.GetPolicy(ctx, p, row.ID)
	}
	details["comment"] = in.Comment
	// Read the result before the post-commit effects, which may take long.
	out, err := s.GetPolicy(ctx, p, row.ID)
	s.finishPublish(ctx, p, ns, res, &eff, AuditPolicyCreate, EventActionPublish, details)
	return out, err
}

// SaveDraft stores draft YAML (policy:write). The YAML must parse and
// validate as a policy of the same kind and name; extends chains and bind
// targets are checked on publish.
func (s *Service) SaveDraft(ctx context.Context, p *authz.Principal, id, yamlText string) (Policy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return Policy{}, err
	}
	q := policysvcdb.New(s.pool)
	row, ns, err := s.loadPolicy(ctx, q, id)
	if err != nil {
		return Policy{}, err
	}
	if err := s.authorize(ctx, p, ns, authz.PermPolicyWrite, nsResource(ns), AuditPolicySaveDraft, resourcePolicy, row.ID, row.Name); err != nil {
		return Policy{}, err
	}
	spec, err := parseSpec(policy.Kind(row.Kind), yamlText)
	if err != nil {
		return Policy{}, err
	}
	if spec.PolicyName() != row.Name {
		return Policy{}, apperr.InvalidArgument("", "policy name cannot be changed from %q to %q", row.Name, spec.PolicyName())
	}
	if _, err := q.PolicySaveDraft(ctx, policysvcdb.PolicySaveDraftParams{
		DraftYaml: yamlText, Actor: p.Actor(), Description: specDescription(spec), ID: row.ID,
	}); err != nil {
		return Policy{}, pgstore.MapError(err, "policy")
	}
	s.record(ctx, p, ns, AuditPolicySaveDraft, resourcePolicy, row.ID, row.Name, audit.ResultOK,
		map[string]any{"kind": row.Kind, "bytes": len(yamlText)})
	return s.GetPolicy(ctx, p, row.ID)
}

// DeletePolicy deletes a policy and its bindings (policy:write and
// policy:publish). It is refused while published policies extend it and for
// a built-in default policy (default-<kind>) that is bound at namespace level.
func (s *Service) DeletePolicy(ctx context.Context, p *authz.Principal, id string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return err
	}
	row, ns, err := s.loadPolicy(ctx, policysvcdb.New(s.pool), id)
	if err != nil {
		return err
	}
	for _, perm := range []authz.Permission{authz.PermPolicyWrite, authz.PermPolicyPublish} {
		if err := s.authorize(ctx, p, ns, perm, nsResource(ns), AuditPolicyDelete, resourcePolicy, row.ID, row.Name); err != nil {
			return err
		}
	}
	eff := effects{ns: ns, kind: policy.Kind(row.Kind)}
	var bindings int
	err = s.inTx(ctx, func(q *policysvcdb.Queries) error {
		if err := lockKind(ctx, q, row.NamespaceID, row.Kind); err != nil {
			return err
		}
		locked, err := lockPolicy(ctx, q, id)
		if err != nil {
			return err
		}
		row = locked
		children, err := q.PolicyPublishedChildren(ctx, policysvcdb.PolicyPublishedChildrenParams{
			NamespaceID: locked.NamespaceID, Kind: locked.Kind, Name: locked.Name, ID: locked.ID,
		})
		if err != nil {
			return fmt.Errorf("list policies extending %s: %w", locked.ID, err)
		}
		if len(children) > 0 {
			return apperr.FailedPrecondition("", "policy %q is extended by published policies: %s",
				locked.Name, strings.Join(children, ", "))
		}
		if locked.Name == policy.DefaultPolicyName(policy.Kind(locked.Kind)) {
			bound, err := q.BindingNamespaceLevelExists(ctx, locked.ID)
			if err != nil {
				return fmt.Errorf("check namespace binding of %s: %w", locked.ID, err)
			}
			if bound {
				return apperr.FailedPrecondition("",
					"default policy %q is bound at namespace level; bind another policy or delete the binding first", locked.Name)
			}
		}
		rows, err := q.BindingListByPolicies(ctx, policysvcdb.BindingListByPoliciesParams{PolicyIds: []string{locked.ID}, RowLimit: maxBindingRows})
		if err != nil {
			return fmt.Errorf("list bindings of policy %s: %w", locked.ID, err)
		}
		for _, b := range rows {
			eff.addSite(b.SiteID)
		}
		bindings = len(rows)
		n, err := q.PolicyDelete(ctx, locked.ID)
		if err != nil {
			return pgstore.MapError(err, "policy")
		}
		if n == 0 {
			return apperr.NotFound("policy not found")
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.record(ctx, p, ns, AuditPolicyDelete, resourcePolicy, row.ID, row.Name, audit.ResultOK,
		map[string]any{"kind": row.Kind, "bindings": bindings, "version": row.CurrentVersion})
	eff.event = &PublishedEvent{
		PolicyID: row.ID, Name: row.Name, Kind: row.Kind, Version: int(row.CurrentVersion),
		Action: EventActionDelete, Actor: p.Actor(),
	}
	s.apply(ctx, eff)
	return nil
}
