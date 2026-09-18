package identitysvc

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/identity"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/TikHub/Spinneret/internal/vault"
)

// Payload cache defaults (spec §8).
const (
	// DefaultPayloadCacheSize is the entry limit used when size <= 0.
	DefaultPayloadCacheSize = 200_000
	// SecretCredentialTTL is the lifetime of cached credentials that resolved
	// secret references.
	SecretCredentialTTL = 60 * time.Second
	// credentialLoadTimeout bounds one load (database, decryption, secrets).
	credentialLoadTimeout = 10 * time.Second
)

// SecretResolver resolves namespace-relative secret paths referenced by
// secret_ref payload fields (provided by vault.SecretStore).
type SecretResolver interface {
	ResolveForIdentity(ctx context.Context, namespaceID, path string) (string, error)
}

// cachedCredential is one cache entry. Entries are indexed by identity ID and
// only served when payload version, type ID and type version all match, which
// is equivalent to keying by identityID:payloadVersion:typeVersion while
// allowing Invalidate to drop an identity in O(1). Entries rendered with
// secrets carry an expiry (Unix nanoseconds); expiresAt 0 never expires.
type cachedCredential struct {
	payloadVersion int
	typeVersion    int
	typeID         string
	expiresAt      int64
	cred           *identity.Credential
}

func (e *cachedCredential) matches(t *identity.CompiledType, payloadVersion int) bool {
	return e.payloadVersion == payloadVersion && e.typeVersion == t.Version && e.typeID == t.ID
}

// PayloadCacheStats reports cache effectiveness.
type PayloadCacheStats struct {
	Hits    uint64
	Misses  uint64
	Entries int
}

// PayloadCache renders and caches the credentials of identity payloads for
// the Acquire hot path. It is one LRU of at most size entries (spec §8);
// entries whose rendering resolved secrets expire SecretCredentialTTL after
// they were loaded and are then reloaded on the next request (expired entries
// are evicted lazily, so the cache runs no background goroutine). Concurrent
// misses for the same identity, payload version and type version share one
// load.
//
// Returned credentials are shared between callers and must not be modified.
// Payloads and secrets are never logged.
type PayloadCache struct {
	pool      *pgxpool.Pool
	cipher    *vault.Cipher
	secrets   SecretResolver
	logger    *slog.Logger
	enabled   bool
	secretTTL time.Duration
	now       func() time.Time

	entries *lru.Cache[string, *cachedCredential]
	flight  singleflight.Group

	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewPayloadCache creates a payload cache. size <= 0 selects
// DefaultPayloadCacheSize; enabled=false renders every request without
// caching. secrets may be nil, in which case credentials that reference
// secrets fail to render.
func NewPayloadCache(pool *pgxpool.Pool, cipher *vault.Cipher, secrets SecretResolver, size int, enabled bool, logger *slog.Logger) *PayloadCache {
	return newPayloadCache(pool, cipher, secrets, size, enabled, SecretCredentialTTL, logger)
}

func newPayloadCache(pool *pgxpool.Pool, cipher *vault.Cipher, secrets SecretResolver, size int, enabled bool,
	secretTTL time.Duration, logger *slog.Logger) *PayloadCache {
	if logger == nil {
		logger = slog.Default()
	}
	if size <= 0 {
		size = DefaultPayloadCacheSize
	}
	if secretTTL <= 0 {
		secretTTL = SecretCredentialTTL
	}
	c := &PayloadCache{
		pool:      pool,
		cipher:    cipher,
		secrets:   secrets,
		logger:    logger.With(slog.String("component", "payload_cache")),
		enabled:   enabled,
		secretTTL: secretTTL,
		now:       time.Now,
	}
	if enabled {
		entries, err := lru.New[string, *cachedCredential](size)
		if err != nil {
			// lru.New only fails for a non-positive size, which is excluded above.
			c.enabled = false
			c.logger.Error("payload cache disabled", slog.Any("error", err))
			return c
		}
		c.entries = entries
	}
	return c
}

// Credential returns the rendered credential of payload version
// payloadVersion of an identity of type t. A missing payload version is
// not_found; decryption and rendering failures are internal errors.
func (c *PayloadCache) Credential(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, error) {
	if t == nil {
		return nil, apperr.Internal(fmt.Errorf("credential of identity %s: identity type is nil", identityID))
	}
	if c.enabled {
		if e, ok := c.entries.Get(identityID); ok && e.matches(t, payloadVersion) &&
			(e.expiresAt == 0 || c.now().UnixNano() < e.expiresAt) {
			c.hits.Add(1)
			return e.cred, nil
		}
	}
	c.misses.Add(1)
	if !c.enabled {
		cred, _, err := c.load(ctx, t, namespaceID, identityID, payloadVersion)
		return cred, err
	}
	key := identityID + ":" + strconv.Itoa(payloadVersion) + ":" + strconv.Itoa(t.Version) + ":" + t.ID
	ch := c.flight.DoChan(key, func() (any, error) {
		// The shared load must not fail because the first caller gave up.
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialLoadTimeout)
		defer cancel()
		cred, usedSecrets, err := c.load(lctx, t, namespaceID, identityID, payloadVersion)
		if err != nil {
			return nil, err
		}
		entry := &cachedCredential{payloadVersion: payloadVersion, typeVersion: t.Version, typeID: t.ID, cred: cred}
		if usedSecrets {
			entry.expiresAt = c.now().Add(c.secretTTL).UnixNano()
		}
		c.entries.Add(identityID, entry)
		return cred, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*identity.Credential), nil
	}
}

// Invalidate drops the cached credential of an identity.
func (c *PayloadCache) Invalidate(identityID string) {
	if !c.enabled {
		return
	}
	c.entries.Remove(identityID)
}

// Stats returns cache statistics. Entries includes expired entries that were
// not evicted yet.
func (c *PayloadCache) Stats() PayloadCacheStats {
	st := PayloadCacheStats{Hits: c.hits.Load(), Misses: c.misses.Load()}
	if c.enabled {
		st.Entries = c.entries.Len()
	}
	return st
}

// load decrypts and renders one payload version.
func (c *PayloadCache) load(ctx context.Context, t *identity.CompiledType, namespaceID, identityID string, payloadVersion int) (*identity.Credential, bool, error) {
	if payloadVersion < 1 || payloadVersion > math.MaxInt32 {
		return nil, false, apperr.NotFound("payload version %d of identity %s not found", payloadVersion, identityID)
	}
	if c.pool == nil || c.cipher == nil {
		return nil, false, apperr.Internal(fmt.Errorf("payload cache: database pool or cipher is not configured"))
	}
	payload, err := openPayload(ctx, identitysvcdb.New(c.pool), c.cipher, identityID, int32(payloadVersion))
	if err != nil {
		if apperr.IsNotFound(err) {
			return nil, false, err
		}
		c.logger.Warn("identity payload cannot be loaded", slog.String("identity_id", identityID),
			slog.Int("payload_version", payloadVersion), slog.Any("error", err))
		return nil, false, apperr.Internal(err)
	}
	var resolve identity.SecretResolver
	if c.secrets != nil {
		resolve = func(ctx context.Context, path string) (string, error) {
			return c.secrets.ResolveForIdentity(ctx, namespaceID, path)
		}
	}
	cred, usedSecrets, err := t.Render(ctx, payload, resolve)
	if err != nil {
		c.logger.Warn("identity credential cannot be rendered", slog.String("identity_id", identityID),
			slog.String("identity_type", t.Name), slog.Any("error", err))
		return nil, false, apperr.Internal(fmt.Errorf("render credential of identity %s: %w", identityID, err))
	}
	return cred, usedSecrets, nil
}
