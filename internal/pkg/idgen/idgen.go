// Package idgen generates prefixed, time-ordered identifiers (UUIDv7) and
// encodes/decodes lease IDs, which embed the site hot-state key and the
// report stream shard so that reports can be routed without any lookup.
package idgen

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Entity ID prefixes.
const (
	Tenant        = "ten"
	Namespace     = "ns"
	Site          = "sit"
	EndpointGroup = "eg"
	URIRule       = "uri"
	IdentityType  = "ity"
	Identity      = "idt"
	Account       = "acc"
	Proxy         = "pxy"
	Policy        = "pol"
	PolicyBinding = "pbd"
	ConfigItem    = "cfg"
	Secret        = "sec"
	Token         = "tok"
	User          = "usr"
	RoleBinding   = "rb"
	StateEvent    = "evt"
	Audit         = "aud"
	BreakerEvent  = "brk"
	Channel       = "nch"
	Alert         = "alt"
	RiskEvent     = "rsk"
	Lease         = "lse"
)

const hexLen = 32

// ErrInvalidLeaseID is returned by ParseLeaseID for malformed lease IDs.
var ErrInvalidLeaseID = errors.New("idgen: invalid lease id")

// New returns "<prefix>_<32 lowercase hex of a UUIDv7>".
func New(prefix string) string {
	return prefix + "_" + newHex()
}

func newHex() string {
	u, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 only fails when the system random source fails, which
		// is unrecoverable for a security-sensitive service.
		panic(fmt.Sprintf("idgen: generate uuidv7: %v", err))
	}
	var buf [hexLen]byte
	hex.Encode(buf[:], u[:])
	return string(buf[:])
}

// Valid reports whether id has the given prefix followed by 32 lowercase hex characters.
func Valid(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix+"_") {
		return false
	}
	rest := id[len(prefix)+1:]
	return len(rest) == hexLen && isLowerHex(rest)
}

// Time returns the creation time embedded in the UUIDv7 part of id.
func Time(id string) (time.Time, bool) {
	i := strings.IndexByte(id, '_')
	if i < 0 || len(id) < i+1+hexLen {
		return time.Time{}, false
	}
	h := id[i+1 : i+1+hexLen]
	if !isLowerHex(h) {
		return time.Time{}, false
	}
	b, err := hex.DecodeString(h[:12])
	if err != nil {
		return time.Time{}, false
	}
	var ms int64
	for _, c := range b {
		ms = ms<<8 | int64(c)
	}
	return time.UnixMilli(ms).UTC(), true
}

// LeaseRef is the routing information embedded in a lease ID.
type LeaseRef struct {
	SiteKey int64
	Shard   int
}

// LeasePrefix returns a fresh lease ID prefix "lse_<32hex>_<siteKey base36>_".
// The acquire script appends the two-hex-digit shard.
func LeasePrefix(siteKey int64) string {
	return Lease + "_" + newHex() + "_" + strconv.FormatInt(siteKey, 36) + "_"
}

// FormatShard formats a shard number as two lowercase hex digits.
func FormatShard(shard int) string {
	return fmt.Sprintf("%02x", shard)
}

// ParseLeaseID decodes the site key and shard of a lease ID.
func ParseLeaseID(id string) (LeaseRef, error) {
	// lse_ + 32 hex + _ + site36 (>=1) + _ + 2 hex
	if len(id) < len(Lease)+1+hexLen+1+1+1+2 || !strings.HasPrefix(id, Lease+"_") {
		return LeaseRef{}, ErrInvalidLeaseID
	}
	parts := strings.Split(id[len(Lease)+1:], "_")
	if len(parts) != 3 || len(parts[0]) != hexLen || !isLowerHex(parts[0]) || len(parts[2]) != 2 || !isLowerHex(parts[2]) {
		return LeaseRef{}, ErrInvalidLeaseID
	}
	if parts[1] == "" || len(parts[1]) > 13 {
		return LeaseRef{}, ErrInvalidLeaseID
	}
	siteKey, err := strconv.ParseInt(parts[1], 36, 64)
	if err != nil || siteKey <= 0 || strconv.FormatInt(siteKey, 36) != parts[1] {
		return LeaseRef{}, ErrInvalidLeaseID
	}
	shard, err := strconv.ParseInt(parts[2], 16, 32)
	if err != nil {
		return LeaseRef{}, ErrInvalidLeaseID
	}
	return LeaseRef{SiteKey: siteKey, Shard: int(shard)}, nil
}

// ValidReportID reports whether a client-supplied report ID is acceptable:
// 1 to 64 characters of [A-Za-z0-9_.:-].
func ValidReportID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == ':' || c == '-':
		default:
			return false
		}
	}
	return true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
