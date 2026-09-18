package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/jobs"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

var discardLogger = slog.New(slog.DiscardHandler)

// fakeMembership is a static worker membership.
type fakeMembership struct {
	index, total int
	err          error
}

func (m fakeMembership) Membership(context.Context) (int, int, error) {
	return m.index, m.total, m.err
}

func (e *testEnv) newChecker(cfg HealthConfig, member Membership) *HealthChecker {
	return NewHealthChecker(cfg, e.pool, e.cipher, e.rdb, e.keys, e.cat, e.hot, member, e.bus, observability.NewMetrics(), e.logger)
}

func mustParse(t *testing.T, raw string) ParsedURL {
	t.Helper()
	u, err := ParseProxyURL(raw)
	require.NoError(t, err)
	return u
}

func TestHealthProbe(t *testing.T) {
	target := newCheckTarget(t, false)
	tlsTarget := newCheckTarget(t, true)
	authProxy := newTestHTTPProxy(t, "alice", "s3cr3t-pw", false)
	tlsProxy := newTestHTTPProxy(t, "", "", true)
	socksAuth := newTestSOCKS5(t, "sockuser", "sockpass-123")
	socksOpen := newTestSOCKS5(t, "", "")
	dead := closedAddr(t)

	tests := []struct {
		name    string
		proxy   string
		check   string
		ok      bool
		errPart string
	}{
		{name: "http proxy absolute-form", proxy: "http://alice:s3cr3t-pw@" + authProxy.Listener.Addr().String(), check: target.URL + "/check", ok: true},
		{name: "http proxy CONNECT to https", proxy: "http://alice:s3cr3t-pw@" + authProxy.Listener.Addr().String(), check: tlsTarget.URL + "/check", ok: true},
		{name: "https proxy", proxy: "https://" + tlsProxy.Listener.Addr().String(), check: target.URL + "/check", ok: true},
		{name: "socks5 with auth", proxy: "socks5://sockuser:sockpass-123@" + socksAuth.addr(), check: target.URL + "/check", ok: true},
		{name: "socks5 without auth to https", proxy: "socks5://" + socksOpen.addr(), check: tlsTarget.URL + "/check", ok: true},
		{name: "http proxy wrong password", proxy: "http://alice:wrong-pass@" + authProxy.Listener.Addr().String(), check: target.URL + "/check", errPart: "HTTP 407"},
		{name: "CONNECT wrong password", proxy: "http://alice:wrong-pass@" + authProxy.Listener.Addr().String(), check: tlsTarget.URL + "/check", errPart: "Proxy Authentication Required"},
		{name: "socks5 wrong password", proxy: "socks5://sockuser:wrong-pass@" + socksAuth.addr(), check: target.URL + "/check", errPart: "authentication failed"},
		{name: "proxy down", proxy: "http://alice:s3cr3t-pw@" + dead, check: target.URL + "/check", errPart: "refused"},
		{name: "socks5 down", proxy: "socks5://sockuser:sockpass-123@" + dead, check: target.URL + "/check", errPart: "refused"},
		{name: "target error status", proxy: "socks5://" + socksOpen.addr(), check: target.URL + "/unavailable", errPart: "HTTP 503"},
		{name: "timeout", proxy: "socks5://" + socksOpen.addr(), check: target.URL + "/slow", errPart: "timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthChecker(HealthConfig{CheckURL: tt.check, Timeout: 700 * time.Millisecond}, nil, nil, nil, redis.Keys{}, nil, nil, nil, nil, nil, discardLogger)
			h.tlsConfig = testTLSConfig(tlsTarget, tlsProxy.Server)
			res := h.probe(context.Background(), mustParse(t, tt.proxy))
			require.Equal(t, tt.ok, res.OK, res.Error)
			if tt.ok {
				require.Empty(t, res.Error)
				require.GreaterOrEqual(t, res.LatencyMs, 0)
				return
			}
			require.Contains(t, res.Error, tt.errPart)
			for _, secret := range []string{"s3cr3t-pw", "sockpass-123", "wrong-pass"} {
				require.NotContains(t, res.Error, secret)
			}
		})
	}
	require.Positive(t, authProxy.requests.Load())
	require.Positive(t, authProxy.connects.Load())
	require.Positive(t, socksAuth.conns.Load())
}

func TestHealthProbeExitIPAndGeo(t *testing.T) {
	target := newCheckTarget(t, false)
	socks := newTestSOCKS5(t, "", "")
	proxyURL := mustParse(t, "socks5://"+socks.addr())
	geoDB := writeTestGeoDB(t)

	tests := []struct {
		name, exitPath, geo string
		wantIP, region      string
	}{
		{name: "json", exitPath: "/ip", geo: geoDB, wantIP: "203.0.113.9", region: "JP"},
		{name: "plain text without geo", exitPath: "/ip-text", wantIP: "198.51.100.1"},
		{name: "invalid body", exitPath: "/ip-bad", geo: geoDB},
		{name: "error status", exitPath: "/unavailable", geo: geoDB},
		{name: "missing geo database", exitPath: "/ip", geo: "/nonexistent.mmdb", wantIP: "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHealthChecker(HealthConfig{CheckURL: target.URL + "/check", ExitIPURL: target.URL + tt.exitPath, GeoIPDB: tt.geo},
				nil, nil, nil, redis.Keys{}, nil, nil, nil, nil, nil, discardLogger)
			defer func() { require.NoError(t, h.Close()) }()
			res := h.probe(context.Background(), proxyURL)
			require.True(t, res.OK, res.Error)
			require.Equal(t, tt.wantIP, res.ExitIP)
			require.Equal(t, tt.region, res.Region)
			if tt.region != "" {
				require.Equal(t, "Tokyo", res.city)
			}
		})
	}

	t.Run("ipv6 lookup in ipv4 database is ignored", func(t *testing.T) {
		h := NewHealthChecker(HealthConfig{GeoIPDB: geoDB}, nil, nil, nil, redis.Keys{}, nil, nil, nil, nil, nil, discardLogger)
		defer func() { require.NoError(t, h.Close()) }()
		require.Equal(t, geoInfo{}, h.lookupRegion(netip.MustParseAddr("2001:db8::1")))
		require.Equal(t, geoInfo{region: "JP", city: "Tokyo"}, h.lookupRegion(netip.MustParseAddr("192.0.2.1")))
		require.NoError(t, h.Close())
	})
}

func TestHealthHelpers(t *testing.T) {
	interval := time.Minute
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		state    string
		failures int32
		ok       bool
		want     string
		wantFail int32
		next     time.Duration
	}{
		{name: "active success", state: StateActive, failures: 2, ok: true, want: StateActive, wantFail: 0, next: interval},
		{name: "active first failure", state: StateActive, ok: false, want: StateActive, wantFail: 1, next: interval},
		{name: "active third failure dies", state: StateActive, failures: 2, want: StateDead, wantFail: 3, next: interval},
		{name: "dead backoff doubles", state: StateDead, failures: 4, want: StateDead, wantFail: 5, next: 4 * interval},
		{name: "dead backoff capped", state: StateDead, failures: 40, want: StateDead, wantFail: 41, next: MaxDeadBackoff},
		{name: "dead recovers", state: StateDead, failures: 9, ok: true, want: StateActive, wantFail: 0, next: interval},
		{name: "disabled stays", state: StateDisabled, failures: 7, want: StateDisabled, wantFail: 8, next: interval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, failures, next := nextCheckState(tt.state, tt.failures, tt.ok, now, interval)
			require.Equal(t, tt.want, state)
			require.Equal(t, tt.wantFail, failures)
			require.Equal(t, now.Add(tt.next), next)
		})
	}
	require.Equal(t, interval, deadBackoff(interval, 0))

	u := ParsedURL{Scheme: SchemeHTTP, Host: "1.2.3.4", Port: 80, HasUser: true, Username: "someuser", Password: "p@ss w0rd"}
	require.Equal(t, "auth *** failed for ***", redact("auth someuser failed for p%40ss+w0rd", u))
	require.Equal(t, "x", redact("x", mustParse(t, "http://1.2.3.4:80")))
	require.Equal(t, "timeout: context deadline exceeded", describeError(context.DeadlineExceeded, u))
	require.Empty(t, describeErrorOrEmpty(nil, u))

	for body, want := range map[string]string{`{"ip":" 1.2.3.4 "}`: "1.2.3.4", "::ffff:5.6.7.8\n": "5.6.7.8", `{"ip":1}`: "", "": ""} {
		ip, ok := parseExitIP([]byte(body))
		if want == "" {
			require.False(t, ok, body)
			continue
		}
		require.True(t, ok, body)
		require.Equal(t, want, ip.String())
	}

	require.Equal(t, 0, ShardOf("pxy_1", 1))
	counts := make([]int, 4)
	for i := range 400 {
		counts[ShardOf(fmt.Sprintf("pxy_%d", i), 4)]++
	}
	for _, c := range counts {
		require.Positive(t, c)
	}

	h := NewHealthChecker(HealthConfig{}, nil, nil, nil, redis.Keys{}, nil, nil, nil, nil, nil, discardLogger)
	require.Equal(t, HealthConfig{CheckURL: DefaultCheckURL, Interval: DefaultCheckInterval, Timeout: DefaultCheckTimeout,
		Concurrency: DefaultCheckConcurrency}, h.cfg)
	job := h.Job()
	require.NoError(t, job.Validate())
	require.Equal(t, HealthJobName, job.Name)
	require.Equal(t, jobs.EachInstance, job.Mode)
	require.Equal(t, DefaultCheckInterval, job.Interval)
	_, err := h.transport(ParsedURL{Scheme: "ftp"})
	require.Error(t, err)
	require.Contains(t, h.probe(context.Background(), ParsedURL{Scheme: "ftp", Host: "x", Port: 1}).Error, "unsupported")
}

func TestHealthCheckerRunOnce(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	target := newCheckTarget(t, false)
	socks := newTestSOCKS5(t, "u", "pw-socks")
	dead := closedAddr(t)
	p := env.importLines(t, env.ns, "http://bad:pw-down@"+dead)[0]
	env.materialize(t, env.alpha, p)
	env.hot.reset()

	clock := time.Now().UTC().Add(time.Second).Truncate(time.Millisecond)
	h := env.newChecker(HealthConfig{CheckURL: target.URL + "/check", ExitIPURL: target.URL + "/ip", GeoIPDB: writeTestGeoDB(t),
		Interval: time.Minute, Timeout: 2 * time.Second, Concurrency: 4}, fakeMembership{index: 0, total: 1})
	defer func() { require.NoError(t, h.Close()) }()
	h.now = func() time.Time { return clock }
	run := func() int {
		t.Helper()
		n, err := h.RunOnce(ctx)
		require.NoError(t, err)
		return n
	}

	require.Equal(t, 1, run())
	row := env.row(t, p.ID)
	require.Equal(t, StateActive, row.State)
	require.Equal(t, 1, row.ConsecutiveCheckFailures)
	require.False(t, row.LastCheckOK)
	require.True(t, clock.Equal(*row.LastCheckAt))
	require.True(t, clock.Add(time.Minute).Equal(row.NextCheckAt))
	alphaKey := env.keys.ProxySite(env.alpha.Key, p.Key)
	require.Equal(t, "63.00", env.hget(t, alphaKey, "sc"), "0.1*0 + 0.9*70")
	require.Equal(t, "1", env.hget(t, alphaKey, "sn"))
	require.Equal(t, "1", env.hget(t, alphaKey, "nf"))
	require.Equal(t, strconv.FormatInt(clock.UnixMilli(), 10), env.hget(t, alphaKey, "lf"))
	require.Contains(t, env.dirty(t, env.alpha), "p"+strconv.FormatInt(p.Key, 10))
	exists, err := env.rdb.Do(ctx, env.rdb.B().Exists().Key(env.keys.ProxySite(env.beta.Key, p.Key)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, exists, "hashes are not created for sites without the proxy")

	require.Zero(t, run(), "not due before next_check_at")
	for i := 2; i <= 3; i++ {
		clock = clock.Add(time.Minute)
		require.Equal(t, 1, run())
	}
	row = env.row(t, p.ID)
	require.Equal(t, StateDead, row.State)
	require.Equal(t, 3, row.ConsecutiveCheckFailures)
	require.Contains(t, row.StateReason, "health check failed 3 times")
	require.NotContains(t, row.StateReason, "pw-down")
	require.True(t, clock.Add(time.Minute).Equal(row.NextCheckAt))
	evs := env.stateEvents(t, p.ID)
	require.Len(t, evs, 1)
	require.Equal(t, seRow{SubjectID: p.ID, From: StateActive, To: StateDead, Action: "health_check", Scope: "proxy",
		Actor: "system", Reason: row.StateReason}, evs[0])
	require.Equal(t, []string{p.ID}, env.hot.syncedIDs())
	var stateEvent *events.Event
	for _, ev := range env.events.snapshot() {
		if ev.Type == events.TypeProxyState {
			stateEvent = &ev
		}
	}
	require.NotNil(t, stateEvent)
	var data StateEventData
	require.NoError(t, json.Unmarshal(stateEvent.Data, &data))
	require.Equal(t, "health_check", data.Action)
	require.Equal(t, StateDead, data.To)
	require.Equal(t, 1.0, gaugeValue(t, h.metrics.Proxies.WithLabelValues("alpha", StateDead)))
	require.Equal(t, 0.0, gaugeValue(t, h.metrics.Proxies.WithLabelValues("alpha", StateActive)))

	clock = clock.Add(time.Minute)
	require.Equal(t, 1, run())
	require.True(t, clock.Add(2*time.Minute).Equal(env.row(t, p.ID).NextCheckAt), "dead proxies back off exponentially")

	// Point the proxy at a working SOCKS5 server: the next check recovers it.
	newURL := "socks5://u:pw-socks@" + socks.addr()
	_, err = env.svc.UpdateProxy(ctx, adminUser(), p.ID, UpdateRequest{URL: &newURL})
	require.NoError(t, err)
	env.hot.reset()
	clock = clock.Add(2 * time.Minute)
	require.Equal(t, 1, run())
	row = env.row(t, p.ID)
	require.Equal(t, StateActive, row.State)
	require.Zero(t, row.ConsecutiveCheckFailures)
	require.True(t, row.LastCheckOK)
	require.Equal(t, "203.0.113.9", row.ExitIP)
	require.Equal(t, "JP", row.Attributes.Region, "empty regions are filled from GeoIP")
	require.Equal(t, "Tokyo", row.Attributes.City)
	require.Equal(t, "health check succeeded", row.StateReason)
	require.Equal(t, []string{p.ID}, env.hot.syncedIDs())
	require.Len(t, env.stateEvents(t, p.ID), 2)
	require.Equal(t, "2", env.hget(t, alphaKey, "nf"), "success halves the failure streak (4 -> 2)")
}

func TestHealthCheckerSharding(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	dead := closedAddr(t)
	var urls []string
	for i := range 12 {
		urls = append(urls, fmt.Sprintf("http://user%d:pw@%s", i, dead))
	}
	_, err := env.svc.ImportProxies(ctx, adminUser(), env.ns, ImportRequest{Format: FormatLines, Data: strings.Join(urls, "\n")})
	require.NoError(t, err)
	var total int
	require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM proxies`).Scan(&total))
	require.Equal(t, 12, total)

	clock := time.Now().UTC().Add(time.Second)
	cfg := HealthConfig{CheckURL: "http://127.0.0.1:9/check", Timeout: time.Second, Concurrency: 3}
	var mu sync.Mutex
	checked := 0
	for index := range 2 {
		h := env.newChecker(cfg, fakeMembership{index: index, total: 2})
		h.now = func() time.Time { return clock }
		n, err := h.RunOnce(ctx)
		require.NoError(t, err)
		mu.Lock()
		checked += n
		mu.Unlock()
	}
	require.Equal(t, 12, checked)
	var unchecked int
	require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM proxies WHERE last_check_at IS NULL OR consecutive_check_failures <> 1`).Scan(&unchecked))
	require.Zero(t, unchecked, "every proxy is checked exactly once across the shards")

	t.Run("membership", func(t *testing.T) {
		h := env.newChecker(cfg, fakeMembership{err: errors.New("registry down")})
		_, err := h.RunOnce(ctx)
		require.ErrorContains(t, err, "registry down")
		require.ErrorContains(t, h.Job().Run(ctx), "registry down")
		h = env.newChecker(cfg, fakeMembership{index: 0, total: 0})
		n, err := h.RunOnce(ctx)
		require.NoError(t, err)
		require.Zero(t, n)
		h = env.newChecker(cfg, nil)
		_, err = h.RunOnce(ctx)
		require.Error(t, err)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		h = env.newChecker(cfg, fakeMembership{index: 0, total: 1})
		h.now = func() time.Time { return clock.Add(time.Hour) }
		n, err = h.RunOnce(cctx)
		require.NoError(t, err)
		require.Zero(t, n)
	})
}

// gaugeValue reads the current value of a gauge.
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

func TestCheckProxy(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	target := newCheckTarget(t, false)
	httpProxy := newTestHTTPProxy(t, "", "", false)
	p := env.importLines(t, env.ns, "http://"+httpProxy.Listener.Addr().String())[0]
	h := env.newChecker(HealthConfig{CheckURL: target.URL + "/check", Timeout: 2 * time.Second}, fakeMembership{total: 1})

	res, err := h.CheckProxy(ctx, adminUser(), p.ID)
	require.NoError(t, err)
	require.True(t, res.OK, res.Error)
	require.Equal(t, env.ns.ID, res.NamespaceID)
	require.Equal(t, testTenant, res.TenantID)
	row := env.row(t, p.ID)
	require.True(t, row.LastCheckOK)
	require.NotNil(t, row.LastCheckAt)

	_, err = h.CheckProxy(ctx, viewerUser(), p.ID)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = h.CheckProxy(ctx, foreignUser(), p.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = h.CheckProxy(ctx, tokenPrincipal(t, "report:write"), p.ID)
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = h.CheckProxy(ctx, tokenPrincipal(t, "proxy:write"), "pxy_missing")
	requireReason(t, err, apperr.ReasonNotFound)

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = h.CheckProxy(cctx, adminUser(), p.ID)
	require.Error(t, err)

	_, err = env.pool.Exec(ctx, `UPDATE proxies SET url_kek_id = 'missing' WHERE id = $1`, p.ID)
	require.NoError(t, err)
	_, err = h.CheckProxy(ctx, adminUser(), p.ID)
	requireReason(t, err, apperr.ReasonInternal)
}
