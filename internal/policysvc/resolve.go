package policysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
)

// ResolvePolicies returns the effective policy of every kind, ordered
// rotation, signal, action, breaker (policy:read on the site, or anywhere in
// the namespace for namespace-level resolution). For an endpoint group the
// references come from the catalog snapshot (the policies actually in
// effect); for namespace, site and client levels they are resolved from the
// stored bindings of published policies, like the catalog does. Built-in
// defaults return policy.DefaultYAML, and so does a stored policy that no
// longer parses or whose extends chain no longer resolves (the catalog falls
// back to the built-in default for it as well). Signal and action YAML is
// flattened over the extends chain.
func (s *Service) ResolvePolicies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, t Target) ([]ResolvedPolicy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return nil, err
	}
	// Check access before resolving names so that callers without any
	// policy:read in the namespace cannot probe site and group names.
	read := accessFor(p, ns, authz.PermPolicyRead)
	if !read.any() {
		return nil, denied(p, ns, authz.PermPolicyRead)
	}
	target, err := resolveTarget(ns, t)
	if err != nil {
		return nil, err
	}
	if target.site != nil && !read.site(target.site.ID) {
		return nil, p.Require(authz.PermPolicyRead, siteResource(ns, target.site.ID))
	}
	q := policysvcdb.New(s.pool)
	refs, err := s.references(ctx, q, ns, target)
	if err != nil {
		return nil, err
	}
	out := make([]ResolvedPolicy, 0, len(refs))
	for i, kind := range policy.Kinds() {
		rp, err := s.resolvedYAML(ctx, q, ns.ID, kind, refs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, nil
}

// references returns one policy reference per kind in policy.Kinds() order.
func (s *Service) references(ctx context.Context, q *policysvcdb.Queries, ns *catalog.Namespace, t resolvedTarget) ([]catalog.PolicyRef, error) {
	if g := t.group; g != nil {
		return []catalog.PolicyRef{g.RotationRef, g.SignalRef, g.ActionRef, g.BreakerRef}, nil
	}
	rows, err := q.BindingListEffective(ctx, policysvcdb.BindingListEffectiveParams{
		NamespaceID: ns.ID, SiteID: t.siteID(), RowLimit: maxBindingRows + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("list effective policy bindings: %w", err)
	}
	if len(rows) > maxBindingRows {
		return nil, apperr.FailedPrecondition("", "too many policy bindings to resolve (more than %d)", maxBindingRows)
	}
	bindings := make([]policy.BindingRow, 0, len(rows))
	byID := make(map[string]policysvcdb.BindingListEffectiveRow, len(rows))
	for _, r := range rows {
		bindings = append(bindings, policy.BindingRow{
			PolicyID: r.PolicyID, Kind: policy.Kind(r.Kind),
			SiteID: deref(r.SiteID), Client: deref(r.Client), EndpointGroupID: deref(r.EndpointGroupID),
		})
		byID[r.PolicyID] = r
	}
	refs := make([]catalog.PolicyRef, 0, len(policy.Kinds()))
	for _, kind := range policy.Kinds() {
		id, level := policy.Resolve(kind, bindings, t.siteID(), t.client, "")
		r, ok := byID[id]
		if id == "" || !ok {
			refs = append(refs, builtinRef(kind))
			continue
		}
		refs = append(refs, catalog.PolicyRef{PolicyID: id, Name: r.PolicyName, Version: int(r.CurrentVersion), Level: level})
	}
	return refs, nil
}

// builtinRef references the built-in default of kind.
func builtinRef(kind policy.Kind) catalog.PolicyRef {
	return catalog.PolicyRef{Name: policy.DefaultPolicyName(kind), Level: policy.LevelBuiltin}
}

// builtinResolved returns the built-in default of kind.
func builtinResolved(kind policy.Kind) ResolvedPolicy {
	return ResolvedPolicy{
		Kind: kind, Name: policy.DefaultPolicyName(kind), Level: policy.LevelBuiltin, YAML: policy.DefaultYAML(kind),
	}
}

// resolvedYAML loads the effective YAML of a policy reference.
func (s *Service) resolvedYAML(ctx context.Context, q *policysvcdb.Queries, namespaceID string, kind policy.Kind, ref catalog.PolicyRef) (ResolvedPolicy, error) {
	if ref.PolicyID == "" {
		return builtinResolved(kind), nil
	}
	rp := ResolvedPolicy{Kind: kind, PolicyID: ref.PolicyID, Name: ref.Name, Version: ref.Version, Level: ref.Level}
	v, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: ref.PolicyID, Version: int32(ref.Version)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResolvedPolicy{}, apperr.Conflict("policy %q changed while resolving, retry", ref.Name)
		}
		return ResolvedPolicy{}, fmt.Errorf("load version %d of policy %s: %w", ref.Version, ref.PolicyID, err)
	}
	rp.YAML = v.SpecYaml
	unusable := func(err error) (ResolvedPolicy, error) {
		s.logger.Warn("policy: resolved policy is unusable, reporting the built-in default",
			slog.String("policy_id", ref.PolicyID), slog.String("kind", string(kind)),
			slog.Int("version", ref.Version), slog.Any("error", err))
		return builtinResolved(kind), nil
	}
	leaf, err := policy.ParseJSON(kind, v.Spec)
	if err != nil {
		return unusable(err)
	}
	if (kind != policy.KindSignal && kind != policy.KindAction) || policy.ParentName(leaf) == "" {
		return rp, nil
	}
	chain, err := resolveChain(ctx, q, namespaceID, leaf)
	if err != nil {
		if e, ok := apperr.As(err); ok {
			return unusable(e)
		}
		return ResolvedPolicy{}, err
	}
	flat, err := flattenYAML(kind, chain)
	if err != nil {
		return ResolvedPolicy{}, err
	}
	rp.YAML = flat
	return rp, nil
}

// flattenYAML renders a signal or action chain (root → leaf) as one policy:
// rules concatenated root first, settings of the leaf (escalation of the
// nearest policy that defines it), without extends and bind.
func flattenYAML(kind policy.Kind, chain []policy.Spec) (string, error) {
	names := make([]string, 0, len(chain))
	for _, sp := range chain {
		names = append(names, sp.PolicyName())
	}
	var flat policy.Spec
	switch kind {
	case policy.KindSignal:
		specs, err := policy.SignalChain(chain)
		if err != nil {
			return "", fmt.Errorf("flatten signal chain: %w", err)
		}
		leaf := *specs[len(specs)-1]
		leaf.Extends, leaf.Bind, leaf.Rules = "", nil, nil
		for _, sp := range specs {
			leaf.Rules = append(leaf.Rules, sp.Rules...)
		}
		flat = &leaf
	case policy.KindAction:
		specs, err := policy.ActionChain(chain)
		if err != nil {
			return "", fmt.Errorf("flatten action chain: %w", err)
		}
		leaf := *specs[len(specs)-1]
		leaf.Extends, leaf.Bind, leaf.Rules, leaf.Escalation = "", nil, nil, nil
		for _, sp := range specs {
			leaf.Rules = append(leaf.Rules, sp.Rules...)
		}
		for i := len(specs) - 1; i >= 0; i-- {
			if len(specs[i].Escalation) > 0 {
				leaf.Escalation = specs[i].Escalation
				break
			}
		}
		flat = &leaf
	default:
		return "", fmt.Errorf("flatten: kind %s has no extends", kind)
	}
	body, err := policy.MarshalYAML(flat)
	if err != nil {
		return "", fmt.Errorf("encode flattened %s policy: %w", kind, err)
	}
	header := "# Effective " + string(kind) + " policy: extends chain of " + strconv.Itoa(len(chain)) +
		" policies (root first): " + strings.Join(names, " -> ") + "\n# Rules are concatenated in chain order.\n"
	return header + string(body), nil
}
