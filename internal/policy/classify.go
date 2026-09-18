package policy

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ReportFacts are the facts of one report used for classification.
// HTTPStatus 0 means the report carried no status.
type ReportFacts struct {
	URI           string
	Method        string
	HTTPStatus    int
	BusinessCode  string
	ErrorKind     string
	Markers       []string
	OutcomeHint   string
	LatencyMs     int64
	ResponseBytes int64
}

// Classification is the result of classifying a report. RuleIndex is the
// index of the matching rule in the concatenated chain, or -1 when no rule
// matched; RuleName is the rule name or a generated "<policy>.rules[i]" label.
type Classification struct {
	Outcome   string
	Blame     Blame
	RuleIndex int
	RuleName  string
}

// CompiledSignal is an immutable, precompiled signal rule chain. It is safe
// for concurrent use.
type CompiledSignal struct {
	trustHint bool
	rules     []signalMatcher
}

// statusMode selects how a rule matches HTTP status codes.
type statusMode uint8

const (
	statusAny statusMode = iota
	statusList
	statusRange
)

// int64Range is an inclusive, precomputed interval.
type int64Range struct {
	set    bool
	lo, hi int64
}

func (r int64Range) contains(v int64) bool { return v >= r.lo && v <= r.hi }

func newInt64Range(r *IntRange) int64Range {
	if r == nil {
		return int64Range{}
	}
	lo, hi := r.Bounds()
	return int64Range{set: true, lo: lo, hi: hi}
}

// signalMatcher is a compiled signal rule.
type signalMatcher struct {
	name    string
	outcome string
	blame   Blame

	status      statusMode
	statusBits  [16]uint64 // codes 0..1023
	statusRange int64Range

	businessCodes []string
	errorKinds    []string
	markers       []string
	methods       []string
	uriPrefix     string
	uriRegex      *regexp.Regexp
	latency       int64Range
	bytes         int64Range
}

// CompileSignal compiles a signal chain ordered root → leaf (see
// ResolveExtends). Rules are concatenated in chain order; trust_outcome_hint
// is taken from the leaf. Every spec is validated first.
func CompileSignal(chain []*SignalSpec) (*CompiledSignal, error) {
	if len(chain) == 0 {
		return nil, errors.New("compile signal policy: chain is empty")
	}
	total := 0
	for i, s := range chain {
		if s == nil {
			return nil, fmt.Errorf("compile signal policy: chain element %d is nil", i)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("compile signal policy %q: %w", s.Name, err)
		}
		total += len(s.Rules)
	}
	c := &CompiledSignal{
		trustHint: chain[len(chain)-1].TrustOutcomeHint,
		rules:     make([]signalMatcher, 0, total),
	}
	for _, s := range chain {
		for i := range s.Rules {
			m, err := compileSignalRule(s.Name, i, &s.Rules[i])
			if err != nil {
				return nil, err
			}
			c.rules = append(c.rules, m)
		}
	}
	return c, nil
}

func compileSignalRule(policyName string, i int, r *SignalRule) (signalMatcher, error) {
	m := signalMatcher{
		name:          ruleLabel(policyName, i, r.Name),
		outcome:       r.Outcome,
		blame:         r.Blame,
		businessCodes: cloneStrings(r.When.BusinessCode),
		errorKinds:    cloneStrings(r.When.ErrorKind),
		markers:       cloneStrings(r.When.Markers),
		methods:       cloneStrings(r.When.Method),
		latency:       newInt64Range(r.When.LatencyMs),
		bytes:         newInt64Range(r.When.ResponseBytes),
	}
	if m.blame == "" {
		m.blame = DefaultBlame(r.Outcome)
	}
	if hs := r.When.HTTPStatus; hs != nil {
		if hs.Range != nil {
			m.status = statusRange
			m.statusRange = newInt64Range(hs.Range)
		} else {
			m.status = statusList
			for _, code := range hs.Values {
				if code >= 0 && code < len(m.statusBits)*64 {
					m.statusBits[code>>6] |= 1 << (uint(code) & 63)
				}
			}
		}
	}
	if u := r.When.URI; u != nil {
		if u.Regex != "" {
			re, err := regexp.Compile(u.Regex)
			if err != nil {
				return signalMatcher{}, fmt.Errorf("compile signal policy %q: %s.when.uri.regex: %w", policyName, item("rules", i), err)
			}
			m.uriRegex = re
		} else {
			m.uriPrefix = u.Prefix
		}
	}
	return m, nil
}

// matches reports whether every present condition of m holds for f.
func (m *signalMatcher) matches(f *ReportFacts) bool {
	switch m.status {
	case statusList:
		code := f.HTTPStatus
		if code < 0 || code >= len(m.statusBits)*64 || m.statusBits[code>>6]&(1<<(uint(code)&63)) == 0 {
			return false
		}
	case statusRange:
		if f.HTTPStatus <= 0 || !m.statusRange.contains(int64(f.HTTPStatus)) {
			return false
		}
	}
	if m.errorKinds != nil && !containsString(m.errorKinds, f.ErrorKind) {
		return false
	}
	if m.businessCodes != nil && !containsString(m.businessCodes, f.BusinessCode) {
		return false
	}
	if m.markers != nil && !anyMarker(m.markers, f.Markers) {
		return false
	}
	if m.methods != nil && !containsFold(m.methods, f.Method) {
		return false
	}
	if m.uriPrefix != "" && !strings.HasPrefix(f.URI, m.uriPrefix) {
		return false
	}
	if m.uriRegex != nil && !m.uriRegex.MatchString(f.URI) {
		return false
	}
	if m.latency.set && !m.latency.contains(f.LatencyMs) {
		return false
	}
	if m.bytes.set && !m.bytes.contains(f.ResponseBytes) {
		return false
	}
	return true
}

// Classify returns the outcome of the first matching rule. When no rule
// matches, it returns the outcome hint (if trust_outcome_hint is enabled and
// the hint is a valid outcome) or unknown, with the default blame. A nil
// receiver classifies everything as unknown.
func (c *CompiledSignal) Classify(f ReportFacts) Classification {
	if c != nil {
		for i := range c.rules {
			m := &c.rules[i]
			if m.matches(&f) {
				return Classification{Outcome: m.outcome, Blame: m.blame, RuleIndex: i, RuleName: m.name}
			}
		}
		if c.trustHint && ValidOutcome(f.OutcomeHint) {
			return Classification{Outcome: f.OutcomeHint, Blame: DefaultBlame(f.OutcomeHint), RuleIndex: -1}
		}
	}
	return Classification{Outcome: OutcomeUnknown, Blame: DefaultBlame(OutcomeUnknown), RuleIndex: -1}
}

// RuleCount returns the number of compiled rules.
func (c *CompiledSignal) RuleCount() int {
	if c == nil {
		return 0
	}
	return len(c.rules)
}

// TrustOutcomeHint reports whether unmatched reports fall back to the hint.
func (c *CompiledSignal) TrustOutcomeHint() bool {
	return c != nil && c.trustHint
}

// ruleLabel returns the rule name, or "<policy>.rules[i]" for unnamed rules.
func ruleLabel(policyName string, i int, name string) string {
	if name != "" {
		return name
	}
	return policyName + ".rules[" + strconv.Itoa(i) + "]"
}

func cloneStrings(l []string) []string {
	if l == nil {
		return nil
	}
	return append(make([]string, 0, len(l)), l...)
}

func containsString(list []string, s string) bool {
	if s == "" {
		return false
	}
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	if s == "" {
		return false
	}
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func anyMarker(ruleMarkers, reported []string) bool {
	for _, r := range reported {
		if containsString(ruleMarkers, r) {
			return true
		}
	}
	return false
}
