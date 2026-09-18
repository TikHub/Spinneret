package server_test

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	chstore "github.com/Evil0ctal/Spinneret/internal/store/clickhouse"
)

// cookieTypeYAML is an immediately activated cookie identity type.
const cookieTypeYAML = `name: web_cookie
client: web
fields:
  cookies:    { type: cookie_map, required: true, sensitive: true }
  user_agent: { type: string }
unique_by: [cookies.sessionid]
activation: immediate
deliver:
  cookie_header: "{{ cookies }}"
  headers:
    User-Agent: "{{ user_agent }}"
`

var leaseIDPattern = regexp.MustCompile(`^lse_[0-9a-f]{32}_[0-9a-z]+_[0-9a-f]{2}$`)

// identityRows returns JSON Lines for n identities whose session ids start with prefix.
func identityRows(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"cookies":{"sessionid":"%s%d","csrftoken":"c%d"},"user_agent":"UA-%s%d"}`+"\n", prefix, i, i, prefix, i)
	}
	return b.String()
}

// expectedCookieHeader is the rendered cookie header of identityRows row i.
func expectedCookieHeader(prefix string, i int) string {
	return fmt.Sprintf("csrftoken=c%d; sessionid=%s%d", i, prefix, i)
}

func TestEndToEnd(t *testing.T) {
	env := startE2E(t)
	ctx := context.Background()
	admin := newConsoleClient(t, env.baseURL)

	var nodeToken string
	t.Run("a_setup_console", func(t *testing.T) {
		login, err := admin.auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: e2eAdminUser, Password: e2eAdminPassword}))
		require.NoError(t, err)
		require.True(t, login.Msg.GetUser().GetIsPlatformAdmin())
		for _, ta := range login.Msg.GetTenants() {
			if ta.GetTenant().GetName() == e2eTenant {
				env.tenantID = ta.GetTenant().GetId()
			}
		}
		require.NotEmpty(t, env.tenantID, "login lists the bootstrap tenant")
		admin.headers.set("X-Spinneret-Tenant", env.tenantID)

		me, err := admin.auth.GetMe(ctx, connect.NewRequest(&spinneretv1.GetMeRequest{}))
		require.NoError(t, err)
		require.Equal(t, e2eAdminUser, me.Msg.GetUser().GetUsername())

		createCookieSite(t, admin, "shop", "search", "/search/", "s", 5)

		tok, err := admin.access.CreateToken(ctx, connect.NewRequest(&spinneretv1.CreateTokenRequest{
			Namespace: e2eNamespace, Name: "node-1",
			Scopes: []string{"lease:acquire", "report:write", "config:read", "secret:read:" + e2eNamespace + "/signer/*"},
		}))
		require.NoError(t, err)
		nodeToken = tok.Msg.GetPlaintext()
		require.True(t, strings.HasPrefix(nodeToken, "spn_"))
	})
	if t.Failed() {
		return
	}
	node := newNodeClient(env.baseURL, nodeToken)

	t.Run("b_acquire_renew_release", func(t *testing.T) {
		resp, err := node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Site: "shop", Client: "web", Uri: "/search/items?q=1"}))
		require.NoError(t, err)
		lease := resp.Msg.GetLease()
		require.Equal(t, "search", lease.GetEndpointGroup())
		require.Equal(t, "web_cookie", lease.GetIdentityType())
		require.Regexp(t, leaseIDPattern, lease.GetLeaseId())
		_, err = idgen.ParseLeaseID(lease.GetLeaseId())
		require.NoError(t, err)
		require.True(t, lease.GetExpiresAt().AsTime().After(time.Now()))
		cookie := resp.Msg.GetCredential().GetCookieHeader()
		requireKnownCookie(t, "s", 5, cookie, resp.Msg.GetCredential().GetHeaders()["User-Agent"])
		require.Positive(t, resp.Msg.GetHints().GetRenewBeforeMs())

		batch, err := node.leases.AcquireBatch(ctx, connect.NewRequest(&spinneretv1.AcquireBatchRequest{Site: "shop", Client: "web", Uri: "/search/x", Count: 3}))
		require.NoError(t, err)
		require.Len(t, batch.Msg.GetLeases(), 3)
		seen := map[string]bool{lease.GetIdentityId(): true}
		for _, l := range batch.Msg.GetLeases() {
			require.Falsef(t, seen[l.GetLease().GetIdentityId()], "identity %s leased twice", l.GetLease().GetIdentityId())
			seen[l.GetLease().GetIdentityId()] = true
		}

		// The default lease TTL is 2m; renewing never shortens a lease.
		renew, err := node.leases.Renew(ctx, connect.NewRequest(&spinneretv1.RenewRequest{LeaseId: lease.GetLeaseId(), ExtendMs: 300_000}))
		require.NoError(t, err)
		require.WithinDuration(t, time.Now().Add(5*time.Minute), renew.Msg.GetExpiresAt().AsTime(), 10*time.Second)
		require.True(t, renew.Msg.GetExpiresAt().AsTime().After(lease.GetExpiresAt().AsTime()))

		for _, id := range append([]string{lease.GetLeaseId()}, leaseIDs(batch.Msg.GetLeases())...) {
			rel, err := node.leases.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: id}))
			require.NoError(t, err)
			require.True(t, rel.Msg.GetReleased())
		}
		rel, err := node.leases.Release(ctx, connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: lease.GetLeaseId()}))
		require.NoError(t, err)
		require.False(t, rel.Msg.GetReleased(), "release is idempotent")
	})

	t.Run("c_rate_limited_cooldown", func(t *testing.T) {
		lease := acquire(t, node, "shop", "/search/items?q=2")
		sendReports(t, node, lease, "/search/items?q=2", 1, 429, nil)
		waitEndpointCooldown(t, admin, lease.GetIdentityId(), "search")

		// A site with a single identity: once it cools down nothing is available.
		createCookieSite(t, admin, "solo", "search", "/search/", "solo", 1)
		solo := acquire(t, node, "solo", "/search/a")
		sendReports(t, node, solo, "/search/a", 1, 429, nil)
		waitEndpointCooldown(t, admin, solo.GetIdentityId(), "search")

		_, err := node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Site: "solo", Client: "web", Uri: "/search/b"}))
		ce := requireConnectError(t, err, connect.CodeResourceExhausted, apperr.ReasonNoIdentityAvailable)
		retryAfter, err := strconv.ParseInt(ce.Meta().Get(apperr.HeaderRetryAfter), 10, 64)
		require.NoError(t, err)
		require.Positive(t, retryAfter)
	})

	t.Run("d_captcha_ban", func(t *testing.T) {
		lease := acquire(t, node, "shop", "/search/captcha")
		// Three captcha reports on one lease: the identity × site cooldown of the
		// first report does not prevent further reports of an issued lease.
		sendReports(t, node, lease, "/search/captcha", 3, 200, []string{"captcha_page"})
		id := lease.GetIdentityId()
		waitFor(t, 15*time.Second, "identity banned in postgresql", func() bool {
			got, err := admin.ids.GetIdentity(ctx, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
			require.NoError(t, err)
			return got.Msg.GetIdentity().GetState() == "banned"
		})
		events, err := admin.ids.ListStateEvents(ctx, connect.NewRequest(&spinneretv1.ListStateEventsRequest{
			Namespace: e2eNamespace, SubjectKind: "identity", SubjectId: id,
		}))
		require.NoError(t, err)
		var actions []string
		for _, ev := range events.Msg.GetEvents() {
			actions = append(actions, ev.GetAction())
		}
		require.Contains(t, actions, "ban")
	})

	t.Run("e_breaker", func(t *testing.T) {
		// Three identities report, a fourth stays available for the acquire
		// after the breaker is closed again.
		createCookieSite(t, admin, "brk", "api", "/api/", "b", 4)
		pol, err := admin.policies.CreatePolicy(ctx, connect.NewRequest(&spinneretv1.CreatePolicyRequest{
			Namespace: e2eNamespace, Kind: "breaker", Publish: true,
			Yaml: "name: e2e-breaker\ndescription: Sensitive breaker for the end-to-end test.\nmin_requests: 10\nopen_duration: 10m\n",
		}))
		require.NoError(t, err)
		_, err = admin.policies.SetBinding(ctx, connect.NewRequest(&spinneretv1.SetBindingRequest{PolicyId: pol.Msg.GetPolicy().GetId(), Site: "brk"}))
		require.NoError(t, err)

		stream := openEventStream(t, admin, env.tenantID, e2eNamespace)
		// 12 rate-limited reports from three distinct leases and identities.
		leases := make([]*spinneretv1.Lease, 3)
		identities := map[string]bool{}
		for i := range leases {
			leases[i] = acquire(t, node, "brk", "/api/items")
			identities[leases[i].GetIdentityId()] = true
		}
		require.Len(t, identities, len(leases), "every lease holds a different identity")
		for _, lease := range leases {
			sendReports(t, node, lease, "/api/items", 4, 429, nil)
		}

		var groupID string
		waitFor(t, 20*time.Second, "breaker open", func() bool {
			list, err := admin.breakers.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{Namespace: e2eNamespace, Site: "brk"}))
			require.NoError(t, err)
			for _, b := range list.Msg.GetBreakers() {
				if b.GetEndpointGroup() == "api" && b.GetState() == "open" {
					groupID = b.GetEndpointGroupId()
					return true
				}
			}
			return false
		})
		_, err = node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Site: "brk", Client: "web", Uri: "/api/items"}))
		_ = requireConnectError(t, err, connect.CodeUnavailable, apperr.ReasonCircuitOpen)
		stream.waitEvent(t, "breaker.transition", 10*time.Second)

		_, err = admin.breakers.CloseBreaker(ctx, connect.NewRequest(&spinneretv1.CloseBreakerRequest{EndpointGroupId: groupID, Reason: "e2e"}))
		require.NoError(t, err)
		reopened := acquire(t, node, "brk", "/api/items")
		release(t, node, reopened.GetLeaseId())
	})

	t.Run("f_config_center", func(t *testing.T) {
		_, err := admin.secrets.CreateSecret(ctx, connect.NewRequest(&spinneretv1.CreateSecretRequest{Namespace: e2eNamespace, Path: "signer/key", Value: "s3cr3t-value"}))
		require.NoError(t, err)
		item, err := admin.configs.CreateConfigItem(ctx, connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
			Namespace: e2eNamespace, Group: "crawler", Key: "signer.json", Format: "json", Publish: true,
			Content: `{"key":"${secret:signer/key}","rev":1}`,
		}))
		require.NoError(t, err)
		require.EqualValues(t, 1, item.Msg.GetItem().GetCurrentVersion())

		got, err := node.configs.GetConfig(ctx, connect.NewRequest(&spinneretv1.GetConfigRequest{Group: "crawler", Key: "signer.json"}))
		require.NoError(t, err)
		require.True(t, got.Msg.GetItem().GetHasSecretRefs())
		require.JSONEq(t, `{"key":"s3cr3t-value","rev":1}`, got.Msg.GetItem().GetContent())

		type watchResult struct {
			resp *connect.Response[spinneretv1.WatchConfigResponse]
			err  error
			at   time.Time
		}
		watched := make(chan watchResult, 1)
		go func() {
			resp, err := node.configs.WatchConfig(ctx, connect.NewRequest(&spinneretv1.WatchConfigRequest{
				Items: []*spinneretv1.WatchItem{{Group: "crawler", Key: "signer.json", Version: 1}}, TimeoutMs: 30_000,
			}))
			watched <- watchResult{resp: resp, err: err, at: time.Now()}
		}()
		select {
		case r := <-watched:
			t.Fatalf("watch returned before any change: %v %v", r.resp, r.err)
		case <-time.After(700 * time.Millisecond):
		}
		_, err = admin.configs.SaveConfigDraft(ctx, connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{
			Id: item.Msg.GetItem().GetId(), Content: `{"key":"${secret:signer/key}","rev":2}`,
		}))
		require.NoError(t, err)
		published := time.Now()
		_, err = admin.configs.PublishConfig(ctx, connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: item.Msg.GetItem().GetId()}))
		require.NoError(t, err)
		select {
		case r := <-watched:
			require.NoError(t, r.err)
			require.Len(t, r.resp.Msg.GetItems(), 1)
			require.EqualValues(t, 2, r.resp.Msg.GetItems()[0].GetVersion())
			require.JSONEq(t, `{"key":"s3cr3t-value","rev":2}`, r.resp.Msg.GetItems()[0].GetContent())
			require.Less(t, r.at.Sub(published), 2*time.Second)
		case <-time.After(5 * time.Second):
			t.Fatal("watch did not return after publish")
		}
	})

	t.Run("g_node_secrets", func(t *testing.T) {
		sec, err := node.secrets.GetSecret(ctx, connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "signer/key"}))
		require.NoError(t, err)
		require.Equal(t, "s3cr3t-value", sec.Msg.GetValue())

		tok, err := admin.access.CreateToken(ctx, connect.NewRequest(&spinneretv1.CreateTokenRequest{
			Namespace: e2eNamespace, Name: "node-no-secrets", Scopes: []string{"lease:acquire"},
		}))
		require.NoError(t, err)
		limited := newNodeClient(env.baseURL, tok.Msg.GetPlaintext())
		_, err = limited.secrets.GetSecret(ctx, connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "signer/key"}))
		_ = requireConnectError(t, err, connect.CodePermissionDenied, apperr.ReasonScopeMissing)
	})

	t.Run("h_permission_isolation", func(t *testing.T) {
		const password = "Another-Long-Passw0rd"
		// A second tenant with its own owner.
		ten, err := admin.tenants.CreateTenant(ctx, connect.NewRequest(&spinneretv1.CreateTenantRequest{Name: "globex"}))
		require.NoError(t, err)
		otherTenant := ten.Msg.GetTenant().GetId()
		platform := newConsoleClient(t, env.baseURL)
		_, err = platform.auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: e2eAdminUser, Password: e2eAdminPassword}))
		require.NoError(t, err)
		platform.headers.set("X-Spinneret-Tenant", otherTenant)
		_, err = platform.tenants.CreateNamespace(ctx, connect.NewRequest(&spinneretv1.CreateNamespaceRequest{Name: e2eNamespace}))
		require.NoError(t, err)
		_, err = platform.access.CreateUser(ctx, connect.NewRequest(&spinneretv1.CreateUserRequest{Username: "globex-owner", Password: password, Role: "owner"}))
		require.NoError(t, err)

		outsider := newConsoleClient(t, env.baseURL)
		_, err = outsider.auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: "globex-owner", Password: password}))
		require.NoError(t, err)
		outsider.headers.set("X-Spinneret-Tenant", otherTenant)
		own, err := outsider.sites.ListSites(ctx, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: e2eNamespace}))
		require.NoError(t, err)
		require.Empty(t, own.Msg.GetSites(), "the other tenant's namespace has no sites")
		outsider.headers.set("X-Spinneret-Tenant", env.tenantID)
		_, err = outsider.sites.ListSites(ctx, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: e2eNamespace}))
		require.Error(t, err, "a user of another tenant cannot list this tenant's sites")
		require.Contains(t, []connect.Code{connect.CodePermissionDenied, connect.CodeNotFound}, connect.CodeOf(err))

		// A site-restricted operator in the first tenant.
		_, err = admin.access.CreateUser(ctx, connect.NewRequest(&spinneretv1.CreateUserRequest{
			Username: "shop-operator", Password: password, Role: "operator", Namespace: e2eNamespace, Sites: []string{"shop"},
		}))
		require.NoError(t, err)
		operator := newConsoleClient(t, env.baseURL)
		_, err = operator.auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{Username: "shop-operator", Password: password}))
		require.NoError(t, err)
		operator.headers.set("X-Spinneret-Tenant", env.tenantID)

		sites, err := operator.sites.ListSites(ctx, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: e2eNamespace}))
		require.NoError(t, err)
		require.Len(t, sites.Msg.GetSites(), 1)
		require.Equal(t, "shop", sites.Msg.GetSites()[0].GetName())

		all, err := admin.ids.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{Namespace: e2eNamespace, PageSize: 100}))
		require.NoError(t, err)
		require.Greater(t, len(all.Msg.GetIdentities()), 5, "the admin sees identities of every site")
		ids, err := operator.ids.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{Namespace: e2eNamespace, PageSize: 100}))
		require.NoError(t, err)
		require.Len(t, ids.Msg.GetIdentities(), 5)
		for _, identity := range ids.Msg.GetIdentities() {
			require.Equal(t, "shop", identity.GetSite())
		}
	})

	t.Run("i_observability", func(t *testing.T) {
		status, body := httpGet(t, env.baseURL+"/readyz")
		require.Equal(t, 200, status, body)
		status, body = httpGet(t, env.baseURL+"/metrics")
		require.Equal(t, 200, status)
		require.Contains(t, body, "spinneret_acquire_total")
		require.Contains(t, body, "spinneret_job_runs_total")

		waitFor(t, 10*time.Second, "identity.import audit entry", func() bool {
			logs, err := admin.access.ListAuditLogs(ctx, connect.NewRequest(&spinneretv1.ListAuditLogsRequest{Namespace: e2eNamespace, Action: "identity.import"}))
			require.NoError(t, err)
			return len(logs.Msg.GetLogs()) > 0
		})
	})

	t.Run("j_admin_service_smoke", func(t *testing.T) {
		// Every remaining admin service answers through the full interceptor chain.
		ns := e2eNamespace
		_, err := admin.tenants.ListNamespaces(ctx, connect.NewRequest(&spinneretv1.ListNamespacesRequest{}))
		require.NoError(t, err)
		pols, err := admin.policies.ListPolicies(ctx, connect.NewRequest(&spinneretv1.ListPoliciesRequest{Namespace: ns}))
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(pols.Msg.GetPolicies()), 5, "four defaults plus the e2e breaker")
		uri, err := admin.sites.TestURI(ctx, connect.NewRequest(&spinneretv1.TestURIRequest{Namespace: ns, Site: "shop", Client: "web", Uri: "/search/x"}))
		require.NoError(t, err)
		require.Equal(t, "search", uri.Msg.GetEndpointGroup())
		_, err = admin.proxies.ListProxies(ctx, connect.NewRequest(&spinneretv1.ListProxiesRequest{Namespace: ns}))
		require.NoError(t, err)
		kek, err := admin.secrets.GetKEKStatus(ctx, connect.NewRequest(&spinneretv1.GetKEKStatusRequest{}))
		require.NoError(t, err)
		require.Equal(t, "k1", kek.Msg.GetCurrentKekId())
		_, err = admin.notify.ListChannels(ctx, connect.NewRequest(&spinneretv1.ListChannelsRequest{Namespace: ns}))
		require.NoError(t, err)
		_, err = admin.notify.ListAlertEvents(ctx, connect.NewRequest(&spinneretv1.ListAlertEventsRequest{Namespace: ns}))
		require.NoError(t, err)
		_, err = admin.dash.GetOverview(ctx, connect.NewRequest(&spinneretv1.GetOverviewRequest{Namespace: ns}))
		require.NoError(t, err)
		_, err = admin.dash.GetNodeStats(ctx, connect.NewRequest(&spinneretv1.GetNodeStatsRequest{Namespace: ns}))
		require.NoError(t, err)
		_, err = admin.dash.ListRiskEvents(ctx, connect.NewRequest(&spinneretv1.ListRiskEventsRequest{Namespace: ns}))
		require.NoError(t, err)
		waitFor(t, 15*time.Second, "raw report events in ClickHouse", func() bool {
			events, err := admin.dash.QueryRequestEvents(ctx, connect.NewRequest(&spinneretv1.QueryRequestEventsRequest{Namespace: ns, Site: "shop"}))
			require.NoError(t, err)
			return len(events.Msg.GetEvents()) > 0
		})

		// Request validation errors carry a reason as well.
		_, err = node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Client: "web"}))
		_ = requireConnectError(t, err, connect.CodeInvalidArgument, apperr.ReasonInvalidArgument)
	})

	t.Run("k_graceful_shutdown_with_long_requests", func(t *testing.T) {
		stream := openEventStream(t, admin, env.tenantID, e2eNamespace)
		watchDone := make(chan error, 1)
		go func() {
			_, err := node.configs.WatchConfig(ctx, connect.NewRequest(&spinneretv1.WatchConfigRequest{
				Items: []*spinneretv1.WatchItem{{Group: "crawler", Key: "signer.json", Version: 2}}, TimeoutMs: 60_000,
			}))
			watchDone <- err
		}()
		time.Sleep(300 * time.Millisecond) // let the long poll reach the server

		took, err := env.stop()
		require.NoError(t, err)
		// The drain delay is min(5s, SPINNERET_SHUTDOWN_TIMEOUT/4) = 2s; the event
		// stream and the long poll are canceled right after it instead of
		// holding http.Server.Shutdown for its whole budget.
		require.Less(t, took, 4500*time.Millisecond)
		select {
		case <-stream.done:
		case <-time.After(5 * time.Second):
			t.Fatal("event stream still open after shutdown")
		}
		select {
		case err := <-watchDone:
			require.Error(t, err, "the long poll ends with the instance")
		case <-time.After(5 * time.Second):
			t.Fatal("config long poll still pending after shutdown")
		}

		// The writers flushed what the instance produced before it stopped.
		var outcomes, stateEvents int
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM outcome_stats_minutely`).Scan(&outcomes))
		require.Positive(t, outcomes, "report statistics were flushed")
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM state_events WHERE action = 'ban'`).Scan(&stateEvents))
		require.Positive(t, stateEvents)
		conn, err := chstore.Open(ctx, env.chURL)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		var reports uint64
		require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM "+chstore.ReportEventsTable).Scan(&reports))
		require.Positive(t, reports, "raw report events reached ClickHouse")
	})
}

// acquire leases an identity of site for uri and returns the lease.
func acquire(t *testing.T, node *nodeClient, site, uri string) *spinneretv1.Lease {
	t.Helper()
	resp, err := node.leases.Acquire(context.Background(), connect.NewRequest(&spinneretv1.AcquireRequest{Site: site, Client: "web", Uri: uri, WaitMs: 1000}))
	require.NoError(t, err)
	return resp.Msg.GetLease()
}

func release(t *testing.T, node *nodeClient, leaseID string) {
	t.Helper()
	_, err := node.leases.Release(context.Background(), connect.NewRequest(&spinneretv1.ReleaseRequest{LeaseId: leaseID}))
	require.NoError(t, err)
}

// sendReports reports n requests with the same outcome on one lease; the last
// report releases the lease.
func sendReports(t *testing.T, node *nodeClient, lease *spinneretv1.Lease, uri string, n int, status int32, markers []string) {
	t.Helper()
	now := time.Now()
	reports := make([]*spinneretv1.Report, n)
	for i := range n {
		reports[i] = &spinneretv1.Report{
			ReportId:   fmt.Sprintf("%s-%d-%d", lease.GetLeaseId()[4:20], now.UnixNano(), i),
			LeaseId:    lease.GetLeaseId(),
			Uri:        uri,
			Method:     "GET",
			HttpStatus: status,
			Markers:    markers,
			LatencyMs:  120,
			StartedAt:  timestamppb.New(now.Add(-150 * time.Millisecond)),
			FinishedAt: timestamppb.New(now.Add(-30 * time.Millisecond)),
			Release:    i == n-1,
		}
	}
	resp, err := node.reports.Report(context.Background(), connect.NewRequest(&spinneretv1.ReportRequest{Reports: reports}))
	require.NoError(t, err)
	require.Empty(t, resp.Msg.GetRejected())
	require.EqualValues(t, n, resp.Msg.GetAccepted())
}

// waitEndpointCooldown waits until the identity is cooling down on the endpoint group.
func waitEndpointCooldown(t *testing.T, admin *consoleClient, identityID, group string) {
	t.Helper()
	waitFor(t, 10*time.Second, "endpoint cooldown of "+identityID, func() bool {
		hs, err := admin.ids.GetIdentityHotState(context.Background(), connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: identityID}))
		require.NoError(t, err)
		for _, g := range hs.Msg.GetHotState().GetGroups() {
			if g.GetEndpointGroup() == group && g.GetCooldownUntil().AsTime().After(time.Now()) {
				return true
			}
		}
		return false
	})
}

// waitFor polls cond every 100ms until it holds or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// createCookieSite creates a site with one prefix-matched endpoint group, the
// cookie identity type and n imported identities.
func createCookieSite(t *testing.T, c *consoleClient, site, group, prefix, sessionPrefix string, n int) {
	t.Helper()
	ctx := context.Background()
	_, err := c.sites.CreateSite(ctx, connect.NewRequest(&spinneretv1.CreateSiteRequest{Namespace: e2eNamespace, Name: site, Clients: []string{"web"}}))
	require.NoError(t, err)
	eg, err := c.sites.CreateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{
		Namespace: e2eNamespace, Site: site, Client: "web", Name: group,
		Rules: []*spinneretv1.URIRule{{Kind: "prefix", Pattern: prefix}},
	}))
	require.NoError(t, err)
	require.Equal(t, group, eg.Msg.GetEndpointGroup().GetName())
	_, err = c.ids.CreateIdentityType(ctx, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{Namespace: e2eNamespace, Site: site, SpecYaml: cookieTypeYAML}))
	require.NoError(t, err)
	imp, err := c.ids.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: e2eNamespace, Site: site, Type: "web_cookie", Format: "jsonl", Data: identityRows(sessionPrefix, n),
	}))
	require.NoError(t, err)
	require.Empty(t, imp.Msg.GetFailed())
	require.EqualValues(t, n, imp.Msg.GetCreated())
}

// requireKnownCookie asserts that a rendered credential belongs to one of the n imported identities.
func requireKnownCookie(t *testing.T, sessionPrefix string, n int, cookie, userAgent string) {
	t.Helper()
	for i := range n {
		if cookie == expectedCookieHeader(sessionPrefix, i) {
			require.Equal(t, fmt.Sprintf("UA-%s%d", sessionPrefix, i), userAgent)
			return
		}
	}
	t.Fatalf("unexpected cookie header %q", cookie)
}

func leaseIDs(leases []*spinneretv1.AcquireResponse) []string {
	out := make([]string, len(leases))
	for i, l := range leases {
		out[i] = l.GetLease().GetLeaseId()
	}
	return out
}
