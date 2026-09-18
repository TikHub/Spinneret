package worker

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/policy"
	"github.com/TikHub/Spinneret/internal/signal"
)

func TestActionsRateLimitedCooldown(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	f.proxy(t, 9)
	lease := f.lease(t, 1, 9)

	f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
	f.now = f.now.Add(time.Second)
	f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))

	calls := f.exec.snapshot()
	require.Len(t, calls, 2)
	first := calls[0]
	require.Equal(t, []policy.PlannedAction{{
		Action: policy.ActionCooldown, Scope: policy.ScopeIdentityEndpoint, Duration: time.Minute,
		Severity: 1, RuleIndex: 0, RuleName: "rate-limited-cooldown", Source: policy.SourceRule,
	}}, first.Planned)
	require.False(t, first.Shadow)
	require.Equal(t, baseTime, first.Now)
	require.Same(t, f.ns, first.Namespace)
	require.Same(t, f.site, first.Site)
	require.Same(t, f.group, first.Group)
	require.Equal(t, lease, first.LeaseID)
	require.Equal(t, "r-1", first.ReportID)
	require.EqualValues(t, 1, first.IdentityKey)
	require.Equal(t, "idt_1", first.IdentityID)
	require.EqualValues(t, 9, first.ProxyKey)
	require.Equal(t, "pxy_9", first.ProxyID)
	require.Zero(t, first.AccountKey)
	require.Equal(t, policy.OutcomeRateLimited, first.Outcome)
	require.Equal(t, "rate-limited", first.RuleName)
	// Streak 2: base 60s x multiplier 2.
	require.Equal(t, 2*time.Minute, calls[1].Planned[0].Duration)
	require.Equal(t, [][2]int64{{testSiteKey, testGroupKey}, {testSiteKey, testGroupKey}}, f.brk.calls)

	recs := f.rec.snapshot()
	require.Len(t, recs, 2)
	rec := recs[0]
	require.Equal(t, ReportRecord{
		ReceivedAt: time.UnixMilli(baseTime.UnixMilli()), StartedAt: time.UnixMilli(baseTime.Add(-10 * time.Millisecond).UnixMilli()),
		FinishedAt: time.UnixMilli(baseTime.UnixMilli()),
		TenantID:   testTenant, NamespaceID: testNS, SiteID: "sit_1", Site: "shop", EndpointGroupID: "eg_search",
		EndpointGroup: "search", Client: "web", IdentityID: "idt_1", IdentityType: "web_cookie", ProxyID: "pxy_9",
		LeaseID: lease, ReportID: "r-1", Node: "node-1", TokenID: "tok_1", URI: "/search", Method: "GET",
		HTTPStatus: 429, Markers: []string{}, Outcome: policy.OutcomeRateLimited, Blame: "both", Rule: "rate-limited",
		LatencyMs: 10, ResponseBytes: 100,
	}, rec)
	require.InDelta(t, 2, counterValue(t, f.metrics.ReportTotal.WithLabelValues("shop", "search", policy.OutcomeRateLimited)), 0)
	require.InDelta(t, 2, counterValue(t, f.metrics.ReportLag), 0)
}

func TestActionsCaptchaBanAndEscalation(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	for k := range 3 {
		f.process(t, withOutcome(f.event(lease), policy.OutcomeCaptcha))
		f.now = f.now.Add(time.Hour)
		if k < 2 {
			calls := f.exec.snapshot()
			require.Equal(t, policy.ActionCooldown, calls[len(calls)-1].Planned[0].Action)
			require.Equal(t, policy.ScopeIdentitySite, calls[len(calls)-1].Planned[0].Scope)
		}
	}
	calls := f.exec.snapshot()
	require.Len(t, calls, 3)
	require.Equal(t, []policy.PlannedAction{{
		Action: policy.ActionBan, Scope: policy.ScopeIdentity, Duration: 12 * time.Hour,
		Severity: 4, RuleIndex: 3, RuleName: "captcha-ban", Source: policy.SourceRule,
	}}, calls[2].Planned)

	// One earlier ban within 7 days escalates the next ban to 72h.
	ms := f.now.Add(-2 * 24 * time.Hour).UnixMilli()
	require.NoError(t, f.rdb.Do(context.Background(), f.rdb.B().Zadd().Key(f.keys.Bans(testSiteKey, 1)).ScoreMember().
		ScoreMember(float64(ms), strconv.FormatInt(ms, 10)).Build()).Error())
	f.process(t, withOutcome(f.event(lease), policy.OutcomeCaptcha))
	calls = f.exec.snapshot()
	last := calls[len(calls)-1].Planned
	require.Len(t, last, 1)
	require.Equal(t, policy.ActionBan, last[0].Action)
	require.Equal(t, 72*time.Hour, last[0].Duration)
	require.Equal(t, policy.SourceEscalation, last[0].Source)
}

func TestActionsLifecycleAndShadow(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1, "st", "pending")
	lease := f.lease(t, 1, 0)
	f.process(t, withOutcome(f.event(lease), policy.OutcomeSuccess))
	calls := f.exec.snapshot()
	require.Len(t, calls, 1)
	require.Equal(t, policy.ActionActivate, calls[0].Planned[0].Action)

	f.setPolicies(t, "name: shadowed\nmode: shadow\nrules:\n  - when: { outcome: forbidden }\n    action: quarantine\n    scope: identity\n    duration: 1h\n")
	f.identity(t, 2)
	f.process(t, withOutcome(f.event(f.lease(t, 2, 0)), policy.OutcomeForbidden))
	calls = f.exec.snapshot()
	require.Len(t, calls, 2)
	require.True(t, calls[1].Shadow)
	require.Equal(t, policy.ActionQuarantine, calls[1].Planned[0].Action)
	require.Equal(t, time.Hour, calls[1].Planned[0].Duration)
}

func TestActionsAccountContext(t *testing.T) {
	f := newFixture(t)
	spec, err := policy.ParseYAML(policy.KindSignal, []byte("name: banned\nrules:\n  - when: { business_code: [\"b1\"] }\n    outcome: banned\n"))
	require.NoError(t, err)
	sig, err := policy.CompileSignal([]*policy.SignalSpec{spec.(*policy.SignalSpec)})
	require.NoError(t, err)
	f.group.Signal = sig
	f.identity(t, 1, "acc", "77")
	ev := f.event(f.lease(t, 1, 0))
	ev.BusinessCode = "b1"
	f.process(t, ev)
	require.Equal(t, policy.OutcomeBanned, f.lastRecord(t).Outcome)
	calls := f.exec.snapshot()
	require.Len(t, calls, 1)
	require.EqualValues(t, 77, calls[0].AccountKey)
	require.Equal(t, policy.ScopeAccount, calls[0].Planned[0].Scope)
	require.True(t, calls[0].Planned[0].Permanent)
}

func TestReleaseAndBreakerNotify(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	active := f.lease(t, 1, 0)
	released := f.lease(t, 1, 0, "st", "released", "end", strconv.FormatInt(f.now.Add(-time.Minute).UnixMilli(), 10))

	ev := f.event(active)
	ev.Release = true
	f.process(t, ev)
	ev = f.event(released)
	ev.Release = true
	f.process(t, ev)
	f.process(t, f.event(active))

	require.Equal(t, []releaseCall{{testSiteKey, active}}, f.rel.calls)
	require.Empty(t, f.brk.calls, "success is not a risk outcome")
	recs := f.rec.snapshot()
	require.Len(t, recs, 3)
	require.False(t, recs[1].Late, "within the late window")
	_, _, samples, _, _ := f.hs(t, 1)
	require.EqualValues(t, 3, samples, "reports on ended leases inside the window still count")
}

func TestLateReportsAreStatisticsOnly(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	ended := f.now.Add(-11 * time.Minute).UnixMilli()
	lease := f.lease(t, 1, 0, "st", "expired", "end", strconv.FormatInt(ended, 10))
	ev := withOutcome(f.event(lease), policy.OutcomeCaptcha)
	ev.Release = true
	f.process(t, ev)

	rec := f.lastRecord(t)
	require.True(t, rec.Late)
	require.False(t, rec.Suppressed)
	require.Equal(t, policy.OutcomeCaptcha, rec.Outcome)
	require.Empty(t, f.exec.snapshot())
	require.Empty(t, f.rel.calls)
	require.Empty(t, f.hget(t, f.keys.Health(testSiteKey, testGroupKey), "1"))
	n, err := f.rdb.Do(context.Background(), f.rdb.B().Exists().Key(f.keys.CrossIdentity(testSiteKey, 1)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, "1", f.hget(t, f.keys.Lease(testSiteKey, lease), "rc"))
}

func TestMissingLeaseRecordsSiteStatistics(t *testing.T) {
	f := newFixture(t)
	gone := idgen.LeasePrefix(testSiteKey) + idgen.FormatShard(2)
	ev := withOutcome(f.event(gone), policy.OutcomeRateLimited)
	ev.Release = true
	id := f.process(t, ev)
	f.processWithID(t, ev, id) // redelivery

	recs := f.rec.snapshot()
	require.Len(t, recs, 1)
	rec := recs[0]
	require.Equal(t, policy.OutcomeRateLimited, rec.Outcome)
	require.Empty(t, rec.EndpointGroupID)
	require.Empty(t, rec.IdentityID)
	require.Equal(t, "sit_1", rec.SiteID)
	require.Empty(t, f.exec.snapshot())
	require.Empty(t, f.rel.calls)
	require.Empty(t, f.brk.calls)
	require.Equal(t, id, f.hget(t, f.keys.Checkpoint(testSiteKey), "2"))
	require.InDelta(t, 1, counterValue(t, f.metrics.ReportTotal.WithLabelValues("shop", "_", policy.OutcomeRateLimited)), 0)
}

func TestUnknownEndpointGroupRecordsStatistics(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 3, "e", "999")
	f.process(t, withOutcome(f.event(lease), policy.OutcomeSuccess))
	rec := f.lastRecord(t)
	require.Empty(t, rec.EndpointGroupID)
	require.Equal(t, "idt_1", rec.IdentityID)
	require.Equal(t, "pxy_3", rec.ProxyID)
	require.Equal(t, policy.OutcomeSuccess, rec.Outcome)
	require.Empty(t, f.hget(t, f.keys.Health(testSiteKey, 999), "1"))
}

func TestDroppedReports(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	otherNS := f.lease(t, 1, 0, "ns", "ns_other")
	good := f.lease(t, 1, 0)
	tests := []struct {
		name string
		ev   signal.Event
	}{
		{name: "malformed lease id", ev: f.event("lse_bad")},
		{name: "unknown site", ev: f.event(idgen.LeasePrefix(777) + "00")},
		{name: "event namespace differs from site namespace", ev: func() signal.Event {
			ev := f.event(good)
			ev.NamespaceID = "ns_other"
			return ev
		}()},
		{name: "lease namespace differs", ev: f.event(otherNS)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.w.processEvent(context.Background(), 0, f.streamID(), tt.ev)
			require.Empty(t, f.rec.snapshot())
		})
	}
}

func TestProcessEntryDecoding(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	data, err := signal.EncodeEvent(f.event(lease))
	require.NoError(t, err)
	ctx := context.Background()

	f.w.processEntry(ctx, 1, rueidis.XRangeEntry{ID: f.streamID()})
	f.w.processEntry(ctx, 1, rueidis.XRangeEntry{ID: f.streamID(), FieldValues: map[string]string{"v": "2", "d": string(data)}})
	f.w.processEntry(ctx, 1, rueidis.XRangeEntry{ID: f.streamID(), FieldValues: map[string]string{"v": "1", "d": "{"}})
	require.Empty(t, f.rec.snapshot())

	f.w.processEntry(ctx, 1, rueidis.XRangeEntry{ID: f.streamID(), FieldValues: map[string]string{"v": "1", "d": string(data)}})
	require.Len(t, f.rec.snapshot(), 1)
	require.InDelta(t, 1, counterValue(t, f.metrics.ReportProcessDuration), 0)
}

func TestExecutorRetries(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	f.exec.idempotent = true

	f.exec.fails = 2
	f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
	calls := f.exec.snapshot()
	require.Len(t, calls, 3, "two transient failures then success")
	for _, c := range calls[1:] {
		require.Equal(t, calls[0].LeaseID, c.LeaseID, "retries carry the same report identity")
		require.Equal(t, calls[0].ReportID, c.ReportID)
		require.Equal(t, calls[0].Now, c.Now, "retries evaluate at the same time")
		require.Equal(t, calls[0].Planned, c.Planned)
	}

	f.exec.err = errors.New("permanent failure")
	ev := withOutcome(f.event(lease), policy.OutcomeRateLimited)
	ev.Release = true
	f.process(t, ev)
	require.Len(t, f.exec.snapshot(), 3+1+len(retryBackoff), "gives up after the retries")
	require.Len(t, f.rel.calls, 1, "the pipeline continues after an executor failure")
	require.Len(t, f.rec.snapshot(), 2)
}

func TestRetryStopsOnContextDone(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := f.w.retry(ctx, func(context.Context) error {
		attempts++
		cancel()
		return errors.New("boom")
	})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
	require.False(t, sleepCtx(ctx, time.Hour))
}

func TestRedisFailureRecordsStatistics(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	// A lease hash with a wrong type makes HMGET fail.
	bad := idgen.LeasePrefix(testSiteKey) + "01"
	require.NoError(t, f.rdb.Do(context.Background(), f.rdb.B().Set().Key(f.keys.Lease(testSiteKey, bad)).Value("x").Build()).Error())
	f.process(t, f.event(bad))
	rec := f.lastRecord(t)
	require.Equal(t, policy.OutcomeSuccess, rec.Outcome)
	require.Empty(t, rec.EndpointGroupID)

	// An hs field of the wrong type makes observe.lua fail.
	require.NoError(t, f.rdb.Do(context.Background(), f.rdb.B().Set().Key(f.keys.Health(testSiteKey, testGroupKey)).Value("x").Build()).Error())
	f.process(t, withOutcome(f.event(lease), policy.OutcomeRateLimited))
	rec = f.lastRecord(t)
	require.Equal(t, policy.OutcomeRateLimited, rec.Outcome)
	require.Equal(t, "eg_search", rec.EndpointGroupID)
	require.Empty(t, f.exec.snapshot(), "no actions without observed state")
}

func TestFallbackPolicies(t *testing.T) {
	f := newFixture(t)
	f.group.Signal = nil
	f.group.Action = nil
	f.identity(t, 1)
	f.process(t, withOutcome(f.event(f.lease(t, 1, 0)), policy.OutcomeRateLimited))
	rec := f.lastRecord(t)
	require.Equal(t, policy.OutcomeRateLimited, rec.Outcome, "built-in signal policy")
	require.Len(t, f.exec.snapshot(), 1, "built-in action policy")

	f.w.fallbackAction = nil
	f.process(t, withOutcome(f.event(f.lease(t, 1, 0)), policy.OutcomeRateLimited))
	require.Len(t, f.exec.snapshot(), 1)
	require.Len(t, f.rec.snapshot(), 2)
}

// A failed Execute call may have applied its actions in Redis before failing
// (for example a client-side timeout). Calling a non-idempotent executor again
// would report them as already applied and lose their state changes, so it is
// called only once.
func TestExecutorWithUnknownOutcomeIsNotRetried(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)

	f.exec.fails = 1
	ev := withOutcome(f.event(lease), policy.OutcomeRateLimited)
	ev.Release = true
	f.process(t, ev)
	require.Len(t, f.exec.snapshot(), 1, "a non-idempotent executor is not called again")
	require.Len(t, f.rel.calls, 1, "the pipeline continues")
	require.Len(t, f.rec.snapshot(), 1)
}

// lateCatalog is a catalog whose namespace snapshot appears only on Reload,
// like an instance that has not yet received a newly created site.
type lateCatalog struct {
	*catalogtest.Catalog
	ns *catalog.Namespace

	mu      sync.Mutex
	reloads int
}

func (c *lateCatalog) Reload(ctx context.Context, namespaceID string) error {
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
	c.Put(c.ns)
	return c.Catalog.Reload(ctx, namespaceID)
}

func (c *lateCatalog) reloadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reloads
}

func TestReportOfUnknownSiteReloadsCatalogBeforeDropping(t *testing.T) {
	f := newFixture(t)
	f.identity(t, 1)
	lease := f.lease(t, 1, 0)
	late := &lateCatalog{Catalog: f.cat, ns: f.ns}
	w := f.newWorkerWithCatalog("inst-late", late)

	// The site is missing from this instance's snapshot: the report must not be
	// dropped, the namespace is reloaded and the report is processed.
	f.cat.Remove(f.ns.ID)
	w.processEvent(context.Background(), 0, f.streamID(), withOutcome(f.event(lease), policy.OutcomeSuccess))
	require.Equal(t, 1, late.reloadCount())
	require.Len(t, f.rec.snapshot(), 1)

	// Reloads are rate limited, so reports for a site that is really gone cannot
	// hammer PostgreSQL.
	f.cat.Remove(f.ns.ID)
	w.processEvent(context.Background(), 0, f.streamID(), f.event(lease))
	require.Equal(t, 1, late.reloadCount())
	require.Len(t, f.rec.snapshot(), 1)

	// After the interval a new report reloads again.
	f.now = f.now.Add(catalogRefreshInterval + time.Second)
	w.processEvent(context.Background(), 0, f.streamID(), withOutcome(f.event(lease), policy.OutcomeSuccess))
	require.Equal(t, 2, late.reloadCount())
	require.Len(t, f.rec.snapshot(), 2)
}
