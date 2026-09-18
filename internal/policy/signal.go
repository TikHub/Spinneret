package policy

import (
	"regexp"
	"strings"
)

// Signal rule limits.
const (
	maxHTTPStatus    = 999
	maxURIPatternLen = 1024
)

// SignalSpec is a signal policy: ordered rules that classify report facts into
// outcomes. The first matching rule wins.
type SignalSpec struct {
	Name             string       `yaml:"name" json:"name"`
	Description      string       `yaml:"description,omitempty" json:"description,omitempty"`
	Bind             *Binding     `yaml:"bind,omitempty" json:"bind,omitempty"`
	Extends          string       `yaml:"extends,omitempty" json:"extends,omitempty"`
	TrustOutcomeHint bool         `yaml:"trust_outcome_hint" json:"trust_outcome_hint"`
	Rules            []SignalRule `yaml:"rules" json:"rules"`
}

// SignalRule maps report facts matching When to Outcome. Blame, when set,
// overrides DefaultBlame(Outcome).
type SignalRule struct {
	Name    string     `yaml:"name,omitempty" json:"name,omitempty"`
	When    SignalWhen `yaml:"when" json:"when"`
	Outcome string     `yaml:"outcome" json:"outcome"`
	Blame   Blame      `yaml:"blame,omitempty" json:"blame,omitempty"`
}

// SignalWhen lists the conditions of a signal rule. All present conditions
// must match; a rule without conditions matches every report.
type SignalWhen struct {
	// HTTPStatus matches the status code; 0 in a list means "no status". A
	// range never matches a missing status.
	HTTPStatus *IntMatcher `yaml:"http_status,omitempty" json:"http_status,omitempty"`
	// BusinessCode matches any listed business code.
	BusinessCode StringList `yaml:"business_code,omitempty" json:"business_code,omitempty"`
	// ErrorKind matches any listed node-side error kind.
	ErrorKind StringList `yaml:"error_kind,omitempty" json:"error_kind,omitempty"`
	// Markers matches when any reported marker is listed.
	Markers StringList `yaml:"markers,omitempty" json:"markers,omitempty"`
	// URI matches the request path by prefix or regular expression.
	URI *URIMatcher `yaml:"uri,omitempty" json:"uri,omitempty"`
	// Method matches any listed HTTP method (case-insensitive).
	Method StringList `yaml:"method,omitempty" json:"method,omitempty"`
	// LatencyMs matches the request latency in milliseconds.
	LatencyMs *IntRange `yaml:"latency_ms,omitempty" json:"latency_ms,omitempty"`
	// ResponseBytes matches the response body size.
	ResponseBytes *IntRange `yaml:"response_bytes,omitempty" json:"response_bytes,omitempty"`
}

// newSignalBase returns an unnamed signal spec carrying every default.
func newSignalBase() *SignalSpec {
	s := &SignalSpec{}
	s.ApplyDefaults()
	return s
}

// Kind implements Spec.
func (s *SignalSpec) Kind() Kind { return KindSignal }

// PolicyName implements Spec.
func (s *SignalSpec) PolicyName() string {
	if s == nil {
		return ""
	}
	return s.Name
}

// PolicyBinding implements Spec.
func (s *SignalSpec) PolicyBinding() *Binding {
	if s == nil {
		return nil
	}
	return copyBinding(s.Bind)
}

// ApplyDefaults implements Spec. Signal policies have no defaults besides an
// empty rule list.
func (s *SignalSpec) ApplyDefaults() {
	if s == nil {
		return
	}
	if s.Rules == nil {
		s.Rules = []SignalRule{}
	}
}

// Validate implements Spec.
func (s *SignalSpec) Validate() error {
	if s == nil {
		return &ValidationError{Kind: KindSignal, Problems: []Problem{{Message: "spec is nil"}}}
	}
	v := &validator{}
	validateCommon(v, s.Name, s.Description, s.Bind)
	validateExtends(v, s.Name, s.Extends)
	names := make(map[string]int, len(s.Rules))
	for i := range s.Rules {
		path := item("rules", i)
		r := &s.Rules[i]
		validateRuleName(v, path, r.Name, i, names)
		switch {
		case r.Outcome == "":
			v.addf(field(path, "outcome"), "is required")
		case !ValidOutcome(r.Outcome):
			v.addf(field(path, "outcome"), "must be one of %s (got %q)", strings.Join(Outcomes(), "|"), r.Outcome)
		}
		if r.Blame != "" && !ValidBlame(r.Blame) {
			v.addf(field(path, "blame"), "must be one of none|identity|proxy|both (got %q)", r.Blame)
		}
		r.When.validate(v, field(path, "when"))
	}
	return v.result(KindSignal, s.Name)
}

func (w *SignalWhen) validate(v *validator, path string) {
	if m := w.HTTPStatus; m != nil {
		p := field(path, "http_status")
		if m.Range != nil {
			before := len(v.problems)
			m.Range.validate(v, p, 0, maxHTTPStatus)
			if _, hi := m.Range.Bounds(); len(v.problems) == before && hi < 1 {
				v.addf(p, "never matches: a range only matches reported statuses (use [0] to match a missing status)")
			}
		} else if len(m.Values) == 0 {
			v.addf(p, "must not be empty")
		}
		for i, code := range m.Values {
			if code < 0 || code > maxHTTPStatus {
				v.addf(item(p, i), "must be between 0 and %d (got %d)", maxHTTPStatus, code)
			}
		}
	}
	v.stringList(field(path, "business_code"), w.BusinessCode, nil)
	v.stringList(field(path, "error_kind"), w.ErrorKind, func(p, kind string) {
		if !ValidErrorKind(kind) {
			v.addf(p, "must be one of %s (got %q)", strings.Join(ErrorKinds(), "|"), kind)
		}
	})
	v.stringList(field(path, "markers"), w.Markers, nil)
	if u := w.URI; u != nil {
		p := field(path, "uri")
		switch {
		case u.Prefix == "" && u.Regex == "":
			v.addf(p, "must set prefix or regex")
		case u.Prefix != "" && u.Regex != "":
			v.addf(p, "prefix and regex are mutually exclusive")
		case u.Prefix != "":
			if !strings.HasPrefix(u.Prefix, "/") {
				v.addf(field(p, "prefix"), "must start with /")
			}
			if len(u.Prefix) > maxURIPatternLen {
				v.addf(field(p, "prefix"), "must be at most %d characters", maxURIPatternLen)
			}
		default:
			if len(u.Regex) > maxURIPatternLen {
				v.addf(field(p, "regex"), "must be at most %d characters", maxURIPatternLen)
			} else if _, err := regexp.Compile(u.Regex); err != nil {
				v.addf(field(p, "regex"), "invalid regular expression: %v", err)
			}
		}
	}
	v.stringList(field(path, "method"), w.Method, func(p, m string) {
		if !validMethodToken(m) {
			v.addf(p, "must be an HTTP method token (got %q)", m)
		}
	})
	if w.LatencyMs != nil {
		w.LatencyMs.validate(v, field(path, "latency_ms"), 0, maxRangeBound)
	}
	if w.ResponseBytes != nil {
		w.ResponseBytes.validate(v, field(path, "response_bytes"), 0, maxRangeBound)
	}
}

// validateRuleName checks an optional rule name and its uniqueness.
func validateRuleName(v *validator, path, name string, i int, seen map[string]int) {
	if name == "" {
		return
	}
	if !ValidName(name) {
		v.addf(field(path, "name"), "must match ^[a-z0-9][a-z0-9._-]{0,63}$ (got %q)", name)
		return
	}
	if prev, dup := seen[name]; dup {
		v.addf(field(path, "name"), "duplicates the name of rules[%d] (%q)", prev, name)
		return
	}
	seen[name] = i
}

// validMethodToken reports whether m is a plausible HTTP method (letters only,
// at most 16 characters).
func validMethodToken(m string) bool {
	if m == "" || len(m) > 16 {
		return false
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		isLetter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		if !isLetter {
			return false
		}
	}
	return true
}
