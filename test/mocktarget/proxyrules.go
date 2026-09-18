package main

import (
	"fmt"
	"strings"
)

// Limits applied to proxy behavior rules.
const (
	maxProxyRules     = 10000
	maxProxyIDLen     = 256
	proxyRuleWildcard = "*"
)

// ProxyMode names a scripted behavior of the mock forward proxy for one proxy id.
type ProxyMode string

// Proxy behavior modes.
const (
	ProxyModeOK       ProxyMode = "ok"
	ProxyModeRefuse   ProxyMode = "refuse"
	ProxyModeAuthFail ProxyMode = "auth_fail"
	ProxyModeSlow     ProxyMode = "slow"
)

// validProxyModes lists the accepted proxy modes.
var validProxyModes = map[ProxyMode]struct{}{
	ProxyModeOK: {}, ProxyModeRefuse: {}, ProxyModeAuthFail: {}, ProxyModeSlow: {},
}

// ProxyRule scripts the behavior of one proxy id, as accepted by PUT /_admin/proxies.
type ProxyRule struct {
	// ProxyID is the proxy username the rule applies to; "*" applies to proxy ids without their own rule.
	ProxyID string `json:"proxy_id"`
	// Mode is the proxy behavior.
	Mode ProxyMode `json:"mode"`
	// LatencyMs is slept before the proxy acts on a request.
	LatencyMs int `json:"latency_ms,omitempty"`
}

// defaultProxyRule is applied to proxy ids without a rule.
var defaultProxyRule = ProxyRule{Mode: ProxyModeOK}

// proxyRuleSet is an immutable, validated set of proxy rules.
type proxyRuleSet struct {
	rules []ProxyRule // normalized, in submission order
	byID  map[string]ProxyRule
}

// emptyProxyRuleSet returns the "every proxy ok" rule set.
func emptyProxyRuleSet() *proxyRuleSet {
	return &proxyRuleSet{rules: []ProxyRule{}, byID: map[string]ProxyRule{}}
}

// lookup returns the rule of proxyID, the wildcard rule, or the default ok rule.
func (s *proxyRuleSet) lookup(proxyID string) ProxyRule {
	if r, ok := s.byID[proxyID]; ok {
		return r
	}
	if r, ok := s.byID[proxyRuleWildcard]; ok {
		return r
	}
	return defaultProxyRule
}

// parseProxyRules decodes and validates a JSON proxy rule array.
func parseProxyRules(data []byte) (*proxyRuleSet, error) {
	var rules []ProxyRule
	if err := decodeStrict(data, &rules); err != nil {
		return nil, fmt.Errorf("decode proxy rules: %w", err)
	}
	return newProxyRuleSet(rules)
}

// newProxyRuleSet validates and normalizes proxy rules.
func newProxyRuleSet(rules []ProxyRule) (*proxyRuleSet, error) {
	if len(rules) > maxProxyRules {
		return nil, fmt.Errorf("too many proxy rules: %d > %d", len(rules), maxProxyRules)
	}
	set := &proxyRuleSet{rules: make([]ProxyRule, 0, len(rules)), byID: make(map[string]ProxyRule, len(rules))}
	for i, r := range rules {
		r.ProxyID = strings.TrimSpace(r.ProxyID)
		if r.ProxyID == "" {
			return nil, fmt.Errorf("proxy rule %d: proxy_id is required", i)
		}
		if len(r.ProxyID) > maxProxyIDLen {
			return nil, fmt.Errorf("proxy rule %d: proxy_id longer than %d bytes", i, maxProxyIDLen)
		}
		if _, dup := set.byID[r.ProxyID]; dup {
			return nil, fmt.Errorf("proxy rule %d: duplicate proxy_id %q", i, r.ProxyID)
		}
		if _, ok := validProxyModes[r.Mode]; !ok {
			return nil, fmt.Errorf("proxy rule %d: unknown mode %q", i, r.Mode)
		}
		if r.LatencyMs < 0 || r.LatencyMs > maxLatencyMs {
			return nil, fmt.Errorf("proxy rule %d: latency_ms %d out of range [0,%d]", i, r.LatencyMs, maxLatencyMs)
		}
		if r.Mode == ProxyModeSlow && r.LatencyMs == 0 {
			r.LatencyMs = defaultSlowMs
		}
		set.rules = append(set.rules, r)
		set.byID[r.ProxyID] = r
	}
	return set, nil
}
