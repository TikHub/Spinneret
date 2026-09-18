package identity

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// JSONSchemaDraft is the "$schema" URI of generated payload schemas.
const JSONSchemaDraft = "https://json-schema.org/draft/2020-12/schema"

// schemaResourceURL is the in-memory location payload schemas are compiled
// under. It is never fetched.
const schemaResourceURL = "urn:spinneret:identity-type:payload-schema"

// JSONSchema returns the JSON Schema (draft 2020-12) of payloads of this type
// in canonical JSON. The spec must be valid.
//
// Field types map to: string → string, number → number, bool → boolean,
// cookie_map → oneOf [object of strings, Cookie header string, array of
// {name, value} objects], json → any value, secret_ref → string matching
// SecretPathPattern. Sensitive fields are annotated with "writeOnly": true.
func (s *TypeSpec) JSONSchema() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return payloadSchema(s)
}

func payloadSchema(s *TypeSpec) ([]byte, error) {
	props := make(map[string]any, len(s.Fields))
	var required []string
	for name, f := range s.Fields {
		props[name] = fieldSchema(f)
		if f.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	doc := map[string]any{
		"$schema":              JSONSchemaDraft,
		"title":                s.Name,
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if s.Description != "" {
		doc["description"] = s.Description
	}
	if len(required) > 0 {
		doc["required"] = required
	}
	out, err := CanonicalJSON(doc)
	if err != nil {
		return nil, fmt.Errorf("identity type %q: build json schema: %w", s.Name, err)
	}
	return out, nil
}

func fieldSchema(f FieldSpec) map[string]any {
	var out map[string]any
	switch f.Type {
	case FieldString:
		out = map[string]any{"type": "string"}
	case FieldNumber:
		out = map[string]any{"type": "number"}
	case FieldBool:
		out = map[string]any{"type": "boolean"}
	case FieldCookieMap:
		out = map[string]any{"oneOf": []any{
			map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
			},
			map[string]any{"type": "string"},
			map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":     "object",
					"required": []any{"name", "value"},
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "minLength": 1},
						"value": map[string]any{"type": "string"},
					},
				},
			},
		}}
	case FieldSecretRef:
		out = map[string]any{"type": "string", "pattern": SecretPathPattern}
	default: // FieldJSON: any JSON value
		out = map[string]any{}
	}
	if f.Description != "" {
		out["description"] = f.Description
	}
	if f.Sensitive {
		out["writeOnly"] = true
	}
	return out
}

// compileSchema compiles a generated payload schema.
func compileSchema(doc []byte) (*jsonschema.Schema, error) {
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, fmt.Errorf("decode json schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(schemaResourceURL, parsed); err != nil {
		return nil, fmt.Errorf("add json schema resource: %w", err)
	}
	sch, err := c.Compile(schemaResourceURL)
	if err != nil {
		return nil, fmt.Errorf("compile json schema: %w", err)
	}
	return sch, nil
}

// schemaErrorMessages flattens a validation error into "location: message"
// strings. Locations are JSON pointers into the payload ("/cookies").
func schemaErrorMessages(err error) []string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []string{err.Error()}
	}
	var out []string
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
		out = append(out, msg)
	}
	if len(out) == 0 {
		out = append(out, strings.TrimSpace(ve.Error()))
	}
	return out
}
