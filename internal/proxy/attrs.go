package proxy

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Proxy kinds.
const (
	KindDatacenter  = "datacenter"
	KindResidential = "residential"
	KindMobile      = "mobile"
	KindTunnel      = "tunnel"
)

// Proxy states.
const (
	StateActive      = "active"
	StateDisabled    = "disabled"
	StateDead        = "dead"
	StateBanned      = "banned"
	StateQuarantined = "quarantined"
	StateRetired     = "retired"
)

// Attribute limits.
const (
	MaxRegionLength          = 64
	MaxCityLength            = 128
	MaxProviderLength        = 128
	MaxTagLength             = 64
	MaxTags                  = 64
	MaxMaxConcurrency        = 100000
	MaxSessionTemplateLength = 512
)

// allStates lists every proxy state.
var allStates = []string{StateActive, StateDisabled, StateDead, StateBanned, StateQuarantined, StateRetired}

// ValidKind reports whether k is a known proxy kind.
func ValidKind(k string) bool {
	switch k {
	case KindDatacenter, KindResidential, KindMobile, KindTunnel:
		return true
	}
	return false
}

// ValidState reports whether s is a known proxy state.
func ValidState(s string) bool {
	return slices.Contains(allStates, s)
}

// Attributes are the operator-controlled, non-secret properties of a proxy.
type Attributes struct {
	Kind            string
	Region          string
	City            string
	Provider        string
	MaxConcurrency  int
	Tags            []string
	SessionTemplate string
}

// equal reports whether a and b are identical (tags compared in order).
func (a Attributes) equal(b Attributes) bool {
	return a.Kind == b.Kind && a.Region == b.Region && a.City == b.City && a.Provider == b.Provider &&
		a.MaxConcurrency == b.MaxConcurrency && a.SessionTemplate == b.SessionTemplate && slices.Equal(a.Tags, b.Tags)
}

// validate checks every attribute and normalizes tags (trimmed, de-duplicated).
func (a *Attributes) validate() error {
	if !ValidKind(a.Kind) {
		return fmt.Errorf("kind must be one of datacenter, residential, mobile, tunnel")
	}
	if err := checkText("region", a.Region, MaxRegionLength); err != nil {
		return err
	}
	if err := checkText("city", a.City, MaxCityLength); err != nil {
		return err
	}
	if err := checkText("provider", a.Provider, MaxProviderLength); err != nil {
		return err
	}
	if a.MaxConcurrency < 1 || a.MaxConcurrency > MaxMaxConcurrency {
		return fmt.Errorf("max_concurrency must be between 1 and %d", MaxMaxConcurrency)
	}
	tags, err := normalizeTags(a.Tags)
	if err != nil {
		return err
	}
	a.Tags = tags
	return ValidateSessionTemplate(a.SessionTemplate)
}

// checkText validates a free-form single-line attribute.
func checkText(field, v string, maxLen int) error {
	if len(v) > maxLen {
		return fmt.Errorf("%s must be at most %d bytes", field, maxLen)
	}
	if !utf8.ValidString(v) || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return fmt.Errorf("%s contains invalid characters", field)
	}
	return nil
}

// normalizeTags trims tags, removes duplicates (keeping the first occurrence)
// and validates them. Tags are stored in the hot state as ",t1,t2,", so they
// must not contain commas or white space.
func normalizeTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			return nil, fmt.Errorf("tags must not be empty")
		}
		if len(t) > MaxTagLength {
			return nil, fmt.Errorf("tag must be at most %d bytes", MaxTagLength)
		}
		if !utf8.ValidString(t) || strings.ContainsFunc(t, func(r rune) bool {
			return r == ',' || unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
			return nil, fmt.Errorf("tags must not contain commas, white space or control characters")
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("at most %d tags are allowed", MaxTags)
	}
	return out, nil
}

// splitTags splits a tag list on commas or semicolons, dropping empty items.
func splitTags(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// mergeTags returns a ∪ b, keeping order and removing duplicates.
func mergeTags(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	for _, t := range append(slices.Clone(a), b...) {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}
