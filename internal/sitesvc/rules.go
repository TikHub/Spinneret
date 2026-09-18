package sitesvc

import (
	"context"
	"unicode/utf8"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/sitesvc/sitesvcdb"
)

// RuleCursor is the position after which a rule listing continues.
type RuleCursor struct {
	Position int    `json:"p"`
	ID       string `json:"i"`
}

// RulePage is one page of URI rules ordered by position.
type RulePage struct {
	Rules []URIRule
	// Total counts every rule of the endpoint group.
	Total int
	// Next is the cursor of the next page; nil when there are no more pages.
	Next *RuleCursor
}

// ResolvedPolicy is the effective policy of one kind for an endpoint group.
type ResolvedPolicy struct {
	Kind policy.Kind
	Ref  catalog.PolicyRef
}

// TestURIResult describes how a URI would be scheduled.
type TestURIResult struct {
	GroupID   string
	GroupName string
	// RuleID and Kind are empty when the "_default" group was used.
	RuleID  string
	Kind    site.RuleKind
	Default bool
	// Policies lists the effective policies in the order rotation, signal,
	// action, breaker.
	Policies []ResolvedPolicy
}

// ListURIRules returns one page of the rules of an endpoint group. A nil
// cursor starts at the first rule.
func (s *Service) ListURIRules(ctx context.Context, groupID string, pageSize int, after *RuleCursor) (RulePage, error) {
	limit := clampPageSize(pageSize)
	params := sitesvcdb.URIRuleListPageParams{EndpointGroupID: groupID, AfterPosition: -1, MaxRows: int32(limit + 1)}
	if after != nil {
		params.AfterPosition, params.AfterID = int32(after.Position), after.ID
	}
	q := sitesvcdb.New(s.pool)
	rows, err := q.URIRuleListPage(ctx, params)
	if err != nil {
		return RulePage{}, mapDBError(err, "list uri rules")
	}
	total, err := q.URIRuleCount(ctx, groupID)
	if err != nil {
		return RulePage{}, mapDBError(err, "count uri rules")
	}
	page := RulePage{Rules: make([]URIRule, 0, min(len(rows), limit)), Total: int(total)}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			page.Next = &RuleCursor{Position: int(last.Position), ID: last.ID}
			break
		}
		page.Rules = append(page.Rules, URIRule{ID: r.ID, Kind: site.RuleKind(r.Kind), Pattern: r.Pattern, Position: int(r.Position)})
	}
	return page, nil
}

// ReplaceURIRules atomically replaces the rules of an endpoint group with
// rules, in evaluation order (IDs are generated and positions assigned from
// list order). Every pattern is validated, the complete rule set of the site
// client must compile into a matcher, at most MaxRulesPerGroup rules per group
// and MaxRegexRulesPerClient regex rules per site client are allowed, and a
// (kind, pattern) pair may appear only once per site client. "_default"
// groups cannot have rules.
func (s *Service) ReplaceURIRules(ctx context.Context, p *authz.Principal, groupID string, rules []URIRule) ([]URIRule, error) {
	if err := validateRuleList(rules); err != nil {
		return nil, err
	}
	ref, err := s.GroupRef(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if ref.Name == site.DefaultGroup {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument,
			"the %q endpoint group matches every unmatched path and cannot have uri rules", site.DefaultGroup)
	}
	var stored []URIRule
	err = s.inTx(ctx, func(q *sitesvcdb.Queries) error {
		if _, err := q.SiteLock(ctx, ref.Site.ID); err != nil {
			return mapDBError(err, "site")
		}
		if _, err := q.EndpointGroupGet(ctx, groupID); err != nil {
			return mapDBError(err, "endpoint group")
		}
		stored, err = writeRules(ctx, q, ref.Site.ID, ref.Client, ref.ID, ref.Name, rules)
		return err
	})
	if err != nil {
		return nil, mapDBError(err, "replace uri rules")
	}
	s.record(ctx, p, ref.Site, ActionURIRulesReplace, ResourceEndpointGroup, ref.ID, ref.Name,
		map[string]any{"site": ref.Site.Name, "client": ref.Client, "rules": len(stored)})
	return stored, s.propagate(ctx, change{tenantID: ref.Site.TenantID, namespaceID: ref.Site.NamespaceID, siteID: ref.Site.ID})
}

// validateRuleList checks the size of a rule list and every rule's kind and pattern.
func validateRuleList(rules []URIRule) error {
	if len(rules) > MaxRulesPerGroup {
		return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "at most %d uri rules per endpoint group are allowed", MaxRulesPerGroup)
	}
	for i, r := range rules {
		switch r.Kind {
		case site.RuleExact, site.RuleTemplate, site.RulePrefix, site.RuleRegex:
		default:
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "rules[%d]: unknown kind %q", i, truncate(string(r.Kind), 32))
		}
		if err := site.ValidatePattern(r.Kind, r.Pattern); err != nil {
			msg := err.Error()
			if ae, ok := apperr.As(err); ok {
				msg = ae.Message
			}
			return apperr.InvalidArgument(apperr.ReasonInvalidArgument, "rules[%d]: %s", i, msg).WithCause(err)
		}
	}
	return nil
}

type ruleKey struct {
	kind    site.RuleKind
	pattern string
}

// writeRules replaces the rules of one group inside a transaction that holds
// the site lock, after checking them against the other groups of the client.
func writeRules(ctx context.Context, q *sitesvcdb.Queries, siteID, client, groupID, groupName string, rules []URIRule) ([]URIRule, error) {
	existing, err := q.URIRuleListBySiteClient(ctx, sitesvcdb.URIRuleListBySiteClientParams{SiteID: siteID, Client: client})
	if err != nil {
		return nil, mapDBError(err, "list uri rules")
	}
	merged := make([]site.Rule, 0, len(existing)+len(rules))
	owners := make(map[ruleKey]string, len(existing)+len(rules))
	regexes, replacedRegexes := 0, 0
	for _, r := range existing {
		kind := site.RuleKind(r.Kind)
		if r.EndpointGroupID == groupID && kind == site.RuleRegex {
			replacedRegexes++
		}
		if r.EndpointGroupID == groupID || site.ValidatePattern(kind, r.Pattern) != nil {
			// Rules being replaced, and legacy invalid rules (ignored by the
			// catalog as well), do not take part in the checks.
			continue
		}
		merged = append(merged, site.Rule{ID: r.ID, GroupID: r.EndpointGroupID, GroupName: r.GroupName, Kind: kind, Pattern: r.Pattern, Position: int(r.Position)})
		owners[ruleKey{kind: kind, pattern: r.Pattern}] = r.GroupName
		if kind == site.RuleRegex {
			regexes++
		}
	}
	newRegexes := 0
	stored := make([]URIRule, len(rules))
	params := sitesvcdb.URIRuleInsertBatchParams{
		EndpointGroupID: groupID,
		Ids:             make([]string, len(rules)),
		Kinds:           make([]string, len(rules)),
		Patterns:        make([]string, len(rules)),
		Positions:       make([]int32, len(rules)),
	}
	for i, r := range rules {
		key := ruleKey{kind: r.Kind, pattern: r.Pattern}
		if owner, dup := owners[key]; dup {
			return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument,
				"rules[%d]: a %s rule with the same pattern already exists in endpoint group %q of client %q", i, r.Kind, owner, client)
		}
		owners[key] = groupName
		if r.Kind == site.RuleRegex {
			regexes++
			newRegexes++
		}
		stored[i] = URIRule{ID: idgen.New(idgen.URIRule), Kind: r.Kind, Pattern: r.Pattern, Position: i}
		merged = append(merged, site.Rule{ID: stored[i].ID, GroupID: groupID, GroupName: groupName, Kind: r.Kind, Pattern: r.Pattern, Position: i})
		params.Ids[i], params.Kinds[i], params.Patterns[i], params.Positions[i] = stored[i].ID, string(r.Kind), r.Pattern, int32(i)
	}
	// Changes that do not add regex rules to the group are always accepted, so
	// a client above the limit can be brought back below it.
	if regexes > MaxRegexRulesPerClient && newRegexes > replacedRegexes {
		return nil, apperr.InvalidArgument(apperr.ReasonInvalidArgument,
			"client %q would have %d regex rules; at most %d are allowed per site client", client, regexes, MaxRegexRulesPerClient)
	}
	if _, err := site.NewMatcher(merged, ""); err != nil {
		return nil, err
	}
	if _, err := q.URIRuleDeleteByGroup(ctx, groupID); err != nil {
		return nil, mapDBError(err, "delete uri rules")
	}
	if len(rules) > 0 {
		if _, err := q.URIRuleInsertBatch(ctx, params); err != nil {
			return nil, mapDBError(err, "insert uri rules")
		}
	}
	if err := q.EndpointGroupTouch(ctx, groupID); err != nil {
		return nil, mapDBError(err, "endpoint group")
	}
	return stored, nil
}

// TestURI resolves uri for a client of a site snapshot to its endpoint group
// and effective policies.
func (s *Service) TestURI(st *catalog.Site, client, uri string) (TestURIResult, error) {
	if st == nil {
		return TestURIResult{}, apperr.InvalidArgument(apperr.ReasonSiteUnknown, "site is required")
	}
	if !st.HasClient(client) {
		return TestURIResult{}, apperr.InvalidArgument(apperr.ReasonClientUnknown, "client %q is not a client of site %q", truncate(client, 32), st.Name)
	}
	path, err := site.NormalizePath(uri)
	if err != nil {
		return TestURIResult{}, err
	}
	g, res, ok := st.MatchGroup(client, path)
	if !ok || g == nil {
		return TestURIResult{}, apperr.FailedPrecondition(apperr.ReasonFailedPrecondition,
			"client %q of site %q has no %q endpoint group", client, st.Name, site.DefaultGroup)
	}
	out := TestURIResult{GroupID: g.ID, GroupName: g.Name, Default: res.Default || res.GroupID != g.ID}
	if !out.Default {
		out.RuleID, out.Kind = res.RuleID, res.Kind
	}
	out.Policies = []ResolvedPolicy{
		{Kind: policy.KindRotation, Ref: g.RotationRef},
		{Kind: policy.KindSignal, Ref: g.SignalRef},
		{Kind: policy.KindAction, Ref: g.ActionRef},
		{Kind: policy.KindBreaker, Ref: g.BreakerRef},
	}
	return out, nil
}

// truncate shortens user input echoed in error messages to at most n bytes,
// cutting at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
