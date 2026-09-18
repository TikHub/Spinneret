package worker

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/signal"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

const (
	testTenant   = "ten_1"
	testNS       = "ns_1"
	testSiteKey  = 42
	testGroupKey = 4201
	testShards   = 4
)

// baseTime is a fixed processing clock origin.
var baseTime = time.UnixMilli(1_758_011_411_962)

type fakeExec struct {
	mu    sync.Mutex
	calls []ExecInput
	err   error
	fails int
	// idempotent is returned by IdempotentExecute.
	idempotent bool
}

func (f *fakeExec) IdempotentExecute() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.idempotent
}

func (f *fakeExec) Execute(_ context.Context, in ExecInput) (ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.fails > 0 {
		f.fails--
		return ExecResult{}, fmt.Errorf("transient executor failure")
	}
	return ExecResult{}, f.err
}

func (f *fakeExec) snapshot() []ExecInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ExecInput(nil), f.calls...)
}

type releaseCall struct {
	siteKey int64
	leaseID string
}

type fakeReleaser struct {
	mu    sync.Mutex
	calls []releaseCall
}

func (f *fakeReleaser) ReleaseLease(_ context.Context, siteKey int64, leaseID string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, releaseCall{siteKey, leaseID})
	return true, nil
}

type fakeBreaker struct {
	mu    sync.Mutex
	calls [][2]int64
}

func (f *fakeBreaker) NotifyRisk(siteKey, egKey int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, [2]int64{siteKey, egKey})
}

type fakeRecorder struct {
	mu      sync.Mutex
	records []ReportRecord
}

func (f *fakeRecorder) RecordReport(r ReportRecord) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, r)
}

func (f *fakeRecorder) snapshot() []ReportRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReportRecord(nil), f.records...)
}

func (f *fakeRecorder) countByReport() map[string]int {
	out := map[string]int{}
	for _, r := range f.snapshot() {
		out[r.ReportID]++
	}
	return out
}

// fixture is a site with a "search" endpoint group backed by real Redis.
type fixture struct {
	rdb     rueidis.Client
	keys    redis.Keys
	cat     *catalogtest.Catalog
	ns      *catalog.Namespace
	site    *catalog.Site
	group   *catalog.EndpointGroup
	exec    *fakeExec
	rel     *fakeReleaser
	brk     *fakeBreaker
	rec     *fakeRecorder
	metrics *observability.Metrics
	w       *Worker

	now time.Time
	seq int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rdb, keys := testutil.Redis(t)
	ns := catalogtest.NewNamespace(testTenant, testNS, "prod")
	st := catalogtest.AddSite(ns, "sit_1", "shop", testSiteKey, "web")
	group := catalogtest.AddGroup(st, "eg_search", "web", "search", testGroupKey)
	f := &fixture{
		rdb: rdb, keys: keys, cat: catalogtest.New(ns), ns: ns, site: st, group: group,
		exec: &fakeExec{}, rel: &fakeReleaser{}, brk: &fakeBreaker{}, rec: &fakeRecorder{},
		metrics: observability.NewMetrics(), now: baseTime,
	}
	f.w = f.newWorker("inst-a", Config{})
	return f
}

// newWorkerWithCatalog builds a worker of this fixture backed by another catalog.
func (f *fixture) newWorkerWithCatalog(instance string, cat catalog.Catalog) *Worker {
	w := New(Config{InstanceID: instance, ReportShards: testShards}, f.rdb, f.keys, cat,
		f.exec, f.rel, f.brk, f.rec, f.metrics, nil)
	w.now = func() time.Time { return f.now }
	w.jitter = func() float64 { return 0.5 }
	return w
}

func (f *fixture) newWorker(instance string, cfg Config) *Worker {
	cfg.InstanceID = instance
	if cfg.ReportShards == 0 {
		cfg.ReportShards = testShards
	}
	w := New(cfg, f.rdb, f.keys, f.cat, f.exec, f.rel, f.brk, f.rec, f.metrics, nil)
	w.now = func() time.Time { return f.now }
	w.jitter = func() float64 { return 0.5 } // cooldown jitter factor 1.0
	return w
}

func (f *fixture) setPolicies(t *testing.T, actionYAML string) {
	t.Helper()
	spec, err := policy.ParseYAML(policy.KindAction, []byte(actionYAML))
	require.NoError(t, err)
	act, err := policy.CompileAction([]*policy.ActionSpec{spec.(*policy.ActionSpec)})
	require.NoError(t, err)
	catalogtest.SetPolicies(f.group, catalogtest.Policies{Action: act})
}

func (f *fixture) hset(t *testing.T, key string, kv ...string) {
	t.Helper()
	require.Zero(t, len(kv)%2)
	cmd := f.rdb.B().Hset().Key(key).FieldValue()
	for i := 0; i+1 < len(kv); i += 2 {
		cmd = cmd.FieldValue(kv[i], kv[i+1])
	}
	require.NoError(t, f.rdb.Do(context.Background(), cmd.Build()).Error())
}

func (f *fixture) hget(t *testing.T, key, field string) string {
	t.Helper()
	v, err := f.rdb.Do(context.Background(), f.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return ""
	}
	require.NoError(t, err)
	return v
}

// identity creates an identity hash (state active unless overridden).
func (f *fixture) identity(t *testing.T, i int64, kv ...string) {
	t.Helper()
	fields := append([]string{"iid", fmt.Sprintf("idt_%d", i), "st", "active", "ty", "web_cookie", "acc", ""}, kv...)
	f.hset(t, f.keys.Identity(testSiteKey, i), fields...)
}

// proxy creates a proxy-per-site hash.
func (f *fixture) proxy(t *testing.T, p int64) {
	t.Helper()
	f.hset(t, f.keys.ProxySite(testSiteKey, p), "pid", fmt.Sprintf("pxy_%d", p), "st", "active")
}

// lease creates an active lease of identity i (proxy p, 0 = none) on the
// search group and returns its id.
func (f *fixture) lease(t *testing.T, i, p int64, kv ...string) string {
	t.Helper()
	id := idgen.LeasePrefix(testSiteKey) + idgen.FormatShard(int(i%testShards))
	proxyKey, proxyID := "", ""
	if p > 0 {
		proxyKey, proxyID = strconv.FormatInt(p, 10), fmt.Sprintf("pxy_%d", p)
	}
	fields := append([]string{
		"i", strconv.FormatInt(i, 10), "iid", fmt.Sprintf("idt_%d", i), "e", strconv.Itoa(testGroupKey),
		"p", proxyKey, "pid", proxyID, "n", "node-1", "tk", "tok_1", "ns", testNS,
		"a", strconv.FormatInt(f.now.UnixMilli(), 10), "x", strconv.FormatInt(f.now.Add(2*time.Minute).UnixMilli(), 10),
		"st", "active", "pr", "0", "rc", "0",
	}, kv...)
	f.hset(t, f.keys.Lease(testSiteKey, id), fields...)
	return id
}

// event builds a successful (HTTP 200) report event for a lease.
func (f *fixture) event(lease string) signal.Event {
	f.seq++
	return signal.Event{
		ReportID: fmt.Sprintf("r-%d", f.seq), LeaseID: lease, NamespaceID: testNS, TenantID: testTenant,
		TokenID: "tok_1", Node: "node-1", ReceivedAt: f.now.UnixMilli(), URI: "/search", Method: "GET",
		HTTPStatus: 200, Markers: []string{}, LatencyMs: 10, ResponseBytes: 100,
		StartedAt: f.now.Add(-10 * time.Millisecond).UnixMilli(), FinishedAt: f.now.UnixMilli(),
	}
}

// streamID returns a fresh, increasing stream id.
func (f *fixture) streamID() string {
	f.seq++
	return fmt.Sprintf("%d-%d", f.now.UnixMilli(), f.seq)
}

// process runs the pipeline for ev with a fresh stream id on the lease shard.
func (f *fixture) process(t *testing.T, ev signal.Event) string {
	t.Helper()
	id := f.streamID()
	f.processWithID(t, ev, id)
	return id
}

func (f *fixture) processWithID(t *testing.T, ev signal.Event, id string) {
	t.Helper()
	ref, err := idgen.ParseLeaseID(ev.LeaseID)
	require.NoError(t, err)
	f.w.processEvent(context.Background(), ref.Shard, id, ev)
}

// hs returns the decoded hs entry of identity i on the search group.
func (f *fixture) hs(t *testing.T, i int64) (score float64, sts, samples, nfail, lastfail int64) {
	t.Helper()
	v := f.hget(t, f.keys.Health(testSiteKey, testGroupKey), strconv.FormatInt(i, 10))
	if v == "" {
		return 0, 0, 0, 0, 0
	}
	var cd, ru, lu int64
	_, err := fmt.Sscanf(replacePipes(v), "%f %d %d %d %d %d %d %d", &score, &sts, &samples, &nfail, &lastfail, &cd, &ru, &lu)
	require.NoError(t, err)
	return score, sts, samples, nfail, lastfail
}

func replacePipes(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '|' {
			b[i] = ' '
		}
	}
	return string(b)
}

func (f *fixture) smembers(t *testing.T, key string) []string {
	t.Helper()
	v, err := f.rdb.Do(context.Background(), f.rdb.B().Smembers().Key(key).Build()).AsStrSlice()
	require.NoError(t, err)
	return v
}

func counterValue(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	m, ok := c.(prometheus.Metric)
	require.True(t, ok)
	var out dto.Metric
	require.NoError(t, m.Write(&out))
	if out.Counter != nil {
		return out.GetCounter().GetValue()
	}
	if out.Gauge != nil {
		return out.GetGauge().GetValue()
	}
	return float64(out.GetHistogram().GetSampleCount())
}

func (f *fixture) lastRecord(t *testing.T) ReportRecord {
	t.Helper()
	recs := f.rec.snapshot()
	require.NotEmpty(t, recs)
	return recs[len(recs)-1]
}
