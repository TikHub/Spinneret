package site

import (
	"bytes"
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// RuleKind is the kind of a URI rule.
type RuleKind string

// URI rule kinds, in decreasing match priority.
const (
	RuleExact    RuleKind = "exact"
	RuleTemplate RuleKind = "template"
	RulePrefix   RuleKind = "prefix"
	RuleRegex    RuleKind = "regex"
)

// DefaultGroup is the name of the endpoint group every site+client has, used
// when no URI rule matches.
const DefaultGroup = "_default"

// Rule maps a path pattern to an endpoint group.
type Rule struct {
	ID        string
	GroupID   string
	GroupName string
	Kind      RuleKind
	Pattern   string
	// Position orders rules of equal precedence (lower first).
	Position int
}

// MatchResult is the endpoint group selected for a path. RuleID and Kind are
// empty when Default is true.
type MatchResult struct {
	GroupID   string
	GroupName string
	RuleID    string
	Kind      RuleKind
	Default   bool
}

// Matcher maps paths to endpoint groups. It is immutable after construction
// and safe for concurrent use; rule changes build a new Matcher that callers
// swap atomically.
type Matcher struct {
	exact     map[string]*ruleEntry
	templates *templateNode
	prefixes  *radixNode
	regexes   []regexEntry
	fallback  MatchResult
	size      int
}

// ruleEntry is a compiled rule. rank is the rule's ordinal in (Position, ID,
// input index) order and breaks ties deterministically.
type ruleEntry struct {
	result MatchResult
	rank   int
}

type regexEntry struct {
	re    *regexp.Regexp
	entry *ruleEntry
}

// NewMatcher compiles rules into a Matcher. Unmatched paths resolve to
// defaultGroupID with group name DefaultGroup; an empty defaultGroupID is
// allowed and yields a default result with an empty GroupID. Every rule must
// have a GroupID and a valid pattern (see ValidatePattern). Rules with the
// same kind and pattern do not conflict: the one ranked first by (Position,
// ID, input order) wins. Errors are apperr InvalidArgument naming the rule.
func NewMatcher(rules []Rule, defaultGroupID string) (*Matcher, error) {
	m := &Matcher{
		exact:     make(map[string]*ruleEntry),
		templates: &templateNode{},
		prefixes:  &radixNode{},
		fallback:  MatchResult{GroupID: defaultGroupID, GroupName: DefaultGroup, Default: true},
		size:      len(rules),
	}
	ranks := rankRules(rules)
	for i, r := range rules {
		if r.GroupID == "" {
			return nil, ruleError(i, r, patternInvalid("endpoint group id is empty"))
		}
		e := &ruleEntry{
			result: MatchResult{GroupID: r.GroupID, GroupName: r.GroupName, RuleID: r.ID, Kind: r.Kind},
			rank:   ranks[i],
		}
		if err := m.add(r, e); err != nil {
			return nil, ruleError(i, r, err)
		}
	}
	slices.SortFunc(m.regexes, func(a, b regexEntry) int { return cmp.Compare(a.entry.rank, b.entry.rank) })
	m.templates.finalize()
	return m, nil
}

func (m *Matcher) add(r Rule, e *ruleEntry) error {
	switch r.Kind {
	case RuleExact:
		if err := validatePathPattern(r.Pattern); err != nil {
			return err
		}
		if prev, ok := m.exact[r.Pattern]; !ok || e.rank < prev.rank {
			m.exact[r.Pattern] = e
		}
	case RuleTemplate:
		segments, err := parseTemplate(r.Pattern)
		if err != nil {
			return err
		}
		m.templates.insert(segments, e)
	case RulePrefix:
		if err := validatePathPattern(r.Pattern); err != nil {
			return err
		}
		m.prefixes.insert(r.Pattern, e)
	case RuleRegex:
		re, err := compileRegex(r.Pattern)
		if err != nil {
			return err
		}
		m.regexes = append(m.regexes, regexEntry{re: re, entry: e})
	default:
		return ValidatePattern(r.Kind, r.Pattern)
	}
	return nil
}

// ruleError prefixes a validation error with the rule's identity while
// keeping the apperr code and reason.
func ruleError(index int, r Rule, err error) error {
	msg := err.Error()
	if ae, ok := apperr.As(err); ok {
		msg = ae.Message
	}
	return patternInvalid("uri rule at index %d (id %q, kind %q): %s", index, r.ID, r.Kind, msg).WithCause(err)
}

// rankRules returns, for each rule, its ordinal in (Position, ID, index) order.
func rankRules(rules []Rule) []int {
	order := make([]int, len(rules))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		ra, rb := &rules[a], &rules[b]
		if c := cmp.Compare(ra.Position, rb.Position); c != 0 {
			return c
		}
		if c := strings.Compare(ra.ID, rb.ID); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	ranks := make([]int, len(rules))
	for rank, idx := range order {
		ranks[idx] = rank
	}
	return ranks
}

// Len returns the number of rules the matcher was built from.
func (m *Matcher) Len() int {
	if m == nil {
		return 0
	}
	return m.size
}

// Match returns the endpoint group of path, which should come from
// NormalizePath. Priority: exact > template (more literal segments, then
// fewer params, then position) > prefix (longest) > regex (position) >
// default. A nil or zero Matcher returns the default result with an empty
// GroupID. Match does not allocate.
func (m *Matcher) Match(path string) MatchResult {
	if m == nil || !m.fallback.Default {
		return MatchResult{GroupName: DefaultGroup, Default: true}
	}
	if e, ok := m.exact[path]; ok {
		return e.result
	}
	if t := m.templates.match(path); t != nil {
		return t.entry.result
	}
	if e := m.prefixes.longest(path); e != nil {
		return e.result
	}
	for _, r := range m.regexes {
		if r.re.MatchString(path) {
			return r.entry.result
		}
	}
	return m.fallback
}

// MatchURI normalizes uri with NormalizePath and matches the resulting path.
func (m *Matcher) MatchURI(uri string) (MatchResult, error) {
	path, err := NormalizePath(uri)
	if err != nil {
		return MatchResult{}, err
	}
	return m.Match(path), nil
}

// templateNode is a node of the template segment trie. Literal children are
// keyed by segment text; every parameter segment shares the single param
// child because parameter names do not affect matching.
type templateNode struct {
	literals map[string]*templateNode
	param    *templateNode
	terminal *templateTerminal
	// maxLiterals and minRank bound the terminals of the subtree (terminal
	// included) for pruning: maxLiterals is -1 when the subtree is empty.
	maxLiterals int
	minRank     int
}

// templateTerminal is the best template ending at a trie node. All templates
// ending at the same node have the same literal and parameter counts, so only
// the lowest rank is kept.
type templateTerminal struct {
	entry    *ruleEntry
	literals int
	params   int
}

// betterThan reports whether t has precedence over o for the same path.
func (t *templateTerminal) betterThan(o *templateTerminal) bool {
	if t.literals != o.literals {
		return t.literals > o.literals
	}
	if t.params != o.params {
		return t.params < o.params
	}
	return t.entry.rank < o.entry.rank
}

func (n *templateNode) insert(segments []templateSegment, e *ruleEntry) {
	literals := 0
	for _, seg := range segments {
		if seg.param {
			if n.param == nil {
				n.param = &templateNode{}
			}
			n = n.param
			continue
		}
		literals++
		child, ok := n.literals[seg.literal]
		if !ok {
			if n.literals == nil {
				n.literals = make(map[string]*templateNode)
			}
			child = &templateNode{}
			n.literals[seg.literal] = child
		}
		n = child
	}
	t := &templateTerminal{entry: e, literals: literals, params: len(segments) - literals}
	if n.terminal == nil || t.betterThan(n.terminal) {
		n.terminal = t
	}
}

// finalize computes the pruning bounds bottom-up.
func (n *templateNode) finalize() {
	n.maxLiterals, n.minRank = -1, math.MaxInt
	if n.terminal != nil {
		n.maxLiterals, n.minRank = n.terminal.literals, n.terminal.entry.rank
	}
	merge := func(c *templateNode) {
		c.finalize()
		n.maxLiterals = max(n.maxLiterals, c.maxLiterals)
		n.minRank = min(n.minRank, c.minRank)
	}
	for _, c := range n.literals {
		merge(c)
	}
	if n.param != nil {
		merge(n.param)
	}
}

// canBeat reports whether some terminal of the subtree might take precedence
// over best. Templates matching one path all have the same segment count, so
// equal literal counts imply equal parameter counts and only rank remains.
func (n *templateNode) canBeat(best *templateTerminal) bool {
	if n.maxLiterals < 0 {
		return false
	}
	if best == nil {
		return true
	}
	return n.maxLiterals > best.literals || (n.maxLiterals == best.literals && n.minRank < best.entry.rank)
}

// match returns the best template for path, or nil.
func (n *templateNode) match(path string) *templateTerminal {
	if n.maxLiterals < 0 || path == "" || path[0] != '/' {
		return nil
	}
	var best *templateTerminal
	n.search(path, 1, &best)
	return best
}

// search walks the trie depth-first from the segment starting at index
// start, trying the literal child before the param child and recording the
// best terminal. Every trie node is visited at most once per call, so the cost
// is bounded by both the path length and the trie size.
func (n *templateNode) search(path string, start int, best **templateTerminal) {
	if !n.canBeat(*best) {
		return
	}
	if start > len(path) {
		if n.terminal != nil && (*best == nil || n.terminal.betterThan(*best)) {
			*best = n.terminal
		}
		return
	}
	end := strings.IndexByte(path[start:], '/')
	if end < 0 {
		end = len(path)
	} else {
		end += start
	}
	segment := path[start:end]
	if child, ok := n.literals[segment]; ok {
		child.search(path, end+1, best)
	}
	if n.param != nil && segment != "" {
		n.param.search(path, end+1, best)
	}
}

// radixNode is a node of the byte-wise radix tree of prefix patterns.
type radixNode struct {
	label    string
	entry    *ruleEntry
	indices  []byte // first byte of each child label, parallel to children
	children []*radixNode
}

func (n *radixNode) insert(key string, e *ruleEntry) {
	for {
		if key == "" {
			if n.entry == nil || e.rank < n.entry.rank {
				n.entry = e
			}
			return
		}
		i := bytes.IndexByte(n.indices, key[0])
		if i < 0 {
			n.indices = append(n.indices, key[0])
			n.children = append(n.children, &radixNode{label: key, entry: e})
			return
		}
		child := n.children[i]
		common := commonPrefixLen(child.label, key)
		if common < len(child.label) {
			split := &radixNode{
				label:    child.label[:common],
				indices:  []byte{child.label[common]},
				children: []*radixNode{child},
			}
			child.label = child.label[common:]
			n.children[i] = split
			child = split
		}
		key = key[common:]
		n = child
	}
}

// longest returns the entry of the longest prefix pattern of path, or nil.
func (n *radixNode) longest(path string) *ruleEntry {
	var best *ruleEntry
	for {
		if n.entry != nil {
			best = n.entry
		}
		if path == "" {
			return best
		}
		i := bytes.IndexByte(n.indices, path[0])
		if i < 0 {
			return best
		}
		child := n.children[i]
		if !strings.HasPrefix(path, child.label) {
			return best
		}
		path = path[len(child.label):]
		n = child
	}
}

func commonPrefixLen(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
