package proxy

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestListProxies(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	proxies := env.importLines(t, env.ns,
		"http://u1:p1@10.2.0.1:80 kind=residential provider=acme region=us tags=a,b",
		"http://10.2.0.2:80 kind=datacenter provider=acme region=de tags=a",
		"socks5://10.2.0.3:1080 kind=mobile provider=beta region=us",
		"http://gw.example.net:3128 kind=tunnel provider=beta_x region=jp",
	)
	_, err := env.pool.Exec(ctx, `UPDATE proxies SET state = 'retired' WHERE id = $1`, proxies[3].ID)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `UPDATE proxies SET exit_ip = '198.51.100.7' WHERE id = $1`, proxies[2].ID)
	require.NoError(t, err)

	ids := func(res ListResult) []string {
		out := make([]string, len(res.Proxies))
		for i, p := range res.Proxies {
			out[i] = p.ID
		}
		return out
	}
	tests := []struct {
		name string
		f    ListFilter
		want []*Proxy
	}{
		{name: "default excludes retired", f: ListFilter{}, want: proxies[:3]},
		{name: "retired state", f: ListFilter{States: []string{StateRetired}}, want: proxies[3:]},
		{name: "kinds", f: ListFilter{Kinds: []string{KindMobile, KindResidential}}, want: []*Proxy{proxies[0], proxies[2]}},
		{name: "providers", f: ListFilter{Providers: []string{"beta"}}, want: proxies[2:3]},
		{name: "regions", f: ListFilter{Regions: []string{"us"}}, want: []*Proxy{proxies[0], proxies[2]}},
		{name: "all tags", f: ListFilter{Tags: []string{"a", "b"}}, want: proxies[:1]},
		{name: "search host", f: ListFilter{Search: "10.2.0.2"}, want: proxies[1:2]},
		{name: "search exit ip", f: ListFilter{Search: "198.51"}, want: proxies[2:3]},
		{name: "search like metacharacters are literal", f: ListFilter{Search: "%"}, want: nil},
		{name: "search id prefix", f: ListFilter{Search: proxies[0].ID}, want: proxies[:1]},
		{name: "search does not match credentials", f: ListFilter{Search: "p1@"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := env.svc.ListProxies(ctx, viewerUser(), env.ns, tt.f)
			require.NoError(t, err)
			want := make([]string, len(tt.want))
			for i, p := range tt.want {
				want[i] = p.ID
			}
			require.ElementsMatch(t, want, ids(res))
			require.Equal(t, len(want), res.Total)
		})
	}

	t.Run("keyset pagination", func(t *testing.T) {
		var seen []string
		token := ""
		for page := 0; page < 5; page++ {
			res, err := env.svc.ListProxies(ctx, adminUser(), env.ns, ListFilter{PageSize: 2, PageToken: token})
			require.NoError(t, err)
			require.Equal(t, 3, res.Total)
			seen = append(seen, ids(res)...)
			token = res.NextPageToken
			if token == "" {
				break
			}
		}
		require.Equal(t, []string{proxies[2].ID, proxies[1].ID, proxies[0].ID}, seen, "newest first")
	})

	t.Run("invalid filters", func(t *testing.T) {
		for _, f := range []ListFilter{
			{PageToken: "!!"},
			{States: []string{"zombie"}},
			{Kinds: []string{"satellite"}},
			{Search: string(make([]byte, maxSearchLength+1))},
			{Providers: make([]string, maxFilterValues+1)},
		} {
			_, err := env.svc.ListProxies(ctx, adminUser(), env.ns, f)
			requireReason(t, err, apperr.ReasonInvalidArgument)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		_, err := env.svc.ListProxies(ctx, foreignUser(), env.ns, ListFilter{})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.ListProxies(ctx, tokenPrincipal(t, "lease:acquire"), env.ns, ListFilter{})
		requireReason(t, err, apperr.ReasonScopeMissing)
		res, err := env.svc.ListProxies(ctx, tokenPrincipal(t, "proxy:write"), env.ns, ListFilter{})
		require.NoError(t, err)
		require.Len(t, res.Proxies, 3)
	})
}

func TestListProxiesHotState(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	env.svc.now = fixedClock(now)
	proxies := env.importLines(t, env.ns, "http://10.3.0.1:80", "http://10.3.0.2:80")
	_, err := env.pool.Exec(ctx, `INSERT INTO sites (id, namespace_id, name) VALUES ('sit_alpha', 'ns_main', 'alpha')`)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `INSERT INTO identity_types (id, site_id, client, name) VALUES ('ity_1', 'sit_alpha', 'web', 't')`)
	require.NoError(t, err)
	for i := 1; i <= 2; i++ {
		_, err = env.pool.Exec(ctx, `INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ($1, 'sit_alpha', 'web', 'ity_1', $2, $2)`,
			fmt.Sprintf("idt_%d", i), []byte{byte(i)})
		require.NoError(t, err)
		_, err = env.pool.Exec(ctx, `INSERT INTO proxy_bindings (identity_id, proxy_id) VALUES ($1, $2)`, fmt.Sprintf("idt_%d", i), proxies[0].ID)
		require.NoError(t, err)
	}

	sts := now.Add(-HealthTau).UnixMilli()
	cd := now.Add(5 * time.Minute)
	env.materialize(t, env.alpha, proxies[0], "sc", "10.00", "sts", strconv.FormatInt(sts, 10), "sn", "7", "al", "2",
		"cd", strconv.FormatInt(cd.UnixMilli(), 10))
	env.materialize(t, env.beta, proxies[0], "st", StateDead)

	res, err := env.svc.ListProxies(ctx, adminUser(), env.ns, ListFilter{})
	require.NoError(t, err)
	require.Len(t, res.Proxies, 2)
	byID := map[string]*Proxy{}
	for _, p := range res.Proxies {
		byID[p.ID] = p
	}
	first := byID[proxies[0].ID]
	require.Equal(t, 2, first.BoundIdentities)
	require.Equal(t, "main", first.NamespaceName)
	require.Len(t, first.Sites, 2)
	alpha := first.Sites[0]
	require.Equal(t, "alpha", alpha.Site)
	require.Equal(t, env.alpha.ID, alpha.SiteID)
	require.Equal(t, StateActive, alpha.State)
	require.InDelta(t, DecayScore(10, sts, now.UnixMilli()), alpha.Score, 0.001)
	require.InDelta(t, 70+(10-70)/2.718281828, alpha.Score, 0.01, "one tau decays 1/e of the distance to the baseline")
	require.Equal(t, 7, alpha.Samples)
	require.Equal(t, 2, alpha.ActiveLeases)
	require.NotNil(t, alpha.CooldownUntil)
	require.True(t, cd.Equal(*alpha.CooldownUntil))
	beta := first.Sites[1]
	require.Equal(t, SiteState{SiteID: env.beta.ID, Site: "beta", State: StateDead, Score: HealthBaseline}, beta)
	require.Empty(t, byID[proxies[1].ID].Sites, "sites without hot state are omitted")
	require.Zero(t, byID[proxies[1].ID].BoundIdentities)

	t.Run("site restricted principal sees only its sites", func(t *testing.T) {
		p := siteOperator(env.ns.ID, env.beta.ID)
		got, err := env.svc.GetProxy(ctx, p, proxies[0].ID)
		require.NoError(t, err)
		require.Len(t, got.Sites, 1)
		require.Equal(t, "beta", got.Sites[0].Site)
	})
}

func TestGetProxy(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	p := env.importLines(t, env.ns, "http://user:pass@10.4.0.1:80")[0]

	got, err := env.svc.GetProxy(ctx, viewerUser(), p.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, got.ID)
	require.Equal(t, "http://10.4.0.1:80", got.DisplayURL)
	require.Equal(t, "user***", got.UsernameHint)

	_, err = env.svc.GetProxy(ctx, adminUser(), "pxy_missing")
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = env.svc.GetProxy(ctx, foreignUser(), p.ID)
	requireReason(t, err, apperr.ReasonNotFound) // other tenants do not learn that the proxy exists

	env.cat.Remove(env.ns.ID)
	_, err = env.svc.GetProxy(ctx, adminUser(), p.ID)
	requireReason(t, err, apperr.ReasonNotFound)
}
