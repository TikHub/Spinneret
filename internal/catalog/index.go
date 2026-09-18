package catalog

import (
	"sort"
)

// nameKey identifies a namespace by tenant and name.
type nameKey struct {
	tenantID string
	name     string
}

// siteEntry pairs a site with its namespace.
type siteEntry struct {
	site *Site
	ns   *Namespace
}

// index is an immutable lookup structure over every loaded namespace. A new
// index is built for every change and swapped atomically, so readers never
// take locks.
type index struct {
	byID       map[string]*Namespace
	byName     map[nameKey]*Namespace
	sitesByID  map[string]siteEntry
	sitesByKey map[int64]siteEntry
	// sorted lists every namespace ordered by ID.
	sorted []*Namespace
}

func emptyIndex() *index {
	return newIndex(nil)
}

// newIndex builds an index over namespaces.
func newIndex(namespaces map[string]*Namespace) *index {
	idx := &index{
		byID:       make(map[string]*Namespace, len(namespaces)),
		byName:     make(map[nameKey]*Namespace, len(namespaces)),
		sitesByID:  map[string]siteEntry{},
		sitesByKey: map[int64]siteEntry{},
		sorted:     make([]*Namespace, 0, len(namespaces)),
	}
	for id, ns := range namespaces {
		idx.byID[id] = ns
		idx.byName[nameKey{tenantID: ns.TenantID, name: ns.Name}] = ns
		for _, s := range ns.SitesByID {
			e := siteEntry{site: s, ns: ns}
			idx.sitesByID[s.ID] = e
			idx.sitesByKey[s.Key] = e
		}
		idx.sorted = append(idx.sorted, ns)
	}
	sort.Slice(idx.sorted, func(i, j int) bool { return idx.sorted[i].ID < idx.sorted[j].ID })
	return idx
}

// with returns a copy of the index in which ns replaces the namespace with the
// same ID.
func (idx *index) with(ns *Namespace) *index {
	next := make(map[string]*Namespace, len(idx.byID)+1)
	for id, cur := range idx.byID {
		next[id] = cur
	}
	next[ns.ID] = ns
	return newIndex(next)
}

// without returns a copy of the index without the namespace id.
func (idx *index) without(id string) *index {
	next := make(map[string]*Namespace, len(idx.byID))
	for nsID, cur := range idx.byID {
		if nsID != id {
			next[nsID] = cur
		}
	}
	return newIndex(next)
}
