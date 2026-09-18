package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Limits applied to scripted rules so a malformed admin request cannot exhaust memory or stall responses.
const (
	maxRules          = 1000
	maxRulePrefixLen  = 2048
	maxRuleFilterLen  = 10000
	maxLatencyMs      = 120_000
	maxItemCount      = 1000
	defaultItemCount  = 3
	defaultSlowMs     = 1000
	defaultFlakyProb  = 0.5
	defaultBizCode    = "10001"
	maxBusinessCodeLn = 64
)

// Mode names a scripted behavior of the target site.
type Mode string

// Target site behavior modes.
const (
	ModeOK            Mode = "ok"
	ModeRateLimit     Mode = "rate_limit"
	ModeCaptcha       Mode = "captcha"
	ModeLoginRedirect Mode = "login_redirect"
	ModeServerError   Mode = "server_error"
	ModeEmpty         Mode = "empty"
	ModeBusinessError Mode = "business_error"
	ModeSlow          Mode = "slow"
	ModeFlaky         Mode = "flaky"
)

// statusRange is the inclusive range of HTTP statuses a mode may answer with plus its default.
type statusRange struct {
	min, max, def int
	allowed       []int // when non-empty, only these statuses are accepted
}

// modeStatuses defines which status codes each mode accepts.
var modeStatuses = map[Mode]statusRange{
	ModeOK:            {min: 200, max: 299, def: 200},
	ModeSlow:          {min: 200, max: 299, def: 200},
	ModeEmpty:         {min: 200, max: 299, def: 200},
	ModeCaptcha:       {min: 200, max: 299, def: 200},
	ModeBusinessError: {min: 200, max: 299, def: 200},
	ModeRateLimit:     {min: 400, max: 599, def: 429},
	ModeFlaky:         {min: 400, max: 599, def: 429},
	ModeServerError:   {min: 500, max: 599, def: 503},
	ModeLoginRedirect: {def: 302, allowed: []int{301, 302, 303, 307, 308}},
}

// validate reports whether status is acceptable for the range.
func (s statusRange) validate(status int) bool {
	if len(s.allowed) > 0 {
		return slices.Contains(s.allowed, status)
	}
	// 204 and 205 cannot carry the JSON/HTML bodies every mode writes.
	return status >= s.min && status <= s.max && status != 204 && status != 205
}

// BusinessCode is a business error code that accepts both JSON strings and JSON numbers.
type BusinessCode string

// UnmarshalJSON accepts "10001" as well as 10001.
func (c *BusinessCode) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		*c = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("business_code: %w", err)
		}
		*c = BusinessCode(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("business_code must be a string or a number: %w", err)
	}
	*c = BusinessCode(n.String())
	return nil
}

// Rule is one scripted behavior rule of the target site, as accepted by PUT /_admin/rules.
type Rule struct {
	// Prefix is matched against the request path; the longest matching prefix wins.
	Prefix string `json:"prefix"`
	// Mode is the behavior served when the rule triggers.
	Mode Mode `json:"mode"`
	// Status overrides the HTTP status of the mode (see modeStatuses).
	Status int `json:"status,omitempty"`
	// BusinessCode is the code returned by business_error.
	BusinessCode BusinessCode `json:"business_code,omitempty"`
	// Probability is the chance in [0,1] that the rule triggers; otherwise the request is served as ok.
	Probability *float64 `json:"probability,omitempty"`
	// LatencyMs is slept before a triggered rule responds.
	LatencyMs int `json:"latency_ms,omitempty"`
	// ItemCount is the number of items of an ok response.
	ItemCount *int `json:"item_count,omitempty"`
	// Identities restricts the rule to these identities; empty means all.
	Identities []string `json:"identities,omitempty"`
	// Proxies restricts the rule to these proxy ids ("direct" for no proxy); empty means all.
	Proxies []string `json:"proxies,omitempty"`
}

// compiledRule is a normalized rule prepared for matching.
type compiledRule struct {
	Rule
	probability float64
	itemCount   int
	identities  map[string]struct{}
	proxies     map[string]struct{}
	order       int
}

// matches reports whether the rule applies to the request attributes.
func (r *compiledRule) matches(path, identity, proxy string) bool {
	if !strings.HasPrefix(path, r.Prefix) {
		return false
	}
	if len(r.identities) > 0 {
		if _, ok := r.identities[identity]; !ok {
			return false
		}
	}
	if len(r.proxies) > 0 {
		if _, ok := r.proxies[proxy]; !ok {
			return false
		}
	}
	return true
}

// specificity ranks rules with the same prefix: filtered rules are tried before unfiltered ones.
func (r *compiledRule) specificity() int {
	n := 0
	if len(r.identities) > 0 {
		n++
	}
	if len(r.proxies) > 0 {
		n++
	}
	return n
}

// ruleSet is an immutable, validated set of rules.
type ruleSet struct {
	rules   []Rule          // normalized, in submission order
	ordered []*compiledRule // matching order
}

// emptyRuleSet returns the "everything ok" rule set.
func emptyRuleSet() *ruleSet {
	return &ruleSet{rules: []Rule{}}
}

// match returns the most specific rule applying to the request, or nil when none applies.
func (s *ruleSet) match(path, identity, proxy string) *compiledRule {
	for _, r := range s.ordered {
		if r.matches(path, identity, proxy) {
			return r
		}
	}
	return nil
}

// parseRules decodes and validates a JSON rule array.
func parseRules(data []byte) (*ruleSet, error) {
	var rules []Rule
	if err := decodeStrict(data, &rules); err != nil {
		return nil, fmt.Errorf("decode rules: %w", err)
	}
	return newRuleSet(rules)
}

// newRuleSet validates, normalizes and compiles rules.
func newRuleSet(rules []Rule) (*ruleSet, error) {
	if len(rules) > maxRules {
		return nil, fmt.Errorf("too many rules: %d > %d", len(rules), maxRules)
	}
	set := &ruleSet{rules: make([]Rule, 0, len(rules)), ordered: make([]*compiledRule, 0, len(rules))}
	for i, r := range rules {
		c, err := compileRule(i, r)
		if err != nil {
			return nil, fmt.Errorf("rule %d: %w", i, err)
		}
		set.rules = append(set.rules, c.Rule)
		set.ordered = append(set.ordered, c)
	}
	slices.SortStableFunc(set.ordered, func(a, b *compiledRule) int {
		if d := len(b.Prefix) - len(a.Prefix); d != 0 {
			return d
		}
		if d := b.specificity() - a.specificity(); d != 0 {
			return d
		}
		return a.order - b.order
	})
	return set, nil
}

// compileRule validates one rule and fills in its defaults.
func compileRule(order int, r Rule) (*compiledRule, error) {
	if r.Prefix == "" || r.Prefix[0] != '/' {
		return nil, fmt.Errorf("prefix %q must start with '/'", r.Prefix)
	}
	if len(r.Prefix) > maxRulePrefixLen {
		return nil, fmt.Errorf("prefix longer than %d bytes", maxRulePrefixLen)
	}
	statuses, ok := modeStatuses[r.Mode]
	if !ok {
		return nil, fmt.Errorf("unknown mode %q", r.Mode)
	}
	if r.Status == 0 {
		r.Status = statuses.def
	} else if !statuses.validate(r.Status) {
		return nil, fmt.Errorf("status %d is not allowed for mode %s", r.Status, r.Mode)
	}
	if err := normalizeBusinessCode(&r); err != nil {
		return nil, err
	}
	prob, err := normalizeProbability(&r)
	if err != nil {
		return nil, err
	}
	if r.LatencyMs < 0 || r.LatencyMs > maxLatencyMs {
		return nil, fmt.Errorf("latency_ms %d out of range [0,%d]", r.LatencyMs, maxLatencyMs)
	}
	if r.Mode == ModeSlow && r.LatencyMs == 0 {
		r.LatencyMs = defaultSlowMs
	}
	items := defaultItemCount
	if r.ItemCount != nil {
		items = *r.ItemCount
	}
	if items < 0 || items > maxItemCount {
		return nil, fmt.Errorf("item_count %d out of range [0,%d]", items, maxItemCount)
	}
	r.ItemCount = &items
	c := &compiledRule{probability: prob, itemCount: items, order: order}
	if r.Identities, c.identities, err = normalizeFilter("identities", r.Identities); err != nil {
		return nil, err
	}
	if r.Proxies, c.proxies, err = normalizeFilter("proxies", r.Proxies); err != nil {
		return nil, err
	}
	c.Rule = r
	return c, nil
}

// normalizeBusinessCode checks that business_code is only used by business_error and defaults it.
func normalizeBusinessCode(r *Rule) error {
	r.BusinessCode = BusinessCode(strings.TrimSpace(string(r.BusinessCode)))
	if r.Mode != ModeBusinessError {
		if r.BusinessCode != "" {
			return fmt.Errorf("business_code is only valid for mode %s", ModeBusinessError)
		}
		return nil
	}
	if r.BusinessCode == "" {
		r.BusinessCode = defaultBizCode
	}
	if len(r.BusinessCode) > maxBusinessCodeLn {
		return fmt.Errorf("business_code longer than %d bytes", maxBusinessCodeLn)
	}
	return nil
}

// normalizeProbability validates probability and applies the per-mode default.
func normalizeProbability(r *Rule) (float64, error) {
	p := 1.0
	if r.Mode == ModeFlaky {
		p = defaultFlakyProb
	}
	if r.Probability != nil {
		p = *r.Probability
	}
	if !(p >= 0 && p <= 1) { // also rejects NaN
		return 0, fmt.Errorf("probability %v out of range [0,1]", p)
	}
	r.Probability = &p
	return p, nil
}

// normalizeFilter trims, de-duplicates and indexes an identity or proxy filter list.
func normalizeFilter(name string, values []string) ([]string, map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, nil, nil
	}
	if len(values) > maxRuleFilterLen {
		return nil, nil, fmt.Errorf("%s: too many entries (%d > %d)", name, len(values), maxRuleFilterLen)
	}
	out := make([]string, 0, len(values))
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, nil, fmt.Errorf("%s: empty entry", name)
		}
		if _, dup := set[v]; dup {
			continue
		}
		set[v] = struct{}{}
		out = append(out, v)
	}
	return out, set, nil
}

// decodeStrict decodes exactly one JSON value, rejecting unknown fields and trailing data.
func decodeStrict(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after JSON value")
	}
	return nil
}
