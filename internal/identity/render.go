package identity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

// Credential is the rendered delivery of an identity payload. The maps are
// never nil; JSON is nil when the json segment is not configured or its
// placeholder is unset.
type Credential struct {
	Cookies      map[string]string
	CookieHeader string
	Headers      map[string]string
	Query        map[string]string
	JSON         any
	Values       map[string]any
}

// SecretResolver resolves a namespace-relative secret path to its plaintext.
type SecretResolver func(ctx context.Context, path string) (string, error)

// ---------------------------------------------------------------------------
// Templates

// templatePart is either literal text (ph == nil) or a placeholder.
type templatePart struct {
	literal string
	ph      *fieldPath
}

// textTemplate is a pre-parsed delivery template string.
type textTemplate struct {
	parts []templatePart
	// single is set when the template is exactly one placeholder with no
	// surrounding text; it then yields the typed value.
	single *fieldPath
}

// parseTemplate parses a template string. Placeholders have the form
// "{{ path }}" with optional spaces or tabs inside the braces.
func parseTemplate(s string, fields map[string]FieldSpec) (*textTemplate, error) {
	if len(s) > MaxTemplateBytes {
		return nil, fmt.Errorf("template is longer than %d bytes", MaxTemplateBytes)
	}
	t := &textTemplate{}
	rest := s
	for rest != "" {
		open := strings.Index(rest, "{{")
		if open < 0 {
			t.parts = append(t.parts, templatePart{literal: rest})
			break
		}
		if open > 0 {
			t.parts = append(t.parts, templatePart{literal: rest[:open]})
		}
		after := rest[open+2:]
		end := strings.Index(after, "}}")
		if end < 0 || strings.Contains(after[:end], "{{") {
			return nil, errors.New("unclosed placeholder: \"{{\" without matching \"}}\"")
		}
		ph, err := parsePlaceholder(after[:end], fields)
		if err != nil {
			return nil, err
		}
		t.parts = append(t.parts, templatePart{ph: ph})
		rest = after[end+2:]
	}
	if len(t.parts) == 1 && t.parts[0].ph != nil {
		t.single = t.parts[0].ph
	}
	return t, nil
}

func parsePlaceholder(inner string, fields map[string]FieldSpec) (*fieldPath, error) {
	expr := strings.Trim(inner, " \t")
	switch {
	case expr == "":
		return nil, errors.New("empty placeholder")
	case strings.Contains(expr, "|"):
		return nil, fmt.Errorf("placeholder %q: pipes are not supported", truncate(expr))
	case strings.ContainsAny(expr, "()"):
		return nil, fmt.Errorf("placeholder %q: function calls are not supported", truncate(expr))
	case strings.ContainsAny(expr, " \t\r\n\"'`,+*/\\$!=<>&"):
		return nil, fmt.Errorf("placeholder %q: expressions are not supported, use {{ field }} or {{ field.key }}", truncate(expr))
	}
	fp, err := resolvePath(expr, fields)
	if err != nil {
		return nil, fmt.Errorf("placeholder: %w", err)
	}
	return &fp, nil
}

// ---------------------------------------------------------------------------
// Delivery plan

type namedTemplate struct {
	name string
	tmpl *textTemplate
}

type jsonNodeKind uint8

const (
	jsonLiteral jsonNodeKind = iota
	jsonTemplate
	jsonObject
	jsonArray
)

// jsonNode is a pre-parsed json segment template.
type jsonNode struct {
	kind     jsonNodeKind
	literal  any // nil, bool or float64
	tmpl     *textTemplate
	keys     []string // object keys, sorted, aligned with children
	children []*jsonNode
}

// renderPlan is the compiled deliver section of a type.
type renderPlan struct {
	cookiesField *fieldPath // cookies: "{{ cookie_map_field }}"
	cookies      []namedTemplate
	cookieHeader *textTemplate
	headers      []namedTemplate
	query        []namedTemplate
	json         *jsonNode
	values       []namedTemplate
}

// compileDeliver validates the deliver section and builds its render plan.
// Problems are appended to errs.
func compileDeliver(errs *problems, deliver map[string]any, fields map[string]FieldSpec) *renderPlan {
	plan := &renderPlan{}
	for _, seg := range sortedKeys(deliver) {
		v, err := normalizeJSONValue(deliver[seg], 0)
		if err != nil {
			errs.addf("deliver.%s: %v", truncate(seg), err)
			continue
		}
		switch seg {
		case SegmentCookies:
			compileCookiesSegment(errs, plan, v, fields)
		case SegmentCookieHeader:
			s, ok := v.(string)
			if !ok {
				errs.addf("deliver.cookie_header must be a string template")
				continue
			}
			t, err := parseTemplate(s, fields)
			if err != nil {
				errs.addf("deliver.cookie_header: %v", err)
				continue
			}
			if !validHeaderLiterals(t) {
				errs.addf("deliver.cookie_header: template contains characters not allowed in a header value")
				continue
			}
			plan.cookieHeader = t
		case SegmentHeaders:
			plan.headers = compileStringMap(errs, seg, v, fields, checkHeaderEntry)
		case SegmentQuery:
			plan.query = compileStringMap(errs, seg, v, fields, nil)
		case SegmentValues:
			plan.values = compileStringMap(errs, seg, v, fields, nil)
		case SegmentJSON:
			plan.json = compileJSONNode(errs, "deliver.json", v, fields, 0)
		default:
			errs.addf("deliver: unknown segment %q (allowed: cookies, cookie_header, headers, query, json, values)", truncate(seg))
		}
	}
	return plan
}

func compileCookiesSegment(errs *problems, plan *renderPlan, v any, fields map[string]FieldSpec) {
	if s, ok := v.(string); ok {
		t, err := parseTemplate(s, fields)
		if err != nil {
			errs.addf("deliver.cookies: %v", err)
			return
		}
		if t.single == nil || t.single.ftype != FieldCookieMap || len(t.single.sub) > 0 {
			errs.addf("deliver.cookies must be a single placeholder of a cookie_map field or a map of string templates")
			return
		}
		plan.cookiesField = t.single
		return
	}
	plan.cookies = compileStringMap(errs, SegmentCookies, v, fields, checkCookieEntry)
}

// entryCheck validates one entry of a delivery map.
type entryCheck func(name string, t *textTemplate) error

func checkHeaderEntry(name string, t *textTemplate) error {
	if !httpguts.ValidHeaderFieldName(name) {
		return errors.New("invalid header name")
	}
	if !validHeaderLiterals(t) {
		return errors.New("template contains characters not allowed in a header value")
	}
	return nil
}

func checkCookieEntry(name string, t *textTemplate) error {
	if err := validateCookieName(name); err != nil {
		return errors.New("invalid cookie name")
	}
	for _, p := range t.parts {
		switch {
		case p.ph == nil && !validCookieValue(p.literal):
			return errors.New("template contains characters not allowed in a cookie value")
		case p.ph != nil && p.ph.ftype == FieldCookieMap && len(p.ph.sub) == 0:
			// A whole cookie map renders as "k1=v1; k2=v2", which is not a
			// valid cookie value as soon as the map holds two cookies.
			return fmt.Errorf("placeholder %q renders a whole cookie map, use {{ %s.<name> }}", p.ph.raw, p.ph.field)
		}
	}
	return nil
}

func validHeaderLiterals(t *textTemplate) bool {
	for _, p := range t.parts {
		if p.ph == nil && !httpguts.ValidHeaderFieldValue(p.literal) {
			return false
		}
	}
	return true
}

func compileStringMap(errs *problems, seg string, v any, fields map[string]FieldSpec, check entryCheck) []namedTemplate {
	m, ok := v.(map[string]any)
	if !ok {
		errs.addf("deliver.%s must be a map of string templates", seg)
		return nil
	}
	if len(m) > MaxDeliverEntries {
		errs.addf("deliver.%s has %d entries, the maximum is %d", seg, len(m), MaxDeliverEntries)
		return nil
	}
	out := make([]namedTemplate, 0, len(m))
	for _, name := range sortedKeys(m) {
		label := fmt.Sprintf("deliver.%s[%q]", seg, truncate(name))
		if name == "" {
			errs.addf("deliver.%s: entry name is empty", seg)
			continue
		}
		s, ok := m[name].(string)
		if !ok {
			errs.addf("%s must be a string template", label)
			continue
		}
		t, err := parseTemplate(s, fields)
		if err != nil {
			errs.addf("%s: %v", label, err)
			continue
		}
		if check != nil {
			if err := check(name, t); err != nil {
				errs.addf("%s: %v", label, err)
				continue
			}
		}
		out = append(out, namedTemplate{name: name, tmpl: t})
	}
	return out
}

func compileJSONNode(errs *problems, label string, v any, fields map[string]FieldSpec, depth int) *jsonNode {
	if depth > maxJSONDepth {
		errs.addf("%s: %v", label, errJSONTooDeep)
		return nil
	}
	switch t := v.(type) {
	case string:
		tmpl, err := parseTemplate(t, fields)
		if err != nil {
			errs.addf("%s: %v", label, err)
			return nil
		}
		return &jsonNode{kind: jsonTemplate, tmpl: tmpl}
	case map[string]any:
		if len(t) > MaxDeliverEntries {
			errs.addf("%s has %d entries, the maximum is %d", label, len(t), MaxDeliverEntries)
			return nil
		}
		node := &jsonNode{kind: jsonObject, keys: sortedKeys(t)}
		node.children = make([]*jsonNode, len(node.keys))
		for i, k := range node.keys {
			node.children[i] = compileJSONNode(errs, fmt.Sprintf("%s.%s", label, truncate(k)), t[k], fields, depth+1)
		}
		return node
	case []any:
		if len(t) > MaxDeliverEntries {
			errs.addf("%s has %d entries, the maximum is %d", label, len(t), MaxDeliverEntries)
			return nil
		}
		node := &jsonNode{kind: jsonArray, children: make([]*jsonNode, len(t))}
		for i, e := range t {
			node.children[i] = compileJSONNode(errs, fmt.Sprintf("%s[%d]", label, i), e, fields, depth+1)
		}
		return node
	default: // nil, bool, float64 (normalizeJSONValue already ran)
		return &jsonNode{kind: jsonLiteral, literal: t}
	}
}

// ---------------------------------------------------------------------------
// Rendering

// Render builds the Credential for payload. secret_ref fields referenced by
// the delivery templates are resolved through resolve (each distinct path at
// most once per call); the boolean result reports whether any secret was
// resolved; resolved secrets must be valid UTF-8. Missing optional fields
// render as empty strings, omitted map entries or JSON null. JSON and Values
// only hold JSON value types (nil, bool, float64, string, []any,
// map[string]any).
func (c *CompiledType) Render(ctx context.Context, payload map[string]any, resolve SecretResolver) (*Credential, bool, error) {
	r := renderer{ctx: ctx, typeName: c.Name, payload: payload, resolve: resolve}
	cred, err := r.render(c.plan)
	if err != nil {
		return nil, false, fmt.Errorf("render identity type %q: %w", c.Name, err)
	}
	return cred, r.usedSecrets, nil
}

type renderer struct {
	ctx         context.Context
	typeName    string
	payload     map[string]any
	resolve     SecretResolver
	usedSecrets bool
	secrets     map[string]string
	cookieMaps  map[string]map[string]string
}

func (r *renderer) render(plan *renderPlan) (*Credential, error) {
	cred := &Credential{
		Headers: make(map[string]string, len(plan.headers)),
		Query:   make(map[string]string, len(plan.query)),
		Values:  make(map[string]any, len(plan.values)),
	}
	if err := r.renderCookies(plan, cred); err != nil {
		return nil, err
	}
	if plan.cookieHeader != nil {
		s, err := r.text(plan.cookieHeader)
		if err != nil {
			return nil, fmt.Errorf("cookie_header: %w", err)
		}
		if !httpguts.ValidHeaderFieldValue(s) {
			return nil, errors.New("cookie_header: rendered value contains invalid characters")
		}
		cred.CookieHeader = s
	}
	for _, e := range plan.headers {
		s, err := r.text(e.tmpl)
		if err != nil {
			return nil, fmt.Errorf("headers[%q]: %w", e.name, err)
		}
		if s == "" {
			continue
		}
		if !httpguts.ValidHeaderFieldValue(s) {
			return nil, fmt.Errorf("headers[%q]: rendered value contains invalid characters", e.name)
		}
		cred.Headers[e.name] = s
	}
	for _, e := range plan.query {
		s, err := r.text(e.tmpl)
		if err != nil {
			return nil, fmt.Errorf("query[%q]: %w", e.name, err)
		}
		if s != "" {
			cred.Query[e.name] = s
		}
	}
	if plan.json != nil {
		v, err := r.jsonValue(plan.json)
		if err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
		cred.JSON = v
	}
	for _, e := range plan.values {
		v, err := r.typed(e.tmpl)
		if err != nil {
			return nil, fmt.Errorf("values[%q]: %w", e.name, err)
		}
		if v == nil || v == "" {
			continue
		}
		cred.Values[e.name] = v
	}
	return cred, nil
}

func (r *renderer) renderCookies(plan *renderPlan, cred *Credential) error {
	if plan.cookiesField != nil {
		v, err := r.value(plan.cookiesField)
		if err != nil {
			return fmt.Errorf("cookies: %w", err)
		}
		if cm, ok := v.(map[string]string); ok && len(cm) > 0 {
			cred.Cookies = maps.Clone(cm)
		} else {
			cred.Cookies = make(map[string]string)
		}
		return nil
	}
	cred.Cookies = make(map[string]string, len(plan.cookies))
	for _, e := range plan.cookies {
		s, err := r.text(e.tmpl)
		if err != nil {
			return fmt.Errorf("cookies[%q]: %w", e.name, err)
		}
		if s == "" {
			continue
		}
		if !validCookieValue(s) {
			return fmt.Errorf("cookies[%q]: rendered value contains invalid characters", e.name)
		}
		cred.Cookies[e.name] = s
	}
	return nil
}

func (r *renderer) jsonValue(n *jsonNode) (any, error) {
	switch n.kind {
	case jsonTemplate:
		return r.typed(n.tmpl)
	case jsonObject:
		out := make(map[string]any, len(n.keys))
		for i, k := range n.keys {
			v, err := r.jsonValue(n.children[i])
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case jsonArray:
		out := make([]any, len(n.children))
		for i, child := range n.children {
			v, err := r.jsonValue(child)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	default:
		return n.literal, nil
	}
}

// typed renders t keeping the value type for single-placeholder templates.
// The result only uses JSON value types (nil, bool, float64, string, []any,
// map[string]any), so it converts to protobuf Value/Struct, and composite
// values are deep copies so credentials never alias payloads.
func (r *renderer) typed(t *textTemplate) (any, error) {
	if t.single == nil {
		return r.text(t)
	}
	v, err := r.value(t.single)
	if err != nil {
		return nil, err
	}
	return normalizeJSONValue(v, 0)
}

// text renders t as a string.
func (r *renderer) text(t *textTemplate) (string, error) {
	if t.single != nil {
		v, err := r.value(t.single)
		if err != nil {
			return "", err
		}
		return stringify(v)
	}
	var b strings.Builder
	for _, p := range t.parts {
		if p.ph == nil {
			b.WriteString(p.literal)
			continue
		}
		v, err := r.value(p.ph)
		if err != nil {
			return "", err
		}
		s, err := stringify(v)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// value looks up the typed value addressed by p: string, float64, bool,
// map[string]string (whole cookie_map), a JSON value, or nil when unset.
func (r *renderer) value(p *fieldPath) (any, error) {
	raw, ok := r.payload[p.field]
	if !ok || raw == nil {
		return nil, nil
	}
	switch p.ftype {
	case FieldCookieMap:
		cm, err := r.cookieMap(p.field, raw)
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
		return lookupJSON(raw, p.sub), nil
	case FieldSecretRef:
		path, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: secret reference must be a string", p.field)
		}
		if path == "" {
			return nil, nil
		}
		if !ValidSecretRefPath(path) {
			// Stored payloads predating the path checks never resolve paths
			// outside the canonical namespace-relative form.
			return nil, fmt.Errorf("field %q: secret reference is not a namespace-relative secret path", p.field)
		}
		return r.secret(p.field, path)
	case FieldNumber:
		f, err := coerceNumber(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", p.field, err)
		}
		return f, nil
	case FieldBool:
		b, err := coerceBool(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", p.field, err)
		}
		return b, nil
	default:
		s, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected a string", p.field)
		}
		return s, nil
	}
}

func (r *renderer) cookieMap(field string, raw any) (map[string]string, error) {
	if cm, ok := raw.(map[string]string); ok {
		return cm, nil
	}
	if cm, ok := r.cookieMaps[field]; ok {
		return cm, nil
	}
	cm, err := ParseCookieMap(raw)
	if err != nil {
		// Stored payloads are server-side data: do not surface the parse
		// failure as a client invalid_argument error.
		return nil, fmt.Errorf("field %q: %s", field, errorMessage(err))
	}
	if r.cookieMaps == nil {
		r.cookieMaps = make(map[string]map[string]string)
	}
	r.cookieMaps[field] = cm
	return cm, nil
}

func (r *renderer) secret(field, path string) (string, error) {
	if s, ok := r.secrets[path]; ok {
		return s, nil
	}
	if r.resolve == nil {
		return "", fmt.Errorf("field %q references secret %q but no secret resolver is configured", field, truncate(path))
	}
	s, err := r.resolve(r.ctx, path)
	if err != nil {
		return "", fmt.Errorf("resolve secret %q for field %q: %w", truncate(path), field, err)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("secret %q for field %q: %w", truncate(path), field, errInvalidUTF8)
	}
	if r.secrets == nil {
		r.secrets = make(map[string]string)
	}
	r.secrets[path] = s
	r.usedSecrets = true
	return s, nil
}

// lookupJSON walks sub keys through objects (by key) and arrays (by
// non-negative decimal index). It returns nil when the path does not exist.
func lookupJSON(v any, sub []string) any {
	for _, key := range sub {
		switch t := v.(type) {
		case map[string]any:
			v = t[key]
		case map[string]string:
			s, ok := t[key]
			if !ok {
				return nil
			}
			v = s
		case []any:
			idx, err := strconv.Atoi(key)
			if err != nil || idx < 0 || idx >= len(t) {
				return nil
			}
			v = t[idx]
		default:
			return nil
		}
	}
	return v
}

// stringify converts a typed value to its interpolation form: strings as is,
// numbers in canonical form, cookie maps as a Cookie header, other JSON
// values as canonical JSON and nil as "".
func stringify(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case float64:
		if err := finite(t); err != nil {
			return "", err
		}
		return formatNumber(t), nil
	case map[string]string:
		return cookieHeader(t), nil
	default:
		b, err := CanonicalJSON(v)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
