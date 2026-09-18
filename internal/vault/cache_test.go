package vault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheKey(t *testing.T) {
	wrapped := []byte("wrapped-dek-bytes")
	require.Equal(t, cacheKey("k1", wrapped), cacheKey("k1", wrapped))
	require.NotEqual(t, cacheKey("k1", wrapped), cacheKey("k2", wrapped))
	require.NotEqual(t, cacheKey("k1", wrapped), cacheKey("k1", []byte("wrapped-dek-byteZ")))
	// The separator prevents id/wrapped boundary ambiguity.
	require.NotEqual(t, cacheKey("k1", []byte("2x")), cacheKey("k12", []byte("x")))

	// Wrapped values larger than the stack buffer still hash correctly.
	large := make([]byte, 512)
	require.Equal(t, cacheKey("k1", large), cacheKey("k1", large))
}

func TestDEKCache(t *testing.T) {
	var disabled *dekCache
	require.Nil(t, newDEKCache(0, time.Minute))
	disabled.add(dekCacheKey{1}, nil)
	_, ok := disabled.get(dekCacheKey{1})
	require.False(t, ok)
	require.Zero(t, disabled.len())

	c := newDEKCache(2, -time.Second) // negative TTL means no expiry
	aead, err := newAEAD(make([]byte, DEKSize))
	require.NoError(t, err)
	c.add(dekCacheKey{1}, aead)
	c.add(dekCacheKey{2}, aead)
	got, ok := c.get(dekCacheKey{1})
	require.True(t, ok)
	require.Equal(t, aead, got)
	c.add(dekCacheKey{3}, aead) // evicts key 2 (least recently used)
	_, ok = c.get(dekCacheKey{2})
	require.False(t, ok)
	require.Equal(t, 2, c.len())
}

func TestEffectiveDEKCacheTTL(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
		want time.Duration
	}{
		{"negative disables expiry", -time.Second, 0},
		{"zero disables expiry", 0, 0},
		{"one nanosecond is raised", time.Nanosecond, minDEKCacheTTL},
		{"just below the minimum is raised", minDEKCacheTTL - 1, minDEKCacheTTL},
		{"minimum kept", minDEKCacheTTL, minDEKCacheTTL},
		{"default kept", 10 * time.Minute, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, effectiveDEKCacheTTL(tt.ttl))
		})
	}
}

// TestNewCipherTinyTTLDoesNotPanic guards against the LRU's purge ticker
// panicking (time.NewTicker with a non-positive interval) for TTLs < 100ns,
// which would crash the process from a background goroutine.
func TestNewCipherTinyTTLDoesNotPanic(t *testing.T) {
	p, err := NewLocalKEKProvider(map[string][]byte{"k1": make([]byte, KEKSize)}, "")
	require.NoError(t, err)
	for _, ttl := range []time.Duration{time.Nanosecond, 99 * time.Nanosecond} {
		c := NewCipher(p, 4, ttl)
		s, err := c.Seal([]byte("v"), nil)
		require.NoError(t, err)
		got, err := c.Open(s, nil)
		require.NoError(t, err)
		require.Equal(t, []byte("v"), got)
	}
	time.Sleep(20 * time.Millisecond) // give a panicking purge goroutine time to fire
}

func TestCipherOpenPopulatesCache(t *testing.T) {
	p, err := NewLocalKEKProvider(map[string][]byte{"k1": make([]byte, KEKSize)}, "")
	require.NoError(t, err)
	c := NewCipher(p, 10, 0)
	s, err := c.Seal([]byte("v"), nil)
	require.NoError(t, err)
	require.Zero(t, c.cache.len())
	_, err = c.Open(s, nil)
	require.NoError(t, err)
	require.Equal(t, 1, c.cache.len())
	_, ok := c.cache.get(cacheKey(s.KEKID, s.WrappedDEK))
	require.True(t, ok)
}

func TestNewAEADRejectsBadKey(t *testing.T) {
	_, err := newAEAD(make([]byte, 7))
	require.ErrorContains(t, err, "aes")
}

func TestDecodeKEKZeroLength(t *testing.T) {
	_, err := decodeKEK(nil)
	require.ErrorContains(t, err, "base64 of exactly 32 bytes")
}
