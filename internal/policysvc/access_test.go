package policysvc_test

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvctest"
)

func TestInstallNamespaceDefaultsIdempotent(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()

	counts := func() [3]int {
		return [3]int{
			f.Count(t, `SELECT count(*) FROM policies WHERE namespace_id = $1`, f.Namespace.ID),
			f.Count(t, `SELECT count(*) FROM policy_versions v JOIN policies p ON p.id = v.policy_id WHERE p.namespace_id = $1`, f.Namespace.ID),
			f.Count(t, `SELECT count(*) FROM policy_bindings WHERE namespace_id = $1 AND site_id IS NULL`, f.Namespace.ID),
		}
	}
	require.Equal(t, [3]int{4, 4, 4}, counts())

	// Installing again changes nothing, even concurrently.
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := f.Pool.Begin(ctx)
			if err != nil {
				errs <- err
				return
			}
			if err := f.Service.InstallNamespaceDefaults(ctx, tx, f.Namespace.ID, ""); err != nil {
				_ = tx.Rollback(ctx)
				errs <- err
				return
			}
			errs <- tx.Commit(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, [3]int{4, 4, 4}, counts())

	page, err := f.Service.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{})
	require.NoError(t, err)
	require.Equal(t, 4, page.Total)
	for _, p := range page.Policies {
		require.Equal(t, policy.DefaultPolicyName(p.Kind), p.Name)
		require.Equal(t, 1, p.CurrentVersion)
		require.False(t, p.HasDraft)
		require.Equal(t, "system", p.CreatedBy)
		require.Len(t, p.Bindings, 1)
		require.Equal(t, policy.LevelNamespace, p.Bindings[0].Level)
		got, err := f.Service.GetPolicy(ctx, f.Viewer, p.ID)
		require.NoError(t, err)
		require.Equal(t, policy.DefaultYAML(p.Kind), got.PublishedYAML)
	}

	// A never-published policy with a default name is completed on install.
	_, err = f.Pool.Exec(ctx, `DELETE FROM policies WHERE namespace_id = $1 AND kind = 'breaker'`, f.Namespace.ID)
	require.NoError(t, err)
	draft, err := f.Service.CreatePolicy(ctx, f.Operator, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: default-breaker\n"})
	require.NoError(t, err)
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, f.Service.InstallNamespaceDefaults(ctx, tx, f.Namespace.ID, "user:usr_x"))
	require.NoError(t, tx.Commit(ctx))
	got, err := f.Service.GetPolicy(ctx, f.Viewer, draft.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.CurrentVersion)
	require.True(t, got.HasDraft)
	require.Len(t, got.Bindings, 1)

	// Argument validation.
	require.Error(t, f.Service.InstallNamespaceDefaults(ctx, nil, f.Namespace.ID, ""))
	tx, err = f.Pool.Begin(ctx)
	require.NoError(t, err)
	require.Error(t, f.Service.InstallNamespaceDefaults(ctx, tx, "", ""))
	require.NoError(t, tx.Rollback(ctx))
	// A namespace that does not exist violates the foreign key.
	tx, err = f.Pool.Begin(ctx)
	require.NoError(t, err)
	require.Error(t, f.Service.InstallNamespaceDefaults(ctx, tx, "ns_missing", ""))
	require.NoError(t, tx.Rollback(ctx))
}

func TestListPoliciesVisibilityAndPagination(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	shop := publishNew(t, f, policy.KindSignal, "name: shop-signals\nbind: {site: shop, client: web}\n")
	forum := publishNew(t, f, policy.KindSignal, "name: forum-signals\nbind: {site: forum}\n")
	unbound := draftNew(t, f, policy.KindSignal, "name: unbound-signals\n")

	// Namespace-wide readers see everything, keyset-paginated by kind and name.
	var names []string
	afterKind, afterName := "", ""
	for {
		page, err := svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{PageSize: 2, AfterKind: afterKind, AfterName: afterName})
		require.NoError(t, err)
		require.Equal(t, 7, page.Total)
		for _, p := range page.Policies {
			names = append(names, string(p.Kind)+"/"+p.Name)
			require.Empty(t, p.PublishedYAML, "list omits YAML bodies")
		}
		if !page.More {
			break
		}
		last := page.Policies[len(page.Policies)-1]
		afterKind, afterName = string(last.Kind), last.Name
	}
	require.Equal(t, []string{
		"action/default-action", "breaker/default-breaker", "rotation/default-rotation",
		"signal/default-signal", "signal/forum-signals", "signal/shop-signals", "signal/unbound-signals",
	}, names)

	page, err := svc.ListPolicies(ctx, f.AdminToken, f.Namespace, policysvc.ListPoliciesInput{Kind: policy.KindSignal, Search: "SIGNALS"})
	require.NoError(t, err)
	require.Equal(t, 3, page.Total, "search is case-insensitive and default-signal does not match")
	page, err = svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{Search: "orum"})
	require.NoError(t, err)
	require.Len(t, page.Policies, 1)
	require.Equal(t, forum.ID, page.Policies[0].ID)

	// Site-restricted principals see namespace-bound policies and those of their sites.
	page, err = svc.ListPolicies(ctx, f.ShopAdmin, f.Namespace, policysvc.ListPoliciesInput{Kind: policy.KindSignal})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Equal(t, "default-signal", page.Policies[0].Name)
	require.Equal(t, shop.ID, page.Policies[1].ID)
	require.Len(t, page.Policies[1].Bindings, 1)

	_, err = svc.GetPolicy(ctx, f.ShopAdmin, shop.ID)
	require.NoError(t, err)
	for _, id := range []string{forum.ID, unbound.ID} {
		_, err = svc.GetPolicy(ctx, f.ShopAdmin, id)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = svc.ListPolicyVersions(ctx, f.ShopAdmin, id, 0, 0)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = svc.DiffPolicyVersions(ctx, f.ShopAdmin, id, 0, 0)
		requireReason(t, err, apperr.ReasonPermissionDenied)
	}
	// Site admins cannot mutate namespace-level policy objects.
	_, err = svc.SaveDraft(ctx, f.ShopAdmin, shop.ID, "name: shop-signals\n")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.PublishPolicy(ctx, f.ShopAdmin, policysvc.PublishInput{ID: shop.ID})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.CreatePolicy(ctx, f.ShopAdmin, f.Namespace, policysvc.CreateInput{Kind: policy.KindSignal, YAML: "name: x\n"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	// But they can resolve their site and see its bindings.
	_, err = svc.ResolvePolicies(ctx, f.ShopAdmin, f.Namespace, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	_, err = svc.ResolvePolicies(ctx, f.ShopAdmin, f.Namespace, policysvc.Target{})
	require.NoError(t, err)
	_, err = svc.ResolvePolicies(ctx, f.ShopAdmin, f.Namespace, policysvc.Target{Site: "forum"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	bs, err := svc.ListBindings(ctx, f.ShopAdmin, f.Namespace, "", "")
	require.NoError(t, err)
	require.Len(t, bs, 5, "4 namespace defaults + shop web signal binding")

	// Principals without policy:read in the namespace.
	for _, p := range []struct {
		name   string
		reason apperr.Reason
		fn     func() error
	}{
		{"lease token list", apperr.ReasonScopeMissing, func() error {
			_, err := svc.ListPolicies(ctx, f.LeaseToken, f.Namespace, policysvc.ListPoliciesInput{})
			return err
		}},
		{"other tenant list", apperr.ReasonPermissionDenied, func() error {
			_, err := svc.ListPolicies(ctx, f.OtherTenant, f.Namespace, policysvc.ListPoliciesInput{})
			return err
		}},
		{"other tenant get", apperr.ReasonPermissionDenied, func() error {
			_, err := svc.GetPolicy(ctx, f.OtherTenant, shop.ID)
			return err
		}},
		{"other tenant resolve", apperr.ReasonPermissionDenied, func() error {
			_, err := svc.ResolvePolicies(ctx, f.OtherTenant, f.Namespace, policysvc.Target{})
			return err
		}},
		{"nil get", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.GetPolicy(ctx, nil, shop.ID)
			return err
		}},
		{"nil list", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.ListPolicies(ctx, nil, f.Namespace, policysvc.ListPoliciesInput{})
			return err
		}},
		{"nil bindings", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.ListBindings(ctx, nil, f.Namespace, "", "")
			return err
		}},
		{"nil set binding", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.SetBinding(ctx, nil, shop.ID, policysvc.Target{})
			return err
		}},
		{"nil delete binding", apperr.ReasonSessionInvalid, func() error { return svc.DeleteBinding(ctx, nil, "pbd_x") }},
		{"nil delete", apperr.ReasonSessionInvalid, func() error { return svc.DeletePolicy(ctx, nil, shop.ID) }},
		{"nil publish", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.PublishPolicy(ctx, nil, policysvc.PublishInput{ID: shop.ID})
			return err
		}},
		{"nil rollback", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.RollbackPolicy(ctx, nil, shop.ID, 1, "")
			return err
		}},
		{"nil save", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.SaveDraft(ctx, nil, shop.ID, "name: x\n")
			return err
		}},
		{"nil resolve", apperr.ReasonSessionInvalid, func() error {
			_, err := svc.ResolvePolicies(ctx, nil, f.Namespace, policysvc.Target{})
			return err
		}},
	} {
		t.Run(p.name, func(t *testing.T) {
			requireReason(t, p.fn(), p.reason)
		})
	}

	// Input validation.
	_, err = svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{Kind: "bogus"})
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{Search: string(make([]byte, 300))})
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{AfterKind: "signal"})
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: shop.ID, ExpectedVersion: -1})
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: shop.ID, Comment: string(make([]byte, 2000))})
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: "pol_missing"})
	requireCode(t, err, connect.CodeNotFound)

	// A policy whose namespace is not in the catalog is not found.
	f.Catalog.Remove(f.Namespace.ID)
	_, err = svc.GetPolicy(ctx, f.Admin, shop.ID)
	requireCode(t, err, connect.CodeNotFound)
	f.Catalog.Put(f.Namespace)
}

func TestConcurrentPublishesSerialize(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	p := draftNew(t, f, policy.KindBreaker, "name: racy\n")
	const workers = 6
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.SaveDraft(ctx, f.Operator, p.ID, "name: racy\nmin_requests: "+string(rune('1'+i))+"0\n"); err != nil {
				results <- err
				return
			}
			_, err := svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	published := 0
	for err := range results {
		if err == nil {
			published++
			continue
		}
		// A publish that finds the draft already consumed fails cleanly.
		requireReason(t, err, apperr.ReasonFailedPrecondition)
	}
	got, err := svc.GetPolicy(ctx, f.Admin, p.ID)
	require.NoError(t, err)
	require.Equal(t, published, got.CurrentVersion)
	require.Equal(t, published, f.Count(t, `SELECT count(*) FROM policy_versions WHERE policy_id = $1`, p.ID))
}
