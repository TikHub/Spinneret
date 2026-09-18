package policy

import (
	"fmt"
)

// Kind identifies one of the four policy kinds.
type Kind string

// Policy kinds.
const (
	KindRotation Kind = "rotation"
	KindSignal   Kind = "signal"
	KindAction   Kind = "action"
	KindBreaker  Kind = "breaker"
)

const (
	// MaxNameLength is the maximum length of policy and rule names.
	MaxNameLength = 64
	// MaxExtendsDepth is the maximum number of policies in an extends chain,
	// the leaf included.
	MaxExtendsDepth = 5
	// maxBindingFieldLength bounds the site, client and endpoint group names
	// of a bind block.
	maxBindingFieldLength = 128
)

// Kinds returns every policy kind in a stable order.
func Kinds() []Kind {
	return []Kind{KindRotation, KindSignal, KindAction, KindBreaker}
}

// ValidKind reports whether k is a known policy kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindRotation, KindSignal, KindAction, KindBreaker:
		return true
	}
	return false
}

// DefaultPolicyName returns the name of the built-in default policy of kind,
// e.g. "default-rotation". It returns "" for unknown kinds.
func DefaultPolicyName(kind Kind) string {
	if !ValidKind(kind) {
		return ""
	}
	return "default-" + string(kind)
}

// Binding is the optional bind block of a policy. It is converted to a
// policy binding row when the policy is created.
type Binding struct {
	Site          string `yaml:"site" json:"site"`
	Client        string `yaml:"client,omitempty" json:"client,omitempty"`
	EndpointGroup string `yaml:"endpoint_group,omitempty" json:"endpoint_group,omitempty"`
}

// Spec is implemented by *RotationSpec, *SignalSpec, *ActionSpec and
// *BreakerSpec.
type Spec interface {
	// Kind returns the policy kind.
	Kind() Kind
	// PolicyName returns the policy name.
	PolicyName() string
	// PolicyBinding returns a copy of the bind block, or nil when absent.
	PolicyBinding() *Binding
	// Validate returns a *ValidationError listing every problem, or nil.
	Validate() error
	// ApplyDefaults fills zero values whose zero is not a legal setting.
	ApplyDefaults()
}

// ValidName reports whether s matches ^[a-z0-9][a-z0-9._-]{0,63}$, the pattern
// shared by policy names, extends references and rule names.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > MaxNameLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// ParentName returns the extends reference of a signal or action spec and ""
// for every other spec (including nil).
func ParentName(spec Spec) string {
	switch s := spec.(type) {
	case *SignalSpec:
		if s != nil {
			return s.Extends
		}
	case *ActionSpec:
		if s != nil {
			return s.Extends
		}
	}
	return ""
}

// SignalChain converts a chain returned by ResolveExtends into signal specs.
func SignalChain(specs []Spec) ([]*SignalSpec, error) {
	out := make([]*SignalSpec, 0, len(specs))
	for i, s := range specs {
		sig, ok := s.(*SignalSpec)
		if !ok || sig == nil {
			return nil, fmt.Errorf("policy: chain element %d is not a signal policy", i)
		}
		out = append(out, sig)
	}
	return out, nil
}

// ActionChain converts a chain returned by ResolveExtends into action specs.
func ActionChain(specs []Spec) ([]*ActionSpec, error) {
	out := make([]*ActionSpec, 0, len(specs))
	for i, s := range specs {
		act, ok := s.(*ActionSpec)
		if !ok || act == nil {
			return nil, fmt.Errorf("policy: chain element %d is not an action policy", i)
		}
		out = append(out, act)
	}
	return out, nil
}

// copyBinding returns a copy of b (nil when b is nil).
func copyBinding(b *Binding) *Binding {
	if b == nil {
		return nil
	}
	c := *b
	return &c
}

// validateCommon checks the fields shared by every kind.
func validateCommon(v *validator, name, description string, bind *Binding) {
	switch {
	case name == "":
		v.addf("name", "is required")
	case !ValidName(name):
		v.addf("name", "must match ^[a-z0-9][a-z0-9._-]{0,63}$ (got %q)", name)
	}
	if len(description) > 1024 {
		v.addf("description", "must be at most 1024 characters")
	}
	if bind == nil {
		return
	}
	if bind.Site == "" {
		v.addf("bind.site", "is required when bind is set")
	}
	for _, f := range []struct{ path, value string }{
		{"bind.site", bind.Site},
		{"bind.client", bind.Client},
		{"bind.endpoint_group", bind.EndpointGroup},
	} {
		if len(f.value) > maxBindingFieldLength {
			v.addf(f.path, "must be at most %d characters", maxBindingFieldLength)
		}
	}
}

// validateExtends checks an extends reference.
func validateExtends(v *validator, name, extends string) {
	if extends == "" {
		return
	}
	switch {
	case !ValidName(extends):
		v.addf("extends", "must match ^[a-z0-9][a-z0-9._-]{0,63}$ (got %q)", extends)
	case extends == name:
		v.addf("extends", "a policy cannot extend itself")
	}
}
