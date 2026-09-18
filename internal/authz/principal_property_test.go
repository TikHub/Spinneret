package authz

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/pkg/glob"
)

// The property tests below compare Can against an oracle written directly
// from spec §3.2 (plus the documented fail-closed rules) over randomly
// generated principals and resources, and check SiteFilter consistency and
// monotonicity.

const propertyIterations = 20_000

var (
	propTenants    = []string{tenantA, tenantB, ""}
	propNamespaces = []string{nsProd, nsStaging, ""}
	propSites      = []string{siteShop, siteMarket, siteBlog, ""}
	propSiteNames  = map[string]string{siteShop: "shop", siteMarket: "market", siteBlog: "blog"}
	propRoles      = []Role{RoleViewer, RoleOperator, RoleAdmin, RoleOwner, "bogus"}
	propPerms      = append(AllPermissions(), "bogus:perm")
	propScopes     = []string{
		"lease:acquire", "lease:acquire:shop", "report:write", "report:write:market",
		"config:read", "config:read:app*", "config:publish", "config:publish:ops",
		"secret:read:prod/db/*", "secret:read:*", "identity:write", "identity:write:blog",
		"proxy:write", "admin",
	}
	propGroups  = []string{"", "app", "app-hk", "ops", "_runtime"}
	propSecrets = []string{"", "db/password", "cache/token", "db//x", "db/../x"}
)

func pick[T any](rng *rand.Rand, list []T) T { return list[rng.IntN(len(list))] }

func randomSubset[T any](rng *rand.Rand, list []T, maxLen int) []T {
	n := rng.IntN(maxLen + 1)
	out := make([]T, 0, n)
	for range n {
		out = append(out, pick(rng, list))
	}
	return out
}

func randomBinding(rng *rand.Rand) Binding {
	b := Binding{TenantID: pick(rng, propTenants), Role: pick(rng, propRoles), NamespaceID: pick(rng, propNamespaces)}
	if rng.IntN(2) == 0 {
		b.SiteIDs = randomSubset(rng, propSites, 3)
	}
	if rng.IntN(3) == 0 {
		b.Extra = randomSubset(rng, propPerms, 4)
	}
	return b
}

func randomResource(rng *rand.Rand) Resource {
	r := Resource{TenantID: pick(rng, propTenants), NamespaceID: pick(rng, propNamespaces)}
	switch r.NamespaceID {
	case nsProd:
		r.NamespaceName = nsProdName
	case nsStaging:
		r.NamespaceName = nsStageName
	}
	if rng.IntN(4) == 0 {
		r.NamespaceName = ""
	}
	r.SiteID = pick(rng, propSites)
	r.SiteName = propSiteNames[r.SiteID]
	r.ConfigGroup = pick(rng, propGroups)
	r.SecretPath = pick(rng, propSecrets)
	return r
}

// oracleUserCan restates the user rules of spec §3.2.
func oracleUserCan(p *Principal, perm Permission, r Resource) bool {
	if !slices.Contains(AllPermissions(), perm) {
		return false
	}
	if p.IsPlatformAdmin {
		return true
	}
	if perm == PermTenantManage || perm == PermKEKManage || r.TenantID == "" || (r.SiteID != "" && r.NamespaceID == "") {
		return false
	}
	for _, b := range p.Bindings {
		if b.TenantID == "" || b.TenantID != r.TenantID || (b.NamespaceID != "" && b.NamespaceID != r.NamespaceID) {
			continue
		}
		rolePerms := RolePermissions(b.Role)
		if rolePerms == nil {
			continue
		}
		extra := slices.Contains(b.Extra, perm) &&
			(perm == PermConfigPublish || perm == PermSecretReveal || perm == PermIdentityReveal || perm == PermPolicyPublish)
		if !slices.Contains(rolePerms, perm) && !extra {
			continue
		}
		switch {
		case len(b.SiteIDs) == 0:
			return true
		case r.SiteID == "":
			if (perm == PermProxyRead || perm == PermNamespaceRead) && b.NamespaceID != "" {
				return true
			}
		case slices.Contains(b.SiteIDs, r.SiteID):
			return true
		}
	}
	return false
}

// oracleTokenCan restates the token scope table of spec §3.2.
func oracleTokenCan(p *Principal, perm Permission, r Resource) bool {
	if !slices.Contains(AllPermissions(), perm) || perm == PermTenantManage || perm == PermKEKManage {
		return false
	}
	if r.TenantID == "" || r.NamespaceID == "" || p.TenantID != r.TenantID || p.NamespaceID != r.NamespaceID {
		return false
	}
	siteOK := func(arg string) bool { return arg == "" || (r.SiteName != "" && arg == r.SiteName) }
	for _, s := range p.Scopes {
		switch s.Name {
		case ScopeLeaseAcquire:
			if perm == PermLeaseAcquire && siteOK(s.Arg) {
				return true
			}
		case ScopeReportWrite:
			if perm == PermReportWrite && siteOK(s.Arg) {
				return true
			}
		case ScopeIdentityWrite:
			if (perm == PermIdentityRead || perm == PermIdentityWrite || perm == PermIdentityOperate) && siteOK(s.Arg) {
				return true
			}
		case ScopeConfigRead, ScopeConfigPublish:
			granted := perm == PermConfigRead ||
				(s.Name == ScopeConfigPublish && (perm == PermConfigWrite || perm == PermConfigPublish))
			if granted && (s.Arg == "" || glob.Match(s.Arg, r.ConfigGroup)) {
				return true
			}
		case ScopeSecretRead:
			name := r.NamespaceName
			if name == "" {
				name = p.NamespaceName
			}
			// The resource namespace name wins; it falls back to the token's and
			// must agree with it when both are known.
			if perm == PermSecretRead && name != "" && (p.NamespaceName == "" || name == p.NamespaceName) && cleanSecretPath(r.SecretPath) &&
				glob.Match(s.Arg, name+"/"+r.SecretPath) {
				return true
			}
		case ScopeProxyWrite:
			if perm == PermProxyRead || perm == PermProxyWrite || perm == PermProxyOperate {
				return true
			}
		case ScopeAdmin:
			if slices.Contains(RolePermissions(RoleAdmin), perm) {
				return true
			}
		}
	}
	return false
}

func TestUserCanMatchesOracle(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(11, 17))
	allowed := 0
	for i := range propertyIterations {
		var bindings []Binding
		for range rng.IntN(4) {
			bindings = append(bindings, randomBinding(rng))
		}
		p := &Principal{Kind: KindUser, ID: "usr_x", TenantID: pick(rng, propTenants), Bindings: bindings, IsPlatformAdmin: rng.IntN(20) == 0}
		r := randomResource(rng)
		perm := pick(rng, propPerms)
		require.Equal(t, oracleUserCan(p, perm, r), p.Can(perm, r), "iteration %d: %s on %+v by %+v", i, perm, r, p)

		// Monotonicity: an additional binding never removes access.
		if p.Can(perm, r) {
			allowed++
			more := *p
			more.Bindings = append(slices.Clone(p.Bindings), randomBinding(rng))
			require.True(t, more.Can(perm, r), "iteration %d: adding a binding removed access", i)
		}

		// SiteFilter agrees with Can for site resources of the namespace.
		if r.TenantID != "" && r.NamespaceID != "" && r.SiteID != "" {
			all, ids := p.SiteFilter(r.TenantID, r.NamespaceID, perm)
			want := all || slices.Contains(ids, r.SiteID)
			require.Equal(t, want, p.Can(perm, r), "iteration %d: SiteFilter disagrees for %s on %+v by %+v", i, perm, r, p)
		}
	}
	requireBalanced(t, allowed)
}

func TestTokenCanMatchesOracle(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(23, 29))
	allowed := 0
	for i := range propertyIterations {
		scopes, err := ParseScopes(slices.Compact(randomSubset(rng, propScopes, 3)))
		if err != nil {
			continue // duplicates that were not adjacent
		}
		p := &Principal{
			Kind: KindToken, ID: "tok_x", TenantID: pick(rng, propTenants[:2]), NamespaceID: pick(rng, propNamespaces[:2]),
			NamespaceName: nsProdName, Scopes: scopes,
		}
		if rng.IntN(10) == 0 {
			// Occasionally a malformed token principal without a binding.
			p.TenantID, p.NamespaceID = pick(rng, propTenants), pick(rng, propNamespaces)
		}
		switch {
		case rng.IntN(8) == 0:
			p.NamespaceName = ""
		case p.NamespaceID == nsStaging:
			p.NamespaceName = nsStageName
		}
		r := randomResource(rng)
		if rng.IntN(4) != 0 {
			// Mostly target the token's own tenant and namespace so that
			// scope matching, not only the binding check, is exercised.
			r.TenantID, r.NamespaceID = p.TenantID, p.NamespaceID
		}
		perm := pick(rng, propPerms)
		require.Equal(t, oracleTokenCan(p, perm, r), p.Can(perm, r), "iteration %d: %s on %+v by scopes %v", i, perm, r, scopes)

		// A token never holds anything outside its own tenant and namespace.
		if p.Can(perm, r) {
			allowed++
			require.Equal(t, p.TenantID, r.TenantID)
			require.Equal(t, p.NamespaceID, r.NamespaceID)
			require.NotEmpty(t, r.NamespaceID)
		}

		// SiteFilter never claims unrestricted site access Can would refuse.
		// Config-group and secret-path globs are not site restrictions, so
		// those permissions are excluded.
		globbed := perm == PermConfigRead || perm == PermConfigWrite || perm == PermConfigPublish || perm == PermSecretRead
		if r.TenantID != "" && r.NamespaceID != "" && r.SiteID != "" && !globbed {
			if all, ids := p.SiteFilter(r.TenantID, r.NamespaceID, perm); all {
				require.Nil(t, ids)
				require.True(t, p.Can(perm, r), "iteration %d: SiteFilter all=true but Can denies %s on %+v", i, perm, r)
			}
		}
	}
	requireBalanced(t, allowed)
}

// requireBalanced guards against generators that only exercise one outcome.
func requireBalanced(t *testing.T, allowed int) {
	t.Helper()
	t.Logf("allowed %d of %d", allowed, propertyIterations)
	require.Greater(t, allowed, propertyIterations/50, "too few allowed checks to be meaningful")
	require.Less(t, allowed, propertyIterations-propertyIterations/50, "too few denied checks to be meaningful")
}
