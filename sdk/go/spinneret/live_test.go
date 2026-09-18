//go:build live

// Live tests run the SDK against a real Spinneret deployment (for example the
// Docker Compose stack) and the mocktarget site:
//
//	SPINNERET_LIVE_ADMIN_PASSWORD=... go test -tags live -race -count=1 -run TestLive ./sdk/go/spinneret
//
// Variables: SPINNERET_LIVE_URL (default http://localhost:8080),
// SPINNERET_LIVE_ADMIN_USER (admin), SPINNERET_LIVE_TENANT (default),
// SPINNERET_LIVE_TARGET (mocktarget, default http://localhost:19090) and
// SPINNERET_LIVE_GRPC_URL (gRPC address, default SPINNERET_LIVE_URL).
package spinneret_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1/spinneretv1connect"
	"github.com/Evil0ctal/Spinneret/sdk/go/spinneret"
)

func liveEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

type setHeaders struct {
	mu      sync.Mutex
	headers http.Header
}

func (h *setHeaders) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		h.mu.Lock()
		for k, v := range h.headers {
			req.Header()[k] = v
		}
		h.mu.Unlock()
		return next(ctx, req)
	}
}

func (h *setHeaders) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (h *setHeaders) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type liveFixture struct {
	baseURL   string
	target    string
	namespace string
	site      string
	token     string
	configID  string
	admin     liveAdmin
	configs   spinneretv1connect.ConfigAdminServiceClient
}

// liveAdmin holds the console clients used to create and remove fixtures.
type liveAdmin struct {
	tenants spinneretv1connect.TenantAdminServiceClient
	sites   spinneretv1connect.SiteAdminServiceClient
	access  spinneretv1connect.AccessAdminServiceClient
	secrets spinneretv1connect.SecretAdminServiceClient
	configs spinneretv1connect.ConfigAdminServiceClient
}

// deleteNamespace removes a namespace created by this test and everything in it:
// a namespace is deletable only once its sites, config items, secrets and
// usable tokens are gone.
func (a liveAdmin) deleteNamespace(ctx context.Context, id, name string) error {
	tokens, err := a.access.ListTokens(ctx, connect.NewRequest(&spinneretv1.ListTokensRequest{Namespace: name, PageSize: 500}))
	if err != nil {
		return err
	}
	for _, tok := range tokens.Msg.GetTokens() {
		if _, err := a.access.RevokeToken(ctx, connect.NewRequest(&spinneretv1.RevokeTokenRequest{Id: tok.GetId()})); err != nil {
			return err
		}
	}
	sites, err := a.sites.ListSites(ctx, connect.NewRequest(&spinneretv1.ListSitesRequest{Namespace: name, PageSize: 500}))
	if err != nil {
		return err
	}
	for _, site := range sites.Msg.GetSites() {
		if _, err := a.sites.DeleteSite(ctx, connect.NewRequest(&spinneretv1.DeleteSiteRequest{Id: site.GetId(), Force: true})); err != nil {
			return err
		}
	}
	items, err := a.configs.ListConfigItems(ctx, connect.NewRequest(&spinneretv1.ListConfigItemsRequest{Namespace: name, PageSize: 500}))
	if err != nil {
		return err
	}
	for _, item := range items.Msg.GetItems() {
		if strings.HasPrefix(item.GetGroup(), "_") {
			continue
		}
		if _, err := a.configs.DeleteConfigItem(ctx, connect.NewRequest(&spinneretv1.DeleteConfigItemRequest{Id: item.GetId()})); err != nil {
			return err
		}
	}
	secrets, err := a.secrets.ListSecrets(ctx, connect.NewRequest(&spinneretv1.ListSecretsRequest{Namespace: name, PageSize: 500}))
	if err != nil {
		return err
	}
	for _, sec := range secrets.Msg.GetSecrets() {
		if _, err := a.secrets.DeleteSecret(ctx, connect.NewRequest(&spinneretv1.DeleteSecretRequest{Id: sec.GetId()})); err != nil {
			return err
		}
	}
	_, err = a.tenants.DeleteNamespace(ctx, connect.NewRequest(&spinneretv1.DeleteNamespaceRequest{Id: id}))
	return err
}

func setupLive(ctx context.Context, t *testing.T) *liveFixture {
	t.Helper()
	password := os.Getenv("SPINNERET_LIVE_ADMIN_PASSWORD")
	if password == "" {
		t.Skip("SPINNERET_LIVE_ADMIN_PASSWORD is not set")
	}
	baseURL := strings.TrimRight(liveEnv("SPINNERET_LIVE_URL", "http://localhost:8080"), "/")
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	headers := &setHeaders{headers: http.Header{"X-Spinneret-Csrf": {"1"}}}
	opts := []connect.ClientOption{connect.WithProtoJSON(), connect.WithInterceptors(headers)}
	auth := spinneretv1connect.NewAuthServiceClient(hc, baseURL, opts...)
	tenants := spinneretv1connect.NewTenantAdminServiceClient(hc, baseURL, opts...)
	sites := spinneretv1connect.NewSiteAdminServiceClient(hc, baseURL, opts...)
	ids := spinneretv1connect.NewIdentityAdminServiceClient(hc, baseURL, opts...)
	access := spinneretv1connect.NewAccessAdminServiceClient(hc, baseURL, opts...)
	secrets := spinneretv1connect.NewSecretAdminServiceClient(hc, baseURL, opts...)

	login, err := auth.Login(ctx, connect.NewRequest(&spinneretv1.LoginRequest{
		Username: liveEnv("SPINNERET_LIVE_ADMIN_USER", "admin"), Password: password,
	}))
	require.NoError(t, err, "admin login")
	tenantName := liveEnv("SPINNERET_LIVE_TENANT", "default")
	for _, ta := range login.Msg.GetTenants() {
		if ta.GetTenant().GetName() == tenantName {
			headers.mu.Lock()
			headers.headers.Set("X-Spinneret-Tenant", ta.GetTenant().GetId())
			headers.mu.Unlock()
		}
	}

	admin := liveAdmin{
		tenants: tenants,
		sites:   sites,
		access:  access,
		secrets: secrets,
		configs: spinneretv1connect.NewConfigAdminServiceClient(hc, baseURL, opts...),
	}
	// Remove namespaces left behind by interrupted runs of this test.
	if list, err := tenants.ListNamespaces(ctx, connect.NewRequest(&spinneretv1.ListNamespacesRequest{PageSize: 500})); err == nil {
		for _, ns := range list.Msg.GetNamespaces() {
			if strings.HasPrefix(ns.GetName(), "gosdk-") {
				if err := admin.deleteNamespace(ctx, ns.GetId(), ns.GetName()); err != nil {
					t.Logf("remove stale namespace %s: %v", ns.GetName(), err)
				}
			}
		}
	}

	var suffix [3]byte
	_, _ = rand.Read(suffix[:])
	run := time.Now().UTC().Format("0102150405") + hex.EncodeToString(suffix[:])
	f := &liveFixture{
		baseURL:   baseURL,
		target:    strings.TrimRight(liveEnv("SPINNERET_LIVE_TARGET", "http://localhost:19090"), "/"),
		namespace: "gosdk-" + run,
		site:      "gosdk-" + run,
		admin:     admin,
		configs:   admin.configs,
	}
	ns, err := tenants.CreateNamespace(ctx, connect.NewRequest(&spinneretv1.CreateNamespaceRequest{
		Name: f.namespace, DisplayName: "Go SDK live " + run,
	}))
	require.NoError(t, err, "CreateNamespace")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := admin.deleteNamespace(cleanupCtx, ns.Msg.GetNamespace().GetId(), f.namespace); err != nil {
			t.Errorf("delete namespace %s: %v", f.namespace, err)
		}
	})

	_, err = sites.CreateSite(ctx, connect.NewRequest(&spinneretv1.CreateSiteRequest{
		Namespace: f.namespace, Name: f.site, Clients: []string{"app"},
	}))
	require.NoError(t, err, "CreateSite")
	_, err = sites.CreateEndpointGroup(ctx, connect.NewRequest(&spinneretv1.CreateEndpointGroupRequest{
		Namespace: f.namespace, Site: f.site, Client: "app", Name: "search",
		Rules: []*spinneretv1.URIRule{{Kind: "prefix", Pattern: "/site/search"}},
	}))
	require.NoError(t, err, "CreateEndpointGroup")
	_, err = ids.CreateIdentityType(ctx, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{
		Namespace: f.namespace, Site: f.site, SpecYaml: `name: app_device
client: app
fields:
  device_id:  { type: string, required: true }
  install_id: { type: string, required: true }
unique_by: [device_id]
activation: immediate
deliver:
  query:
    device_id: "{{ device_id }}"
    iid: "{{ install_id }}"
`,
	}))
	require.NoError(t, err, "CreateIdentityType")
	imp, err := ids.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: f.namespace, Site: f.site, Type: "app_device", Format: "csv",
		Data: "device_id,install_id\ngo-dev-1-" + run + ",i1\ngo-dev-2-" + run + ",i2\ngo-dev-3-" + run + ",i3\n",
	}))
	require.NoError(t, err, "ImportIdentities")
	require.EqualValues(t, 3, imp.Msg.GetCreated())

	_, err = secrets.CreateSecret(ctx, connect.NewRequest(&spinneretv1.CreateSecretRequest{
		Namespace: f.namespace, Path: "gosdk/api_key", Value: "live-secret-" + run,
	}))
	require.NoError(t, err, "CreateSecret")
	item, err := f.configs.CreateConfigItem(ctx, connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
		Namespace: f.namespace, Group: "crawler", Key: "gosdk.json", Format: "json",
		Content: `{"requests":1}`, Publish: true,
	}))
	require.NoError(t, err, "CreateConfigItem")
	f.configID = item.Msg.GetItem().GetId()

	tok, err := access.CreateToken(ctx, connect.NewRequest(&spinneretv1.CreateTokenRequest{
		Namespace: f.namespace, Name: "gosdk-node",
		Scopes: []string{"lease:acquire", "report:write", "config:read", "secret:read:" + f.namespace + "/gosdk/*"},
	}))
	require.NoError(t, err, "CreateToken")
	f.token = tok.Msg.GetPlaintext()
	return f
}

func (f *liveFixture) publish(ctx context.Context, t *testing.T, content string) {
	t.Helper()
	_, err := f.configs.SaveConfigDraft(ctx, connect.NewRequest(&spinneretv1.SaveConfigDraftRequest{Id: f.configID, Content: content}))
	require.NoError(t, err, "SaveConfigDraft")
	_, err = f.configs.PublishConfig(ctx, connect.NewRequest(&spinneretv1.PublishConfigRequest{Id: f.configID}))
	require.NoError(t, err, "PublishConfig")
}

func TestLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := setupLive(ctx, t)

	client, err := spinneret.New(spinneret.Options{BaseURL: f.baseURL, Token: f.token, Node: "gosdk-live"})
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close(context.Background())) }()

	// The catalog and hot state of the new site propagate asynchronously.
	var lease *spinneret.Lease
	deadline := time.Now().Add(60 * time.Second)
	for {
		lease, err = client.Lease(ctx, &spinneret.AcquireRequest{Site: f.site, Client: "app", Uri: "/site/search?q=go", WaitMs: 1000})
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, err, "acquire on the new site")
	require.Equal(t, "search", lease.Info().GetEndpointGroup())
	deviceID := lease.Credential().GetQuery()["device_id"]
	require.NotEmpty(t, deviceID)

	t.Run("lease request report release", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.target+"/site/search?q=go", nil)
		require.NoError(t, err)
		lease.Apply(req)
		started := time.Now()
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		var payload struct {
			Identity string `json:"identity"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Equal(t, deviceID, payload.Identity, "the target saw the leased credential")
		require.NoError(t, lease.ReportResponse(resp, spinneret.ReportInput{StartedAt: started, ResponseBytes: int64(len(body))}))
		require.NoError(t, lease.ReportResponse(resp, spinneret.ReportInput{StartedAt: started, Markers: []string{"empty_list"}}))
		require.NoError(t, lease.Close(ctx))
		require.NoError(t, client.Reporter().Flush(ctx))
		stats := client.Reporter().Stats()
		require.Equal(t, int64(2), stats.Accepted, "stats: %+v", stats)
		require.Zero(t, stats.Rejected)

		// Nothing reported: Close releases with LeaseService/Release.
		second, err := client.Lease(ctx, &spinneret.AcquireRequest{Site: f.site, Client: "app", EndpointGroup: "search"})
		require.NoError(t, err)
		require.NoError(t, second.Close(ctx))
		_, err = client.Renew(ctx, &spinneret.RenewRequest{LeaseId: second.ID()})
		require.True(t, spinneret.IsLeaseGone(err), "renewing a released lease: %v", err)

		batch, err := client.LeaseBatch(ctx, &spinneret.AcquireBatchRequest{Site: f.site, Client: "app", EndpointGroup: "search", Count: 2})
		require.NoError(t, err)
		require.NotEmpty(t, batch)
		for _, l := range batch {
			require.NoError(t, l.Close(ctx))
		}
	})

	t.Run("typed errors", func(t *testing.T) {
		_, err := client.Acquire(ctx, &spinneret.AcquireRequest{Site: "no-such-site", Client: "app"})
		require.Equal(t, connect.CodeInvalidArgument, spinneret.CodeOf(err), "%v", err)
		require.Equal(t, spinneret.ReasonSiteUnknown, spinneret.ReasonOf(err))
		e, ok := spinneret.AsError(err)
		require.True(t, ok)
		require.True(t, e.FromServer())

		resp, err := client.Report(ctx, &spinneret.ReportRequest{Reports: []*spinneret.Report{{
			ReportId: spinneret.NewReportID(), LeaseId: "lse_00000000000000000000000000000000_x_0", Uri: "/site/search",
		}}})
		require.NoError(t, err)
		require.Len(t, resp.GetRejected(), 1)

		bad, err := spinneret.New(spinneret.Options{BaseURL: f.baseURL, Token: "spn_invalid", Node: "gosdk-live"})
		require.NoError(t, err)
		defer func() { _ = bad.Close(ctx) }()
		_, err = bad.GetConfig(ctx, &spinneret.GetConfigRequest{Group: "crawler", Key: "gosdk.json"})
		require.True(t, spinneret.IsUnauthenticated(err), "%v", err)
	})

	t.Run("config and secrets", func(t *testing.T) {
		got, err := client.GetConfig(ctx, &spinneret.GetConfigRequest{Group: "crawler", Key: "gosdk.json"})
		require.NoError(t, err)
		require.JSONEq(t, `{"requests":1}`, got.GetItem().GetContent())

		secret, err := client.GetSecret(ctx, &spinneret.GetSecretRequest{Path: "gosdk/api_key"})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(secret.GetValue(), "live-secret-"))

		dir := t.TempDir()
		watcher, err := client.NewConfigWatcher(spinneret.WatcherOptions{
			Items:       []spinneret.ConfigKey{{Group: "crawler", Key: "gosdk.json"}},
			Timeout:     5 * time.Second,
			SnapshotDir: dir,
		})
		require.NoError(t, err)
		require.NoError(t, watcher.Start(ctx))
		defer watcher.Stop()
		item, ok := watcher.Get("crawler", "gosdk.json")
		require.True(t, ok)
		version := item.GetVersion()

		waitCtx, waitCancel := context.WithTimeout(ctx, 30*time.Second)
		defer waitCancel()
		changed := make(chan *spinneret.ConfigItem, 1)
		go func() {
			it, err := watcher.WaitForChange(waitCtx, "crawler", "gosdk.json")
			if err == nil {
				changed <- it
			}
			close(changed)
		}()
		time.Sleep(200 * time.Millisecond)
		f.publish(ctx, t, `{"requests":2}`)
		it, ok := <-changed
		require.True(t, ok, "change not observed")
		require.Greater(t, it.GetVersion(), version)
		require.JSONEq(t, `{"requests":2}`, it.GetContent())
	})

	t.Run("grpc", func(t *testing.T) {
		gc, err := spinneret.New(spinneret.Options{BaseURL: liveEnv("SPINNERET_LIVE_GRPC_URL", f.baseURL), Token: f.token, UseGRPC: true})
		require.NoError(t, err)
		defer func() { require.NoError(t, gc.Close(ctx)) }()
		l, err := gc.Lease(ctx, &spinneret.AcquireRequest{Site: f.site, Client: "app", EndpointGroup: "search"})
		require.NoError(t, err)
		require.NotEmpty(t, l.Credential().GetQuery()["device_id"])
		require.NoError(t, l.Report(spinneret.ReportInput{URI: "/site/search", HTTPStatus: 200, Latency: 12 * time.Millisecond}))
		require.NoError(t, l.Close(ctx))
		require.NoError(t, gc.Reporter().Flush(ctx))
		require.Equal(t, int64(1), gc.Reporter().Stats().Accepted)
		got, err := gc.GetConfig(ctx, &spinneret.GetConfigRequest{Group: "crawler", Key: "gosdk.json"})
		require.NoError(t, err)
		require.NotEmpty(t, got.GetItem().GetContent())
		_, err = gc.Acquire(ctx, &spinneret.AcquireRequest{Site: "no-such-site", Client: "app"})
		require.Equal(t, spinneret.ReasonSiteUnknown, spinneret.ReasonOf(err), "%v", err)
	})
}

// TestLiveProtocols checks error metadata over both protocols without creating fixtures.
func TestLiveProtocols(t *testing.T) {
	if os.Getenv("SPINNERET_LIVE_ADMIN_PASSWORD") == "" {
		t.Skip("SPINNERET_LIVE_ADMIN_PASSWORD is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	baseURL := strings.TrimRight(liveEnv("SPINNERET_LIVE_URL", "http://localhost:8080"), "/")
	for _, useGRPC := range []bool{false, true} {
		client, err := spinneret.New(spinneret.Options{BaseURL: baseURL, Token: "spn_not_a_real_token", UseGRPC: useGRPC})
		require.NoError(t, err)
		_, err = client.GetConfig(ctx, &spinneret.GetConfigRequest{Group: "crawler", Key: "x.json"})
		e, ok := spinneret.AsError(err)
		require.True(t, ok, "grpc=%v: %v", useGRPC, err)
		require.Equal(t, connect.CodeUnauthenticated, e.Code, "grpc=%v: %v", useGRPC, err)
		require.True(t, e.FromServer(), "grpc=%v", useGRPC)
		require.NotEmpty(t, e.Reason, "grpc=%v: reason from %s", useGRPC, spinneret.HeaderReason)
		t.Logf("grpc=%v: %v", useGRPC, err)
		require.NoError(t, client.Close(ctx))
	}
}
