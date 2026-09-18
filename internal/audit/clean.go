package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// replacementChar replaces NUL characters and invalid UTF-8 in stored values,
// keeping a visible trace of the rejected input.
const replacementChar = "\uFFFD"

// escapedNUL is the JSON escape of U+0000, which jsonb rejects.
var escapedNUL = []byte("\\u0000")

// emptyDetails is the stored details of an entry without details.
var emptyDetails = []byte("{}")

// cleanText returns s as valid UTF-8 without NUL characters: every run of
// invalid bytes and every NUL is replaced with U+FFFD. PostgreSQL rejects
// both in text values. Clean strings are returned without allocating.
func cleanText(s string) string {
	valid := utf8.ValidString(s)
	if valid && strings.IndexByte(s, 0) < 0 {
		return s
	}
	if !valid {
		s = strings.ToValidUTF8(s, replacementChar)
	}
	return strings.ReplaceAll(s, "\x00", replacementChar)
}

// encodeDetails encodes details as a JSON object that jsonb accepts. JSON
// encoding already coerces strings to valid UTF-8; escaped NUL characters are
// replaced by decoding the document generically, cleaning every string
// (including object keys) and encoding it again. Details that cannot be
// encoded are stored as an empty object instead of failing the entry.
func encodeDetails(details map[string]any) []byte {
	if len(details) == 0 {
		return emptyDetails
	}
	b, err := json.Marshal(details)
	if err != nil {
		return emptyDetails
	}
	if !bytes.Contains(b, escapedNUL) {
		return b
	}
	// The match may be a literal backslash followed by "u0000"; the generic
	// round trip keeps such content unchanged.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return emptyDetails
	}
	cleaned, err := json.Marshal(cleanJSON(doc))
	if err != nil {
		return emptyDetails
	}
	return cleaned
}

// cleanJSON returns a copy of a generically decoded JSON value with every
// string and object key passed through cleanText.
func cleanJSON(v any) any {
	switch t := v.(type) {
	case string:
		return cleanText(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[cleanText(k)] = cleanJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = cleanJSON(val)
		}
		return out
	default:
		return v
	}
}
