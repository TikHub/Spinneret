package vault

import (
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
)

// resolveEntryOverhead approximates the memory of one cache entry besides
// its key and value bytes (LRU element, map bucket, strings headers).
const resolveEntryOverhead = 128

// resolveEntry is a cached secret value.
type resolveEntry struct {
	secretID string
	value    string
	expires  time.Time
}

// resolveCache is a small LRU of decrypted secret values with a fixed TTL,
// bounded both by entry count and by the approximate bytes it holds (secret
// values may be up to 64 KiB each). Expiry is checked on access, so no
// background goroutine is needed. It is safe for concurrent use.
type resolveCache struct {
	mu       sync.Mutex
	lru      *simplelru.LRU[string, resolveEntry]
	ttl      time.Duration
	maxBytes int
	bytes    int
	// gen counts invalidations; loads that started before one are not cached.
	gen uint64
}

func newResolveCache(size, maxBytes int, ttl time.Duration) *resolveCache {
	c := &resolveCache{ttl: ttl, maxBytes: maxBytes}
	onEvict := func(key string, e resolveEntry) { c.bytes -= resolveEntrySize(key, e) }
	lru, err := simplelru.NewLRU[string, resolveEntry](max(size, 1), onEvict)
	if err != nil {
		// Unreachable: NewLRU only fails for a non-positive size.
		lru, _ = simplelru.NewLRU[string, resolveEntry](1, onEvict)
	}
	c.lru = lru
	return c
}

func resolveKey(namespaceID, path string) string {
	return namespaceID + "\x00" + path
}

func resolveEntrySize(key string, e resolveEntry) int {
	return len(key) + len(e.secretID) + len(e.value) + resolveEntryOverhead
}

func (c *resolveCache) get(namespaceID, path string, now time.Time) (resolveEntry, bool) {
	key := resolveKey(namespaceID, path)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.lru.Get(key)
	if !ok {
		return resolveEntry{}, false
	}
	if !now.Before(e.expires) {
		c.lru.Remove(key)
		return resolveEntry{}, false
	}
	return e, true
}

// generation returns the current invalidation counter.
func (c *resolveCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// addIfCurrent caches e unless an invalidation happened after gen was read,
// in which case e may predate that change and is dropped. Entries larger than
// the byte budget are not cached; older entries are evicted to make room.
func (c *resolveCache) addIfCurrent(namespaceID, path string, e resolveEntry, now time.Time, gen uint64) {
	e.expires = now.Add(c.ttl)
	key := resolveKey(namespaceID, path)
	size := resolveEntrySize(key, e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || size > c.maxBytes {
		return
	}
	// Replacing an entry does not trigger the eviction callback.
	if old, ok := c.lru.Peek(key); ok {
		c.bytes -= resolveEntrySize(key, old)
	}
	c.lru.Add(key, e)
	c.bytes += size
	for c.bytes > c.maxBytes {
		if _, _, ok := c.lru.RemoveOldest(); !ok {
			break
		}
	}
}

func (c *resolveCache) remove(namespaceID, path string) {
	c.mu.Lock()
	c.gen++
	c.lru.Remove(resolveKey(namespaceID, path))
	c.mu.Unlock()
}

// usage returns the number of entries and approximate bytes held.
func (c *resolveCache) usage() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len(), c.bytes
}
