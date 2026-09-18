package vault

import (
	"crypto/cipher"
	"crypto/sha256"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// minDEKCacheTTL is the smallest expiry the DEK cache uses. The underlying
// LRU purges expired entries on a ticker of TTL/100, which panics for a TTL
// below 100ns and burns CPU for very small ones; sub-second DEK caching has no
// practical use, so smaller positive TTLs are raised to this value.
const minDEKCacheTTL = time.Second

// dekCacheKey identifies an unwrapped DEK by SHA-256 over the KEK id, a zero
// separator (KEK ids never contain NUL) and the wrapped DEK bytes.
type dekCacheKey [sha256.Size]byte

// dekCache is a concurrency-safe LRU+TTL cache of AES-GCM instances derived
// from unwrapped DEKs. Caching the AEAD instead of raw key bytes lets the DEK
// be zeroed right after key expansion and avoids re-expanding the key on every
// Open. A nil *dekCache is a valid, disabled cache.
type dekCache struct {
	lru *expirable.LRU[dekCacheKey, cipher.AEAD]
}

// newDEKCache returns a cache holding at most size entries for at most ttl.
// size <= 0 disables caching (nil is returned). ttl <= 0 disables expiry; a
// positive ttl below minDEKCacheTTL is raised to it. With a positive ttl the
// underlying LRU runs one background goroutine for the lifetime of the
// process to purge expired entries proactively, which keeps key material from
// lingering in memory after its TTL.
func newDEKCache(size int, ttl time.Duration) *dekCache {
	if size <= 0 {
		return nil
	}
	return &dekCache{lru: expirable.NewLRU[dekCacheKey, cipher.AEAD](size, nil, effectiveDEKCacheTTL(ttl))}
}

// effectiveDEKCacheTTL maps a configured TTL to the one given to the LRU:
// 0 (no expiry) for ttl <= 0, otherwise at least minDEKCacheTTL.
func effectiveDEKCacheTTL(ttl time.Duration) time.Duration {
	switch {
	case ttl <= 0:
		return 0
	case ttl < minDEKCacheTTL:
		return minDEKCacheTTL
	default:
		return ttl
	}
}

// cacheKey computes the cache key without heap allocation for the usual
// wrapped DEK sizes.
func cacheKey(kekID string, wrapped []byte) dekCacheKey {
	var stack [128]byte
	buf := append(stack[:0], kekID...)
	buf = append(buf, 0)
	buf = append(buf, wrapped...)
	return sha256.Sum256(buf)
}

func (c *dekCache) get(key dekCacheKey) (cipher.AEAD, bool) {
	if c == nil {
		return nil, false
	}
	return c.lru.Get(key)
}

func (c *dekCache) add(key dekCacheKey, aead cipher.AEAD) {
	if c == nil {
		return
	}
	c.lru.Add(key, aead)
}

func (c *dekCache) len() int {
	if c == nil {
		return 0
	}
	return c.lru.Len()
}
