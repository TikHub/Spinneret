package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/events"
)

// proxyExists reports whether a proxy row exists.
func (e *testEnv) proxyExists(t *testing.T, id string) bool {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), `SELECT count(*) FROM proxies WHERE id = $1`, id).Scan(&n))
	return n == 1
}

func TestDeleteProxies(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	proxies := env.importLines(t, env.ns, "http://10.7.0.1:80", "http://10.7.0.2:80", "http://10.7.0.3:80")
	sideProxy := env.importLines(t, env.other, "http://10.7.1.1:80")[0]
	_, err := env.pool.Exec(ctx, `INSERT INTO sites (id, namespace_id, name) VALUES ('sit_alpha', 'ns_main', 'alpha');
		INSERT INTO identity_types (id, site_id, client, name) VALUES ('ity_1', 'sit_alpha', 'web', 't');
		INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ('idt_1', 'sit_alpha', 'web', 'ity_1', '\x01', '\x01')`)
	require.NoError(t, err)
	_, err = env.pool.Exec(ctx, `INSERT INTO proxy_bindings (identity_id, proxy_id) VALUES ('idt_1', $1)`, proxies[0].ID)
	require.NoError(t, err)
	env.hot.reset()

	t.Run("permissions and validation", func(t *testing.T) {
		_, err := env.svc.DeleteProxies(ctx, viewerUser(), []string{proxies[0].ID})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.DeleteProxies(ctx, siteOperator(env.ns.ID, env.alpha.ID), []string{proxies[0].ID})
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.svc.DeleteProxies(ctx, tokenPrincipal(t, "lease:acquire"), []string{proxies[0].ID})
		requireReason(t, err, apperr.ReasonScopeMissing)
		_, err = env.svc.DeleteProxies(ctx, nil, []string{proxies[0].ID})
		requireReason(t, err, apperr.ReasonSessionInvalid)
		_, err = env.svc.DeleteProxies(ctx, adminUser(), nil)
		requireReason(t, err, apperr.ReasonInvalidArgument)

		res, err := env.svc.DeleteProxies(ctx, foreignUser(), []string{proxies[0].ID})
		require.NoError(t, err)
		require.Zero(t, res.Succeeded)
		require.Equal(t, []BulkFailure{{ID: proxies[0].ID, Reason: "not_found", Message: "proxy not found"}}, res.Failed)

		res, err = env.svc.DeleteProxies(ctx, tokenPrincipal(t, "proxy:write"), []string{sideProxy.ID})
		require.NoError(t, err)
		require.Zero(t, res.Succeeded, "tokens cannot delete proxies of other namespaces")
		require.True(t, env.proxyExists(t, proxies[0].ID))
		require.True(t, env.proxyExists(t, sideProxy.ID))
		require.Empty(t, env.hot.removedIDs())
	})

	t.Run("hot state is cleaned while the rows still exist", func(t *testing.T) {
		env.hot.reset()
		var existedDuringRemoval []bool
		env.hot.setOnRemove(func(_ string, ids []string) {
			for _, id := range ids {
				existedDuringRemoval = append(existedDuringRemoval, env.proxyExists(t, id))
			}
		})
		res, err := env.svc.DeleteProxies(ctx, adminUser(), []string{proxies[0].ID, "pxy_gone"})
		require.NoError(t, err)
		require.Equal(t, 1, res.Matched)
		require.Equal(t, 1, res.Succeeded)
		require.Equal(t, []BulkFailure{{ID: "pxy_gone", Reason: "not_found", Message: "proxy not found"}}, res.Failed)
		require.Equal(t, []bool{true}, existedDuringRemoval, "RemoveProxies runs before the rows are deleted")
		require.False(t, env.proxyExists(t, proxies[0].ID))
		var n int
		require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM proxy_bindings`).Scan(&n))
		require.Zero(t, n, "bindings cascade")
		require.Equal(t, []string{proxies[0].ID}, env.hot.removedIDs())
		require.Empty(t, env.hot.syncedIDs())
		require.Contains(t, env.audit.actions(), "proxy.delete")

		var data StateEventData
		found := false
		for _, ev := range env.events.snapshot() {
			if ev.Type != events.TypeProxyState {
				continue
			}
			require.NoError(t, json.Unmarshal(ev.Data, &data))
			if data.Action == "delete" {
				found = true
				require.Equal(t, env.ns.ID, ev.NamespaceID)
				require.Equal(t, StateEventData{SubjectKind: "proxy", SubjectID: proxies[0].ID, From: StateActive, Action: "delete"}, data)
			}
		}
		require.True(t, found)
	})

	t.Run("hot state failure deletes nothing and a retry succeeds", func(t *testing.T) {
		env.hot.reset()
		env.hot.removeErr = errors.New("redis down")
		res, err := env.svc.DeleteProxies(ctx, adminUser(), []string{proxies[1].ID})
		requireReason(t, err, apperr.ReasonInternal)
		require.Zero(t, res.Succeeded)
		require.True(t, env.proxyExists(t, proxies[1].ID), "the deletion is rolled back")
		require.Equal(t, []string{proxies[1].ID}, env.hot.syncedIDs(), "the hot state is restored")

		env.hot.reset()
		res, err = env.svc.DeleteProxies(ctx, adminUser(), []string{proxies[1].ID})
		require.NoError(t, err)
		require.Equal(t, 1, res.Succeeded)
		require.False(t, env.proxyExists(t, proxies[1].ID))
		require.Equal(t, []string{proxies[1].ID}, env.hot.removedIDs())
	})

	t.Run("failure of one namespace reports its proxies as failed items", func(t *testing.T) {
		env.hot.reset()
		env.hot.removeErr = errors.New("redis down")
		env.hot.failNamespace = env.other.ID
		res, err := env.svc.DeleteProxies(ctx, adminUser(), []string{proxies[2].ID, sideProxy.ID})
		require.NoError(t, err)
		require.Equal(t, 2, res.Matched)
		require.Equal(t, 1, res.Succeeded)
		require.Equal(t, []BulkFailure{{ID: sideProxy.ID, Reason: "internal", Message: "delete failed; retry the request"}}, res.Failed)
		require.False(t, env.proxyExists(t, proxies[2].ID))
		require.True(t, env.proxyExists(t, sideProxy.ID))
	})

	t.Run("canceled context", func(t *testing.T) {
		env.hot.reset()
		cctx, cancel := context.WithCancel(ctx)
		env.hot.setOnRemove(func(string, []string) { cancel() })
		_, err := env.svc.DeleteProxies(cctx, adminUser(), []string{sideProxy.ID})
		require.ErrorIs(t, err, context.Canceled)
		require.True(t, env.proxyExists(t, sideProxy.ID))
	})
}
