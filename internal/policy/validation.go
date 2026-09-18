package policy

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

// Problem is one validation failure located by its YAML path, e.g.
// "rules[2].scope".
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// String renders the problem as "path: message".
func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// ValidationError aggregates every problem found in a policy spec.
type ValidationError struct {
	Kind     Kind
	Name     string
	Problems []Problem
}

// Error renders a human-readable, multi-line summary of all problems.
func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid ")
	b.WriteString(string(e.Kind))
	b.WriteString(" policy")
	if e.Name != "" {
		b.WriteString(" ")
		b.WriteString(strconv.Quote(e.Name))
	}
	if len(e.Problems) == 1 {
		b.WriteString(": ")
		b.WriteString(e.Problems[0].String())
		return b.String()
	}
	fmt.Fprintf(&b, ": %d problems:", len(e.Problems))
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.String())
	}
	return b.String()
}

// validator collects problems.
type validator struct {
	problems []Problem
}

func (v *validator) addf(path, format string, args ...any) {
	v.problems = append(v.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// result returns a *ValidationError when problems were collected, else nil.
func (v *validator) result(kind Kind, name string) error {
	if len(v.problems) == 0 {
		return nil
	}
	return &ValidationError{Kind: kind, Name: name, Problems: v.problems}
}

// field joins a parent path and a child key.
func field(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

// item formats an indexed path element such as "rules[2]".
func item(parent string, i int) string {
	return parent + "[" + strconv.Itoa(i) + "]"
}

// duration checks min <= d <= max (max <= 0 means unbounded) and rejects
// "permanent".
func (v *validator) duration(path string, d durationx.Duration, minD, maxD time.Duration) {
	if d.IsPermanent() {
		v.addf(path, "must not be permanent")
		return
	}
	if d.Std() < minD {
		if minD == 0 {
			v.addf(path, "must not be negative")
		} else {
			v.addf(path, "must be at least %s (got %s)", durationx.Duration(minD), d)
		}
		return
	}
	if maxD > 0 && d.Std() > maxD {
		v.addf(path, "must be at most %s (got %s)", durationx.Duration(maxD), d)
	}
}

// intRange checks lo <= n <= hi.
func (v *validator) intRange(path string, n, lo, hi int64) {
	if n < lo || n > hi {
		v.addf(path, "must be between %d and %d (got %d)", lo, hi, n)
	}
}

// minInt checks n >= lo.
func (v *validator) minInt(path string, n, lo int64) {
	if n < lo {
		v.addf(path, "must be at least %d (got %d)", lo, n)
	}
}

// ratio checks 0 <= f <= 1, or 0 < f <= 1 when openLow is set. NaN fails.
func (v *validator) ratio(path string, f float64, openLow bool) {
	if openLow {
		if !(f > 0 && f <= 1) {
			v.addf(path, "must be greater than 0 and at most 1 (got %v)", f)
		}
		return
	}
	if !(f >= 0 && f <= 1) {
		v.addf(path, "must be between 0 and 1 (got %v)", f)
	}
}

// score checks 0 <= f <= 100. NaN fails.
func (v *validator) score(path string, f float64) {
	if !(f >= 0 && f <= 100) {
		v.addf(path, "must be between 0 and 100 (got %v)", f)
	}
}

// enum checks that value is one of allowed.
func (v *validator) enum(path, value string, allowed ...string) {
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.addf(path, "must be one of %s (got %q)", strings.Join(allowed, "|"), value)
}

// stringList checks a present list: non-empty, no empty or oversized entries,
// no duplicates. check, when non-nil, validates each entry.
func (v *validator) stringList(path string, list StringList, check func(path, s string)) {
	if list == nil {
		return
	}
	if len(list) == 0 {
		v.addf(path, "must not be empty")
		return
	}
	seen := make(map[string]struct{}, len(list))
	for i, s := range list {
		p := item(path, i)
		switch {
		case s == "":
			v.addf(p, "must not be empty")
			continue
		case len(s) > 256:
			v.addf(p, "must be at most 256 characters")
			continue
		}
		if _, dup := seen[s]; dup {
			v.addf(p, "duplicate value %q", s)
			continue
		}
		seen[s] = struct{}{}
		if check != nil {
			check(p, s)
		}
	}
}
