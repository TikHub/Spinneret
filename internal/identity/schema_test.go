package identity

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONSchemaAppDevice(t *testing.T) {
	spec := loadTestSpec(t, "app_device.yaml")
	raw, err := spec.JSONSchema()
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, JSONSchemaDraft, doc["$schema"])
	require.Equal(t, "app_device", doc["title"])
	require.Equal(t, "object", doc["type"])
	require.Equal(t, false, doc["additionalProperties"])
	require.Equal(t, []any{"device_id", "install_id"}, doc["required"])
	require.NotContains(t, doc, "description")

	props := doc["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string"}, props["device_id"])
	require.Equal(t, map[string]any{}, props["extra"])
	cookies := props["cookies"].(map[string]any)
	require.Equal(t, true, cookies["writeOnly"])
	require.Len(t, cookies["oneOf"], 3)

	// Canonical output is stable.
	again, err := spec.JSONSchema()
	require.NoError(t, err)
	require.Equal(t, raw, again)
}

func TestJSONSchemaAllTypes(t *testing.T) {
	spec := &TypeSpec{
		Name: "all", Site: "s", Client: "web", Description: "every field type",
		Fields: map[string]FieldSpec{
			"s":  {Type: FieldString, Required: true, Description: "a string"},
			"n":  {Type: FieldNumber},
			"b":  {Type: FieldBool},
			"c":  {Type: FieldCookieMap},
			"j":  {Type: FieldJSON, Sensitive: true},
			"sr": {Type: FieldSecretRef},
		},
	}
	raw, err := spec.JSONSchema()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, "every field type", doc["description"])
	props := doc["properties"].(map[string]any)
	require.Equal(t, map[string]any{"type": "string", "description": "a string"}, props["s"])
	require.Equal(t, map[string]any{"type": "number"}, props["n"])
	require.Equal(t, map[string]any{"type": "boolean"}, props["b"])
	require.Equal(t, map[string]any{"writeOnly": true}, props["j"])
	require.Equal(t, map[string]any{"type": "string", "pattern": SecretPathPattern}, props["sr"])

	sch, err := compileSchema(raw)
	require.NoError(t, err)
	valid := []map[string]any{
		{"s": "x"},
		{"s": "x", "n": 1.5, "b": true, "j": []any{1.0}, "sr": "team/api-key.v1"},
		{"s": "x", "c": "a=1; b=2"},
		{"s": "x", "c": map[string]any{"a": "1"}},
		{"s": "x", "c": []any{map[string]any{"name": "a", "value": "1", "domain": "x"}}},
	}
	for _, v := range valid {
		require.NoError(t, sch.Validate(v), "%v", v)
	}
	invalid := []map[string]any{
		{},
		{"s": 1.0},
		{"s": "x", "extra": 1.0},
		{"s": "x", "n": "1"},
		{"s": "x", "b": "true"},
		{"s": "x", "sr": "Bad Path"},
		{"s": "x", "c": map[string]any{"a": 1.0}},
		{"s": "x", "c": []any{map[string]any{"name": "", "value": "1"}}},
		{"s": "x", "c": []any{map[string]any{"name": "a"}}},
	}
	for _, v := range invalid {
		require.Error(t, sch.Validate(v), "%v", v)
	}

	_, err = (&TypeSpec{Name: "x"}).JSONSchema()
	requireInvalid(t, err, "site is required")
}

func TestCompileSchemaErrors(t *testing.T) {
	_, err := compileSchema([]byte("{"))
	require.ErrorContains(t, err, "decode json schema")
	_, err = compileSchema([]byte(`{"type": 5}`))
	require.ErrorContains(t, err, "compile json schema")
}

func TestSchemaErrorMessages(t *testing.T) {
	ct := compileTestType(t, "web_cookie.yaml")
	msgs := schemaErrorMessages(ct.schema.Validate(map[string]any{"foo": "x"}))
	require.Contains(t, msgs, "/: missing property 'cookies'")
	require.Contains(t, msgs, "/: additional properties 'foo' not allowed")

	require.Equal(t, []string{"boom"}, schemaErrorMessages(errors.New("boom")))
}
