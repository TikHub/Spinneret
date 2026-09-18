package hotstate

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
)

// profile is the endpoint-group partition of an identity: the groups whose
// ready queue it belongs to when schedulable (eligible) and the other groups
// of its client (or of the whole site for removals).
type profile struct {
	eligible []int64
	other    []int64
}

// size is the number of endpoint groups touched by an identity of this profile.
func (p profile) size() int {
	return len(p.eligible) + len(p.other)
}

type profileKey struct {
	client string
	typeID string
}

// groupPlan precomputes the endpoint groups of one site snapshot. It is not
// safe for concurrent use.
type groupPlan struct {
	site     *catalog.Site
	groups   []*catalog.EndpointGroup // sorted by hot-state key
	all      []int64                  // hot-state keys of every group of the site
	profiles map[profileKey]int
	list     []profile
	removal  int
}

// newGroupPlan builds the plan of a site snapshot. Profile index 0 is the
// removal profile (every group of the site, none eligible).
func newGroupPlan(site *catalog.Site) *groupPlan {
	groups := make([]*catalog.EndpointGroup, 0, len(site.GroupsByID))
	for _, g := range site.GroupsByID {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
	all := make([]int64, len(groups))
	for i, g := range groups {
		all[i] = g.Key
	}
	p := &groupPlan{site: site, groups: groups, all: all, profiles: map[profileKey]int{}}
	p.list = append(p.list, profile{other: all})
	p.removal = 0
	return p
}

// profileIndex returns the index of the profile of an identity of client and
// type typeID: its eligible groups are the groups of the client whose
// resolved rotation policy admits the type (spec §5, rdy).
func (p *groupPlan) profileIndex(client, typeID string) int {
	key := profileKey{client: client, typeID: typeID}
	if idx, ok := p.profiles[key]; ok {
		return idx
	}
	var pr profile
	for _, g := range p.groups {
		if g.Client != client {
			continue
		}
		if containsString(g.IdentityTypeIDs, typeID) {
			pr.eligible = append(pr.eligible, g.Key)
		} else {
			pr.other = append(pr.other, g.Key)
		}
	}
	idx := len(p.list)
	p.list = append(p.list, pr)
	p.profiles[key] = idx
	return idx
}

// eligibleFor reports whether profile idx makes the identity eligible for group key.
func (p *groupPlan) eligibleFor(idx int, groupKey int64) bool {
	if idx < 0 || idx >= len(p.list) {
		return false
	}
	for _, k := range p.list[idx].eligible {
		if k == groupKey {
			return true
		}
	}
	return false
}

// baselineCSV returns the health baselines of every endpoint group of the site
// as "eg:baseline,..." (argument of sync_identities.lua health resets). Groups
// without an action policy use policy.DefaultHealthBaseline.
func (p *groupPlan) baselineCSV() string {
	var b strings.Builder
	for i, g := range p.groups {
		baseline := policy.DefaultHealthBaseline
		if g.Action != nil {
			baseline = g.Action.Health.Baseline
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatInt(g.Key, 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(baseline, 'f', -1, 64))
	}
	return b.String()
}

// eligibleTypeNames returns the newline-separated names of the identity types
// eligible for a group (argument of the prune_ready maintenance operation).
func (p *groupPlan) eligibleTypeNames(g *catalog.EndpointGroup) string {
	names := make([]string, 0, len(g.IdentityTypeIDs))
	for _, id := range g.IdentityTypeIDs {
		if t, ok := p.site.IdentityTypesByID[id]; ok && t != nil {
			names = append(names, t.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

// groupsOfClient returns the hot-state keys of the groups of one client.
func (p *groupPlan) groupsOfClient(client string) []*catalog.EndpointGroup {
	out := make([]*catalog.EndpointGroup, 0, len(p.groups))
	for _, g := range p.groups {
		if g.Client == client {
			out = append(out, g)
		}
	}
	return out
}

func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
