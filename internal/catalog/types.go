// Package catalog maintains an immutable, per-namespace in-memory snapshot of
// the metadata that hot paths need on every request: sites, clients, endpoint
// groups, URI matchers, compiled identity types and resolved, compiled
// policies. Snapshots are rebuilt from PostgreSQL and swapped atomically when a
// change notification arrives on the event bus.
package catalog

import (
	"context"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// Namespace is the snapshot of one namespace. It must be treated as read-only.
type Namespace struct {
	ID          string
	TenantID    string
	TenantName  string
	Name        string
	DisplayName string

	// Sites indexed by name and by ID.
	Sites     map[string]*Site
	SitesByID map[string]*Site

	// Version increases every time the snapshot of this namespace is rebuilt.
	Version  uint64
	LoadedAt time.Time
}

// Site is the snapshot of one site. It must be treated as read-only.
type Site struct {
	ID            string
	Name          string
	DisplayName   string
	Key           int64 // hot-state key (sites.hkey)
	NamespaceID   string
	NamespaceName string
	TenantID      string
	Clients       []string
	Paused        bool
	PausedReason  string

	// Groups indexes endpoint groups by (client, name); GroupsByID by ID and
	// GroupsByKey by hot-state key.
	Groups      map[GroupKey]*EndpointGroup
	GroupsByID  map[string]*EndpointGroup
	GroupsByKey map[int64]*EndpointGroup

	// Matchers holds one URI matcher per client.
	Matchers map[string]*site.Matcher

	// IdentityTypes indexes compiled identity types by name and by ID.
	IdentityTypes     map[string]*identity.CompiledType
	IdentityTypesByID map[string]*identity.CompiledType
}

// GroupKey identifies an endpoint group inside a site.
type GroupKey struct {
	Client string
	Name   string
}

// PolicyRef describes which published policy version was resolved for an
// endpoint group and at which binding level. PolicyID is empty for built-in
// defaults.
type PolicyRef struct {
	PolicyID string
	Name     string
	Version  int
	Level    policy.Level
}

// EndpointGroup is the snapshot of one endpoint group including its resolved
// and compiled policies.
type EndpointGroup struct {
	ID           string
	Name         string
	Client       string
	Key          int64 // hot-state key (endpoint_groups.hkey)
	SiteID       string
	SiteKey      int64
	LowWatermark int

	Rotation    *policy.RotationSpec
	RotationRef PolicyRef
	Signal      *policy.CompiledSignal
	SignalRef   PolicyRef
	Action      *policy.CompiledAction
	ActionRef   PolicyRef
	Breaker     *policy.BreakerSpec
	BreakerRef  PolicyRef

	// IdentityTypeIDs lists identity types eligible for this group, resolved
	// from rotation.identity_types (empty spec = every type of the site+client).
	IdentityTypeIDs []string
}

// HasClient reports whether the site declares the client type.
func (s *Site) HasClient(client string) bool {
	for _, c := range s.Clients {
		if c == client {
			return true
		}
	}
	return false
}

// Group returns the endpoint group (client, name).
func (s *Site) Group(client, name string) (*EndpointGroup, bool) {
	g, ok := s.Groups[GroupKey{Client: client, Name: name}]
	return g, ok
}

// MatchGroup maps a request path to an endpoint group of the client, falling
// back to the client's "_default" group. The boolean is false when the client
// has no matcher or no default group (which indicates a broken snapshot).
func (s *Site) MatchGroup(client, path string) (*EndpointGroup, site.MatchResult, bool) {
	m, ok := s.Matchers[client]
	if !ok || m == nil {
		return nil, site.MatchResult{}, false
	}
	res := m.Match(path)
	if g, ok := s.GroupsByID[res.GroupID]; ok {
		return g, res, true
	}
	g, ok := s.Group(client, site.DefaultGroup)
	return g, res, ok
}

// GroupsOfClient returns the endpoint groups of one client.
func (s *Site) GroupsOfClient(client string) []*EndpointGroup {
	out := make([]*EndpointGroup, 0, len(s.GroupsByID))
	for _, g := range s.GroupsByID {
		if g.Client == client {
			out = append(out, g)
		}
	}
	return out
}

// GroupKeysOfClient returns the hot-state keys of all endpoint groups of one client.
func (s *Site) GroupKeysOfClient(client string) []int64 {
	out := make([]int64, 0, len(s.GroupsByID))
	for _, g := range s.GroupsByID {
		if g.Client == client {
			out = append(out, g.Key)
		}
	}
	return out
}

// Catalog provides read access to namespace snapshots and triggers reloads.
// Implementations must be safe for concurrent use; returned snapshots are
// immutable and may be retained by callers for the duration of a request.
type Catalog interface {
	// Namespace returns the snapshot of a namespace by ID.
	Namespace(id string) (*Namespace, bool)
	// NamespaceByName returns the snapshot of a namespace by tenant ID and name.
	NamespaceByName(tenantID, name string) (*Namespace, bool)
	// Namespaces returns all namespace snapshots (optionally of one tenant when tenantID != "").
	Namespaces(tenantID string) []*Namespace
	// Site returns a site and its namespace by site ID.
	Site(id string) (*Site, *Namespace, bool)
	// SiteByKey returns a site and its namespace by hot-state key.
	SiteByKey(key int64) (*Site, *Namespace, bool)
	// Reload synchronously rebuilds one namespace from PostgreSQL on this
	// instance (a namespace that no longer exists is removed).
	Reload(ctx context.Context, namespaceID string) error
	// ReloadAll synchronously rebuilds every namespace.
	ReloadAll(ctx context.Context) error
	// Invalidate reloads the namespace locally and notifies peer instances.
	Invalidate(ctx context.Context, namespaceID string) error
	// OnChange registers fn to be called after a namespace snapshot changed or
	// was removed. It returns a function that unregisters fn.
	OnChange(fn func(namespaceID string)) (unregister func())
}
