package catalog

import (
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/site"
)

// builder turns the rows of one namespace into an immutable snapshot. Broken
// rows (invalid URI patterns, identity types or policies) are logged and
// skipped or replaced by defaults so that one bad object never prevents the
// rest of the namespace from loading.
type builder struct {
	raw    *rawNamespace
	logger *slog.Logger
}

// build creates the namespace snapshot with the given version.
func (b *builder) build(version uint64, now time.Time) *Namespace {
	r := b.raw
	ns := &Namespace{
		ID:          r.Namespace.ID,
		TenantID:    r.Namespace.TenantID,
		TenantName:  r.Namespace.TenantName,
		Name:        r.Namespace.Name,
		DisplayName: r.Namespace.DisplayName,
		Sites:       make(map[string]*Site, len(r.Sites)),
		SitesByID:   make(map[string]*Site, len(r.Sites)),
		Version:     version,
		LoadedAt:    now,
	}
	for _, row := range r.Sites {
		s := &Site{
			ID:                row.ID,
			Name:              row.Name,
			DisplayName:       row.DisplayName,
			Key:               row.Hkey,
			NamespaceID:       ns.ID,
			NamespaceName:     ns.Name,
			TenantID:          ns.TenantID,
			Clients:           slices.Clone(row.Clients),
			Paused:            row.Paused,
			PausedReason:      row.PausedReason,
			Groups:            map[GroupKey]*EndpointGroup{},
			GroupsByID:        map[string]*EndpointGroup{},
			GroupsByKey:       map[int64]*EndpointGroup{},
			Matchers:          map[string]*site.Matcher{},
			IdentityTypes:     map[string]*identity.CompiledType{},
			IdentityTypesByID: map[string]*identity.CompiledType{},
		}
		if s.Clients == nil {
			s.Clients = []string{}
		}
		ns.Sites[s.Name] = s
		ns.SitesByID[s.ID] = s
	}
	b.addGroups(ns)
	b.addMatchers(ns)
	b.addIdentityTypes(ns)
	newPolicyResolver(r, b.logger).apply(ns)
	return ns
}

func (b *builder) addGroups(ns *Namespace) {
	for _, row := range b.raw.Groups {
		s, ok := ns.SitesByID[row.SiteID]
		if !ok {
			continue
		}
		g := &EndpointGroup{
			ID:           row.ID,
			Name:         row.Name,
			Client:       row.Client,
			Key:          row.Hkey,
			SiteID:       s.ID,
			SiteKey:      s.Key,
			LowWatermark: int(row.LowWatermark),
		}
		s.Groups[GroupKey{Client: g.Client, Name: g.Name}] = g
		s.GroupsByID[g.ID] = g
		s.GroupsByKey[g.Key] = g
	}
}

// siteClientKey identifies a client of a site.
type siteClientKey struct {
	siteID string
	client string
}

// addMatchers builds one URI matcher per client of every site (declared
// clients plus clients that own endpoint groups).
func (b *builder) addMatchers(ns *Namespace) {
	type groupSite struct {
		group *EndpointGroup
		site  *Site
	}
	groups := map[string]groupSite{}
	for _, s := range ns.SitesByID {
		for id, g := range s.GroupsByID {
			groups[id] = groupSite{group: g, site: s}
		}
	}
	rulesBySiteClient := map[siteClientKey][]site.Rule{}
	for _, row := range b.raw.Rules {
		gs, ok := groups[row.EndpointGroupID]
		if !ok {
			continue
		}
		g, s := gs.group, gs.site
		rule := site.Rule{
			ID:        row.ID,
			GroupID:   g.ID,
			GroupName: g.Name,
			Kind:      site.RuleKind(row.Kind),
			Pattern:   row.Pattern,
			Position:  int(row.Position),
		}
		if err := site.ValidatePattern(rule.Kind, rule.Pattern); err != nil {
			b.logger.Warn("catalog: skipping invalid uri rule",
				slog.String("namespace_id", ns.ID), slog.String("site_id", s.ID),
				slog.String("endpoint_group_id", g.ID), slog.String("rule_id", rule.ID), slog.Any("error", err))
			continue
		}
		key := siteClientKey{siteID: s.ID, client: g.Client}
		rulesBySiteClient[key] = append(rulesBySiteClient[key], rule)
	}
	for _, s := range ns.SitesByID {
		for _, client := range siteClients(s) {
			rules := rulesBySiteClient[siteClientKey{siteID: s.ID, client: client}]
			defaultID := ""
			if g, ok := s.Groups[GroupKey{Client: client, Name: site.DefaultGroup}]; ok {
				defaultID = g.ID
			} else {
				b.logger.Warn("catalog: client has no default endpoint group",
					slog.String("namespace_id", ns.ID), slog.String("site_id", s.ID), slog.String("client", client))
			}
			m, err := site.NewMatcher(rules, defaultID)
			if err != nil {
				b.logger.Warn("catalog: uri rules do not compile, using the default group only",
					slog.String("namespace_id", ns.ID), slog.String("site_id", s.ID),
					slog.String("client", client), slog.Any("error", err))
				m, err = site.NewMatcher(nil, defaultID)
				if err != nil {
					continue
				}
			}
			s.Matchers[client] = m
		}
	}
}

// siteClients returns the declared clients of a site followed by any other
// client that owns endpoint groups, sorted after the declared ones.
func siteClients(s *Site) []string {
	out := slices.Clone(s.Clients)
	var extra []string
	for _, g := range s.GroupsByID {
		if !slices.Contains(out, g.Client) && !slices.Contains(extra, g.Client) {
			extra = append(extra, g.Client)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

// addIdentityTypes compiles the identity types of every site. The database
// columns (name, client) are authoritative for indexing, so they override the
// values stored inside the spec.
func (b *builder) addIdentityTypes(ns *Namespace) {
	for _, row := range b.raw.IdentityTypes {
		s, ok := ns.SitesByID[row.SiteID]
		if !ok {
			continue
		}
		log := b.logger.With(slog.String("namespace_id", ns.ID), slog.String("site_id", s.ID),
			slog.String("identity_type_id", row.ID), slog.String("identity_type", row.Name))
		spec, err := identity.ParseTypeJSON(row.Spec)
		if err != nil {
			log.Warn("catalog: skipping identity type with an invalid spec", slog.Any("error", err))
			continue
		}
		if spec.Name != row.Name || spec.Client != row.Client || spec.Site != s.Name {
			log.Warn("catalog: identity type spec disagrees with its row, using the row values",
				slog.String("spec_name", spec.Name), slog.String("spec_client", spec.Client))
			spec.Name, spec.Client, spec.Site = row.Name, row.Client, s.Name
		}
		ct, err := identity.Compile(row.ID, s.ID, int(row.Version), spec)
		if err != nil {
			log.Warn("catalog: skipping identity type that does not compile", slog.Any("error", err))
			continue
		}
		s.IdentityTypes[ct.Name] = ct
		s.IdentityTypesByID[ct.ID] = ct
	}
}
