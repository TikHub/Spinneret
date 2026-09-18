package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// newBase returns an unnamed spec of kind carrying every default. Decoding on
// top of it keeps explicitly written zero values.
func newBase(kind Kind) (Spec, error) {
	switch kind {
	case KindRotation:
		return newRotationBase(), nil
	case KindSignal:
		return newSignalBase(), nil
	case KindAction:
		return newActionBase(), nil
	case KindBreaker:
		return newBreakerBase(), nil
	}
	return nil, fmt.Errorf("policy: unknown kind %q", kind)
}

// ParseYAML strictly decodes a single YAML document (unknown fields are
// rejected), applies defaults and validates the result. Validation failures
// are returned as *ValidationError.
func ParseYAML(kind Kind, data []byte) (Spec, error) {
	spec, err := decodeYAML(kind, data)
	if err != nil {
		return nil, err
	}
	return finish(spec)
}

// ParseJSON strictly decodes a JSON object (unknown fields are rejected),
// applies defaults and validates the result. Validation failures are returned
// as *ValidationError.
func ParseJSON(kind Kind, data []byte) (Spec, error) {
	spec, err := decodeJSON(kind, data)
	if err != nil {
		return nil, err
	}
	return finish(spec)
}

func finish(spec Spec) (Spec, error) {
	spec.ApplyDefaults()
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return spec, nil
}

func decodeYAML(kind Kind, data []byte) (Spec, error) {
	spec, err := newBase(kind)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(spec); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse %s policy YAML: document is empty", kind)
		}
		return nil, fmt.Errorf("parse %s policy YAML: %w", kind, err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, fmt.Errorf("parse %s policy YAML: %w", kind, err)
	default:
		return nil, fmt.Errorf("parse %s policy YAML: multiple documents are not allowed", kind)
	}
	return spec, nil
}

func decodeJSON(kind Kind, data []byte) (Spec, error) {
	spec, err := newBase(kind)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("parse %s policy JSON: document is empty", kind)
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("parse %s policy JSON: document must be an object", kind)
	}
	if err := strictJSON(trimmed, spec); err != nil {
		return nil, fmt.Errorf("parse %s policy JSON: %w", kind, err)
	}
	return spec, nil
}

// MarshalYAML encodes spec as YAML (two-space indentation) after applying
// defaults to a copy; spec itself is not modified.
func MarshalYAML(spec Spec) ([]byte, error) {
	c, err := canonicalCopy(spec)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("encode %s policy YAML: %w", c.Kind(), err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode %s policy YAML: %w", c.Kind(), err)
	}
	return buf.Bytes(), nil
}

// MarshalJSON encodes the canonical JSON form stored in policy_versions.spec:
// defaults applied (to a copy; spec is not modified), fields in declaration
// order, map keys sorted, empty lists as [].
func MarshalJSON(spec Spec) ([]byte, error) {
	c, err := canonicalCopy(spec)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode %s policy JSON: %w", c.Kind(), err)
	}
	return out, nil
}

// Clone returns a deep copy of spec with defaults applied. It returns an error
// for nil or unknown specs.
func Clone(spec Spec) (Spec, error) {
	return canonicalCopy(spec)
}

// canonicalCopy deep-copies spec through JSON and applies defaults.
func canonicalCopy(spec Spec) (Spec, error) {
	if isNilSpec(spec) {
		return nil, errors.New("policy: spec is nil")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("copy %s policy: %w", spec.Kind(), err)
	}
	c, err := decodeJSON(spec.Kind(), raw)
	if err != nil {
		return nil, fmt.Errorf("copy %s policy: %w", spec.Kind(), err)
	}
	c.ApplyDefaults()
	return c, nil
}

// isNilSpec reports whether spec is nil or a typed nil pointer.
func isNilSpec(spec Spec) bool {
	switch s := spec.(type) {
	case nil:
		return true
	case *RotationSpec:
		return s == nil
	case *SignalSpec:
		return s == nil
	case *ActionSpec:
		return s == nil
	case *BreakerSpec:
		return s == nil
	}
	return false
}
