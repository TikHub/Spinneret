package sitesvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/site"
)

func TestCreateEndpointGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop", "web", "app")

	g, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{
		Client: "web", Name: "search", Description: "search APIs", LowWatermark: 10,
		Rules: []URIRule{
			{Kind: site.RulePrefix, Pattern: "/api/v1/search/"},
			{ID: "ignored", Kind: site.RuleExact, Pattern: "/search", Position: 99},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "search", g.Name)
	require.Equal(t, "web", g.Client)
	require.Equal(t, s.ID, g.Site.ID)
	require.Equal(t, s.Key, g.Site.Key)
	require.Equal(t, 10, g.LowWatermark)
	require.Equal(t, BreakerClosed, g.BreakerState)
	require.Len(t, g.Rules, 2)
	require.NotEqual(t, "ignored", g.Rules[1].ID)
	require.Equal(t, []int{0, 1}, []int{g.Rules[0].Position, g.Rules[1].Position})
	require.Equal(t, ActionEndpointGroupCreate, e.audit.last().Action)

	cs, _, ok := e.cat.Site(s.ID)
	require.True(t, ok)
	cg, res, ok := cs.MatchGroup("web", "/api/v1/search/x")
	require.True(t, ok)
	require.Equal(t, g.ID, cg.ID)
	require.Equal(t, site.RulePrefix, res.Kind)

	tests := []struct {
		name   string
		in     CreateGroupInput
		reason apperr.Reason
	}{
		{"duplicate", CreateGroupInput{Client: "web", Name: "search"}, apperr.ReasonAlreadyExists},
		{"reserved", CreateGroupInput{Client: "web", Name: site.DefaultGroup}, apperr.ReasonInvalidArgument},
		{"unknown client", CreateGroupInput{Client: "ios", Name: "x"}, apperr.ReasonClientUnknown},
		{"malformed client", CreateGroupInput{Client: "Web", Name: "x"}, apperr.ReasonClientUnknown},
		{"bad name", CreateGroupInput{Client: "web", Name: "X"}, apperr.ReasonInvalidArgument},
		{"negative watermark", CreateGroupInput{Client: "web", Name: "x", LowWatermark: -1}, apperr.ReasonInvalidArgument},
		{"long description", CreateGroupInput{Client: "web", Name: "x", Description: strings.Repeat("d", MaxDescriptionLength+1)}, apperr.ReasonInvalidArgument},
		{"invalid rule", CreateGroupInput{Client: "web", Name: "x", Rules: []URIRule{{Kind: site.RuleRegex, Pattern: "("}}}, apperr.ReasonInvalidArgument},
		{"conflicting rule", CreateGroupInput{Client: "web", Name: "x", Rules: []URIRule{{Kind: site.RuleExact, Pattern: "/search"}}}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, tc.in)
			requireCode(t, err, tc.reason)
		})
	}
	_, err = e.svc.CreateEndpointGroup(ctx, e.actor, "sit_missing", CreateGroupInput{Client: "web", Name: "x"})
	requireCode(t, err, apperr.ReasonNotFound)

	// The same pattern is allowed for another client.
	_, err = e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "app", Name: "search",
		Rules: []URIRule{{Kind: site.RuleExact, Pattern: "/search"}}})
	require.NoError(t, err)
}

func TestUpdateAndDeleteEndpointGroup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop")
	g, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: "search", Description: "old"})
	require.NoError(t, err)

	desc, low := "new", 7
	updated, err := e.svc.UpdateEndpointGroup(ctx, e.actor, g.ID, UpdateGroupInput{Description: &desc})
	require.NoError(t, err)
	require.Equal(t, "new", updated.Description)
	require.Equal(t, 0, updated.LowWatermark)
	updated, err = e.svc.UpdateEndpointGroup(ctx, e.actor, g.ID, UpdateGroupInput{LowWatermark: &low})
	require.NoError(t, err)
	require.Equal(t, "new", updated.Description)
	require.Equal(t, 7, updated.LowWatermark)
	cs, _, _ := e.cat.Site(s.ID)
	cg, _ := cs.Group("web", "search")
	require.Equal(t, 7, cg.LowWatermark)

	neg := -1
	_, err = e.svc.UpdateEndpointGroup(ctx, e.actor, g.ID, UpdateGroupInput{LowWatermark: &neg})
	requireCode(t, err, apperr.ReasonInvalidArgument)
	long := strings.Repeat("d", MaxDescriptionLength+1)
	_, err = e.svc.UpdateEndpointGroup(ctx, e.actor, g.ID, UpdateGroupInput{Description: &long})
	requireCode(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.UpdateEndpointGroup(ctx, e.actor, "eg_missing", UpdateGroupInput{Description: &desc})
	requireCode(t, err, apperr.ReasonNotFound)

	// _default groups can be updated but not deleted.
	def, ok := cs.Group("web", site.DefaultGroup)
	require.True(t, ok)
	_, err = e.svc.UpdateEndpointGroup(ctx, e.actor, def.ID, UpdateGroupInput{LowWatermark: &low})
	require.NoError(t, err)
	requireCode(t, e.svc.DeleteEndpointGroup(ctx, e.actor, def.ID), apperr.ReasonFailedPrecondition)

	require.NoError(t, e.svc.DeleteEndpointGroup(ctx, e.actor, g.ID))
	require.Equal(t, ActionEndpointGroupDelete, e.audit.last().Action)
	cs, _, _ = e.cat.Site(s.ID)
	_, ok = cs.Group("web", "search")
	require.False(t, ok)
	requireCode(t, e.svc.DeleteEndpointGroup(ctx, e.actor, g.ID), apperr.ReasonNotFound)
}

func TestListEndpointGroupsWithHotState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop", "web", "app")
	for _, name := range []string{"search", "detail", "feed"} {
		_, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "web", Name: name,
			Rules: []URIRule{{Kind: site.RulePrefix, Pattern: "/" + name + "/"}}})
		require.NoError(t, err)
	}
	cs, _, _ := e.cat.Site(s.ID)
	search, _ := cs.Group("web", "search")
	detail, _ := cs.Group("web", "detail")

	now := time.Now().UnixMilli()
	ready := e.keys.Ready(s.Key, search.Key)
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Zadd().Key(ready).ScoreMember().
		ScoreMember(float64(now-1000), "1").ScoreMember(float64(now-10), "2").ScoreMember(float64(now+60000), "3").Build()).Error())
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Hset().Key(e.keys.Breaker(s.Key, search.Key)).FieldValue().
		FieldValue("st", "open").FieldValue("v", "3").Build()).Error())
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Hset().Key(e.keys.Breaker(s.Key, detail.Key)).FieldValue().
		FieldValue("st", "half_open").Build()).Error())

	var all []EndpointGroup
	after := GroupCursor{}
	for {
		page, err := e.svc.ListEndpointGroups(ctx, SiteRef{ID: s.ID, Key: s.Key, Name: s.Name}, "", 2, after)
		require.NoError(t, err)
		require.Equal(t, 5, page.Total)
		all = append(all, page.Groups...)
		if page.Next == nil {
			break
		}
		after = *page.Next
	}
	require.Len(t, all, 5)
	names := make([]string, 0, len(all))
	for _, g := range all {
		names = append(names, g.Client+"/"+g.Name)
	}
	require.Equal(t, []string{"app/_default", "web/_default", "web/detail", "web/feed", "web/search"}, names)
	byName := map[string]EndpointGroup{}
	for _, g := range all {
		byName[g.Client+"/"+g.Name] = g
	}
	require.Equal(t, int64(2), byName["web/search"].AvailableIdentities)
	require.Equal(t, BreakerOpen, byName["web/search"].BreakerState)
	require.Equal(t, BreakerHalfOpen, byName["web/detail"].BreakerState)
	require.Equal(t, BreakerClosed, byName["web/feed"].BreakerState)
	require.Len(t, byName["web/feed"].Rules, 1)
	require.Empty(t, byName["web/_default"].Rules)
	require.NotNil(t, byName["web/_default"].Rules)

	webOnly, err := e.svc.ListEndpointGroups(ctx, SiteRef{ID: s.ID, Key: s.Key}, "app", 0, GroupCursor{})
	require.NoError(t, err)
	require.Equal(t, 1, webOnly.Total)
	require.Nil(t, webOnly.Next)

	got, err := e.svc.GetEndpointGroup(ctx, search.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.AvailableIdentities)
	require.Equal(t, BreakerOpen, got.BreakerState)
	require.Equal(t, "prod", got.Site.NamespaceName)
	_, err = e.svc.GetEndpointGroup(ctx, "eg_missing")
	requireCode(t, err, apperr.ReasonNotFound)

	// Without Redis the figures default to zero and closed.
	noRedis := NewService(e.pool, e.cat, nil, nil, nil, e.keys, nil)
	got, err = noRedis.GetEndpointGroup(ctx, search.ID)
	require.NoError(t, err)
	require.Zero(t, got.AvailableIdentities)
	require.Equal(t, BreakerClosed, got.BreakerState)

	// A wrong key type surfaces as an internal error.
	require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Set().Key(e.keys.Ready(s.Key, detail.Key)).Value("x").Build()).Error())
	_, err = e.svc.GetEndpointGroup(ctx, detail.ID)
	requireCode(t, err, apperr.ReasonInternal)
}
