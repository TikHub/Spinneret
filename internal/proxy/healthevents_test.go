package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

// lastProxyEvent returns the data of the last proxy.state event about id.
func (e *testEnv) lastProxyEvent(t *testing.T, id string) (StateEventData, bool) {
	t.Helper()
	var out StateEventData
	found := false
	for _, ev := range e.events.snapshot() {
		if ev.Type != events.TypeProxyState {
			continue
		}
		var data StateEventData
		require.NoError(t, json.Unmarshal(ev.Data, &data))
		if data.SubjectID == id {
			out, found = data, true
		}
	}
	return out, found
}

func TestHealthCheckEvents(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	target := newCheckTarget(t, false)
	socks := newTestSOCKS5(t, "", "")
	h := env.newChecker(HealthConfig{CheckURL: target.URL + "/check", ExitIPURL: target.URL + "/ip", GeoIPDB: writeTestGeoDB(t),
		Timeout: 2 * time.Second}, fakeMembership{total: 1})
	defer func() { require.NoError(t, h.Close()) }()

	t.Run("region filled without a state change publishes update", func(t *testing.T) {
		p := env.importLines(t, env.ns, "socks5://"+socks.addr())[0]
		r := NewResolver(env.pool, env.cipher, env.logger)
		unsubscribe := r.Subscribe(env.bus)
		defer unsubscribe()
		a, err := r.Resolve(ctx, env.ns.ID, p.ID, "idt_1", "lse_1")
		require.NoError(t, err)
		require.Empty(t, a.Region)
		env.hot.reset()

		res, err := h.CheckProxy(ctx, adminUser(), p.ID)
		require.NoError(t, err)
		require.True(t, res.OK, res.Error)
		row := env.row(t, p.ID)
		require.Equal(t, StateActive, row.State)
		require.Equal(t, "JP", row.Attributes.Region)
		require.Equal(t, []string{p.ID}, env.hot.syncedIDs())
		ev, ok := env.lastProxyEvent(t, p.ID)
		require.True(t, ok)
		require.Equal(t, StateEventData{SubjectKind: "proxy", SubjectID: p.ID, From: StateActive, To: StateActive, Action: "update"}, ev)
		require.Empty(t, env.stateEvents(t, p.ID), "attribute changes are not lifecycle transitions")

		a, err = r.Resolve(ctx, env.ns.ID, p.ID, "idt_1", "lse_1")
		require.NoError(t, err)
		require.Equal(t, "JP", a.Region, "subscribed resolvers drop the cached assignment")
	})

	t.Run("scheduled results of proxies no longer due are dropped", func(t *testing.T) {
		p := env.importLines(t, env.ns, "http://10.44.0.1:80")[0]
		runAt := time.Now().UTC().Add(-time.Minute)
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET next_check_at = now() + interval '1 minute' WHERE id = $1`, p.ID)
		require.NoError(t, err)
		require.NoError(t, h.record(ctx, p.ID, CheckResult{Error: "boom"}, &runAt))
		row := env.row(t, p.ID)
		require.Nil(t, row.LastCheckAt, "another instance checked it after the run started")
		require.Zero(t, row.ConsecutiveCheckFailures)

		require.NoError(t, h.record(ctx, p.ID, CheckResult{Error: "boom"}, nil), "manual checks always record")
		require.Equal(t, 1, env.row(t, p.ID).ConsecutiveCheckFailures)
		later := time.Now().UTC().Add(time.Hour)
		require.NoError(t, h.record(ctx, p.ID, CheckResult{Error: "boom"}, &later))
		require.Equal(t, 2, env.row(t, p.ID).ConsecutiveCheckFailures)
		require.NoError(t, h.record(ctx, "pxy_missing", CheckResult{}, nil), "deleted proxies are ignored")
	})

	t.Run("hot sync failure still publishes the transition", func(t *testing.T) {
		p := env.importLines(t, env.ns, "socks5://"+socks.addr()+" region=us")[0]
		_, err := env.pool.Exec(ctx, `UPDATE proxies SET state = 'dead', consecutive_check_failures = 5 WHERE id = $1`, p.ID)
		require.NoError(t, err)
		env.hot.reset()
		env.hot.err = errors.New("redis down")
		defer env.hot.reset()

		_, err = h.CheckProxy(ctx, adminUser(), p.ID)
		requireReason(t, err, apperr.ReasonInternal)
		row := env.row(t, p.ID)
		require.Equal(t, StateActive, row.State, "PostgreSQL is updated first")
		require.Equal(t, "us", row.Attributes.Region)
		ev, ok := env.lastProxyEvent(t, p.ID)
		require.True(t, ok)
		require.Equal(t, "health_check", ev.Action)
		require.Equal(t, StateDead, ev.From)
		require.Equal(t, StateActive, ev.To)
	})
}
