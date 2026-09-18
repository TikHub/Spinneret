package policysvc

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/textdiff"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvcdb"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// diffContextLines is the number of context lines of unified diffs.
const diffContextLines = 3

// loaded is a stored policy with its namespace and the caller's read access.
type loaded struct {
	row      policysvcdb.Policy
	ns       *catalog.Namespace
	read     access
	bindings []bindingRow
}

// loadPolicy loads a policy without authorization.
func (s *Service) loadPolicy(ctx context.Context, q *policysvcdb.Queries, id string) (policysvcdb.Policy, *catalog.Namespace, error) {
	if strings.TrimSpace(id) == "" {
		return policysvcdb.Policy{}, nil, apperr.InvalidArgument("", "policy id is required")
	}
	row, err := q.PolicyGet(ctx, id)
	if err != nil {
		return policysvcdb.Policy{}, nil, pgstore.MapError(err, "policy")
	}
	ns, err := s.namespace(row.NamespaceID, "policy")
	if err != nil {
		return policysvcdb.Policy{}, nil, err
	}
	return row, ns, nil
}

// loadVisiblePolicy loads a policy that the principal may read. When the
// policy exists but is not visible, the returned value carries its row and
// namespace (for denied audit entries) together with the permission error.
func (s *Service) loadVisiblePolicy(ctx context.Context, p *authz.Principal, id string) (loaded, error) {
	if err := requirePrincipal(p); err != nil {
		return loaded{}, err
	}
	q := policysvcdb.New(s.pool)
	row, ns, err := s.loadPolicy(ctx, q, id)
	if err != nil {
		return loaded{}, err
	}
	l := loaded{row: row, ns: ns, read: accessFor(p, ns, authz.PermPolicyRead)}
	if !l.read.any() {
		return loaded{row: row, ns: ns}, denied(p, ns, authz.PermPolicyRead)
	}
	rows, err := q.BindingListByPolicies(ctx, policysvcdb.BindingListByPoliciesParams{PolicyIds: []string{id}, RowLimit: maxBindingRows})
	if err != nil {
		return loaded{}, fmt.Errorf("list bindings of policy %s: %w", id, err)
	}
	l.bindings = fromPolicyRows(rows)
	sites := make([]string, 0, len(l.bindings))
	for _, b := range l.bindings {
		sites = append(sites, b.siteID())
	}
	if !l.read.policyVisible(sites) {
		return loaded{row: row, ns: ns}, denied(p, ns, authz.PermPolicyRead)
	}
	return l, nil
}

// view assembles the full policy view including published YAML and the
// bindings visible to the caller.
func (s *Service) view(ctx context.Context, q *policysvcdb.Queries, l loaded) (Policy, error) {
	out := toPolicy(l.ns, l.row)
	if l.row.CurrentVersion > 0 {
		v, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: l.row.ID, Version: l.row.CurrentVersion})
		if err != nil {
			return Policy{}, fmt.Errorf("load version %d of policy %s: %w", l.row.CurrentVersion, l.row.ID, err)
		}
		out.PublishedYAML = v.SpecYaml
	}
	out.Bindings = visibleBindings(l.ns, l.read, l.bindings)
	return out, nil
}

// GetPolicy returns a policy with its published YAML, draft and visible
// bindings (policy:read).
func (s *Service) GetPolicy(ctx context.Context, p *authz.Principal, id string) (Policy, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	l, err := s.loadVisiblePolicy(ctx, p, id)
	if err != nil {
		return Policy{}, err
	}
	return s.view(ctx, policysvcdb.New(s.pool), l)
}

// ListPolicies lists the policies of a namespace visible to the caller
// (policy:read), ordered by kind and name. YAML bodies are omitted.
func (s *Service) ListPolicies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, in ListPoliciesInput) (PolicyPage, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if err := requirePrincipal(p); err != nil {
		return PolicyPage{}, err
	}
	if err := validKindFilter(in.Kind); err != nil {
		return PolicyPage{}, err
	}
	if utf8.RuneCountInString(in.Search) > MaxSearchLength {
		return PolicyPage{}, apperr.InvalidArgument("", "search must be at most %d characters", MaxSearchLength)
	}
	if (in.AfterKind == "") != (in.AfterName == "") {
		return PolicyPage{}, apperr.InvalidArgument("", "invalid page_token")
	}
	read := accessFor(p, ns, authz.PermPolicyRead)
	if !read.any() {
		return PolicyPage{}, denied(p, ns, authz.PermPolicyRead)
	}
	size := normalizePageSize(in.PageSize)
	siteIDs := read.siteIDs()
	if siteIDs == nil {
		siteIDs = []string{}
	}
	q := policysvcdb.New(s.pool)
	search := strings.ToLower(in.Search)
	rows, err := q.PolicyList(ctx, policysvcdb.PolicyListParams{
		NamespaceID: ns.ID, Kind: string(in.Kind), Search: search, AllSites: read.all, SiteIds: siteIDs,
		AfterKind: in.AfterKind, AfterName: in.AfterName, PageLimit: int32(size + 1),
	})
	if err != nil {
		return PolicyPage{}, fmt.Errorf("list policies: %w", err)
	}
	total, err := q.PolicyCount(ctx, policysvcdb.PolicyCountParams{
		NamespaceID: ns.ID, Kind: string(in.Kind), Search: search, AllSites: read.all, SiteIds: siteIDs,
	})
	if err != nil {
		return PolicyPage{}, fmt.Errorf("count policies: %w", err)
	}
	page := PolicyPage{Total: int(total), More: len(rows) > size}
	if page.More {
		rows = rows[:size]
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	byPolicy := map[string][]bindingRow{}
	if len(ids) > 0 {
		brows, err := q.BindingListByPolicies(ctx, policysvcdb.BindingListByPoliciesParams{PolicyIds: ids, RowLimit: maxBindingRows})
		if err != nil {
			return PolicyPage{}, fmt.Errorf("list policy bindings: %w", err)
		}
		for _, b := range fromPolicyRows(brows) {
			byPolicy[b.PolicyID] = append(byPolicy[b.PolicyID], b)
		}
	}
	page.Policies = make([]Policy, 0, len(rows))
	for _, r := range rows {
		pol := toListPolicy(ns, r)
		pol.Bindings = visibleBindings(ns, read, byPolicy[r.ID])
		page.Policies = append(page.Policies, pol)
	}
	return page, nil
}

// ListPolicyVersions lists published versions newest first (policy:read).
// beforeVersion > 0 continues after that version.
func (s *Service) ListPolicyVersions(ctx context.Context, p *authz.Principal, id string, pageSize, beforeVersion int) (VersionPage, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if beforeVersion < 0 || beforeVersion > math.MaxInt32 {
		return VersionPage{}, apperr.InvalidArgument("", "invalid page_token")
	}
	l, err := s.loadVisiblePolicy(ctx, p, id)
	if err != nil {
		return VersionPage{}, err
	}
	size := normalizePageSize(pageSize)
	q := policysvcdb.New(s.pool)
	rows, err := q.PolicyVersionList(ctx, policysvcdb.PolicyVersionListParams{
		PolicyID: l.row.ID, BeforeVersion: int32(beforeVersion), PageLimit: int32(size + 1),
	})
	if err != nil {
		return VersionPage{}, fmt.Errorf("list versions of policy %s: %w", id, err)
	}
	total, err := q.PolicyVersionCount(ctx, l.row.ID)
	if err != nil {
		return VersionPage{}, fmt.Errorf("count versions of policy %s: %w", id, err)
	}
	page := VersionPage{Total: int(total), More: len(rows) > size}
	if page.More {
		rows = rows[:size]
	}
	page.Versions = make([]Version, 0, len(rows))
	for _, r := range rows {
		page.Versions = append(page.Versions, toVersion(r))
	}
	return page, nil
}

// DiffPolicyVersions compares two sides of a policy (policy:read):
// fromVersion 0 is the current published version, toVersion 0 the draft (or
// the current published version when there is no draft).
func (s *Service) DiffPolicyVersions(ctx context.Context, p *authz.Principal, id string, fromVersion, toVersion int) (Diff, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	if fromVersion < 0 || toVersion < 0 || fromVersion > math.MaxInt32 || toVersion > math.MaxInt32 {
		return Diff{}, apperr.InvalidArgument("", "versions must be between 0 and %d", math.MaxInt32)
	}
	l, err := s.loadVisiblePolicy(ctx, p, id)
	if err != nil {
		return Diff{}, err
	}
	q := policysvcdb.New(s.pool)
	fromName, fromYAML, err := s.side(ctx, q, l.row, fromVersion, false)
	if err != nil {
		return Diff{}, err
	}
	toName, toYAML, err := s.side(ctx, q, l.row, toVersion, true)
	if err != nil {
		return Diff{}, err
	}
	return Diff{
		FromYAML:    fromYAML,
		ToYAML:      toYAML,
		UnifiedDiff: textdiff.Unified(fromName, toName, fromYAML, toYAML, diffContextLines),
	}, nil
}

// side returns the label and YAML of one diff side.
func (s *Service) side(ctx context.Context, q *policysvcdb.Queries, row policysvcdb.Policy, version int, draft bool) (string, string, error) {
	if version == 0 {
		if draft && row.DraftYaml != nil {
			return "draft", *row.DraftYaml, nil
		}
		if row.CurrentVersion == 0 {
			return "published", "", nil
		}
		version = int(row.CurrentVersion)
	}
	v, err := q.PolicyVersionGet(ctx, policysvcdb.PolicyVersionGetParams{PolicyID: row.ID, Version: int32(version)})
	if err != nil {
		return "", "", pgstore.MapError(err, "policy version "+strconv.Itoa(version))
	}
	return "v" + strconv.Itoa(version), v.SpecYaml, nil
}
