package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// publishedPolicy is the current published version of one policy.
type publishedPolicy struct {
	id      string
	name    string
	kind    policy.Kind
	version int
	raw     json.RawMessage

	parsed bool
	spec   policy.Spec
	err    error
}

// compiledPolicy is the ready-to-use form of one resolved policy, shared by
// every endpoint group of the snapshot that resolves to it.
type compiledPolicy struct {
	rotation *policy.RotationSpec
	signal   *policy.CompiledSignal
	action   *policy.CompiledAction
	breaker  *policy.BreakerSpec
	err      error
}

type policyNameKey struct {
	kind policy.Kind
	name string
}

// policyResolver resolves and compiles the effective policies of every
// endpoint group of a namespace. Parsing and compilation results are cached per
// policy, so a policy bound at namespace level is compiled once. Any failure
// falls back to the built-in default of that kind and is logged once per policy.
type policyResolver struct {
	logger   *slog.Logger
	bindings []policy.BindingRow
	byID     map[string]*publishedPolicy
	byName   map[policyNameKey]*publishedPolicy
	compiled map[string]*compiledPolicy
	defaults map[policy.Kind]*compiledPolicy
}

func newPolicyResolver(raw *rawNamespace, logger *slog.Logger) *policyResolver {
	r := &policyResolver{
		logger:   logger.With(slog.String("namespace_id", raw.Namespace.ID)),
		byID:     make(map[string]*publishedPolicy, len(raw.Policies)),
		byName:   make(map[policyNameKey]*publishedPolicy, len(raw.Policies)),
		compiled: map[string]*compiledPolicy{},
		defaults: map[policy.Kind]*compiledPolicy{},
	}
	for _, row := range raw.Policies {
		kind := policy.Kind(row.Kind)
		if !policy.ValidKind(kind) {
			continue
		}
		p := &publishedPolicy{id: row.ID, name: row.Name, kind: kind, version: int(row.CurrentVersion), raw: row.Spec}
		r.byID[p.id] = p
		r.byName[policyNameKey{kind: kind, name: p.name}] = p
	}
	r.bindings = make([]policy.BindingRow, 0, len(raw.Bindings))
	for _, row := range raw.Bindings {
		kind := policy.Kind(row.Kind)
		p, ok := r.byID[row.PolicyID]
		if !ok || p.kind != kind {
			// Bindings of unpublished policies are not in effect; the next less
			// specific binding applies instead.
			continue
		}
		r.bindings = append(r.bindings, policy.BindingRow{
			PolicyID:        row.PolicyID,
			Kind:            kind,
			SiteID:          deref(row.SiteID),
			Client:          deref(row.Client),
			EndpointGroupID: deref(row.EndpointGroupID),
		})
	}
	return r
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// apply resolves the policies of every endpoint group of ns in place. It must
// run after identity types were added, because it also computes the eligible
// identity types of each group.
func (r *policyResolver) apply(ns *Namespace) {
	for _, s := range ns.SitesByID {
		for _, g := range s.GroupsByID {
			r.applyGroup(s, g)
		}
	}
}

func (r *policyResolver) applyGroup(s *Site, g *EndpointGroup) {
	for _, kind := range policy.Kinds() {
		c, ref := r.resolve(kind, s.ID, g.Client, g.ID)
		switch kind {
		case policy.KindRotation:
			g.Rotation, g.RotationRef = c.rotation, ref
		case policy.KindSignal:
			g.Signal, g.SignalRef = c.signal, ref
		case policy.KindAction:
			g.Action, g.ActionRef = c.action, ref
		case policy.KindBreaker:
			g.Breaker, g.BreakerRef = c.breaker, ref
		}
	}
	g.IdentityTypeIDs = r.identityTypeIDs(s, g)
}

// resolve returns the compiled policy of kind for an endpoint group and the
// reference describing it.
func (r *policyResolver) resolve(kind policy.Kind, siteID, client, groupID string) (*compiledPolicy, PolicyRef) {
	id, level := policy.Resolve(kind, r.bindings, siteID, client, groupID)
	if id != "" {
		p := r.byID[id]
		if c := r.compile(p); c.err == nil {
			return c, PolicyRef{PolicyID: p.id, Name: p.name, Version: p.version, Level: level}
		}
	}
	return r.builtin(kind), PolicyRef{Name: policy.DefaultPolicyName(kind), Level: policy.LevelBuiltin}
}

// parse decodes a published policy once.
func (r *policyResolver) parse(p *publishedPolicy) (policy.Spec, error) {
	if !p.parsed {
		p.parsed = true
		p.spec, p.err = policy.ParseJSON(p.kind, p.raw)
	}
	return p.spec, p.err
}

// compile parses, resolves extends and compiles a published policy once.
func (r *policyResolver) compile(p *publishedPolicy) *compiledPolicy {
	if c, ok := r.compiled[p.id]; ok {
		return c
	}
	c := r.compileUncached(p)
	if c.err != nil {
		r.logger.Warn("catalog: published policy is unusable, falling back to the built-in default",
			slog.String("policy_id", p.id), slog.String("policy", p.name), slog.String("kind", string(p.kind)),
			slog.Int("version", p.version), slog.Any("error", c.err))
	}
	r.compiled[p.id] = c
	return c
}

func (r *policyResolver) compileUncached(p *publishedPolicy) *compiledPolicy {
	spec, err := r.parse(p)
	if err != nil {
		return &compiledPolicy{err: err}
	}
	lookup := func(name string) (policy.Spec, bool) {
		parent, ok := r.byName[policyNameKey{kind: p.kind, name: name}]
		if !ok {
			return nil, false
		}
		ps, err := r.parse(parent)
		return ps, err == nil
	}
	return compileSpec(p.kind, spec, lookup)
}

// builtin returns the compiled built-in default of kind.
func (r *policyResolver) builtin(kind policy.Kind) *compiledPolicy {
	if c, ok := r.defaults[kind]; ok {
		return c
	}
	c := compileSpec(kind, policy.Default(kind), nil)
	if c.err != nil {
		// The built-in defaults are covered by the policy package tests; this
		// only guards against a broken build.
		r.logger.Error("catalog: built-in default policy does not compile",
			slog.String("kind", string(kind)), slog.Any("error", c.err))
	}
	r.defaults[kind] = c
	return c
}

// compileSpec resolves the extends chain of spec and compiles it.
func compileSpec(kind policy.Kind, spec policy.Spec, lookup func(string) (policy.Spec, bool)) *compiledPolicy {
	switch kind {
	case policy.KindRotation:
		s, ok := spec.(*policy.RotationSpec)
		if !ok || s == nil {
			return &compiledPolicy{err: errors.New("spec is not a rotation policy")}
		}
		return &compiledPolicy{rotation: s}
	case policy.KindBreaker:
		s, ok := spec.(*policy.BreakerSpec)
		if !ok || s == nil {
			return &compiledPolicy{err: errors.New("spec is not a breaker policy")}
		}
		return &compiledPolicy{breaker: s}
	case policy.KindSignal:
		chain, err := policy.ResolveExtends(kind, spec, lookup)
		if err != nil {
			return &compiledPolicy{err: err}
		}
		specs, err := policy.SignalChain(chain)
		if err != nil {
			return &compiledPolicy{err: err}
		}
		compiled, err := policy.CompileSignal(specs)
		return &compiledPolicy{signal: compiled, err: err}
	case policy.KindAction:
		chain, err := policy.ResolveExtends(kind, spec, lookup)
		if err != nil {
			return &compiledPolicy{err: err}
		}
		specs, err := policy.ActionChain(chain)
		if err != nil {
			return &compiledPolicy{err: err}
		}
		compiled, err := policy.CompileAction(specs)
		return &compiledPolicy{action: compiled, err: err}
	}
	return &compiledPolicy{err: fmt.Errorf("unknown policy kind %q", kind)}
}

// identityTypeIDs resolves rotation.identity_types to the IDs of the site's
// identity types of the group's client; an empty list selects every type of
// that client. Unknown names are logged and ignored.
func (r *policyResolver) identityTypeIDs(s *Site, g *EndpointGroup) []string {
	out := []string{}
	var names policy.StringList
	if g.Rotation != nil {
		names = g.Rotation.IdentityTypes
	}
	if len(names) == 0 {
		for _, t := range s.IdentityTypesByID {
			if t.Client == g.Client {
				out = append(out, t.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	for _, name := range names {
		t, ok := s.IdentityTypes[name]
		if !ok || t.Client != g.Client {
			r.logger.Warn("catalog: rotation policy names an identity type that does not exist for the client",
				slog.String("site_id", s.ID), slog.String("endpoint_group_id", g.ID),
				slog.String("client", g.Client), slog.String("identity_type", name),
				slog.String("policy", g.RotationRef.Name))
			continue
		}
		if !slices.Contains(out, t.ID) {
			out = append(out, t.ID)
		}
	}
	slices.Sort(out)
	return out
}
