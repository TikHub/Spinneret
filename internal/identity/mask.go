package identity

import (
	"unicode/utf8"
)

// MaskPrefix is the placeholder shown instead of hidden sensitive content.
const MaskPrefix = "••••"

// maskVisibleRunes is the number of trailing runes MaskString keeps.
const maskVisibleRunes = 4

// MaskString masks a sensitive string as "••••" followed by its last four
// characters. Strings of four characters or fewer are fully masked.
func MaskString(s string) string {
	n := utf8.RuneCountInString(s)
	if n <= maskVisibleRunes {
		return MaskPrefix
	}
	cut := len(s)
	for range maskVisibleRunes {
		_, size := utf8.DecodeLastRuneInString(s[:cut])
		cut -= size
	}
	return MaskPrefix + s[cut:]
}

// Mask returns a copy of payload suitable for display: sensitive string,
// secret_ref, number and bool fields are masked with MaskString (non-strings
// become "••••"), sensitive cookie_map values are masked per cookie and
// sensitive json values become "••••". Non-sensitive values are deep-copied
// (cookie maps as map[string]any). Keys that are not declared fields are
// fully masked because their sensitivity is unknown. Null values stay null.
func (c *CompiledType) Mask(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	out := make(map[string]any, len(payload))
	for name, v := range payload {
		f, declared := c.Spec.Fields[name]
		switch {
		case v == nil:
			out[name] = nil
		case !declared:
			out[name] = MaskPrefix
		case f.Sensitive:
			out[name] = maskSensitive(f.Type, v)
		default:
			out[name] = copyForDisplay(f.Type, v)
		}
	}
	return out
}

func maskSensitive(t FieldType, v any) any {
	switch t {
	case FieldCookieMap:
		cm, err := ParseCookieMap(v)
		if err != nil {
			return MaskPrefix
		}
		out := make(map[string]any, len(cm))
		for name, value := range cm {
			out[name] = MaskString(value)
		}
		return out
	case FieldJSON:
		return MaskPrefix
	default:
		if s, ok := v.(string); ok {
			return MaskString(s)
		}
		return MaskPrefix
	}
}

func copyForDisplay(t FieldType, v any) any {
	if t == FieldCookieMap {
		if cm, err := ParseCookieMap(v); err == nil {
			out := make(map[string]any, len(cm))
			for name, value := range cm {
				out[name] = value
			}
			return out
		}
	}
	if nv, err := normalizeJSONValue(v, 0); err == nil {
		return nv
	}
	return MaskPrefix
}
