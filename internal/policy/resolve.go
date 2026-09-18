package policy

import (
	"errors"
	"fmt"
	"strings"
)

// BindingRow is one policy binding: SiteID "" binds at namespace level,
// Client "" and EndpointGroupID "" widen the binding to the whole site.
type BindingRow struct {
	PolicyID        string
	Kind            Kind
	SiteID          string
	Client          string
	EndpointGroupID string
}

// Level is the specificity at which a policy was resolved.
type Level string

// Resolution levels, most specific first.
const (
	LevelEndpointGroup Level = "endpoint_group"
	LevelClient        Level = "client"
	LevelSite          Level = "site"
	LevelNamespace     Level = "namespace"
	LevelBuiltin       Level = "builtin"
)

// Resolve picks the most specific binding of kind for an endpoint group:
// endpoint group (matching EndpointGroupID; the row's site and client, when
// set, must match too) > client (site + client, no endpoint group) > site
// (site only) > namespace (no site, client or endpoint group). Rows with a
// client or endpoint group but no site never match. When several rows share
// the winning level the first one wins. It returns ("", LevelBuiltin) when no
// binding applies.
func Resolve(kind Kind, bindings []BindingRow, siteID, client, endpointGroupID string) (policyID string, level Level) {
	best, bestRank := -1, rankNone
	for i := range bindings {
		b := &bindings[i]
		if b.Kind != kind {
			continue
		}
		rank := bindingRank(b, siteID, client, endpointGroupID)
		if rank > bestRank {
			best, bestRank = i, rank
		}
	}
	switch bestRank {
	case rankEndpointGroup:
		return bindings[best].PolicyID, LevelEndpointGroup
	case rankClient:
		return bindings[best].PolicyID, LevelClient
	case rankSite:
		return bindings[best].PolicyID, LevelSite
	case rankNamespace:
		return bindings[best].PolicyID, LevelNamespace
	}
	return "", LevelBuiltin
}

// Binding specificity ranks; higher is more specific.
const (
	rankNone = iota
	rankNamespace
	rankSite
	rankClient
	rankEndpointGroup
)

// bindingRank returns the specificity rank of a row for the target, or
// rankNone when the row does not apply.
func bindingRank(b *BindingRow, siteID, client, endpointGroupID string) int {
	switch {
	case b.SiteID == "":
		if b.Client == "" && b.EndpointGroupID == "" {
			return rankNamespace
		}
		return rankNone
	case b.SiteID != siteID:
		return rankNone
	case b.EndpointGroupID != "":
		if endpointGroupID != "" && b.EndpointGroupID == endpointGroupID && (b.Client == "" || b.Client == client) {
			return rankEndpointGroup
		}
		return rankNone
	case b.Client != "":
		if client != "" && b.Client == client {
			return rankClient
		}
		return rankNone
	}
	return rankSite
}

// ResolveExtends resolves the extends chain of leaf and returns it ordered
// root → leaf. lookup finds a policy of the same kind and namespace by name.
// Only signal and action policies support extends; other kinds return
// [leaf]. Missing parents, kind mismatches, cycles and chains longer than
// MaxExtendsDepth policies are errors.
func ResolveExtends(kind Kind, leaf Spec, lookup func(name string) (Spec, bool)) ([]Spec, error) {
	if isNilSpec(leaf) {
		return nil, errors.New("resolve extends: leaf policy is nil")
	}
	if leaf.Kind() != kind {
		return nil, fmt.Errorf("resolve extends: policy %q has kind %s, expected %s", leaf.PolicyName(), leaf.Kind(), kind)
	}
	if kind != KindSignal && kind != KindAction {
		return []Spec{leaf}, nil
	}
	chain := []Spec{leaf}
	names := []string{leaf.PolicyName()}
	cur := leaf
	for parent := ParentName(cur); parent != ""; parent = ParentName(cur) {
		for _, n := range names {
			if n == parent {
				return nil, fmt.Errorf("resolve extends: cycle detected: %s -> %s", strings.Join(names, " -> "), parent)
			}
		}
		if len(chain) >= MaxExtendsDepth {
			return nil, fmt.Errorf("resolve extends: chain %s -> %s exceeds the maximum depth of %d policies",
				strings.Join(names, " -> "), parent, MaxExtendsDepth)
		}
		if lookup == nil {
			return nil, fmt.Errorf("resolve extends: policy %q extends %q but no lookup was provided", cur.PolicyName(), parent)
		}
		next, ok := lookup(parent)
		if !ok || isNilSpec(next) {
			return nil, fmt.Errorf("resolve extends: policy %q extends unknown %s policy %q", cur.PolicyName(), kind, parent)
		}
		if next.Kind() != kind {
			return nil, fmt.Errorf("resolve extends: policy %q extends %q, which has kind %s, expected %s",
				cur.PolicyName(), parent, next.Kind(), kind)
		}
		if next.PolicyName() != parent {
			return nil, fmt.Errorf("resolve extends: lookup of %q returned policy %q", parent, next.PolicyName())
		}
		chain = append(chain, next)
		names = append(names, parent)
		cur = next
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}
