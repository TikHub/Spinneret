package main

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// overflowLabel replaces high-cardinality labels once a counter table reaches its key limit.
const overflowLabel = "_overflow"

// counterTable is one generation of counters; reset swaps in a fresh table.
type counterTable struct {
	m          sync.Map // key -> *atomic.Int64
	size       atomic.Int64
	overflowed atomic.Int64
}

// counters is a lock-free, bounded set of monotonically increasing counters keyed by K.
type counters[K comparable] struct {
	maxKeys  int64
	overflow func(K) K
	table    atomic.Pointer[counterTable]
}

// newCounters creates a counter set holding at most maxKeys distinct keys; further keys are folded by overflow.
func newCounters[K comparable](maxKeys int, overflow func(K) K) *counters[K] {
	c := &counters[K]{maxKeys: int64(maxKeys), overflow: overflow}
	c.table.Store(&counterTable{})
	return c
}

// inc increments the counter of key k.
func (c *counters[K]) inc(k K) {
	t := c.table.Load()
	if v, ok := t.m.Load(k); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	if t.size.Load() >= c.maxKeys {
		t.overflowed.Add(1)
		k = c.overflow(k)
		if v, ok := t.m.Load(k); ok {
			v.(*atomic.Int64).Add(1)
			return
		}
	}
	fresh := new(atomic.Int64)
	v, loaded := t.m.LoadOrStore(k, fresh)
	if !loaded {
		t.size.Add(1)
	}
	v.(*atomic.Int64).Add(1)
}

// reset discards all counters.
func (c *counters[K]) reset() {
	c.table.Store(&counterTable{})
}

// snapshot copies the current counters; overflowed counts increments folded into overflow keys.
func (c *counters[K]) snapshot() (values map[K]int64, overflowed int64) {
	t := c.table.Load()
	values = make(map[K]int64)
	t.m.Range(func(k, v any) bool {
		values[k.(K)] = v.(*atomic.Int64).Load()
		return true
	})
	return values, t.overflowed.Load()
}

// siteStatKey identifies one target site counter.
type siteStatKey struct {
	Path     string
	Rule     string
	Mode     Mode
	Identity string
	Proxy    string
	Status   int
}

// siteOverflow folds high-cardinality labels while keeping mode and status exact.
func siteOverflow(k siteStatKey) siteStatKey {
	return siteStatKey{Path: overflowLabel, Rule: overflowLabel, Mode: k.Mode, Identity: overflowLabel, Proxy: overflowLabel, Status: k.Status}
}

// proxyStatKey identifies one proxy listener counter.
type proxyStatKey struct {
	ProxyID string
	Result  string
}

// proxyOverflow folds the proxy id while keeping the result exact.
func proxyOverflow(k proxyStatKey) proxyStatKey {
	return proxyStatKey{ProxyID: overflowLabel, Result: k.Result}
}

// Proxy listener results recorded in statistics.
const (
	proxyResultForwarded     = "forwarded"
	proxyResultTunneled      = "tunneled"
	proxyResultAuthMissing   = "auth_missing"
	proxyResultAuthInvalid   = "auth_invalid"
	proxyResultAuthFail      = "auth_fail"
	proxyResultRefused       = "refused"
	proxyResultBadRequest    = "bad_request"
	proxyResultUpstreamError = "upstream_error"
	proxyResultCanceled      = "canceled"
	proxyResultAborted       = "aborted"
)

// stats aggregates counters of both listeners.
type stats struct {
	site  *counters[siteStatKey]
	proxy *counters[proxyStatKey]
}

// newStats creates statistics bounded to maxKeys distinct keys per listener.
func newStats(maxKeys int) *stats {
	return &stats{
		site:  newCounters(maxKeys, siteOverflow),
		proxy: newCounters(maxKeys, proxyOverflow),
	}
}

// reset clears the counters of both listeners.
func (s *stats) reset() {
	s.site.reset()
	s.proxy.reset()
}

// statsFilter narrows a stats report; empty fields match everything.
type statsFilter struct {
	PathPrefix string
	Identity   string
	Proxy      string
	Mode       Mode
	Entries    bool
}

// SiteStatEntry is one counter of the target site in a stats report.
type SiteStatEntry struct {
	Path     string `json:"path"`
	Rule     string `json:"rule"`
	Mode     Mode   `json:"mode"`
	Identity string `json:"identity"`
	Proxy    string `json:"proxy"`
	Status   int    `json:"status"`
	Count    int64  `json:"count"`
}

// ProxyStatEntry is one counter of the proxy listener in a stats report.
type ProxyStatEntry struct {
	ProxyID string `json:"proxy_id"`
	Result  string `json:"result"`
	Count   int64  `json:"count"`
}

// SiteStatsReport is the target site section of GET /_admin/stats.
type SiteStatsReport struct {
	Total      int64            `json:"total"`
	ByPath     map[string]int64 `json:"by_path"`
	ByMode     map[string]int64 `json:"by_mode"`
	ByIdentity map[string]int64 `json:"by_identity"`
	ByProxy    map[string]int64 `json:"by_proxy"`
	ByStatus   map[string]int64 `json:"by_status"`
	Entries    []SiteStatEntry  `json:"entries,omitempty"`
	Overflowed int64            `json:"overflowed"`
}

// ProxyStatsReport is the proxy listener section of GET /_admin/stats.
type ProxyStatsReport struct {
	Total      int64            `json:"total"`
	ByProxy    map[string]int64 `json:"by_proxy"`
	ByResult   map[string]int64 `json:"by_result"`
	Entries    []ProxyStatEntry `json:"entries,omitempty"`
	Overflowed int64            `json:"overflowed"`
}

// StatsReport is the response body of GET /_admin/stats.
type StatsReport struct {
	SiteStatsReport
	ProxyListener ProxyStatsReport `json:"proxy_listener"`
}

// report builds a stats report honoring filter.
func (s *stats) report(f statsFilter) StatsReport {
	return StatsReport{SiteStatsReport: s.siteReport(f), ProxyListener: s.proxyReport(f)}
}

// siteReport aggregates the target site counters.
func (s *stats) siteReport(f statsFilter) SiteStatsReport {
	values, overflowed := s.site.snapshot()
	r := SiteStatsReport{
		ByPath: map[string]int64{}, ByMode: map[string]int64{}, ByIdentity: map[string]int64{},
		ByProxy: map[string]int64{}, ByStatus: map[string]int64{}, Overflowed: overflowed,
	}
	for k, n := range values {
		if !f.matchSite(k) {
			continue
		}
		r.Total += n
		r.ByPath[k.Path] += n
		r.ByMode[string(k.Mode)] += n
		r.ByIdentity[k.Identity] += n
		r.ByProxy[k.Proxy] += n
		r.ByStatus[strconv.Itoa(k.Status)] += n
		if f.Entries {
			r.Entries = append(r.Entries, SiteStatEntry{
				Path: k.Path, Rule: k.Rule, Mode: k.Mode, Identity: k.Identity, Proxy: k.Proxy, Status: k.Status, Count: n,
			})
		}
	}
	slices.SortFunc(r.Entries, func(a, b SiteStatEntry) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Identity, b.Identity), cmp.Compare(a.Proxy, b.Proxy),
			cmp.Compare(a.Mode, b.Mode), cmp.Compare(a.Status, b.Status), cmp.Compare(a.Rule, b.Rule))
	})
	return r
}

// proxyReport aggregates the proxy listener counters.
func (s *stats) proxyReport(f statsFilter) ProxyStatsReport {
	values, overflowed := s.proxy.snapshot()
	r := ProxyStatsReport{ByProxy: map[string]int64{}, ByResult: map[string]int64{}, Overflowed: overflowed}
	for k, n := range values {
		if f.Proxy != "" && k.ProxyID != f.Proxy {
			continue
		}
		r.Total += n
		r.ByProxy[k.ProxyID] += n
		r.ByResult[k.Result] += n
		if f.Entries {
			r.Entries = append(r.Entries, ProxyStatEntry{ProxyID: k.ProxyID, Result: k.Result, Count: n})
		}
	}
	slices.SortFunc(r.Entries, func(a, b ProxyStatEntry) int {
		return cmp.Or(cmp.Compare(a.ProxyID, b.ProxyID), cmp.Compare(a.Result, b.Result))
	})
	return r
}

// matchSite reports whether a site counter passes the filter.
func (f statsFilter) matchSite(k siteStatKey) bool {
	if f.PathPrefix != "" && !strings.HasPrefix(k.Path, f.PathPrefix) {
		return false
	}
	if f.Identity != "" && k.Identity != f.Identity {
		return false
	}
	if f.Proxy != "" && k.Proxy != f.Proxy {
		return false
	}
	return f.Mode == "" || k.Mode == f.Mode
}
