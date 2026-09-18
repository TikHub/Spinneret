package main

import (
	"errors"
	"fmt"
	"strings"
)

// Seed data names and limits.
const (
	seedIdentityType    = "loadtest_cookie"
	seedRotationPolicy  = "loadtest-rotation"
	seedBreakerPolicy   = "loadtest-breaker"
	seedConfigGroup     = "crawler"
	seedConfigKey       = "loadtest.json"
	seedMaxGroups       = 1000
	seedMaxIdentities   = 5_000_000
	seedMaxProxies      = 100_000
	seedDefaultChunk    = 5000
	seedMaxChunk        = 50_000
	proxyIndexToken     = "{i}"
	seedBreakerMinCount = 1_000_000_000
)

// seedScopes are the scopes of the load-test node token.
var seedScopes = []string{"lease:acquire", "report:write", "config:read"}

// seedOptions are the flags of the seed command.
type seedOptions struct {
	Tenant, Namespace, Site, Client string
	Groups, Identities, Proxies     int
	ProxyURL                        string
	TokenName                       string
	ChunkSize                       int
}

// applyDefaults fills in options derived from other flags. The node token is
// named after the site: token names are unique per namespace, so a fixed
// default would make `spnr seed --site b` revoke the token of site a.
func (o *seedOptions) applyDefaults() {
	if strings.TrimSpace(o.TokenName) == "" {
		o.TokenName = o.Site
	}
}

// validate checks the seed options.
func (o seedOptions) validate() error {
	var errs []error
	// Name syntax is validated by the services; only reject obviously unusable values here.
	for _, f := range []struct{ flag, value string }{
		{"--tenant", o.Tenant}, {"--namespace", o.Namespace}, {"--site", o.Site}, {"--client", o.Client}, {"--token-name", o.TokenName},
	} {
		if strings.TrimSpace(f.value) == "" || strings.ContainsAny(f.value, " \t\r\n/") {
			errs = append(errs, fmt.Errorf("%s %q must be a non-empty name without spaces or slashes", f.flag, f.value))
		}
	}
	if o.Groups < 0 || o.Groups > seedMaxGroups {
		errs = append(errs, fmt.Errorf("--groups must be between 0 and %d", seedMaxGroups))
	}
	if o.Identities < 0 || o.Identities > seedMaxIdentities {
		errs = append(errs, fmt.Errorf("--identities must be between 0 and %d", seedMaxIdentities))
	}
	if o.Proxies < 0 || o.Proxies > seedMaxProxies {
		errs = append(errs, fmt.Errorf("--proxies must be between 0 and %d", seedMaxProxies))
	}
	if o.Proxies > 1 && !strings.Contains(o.ProxyURL, proxyIndexToken) {
		errs = append(errs, fmt.Errorf("--proxy-url must contain %s when --proxies > 1 (proxies are deduplicated by URL)", proxyIndexToken))
	}
	if o.Proxies > 0 && o.ProxyURL == "" {
		errs = append(errs, errors.New("--proxy-url is required when --proxies > 0"))
	}
	if o.ChunkSize < 1 || o.ChunkSize > seedMaxChunk {
		errs = append(errs, fmt.Errorf("--chunk-size must be between 1 and %d", seedMaxChunk))
	}
	return errors.Join(errs...)
}

// seedGroupName is the name of load-test endpoint group i.
func seedGroupName(i int) string { return fmt.Sprintf("g%d", i) }

// seedGroupPrefix is the URI prefix rule of endpoint group i (k6 requests /api/g<i>/items).
func seedGroupPrefix(i int) string { return fmt.Sprintf("/api/g%d/", i) }

// seedIdentityTypeYAML is the identity type of the load-test site.
func seedIdentityTypeYAML(site, client string) string {
	return `name: ` + seedIdentityType + `
site: ` + site + `
client: ` + client + `
description: Synthetic cookie identities for load tests (spnr seed).
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
}

// seedIdentityRows renders identities [start, end) as JSON Lines. Rows are
// deterministic, so re-running the seed finds them unchanged.
func seedIdentityRows(start, end int) string {
	var b strings.Builder
	b.Grow((end - start) * 96)
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, `{"cookies":{"sessionid":"lt%08d","csrftoken":"%08x"},"user_agent":"spinneret-loadtest/%d"}`+"\n", i, uint32(i)*2654435761, i)
	}
	return b.String()
}

// seedChunks splits [0, total) into consecutive ranges of at most size.
func seedChunks(total, size int) [][2]int {
	if total <= 0 || size <= 0 {
		return nil
	}
	out := make([][2]int, 0, (total+size-1)/size)
	for start := 0; start < total; start += size {
		out = append(out, [2]int{start, min(start+size, total)})
	}
	return out
}

// seedRotationYAML is the rotation policy bound to the load-test site.
func seedRotationYAML(withProxies bool) string {
	mode := "none"
	if withProxies {
		mode = "pool"
	}
	return `name: ` + seedRotationPolicy + `
description: Rotation policy of the load-test site (spnr seed).
identity_types: [` + seedIdentityType + `]
rotation:
  strategy: weighted_random
  candidate_sample: 32
  lease_ttl: 60s
  max_concurrent_leases: 1
  reuse_interval: 0s
proxy:
  mode: ` + mode + `
`
}

// seedBreakerYAML is a breaker policy that never trips under load tests.
func seedBreakerYAML() string {
	return fmt.Sprintf(`name: %s
description: Relaxed breaker of the load-test site so load tests never open it (spnr seed).
min_requests: %d
`, seedBreakerPolicy, seedBreakerMinCount)
}

// seedConfigContent is the published load-test config item.
func seedConfigContent(o seedOptions) string {
	return fmt.Sprintf(`{"site":%q,"client":%q,"groups":%d,"seeded_by":"spnr seed"}`, o.Site, o.Client, o.Groups)
}

// seedProxyLines renders n proxy URLs from the template ({i} = index).
func seedProxyLines(template string, n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(strings.ReplaceAll(template, proxyIndexToken, fmt.Sprint(i)))
		b.WriteByte('\n')
	}
	return b.String()
}

// seedResult is printed as JSON when the seed finished.
type seedResult struct {
	Token      string `json:"token"`
	Site       string `json:"site"`
	Groups     int    `json:"groups"`
	Identities int    `json:"identities"`
	Proxies    int    `json:"proxies,omitempty"`
}

// seedDisplayName derives a human-readable site title from the site name, so
// that seeding several sites does not create duplicate display names (the
// console lists sites by display name).
func seedDisplayName(site string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' {
			return ' '
		}
		return r
	}, site)
	fields := strings.Fields(cleaned)
	for i, f := range fields {
		fields[i] = strings.ToUpper(f[:1]) + f[1:]
	}
	if len(fields) == 0 {
		return site
	}
	return strings.Join(fields, " ") + " (seed)"
}
