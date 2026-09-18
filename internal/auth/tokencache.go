package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/pkg/netx"
)

// Cache sizes. Entries are small; the bounds protect against memory growth
// from floods of distinct (valid-looking) tokens.
const (
	tokenCacheSize         = 100_000
	negativeTokenCacheSize = 20_000
	// maxLastUsedEntries bounds the last-used buffer between flushes.
	maxLastUsedEntries = 100_000
	// lastUsedBatchSize bounds one UPDATE of the last-used flush.
	lastUsedBatchSize = 1_000
)

// tokenRecord is the cached verification data of one token. It is immutable.
type tokenRecord struct {
	id            string
	tenantID      string
	namespaceID   string
	namespaceName string
	name          string
	scopes        []authz.Scope
	allow         []netip.Prefix
	rateLimitRPS  int
	expiresAt     time.Time // zero = never
	revoked       bool
	// corrupt marks a stored token whose scopes or allowlist no longer parse.
	corrupt bool
}

// tokenCache resolves token hashes to records with positive and negative caching.
type tokenCache struct {
	q        *authdb.Queries
	logger   *slog.Logger
	positive *expirable.LRU[string, *tokenRecord]
	negative *expirable.LRU[string, struct{}]
	group    singleflight.Group
	// generation changes on every invalidation, so a lookup that started
	// before an invalidation does not repopulate the cache with stale data.
	generation atomic.Uint64
	// mu makes "compare generation, then add" atomic with respect to
	// invalidations (which bump the generation and remove entries under mu).
	mu sync.Mutex
}

func newTokenCache(pool *pgxpool.Pool, ttl time.Duration, logger *slog.Logger) *tokenCache {
	return &tokenCache{
		q:        authdb.New(pool),
		logger:   logger,
		positive: expirable.NewLRU[string, *tokenRecord](tokenCacheSize, nil, ttl),
		negative: expirable.NewLRU[string, struct{}](negativeTokenCacheSize, nil, negativeTokenCacheTTL),
	}
}

// errTokenUnknown reports a hash with no matching token.
var errTokenUnknown = errors.New("auth: unknown token")

// lookup returns the record for a token hash, or errTokenUnknown.
func (c *tokenCache) lookup(ctx context.Context, hash []byte) (*tokenRecord, error) {
	key := string(hash)
	if rec, ok := c.positive.Get(key); ok {
		return rec, nil
	}
	if c.negative.Contains(key) {
		return nil, errTokenUnknown
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		gen := c.generation.Load()
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
		defer cancel()
		row, err := c.q.AuthTokenByHash(qctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			c.addIfCurrent(gen, func() { c.negative.Add(key, struct{}{}) })
			return nil, errTokenUnknown
		}
		if err != nil {
			return nil, fmt.Errorf("load token: %w", err)
		}
		rec := c.recordFromRow(row)
		c.addIfCurrent(gen, func() { c.positive.Add(key, rec) })
		return rec, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tokenRecord), nil
}

func (c *tokenCache) recordFromRow(row authdb.AuthTokenByHashRow) *tokenRecord {
	rec := &tokenRecord{
		id:            row.ID,
		tenantID:      row.TenantID,
		namespaceID:   row.NamespaceID,
		namespaceName: row.NamespaceName,
		name:          row.Name,
		rateLimitRPS:  int(row.RateLimitRps),
		revoked:       row.RevokedAt != nil,
	}
	if row.ExpiresAt != nil {
		rec.expiresAt = *row.ExpiresAt
	}
	scopes, err := authz.ParseScopes(row.Scopes)
	if err != nil {
		c.logger.Error("stored token scopes are invalid; rejecting token",
			slog.String("token_id", row.ID), slog.Any("error", err))
		rec.corrupt = true
	}
	rec.scopes = scopes
	allow, err := netx.ParsePrefixes(row.IpAllowlist)
	if err != nil {
		c.logger.Error("stored token IP allowlist is invalid; rejecting token",
			slog.String("token_id", row.ID), slog.Any("error", err))
		rec.corrupt = true
	}
	rec.allow = allow
	return rec
}

// addIfCurrent runs add unless an invalidation happened since gen was read.
func (c *tokenCache) addIfCurrent(gen uint64, add func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation.Load() == gen {
		add()
	}
}

// dropToken removes every cached entry of a token ID. Revocations are rare, so
// a scan over the cache is acceptable.
func (c *tokenCache) dropToken(tokenID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation.Add(1)
	for _, key := range c.positive.Keys() {
		if rec, ok := c.positive.Peek(key); ok && rec.id == tokenID {
			c.positive.Remove(key)
		}
	}
}

// purge empties both caches.
func (c *tokenCache) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation.Add(1)
	c.positive.Purge()
	c.negative.Purge()
}

// lastUse is the most recent use of a token on this instance.
type lastUse struct {
	at time.Time
	ip string
}

// lastUsedTracker buffers token usage in memory until the periodic flush.
type lastUsedTracker struct {
	mu      sync.Mutex
	entries map[string]lastUse
	dropped int64
}

func newLastUsedTracker() *lastUsedTracker {
	return &lastUsedTracker{entries: make(map[string]lastUse)}
}

// record remembers a use; when the buffer is full new tokens are skipped
// until the next flush (existing entries are still updated).
func (t *lastUsedTracker) record(tokenID string, at time.Time, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.entries[tokenID]; ok {
		if at.After(prev.at) {
			t.entries[tokenID] = lastUse{at: at, ip: ip}
		}
		return
	}
	if len(t.entries) >= maxLastUsedEntries {
		t.dropped++
		return
	}
	t.entries[tokenID] = lastUse{at: at, ip: ip}
}

// take returns and clears the buffered entries.
func (t *lastUsedTracker) take() map[string]lastUse {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.entries) == 0 {
		return nil
	}
	out := t.entries
	t.entries = make(map[string]lastUse, len(out))
	return out
}

// flush writes buffered usage to PostgreSQL in batches. Entries of a failed
// batch are put back (unless newer data arrived meanwhile) for the next flush.
func (t *lastUsedTracker) flush(ctx context.Context, q *authdb.Queries) error {
	entries := t.take()
	if len(entries) == 0 {
		return nil
	}
	params := authdb.AuthTokenTouchParams{
		Ids:    make([]string, 0, min(len(entries), lastUsedBatchSize)),
		UsedAt: make([]time.Time, 0, min(len(entries), lastUsedBatchSize)),
		UsedIp: make([]string, 0, min(len(entries), lastUsedBatchSize)),
	}
	var errs []error
	send := func() {
		if len(params.Ids) == 0 {
			return
		}
		if err := q.AuthTokenTouch(ctx, params); err != nil {
			errs = append(errs, err)
			for i, id := range params.Ids {
				t.record(id, params.UsedAt[i], params.UsedIp[i])
			}
		}
		params.Ids, params.UsedAt, params.UsedIp = params.Ids[:0], params.UsedAt[:0], params.UsedIp[:0]
	}
	for id, use := range entries {
		params.Ids = append(params.Ids, id)
		params.UsedAt = append(params.UsedAt, use.at)
		params.UsedIp = append(params.UsedIp, use.ip)
		if len(params.Ids) >= lastUsedBatchSize {
			send()
		}
	}
	send()
	if len(errs) > 0 {
		return fmt.Errorf("flush token last-used: %w", errors.Join(errs...))
	}
	return nil
}
