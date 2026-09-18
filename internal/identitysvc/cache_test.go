package identitysvc_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

// countingSecrets is a SecretResolver that counts calls and can block.
type countingSecrets struct {
	calls atomic.Int64
	delay time.Duration
	err   error
}

func (s *countingSecrets) ResolveForIdentity(_ context.Context, namespaceID, path string) (string, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.err != nil {
		return "", s.err
	}
	return "secret-of-" + path + "-in-" + namespaceID, nil
}

type cacheFixture struct {
	env      *identitysvctest.Env
	ct       *identity.CompiledType
	plainID  string
	secretID string
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	env := identitysvctest.NewEnv(t)
	it := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	addSecret(t, env, "keys/api")
	res, err := env.Service.ImportIdentities(context.Background(), env.Role(authz.RoleAdmin), env.NS, identitysvc.ImportInput{
		Site: "shop", Type: "web_cookie", Format: "jsonl",
		Data: `{"cookies":"sessionid=plain","user_agent":"UA","_labels":{"k":"plain"}}
{"cookies":"sessionid=secret","api_key":"keys/api","_labels":{"k":"secret"}}`,
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Created, "failures: %v", res.Failed)
	return &cacheFixture{
		env:      env,
		ct:       env.SiteA.IdentityTypesByID[it.ID],
		plainID:  identityBySession(t, env, "plain"),
		secretID: identityBySession(t, env, "secret"),
	}
}

func TestPayloadCache(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	secrets := &countingSecrets{}
	cache := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, secrets, 0, true, nil)

	t.Run("miss then hit", func(t *testing.T) {
		cred, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
		require.NoError(t, err)
		require.Equal(t, "sessionid=plain", cred.CookieHeader)
		require.Equal(t, "UA", cred.Headers["User-Agent"])
		again, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
		require.NoError(t, err)
		require.Same(t, cred, again)
		st := cache.Stats()
		require.Equal(t, uint64(1), st.Hits)
		require.Equal(t, uint64(1), st.Misses)
		require.Equal(t, 1, st.Entries)
		plain, secret := cache.CacheSizes()
		require.Equal(t, 1, plain)
		require.Equal(t, 0, secret)
	})

	t.Run("secret entries live in the expiring cache", func(t *testing.T) {
		cred, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		require.NoError(t, err)
		require.Equal(t, "secret-of-keys/api-in-"+f.env.NS.ID, cred.Headers["X-Api-Key"])
		_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		require.NoError(t, err)
		require.Equal(t, int64(1), secrets.calls.Load())
		plain, secret := cache.CacheSizes()
		require.Equal(t, 1, plain)
		require.Equal(t, 1, secret)
	})

	t.Run("version mismatch is a miss", func(t *testing.T) {
		_, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 2)
		require.True(t, apperr.IsNotFound(err), "err: %v", err)
		bumped, err := identity.Compile(f.ct.ID, f.ct.SiteID, f.ct.Version+1, f.ct.Spec)
		require.NoError(t, err)
		before := cache.Stats().Misses
		_, err = cache.Credential(ctx, bumped, f.env.NS.ID, f.plainID, 1)
		require.NoError(t, err)
		require.Equal(t, before+1, cache.Stats().Misses)
	})

	t.Run("invalidate", func(t *testing.T) {
		cache.Invalidate(f.plainID)
		cache.Invalidate(f.secretID)
		plain, secret := cache.CacheSizes()
		require.Zero(t, plain)
		require.Zero(t, secret)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := cache.Credential(ctx, nil, f.env.NS.ID, f.plainID, 1)
		require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))
		_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 0)
		require.True(t, apperr.IsNotFound(err))
		_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, "idt_missing", 1)
		require.True(t, apperr.IsNotFound(err))

		failing := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, &countingSecrets{err: errors.New("vault sealed")}, 10, true, nil)
		_, err = failing.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))

		noSecrets := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, nil, 10, true, nil)
		_, err = noSecrets.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))

		wrongKey := identitysvc.NewPayloadCache(f.env.Pool, vaulttest.NewCipher(t), nil, 10, true, nil)
		_, err = wrongKey.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
		require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))

		unconfigured := identitysvc.NewPayloadCache(nil, nil, nil, 10, false, nil)
		_, err = unconfigured.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
		require.Equal(t, apperr.ReasonInternal, apperr.ReasonOf(err))
	})

	t.Run("canceled caller", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		fresh := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, secrets, 10, true, nil)
		_, err := fresh.Credential(cctx, f.ct, f.env.NS.ID, f.plainID, 1)
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
	})
}

func TestPayloadCacheSecretTTL(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	secrets := &countingSecrets{}
	cache := identitysvc.NewPayloadCacheWithTTL(f.env.Pool, f.env.Cipher, secrets, 10, true, time.Minute, nil)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	cache.SetCacheNow(func() time.Time { return time.Unix(0, clock.Load()) })

	_, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
	require.NoError(t, err)
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), secrets.calls.Load(), "served from the cache before the TTL")

	// Plain entries never expire.
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
	require.NoError(t, err)

	clock.Add(int64(time.Minute))
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), secrets.calls.Load(), "an expired secret entry is reloaded")
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), secrets.calls.Load(), "the reloaded entry is cached again")

	misses := cache.Stats().Misses
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
	require.NoError(t, err)
	require.Equal(t, misses, cache.Stats().Misses, "plain entries survive the secret TTL")
	plain, secret := cache.CacheSizes()
	require.Equal(t, 1, plain)
	require.Equal(t, 1, secret)
}

func TestPayloadCacheBounds(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	cache := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, &countingSecrets{}, 1, true, nil)
	_, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.plainID, 1)
	require.NoError(t, err)
	_, err = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
	require.NoError(t, err)
	require.Equal(t, 1, cache.Stats().Entries, "plain and secret entries share one size bound")

	// Caches do not start background goroutines.
	before := runtime.NumGoroutine()
	for range 50 {
		identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, nil, 10, true, nil)
	}
	require.Less(t, runtime.NumGoroutine(), before+25, "one goroutine per cache would add 50")
}

func TestPayloadCacheDisabled(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	secrets := &countingSecrets{}
	cache := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, secrets, 10, false, nil)
	for range 3 {
		cred, err := cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		require.NoError(t, err)
		require.NotEmpty(t, cred.Headers["X-Api-Key"])
	}
	require.Equal(t, int64(3), secrets.calls.Load())
	st := cache.Stats()
	require.Equal(t, uint64(0), st.Hits)
	require.Equal(t, uint64(3), st.Misses)
	require.Zero(t, st.Entries)
	cache.Invalidate(f.secretID)
}

func TestPayloadCacheSingleflight(t *testing.T) {
	f := newCacheFixture(t)
	ctx := context.Background()
	secrets := &countingSecrets{delay: 200 * time.Millisecond}
	cache := identitysvc.NewPayloadCache(f.env.Pool, f.env.Cipher, secrets, 10, true, nil)

	const workers = 32
	var wg sync.WaitGroup
	creds := make([]*identity.Credential, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			creds[i], errs[i] = cache.Credential(ctx, f.ct, f.env.NS.ID, f.secretID, 1)
		}()
	}
	close(start)
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
		require.Same(t, creds[0], creds[i])
	}
	require.Equal(t, int64(1), secrets.calls.Load())
}

func BenchmarkPayloadCacheHit(b *testing.B) {
	env := identitysvctest.NewEnv(b)
	it := env.CreateType(b, env.SiteA, identitysvctest.WebCookieYAML)
	_, err := env.Service.ImportIdentities(context.Background(), env.Owner(env.NS), env.NS, identitysvc.ImportInput{
		Site: "shop", Type: "web_cookie", Format: "jsonl", Data: `{"cookies":"sessionid=bench","user_agent":"UA"}`,
	})
	if err != nil {
		b.Fatal(err)
	}
	var id string
	if err := env.Pool.QueryRow(context.Background(), `SELECT id FROM identities LIMIT 1`).Scan(&id); err != nil {
		b.Fatal(err)
	}
	ct := env.SiteA.IdentityTypesByID[it.ID]
	cache := identitysvc.NewPayloadCache(env.Pool, env.Cipher, nil, 1000, true, nil)
	ctx := context.Background()
	if _, err := cache.Credential(ctx, ct, env.NS.ID, id, 1); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := cache.Credential(ctx, ct, env.NS.ID, id, 1); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
