package vault_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/vault"
)

func TestReadSecretPermissionMatrix(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.create(t, "signing/api_key", "signing-value")
	f.create(t, "vendor/api_key", "vendor-value")

	tests := []struct {
		name   string
		p      *authz.Principal
		path   string
		reason apperr.Reason // "" = allowed
	}{
		{"token namespace glob", tokenFor(t, f.ns, "secret:read:prod/*"), "signing/api_key", ""},
		{"token path glob", tokenFor(t, f.ns, "secret:read:prod/signing/*"), "signing/api_key", ""},
		{"token exact path", tokenFor(t, f.ns, "secret:read:prod/vendor/api_key"), "vendor/api_key", ""},
		{"token question glob", tokenFor(t, f.ns, "secret:read:prod/signing/api_ke?"), "signing/api_key", ""},
		{"token glob other path", tokenFor(t, f.ns, "secret:read:prod/signing/*"), "vendor/api_key", apperr.ReasonScopeMissing},
		{"token glob other namespace name", tokenFor(t, f.ns, "secret:read:staging/*"), "signing/api_key", apperr.ReasonScopeMissing},
		{"token bound to other namespace", tokenFor(t, f.staging, "secret:read:prod/*"), "signing/api_key", apperr.ReasonScopeMissing},
		{"token other tenant", tokenFor(t, f.other, "secret:read:prod/*"), "signing/api_key", apperr.ReasonScopeMissing},
		{"token without secret scope", tokenFor(t, f.ns, "lease:acquire", "config:read"), "signing/api_key", apperr.ReasonScopeMissing},
		{"admin token lacks node permission", tokenFor(t, f.ns, "admin"), "signing/api_key", apperr.ReasonScopeMissing},
		{"user admin reveal", f.admin(), "signing/api_key", ""},
		{"user viewer", f.viewer(), "signing/api_key", apperr.ReasonPermissionDenied},
		{"user viewer with reveal", userWith(f.tenant, authz.RoleViewer, "", authz.PermSecretReveal), "vendor/api_key", ""},
		{"user other tenant", f.outsider(), "signing/api_key", apperr.ReasonPermissionDenied},
		{"platform admin", platformAdmin(), "signing/api_key", ""},
		{"system", authz.System("configcenter"), "signing/api_key", ""},
		{"nil principal", nil, "signing/api_key", apperr.ReasonSessionInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := f.rec.count()
			v, err := f.store.ReadSecret(ctx, tt.p, f.ns, tt.path, 0, "config")
			if tt.reason != "" {
				requireReason(t, err, tt.reason)
				if tt.p == nil {
					require.Equal(t, before, f.rec.count(), "anonymous reads are not audited")
					return
				}
				e := f.rec.last(t)
				require.Equal(t, vault.AuditActionSecretRead, e.Action)
				require.Equal(t, audit.ResultDenied, e.Result)
				require.NotEmpty(t, e.ResourceID, "denied reads are linked to the secret")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.path, v.Path)
			require.Equal(t, 1, v.Version)
			require.Contains(t, v.Value, "-value")
			e := f.rec.last(t)
			require.Equal(t, vault.AuditActionSecretRead, e.Action)
			require.Equal(t, audit.ResultOK, e.Result)
			require.Equal(t, "config", e.Details["purpose"])
			require.Equal(t, 1, e.Details["version"])
			require.Equal(t, tt.path, e.ResourceName)
			require.Equal(t, f.ns.ID, e.NamespaceID)
			if tt.p.Kind == authz.KindToken {
				require.Equal(t, "192.0.2.10", e.IP)
			}
		})
	}
}

func TestReadSecretVersionsAndErrors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "app/key", "first-value")
	_, err := f.store.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{Value: ptr("second-value")})
	require.NoError(t, err)
	node := tokenFor(t, f.ns, "secret:read:prod/*")

	v, err := f.store.ReadSecret(ctx, node, f.ns, "app/key", 0, "api")
	require.NoError(t, err)
	require.Equal(t, 2, v.Version)
	require.Equal(t, "second-value", v.Value)
	v, err = f.store.ReadSecret(ctx, node, f.ns, "app/key", 1, "api")
	require.NoError(t, err)
	require.Equal(t, 1, v.Version)
	require.Equal(t, "first-value", v.Value)

	_, err = f.store.ReadSecret(ctx, node, f.ns, "app/key", 3, "api")
	requireReason(t, err, apperr.ReasonNotFound)
	e := f.rec.last(t)
	require.Equal(t, audit.ResultError, e.Result)
	require.Equal(t, s.ID, e.ResourceID)
	require.Equal(t, "not_found", e.Details["error"])

	_, err = f.store.ReadSecret(ctx, node, f.ns, "app/missing", 0, "api")
	requireReason(t, err, apperr.ReasonNotFound)
	require.Empty(t, f.rec.last(t).ResourceID)

	_, err = f.store.ReadSecret(ctx, node, f.ns, "../etc", 0, "api")
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.store.ReadSecret(ctx, node, f.ns, "app/key", -1, "api")
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.store.ReadSecret(ctx, node, nil, "app/key", 0, "api")
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Expired secrets are still readable.
	past := time.Now().Add(-time.Hour)
	_, err = f.store.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{ExpiresAt: &past})
	require.NoError(t, err)
	v, err = f.store.ReadSecret(ctx, node, f.ns, "app/key", 0, "")
	require.NoError(t, err)
	require.NotNil(t, v.ExpiresAt)
	require.Equal(t, "api", f.rec.last(t).Details["purpose"])

	// Data that cannot be decrypted is an internal error, audited as error.
	blind := vault.NewSecretStore(f.pool, vault.NewCipher(mustProvider(t, "test"), 0, 0), f.rec, nil)
	_, err = blind.ReadSecret(ctx, node, f.ns, "app/key", 0, "api")
	requireReason(t, err, apperr.ReasonInternal)
	require.Equal(t, audit.ResultError, f.rec.last(t).Result)
	_, err = blind.Reveal(ctx, f.admin(), s.ID, 0)
	requireReason(t, err, apperr.ReasonInternal)
	_, err = blind.ResolveForIdentity(ctx, f.ns.ID, "app/key")
	requireReason(t, err, apperr.ReasonInternal)
}

func TestResolveForIdentity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "shared/device_key", "value-one")

	var nowNanos atomic.Int64
	nowNanos.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, nowNanos.Load()) }
	vault.SetSecretStoreClock(f.store, clock)
	auditBefore := f.rec.count()

	got, err := f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/device_key")
	require.NoError(t, err)
	require.Equal(t, "value-one", got)

	// Another instance updates the value: this instance keeps serving its
	// cached value until the TTL expires.
	peer := vault.NewSecretStore(f.pool, f.cipher, f.rec, nil)
	_, err = peer.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{Value: ptr("value-two")})
	require.NoError(t, err)
	got, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/device_key")
	require.NoError(t, err)
	require.Equal(t, "value-one", got)

	nowNanos.Add(int64(31 * time.Second))
	got, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/device_key")
	require.NoError(t, err)
	require.Equal(t, "value-two", got)

	// Local updates invalidate the cache immediately.
	_, err = f.store.Update(ctx, f.admin(), s.ID, vault.UpdateSecretInput{Value: ptr("value-three")})
	require.NoError(t, err)
	got, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/device_key")
	require.NoError(t, err)
	require.Equal(t, "value-three", got)

	// Namespaces are isolated.
	_, err = f.store.ResolveForIdentity(ctx, f.staging.ID, "shared/device_key")
	requireReason(t, err, apperr.ReasonNotFound)

	// Local deletes invalidate as well.
	require.NoError(t, f.store.Delete(ctx, f.admin(), s.ID))
	_, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/device_key")
	requireReason(t, err, apperr.ReasonNotFound)

	_, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "Bad/Path")
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = f.store.ResolveForIdentity(ctx, "", "shared/device_key")
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Resolving never writes per-read audit entries (the updates and delete did).
	for _, e := range f.rec.entries[auditBefore:] {
		require.NotEqual(t, vault.AuditActionSecretRead, e.Action)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.store.ResolveForIdentity(canceled, f.ns.ID, "other/missing")
	require.Error(t, err)
}

func TestResolveForIdentityChangeDuringLoad(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rotated := f.create(t, "shared/rotated", "value-old")
	deleted := f.create(t, "shared/deleted", "value-gone")

	// The hook runs on the load goroutine, so errors are checked afterwards.
	var (
		mutate    func() error
		mutateErr error
		calls     atomic.Int32
	)
	vault.SetAfterResolveLoad(f.store, func() {
		if calls.Add(1) == 1 {
			mutateErr = mutate()
		}
	})

	// A local rotation commits after the load read the old value.
	mutate = func() error {
		_, err := f.store.Update(ctx, f.admin(), rotated.ID, vault.UpdateSecretInput{Value: ptr("value-new")})
		return err
	}
	got, err := f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/rotated")
	require.NoError(t, err)
	require.NoError(t, mutateErr)
	require.Equal(t, "value-old", got, "the in-flight read predates the rotation")
	got, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/rotated")
	require.NoError(t, err)
	require.Equal(t, "value-new", got, "a read that raced a local change must not be cached")

	// A local delete during a load: the deleted value is not served afterwards.
	calls.Store(0)
	mutate = func() error { return f.store.Delete(ctx, f.admin(), deleted.ID) }
	got, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/deleted")
	require.NoError(t, err)
	require.NoError(t, mutateErr)
	require.Equal(t, "value-gone", got)
	_, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "shared/deleted")
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestResolveForIdentityConcurrent(t *testing.T) {
	f := newFixture(t)
	f.create(t, "shared/key", "concurrent-value")
	const workers = 32
	var wg sync.WaitGroup
	errs := make([]error, workers)
	values := make([]string, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values[i], errs[i] = f.store.ResolveForIdentity(context.Background(), f.ns.ID, "shared/key")
		}()
	}
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
		require.Equal(t, "concurrent-value", values[i])
	}
}

func TestSecretAccessTimes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "app/key", "value")
	require.Nil(t, s.LastAccessedAt)
	node := tokenFor(t, f.ns, "secret:read:prod/*")

	_, err := f.store.ReadSecret(ctx, node, f.ns, "app/key", 0, "api")
	require.NoError(t, err)
	_, err = f.store.ResolveForIdentity(ctx, f.ns.ID, "app/key")
	require.NoError(t, err)
	require.Equal(t, 1, vault.PendingAccessCount(f.store))

	require.NoError(t, f.store.FlushAccessTimes(ctx))
	require.Zero(t, vault.PendingAccessCount(f.store))
	got, err := f.store.Get(ctx, f.admin(), s.ID)
	require.NoError(t, err)
	require.NotNil(t, got.LastAccessedAt)
	first := *got.LastAccessedAt
	require.NoError(t, f.store.FlushAccessTimes(ctx), "empty flush is a no-op")

	// Run flushes on shutdown; an older access never moves the time backwards.
	vault.SetSecretStoreClock(f.store, func() time.Time { return first.Add(-time.Hour) })
	_, err = f.store.Reveal(ctx, f.admin(), s.ID, 0)
	require.NoError(t, err)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.store.Run(runCtx) }()
	stop()
	require.NoError(t, <-done)
	require.Zero(t, vault.PendingAccessCount(f.store))
	got, err = f.store.Get(ctx, f.admin(), s.ID)
	require.NoError(t, err)
	require.True(t, first.Equal(*got.LastAccessedAt))

	// A failed flush keeps the entries.
	vault.SetSecretStoreClock(f.store, time.Now)
	_, err = f.store.Reveal(ctx, f.admin(), s.ID, 0)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(t, f.store.FlushAccessTimes(canceled))
	require.Equal(t, 1, vault.PendingAccessCount(f.store))

	// The buffer is bounded.
	vault.SetMaxPendingAccesses(f.store, 1)
	other := f.create(t, "app/other", "value")
	_, err = f.store.Reveal(ctx, f.admin(), other.ID, 0)
	require.NoError(t, err)
	require.Equal(t, 1, vault.PendingAccessCount(f.store))
	require.EqualValues(t, 1, vault.DroppedAccessCount(f.store))
}

func TestSecretAccessTimesPeriodicFlush(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s := f.create(t, "app/periodic", "value")
	vault.SetAccessFlushInterval(f.store, 10*time.Millisecond)

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.store.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		require.NoError(t, <-done)
	})

	_, err := f.store.ResolveForIdentity(ctx, f.ns.ID, "app/periodic")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := f.store.Get(ctx, f.admin(), s.ID)
		return err == nil && got.LastAccessedAt != nil
	}, 10*time.Second, 20*time.Millisecond, "Run persists access times without waiting for shutdown")
	require.Zero(t, vault.PendingAccessCount(f.store))
}

func mustProvider(t *testing.T, id string) vault.KEKProvider {
	t.Helper()
	key := make([]byte, vault.KEKSize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	p, err := vault.NewLocalKEKProvider(map[string][]byte{id: key}, id)
	require.NoError(t, err)
	return p
}
