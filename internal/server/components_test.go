package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/appconfig"
	"github.com/Evil0ctal/Spinneret/internal/breaker"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// recordingGroup records the loops started in a tier.
type recordingGroup struct{ names []string }

func (g *recordingGroup) Go(name string, _ func(context.Context) error) {
	g.names = append(g.names, name)
}

// Acquire on any instance may move a breaker from open to half_open; the
// transition hook and the breaker's notification loop must therefore be
// present on api-only instances too, not only on worker instances.
func TestBreakerHalfOpenHookOnEveryRole(t *testing.T) {
	for _, role := range []appconfig.Role{appconfig.RoleAPI, appconfig.RoleWorker, appconfig.RoleAll} {
		t.Run(string(role), func(t *testing.T) {
			c := &components{
				bus:     events.NewMemoryBus(),
				breaker: breaker.New(breaker.Config{}, nil, nil, redis.NewKeys("t"), nil, nil, nil, nil, nil),
			}
			cfg := appconfig.Config{Role: role, ReportShards: 4}
			sched := c.schedulerConfig(cfg)
			require.NotNil(t, sched.OnBreakerHalfOpen, "acquire reports lazy half-open transitions")
			require.NotNil(t, sched.OnProxyBound)
			require.Equal(t, 4, sched.ReportShards)
			sched.OnBreakerHalfOpen(7, 7000) // non-blocking

			services, sinks, ch := &recordingGroup{}, &recordingGroup{}, &recordingGroup{}
			c.startLoops(role, services, sinks, ch, func(context.Context) error { return nil })
			require.Contains(t, services.names, "breaker", "the breaker notification loop runs on %s instances", role)
			if role.RunsWorkers() {
				require.Contains(t, services.names, "worker_startup")
			} else {
				require.NotContains(t, services.names, "worker_startup")
			}
		})
	}
}
