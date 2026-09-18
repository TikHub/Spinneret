package stats

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/store/postgres"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// newDBAggregator returns an aggregator on pool with a fixed clock and fast retries.
func newDBAggregator(t *testing.T, pool *pgxpool.Pool, now time.Time) *Aggregator {
	t.Helper()
	a := NewAggregator(pool, nil, observability.NewMetrics(), nil)
	a.now = func() time.Time { return now }
	a.retryBackoff = 5 * time.Millisecond
	return a
}

func queryInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int64
	require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func TestIntegrationConcurrentRecording(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	now := time.Now().UTC()
	a := newDBAggregator(t, pool, now)
	a.chunkSize = 97 // force several statements per table

	const goroutines, perGoroutine = 24, 400
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				site := fmt.Sprintf("sit_%d", i%3)
				eg := fmt.Sprintf("eg_%d", i%4)
				node := fmt.Sprintf("node-%d", g%6)
				at := now.Add(-time.Duration(i%3) * time.Minute)
				result := ResultOK
				if i%5 == 0 {
					result = ResultExhausted
				}
				a.RecordAcquire(AcquireRecord{At: at, NamespaceID: "ns_1", SiteID: site, EndpointGroupID: eg,
					IdentityTypeID: "ity_1", TokenID: fmt.Sprintf("tok_%d", g%2), Node: node, Result: result,
					Duration: time.Millisecond})
				outcome := "success"
				if i%10 == 0 {
					outcome = "captcha"
				}
				a.RecordReport(ReportRecord{ReceivedAt: at.Add(time.Second), FinishedAt: at, NamespaceID: "ns_1",
					TenantID: "ten_1", SiteID: site, EndpointGroupID: eg, IdentityID: fmt.Sprintf("idt_%d", i%50),
					ProxyID: fmt.Sprintf("pxy_%d", i%7), Node: node, Outcome: outcome, LatencyMs: 10, ResponseBytes: 100,
					ReportID: fmt.Sprintf("r-%d-%d", g, i), Markers: []string{"m"}})
				if i%8 == 0 {
					a.RecordLeaseEnd(LeaseEndRecord{At: at, NamespaceID: "ns_1", Node: node, Kind: LeaseEndAbandoned})
				}
				if i%20 == 0 {
					a.RecordRejectedReports("ns_1", node, 2)
				}
				// Flush concurrently with recording from time to time.
				if g == 0 && i%100 == 0 {
					if err := a.Flush(context.Background()); err != nil {
						t.Errorf("flush: %v", err)
					}
				}
			}
		}()
	}
	wg.Wait()
	require.NoError(t, a.Flush(context.Background()))

	total := int64(goroutines * perGoroutine)
	var exhausted, captcha, abandoned, rejected int64
	for i := range perGoroutine {
		if i%5 == 0 {
			exhausted++
		}
		if i%10 == 0 {
			captcha++
		}
		if i%8 == 0 {
			abandoned++
		}
		if i%20 == 0 {
			rejected += 2
		}
	}
	exhausted *= goroutines
	captcha *= goroutines
	abandoned *= goroutines
	rejected *= goroutines
	ok := total - exhausted

	require.Equal(t, total, queryInt(t, pool, "SELECT sum(count) FROM acquire_stats_minutely"))
	require.Equal(t, exhausted, queryInt(t, pool, "SELECT sum(count) FROM acquire_stats_minutely WHERE result = 'exhausted'"))
	require.Equal(t, total*1000, queryInt(t, pool, "SELECT sum(duration_us_sum) FROM acquire_stats_minutely"))
	// site and bucket both derive from i%3: 12 (i%3, i%4) combinations x 2 results.
	require.Equal(t, int64(12*2), queryInt(t, pool, "SELECT count(*) FROM acquire_stats_minutely"))
	require.Equal(t, ok, queryInt(t, pool, "SELECT sum(count) FROM payload_access_minutely"))
	require.Equal(t, int64(3*2), queryInt(t, pool, "SELECT count(*) FROM payload_access_minutely"))

	require.Equal(t, ok, queryInt(t, pool, "SELECT sum(acquires) FROM node_stats_minutely"))
	require.Equal(t, total, queryInt(t, pool, "SELECT sum(reports) FROM node_stats_minutely"))
	require.Equal(t, abandoned, queryInt(t, pool, "SELECT sum(abandoned) FROM node_stats_minutely"))
	require.Equal(t, rejected, queryInt(t, pool, "SELECT sum(rejected) FROM node_stats_minutely"))

	require.Equal(t, total, queryInt(t, pool, "SELECT sum(count) FROM outcome_stats_minutely"))
	require.Equal(t, captcha, queryInt(t, pool, "SELECT sum(count) FROM outcome_stats_minutely WHERE outcome = 'captcha'"))
	require.Equal(t, total*10, queryInt(t, pool, "SELECT sum(latency_ms_sum) FROM outcome_stats_minutely"))
	require.Equal(t, total*100, queryInt(t, pool, "SELECT sum(response_bytes_sum) FROM outcome_stats_minutely"))

	require.Equal(t, total, queryInt(t, pool, "SELECT sum(count) FROM identity_stats_hourly"))
	require.Equal(t, int64(0), queryInt(t, pool, "SELECT count(*) FROM identity_stats_hourly WHERE site_id = ''"))

	require.Equal(t, captcha, queryInt(t, pool, "SELECT count(*) FROM risk_events"))
	require.Equal(t, int64(0), queryInt(t, pool, "SELECT count(*) FROM risk_events WHERE outcome = 'success'"))
	require.Equal(t, captcha, queryInt(t, pool, "SELECT count(DISTINCT report_id) FROM risk_events"))

	c := a.Counters()
	for table, n := range c.DroppedRecords {
		require.Zero(t, n, table)
	}
	for table, n := range c.DroppedRows {
		require.Zero(t, n, table)
	}
	require.Zero(t, c.Invalid)
	require.Positive(t, counterValue(a.metrics, postgres.TableOutcomeStatsMinutely, batchOK))
}

func TestIntegrationMultipleAggregatorsAddUp(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	now := time.Now().UTC()
	const aggregators, records = 4, 300
	aggs := make([]*Aggregator, aggregators)
	for i := range aggs {
		aggs[i] = newDBAggregator(t, pool, now)
	}
	var wg sync.WaitGroup
	for round := range 3 {
		for _, a := range aggs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range records {
					a.RecordAcquire(AcquireRecord{At: now, NamespaceID: "ns_1", SiteID: "sit_1", EndpointGroupID: fmt.Sprintf("eg_%d", i%10),
						IdentityTypeID: "ity_1", TokenID: "tok_1", Node: "shared", Result: ResultOK, Duration: time.Microsecond})
					a.RecordReport(ReportRecord{ReceivedAt: now, FinishedAt: now, NamespaceID: "ns_1", SiteID: "sit_1",
						EndpointGroupID: fmt.Sprintf("eg_%d", i%10), IdentityID: fmt.Sprintf("idt_%d", i%10), Node: "shared",
						Outcome: "success", LatencyMs: int64(round + 1)})
				}
				// Every aggregator flushes the same buckets concurrently.
				if err := a.Flush(context.Background()); err != nil {
					t.Errorf("flush: %v", err)
				}
			}()
		}
		wg.Wait()
	}

	want := int64(aggregators * records * 3)
	require.Equal(t, want, queryInt(t, pool, "SELECT sum(count) FROM acquire_stats_minutely"))
	require.Equal(t, int64(10), queryInt(t, pool, "SELECT count(*) FROM acquire_stats_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT sum(duration_us_sum) FROM acquire_stats_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT count FROM payload_access_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT acquires FROM node_stats_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT reports FROM node_stats_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT sum(count) FROM outcome_stats_minutely"))
	require.Equal(t, int64(aggregators*records*(1+2+3)), queryInt(t, pool, "SELECT sum(latency_ms_sum) FROM outcome_stats_minutely"))
	require.Equal(t, want, queryInt(t, pool, "SELECT sum(count) FROM identity_stats_hourly"))
	require.Equal(t, int64(10), queryInt(t, pool, "SELECT count(*) FROM identity_stats_hourly"))
}

func TestIntegrationRiskEvents(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	a := newDBAggregator(t, pool, now)
	finished := now.Add(-3 * time.Second)

	a.RecordReport(ReportRecord{ReceivedAt: now, StartedAt: finished.Add(-800 * time.Millisecond), FinishedAt: finished,
		TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1", EndpointGroupID: "eg_1", IdentityID: "idt_1",
		ProxyID: "pxy_1", LeaseID: "lse_1", ReportID: "rep-1", Node: "node-a", TokenID: "tok_1", URI: "/api/feed?x=1",
		Method: "GET", HTTPStatus: 429, BusinessCode: "too_many", ErrorKind: "", Markers: []string{"rl", "bad\x00"},
		Outcome: "rate_limited", Blame: "both", Rule: "http-429", LatencyMs: 842, ResponseBytes: 48213})
	a.RecordReport(ReportRecord{ReceivedAt: now, TenantID: "ten_1", NamespaceID: "ns_1", SiteID: "sit_1",
		ReportID: "rep-2", Outcome: "network_error", ErrorKind: "timeout", URI: "bad\xffuri"})
	a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", ReportID: "rep-3", Outcome: "success"})
	require.NoError(t, a.Flush(context.Background()))

	require.Equal(t, int64(2), queryInt(t, pool, "SELECT count(*) FROM risk_events"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		id, tenant, ns, site, eg, identity, proxy, lease, node, token, uri, method, code, errKind, outcome, blame, rule string
		created                                                                                                         time.Time
		status, latency                                                                                                 int32
		bytes                                                                                                           int64
		markers                                                                                                         []string
		started, finishedAt                                                                                             *time.Time
	)
	err := pool.QueryRow(ctx, `SELECT id, created_at, tenant_id, namespace_id, site_id, endpoint_group_id, identity_id, proxy_id,
		lease_id, node, token_id, uri, method, http_status, business_code, error_kind, markers, outcome, blame, rule,
		latency_ms, response_bytes, started_at, finished_at FROM risk_events WHERE report_id = 'rep-1'`).Scan(
		&id, &created, &tenant, &ns, &site, &eg, &identity, &proxy, &lease, &node, &token, &uri, &method, &status, &code,
		&errKind, &markers, &outcome, &blame, &rule, &latency, &bytes, &started, &finishedAt)
	require.NoError(t, err)
	require.True(t, idgen.Valid(id, idgen.RiskEvent), id)
	require.True(t, created.Equal(finished))
	require.Equal(t, []string{"ten_1", "ns_1", "sit_1", "eg_1", "idt_1", "pxy_1", "lse_1", "node-a", "tok_1", "/api/feed?x=1", "GET"},
		[]string{tenant, ns, site, eg, identity, proxy, lease, node, token, uri, method})
	require.Equal(t, int32(429), status)
	require.Equal(t, "too_many", code)
	require.Empty(t, errKind)
	require.Equal(t, []string{"rl", "bad"}, markers)
	require.Equal(t, "rate_limited", outcome)
	require.Equal(t, "both", blame)
	require.Equal(t, "http-429", rule)
	require.Equal(t, int32(842), latency)
	require.Equal(t, int64(48213), bytes)
	require.NotNil(t, started)
	require.True(t, started.Equal(finished.Add(-800*time.Millisecond)))
	require.NotNil(t, finishedAt)
	require.True(t, finishedAt.Equal(finished))

	var createdAt2 time.Time
	var started2 *time.Time
	var uri2 string
	var markers2 []string
	require.NoError(t, pool.QueryRow(ctx, "SELECT created_at, started_at, uri, markers FROM risk_events WHERE report_id = 'rep-2'").
		Scan(&createdAt2, &started2, &uri2, &markers2))
	require.True(t, createdAt2.Equal(now))
	require.Nil(t, started2)
	require.Equal(t, "bad�uri", uri2)
	require.Empty(t, markers2)
}

func TestIntegrationRiskEventsDuplicateCopy(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rows := make([]riskRow, 3)
	for i := range rows {
		rows[i] = newRiskRow(&ReportRecord{NamespaceID: "ns_1", TenantID: "ten_1", ReportID: fmt.Sprintf("r%d", i)}, now, "captcha",
			cleanMarkers(nil))
		rows[i].id = idgen.New(idgen.RiskEvent)
	}
	require.NoError(t, copyRiskEvents(ctx, pool, rows[:2]))
	// A retried batch (lost commit acknowledgement) plus a new row.
	require.NoError(t, copyRiskEvents(ctx, pool, rows))
	require.Equal(t, int64(3), queryInt(t, pool, "SELECT count(*) FROM risk_events"))
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT count(*) FROM risk_events WHERE report_id = 'r2'"))
	// The staging table is dropped at commit and can be created again.
	require.NoError(t, copyRiskEvents(ctx, pool, rows))
	require.Equal(t, int64(3), queryInt(t, pool, "SELECT count(*) FROM risk_events"))
}

func TestIntegrationPartitionRecovery(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := newDBAggregator(t, pool, now)
	var ensured atomic.Int32
	realEnsure := a.ensurePartitions
	a.ensurePartitions = func(ctx context.Context) error {
		ensured.Add(1)
		return realEnsure(ctx)
	}

	for _, table := range []string{postgres.TableNodeStatsMinutely, postgres.TableRiskEvents} {
		partition := pgx.Identifier{table + "_p" + now.Format("20060102")}.Sanitize()
		_, err := pool.Exec(ctx, "DROP TABLE "+partition)
		require.NoError(t, err)
	}

	a.RecordRejectedReports("ns_1", "node-a", 3)
	a.RecordReport(ReportRecord{ReceivedAt: now, NamespaceID: "ns_1", TenantID: "ten_1", Node: "node-a", Outcome: "banned"})
	require.NoError(t, a.Flush(ctx))

	require.Equal(t, int32(1), ensured.Load(), "partitions are ensured once per flush")
	require.Equal(t, int64(3), queryInt(t, pool, "SELECT rejected FROM node_stats_minutely WHERE node = 'node-a'"))
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT reports FROM node_stats_minutely WHERE node = 'node-a'"))
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT count(*) FROM risk_events"))
	for table, n := range a.Counters().DroppedRows {
		require.Zero(t, n, table)
	}
}

func TestIntegrationBucketWithoutPartitionIsDropped(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	now := time.Now().UTC()
	a := newDBAggregator(t, pool, now)
	var ensured atomic.Int32
	realEnsure := a.ensurePartitions
	a.ensurePartitions = func(ctx context.Context) error {
		ensured.Add(1)
		return realEnsure(ctx)
	}
	farFuture := now.AddDate(2, 0, 0)

	a.RecordAcquire(AcquireRecord{At: now, NamespaceID: "ns_1", SiteID: "sit_1", Result: ResultOK})
	a.RecordAcquire(AcquireRecord{At: farFuture, NamespaceID: "ns_1", SiteID: "sit_1", Result: ResultOK})
	a.RecordAcquire(AcquireRecord{At: farFuture.Add(time.Minute), NamespaceID: "ns_1", SiteID: "sit_1", Result: ResultOK})
	require.NoError(t, a.Flush(context.Background()))

	require.Equal(t, int32(1), ensured.Load())
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT count(*) FROM acquire_stats_minutely"))
	c := a.Counters()
	require.Equal(t, int64(2), c.DroppedRows[postgres.TableAcquireStatsMinutely])
	require.Equal(t, int64(2), c.DroppedRows[postgres.TableNodeStatsMinutely])
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT acquires FROM node_stats_minutely"))
	require.Equal(t, 1.0, counterValue(a.metrics, postgres.TableAcquireStatsMinutely, batchDropped))
	require.Zero(t, a.acquire.pendingLen())
}

func TestIntegrationRetryAfterTransientFailure(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	now := time.Now().UTC()
	a := newDBAggregator(t, pool, now)
	a.maxRetries = 0
	a.outcome.exec = func(context.Context, []entry[outcomeKey, outcomeValue]) error { return errTransient }

	record := func() {
		a.RecordReport(ReportRecord{ReceivedAt: now, FinishedAt: now, NamespaceID: "ns_1", SiteID: "sit_1",
			IdentityID: "idt_1", Node: "n", Outcome: "success", LatencyMs: 5})
	}
	record()
	require.Error(t, a.Flush(context.Background()))
	require.Equal(t, int64(0), queryInt(t, pool, "SELECT count(*) FROM outcome_stats_minutely"))

	a.outcome.exec = a.writeOutcome
	record()
	require.NoError(t, a.Flush(context.Background()))
	require.Equal(t, int64(2), queryInt(t, pool, "SELECT count FROM outcome_stats_minutely"))
	require.Equal(t, int64(10), queryInt(t, pool, "SELECT latency_ms_sum FROM outcome_stats_minutely"))
	require.Equal(t, int64(2), queryInt(t, pool, "SELECT count FROM identity_stats_hourly"))
	require.Equal(t, int64(2), queryInt(t, pool, "SELECT reports FROM node_stats_minutely"))
}

func TestIntegrationRunFinalFlush(t *testing.T) {
	t.Parallel()
	pool := testutil.Postgres(t)
	a := NewAggregator(pool, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	require.Eventually(t, func() bool { return a.running.Load() }, 5*time.Second, time.Millisecond)
	a.RecordAcquire(AcquireRecord{NamespaceID: "ns_1", SiteID: "sit_1", Result: ResultSitePaused, Duration: time.Millisecond})
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return")
	}
	require.Equal(t, int64(1), queryInt(t, pool, "SELECT count FROM acquire_stats_minutely WHERE result = 'site_paused'"))
}
