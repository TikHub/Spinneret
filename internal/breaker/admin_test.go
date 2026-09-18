package breaker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakertest"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok, "not an application error: %v", err)
	require.Equal(t, code, e.Code, err.Error())
}

func TestParseOpenDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "", want: 0},
		{in: "  ", want: 0},
		{in: "permanent", want: 0},
		{in: "30m", want: 30 * time.Minute},
		{in: "2h", want: 2 * time.Hour},
		{in: "365d", want: MaxManualOpenDuration},
		{in: "366d", wantErr: true},
		{in: "0", wantErr: true},
		{in: "soon", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseOpenDuration(tc.in)
			if tc.wantErr {
				requireCode(t, err, connect.CodeInvalidArgument)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestManualOpenClose(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	operator := breakertest.User("usr_op", authz.RoleOperator)
	g := env.Search
	env.WriteBucket(t, g, now, 5000, 20, 15, 5)

	st, err := svc.Open(ctx, operator, g.ID, "30m", "  upstream incident ")
	require.NoError(t, err)
	require.Equal(t, StateOpen, st.State)
	require.True(t, st.Manual)
	require.Equal(t, now.Add(30*time.Minute), st.OpenUntil)
	require.Equal(t, "upstream incident", st.Reason)
	require.Equal(t, int64(20), st.Window.Total)
	require.Equal(t, 0.75, st.Window.SuccessRatio)
	require.Equal(t, "alpha", st.Site)
	require.Equal(t, "search", st.EndpointGroup)
	require.Equal(t, "default-breaker", st.PolicyName)

	env.Clock.Advance(time.Minute)
	st, err = svc.Open(ctx, operator, g.ID, "", "until further notice")
	require.NoError(t, err)
	require.True(t, st.OpenUntil.IsZero())

	env.Clock.Advance(time.Minute)
	env.WriteBucket(t, g, env.Clock.Now(), 5000, 8, 8, 0)
	st, err = svc.Close(ctx, operator, g.ID, "resolved")
	require.NoError(t, err)
	require.Equal(t, StateClosed, st.State)
	require.False(t, st.Manual)
	require.Equal(t, now.Add(2*time.Minute), st.LastClosedAt)

	// Closing a closed breaker records nothing but the audit entry.
	_, err = svc.Close(ctx, operator, g.ID, "again")
	require.NoError(t, err)

	rows := env.BreakerEvents(t)
	require.Len(t, rows, 3)
	for _, r := range rows {
		require.Equal(t, TriggerManual, r.Trigger)
		require.Equal(t, "user:usr_op", r.Actor)
	}
	require.Equal(t, []string{StateClosed, StateOpen, StateOpen}, []string{rows[0].From, rows[1].From, rows[2].From})
	require.Equal(t, []string{StateOpen, StateOpen, StateClosed}, []string{rows[0].To, rows[1].To, rows[2].To})
	require.NotNil(t, rows[0].OpenUntil)
	require.Nil(t, rows[1].OpenUntil)
	require.EqualValues(t, 20, rows[0].Metrics["total"])
	require.EqualValues(t, 8, rows[2].Metrics["total"], "close events carry the window before the close")
	require.Equal(t, int64(3), env.RuntimeVersion(t, KindBreakers))

	nsEvents := env.Events.On(events.NamespaceChannel(breakertest.NamespaceID))
	require.Len(t, nsEvents, 3)
	first := breakertest.Decode[TransitionData](t, nsEvents[0])
	require.Equal(t, TriggerManual, first.Trigger)
	require.True(t, first.Manual)
	require.Equal(t, "user:usr_op", first.Actor)

	entries := env.Audit.Entries()
	require.Len(t, entries, 4)
	require.Equal(t, []string{AuditBreakerOpen, AuditBreakerOpen, AuditBreakerClose, AuditBreakerClose},
		[]string{entries[0].Action, entries[1].Action, entries[2].Action, entries[3].Action})
	require.Equal(t, audit.ResultOK, entries[0].Result)
	require.Equal(t, "30m", entries[0].Details["duration"])
	require.Equal(t, "indefinite", entries[1].Details["duration"])
	require.Equal(t, false, entries[3].Details["changed"])
	require.Equal(t, g.ID, entries[0].ResourceID)
	require.Equal(t, breakertest.NamespaceID, entries[0].NamespaceID)
}

func TestManualOperationErrors(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	operator := breakertest.User("usr_op", authz.RoleOperator)
	tests := []struct {
		name string
		call func() error
		code connect.Code
	}{
		{name: "viewer cannot open", code: connect.CodePermissionDenied, call: func() error {
			_, err := svc.Open(ctx, breakertest.User("usr_v", authz.RoleViewer), env.Search.ID, "", "")
			return err
		}},
		{name: "operator of another site cannot close", code: connect.CodePermissionDenied, call: func() error {
			_, err := svc.Close(ctx, breakertest.User("usr_b", authz.RoleOperator, env.SiteB.ID), env.Search.ID, "")
			return err
		}},
		{name: "token without admin scope", code: connect.CodePermissionDenied, call: func() error {
			_, err := svc.Open(ctx, breakertest.Token(t, "tok_1", "lease:acquire"), env.Search.ID, "", "")
			return err
		}},
		{name: "invalid duration", code: connect.CodeInvalidArgument, call: func() error {
			_, err := svc.Open(ctx, operator, env.Search.ID, "-5m", "")
			return err
		}},
		{name: "reason too long", code: connect.CodeInvalidArgument, call: func() error {
			_, err := svc.Close(ctx, operator, env.Search.ID, string(make([]byte, MaxReasonLength+1)))
			return err
		}},
		{name: "unknown group", code: connect.CodeNotFound, call: func() error {
			_, err := svc.Open(ctx, operator, "eg_missing", "", "")
			return err
		}},
		{name: "empty group", code: connect.CodeInvalidArgument, call: func() error {
			_, err := svc.Get(ctx, operator, "")
			return err
		}},
		{name: "user of another tenant", code: connect.CodeNotFound, call: func() error {
			other := breakertest.User("usr_x", authz.RoleOwner)
			other.TenantID = "ten_other"
			_, err := svc.Get(ctx, other, env.Search.ID)
			return err
		}},
		{name: "nil principal", code: connect.CodeUnauthenticated, call: func() error {
			_, err := svc.Get(ctx, nil, env.Search.ID)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requireCode(t, tc.call(), tc.code)
		})
	}
	require.Empty(t, env.BreakerEvents(t))
	require.Empty(t, env.Breaker(t, env.Search))

	// Only the three authorization failures of mutations are audited.
	entries := env.Audit.Entries()
	require.Len(t, entries, 3)
	require.Equal(t, []string{AuditBreakerOpen, AuditBreakerClose, AuditBreakerOpen},
		[]string{entries[0].Action, entries[1].Action, entries[2].Action})
	for _, e := range entries {
		require.Equal(t, audit.ResultDenied, e.Result)
		require.Equal(t, env.Search.ID, e.ResourceID)
		require.Equal(t, string(authz.PermBreakerOperate), e.Details["permission"])
	}
	require.Equal(t, "token", entries[2].ActorKind)
}

func TestManualOperationPrincipals(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)

	_, err := svc.Open(ctx, breakertest.Token(t, "tok_admin", "admin"), env.DefaultB.ID, "1h", "token")
	require.NoError(t, err)
	platform := &authz.Principal{Kind: authz.KindUser, ID: "usr_root", IsPlatformAdmin: true}
	st, err := svc.Close(ctx, platform, env.DefaultB.ID, "platform")
	require.NoError(t, err)
	require.Equal(t, StateClosed, st.State)
	st, err = svc.Get(ctx, authz.System("test"), env.DefaultB.ID)
	require.NoError(t, err)
	require.Equal(t, int64(2), st.Version)

	rows := env.BreakerEvents(t)
	require.Len(t, rows, 2)
	require.Equal(t, "token:tok_admin", rows[0].Actor)
	require.Equal(t, "user:usr_root", rows[1].Actor)
}

func TestSetSitePaused(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	admin := breakertest.User("usr_admin", authz.RoleAdmin)
	meta := env.Keys.SiteMeta(env.SiteA.Key)
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Hset().Key(meta).FieldValue().FieldValue("site", env.SiteA.ID).FieldValue("paused", "0").Build()).Error())

	sw, err := svc.SetSitePaused(ctx, admin, env.Namespace, "alpha", true, "site redesign")
	require.NoError(t, err)
	require.True(t, sw.Changed)
	require.True(t, sw.Paused)
	require.Equal(t, "site redesign", sw.Reason)
	require.Equal(t, "user:usr_admin", sw.PausedBy)
	require.NotNil(t, sw.PausedAt)
	require.Equal(t, now.UnixMicro(), sw.PausedAt.UnixMicro())

	var paused bool
	var reason, by string
	var pausedAt *time.Time
	require.NoError(t, env.Pool.QueryRow(ctx, `SELECT paused, paused_reason, paused_by, paused_at FROM sites WHERE id = $1`, env.SiteA.ID).
		Scan(&paused, &reason, &by, &pausedAt))
	require.True(t, paused)
	require.Equal(t, "site redesign", reason)
	require.Equal(t, "user:usr_admin", by)
	require.NotNil(t, pausedAt)

	flag, err := env.Redis.Do(ctx, env.Redis.B().Hget().Key(meta).Field("paused").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "1", flag)
	require.Contains(t, env.Catalog.Calls(), "invalidate:"+breakertest.NamespaceID)
	require.Equal(t, int64(1), env.RuntimeVersion(t, KindSiteSwitches))
	require.Equal(t, int64(1), env.RuntimeVersion(t, KindBreakers))

	rows := env.BreakerEvents(t)
	require.Len(t, rows, 1)
	require.Equal(t, TriggerSiteSwitch, rows[0].Trigger)
	require.Equal(t, SiteRunning, rows[0].From)
	require.Equal(t, SitePaused, rows[0].To)
	require.Empty(t, rows[0].EndpointGroupID)
	require.Equal(t, "site redesign", rows[0].Reason)

	nsEvents := env.Events.On(events.NamespaceChannel(breakertest.NamespaceID))
	require.Len(t, nsEvents, 1)
	data := breakertest.Decode[TransitionData](t, nsEvents[0])
	require.Equal(t, TriggerSiteSwitch, data.Trigger)
	require.Equal(t, SitePaused, data.To)
	require.Empty(t, data.EndpointGroupID)
	require.Len(t, env.Events.On(events.ChannelRuntime), 2)

	// Same state and reason: nothing changes, paused_at is kept.
	env.Clock.Advance(time.Minute)
	sw, err = svc.SetSitePaused(ctx, admin, env.Namespace, "alpha", true, "site redesign")
	require.NoError(t, err)
	require.False(t, sw.Changed)
	require.Equal(t, now.UnixMicro(), sw.PausedAt.UnixMicro())
	require.Equal(t, int64(1), env.RuntimeVersion(t, KindSiteSwitches))

	// New reason: content changes, no state transition.
	sw, err = svc.SetSitePaused(ctx, admin, env.Namespace, "alpha", true, "extended")
	require.NoError(t, err)
	require.False(t, sw.Changed)
	require.Equal(t, "extended", sw.Reason)
	require.Equal(t, int64(2), env.RuntimeVersion(t, KindSiteSwitches))
	require.Len(t, env.BreakerEvents(t), 1)

	// Resume.
	sw, err = svc.SetSitePaused(ctx, admin, env.Namespace, "alpha", false, "done")
	require.NoError(t, err)
	require.True(t, sw.Changed)
	require.False(t, sw.Paused)
	require.Nil(t, sw.PausedAt)
	require.Empty(t, sw.Reason)
	flag, err = env.Redis.Do(ctx, env.Redis.B().Hget().Key(meta).Field("paused").Build()).ToString()
	require.NoError(t, err)
	require.Equal(t, "0", flag)
	rows = env.BreakerEvents(t)
	require.Len(t, rows, 2)
	require.Equal(t, SitePaused, rows[1].From)
	require.Equal(t, SiteRunning, rows[1].To)
	require.Equal(t, "done", rows[1].Reason)
	require.Equal(t, int64(3), env.RuntimeVersion(t, KindSiteSwitches))

	// Site B has no hot state yet: the meta hash is not created.
	_, err = svc.SetSitePaused(ctx, admin, env.Namespace, "beta", true, "")
	require.NoError(t, err)
	exists, err := env.Redis.Do(ctx, env.Redis.B().Exists().Key(env.Keys.SiteMeta(env.SiteB.Key)).Build()).AsInt64()
	require.NoError(t, err)
	require.Zero(t, exists)

	entries := env.Audit.Entries()
	require.Len(t, entries, 5)
	require.Equal(t, AuditSitePause, entries[0].Action)
	require.Equal(t, AuditSiteResume, entries[3].Action)
	require.Equal(t, "site", entries[0].ResourceKind)
}

func TestSetSitePausedErrors(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	admin := breakertest.User("usr_admin", authz.RoleAdmin)
	tests := []struct {
		name string
		p    *authz.Principal
		site string
		code connect.Code
	}{
		{name: "viewer", p: breakertest.User("usr_v", authz.RoleViewer), site: "alpha", code: connect.CodePermissionDenied},
		{name: "operator of another site", p: breakertest.User("usr_o", authz.RoleOperator, env.SiteB.ID), site: "alpha", code: connect.CodePermissionDenied},
		{name: "unknown site", p: admin, site: "gamma", code: connect.CodeInvalidArgument},
		{name: "empty site", p: admin, site: "", code: connect.CodeInvalidArgument},
		{name: "nil principal", p: nil, site: "alpha", code: connect.CodeUnauthenticated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SetSitePaused(ctx, tc.p, env.Namespace, tc.site, true, "")
			requireCode(t, err, tc.code)
		})
	}
	_, err := svc.SetSitePaused(ctx, admin, env.Namespace, "alpha", true, string(make([]byte, MaxReasonLength+1)))
	requireCode(t, err, connect.CodeInvalidArgument)

	// The site vanished from PostgreSQL while still in the snapshot.
	_, err = env.Pool.Exec(ctx, `DELETE FROM sites WHERE id = $1`, env.SiteB.ID)
	require.NoError(t, err)
	_, err = svc.SetSitePaused(ctx, admin, env.Namespace, "beta", true, "")
	requireCode(t, err, connect.CodeNotFound)
	entries := env.Audit.Entries()
	require.Len(t, entries, 3)
	require.Equal(t, []string{audit.ResultDenied, audit.ResultDenied, audit.ResultError},
		[]string{entries[0].Result, entries[1].Result, entries[2].Result})
	require.Equal(t, AuditSitePause, entries[0].Action)
	require.Equal(t, env.SiteA.ID, entries[0].ResourceID)
	require.Equal(t, env.SiteB.ID, entries[2].ResourceID)
}

// flakyClient fails the n-th DoMulti call (1-based) of the wrapped client
// with a canceled context, leaving every other call untouched.
type flakyClient struct {
	rueidis.Client
	calls  atomic.Int32
	failOn int32
}

func (c *flakyClient) DoMulti(ctx context.Context, multi ...rueidis.Completed) []rueidis.RedisResult {
	if c.calls.Add(1) == c.failOn {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return c.Client.DoMulti(canceled, multi...)
	}
	return c.Client.DoMulti(ctx, multi...)
}

func TestManualOperationStatusReadFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		failOn int32
		op     string
	}{
		{name: "status read before open fails", failOn: 1, op: "open"},
		{name: "status read after open fails", failOn: 2, op: "open"},
		{name: "status read after close fails", failOn: 2, op: "close"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := breakertest.New(t)
			ctx := breakertest.Context(t)
			operator := breakertest.User("usr_op", authz.RoleOperator)
			g := env.Search
			if tc.op == "close" {
				env.SetBreaker(t, g, map[string]string{"st": "open", "man": "1", "ou": "0", "v": "1"})
			}
			env.WriteBucket(t, g, env.Clock.Now(), 5000, 20, 15, 5)
			client := &flakyClient{Client: env.Redis, failOn: tc.failOn}
			svc := New(Config{Now: env.Clock.Now}, env.Pool, client, env.Keys, env.Catalog, env.Bus, env.Audit, env.Metrics, nil)

			var st *Status
			var err error
			if tc.op == "open" {
				st, err = svc.Open(ctx, operator, g.ID, "10m", "incident")
			} else {
				st, err = svc.Close(ctx, operator, g.ID, "resolved")
			}
			require.NoError(t, err, "a successful operation is not failed by a status read")
			rows := env.BreakerEvents(t)
			require.Len(t, rows, 1)
			if tc.op == "open" {
				require.Equal(t, StateOpen, st.State)
				require.True(t, st.Manual)
				require.Equal(t, env.Clock.Now().Add(10*time.Minute), st.OpenUntil)
				if tc.failOn == 1 {
					require.EqualValues(t, 0, rows[0].Metrics["total"], "metrics are empty without the prior read")
				} else {
					require.EqualValues(t, 20, st.Window.Total)
				}
				return
			}
			require.Equal(t, StateClosed, st.State)
			require.Zero(t, st.Window.Total)
			require.Equal(t, env.Clock.Now(), st.LastClosedAt)
		})
	}
}
