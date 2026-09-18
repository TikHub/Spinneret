// Package glob implements the minimal wildcard matching used by API token
// scopes (config group globs and secret path globs).
//
// Only two metacharacters exist: '*' matches any sequence of characters
// (including the empty sequence and '/'), and '?' matches exactly one rune.
// Every other character, including '[', ']' and '\\', is a literal. Matching
// runs in O(len(pattern)·len(s)) time in the worst case and never allocates,
// so hostile patterns or inputs cannot trigger exponential backtracking.
package glob

import (
	"strings"
	"unicode/utf8"
)

// Match reports whether s matches pattern in its entirety.
//
// Invalid UTF-8 bytes are treated as single one-byte runes on both sides, so
// '?' consumes exactly one invalid byte and literals compare byte-for-byte.
func Match(pattern, s string) bool {
	var (
		pi, si int
		// Position just after the most recent '*' in pattern and the position in
		// s that the star is currently assumed to extend to. -1 means no star seen.
		starPI = -1
		starSI = -1
	)
	for si < len(s) {
		if pi < len(pattern) {
			switch pattern[pi] {
			case '*':
				pi++
				starPI, starSI = pi, si
				continue
			case '?':
				_, w := utf8.DecodeRuneInString(s[si:])
				pi++
				si += w
				continue
			default:
				_, pw := utf8.DecodeRuneInString(pattern[pi:])
				_, sw := utf8.DecodeRuneInString(s[si:])
				if pw == sw && pattern[pi:pi+pw] == s[si:si+sw] {
					pi += pw
					si += sw
					continue
				}
			}
		}
		if starPI < 0 {
			return false
		}
		// Backtrack: let the last star swallow one more rune of s.
		_, w := utf8.DecodeRuneInString(s[starSI:])
		starSI += w
		pi, si = starPI, starSI
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// HasWildcard reports whether pattern contains a '*' or '?' metacharacter.
func HasWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}
