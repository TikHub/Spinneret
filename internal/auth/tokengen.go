package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
)

// API token format: "spn_" + base62 of 32 random bytes, left-padded to a fixed
// length so that every token has the same shape.
const (
	TokenPrefix = "spn_"
	// tokenRandomBytes is the entropy of a token.
	tokenRandomBytes = 32
	// tokenBodyLen is the base62 length of 2^256 - 1 (62^43 > 2^256).
	tokenBodyLen = 43
	// TokenLength is the total length of a plaintext token.
	TokenLength = len(TokenPrefix) + tokenBodyLen
	// TokenPrefixLength is the number of leading characters stored for identification.
	TokenPrefixLength = 12
)

// GenerateToken returns a new plaintext API token.
func GenerateToken() (string, error) {
	b := make([]byte, tokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	body := new(big.Int).SetBytes(b).Text(62)
	if len(body) < tokenBodyLen {
		body = strings.Repeat("0", tokenBodyLen-len(body)) + body
	}
	return TokenPrefix + body, nil
}

// HashToken returns the SHA-256 digest stored for a plaintext token.
func HashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// WellFormedToken reports whether s has the shape of a generated token, so
// malformed credentials are rejected without touching caches or the database.
func WellFormedToken(s string) bool {
	if len(s) != TokenLength || !strings.HasPrefix(s, TokenPrefix) {
		return false
	}
	for i := len(TokenPrefix); i < len(s); i++ {
		if !isBase62(s[i]) {
			return false
		}
	}
	return true
}

func isBase62(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
