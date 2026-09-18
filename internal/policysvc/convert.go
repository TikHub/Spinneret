package policysvc

import (
	"cmp"
	"slices"
	"time"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvcdb"
)

// bindingRow is the common shape of the binding query rows.
type bindingRow struct {
	ID              string
	PolicyID        string
	PolicyName      string
	Kind            string
	NamespaceID     string
	SiteID          *string
	Client          *string
	EndpointGroupID *string
	CreatedBy       string
	CreatedAt       time.Time
}

func fromNamespaceRows(rows []policysvcdb.BindingListByNamespaceRow) []bindingRow {
	out := make([]bindingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, bindingRow{
			ID: r.ID, PolicyID: r.PolicyID, PolicyName: r.PolicyName, Kind: r.Kind, NamespaceID: r.NamespaceID,
			SiteID: r.SiteID, Client: r.Client, EndpointGroupID: r.EndpointGroupID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		})
	}
	return out
}

func fromPolicyRows(rows []policysvcdb.BindingListByPoliciesRow) []bindingRow {
	out := make([]bindingRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, bindingRow{
			ID: r.ID, PolicyID: r.PolicyID, PolicyName: r.PolicyName, Kind: r.Kind, NamespaceID: r.NamespaceID,
			SiteID: r.SiteID, Client: r.Client, EndpointGroupID: r.EndpointGroupID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		})
	}
	return out
}

func fromGetRow(r policysvcdb.BindingGetRow) bindingRow {
	return bindingRow{
		ID: r.ID, PolicyID: r.PolicyID, PolicyName: r.PolicyName, Kind: r.Kind, NamespaceID: r.NamespaceID,
		SiteID: r.SiteID, Client: r.Client, EndpointGroupID: r.EndpointGroupID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}
}

func fromStoredBinding(r policysvcdb.PolicyBinding, policyName string) bindingRow {
	return bindingRow{
		ID: r.ID, PolicyID: r.PolicyID, PolicyName: policyName, Kind: r.Kind, NamespaceID: r.NamespaceID,
		SiteID: r.SiteID, Client: r.Client, EndpointGroupID: r.EndpointGroupID, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
	}
}

// siteID returns the bound site ID or "" for namespace-level bindings.
func (b bindingRow) siteID() string { return deref(b.SiteID) }

// bindingLevel derives the level of a binding from its target columns.
func bindingLevel(siteID, client, egID string) policy.Level {
	switch {
	case siteID == "":
		return policy.LevelNamespace
	case egID != "":
		return policy.LevelEndpointGroup
	case client != "":
		return policy.LevelClient
	default:
		return policy.LevelSite
	}
}

// toBinding converts a row, resolving names from the snapshot.
func toBinding(ns *catalog.Namespace, r bindingRow) Binding {
	b := Binding{
		ID:              r.ID,
		PolicyID:        r.PolicyID,
		PolicyName:      r.PolicyName,
		Kind:            policy.Kind(r.Kind),
		NamespaceID:     r.NamespaceID,
		NamespaceName:   ns.Name,
		SiteID:          deref(r.SiteID),
		Client:          deref(r.Client),
		EndpointGroupID: deref(r.EndpointGroupID),
		CreatedBy:       r.CreatedBy,
		CreatedAt:       r.CreatedAt,
	}
	b.Level = bindingLevel(b.SiteID, b.Client, b.EndpointGroupID)
	if s, ok := ns.SitesByID[b.SiteID]; ok {
		b.SiteName = s.Name
		if g, ok := s.GroupsByID[b.EndpointGroupID]; ok {
			b.EndpointGroupName = g.Name
		}
	}
	return b
}

// visibleBindings converts the rows visible under a.
func visibleBindings(ns *catalog.Namespace, a access, rows []bindingRow) []Binding {
	out := make([]Binding, 0, len(rows))
	for _, r := range rows {
		if a.bindingVisible(r.siteID()) {
			out = append(out, toBinding(ns, r))
		}
	}
	sortBindings(out)
	return out
}

// sortBindings orders bindings by kind (rotation, signal, action, breaker),
// level (namespace, site, client, endpoint group) and target names.
func sortBindings(bs []Binding) {
	slices.SortStableFunc(bs, func(a, b Binding) int {
		return cmp.Or(
			cmp.Compare(kindOrder(a.Kind), kindOrder(b.Kind)),
			cmp.Compare(levelOrder(a.Level), levelOrder(b.Level)),
			cmp.Compare(a.SiteName, b.SiteName),
			cmp.Compare(a.Client, b.Client),
			cmp.Compare(a.EndpointGroupName, b.EndpointGroupName),
			cmp.Compare(a.ID, b.ID),
		)
	})
}

func kindOrder(k policy.Kind) int {
	if i := slices.Index(policy.Kinds(), k); i >= 0 {
		return i
	}
	return len(policy.Kinds())
}

func levelOrder(l policy.Level) int {
	switch l {
	case policy.LevelNamespace:
		return 0
	case policy.LevelSite:
		return 1
	case policy.LevelClient:
		return 2
	case policy.LevelEndpointGroup:
		return 3
	}
	return 4
}

// toPolicy converts a stored policy row.
func toPolicy(ns *catalog.Namespace, r policysvcdb.Policy) Policy {
	return Policy{
		ID:             r.ID,
		NamespaceID:    r.NamespaceID,
		NamespaceName:  ns.Name,
		Kind:           policy.Kind(r.Kind),
		Name:           r.Name,
		Description:    r.Description,
		CurrentVersion: int(r.CurrentVersion),
		DraftYAML:      deref(r.DraftYaml),
		HasDraft:       r.DraftYaml != nil,
		DraftUpdatedBy: deref(r.DraftUpdatedBy),
		DraftUpdatedAt: r.DraftUpdatedAt,
		CreatedBy:      r.CreatedBy,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

// toListPolicy converts a list row (without YAML bodies).
func toListPolicy(ns *catalog.Namespace, r policysvcdb.PolicyListRow) Policy {
	return Policy{
		ID:             r.ID,
		NamespaceID:    r.NamespaceID,
		NamespaceName:  ns.Name,
		Kind:           policy.Kind(r.Kind),
		Name:           r.Name,
		Description:    r.Description,
		CurrentVersion: int(r.CurrentVersion),
		HasDraft:       r.HasDraft,
		DraftUpdatedBy: deref(r.DraftUpdatedBy),
		DraftUpdatedAt: r.DraftUpdatedAt,
		CreatedBy:      r.CreatedBy,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
	}
}

func toVersion(r policysvcdb.PolicyVersion) Version {
	return Version{
		Version:   int(r.Version),
		SpecYAML:  r.SpecYaml,
		Comment:   r.Comment,
		CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func normalizePageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	default:
		return n
	}
}
