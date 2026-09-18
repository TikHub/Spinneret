package breaker

import (
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/breaker/breakertest"
)

func groupIDs(page ListPage) []string {
	out := make([]string, 0, len(page.Breakers))
	for _, b := range page.Breakers {
		out = append(out, b.EndpointGroupID)
	}
	return out
}

func TestListAndGet(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	ms := func(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

	env.SetBreaker(t, env.Search, map[string]string{"st": "open", "ou": ms(now.Add(time.Minute)), "oc": "2", "lo": ms(now), "rsn": "tripped", "v": "4"})
	env.SetBreaker(t, env.DefaultB, map[string]string{
		"st": "half_open", "hw": ms(now.Add(-2 * time.Second)), "hc": "3", "ps": "2", "pk": "1", "lc": ms(now.Add(-time.Hour)), "v": "5",
	})
	env.WriteBucket(t, env.DefaultA, now, 5000, 10, 7, 3, 42)
	env.SiteB.Paused = true

	admin := breakertest.User("usr_admin", authz.RoleAdmin)
	page, err := svc.List(ctx, admin, env.Namespace, ListFilter{})
	require.NoError(t, err)
	require.Equal(t, 4, page.Total)
	require.Empty(t, page.NextPageToken)
	require.Equal(t, []string{env.Search.ID, env.DefaultB.ID, env.AppDefaultA.ID, env.DefaultA.ID}, groupIDs(page))

	open := page.Breakers[0]
	require.Equal(t, StateOpen, open.State)
	require.Equal(t, now.Add(time.Minute), open.OpenUntil)
	require.Equal(t, int64(2), open.ConsecutiveOpens)
	require.Equal(t, now, open.LastOpenedAt)
	require.Equal(t, "tripped", open.Reason)
	require.Equal(t, breakertest.Namespace, open.Namespace)

	half := page.Breakers[1]
	require.Equal(t, StateHalfOpen, half.State)
	require.True(t, half.OpenUntil.IsZero())
	require.Equal(t, ProbeMetrics{Samples: 2, Successes: 1, Issued: 3}, half.Probe)
	require.True(t, half.SitePaused)

	closed := page.Breakers[3]
	require.Equal(t, WindowMetrics{Total: 10, Success: 7, Risk: 3, CaptchaIdentities: 1, RiskRatio: 0.3, SuccessRatio: 0.7}, closed.Window)

	// Pagination with a stable keyset.
	var ids []string
	token := ""
	for range 5 {
		p, err := svc.List(ctx, admin, env.Namespace, ListFilter{PageSize: 3, PageToken: token})
		require.NoError(t, err)
		require.Equal(t, 4, p.Total)
		ids = append(ids, groupIDs(p)...)
		token = p.NextPageToken
		if token == "" {
			break
		}
	}
	require.Equal(t, groupIDs(page), ids)

	tests := []struct {
		name   string
		p      *authz.Principal
		filter ListFilter
		want   []string
	}{
		{name: "state filter", p: admin, filter: ListFilter{States: []string{StateOpen, StateHalfOpen}}, want: []string{env.Search.ID, env.DefaultB.ID}},
		{name: "client filter", p: admin, filter: ListFilter{Client: "app"}, want: []string{env.AppDefaultA.ID}},
		{name: "site filter", p: admin, filter: ListFilter{Site: "beta"}, want: []string{env.DefaultB.ID}},
		{name: "site restricted viewer", p: breakertest.User("usr_b", authz.RoleViewer, env.SiteB.ID), want: []string{env.DefaultB.ID}},
		{name: "admin token", p: breakertest.Token(t, "tok_admin", "admin"), filter: ListFilter{States: []string{StateClosed}}, want: []string{env.AppDefaultA.ID, env.DefaultA.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := svc.List(ctx, tc.p, env.Namespace, tc.filter)
			require.NoError(t, err)
			require.Equal(t, tc.want, groupIDs(p))
			require.Equal(t, len(tc.want), p.Total)
		})
	}

	errTests := []struct {
		name   string
		p      *authz.Principal
		filter ListFilter
		code   connect.Code
	}{
		{name: "no binding", p: breakertest.User("usr_none", ""), code: connect.CodePermissionDenied},
		{name: "token without breaker scope", p: breakertest.Token(t, "tok_lease", "lease:acquire"), code: connect.CodePermissionDenied},
		{name: "inaccessible site filter", p: breakertest.User("usr_b", authz.RoleViewer, env.SiteB.ID), filter: ListFilter{Site: "alpha"}, code: connect.CodePermissionDenied},
		{name: "unknown site", p: admin, filter: ListFilter{Site: "gamma"}, code: connect.CodeInvalidArgument},
		{name: "bad page token", p: admin, filter: ListFilter{PageToken: "!!"}, code: connect.CodeInvalidArgument},
		{name: "nil principal", p: nil, code: connect.CodeUnauthenticated},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.List(ctx, tc.p, env.Namespace, tc.filter)
			requireCode(t, err, tc.code)
		})
	}

	st, err := svc.Get(ctx, breakertest.User("usr_b", authz.RoleViewer, env.SiteB.ID), env.DefaultB.ID)
	require.NoError(t, err)
	require.Equal(t, StateHalfOpen, st.State)
	_, err = svc.Get(ctx, breakertest.User("usr_b", authz.RoleViewer, env.SiteB.ID), env.Search.ID)
	requireCode(t, err, connect.CodePermissionDenied)

	// The probe issuance count expires with its 10 s window.
	env.Clock.Advance(10 * time.Second)
	st, err = svc.Get(ctx, admin, env.DefaultB.ID)
	require.NoError(t, err)
	require.Zero(t, st.Probe.Issued)
}

func TestListEvents(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	start := env.Clock.Now()
	admin := breakertest.User("usr_admin", authz.RoleAdmin)

	// alpha/search: open, close; beta/_default: open; beta: pause.
	_, err := svc.Open(ctx, admin, env.Search.ID, "10m", "first")
	require.NoError(t, err)
	env.Clock.Advance(time.Second)
	_, err = svc.Close(ctx, admin, env.Search.ID, "second")
	require.NoError(t, err)
	env.Clock.Advance(time.Second)
	_, err = svc.Open(ctx, admin, env.DefaultB.ID, "", "third")
	require.NoError(t, err)
	env.Clock.Advance(time.Second)
	_, err = svc.SetSitePaused(ctx, admin, env.Namespace, "beta", true, "fourth")
	require.NoError(t, err)

	page, err := svc.ListEvents(ctx, admin, env.Namespace, EventFilter{})
	require.NoError(t, err)
	require.Empty(t, page.NextPageToken)
	require.Len(t, page.Events, 4)
	reasons := func(evs []Event) []string {
		out := make([]string, 0, len(evs))
		for _, e := range evs {
			out = append(out, e.Reason)
		}
		return out
	}
	require.Equal(t, []string{"fourth", "third", "second", "first"}, reasons(page.Events))
	ev := page.Events[3]
	require.Equal(t, "alpha", ev.Site)
	require.Equal(t, "search", ev.EndpointGroup)
	require.Equal(t, "web", ev.Client)
	require.Equal(t, TriggerManual, ev.Trigger)
	require.Equal(t, "user:usr_admin", ev.Actor)
	require.NotNil(t, ev.OpenUntil)
	require.Equal(t, start.Add(10*time.Minute), ev.OpenUntil.UTC())
	require.Equal(t, start, ev.CreatedAt)
	require.Contains(t, ev.Metrics, "total")
	sw := page.Events[0]
	require.Equal(t, TriggerSiteSwitch, sw.Trigger)
	require.Empty(t, sw.EndpointGroup)
	require.Empty(t, sw.Metrics)

	var all []string
	token := ""
	for range 10 {
		p, err := svc.ListEvents(ctx, admin, env.Namespace, EventFilter{PageSize: 3, PageToken: token})
		require.NoError(t, err)
		all = append(all, reasons(p.Events)...)
		token = p.NextPageToken
		if token == "" {
			break
		}
	}
	require.Equal(t, []string{"fourth", "third", "second", "first"}, all)

	mid := start.Add(time.Second)
	end := start.Add(2 * time.Second)
	tests := []struct {
		name   string
		p      *authz.Principal
		filter EventFilter
		want   []string
	}{
		{name: "site", p: admin, filter: EventFilter{Site: "beta"}, want: []string{"fourth", "third"}},
		{name: "group", p: admin, filter: EventFilter{EndpointGroupID: env.Search.ID}, want: []string{"second", "first"}},
		{name: "trigger", p: admin, filter: EventFilter{Trigger: TriggerSiteSwitch}, want: []string{"fourth"}},
		{name: "time range", p: admin, filter: EventFilter{Start: &mid, End: &end}, want: []string{"second"}},
		{name: "site restricted viewer", p: breakertest.User("usr_a", authz.RoleViewer, env.SiteA.ID), want: []string{"second", "first"}},
		{name: "admin token", p: breakertest.Token(t, "tok_admin", "admin"), filter: EventFilter{Site: "alpha"}, want: []string{"second", "first"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := svc.ListEvents(ctx, tc.p, env.Namespace, tc.filter)
			require.NoError(t, err)
			require.Equal(t, tc.want, reasons(p.Events))
		})
	}

	errTests := []struct {
		name   string
		p      *authz.Principal
		filter EventFilter
		code   connect.Code
	}{
		{name: "no binding", p: breakertest.User("usr_none", ""), code: connect.CodePermissionDenied},
		{name: "inaccessible site", p: breakertest.User("usr_a", authz.RoleViewer, env.SiteA.ID), filter: EventFilter{Site: "beta"}, code: connect.CodePermissionDenied},
		{name: "unknown site", p: admin, filter: EventFilter{Site: "gamma"}, code: connect.CodeInvalidArgument},
		{name: "unknown trigger", p: admin, filter: EventFilter{Trigger: "magic"}, code: connect.CodeInvalidArgument},
		{name: "inverted range", p: admin, filter: EventFilter{Start: &end, End: &mid}, code: connect.CodeInvalidArgument},
		{name: "bad token", p: admin, filter: EventFilter{PageToken: "%%%"}, code: connect.CodeInvalidArgument},
		{name: "nil principal", code: connect.CodeUnauthenticated},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.ListEvents(ctx, tc.p, env.Namespace, tc.filter)
			requireCode(t, err, tc.code)
		})
	}
}
