package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func TestImportProxies(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	data := "http://alice:s3cret@10.0.0.1:8080 kind=residential region=us tags=fast\n" +
		"socks5://10.0.0.2:1080 max_concurrency=4\n" +
		"http://alice:s3cret@10.0.0.1:8080/\n" +
		"ftp://10.0.0.3:21\n"
	req := ImportRequest{Format: FormatLines, Data: data, Defaults: Defaults{
		Provider: "acme", Tags: []string{"pool-a"}, MaxConcurrency: 2, SessionTemplate: "{username}-{identity_hash}",
	}}

	t.Run("dry run stores nothing", func(t *testing.T) {
		dry := req
		dry.DryRun = true
		res, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, dry)
		require.NoError(t, err)
		require.Equal(t, 2, res.Created)
		require.Len(t, res.Failed, 2)
		var n int
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM proxies`).Scan(&n))
		require.Zero(t, n)
		require.Empty(t, env.hot.syncedIDs())
	})

	res, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, req)
	require.NoError(t, err)
	require.Equal(t, 2, res.Created)
	require.Zero(t, res.Updated)
	require.Zero(t, res.Unchanged)
	require.Equal(t, []ImportFailure{
		{Line: 3, Message: "duplicate of line 1"},
		{Line: 4, Message: "proxy url scheme must be http, https or socks5"},
	}, res.Failed)

	httpProxy := env.proxyByDisplay(t, env.ns, "http://10.0.0.1:8080")
	require.Equal(t, "http", httpProxy.Scheme)
	require.Equal(t, "10.0.0.1", httpProxy.Host)
	require.Equal(t, 8080, httpProxy.Port)
	require.Equal(t, "alic***", httpProxy.UsernameHint)
	require.Equal(t, Attributes{
		Kind: KindResidential, Region: "us", Provider: "acme", MaxConcurrency: 2,
		Tags: []string{"fast", "pool-a"}, SessionTemplate: "{username}-{identity_hash}",
	}, httpProxy.Attributes)
	require.Equal(t, StateActive, httpProxy.State)
	require.Equal(t, 1, httpProxy.URLVersion)

	socks := env.proxyByDisplay(t, env.ns, "socks5://10.0.0.2:1080")
	require.Empty(t, socks.UsernameHint)
	require.Equal(t, KindDatacenter, socks.Attributes.Kind)
	require.Equal(t, 4, socks.Attributes.MaxConcurrency)
	require.Equal(t, []string{"pool-a"}, socks.Attributes.Tags)

	// The URL is sealed with AAD(proxyID, "url") and never stored in clear text.
	raw := env.rawRow(t, httpProxy.ID)
	require.NotContains(t, string(raw.UrlCiphertext), "s3cret")
	u, err := OpenURL(env.cipher, httpProxy.ID, sealedURL(raw))
	require.NoError(t, err)
	require.Equal(t, "http://alice:s3cret@10.0.0.1:8080", u.String())
	require.Equal(t, URLHash(env.pepper, u), raw.UrlHash)

	require.ElementsMatch(t, []string{httpProxy.ID, socks.ID}, env.hot.syncedIDs())
	require.Equal(t, []string{"proxy.import"}, env.audit.actions())

	t.Run("reimport is unchanged or updates only set attributes", func(t *testing.T) {
		env.hot.reset()
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET city = 'geo-city' WHERE id = $1`, httpProxy.ID)
		require.NoError(t, err)
		res, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, req)
		require.NoError(t, err)
		require.Equal(t, ImportResult{Unchanged: 2, Failed: res.Failed}, res)
		require.Empty(t, env.hot.syncedIDs())

		res, err = env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{
			Format: FormatJSONL,
			Data:   `{"url":"http://alice:s3cret@10.0.0.1:8080","kind":"mobile","tags":["slow"]}` + "\n" + `{"url":"http://10.9.9.9:3128"}`,
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.Updated)
		require.Equal(t, 1, res.Created)
		updated := env.row(t, httpProxy.ID)
		require.Equal(t, KindMobile, updated.Attributes.Kind)
		require.Equal(t, []string{"slow"}, updated.Attributes.Tags)
		require.Equal(t, "us", updated.Attributes.Region, "attributes not set by the row keep their value")
		require.Equal(t, "geo-city", updated.Attributes.City)
		require.Equal(t, "acme", updated.Attributes.Provider)
		require.Contains(t, env.hot.syncedIDs(), httpProxy.ID)
	})

	t.Run("csv import into another namespace does not dedupe across namespaces", func(t *testing.T) {
		res, err := env.svc.ImportProxies(ctx, adminUser(), env.other, ImportRequest{
			Format: FormatCSV, Data: "url,kind,tags\nhttp://alice:s3cret@10.0.0.1:8080,tunnel,x;y\n",
		})
		require.NoError(t, err)
		require.Equal(t, 1, res.Created)
		p := env.proxyByDisplay(t, env.other, "http://10.0.0.1:8080")
		require.NotEqual(t, httpProxy.ID, p.ID)
		require.Equal(t, []string{"x", "y"}, p.Attributes.Tags)
	})
}

func TestImportProxiesErrors(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	data := "http://10.1.0.1:80"

	tests := []struct {
		name   string
		p      func() error
		reason apperr.Reason
	}{
		{name: "viewer", reason: apperr.ReasonPermissionDenied, p: func() error {
			_, err := env.svc.ImportProxies(ctx, viewerUser(), env.ns, ImportRequest{Format: FormatLines, Data: data})
			return err
		}},
		{name: "site restricted operator", reason: apperr.ReasonPermissionDenied, p: func() error {
			_, err := env.svc.ImportProxies(ctx, siteOperator(env.ns.ID, env.alpha.ID), env.ns, ImportRequest{Format: FormatLines, Data: data})
			return err
		}},
		{name: "token without proxy scope", reason: apperr.ReasonScopeMissing, p: func() error {
			_, err := env.svc.ImportProxies(ctx, tokenPrincipal(t, "lease:acquire"), env.ns, ImportRequest{Format: FormatLines, Data: data})
			return err
		}},
		{name: "foreign tenant", reason: apperr.ReasonPermissionDenied, p: func() error {
			_, err := env.svc.ImportProxies(ctx, foreignUser(), env.ns, ImportRequest{Format: FormatLines, Data: data})
			return err
		}},
		{name: "nil namespace", reason: apperr.ReasonNotFound, p: func() error {
			_, err := env.svc.ImportProxies(ctx, adminUser(), nil, ImportRequest{Format: FormatLines, Data: data})
			return err
		}},
		{name: "bad default kind", reason: apperr.ReasonInvalidArgument, p: func() error {
			_, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: FormatLines, Data: data, Defaults: Defaults{Kind: "x"}})
			return err
		}},
		{name: "bad default concurrency", reason: apperr.ReasonInvalidArgument, p: func() error {
			_, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: FormatLines, Data: data, Defaults: Defaults{MaxConcurrency: -1}})
			return err
		}},
		{name: "bad default template", reason: apperr.ReasonInvalidArgument, p: func() error {
			_, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: FormatLines, Data: data, Defaults: Defaults{SessionTemplate: "{x}"}})
			return err
		}},
		{name: "bad format", reason: apperr.ReasonInvalidArgument, p: func() error {
			_, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: "xml", Data: data})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireReason(t, tt.p(), tt.reason)
		})
	}

	t.Run("token with proxy:write imports", func(t *testing.T) {
		res, err := env.svc.ImportProxies(ctx, tokenPrincipal(t, "proxy:write"), env.ns, ImportRequest{Format: FormatLines, Data: data})
		require.NoError(t, err)
		require.Equal(t, 1, res.Created)
	})

	t.Run("hot sync failure is reported after commit", func(t *testing.T) {
		env.hot.err = errors.New("redis down")
		defer env.hot.reset()
		res, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: FormatLines, Data: "http://10.1.0.2:80"})
		requireReason(t, err, apperr.ReasonInternal)
		require.Equal(t, 1, res.Created)
		env.proxyByDisplay(t, env.ns, "http://10.1.0.2:80")
	})
}
