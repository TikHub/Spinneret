package configcenter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

const personSchema = `{
  "type": "object",
  "properties": {"name": {"type": "string"}, "age": {"type": "integer", "minimum": 0}},
  "required": ["name"],
  "additionalProperties": false
}`

func TestParseSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		wantNil bool
		wantErr string
	}{
		{name: "empty", doc: "", wantNil: true},
		{name: "blank", doc: "  \n", wantNil: true},
		{name: "object", doc: personSchema},
		{name: "boolean", doc: "true"},
		{name: "draft 7 meta", doc: `{"$schema":"http://json-schema.org/draft-07/schema#","type":"string"}`},
		{name: "not json", doc: "{", wantErr: "not valid JSON"},
		{name: "array", doc: "[]", wantErr: "JSON object or boolean"},
		{name: "invalid keyword", doc: `{"type": 5}`, wantErr: "invalid JSON Schema"},
		{name: "external ref", doc: `{"$ref": "file:///etc/passwd"}`, wantErr: "invalid JSON Schema"},
		{name: "remote ref", doc: `{"$ref": "https://example.com/schema.json"}`, wantErr: "invalid JSON Schema"},
		{name: "too large", doc: `{"description":"` + strings.Repeat("x", MaxSchemaBytes) + `"}`, wantErr: "exceeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sch, err := parseSchema([]byte(tc.doc))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			if tc.wantNil {
				require.Nil(t, sch)
				return
			}
			require.NotNil(t, sch)
			require.NotContains(t, string(sch.doc), "\n")
		})
	}
}

func TestSchemaForText(t *testing.T) {
	t.Parallel()
	_, err := schemaFor(FormatText, []byte(personSchema))
	require.ErrorContains(t, err, "json or yaml")
	sch, err := schemaFor(FormatText, nil)
	require.NoError(t, err)
	require.Nil(t, sch)
}

func TestValidateContent(t *testing.T) {
	t.Parallel()
	sch, err := parseSchema([]byte(personSchema))
	require.NoError(t, err)
	tests := []struct {
		name     string
		format   string
		content  string
		schema   *compiledSchema
		wantRefs []SecretRef
		wantErr  string
	}{
		{name: "json ok", format: FormatJSON, content: `{"name":"x","age":3}`, schema: sch},
		{name: "json refs", format: FormatJSON, content: `{"name":"${secret:a/b}"}`, schema: sch, wantRefs: []SecretRef{{Path: "a/b"}}},
		{name: "json syntax", format: FormatJSON, content: `{"name":`, wantErr: "not valid JSON"},
		{name: "json trailing data", format: FormatJSON, content: `{} {}`, wantErr: "not valid JSON"},
		{name: "json empty", format: FormatJSON, content: ``, wantErr: "not valid JSON"},
		{name: "json schema mismatch", format: FormatJSON, content: `{"age":-1,"x":1}`, schema: sch, wantErr: "does not match the schema"},
		{name: "json ref outside string", format: FormatJSON, content: `{"name": ${secret:a}}`, wantErr: "not valid JSON"},
		{name: "yaml ok", format: FormatYAML, content: "name: x\nage: 4\n", schema: sch},
		{name: "yaml syntax", format: FormatYAML, content: "a: [1, 2\n", wantErr: "not valid YAML"},
		{name: "yaml schema mismatch", format: FormatYAML, content: "age: x\n", schema: sch, wantErr: "does not match the schema"},
		{name: "yaml multi doc ok", format: FormatYAML, content: "name: a\n---\nname: b\n", schema: sch},
		{name: "yaml multi doc second invalid", format: FormatYAML, content: "name: a\n---\nage: 1\n", schema: sch, wantErr: "YAML document 2"},
		{name: "yaml empty with schema", format: FormatYAML, content: "", schema: sch, wantErr: "does not match the schema"},
		{name: "yaml empty without schema", format: FormatYAML, content: ""},
		{name: "yaml nan", format: FormatYAML, content: "name: .nan\n", schema: sch, wantErr: "cannot be validated as JSON"},
		{name: "yaml non-string keys", format: FormatYAML, content: "1: a\ntrue: b\n", schema: mustSchema(t, `{"type":"object","required":["1","true"]}`)},
		{name: "yaml complex key", format: FormatYAML, content: "? [a, b]\n: c\n", schema: mustSchema(t, `true`), wantErr: "not valid YAML"},
		{name: "text anything", format: FormatText, content: "{not json"},
		{name: "text bad ref", format: FormatText, content: "${secret:../x}", wantErr: "invalid secret reference"},
		{name: "nul byte", format: FormatText, content: "a\x00b", wantErr: "NUL"},
		{name: "invalid utf8", format: FormatText, content: "a\xffb", wantErr: "UTF-8"},
		{name: "too large", format: FormatText, content: strings.Repeat("x", MaxContentBytes+1), wantErr: "exceeds"},
		{name: "unknown format", format: "toml", content: "a=1", wantErr: "unsupported format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			refs, err := validateContent(tc.format, tc.content, tc.schema)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantRefs, refs)
		})
	}
}

func TestNameValidation(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateCreatableName("crawler", "search.json"))
	require.NoError(t, validateCreatableName("a-b_c.d", "x/y-z_1.yaml"))
	require.ErrorContains(t, validateCreatableName("_runtime", "breakers"), "reserved")
	require.ErrorContains(t, validateCreatableName("-bad", "k"), "group must match")
	require.ErrorContains(t, validateCreatableName("g", "/k"), "key must match")
	require.True(t, wellFormedRef("_runtime", "breakers"))
	require.False(t, wellFormedRef("__x", "k"))
	require.False(t, wellFormedRef("g", strings.Repeat("k", 129)))
	require.True(t, reservedGroup("_x"))
	require.False(t, reservedGroup("x_"))
	require.ErrorContains(t, validateText("comment", strings.Repeat("é", MaxCommentLen+1), MaxCommentLen), "at most")
	require.NoError(t, validateText("comment", strings.Repeat("é", MaxCommentLen), MaxCommentLen))
	require.ErrorContains(t, validateText("comment", "\xff", MaxCommentLen), "UTF-8")
}

func mustSchema(t *testing.T, doc string) *compiledSchema {
	t.Helper()
	sch, err := parseSchema([]byte(doc))
	require.NoError(t, err)
	return sch
}
