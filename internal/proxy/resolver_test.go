package proxy

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

func TestResolver(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	proxies := env.importLines(t, env.ns,
		"http://bob:pa%3Ass@10.9.0.1:8000 kind=tunnel region=us session_template=user-{username}-session-{identity_hash}-{lease_id}",
		"socks5://10.9.0.2:1080 kind=residential region=de",
		"http://tok@10.9.0.3:8000 session_template={identity_id}/{random}",
		"http://user:pw@10.9.0.4:8000 session_template={lease_id}",
	)
	r := NewResolver(env.pool, env.cipher, env.logger)

	t.Run("template replaces the user name and keeps the password", func(t *testing.T) {
		a, err := r.Resolve(ctx, env.ns.ID, proxies[0].ID, "idt_42", "lse_7")
		require.NoError(t, err)
		require.Equal(t, proxies[0].ID, a.ID)
		require.Equal(t, KindTunnel, a.Kind)
		require.Equal(t, "us", a.Region)
		u, err := url.Parse(a.URL)
		require.NoError(t, err)
		require.Equal(t, "user-bob-session-"+IdentityHash("idt_42")+"-lse_7", u.User.Username())
		pass, ok := u.User.Password()
		require.True(t, ok)
		require.Equal(t, "pa:ss", pass)
		require.Equal(t, "10.9.0.1:8000", u.Host)
	})

	t.Run("no template returns the stored url", func(t *testing.T) {
		a, err := r.Resolve(ctx, env.ns.ID, proxies[1].ID, "idt_1", "lse_1")
		require.NoError(t, err)
		require.Equal(t, &Assignment{ID: proxies[1].ID, URL: "socks5://10.9.0.2:1080", Kind: KindResidential, Region: "de"}, a)
	})

	t.Run("rendered user info is escaped", func(t *testing.T) {
		a, err := r.Resolve(ctx, env.ns.ID, proxies[2].ID, "idt/1 x", "lse_1")
		require.NoError(t, err)
		u, err := url.Parse(a.URL)
		require.NoError(t, err)
		require.Regexp(t, `^idt/1 x/[0-9a-f]{8}$`, u.User.Username())
		_, hasPass := u.User.Password()
		require.False(t, hasPass)
	})

	t.Run("empty rendered user name keeps the stored url", func(t *testing.T) {
		a, err := r.Resolve(ctx, env.ns.ID, proxies[3].ID, "idt_1", "")
		require.NoError(t, err)
		require.Equal(t, "http://user:pw@10.9.0.4:8000", a.URL)
		a, err = r.Resolve(ctx, env.ns.ID, proxies[3].ID, "idt_1", "lse_9")
		require.NoError(t, err)
		require.Equal(t, "http://lse_9:pw@10.9.0.4:8000", a.URL)
	})

	t.Run("concurrent invalidation and resolves", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i%4 == 0 {
					r.Invalidate(proxies[1].ID)
					return
				}
				_, err := r.Resolve(ctx, env.ns.ID, proxies[1].ID, "idt_1", "lse_1")
				assert.NoError(t, err)
			}()
		}
		wg.Wait()
		// Leave the entry cached for the cache subtest below.
		_, err := r.Resolve(ctx, env.ns.ID, proxies[1].ID, "idt_1", "lse_1")
		require.NoError(t, err)
	})

	t.Run("namespace mismatch and missing proxy", func(t *testing.T) {
		_, err := r.Resolve(ctx, env.other.ID, proxies[0].ID, "idt_1", "lse_1")
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = r.Resolve(ctx, env.ns.ID, "pxy_missing", "idt_1", "lse_1")
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = r.Resolve(ctx, env.ns.ID, "", "idt_1", "lse_1")
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	t.Run("cache, invalidation and bus subscription", func(t *testing.T) {
		id := proxies[1].ID
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET region = 'fr' WHERE id = $1`, id)
		require.NoError(t, err)
		a, err := r.Resolve(ctx, env.ns.ID, id, "", "")
		require.NoError(t, err)
		require.Equal(t, "de", a.Region, "served from cache")

		r.Invalidate(id)
		a, err = r.Resolve(ctx, env.ns.ID, id, "", "")
		require.NoError(t, err)
		require.Equal(t, "fr", a.Region)

		unsubscribe := r.Subscribe(env.bus)
		defer unsubscribe()
		_, err = env.pool.Exec(ctx, `UPDATE proxies SET region = 'nl' WHERE id = $1`, id)
		require.NoError(t, err)
		require.NoError(t, env.bus.Publish(ctx, events.NamespaceChannel(env.ns.ID), events.Event{Type: events.TypeAlert, Data: json.RawMessage(`{}`)}))
		a, err = r.Resolve(ctx, env.ns.ID, id, "", "")
		require.NoError(t, err)
		require.Equal(t, "fr", a.Region, "other event types do not invalidate")

		data, err := json.Marshal(StateEventData{SubjectID: id, Action: "update"})
		require.NoError(t, err)
		require.NoError(t, env.bus.Publish(ctx, events.NamespaceChannel(env.ns.ID), events.Event{Type: events.TypeProxyState, Data: data}))
		a, err = r.Resolve(ctx, env.ns.ID, id, "", "")
		require.NoError(t, err)
		require.Equal(t, "nl", a.Region)
		require.NotNil(t, r.Subscribe(nil))
	})

	t.Run("service updates invalidate subscribed resolvers", func(t *testing.T) {
		unsubscribe := r.Subscribe(env.bus)
		defer unsubscribe()
		_, err := r.Resolve(ctx, env.ns.ID, proxies[0].ID, "idt_1", "lse_1")
		require.NoError(t, err)
		newURL := "http://carol:pw@10.9.0.1:8000"
		_, err = env.svc.UpdateProxy(ctx, adminUser(), proxies[0].ID, UpdateRequest{URL: &newURL})
		require.NoError(t, err)
		a, err := r.Resolve(ctx, env.ns.ID, proxies[0].ID, "idt_1", "lse_1")
		require.NoError(t, err)
		u, err := url.Parse(a.URL)
		require.NoError(t, err)
		require.Contains(t, u.User.Username(), "user-carol-session-")
	})

	t.Run("concurrent resolves share one load", func(t *testing.T) {
		fresh := NewResolver(env.pool, env.cipher, nil)
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a, err := fresh.Resolve(ctx, env.ns.ID, proxies[1].ID, "idt_1", "lse_1")
				if err == nil && a.ID != proxies[1].ID {
					err = apperr.Internal(nil)
				}
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		fresh := NewResolver(env.pool, env.cipher, nil)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := fresh.Resolve(cctx, env.ns.ID, proxies[1].ID, "idt_1", "lse_1")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("undecryptable url", func(t *testing.T) {
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET url_ciphertext = '\x00' WHERE id = $1`, proxies[2].ID)
		require.NoError(t, err)
		r.Invalidate(proxies[2].ID)
		_, err = r.Resolve(ctx, env.ns.ID, proxies[2].ID, "idt_1", "lse_1")
		requireReason(t, err, apperr.ReasonInternal)
	})
}
