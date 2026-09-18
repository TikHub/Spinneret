package sitesvc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/site"
)

func regexRules(prefix string, n int) []URIRule {
	out := make([]URIRule, n)
	for i := range out {
		out[i] = URIRule{Kind: site.RuleRegex, Pattern: fmt.Sprintf("^/%s/%d/[0-9]+$", prefix, i)}
	}
	return out
}

func TestReplaceURIRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop")
	search, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: "search",
		Rules: []URIRule{{Kind: site.RulePrefix, Pattern: "/old/"}}})
	require.NoError(t, err)
	detail, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: "detail",
		Rules: []URIRule{{Kind: site.RuleTemplate, Pattern: "/item/{id}"}}})
	require.NoError(t, err)

	stored, err := e.svc.ReplaceURIRules(ctx, e.actor, search.ID, []URIRule{
		{Kind: site.RuleExact, Pattern: "/search"},
		{Kind: site.RuleRegex, Pattern: "^/s/[a-z]+$"},
		{Kind: site.RulePrefix, Pattern: "/search/"},
	})
	require.NoError(t, err)
	require.Len(t, stored, 3)
	for i, r := range stored {
		require.Equal(t, i, r.Position)
		require.NotEmpty(t, r.ID)
	}
	entry := e.audit.last()
	require.Equal(t, ActionURIRulesReplace, entry.Action)
	require.Equal(t, 3, entry.Details["rules"])

	cs, _, _ := e.cat.Site(s.ID)
	g, res, ok := cs.MatchGroup("web", "/s/abc")
	require.True(t, ok)
	require.Equal(t, search.ID, g.ID)
	require.Equal(t, stored[1].ID, res.RuleID)
	g, _, _ = cs.MatchGroup("web", "/old/x")
	require.Equal(t, site.DefaultGroup, g.Name, "old rules are gone")

	// Paging through the stored rules.
	var listed []URIRule
	var cursor *RuleCursor
	for {
		page, err := e.svc.ListURIRules(ctx, search.ID, 2, cursor)
		require.NoError(t, err)
		require.Equal(t, 3, page.Total)
		listed = append(listed, page.Rules...)
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	require.Equal(t, stored, listed)

	tooMany := make([]URIRule, MaxRulesPerGroup+1)
	for i := range tooMany {
		tooMany[i] = URIRule{Kind: site.RuleExact, Pattern: fmt.Sprintf("/p/%d", i)}
	}
	tests := []struct {
		name   string
		group  string
		rules  []URIRule
		reason apperr.Reason
	}{
		{"invalid regex", search.ID, []URIRule{{Kind: site.RuleRegex, Pattern: "(["}}, apperr.ReasonInvalidArgument},
		{"invalid template", search.ID, []URIRule{{Kind: site.RuleTemplate, Pattern: "/a/{id}/{id}"}}, apperr.ReasonInvalidArgument},
		{"unknown kind", search.ID, []URIRule{{Kind: "glob", Pattern: "/a"}}, apperr.ReasonInvalidArgument},
		{"relative path", search.ID, []URIRule{{Kind: site.RuleExact, Pattern: "a"}}, apperr.ReasonInvalidArgument},
		{"duplicate in list", search.ID, []URIRule{{Kind: site.RuleExact, Pattern: "/a"}, {Kind: site.RuleExact, Pattern: "/a"}}, apperr.ReasonInvalidArgument},
		{"duplicate of other group", search.ID, []URIRule{{Kind: site.RuleTemplate, Pattern: "/item/{id}"}}, apperr.ReasonInvalidArgument},
		{"too many rules", search.ID, tooMany, apperr.ReasonInvalidArgument},
		{"too many regexes", search.ID, regexRules("r", MaxRegexRulesPerClient+1), apperr.ReasonInvalidArgument},
		{"missing group", "eg_missing", nil, apperr.ReasonNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.ReplaceURIRules(ctx, e.actor, tc.group, tc.rules)
			requireCode(t, err, tc.reason)
		})
	}
	listed2, err := e.svc.ListURIRules(ctx, search.ID, 0, nil)
	require.NoError(t, err)
	require.Equal(t, stored, listed2.Rules, "failed replacements leave the rules untouched")

	// _default groups cannot have rules.
	def, _ := cs.Group("web", site.DefaultGroup)
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, def.ID, []URIRule{{Kind: site.RuleExact, Pattern: "/x"}})
	requireCode(t, err, apperr.ReasonInvalidArgument)

	// The regex limit counts every group of the client.
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, detail.ID, regexRules("d", MaxRegexRulesPerClient))
	requireCode(t, err, apperr.ReasonInvalidArgument) // search already has one regex rule
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, detail.ID, regexRules("d", MaxRegexRulesPerClient-1))
	require.NoError(t, err)

	// A client above the regex limit (e.g. rows written by an older version)
	// accepts changes that do not add regex rules.
	legacy, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: "legacy"})
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		e.exec(`INSERT INTO uri_rules (id, endpoint_group_id, kind, pattern, position) VALUES ($1, $2, 'regex', $3, $4)`,
			fmt.Sprintf("uri_legacy_%d", i), legacy.ID, fmt.Sprintf("^/legacy/%d$", i), i)
	}
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, legacy.ID, regexRules("legacy", 6))
	requireCode(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, legacy.ID, regexRules("legacy", 4))
	require.NoError(t, err)

	// An empty list removes all rules.
	cleared, err := e.svc.ReplaceURIRules(ctx, e.actor, search.ID, nil)
	require.NoError(t, err)
	require.Empty(t, cleared)
	require.Zero(t, e.count(`SELECT count(*) FROM uri_rules WHERE endpoint_group_id = $1`, search.ID))
}

// TestReplaceURIRulesConcurrentRegexLimit checks that the per-client regex
// limit holds when groups are changed concurrently.
func TestReplaceURIRulesConcurrentRegexLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop")
	const groups, perGroup = 5, 50
	ids := make([]string, groups)
	for i := range ids {
		g, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: fmt.Sprintf("g%d", i)})
		require.NoError(t, err)
		ids[i] = g.ID
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, err := e.svc.ReplaceURIRules(ctx, e.actor, id, regexRules(fmt.Sprintf("g%d", i), perGroup))
			if err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			} else if apperr.ReasonOf(err) != apperr.ReasonInvalidArgument {
				t.Errorf("unexpected error: %v", err)
			}
		}(i, id)
	}
	wg.Wait()
	require.Equal(t, MaxRegexRulesPerClient/perGroup, succeeded)
	require.Equal(t, MaxRegexRulesPerClient, e.count(
		`SELECT count(*) FROM uri_rules r JOIN endpoint_groups g ON g.id = r.endpoint_group_id WHERE g.site_id = $1 AND r.kind = 'regex'`, s.ID))
}

func TestTestURI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop", "web", "app")
	search, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: "search",
		Rules: []URIRule{{Kind: site.RulePrefix, Pattern: "/search/"}}})
	require.NoError(t, err)
	cs, _, _ := e.cat.Site(s.ID)

	res, err := e.svc.TestURI(cs, "web", "https://target.example.com/search/abc?q=secret")
	require.NoError(t, err)
	require.Equal(t, search.ID, res.GroupID)
	require.Equal(t, "search", res.GroupName)
	require.Equal(t, search.Rules[0].ID, res.RuleID)
	require.Equal(t, site.RulePrefix, res.Kind)
	require.False(t, res.Default)
	require.Len(t, res.Policies, 4)
	for i, kind := range policy.Kinds() {
		require.Equal(t, kind, res.Policies[i].Kind)
		require.Equal(t, catalog.PolicyRef{Name: policy.DefaultPolicyName(kind), Level: policy.LevelBuiltin}, res.Policies[i].Ref)
	}

	res, err = e.svc.TestURI(cs, "app", "/search/abc")
	require.NoError(t, err)
	require.True(t, res.Default)
	require.Equal(t, site.DefaultGroup, res.GroupName)
	require.Empty(t, res.RuleID)
	require.Empty(t, res.Kind)

	_, err = e.svc.TestURI(cs, "ios", "/x")
	requireCode(t, err, apperr.ReasonClientUnknown)
	_, err = e.svc.TestURI(cs, "web", "not a uri")
	requireCode(t, err, apperr.ReasonURIInvalid)
	_, err = e.svc.TestURI(nil, "web", "/x")
	requireCode(t, err, apperr.ReasonSiteUnknown)

	ns := catalogtest.NewNamespace("ten_1", "ns_1", "prod")
	broken := catalogtest.AddSite(ns, "sit_1", "broken", 1, "web")
	broken.Clients = append(broken.Clients, "app") // declared client without matcher
	_, err = e.svc.TestURI(broken, "app", "/x")
	requireCode(t, err, apperr.ReasonFailedPrecondition)
	require.Equal(t, strings.Repeat("a", 3)+"...", truncate("aaaa", 3))
	require.Equal(t, "é", strings.TrimSuffix(truncate("éé", 3), "..."))
}
