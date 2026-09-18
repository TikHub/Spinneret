package policysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
	pgstore "github.com/TikHub/Spinneret/internal/store/postgres"
)

// publishRequest is one publication of a parsed spec as the next version.
type publishRequest struct {
	p          *authz.Principal
	ns         *catalog.Namespace
	row        policysvcdb.Policy // the caller holds the kind lock and the row lock
	spec       policy.Spec
	comment    string
	clearDraft bool
	action     string // audit action used for denied bind checks
}

// publishResult reports what a publication changed.
type publishResult struct {
	row     policysvcdb.Policy
	binding *policysvcdb.PolicyBinding
}

// publishTx publishes req.spec as version current+1 inside tx: it validates
// the name and extends chain, applies the bind block, stores the version,
// updates the policy row and records the post-commit effects. The caller must
// hold the namespace+kind lock (lockKind) and the row lock.
//
// A bind block is applied on the first publication and whenever it differs
// from the bind block of the current version. Republishing an unchanged bind
// block therefore never reverts binding changes made since with SetBinding or
// DeleteBinding.
func (s *Service) publishTx(ctx context.Context, q *policysvcdb.Queries, req publishRequest, eff *effects) (publishResult, error) {
	row := req.row
	kind := policy.Kind(row.Kind)
	if req.spec.PolicyName() != row.Name {
		return publishResult{}, apperr.InvalidArgument("", "policy name cannot be changed from %q to %q", row.Name, req.spec.PolicyName())
	}
	if row.CurrentVersion == math.MaxInt32 {
		return publishResult{}, apperr.FailedPrecondition("", "policy %q has reached the maximum number of versions", row.Name)
	}
	if err := checkExtends(ctx, q, row.NamespaceID, req.spec); err != nil {
		return publishResult{}, err
	}
	prev, err := s.previousSpec(ctx, q, row)
	if err != nil {
		return publishResult{}, err
	}
	res := publishResult{}
	if bind := req.spec.PolicyBinding(); bind != nil && (prev == nil || !sameBinding(prev.PolicyBinding(), bind)) {
		target, err := resolveTarget(req.ns, Target{Site: bind.Site, Client: bind.Client, EndpointGroup: bind.EndpointGroup})
		if err != nil {
			return publishResult{}, err
		}
		if err := s.authorize(ctx, req.p, req.ns, authz.PermPolicyPublish, target.resource(req.ns), req.action, resourcePolicy, row.ID, row.Name); err != nil {
			return publishResult{}, err
		}
		stored, changed, err := upsertBinding(ctx, q, req.p, row, target)
		if err != nil {
			return publishResult{}, err
		}
		res.binding = &stored
		if changed {
			eff.addSite(stored.SiteID)
		}
	}
	specJSON, err := policy.MarshalJSON(req.spec)
	if err != nil {
		return publishResult{}, fmt.Errorf("encode policy %s: %w", row.ID, err)
	}
	specYAML, err := policy.MarshalYAML(req.spec)
	if err != nil {
		return publishResult{}, fmt.Errorf("encode policy %s: %w", row.ID, err)
	}
	version := row.CurrentVersion + 1
	if err := q.PolicyVersionInsert(ctx, policysvcdb.PolicyVersionInsertParams{
		PolicyID: row.ID, Version: version, Spec: specJSON, SpecYaml: string(specYAML),
		Comment: req.comment, CreatedBy: req.p.Actor(),
	}); err != nil {
		return publishResult{}, pgstore.MapError(err, "policy version")
	}
	res.row, err = q.PolicySetPublished(ctx, policysvcdb.PolicySetPublishedParams{
		Version: version, Description: specDescription(req.spec), ClearDraft: req.clearDraft, ID: row.ID,
	})
	if err != nil {
		return publishResult{}, pgstore.MapError(err, "policy")
	}
	if kind == policy.KindRotation && (prev == nil || !slices.Equal(identityTypes(prev), identityTypes(req.spec))) {
		if err := addPolicySites(ctx, q, row.ID, eff); err != nil {
			return publishResult{}, err
		}
	}
	return res, nil
}

// previousSpec returns the spec of the current published version. It returns
// nil for an unpublished policy and for a stored version that no longer
// parses; callers then treat every setting derived from it as changed.
func (s *Service) previousSpec(ctx context.Context, q *policysvcdb.Queries, row policysvcdb.Policy) (policy.Spec, error) {
	if row.CurrentVersion == 0 {
		return nil, nil
	}
	v, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: row.ID, Version: row.CurrentVersion})
	if err != nil {
		return nil, fmt.Errorf("load version %d of policy %s: %w", row.CurrentVersion, row.ID, err)
	}
	spec, err := policy.ParseJSON(policy.Kind(row.Kind), v.Spec)
	if err != nil {
		s.logger.Warn("policy: stored version does not parse",
			slog.String("policy_id", row.ID), slog.Int("version", int(row.CurrentVersion)), slog.Any("error", err))
		return nil, nil
	}
	return spec, nil
}

// sameBinding reports whether two bind blocks name the same target.
func sameBinding(a, b *policy.Binding) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// addPolicySites records the sites bound to a policy as needing a resync.
func addPolicySites(ctx context.Context, q *policysvcdb.Queries, policyID string, eff *effects) error {
	rows, err := q.BindingListByPolicies(ctx, policysvcdb.BindingListByPoliciesParams{PolicyIds: []string{policyID}, RowLimit: maxBindingRows})
	if err != nil {
		return fmt.Errorf("list bindings of policy %s: %w", policyID, err)
	}
	for _, b := range rows {
		eff.addSite(b.SiteID)
	}
	return nil
}

// specDescription returns the description of a spec.
func specDescription(spec policy.Spec) string {
	switch s := spec.(type) {
	case *policy.RotationSpec:
		return s.Description
	case *policy.SignalSpec:
		return s.Description
	case *policy.ActionSpec:
		return s.Description
	case *policy.BreakerSpec:
		return s.Description
	}
	return ""
}

// parseSpec parses and validates policy YAML into an InvalidArgument error on failure.
func parseSpec(kind policy.Kind, yamlText string) (policy.Spec, error) {
	if err := requireKind(kind); err != nil {
		return nil, err
	}
	if yamlText == "" {
		return nil, apperr.InvalidArgument("", "yaml is required")
	}
	if len(yamlText) > MaxYAMLBytes {
		return nil, apperr.InvalidArgument("", "yaml must be at most %d bytes", MaxYAMLBytes)
	}
	spec, err := policy.ParseYAML(kind, []byte(yamlText))
	if err != nil {
		return nil, apperr.InvalidArgument("", "%s", err.Error()).WithCause(err)
	}
	return spec, nil
}

// validateComment bounds a publish comment, counted in characters like
// protovalidate max_len.
func validateComment(comment string) error {
	if utf8.RuneCountInString(comment) > MaxCommentLength {
		return apperr.InvalidArgument("", "comment must be at most %d characters", MaxCommentLength)
	}
	return nil
}

// lockKind takes the transaction-scoped advisory lock serializing publish,
// rollback and delete operations of one namespace and kind. Every path takes
// it before any policy row lock so lock ordering is consistent.
func lockKind(ctx context.Context, q *policysvcdb.Queries, namespaceID, kind string) error {
	if err := q.PolicyLockKind(ctx, policysvcdb.PolicyLockKindParams{NamespaceID: namespaceID, Kind: kind}); err != nil {
		return fmt.Errorf("lock %s policies: %w", kind, err)
	}
	return nil
}

// lockPolicy loads a policy row FOR UPDATE.
func lockPolicy(ctx context.Context, q *policysvcdb.Queries, id string) (policysvcdb.Policy, error) {
	row, err := q.PolicyGetForUpdate(ctx, id)
	if err != nil {
		return policysvcdb.Policy{}, pgstore.MapError(err, "policy")
	}
	return row, nil
}

// PublishPolicy validates the draft of a policy and publishes it as a new
// version (policy:publish). For signal and action policies the extends chain
// must resolve among published policies and compile. A bind block that is new
// or differs from the current version creates or replaces the binding at its
// target.
func (s *Service) PublishPolicy(ctx context.Context, p *authz.Principal, in PublishInput) (Policy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return Policy{}, err
	}
	if err := validateComment(in.Comment); err != nil {
		return Policy{}, err
	}
	if in.ExpectedVersion < 0 {
		return Policy{}, apperr.InvalidArgument("", "expected_version must not be negative")
	}
	row, ns, err := s.loadPolicy(ctx, policysvcdb.New(s.pool), in.ID)
	if err != nil {
		return Policy{}, err
	}
	if err := s.authorize(ctx, p, ns, authz.PermPolicyPublish, nsResource(ns), AuditPolicyPublish, resourcePolicy, row.ID, row.Name); err != nil {
		return Policy{}, err
	}
	eff := effects{ns: ns, kind: policy.Kind(row.Kind)}
	var res publishResult
	err = s.inTx(ctx, func(q *policysvcdb.Queries) error {
		if err := lockKind(ctx, q, row.NamespaceID, row.Kind); err != nil {
			return err
		}
		locked, err := lockPolicy(ctx, q, in.ID)
		if err != nil {
			return err
		}
		if in.ExpectedVersion > 0 && int(locked.CurrentVersion) != in.ExpectedVersion {
			return apperr.Conflict("policy %q is at version %d, expected %d", locked.Name, locked.CurrentVersion, in.ExpectedVersion)
		}
		if locked.DraftYaml == nil {
			return apperr.FailedPrecondition("", "policy %q has no draft to publish", locked.Name)
		}
		spec, err := parseSpec(policy.Kind(locked.Kind), *locked.DraftYaml)
		if err != nil {
			return err
		}
		res, err = s.publishTx(ctx, q, publishRequest{
			p: p, ns: ns, row: locked, spec: spec, comment: in.Comment, clearDraft: true, action: AuditPolicyPublish,
		}, &eff)
		return err
	})
	if err != nil {
		return Policy{}, err
	}
	// Read the result before the post-commit effects, which may take long.
	out, err := s.GetPolicy(ctx, p, in.ID)
	s.finishPublish(ctx, p, ns, res, &eff, AuditPolicyPublish, EventActionPublish, map[string]any{"comment": in.Comment})
	return out, err
}

// RollbackPolicy publishes the YAML of an old version as a new version
// (policy:publish). The draft is kept.
func (s *Service) RollbackPolicy(ctx context.Context, p *authz.Principal, id string, version int, comment string) (Policy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return Policy{}, err
	}
	if err := validateComment(comment); err != nil {
		return Policy{}, err
	}
	if version < 1 || version > math.MaxInt32 {
		return Policy{}, apperr.InvalidArgument("", "version must be between 1 and %d", math.MaxInt32)
	}
	row, ns, err := s.loadPolicy(ctx, policysvcdb.New(s.pool), id)
	if err != nil {
		return Policy{}, err
	}
	if err := s.authorize(ctx, p, ns, authz.PermPolicyPublish, nsResource(ns), AuditPolicyRollback, resourcePolicy, row.ID, row.Name); err != nil {
		return Policy{}, err
	}
	eff := effects{ns: ns, kind: policy.Kind(row.Kind)}
	var res publishResult
	err = s.inTx(ctx, func(q *policysvcdb.Queries) error {
		if err := lockKind(ctx, q, row.NamespaceID, row.Kind); err != nil {
			return err
		}
		locked, err := lockPolicy(ctx, q, id)
		if err != nil {
			return err
		}
		if int(locked.CurrentVersion) == version {
			return apperr.FailedPrecondition("", "version %d of policy %q is already current", version, locked.Name)
		}
		old, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: id, Version: int32(version)})
		if err != nil {
			return pgstore.MapError(err, fmt.Sprintf("version %d of policy %q", version, locked.Name))
		}
		spec, err := policy.ParseJSON(policy.Kind(locked.Kind), old.Spec)
		if err != nil {
			return apperr.FailedPrecondition("", "version %d of policy %q is no longer valid: %v", version, locked.Name, err).WithCause(err)
		}
		res, err = s.publishTx(ctx, q, publishRequest{
			p: p, ns: ns, row: locked, spec: spec, comment: comment, action: AuditPolicyRollback,
		}, &eff)
		return err
	})
	if err != nil {
		return Policy{}, err
	}
	out, err := s.GetPolicy(ctx, p, id)
	s.finishPublish(ctx, p, ns, res, &eff, AuditPolicyRollback, EventActionRollback,
		map[string]any{"comment": comment, "from_version": version})
	return out, err
}

// finishPublish writes the audit entry and applies post-commit effects.
func (s *Service) finishPublish(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, res publishResult, eff *effects, auditAction, eventAction string, details map[string]any) {
	details["version"] = res.row.CurrentVersion
	eff.event = &PublishedEvent{
		PolicyID: res.row.ID, Name: res.row.Name, Kind: res.row.Kind, Version: int(res.row.CurrentVersion),
		Action: eventAction, Actor: p.Actor(),
	}
	if res.binding != nil {
		details["binding_id"] = res.binding.ID
		eff.event.BindingID, eff.event.SiteID = res.binding.ID, deref(res.binding.SiteID)
	}
	s.record(ctx, p, ns, auditAction, resourcePolicy, res.row.ID, res.row.Name, audit.ResultOK, details)
	s.apply(ctx, *eff)
}

// inTx runs fn in a transaction with the private queries.
func (s *Service) inTx(ctx context.Context, fn func(q *policysvcdb.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer s.rollback(ctx, tx)
	if err := fn(policysvcdb.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return pgstore.MapError(fmt.Errorf("commit transaction: %w", err), "policy")
	}
	return nil
}

// rollback rolls back tx, ignoring cancellation of ctx. After a commit it is
// a no-op (pgx.ErrTxClosed); a failed rollback only means pgx discards the
// connection, so the original error is what matters to the caller.
func (s *Service) rollback(ctx context.Context, tx pgx.Tx) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		s.logger.Warn("policy: rollback transaction", "error", err)
	}
}
