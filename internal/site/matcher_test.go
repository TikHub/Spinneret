package site_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

const defaultGroupID = "eg_default"

// rule builds a rule whose ID and group are derived from the pattern so that
// results are easy to assert.
func rule(kind site.RuleKind, pattern string, position int) site.Rule {
	id := string(kind) + ":" + pattern
	return site.Rule{ID: id, GroupID: "eg:" + id, GroupName: "group " + pattern, Kind: kind, Pattern: pattern, Position: position}
}

func newMatcher(t *testing.T, rules ...site.Rule) *site.Matcher {
	t.Helper()
	m, err := site.NewMatcher(rules, defaultGroupID)
	require.NoError(t, err)
	return m
}

// assertMatch checks that path matches the rule with the given ID ("" means
// the default group).
func assertMatch(t *testing.T, m *site.Matcher, path, wantRuleID string) {
	t.Helper()
	got := m.Match(path)
	if wantRuleID == "" {
		require.Equal(t, site.MatchResult{GroupID: defaultGroupID, GroupName: site.DefaultGroup, Default: true}, got, "path %q", path)
		return
	}
	require.Equal(t, wantRuleID, got.RuleID, "path %q", path)
	require.False(t, got.Default)
	require.Equal(t, "eg:"+wantRuleID, got.GroupID)
}

func TestMatcherPriorityMatrix(t *testing.T) {
	m := newMatcher(t,
		rule(site.RuleRegex, `profile$`, 0),
		rule(site.RuleRegex, `^/static/.*\.js$`, 1),
		rule(site.RulePrefix, "/api/", 0),
		rule(site.RulePrefix, "/api/v1/", 0),
		rule(site.RuleTemplate, "/api/v1/user/{name}", 9),
		rule(site.RuleTemplate, "/api/v1/{kind}/{name}", 0),
		rule(site.RuleExact, "/api/v1/user/profile", 99),
	)
	tests := []struct {
		name, path, want string
	}{
		{"exact beats everything", "/api/v1/user/profile", "exact:/api/v1/user/profile"},
		{"template beats prefix and regex", "/api/v1/user/alice", "template:/api/v1/user/{name}"},
		{"literal-heavier template beats lower position", "/api/v1/user/bob", "template:/api/v1/user/{name}"},
		{"other template", "/api/v1/order/7", "template:/api/v1/{kind}/{name}"},
		{"template beats regex", "/api/v1/order/profile", "template:/api/v1/{kind}/{name}"},
		{"trailing slash defeats template, longest prefix wins", "/api/v1/user/alice/", "prefix:/api/v1/"},
		{"empty segment does not bind a param", "/api/v1/user/", "prefix:/api/v1/"},
		{"shorter prefix", "/api/v2/x", "prefix:/api/"},
		{"prefix beats regex", "/api/v2/profile", "prefix:/api/"},
		{"regex by position", "/me/profile", "regex:profile$"},
		{"second regex", "/static/app.js", "regex:^/static/.*\\.js$"},
		{"default", "/nothing/here", ""},
		{"prefix is not a match without its trailing slash", "/api", ""},
		{"unnormalized path only reaches regexes", "api/v1/user/profile", "regex:profile$"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertMatch(t, m, tt.path, tt.want)
		})
	}

	got := m.Match("/api/v1/user/profile")
	require.Equal(t, site.MatchResult{
		GroupID: "eg:exact:/api/v1/user/profile", GroupName: "group /api/v1/user/profile",
		RuleID: "exact:/api/v1/user/profile", Kind: site.RuleExact,
	}, got)
	require.Equal(t, 7, m.Len())
}

func TestMatcherLongestPrefix(t *testing.T) {
	patterns := []string{"/abc/", "/ab", "/", "/abd", "/a", "/b/c", "/abc/def/ghi"}
	tests := []struct{ path, want string }{
		{"/abc/x", "/abc/"},
		{"/abc/def/gh", "/abc/"},
		{"/abc/def/ghi/j", "/abc/def/ghi"},
		{"/abc", "/ab"},
		{"/abd", "/abd"},
		{"/abdx", "/abd"},
		{"/abx", "/ab"},
		{"/a", "/a"},
		{"/x", "/"},
		{"/b/", "/"},
		{"/b/cd", "/b/c"},
		{"/", "/"},
	}
	// Every insertion order must build an equivalent radix tree.
	rng := rand.New(rand.NewPCG(1, 2))
	for round := range 20 {
		order := rng.Perm(len(patterns))
		rules := make([]site.Rule, 0, len(patterns))
		for _, i := range order {
			rules = append(rules, rule(site.RulePrefix, patterns[i], 0))
		}
		m := newMatcher(t, rules...)
		for _, tt := range tests {
			assertMatch(t, m, tt.path, "prefix:"+tt.want)
		}
		require.Equal(t, site.DefaultGroup, m.Match("").GroupName, "round %d", round)
		require.True(t, m.Match("x/abc").Default)
	}
}

func TestMatcherTemplateBacktracking(t *testing.T) {
	tests := []struct {
		name  string
		rules []site.Rule
		path  string
		want  string
	}{
		{
			name:  "equal literal counts fall back to position (param branch found by backtracking)",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/{a}/x", 0), rule(site.RuleTemplate, "/api/b/{c}", 1)},
			path:  "/api/b/x", want: "template:/api/{a}/x",
		},
		{
			name:  "equal literal counts, literal branch has lower position",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/{a}/x", 1), rule(site.RuleTemplate, "/api/b/{c}", 0)},
			path:  "/api/b/x", want: "template:/api/b/{c}",
		},
		{
			name:  "literal-heavier template on the param branch wins",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/b/{c}/{d}", 0), rule(site.RuleTemplate, "/api/{a}/x/y", 1)},
			path:  "/api/b/x/y", want: "template:/api/{a}/x/y",
		},
		{
			name:  "literal-heavier wins over lower position",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/{a}/{b}", 0), rule(site.RuleTemplate, "/api/{a}/x", 5)},
			path:  "/api/z/x", want: "template:/api/{a}/x",
		},
		{
			name:  "dead-end literal branch backtracks to param",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/b/c/d", 0), rule(site.RuleTemplate, "/api/{a}/c/e", 1)},
			path:  "/api/b/c/e", want: "template:/api/{a}/c/e",
		},
		{
			name:  "deeper dead end",
			rules: []site.Rule{rule(site.RuleTemplate, "/a/b/c/d/e/f", 0), rule(site.RuleTemplate, "/a/{x}/c/d/e/g", 1), rule(site.RuleTemplate, "/{p}/{q}/{r}/{s}/{t}/{u}", 2)},
			path:  "/a/b/c/d/e/g", want: "template:/a/{x}/c/d/e/g",
		},
		{
			name:  "all-param template as last resort",
			rules: []site.Rule{rule(site.RuleTemplate, "/a/b/c/d/e/f", 0), rule(site.RuleTemplate, "/a/{x}/c/d/e/g", 1), rule(site.RuleTemplate, "/{p}/{q}/{r}/{s}/{t}/{u}", 2)},
			path:  "/a/b/c/d/e/h", want: "template:/{p}/{q}/{r}/{s}/{t}/{u}",
		},
		{
			name:  "same shape different param names resolved by position",
			rules: []site.Rule{rule(site.RuleTemplate, "/i/{id}", 5), rule(site.RuleTemplate, "/i/{key}", 2)},
			path:  "/i/42", want: "template:/i/{key}",
		},
		{
			name:  "trailing slash template",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/item/{id}/", 0), rule(site.RuleTemplate, "/api/item/{id}", 1)},
			path:  "/api/item/1/", want: "template:/api/item/{id}/",
		},
		{
			name:  "no trailing slash template",
			rules: []site.Rule{rule(site.RuleTemplate, "/api/item/{id}/", 0), rule(site.RuleTemplate, "/api/item/{id}", 1)},
			path:  "/api/item/1", want: "template:/api/item/{id}",
		},
		{
			name:  "param does not match an empty segment",
			rules: []site.Rule{rule(site.RuleTemplate, "/a/{x}/b", 0)},
			path:  "/a//b", want: "",
		},
		{
			name:  "literal empty segment",
			rules: []site.Rule{rule(site.RuleTemplate, "/a/{x}/b", 0), rule(site.RuleTemplate, "/a//b", 1)},
			path:  "/a//b", want: "template:/a//b",
		},
		{
			name:  "segment count must match",
			rules: []site.Rule{rule(site.RuleTemplate, "/a/{x}", 0)},
			path:  "/a/b/c", want: "",
		},
		{
			name:  "root template",
			rules: []site.Rule{rule(site.RuleTemplate, "/", 0), rule(site.RuleTemplate, "/{x}", 0)},
			path:  "/", want: "template:/",
		},
		{
			name:  "params match percent-encoded and unicode segments raw",
			rules: []site.Rule{rule(site.RuleTemplate, "/u/{name}/posts", 0)},
			path:  "/u/%E4%B8%AD文/posts", want: "template:/u/{name}/posts",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertMatch(t, newMatcher(t, tt.rules...), tt.path, tt.want)
		})
	}
}

func TestMatcherTieBreaks(t *testing.T) {
	t.Run("same exact pattern, same position: lower id wins", func(t *testing.T) {
		m := newMatcher(t,
			site.Rule{ID: "uri_b", GroupID: "eg_b", Kind: site.RuleExact, Pattern: "/x"},
			site.Rule{ID: "uri_a", GroupID: "eg_a", Kind: site.RuleExact, Pattern: "/x"},
		)
		require.Equal(t, "eg_a", m.Match("/x").GroupID)
	})
	t.Run("same exact pattern: lower position wins", func(t *testing.T) {
		m := newMatcher(t,
			site.Rule{ID: "uri_a", GroupID: "eg_a", Kind: site.RuleExact, Pattern: "/x", Position: 3},
			site.Rule{ID: "uri_b", GroupID: "eg_b", Kind: site.RuleExact, Pattern: "/x", Position: 1},
		)
		require.Equal(t, "eg_b", m.Match("/x").GroupID)
	})
	t.Run("identical position and id: input order wins", func(t *testing.T) {
		m := newMatcher(t,
			site.Rule{ID: "uri", GroupID: "eg_first", Kind: site.RulePrefix, Pattern: "/x"},
			site.Rule{ID: "uri", GroupID: "eg_second", Kind: site.RulePrefix, Pattern: "/x"},
		)
		require.Equal(t, "eg_first", m.Match("/x/y").GroupID)
	})
	t.Run("same prefix: lower position wins regardless of order", func(t *testing.T) {
		m := newMatcher(t,
			site.Rule{ID: "uri_a", GroupID: "eg_a", Kind: site.RulePrefix, Pattern: "/p/", Position: 2},
			site.Rule{ID: "uri_b", GroupID: "eg_b", Kind: site.RulePrefix, Pattern: "/p/", Position: 1},
		)
		require.Equal(t, "eg_b", m.Match("/p/q").GroupID)
	})
	t.Run("regexes by position, not input order", func(t *testing.T) {
		m := newMatcher(t,
			site.Rule{ID: "uri_a", GroupID: "eg_a", Kind: site.RuleRegex, Pattern: "a", Position: 2},
			site.Rule{ID: "uri_b", GroupID: "eg_b", Kind: site.RuleRegex, Pattern: "ab", Position: 1},
			site.Rule{ID: "uri_c", GroupID: "eg_c", Kind: site.RuleRegex, Pattern: "^/c", Position: 0},
		)
		require.Equal(t, "eg_b", m.Match("/xab").GroupID)
		require.Equal(t, "eg_a", m.Match("/xa").GroupID)
		require.Equal(t, "eg_c", m.Match("/cab").GroupID)
	})
}

func TestMatcherDefaults(t *testing.T) {
	var nilMatcher *site.Matcher
	require.Equal(t, site.MatchResult{GroupName: site.DefaultGroup, Default: true}, nilMatcher.Match("/x"))
	require.Zero(t, nilMatcher.Len())

	var zero site.Matcher
	require.Equal(t, site.MatchResult{GroupName: site.DefaultGroup, Default: true}, zero.Match("/x"), "zero Matcher must not panic")
	require.Zero(t, zero.Len())

	empty, err := site.NewMatcher(nil, "")
	require.NoError(t, err)
	require.Equal(t, site.MatchResult{GroupName: site.DefaultGroup, Default: true}, empty.Match("/x"))
	require.Zero(t, empty.Len())

	m := newMatcher(t)
	assertMatch(t, m, "/anything", "")
	assertMatch(t, m, "", "")
}

func TestMatcherMatchURI(t *testing.T) {
	m := newMatcher(t, rule(site.RuleExact, "/api/v1/item/detail/", 0))
	got, err := m.MatchURI("https://target.example.com/api/v1/item/detail/?item_id=1")
	require.NoError(t, err)
	require.Equal(t, "exact:/api/v1/item/detail/", got.RuleID)

	got, err = m.MatchURI("/other?x")
	require.NoError(t, err)
	require.True(t, got.Default)

	got, err = m.MatchURI("not a uri")
	requireAppErr(t, err, apperr.ReasonURIInvalid, "")
	require.Equal(t, site.MatchResult{}, got)
}

func TestNewMatcherErrors(t *testing.T) {
	tests := []struct {
		name     string
		rule     site.Rule
		contains string
	}{
		{"missing group", site.Rule{ID: "uri_1", Kind: site.RuleExact, Pattern: "/a"}, `uri rule at index 1 (id "uri_1", kind "exact"): endpoint group id is empty`},
		{"invalid exact", site.Rule{ID: "uri_1", GroupID: "eg", Kind: site.RuleExact, Pattern: "a"}, `must start with "/"`},
		{"invalid prefix", site.Rule{ID: "uri_1", GroupID: "eg", Kind: site.RulePrefix, Pattern: "/a?"}, `must not contain "?"`},
		{"invalid template", site.Rule{ID: "uri_1", GroupID: "eg", Kind: site.RuleTemplate, Pattern: "/a{b}"}, "whole segment"},
		{"invalid regex", site.Rule{ID: "uri_1", GroupID: "eg", Kind: site.RuleRegex, Pattern: "("}, "invalid regex"},
		{"too complex regex", site.Rule{ID: "uri_1", GroupID: "eg", Kind: site.RuleRegex, Pattern: `(?:.?.?.?.?.?.?.?.?.?.?){1000}`}, "regex is too complex"},
		{"unknown kind", site.Rule{ID: "uri_1", GroupID: "eg", Kind: "glob", Pattern: "/a"}, `unknown uri rule kind "glob"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules := []site.Rule{rule(site.RuleExact, "/ok", 0), tt.rule}
			m, err := site.NewMatcher(rules, defaultGroupID)
			require.Nil(t, m)
			requireAppErr(t, err, apperr.ReasonInvalidArgument, tt.contains)
			require.Contains(t, err.Error(), "uri rule at index 1")
		})
	}
}

func TestMatcherDoesNotAllocate(t *testing.T) {
	m := newMatcher(t,
		rule(site.RuleExact, "/e", 0),
		rule(site.RuleTemplate, "/t/{a}/x", 0),
		rule(site.RuleTemplate, "/t/b/{c}", 1),
		rule(site.RulePrefix, "/p/", 0),
		rule(site.RuleRegex, "^/r/", 0),
	)
	for _, path := range []string{"/e", "/t/b/x", "/p/q", "/r/s", "/none"} {
		allocs := testing.AllocsPerRun(200, func() { _ = m.Match(path) })
		require.Zero(t, allocs, path)
	}
}

func TestMatcherConcurrentReads(t *testing.T) {
	m := newMatcher(t,
		rule(site.RuleExact, "/e", 0),
		rule(site.RuleTemplate, "/t/{a}", 0),
		rule(site.RulePrefix, "/p/", 0),
		rule(site.RuleRegex, "^/r/", 0),
	)
	want := map[string]string{"/e": "exact:/e", "/t/1": "template:/t/{a}", "/p/1": "prefix:/p/", "/r/1": "regex:^/r/", "/n": ""}
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			for range 500 {
				for path, id := range want {
					if got := m.Match(path).RuleID; got != id {
						errs <- fmt.Sprintf("%s: got %q want %q", path, got, id)
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.Fail(t, e)
	}
}

// referenceMatch is a brute-force implementation of the documented priority
// rules, used to cross-check the trie and radix tree.
func referenceMatch(rules []site.Rule, path string) site.MatchResult {
	rank := func(i int) [3]any { return [3]any{rules[i].Position, rules[i].ID, i} }
	less := func(a, b int) bool {
		ra, rb := rank(a), rank(b)
		if ra[0].(int) != rb[0].(int) {
			return ra[0].(int) < rb[0].(int)
		}
		if ra[1].(string) != rb[1].(string) {
			return ra[1].(string) < rb[1].(string)
		}
		return ra[2].(int) < rb[2].(int)
	}
	result := func(i int) site.MatchResult {
		r := rules[i]
		return site.MatchResult{GroupID: r.GroupID, GroupName: r.GroupName, RuleID: r.ID, Kind: r.Kind}
	}
	best := -1
	for i, r := range rules {
		if r.Kind == site.RuleExact && r.Pattern == path && (best < 0 || less(i, best)) {
			best = i
		}
	}
	if best >= 0 {
		return result(best)
	}
	bestLits, bestParams := -1, 0
	for i, r := range rules {
		if r.Kind != site.RuleTemplate {
			continue
		}
		lits, params, ok := referenceTemplate(r.Pattern, path)
		if !ok {
			continue
		}
		better := best < 0 || lits > bestLits || (lits == bestLits && params < bestParams) ||
			(lits == bestLits && params == bestParams && less(i, best))
		if better {
			best, bestLits, bestParams = i, lits, params
		}
	}
	if best >= 0 {
		return result(best)
	}
	for i, r := range rules {
		if r.Kind != site.RulePrefix || len(r.Pattern) > len(path) || path[:len(r.Pattern)] != r.Pattern {
			continue
		}
		if best < 0 || len(r.Pattern) > len(rules[best].Pattern) ||
			(len(r.Pattern) == len(rules[best].Pattern) && less(i, best)) {
			best = i
		}
	}
	if best >= 0 {
		return result(best)
	}
	for i, r := range rules {
		if r.Kind != site.RuleRegex || !regexpMatch(r.Pattern, path) {
			continue
		}
		if best < 0 || less(i, best) {
			best = i
		}
	}
	if best >= 0 {
		return result(best)
	}
	return site.MatchResult{GroupID: defaultGroupID, GroupName: site.DefaultGroup, Default: true}
}

func referenceTemplate(pattern, path string) (lits, params int, ok bool) {
	if path == "" || path[0] != '/' {
		return 0, 0, false
	}
	ps, xs := splitSegments(pattern), splitSegments(path)
	if len(ps) != len(xs) {
		return 0, 0, false
	}
	for i, p := range ps {
		if len(p) > 1 && p[0] == '{' {
			if xs[i] == "" {
				return 0, 0, false
			}
			params++
			continue
		}
		if p != xs[i] {
			return 0, 0, false
		}
		lits++
	}
	return lits, params, true
}

func splitSegments(s string) []string {
	var out []string
	start := 1
	for i := 1; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func regexpMatch(pattern, path string) bool {
	m, err := site.NewMatcher([]site.Rule{{ID: "r", GroupID: "g", Kind: site.RuleRegex, Pattern: pattern}}, "")
	return err == nil && !m.Match(path).Default
}

func TestMatcherAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 7))
	literals := []string{"a", "b", "ab", ""}
	randomPath := func(maxSegs int) string {
		n := 1 + rng.IntN(maxSegs)
		p := ""
		for range n {
			p += "/" + literals[rng.IntN(len(literals))]
		}
		return p
	}
	for round := range 300 {
		var rules []site.Rule
		for i := range 1 + rng.IntN(25) {
			var r site.Rule
			switch rng.IntN(4) {
			case 0:
				r = site.Rule{Kind: site.RuleExact, Pattern: randomPath(4)}
			case 1:
				n := 1 + rng.IntN(4)
				p := ""
				for s := range n {
					if rng.IntN(2) == 0 {
						p += fmt.Sprintf("/{p%d}", s)
					} else {
						p += "/" + literals[rng.IntN(len(literals))]
					}
				}
				r = site.Rule{Kind: site.RuleTemplate, Pattern: p}
			case 2:
				full := randomPath(4)
				r = site.Rule{Kind: site.RulePrefix, Pattern: full[:1+rng.IntN(len(full))]}
			default:
				r = site.Rule{Kind: site.RuleRegex, Pattern: []string{"a$", "^/b", "ab/", "//"}[rng.IntN(4)]}
			}
			r.ID = fmt.Sprintf("uri_%02d", rng.IntN(30))
			r.GroupID = fmt.Sprintf("eg_%d", i)
			r.Position = rng.IntN(4)
			rules = append(rules, r)
		}
		m, err := site.NewMatcher(rules, defaultGroupID)
		require.NoError(t, err)
		for range 60 {
			path := randomPath(5)
			require.Equal(t, referenceMatch(rules, path), m.Match(path), "round %d path %q rules %+v", round, path, rules)
		}
	}
}

// benchmarkRules builds 10 000 rules shaped like a large real site: many
// exact endpoints, templates sharing literal prefixes, nested prefixes and a
// few regexes.
func benchmarkRules() []site.Rule {
	rules := make([]site.Rule, 0, 10_000)
	add := func(kind site.RuleKind, pattern string) {
		n := len(rules)
		rules = append(rules, site.Rule{
			ID: fmt.Sprintf("uri_%05d", n), GroupID: fmt.Sprintf("eg_%04d", n%1000),
			GroupName: fmt.Sprintf("group%d", n%1000), Kind: kind, Pattern: pattern, Position: n,
		})
	}
	for i := range 4000 {
		add(site.RuleExact, fmt.Sprintf("/api/v1/service%d/endpoint%d/", i%40, i))
	}
	for i := range 3500 {
		add(site.RuleTemplate, fmt.Sprintf("/api/v%d/resource%d/{id}/sub%d/{sub}", i%5, i%700, i))
	}
	for i := range 2450 {
		add(site.RulePrefix, fmt.Sprintf("/static/bucket%d/shard%d/", i%50, i))
	}
	for i := range 50 {
		add(site.RuleRegex, fmt.Sprintf(`^/legacy/v%d/[a-z]+/\d+$`, i))
	}
	return rules
}

func BenchmarkMatcher10kRules(b *testing.B) {
	rules := benchmarkRules()
	m, err := site.NewMatcher(rules, defaultGroupID)
	require.NoError(b, err)
	require.Equal(b, 10_000, m.Len())

	cases := []struct{ name, path, kind string }{
		{"exact", "/api/v1/service17/endpoint3657/", "exact"},
		{"template", "/api/v2/resource312/123456789/sub3112/comments", "template"},
		{"prefix", "/static/bucket21/shard1471/img/logo.png", "prefix"},
		{"regex_last", "/legacy/v49/items/42", "regex"},
		{"default", "/unknown/path/that/matches/nothing", ""},
	}
	for _, c := range cases {
		got := m.Match(c.path)
		require.Equal(b, site.RuleKind(c.kind), got.Kind, c.name)
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = m.Match(c.path)
			}
		})
	}
}

func BenchmarkNewMatcher10kRules(b *testing.B) {
	rules := benchmarkRules()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := site.NewMatcher(rules, defaultGroupID); err != nil {
			b.Fatal(err)
		}
	}
}
