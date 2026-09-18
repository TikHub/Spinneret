package identity

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

// CompiledType is an immutable, validated identity type ready for payload
// normalization and credential rendering. It is safe for concurrent use.
type CompiledType struct {
	ID      string
	Name    string
	SiteID  string
	Client  string
	Version int
	// Spec is the effective spec (defaults applied). It must not be modified.
	Spec *TypeSpec

	schema     *jsonschema.Schema
	schemaJSON []byte
	plan       *renderPlan
	uniqueBy   []fieldPath
	required   []string
	sensitive  bool
}

// Compile validates spec (after applying defaults), generates and compiles
// its payload JSON Schema and pre-parses its delivery templates. The spec is
// deep-copied, so later changes to it do not affect the compiled type.
func Compile(id, siteID string, version int, spec *TypeSpec) (*CompiledType, error) {
	if spec == nil {
		return nil, invalidf("compile identity type %s: spec is nil", id)
	}
	if version < 0 {
		return nil, invalidf("compile identity type %s: version %d is negative", id, version)
	}
	eff := spec.WithDefaults()
	uniqueBy, plan, err := eff.check()
	if err != nil {
		return nil, err
	}
	schemaJSON, err := payloadSchema(eff)
	if err != nil {
		return nil, fmt.Errorf("compile identity type %s: %w", id, err)
	}
	schema, err := compileSchema(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("compile identity type %s: %w", id, err)
	}
	ct := &CompiledType{
		ID:         id,
		Name:       eff.Name,
		SiteID:     siteID,
		Client:     eff.Client,
		Version:    version,
		Spec:       eff,
		schema:     schema,
		schemaJSON: schemaJSON,
		plan:       plan,
		uniqueBy:   uniqueBy,
	}
	for _, name := range sortedKeys(eff.Fields) {
		f := eff.Fields[name]
		if f.Required {
			ct.required = append(ct.required, name)
		}
		if f.Sensitive {
			ct.sensitive = true
		}
	}
	return ct, nil
}

// SchemaJSON returns a copy of the payload JSON Schema in canonical JSON.
func (c *CompiledType) SchemaJSON() []byte { return slices.Clone(c.schemaJSON) }

// HasSensitive reports whether any field is marked sensitive.
func (c *CompiledType) HasSensitive() bool { return c.sensitive }

// Normalize validates payload and returns a normalized copy: JSON null values
// are dropped (treated as absent), cookie_map values become map[string]string
// (see ParseCookieMap), number fields become float64 (numeric strings such as
// "123" are accepted), bool fields become bool (the strings accepted by
// strconv.ParseBool, such as "true" and "0") and json values are deep-copied
// with numbers as float64. Strings, cookie names and values and json strings
// and keys must be valid UTF-8 so that rendered credentials fit protobuf
// messages. The result is then validated against the payload JSON Schema.
// Every problem is reported, prefixed with its field name, in one
// invalid_argument error.
func (c *CompiledType) Normalize(payload map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(payload))
	var errs problems
	failed := make(map[string]struct{})
	for _, name := range sortedKeys(payload) {
		f, ok := c.Spec.Fields[name]
		if !ok {
			errs.addf("%q: unknown field", truncate(name))
			continue
		}
		raw := payload[name]
		if raw == nil {
			continue
		}
		v, err := normalizeField(f.Type, raw)
		if err != nil {
			failed[name] = struct{}{}
			errs.addf("%s: %s", name, errorMessage(err))
			continue
		}
		out[name] = v
	}
	for _, name := range c.required {
		if _, bad := failed[name]; bad {
			continue
		}
		if _, ok := out[name]; !ok {
			errs.addf("%s: required field is missing", name)
		}
	}
	if len(errs) == 0 {
		if err := c.schema.Validate(schemaView(out)); err != nil {
			errs = append(errs, schemaErrorMessages(err)...)
		}
	}
	if len(errs) > 0 {
		return nil, invalidf("invalid payload for identity type %q: %s", c.Name, errs.join())
	}
	return out, nil
}

// normalizeField coerces one non-nil field value.
func normalizeField(t FieldType, v any) (any, error) {
	switch t {
	case FieldString:
		s, ok := v.(string)
		switch {
		case !ok:
			return nil, errors.New("expected a string")
		case !utf8.ValidString(s):
			return nil, errInvalidUTF8
		}
		return s, nil
	case FieldNumber:
		return coerceNumber(v)
	case FieldBool:
		return coerceBool(v)
	case FieldCookieMap:
		return ParseCookieMap(v)
	case FieldSecretRef:
		s, ok := v.(string)
		if !ok || !ValidSecretRefPath(s) {
			return nil, fmt.Errorf(`expected a secret path matching %s without empty, "." or ".." segments`, SecretPathPattern)
		}
		return s, nil
	default: // FieldJSON
		return normalizeJSONValue(v, 0)
	}
}

// errorMessage returns the client-facing message of err.
func errorMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

var numericString = regexp.MustCompile(`^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$`)

// coerceNumber converts numbers and numeric strings to a finite float64.
func coerceNumber(v any) (float64, error) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if !numericString.MatchString(s) {
			return 0, errors.New("expected a number")
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || finite(f) != nil {
			return 0, errors.New("number is out of range")
		}
		return f, nil
	}
	f, ok, err := numberValue(v)
	switch {
	case !ok:
		return 0, errors.New("expected a number")
	case err != nil:
		return 0, errors.New("number is not finite")
	}
	return f, nil
}

// coerceBool converts booleans and the strings accepted by strconv.ParseBool.
func coerceBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return false, errors.New("expected a boolean")
		}
		return b, nil
	default:
		return false, errors.New("expected a boolean")
	}
}

// schemaView converts a normalized payload into the value types understood
// by the JSON Schema validator.
func schemaView(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if cm, ok := v.(map[string]string); ok {
			m := make(map[string]any, len(cm))
			for name, value := range cm {
				m[name] = value
			}
			out[k] = m
			continue
		}
		out[k] = v
	}
	return out
}

// UniqueKey returns the canonical JSON array of the unique_by values of
// payload, used (HMAC'd) for deduplication. Values are coerced like
// Normalize, so "123" and 123 produce the same key for a number field.
// A missing, null or empty value is an error.
func (c *CompiledType) UniqueKey(payload map[string]any) ([]byte, error) {
	values := make([]any, 0, len(c.uniqueBy))
	for i := range c.uniqueBy {
		p := &c.uniqueBy[i]
		v, err := uniqueValue(payload, p)
		if err != nil {
			return nil, invalidf("unique_by path %q: %s", p.raw, errorMessage(err))
		}
		if isEmptyValue(v) {
			return nil, invalidf("unique_by path %q is missing from the payload", p.raw)
		}
		values = append(values, v)
	}
	key, err := CanonicalJSON(values)
	if err != nil {
		return nil, invalidf("unique key of identity type %q: %s", c.Name, err)
	}
	return key, nil
}

func uniqueValue(payload map[string]any, p *fieldPath) (any, error) {
	raw, ok := payload[p.field]
	if !ok || raw == nil {
		return nil, nil
	}
	switch p.ftype {
	case FieldCookieMap:
		cm, err := ParseCookieMap(raw)
		if err != nil {
			return nil, err
		}
		if len(p.sub) == 0 {
			return cm, nil
		}
		if v, ok := cm[p.sub[0]]; ok {
			return v, nil
		}
		return nil, nil
	case FieldJSON:
		return normalizeJSONValue(lookupJSON(raw, p.sub), 0)
	default:
		return normalizeField(p.ftype, raw)
	}
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]string:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	default:
		return false
	}
}
