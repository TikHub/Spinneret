package identity

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
)

func loadTestSpec(t testing.TB, name string) *TypeSpec {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	spec, err := ParseTypeYAML(data)
	require.NoError(t, err)
	return spec
}

func compileTestType(t testing.TB, name string) *CompiledType {
	t.Helper()
	ct, err := Compile("ity_test", "sit_test", 1, loadTestSpec(t, name))
	require.NoError(t, err)
	return ct
}

func requireInvalid(t *testing.T, err error, contains ...string) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.As(err)
	require.True(t, ok, "expected *apperr.Error, got %T: %v", err, err)
	require.Equal(t, apperr.ReasonInvalidArgument, ae.Reason)
	for _, c := range contains {
		require.Contains(t, ae.Message, c)
	}
}

func TestParseTypeYAMLDesignDocExamples(t *testing.T) {
	web := loadTestSpec(t, "web_cookie.yaml")
	require.Equal(t, "web_cookie", web.Name)
	require.Equal(t, "shop", web.Site)
	require.Equal(t, "web", web.Client)
	require.Equal(t, map[string]FieldSpec{
		"cookies":    {Type: FieldCookieMap, Required: true, Sensitive: true},
		"user_agent": {Type: FieldString},
		"signature":  {Type: FieldString, Sensitive: true},
	}, web.Fields)
	require.Equal(t, []string{"cookies.sessionid"}, web.UniqueBy)
	require.Equal(t, ActivationProbe, web.Activation)
	require.Equal(t, map[string]any{
		"cookies":       "{{ cookies }}",
		"cookie_header": "{{ cookies }}",
		"headers":       map[string]any{"User-Agent": "{{ user_agent }}"},
		"values":        map[string]any{"signature": "{{ signature }}"},
	}, web.Deliver)

	app := loadTestSpec(t, "app_device.yaml")
	require.Equal(t, "app", app.Client)
	require.Equal(t, []string{"device_id"}, app.UniqueBy)
	require.Equal(t, ActivationImmediate, app.Activation)
	require.Equal(t, FieldJSON, app.Fields["extra"].Type)
	require.Equal(t, map[string]any{"device_id": "{{ device_id }}", "iid": "{{ install_id }}"}, app.Deliver["query"])
}

func TestParseTypeYAMLDefaults(t *testing.T) {
	spec, err := ParseTypeYAML([]byte(`
name: t1
site: s
client: web
description: test type
fields:
  zeta: { type: string, required: true }
  alpha: { type: number, required: true }
  opt: { type: bool }
`))
	require.NoError(t, err)
	require.Equal(t, ActivationProbe, spec.Activation)
	require.Equal(t, []string{"alpha", "zeta"}, spec.UniqueBy)
	require.Nil(t, spec.Deliver)
}

const validSpecHead = "name: t\nsite: s\nclient: web\n"

func TestParseTypeYAMLErrors(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		contains []string
	}{
		{"empty", "  \n", []string{"empty"}},
		{"syntax", "name: [", []string{"parse identity type yaml"}},
		{"unknown top-level key", validSpecHead + "fields:\n  a: {type: string, required: true}\nbogus: 1\n", []string{"field bogus not found"}},
		{"unknown field key", validSpecHead + "fields:\n  a: {type: string, required: true, secret: true}\n", []string{"field secret not found"}},
		{"multiple documents", validSpecHead + "fields:\n  a: {type: string, required: true}\n---\nname: x\n", []string{"single YAML document"}},
		{"bad name", "name: Bad Name\nsite: s\nclient: web\nfields:\n  a: {type: string, required: true}\n", []string{`name "Bad Name" must match`}},
		{"missing name site client", "fields:\n  a: {type: string, required: true}\n", []string{"name is required", "site is required", "client is required"}},
		{"long description", validSpecHead + "description: " + strings.Repeat("x", MaxDescriptionBytes+1) + "\nfields:\n  a: {type: string, required: true}\n", []string{"description is longer"}},
		{"no fields", validSpecHead + "unique_by: [a]\n", []string{"at least one field is required"}},
		{"bad field name", validSpecHead + "fields:\n  1abc: {type: string, required: true}\n", []string{`field name "1abc" must match`}},
		{"reserved field name", validSpecHead + "fields:\n  payload: {type: json, required: true}\n", []string{`field name "payload" is reserved`}},
		{"unknown type", validSpecHead + "fields:\n  a: {type: int, required: true}\n", []string{`fields.a: unknown type "int"`}},
		{"missing type", validSpecHead + "fields:\n  a: {required: true}\n", []string{"fields.a: type is required"}},
		{"field description too long", validSpecHead + "fields:\n  a: {type: string, required: true, description: " + strings.Repeat("x", MaxDescriptionBytes+1) + "}\n", []string{"fields.a: description is longer"}},
		{"bad activation", validSpecHead + "activation: lazy\nfields:\n  a: {type: string, required: true}\n", []string{`activation "lazy"`}},
		{"no unique_by and nothing required", validSpecHead + "fields:\n  a: {type: string}\n", []string{"unique_by is empty and no field is required"}},
		{"unique_by unknown field", validSpecHead + "fields:\n  a: {type: string, required: true}\nunique_by: [b]\n", []string{`unknown field "b"`}},
		{"unique_by sub key on string", validSpecHead + "fields:\n  a: {type: string, required: true}\nunique_by: [a.b]\n", []string{"has no sub keys"}},
		{"unique_by bad sub key", validSpecHead + "fields:\n  c: {type: cookie_map, required: true}\nunique_by: [c..x]\n", []string{"sub keys must match"}},
		{"unique_by bad path", validSpecHead + "fields:\n  a: {type: string, required: true}\nunique_by: [\"9x\"]\n", []string{`invalid path "9x"`}},
		{"unique_by duplicate", validSpecHead + "fields:\n  a: {type: string, required: true}\nunique_by: [a, a]\n", []string{`duplicate path "a"`}},
		{"unique_by too many", validSpecHead + "fields:\n  a: {type: json, required: true}\nunique_by: [" + manyPaths(MaxUniqueBy+1) + "]\n", []string{"unique_by has 17 paths"}},
		{"unknown segment", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  body: x\n", []string{`unknown segment "body"`}},
		{"cookies sub key placeholder", validSpecHead + "fields:\n  c: {type: cookie_map, required: true}\ndeliver:\n  cookies: \"{{ c.sid }}\"\n", []string{"deliver.cookies must be a single placeholder"}},
		{"cookies string field", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookies: \"{{ a }}\"\n", []string{"deliver.cookies must be a single placeholder"}},
		{"cookies bad template", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookies: \"{{ a\"\n", []string{"deliver.cookies: unclosed placeholder"}},
		{"cookies list", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookies: [a]\n", []string{"deliver.cookies must be a map of string templates"}},
		{"cookies bad name", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookies:\n    \"bad name\": \"{{ a }}\"\n", []string{"invalid cookie name"}},
		{"cookies bad literal", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookies:\n    sid: \"x;{{ a }}\"\n", []string{"not allowed in a cookie value"}},
		{"cookie_header not string", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookie_header: {a: b}\n", []string{"deliver.cookie_header must be a string template"}},
		{"cookie_header bad template", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookie_header: \"{{ nope }}\"\n", []string{`deliver.cookie_header: placeholder: path "nope" refers to unknown field`}},
		{"cookie_header newline literal", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  cookie_header: \"a\\n{{ a }}\"\n", []string{"deliver.cookie_header: template contains characters not allowed"}},
		{"headers not map", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  headers: \"{{ a }}\"\n", []string{"deliver.headers must be a map of string templates"}},
		{"headers bad name", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  headers:\n    \"X Bad\": \"{{ a }}\"\n", []string{"invalid header name"}},
		{"headers newline literal", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  headers:\n    X-A: \"v\\r\\nX-Evil: 1\"\n", []string{"not allowed in a header value"}},
		{"headers number value", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  headers:\n    X-A: 5\n", []string{`deliver.headers["X-A"] must be a string template`}},
		{"headers non-string key", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  headers:\n    1: x\n", []string{"deliver.headers: object key of type int is not a string"}},
		{"query empty name", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  query:\n    \"\": \"{{ a }}\"\n", []string{"deliver.query: entry name is empty"}},
		{"values template error", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  values:\n    v: \"{{ a | upper }}\"\n", []string{"pipes are not supported"}},
		{"json template error", validSpecHead + "fields:\n  a: {type: string, required: true}\ndeliver:\n  json:\n    x: [\"{{ upper(a) }}\"]\n", []string{"deliver.json.x[0]: placeholder", "function calls are not supported"}},
		{"multiple problems", "name: t\nsite: S\nclient: web\nfields:\n  a: {type: nope}\n", []string{`site "S" must match`, `unknown type "nope"`, "unique_by is empty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseTypeYAML([]byte(tt.yaml))
			require.Nil(t, spec)
			requireInvalid(t, err, tt.contains...)
		})
	}
}

func manyPaths(n int) string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = "a.k" + string(rune('a'+i))
	}
	return strings.Join(paths, ", ")
}

func TestParseTypeJSON(t *testing.T) {
	spec, err := ParseTypeJSON([]byte(`{
		"name": "api_token", "site": "example", "client": "app",
		"fields": {
			"token": {"type": "secret_ref", "required": true, "description": "vault path"},
			"retries": {"type": "number"},
			"debug": {"type": "bool"}
		},
		"deliver": {
			"headers": {"Authorization": "Bearer {{ token }}"},
			"json": {"retries": "{{ retries }}", "flags": [true, 2, null]}
		}
	}`))
	require.NoError(t, err)
	require.Equal(t, []string{"token"}, spec.UniqueBy)
	require.Equal(t, ActivationProbe, spec.Activation)
	require.Equal(t, []any{true, 2.0, nil}, spec.Deliver["json"].(map[string]any)["flags"])

	tests := []struct {
		name     string
		json     string
		contains string
	}{
		{"empty", "", "empty"},
		{"syntax", "{", "parse identity type json"},
		{"unknown key", `{"name":"t","site":"s","client":"web","fields":{"a":{"type":"string","required":true}},"extra":1}`, `unknown field "extra"`},
		{"unknown field key", `{"name":"t","site":"s","client":"web","fields":{"a":{"type":"string","required":true,"x":1}}}`, `unknown field "x"`},
		{"trailing data", `{"name":"t","site":"s","client":"web","fields":{"a":{"type":"string","required":true}}} {}`, "unexpected data"},
		{"case-insensitive top-level key", `{"NAME":"t","site":"s","client":"web","fields":{"a":{"type":"string","required":true}}}`, `unknown field "NAME"`},
		{"case-insensitive unique_by", `{"name":"t","site":"s","client":"web","fields":{"a":{"type":"string","required":true}},"Unique_By":["a"]}`, `unknown field "Unique_By"`},
		{"case-insensitive field key", `{"name":"t","site":"s","client":"web","fields":{"a":{"Type":"string","required":true}}}`, `fields.a: unknown field "Type"`},
		{"invalid spec", `{"name":"t","site":"s","client":"web","fields":{}}`, "at least one field is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseTypeJSON([]byte(tt.json))
			require.Nil(t, spec)
			requireInvalid(t, err, tt.contains)
		})
	}
}

func TestSiteAndClientNames(t *testing.T) {
	spec := func(site, client string) *TypeSpec {
		return &TypeSpec{Name: "t", Site: site, Client: client, Fields: map[string]FieldSpec{"a": {Type: FieldString, Required: true}}}
	}
	// Every site and client name the site admin API accepts is valid.
	for _, tt := range []struct{ site, client string }{
		{"shop", "web"},
		{"_internal", "_app"},
		{"a.b-c_d", "-x"},
		{strings.Repeat("s", 64), strings.Repeat("c", 32)},
	} {
		require.NoError(t, spec(tt.site, tt.client).Validate(), "%s/%s", tt.site, tt.client)
	}
	for _, tt := range []struct{ site, client, contains string }{
		{"Shop", "web", `site "Shop" must match`},
		{".x", "web", `site ".x" must match`},
		{strings.Repeat("s", 65), "web", "site"},
		{"s", "app.v2", `client "app.v2" must match`},
		{"s", "Web", `client "Web" must match`},
		{"s", strings.Repeat("c", 33), "client"},
	} {
		requireInvalid(t, spec(tt.site, tt.client).Validate(), tt.contains)
	}
}

func TestMarshalTypeRoundTrip(t *testing.T) {
	for _, file := range []string{"web_cookie.yaml", "app_device.yaml"} {
		t.Run(file, func(t *testing.T) {
			spec := loadTestSpec(t, file)

			out, err := MarshalTypeYAML(spec)
			require.NoError(t, err)
			require.Contains(t, string(out), "unique_by: [")
			require.Regexp(t, `(?m)^  \w+: \{type: `, string(out))
			again, err := ParseTypeYAML(out)
			require.NoError(t, err)
			require.Equal(t, spec, again)

			js, err := MarshalTypeJSON(spec)
			require.NoError(t, err)
			require.NotContains(t, string(js), `<`)
			fromJSON, err := ParseTypeJSON(js)
			require.NoError(t, err)
			require.Equal(t, spec, fromJSON)
			js2, err := MarshalTypeJSON(fromJSON)
			require.NoError(t, err)
			require.Equal(t, js, js2)
		})
	}

	_, err := MarshalTypeYAML(nil)
	require.Error(t, err)
	_, err = MarshalTypeJSON(nil)
	require.Error(t, err)
	_, err = MarshalTypeJSON(&TypeSpec{Deliver: map[string]any{"json": make(chan int)}})
	require.Error(t, err)
	_, err = MarshalTypeYAML(&TypeSpec{Deliver: map[string]any{"json": make(chan int)}})
	require.Error(t, err)
}

func TestWithDefaults(t *testing.T) {
	require.Nil(t, (*TypeSpec)(nil).WithDefaults())

	orig := &TypeSpec{
		Name: "t", Site: "s", Client: "web",
		Fields:  map[string]FieldSpec{"a": {Type: FieldString, Required: true}},
		Deliver: map[string]any{"headers": map[string]string{"X-A": "{{ a }}"}, "json": map[any]any{1: "x"}},
	}
	got := orig.WithDefaults()
	require.Equal(t, ActivationProbe, got.Activation)
	require.Equal(t, []string{"a"}, got.UniqueBy)
	require.Equal(t, map[string]any{"X-A": "{{ a }}"}, got.Deliver["headers"])
	require.Equal(t, map[any]any{1: "x"}, got.Deliver["json"], "unsupported values are kept for Validate")

	got.Fields["b"] = FieldSpec{Type: FieldBool}
	got.UniqueBy[0] = "changed"
	require.NotContains(t, orig.Fields, "b")
	require.Empty(t, orig.UniqueBy)
	require.Empty(t, orig.Activation)

	requireInvalid(t, got.WithDefaults().Validate(), "object key of type int is not a string")
	requireInvalid(t, (*TypeSpec)(nil).Validate(), "spec is nil")

	// Validate accepts empty activation and unique_by as their defaults.
	require.NoError(t, (&TypeSpec{Name: "t", Site: "s", Client: "web", Fields: map[string]FieldSpec{"a": {Type: FieldString, Required: true}}}).Validate())
}

func TestValidateLimits(t *testing.T) {
	fields := make(map[string]FieldSpec, MaxFields+1)
	for i := 0; i <= MaxFields; i++ {
		fields["f"+strings.Repeat("x", i%50)+string(rune('a'+i%26))+strings.Repeat("y", i/26)] = FieldSpec{Type: FieldString, Required: true}
	}
	spec := &TypeSpec{Name: "t", Site: "s", Client: "web", Fields: fields}
	requireInvalid(t, spec.Validate(), "fields, the maximum is 128")

	headers := make(map[string]any, MaxDeliverEntries+1)
	for i := 0; i <= MaxDeliverEntries; i++ {
		headers["X-H"+strings.Repeat("a", i)] = "v"
	}
	jsonItems := make([]any, MaxDeliverEntries+1)
	for i := range jsonItems {
		jsonItems[i] = "x"
	}
	spec = &TypeSpec{
		Name: "t", Site: "s", Client: "web",
		Fields: map[string]FieldSpec{"a": {Type: FieldString, Required: true}},
		Deliver: map[string]any{
			"headers": headers,
			"json":    map[string]any{"list": jsonItems, "obj": headers},
			"values":  map[string]any{"v": strings.Repeat("x", MaxTemplateBytes+1)},
		},
	}
	requireInvalid(t, spec.Validate(),
		"deliver.headers has 257 entries",
		"deliver.json.list has 257 entries",
		"deliver.json.obj has 257 entries",
		"template is longer than",
	)

	var deep any = "{{ a }}"
	for range maxJSONDepth + 2 {
		deep = map[string]any{"n": deep}
	}
	spec.Deliver = map[string]any{"json": deep}
	requireInvalid(t, spec.Validate(), "nested deeper")
}

func TestParseTemplate(t *testing.T) {
	fields := map[string]FieldSpec{
		"a":       {Type: FieldString},
		"cookies": {Type: FieldCookieMap},
		"extra":   {Type: FieldJSON},
	}
	tests := []struct {
		name     string
		in       string
		literals []string // "" marks a placeholder part
		single   string
		err      string
	}{
		{name: "literal", in: "plain text }} ok", literals: []string{"plain text }} ok"}},
		{name: "empty", in: ""},
		{name: "single", in: "{{ a }}", literals: []string{""}, single: "a"},
		{name: "single no spaces", in: "{{a}}", literals: []string{""}, single: "a"},
		{name: "single tabs", in: "{{\ta\t}}", literals: []string{""}, single: "a"},
		{name: "cookie sub key with dots", in: "{{ cookies.a.b }}", literals: []string{""}, single: "cookies.a.b"},
		{name: "json nested", in: "{{ extra.items.0 }}", literals: []string{""}, single: "extra.items.0"},
		{name: "mixed", in: "Bearer {{ a }}-{{ cookies.sid }}!", literals: []string{"Bearer ", "", "-", "", "!"}},
		{name: "outer spaces are not single", in: " {{ a }}", literals: []string{" ", ""}},
		{name: "unclosed", in: "x {{ a", err: "unclosed placeholder"},
		{name: "nested open", in: "{{ a {{ a }}", err: "unclosed placeholder"},
		{name: "empty placeholder", in: "{{  }}", err: "empty placeholder"},
		{name: "pipe", in: "{{ a | lower }}", err: "pipes are not supported"},
		{name: "call", in: "{{ len(a) }}", err: "function calls are not supported"},
		{name: "expression", in: "{{ a + a }}", err: "expressions are not supported"},
		{name: "quoted", in: `{{ "a" }}`, err: "expressions are not supported"},
		{name: "go template dot", in: "{{ .a }}", err: "invalid path"},
		{name: "unknown field", in: "{{ b }}", err: `unknown field "b"`},
		{name: "sub key on string", in: "{{ a.b }}", err: "has no sub keys"},
		{name: "empty sub key", in: "{{ extra. }}", err: "sub keys must match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := parseTemplate(tt.in, fields)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			require.Len(t, tmpl.parts, len(tt.literals))
			for i, lit := range tt.literals {
				if lit == "" {
					require.NotNil(t, tmpl.parts[i].ph)
				} else {
					require.Equal(t, lit, tmpl.parts[i].literal)
				}
			}
			if tt.single == "" {
				require.Nil(t, tmpl.single)
			} else {
				require.NotNil(t, tmpl.single)
				require.Equal(t, tt.single, tmpl.single.raw)
			}
		})
	}
}

func TestResolvePathSubKeys(t *testing.T) {
	fields := map[string]FieldSpec{"c": {Type: FieldCookieMap}, "j": {Type: FieldJSON}}
	cp, err := resolvePath("c.a.b", fields)
	require.NoError(t, err)
	require.Equal(t, []string{"a.b"}, cp.sub)
	jp, err := resolvePath("j.a.b", fields)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, jp.sub)
	require.True(t, FieldJSON.Valid())
	require.False(t, FieldType("x").Valid())
}
