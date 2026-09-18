package vault

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// ErrDecrypt is returned (wrapped) when a ciphertext or a wrapped DEK fails
// authentication: wrong AAD, tampered bytes, or a key that does not match.
// The error intentionally carries no further details.
var ErrDecrypt = errors.New("vault: decryption failed")

// maxPlaintextSize is the largest plaintext AES-GCM accepts
// ((2^32 - 2) blocks of 16 bytes).
const maxPlaintextSize = ((1 << 32) - 2) * 16

var (
	errNoProvider     = errors.New("vault: cipher has no kek provider")
	errDecryptData    = fmt.Errorf("%w: ciphertext authentication failed", ErrDecrypt)
	errDecryptDEKSize = fmt.Errorf("%w: unwrapped data key has an invalid size", ErrDecrypt)
)

// Sealed is an envelope-encrypted value as stored in the database.
type Sealed struct {
	// Ciphertext is nonce(12) || AES-256-GCM(DEK, plaintext, AAD).
	Ciphertext []byte
	// WrappedDEK is the DEK encrypted with the KEK identified by KEKID.
	WrappedDEK []byte
	// KEKID identifies the KEK that wrapped the DEK.
	KEKID string
}

// Cipher seals and opens values with envelope encryption. It is safe for
// concurrent use.
type Cipher struct {
	provider KEKProvider
	cache    *dekCache
}

// NewCipher returns a Cipher using p to wrap DEKs. Unwrapped DEKs are cached
// for Open in an LRU of cacheSize entries (<= 0 disables the cache) whose
// entries expire after cacheTTL (<= 0 means no expiry; entries then leave the
// cache only by LRU eviction; a positive value below one second is raised to
// one second). A positive cacheTTL starts one background
// goroutine, owned by the cache for the life of the process, that purges
// expired entries proactively so key material does not outlive its TTL; create
// one long-lived Cipher per process rather than one per request.
func NewCipher(p KEKProvider, cacheSize int, cacheTTL time.Duration) *Cipher {
	return &Cipher{provider: p, cache: newDEKCache(cacheSize, cacheTTL)}
}

// Provider returns the KEK provider of the cipher.
func (c *Cipher) Provider() KEKProvider {
	if c == nil {
		return nil
	}
	return c.provider
}

// AAD returns the additional authenticated data for field of record recordID:
// recordID + "\x00" + field (for example "idt_…\x00payload:v3").
func AAD(recordID, field string) []byte {
	b := make([]byte, 0, len(recordID)+1+len(field))
	b = append(b, recordID...)
	b = append(b, 0)
	return append(b, field...)
}

// Seal encrypts plaintext under a fresh random DEK bound to aad and wraps the
// DEK with the provider's current KEK.
func (c *Cipher) Seal(plaintext, aad []byte) (Sealed, error) {
	if c == nil || c.provider == nil {
		return Sealed{}, errNoProvider
	}
	if uint64(len(plaintext)) > maxPlaintextSize {
		return Sealed{}, errors.New("vault: seal: plaintext too large for AES-GCM")
	}
	dek := make([]byte, DEKSize)
	defer clear(dek)
	if _, err := rand.Read(dek); err != nil {
		return Sealed{}, fmt.Errorf("vault: seal: generate dek: %w", err)
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return Sealed{}, fmt.Errorf("vault: seal: init dek cipher: %w", err)
	}
	ciphertext := aead.Seal(nil, nil, plaintext, aad)
	wrapped, kekID, err := c.provider.Wrap(dek)
	if err != nil {
		return Sealed{}, fmt.Errorf("vault: seal: wrap dek: %w", err)
	}
	return Sealed{Ciphertext: ciphertext, WrappedDEK: wrapped, KEKID: kekID}, nil
}

// Open decrypts s with the given aad. Authentication failures of either the
// ciphertext or the wrapped DEK return an error wrapping ErrDecrypt; a KEK id
// the provider does not hold returns an error wrapping ErrUnknownKEK.
func (c *Cipher) Open(s Sealed, aad []byte) ([]byte, error) {
	if c == nil || c.provider == nil {
		return nil, errNoProvider
	}
	if len(s.Ciphertext) < gcmOverhead {
		return nil, errDecryptData
	}
	aead, err := c.dataAEAD(s.KEKID, s.WrappedDEK)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nil, s.Ciphertext, aad)
	if err != nil {
		return nil, errDecryptData
	}
	return plaintext, nil
}

// Rewrap re-wraps the DEK of s with the provider's current KEK. The
// ciphertext is unchanged (the returned value shares its backing array with
// s.Ciphertext). The boolean is false, and s is returned as is, when s is
// already wrapped by the current KEK.
func (c *Cipher) Rewrap(s Sealed) (Sealed, bool, error) {
	if c == nil || c.provider == nil {
		return Sealed{}, false, errNoProvider
	}
	if s.KEKID == c.provider.CurrentID() {
		return s, false, nil
	}
	dek, err := c.provider.Unwrap(s.KEKID, s.WrappedDEK)
	defer clear(dek)
	if err != nil {
		return Sealed{}, false, unwrapError("rewrap", err)
	}
	if len(dek) != DEKSize {
		return Sealed{}, false, errDecryptDEKSize
	}
	wrapped, kekID, err := c.provider.Wrap(dek)
	if err != nil {
		return Sealed{}, false, fmt.Errorf("vault: rewrap: wrap dek: %w", err)
	}
	return Sealed{Ciphertext: s.Ciphertext, WrappedDEK: wrapped, KEKID: kekID}, true, nil
}

// dataAEAD returns the AES-GCM instance of a wrapped DEK, from the cache when
// possible. The raw DEK is zeroed once the key schedule is derived.
func (c *Cipher) dataAEAD(kekID string, wrapped []byte) (cipher.AEAD, error) {
	var key dekCacheKey
	if c.cache != nil {
		key = cacheKey(kekID, wrapped)
		if aead, ok := c.cache.get(key); ok {
			return aead, nil
		}
	}
	dek, err := c.provider.Unwrap(kekID, wrapped)
	defer clear(dek)
	if err != nil {
		return nil, unwrapError("open", err)
	}
	if len(dek) != DEKSize {
		return nil, errDecryptDEKSize
	}
	aead, err := newAEAD(dek)
	if err != nil {
		return nil, fmt.Errorf("vault: open: init dek cipher: %w", err)
	}
	if c.cache != nil {
		c.cache.add(key, aead)
	}
	return aead, nil
}

// unwrapError returns provider errors that already classify the failure
// (ErrDecrypt, ErrUnknownKEK) unchanged and adds context to any other error.
func unwrapError(op string, err error) error {
	if errors.Is(err, ErrDecrypt) || errors.Is(err, ErrUnknownKEK) {
		return err
	}
	return fmt.Errorf("vault: %s: unwrap dek: %w", op, err)
}
