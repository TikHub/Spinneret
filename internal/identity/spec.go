package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// FieldType is the type of a payload field.
type FieldType string

// Field types.
const (
	FieldString    FieldType = "string"
	FieldNumber    FieldType = "number"
	FieldBool      FieldType = "bool"
	FieldCookieMap FieldType = "cookie_map"
	FieldJSON      FieldType = "json"
	FieldSecretRef FieldType = "secret_ref"
)

// Valid reports whether t is a known field type.
func (t FieldType) Valid() bool {
	switch t {
	case FieldString, FieldNumber, FieldBool, FieldCookieMap, FieldJSON, FieldSecretRef:
		return true
	default:
		return false
	}
}

// hasSubKeys reports whether paths may address values inside a field.
func (t FieldType) hasSubKeys() bool { return t == FieldCookieMap || t == FieldJSON }

// Activation modes.
const (
	// ActivationProbe imports identities as pending; they become active after
	// their first successful use.
	ActivationProbe = "probe"
	// ActivationImmediate imports identities as active.
	ActivationImmediate = "immediate"
)

// Delivery segments of a Credential.
const (
	SegmentCookies      = "cookies"
	SegmentCookieHeader = "cookie_header"
	SegmentHeaders      = "headers"
	SegmentQuery        = "query"
	SegmentJSON         = "json"
	SegmentValues       = "values"
)

// Spec limits.
const (
	// MaxFields is the maximum number of fields of an identity type.
	MaxFields = 128
	// MaxUniqueBy is the maximum number of unique_by paths.
	MaxUniqueBy = 16
	// MaxTemplateBytes is the maximum length of one delivery template string.
	MaxTemplateBytes = 8 << 10
	// MaxDeliverEntries is the maximum number of entries of one delivery map.
	MaxDeliverEntries = 256
	// MaxDescriptionBytes is the maximum length of a description.
	MaxDescriptionBytes = 2048
)

var (
	typeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	// siteNamePattern and clientNamePattern mirror the site admin API
	// (CreateSiteRequest.name and clients) so every site and client that can
	// exist can also carry identity types.
	siteNamePattern   = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]{0,63}$`)
	clientNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	fieldNamePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	subKeyPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	secretPathPattern = regexp.MustCompile(SecretPathPattern)
)

// SecretPathPattern is the regular expression a secret_ref value must match.
// Values must also be canonical namespace-relative paths (ValidSecretRefPath).
const SecretPathPattern = `^[a-z0-9][a-z0-9_./-]{0,255}$` //nolint:gosec // G101 false positive: a path syntax, not a credential

// ValidSecretRefPath reports whether path is a valid secret_ref value: it
// matches SecretPathPattern and is a canonical namespace-relative vault path
// (no empty, "." or ".." segments and no trailing '/'), so a reference can
// only name a secret of the identity's own namespace and cannot alias another
// path through which authorization globs would be matched.
func ValidSecretRefPath(path string) bool {
	if !secretPathPattern.MatchString(path) {
		return false
	}
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// SecretRefs returns the distinct secret paths referenced by the secret_ref
// fields of a normalized payload, sorted. Values that are not strings or are
// empty are skipped.
func (c *CompiledType) SecretRefs(payload map[string]any) []string {
	var out []string
	for _, path := range c.SecretRefFields(payload) {
		if !slices.Contains(out, path) {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

// SecretRefFields returns the secret paths referenced by the secret_ref
// fields of a normalized payload, keyed by field name. Values that are not
// strings or are empty are skipped.
func (c *CompiledType) SecretRefFields(payload map[string]any) map[string]string {
	out := make(map[string]string)
	for name, f := range c.Spec.Fields {
		if f.Type != FieldSecretRef {
			continue
		}
		if path, ok := payload[name].(string); ok && path != "" {
			out[name] = path
		}
	}
	return out
}

// reservedFieldNames cannot be used as field names because they are
// reserved keys of the import formats.
var reservedFieldNames = []string{"payload", "_account", "_region", "_tags", "_labels"}

// FieldSpec declares one payload field.
type FieldSpec struct {
	Type        FieldType `json:"type" yaml:"type"`
	Required    bool      `json:"required,omitempty" yaml:"required,omitempty"`
	Sensitive   bool      `json:"sensitive,omitempty" yaml:"sensitive,omitempty"`
	Description string    `json:"description,omitempty" yaml:"description,omitempty"`
}

// TypeSpec is the declarative definition of an identity type (design doc
// section 5.1). Deliver maps segment names (cookies, cookie_header, headers,
// query, json, values) to templates.
type TypeSpec struct {
	Name        string               `json:"name" yaml:"name"`
	Site        string               `json:"site" yaml:"site"`
	Client      string               `json:"client" yaml:"client"`
	Description string               `json:"description,omitempty" yaml:"description,omitempty"`
	Fields      map[string]FieldSpec `json:"fields" yaml:"fields"`
	UniqueBy    []string             `json:"unique_by,omitempty" yaml:"unique_by,omitempty"`
	Activation  string               `json:"activation,omitempty" yaml:"activation,omitempty"`
	Deliver     map[string]any       `json:"deliver,omitempty" yaml:"deliver,omitempty"`
}

// ParseTypeYAML strictly decodes a YAML identity type (unknown keys are
// rejected), applies defaults and validates the result.
func ParseTypeYAML(data []byte) (*TypeSpec, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, invalidf("identity type spec is empty")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var spec TypeSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, invalidf("parse identity type yaml: %s", yamlErrorMessage(err)).WithCause(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidf("parse identity type yaml: expected a single YAML document")
	}
	return finishParse(&spec)
}

// ParseTypeJSON strictly decodes a JSON identity type (unknown keys are
// rejected), applies defaults and validates the result.
func ParseTypeJSON(data []byte) (*TypeSpec, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, invalidf("identity type spec is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var spec TypeSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, invalidf("parse identity type json: %v", err).WithCause(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidf("parse identity type json: unexpected data after the top-level object")
	}
	if err := checkExactJSONKeys(data); err != nil {
		return nil, err
	}
	return finishParse(&spec)
}

// Keys accepted by ParseTypeJSON, matched case-sensitively.
var (
	typeSpecJSONKeys  = []string{"name", "site", "client", "description", "fields", "unique_by", "activation", "deliver"}
	fieldSpecJSONKeys = []string{"type", "required", "sensitive", "description"}
)

// checkExactJSONKeys rejects keys that encoding/json matched to a struct field
// case-insensitively ("Name", "UNIQUE_BY"): specs must use the documented
// spelling, exactly like the YAML decoder requires. data has already been
// decoded successfully into a TypeSpec.
func checkExactJSONKeys(data []byte) error {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return invalidf("parse identity type json: %v", err).WithCause(err)
	}
	for _, key := range sortedKeys(top) {
		if !slices.Contains(typeSpecJSONKeys, key) {
			return invalidf("parse identity type json: unknown field %q", truncate(key))
		}
	}
	var fields map[string]map[string]json.RawMessage
	if raw, ok := top["fields"]; ok {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return invalidf("parse identity type json: fields: %v", err).WithCause(err)
		}
	}
	for _, name := range sortedKeys(fields) {
		for _, key := range sortedKeys(fields[name]) {
			if !slices.Contains(fieldSpecJSONKeys, key) {
				return invalidf("parse identity type json: fields.%s: unknown field %q", truncate(name), truncate(key))
			}
		}
	}
	return nil
}

func finishParse(spec *TypeSpec) (*TypeSpec, error) {
	out := spec.WithDefaults()
	if err := out.Validate(); err != nil {
		return nil, err
	}
	return out, nil
}

func yamlErrorMessage(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return strings.Join(te.Errors, "; ")
	}
	return err.Error()
}

// MarshalTypeYAML encodes s as YAML with keys in declaration order and the
// field declarations and unique_by in flow style, like the design doc
// examples.
func MarshalTypeYAML(s *TypeSpec) ([]byte, error) {
	if s == nil {
		return nil, errors.New("marshal identity type yaml: spec is nil")
	}
	// The YAML encoder panics on values it cannot represent, so the delivery
	// templates are converted to plain JSON value types first.
	out := *s
	if s.Deliver != nil {
		out.Deliver = make(map[string]any, len(s.Deliver))
		for seg, v := range s.Deliver {
			nv, err := normalizeJSONValue(v, 0)
			if err != nil {
				return nil, fmt.Errorf("marshal identity type yaml: deliver.%s: %w", seg, err)
			}
			out.Deliver[seg] = nv
		}
	}
	var node yaml.Node
	if err := node.Encode(&out); err != nil {
		return nil, fmt.Errorf("marshal identity type yaml: %w", err)
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		switch value := node.Content[i+1]; node.Content[i].Value {
		case "fields":
			for j := 1; j < len(value.Content); j += 2 {
				value.Content[j].Style = yaml.FlowStyle
			}
		case "unique_by":
			value.Style = yaml.FlowStyle
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, fmt.Errorf("marshal identity type yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("marshal identity type yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// MarshalTypeJSON encodes s as compact JSON without HTML escaping. Map keys
// are sorted, so equal specs produce equal bytes.
func MarshalTypeJSON(s *TypeSpec) ([]byte, error) {
	if s == nil {
		return nil, errors.New("marshal identity type json: spec is nil")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, fmt.Errorf("marshal identity type json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// WithDefaults returns a deep copy of s with defaults applied: activation
// "probe" when empty and unique_by set to the sorted required fields when
// empty. Delivery templates are converted to JSON value types (YAML maps with
// non-string keys and unsupported values are kept for Validate to report).
func (s *TypeSpec) WithDefaults() *TypeSpec {
	if s == nil {
		return nil
	}
	out := &TypeSpec{
		Name:        s.Name,
		Site:        s.Site,
		Client:      s.Client,
		Description: s.Description,
		UniqueBy:    slices.Clone(s.UniqueBy),
		Activation:  s.Activation,
	}
	if s.Fields != nil {
		out.Fields = make(map[string]FieldSpec, len(s.Fields))
		for name, f := range s.Fields {
			out.Fields[name] = f
		}
	}
	if s.Deliver != nil {
		out.Deliver = make(map[string]any, len(s.Deliver))
		for seg, v := range s.Deliver {
			if nv, err := normalizeJSONValue(v, 0); err == nil {
				out.Deliver[seg] = nv
			} else {
				out.Deliver[seg] = v
			}
		}
	}
	if out.Activation == "" {
		out.Activation = ActivationProbe
	}
	if len(out.UniqueBy) == 0 {
		out.UniqueBy = defaultUniqueBy(out.Fields)
	}
	return out
}

func defaultUniqueBy(fields map[string]FieldSpec) []string {
	var out []string
	for _, name := range sortedKeys(fields) {
		if fields[name].Required {
			out = append(out, name)
		}
	}
	return out
}

// Validate checks the spec and reports every problem found in one
// invalid_argument error. Empty activation and unique_by are validated as
// their defaults (see WithDefaults).
func (s *TypeSpec) Validate() error {
	_, _, err := s.check()
	return err
}

// maxReportedProblems bounds the number of messages joined into one error.
const maxReportedProblems = 32

// problems collects validation messages.
type problems []string

func (p *problems) addf(format string, args ...any) {
	*p = append(*p, fmt.Sprintf(format, args...))
}

// join returns the messages separated by "; ", truncated after
// maxReportedProblems entries.
func (p problems) join() string {
	if len(p) <= maxReportedProblems {
		return strings.Join(p, "; ")
	}
	return fmt.Sprintf("%s; and %d more problems", strings.Join(p[:maxReportedProblems], "; "), len(p)-maxReportedProblems)
}

// check validates the spec and returns the parsed unique_by paths and the
// delivery render plan.
func (s *TypeSpec) check() ([]fieldPath, *renderPlan, error) {
	if s == nil {
		return nil, nil, invalidf("identity type spec is nil")
	}
	var errs problems
	checkName(&errs, "name", s.Name, typeNamePattern)
	checkName(&errs, "site", s.Site, siteNamePattern)
	checkName(&errs, "client", s.Client, clientNamePattern)
	if len(s.Description) > MaxDescriptionBytes {
		errs.addf("description is longer than %d bytes", MaxDescriptionBytes)
	}
	s.checkFields(&errs)
	switch s.Activation {
	case "", ActivationProbe, ActivationImmediate:
	default:
		errs.addf("activation %q must be %q or %q", truncate(s.Activation), ActivationProbe, ActivationImmediate)
	}
	uniqueBy := s.checkUniqueBy(&errs)
	plan := compileDeliver(&errs, s.Deliver, s.Fields)
	if len(errs) > 0 {
		label := s.Name
		if label == "" {
			label = "<unnamed>"
		}
		return nil, nil, invalidf("invalid identity type %q: %s", truncate(label), errs.join())
	}
	return uniqueBy, plan, nil
}

func checkName(errs *problems, what, value string, pattern *regexp.Regexp) {
	switch {
	case value == "":
		errs.addf("%s is required", what)
	case !pattern.MatchString(value):
		errs.addf("%s %q must match %s", what, truncate(value), pattern)
	}
}

func (s *TypeSpec) checkFields(errs *problems) {
	switch {
	case len(s.Fields) == 0:
		errs.addf("at least one field is required")
		return
	case len(s.Fields) > MaxFields:
		errs.addf("identity type has %d fields, the maximum is %d", len(s.Fields), MaxFields)
	}
	for _, name := range sortedKeys(s.Fields) {
		f := s.Fields[name]
		switch {
		case !fieldNamePattern.MatchString(name):
			errs.addf("field name %q must match %s", truncate(name), fieldNamePattern)
		case slices.Contains(reservedFieldNames, name):
			errs.addf("field name %q is reserved", name)
		}
		if !f.Type.Valid() {
			if f.Type == "" {
				errs.addf("fields.%s: type is required", truncate(name))
			} else {
				errs.addf("fields.%s: unknown type %q", truncate(name), truncate(string(f.Type)))
			}
		}
		if len(f.Description) > MaxDescriptionBytes {
			errs.addf("fields.%s: description is longer than %d bytes", truncate(name), MaxDescriptionBytes)
		}
	}
}

func (s *TypeSpec) checkUniqueBy(errs *problems) []fieldPath {
	raw := s.UniqueBy
	if len(raw) == 0 {
		raw = defaultUniqueBy(s.Fields)
		if len(raw) == 0 && len(s.Fields) > 0 {
			errs.addf("unique_by is empty and no field is required")
			return nil
		}
	}
	if len(raw) > MaxUniqueBy {
		errs.addf("unique_by has %d paths, the maximum is %d", len(raw), MaxUniqueBy)
	}
	paths := make([]fieldPath, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, p := range raw {
		if _, dup := seen[p]; dup {
			errs.addf("unique_by: duplicate path %q", truncate(p))
			continue
		}
		seen[p] = struct{}{}
		fp, err := resolvePath(p, s.Fields)
		if err != nil {
			errs.addf("unique_by: %s", err)
			continue
		}
		paths = append(paths, fp)
	}
	return paths
}

// fieldPath is a parsed field reference: a field name optionally followed by
// dotted sub keys ("cookies.sessionid", "extra.device.id").
type fieldPath struct {
	raw   string
	field string
	ftype FieldType
	// sub holds the dotted sub keys. For cookie_map fields it has at most one
	// element: the cookie name (which may itself contain dots).
	sub []string
}

// resolvePath parses p and checks it against the declared fields.
func resolvePath(p string, fields map[string]FieldSpec) (fieldPath, error) {
	fieldName, rest, hasSub := strings.Cut(p, ".")
	if !fieldNamePattern.MatchString(fieldName) {
		return fieldPath{}, fmt.Errorf("invalid path %q", truncate(p))
	}
	f, ok := fields[fieldName]
	if !ok {
		return fieldPath{}, fmt.Errorf("path %q refers to unknown field %q", truncate(p), fieldName)
	}
	fp := fieldPath{raw: p, field: fieldName, ftype: f.Type}
	if !hasSub {
		return fp, nil
	}
	if !f.Type.hasSubKeys() {
		return fieldPath{}, fmt.Errorf("path %q: field %q of type %s has no sub keys", truncate(p), fieldName, f.Type)
	}
	segments := strings.Split(rest, ".")
	for _, seg := range segments {
		if !subKeyPattern.MatchString(seg) {
			return fieldPath{}, fmt.Errorf("invalid path %q: sub keys must match %s", truncate(p), subKeyPattern)
		}
	}
	if f.Type == FieldCookieMap {
		fp.sub = []string{rest}
	} else {
		fp.sub = segments
	}
	return fp, nil
}

// invalidf returns an invalid_argument application error.
func invalidf(format string, args ...any) *apperr.Error {
	return apperr.InvalidArgument(apperr.ReasonInvalidArgument, format, args...)
}
