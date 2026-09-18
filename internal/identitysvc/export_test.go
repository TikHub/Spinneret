package identitysvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvcdb"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// NewPayloadCacheWithTTL exposes the secret-entry TTL to tests.
func NewPayloadCacheWithTTL(pool *pgxpool.Pool, cipher *vault.Cipher, secrets SecretResolver, size int, enabled bool,
	secretTTL time.Duration, logger *slog.Logger) *PayloadCache {
	return newPayloadCache(pool, cipher, secrets, size, enabled, secretTTL, logger)
}

// CacheSizes returns the number of cached entries without and with an expiry
// (credentials rendered with secrets), expired entries included.
func (c *PayloadCache) CacheSizes() (plain, secret int) {
	if !c.enabled {
		return 0, 0
	}
	for _, e := range c.entries.Values() {
		if e.expiresAt == 0 {
			plain++
		} else {
			secret++
		}
	}
	return plain, secret
}

// SetCacheNow replaces the clock of the payload cache.
func (c *PayloadCache) SetCacheNow(now func() time.Time) { c.now = now }

// CheckTypeVersion exposes the in-transaction identity type version check.
func (s *Service) CheckTypeVersion(ctx context.Context, ct *identity.CompiledType) error {
	return checkTypeVersion(ctx, identitysvcdb.New(s.pool), ct)
}

// SetNow replaces the clock of the service.
func (s *Service) SetNow(now func() time.Time) { s.now = now }
