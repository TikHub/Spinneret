// Package breakertest provides integration-test fixtures for the breaker
// service and its Connect handler: a PostgreSQL database and a Redis key
// prefix holding one tenant, one namespace and two sites whose rows mirror an
// in-memory catalog snapshot, plus a controllable clock, recorders for audit
// entries and bus events, and principal builders.
package breakertest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/catalog/catalogtest"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// Fixture identifiers.
const (
	TenantID    = "ten_breakertest"
	NamespaceID = "ns_breakertest"
	Namespace   = "prod"
	SiteAID     = "sit_alpha"
	SiteBID     = "sit_beta"
	SiteAKey    = 11
	SiteBKey    = 22
	SearchID    = "eg_alpha_search"
	SearchKey   = 11_500
)

// BaseTime is a Unix millisecond timestamp aligned to 1 s, 5 s and 1 min
// bucket boundaries.
var BaseTime = time.UnixMilli(1_758_011_400_000).UTC()

// Env is the fixture of one test.
type Env struct {
	Pool    *pgxpool.Pool
	Redis   rueidis.Client
	Keys    redis.Keys
	Catalog *catalogtest.Catalog
	Bus     events.Bus
	Audit   *AuditRecorder
	Events  *EventRecorder
	Metrics *observability.Metrics
	Clock   *Clock

	Namespace *catalog.Namespace
	// SiteA ("alpha", clients web+app) has the groups web/_default, app/_default
	// and web/search; SiteB ("beta", client web) has web/_default.
	SiteA, SiteB *catalog.Site
	Search       *catalog.EndpointGroup
	DefaultA     *catalog.EndpointGroup
	AppDefaultA  *catalog.EndpointGroup
	DefaultB     *catalog.EndpointGroup
}

// New creates the fixture: database rows, catalog snapshot, memory bus with
// an event recorder, audit recorder, metrics and a clock at BaseTime.
func New(t testing.TB) *Env {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)

	ns := catalogtest.NewNamespace(TenantID, NamespaceID, Namespace)
	siteA := catalogtest.AddSite(ns, SiteAID, "alpha", SiteAKey, "web", "app")
	siteB := catalogtest.AddSite(ns, SiteBID, "beta", SiteBKey, "web")
	search := catalogtest.AddGroup(siteA, SearchID, "web", "search", SearchKey)

	env := &Env{
		Pool:        pool,
		Redis:       rdb,
		Keys:        keys,
		Catalog:     catalogtest.New(ns),
		Bus:         events.NewMemoryBus(),
		Audit:       &AuditRecorder{},
		Events:      &EventRecorder{},
		Metrics:     observability.NewMetrics(),
		Clock:       NewClock(BaseTime),
		Namespace:   ns,
		SiteA:       siteA,
		SiteB:       siteB,
		Search:      search,
		DefaultA:    mustGroup(t, siteA, "web", "_default"),
		AppDefaultA: mustGroup(t, siteA, "app", "_default"),
		DefaultB:    mustGroup(t, siteB, "web", "_default"),
	}
	env.Bus.Subscribe(events.ChannelAll, env.Events.handle)
	env.insertRows(t)
	return env
}

func mustGroup(t testing.TB, s *catalog.Site, client, name string) *catalog.EndpointGroup {
	t.Helper()
	g, ok := s.Group(client, name)
	require.True(t, ok, "group %s/%s", client, name)
	return g
}

// insertRows mirrors the catalog snapshot into PostgreSQL with the snapshot's
// hot-state keys.
func (e *Env) insertRows(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exec := func(sql string, args ...any) {
		_, err := e.Pool.Exec(ctx, sql, args...)
		require.NoError(t, err, sql)
	}
	exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, TenantID, "breakertest")
	exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, $3)`, NamespaceID, TenantID, Namespace)
	for _, s := range []*catalog.Site{e.SiteA, e.SiteB} {
		exec(`INSERT INTO sites (id, hkey, namespace_id, name, clients) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5)`,
			s.ID, s.Key, NamespaceID, s.Name, s.Clients)
		for _, g := range s.GroupsByID {
			exec(`INSERT INTO endpoint_groups (id, hkey, site_id, client, name) OVERRIDING SYSTEM VALUE VALUES ($1, $2, $3, $4, $5)`,
				g.ID, g.Key, s.ID, g.Client, g.Name)
		}
	}
}

// Context returns a context with a generous timeout for one test step.
func Context(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// WriteBucket adds counts to the window bucket containing at and records the
// captcha identities in the bucket's HyperLogLog (as observe.lua does).
func (e *Env) WriteBucket(t testing.TB, g *catalog.EndpointGroup, at time.Time, bucketMs, total, success, risk int64, captchaIdentities ...int64) {
	t.Helper()
	ctx := Context(t)
	bucket := at.UnixMilli() / bucketMs
	cmds := rueidis.Commands{
		e.Redis.B().Hincrby().Key(e.Keys.Window(g.SiteKey, g.Key, bucket)).Field("t").Increment(total).Build(),
		e.Redis.B().Hincrby().Key(e.Keys.Window(g.SiteKey, g.Key, bucket)).Field("s").Increment(success).Build(),
		e.Redis.B().Hincrby().Key(e.Keys.Window(g.SiteKey, g.Key, bucket)).Field("r").Increment(risk).Build(),
	}
	if len(captchaIdentities) > 0 {
		members := make([]string, 0, len(captchaIdentities))
		for _, id := range captchaIdentities {
			members = append(members, strconv.FormatInt(id, 10))
		}
		cmds = append(cmds, e.Redis.B().Pfadd().Key(e.Keys.WindowHLL(g.SiteKey, g.Key, bucket)).Element(members...).Build())
	}
	for _, res := range e.Redis.DoMulti(ctx, cmds...) {
		require.NoError(t, res.Error())
	}
}

// MarkActive records acquire activity of a group at "at" in "aeg".
func (e *Env) MarkActive(t testing.TB, g *catalog.EndpointGroup, at time.Time) {
	t.Helper()
	err := e.Redis.Do(Context(t), e.Redis.B().Zadd().Key(e.Keys.ActiveGroups(g.SiteKey)).ScoreMember().
		ScoreMember(float64(at.UnixMilli()), strconv.FormatInt(g.Key, 10)).Build()).Error()
	require.NoError(t, err)
}

// SetBreaker overwrites fields of the breaker hash of a group and keeps the
// "brko" set consistent with the "st" field when present.
func (e *Env) SetBreaker(t testing.TB, g *catalog.EndpointGroup, fields map[string]string) {
	t.Helper()
	ctx := Context(t)
	cmd := e.Redis.B().Hset().Key(e.Keys.Breaker(g.SiteKey, g.Key)).FieldValue()
	for k, v := range fields {
		cmd = cmd.FieldValue(k, v)
	}
	require.NoError(t, e.Redis.Do(ctx, cmd.Build()).Error())
	if st, ok := fields["st"]; ok {
		member := strconv.FormatInt(g.Key, 10)
		key := e.Keys.OpenBreakers(g.SiteKey)
		if st == "closed" {
			require.NoError(t, e.Redis.Do(ctx, e.Redis.B().Srem().Key(key).Member(member).Build()).Error())
		} else {
			require.NoError(t, e.Redis.Do(ctx, e.Redis.B().Sadd().Key(key).Member(member).Build()).Error())
		}
	}
}

// Breaker returns every field of the breaker hash of a group.
func (e *Env) Breaker(t testing.TB, g *catalog.EndpointGroup) map[string]string {
	t.Helper()
	m, err := e.Redis.Do(Context(t), e.Redis.B().Hgetall().Key(e.Keys.Breaker(g.SiteKey, g.Key)).Build()).AsStrMap()
	require.NoError(t, err)
	return m
}

// OpenMembers returns the "brko" members of a site.
func (e *Env) OpenMembers(t testing.TB, site *catalog.Site) []string {
	t.Helper()
	m, err := e.Redis.Do(Context(t), e.Redis.B().Smembers().Key(e.Keys.OpenBreakers(site.Key)).Build()).AsStrSlice()
	require.NoError(t, err)
	return m
}

// BreakerEventRow is a breaker_events row as stored.
type BreakerEventRow struct {
	ID, SiteID, EndpointGroupID, From, To, Trigger, Reason, Actor string
	OpenUntil                                                     *time.Time
	Metrics                                                       map[string]any
}

// BreakerEvents returns the breaker_events rows of the namespace, oldest first.
func (e *Env) BreakerEvents(t testing.TB) []BreakerEventRow {
	t.Helper()
	rows, err := e.Pool.Query(Context(t), `SELECT id, site_id, endpoint_group_id, from_state, to_state, trigger, reason, actor, open_until, metrics
		FROM breaker_events WHERE namespace_id = $1 ORDER BY created_at, id`, NamespaceID)
	require.NoError(t, err)
	defer rows.Close()
	var out []BreakerEventRow
	for rows.Next() {
		var r BreakerEventRow
		var metrics []byte
		require.NoError(t, rows.Scan(&r.ID, &r.SiteID, &r.EndpointGroupID, &r.From, &r.To, &r.Trigger, &r.Reason, &r.Actor, &r.OpenUntil, &metrics))
		require.NoError(t, json.Unmarshal(metrics, &r.Metrics))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// RuntimeVersion reads "rtv:<ns>" field kind (0 when missing).
func (e *Env) RuntimeVersion(t testing.TB, kind string) int64 {
	t.Helper()
	v, err := e.Redis.Do(Context(t), e.Redis.B().Hget().Key(e.Keys.RuntimeVersions(NamespaceID)).Field(kind).Build()).AsInt64()
	if rueidis.IsRedisNil(err) {
		return 0
	}
	require.NoError(t, err)
	return v
}

// User returns a user principal with one role binding in the fixture tenant
// and namespace, optionally restricted to sites. An empty role yields a user
// without bindings.
func User(id string, role authz.Role, siteIDs ...string) *authz.Principal {
	p := &authz.Principal{Kind: authz.KindUser, ID: id, Name: id, TenantID: TenantID}
	if role != "" {
		p.Bindings = []authz.Binding{{
			ID: "rb_" + id, TenantID: TenantID, Role: role, NamespaceID: NamespaceID, SiteIDs: siteIDs,
		}}
	}
	return p
}

// Token returns a token principal of the fixture namespace with scopes.
func Token(t testing.TB, id string, scopes ...string) *authz.Principal {
	t.Helper()
	parsed, err := authz.ParseScopes(scopes)
	require.NoError(t, err)
	return &authz.Principal{
		Kind: authz.KindToken, ID: id, Name: id, TenantID: TenantID,
		NamespaceID: NamespaceID, NamespaceName: Namespace, Scopes: parsed,
	}
}

// Clock is a controllable time source.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// AuditRecorder collects audit entries.
type AuditRecorder struct {
	mu      sync.Mutex
	entries []audit.Entry
}

// Record implements audit.Recorder.
func (r *AuditRecorder) Record(_ context.Context, e audit.Entry) {
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

// Entries returns the recorded entries.
func (r *AuditRecorder) Entries() []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.Entry(nil), r.entries...)
}

// Recorded is one captured bus event.
type Recorded struct {
	Channel string
	Event   events.Event
}

// EventRecorder collects bus events.
type EventRecorder struct {
	mu     sync.Mutex
	events []Recorded
}

func (r *EventRecorder) handle(_ context.Context, channel string, ev events.Event) {
	r.mu.Lock()
	r.events = append(r.events, Recorded{Channel: channel, Event: ev})
	r.mu.Unlock()
}

// On returns the events published on channel.
func (r *EventRecorder) On(channel string) []Recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Recorded
	for _, e := range r.events {
		if e.Channel == channel {
			out = append(out, e)
		}
	}
	return out
}

// Decode unmarshals the data of a recorded event.
func Decode[T any](t testing.TB, r Recorded) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(r.Event.Data, &v), fmt.Sprintf("event %s", r.Event.Type))
	return v
}
