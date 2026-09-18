package signal

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	spntestutil "github.com/Evil0ctal/Spinneret/internal/testutil"
)

const (
	tenantID  = "ten_1"
	nsAID     = "ns_a"
	nsBID     = "ns_b"
	siteAKey  = 11
	siteA2Key = 12
	siteBKey  = 21
)

type rejectCall struct {
	namespaceID, node string
	n                 int
}

type fakeRejects struct {
	mu    sync.Mutex
	calls []rejectCall
}

func (f *fakeRejects) RecordRejectedReports(namespaceID, node string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rejectCall{namespaceID, node, n})
}

type ingestFixture struct {
	rdb     rueidis.Client
	keys    redis.Keys
	nsA     *catalog.Namespace
	siteA   *catalog.Site
	siteA2  *catalog.Site
	cat     *catalogtest.Catalog
	rejects *fakeRejects
	metrics *observability.Metrics
	ing     *Ingestor
}

func newIngestFixture(t *testing.T) *ingestFixture {
	t.Helper()
	rdb, keys := spntestutil.Redis(t)
	nsA := catalogtest.NewNamespace(tenantID, nsAID, "prod")
	siteA := catalogtest.AddSite(nsA, "sit_a", "shop", siteAKey, "web")
	siteA2 := catalogtest.AddSite(nsA, "sit_a2", "market", siteA2Key, "web")
	nsB := catalogtest.NewNamespace(tenantID, nsBID, "staging")
	catalogtest.AddSite(nsB, "sit_b", "other", siteBKey, "web")
	cat := catalogtest.New(nsA, nsB)
	f := &ingestFixture{
		rdb: rdb, keys: keys, nsA: nsA, siteA: siteA, siteA2: siteA2, cat: cat,
		rejects: &fakeRejects{}, metrics: observability.NewMetrics(),
	}
	f.ing = NewIngestor(Config{ReportShards: 16, DedupTTL: time.Hour, StreamMaxLen: 1000}, rdb, keys, cat, f.rejects, f.metrics, nil)
	return f
}

// lease creates a lease hash and returns its id.
func (f *ingestFixture) lease(t *testing.T, siteKey int64, shard int, namespaceID string) string {
	t.Helper()
	id := idgen.LeasePrefix(siteKey) + idgen.FormatShard(shard)
	err := f.rdb.Do(context.Background(), f.rdb.B().Hset().Key(f.keys.Lease(siteKey, id)).
		FieldValue().FieldValue("ns", namespaceID).FieldValue("st", "active").Build()).Error()
	require.NoError(t, err)
	return id
}

func tokenPrincipal(t *testing.T, namespaceID, namespaceName string, scopes ...string) *authz.Principal {
	t.Helper()
	sc, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: "tok_1", Name: "node-token", TenantID: tenantID,
		NamespaceID: namespaceID, NamespaceName: namespaceName, Scopes: sc, Node: "node-1",
	}
}

func newReport(id, lease string) *spinneretv1.Report {
	start := time.Now().Add(-time.Second)
	return &spinneretv1.Report{
		ReportId:   id,
		LeaseId:    lease,
		Uri:        "/api/v1/search?q=1",
		Method:     "GET",
		HttpStatus: 200,
		Markers:    []string{"empty_list"},
		LatencyMs:  842,
		StartedAt:  timestamppb.New(start),
		FinishedAt: timestamppb.New(start.Add(842 * time.Millisecond)),
		Release:    true,
	}
}

func reasons(rejected []Rejected) map[string]apperr.Reason {
	out := make(map[string]apperr.Reason, len(rejected))
	for _, r := range rejected {
		out[r.ReportID] = r.Reason
	}
	return out
}

func TestIngestValidationMatrix(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	p := tokenPrincipal(t, nsAID, "prod", "report:write")

	good := f.lease(t, siteAKey, 3, nsAID)
	otherNS := f.lease(t, siteBKey, 3, nsBID)
	mismatch := f.lease(t, siteAKey, 4, nsBID)

	noStart := newReport("no-start", good)
	noStart.StartedAt = nil
	backwards := newReport("backwards", good)
	backwards.FinishedAt = timestamppb.New(backwards.GetStartedAt().AsTime().Add(-time.Second))
	emptyURI := newReport("empty-uri", good)
	emptyURI.Uri = ""
	badStatus := newReport("bad-status", good)
	badStatus.HttpStatus = 1000
	badTime := newReport("bad-time", good)
	badTime.StartedAt = &timestamppb.Timestamp{Seconds: badTime.GetStartedAt().GetSeconds(), Nanos: -1}
	farFuture := newReport("far-future", good)
	farFuture.FinishedAt = &timestamppb.Timestamp{Seconds: 1 << 40}

	reports := []*spinneretv1.Report{
		newReport("ok-1", good),
		newReport("bad id!", good),
		noStart,
		backwards,
		emptyURI,
		badStatus,
		badTime,
		farFuture,
		newReport("malformed-lease", "lse_nope"),
		newReport("shard-out-of-range", idgen.LeasePrefix(siteAKey)+idgen.FormatShard(32)),
		newReport("unknown-site", idgen.LeasePrefix(999)+idgen.FormatShard(1)),
		newReport("other-namespace", otherNS),
		newReport("missing-lease", idgen.LeasePrefix(siteAKey)+idgen.FormatShard(2)),
		newReport("ns-mismatch", mismatch),
		nil,
	}
	accepted, duplicated, rejected, err := f.ing.Ingest(ctx, p, "node-1", reports)
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	require.Equal(t, 0, duplicated)
	require.Len(t, rejected, len(reports)-1)
	got := reasons(rejected)
	require.Equal(t, map[string]apperr.Reason{
		"bad id!":            apperr.ReasonInvalidArgument,
		"no-start":           apperr.ReasonInvalidArgument,
		"backwards":          apperr.ReasonInvalidArgument,
		"empty-uri":          apperr.ReasonInvalidArgument,
		"bad-status":         apperr.ReasonInvalidArgument,
		"bad-time":           apperr.ReasonInvalidArgument,
		"far-future":         apperr.ReasonInvalidArgument,
		"malformed-lease":    apperr.ReasonLeaseUnknown,
		"shard-out-of-range": apperr.ReasonLeaseUnknown,
		"unknown-site":       apperr.ReasonLeaseUnknown,
		"other-namespace":    apperr.ReasonLeaseUnknown,
		"missing-lease":      apperr.ReasonLeaseUnknown,
		"ns-mismatch":        apperr.ReasonLeaseUnknown,
		"":                   apperr.ReasonInvalidArgument,
	}, got)
	for _, r := range rejected {
		require.NotEmpty(t, r.Message)
		require.LessOrEqual(t, len(r.Message), maxRejectMessageLen)
	}

	require.Equal(t, []rejectCall{{nsAID, "node-1", len(reports) - 1}}, f.rejects.calls)
	require.InDelta(t, 1, counterValue(t, f.metrics.ReportIngestTotal.WithLabelValues("accepted")), 0)
	require.InDelta(t, float64(len(reports)-1), counterValue(t, f.metrics.ReportIngestTotal.WithLabelValues("rejected")), 0)

	entries, err := f.rdb.Do(ctx, f.rdb.B().Xrange().Key(f.keys.Stream(3)).Start("-").End("+").Build()).AsXRange()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, StreamVersion, entries[0].FieldValues[StreamFieldVersion])
	ev, err := DecodeEvent([]byte(entries[0].FieldValues[StreamFieldData]))
	require.NoError(t, err)
	require.Equal(t, "ok-1", ev.ReportID)
	require.Equal(t, good, ev.LeaseID)
	require.Equal(t, nsAID, ev.NamespaceID)
	require.Equal(t, tenantID, ev.TenantID)
	require.Equal(t, "tok_1", ev.TokenID)
	require.Equal(t, "node-1", ev.Node)
	require.Equal(t, "/api/v1/search?q=1", ev.URI)
	require.Equal(t, 200, ev.HTTPStatus)
	require.Equal(t, []string{"empty_list"}, ev.Markers)
	require.EqualValues(t, 842, ev.LatencyMs)
	require.EqualValues(t, 842, ev.FinishedAt-ev.StartedAt)
	require.True(t, ev.Release)
	require.WithinDuration(t, time.Now(), ev.Received(), 5*time.Second)
}

func TestIngestScopes(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	leaseA := f.lease(t, siteAKey, 1, nsAID)
	leaseA2 := f.lease(t, siteA2Key, 1, nsAID)

	tests := []struct {
		name      string
		principal *authz.Principal
		accepted  int
		rejected  map[string]apperr.Reason
	}{
		{
			name:      "site scoped token",
			principal: tokenPrincipal(t, nsAID, "prod", "report:write:market"),
			accepted:  1,
			rejected:  map[string]apperr.Reason{"r-a": apperr.ReasonScopeMissing},
		},
		{
			name:      "token without report scope",
			principal: tokenPrincipal(t, nsAID, "prod", "lease:acquire"),
			rejected:  map[string]apperr.Reason{"r-a": apperr.ReasonScopeMissing, "r-a2": apperr.ReasonScopeMissing},
		},
		{
			name:      "token of another namespace",
			principal: tokenPrincipal(t, nsBID, "staging", "report:write"),
			rejected:  map[string]apperr.Reason{"r-a": apperr.ReasonLeaseUnknown, "r-a2": apperr.ReasonLeaseUnknown},
		},
		{
			name:      "viewer user",
			principal: &authz.Principal{Kind: authz.KindUser, ID: "usr_1", TenantID: tenantID, Bindings: []authz.Binding{{TenantID: tenantID, Role: authz.RoleOwner}}},
			rejected:  map[string]apperr.Reason{"r-a": apperr.ReasonScopeMissing, "r-a2": apperr.ReasonScopeMissing},
		},
		{
			name:      "platform admin",
			principal: &authz.Principal{Kind: authz.KindUser, ID: "usr_admin", IsPlatformAdmin: true},
			accepted:  2,
			rejected:  map[string]apperr.Reason{},
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suffix := strings.Repeat("x", i)
			reports := []*spinneretv1.Report{newReport("r-a"+suffix, leaseA), newReport("r-a2"+suffix, leaseA2)}
			accepted, _, rejected, err := f.ing.Ingest(ctx, tt.principal, "n", reports)
			require.NoError(t, err)
			require.Equal(t, tt.accepted, accepted)
			want := make(map[string]apperr.Reason, len(tt.rejected))
			for id, reason := range tt.rejected {
				want[id+suffix] = reason
			}
			require.Equal(t, want, reasons(rejected))
		})
	}
}

func TestIngestDeduplication(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	p := tokenPrincipal(t, nsAID, "prod", "report:write")
	lease0 := f.lease(t, siteAKey, 0, nsAID)
	lease5 := f.lease(t, siteAKey, 5, nsAID)

	batch := []*spinneretv1.Report{
		newReport("dup", lease0),
		newReport("dup", lease0),
		newReport("other-shard", lease5),
	}
	accepted, duplicated, rejected, err := f.ing.Ingest(ctx, p, "n", batch)
	require.NoError(t, err)
	require.Equal(t, 2, accepted)
	require.Equal(t, 1, duplicated)
	require.Empty(t, rejected)

	accepted, duplicated, rejected, err = f.ing.Ingest(ctx, p, "n", batch)
	require.NoError(t, err)
	require.Equal(t, 0, accepted)
	require.Equal(t, 3, duplicated)
	require.Empty(t, rejected)
	require.InDelta(t, 4, counterValue(t, f.metrics.ReportIngestTotal.WithLabelValues("duplicated")), 0)
	require.Empty(t, f.rejects.calls)

	for shard, want := range map[int]int64{0: 1, 5: 1} {
		n, err := f.rdb.Do(ctx, f.rdb.B().Xlen().Key(f.keys.Stream(shard)).Build()).AsInt64()
		require.NoError(t, err)
		require.Equal(t, want, n)
	}
	ttl, err := f.rdb.Do(ctx, f.rdb.B().Pttl().Key(f.keys.Dedup(0, "dup")).Build()).AsInt64()
	require.NoError(t, err)
	require.Greater(t, ttl, int64(59*time.Minute/time.Millisecond))
	require.LessOrEqual(t, ttl, time.Hour.Milliseconds())
}

func TestIngestBatchErrors(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	p := tokenPrincipal(t, nsAID, "prod", "report:write")

	_, _, _, err := f.ing.Ingest(ctx, nil, "n", []*spinneretv1.Report{newReport("a", "b")})
	require.Equal(t, apperr.ReasonSessionInvalid, apperr.ReasonOf(err))

	tooMany := make([]*spinneretv1.Report, MaxReports+1)
	_, _, _, err = f.ing.Ingest(ctx, p, "n", tooMany)
	require.Equal(t, apperr.ReasonInvalidArgument, apperr.ReasonOf(err))

	accepted, duplicated, rejected, err := f.ing.Ingest(ctx, p, "n", nil)
	require.NoError(t, err)
	require.Zero(t, accepted+duplicated)
	require.Empty(t, rejected)

	accepted, _, rejected, err = f.ing.Ingest(ctx, p, "n", []*spinneretv1.Report{newReport("bad id", "x")})
	require.NoError(t, err)
	require.Zero(t, accepted)
	require.Len(t, rejected, 1)
}

func TestIngestRedisUnavailable(t *testing.T) {
	url := os.Getenv(spntestutil.RedisURLEnv)
	if url == "" || testing.Short() {
		t.Skip("requires " + spntestutil.RedisURLEnv)
	}
	f := newIngestFixture(t)
	ctx := context.Background()
	lease := f.lease(t, siteAKey, 1, nsAID)
	closed, err := redis.Open(ctx, url, nil)
	require.NoError(t, err)
	closed.Close()
	ing := NewIngestor(Config{}, closed, f.keys, f.cat, nil, nil, nil)
	_, _, _, err = ing.Ingest(ctx, tokenPrincipal(t, nsAID, "prod", "report:write"), "n",
		[]*spinneretv1.Report{newReport("r", lease)})
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok)
	require.Equal(t, apperr.ReasonInternal, e.Reason)
	require.Equal(t, "unavailable", e.Code.String())
}

func TestNewIngestorDefaults(t *testing.T) {
	ing := NewIngestor(Config{ReportShards: 1000, DedupTTL: time.Microsecond}, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil)
	require.Equal(t, MaxReportShards, ing.cfg.ReportShards)
	require.Equal(t, time.Millisecond, ing.cfg.DedupTTL)
	require.Equal(t, DefaultStreamMaxLen, ing.cfg.StreamMaxLen)
	ing = NewIngestor(Config{}, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil)
	require.Equal(t, DefaultReportShards, ing.cfg.ReportShards)
	require.Equal(t, DefaultDedupTTL, ing.cfg.DedupTTL)
}

func TestSanitizeNode(t *testing.T) {
	require.Equal(t, "node-1", sanitizeNode("  node-1\n"))
	require.Equal(t, "ab", sanitizeNode("a\x00bé"))
	require.Len(t, sanitizeNode(strings.Repeat("n", 300)), maxNodeLen)
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// Accepted reports keep their lease hash readable until the worker can process
// them even when the stream is backlogged beyond the late-report window: ingest
// counts them ("rv"), records how long the hash must be kept ("kx") and extends
// the TTL of an ended lease; an active lease keeps no TTL.
func TestIngestRetainsLeaseForProcessing(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	p := tokenPrincipal(t, nsAID, "prod", "report:write")
	now := time.UnixMilli(1_758_011_411_962)
	f.ing.now = func() time.Time { return now }

	active := f.lease(t, siteAKey, 1, nsAID)
	ended := f.lease(t, siteAKey, 2, nsAID)
	endedKey := f.keys.Lease(siteAKey, ended)
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Hset().Key(endedKey).FieldValue().FieldValue("st", "released").Build()).Error())
	require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Pexpire().Key(endedKey).Milliseconds(100).Build()).Error())
	foreign := f.lease(t, siteAKey, 3, nsBID)

	accepted, _, rejected, err := f.ing.Ingest(ctx, p, "n", []*spinneretv1.Report{
		newReport("a1", active), newReport("a2", active), newReport("e1", ended), newReport("f1", foreign),
	})
	require.NoError(t, err)
	require.Equal(t, 3, accepted)
	require.Equal(t, map[string]apperr.Reason{"f1": apperr.ReasonLeaseUnknown}, reasons(rejected))

	hget := func(key, field string) string {
		v, err := f.rdb.Do(ctx, f.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
		if rueidis.IsRedisNil(err) {
			return ""
		}
		require.NoError(t, err)
		return v
	}
	pttl := func(key string) int64 {
		n, err := f.rdb.Do(ctx, f.rdb.B().Pttl().Key(key).Build()).AsInt64()
		require.NoError(t, err)
		return n
	}
	keep := strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10)
	activeKey := f.keys.Lease(siteAKey, active)
	require.Equal(t, "2", hget(activeKey, "rv"))
	require.Equal(t, keep, hget(activeKey, "kx"))
	require.Equal(t, int64(-1), pttl(activeKey), "an active lease keeps no TTL")
	require.Equal(t, "1", hget(endedKey, "rv"))
	require.Equal(t, keep, hget(endedKey, "kx"))
	require.Greater(t, pttl(endedKey), int64(59*time.Minute/time.Millisecond), "the ended lease is retained")
	foreignKey := f.keys.Lease(siteAKey, foreign)
	require.Equal(t, "", hget(foreignKey, "rv"), "leases of another namespace are not touched")

	// The ended lease outlives its former TTL (real Redis time).
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, nsAID, hget(endedKey, "ns"))

	// kx never moves backwards; a missing lease is not created.
	f.ing.now = func() time.Time { return now.Add(-time.Minute) }
	missing := idgen.LeasePrefix(siteAKey) + idgen.FormatShard(4)
	_, _, rejected, err = f.ing.Ingest(ctx, p, "n", []*spinneretv1.Report{newReport("a3", active), newReport("m1", missing)})
	require.NoError(t, err)
	require.Equal(t, map[string]apperr.Reason{"m1": apperr.ReasonLeaseUnknown}, reasons(rejected))
	require.Equal(t, keep, hget(activeKey, "kx"))
	require.Equal(t, "3", hget(activeKey, "rv"))
	n, err := f.rdb.Do(ctx, f.rdb.B().Exists().Key(f.keys.Lease(siteAKey, missing)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)
}

// Retention covers reports that were timely at ingest (the lease is active or
// ended at most the late window ago). A report after the late window is still
// accepted while the hash exists, but it does not extend the retention: a node
// that keeps reporting on an ended lease eventually gets lease_unknown, and a
// lease hash lives at most late window + DedupTTL after the lease ended.
// TestIngestRetainsLeasesInBatches pins the chunked, per-site lease_retain
// call: more distinct leases than maxRetainBatch are split over several calls,
// every lease of the request is still counted exactly once, and the reply of
// each chunk is mapped back to the right lease.
func TestIngestRetainsLeasesInBatches(t *testing.T) {
	f := newIngestFixture(t)
	ctx := context.Background()
	p := tokenPrincipal(t, nsAID, "prod", "report:write")

	const leases = maxRetainBatch + 50
	ids := make([]string, 0, leases)
	reports := make([]*spinneretv1.Report, 0, leases+2)
	for i := 0; i < leases; i++ {
		id := f.lease(t, siteAKey, i%16, nsAID)
		ids = append(ids, id)
		reports = append(reports, newReport("b"+strconv.Itoa(i), id))
	}
	// One lease of another namespace and one that does not exist: both must be
	// rejected without disturbing the mapping of the chunk they fall into.
	foreign := f.lease(t, siteAKey, 3, nsBID)
	reports = append(reports, newReport("foreign", foreign),
		newReport("gone", idgen.LeasePrefix(siteAKey)+idgen.FormatShard(4)))

	accepted, duplicated, rejected, err := f.ing.Ingest(ctx, p, "n", reports)
	require.NoError(t, err)
	require.Equal(t, leases, accepted)
	require.Equal(t, 0, duplicated)
	require.Equal(t, map[string]apperr.Reason{
		"foreign": apperr.ReasonLeaseUnknown,
		"gone":    apperr.ReasonLeaseUnknown,
	}, reasons(rejected))

	for _, id := range ids {
		key := f.keys.Lease(siteAKey, id)
		rv, err := f.rdb.Do(ctx, f.rdb.B().Hget().Key(key).Field("rv").Build()).ToString()
		require.NoError(t, err, id)
		require.Equal(t, "1", rv, id)
		kx, err := f.rdb.Do(ctx, f.rdb.B().Hget().Key(key).Field("kx").Build()).AsInt64()
		require.NoError(t, err, id)
		require.Greater(t, kx, time.Now().UnixMilli(), id)
	}
	// The foreign lease is never touched.
	exists, err := f.rdb.Do(ctx, f.rdb.B().Hexists().Key(f.keys.Lease(siteAKey, foreign)).Field("rv").Build()).AsBool()
	require.NoError(t, err)
	require.False(t, exists)
}

func TestIngestRetainsLeaseOnlyForTimelyReports(t *testing.T) {
	tests := []struct {
		name   string
		cfg    time.Duration
		window time.Duration
	}{
		{name: "default window", cfg: 0, window: DefaultLateReportWindow},
		{name: "configured window", cfg: time.Minute, window: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newIngestFixture(t)
			ctx := context.Background()
			f.ing = NewIngestor(Config{ReportShards: 16, DedupTTL: time.Hour, StreamMaxLen: 1000, LateReportWindow: tc.cfg},
				f.rdb, f.keys, f.cat, f.rejects, f.metrics, nil)
			now := time.UnixMilli(1_758_011_411_962)
			f.ing.now = func() time.Time { return now }
			p := tokenPrincipal(t, nsAID, "prod", "report:write")

			const ttlMs = 60_000
			prevKeep := strconv.FormatInt(now.Add(time.Minute).UnixMilli(), 10)
			timely := f.lease(t, siteAKey, 1, nsAID)
			late := f.lease(t, siteAKey, 2, nsAID)
			for id, end := range map[string]time.Time{timely: now.Add(-tc.window), late: now.Add(-tc.window - time.Millisecond)} {
				key := f.keys.Lease(siteAKey, id)
				require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Hset().Key(key).FieldValue().
					FieldValue("st", "expired").FieldValue("end", strconv.FormatInt(end.UnixMilli(), 10)).
					FieldValue("kx", prevKeep).Build()).Error())
				require.NoError(t, f.rdb.Do(ctx, f.rdb.B().Pexpire().Key(key).Milliseconds(ttlMs).Build()).Error())
			}

			accepted, _, rejected, err := f.ing.Ingest(ctx, p, "n", []*spinneretv1.Report{newReport("t1", timely), newReport("l1", late)})
			require.NoError(t, err)
			require.Equal(t, 2, accepted, "both leases still exist")
			require.Empty(t, rejected)

			hget := func(key, field string) string {
				v, err := f.rdb.Do(ctx, f.rdb.B().Hget().Key(key).Field(field).Build()).ToString()
				require.NoError(t, err)
				return v
			}
			pttl := func(key string) int64 {
				n, err := f.rdb.Do(ctx, f.rdb.B().Pttl().Key(key).Build()).AsInt64()
				require.NoError(t, err)
				return n
			}
			timelyKey, lateKey := f.keys.Lease(siteAKey, timely), f.keys.Lease(siteAKey, late)
			require.Equal(t, strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10), hget(timelyKey, "kx"))
			require.Greater(t, pttl(timelyKey), int64(59*time.Minute/time.Millisecond), "a timely report retains the lease")
			require.Equal(t, "1", hget(lateKey, "rv"), "late reports are still counted")
			require.Equal(t, prevKeep, hget(lateKey, "kx"), "a late report does not extend the retention")
			require.LessOrEqual(t, pttl(lateKey), int64(ttlMs), "a late report does not raise the TTL")
		})
	}
}
