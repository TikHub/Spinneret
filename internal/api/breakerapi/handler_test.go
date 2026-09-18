package breakerapi

import (
	"context"
	"math"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/breaker"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakertest"
)

type testEnv struct {
	*breakertest.Env
	h *Handler
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := breakertest.New(t)
	svc := breaker.New(breaker.Config{Now: env.Clock.Now}, env.Pool, env.Redis, env.Keys, env.Catalog, env.Bus, env.Audit, env.Metrics, nil)
	return &testEnv{Env: env, h: New(svc, env.Catalog)}
}

func as(t *testing.T, p *authz.Principal) context.Context {
	t.Helper()
	ctx := breakertest.Context(t)
	if p == nil {
		return ctx
	}
	return authz.WithPrincipal(ctx, p)
}

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.True(t, ok, "not an application error: %v", err)
	require.Equal(t, code, e.Code, err.Error())
}

var (
	admin    = breakertest.User("usr_admin", authz.RoleAdmin)
	operator = breakertest.User("usr_operator", authz.RoleOperator)
	viewer   = breakertest.User("usr_viewer", authz.RoleViewer)
	nobody   = breakertest.User("usr_nobody", "")
)

func TestOpenGetCloseBreaker(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	now := env.Clock.Now()
	env.WriteBucket(t, env.Search, now, 5000, 40, 30, 10, 1, 2, 3)

	opened, err := env.h.OpenBreaker(as(t, operator), connect.NewRequest(&spinneretv1.OpenBreakerRequest{
		EndpointGroupId: env.Search.ID, Duration: "30m", Reason: "incident",
	}))
	require.NoError(t, err)
	b := opened.Msg.GetBreaker()
	require.Equal(t, "open", b.GetState())
	require.True(t, b.GetManual())
	require.Equal(t, "incident", b.GetReason())
	require.Equal(t, now.Add(30*time.Minute), b.GetOpenUntil().AsTime())
	require.Equal(t, now, b.GetLastOpenedAt().AsTime())
	require.Nil(t, b.GetLastClosedAt())
	require.Equal(t, breakertest.Namespace, b.GetNamespace())
	require.Equal(t, "alpha", b.GetSite())
	require.Equal(t, breakertest.SiteAID, b.GetSiteId())
	require.Equal(t, "web", b.GetClient())
	require.Equal(t, "search", b.GetEndpointGroup())
	require.Equal(t, env.Search.ID, b.GetEndpointGroupId())
	require.Equal(t, "default-breaker", b.GetPolicyName())
	require.Empty(t, b.GetPolicyId())
	require.Equal(t, &spinneretv1.WindowMetrics{Total: 40, Success: 30, Risk: 10, CaptchaIdentities: 3, RiskRatio: 0.25, SuccessRatio: 0.75}, b.GetWindow())
	require.NotNil(t, b.GetProbe())

	got, err := env.h.GetBreaker(as(t, viewer), connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: env.Search.ID}))
	require.NoError(t, err)
	require.Equal(t, "open", got.Msg.GetBreaker().GetState())

	env.Clock.Advance(time.Minute)
	closed, err := env.h.CloseBreaker(as(t, operator), connect.NewRequest(&spinneretv1.CloseBreakerRequest{
		EndpointGroupId: env.Search.ID, Reason: "resolved",
	}))
	require.NoError(t, err)
	require.Equal(t, "closed", closed.Msg.GetBreaker().GetState())
	require.Nil(t, closed.Msg.GetBreaker().GetOpenUntil())
	require.Equal(t, now.Add(time.Minute), closed.Msg.GetBreaker().GetLastClosedAt().AsTime())
	require.Equal(t, int32(0), closed.Msg.GetBreaker().GetWindow().GetTotal(), "samples before the close are excluded")

	entries := env.Audit.Entries()
	require.Len(t, entries, 2)
	require.Equal(t, breaker.AuditBreakerOpen, entries[0].Action)
	require.Equal(t, "usr_operator", entries[0].ActorID)
	require.Equal(t, breaker.AuditBreakerClose, entries[1].Action)
	require.Len(t, env.BreakerEvents(t), 2)
}

func TestBreakerRPCDenials(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	siteB := breakertest.User("usr_site_b", authz.RoleOperator, breakertest.SiteBID)
	leaseToken := breakertest.Token(t, "tok_lease", "lease:acquire")
	tests := []struct {
		name string
		p    *authz.Principal
		call func(ctx context.Context) error
		code connect.Code
	}{
		{name: "open as viewer", p: viewer, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.OpenBreaker(ctx, connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "open other site", p: siteB, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.OpenBreaker(ctx, connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "open with lease token", p: leaseToken, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.OpenBreaker(ctx, connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "close without binding", p: nobody, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.CloseBreaker(ctx, connect.NewRequest(&spinneretv1.CloseBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "get other site", p: siteB, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.GetBreaker(ctx, connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "get with lease token", p: leaseToken, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.GetBreaker(ctx, connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "list without binding", p: nobody, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{Namespace: breakertest.Namespace}))
			return err
		}},
		{name: "list with lease token", p: leaseToken, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{}))
			return err
		}},
		{name: "list other namespace with token", p: breakertest.Token(t, "tok_admin", "admin"), code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{Namespace: "staging"}))
			return err
		}},
		{name: "list events without binding", p: nobody, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakerEvents(ctx, connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{Namespace: breakertest.Namespace}))
			return err
		}},
		{name: "list events of other site", p: siteB, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakerEvents(ctx, connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{Namespace: breakertest.Namespace, Site: "alpha"}))
			return err
		}},
		{name: "pause as viewer", p: viewer, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{Namespace: breakertest.Namespace, Site: "alpha", Paused: true}))
			return err
		}},
		{name: "pause other site", p: siteB, code: connect.CodePermissionDenied, call: func(ctx context.Context) error {
			_, err := env.h.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{Namespace: breakertest.Namespace, Site: "alpha", Paused: true}))
			return err
		}},
		{name: "unknown namespace", p: admin, code: connect.CodeNotFound, call: func(ctx context.Context) error {
			_, err := env.h.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{Namespace: "staging", Site: "alpha", Paused: true}))
			return err
		}},
		{name: "unknown group", p: admin, code: connect.CodeNotFound, call: func(ctx context.Context) error {
			_, err := env.h.GetBreaker(ctx, connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: "eg_missing"}))
			return err
		}},
		{name: "invalid duration", p: admin, code: connect.CodeInvalidArgument, call: func(ctx context.Context) error {
			_, err := env.h.OpenBreaker(ctx, connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID, Duration: "tomorrow"}))
			return err
		}},
		{name: "unauthenticated get", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.GetBreaker(ctx, connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "unauthenticated open", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.OpenBreaker(ctx, connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "unauthenticated close", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.CloseBreaker(ctx, connect.NewRequest(&spinneretv1.CloseBreakerRequest{EndpointGroupId: env.Search.ID}))
			return err
		}},
		{name: "unauthenticated list", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{Namespace: breakertest.Namespace}))
			return err
		}},
		{name: "unauthenticated events", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.ListBreakerEvents(ctx, connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{Namespace: breakertest.Namespace}))
			return err
		}},
		{name: "unauthenticated pause", code: connect.CodeUnauthenticated, call: func(ctx context.Context) error {
			_, err := env.h.SetSitePaused(ctx, connect.NewRequest(&spinneretv1.SetSitePausedRequest{Namespace: breakertest.Namespace, Site: "alpha"}))
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requireCode(t, tc.call(as(t, tc.p)), tc.code)
		})
	}
	require.Empty(t, env.BreakerEvents(t))
	require.Empty(t, env.Breaker(t, env.Search))

	// Authorization failures of the mutating RPCs are audited as denied; reads,
	// invalid requests and unauthenticated calls are not audited.
	entries := env.Audit.Entries()
	require.Len(t, entries, 6)
	actions := make([]string, 0, len(entries))
	for _, e := range entries {
		require.Equal(t, audit.ResultDenied, e.Result)
		actions = append(actions, e.Action)
	}
	require.Equal(t, []string{
		breaker.AuditBreakerOpen, breaker.AuditBreakerOpen, breaker.AuditBreakerOpen,
		breaker.AuditBreakerClose, breaker.AuditSitePause, breaker.AuditSitePause,
	}, actions)
}

func TestListBreakers(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, err := env.h.OpenBreaker(as(t, admin), connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.DefaultB.ID}))
	require.NoError(t, err)

	resp, err := env.h.ListBreakers(as(t, viewer), connect.NewRequest(&spinneretv1.ListBreakersRequest{
		Namespace: breakertest.Namespace, PageSize: 2,
	}))
	require.NoError(t, err)
	require.Equal(t, int32(4), resp.Msg.GetTotal())
	require.Len(t, resp.Msg.GetBreakers(), 2)
	require.Equal(t, env.DefaultB.ID, resp.Msg.GetBreakers()[0].GetEndpointGroupId())
	require.Nil(t, resp.Msg.GetBreakers()[0].GetOpenUntil(), "indefinite open")
	require.NotEmpty(t, resp.Msg.GetNextPageToken())

	next, err := env.h.ListBreakers(as(t, viewer), connect.NewRequest(&spinneretv1.ListBreakersRequest{
		Namespace: breakertest.Namespace, PageSize: 2, PageToken: resp.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	require.Len(t, next.Msg.GetBreakers(), 2)
	require.Empty(t, next.Msg.GetNextPageToken())

	token := breakertest.Token(t, "tok_admin", "admin")
	filtered, err := env.h.ListBreakers(as(t, token), connect.NewRequest(&spinneretv1.ListBreakersRequest{
		States: []string{"open"}, Site: "beta", Client: "web",
	}))
	require.NoError(t, err)
	require.Len(t, filtered.Msg.GetBreakers(), 1)
	require.Equal(t, "open", filtered.Msg.GetBreakers()[0].GetState())

	restricted, err := env.h.ListBreakers(as(t, breakertest.User("usr_a", authz.RoleViewer, breakertest.SiteAID)),
		connect.NewRequest(&spinneretv1.ListBreakersRequest{Namespace: breakertest.Namespace}))
	require.NoError(t, err)
	require.Equal(t, int32(3), restricted.Msg.GetTotal())
	for _, b := range restricted.Msg.GetBreakers() {
		require.Equal(t, "alpha", b.GetSite())
	}
}

func TestListBreakerEvents(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	start := env.Clock.Now()
	_, err := env.h.OpenBreaker(as(t, admin), connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.Search.ID, Duration: "5m", Reason: "one"}))
	require.NoError(t, err)
	env.Clock.Advance(time.Second)
	_, err = env.h.OpenBreaker(as(t, admin), connect.NewRequest(&spinneretv1.OpenBreakerRequest{EndpointGroupId: env.DefaultB.ID, Reason: "two"}))
	require.NoError(t, err)
	env.Clock.Advance(time.Second)
	_, err = env.h.SetSitePaused(as(t, admin), connect.NewRequest(&spinneretv1.SetSitePausedRequest{Namespace: breakertest.Namespace, Site: "beta", Paused: true, Reason: "three"}))
	require.NoError(t, err)

	resp, err := env.h.ListBreakerEvents(as(t, viewer), connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{
		Namespace: breakertest.Namespace, PageSize: 2,
	}))
	require.NoError(t, err)
	evs := resp.Msg.GetEvents()
	require.Len(t, evs, 2)
	require.Equal(t, "three", evs[0].GetReason())
	require.Equal(t, "site_switch", evs[0].GetTrigger())
	require.Equal(t, "running", evs[0].GetFromState())
	require.Equal(t, "paused", evs[0].GetToState())
	require.Equal(t, "two", evs[1].GetReason())
	require.Equal(t, "beta", evs[1].GetSite())
	require.Equal(t, "_default", evs[1].GetEndpointGroup())
	require.Equal(t, "web", evs[1].GetClient())
	require.Equal(t, "user:usr_admin", evs[1].GetActor())
	require.Contains(t, evs[1].GetMetrics().GetFields(), "total")
	require.NotEmpty(t, resp.Msg.GetNextPageToken())

	rest, err := env.h.ListBreakerEvents(as(t, viewer), connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{
		Namespace: breakertest.Namespace, PageSize: 2, PageToken: resp.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	require.Len(t, rest.Msg.GetEvents(), 1)
	first := rest.Msg.GetEvents()[0]
	require.Equal(t, "one", first.GetReason())
	require.Equal(t, start.Add(5*time.Minute), first.GetOpenUntil().AsTime())
	require.Equal(t, start, first.GetCreatedAt().AsTime())
	require.Empty(t, rest.Msg.GetNextPageToken())

	ranged, err := env.h.ListBreakerEvents(as(t, admin), connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{
		Namespace: breakertest.Namespace,
		Trigger:   "manual",
		TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(start.Add(time.Second)), End: timestamppb.New(start.Add(time.Hour))},
	}))
	require.NoError(t, err)
	require.Len(t, ranged.Msg.GetEvents(), 1)
	require.Equal(t, "two", ranged.Msg.GetEvents()[0].GetReason())

	_, err = env.h.ListBreakerEvents(as(t, admin), connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{
		Namespace: breakertest.Namespace, PageToken: "not-a-token",
	}))
	requireCode(t, err, connect.CodeInvalidArgument)
}

func TestSetSitePausedRPC(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	now := env.Clock.Now()

	resp, err := env.h.SetSitePaused(as(t, operator), connect.NewRequest(&spinneretv1.SetSitePausedRequest{
		Namespace: breakertest.Namespace, Site: "alpha", Paused: true, Reason: "redesign",
	}))
	require.NoError(t, err)
	require.Equal(t, "alpha", resp.Msg.GetSite())
	require.Equal(t, breakertest.SiteAID, resp.Msg.GetSiteId())
	require.True(t, resp.Msg.GetPaused())
	require.Equal(t, "redesign", resp.Msg.GetPausedReason())
	require.Equal(t, now, resp.Msg.GetPausedAt().AsTime())

	resp, err = env.h.SetSitePaused(as(t, operator), connect.NewRequest(&spinneretv1.SetSitePausedRequest{
		Namespace: breakertest.Namespace, Site: "alpha", Paused: false,
	}))
	require.NoError(t, err)
	require.False(t, resp.Msg.GetPaused())
	require.Nil(t, resp.Msg.GetPausedAt())
	require.Empty(t, resp.Msg.GetPausedReason())

	_, err = env.h.SetSitePaused(as(t, operator), connect.NewRequest(&spinneretv1.SetSitePausedRequest{
		Namespace: breakertest.Namespace, Site: "gamma", Paused: true,
	}))
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Len(t, env.Audit.Entries(), 2)
}

func TestClampInt32(t *testing.T) {
	t.Parallel()
	require.Equal(t, int32(math.MaxInt32), clampInt32(math.MaxInt64))
	require.Equal(t, int32(math.MinInt32), clampInt32(math.MinInt64))
	require.Equal(t, int32(42), clampInt32(42))
}
