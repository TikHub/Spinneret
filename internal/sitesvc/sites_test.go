package sitesvc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/site"
)

func TestCreateSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	created, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop", DisplayName: "Shop (example site)", Description: "d"})
	require.NoError(t, err)
	require.True(t, idgen.Valid(created.ID, idgen.Site))
	require.Equal(t, []string{DefaultClient}, created.Clients)
	require.Equal(t, 1, created.EndpointGroupCount)
	require.Equal(t, 0, created.IdentityCount)
	require.Equal(t, "prod", created.NamespaceName)
	require.Equal(t, e.tenantID, created.TenantID)
	require.NotZero(t, created.Key)

	// Catalog and hot state were updated.
	cs, _, ok := e.cat.Site(created.ID)
	require.True(t, ok)
	_, ok = cs.Group("web", site.DefaultGroup)
	require.True(t, ok)
	require.Equal(t, []string{created.ID}, e.hot.syncedSites())
	entry := e.audit.last()
	require.Equal(t, ActionSiteCreate, entry.Action)
	require.Equal(t, audit.ResultOK, entry.Result)
	require.Equal(t, created.ID, entry.ResourceID)
	require.Equal(t, e.ns.ID, entry.NamespaceID)

	multi, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "market", Clients: []string{"web", "app"}})
	require.NoError(t, err)
	require.Equal(t, 2, multi.EndpointGroupCount)

	_, err = e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
	requireCode(t, err, apperr.ReasonAlreadyExists)
	_, err = e.svc.CreateSite(ctx, e.actor, nil, CreateSiteInput{Name: "nons"})
	requireCode(t, err, apperr.ReasonInvalidArgument)

	tooMany := make([]string, MaxClients+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("c%d", i)
	}
	invalid := []struct {
		name string
		in   CreateSiteInput
	}{
		{"uppercase name", CreateSiteInput{Name: "Shop"}},
		{"empty name", CreateSiteInput{Name: ""}},
		{"long name", CreateSiteInput{Name: strings.Repeat("a", 65)}},
		{"bad client", CreateSiteInput{Name: "a", Clients: []string{"Web"}}},
		{"duplicate client", CreateSiteInput{Name: "a", Clients: []string{"web", "web"}}},
		{"too many clients", CreateSiteInput{Name: "a", Clients: tooMany}},
		{"long display name", CreateSiteInput{Name: "a", DisplayName: strings.Repeat("x", MaxDisplayNameLength+1)}},
		{"long description", CreateSiteInput{Name: "a", Description: strings.Repeat("x", MaxDescriptionLength+1)}},
		{"invalid utf8", CreateSiteInput{Name: "a", Description: "\xff"}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.CreateSite(ctx, e.actor, e.ns, tc.in)
			requireCode(t, err, apperr.ReasonInvalidArgument)
		})
	}
}

func TestGetAndListSites(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	names := []string{"delta", "alpha", "charlie", "bravo", "echo"}
	ids := map[string]string{}
	for _, n := range names {
		ids[n] = e.createSite(n).ID
	}
	e.addIdentity(ids["alpha"], "web", "")
	e.exec(`UPDATE identities SET state = 'retired' WHERE site_id = $1`, ids["alpha"])
	e.addIdentity(ids["alpha"], "web", "")

	got, err := e.svc.GetSite(ctx, ids["alpha"])
	require.NoError(t, err)
	require.Equal(t, 1, got.IdentityCount, "retired identities are not counted")
	_, err = e.svc.GetSite(ctx, "sit_missing")
	requireCode(t, err, apperr.ReasonNotFound)

	ref, err := e.svc.SiteRefByName(ctx, e.ns.ID, "bravo")
	require.NoError(t, err)
	require.Equal(t, ids["bravo"], ref.ID)
	_, err = e.svc.SiteRefByName(ctx, e.ns.ID, "missing")
	requireCode(t, err, apperr.ReasonNotFound)
	_, err = e.svc.SiteRef(ctx, "sit_missing")
	requireCode(t, err, apperr.ReasonNotFound)

	// Paging through all sites.
	var seen []string
	after := ""
	for {
		page, err := e.svc.ListSites(ctx, e.ns.ID, SiteAccess{All: true}, 2, after)
		require.NoError(t, err)
		require.Equal(t, 5, page.Total)
		for _, s := range page.Sites {
			seen = append(seen, s.Name)
		}
		if page.NextAfterName == "" {
			break
		}
		after = page.NextAfterName
	}
	require.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, seen)

	restricted, err := e.svc.ListSites(ctx, e.ns.ID, SiteAccess{SiteIDs: []string{ids["echo"], ids["bravo"], "sit_other"}}, 0, "")
	require.NoError(t, err)
	require.Equal(t, 2, restricted.Total)
	require.Len(t, restricted.Sites, 2)
	require.Equal(t, "bravo", restricted.Sites[0].Name)
	require.Empty(t, restricted.NextAfterName)

	none, err := e.svc.ListSites(ctx, e.ns.ID, SiteAccess{}, 10, "")
	require.NoError(t, err)
	require.Empty(t, none.Sites)
	require.Zero(t, none.Total)
}

func TestUpdateSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop", "web", "app")
	display, desc := "Shop (example site)", "an example shop site"

	updated, err := e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{DisplayName: &display, Description: &desc})
	require.NoError(t, err)
	require.Equal(t, display, updated.DisplayName)
	require.Equal(t, desc, updated.Description)
	require.Equal(t, []string{"web", "app"}, updated.Clients)

	// Add ios and remove app (which has a group, a rule and a client binding).
	appGroup, err := e.svc.CreateEndpointGroup(ctx, e.actor, s.ID, CreateGroupInput{Client: "app", Name: "feed",
		Rules: []URIRule{{Kind: site.RulePrefix, Pattern: "/feed/"}}})
	require.NoError(t, err)
	policyID := idgen.New(idgen.Policy)
	e.exec(`INSERT INTO policies (id, namespace_id, kind, name) VALUES ($1, $2, 'rotation', 'r')`, policyID, e.ns.ID)
	e.exec(`INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id, client) VALUES ($1, $2, 'rotation', $3, $4, 'app')`,
		idgen.New(idgen.PolicyBinding), policyID, e.ns.ID, s.ID)
	e.exec(`INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id, client) VALUES ($1, $2, 'rotation', $3, $4, 'web')`,
		idgen.New(idgen.PolicyBinding), policyID, e.ns.ID, s.ID)

	updated, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{Clients: []string{"web", "ios"}})
	require.NoError(t, err)
	require.Equal(t, []string{"web", "ios"}, updated.Clients)
	require.Equal(t, display, updated.DisplayName, "unset fields keep their value")
	require.Equal(t, 2, updated.EndpointGroupCount)
	require.Zero(t, e.count(`SELECT count(*) FROM endpoint_groups WHERE id = $1`, appGroup.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM uri_rules WHERE endpoint_group_id = $1`, appGroup.ID))
	require.Equal(t, 1, e.count(`SELECT count(*) FROM policy_bindings WHERE site_id = $1`, s.ID))
	cs, _, ok := e.cat.Site(s.ID)
	require.True(t, ok)
	_, ok = cs.Group("ios", site.DefaultGroup)
	require.True(t, ok)
	require.False(t, cs.HasClient("app"))
	entry := e.audit.last()
	require.Equal(t, ActionSiteUpdate, entry.Action)
	require.Equal(t, []string{"ios"}, entry.Details["added_clients"])
	require.Equal(t, []string{"app"}, entry.Details["removed_clients"])

	// A client with identity types cannot be removed.
	e.addIdentity(s.ID, "ios", "")
	_, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{Clients: []string{"web"}})
	requireCode(t, err, apperr.ReasonFailedPrecondition)
	// ... not even when only the type remains.
	e.exec(`DELETE FROM identities WHERE site_id = $1`, s.ID)
	_, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{Clients: []string{"web"}})
	requireCode(t, err, apperr.ReasonFailedPrecondition)

	_, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{Clients: []string{"Bad"}})
	requireCode(t, err, apperr.ReasonInvalidArgument)
	long := strings.Repeat("x", MaxDescriptionLength+1)
	_, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{Description: &long})
	requireCode(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.UpdateSite(ctx, e.actor, s.ID, UpdateSiteInput{DisplayName: &long})
	requireCode(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.UpdateSite(ctx, e.actor, "sit_missing", UpdateSiteInput{DisplayName: &display})
	requireCode(t, err, apperr.ReasonNotFound)
}

func TestDeleteSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := e.createSite("shop")
	typeID := e.addIdentity(s.ID, "web", "")
	e.addIdentity(s.ID, "web", typeID)
	e.exec(`INSERT INTO accounts (id, site_id, external_ref) VALUES ($1, $2, 'acc-1')`, idgen.New(idgen.Account), s.ID)

	err := e.svc.DeleteSite(ctx, e.actor, s.ID, false)
	requireCode(t, err, apperr.ReasonFailedPrecondition)
	require.Equal(t, 1, e.count(`SELECT count(*) FROM sites WHERE id = $1`, s.ID))

	require.NoError(t, e.svc.DeleteSite(ctx, e.actor, s.ID, true))
	require.Zero(t, e.count(`SELECT count(*) FROM sites WHERE id = $1`, s.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM identities WHERE site_id = $1`, s.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM identity_types WHERE site_id = $1`, s.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM accounts WHERE site_id = $1`, s.ID))
	require.Zero(t, e.count(`SELECT count(*) FROM endpoint_groups WHERE site_id = $1`, s.ID))
	_, _, ok := e.cat.Site(s.ID)
	require.False(t, ok)
	require.Equal(t, []int64{s.Key}, e.hot.removedKeys())
	entry := e.audit.last()
	require.Equal(t, ActionSiteDelete, entry.Action)
	require.Equal(t, int32(2), entry.Details["deleted_identities"])

	// Sites without identities are deleted without force.
	empty := e.createSite("empty")
	require.NoError(t, e.svc.DeleteSite(ctx, e.actor, empty.ID, false))
	requireCode(t, e.svc.DeleteSite(ctx, e.actor, empty.ID, false), apperr.ReasonNotFound)
}

func TestMutationsReportPropagationFailures(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.hot.err = fmt.Errorf("redis down")
	s, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
	requireCode(t, err, apperr.ReasonInternal)
	require.Equal(t, "shop", s.Name, "the committed site is returned with the error")
	require.Equal(t, 1, e.count(`SELECT count(*) FROM sites WHERE id = $1`, s.ID), "the change is not rolled back")
	_, _, ok := e.cat.Site(s.ID)
	require.True(t, ok, "the catalog is invalidated before the hot-state sync")

	requireCode(t, e.svc.DeleteSite(ctx, e.actor, s.ID, false), apperr.ReasonInternal)

	e.hot.err = nil
	failing := NewService(e.pool, failingCatalog{Catalog: e.cat}, nil, nil, nil, e.keys, nil)
	_, err = failing.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "market"})
	requireCode(t, err, apperr.ReasonInternal)
}
