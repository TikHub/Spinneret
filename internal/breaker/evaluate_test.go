package breaker

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/breaker/breakertest"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/jobs"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

func newService(t *testing.T, env *breakertest.Env, mutate ...func(*Config)) *Service {
	t.Helper()
	cfg := Config{Now: env.Clock.Now}
	for _, m := range mutate {
		m(&cfg)
	}
	return New(cfg, env.Pool, env.Redis, env.Keys, env.Catalog, env.Bus, env.Audit, env.Metrics, nil)
}

// metricValue reads the value of a gauge or counter.
func metricValue(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	require.NoError(t, m.Write(&out))
	if out.Gauge != nil {
		return out.Gauge.GetValue()
	}
	require.NotNil(t, out.Counter)
	return out.Counter.GetValue()
}

func TestConfigDefaultsAndJob(t *testing.T) {
	t.Parallel()
	cfg := Config{}.withDefaults()
	require.Equal(t, DefaultEvalInterval, cfg.EvalInterval)
	require.Equal(t, DefaultActiveWindow, cfg.ActiveWindow)
	require.Equal(t, DefaultNotifyMinInterval, cfg.NotifyMinInterval)
	require.Equal(t, DefaultLockTTL, cfg.LockTTL)
	require.Equal(t, DefaultNotifyBuffer, cfg.NotifyBuffer)
	require.Equal(t, DefaultConcurrency, cfg.Concurrency)
	require.Equal(t, DefaultIOTimeout, cfg.IOTimeout)
	require.NotNil(t, cfg.Now)

	s := New(Config{}, nil, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil, nil)
	job := s.EvaluateJob()
	require.NoError(t, job.Validate())
	require.Equal(t, EvaluateJobName, job.Name)
	require.Equal(t, 5*time.Second, job.Interval)
	require.Equal(t, jobs.EachInstance, job.Mode)
}

func TestParamsFor(t *testing.T) {
	t.Parallel()
	def := paramsFor(nil)
	require.Equal(t, int64(5000), def.BucketMs)
	require.Equal(t, 12, def.Buckets)
	require.Equal(t, 50, def.MinRequests)
	require.Equal(t, 0.4, def.RiskRatioGte)
	require.Equal(t, 10, def.CaptchaGte)
	require.Equal(t, 0.2, def.SuccessRatioLte)
	require.Equal(t, int64(120_000), def.OpenMs)
	require.Equal(t, int64(3_600_000), def.MaxOpenMs)
	require.Equal(t, int64(1_800_000), def.ResetAfterMs)
	require.Equal(t, 5, def.CloseMinSamples)
	require.Equal(t, 0.8, def.CloseSuccessRatioGte)
	require.True(t, def.Enabled)
	require.Equal(t, policy.RevertEndpoint, def.Revert)

	broken := &policy.BreakerSpec{Buckets: 0, MaxOpenDuration: 1000_000_000, OpenDuration: 5000_000_000, RevertRecentCooldowns: "bogus"}
	p := paramsFor(broken)
	require.Equal(t, 12, p.Buckets)
	require.Equal(t, 50, p.MinRequests)
	require.Equal(t, int64(5000), p.OpenMs)
	require.Equal(t, int64(5000), p.MaxOpenMs, "max is raised to the open duration")
	require.Equal(t, 5, p.CloseMinSamples)
	require.Equal(t, 0.8, p.CloseSuccessRatioGte)
	require.False(t, p.Enabled)
	require.Equal(t, policy.RevertEndpoint, p.Revert)
}

func TestSweepTransitions(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	g := env.Search

	// A cooldown applied inside the window, reverted when the breaker opens.
	cd := now.Add(20 * time.Minute).UnixMilli()
	hsKey := env.Keys.Health(g.SiteKey, g.Key)
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Hset().Key(hsKey).FieldValue().
		FieldValue("9", "70.00|0|3|2|0|"+strconv.FormatInt(cd, 10)+"|0|0").Build()).Error())
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Zadd().Key(env.Keys.RecentCooldowns(g.SiteKey, g.Key)).ScoreMember().
		ScoreMember(float64(now.Add(-time.Second).UnixMilli()), "9|0|1").Build()).Error())

	env.WriteBucket(t, g, now, 5000, 60, 10, 50, 1, 2)
	env.MarkActive(t, g, now)
	// A quiet active group and an active group unknown to the catalog.
	env.MarkActive(t, env.DefaultA, now)
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Zadd().Key(env.Keys.ActiveGroups(env.SiteA.Key)).ScoreMember().
		ScoreMember(float64(now.UnixMilli()), "999999").Build()).Error())

	require.NoError(t, svc.Sweep(ctx))

	h := env.Breaker(t, g)
	require.Equal(t, "open", h["st"])
	require.Equal(t, strconv.FormatInt(g.Key, 10), env.OpenMembers(t, env.SiteA)[0])
	require.Empty(t, env.Breaker(t, env.DefaultA))

	rows := env.BreakerEvents(t)
	require.Len(t, rows, 1)
	require.Equal(t, StateClosed, rows[0].From)
	require.Equal(t, StateOpen, rows[0].To)
	require.Equal(t, TriggerAuto, rows[0].Trigger)
	require.Equal(t, actorSystem, rows[0].Actor)
	require.Equal(t, g.ID, rows[0].EndpointGroupID)
	require.NotNil(t, rows[0].OpenUntil)
	require.Equal(t, now.Add(2*time.Minute).UnixMilli(), rows[0].OpenUntil.UnixMilli())
	require.EqualValues(t, 60, rows[0].Metrics["total"])
	require.EqualValues(t, 2, rows[0].Metrics["captcha_identities"])
	require.EqualValues(t, 1, rows[0].Metrics["reverted_endpoint_cooldowns"])

	packed, err := env.Redis.Do(ctx, env.Redis.B().Hget().Key(hsKey).Field("9").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "70.00|0|3|1|0|0|0|0", packed)

	require.Equal(t, int64(1), env.RuntimeVersion(t, KindBreakers))
	nsEvents := env.Events.On(events.NamespaceChannel(breakertest.NamespaceID))
	require.Len(t, nsEvents, 1)
	require.Equal(t, events.TypeBreakerTransition, nsEvents[0].Event.Type)
	require.Equal(t, env.SiteA.ID, nsEvents[0].Event.SiteID)
	data := breakertest.Decode[TransitionData](t, nsEvents[0])
	require.Equal(t, "alpha", data.Site)
	require.Equal(t, "search", data.EndpointGroup)
	require.Equal(t, "web", data.Client)
	require.Equal(t, StateClosed, data.From)
	require.Equal(t, StateOpen, data.To)
	require.Equal(t, int64(1), data.ConsecutiveOpens)
	require.NotNil(t, data.OpenUntil)
	require.Contains(t, data.Reason, "risk_ratio")
	require.Equal(t, 0.8333, data.Metrics.RiskRatio)
	runtimeEvents := env.Events.On(events.ChannelRuntime)
	require.Len(t, runtimeEvents, 1)
	require.Equal(t, RuntimeEventData{Namespace: breakertest.NamespaceID, Kind: KindBreakers, Version: 1},
		breakertest.Decode[RuntimeEventData](t, runtimeEvents[0]))
	require.Equal(t, 1.0, metricValue(t, env.Metrics.BreakerTransitions.WithLabelValues("alpha", "search", StateOpen)))
	require.Equal(t, 2.0, metricValue(t, env.Metrics.BreakerState.WithLabelValues("alpha", "search")))

	// Still open: the non-closed breaker is a candidate but nothing changes.
	env.Clock.Advance(time.Minute)
	require.NoError(t, svc.Sweep(ctx))
	require.Len(t, env.BreakerEvents(t), 1)

	// The open period ends: half-open, without reverting anything.
	env.Clock.Set(now.Add(2 * time.Minute))
	require.NoError(t, svc.Sweep(ctx))
	require.Equal(t, "half_open", env.Breaker(t, g)["st"])
	require.Equal(t, 1.0, metricValue(t, env.Metrics.BreakerState.WithLabelValues("alpha", "search")))

	// Probes succeed: closed.
	env.SetBreaker(t, g, map[string]string{"ps": "5", "pk": "5"})
	env.Clock.Advance(10 * time.Second)
	require.NoError(t, svc.Sweep(ctx))
	require.Equal(t, "closed", env.Breaker(t, g)["st"])
	require.Empty(t, env.OpenMembers(t, env.SiteA))

	rows = env.BreakerEvents(t)
	require.Len(t, rows, 3)
	require.Equal(t, []string{StateHalfOpen, StateClosed}, []string{rows[1].To, rows[2].To})
	require.Equal(t, []string{TriggerAuto, TriggerProbe}, []string{rows[1].Trigger, rows[2].Trigger})
	require.Nil(t, rows[1].OpenUntil)
	require.EqualValues(t, 5, rows[2].Metrics["probe_samples"])
	require.Equal(t, int64(3), env.RuntimeVersion(t, KindBreakers))
	require.Len(t, env.Events.On(events.NamespaceChannel(breakertest.NamespaceID)), 3)
}

func TestSweepRecordsLazyHalfOpen(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	g := env.DefaultB
	ms := func(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

	// acquire.lua half-opened the breaker without clearing ou.
	halfOpenedAt := now.Add(-3 * time.Second)
	env.SetBreaker(t, g, map[string]string{
		"st": "half_open", "ou": ms(now.Add(-4 * time.Second)), "oc": "1", "man": "0",
		"hw": ms(halfOpenedAt), "hc": "1", "ps": "5", "pk": "5", "rsn": "risk_ratio 0.90 >= 0.40", "v": "2",
	})
	require.NoError(t, svc.Sweep(ctx))
	rows := env.BreakerEvents(t)
	require.Len(t, rows, 1)
	require.Equal(t, StateOpen, rows[0].From)
	require.Equal(t, StateHalfOpen, rows[0].To)
	require.Equal(t, TriggerAuto, rows[0].Trigger)
	require.Equal(t, "risk_ratio 0.90 >= 0.40", rows[0].Reason)
	require.Equal(t, "half_open", env.Breaker(t, g)["st"])
	require.Equal(t, "0", env.Breaker(t, g)["ou"])
	require.Equal(t, int64(1), env.RuntimeVersion(t, KindBreakers))
	var createdAt time.Time
	require.NoError(t, env.Pool.QueryRow(ctx, `SELECT created_at FROM breaker_events WHERE id = $1`, rows[0].ID).Scan(&createdAt))
	require.Equal(t, halfOpenedAt, createdAt.UTC())

	// The next sweep evaluates the probes.
	require.NoError(t, svc.Sweep(ctx))
	rows = env.BreakerEvents(t)
	require.Len(t, rows, 2)
	require.Equal(t, StateClosed, rows[1].To)
	require.Equal(t, TriggerProbe, rows[1].Trigger)
}

func TestManualCloseRecordsLazyHalfOpen(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	g := env.Search
	env.SetBreaker(t, g, map[string]string{
		"st": "half_open", "ou": strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10), "man": "1",
		"hw": strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10), "rsn": "maintenance", "v": "7",
	})
	st, err := svc.Close(ctx, breakertest.User("usr_op", "operator"), g.ID, "all good")
	require.NoError(t, err)
	require.Equal(t, StateClosed, st.State)
	rows := env.BreakerEvents(t)
	require.Len(t, rows, 2)
	require.Equal(t, []string{StateHalfOpen, StateClosed}, []string{rows[0].To, rows[1].To})
	require.Equal(t, []string{TriggerAuto, TriggerManual}, []string{rows[0].Trigger, rows[1].Trigger})
	require.Equal(t, []string{"maintenance", "all good"}, []string{rows[0].Reason, rows[1].Reason})
	nsEvents := env.Events.On(events.NamespaceChannel(breakertest.NamespaceID))
	require.Len(t, nsEvents, 2)
	lazy := breakertest.Decode[TransitionData](t, nsEvents[0])
	require.True(t, lazy.Manual)
	require.Equal(t, actorSystem, lazy.Actor)
	require.Equal(t, int64(2), env.RuntimeVersion(t, KindBreakers))
}

func TestSweepRevertModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode             policy.RevertMode
		wantEndpoint     bool
		wantSiteReverted bool
	}{
		{mode: policy.RevertNone},
		{mode: policy.RevertEndpoint, wantEndpoint: true},
		{mode: policy.RevertAll, wantEndpoint: true, wantSiteReverted: true},
	}
	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			env := breakertest.New(t)
			spec := *defaultSpec()
			spec.RevertRecentCooldowns = tc.mode
			env.Search.Breaker = &spec
			svc := newService(t, env)
			ctx := breakertest.Context(t)
			now := env.Clock.Now()
			g := env.Search
			cd := strconv.FormatInt(now.Add(time.Hour).UnixMilli(), 10)

			require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Hset().Key(env.Keys.Health(g.SiteKey, g.Key)).FieldValue().
				FieldValue("5", "70.00|0|3|2|0|"+cd+"|0|0").Build()).Error())
			require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Zadd().Key(env.Keys.RecentCooldowns(g.SiteKey, g.Key)).ScoreMember().
				ScoreMember(float64(now.UnixMilli()), "5|0|1").Build()).Error())
			require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Hset().Key(env.Keys.Identity(g.SiteKey, 5)).FieldValue().
				FieldValue("scd", cd).Build()).Error())
			require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Zadd().Key(env.Keys.RecentSiteCooldowns(g.SiteKey)).ScoreMember().
				ScoreMember(float64(now.UnixMilli()), "5|0").Build()).Error())
			for _, eg := range []int64{g.Key, env.DefaultA.Key} {
				require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Zadd().Key(env.Keys.Ready(g.SiteKey, eg)).ScoreMember().
					ScoreMember(float64(now.Add(time.Hour).UnixMilli()), "5").Build()).Error())
			}

			env.WriteBucket(t, g, now, 5000, 50, 0, 50)
			svc.NotifyRisk(g.SiteKey, g.Key) // queued only; Sweep evaluates via aeg
			env.MarkActive(t, g, now)
			require.NoError(t, svc.Sweep(ctx))
			require.Equal(t, "open", env.Breaker(t, g)["st"])

			packed, err := env.Redis.Do(ctx, env.Redis.B().Hget().Key(env.Keys.Health(g.SiteKey, g.Key)).Field("5").Build()).ToString()
			require.NoError(t, err)
			scd, err := env.Redis.Do(ctx, env.Redis.B().Hget().Key(env.Keys.Identity(g.SiteKey, 5)).Field("scd").Build()).ToString()
			require.NoError(t, err)
			otherScore, err := env.Redis.Do(ctx, env.Redis.B().Zscore().Key(env.Keys.Ready(g.SiteKey, env.DefaultA.Key)).Member("5").Build()).AsFloat64()
			require.NoError(t, err)
			if tc.wantEndpoint {
				require.Equal(t, "70.00|0|3|1|0|0|0|0", packed)
			} else {
				require.Equal(t, "70.00|0|3|2|0|"+cd+"|0|0", packed)
			}
			if tc.wantSiteReverted {
				require.Equal(t, "0", scd)
				require.Zero(t, otherScore)
			} else {
				require.Equal(t, cd, scd)
				require.Equal(t, float64(now.Add(time.Hour).UnixMilli()), otherScore)
			}
		})
	}
}

func TestSweepSkipsLockedAndDisabled(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()

	// Locked by another instance.
	env.WriteBucket(t, env.Search, now, 5000, 50, 0, 50)
	env.MarkActive(t, env.Search, now)
	lockKey := env.Keys.SiteLock(env.SiteA.Key, "brk:"+strconv.FormatInt(env.Search.Key, 10))
	ok, err := redis.TryLock(ctx, env.Redis, lockKey, "other-instance", time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	// Disabled policy.
	disabled := *defaultSpec()
	disabled.Enabled = false
	env.DefaultB.Breaker = &disabled
	env.WriteBucket(t, env.DefaultB, now, 5000, 50, 0, 50)
	env.MarkActive(t, env.DefaultB, now)

	// Activity older than the active window.
	env.WriteBucket(t, env.AppDefaultA, now, 5000, 50, 0, 50)
	env.MarkActive(t, env.AppDefaultA, now.Add(-3*time.Minute))

	require.NoError(t, svc.Sweep(ctx))
	require.Empty(t, env.Breaker(t, env.Search))
	require.Empty(t, env.Breaker(t, env.DefaultB))
	require.Empty(t, env.Breaker(t, env.AppDefaultA))
	require.Empty(t, env.BreakerEvents(t))

	require.NoError(t, redis.Unlock(ctx, env.Redis, lockKey, "other-instance"))
	require.NoError(t, svc.Sweep(ctx))
	require.Equal(t, "open", env.Breaker(t, env.Search)["st"])
	require.Empty(t, env.Breaker(t, env.DefaultB))
}

func TestSweepCanceledContext(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, svc.Sweep(ctx))
}

func TestNotifyRiskRun(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env, func(c *Config) { c.Now = time.Now })
	now := time.Now()
	g := env.Search
	env.WriteBucket(t, g, now, 5000, 50, 0, 50)
	env.WriteBucket(t, env.DefaultB, now, 5000, 50, 0, 50)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	for range 100 {
		svc.NotifyRisk(g.SiteKey, g.Key)
	}
	svc.NotifyRisk(999, 1)                // unknown site
	svc.NotifyRisk(env.SiteB.Key, 424242) // unknown group
	svc.NotifyRisk(env.DefaultB.SiteKey, env.DefaultB.Key)
	require.Eventually(t, func() bool {
		return env.Breaker(t, g)["st"] == "open" && env.Breaker(t, env.DefaultB)["st"] == "open"
	}, 10*time.Second, 50*time.Millisecond)

	// A notification inside the rate-limit interval is deferred, not lost:
	// close manually, add data and notify again.
	env.SetBreaker(t, g, map[string]string{"st": "closed", "lc": strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)})
	svc.NotifyRisk(g.SiteKey, g.Key)
	require.Eventually(t, func() bool { return env.Breaker(t, g)["st"] == "open" }, 10*time.Second, 50*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestNotifyRiskNeverBlocks(t *testing.T) {
	t.Parallel()
	svc := New(Config{NotifyBuffer: 2}, nil, nil, redis.NewKeys(""), catalogtest.New(), nil, nil, nil, nil)
	finished := make(chan struct{})
	go func() {
		for i := range 1000 {
			svc.NotifyRisk(1, int64(i))
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("NotifyRisk blocked")
	}
	require.Len(t, svc.notify, 2)
	svc.pendingMu.Lock()
	require.Len(t, svc.pending, 2)
	svc.pendingMu.Unlock()
}
