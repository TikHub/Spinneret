//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
)

// Scenario constants shared by the fixture and the scenarios.
const (
	webIdentities   = 30
	appIdentities   = 6
	accountsEvery   = 3 // every third web identity belongs to an account
	proxyCount      = 4
	reuseInterval   = 2 * time.Second
	searchURIPrefix = "/site/search"
	detailTemplate  = "/site/item/{id}"
	webTypeName     = "web_cookie"
	appTypeName     = "app_device"
	secretPath      = "e2e/key"
	configGroup     = "crawler"
	configKey       = "e2e.json"
)

// identityInfo is one imported identity.
type identityInfo struct {
	ID        string
	Client    string
	SessionID string // web: cookies.sessionid; app: device_id (the mock target attribution label)
	Account   string
}

// proxyInfo is one imported proxy.
type proxyInfo struct {
	ID    string
	Label string // user name = mock proxy id
}

// fixture is the per-run world created through the admin APIs (scenario a).
type fixture struct {
	cfg     config
	console *console
	node    *node
	mock    *mockAdmin
	crawler *crawler
	sink    *webhookSink

	runID       string
	namespace   string
	namespaceID string
	site        string
	siteID      string
	// groups by "<client>/<name>".
	groups    map[string]*spinneretv1.EndpointGroup
	web       []identityInfo
	app       []identityInfo
	byID      map[string]identityInfo
	proxies   []proxyInfo
	proxyByID map[string]proxyInfo
	token     string
	tokenID   string
	channelID string
	// Created by scenario h, deleted by cleanup.
	configItemID string
	secretID     string
	sinkSecret   string
	policyIDs    map[string]string
}

func (f *fixture) group(client, name string) *spinneretv1.EndpointGroup {
	return f.groups[client+"/"+name]
}

// setupFixture creates the namespace, site, endpoint groups, identity types, identities, accounts,
// proxies, published policies, node token and notification channel of the run.
func setupFixture(ctx context.Context, t *testing.T, cfg config) *fixture {
	t.Helper()
	f := &fixture{
		cfg:       cfg,
		mock:      newMockAdmin(cfg.MockURL),
		crawler:   newCrawler(cfg),
		groups:    map[string]*spinneretv1.EndpointGroup{},
		byID:      map[string]identityInfo{},
		proxyByID: map[string]proxyInfo{},
		policyIDs: map[string]string{},
	}
	f.runID = time.Now().UTC().Format("0102150405") + randomHex(t, 2)
	f.namespace = "e2e-" + f.runID
	f.site = "shop-" + f.runID
	f.sinkSecret = randomHex(t, 16)
	f.console = login(ctx, t, cfg)
	f.sink = startSink(t, cfg.SinkAddr, f.sinkSecret)
	// Start from a mock target without scripted behavior.
	f.mock.clearRules(ctx, t)
	f.mock.clearProxyRules(ctx, t)
	t.Logf("run %s: namespace %s, site %s", f.runID, f.namespace, f.site)

	c := f.console
	ns, err := c.tenants.CreateNamespace(ctx, connect.NewRequest(&spinneretv1.CreateNamespaceRequest{
		Name: f.namespace, DisplayName: "E2E " + f.runID, Description: "end-to-end test run",
	}))
	require.NoError(t, err, "CreateNamespace")
	f.namespaceID = ns.Msg.GetNamespace().GetId()
	t.Cleanup(func() { f.cleanup(t) })

	site, err := c.sites.CreateSite(ctx, connect.NewRequest(&spinneretv1.CreateSiteRequest{
		Namespace: f.namespace, Name: f.site, DisplayName: "E2E shop", Clients: []string{"web", "app"},
	}))
	require.NoError(t, err, "CreateSite")
	f.siteID = site.Msg.GetSite().GetId()
	require.ElementsMatch(t, []string{"web", "app"}, site.Msg.GetSite().GetClients())

	for _, client := range []string{"web", "app"} {
		for _, g := range []struct{ name, kind, pattern string }{
			{"search", "prefix", searchURIPrefix},
			{"detail", "template", detailTemplate},
		} {
			res, err := c.sites.CreateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{
				Namespace: f.namespace, Site: f.site, Client: client, Name: g.name,
				Rules: []*spinneretv1.URIRule{{Kind: g.kind, Pattern: g.pattern}},
			}))
			require.NoErrorf(t, err, "CreateEndpointGroup %s/%s", client, g.name)
			f.groups[client+"/"+g.name] = res.Msg.GetEndpointGroup()
		}
	}
	tu, err := c.sites.TestURI(ctx, connect.NewRequest(&spinneretv1.TestURIRequest{
		Namespace: f.namespace, Site: f.site, Client: "web", Uri: "/site/item/42?x=1",
	}))
	require.NoError(t, err, "TestURI")
	require.Equal(t, "detail", tu.Msg.GetEndpointGroup())

	f.createIdentities(ctx, t)
	f.createProxies(ctx, t)
	f.createPolicies(ctx, t)
	f.createToken(ctx, t)
	f.createChannel(ctx, t)
	f.node = newNode(cfg.URL, f.token, "e2e-node-"+f.runID)
	f.waitLeasable(ctx, t, "web", "/site/search?q=warmup")
	return f
}

const webTypeYAML = `name: web_cookie
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
activation: probe
deliver:
  cookies: "{{ cookies }}"
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
`

const appTypeYAML = `name: app_device
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
  extra:      { type: json }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    iid: "{{ install_id }}"
  json: "{{ extra }}"
`

func (f *fixture) createIdentities(ctx context.Context, t *testing.T) {
	t.Helper()
	c := f.console
	for _, spec := range []string{webTypeYAML, appTypeYAML} {
		_, err := c.ids.CreateIdentityType(ctx, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{
			Namespace: f.namespace, Site: f.site, SpecYaml: spec,
		}))
		require.NoError(t, err, "CreateIdentityType")
	}

	var web strings.Builder
	for i := range webIdentities {
		sid := fmt.Sprintf("s%s-%02d", f.runID, i)
		account := ""
		if i%accountsEvery == 0 {
			account = fmt.Sprintf(`,"_account":"acct-%s-%02d"`, f.runID, i)
		}
		fmt.Fprintf(&web, `{"cookies":{"sessionid":%q,"csrftoken":"c%02d"},"user_agent":"E2E-UA/%02d","_labels":{"sid":%q}%s}`+"\n",
			sid, i, i, sid, account)
	}
	imp, err := c.ids.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: f.namespace, Site: f.site, Type: webTypeName, Format: "jsonl", Data: web.String(),
	}))
	require.NoError(t, err, "ImportIdentities web")
	require.Empty(t, imp.Msg.GetFailed())
	require.EqualValues(t, webIdentities, imp.Msg.GetCreated())

	var app strings.Builder
	app.WriteString("device_id,install_id\n")
	for i := range appIdentities {
		fmt.Fprintf(&app, "d%s-%02d,i%02d\n", f.runID, i, i)
	}
	imp, err = c.ids.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: f.namespace, Site: f.site, Type: appTypeName, Format: "csv", Data: app.String(),
	}))
	require.NoError(t, err, "ImportIdentities app")
	require.Empty(t, imp.Msg.GetFailed())
	require.EqualValues(t, appIdentities, imp.Msg.GetCreated())

	// Re-importing the same rows is idempotent.
	again, err := c.ids.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: f.namespace, Site: f.site, Type: webTypeName, Format: "jsonl", Data: web.String(),
	}))
	require.NoError(t, err, "re-import web identities")
	require.EqualValues(t, 0, again.Msg.GetCreated())
	require.EqualValues(t, webIdentities, again.Msg.GetUnchanged())

	list, err := c.ids.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{
		Namespace: f.namespace, Filter: &spinneretv1.IdentityFilter{Site: f.site}, PageSize: 500,
	}))
	require.NoError(t, err, "ListIdentities")
	require.Len(t, list.Msg.GetIdentities(), webIdentities+appIdentities)
	for _, idt := range list.Msg.GetIdentities() {
		info := identityInfo{ID: idt.GetId(), Client: idt.GetClient(), Account: idt.GetAccountRef()}
		switch idt.GetType() {
		case webTypeName:
			require.Equal(t, "pending", idt.GetState(), "probe activation imports pending identities")
			info.SessionID = idt.GetLabels()["sid"]
			require.NotEmpty(t, info.SessionID)
			f.web = append(f.web, info)
		case appTypeName:
			require.Equal(t, "active", idt.GetState(), "immediate activation imports active identities")
			f.app = append(f.app, info)
		}
		f.byID[info.ID] = info
	}
	require.Len(t, f.web, webIdentities)
	require.Len(t, f.app, appIdentities)

	accounts, err := c.ids.ListAccounts(ctx, connect.NewRequest(&spinneretv1.ListAccountsRequest{
		Namespace: f.namespace, Site: f.site, PageSize: 500,
	}))
	require.NoError(t, err, "ListAccounts")
	require.Len(t, accounts.Msg.GetAccounts(), (webIdentities+accountsEvery-1)/accountsEvery)
}

func (f *fixture) createProxies(ctx context.Context, t *testing.T) {
	t.Helper()
	var lines strings.Builder
	labels := map[string]string{} // username hint prefix (first 4 characters) -> label
	for i := 1; i <= proxyCount; i++ {
		// The label leads with the index so that the 4-character username hint identifies the proxy.
		label := fmt.Sprintf("p%dr%s", i, f.runID)
		labels[label[:4]] = label
		fmt.Fprintf(&lines, "http://%s:%s@%s kind=residential provider=mock max_concurrency=50\n", label, f.cfg.ProxyPassword, f.cfg.ProxyHost)
	}
	res, err := f.console.proxies.ImportProxies(ctx, connect.NewRequest(&spinneretv1.ImportProxiesRequest{
		Namespace: f.namespace, Format: "lines", Data: lines.String(),
	}))
	require.NoError(t, err, "ImportProxies")
	require.Empty(t, res.Msg.GetFailed())
	require.EqualValues(t, proxyCount, res.Msg.GetCreated())

	list, err := f.console.proxies.ListProxies(ctx, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: f.namespace, PageSize: 500}))
	require.NoError(t, err, "ListProxies")
	require.Len(t, list.Msg.GetProxies(), proxyCount)
	for _, p := range list.Msg.GetProxies() {
		require.Equal(t, "residential", p.GetKind())
		require.Equal(t, "active", p.GetState())
		require.EqualValues(t, 50, p.GetMaxConcurrency())
		require.NotContains(t, p.GetDisplayUrl(), f.cfg.ProxyPassword, "display URL must not contain credentials")
		label, ok := labels[strings.TrimSuffix(p.GetUsernameHint(), "***")]
		require.Truef(t, ok, "unexpected username hint %q", p.GetUsernameHint())
		info := proxyInfo{ID: p.GetId(), Label: label}
		f.proxies = append(f.proxies, info)
		f.proxyByID[info.ID] = info

		chk, err := f.console.proxies.CheckProxy(ctx, connect.NewRequest(&spinneretv1.CheckProxyRequest{Id: p.GetId()}))
		require.NoError(t, err, "CheckProxy")
		require.Truef(t, chk.Msg.GetOk(), "health check through %s failed: %s", label, chk.Msg.GetError())
	}
}

func (f *fixture) policyYAML() map[string]string {
	bind := fmt.Sprintf("bind: { site: %s }\n", f.site)
	return map[string]string{
		"rotation": "name: e2e-rotation\n" + bind + `identity_types: []
rotation:
  strategy: weighted_random
  lease_ttl: 60s
  max_concurrent_leases: 1
  reuse_interval: 2s
  reuse_anchor: released
  reuse_scope: endpoint_group
proxy:
  mode: bind_identity
  kinds: [residential]
`,
		"signal": "name: e2e-signal\n" + bind + `rules:
  - name: proxy-error
    when: { error_kind: [proxy_auth, conn_refused] }
    outcome: proxy_error
  - name: captcha
    when: { markers: [captcha_page] }
    outcome: captcha
  - name: login-redirect
    when: { markers: [login_redirect] }
    outcome: auth_invalid
  - name: empty-list
    when: { markers: [empty_list] }
    outcome: empty
  - name: business-forbidden
    when: { business_code: [10001] }
    outcome: forbidden
  - name: rate-limited
    when: { http_status: [429] }
    outcome: rate_limited
  - name: success
    when: { http_status: { gte: 200, lt: 300 } }
    outcome: success
`,
		"action": "name: e2e-action\n" + bind + `mode: enforce
rules:
  - name: rate-limited-cooldown
    when: { outcome: rate_limited }
    action: cooldown
    scope: identity_endpoint
    base: 3s
    multiplier: 2
    max: 30s
  - name: captcha-cooldown
    when: { outcome: captcha }
    action: cooldown
    scope: identity_site
    base: 5s
  - name: captcha-ban
    when: { outcome: captcha, count: { gte: 3, within: 10m } }
    action: ban
    scope: identity
    duration: 1h
  - name: auth-invalid-expire
    when: { outcome: auth_invalid }
    action: expire
    scope: identity
  - name: banned-account
    when: { outcome: banned }
    action: ban
    scope: account
    duration: permanent
`,
		"breaker": "name: e2e-breaker\n" + bind + `enabled: true
window: 20s
buckets: 4
min_requests: 20
trip:
  risk_ratio_gte: 0.5
  distinct_captcha_identities_gte: 0
  success_ratio_lte: 0
open_duration: 5s
max_open_duration: 1m
half_open:
  probe_leases_per_10s: 5
  close_min_samples: 3
  close_success_ratio_gte: 0.8
revert_recent_cooldowns: endpoint
`,
	}
}

func (f *fixture) createPolicies(ctx context.Context, t *testing.T) {
	t.Helper()
	c := f.console
	for _, kind := range []string{"rotation", "signal", "action", "breaker"} {
		yaml := f.policyYAML()[kind]
		v, err := c.policies.ValidatePolicy(ctx, connect.NewRequest(&spinneretv1.ValidatePolicyRequest{Kind: kind, Yaml: yaml}))
		require.NoErrorf(t, err, "ValidatePolicy %s", kind)
		require.Truef(t, v.Msg.GetValid(), "policy %s invalid: %v", kind, v.Msg.GetErrors())
		res, err := c.policies.CreatePolicy(ctx, connect.NewRequest(&spinneretv1.CreatePolicyRequest{
			Namespace: f.namespace, Kind: kind, Yaml: yaml, Publish: true, Comment: "e2e",
		}))
		require.NoErrorf(t, err, "CreatePolicy %s", kind)
		require.EqualValues(t, 1, res.Msg.GetPolicy().GetCurrentVersion())
		f.policyIDs[kind] = res.Msg.GetPolicy().GetId()
	}
	resolved, err := c.policies.ResolvePolicies(ctx, connect.NewRequest(&spinneretv1.ResolvePoliciesRequest{
		Namespace: f.namespace, Site: f.site, Client: "web", EndpointGroup: "search",
	}))
	require.NoError(t, err, "ResolvePolicies")
	require.Len(t, resolved.Msg.GetPolicies(), 4)
	for _, p := range resolved.Msg.GetPolicies() {
		require.Equalf(t, f.policyIDs[p.GetKind()], p.GetPolicyId(), "resolved %s policy", p.GetKind())
		require.Equal(t, "site", p.GetLevel())
	}
}

func (f *fixture) createToken(ctx context.Context, t *testing.T) {
	t.Helper()
	res, err := f.console.access.CreateToken(ctx, connect.NewRequest(&spinneretv1.CreateTokenRequest{
		Namespace: f.namespace, Name: "e2e-node", Description: "end-to-end crawler node",
		Scopes: []string{"lease:acquire", "report:write", "config:read", "secret:read:" + f.namespace + "/e2e/*"},
	}))
	require.NoError(t, err, "CreateToken")
	f.token = res.Msg.GetPlaintext()
	f.tokenID = res.Msg.GetToken().GetId()
	require.True(t, strings.HasPrefix(f.token, "spn_"))
}

func (f *fixture) createChannel(ctx context.Context, t *testing.T) {
	t.Helper()
	cfg, err := structpb.NewStruct(map[string]any{"url": f.cfg.CallbackURL + "/hook", "secret": f.sinkSecret})
	require.NoError(t, err)
	res, err := f.console.notify.CreateChannel(ctx, connect.NewRequest(&spinneretv1.CreateChannelRequest{
		Namespace: f.namespace, Name: "e2e-" + f.runID, Kind: "webhook", Config: cfg,
		EventTypes:  []string{"identity_expired", "breaker_opened", "breaker_reopened", "breaker_closed", "test"},
		Sites:       []string{f.site},
		MinSeverity: "info",
	}))
	require.NoError(t, err, "CreateChannel")
	f.channelID = res.Msg.GetChannel().GetId()
	// The secret is write-only.
	require.NotEqual(t, f.sinkSecret, res.Msg.GetChannel().GetConfig().GetFields()["secret"].GetStringValue())

	test, err := f.console.notify.TestChannel(ctx, connect.NewRequest(&spinneretv1.TestChannelRequest{Id: f.channelID}))
	require.NoError(t, err, "TestChannel")
	require.Truef(t, test.Msg.GetDelivery().GetOk(), "test delivery to %s failed: %s", f.cfg.CallbackURL, test.Msg.GetDelivery().GetError())
	f.sink.wait(t, 10*time.Second, "test", func(d webhookDelivery) bool { return d.Kind == "test" })
	f.sink.requireValidSignatures(t)
}

// burstSize is the number of concurrent acquires used to reach every replica through the load
// balancer (least_conn spreads concurrent requests, sequential ones can all land on one replica).
// It must stay below the number of leasable identities of the client used.
const burstSize = 12

// acquireBurst issues n concurrent acquires, releases every lease it got and returns the results
// (one entry per call, nil when the acquire succeeded). check runs for each acquired lease.
func (f *fixture) acquireBurst(ctx context.Context, client, uri string, waitMs int32, n int, check func(*spinneretv1.AcquireResponse) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := f.node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{
				Site: f.site, Client: client, Uri: uri, WaitMs: waitMs,
			}))
			if err != nil {
				errs[i] = err
				return
			}
			if check != nil {
				errs[i] = check(res.Msg)
			}
			_, _ = f.node.leases.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: res.Msg.GetLease().GetLeaseId()}))
		}()
	}
	wg.Wait()
	return errs
}

// waitLeasable waits until the site is leasable on every replica: catalog snapshots are updated
// asynchronously after admin writes (invalidation events are debounced), so a burst of concurrent
// acquires must succeed completely, not just one request that may always hit the same replica.
func (f *fixture) waitLeasable(ctx context.Context, t *testing.T, client, uri string) {
	t.Helper()
	eventually(t, 30*time.Second, 200*time.Millisecond, "site leasable on every replica", func() (bool, string) {
		for _, err := range f.acquireBurst(ctx, client, uri, 2000, burstSize, nil) {
			if err != nil {
				return false, err.Error()
			}
		}
		return true, ""
	})
}

// cleanup deletes everything the run created (unless the run failed or SPINNERET_E2E_KEEP is set): a
// namespace is only deletable once its sites, proxies, config items, secrets and usable tokens are gone.
func (f *fixture) cleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	f.mock.clearRules(ctx, t)
	f.mock.clearProxyRules(ctx, t)
	if t.Failed() || f.cfg.Keep {
		t.Logf("keeping namespace %s (site %s) for inspection", f.namespace, f.site)
		return
	}
	c := f.console
	steps := []struct {
		what string
		skip bool
		run  func() error
	}{
		{"delete channel", f.channelID == "", func() error {
			_, err := c.notify.DeleteChannel(ctx, connect.NewRequest(&spinneretv1.DeleteChannelRequest{Id: f.channelID}))
			return err
		}},
		{"revoke token", f.tokenID == "", func() error {
			_, err := c.access.RevokeToken(ctx, connect.NewRequest(&spinneretv1.RevokeTokenRequest{Id: f.tokenID}))
			return err
		}},
		{"delete site", f.siteID == "", func() error {
			_, err := c.sites.DeleteSite(ctx, connect.NewRequest(&spinneretv1.DeleteSiteRequest{Id: f.siteID, Force: true}))
			return err
		}},
		{"delete proxies", len(f.proxies) == 0, func() error {
			ids := make([]string, 0, len(f.proxies))
			for _, p := range f.proxies {
				ids = append(ids, p.ID)
			}
			_, err := c.proxies.DeleteProxies(ctx, connect.NewRequest(&spinneretv1.DeleteProxiesRequest{Ids: ids}))
			return err
		}},
		{"delete config item", f.configItemID == "", func() error {
			_, err := c.configs.DeleteConfigItem(ctx, connect.NewRequest(&spinneretv1.DeleteConfigItemRequest{Id: f.configItemID}))
			return err
		}},
		{"delete secret", f.secretID == "", func() error {
			_, err := c.secrets.DeleteSecret(ctx, connect.NewRequest(&spinneretv1.DeleteSecretRequest{Id: f.secretID}))
			return err
		}},
		{"delete namespace", f.namespaceID == "", func() error {
			_, err := c.tenants.DeleteNamespace(ctx, connect.NewRequest(&spinneretv1.DeleteNamespaceRequest{Id: f.namespaceID}))
			return err
		}},
	}
	for _, step := range steps {
		if step.skip {
			continue
		}
		if err := step.run(); err != nil {
			t.Errorf("cleanup: %s: %v", step.what, err)
			return
		}
	}
	t.Logf("cleanup: namespace %s deleted", f.namespace)
}
