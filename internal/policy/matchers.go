package policy

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// maxRangeBound bounds IntRange limits so inclusive conversions never overflow.
const maxRangeBound = int64(1) << 62

// IntRange is an integer interval with optional bounds; at least one bound is
// required. gte/gt and lte/lt are mutually exclusive.
type IntRange struct {
	Gte *int64 `yaml:"gte,omitempty" json:"gte,omitempty"`
	Gt  *int64 `yaml:"gt,omitempty" json:"gt,omitempty"`
	Lte *int64 `yaml:"lte,omitempty" json:"lte,omitempty"`
	Lt  *int64 `yaml:"lt,omitempty" json:"lt,omitempty"`
}

// Bounds returns the inclusive lower and upper limits of the range. Open ends
// are math.MinInt64 and math.MaxInt64; an unsatisfiable range (for example
// gt: MaxInt64 or lt: MinInt64) returns lo > hi.
func (r IntRange) Bounds() (lo, hi int64) {
	lo, hi = math.MinInt64, math.MaxInt64
	if r.Gte != nil {
		lo = *r.Gte
	}
	if r.Gt != nil {
		if *r.Gt == math.MaxInt64 {
			return math.MaxInt64, math.MinInt64
		}
		lo = *r.Gt + 1
	}
	if r.Lte != nil {
		hi = *r.Lte
	}
	if r.Lt != nil {
		if *r.Lt == math.MinInt64 {
			return math.MaxInt64, math.MinInt64
		}
		hi = *r.Lt - 1
	}
	return lo, hi
}

// Contains reports whether v lies within the range.
func (r IntRange) Contains(v int64) bool {
	lo, hi := r.Bounds()
	return v >= lo && v <= hi
}

// validate checks the bounds are set, exclusive, within [minV, maxV] and
// describe a non-empty interval.
func (r IntRange) validate(v *validator, path string, minV, maxV int64) {
	if r.Gte == nil && r.Gt == nil && r.Lte == nil && r.Lt == nil {
		v.addf(path, "must set at least one of gte, gt, lte, lt")
		return
	}
	if r.Gte != nil && r.Gt != nil {
		v.addf(path, "gte and gt are mutually exclusive")
	}
	if r.Lte != nil && r.Lt != nil {
		v.addf(path, "lte and lt are mutually exclusive")
	}
	ok := true
	for _, b := range []struct {
		name string
		val  *int64
	}{{"gte", r.Gte}, {"gt", r.Gt}, {"lte", r.Lte}, {"lt", r.Lt}} {
		switch {
		case b.val == nil:
		case maxV >= maxRangeBound && *b.val < minV:
			v.addf(field(path, b.name), "must be at least %d (got %d)", minV, *b.val)
			ok = false
		case maxV >= maxRangeBound && *b.val > maxV:
			v.addf(field(path, b.name), "must be at most 2^62 (got %d)", *b.val)
			ok = false
		case *b.val < minV || *b.val > maxV:
			v.addf(field(path, b.name), "must be between %d and %d (got %d)", minV, maxV, *b.val)
			ok = false
		}
	}
	if !ok {
		return
	}
	lo, hi := r.Bounds()
	if lo < minV {
		lo = minV
	}
	if hi > maxV {
		hi = maxV
	}
	if lo > hi {
		v.addf(path, "describes an empty range")
	}
}

// IntMatcher matches an integer against a list of values or a range. In YAML
// and JSON it is written either as a list (or a single integer) or as an
// object {gte, gt, lte, lt}.
type IntMatcher struct {
	Values []int
	Range  *IntRange
}

// MarshalYAML encodes the list or range form.
func (m IntMatcher) MarshalYAML() (any, error) {
	if m.Range != nil {
		return m.Range, nil
	}
	if m.Values == nil {
		return []int{}, nil
	}
	return m.Values, nil
}

// MarshalJSON encodes the list or range form.
func (m IntMatcher) MarshalJSON() ([]byte, error) {
	if m.Range != nil {
		return json.Marshal(m.Range)
	}
	if m.Values == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(m.Values)
}

// UnmarshalYAML decodes a list, a single integer or a strict range mapping.
func (m *IntMatcher) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	switch n.Kind {
	case yaml.SequenceNode:
		values := make([]int, 0, len(n.Content))
		for _, c := range n.Content {
			x, err := yamlInt(resolveAlias(c))
			if err != nil {
				return err
			}
			values = append(values, x)
		}
		*m = IntMatcher{Values: values}
	case yaml.ScalarNode:
		x, err := yamlInt(n)
		if err != nil {
			return err
		}
		*m = IntMatcher{Values: []int{x}}
	case yaml.MappingNode:
		r, err := yamlRange(n)
		if err != nil {
			return err
		}
		*m = IntMatcher{Range: &r}
	default:
		return fmt.Errorf("line %d: expected a list of integers or a {gte, gt, lte, lt} range", n.Line)
	}
	return nil
}

// UnmarshalJSON decodes a list, a single integer or a strict range object.
func (m *IntMatcher) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch {
	case len(data) == 0:
		return errors.New("expected a list of integers or a {gte, gt, lte, lt} range")
	case string(data) == "null":
		return nil
	case data[0] == '[':
		var values []int
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("expected a list of integers: %w", err)
		}
		if values == nil {
			values = []int{}
		}
		*m = IntMatcher{Values: values}
	case data[0] == '{':
		var r IntRange
		if err := strictJSON(data, &r); err != nil {
			return fmt.Errorf("invalid range: %w", err)
		}
		*m = IntMatcher{Range: &r}
	default:
		var x int
		if err := json.Unmarshal(data, &x); err != nil {
			return fmt.Errorf("expected a list of integers or a {gte, gt, lte, lt} range: %w", err)
		}
		*m = IntMatcher{Values: []int{x}}
	}
	return nil
}

// StringList is a list of strings that also accepts a single scalar. Numbers
// are normalized to their decimal string form (so business_code: [10001] and
// ["10001"] are equivalent). An explicitly empty list decodes to a non-nil
// empty StringList so validation can reject it.
type StringList []string

// MarshalYAML always encodes a list.
func (l StringList) MarshalYAML() (any, error) {
	if l == nil {
		return []string{}, nil
	}
	return []string(l), nil
}

// MarshalJSON always encodes a list.
func (l StringList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(l))
}

// UnmarshalYAML decodes a scalar or a list of scalars.
func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	values, err := yamlStrings(n)
	if err != nil {
		return err
	}
	*l = values
	return nil
}

// UnmarshalJSON decodes a string, a number or a list of them.
func (l *StringList) UnmarshalJSON(data []byte) error {
	values, err := jsonStrings(data)
	if err != nil {
		return err
	}
	if values != nil {
		*l = values
	}
	return nil
}

// Contains reports whether s is in the list.
func (l StringList) Contains(s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// OutcomeList is a list of outcomes that also accepts a single scalar. YAML
// encodes a single outcome as a scalar; JSON always encodes a list.
type OutcomeList []string

// MarshalYAML encodes one outcome as a scalar and several as a list.
func (l OutcomeList) MarshalYAML() (any, error) {
	switch len(l) {
	case 0:
		return []string{}, nil
	case 1:
		return l[0], nil
	}
	return []string(l), nil
}

// MarshalJSON always encodes a list.
func (l OutcomeList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(l))
}

// UnmarshalYAML decodes a scalar or a list of scalars.
func (l *OutcomeList) UnmarshalYAML(n *yaml.Node) error {
	values, err := yamlStrings(n)
	if err != nil {
		return err
	}
	*l = OutcomeList(values)
	return nil
}

// UnmarshalJSON decodes a string or a list of strings.
func (l *OutcomeList) UnmarshalJSON(data []byte) error {
	values, err := jsonStrings(data)
	if err != nil {
		return err
	}
	if values != nil {
		*l = OutcomeList(values)
	}
	return nil
}

// Contains reports whether outcome is in the list.
func (l OutcomeList) Contains(outcome string) bool {
	for _, v := range l {
		if v == outcome {
			return true
		}
	}
	return false
}

// URIMatcher matches the request path by prefix or RE2 regular expression;
// exactly one must be set.
type URIMatcher struct {
	Prefix string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
	Regex  string `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// RevertMode selects which recent cooldowns are reverted when a breaker opens.
type RevertMode string

// RevertMode values.
const (
	RevertNone     RevertMode = "none"
	RevertEndpoint RevertMode = "endpoint"
	RevertAll      RevertMode = "all"
)

// ValidRevertMode reports whether m is a known revert mode.
func ValidRevertMode(m RevertMode) bool {
	switch m {
	case RevertNone, RevertEndpoint, RevertAll:
		return true
	}
	return false
}

// UnmarshalYAML accepts none|endpoint|all or a boolean (true = endpoint,
// false = none).
func (m *RevertMode) UnmarshalYAML(n *yaml.Node) error {
	n = resolveAlias(n)
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: revert mode must be none, endpoint, all or a boolean", n.Line)
	}
	switch n.ShortTag() {
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return fmt.Errorf("line %d: invalid boolean: %w", n.Line, err)
		}
		*m = revertFromBool(b)
	case "!!str":
		*m = RevertMode(n.Value)
	default:
		return fmt.Errorf("line %d: revert mode must be none, endpoint, all or a boolean", n.Line)
	}
	return nil
}

// UnmarshalJSON accepts none|endpoint|all or a boolean.
func (m *RevertMode) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch string(data) {
	case "null":
		return nil
	case "true", "false":
		*m = revertFromBool(string(data) == "true")
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("revert mode must be none, endpoint, all or a boolean")
	}
	*m = RevertMode(s)
	return nil
}

func revertFromBool(b bool) RevertMode {
	if b {
		return RevertEndpoint
	}
	return RevertNone
}

// resolveAlias follows YAML alias nodes.
func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// yamlInt decodes an integer scalar. Decimal literals with leading zeros are
// rejected because YAML silently reads them as octal (010 would become 8).
func yamlInt(n *yaml.Node) (int, error) {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" {
		return 0, fmt.Errorf("line %d: expected an integer (got %q)", n.Line, n.Value)
	}
	if hasLeadingZero(n.Value) {
		return 0, fmt.Errorf("line %d: integer %q must not have leading zeros", n.Line, n.Value)
	}
	var x int
	if err := n.Decode(&x); err != nil {
		return 0, fmt.Errorf("line %d: invalid integer %q", n.Line, n.Value)
	}
	return x, nil
}

// yamlRange strictly decodes a {gte, gt, lte, lt} mapping.
func yamlRange(n *yaml.Node) (IntRange, error) {
	var r IntRange
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := resolveAlias(n.Content[i]), resolveAlias(n.Content[i+1])
		var dst **int64
		switch key.Value {
		case "gte":
			dst = &r.Gte
		case "gt":
			dst = &r.Gt
		case "lte":
			dst = &r.Lte
		case "lt":
			dst = &r.Lt
		default:
			return IntRange{}, fmt.Errorf("line %d: field %s not found in range (allowed: gte, gt, lte, lt)", key.Line, key.Value)
		}
		if *dst != nil {
			return IntRange{}, fmt.Errorf("line %d: field %s already set in range", key.Line, key.Value)
		}
		x, err := yamlInt(val)
		if err != nil {
			return IntRange{}, err
		}
		v := int64(x)
		*dst = &v
	}
	return r, nil
}

// yamlScalarString converts a string or number scalar to a string. Plain
// decimal integer literals are kept exactly as written, so leading zeros
// survive (business_code: [0000] matches "0000" instead of "0", and 0010 is not
// reinterpreted as octal 8). Other numeric forms (hex, octal, underscores,
// exponents, fractions) are normalized to their decimal representation.
func yamlScalarString(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("line %d: expected a string or number", n.Line)
	}
	tag := n.ShortTag()
	if (tag == "!!int" || tag == "!!float") && isDecimalLiteral(n.Value) {
		return n.Value, nil
	}
	switch tag {
	case "!!str":
		return n.Value, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err == nil {
			return strconv.FormatInt(i, 10), nil
		}
		var u uint64
		if err := n.Decode(&u); err == nil {
			return strconv.FormatUint(u, 10), nil
		}
		return "", fmt.Errorf("line %d: invalid integer %q", n.Line, n.Value)
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return "", fmt.Errorf("line %d: invalid number %q", n.Line, n.Value)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("line %d: expected a string or number (got %q)", n.Line, n.Value)
}

// isDecimalLiteral reports whether s is an optional '-' followed by one or more
// ASCII digits.
func isDecimalLiteral(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// hasLeadingZero reports whether s is a decimal literal with a redundant
// leading zero, such as "010" or "-007".
func hasLeadingZero(s string) bool {
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	return len(digits) > 1 && digits[0] == '0' && isDecimalLiteral(digits)
}

// yamlStrings decodes a scalar or a sequence of scalars.
func yamlStrings(n *yaml.Node) ([]string, error) {
	n = resolveAlias(n)
	switch n.Kind {
	case yaml.ScalarNode:
		s, err := yamlScalarString(n)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			s, err := yamlScalarString(resolveAlias(c))
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
}

// jsonScalarString converts a JSON string or number to a string.
func jsonScalarString(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", errors.New("expected a string or number")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("invalid string: %w", err)
		}
		return s, nil
	}
	if string(raw) == "null" {
		return "", errors.New("expected a string or number (got null)")
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return "", fmt.Errorf("expected a string or number (got %s)", raw)
	}
	if i, err := num.Int64(); err == nil {
		return strconv.FormatInt(i, 10), nil
	}
	// Numbers that do not fit a float64 (out of range) keep their literal form.
	if f, err := num.Float64(); err == nil && !math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	return num.String(), nil
}

// jsonStrings decodes a string, a number or a list of them. It returns
// (nil, nil) for JSON null.
func jsonStrings(data []byte) ([]string, error) {
	data = bytes.TrimSpace(data)
	if string(data) == "null" {
		return nil, nil
	}
	if len(data) > 0 && data[0] == '[' {
		var raws []json.RawMessage
		if err := json.Unmarshal(data, &raws); err != nil {
			return nil, fmt.Errorf("expected a list of strings: %w", err)
		}
		out := make([]string, 0, len(raws))
		for _, r := range raws {
			s, err := jsonScalarString(r)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	}
	s, err := jsonScalarString(data)
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

// strictJSON decodes data into v (a non-nil pointer) rejecting unknown fields
// and trailing data. encoding/json matches member names case-insensitively
// and silently keeps the last of duplicate names, so after the decode (which
// provides the merge-into-defaults semantics) the document is checked once
// more with encoding/json/v2, which rejects names that differ in case,
// duplicate names and invalid UTF-8.
func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing data")
	}
	// The successful Decode above guarantees v is a non-nil pointer.
	shape := reflect.New(reflect.TypeOf(v).Elem()).Interface()
	return jsonv2.Unmarshal(data, shape, jsonv2.RejectUnknownMembers(true))
}
