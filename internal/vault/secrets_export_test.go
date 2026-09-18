package vault

import "time"

// Test hooks for the external vault_test package.

// SetSecretStoreClock overrides the clock used for cache expiry and access times.
func SetSecretStoreClock(s *SecretStore, now func() time.Time) { s.now = now }

// PendingAccessCount returns the number of buffered last-access times.
func PendingAccessCount(s *SecretStore) int { return s.access.len() }

// DroppedAccessCount returns the number of dropped last-access times.
func DroppedAccessCount(s *SecretStore) int64 { return s.access.dropped.Load() }

// SetMaxPendingAccesses overrides the access buffer limit.
func SetMaxPendingAccesses(s *SecretStore, n int) { s.access.max = n }

// SetAfterResolveLoad installs a hook run after ResolveForIdentity read the
// database and before it caches the value. Call before resolving.
func SetAfterResolveLoad(s *SecretStore, fn func()) { s.afterResolveLoad = fn }

// SanitizePurpose exposes sanitizePurpose.
func SanitizePurpose(p string) string { return sanitizePurpose(p) }

// SetAccessFlushInterval overrides the period of Run's flushes. Call before Run.
func SetAccessFlushInterval(s *SecretStore, d time.Duration) { s.flushEvery = d }
