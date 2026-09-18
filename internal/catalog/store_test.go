package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// fixture is a namespace with sites, groups, rules, identity types and
// policies bound at every level.
type fixture struct {
	tenantID, nsID                         string
	shopID, marketID, bareID               string
	shopKey                                int64
	webDefault, search, detail, appDefault string
	searchKey                              int64
	marketDefault                          string
	searchPrefixRule, detailTemplateRule   string
	detailRegexRule                        string
	webTypeID, appTypeID                   string
	nsRotation, detailRotation, leafSignal string
	webAction, searchBreaker               string
}

func seedFixture(t *testing.T, sd *seeder) fixture {
	t.Helper()
	var f fixture
	f.tenantID = sd.tenant("acme")
	f.nsID = sd.namespace(f.tenantID, "prod")
	f.shopID, f.shopKey = sd.site(f.nsID, "shop", "web", "app")
	f.marketID, _ = sd.site(f.nsID, "market", "web")
	f.bareID, _ = sd.site(f.nsID, "bare", "web") // no endpoint groups at all

	f.webDefault, _ = sd.group(f.shopID, "web", site.DefaultGroup)
	f.search, f.searchKey = sd.group(f.shopID, "web", "search")
	f.detail, _ = sd.group(f.shopID, "web", "detail")
	f.appDefault, _ = sd.group(f.shopID, "app", site.DefaultGroup)
	f.marketDefault, _ = sd.group(f.marketID, "web", site.DefaultGroup)

	f.searchPrefixRule = sd.rule(f.search, "prefix", "/api/v1/search/", 0)
	sd.rule(f.search, "exact", "/search/exact", 1)
	f.detailTemplateRule = sd.rule(f.detail, "template", "/api/v1/item/{id}", 0)
	f.detailRegexRule = sd.rule(f.detail, "regex", "^/detail/[0-9]+$", 1)
	sd.rule(f.detail, "regex", "(", 2) // invalid, skipped by the loader

	f.webTypeID = sd.identityTypeYAML(f.shopID, webTypeYAML, 3)
	f.appTypeID = sd.identityTypeYAML(f.shopID, appTypeYAML, 1)
	sd.identityTypeRaw(f.shopID, "web", "broken_type", []byte(`{"name":"broken_type"}`), 1)

	// Rotation: namespace level with two versions, an unpublished policy bound
	// at endpoint-group level (ignored) and an endpoint-group level policy.
	f.nsRotation = sd.policyYAML(f.nsID, policy.KindRotation,
		"name: ns-rotation\nrotation: {strategy: round_robin}\n",
		"name: ns-rotation\nrotation: {strategy: best_health}\n")
	sd.bind(f.nsID, f.nsRotation, policy.KindRotation, "", "", "")
	unpublished := sd.policyRaw(f.nsID, policy.KindRotation, "draft-rotation", 0)
	sd.bind(f.nsID, unpublished, policy.KindRotation, f.shopID, "web", f.search)
	f.detailRotation = sd.policyYAML(f.nsID, policy.KindRotation,
		"name: detail-rotation\nidentity_types: [web_cookie, missing_type, app_device]\n")
	sd.bind(f.nsID, f.detailRotation, policy.KindRotation, f.shopID, "web", f.detail)

	// Signal: a three-level extends chain bound at site level, and a cycle
	// bound on another site (falls back to the built-in default).
	sd.policyYAML(f.nsID, policy.KindSignal,
		"name: base-signal\nrules:\n  - {name: teapot, when: {http_status: [418]}, outcome: captcha}\n")
	sd.policyYAML(f.nsID, policy.KindSignal,
		"name: mid-signal\nextends: base-signal\nrules:\n  - {name: blocked, when: {markers: [blocked]}, outcome: banned}\n")
	f.leafSignal = sd.policyYAML(f.nsID, policy.KindSignal,
		"name: leaf-signal\nextends: mid-signal\nrules:\n  - {name: ok, when: {http_status: [200]}, outcome: success}\n")
	sd.bind(f.nsID, f.leafSignal, policy.KindSignal, f.shopID, "", "")
	cycleA := sd.policyYAML(f.nsID, policy.KindSignal, "name: cycle-a\nextends: cycle-b\n")
	sd.policyYAML(f.nsID, policy.KindSignal, "name: cycle-b\nextends: cycle-a\n")
	sd.bind(f.nsID, cycleA, policy.KindSignal, f.marketID, "", "")

	// Action: client level (web) and a broken spec at client level (app).
	f.webAction = sd.policyYAML(f.nsID, policy.KindAction, "name: web-action\nmode: shadow\n")
	sd.bind(f.nsID, f.webAction, policy.KindAction, f.shopID, "web", "")
	broken := sd.policyRaw(f.nsID, policy.KindAction, "broken-action", 1, []byte(`{"name":"broken-action","bogus":true}`))
	sd.bind(f.nsID, broken, policy.KindAction, f.shopID, "app", "")

	// Breaker: endpoint-group level.
	f.searchBreaker = sd.policyYAML(f.nsID, policy.KindBreaker, "name: search-breaker\nmin_requests: 10\n")
	sd.bind(f.nsID, f.searchBreaker, policy.KindBreaker, f.shopID, "", f.search)
	return f
}

func newTestStore(t *testing.T) (*Store, *seeder) {
	t.Helper()
	pool := testutil.Postgres(t)
	return NewStore(pool, nil, nil), newSeeder(t, pool)
}

func TestStoreLoadsNamespaceSnapshot(t *testing.T) {
	store, sd := newTestStore(t)
	f := seedFixture(t, sd)
	ctx := context.Background()
	require.False(t, store.Loaded())
	require.NoError(t, store.ReloadAll(ctx))
	require.True(t, store.Loaded())

	ns, ok := store.Namespace(f.nsID)
	require.True(t, ok)
	require.Equal(t, "acme", ns.TenantName)
	require.Equal(t, f.tenantID, ns.TenantID)
	require.Equal(t, "prod", ns.Name)
	require.Equal(t, "prod display", ns.DisplayName)
	require.Len(t, ns.Sites, 3)
	require.NotZero(t, ns.Version)

	byName, ok := store.NamespaceByName(f.tenantID, "prod")
	require.True(t, ok)
	require.Same(t, ns, byName)
	require.Len(t, store.Namespaces(""), 1)
	require.Len(t, store.Namespaces(f.tenantID), 1)
	require.Empty(t, store.Namespaces("ten_other"))

	shop, ns2, ok := store.Site(f.shopID)
	require.True(t, ok)
	require.Same(t, ns, ns2)
	byKey, _, ok := store.SiteByKey(f.shopKey)
	require.True(t, ok)
	require.Same(t, shop, byKey)
	require.Equal(t, []string{"web", "app"}, shop.Clients)
	require.Equal(t, "prod", shop.NamespaceName)
	require.Len(t, shop.GroupsByID, 4)
	require.Len(t, shop.GroupsByKey, 4)
	search, ok := shop.Group("web", "search")
	require.True(t, ok)
	require.Equal(t, f.searchKey, search.Key)
	require.Equal(t, shop.Key, search.SiteKey)
	require.Equal(t, 5, search.LowWatermark)

	t.Run("uri matching", func(t *testing.T) {
		tests := []struct {
			client, path, group, rule string
			kind                      site.RuleKind
			isDefault                 bool
		}{
			{"web", "/api/v1/search/item/", "search", f.searchPrefixRule, site.RulePrefix, false},
			{"web", "/api/v1/item/123", "detail", f.detailTemplateRule, site.RuleTemplate, false},
			{"web", "/detail/42", "detail", f.detailRegexRule, site.RuleRegex, false},
			{"web", "/unmatched", site.DefaultGroup, "", "", true},
			{"app", "/api/v1/search/item/", site.DefaultGroup, "", "", true},
		}
		for _, tc := range tests {
			g, res, ok := shop.MatchGroup(tc.client, tc.path)
			require.True(t, ok, tc.path)
			require.Equal(t, tc.group, g.Name, tc.path)
			require.Equal(t, tc.client, g.Client, tc.path)
			require.Equal(t, tc.rule, res.RuleID, tc.path)
			require.Equal(t, tc.kind, res.Kind, tc.path)
			require.Equal(t, tc.isDefault, res.Default, tc.path)
		}
		bare := ns.Sites["bare"]
		_, _, ok := bare.MatchGroup("web", "/x")
		require.False(t, ok, "a client without _default group cannot resolve")
		_, _, ok = shop.MatchGroup("ios", "/x")
		require.False(t, ok)
	})

	t.Run("identity types", func(t *testing.T) {
		require.Len(t, shop.IdentityTypes, 2)
		web := shop.IdentityTypes["web_cookie"]
		require.NotNil(t, web)
		require.Equal(t, f.webTypeID, web.ID)
		require.Equal(t, 3, web.Version)
		require.Equal(t, f.shopID, web.SiteID)
		require.Same(t, web, shop.IdentityTypesByID[f.webTypeID])
		require.Equal(t, "app", shop.IdentityTypesByID[f.appTypeID].Client)
	})

	t.Run("policies", func(t *testing.T) {
		builtin := func(kind policy.Kind) PolicyRef {
			return PolicyRef{Name: policy.DefaultPolicyName(kind), Level: policy.LevelBuiltin}
		}
		require.Equal(t, PolicyRef{PolicyID: f.nsRotation, Name: "ns-rotation", Version: 2, Level: policy.LevelNamespace}, search.RotationRef)
		require.Equal(t, policy.StrategyBestHealth, search.Rotation.Rotation.Strategy)
		require.Equal(t, PolicyRef{PolicyID: f.leafSignal, Name: "leaf-signal", Version: 1, Level: policy.LevelSite}, search.SignalRef)
		require.Equal(t, policy.OutcomeCaptcha, search.Signal.Classify(policy.ReportFacts{HTTPStatus: 418}).Outcome)
		require.Equal(t, policy.OutcomeBanned, search.Signal.Classify(policy.ReportFacts{HTTPStatus: 500, Markers: []string{"blocked"}}).Outcome)
		require.Equal(t, policy.OutcomeSuccess, search.Signal.Classify(policy.ReportFacts{HTTPStatus: 200}).Outcome)
		require.Equal(t, PolicyRef{PolicyID: f.webAction, Name: "web-action", Version: 1, Level: policy.LevelClient}, search.ActionRef)
		require.True(t, search.Action.Shadow())
		require.Equal(t, PolicyRef{PolicyID: f.searchBreaker, Name: "search-breaker", Version: 1, Level: policy.LevelEndpointGroup}, search.BreakerRef)
		require.Equal(t, 10, search.Breaker.MinRequests)
		require.Equal(t, []string{f.webTypeID}, search.IdentityTypeIDs)

		detail, _ := shop.Group("web", "detail")
		require.Equal(t, PolicyRef{PolicyID: f.detailRotation, Name: "detail-rotation", Version: 1, Level: policy.LevelEndpointGroup}, detail.RotationRef)
		require.Equal(t, []string{f.webTypeID}, detail.IdentityTypeIDs, "unknown and other-client types are ignored")
		require.Equal(t, builtin(policy.KindBreaker), detail.BreakerRef)
		require.Equal(t, policy.DefaultBreakerMinRequests, detail.Breaker.MinRequests)

		app, _ := shop.Group("app", site.DefaultGroup)
		require.Equal(t, builtin(policy.KindAction), app.ActionRef, "broken spec falls back")
		require.NotNil(t, app.Action)
		require.False(t, app.Action.Shadow())
		require.Equal(t, policy.LevelSite, app.SignalRef.Level)
		require.Equal(t, []string{f.appTypeID}, app.IdentityTypeIDs)

		market := ns.Sites["market"]
		tg, _ := market.Group("web", site.DefaultGroup)
		require.Equal(t, builtin(policy.KindSignal), tg.SignalRef, "extends cycle falls back")
		require.Equal(t, policy.OutcomeRateLimited, tg.Signal.Classify(policy.ReportFacts{HTTPStatus: 429}).Outcome)
		require.Equal(t, policy.LevelNamespace, tg.RotationRef.Level)
		require.Equal(t, builtin(policy.KindAction), tg.ActionRef)
		require.Empty(t, tg.IdentityTypeIDs)
		require.NotNil(t, tg.IdentityTypeIDs)
	})
}

func TestStoreReloadSkipsUnchangedAndTracksChanges(t *testing.T) {
	store, sd := newTestStore(t)
	ctx := context.Background()
	tenantID := sd.tenant("acme")
	nsID := sd.namespace(tenantID, "prod")
	siteID, _ := sd.site(nsID, "shop", "web")
	sd.group(siteID, "web", site.DefaultGroup)

	var changes []string
	unregister := store.OnChange(func(id string) { changes = append(changes, id) })
	require.NoError(t, store.Reload(ctx, nsID))
	first, ok := store.Namespace(nsID)
	require.True(t, ok)
	require.Equal(t, []string{nsID}, changes)

	// Unchanged rows keep the same snapshot and do not notify.
	require.NoError(t, store.Reload(ctx, nsID))
	same, _ := store.Namespace(nsID)
	require.Same(t, first, same)
	require.Len(t, changes, 1)

	// A change produces a new snapshot with a higher version.
	sd.group(siteID, "web", "search")
	require.NoError(t, store.Invalidate(ctx, nsID))
	second, _ := store.Namespace(nsID)
	require.NotSame(t, first, second)
	require.Greater(t, second.Version, first.Version)
	require.Len(t, second.Sites["shop"].GroupsByID, 2)
	require.Len(t, first.Sites["shop"].GroupsByID, 1, "old snapshots are immutable")
	require.Len(t, changes, 2)

	// Deleting the namespace removes it and notifies once.
	sd.deleteNamespace(nsID)
	require.NoError(t, store.Reload(ctx, nsID))
	_, ok = store.Namespace(nsID)
	require.False(t, ok)
	_, _, ok = store.Site(siteID)
	require.False(t, ok)
	require.Len(t, changes, 3)
	require.NoError(t, store.Reload(ctx, nsID))
	require.Len(t, changes, 3)

	unregister()
	unregister()
	require.Error(t, store.Reload(ctx, ""))
	store.OnChange(nil)() // a nil listener yields a no-op unregister
}

func TestStoreReloadAllRemovesDeletedNamespaces(t *testing.T) {
	store, sd := newTestStore(t)
	ctx := context.Background()
	tenantA := sd.tenant("a")
	tenantB := sd.tenant("b")
	ns1 := sd.namespace(tenantA, "one")
	ns2 := sd.namespace(tenantA, "two")
	ns3 := sd.namespace(tenantB, "one")
	require.NoError(t, store.ReloadAll(ctx))
	require.Len(t, store.Namespaces(""), 3)
	require.Len(t, store.Namespaces(tenantA), 2)
	all := store.Namespaces("")
	require.True(t, all[0].ID < all[1].ID && all[1].ID < all[2].ID)
	n, ok := store.NamespaceByName(tenantB, "one")
	require.True(t, ok)
	require.Equal(t, ns3, n.ID)

	sd.deleteNamespace(ns2)
	require.NoError(t, store.ReloadAll(ctx))
	_, ok = store.Namespace(ns2)
	require.False(t, ok)
	_, ok = store.Namespace(ns1)
	require.True(t, ok)
}

func TestStoreReloadErrors(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	nsID := sd.namespace(sd.tenant("acme"), "prod")

	// A store whose pool is closed fails every load but keeps its snapshots.
	store := NewStore(pool, nil, nil)
	require.NoError(t, store.ReloadAll(context.Background()))

	closed, err := newClosedPool(t, pool)
	require.NoError(t, err)
	broken := NewStore(closed, nil, nil)
	broken.idx.Store(store.index())
	require.Error(t, broken.ReloadAll(context.Background()))
	require.Error(t, broken.Reload(context.Background(), nsID))
	require.Error(t, broken.Invalidate(context.Background(), nsID))
	_, ok := broken.Namespace(nsID)
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, store.Reload(ctx, nsID), context.Canceled)
	require.Error(t, store.ReloadAll(ctx))

	// Loads that exceed the timeout fail.
	slow := NewStore(pool, nil, nil)
	slow.loadTimeout = time.Nanosecond
	require.Error(t, slow.Reload(context.Background(), nsID))
}
