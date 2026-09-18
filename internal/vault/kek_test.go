package vault_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

func keyOf(b byte) []byte { return bytes.Repeat([]byte{b}, vault.KEKSize) }

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func TestValidKEKID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"k1", true},
		{"Key_2024-01", true},
		{strings.Repeat("a", 32), true},
		{"", false},
		{strings.Repeat("a", 33), false},
		{"k 1", false},
		{"k:1", false},
		{"ключ", false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			require.Equal(t, tt.want, vault.ValidKEKID(tt.id))
		})
	}
}

func TestNewLocalKEKProvider(t *testing.T) {
	tests := []struct {
		name        string
		keys        map[string][]byte
		current     string
		wantCurrent string
		wantIDs     []string
		wantErr     string
	}{
		{name: "single key defaults current", keys: map[string][]byte{"k1": keyOf(1)}, wantCurrent: "k1", wantIDs: []string{"k1"}},
		{name: "explicit current", keys: map[string][]byte{"b": keyOf(1), "a": keyOf(2)}, current: " a ", wantCurrent: "a", wantIDs: []string{"a", "b"}},
		{name: "no keys", keys: nil, wantErr: "no kek configured"},
		{name: "several keys without current", keys: map[string][]byte{"a": keyOf(1), "b": keyOf(2)}, wantErr: "current kek id is required"},
		{name: "unknown current", keys: map[string][]byte{"a": keyOf(1)}, current: "zz", wantErr: `current kek "zz" is not configured`},
		{name: "malformed current is not echoed", keys: map[string][]byte{"a": keyOf(1)}, current: b64(keyOf(9)), wantErr: "current kek id is invalid"},
		{name: "invalid id", keys: map[string][]byte{"bad id": keyOf(1)}, wantErr: "invalid kek id"},
		{name: "short key", keys: map[string][]byte{"k1": make([]byte, 16)}, wantErr: "must be 32 bytes, got 16"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := vault.NewLocalKEKProvider(tt.keys, tt.current)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, p)
				require.NotContains(t, err.Error(), b64(keyOf(9)), "error must not leak key material")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCurrent, p.CurrentID())
			require.Equal(t, tt.wantIDs, p.IDs())
		})
	}
}

func TestNewLocalKEKProviderDoesNotRetainInput(t *testing.T) {
	key := keyOf(7)
	p, err := vault.NewLocalKEKProvider(map[string][]byte{"k1": key}, "")
	require.NoError(t, err)
	require.Equal(t, keyOf(7), key, "input key must not be modified")

	dek := keyOf(9)
	wrapped, _, err := p.Wrap(dek)
	require.NoError(t, err)
	clear(key) // mutating the caller's copy must not affect the provider
	got, err := p.Unwrap("k1", wrapped)
	require.NoError(t, err)
	require.Equal(t, dek, got)
}

func TestLocalKEKProviderIDsReturnsCopy(t *testing.T) {
	p := vaulttest.NewProvider(t, "a", "b")
	ids := p.IDs()
	ids[0] = "mutated"
	require.Equal(t, []string{"a", "b"}, p.IDs())
}

func TestLocalKEKProviderWrapUnwrap(t *testing.T) {
	// Two ids with identical key material: the AAD binds the wrapped key to
	// its KEK id, so relabelling must still fail.
	same := keyOf(3)
	p, err := vault.NewLocalKEKProvider(map[string][]byte{"k1": same, "k2": same}, "k1")
	require.NoError(t, err)

	dek := keyOf(5)
	wrapped, kekID, err := p.Wrap(dek)
	require.NoError(t, err)
	require.Equal(t, "k1", kekID)
	require.Len(t, wrapped, 12+vault.DEKSize+16)

	got, err := p.Unwrap("k1", wrapped)
	require.NoError(t, err)
	require.Equal(t, dek, got)

	wrapped2, _, err := p.Wrap(dek)
	require.NoError(t, err)
	require.NotEqual(t, wrapped, wrapped2, "random nonce must make wraps differ")

	tampered := bytes.Clone(wrapped)
	tampered[len(tampered)-1] ^= 0x01

	tests := []struct {
		name    string
		kekID   string
		wrapped []byte
		target  error
	}{
		{name: "unknown kek", kekID: "k9", wrapped: wrapped, target: vault.ErrUnknownKEK},
		{name: "relabelled kek id", kekID: "k2", wrapped: wrapped, target: vault.ErrDecrypt},
		{name: "tampered", kekID: "k1", wrapped: tampered, target: vault.ErrDecrypt},
		{name: "too short", kekID: "k1", wrapped: wrapped[:10], target: vault.ErrDecrypt},
		{name: "empty", kekID: "k1", wrapped: nil, target: vault.ErrDecrypt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Unwrap(tt.kekID, tt.wrapped)
			require.ErrorIs(t, err, tt.target)
			require.Nil(t, got)
		})
	}

	t.Run("wrap rejects wrong dek size", func(t *testing.T) {
		_, _, err := p.Wrap(make([]byte, 16))
		require.ErrorContains(t, err, "dek must be 32 bytes")
	})
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keks")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestLoadLocalKEKProvider(t *testing.T) {
	k1, k2, k3 := b64(keyOf(1)), b64(keyOf(2)), b64(keyOf(3))
	rawURL := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xff}, 16))

	tests := []struct {
		name        string
		file        string // file content; "" means no file
		keys        string
		current     string
		wantCurrent string
		wantIDs     []string
		wantErr     string
	}{
		{
			name:        "file with id lines comments blanks and CRLF",
			file:        "# spinneret keks\r\n\r\n  old : " + k1 + "  \r\n# rotated\r\nnew:" + k2 + "\r\n",
			wantCurrent: "new", wantIDs: []string{"new", "old"},
		},
		{
			name: "single bare key in file", file: "\n# key\n" + k1 + "\n",
			wantCurrent: "k1", wantIDs: []string{"k1"},
		},
		{
			name: "keys list only with spaces and trailing comma", keys: " a:" + k1 + " , b : " + k2 + ",",
			wantCurrent: "b", wantIDs: []string{"a", "b"},
		},
		{
			name: "file and list merged, current is last list entry", file: "f1:" + k1 + "\nf2:" + k2, keys: "e1:" + k3,
			wantCurrent: "e1", wantIDs: []string{"e1", "f1", "f2"},
		},
		{
			name: "duplicate id with identical key allowed", file: "k1:" + k1 + "\nk2:" + k2, keys: "k1:" + k1,
			wantCurrent: "k1", wantIDs: []string{"k1", "k2"},
		},
		{
			name: "explicit current", file: "k1:" + k1, keys: "k2:" + k2, current: "k1",
			wantCurrent: "k1", wantIDs: []string{"k1", "k2"},
		},
		{
			name: "url-safe unpadded base64 accepted", keys: "u:" + rawURL,
			wantCurrent: "u", wantIDs: []string{"u"},
		},
		{name: "no sources", wantErr: "no kek configured"},
		{name: "file with only comments", file: "# nothing\n\n", wantErr: "no kek configured"},
		{name: "duplicate id with different key", file: "k1:" + k1, keys: "k1:" + k2, wantErr: `kek "k1" is configured twice with different keys`},
		{name: "bare key mixed with id lines", file: k1 + "\nk2:" + k2, wantErr: "bare base64 key is only allowed"},
		{name: "two bare keys", file: k1 + "\n" + k2, wantErr: "bare base64 key is only allowed"},
		{name: "invalid base64 in file", file: "k1:not-base64!!", wantErr: "line 1: kek \"k1\": key must be base64 of exactly 32 bytes"},
		{name: "wrong key length in list", keys: "k1:" + b64(make([]byte, 16)), wantErr: "entry 1: kek \"k1\": key must be base64"},
		{name: "list entry without id", keys: "k1:" + k1 + "," + k2, wantErr: "entry 2: expected id:base64"},
		{name: "invalid id in file", file: "\nbad id:" + k1, wantErr: "line 2: invalid kek id"},
		{name: "invalid id in list", keys: ":" + k1, wantErr: "entry 1: invalid kek id"},
		{name: "unknown current", keys: "k1:" + k1, current: "k7", wantErr: `current kek "k7" is not configured`},
		{name: "key material as current", keys: "k1:" + k1, current: k2, wantErr: "current kek id is invalid"},
		{name: "reversed base64:id in list", keys: k1 + ":k1", wantErr: "entry 1: invalid kek id"},
		{name: "reversed base64:id in file", file: k2 + ":k2\n", wantErr: "line 1: invalid kek id"},
		{name: "utf8 bom before id line", file: "\ufeffk1:" + k1 + "\n", wantCurrent: "k1", wantIDs: []string{"k1"}},
		{name: "utf8 bom before bare key", file: "\ufeff" + k2, wantCurrent: "k1", wantIDs: []string{"k1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := ""
			if tt.file != "" {
				file = writeFile(t, tt.file)
			}
			p, err := vault.LoadLocalKEKProvider(file, tt.keys, tt.current)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, p)
				for _, secret := range []string{k1, k2, k3, "not-base64!!"} {
					require.NotContains(t, err.Error(), secret, "error must not leak key material")
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCurrent, p.CurrentID())
			require.Equal(t, tt.wantIDs, p.IDs())
		})
	}
}

func TestLoadLocalKEKProviderMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := vault.LoadLocalKEKProvider(missing, "", "")
	require.ErrorContains(t, err, "read kek file")
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestLoadLocalKEKProviderFileLimits(t *testing.T) {
	t.Run("oversized file", func(t *testing.T) {
		content := "k1:" + b64(keyOf(1)) + "\n" + strings.Repeat("#", 1<<20)
		_, err := vault.LoadLocalKEKProvider(writeFile(t, content), "", "")
		require.ErrorContains(t, err, "file exceeds 1048576 bytes")
	})
	t.Run("directory instead of file", func(t *testing.T) {
		_, err := vault.LoadLocalKEKProvider(t.TempDir(), "", "")
		require.ErrorContains(t, err, "read kek file")
	})
}

func TestLoadLocalKEKProviderKeysAreUsable(t *testing.T) {
	key := keyOf(42)
	p, err := vault.LoadLocalKEKProvider(writeFile(t, b64(key)), "", "")
	require.NoError(t, err)

	// A provider built directly from the same key must unwrap what the
	// loaded provider wraps, proving the decoded bytes are correct.
	direct, err := vault.NewLocalKEKProvider(map[string][]byte{"k1": key}, "k1")
	require.NoError(t, err)
	dek := keyOf(8)
	wrapped, kekID, err := p.Wrap(dek)
	require.NoError(t, err)
	got, err := direct.Unwrap(kekID, wrapped)
	require.NoError(t, err)
	require.Equal(t, dek, got)
}

func TestGenerateKEK(t *testing.T) {
	a, err := vault.GenerateKEK()
	require.NoError(t, err)
	b, err := vault.GenerateKEK()
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	raw, err := base64.StdEncoding.DecodeString(a)
	require.NoError(t, err)
	require.Len(t, raw, vault.KEKSize)

	p, err := vault.LoadLocalKEKProvider("", "gen:"+a, "")
	require.NoError(t, err)
	require.Equal(t, "gen", p.CurrentID())
}
