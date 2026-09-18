package policysvc_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/policysvc"
	"github.com/TikHub/Spinneret/internal/policysvc/policysvctest"
)

func TestResolvePoliciesMirrorsCatalogFallbacks(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// A binding of a never-published policy is not in effect (inserted
	// directly: the service refuses to create one).
	draft := draftNew(t, f, policy.KindRotation, "name: draft-rotation\n")
	_, err := f.Pool.Exec(ctx, `INSERT INTO policy_bindings (id, policy_id, kind, namespace_id, site_id)
		VALUES ($1, $2, 'rotation', $3, $4)`, idgen.New(idgen.PolicyBinding), draft.ID, f.Namespace.ID, f.Shop.ID)
	require.NoError(t, err)
	got, err := svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	require.Equal(t, "default-rotation@namespace", resolvedNames(got)[policy.KindRotation])

	// A bound published policy whose stored spec no longer validates is
	// reported as the built-in default, as the catalog applies it.
	broken := publishNew(t, f, policy.KindBreaker, "name: broken-breaker\nbind: {site: shop, client: web}\n")
	_, err = f.Pool.Exec(ctx, `UPDATE policy_versions SET spec = '{"name":"broken-breaker","window":"nope"}' WHERE policy_id = $1`, broken.ID)
	require.NoError(t, err)
	got, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop", Client: "web"})
	require.NoError(t, err)
	require.Equal(t, "default-breaker@builtin", resolvedNames(got)[policy.KindBreaker])
	require.Empty(t, got[3].PolicyID)
	require.Equal(t, policy.DefaultYAML(policy.KindBreaker), got[3].YAML)

	// A signal child whose parent no longer validates falls back as well.
	parent := publishNew(t, f, policy.KindSignal, "name: parent-signals\n")
	publishNew(t, f, policy.KindSignal, "name: child-signals\nextends: parent-signals\nbind: {site: forum}\n")
	_, err = f.Pool.Exec(ctx, `UPDATE policy_versions SET spec = '{"name":"parent-signals","rules":[{"outcome":"nope"}]}' WHERE policy_id = $1`, parent.ID)
	require.NoError(t, err)
	got, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "forum"})
	require.NoError(t, err)
	require.Equal(t, "default-signal@builtin", resolvedNames(got)[policy.KindSignal])
	require.Equal(t, policy.DefaultYAML(policy.KindSignal), got[1].YAML)
}

func TestNamesAreNotDisclosedWithoutPolicyRead(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// Unknown sites answer with the permission error, not site_unknown.
	for _, p := range []*authz.Principal{f.LeaseToken, f.OtherTenant} {
		want := apperr.ReasonPermissionDenied
		if p.Kind == authz.KindToken {
			want = apperr.ReasonScopeMissing
		}
		_, err := svc.ResolvePolicies(ctx, p, f.Namespace, policysvc.Target{Site: "no-such-site"})
		requireReason(t, err, want)
		_, err = svc.DebugReport(ctx, p, f.Namespace, debugIn(func(in *policysvc.DebugInput) { in.Site = "no-such-site" }))
		requireReason(t, err, want)
		_, err = svc.ListBindings(ctx, p, f.Namespace, "", "no-such-site")
		requireReason(t, err, want)
	}
	// With read access the unknown site is reported.
	_, err := svc.ResolvePolicies(ctx, f.ShopAdmin, f.Namespace, policysvc.Target{Site: "no-such-site"})
	requireReason(t, err, apperr.ReasonSiteUnknown)
}

func TestSetBindingOfInvisiblePolicyIsAudited(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()

	hidden := publishNew(t, f, policy.KindAction, "name: hidden-actions\nbind: {site: forum}\n")
	_ = f.Audit.Take()
	_, err := f.Service.SetBinding(ctx, f.ShopAdmin, hidden.ID, policysvc.Target{Site: "shop"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	entries := f.Audit.Take()
	require.Len(t, entries, 1)
	require.Equal(t, policysvc.AuditBindingSet, entries[0].Action)
	require.Equal(t, "denied", entries[0].Result)
	require.Equal(t, hidden.ID, entries[0].ResourceID)
	require.Zero(t, f.Count(t, `SELECT count(*) FROM policy_bindings WHERE policy_id = $1 AND site_id = $2`, hidden.ID, f.Shop.ID))
}

func TestDebugDraftOfAnotherNamespaceIsNotFound(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// A second namespace of another tenant with its own defaults.
	tenantID := idgen.New(idgen.Tenant)
	other := catalogtest.NewNamespace(tenantID, idgen.New(idgen.Namespace), "elsewhere")
	_, err := f.Pool.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenantID, "t-"+tenantID[len(tenantID)-8:])
	require.NoError(t, err)
	_, err = f.Pool.Exec(ctx, `INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, other.ID, tenantID, other.Name)
	require.NoError(t, err)
	f.Catalog.Put(other)
	tx, err := f.Pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, f.Service.InstallNamespaceDefaults(ctx, tx, other.ID, ""))
	require.NoError(t, tx.Commit(ctx))
	var foreignID string
	require.NoError(t, f.Pool.QueryRow(ctx, `SELECT id FROM policies WHERE namespace_id = $1 AND kind = 'signal'`, other.ID).Scan(&foreignID))

	platform := &authz.Principal{Kind: authz.KindUser, ID: "usr_root", IsPlatformAdmin: true}
	for _, p := range []*authz.Principal{f.Viewer, platform} {
		_, err = f.Service.DebugReport(ctx, p, f.Namespace, debugIn(func(in *policysvc.DebugInput) { in.DraftPolicyID = foreignID }))
		requireCode(t, err, connect.CodeNotFound)
	}
}
