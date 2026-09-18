package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"
)

// maxJSONDepth bounds the nesting depth of JSON values handled by this
// package. It protects the recursive encoders against cyclic Go values and
// pathological inputs.
const maxJSONDepth = 64

var errJSONTooDeep = fmt.Errorf("value is nested deeper than %d levels", maxJSONDepth)

// CanonicalJSON encodes v as deterministic JSON: object keys are sorted
// byte-wise, there is no insignificant whitespace, HTML characters are not
// escaped and numbers have a single representation (every number is encoded
// from its float64 value; integral values are written without an exponent or
// fraction, "-0" becomes "0", other values use the shortest representation
// that round-trips, with an exponent only below 1e-6). NaN and infinities are
// rejected.
//
// Supported inputs are the values produced by encoding/json (nil, bool,
// float64, json.Number, string, []any, map[string]any), the Go integer and
// float kinds, []string and map[string]string. Any other value is first
// marshaled with encoding/json and then canonicalized.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v, 0); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any, depth int) error {
	if depth > maxJSONDepth {
		return errJSONTooDeep
	}
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case string:
		writeJSONString(buf, t)
	case map[string]any:
		keys := sortedKeys(t)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k], depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case map[string]string:
		keys := sortedKeys(t)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			writeJSONString(buf, t[k])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e, depth+1); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case []string:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, e)
		}
		buf.WriteByte(']')
	default:
		if f, ok, err := numberValue(v); ok {
			if err != nil {
				return err
			}
			return writeFloat(buf, f)
		}
		return writeMarshaled(buf, v, depth)
	}
	return nil
}

// writeMarshaled canonicalizes an arbitrary Go value through encoding/json.
func writeMarshaled(buf *bytes.Buffer, v any, depth int) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %T: %w", v, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return fmt.Errorf("decode marshaled %T: %w", v, err)
	}
	return writeCanonical(buf, decoded, depth+1)
}

// numberValue converts the numeric Go kinds and json.Number to float64. The
// second result reports whether v is numeric at all.
func numberValue(v any) (float64, bool, error) {
	switch t := v.(type) {
	case float64:
		return t, true, finite(t)
	case float32:
		// Round-trip through the shortest float32 representation so that
		// float32(0.1) canonicalizes like the float64 literal 0.1.
		f, err := strconv.ParseFloat(strconv.FormatFloat(float64(t), 'g', -1, 32), 64)
		if err != nil {
			return 0, true, fmt.Errorf("invalid float32: %w", err)
		}
		return f, true, finite(f)
	case int:
		return float64(t), true, nil
	case int8:
		return float64(t), true, nil
	case int16:
		return float64(t), true, nil
	case int32:
		return float64(t), true, nil
	case int64:
		return float64(t), true, nil
	case uint:
		return float64(t), true, nil
	case uint8:
		return float64(t), true, nil
	case uint16:
		return float64(t), true, nil
	case uint32:
		return float64(t), true, nil
	case uint64:
		return float64(t), true, nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil {
			return 0, true, errors.New("invalid number")
		}
		return f, true, finite(f)
	default:
		return 0, false, nil
	}
}

func finite(f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return errors.New("number is not finite")
	}
	return nil
}

func writeFloat(buf *bytes.Buffer, f float64) error {
	if err := finite(f); err != nil {
		return err
	}
	buf.WriteString(formatNumber(f))
	return nil
}

// formatNumber formats a finite float64 like encoding/json, except that
// integral values never use an exponent (1e21 is written as
// "1000000000000000000000") and negative zero is written as "0".
func formatNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	format := byte('f')
	if f != math.Trunc(f) && math.Abs(f) < 1e-6 {
		format = 'e'
	}
	b := strconv.AppendFloat(make([]byte, 0, 24), f, format, -1, 64)
	if format == 'e' {
		// Clean up e-09 to e-9, as encoding/json does.
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return string(b)
}

const hexDigits = "0123456789abcdef"

// writeJSONString writes s as a JSON string literal without HTML escaping.
// Invalid UTF-8 is replaced by U+FFFD; U+2028 and U+2029 are escaped so the
// output is also valid JavaScript.
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' {
				i++
				continue
			}
			buf.WriteString(s[start:i])
			switch b {
			case '"', '\\':
				buf.WriteByte('\\')
				buf.WriteByte(b)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				buf.WriteString(`\u00`)
				buf.WriteByte(hexDigits[b>>4])
				buf.WriteByte(hexDigits[b&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			buf.WriteString(s[start:i])
			buf.WriteString(`\ufffd`)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			buf.WriteString(s[start:i])
			buf.WriteString(`\u202`)
			buf.WriteByte(hexDigits[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	buf.WriteString(s[start:])
	buf.WriteByte('"')
}

// errInvalidUTF8 reports a string that is not valid UTF-8 (it could not be
// delivered in a protobuf message).
var errInvalidUTF8 = errors.New("string is not valid UTF-8")

// normalizeJSONValue returns a deep copy of v using only the JSON value types
// nil, bool, float64, string, []any and map[string]any. It converts
// json.Number and the Go numeric kinds to float64, map[string]string to
// map[string]any and []string to []any. Other types and strings or object
// keys that are not valid UTF-8 are rejected.
func normalizeJSONValue(v any, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errJSONTooDeep
	}
	switch t := v.(type) {
	case nil, bool:
		return t, nil
	case string:
		if !utf8.ValidString(t) {
			return nil, errInvalidUTF8
		}
		return t, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if !utf8.ValidString(k) {
				return nil, errInvalidUTF8
			}
			ne, err := normalizeJSONValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = ne
		}
		return out, nil
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if !utf8.ValidString(k) || !utf8.ValidString(e) {
				return nil, errInvalidUTF8
			}
			out[k] = e
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("object key of type %T is not a string", k)
			}
			if !utf8.ValidString(ks) {
				return nil, errInvalidUTF8
			}
			ne, err := normalizeJSONValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[ks] = ne
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			ne, err := normalizeJSONValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	case []string:
		out := make([]any, len(t))
		for i, e := range t {
			if !utf8.ValidString(e) {
				return nil, errInvalidUTF8
			}
			out[i] = e
		}
		return out, nil
	default:
		f, ok, err := numberValue(v)
		if !ok {
			return nil, fmt.Errorf("unsupported value of type %T", v)
		}
		if err != nil {
			return nil, err
		}
		return f, nil
	}
}

// sortedKeys returns the keys of m in ascending byte-wise order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
