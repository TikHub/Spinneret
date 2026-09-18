package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/observability"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// peerInstance is one built instance: the components plus the registry it
// exports its gauges through, so a test can read what an operator would scrape.
type peerInstance struct {
	c       *components
	metrics *observability.Metrics
}

// buildPeerInstance builds one instance's components against a shared Redis and
// no PostgreSQL. Construction has no side effects, so a nil pool is enough to
// exercise the acquire admission wiring end to end. Every instance of one test
// shares rdb and keys, which is what makes them peers.
func buildPeerInstance(t *testing.T, rdb rueidis.Client, keys redis.Keys, id string, tune func(*appconfig.Config)) peerInstance {
	t.Helper()
	cfg := appconfig.Config{
		Role:                 appconfig.RoleAPI,
		InstanceID:           id,
		ReportShards:         4,
		AcquireFleetInflight: 64,
	}
	if tune != nil {
		tune(&cfg)
	}
	metrics := observability.NewMetrics()
	c, err := buildComponents(cfg, &infra{rdb: rdb, keys: keys}, metrics, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return peerInstance{c: c, metrics: metrics}
}

// gauge returns one gauge value of an instance's registry, and whether the
// series exists at all.
func (p peerInstance) gauge(t *testing.T, name string) (float64, bool) {
	t.Helper()
	families, err := p.metrics.Registry.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		require.Equal(t, dto.MetricType_GAUGE, f.GetType(), name)
		require.Len(t, f.GetMetric(), 1, name)
		return f.GetMetric()[0].GetGauge().GetValue(), true
	}
	return 0, false
}

// The registry, the OnLive hook and the gate are wired together only in
// buildComponents, and each half is otherwise tested in isolation. This is the
// test that two API instances sharing one Redis really do end up with half the
// fleet budget each.
func TestAcquirePeersDividesBudgetAcrossInstances(t *testing.T) {
	rdb, keys := testutil.Redis(t)
	a := buildPeerInstance(t, rdb, keys, "api-a", nil)
	b := buildPeerInstance(t, rdb, keys, "api-b", nil)
	require.NotNil(t, a.c.peers, "a derived limit needs a registry to divide by")
	require.NotNil(t, b.c.peers)

	ctx := context.Background()
	live, err := a.c.peers.Beat(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, live, "the first instance is alone until the second beats")
	limit, ok := a.gauge(t, "spinneret_acquire_inflight_limit")
	require.True(t, ok)
	require.InDelta(t, 64, limit, 0)

	live, err = b.c.peers.Beat(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, live)
	// The second instance divides immediately; the first divides on its next
	// beat, which is how a scale-up settles within one beat interval.
	live, err = a.c.peers.Beat(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, live)

	for name, in := range map[string]peerInstance{"api-a": a, "api-b": b} {
		limit, ok := in.gauge(t, "spinneret_acquire_inflight_limit")
		require.True(t, ok, name)
		require.InDelta(t, 32, limit, 0, "%s admits half the fleet budget", name)
		peers, ok := in.gauge(t, "spinneret_acquire_peers")
		require.True(t, ok, name)
		require.InDelta(t, 2, peers, 0, name)
		age, ok := in.gauge(t, "spinneret_acquire_peer_beat_age_seconds")
		require.True(t, ok, "%s exports the heartbeat age", name)
		require.GreaterOrEqual(t, age, 0.0)
	}

	// Both members are in the registry under their own ids, which is why the
	// count is 2: a shared instance id would collapse them into one.
	members, err := rdb.Do(ctx, rdb.B().Zrange().Key(keys.Acquirers()).Min("0").Max("-1").Build()).AsStrSlice()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"api-a", "api-b"}, members)
}

// A pinned or disabled limit has nothing to divide, so no registry is built, no
// heartbeat runs and no peer series is exported — an absent series reads as "not
// applicable" where a hardcoded 1 would read as a broken registry.
func TestAcquirePeersRegistryOnlyWhenDerived(t *testing.T) {
	tests := []struct {
		name string
		tune func(*appconfig.Config)
	}{
		{name: "pinned", tune: func(c *appconfig.Config) { c.AcquireMaxInflight = 128 }},
		{name: "disabled", tune: func(c *appconfig.Config) { c.AcquireFleetInflight = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rdb, keys := testutil.Redis(t)
			in := buildPeerInstance(t, rdb, keys, "api-"+tc.name, tc.tune)
			require.Nil(t, in.c.peers)
			_, ok := in.gauge(t, "spinneret_acquire_peers")
			require.False(t, ok)
			_, ok = in.gauge(t, "spinneret_acquire_peer_beat_age_seconds")
			require.False(t, ok)

			services, sinks, ch := &recordingGroup{}, &recordingGroup{}, &recordingGroup{}
			in.c.startLoops(appconfig.RoleAPI, services, sinks, ch, func(context.Context) error { return nil })
			require.NotContains(t, services.names, "acquire_peers")
		})
	}
}

// Acquire only runs on API roles, so only API roles heartbeat: a worker-only
// instance must not appear in the divisor of instances that serve acquires.
func TestAcquirePeersLoopRunsOnAPIRolesOnly(t *testing.T) {
	for _, role := range []appconfig.Role{appconfig.RoleAPI, appconfig.RoleWorker, appconfig.RoleAll} {
		t.Run(string(role), func(t *testing.T) {
			rdb, keys := testutil.Redis(t)
			in := buildPeerInstance(t, rdb, keys, "api-"+string(role), func(c *appconfig.Config) { c.Role = role })
			require.NotNil(t, in.c.peers)

			services, sinks, ch := &recordingGroup{}, &recordingGroup{}, &recordingGroup{}
			in.c.startLoops(role, services, sinks, ch, func(context.Context) error { return nil })
			if role.ServesAPI() {
				require.Contains(t, services.names, "acquire_peers")
			} else {
				require.NotContains(t, services.names, "acquire_peers")
			}
		})
	}
}
