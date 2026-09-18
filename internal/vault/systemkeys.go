package vault

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/vault/vaultdb"
)

// System key limits.
const (
	// MaxSystemKeyNameLength bounds system key names.
	MaxSystemKeyNameLength = 128
	// MaxSystemKeySize bounds the size of generated system keys.
	MaxSystemKeySize = 4096

	systemKeyTimeout = 10 * time.Second
)

// SystemKeyAAD returns the AAD of the system key name: record
// "system_key:<name>", field "v1".
func SystemKeyAAD(name string) []byte {
	return AAD("system_key:"+name, "v1")
}

// SystemKey returns the system key called name (for example "dedupe_pepper"),
// creating it with size random bytes when it does not exist yet. Creation is
// race-safe across instances: the key is inserted with ON CONFLICT DO NOTHING
// and then read back, so every caller ends up with the key that won. An
// existing key whose size differs from size is reported as an error.
func SystemKey(ctx context.Context, pool *pgxpool.Pool, c *Cipher, name string, size int) ([]byte, error) {
	if pool == nil || c == nil {
		return nil, errors.New("vault: system key: pool and cipher are required")
	}
	if name == "" || len(name) > MaxSystemKeyNameLength || strings.IndexFunc(name, invalidSystemKeyNameRune) >= 0 {
		return nil, fmt.Errorf("vault: system key: invalid name %q", name)
	}
	if size <= 0 || size > MaxSystemKeySize {
		return nil, fmt.Errorf("vault: system key %s: size must be in [1, %d]", name, MaxSystemKeySize)
	}
	ctx, cancel := context.WithTimeout(ctx, systemKeyTimeout)
	defer cancel()
	q := vaultdb.New(pool)

	key, err := loadSystemKey(ctx, q, c, name)
	if err == nil {
		return checkSystemKeySize(name, key, size)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	fresh := make([]byte, size)
	defer clear(fresh)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("vault: system key %s: generate: %w", name, err)
	}
	sealed, err := c.Seal(fresh, SystemKeyAAD(name))
	if err != nil {
		return nil, fmt.Errorf("vault: system key %s: seal: %w", name, err)
	}
	if _, err := q.VaultSystemKeyInsert(ctx, vaultdb.VaultSystemKeyInsertParams{
		Name: name, Ciphertext: sealed.Ciphertext, WrappedDek: sealed.WrappedDEK, KekID: sealed.KEKID,
	}); err != nil {
		return nil, fmt.Errorf("vault: system key %s: insert: %w", name, err)
	}
	// Read back: another instance may have inserted its key first.
	key, err = loadSystemKey(ctx, q, c, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("vault: system key %s: missing after insert (deleted concurrently): %w", name, err)
	}
	if err != nil {
		return nil, err
	}
	return checkSystemKeySize(name, key, size)
}

func loadSystemKey(ctx context.Context, q *vaultdb.Queries, c *Cipher, name string) ([]byte, error) {
	row, err := q.VaultSystemKeyGet(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("vault: system key %s: load: %w", name, err)
	}
	key, err := c.Open(Sealed{Ciphertext: row.Ciphertext, WrappedDEK: row.WrappedDek, KEKID: row.KekID}, SystemKeyAAD(name))
	if err != nil {
		return nil, fmt.Errorf("vault: system key %s: decrypt: %w", name, err)
	}
	return key, nil
}

func checkSystemKeySize(name string, key []byte, size int) ([]byte, error) {
	if len(key) != size {
		clear(key)
		return nil, fmt.Errorf("vault: system key %s has %d bytes, expected %d", name, len(key), size)
	}
	return key, nil
}

func invalidSystemKeyNameRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == '�'
}
