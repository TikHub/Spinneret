package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// Password policy.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 1024
)

// Bounds accepted when verifying stored hashes, so that a corrupted or hostile
// hash string cannot make verification allocate unbounded memory or CPU.
const (
	maxArgon2Memory      = 256 * 1024 // KiB
	maxArgon2Iterations  = 16
	maxArgon2Parallelism = 16
	minArgon2SaltLen     = 8
	maxArgon2SaltLen     = 64
	minArgon2KeyLen      = 16
	maxArgon2KeyLen      = 128
)

// Argon2Params are Argon2id cost parameters.
type Argon2Params struct {
	// Memory in KiB.
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params returns the production parameters (spec §3.3):
// m=64 MiB, t=3, p=2, 16-byte salt, 32-byte key.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
}

// validate checks that the parameters are within the accepted bounds.
func (p Argon2Params) validate() error {
	switch {
	case p.Memory < 8*uint32(p.Parallelism) || p.Memory > maxArgon2Memory:
		return fmt.Errorf("argon2 memory %d KiB out of range", p.Memory)
	case p.Iterations < 1 || p.Iterations > maxArgon2Iterations:
		return fmt.Errorf("argon2 iterations %d out of range", p.Iterations)
	case p.Parallelism < 1 || p.Parallelism > maxArgon2Parallelism:
		return fmt.Errorf("argon2 parallelism %d out of range", p.Parallelism)
	case p.SaltLength < minArgon2SaltLen || p.SaltLength > maxArgon2SaltLen:
		return fmt.Errorf("argon2 salt length %d out of range", p.SaltLength)
	case p.KeyLength < minArgon2KeyLen || p.KeyLength > maxArgon2KeyLen:
		return fmt.Errorf("argon2 key length %d out of range", p.KeyLength)
	}
	return nil
}

// ErrMalformedHash reports a stored password hash that cannot be parsed.
var ErrMalformedHash = errors.New("auth: malformed password hash")

// ValidatePassword enforces the password policy: at least 10 and at most
// 1024 characters of valid UTF-8. It returns an apperr InvalidArgument error.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) {
		return apperr.InvalidArgument("", "password must be valid UTF-8")
	}
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength {
		return apperr.InvalidArgument("", "password must be at least %d characters", MinPasswordLength)
	}
	if n > MaxPasswordLength {
		return apperr.InvalidArgument("", "password must be at most %d characters", MaxPasswordLength)
	}
	return nil
}

// HashPassword hashes password with DefaultArgon2Params and returns a PHC
// string "$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>".
func HashPassword(password string) (string, error) {
	return HashPasswordWithParams(password, DefaultArgon2Params())
}

// HashPasswordWithParams hashes password with the given parameters.
func HashPasswordWithParams(password string, p Argon2Params) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-encoded Argon2id
// hash. The comparison is constant time. A malformed hash yields
// ErrMalformedHash.
func VerifyPassword(password, encoded string) (bool, error) {
	p, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// decodeHash parses "$argon2id$v=19$m=<m>,t=<t>,p=<p>$<salt>$<key>".
func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Argon2Params{}, nil, nil, fmt.Errorf("%w: unsupported version", ErrMalformedHash)
	}
	var p Argon2Params
	for _, kv := range strings.Split(parts[3], ",") {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			return Argon2Params{}, nil, nil, ErrMalformedHash
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return Argon2Params{}, nil, nil, ErrMalformedHash
		}
		switch name {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Iterations = uint32(n)
		case "p":
			if n > maxArgon2Parallelism {
				return Argon2Params{}, nil, nil, fmt.Errorf("%w: parallelism out of range", ErrMalformedHash)
			}
			p.Parallelism = uint8(n)
		default:
			return Argon2Params{}, nil, nil, ErrMalformedHash
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	if err := p.validate(); err != nil {
		return Argon2Params{}, nil, nil, fmt.Errorf("%w: %w", ErrMalformedHash, err)
	}
	return p, salt, key, nil
}
