package vault

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// Key sizes and envelope constants.
const (
	// KEKSize is the size in bytes of a key-encryption key (AES-256).
	KEKSize = 32
	// DEKSize is the size in bytes of a data-encryption key (AES-256).
	DEKSize = 32
	// MaxKEKIDLength is the maximum length of a KEK id.
	MaxKEKIDLength = 32
	// DefaultFileKEKID is the id given to a bare key in a KEK file.
	DefaultFileKEKID = "k1"

	// maxKEKFileSize bounds the KEK file read so that a misconfigured path
	// (a large file or a device such as /dev/zero) fails fast instead of
	// exhausting memory.
	maxKEKFileSize = 1 << 20

	dekAADPrefix = "spinneret-dek:"
	// gcmOverhead is nonce (12) + tag (16), the size added by AES-GCM with a
	// prepended random nonce.
	gcmOverhead = 12 + 16
)

// ErrUnknownKEK is returned when a wrapped DEK references a KEK id that the
// provider does not hold. It usually indicates a configuration problem (a
// retired key removed too early) rather than tampering.
var ErrUnknownKEK = errors.New("vault: unknown kek id")

// KEKProvider wraps and unwraps data-encryption keys with key-encryption keys.
// Implementations must be safe for concurrent use.
type KEKProvider interface {
	// CurrentID returns the id of the KEK used by Wrap.
	CurrentID() string
	// IDs returns the ids of every KEK able to unwrap, sorted.
	IDs() []string
	// Wrap encrypts dek with the current KEK and returns the wrapped key
	// together with the id of the KEK used. Wrap must not retain dek: the
	// caller zeroes it after the call.
	Wrap(dek []byte) (wrapped []byte, kekID string, err error)
	// Unwrap decrypts a DEK wrapped by the KEK kekID. It returns an error
	// wrapping ErrUnknownKEK when kekID is not held and ErrDecrypt when the
	// wrapped key fails authentication. The returned slice must be freshly
	// allocated: the caller owns it and zeroes it after use.
	Unwrap(kekID string, wrapped []byte) ([]byte, error)
}

// LocalKEKProvider holds KEKs in process memory. It is immutable after
// construction and safe for concurrent use. Raw key bytes are not retained:
// only the derived AES-GCM instances are kept.
type LocalKEKProvider struct {
	current string
	ids     []string
	keys    map[string]localKEK
}

type localKEK struct {
	aead cipher.AEAD
	aad  []byte
}

var _ KEKProvider = (*LocalKEKProvider)(nil)

// ValidKEKID reports whether id matches ^[a-zA-Z0-9_-]{1,32}$.
func ValidKEKID(id string) bool {
	if id == "" || len(id) > MaxKEKIDLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// NewLocalKEKProvider builds a provider from raw 32-byte keys indexed by id.
// current selects the KEK used for wrapping; it may be empty only when exactly
// one key is given. The caller's slices are neither retained nor modified.
func NewLocalKEKProvider(keys map[string][]byte, current string) (*LocalKEKProvider, error) {
	if len(keys) == 0 {
		return nil, errors.New("vault: no kek configured")
	}
	current = strings.TrimSpace(current)
	if current == "" {
		if len(keys) != 1 {
			return nil, errors.New("vault: current kek id is required when several keys are configured")
		}
		for id := range keys {
			current = id
		}
	}
	p := &LocalKEKProvider{
		current: current,
		ids:     make([]string, 0, len(keys)),
		keys:    make(map[string]localKEK, len(keys)),
	}
	for id, key := range keys {
		if !ValidKEKID(id) {
			// The id is not echoed: a malformed id is often misplaced key
			// material (for example "base64:id" instead of "id:base64").
			return nil, errors.New("vault: invalid kek id: must match ^[a-zA-Z0-9_-]{1,32}$")
		}
		if len(key) != KEKSize {
			return nil, fmt.Errorf("vault: kek %q must be %d bytes, got %d", id, KEKSize, len(key))
		}
		aead, err := newAEAD(key)
		if err != nil {
			return nil, fmt.Errorf("vault: init kek %q: %w", id, err)
		}
		p.keys[id] = localKEK{aead: aead, aad: []byte(dekAADPrefix + id)}
		p.ids = append(p.ids, id)
	}
	if _, ok := p.keys[current]; !ok {
		if !ValidKEKID(current) {
			return nil, errors.New("vault: current kek id is invalid: must match ^[a-zA-Z0-9_-]{1,32}$")
		}
		return nil, fmt.Errorf("vault: current kek %q is not configured", current)
	}
	slices.Sort(p.ids)
	return p, nil
}

// LoadLocalKEKProvider builds a provider from the SPINNERET_KEK_FILE,
// SPINNERET_KEKS and SPINNERET_KEK_CURRENT settings.
//
// file is a path (empty to skip) to a file of at most 1 MiB. Its content is
// either lines "id:base64" or a single bare base64 line, which gets the id
// "k1". Blank lines and lines starting with "#" are ignored, surrounding
// whitespace is trimmed and a leading UTF-8 byte order mark is skipped.
//
// keys is a comma-separated list "id:base64,id2:base64" (empty entries are
// ignored). Keys from both sources are merged; listing the same id twice is
// allowed only with identical key material.
//
// current defaults to the id of the last key listed, where entries of keys
// come after the file entries. Every key must decode to exactly 32 bytes
// (standard or URL-safe base64, padded or not).
func LoadLocalKEKProvider(file, keys, current string) (*LocalKEKProvider, error) {
	var entries []kekEntry
	defer func() {
		for _, e := range entries {
			clear(e.key)
		}
	}()

	if file = strings.TrimSpace(file); file != "" {
		data, err := readKEKFile(file)
		if err != nil {
			return nil, fmt.Errorf("vault: read kek file %q: %w", file, err)
		}
		fileEntries, err := parseKEKFile(data)
		clear(data)
		if err != nil {
			return nil, fmt.Errorf("vault: kek file %q: %w", file, err)
		}
		entries = append(entries, fileEntries...)
	}
	if strings.TrimSpace(keys) != "" {
		listEntries, err := parseKEKList(keys)
		if err != nil {
			return nil, fmt.Errorf("vault: kek list: %w", err)
		}
		entries = append(entries, listEntries...)
	}
	if len(entries) == 0 {
		return nil, errors.New("vault: no kek configured (set a kek file or a kek list)")
	}

	merged := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if prev, ok := merged[e.id]; ok && subtle.ConstantTimeCompare(prev, e.key) != 1 {
			return nil, fmt.Errorf("vault: kek %q is configured twice with different keys", e.id)
		}
		merged[e.id] = e.key
	}
	if current = strings.TrimSpace(current); current == "" {
		current = entries[len(entries)-1].id
	}
	return NewLocalKEKProvider(merged, current)
}

// GenerateKEK returns the standard base64 encoding of 32 random bytes,
// suitable for a KEK file line or SPINNERET_KEKS entry.
func GenerateKEK() (string, error) {
	key := make([]byte, KEKSize)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("vault: generate kek: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// CurrentID returns the id of the KEK used by Wrap.
func (p *LocalKEKProvider) CurrentID() string { return p.current }

// IDs returns a sorted copy of the configured KEK ids.
func (p *LocalKEKProvider) IDs() []string { return slices.Clone(p.ids) }

// Wrap encrypts a 32-byte DEK with the current KEK. The result is
// nonce || ciphertext || tag, authenticated with AAD "spinneret-dek:"+kekID.
func (p *LocalKEKProvider) Wrap(dek []byte) ([]byte, string, error) {
	if len(dek) != DEKSize {
		return nil, "", fmt.Errorf("vault: wrap dek: dek must be %d bytes, got %d", DEKSize, len(dek))
	}
	k, ok := p.keys[p.current]
	if !ok {
		return nil, "", fmt.Errorf("%w %q", ErrUnknownKEK, p.current)
	}
	return k.aead.Seal(nil, nil, dek, k.aad), p.current, nil
}

// Unwrap decrypts a DEK wrapped by the KEK kekID.
func (p *LocalKEKProvider) Unwrap(kekID string, wrapped []byte) ([]byte, error) {
	k, ok := p.keys[kekID]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownKEK, kekID)
	}
	if len(wrapped) < gcmOverhead {
		return nil, fmt.Errorf("%w: wrapped data key does not authenticate with kek %q", ErrDecrypt, kekID)
	}
	dek, err := k.aead.Open(nil, nil, wrapped, k.aad)
	if err != nil {
		return nil, fmt.Errorf("%w: wrapped data key does not authenticate with kek %q", ErrDecrypt, kekID)
	}
	return dek, nil
}

// newAEAD returns AES-256-GCM with a random 12-byte nonce prepended to the
// ciphertext by Seal and consumed by Open.
func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return aead, nil
}

// readKEKFile reads at most maxKEKFileSize bytes of the file at path.
func readKEKFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the KEK file path is operator configuration, not request input.
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxKEKFileSize+1))
	if err != nil {
		clear(data)
		return nil, err
	}
	if len(data) > maxKEKFileSize {
		clear(data)
		return nil, fmt.Errorf("file exceeds %d bytes", maxKEKFileSize)
	}
	return data, nil
}

type kekEntry struct {
	id  string
	key []byte
}

// parseKEKFile parses KEK file content. It works on bytes so that the caller
// can zero the buffer; error messages carry line numbers, never key material.
func parseKEKFile(data []byte) ([]kekEntry, error) {
	var (
		entries []kekEntry
		bare    bool
		lines   int
	)
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	for i, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		lines++
		lineNo := i + 1
		idPart, keyPart, hasID := bytes.Cut(line, []byte(":"))
		if !hasID {
			bare = true
			keyPart, idPart = line, []byte(DefaultFileKEKID)
		}
		e, err := newKEKEntry(string(bytes.TrimSpace(idPart)), bytes.TrimSpace(keyPart))
		if err != nil {
			clearEntries(entries)
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		entries = append(entries, e)
	}
	if bare && lines > 1 {
		clearEntries(entries)
		return nil, errors.New("a bare base64 key is only allowed as the single key of the file; use id:base64 lines")
	}
	return entries, nil
}

// parseKEKList parses "id:base64,id2:base64". Error messages carry entry
// positions, never key material.
func parseKEKList(list string) ([]kekEntry, error) {
	var entries []kekEntry
	for i, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, key, ok := strings.Cut(item, ":")
		if !ok {
			clearEntries(entries)
			return nil, fmt.Errorf("entry %d: expected id:base64", i+1)
		}
		e, err := newKEKEntry(strings.TrimSpace(id), []byte(strings.TrimSpace(key)))
		if err != nil {
			clearEntries(entries)
			return nil, fmt.Errorf("entry %d: %w", i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func newKEKEntry(id string, encoded []byte) (kekEntry, error) {
	if !ValidKEKID(id) {
		// Not echoed: a malformed id is often misplaced key material.
		return kekEntry{}, errors.New("invalid kek id: must match ^[a-zA-Z0-9_-]{1,32}$")
	}
	key, err := decodeKEK(encoded)
	if err != nil {
		return kekEntry{}, fmt.Errorf("kek %q: %w", id, err)
	}
	return kekEntry{id: id, key: key}, nil
}

var kekEncodings = []*base64.Encoding{
	base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
}

// decodeKEK decodes a base64 key that must yield exactly KEKSize bytes.
func decodeKEK(encoded []byte) ([]byte, error) {
	buf := make([]byte, len(encoded))
	defer clear(buf)
	for _, enc := range kekEncodings {
		n, err := enc.Decode(buf, encoded)
		if err == nil && n == KEKSize {
			key := make([]byte, KEKSize)
			copy(key, buf[:n])
			return key, nil
		}
	}
	return nil, fmt.Errorf("key must be base64 of exactly %d bytes", KEKSize)
}

func clearEntries(entries []kekEntry) {
	for _, e := range entries {
		clear(e.key)
	}
}
