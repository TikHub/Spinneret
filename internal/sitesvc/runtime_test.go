package sitesvc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/breaker"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/site"
)

// runtimeRecorder collects events published on events.ChannelRuntime.
type runtimeRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *runtimeRecorder) handle(_ context.Context, _ string, ev events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// take returns and clears the recorded events.
func (r *runtimeRecorder) take() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.events
	r.events = nil
	return out
}

// failingBus delivers locally and then reports a broadcast failure.
type failingBus struct {
	events.Bus
}

func (b failingBus) Publish(ctx context.Context, channel string, ev events.Event) error {
	_ = b.Bus.Publish(ctx, channel, ev)
	return errors.New("broadcast failed")
}

// withBus replaces the env service with one publishing on a memory bus and
// returns the recorder of runtime events.
func (e *env) withBus() *runtimeRecorder {
	e.t.Helper()
	bus := events.NewMemoryBus()
	rec := &runtimeRecorder{}
	e.t.Cleanup(bus.Subscribe(events.ChannelRuntime, rec.handle))
	e.svc = NewService(e.pool, e.cat, e.hot, e.audit, e.rdb, e.keys, nil, WithEventBus(bus))
	return rec
}

// runtimeVersions returns the "breakers" and "site_switches" versions of the
// env namespace (0 when missing).
func (e *env) runtimeVersions() (breakers, switches int64) {
	e.t.Helper()
	values, err := e.rdb.Do(context.Background(), e.rdb.B().Hmget().Key(e.keys.RuntimeVersions(e.ns.ID)).
		Field(runtimeKindBreakers, runtimeKindSiteSwitches).Build()).ToArray()
	require.NoError(e.t, err)
	parse := func(m rueidis.RedisMessage) int64 {
		if m.IsNil() {
			return 0
		}
		n, err := m.AsInt64()
		require.NoError(e.t, err)
		return n
	}
	return parse(values[0]), parse(values[1])
}

// requireBump asserts that exactly one bump of both kinds to version was
// published for the env namespace.
func (e *env) requireBump(rec *runtimeRecorder, version int64) {
	e.t.Helper()
	b, sw := e.runtimeVersions()
	require.Equal(e.t, version, b, "breakers version")
	require.Equal(e.t, version, sw, "site_switches version")
	evs := rec.take()
	require.Len(e.t, evs, 2)
	kinds := map[string]int64{}
	for _, ev := range evs {
		require.Equal(e.t, breaker.RuntimeEventType, ev.Type)
		require.Equal(e.t, e.tenantID, ev.TenantID)
		require.Equal(e.t, e.ns.ID, ev.NamespaceID)
		require.False(e.t, ev.At.IsZero())
		var data breaker.RuntimeEventData
		require.NoError(e.t, json.Unmarshal(ev.Data, &data))
		require.Equal(e.t, e.ns.ID, data.Namespace)
		kinds[data.Kind] = data.Version
	}
	require.Equal(e.t, map[string]int64{breaker.KindBreakers: version, breaker.KindSiteSwitches: version}, kinds)
}

func (e *env) requireNoBump(rec *runtimeRecorder, version int64) {
	e.t.Helper()
	b, sw := e.runtimeVersions()
	require.Equal(e.t, version, b, "breakers version")
	require.Equal(e.t, version, sw, "site_switches version")
	require.Empty(e.t, rec.take())
}

func TestRuntimeConstantsMatchBreaker(t *testing.T) {
	require.Equal(t, breaker.KindBreakers, runtimeKindBreakers)
	require.Equal(t, breaker.KindSiteSwitches, runtimeKindSiteSwitches)
	require.Equal(t, breaker.RuntimeEventType, runtimeEventType)
	raw, err := json.Marshal(runtimeEventData{Namespace: "ns_1", Kind: runtimeKindBreakers, Version: 3})
	require.NoError(t, err)
	require.JSONEq(t, `{"ns":"ns_1","kind":"breakers","version":3}`, string(raw))
}

func TestStructuralChangesBumpRuntimeVersions(t *testing.T) {
	e := newEnv(t)
	rec := e.withBus()
	ctx := context.Background()

	created, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
	require.NoError(t, err)
	e.requireBump(rec, 1)

	// Metadata-only changes keep the runtime documents.
	display := "Shop (example site)"
	_, err = e.svc.UpdateSite(ctx, e.actor, created.ID, UpdateSiteInput{DisplayName: &display})
	require.NoError(t, err)
	e.requireNoBump(rec, 1)

	// Adding and removing clients creates and deletes endpoint groups.
	_, err = e.svc.UpdateSite(ctx, e.actor, created.ID, UpdateSiteInput{Clients: []string{"web", "app"}})
	require.NoError(t, err)
	e.requireBump(rec, 2)
	_, err = e.svc.UpdateSite(ctx, e.actor, created.ID, UpdateSiteInput{Clients: []string{"web", "app"}})
	require.NoError(t, err)
	e.requireNoBump(rec, 2)
	_, err = e.svc.UpdateSite(ctx, e.actor, created.ID, UpdateSiteInput{Clients: []string{"web"}})
	require.NoError(t, err)
	e.requireBump(rec, 3)

	group, err := e.svc.CreateEndpointGroup(ctx, e.actor, created.ID, CreateGroupInput{
		Client: "web", Name: "search", Rules: []URIRule{{Kind: site.RulePrefix, Pattern: "/search"}},
	})
	require.NoError(t, err)
	e.requireBump(rec, 4)

	watermark := 3
	_, err = e.svc.UpdateEndpointGroup(ctx, e.actor, group.ID, UpdateGroupInput{LowWatermark: &watermark})
	require.NoError(t, err)
	e.requireNoBump(rec, 4)
	_, err = e.svc.ReplaceURIRules(ctx, e.actor, group.ID, []URIRule{{Kind: site.RulePrefix, Pattern: "/s"}})
	require.NoError(t, err)
	e.requireNoBump(rec, 4)

	require.NoError(t, e.svc.DeleteEndpointGroup(ctx, e.actor, group.ID))
	e.requireBump(rec, 5)

	// Failed mutations do not bump.
	_, err = e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
	requireCode(t, err, apperr.ReasonAlreadyExists)
	e.requireNoBump(rec, 5)

	require.NoError(t, e.svc.DeleteSite(ctx, e.actor, created.ID, false))
	e.requireBump(rec, 6)
}

func TestRuntimeBumpWithoutBus(t *testing.T) {
	e := newEnv(t)
	e.createSite("shop")
	b, sw := e.runtimeVersions()
	require.Equal(t, int64(1), b)
	require.Equal(t, int64(1), sw)
}

func TestRuntimeBumpWithoutRedis(t *testing.T) {
	e := newEnv(t)
	e.svc = NewService(e.pool, e.cat, e.hot, e.audit, nil, e.keys, nil, WithEventBus(events.NewMemoryBus()), nil)
	e.createSite("shop")
	b, sw := e.runtimeVersions()
	require.Zero(t, b)
	require.Zero(t, sw)
}

func TestRuntimeBumpFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("redis error", func(t *testing.T) {
		e := newEnv(t)
		rec := e.withBus()
		// A string at the hash key makes HINCRBY fail with WRONGTYPE.
		require.NoError(t, e.rdb.Do(ctx, e.rdb.B().Set().Key(e.keys.RuntimeVersions(e.ns.ID)).Value("x").Build()).Error())
		created, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
		requireCode(t, err, apperr.ReasonInternal)
		require.Equal(t, "shop", created.Name, "the committed site is still returned")
		require.Empty(t, rec.take())
		require.Equal(t, []string{created.ID}, e.hot.syncedSites(), "hot state was synced before the bump")
	})

	t.Run("publish error", func(t *testing.T) {
		e := newEnv(t)
		bus := events.NewMemoryBus()
		rec := &runtimeRecorder{}
		t.Cleanup(bus.Subscribe(events.ChannelRuntime, rec.handle))
		e.svc = NewService(e.pool, e.cat, e.hot, e.audit, e.rdb, e.keys, nil, WithEventBus(failingBus{bus}))
		_, err := e.svc.CreateSite(ctx, e.actor, e.ns, CreateSiteInput{Name: "shop"})
		requireCode(t, err, apperr.ReasonInternal)
		require.ErrorContains(t, err, "broadcast failed")
		e.requireBump(rec, 1)
	})
}
