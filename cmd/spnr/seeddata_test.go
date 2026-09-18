package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

func defaultSeedOptions() seedOptions {
	return seedOptions{
		Tenant: "default", Namespace: "default", Site: "loadtest", Client: "web",
		Groups: 50, Identities: 100_000, ProxyURL: "http://lt-{i}:pw@mocktarget:9091", TokenName: "loadtest", ChunkSize: seedDefaultChunk,
	}
}

func TestSeedOptionsValidate(t *testing.T) {
	require.NoError(t, defaultSeedOptions().validate())

	for name, mutate := range map[string]func(*seedOptions){
		"empty site":            func(o *seedOptions) { o.Site = "" },
		"space in token name":   func(o *seedOptions) { o.TokenName = "load test" },
		"negative groups":       func(o *seedOptions) { o.Groups = -1 },
		"too many groups":       func(o *seedOptions) { o.Groups = seedMaxGroups + 1 },
		"too many identities":   func(o *seedOptions) { o.Identities = seedMaxIdentities + 1 },
		"proxy template":        func(o *seedOptions) { o.Proxies, o.ProxyURL = 5, "http://fixed:9091" },
		"proxy url missing":     func(o *seedOptions) { o.Proxies, o.ProxyURL = 1, "" },
		"chunk size zero":       func(o *seedOptions) { o.ChunkSize = 0 },
		"too many proxies":      func(o *seedOptions) { o.Proxies = seedMaxProxies + 1 },
		"slash in namespace":    func(o *seedOptions) { o.Namespace = "a/b" },
		"blank client":          func(o *seedOptions) { o.Client = " " },
		"chunk size above max":  func(o *seedOptions) { o.ChunkSize = seedMaxChunk + 1 },
		"negative identities":   func(o *seedOptions) { o.Identities = -5 },
		"negative proxies":      func(o *seedOptions) { o.Proxies = -1 },
		"empty tenant":          func(o *seedOptions) { o.Tenant = "" },
		"newline in token name": func(o *seedOptions) { o.TokenName = "a\nb" },
	} {
		o := defaultSeedOptions()
		mutate(&o)
		require.Errorf(t, o.validate(), name)
	}
	o := defaultSeedOptions()
	o.Proxies, o.ProxyURL = 1, "http://fixed:9091"
	require.NoError(t, o.validate(), "a single proxy needs no index placeholder")
}

// TestSeedTokenNameDefaultsToSite is the regression test for `spnr seed --site b`
// revoking the node token of site a: the token name is namespace-scoped, so a
// fixed default collides between sites.
func TestSeedTokenNameDefaultsToSite(t *testing.T) {
	o := defaultSeedOptions()
	o.Site, o.TokenName = "other-site", ""
	o.applyDefaults()
	require.Equal(t, "other-site", o.TokenName)
	require.NoError(t, o.validate())

	o.TokenName = " "
	o.applyDefaults()
	require.Equal(t, "other-site", o.TokenName)

	explicit := defaultSeedOptions()
	explicit.Site, explicit.TokenName = "other-site", "chosen"
	explicit.applyDefaults()
	require.Equal(t, "chosen", explicit.TokenName, "an explicit --token-name wins")
}

func TestSeedChunks(t *testing.T) {
	require.Nil(t, seedChunks(0, 5000))
	require.Nil(t, seedChunks(10, 0))
	require.Equal(t, [][2]int{{0, 3}}, seedChunks(3, 5000))
	chunks := seedChunks(100_000, 5000)
	require.Len(t, chunks, 20)
	require.Equal(t, [2]int{95_000, 100_000}, chunks[19])
	require.Equal(t, [][2]int{{0, 4}, {4, 8}, {8, 10}}, seedChunks(10, 4))
}

func TestSeedIdentityRowsImportWithTheSeedType(t *testing.T) {
	spec, err := identity.ParseTypeYAML([]byte(seedIdentityTypeYAML("loadtest", "web")))
	require.NoError(t, err)
	require.NoError(t, spec.Validate())
	compiled, err := identity.Compile("ity_1", "sit_1", 1, spec)
	require.NoError(t, err)

	data := seedIdentityRows(10, 20)
	require.Equal(t, data, seedIdentityRows(10, 20), "rows are deterministic")
	rows, rowErrs, err := identity.ParseImport("jsonl", strings.NewReader(data), identity.ImportLimits{MaxRows: 50_000, MaxBytes: 32 << 20})
	require.NoError(t, err)
	require.Empty(t, rowErrs)
	require.Len(t, rows, 10)

	seen := map[string]bool{}
	for _, row := range rows {
		normalized, err := compiled.Normalize(row.Payload)
		require.NoError(t, err)
		key, err := compiled.UniqueKey(normalized)
		require.NoError(t, err)
		require.False(t, seen[string(key)], "unique keys differ between rows")
		seen[string(key)] = true
		cred, _, err := compiled.Render(context.Background(), normalized, nil)
		require.NoError(t, err)
		require.Contains(t, cred.CookieHeader, "sessionid=lt000000")
		require.True(t, strings.HasPrefix(cred.Headers["User-Agent"], "spinneret-loadtest/"))
	}
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(data, "\n", 2)[0]), &first))
	require.Equal(t, "lt00000010", first["cookies"].(map[string]any)["sessionid"])
}

func TestSeedPoliciesAndConfigAreValid(t *testing.T) {
	for _, withProxies := range []bool{false, true} {
		spec, err := policy.ParseYAML(policy.KindRotation, []byte(seedRotationYAML(withProxies)))
		require.NoError(t, err)
		require.Equal(t, seedRotationPolicy, spec.PolicyName())
		rot := spec.(*policy.RotationSpec)
		mode := "none"
		if withProxies {
			mode = "pool"
		}
		require.Equal(t, mode, rot.Proxy.Mode)
	}
	spec, err := policy.ParseYAML(policy.KindBreaker, []byte(seedBreakerYAML()))
	require.NoError(t, err)
	require.Equal(t, seedBreakerMinCount, spec.(*policy.BreakerSpec).MinRequests)

	var cfg map[string]any
	require.NoError(t, json.Unmarshal([]byte(seedConfigContent(defaultSeedOptions())), &cfg))
	require.Equal(t, "loadtest", cfg["site"])
	require.EqualValues(t, 50, cfg["groups"])
}

func TestSeedGroupsMatchLoadTestURIs(t *testing.T) {
	var rules []site.Rule
	for i := range 12 {
		rules = append(rules, site.Rule{ID: seedGroupName(i), GroupID: seedGroupName(i), GroupName: seedGroupName(i), Kind: site.RulePrefix, Pattern: seedGroupPrefix(i), Position: i})
	}
	m, err := site.NewMatcher(rules, "default")
	require.NoError(t, err)
	for _, i := range []int{0, 1, 10, 11} {
		// test/load/lib.js requests /api/g<i>/items.
		got := m.Match("/api/g" + strings.TrimPrefix(seedGroupName(i), "g") + "/items")
		require.Equal(t, seedGroupName(i), got.GroupName)
	}
}

func TestSeedProxyLines(t *testing.T) {
	require.Equal(t, "http://u0:p@h:1\nhttp://u1:p@h:1\n", seedProxyLines("http://u{i}:p@h:1", 2))
	require.Empty(t, seedProxyLines("x", 0))

	var buf bytes.Buffer
	require.NoError(t, json.NewEncoder(&buf).Encode(seedResult{Token: "spn_x", Site: "s", Groups: 1, Identities: 2}))
	require.JSONEq(t, `{"token":"spn_x","site":"s","groups":1,"identities":2}`, buf.String())
}
