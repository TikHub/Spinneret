package vault_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/testutil"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

func TestSystemKeyCreateAndReload(t *testing.T) {
	pool := testutil.Postgres(t)
	c := vaulttest.NewCipher(t)
	ctx := context.Background()

	key, err := vault.SystemKey(ctx, pool, c, "dedupe_pepper", 32)
	require.NoError(t, err)
	require.Len(t, key, 32)

	again, err := vault.SystemKey(ctx, pool, c, "dedupe_pepper", 32)
	require.NoError(t, err)
	require.Equal(t, key, again)

	other, err := vault.SystemKey(ctx, pool, c, "session_secret", 16)
	require.NoError(t, err)
	require.Len(t, other, 16)
	require.NotEqual(t, key[:16], other)

	var kekID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT kek_id FROM system_keys WHERE name = 'dedupe_pepper'`).Scan(&kekID))
	require.Equal(t, vaulttest.DefaultKEKID, kekID)

	// A different size for an existing key is a configuration error.
	_, err = vault.SystemKey(ctx, pool, c, "dedupe_pepper", 16)
	require.ErrorContains(t, err, "expected 16")

	// A cipher without the KEK cannot open the key.
	_, err = vault.SystemKey(ctx, pool, vaulttest.NewCipher(t), "dedupe_pepper", 32)
	require.ErrorIs(t, err, vault.ErrDecrypt)
}

func TestSystemKeyConcurrentCreation(t *testing.T) {
	pool := testutil.Postgres(t)
	c := vaulttest.NewCipher(t)
	const workers = 16

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		keys  = make([][]byte, workers)
		errs  = make([]error, workers)
	)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			keys[i], errs[i] = vault.SystemKey(context.Background(), pool, c, "dedupe_pepper", 32)
		}()
	}
	close(start)
	wg.Wait()
	for i := range workers {
		require.NoError(t, errs[i])
		require.Equal(t, keys[0], keys[i], "worker %d got a different key", i)
	}
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM system_keys`).Scan(&n))
	require.Equal(t, 1, n)
}

func TestSystemKeyValidation(t *testing.T) {
	pool := testutil.Postgres(t)
	c := vaulttest.NewCipher(t)
	ctx := context.Background()
	tests := []struct {
		name    string
		keyName string
		size    int
	}{
		{"empty name", "", 32},
		{"space in name", "bad name", 32},
		{"long name", strings.Repeat("n", vault.MaxSystemKeyNameLength+1), 32},
		{"zero size", "k", 0},
		{"negative size", "k", -1},
		{"huge size", "k", vault.MaxSystemKeySize + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := vault.SystemKey(ctx, pool, c, tt.keyName, tt.size)
			require.Error(t, err)
		})
	}
	_, err := vault.SystemKey(ctx, nil, c, "k", 32)
	require.Error(t, err)
	_, err = vault.SystemKey(ctx, pool, nil, "k", 32)
	require.Error(t, err)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = vault.SystemKey(canceled, pool, c, "k", 32)
	require.Error(t, err)
}
