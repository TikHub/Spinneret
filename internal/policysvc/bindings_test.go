package policysvc_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvctest"
)

func resolvedNames(rs []policysvc.ResolvedPolicy) map[policy.Kind]string {
	out := map[policy.Kind]string{}
	for _, r := range rs {
		out[r.Kind] = r.Name + "@" + string(r.Level)
	}
	return out
}

func TestBindingResolutionLevels(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	ns := publishNew(t, f, policy.KindRotation, "name: ns-rotation\n")
	st := publishNew(t, f, policy.KindRotation, "name: site-rotation\n")
	cl := publishNew(t, f, policy.KindRotation, "name: client-rotation\n")
	eg := publishNew(t, f, policy.KindRotation, "name: eg-rotation\n")
	_ = f.Hot.Take()
	_ = f.Audit.Take()

	// Namespace level replaces the default binding and resyncs every site.
	b, err := svc.SetBinding(ctx, f.Admin, ns.ID, policysvc.Target{})
	require.NoError(t, err)
	require.Equal(t, policy.LevelNamespace, b.Level)
	require.Equal(t, "ns-rotation", b.PolicyName)
	require.ElementsMatch(t, []string{f.Shop.ID, f.Forum.ID}, f.Hot.Take())
	require.Equal(t, []string{"policy.bind:ok"}, auditActions(f.Audit.Take()))
	require.Equal(t, 1, f.Count(t, `SELECT count(*) FROM policy_bindings WHERE namespace_id = $1 AND kind = 'rotation'`, f.Namespace.ID))

	// Rebinding the same policy at the same target changes nothing to sync.
	b2, err := svc.SetBinding(ctx, f.Admin, ns.ID, policysvc.Target{})
	require.NoError(t, err)
	require.Equal(t, b.ID, b2.ID, "replacing keeps the binding id")
	require.Empty(t, f.Hot.Take())

	bs, err := svc.SetBinding(ctx, f.Admin, st.ID, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	require.Equal(t, policy.LevelSite, bs.Level)
	require.Equal(t, []string{f.Shop.ID}, f.Hot.Take())
	bc, err := svc.SetBinding(ctx, f.Admin, cl.ID, policysvc.Target{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.Equal(t, policy.LevelClient, bc.Level)
	be, err := svc.SetBinding(ctx, f.Admin, eg.ID, policysvc.Target{Site: "shop", Client: "web", EndpointGroup: "search"})
	require.NoError(t, err)
	require.Equal(t, policy.LevelEndpointGroup, be.Level)
	require.Equal(t, f.Search.ID, be.EndpointGroupID)
	require.Equal(t, "search", be.EndpointGroupName)

	// A site-restricted admin can bind policies that apply to its site on that site.
	unbound := publishNew(t, f, policy.KindRotation, "name: unbound-rotation\n")
	_, err = svc.SetBinding(ctx, f.ShopAdmin, unbound.ID, policysvc.Target{Site: "shop", Client: "app"})
	requireReason(t, err, apperr.ReasonPermissionDenied) // not visible to the site admin while unbound
	bApp, err := svc.SetBinding(ctx, f.ShopAdmin, st.ID, policysvc.Target{Site: "shop", Client: "app"})
	require.NoError(t, err)
	require.Equal(t, policy.LevelClient, bApp.Level)

	levels := []struct {
		target policysvc.Target
		want   string
	}{
		{policysvc.Target{}, "ns-rotation@namespace"},
		{policysvc.Target{Site: "forum"}, "ns-rotation@namespace"},
		{policysvc.Target{Site: "shop"}, "site-rotation@site"},
		{policysvc.Target{Site: "shop", Client: "app"}, "site-rotation@client"},
		{policysvc.Target{Site: "shop", Client: "web"}, "client-rotation@client"},
	}
	for _, lv := range levels {
		got, err := svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, lv.target)
		require.NoError(t, err)
		require.Equal(t, lv.want, resolvedNames(got)[policy.KindRotation], "target %+v", lv.target)
		require.Equal(t, "default-signal@namespace", resolvedNames(got)[policy.KindSignal])
	}

	// Endpoint group resolution reads the catalog snapshot references.
	f.Search.RotationRef = catalog.PolicyRef{PolicyID: eg.ID, Name: eg.Name, Version: 1, Level: policy.LevelEndpointGroup}
	got, err := svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop", Client: "web", EndpointGroup: "search"})
	require.NoError(t, err)
	require.Equal(t, "eg-rotation@endpoint_group", resolvedNames(got)[policy.KindRotation])
	require.Equal(t, 1, got[0].Version)
	require.Contains(t, got[0].YAML, "name: eg-rotation")
	require.Equal(t, "default-breaker@builtin", resolvedNames(got)[policy.KindBreaker])
	require.Equal(t, policy.DefaultYAML(policy.KindBreaker), got[3].YAML)
	f.Search.RotationRef = catalog.PolicyRef{PolicyID: eg.ID, Name: eg.Name, Version: 7, Level: policy.LevelEndpointGroup}
	_, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop", Client: "web", EndpointGroup: "search"})
	requireReason(t, err, apperr.ReasonConflict)

	// Listing bindings: ordered by kind then level; site filter keeps namespace-level rows.
	all, err := svc.ListBindings(ctx, f.Viewer, f.Namespace, policy.KindRotation, "")
	require.NoError(t, err)
	var order []string
	for _, x := range all {
		order = append(order, x.PolicyName+"@"+string(x.Level))
	}
	require.Equal(t, []string{
		"ns-rotation@namespace", "site-rotation@site", "site-rotation@client", "client-rotation@client", "eg-rotation@endpoint_group",
	}, order)
	forumBindings, err := svc.ListBindings(ctx, f.Viewer, f.Namespace, "", "forum")
	require.NoError(t, err)
	require.Len(t, forumBindings, 4, "only namespace-level bindings apply to forum")
	_, err = svc.ListBindings(ctx, f.Viewer, f.Namespace, "", "nope")
	requireReason(t, err, apperr.ReasonSiteUnknown)
	_, err = svc.ListBindings(ctx, f.Viewer, f.Namespace, "bogus", "")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.ListBindings(ctx, f.ShopAdmin, f.Namespace, "", "forum")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.ListBindings(ctx, f.LeaseToken, f.Namespace, "", "")
	requireReason(t, err, apperr.ReasonScopeMissing)

	// Target validation.
	targetErrs := []struct {
		target policysvc.Target
		reason apperr.Reason
	}{
		{policysvc.Target{Client: "web"}, apperr.ReasonInvalidArgument},
		{policysvc.Target{Site: "shop", EndpointGroup: "search"}, apperr.ReasonInvalidArgument},
		{policysvc.Target{Site: "nope"}, apperr.ReasonSiteUnknown},
		{policysvc.Target{Site: "shop", Client: "tv"}, apperr.ReasonClientUnknown},
		{policysvc.Target{Site: "shop", Client: "app", EndpointGroup: "search"}, apperr.ReasonEndpointGroupUnknown},
	}
	for _, te := range targetErrs {
		_, err := svc.SetBinding(ctx, f.Admin, st.ID, te.target)
		requireReason(t, err, te.reason)
		_, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, te.target)
		requireReason(t, err, te.reason)
	}

	// Only published policies can be bound; permissions apply to the target.
	draft := draftNew(t, f, policy.KindRotation, "name: draft-rotation\n")
	_, err = svc.SetBinding(ctx, f.Admin, draft.ID, policysvc.Target{Site: "shop"})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	_, err = svc.SetBinding(ctx, f.Operator, st.ID, policysvc.Target{Site: "forum"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.SetBinding(ctx, f.ShopAdmin, st.ID, policysvc.Target{Site: "forum"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.SetBinding(ctx, f.ShopAdmin, st.ID, policysvc.Target{})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.SetBinding(ctx, f.Admin, "pol_missing", policysvc.Target{})
	requireCode(t, err, connect.CodeNotFound)

	// Delete bindings: the site falls back to the namespace level.
	_ = f.Hot.Take()
	err = svc.DeleteBinding(ctx, f.ShopAdmin, b.ID)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	err = svc.DeleteBinding(ctx, f.Operator, bs.ID)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.NoError(t, svc.DeleteBinding(ctx, f.ShopAdmin, bs.ID))
	require.Equal(t, []string{f.Shop.ID}, f.Hot.Take())
	got, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	require.Equal(t, "ns-rotation@namespace", resolvedNames(got)[policy.KindRotation])
	err = svc.DeleteBinding(ctx, f.Admin, bs.ID)
	requireCode(t, err, connect.CodeNotFound)
	err = svc.DeleteBinding(ctx, f.Admin, "")
	requireCode(t, err, connect.CodeInvalidArgument)

	// Deleting a bound policy cascades its bindings and resyncs the bound sites.
	require.NoError(t, svc.DeletePolicy(ctx, f.Admin, cl.ID))
	require.Equal(t, []string{f.Shop.ID}, f.Hot.Take())
	require.Zero(t, f.Count(t, `SELECT count(*) FROM policy_bindings WHERE policy_id = $1`, cl.ID))

	// Without a namespace binding the built-in default applies.
	require.NoError(t, svc.DeleteBinding(ctx, f.Admin, b.ID))
	require.ElementsMatch(t, []string{f.Shop.ID, f.Forum.ID}, f.Hot.Take())
	got, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "forum"})
	require.NoError(t, err)
	require.Equal(t, "default-rotation@builtin", resolvedNames(got)[policy.KindRotation])
	require.Equal(t, policy.DefaultYAML(policy.KindRotation), got[0].YAML)

	// Hot sync failures are logged, not returned.
	f.Hot.SetError(errors.New("redis down"))
	_, err = svc.SetBinding(ctx, f.Admin, ns.ID, policysvc.Target{Site: "forum"})
	require.NoError(t, err)
}

func TestDeleteDefaultPolicy(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	page, err := svc.ListPolicies(ctx, f.Admin, f.Namespace, policysvc.ListPoliciesInput{Kind: policy.KindBreaker})
	require.NoError(t, err)
	require.Len(t, page.Policies, 1)
	def := page.Policies[0]
	require.Equal(t, "default-breaker", def.Name)
	require.Len(t, def.Bindings, 1)

	err = svc.DeletePolicy(ctx, f.Admin, def.ID)
	requireReason(t, err, apperr.ReasonFailedPrecondition)

	// Once another policy is bound at namespace level the default can go.
	other := publishNew(t, f, policy.KindBreaker, "name: other-breaker\n")
	_, err = svc.SetBinding(ctx, f.Admin, other.ID, policysvc.Target{})
	require.NoError(t, err)
	require.NoError(t, svc.DeletePolicy(ctx, f.Admin, def.ID))
}
