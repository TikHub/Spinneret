package policysvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
)

// publishedLookup returns an extends lookup over the published policies of a
// namespace and kind. Database failures are reported through the returned
// error function because policy.ResolveExtends lookups cannot fail.
func publishedLookup(ctx context.Context, q *policysvcdb.Queries, namespaceID string, kind policy.Kind) (func(string) (policy.Spec, bool), func() error) {
	var lookupErr error
	lookup := func(name string) (policy.Spec, bool) {
		if lookupErr != nil {
			return nil, false
		}
		row, err := q.PolicyPublishedSpecByName(ctx, policysvcdb.PolicyPublishedSpecByNameParams{
			NamespaceID: namespaceID, Kind: string(kind), Name: name,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false
		}
		if err != nil {
			lookupErr = fmt.Errorf("load published %s policy %q: %w", kind, name, err)
			return nil, false
		}
		spec, err := policy.ParseJSON(kind, row.Spec)
		if err != nil {
			lookupErr = apperr.FailedPrecondition("", "published %s policy %q (version %d) is no longer valid: %v",
				kind, name, row.CurrentVersion, err).WithCause(err)
			return nil, false
		}
		return spec, true
	}
	return lookup, func() error { return lookupErr }
}

// resolveChain resolves the extends chain of leaf among published policies
// (root → leaf). Invalid chains are InvalidArgument errors.
func resolveChain(ctx context.Context, q *policysvcdb.Queries, namespaceID string, leaf policy.Spec) ([]policy.Spec, error) {
	lookup, lookupErr := publishedLookup(ctx, q, namespaceID, leaf.Kind())
	chain, err := policy.ResolveExtends(leaf.Kind(), leaf, lookup)
	if lerr := lookupErr(); lerr != nil {
		return nil, lerr
	}
	if err != nil {
		return nil, apperr.InvalidArgument("", "%s", err.Error()).WithCause(err)
	}
	return chain, nil
}

// compileChain compiles a signal or action chain; other kinds are no-ops.
func compileChain(kind policy.Kind, chain []policy.Spec) (*policy.CompiledSignal, *policy.CompiledAction, error) {
	switch kind {
	case policy.KindSignal:
		specs, err := policy.SignalChain(chain)
		if err != nil {
			return nil, nil, apperr.InvalidArgument("", "%s", err.Error())
		}
		c, err := policy.CompileSignal(specs)
		if err != nil {
			return nil, nil, apperr.InvalidArgument("", "%s", err.Error()).WithCause(err)
		}
		return c, nil, nil
	case policy.KindAction:
		specs, err := policy.ActionChain(chain)
		if err != nil {
			return nil, nil, apperr.InvalidArgument("", "%s", err.Error())
		}
		c, err := policy.CompileAction(specs)
		if err != nil {
			return nil, nil, apperr.InvalidArgument("", "%s", err.Error()).WithCause(err)
		}
		return nil, c, nil
	}
	return nil, nil, nil
}

// checkExtends verifies, for signal and action policies, that the extends
// chain of spec resolves among published policies and compiles, and that no
// published descendant would exceed the maximum chain depth once spec is
// published. The caller holds the namespace+kind advisory lock.
func checkExtends(ctx context.Context, q *policysvcdb.Queries, namespaceID string, spec policy.Spec) error {
	kind := spec.Kind()
	if kind != policy.KindSignal && kind != policy.KindAction {
		return nil
	}
	chain, err := resolveChain(ctx, q, namespaceID, spec)
	if err != nil {
		return err
	}
	if _, _, err := compileChain(kind, chain); err != nil {
		return err
	}
	rows, err := q.PolicyPublishedExtends(ctx, policysvcdb.PolicyPublishedExtendsParams{
		NamespaceID: namespaceID, Kind: string(kind), RowLimit: maxPublishedScan + 1,
	})
	if err != nil {
		return fmt.Errorf("list published %s policies: %w", kind, err)
	}
	if len(rows) > maxPublishedScan {
		return apperr.FailedPrecondition("", "too many published %s policies to validate extends chains", kind)
	}
	children := make(map[string][]string, len(rows))
	for _, r := range rows {
		if r.Extends != "" && r.Name != spec.PolicyName() {
			children[r.Extends] = append(children[r.Extends], r.Name)
		}
	}
	depth, deepest := descendantDepth(children, spec.PolicyName())
	if len(chain)+depth > policy.MaxExtendsDepth {
		return apperr.InvalidArgument("",
			"publishing %s policy %q would make the extends chain of %q longer than %d policies",
			kind, spec.PolicyName(), deepest, policy.MaxExtendsDepth)
	}
	return nil
}

// descendantDepth returns the length of the longest descendant path below
// root (0 without descendants) and the name at its end. The search is bounded
// by MaxExtendsDepth levels and ignores already visited names.
func descendantDepth(children map[string][]string, root string) (int, string) {
	visited := map[string]bool{root: true}
	level := []string{root}
	depth, deepest := 0, ""
	for len(level) > 0 && depth <= policy.MaxExtendsDepth {
		var next []string
		for _, name := range level {
			for _, child := range children[name] {
				if visited[child] {
					continue
				}
				visited[child] = true
				next = append(next, child)
			}
		}
		if len(next) == 0 {
			break
		}
		slices.Sort(next)
		depth++
		deepest = next[0]
		level = next
	}
	return depth, deepest
}

// identityTypes returns the sorted identity types of a rotation spec.
func identityTypes(spec policy.Spec) []string {
	r, ok := spec.(*policy.RotationSpec)
	if !ok || r == nil {
		return nil
	}
	out := slices.Clone([]string(r.IdentityTypes))
	slices.Sort(out)
	return slices.Compact(out)
}
