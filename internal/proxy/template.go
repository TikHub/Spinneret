package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Session template placeholders.
const (
	PlaceholderUsername     = "username"
	PlaceholderPassword     = "password"
	PlaceholderIdentityID   = "identity_id"
	PlaceholderIdentityHash = "identity_hash"
	PlaceholderLeaseID      = "lease_id"
	PlaceholderRandom       = "random"
)

// identityHashChars is the length of {identity_hash}.
const identityHashChars = 12

// randomBytes is the number of random bytes behind {random} (8 hex characters).
const randomBytes = 4

// templateSegment is a literal text or a placeholder name.
type templateSegment struct {
	literal     string
	placeholder string
}

// parseTemplate splits a session template into segments. Placeholders are
// "{name}" with a known name; braces cannot be escaped.
func parseTemplate(tpl string) ([]templateSegment, error) {
	var segs []templateSegment
	rest := tpl
	for rest != "" {
		open := strings.IndexAny(rest, "{}")
		if open < 0 {
			segs = append(segs, templateSegment{literal: rest})
			break
		}
		if rest[open] == '}' {
			return nil, fmt.Errorf("session_template has an unmatched '}'")
		}
		if open > 0 {
			segs = append(segs, templateSegment{literal: rest[:open]})
		}
		end := strings.IndexAny(rest[open+1:], "{}")
		if end < 0 || rest[open+1+end] != '}' {
			return nil, fmt.Errorf("session_template has an unterminated placeholder")
		}
		name := rest[open+1 : open+1+end]
		switch name {
		case PlaceholderUsername, PlaceholderPassword, PlaceholderIdentityID,
			PlaceholderIdentityHash, PlaceholderLeaseID, PlaceholderRandom:
		default:
			return nil, fmt.Errorf("session_template has unknown placeholder {%s}", truncate(name, 32))
		}
		segs = append(segs, templateSegment{placeholder: name})
		rest = rest[open+2+end:]
	}
	return segs, nil
}

// ValidateSessionTemplate checks a session template. The empty template is valid.
func ValidateSessionTemplate(tpl string) error {
	if len(tpl) > MaxSessionTemplateLength {
		return fmt.Errorf("session_template must be at most %d bytes", MaxSessionTemplateLength)
	}
	if err := checkText("session_template", tpl, MaxSessionTemplateLength); err != nil {
		return err
	}
	_, err := parseTemplate(tpl)
	return err
}

// templateVars are the values substituted into a session template.
type templateVars struct {
	username   string
	password   string
	identityID string
	leaseID    string
}

// IdentityHash returns the first 12 hex characters of sha256(identityID), the
// value of {identity_hash}; empty for an empty identity.
func IdentityHash(identityID string) string {
	if identityID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identityID))
	return hex.EncodeToString(sum[:])[:identityHashChars]
}

// renderTemplate substitutes the placeholders of pre-parsed segments.
func renderTemplate(segs []templateSegment, v templateVars) (string, error) {
	var b strings.Builder
	for _, s := range segs {
		switch s.placeholder {
		case "":
			b.WriteString(s.literal)
		case PlaceholderUsername:
			b.WriteString(v.username)
		case PlaceholderPassword:
			b.WriteString(v.password)
		case PlaceholderIdentityID:
			b.WriteString(v.identityID)
		case PlaceholderIdentityHash:
			b.WriteString(IdentityHash(v.identityID))
		case PlaceholderLeaseID:
			b.WriteString(v.leaseID)
		case PlaceholderRandom:
			var buf [randomBytes]byte
			if _, err := rand.Read(buf[:]); err != nil {
				return "", fmt.Errorf("render session template: %w", err)
			}
			b.WriteString(hex.EncodeToString(buf[:]))
		}
	}
	return b.String(), nil
}

// truncate shortens s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
