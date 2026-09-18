package main

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountersIncAndOverflow(t *testing.T) {
	t.Parallel()
	c := newCounters(2, proxyOverflow)
	c.inc(proxyStatKey{ProxyID: "a", Result: "forwarded"})
	c.inc(proxyStatKey{ProxyID: "a", Result: "forwarded"})
	c.inc(proxyStatKey{ProxyID: "b", Result: "forwarded"})
	// Table full: new keys fold into the overflow key, existing keys keep counting.
	c.inc(proxyStatKey{ProxyID: "c", Result: "refused"})
	c.inc(proxyStatKey{ProxyID: "d", Result: "refused"})
	c.inc(proxyStatKey{ProxyID: "a", Result: "forwarded"})

	values, overflowed := c.snapshot()
	require.Equal(t, int64(2), overflowed)
	require.Equal(t, map[proxyStatKey]int64{
		{ProxyID: "a", Result: "forwarded"}:         3,
		{ProxyID: "b", Result: "forwarded"}:         1,
		{ProxyID: overflowLabel, Result: "refused"}: 2,
	}, values)

	c.reset()
	values, overflowed = c.snapshot()
	require.Empty(t, values)
	require.Zero(t, overflowed)
}

func TestCountersConcurrent(t *testing.T) {
	t.Parallel()
	c := newCounters(1000, proxyOverflow)
	const workers, perWorker = 16, 500
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perWorker {
				c.inc(proxyStatKey{ProxyID: "p" + strconv.Itoa((w+i)%10), Result: "forwarded"})
			}
		}()
	}
	wg.Wait()
	values, _ := c.snapshot()
	var total int64
	for _, n := range values {
		total += n
	}
	require.Equal(t, int64(workers*perWorker), total)
	require.Len(t, values, 10)
}

func TestStatsReport(t *testing.T) {
	t.Parallel()
	s := newStats(100)
	add := func(k siteStatKey, n int) {
		for range n {
			s.site.inc(k)
		}
	}
	add(siteStatKey{Path: "/site/search", Rule: "/site/search", Mode: ModeRateLimit, Identity: "a", Proxy: "p1", Status: 429}, 3)
	add(siteStatKey{Path: "/site/search", Mode: ModeOK, Identity: "b", Proxy: "direct", Status: 200}, 2)
	add(siteStatKey{Path: "/site/feed", Mode: ModeOK, Identity: "a", Proxy: "p1", Status: 200}, 1)
	s.proxy.inc(proxyStatKey{ProxyID: "p1", Result: proxyResultForwarded})
	s.proxy.inc(proxyStatKey{ProxyID: "p2", Result: proxyResultRefused})

	all := s.report(statsFilter{Entries: true})
	require.Equal(t, int64(6), all.Total)
	require.Equal(t, map[string]int64{"/site/search": 5, "/site/feed": 1}, all.ByPath)
	require.Equal(t, map[string]int64{"rate_limit": 3, "ok": 3}, all.ByMode)
	require.Equal(t, map[string]int64{"a": 4, "b": 2}, all.ByIdentity)
	require.Equal(t, map[string]int64{"p1": 4, "direct": 2}, all.ByProxy)
	require.Equal(t, map[string]int64{"429": 3, "200": 3}, all.ByStatus)
	require.Len(t, all.Entries, 3)
	require.Equal(t, "/site/feed", all.Entries[0].Path, "entries are sorted by path")
	require.Equal(t, int64(2), all.ProxyListener.Total)
	require.Len(t, all.ProxyListener.Entries, 2)
	require.Equal(t, "p1", all.ProxyListener.Entries[0].ProxyID)

	tests := []struct {
		name       string
		filter     statsFilter
		wantTotal  int64
		wantProxyT int64
	}{
		{name: "path prefix", filter: statsFilter{PathPrefix: "/site/se"}, wantTotal: 5, wantProxyT: 2},
		{name: "identity", filter: statsFilter{Identity: "a"}, wantTotal: 4, wantProxyT: 2},
		{name: "proxy", filter: statsFilter{Proxy: "p1"}, wantTotal: 4, wantProxyT: 1},
		{name: "mode", filter: statsFilter{Mode: ModeOK}, wantTotal: 3, wantProxyT: 2},
		{name: "combined", filter: statsFilter{Identity: "a", Mode: ModeOK}, wantTotal: 1, wantProxyT: 2},
		{name: "nothing", filter: statsFilter{Identity: "zzz"}, wantTotal: 0, wantProxyT: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := s.report(tt.filter)
			require.Equal(t, tt.wantTotal, rep.Total)
			require.Equal(t, tt.wantProxyT, rep.ProxyListener.Total)
			require.Nil(t, rep.Entries)
		})
	}

	s.reset()
	require.Zero(t, s.report(statsFilter{}).Total)
	require.Zero(t, s.report(statsFilter{}).ProxyListener.Total)
}

func TestSiteOverflowKeepsModeAndStatus(t *testing.T) {
	t.Parallel()
	k := siteOverflow(siteStatKey{Path: "/x", Rule: "/x", Mode: ModeCaptcha, Identity: "i", Proxy: "p", Status: 200})
	require.Equal(t, siteStatKey{
		Path: overflowLabel, Rule: overflowLabel, Mode: ModeCaptcha, Identity: overflowLabel, Proxy: overflowLabel, Status: 200,
	}, k)
}
