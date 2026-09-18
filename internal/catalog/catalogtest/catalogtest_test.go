package catalogtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/site"
)

const cookieType = `
name: demo_web_cookie
site: demo
client: web
fields:
  cookies: { type: cookie_map, required: true, sensitive: true }
unique_by: [cookies.sessionid]
deliver:
  cookie_header: "{{ cookies }}"
`

func TestBuildersAndCatalog(t *testing.T) {
	ns := NewNamespace("ten_1", "ns_1", "prod")
	s := AddSite(ns, "sit_1", "demo", 7, "web", "app")
	g := AddGroup(s, "eg_search", "web", "search", 70, site.Rule{Kind: site.RulePrefix, Pattern: "/search/"})
	AddIdentityType(s, MustCompileType("ity_1", s.ID, 1, cookieType))

	c := New(ns)
	got, ok := c.NamespaceByName("ten_1", "prod")
	require.True(t, ok)
	require.Same(t, ns, got)

	matched, res, ok := got.Sites["demo"].MatchGroup("web", "/search/x")
	require.True(t, ok)
	require.Same(t, g, matched)
	require.False(t, res.Default)

	def, res, ok := s.MatchGroup("web", "/other")
	require.True(t, ok)
	require.True(t, res.Default)
	require.Equal(t, site.DefaultGroup, def.Name)
	require.Equal(t, []string{"ity_1"}, g.IdentityTypeIDs)
	require.Empty(t, s.Groups[catalog.GroupKey{Client: "app", Name: site.DefaultGroup}].IdentityTypeIDs)

	bySite, _, ok := c.SiteByKey(7)
	require.True(t, ok)
	require.Same(t, s, bySite)
	_, _, ok = c.Site("sit_missing")
	require.False(t, ok)

	var notified []string
	unregister := c.OnChange(func(id string) { notified = append(notified, id) })
	require.NoError(t, c.Invalidate(context.Background(), "ns_1"))
	unregister()
	require.NoError(t, c.Reload(context.Background(), "ns_1"))
	require.NoError(t, c.ReloadAll(context.Background()))
	require.Equal(t, []string{"ns_1"}, notified)
	require.Equal(t, []string{"invalidate:ns_1", "reload:ns_1", "reload_all"}, c.Calls())
	require.Len(t, c.Namespaces("ten_1"), 1)
	require.Empty(t, c.Namespaces("ten_2"))
	c.Remove("ns_1")
	_, ok = c.Namespace("ns_1")
	require.False(t, ok)
}
