package vault_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

// countingProvider wraps a KEKProvider and counts Unwrap calls.
type countingProvider struct {
	vault.KEKProvider
	unwraps atomic.Int64
}

func (p *countingProvider) Unwrap(kekID string, wrapped []byte) ([]byte, error) {
	p.unwraps.Add(1)
	return p.KEKProvider.Unwrap(kekID, wrapped)
}

// faultyProvider returns configurable failures.
type faultyProvider struct {
	vault.KEKProvider
	wrapErr   error
	unwrapErr error
	dekSize   int
}

func (p *faultyProvider) Wrap(dek []byte) ([]byte, string, error) {
	if p.wrapErr != nil {
		return nil, "", p.wrapErr
	}
	return p.KEKProvider.Wrap(dek)
}

func (p *faultyProvider) Unwrap(kekID string, wrapped []byte) ([]byte, error) {
	if p.unwrapErr != nil {
		return nil, p.unwrapErr
	}
	if p.dekSize > 0 {
		return make([]byte, p.dekSize), nil
	}
	return p.KEKProvider.Unwrap(kekID, wrapped)
}

func randomBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func TestAAD(t *testing.T) {
	require.Equal(t, []byte("idt_1\x00payload:v3"), vault.AAD("idt_1", "payload:v3"))
	require.Equal(t, []byte("\x00"), vault.AAD("", ""))
}

func TestCipherRoundTrip(t *testing.T) {
	c := vaulttest.NewCipher(t)
	aad := vault.AAD("sec_1", "v1")
	for _, size := range []int{0, 1, 31, 2048, 1 << 20} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			plaintext := randomBytes(t, size)
			s, err := c.Seal(plaintext, aad)
			require.NoError(t, err)
			require.Equal(t, vaulttest.DefaultKEKID, s.KEKID)
			require.Len(t, s.Ciphertext, 12+size+16)
			require.Len(t, s.WrappedDEK, 12+vault.DEKSize+16)
			if size > 16 {
				require.False(t, bytes.Contains(s.Ciphertext, plaintext[:16]), "ciphertext leaks plaintext")
			}

			got, err := c.Open(s, aad)
			require.NoError(t, err)
			require.Equal(t, len(plaintext), len(got))
			require.True(t, bytes.Equal(plaintext, got))
		})
	}
}

func TestCipherSealIsRandomized(t *testing.T) {
	c := vaulttest.NewCipher(t)
	a, err := c.Seal([]byte("same"), nil)
	require.NoError(t, err)
	b, err := c.Seal([]byte("same"), nil)
	require.NoError(t, err)
	require.NotEqual(t, a.Ciphertext, b.Ciphertext)
	require.NotEqual(t, a.WrappedDEK, b.WrappedDEK)
}

func TestCipherOpenFailures(t *testing.T) {
	provider := vaulttest.NewProvider(t, "k1", "k2")
	c := vault.NewCipher(provider, 16, time.Minute)
	aad := vault.AAD("idt_1", "payload:v1")
	s, err := c.Seal([]byte(`{"cookie":"secret"}`), aad)
	require.NoError(t, err)
	require.Equal(t, "k2", s.KEKID)

	flip := func(b []byte, i int) []byte {
		out := bytes.Clone(b)
		out[i] ^= 0x80
		return out
	}
	tests := []struct {
		name   string
		sealed vault.Sealed
		aad    []byte
		target error
	}{
		{name: "aad mismatch field", sealed: s, aad: vault.AAD("idt_1", "payload:v2"), target: vault.ErrDecrypt},
		{name: "aad mismatch record", sealed: s, aad: vault.AAD("idt_2", "payload:v1"), target: vault.ErrDecrypt},
		{name: "missing aad", sealed: s, aad: nil, target: vault.ErrDecrypt},
		{name: "tampered nonce", sealed: vault.Sealed{Ciphertext: flip(s.Ciphertext, 0), WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "tampered body", sealed: vault.Sealed{Ciphertext: flip(s.Ciphertext, 14), WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "tampered tag", sealed: vault.Sealed{Ciphertext: flip(s.Ciphertext, len(s.Ciphertext)-1), WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "truncated ciphertext", sealed: vault.Sealed{Ciphertext: s.Ciphertext[:27], WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "empty ciphertext", sealed: vault.Sealed{WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "tampered wrapped dek", sealed: vault.Sealed{Ciphertext: s.Ciphertext, WrappedDEK: flip(s.WrappedDEK, 20), KEKID: s.KEKID}, aad: aad, target: vault.ErrDecrypt},
		{name: "wrong known kek id", sealed: vault.Sealed{Ciphertext: s.Ciphertext, WrappedDEK: s.WrappedDEK, KEKID: "k1"}, aad: aad, target: vault.ErrDecrypt},
		{name: "unknown kek id", sealed: vault.Sealed{Ciphertext: s.Ciphertext, WrappedDEK: s.WrappedDEK, KEKID: "gone"}, aad: aad, target: vault.ErrUnknownKEK},
		{name: "empty kek id", sealed: vault.Sealed{Ciphertext: s.Ciphertext, WrappedDEK: s.WrappedDEK}, aad: aad, target: vault.ErrUnknownKEK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Open(tt.sealed, tt.aad)
			require.ErrorIs(t, err, tt.target)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "secret")
		})
	}

	// The valid value still opens after all failed attempts (the cache was
	// not poisoned by tampered inputs).
	got, err := c.Open(s, aad)
	require.NoError(t, err)
	require.Equal(t, `{"cookie":"secret"}`, string(got))
}

func TestCipherUnknownKEKIsNotDecryptError(t *testing.T) {
	c := vaulttest.NewCipher(t)
	s, err := c.Seal([]byte("x"), nil)
	require.NoError(t, err)
	s.KEKID = "retired"
	_, err = c.Open(s, nil)
	require.ErrorIs(t, err, vault.ErrUnknownKEK)
	require.False(t, errors.Is(err, vault.ErrDecrypt))
}

func TestCipherRewrap(t *testing.T) {
	oldKey, newKey := vaulttest.RandomKey(t), vaulttest.RandomKey(t)
	oldProvider, err := vault.NewLocalKEKProvider(map[string][]byte{"old": oldKey}, "")
	require.NoError(t, err)
	rotated, err := vault.NewLocalKEKProvider(map[string][]byte{"old": oldKey, "new": newKey}, "new")
	require.NoError(t, err)
	newOnly, err := vault.NewLocalKEKProvider(map[string][]byte{"new": newKey}, "new")
	require.NoError(t, err)

	aad := vault.AAD("pxy_1", "url")
	plaintext := []byte("http://user:pass@proxy:8080")
	sealed, err := vault.NewCipher(oldProvider, 0, 0).Seal(plaintext, aad)
	require.NoError(t, err)
	require.Equal(t, "old", sealed.KEKID)

	c := vault.NewCipher(rotated, 8, time.Minute)
	got, err := c.Open(sealed, aad)
	require.NoError(t, err, "rotated provider still opens values of the old key")
	require.Equal(t, plaintext, got)

	rewrapped, changed, err := c.Rewrap(sealed)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "new", rewrapped.KEKID)
	require.Equal(t, sealed.Ciphertext, rewrapped.Ciphertext, "ciphertext must be unchanged")
	require.NotEqual(t, sealed.WrappedDEK, rewrapped.WrappedDEK)

	for name, cipher := range map[string]*vault.Cipher{"rotated": c, "new only": vault.NewCipher(newOnly, 8, 0)} {
		got, err := cipher.Open(rewrapped, aad)
		require.NoError(t, err, name)
		require.Equal(t, plaintext, got, name)
	}

	again, changed, err := c.Rewrap(rewrapped)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, rewrapped, again)

	t.Run("tampered wrapped dek", func(t *testing.T) {
		bad := sealed
		bad.WrappedDEK = bytes.Clone(sealed.WrappedDEK)
		bad.WrappedDEK[0] ^= 1
		_, changed, err := c.Rewrap(bad)
		require.ErrorIs(t, err, vault.ErrDecrypt)
		require.False(t, changed)
	})
	t.Run("unknown kek", func(t *testing.T) {
		bad := sealed
		bad.KEKID = "missing"
		_, _, err := c.Rewrap(bad)
		require.ErrorIs(t, err, vault.ErrUnknownKEK)
	})
}

func TestCipherProviderFailures(t *testing.T) {
	base := vaulttest.NewProvider(t)
	sealed, err := vault.NewCipher(base, 0, 0).Seal([]byte("data"), nil)
	require.NoError(t, err)
	boom := errors.New("kms unavailable")

	t.Run("seal wrap error", func(t *testing.T) {
		c := vault.NewCipher(&faultyProvider{KEKProvider: base, wrapErr: boom}, 0, 0)
		_, err := c.Seal([]byte("data"), nil)
		require.ErrorIs(t, err, boom)
	})
	t.Run("open unwrap error", func(t *testing.T) {
		c := vault.NewCipher(&faultyProvider{KEKProvider: base, unwrapErr: boom}, 4, 0)
		_, err := c.Open(sealed, nil)
		require.ErrorIs(t, err, boom)
	})
	t.Run("open unwrapped dek of wrong size", func(t *testing.T) {
		c := vault.NewCipher(&faultyProvider{KEKProvider: base, dekSize: 16}, 4, 0)
		_, err := c.Open(sealed, nil)
		require.ErrorIs(t, err, vault.ErrDecrypt)
	})
	t.Run("rewrap unwrapped dek of wrong size", func(t *testing.T) {
		other := vaulttest.NewProvider(t, "other")
		c := vault.NewCipher(&faultyProvider{KEKProvider: other, dekSize: 16}, 4, 0)
		_, _, err := c.Rewrap(sealed)
		require.ErrorIs(t, err, vault.ErrDecrypt)
	})
	t.Run("rewrap wrap error", func(t *testing.T) {
		rotated := vaulttest.NewProvider(t, "next")
		// Unwrap through base (holds the "test" key), wrap fails.
		fp := &faultyProvider{KEKProvider: &splitProvider{unwrap: base, wrap: rotated}, wrapErr: boom}
		c := vault.NewCipher(fp, 0, 0)
		_, _, err := c.Rewrap(sealed)
		require.ErrorIs(t, err, boom)
	})
}

// splitProvider unwraps with one provider and wraps with another.
type splitProvider struct {
	unwrap, wrap vault.KEKProvider
}

func (p *splitProvider) CurrentID() string { return p.wrap.CurrentID() }
func (p *splitProvider) IDs() []string     { return append(p.unwrap.IDs(), p.wrap.IDs()...) }
func (p *splitProvider) Wrap(dek []byte) ([]byte, string, error) {
	return p.wrap.Wrap(dek)
}
func (p *splitProvider) Unwrap(kekID string, wrapped []byte) ([]byte, error) {
	return p.unwrap.Unwrap(kekID, wrapped)
}

func TestCipherNilProvider(t *testing.T) {
	for name, c := range map[string]*vault.Cipher{"nil cipher": nil, "nil provider": vault.NewCipher(nil, 4, 0)} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Seal([]byte("x"), nil)
			require.ErrorContains(t, err, "no kek provider")
			_, err = c.Open(vault.Sealed{}, nil)
			require.ErrorContains(t, err, "no kek provider")
			_, _, err = c.Rewrap(vault.Sealed{})
			require.ErrorContains(t, err, "no kek provider")
			require.Nil(t, c.Provider())
		})
	}
}

func TestCipherProvider(t *testing.T) {
	p := vaulttest.NewProvider(t)
	require.Same(t, p, vault.NewCipher(p, 1, 0).Provider())
}

func TestCipherCacheHitPath(t *testing.T) {
	cp := &countingProvider{KEKProvider: vaulttest.NewProvider(t)}
	c := vault.NewCipher(cp, 2, time.Hour)
	seal := func(msg string) vault.Sealed {
		s, err := c.Seal([]byte(msg), []byte(msg))
		require.NoError(t, err)
		return s
	}
	open := func(s vault.Sealed, msg string) {
		got, err := c.Open(s, []byte(msg))
		require.NoError(t, err)
		require.Equal(t, msg, string(got))
	}
	a, b, d := seal("a"), seal("b"), seal("d")
	require.Zero(t, cp.unwraps.Load(), "seal must not unwrap")

	open(a, "a")
	open(a, "a")
	open(a, "a")
	require.EqualValues(t, 1, cp.unwraps.Load(), "repeated opens hit the cache")

	open(b, "b")
	require.EqualValues(t, 2, cp.unwraps.Load())
	open(d, "d") // evicts a (LRU size 2)
	require.EqualValues(t, 3, cp.unwraps.Load())
	open(a, "a")
	require.EqualValues(t, 4, cp.unwraps.Load(), "evicted entry is unwrapped again")

	// A cached DEK must not let a wrong AAD or a relabelled KEK through.
	_, err := c.Open(a, []byte("wrong"))
	require.ErrorIs(t, err, vault.ErrDecrypt)
	relabelled := a
	relabelled.KEKID = "other"
	_, err = c.Open(relabelled, []byte("a"))
	require.ErrorIs(t, err, vault.ErrUnknownKEK)
}

func TestCipherCacheDisabled(t *testing.T) {
	cp := &countingProvider{KEKProvider: vaulttest.NewProvider(t)}
	c := vault.NewCipher(cp, 0, time.Hour)
	s, err := c.Seal([]byte("x"), nil)
	require.NoError(t, err)
	for range 3 {
		_, err := c.Open(s, nil)
		require.NoError(t, err)
	}
	require.EqualValues(t, 3, cp.unwraps.Load())
}

func TestCipherCacheTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the one-second minimum cache TTL")
	}
	cp := &countingProvider{KEKProvider: vaulttest.NewProvider(t)}
	// 50ms is raised to the one-second minimum TTL.
	c := vault.NewCipher(cp, 16, 50*time.Millisecond)
	s, err := c.Seal([]byte("x"), nil)
	require.NoError(t, err)
	_, err = c.Open(s, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, cp.unwraps.Load())

	require.Eventually(t, func() bool {
		_, err := c.Open(s, nil)
		return err == nil && cp.unwraps.Load() > 1
	}, 5*time.Second, 20*time.Millisecond, "expired entries must be unwrapped again")
}

func TestCipherConcurrentUse(t *testing.T) {
	oldKey, newKey := vaulttest.RandomKey(t), vaulttest.RandomKey(t)
	oldProvider, err := vault.NewLocalKEKProvider(map[string][]byte{"old": oldKey}, "")
	require.NoError(t, err)
	rotated, err := vault.NewLocalKEKProvider(map[string][]byte{"old": oldKey, "new": newKey}, "new")
	require.NoError(t, err)

	oldCipher := vault.NewCipher(oldProvider, 0, 0)
	// A tiny cache forces constant eviction alongside cache hits.
	c := vault.NewCipher(rotated, 4, time.Minute)
	const workers, iterations = 16, 100
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := range workers {
		wg.Go(func() {
			if err := concurrentWorker(oldCipher, c, w, iterations); err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func concurrentWorker(oldCipher, c *vault.Cipher, worker, iterations int) error {
	for i := range iterations {
		id := fmt.Sprintf("rec_%d_%d", worker, i)
		aad := vault.AAD(id, "f")
		msg := []byte(id)
		sealer := c
		if i%2 == 0 {
			sealer = oldCipher
		}
		s, err := sealer.Seal(msg, aad)
		if err != nil {
			return fmt.Errorf("seal %s: %w", id, err)
		}
		for range 2 {
			got, err := c.Open(s, aad)
			if err != nil {
				return fmt.Errorf("open %s: %w", id, err)
			}
			if !bytes.Equal(got, msg) {
				return fmt.Errorf("open %s: plaintext mismatch", id)
			}
		}
		r, changed, err := c.Rewrap(s)
		if err != nil {
			return fmt.Errorf("rewrap %s: %w", id, err)
		}
		if changed != (i%2 == 0) {
			return fmt.Errorf("rewrap %s: changed=%v", id, changed)
		}
		got, err := c.Open(r, aad)
		if err != nil {
			return fmt.Errorf("open rewrapped %s: %w", id, err)
		}
		if !bytes.Equal(got, msg) {
			return fmt.Errorf("open rewrapped %s: plaintext mismatch", id)
		}
	}
	return nil
}

func BenchmarkCipherOpen2KB(b *testing.B) {
	c := vaulttest.NewCipher(b)
	aad := vault.AAD("idt_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", "payload:v1")
	s, err := c.Seal(randomBytes(b, 2048), aad)
	require.NoError(b, err)

	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(2048)
		for b.Loop() {
			if _, err := c.Open(s, aad); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("uncached", func(b *testing.B) {
		nc := vault.NewCipher(c.Provider(), 0, 0)
		b.ReportAllocs()
		b.SetBytes(2048)
		for b.Loop() {
			if _, err := nc.Open(s, aad); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkCipherSeal2KB(b *testing.B) {
	c := vaulttest.NewCipher(b)
	aad := vault.AAD("idt_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", "payload:v1")
	plaintext := randomBytes(b, 2048)
	b.ReportAllocs()
	b.SetBytes(2048)
	for b.Loop() {
		if _, err := c.Seal(plaintext, aad); err != nil {
			b.Fatal(err)
		}
	}
}
