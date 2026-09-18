//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

// scenarioConfigSecretRuntime (h): a config item referencing a vault secret is delivered resolved to the
// node; WatchConfig on _runtime/site_switches wakes up when the site is paused; paused sites refuse
// leases with site_paused until resumed.
func scenarioConfigSecretRuntime(ctx context.Context, t *testing.T, f *fixture) {
	c := f.console
	secretValue := "sk-" + randomHex(t, 12)
	sec, err := c.secrets.CreateSecret(ctx, connect.NewRequest(&spinneretv1.CreateSecretRequest{
		Namespace: f.namespace, Path: secretPath, Value: secretValue, Description: "e2e signing key",
	}))
	require.NoError(t, err, "CreateSecret")
	require.NotContains(t, sec.Msg.GetSecret().GetMaskedValue(), secretValue)
	f.secretID = sec.Msg.GetSecret().GetId()

	content := `{"api_key":"${secret:` + secretPath + `}","page_size":20}`
	item, err := c.configs.CreateConfigItem(ctx, connect.NewRequest(&spinneretv1.CreateConfigItemRequest{
		Namespace: f.namespace, Group: configGroup, Key: configKey, Format: "json",
		Content: content, Publish: true, Comment: "e2e",
	}))
	require.NoError(t, err, "CreateConfigItem")
	require.EqualValues(t, 1, item.Msg.GetItem().GetCurrentVersion())
	f.configItemID = item.Msg.GetItem().GetId()

	// Node reads: resolved content, flagged as containing secret references.
	var got *spinneretv1.ConfigItem
	eventually(t, 15*time.Second, 200*time.Millisecond, "node GetConfig", func() (bool, string) {
		res, err := f.node.configs.GetConfig(ctx, connect.NewRequest(&spinneretv1.GetConfigRequest{Group: configGroup, Key: configKey}))
		if err != nil {
			return false, err.Error()
		}
		got = res.Msg.GetItem()
		return true, ""
	})
	require.EqualValues(t, 1, got.GetVersion())
	require.True(t, got.GetHasSecretRefs())
	require.Equal(t, f.namespace, got.GetNamespace())
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(got.GetContent()), &doc))
	require.Equal(t, secretValue, doc["api_key"])
	require.NotContains(t, got.GetContent(), "${secret:")

	sv, err := f.node.secrets.GetSecret(ctx, connect.NewRequest(&spinneretv1.GetSecretRequest{Path: secretPath}))
	require.NoError(t, err, "node GetSecret")
	require.Equal(t, secretValue, sv.Msg.GetValue())
	// The token scope secret:read:<ns>/e2e/* does not cover other paths.
	_, err = f.node.secrets.GetSecret(ctx, connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "other/key"}))
	require.Error(t, err)
	ae, _ := asAPIError(err)
	require.Contains(t, []connect.Code{connect.CodePermissionDenied, connect.CodeNotFound}, ae.Code)

	// Runtime site switches: long poll with the current version.
	cur, err := f.node.configs.BatchGetConfig(ctx, connect.NewRequest(&spinneretv1.BatchGetConfigRequest{
		Items: []*spinneretv1.ConfigRef{{Group: "_runtime", Key: "site_switches"}},
	}))
	require.NoError(t, err, "BatchGetConfig _runtime/site_switches")
	var version int32
	if items := cur.Msg.GetItems(); len(items) == 1 {
		version = items[0].GetVersion()
		require.Contains(t, items[0].GetContent(), `"`+f.site+`"`)
	}
	type watchResult struct {
		items []*spinneretv1.ConfigItem
		err   error
		took  time.Duration
	}
	watched := make(chan watchResult, 1)
	watchStart := time.Now()
	go func() {
		res, err := f.node.configs.WatchConfig(ctx, connect.NewRequest(&spinneretv1.WatchConfigRequest{
			Items:     []*spinneretv1.WatchItem{{Group: "_runtime", Key: "site_switches", Version: version}},
			TimeoutMs: 30000,
		}))
		r := watchResult{err: err, took: time.Since(watchStart)}
		if err == nil {
			r.items = res.Msg.GetItems()
		}
		watched <- r
	}()
	// Nothing changed yet: the poll is still waiting.
	select {
	case r := <-watched:
		t.Fatalf("WatchConfig returned before any change: %+v", r)
	case <-time.After(1500 * time.Millisecond):
	}

	pausedAt := time.Now()
	paused, err := c.breakers.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{
		Namespace: f.namespace, Site: f.site, Paused: true, Reason: "e2e maintenance",
	}))
	require.NoError(t, err, "SetSitePaused(true)")
	require.True(t, paused.Msg.GetPaused())

	var r watchResult
	select {
	case r = <-watched:
	case <-time.After(15 * time.Second):
		t.Fatal("WatchConfig did not wake up within 15s after the site was paused")
	}
	require.NoError(t, r.err, "WatchConfig")
	require.Len(t, r.items, 1)
	t.Logf("WatchConfig woke %s after SetSitePaused (poll held %s)", time.Since(pausedAt).Round(time.Millisecond), r.took.Round(time.Millisecond))
	require.Greater(t, r.items[0].GetVersion(), version)
	var switches struct {
		Sites map[string]struct {
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		} `json:"sites"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.items[0].GetContent()), &switches))
	require.True(t, switches.Sites[f.site].Paused)
	require.Equal(t, "e2e maintenance", switches.Sites[f.site].Reason)

	eventually(t, 15*time.Second, 100*time.Millisecond, "acquire refused with site_paused", func() (bool, string) {
		// A burst of concurrent acquires reaches every replica: all of them must refuse.
		for _, err := range f.acquireBurst(ctx, "web", "/site/item/1", 0, burstSize, nil) {
			if err == nil {
				return false, "acquire succeeded"
			}
			ae, _ := asAPIError(err)
			if ae.Code != connect.CodeUnavailable || ae.Reason != "site_paused" {
				return false, err.Error()
			}
		}
		return true, ""
	})
	_, err = f.node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{Site: f.site, Client: "app", Uri: "/site/search"}))
	requireReason(t, err, connect.CodeUnavailable, "site_paused")

	_, err = c.breakers.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{
		Namespace: f.namespace, Site: f.site, Paused: false,
	}))
	require.NoError(t, err, "SetSitePaused(false)")
	eventually(t, 15*time.Second, 100*time.Millisecond, "acquire after resume on every replica", func() (bool, string) {
		for _, err := range f.acquireBurst(ctx, "web", "/site/item/1", 1000, burstSize, nil) {
			if err != nil {
				return false, err.Error()
			}
		}
		return true, ""
	})
	// The app client of the same site works too: its credential renders device parameters as query values.
	appErrs := f.acquireBurst(ctx, "app", "/site/item/1", 2000, appIdentities/2, func(res *spinneretv1.AcquireResponse) error {
		if !strings.HasPrefix(res.GetCredential().GetQuery()["device_id"], "d"+f.runID) {
			return fmt.Errorf("unexpected app credential query %v", res.GetCredential().GetQuery())
		}
		return nil
	})
	for _, err := range appErrs {
		require.NoError(t, err, "app lease after resume")
	}
	site, err := c.sites.GetSite(ctx, connect.NewRequest(&spinneretv1.GetSiteRequest{Id: f.siteID}))
	require.NoError(t, err, "GetSite")
	require.False(t, site.Msg.GetSite().GetPaused())
}
