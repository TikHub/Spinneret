package configcenter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

const (
	schemaResourceURL   = "urn:spinneret:config:schema"
	maxValidationErrors = 20
)

var (
	// groupPattern matches creatable group names (no reserved "_" prefix).
	groupPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
	// readableGroupPattern also matches reserved system groups.
	readableGroupPattern = regexp.MustCompile(`^_?[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
	keyPattern           = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./-]{0,127}$`)
)

// validFormat reports whether f is a supported content format.
func validFormat(f string) bool {
	return f == FormatJSON || f == FormatYAML || f == FormatText
}

// reservedGroup reports whether a group name is reserved for the system.
func reservedGroup(group string) bool {
	return strings.HasPrefix(group, "_")
}

// wellFormedRef reports whether group and key can name a stored or runtime
// item. Malformed names cannot exist and are treated as missing.
func wellFormedRef(group, key string) bool {
	return readableGroupPattern.MatchString(group) && keyPattern.MatchString(key)
}

func validateCreatableName(group, key string) error {
	if reservedGroup(group) {
		return apperr.InvalidArgument("", "group %q is reserved: names starting with \"_\" are maintained by the system", group)
	}
	if !groupPattern.MatchString(group) {
		return apperr.InvalidArgument("", "group must match %s", groupPattern.String())
	}
	if !keyPattern.MatchString(key) {
		return apperr.InvalidArgument("", "key must match %s", keyPattern.String())
	}
	return nil
}

func validateText(field, value string, maxLen int) error {
	if !utf8.ValidString(value) {
		return apperr.InvalidArgument("", "%s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxLen {
		return apperr.InvalidArgument("", "%s must be at most %d characters", field, maxLen)
	}
	return nil
}

func validateContentSize(content string) error {
	if len(content) > MaxContentBytes {
		return apperr.InvalidArgument("", "content exceeds %d bytes", MaxContentBytes)
	}
	if !utf8.ValidString(content) {
		return apperr.InvalidArgument("", "content must be valid UTF-8")
	}
	if strings.IndexByte(content, 0) >= 0 {
		return apperr.InvalidArgument("", "content must not contain NUL characters")
	}
	return nil
}

// compiledSchema is a validated JSON Schema with its compact JSON document.
type compiledSchema struct {
	doc    json.RawMessage
	schema *jsonschema.Schema
}

// denyLoader refuses to load external schema resources, so schemas cannot
// make the server read local files or reach the network.
type denyLoader struct{}

func (denyLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference %q is not allowed", url)
}

// parseSchema compiles a JSON Schema (draft 2020-12 by default). An empty
// document yields nil. Invalid schemas are invalid_argument errors.
func parseSchema(doc []byte) (*compiledSchema, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		return nil, nil
	}
	if len(doc) > MaxSchemaBytes {
		return nil, apperr.InvalidArgument("", "schema_json exceeds %d bytes", MaxSchemaBytes)
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, apperr.InvalidArgument("", "schema_json is not valid JSON: %v", err)
	}
	switch parsed.(type) {
	case map[string]any, bool:
	default:
		return nil, apperr.InvalidArgument("", "schema_json must be a JSON object or boolean")
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(denyLoader{})
	if err := c.AddResource(schemaResourceURL, parsed); err != nil {
		return nil, apperr.InvalidArgument("", "invalid JSON Schema: %v", err)
	}
	sch, err := c.Compile(schemaResourceURL)
	if err != nil {
		return nil, apperr.InvalidArgument("", "invalid JSON Schema: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil {
		return nil, apperr.InvalidArgument("", "schema_json is not valid JSON: %v", err)
	}
	return &compiledSchema{doc: compact.Bytes(), schema: sch}, nil
}

// schemaFor validates that a schema may be attached to the format.
func schemaFor(format string, doc []byte) (*compiledSchema, error) {
	sch, err := parseSchema(doc)
	if err != nil {
		return nil, err
	}
	if sch != nil && format == FormatText {
		return nil, apperr.InvalidArgument("", "a JSON Schema can only be attached to json or yaml items")
	}
	return sch, nil
}

// validateContent checks content syntax for the format, validates it
// against the schema (when set) and parses its secret references.
func validateContent(format, content string, sch *compiledSchema) ([]SecretRef, error) {
	if err := validateContentSize(content); err != nil {
		return nil, err
	}
	refs, err := ParseSecretRefs(content)
	if err != nil {
		return nil, err
	}
	switch format {
	case FormatJSON:
		err = validateJSON(content, sch)
	case FormatYAML:
		err = validateYAML(content, sch)
	case FormatText:
	default:
		err = apperr.InvalidArgument("", "unsupported format %q", format)
	}
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func validateJSON(content string, sch *compiledSchema) error {
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(content))
	if err != nil {
		return apperr.InvalidArgument("", "content is not valid JSON: %v", err)
	}
	if sch == nil {
		return nil
	}
	return schemaError(sch.schema.Validate(value), "")
}

// validateYAML decodes every document of a YAML stream. With a schema each
// document (an empty stream counts as one null document) is converted to
// JSON and validated.
func validateYAML(content string, sch *compiledSchema) error {
	dec := yaml.NewDecoder(strings.NewReader(content))
	docs := 0
	for {
		var doc any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return apperr.InvalidArgument("", "content is not valid YAML: %v", err)
		}
		docs++
		if sch == nil {
			continue
		}
		if err := validateYAMLDocument(doc, sch, docs); err != nil {
			return err
		}
	}
	if docs == 0 && sch != nil {
		return schemaError(sch.schema.Validate(nil), "")
	}
	return nil
}

func validateYAMLDocument(doc any, sch *compiledSchema, index int) error {
	converted, err := yamlToJSON(doc)
	if err != nil {
		return apperr.InvalidArgument("", "YAML document %d cannot be validated as JSON: %v", index, err)
	}
	raw, err := json.Marshal(converted)
	if err != nil {
		return apperr.InvalidArgument("", "YAML document %d cannot be validated as JSON: %v", index, err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return apperr.InvalidArgument("", "YAML document %d cannot be validated as JSON: %v", index, err)
	}
	return schemaError(sch.schema.Validate(value), fmt.Sprintf("YAML document %d: ", index))
}

// yamlToJSON converts a decoded YAML value into JSON-compatible values
// (string map keys, no NaN or infinite numbers).
func yamlToJSON(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			c, err := yamlToJSON(val)
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			// The decoder rejects non-scalar keys, so fmt.Sprint is exact.
			c, err := yamlToJSON(val)
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(k)] = c
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			c, err := yamlToJSON(val)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, errors.New("NaN and infinite numbers are not representable in JSON")
		}
		return t, nil
	default:
		return t, nil
	}
}

// schemaError converts a schema validation failure into an invalid_argument
// error listing (a bounded number of) instance locations and messages.
func schemaError(err error, prefix string) error {
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return apperr.InvalidArgument("", "%scontent does not match the schema: %v", prefix, err)
	}
	var msgs []string
	seen := make(map[string]struct{})
	for _, unit := range ve.BasicOutput().Errors {
		if unit.Error == nil {
			continue
		}
		loc := unit.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		msg := loc + ": " + unit.Error.String()
		if _, dup := seen[msg]; dup {
			continue
		}
		seen[msg] = struct{}{}
		msgs = append(msgs, msg)
		if len(msgs) == maxValidationErrors {
			break
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, strings.TrimSpace(ve.Error()))
	}
	return apperr.InvalidArgument("", "%scontent does not match the schema: %s", prefix, strings.Join(msgs, "; "))
}
