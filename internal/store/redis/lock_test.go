package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLockArgs(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		owner string
		ttl   time.Duration
		ms    int64
		err   bool
	}{
		{"valid", "k", "o", 1500 * time.Millisecond, 1500, false},
		{"empty key", "", "o", time.Second, 0, true},
		{"empty owner", "k", "", time.Second, 0, true},
		{"zero ttl", "k", "o", 0, 0, true},
		{"sub-millisecond ttl", "k", "o", 900 * time.Microsecond, 0, true},
		{"negative ttl", "k", "o", -time.Second, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, err := lockArgs(tt.key, tt.owner, tt.ttl)
			if tt.err {
				require.ErrorIs(t, err, ErrInvalidLock)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.ms, ms)
		})
	}
}

func TestLocks(t *testing.T) {
	client, keys := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := keys.SiteLock(1, "reap")

	ok, err := TryLock(ctx, client, key, "a", 10*time.Second)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = TryLock(ctx, client, key, "b", 10*time.Second)
	require.NoError(t, err)
	require.False(t, ok, "lock is held by a")

	ok, err = RefreshLock(ctx, client, key, "b", 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok, "b does not own the lock")

	ok, err = RefreshLock(ctx, client, key, "a", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	pttl, err := client.Do(ctx, client.B().Pttl().Key(key).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, pttl, int64(10_000))

	require.NoError(t, Unlock(ctx, client, key, "b"))
	owner, err := client.Do(ctx, client.B().Get().Key(key).Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "a", owner, "unlock by a non-owner must not release the lock")

	require.NoError(t, Unlock(ctx, client, key, "a"))
	ok, err = TryLock(ctx, client, key, "b", 50*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)

	// Expiry releases the lock; refreshing a lost lock reports false.
	require.Eventually(t, func() bool {
		n, err := client.Do(ctx, client.B().Exists().Key(key).Build()).AsInt64()
		return err == nil && n == 0
	}, 2*time.Second, 20*time.Millisecond)
	ok, err = RefreshLock(ctx, client, key, "b", time.Second)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, Unlock(ctx, client, key, "b"))
}

func TestLockValidationAndErrors(t *testing.T) {
	client, keys := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := TryLock(ctx, client, "", "a", time.Second)
	require.ErrorIs(t, err, ErrInvalidLock)
	_, err = RefreshLock(ctx, client, keys.Lock("x"), "a", 0)
	require.ErrorIs(t, err, ErrInvalidLock)
	require.ErrorIs(t, Unlock(ctx, client, keys.Lock("x"), ""), ErrInvalidLock)

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	_, err = TryLock(canceled, client, keys.Lock("x"), "a", time.Second)
	require.Error(t, err)
	_, err = RefreshLock(canceled, client, keys.Lock("x"), "a", time.Second)
	require.Error(t, err)
	require.Error(t, Unlock(canceled, client, keys.Lock("x"), "a"))

	// A key of the wrong type makes the Lua comparison fail with WRONGTYPE.
	wrong := keys.Lock("wrongtype")
	require.NoError(t, client.Do(ctx, client.B().Hset().Key(wrong).FieldValue().FieldValue("f", "v").Build()).Error())
	_, err = RefreshLock(ctx, client, wrong, "a", time.Second)
	require.Error(t, err)
	require.Error(t, Unlock(ctx, client, wrong, "a"))
}
