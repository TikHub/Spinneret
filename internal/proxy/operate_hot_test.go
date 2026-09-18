package proxy

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
)

func TestOperateQuarantineUsesBanUntil(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	env.svc.now = fixedClock(now)
	p := env.importLines(t, env.ns, "http://10.5.9.1:80")[0]

	res, err := env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpQuarantine, Duration: durationx.MustParse("30m"), Reason: "suspicious"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	row := env.row(t, p.ID)
	require.Equal(t, StateQuarantined, row.State)
	require.NotNil(t, row.BanUntil)
	require.True(t, now.Add(30*time.Minute).Equal(*row.BanUntil))
	evs := env.stateEvents(t, p.ID)
	require.Len(t, evs, 1)
	require.True(t, row.BanUntil.Equal(*evs[0].Until))

	// The ban/quarantine expiry job of the action track releases proxies
	// whose ban_until passed (ActionReleaseDueProxies).
	var due int
	require.NoError(t, env.pool.QueryRow(ctx, `SELECT count(*) FROM proxies
		WHERE state IN ('banned', 'quarantined') AND ban_until IS NOT NULL AND ban_until <= $1`, now.Add(31*time.Minute)).Scan(&due))
	require.Equal(t, 1, due)

	env.svc.now = fixedClock(now.Add(time.Minute))
	_, err = env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpQuarantine, Duration: durationx.MustParse("2h")})
	require.NoError(t, err)
	row = env.row(t, p.ID)
	require.True(t, now.Add(time.Minute+2*time.Hour).Equal(*row.BanUntil), "re-quarantine replaces the end")
	require.Len(t, env.stateEvents(t, p.ID), 2)

	_, err = env.svc.OperateProxies(ctx, adminUser(), []string{p.ID}, OperationRequest{Operation: OpUnquarantine})
	require.NoError(t, err)
	row = env.row(t, p.ID)
	require.Equal(t, StateActive, row.State)
	require.Nil(t, row.BanUntil)
}

func TestCooldownReadyQueue(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	env.svc.now = fixedClock(now)
	ps := env.importLines(t, env.ns, "http://10.5.8.1:80", "http://10.5.8.2:80", "http://10.5.8.3:80")
	free, saturated, unqueued := ps[0], ps[1], ps[2]
	member := func(p *Proxy) string { return strconv.FormatInt(p.Key, 10) }
	queue := env.keys.ProxyReady(env.alpha.Key)
	zadd := func(p *Proxy, at time.Time) {
		t.Helper()
		require.NoError(t, env.rdb.Do(ctx, env.rdb.B().Zadd().Key(queue).ScoreMember().ScoreMember(float64(at.UnixMilli()), member(p)).Build()).Error())
	}
	score := func(p *Proxy) (float64, bool) {
		t.Helper()
		v, err := env.rdb.Do(ctx, env.rdb.B().Zscore().Key(queue).Member(member(p)).Build()).AsFloat64()
		if rueidis.IsRedisNil(err) {
			return 0, false
		}
		require.NoError(t, err)
		return v, true
	}
	ms := func(t time.Time) float64 { return float64(t.UnixMilli()) }
	cooldown := func(d string, site string, ids ...string) {
		t.Helper()
		res, err := env.svc.OperateProxies(ctx, adminUser(), ids, OperationRequest{Operation: OpCooldown, Site: site, Duration: durationx.MustParse(d)})
		require.NoError(t, err)
		require.Equal(t, len(ids), res.Succeeded)
	}

	env.materialize(t, env.alpha, free, "mc", "2", "al", "0")
	env.materialize(t, env.alpha, saturated, "mc", "2", "al", "2")
	env.materialize(t, env.alpha, unqueued, "mc", "2")
	zadd(free, now)
	zadd(saturated, now)

	cooldown("1h", "alpha", free.ID, saturated.ID, unqueued.ID)
	for _, p := range []*Proxy{free, saturated} {
		got, ok := score(p)
		require.True(t, ok)
		require.Equal(t, ms(now.Add(time.Hour)), got, "a longer cooldown pushes the queue score")
	}
	_, ok := score(unqueued)
	require.False(t, ok, "members are never added")

	// acquire pushed the saturated proxy beyond its cooldown.
	zadd(saturated, now.Add(2*time.Hour))
	cooldown("10m", "alpha", free.ID, saturated.ID)
	got, _ := score(free)
	require.Equal(t, ms(now.Add(10*time.Minute)), got, "a shorter cooldown pulls a free proxy back")
	got, _ = score(saturated)
	require.Equal(t, ms(now.Add(2*time.Hour)), got, "a saturated proxy keeps its later score")
	require.Equal(t, strconv.FormatInt(now.Add(10*time.Minute).UnixMilli(), 10), env.hget(t, env.keys.ProxySite(env.alpha.Key, free.Key), "cd"))

	// A global cooldown shorter than the site cooldown keeps max(cd, gcd).
	cooldown("5m", "", free.ID)
	got, _ = score(free)
	require.Equal(t, ms(now.Add(10*time.Minute)), got)
	require.Equal(t, strconv.FormatInt(now.Add(5*time.Minute).UnixMilli(), 10), env.hget(t, env.keys.ProxySite(env.alpha.Key, free.Key), "gcd"))
	requirePartialHash(t, env, env.beta, free)
}

// requirePartialHash checks the hash a global cooldown leaves on a site where
// the proxy was not materialized: only pid and gcd, never in the ready queue.
func requirePartialHash(t *testing.T, env *testEnv, site *catalog.Site, p *Proxy) {
	t.Helper()
	ctx := context.Background()
	fields, err := env.rdb.Do(ctx, env.rdb.B().Hgetall().Key(env.keys.ProxySite(site.Key, p.Key)).Build()).AsStrMap()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"pid", "gcd"}, keysOf(fields))
	require.Equal(t, p.ID, fields["pid"])
	_, err = env.rdb.Do(ctx, env.rdb.B().Zscore().Key(env.keys.ProxyReady(site.Key)).Member(strconv.FormatInt(p.Key, 10)).Build()).AsFloat64()
	require.True(t, rueidis.IsRedisNil(err))
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A manual re-quarantine changes the lifecycle fields (the end of the
// quarantine), so it records a state change time: the hot-state
// synchronization applies PostgreSQL lifecycle fields only when they are at
// least as new as the Redis-first ones.
func TestReQuarantineRecordsStateChangeTime(t *testing.T) {
	changed := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	now := changed.Add(time.Hour)
	until := changed.Add(30 * time.Minute)
	row := proxydb.Proxy{ID: "pxy_1", State: StateQuarantined, StateChangedAt: changed, BanUntil: &until}
	plan, msg := planOperation(row, OperationRequest{Operation: OpQuarantine, Duration: durationx.MustParse("2h")}, false, now)
	require.Empty(t, msg)
	require.True(t, plan.write)
	require.True(t, now.Equal(plan.params.StateChangedAt))
	require.True(t, now.Add(2*time.Hour).Equal(*plan.params.BanUntil))

	// A cooldown does not touch the lifecycle fields.
	row.State = StateActive
	plan, msg = planOperation(row, OperationRequest{Operation: OpCooldown, Duration: durationx.MustParse("5m")}, false, now)
	require.Empty(t, msg)
	require.True(t, changed.Equal(plan.params.StateChangedAt))
}
