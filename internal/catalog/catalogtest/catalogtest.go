// Package catalogtest provides an in-memory catalog.Catalog and snapshot
// builders for tests of packages that consume catalog snapshots.
package catalogtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// Catalog is a thread-safe in-memory catalog. Reload, ReloadAll and
// Invalidate only record the call and notify OnChange listeners.
type Catalog struct {
	mu        sync.RWMutex
	byID      map[string]*catalog.Namespace
	listeners map[int]func(string)
	nextID    int
	calls     []string
}

var _ catalog.Catalog = (*Catalog)(nil)

// New creates a catalog holding the given namespace snapshots.
func New(namespaces ...*catalog.Namespace) *Catalog {
	c := &Catalog{byID: map[string]*catalog.Namespace{}, listeners: map[int]func(string){}}
	for _, ns := range namespaces {
		c.Put(ns)
	}
	return c
}

// Put adds or replaces a namespace snapshot.
func (c *Catalog) Put(ns *catalog.Namespace) {
	c.mu.Lock()
	c.byID[ns.ID] = ns
	c.mu.Unlock()
}

// Remove deletes a namespace snapshot.
func (c *Catalog) Remove(id string) {
	c.mu.Lock()
	delete(c.byID, id)
	c.mu.Unlock()
}

// Calls returns the recorded Reload/ReloadAll/Invalidate calls, e.g. "invalidate:ns_1".
func (c *Catalog) Calls() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.calls...)
}

// Namespace implements catalog.Catalog.
func (c *Catalog) Namespace(id string) (*catalog.Namespace, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ns, ok := c.byID[id]
	return ns, ok
}

// NamespaceByName implements catalog.Catalog.
func (c *Catalog) NamespaceByName(tenantID, name string) (*catalog.Namespace, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ns := range c.byID {
		if ns.TenantID == tenantID && ns.Name == name {
			return ns, true
		}
	}
	return nil, false
}

// Namespaces implements catalog.Catalog.
func (c *Catalog) Namespaces(tenantID string) []*catalog.Namespace {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*catalog.Namespace, 0, len(c.byID))
	for _, ns := range c.byID {
		if tenantID == "" || ns.TenantID == tenantID {
			out = append(out, ns)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Site implements catalog.Catalog.
func (c *Catalog) Site(id string) (*catalog.Site, *catalog.Namespace, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ns := range c.byID {
		if s, ok := ns.SitesByID[id]; ok {
			return s, ns, true
		}
	}
	return nil, nil, false
}

// SiteByKey implements catalog.Catalog.
func (c *Catalog) SiteByKey(key int64) (*catalog.Site, *catalog.Namespace, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, ns := range c.byID {
		for _, s := range ns.SitesByID {
			if s.Key == key {
				return s, ns, true
			}
		}
	}
	return nil, nil, false
}

// Reload implements catalog.Catalog.
func (c *Catalog) Reload(_ context.Context, namespaceID string) error {
	c.record("reload:" + namespaceID)
	c.notify(namespaceID)
	return nil
}

// ReloadAll implements catalog.Catalog.
func (c *Catalog) ReloadAll(context.Context) error {
	c.record("reload_all")
	return nil
}

// Invalidate implements catalog.Catalog.
func (c *Catalog) Invalidate(_ context.Context, namespaceID string) error {
	c.record("invalidate:" + namespaceID)
	c.notify(namespaceID)
	return nil
}

// OnChange implements catalog.Catalog.
func (c *Catalog) OnChange(fn func(namespaceID string)) func() {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.listeners[id] = fn
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.listeners, id)
		c.mu.Unlock()
	}
}

func (c *Catalog) record(call string) {
	c.mu.Lock()
	c.calls = append(c.calls, call)
	c.mu.Unlock()
}

func (c *Catalog) notify(namespaceID string) {
	c.mu.RLock()
	fns := make([]func(string), 0, len(c.listeners))
	for _, fn := range c.listeners {
		fns = append(fns, fn)
	}
	c.mu.RUnlock()
	for _, fn := range fns {
		fn(namespaceID)
	}
}

// NewNamespace creates an empty namespace snapshot.
func NewNamespace(tenantID, id, name string) *catalog.Namespace {
	return &catalog.Namespace{
		ID:         id,
		TenantID:   tenantID,
		TenantName: tenantID,
		Name:       name,
		Sites:      map[string]*catalog.Site{},
		SitesByID:  map[string]*catalog.Site{},
		Version:    1,
		LoadedAt:   time.Now(),
	}
}

// Policies bundles compiled policies for an endpoint group.
type Policies struct {
	Rotation *policy.RotationSpec
	Signal   *policy.CompiledSignal
	Action   *policy.CompiledAction
	Breaker  *policy.BreakerSpec
}

// DefaultPolicies returns the built-in default policies, compiled.
func DefaultPolicies() Policies {
	rot := policy.Default(policy.KindRotation).(*policy.RotationSpec)
	sig, err := policy.CompileSignal([]*policy.SignalSpec{policy.Default(policy.KindSignal).(*policy.SignalSpec)})
	if err != nil {
		panic(fmt.Sprintf("catalogtest: compile default signal: %v", err))
	}
	act, err := policy.CompileAction([]*policy.ActionSpec{policy.Default(policy.KindAction).(*policy.ActionSpec)})
	if err != nil {
		panic(fmt.Sprintf("catalogtest: compile default action: %v", err))
	}
	brk := policy.Default(policy.KindBreaker).(*policy.BreakerSpec)
	return Policies{Rotation: rot, Signal: sig, Action: act, Breaker: brk}
}

// AddSite adds a site with a "_default" endpoint group per client. Default
// group IDs are "<siteID>_<client>_default" and their keys are key*1000+index.
func AddSite(ns *catalog.Namespace, id, name string, key int64, clients ...string) *catalog.Site {
	if len(clients) == 0 {
		clients = []string{"web"}
	}
	s := &catalog.Site{
		ID:                id,
		Name:              name,
		DisplayName:       name,
		Key:               key,
		NamespaceID:       ns.ID,
		NamespaceName:     ns.Name,
		TenantID:          ns.TenantID,
		Clients:           append([]string(nil), clients...),
		Groups:            map[catalog.GroupKey]*catalog.EndpointGroup{},
		GroupsByID:        map[string]*catalog.EndpointGroup{},
		GroupsByKey:       map[int64]*catalog.EndpointGroup{},
		Matchers:          map[string]*site.Matcher{},
		IdentityTypes:     map[string]*identity.CompiledType{},
		IdentityTypesByID: map[string]*identity.CompiledType{},
	}
	ns.Sites[name] = s
	ns.SitesByID[id] = s
	for i, c := range clients {
		AddGroup(s, fmt.Sprintf("%s_%s_default", id, c), c, site.DefaultGroup, key*1000+int64(i))
	}
	return s
}

// AddGroup adds an endpoint group with default policies and optional URI
// rules (GroupID/GroupName are filled in) and rebuilds the client's matcher.
func AddGroup(s *catalog.Site, id, client, name string, key int64, rules ...site.Rule) *catalog.EndpointGroup {
	p := DefaultPolicies()
	g := &catalog.EndpointGroup{
		ID:          id,
		Name:        name,
		Client:      client,
		Key:         key,
		SiteID:      s.ID,
		SiteKey:     s.Key,
		Rotation:    p.Rotation,
		RotationRef: builtinRef(policy.KindRotation),
		Signal:      p.Signal,
		SignalRef:   builtinRef(policy.KindSignal),
		Action:      p.Action,
		ActionRef:   builtinRef(policy.KindAction),
		Breaker:     p.Breaker,
		BreakerRef:  builtinRef(policy.KindBreaker),
	}
	s.Groups[catalog.GroupKey{Client: client, Name: name}] = g
	s.GroupsByID[id] = g
	s.GroupsByKey[key] = g
	groupRulesMu.Lock()
	groupRules[s] = appendRules(groupRules[s], id, name, rules)
	siteRules := append([]site.Rule(nil), groupRules[s]...)
	groupRulesMu.Unlock()
	rebuildMatcher(s, client, siteRules)
	return g
}

// SetPolicies replaces the policies of an endpoint group.
func SetPolicies(g *catalog.EndpointGroup, p Policies) {
	if p.Rotation != nil {
		g.Rotation = p.Rotation
	}
	if p.Signal != nil {
		g.Signal = p.Signal
	}
	if p.Action != nil {
		g.Action = p.Action
	}
	if p.Breaker != nil {
		g.Breaker = p.Breaker
	}
}

// AddIdentityType registers a compiled identity type on the site and makes it
// eligible for every endpoint group of its client that has no explicit list.
func AddIdentityType(s *catalog.Site, t *identity.CompiledType) {
	s.IdentityTypes[t.Name] = t
	s.IdentityTypesByID[t.ID] = t
	for _, g := range s.GroupsByID {
		if g.Client == t.Client {
			g.IdentityTypeIDs = append(g.IdentityTypeIDs, t.ID)
		}
	}
}

// MustCompileType parses and compiles an identity type from YAML.
func MustCompileType(id, siteID string, version int, yamlSpec string) *identity.CompiledType {
	spec, err := identity.ParseTypeYAML([]byte(yamlSpec))
	if err != nil {
		panic(fmt.Sprintf("catalogtest: parse identity type: %v", err))
	}
	t, err := identity.Compile(id, siteID, version, spec)
	if err != nil {
		panic(fmt.Sprintf("catalogtest: compile identity type: %v", err))
	}
	return t
}

func builtinRef(kind policy.Kind) catalog.PolicyRef {
	return catalog.PolicyRef{Name: policy.DefaultPolicyName(kind), Version: 0, Level: policy.LevelBuiltin}
}

// groupRules keeps the URI rules per site so matchers can be rebuilt.
var (
	groupRulesMu sync.Mutex
	groupRules   = map[*catalog.Site][]site.Rule{}
)

func appendRules(existing []site.Rule, groupID, groupName string, rules []site.Rule) []site.Rule {
	for _, r := range rules {
		r.GroupID = groupID
		r.GroupName = groupName
		if r.ID == "" {
			r.ID = fmt.Sprintf("uri_%s_%d", groupID, len(existing))
		}
		existing = append(existing, r)
	}
	return existing
}

func rebuildMatcher(s *catalog.Site, client string, siteRules []site.Rule) {
	var rules []site.Rule
	for _, r := range siteRules {
		if g, ok := s.GroupsByID[r.GroupID]; ok && g.Client == client {
			rules = append(rules, r)
		}
	}
	defaultID := ""
	if g, ok := s.Groups[catalog.GroupKey{Client: client, Name: site.DefaultGroup}]; ok {
		defaultID = g.ID
	}
	m, err := site.NewMatcher(rules, defaultID)
	if err != nil {
		panic(fmt.Sprintf("catalogtest: build matcher: %v", err))
	}
	s.Matchers[client] = m
}
