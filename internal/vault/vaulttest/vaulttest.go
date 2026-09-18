// Package vaulttest provides vault fixtures for tests: ciphers and KEK
// providers backed by freshly generated random keys.
package vaulttest

import (
	"crypto/rand"
	"testing"

	"github.com/TikHub/Spinneret/internal/vault"
)

// DefaultKEKID is the KEK id used by NewCipher and by NewProvider when no ids
// are given.
const DefaultKEKID = "test"

// cacheSize is small enough to exercise LRU eviction in busy tests while
// still covering the cache-hit path. The TTL is zero so that no expiry
// goroutine is started per test.
const cacheSize = 1024

// NewCipher returns a cipher backed by a single random KEK with id
// DefaultKEKID and an in-memory DEK cache without expiry.
func NewCipher(t testing.TB) *vault.Cipher {
	t.Helper()
	return vault.NewCipher(NewProvider(t), cacheSize, 0)
}

// NewProvider returns a local KEK provider holding one random 32-byte key per
// id (DefaultKEKID when ids is empty). The last id is the current KEK.
func NewProvider(t testing.TB, ids ...string) *vault.LocalKEKProvider {
	t.Helper()
	if len(ids) == 0 {
		ids = []string{DefaultKEKID}
	}
	keys := make(map[string][]byte, len(ids))
	for _, id := range ids {
		keys[id] = RandomKey(t)
	}
	p, err := vault.NewLocalKEKProvider(keys, ids[len(ids)-1])
	if err != nil {
		t.Fatalf("vaulttest: new kek provider: %v", err)
	}
	return p
}

// RandomKey returns 32 random bytes usable as a KEK.
func RandomKey(t testing.TB) []byte {
	t.Helper()
	key := make([]byte, vault.KEKSize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("vaulttest: generate key: %v", err)
	}
	return key
}
