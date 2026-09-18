package identitysvc_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identity"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// listFixture holds identities of both sites of the default namespace.
type listFixture struct {
	env   *identitysvctest.Env
	web   identitysvc.IdentityType
	alt   identitysvc.IdentityType
	byKey map[string]string // label "k" → identity ID
}

func newListFixture(t *testing.T) *listFixture {
	t.Helper()
	env := identitysvctest.NewEnv(t)
	f := &listFixture{env: env, byKey: map[string]string{}}
	f.web = env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	f.alt = env.CreateType(t, env.SiteB, strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "alt_cookie", 1))
	var a, b []string
	for i := range 12 {
		row := fmt.Sprintf(`{"cookies":"sessionid=a%d","_labels":{"k":"a%d"},"_region":"%s","_tags":%s,"_account":"%s"}`,
			i, i, []string{"US", "JP"}[i%2], []string{`["x"]`, `["x","y"]`, `[]`}[i%3], []string{"alice", "bob", ""}[i%3])
		a = append(a, row)
	}
	for i := range 3 {
		b = append(b, fmt.Sprintf(`{"cookies":"sessionid=b%d","_labels":{"k":"b%d"}}`, i, i))
	}
	importJSONL(t, env, "web_cookie", strings.Join(a, "\n"))
	_, err := env.Service.ImportIdentities(context.Background(), env.Owner(env.NS), env.NS, identitysvc.ImportInput{
		Site: "market", Type: "alt_cookie", Format: "jsonl", Data: strings.Join(b, "\n"),
	})
	require.NoError(t, err)
	rows, err := env.Pool.Query(context.Background(), `SELECT id, labels->>'k' FROM identities`)
	require.NoError(t, err)
	for rows.Next() {
		var id, k string
		require.NoError(t, rows.Scan(&id, &k))
		f.byKey[k] = id
	}
	rows.Close()
	// Distinct, deterministic timestamps and scores.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 12 {
		id := f.byKey[fmt.Sprintf("a%d", i)]
		env.Exec(t, `UPDATE identities SET created_at = $1, updated_at = $2, state_changed_at = $3, last_used_at = $4 WHERE id = $5`,
			base.Add(time.Duration(i)*time.Minute), base.Add(time.Duration(12-i)*time.Minute),
			base.Add(time.Duration(i%4)*time.Minute), nullableTime(i%5 != 0, base.Add(time.Duration(i)*time.Hour)), id)
		if i%3 != 2 {
			env.Exec(t, `INSERT INTO hot_state_snapshots (site_id, subject, subject_id, score, samples) VALUES ($1, 'ig', $2, $3, $4)`,
				env.SiteA.ID, id, float64(10*i%100), i)
		}
	}
	env.Exec(t, `UPDATE identities SET state = 'retired' WHERE id = $1`, f.byKey["a11"])
	env.Exec(t, `UPDATE identities SET state = 'banned' WHERE id = $1`, f.byKey["a10"])
	return f
}

func nullableTime(ok bool, t time.Time) *time.Time {
	if !ok {
		return nil
	}
	return &t
}

func (f *listFixture) keys(ids []identitysvc.Identity) []string {
	rev := map[string]string{}
	for k, id := range f.byKey {
		rev[id] = k
	}
	out := make([]string, 0, len(ids))
	for _, i := range ids {
		out = append(out, rev[i.ID])
	}
	return out
}

func TestListIdentitiesFilters(t *testing.T) {
	f := newListFixture(t)
	env := f.env
	ctx := context.Background()
	viewer := env.Role(authz.RoleViewer)
	minScore, maxScore := 40.0, 70.0

	tests := []struct {
		name   string
		p      *authz.Principal
		filter identitysvc.IdentityFilter
		want   []string
	}{
		{"default excludes retired", viewer, identitysvc.IdentityFilter{},
			[]string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "b0", "b1", "b2"}},
		{"include retired", viewer, identitysvc.IdentityFilter{Site: "shop", IncludeRetired: true},
			[]string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"}},
		{"states", viewer, identitysvc.IdentityFilter{States: []string{"banned", "retired"}}, []string{"a10", "a11"}},
		{"type", viewer, identitysvc.IdentityFilter{Type: "alt_cookie"}, []string{"b0", "b1", "b2"}},
		{"tags", viewer, identitysvc.IdentityFilter{Tags: []string{"x", "y"}}, []string{"a1", "a4", "a7", "a10"}},
		{"account", viewer, identitysvc.IdentityFilter{AccountRef: "bob"}, []string{"a1", "a4", "a7", "a10"}},
		{"region", viewer, identitysvc.IdentityFilter{Region: "JP", IncludeRetired: true}, []string{"a1", "a3", "a5", "a7", "a9", "a11"}},
		{"search label", viewer, identitysvc.IdentityFilter{Search: "b1"}, []string{"b1"}},
		{"search id prefix", viewer, identitysvc.IdentityFilter{Search: f.byKey["a3"][:30]}, []string{"a3"}},
		// Scores: a0=0 a1=10 a3=30 a4=40 a6=60 a7=70 a9=90 a10=0; others default to 70.
		{"score range", viewer, identitysvc.IdentityFilter{Site: "shop", MinScore: &minScore, MaxScore: &maxScore},
			[]string{"a2", "a4", "a5", "a6", "a7", "a8"}},
		{"site restricted", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.IdentityFilter{}, []string{"b0", "b1", "b2"}},
		{"site scoped token", env.Token(t, "identity:write:market"), identitysvc.IdentityFilter{}, []string{"b0", "b1", "b2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := env.Service.ListIdentities(ctx, tc.p, env.NS, identitysvc.IdentityQuery{Filter: tc.filter, PageSize: 100})
			require.NoError(t, err)
			require.ElementsMatch(t, tc.want, f.keys(page.Identities))
			require.Equal(t, len(tc.want), page.Total)
			require.Empty(t, page.NextPageToken)
		})
	}

	t.Run("identity view", func(t *testing.T) {
		page, err := env.Service.ListIdentities(ctx, viewer, env.NS, identitysvc.IdentityQuery{
			Filter: identitysvc.IdentityFilter{Search: "a4"},
		})
		require.NoError(t, err)
		require.Len(t, page.Identities, 1)
		i := page.Identities[0]
		require.Equal(t, "shop", i.SiteName)
		require.Equal(t, "default", i.NamespaceName)
		require.Equal(t, "web", i.Client)
		require.Equal(t, "web_cookie", i.TypeName)
		require.Equal(t, "bob", i.AccountRef)
		require.NotEmpty(t, i.AccountID)
		require.Equal(t, 40.0, i.GlobalScore)
		require.Equal(t, 4, i.GlobalSamples)
		require.Equal(t, map[string]string{"k": "a4"}, i.Labels)
		require.Equal(t, 1, i.PayloadVersion)
	})

	errTests := []struct {
		name   string
		p      *authz.Principal
		query  identitysvc.IdentityQuery
		reason apperr.Reason
	}{
		{"no permission", env.NoRole(), identitysvc.IdentityQuery{}, apperr.ReasonPermissionDenied},
		{"site not accessible", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{Site: "shop"}}, apperr.ReasonPermissionDenied},
		{"unknown site", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{Site: "nope"}}, apperr.ReasonSiteUnknown},
		{"bad state", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{States: []string{"zombie"}}}, apperr.ReasonInvalidArgument},
		{"bad tag", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{Tags: []string{"a b"}}}, apperr.ReasonInvalidArgument},
		{"score order", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{MinScore: &maxScore, MaxScore: &minScore}}, apperr.ReasonInvalidArgument},
		{"score bound", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{MinScore: ptr(101.0)}}, apperr.ReasonInvalidArgument},
		{"long search", viewer, identitysvc.IdentityQuery{Filter: identitysvc.IdentityFilter{Search: strings.Repeat("s", 300)}}, apperr.ReasonInvalidArgument},
		{"bad order", viewer, identitysvc.IdentityQuery{OrderBy: "name"}, apperr.ReasonInvalidArgument},
		{"bad token", viewer, identitysvc.IdentityQuery{PageToken: "%%%"}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.ListIdentities(ctx, tc.p, env.NS, tc.query)
			requireReason(t, err, tc.reason)
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestListIdentitiesPagination(t *testing.T) {
	f := newListFixture(t)
	env := f.env
	ctx := context.Background()
	viewer := env.Role(authz.RoleViewer)
	filter := identitysvc.IdentityFilter{Site: "shop", IncludeRetired: true}

	for _, order := range []string{"", identitysvc.OrderCreatedAt, identitysvc.OrderUpdatedAt, identitysvc.OrderStateChangedAt, identitysvc.OrderLastUsedAt, identitysvc.OrderScore} {
		for _, desc := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s desc=%v", order, desc), func(t *testing.T) {
				all, err := env.Service.ListIdentities(ctx, viewer, env.NS, identitysvc.IdentityQuery{Filter: filter, OrderBy: order, Descending: desc, PageSize: 100})
				require.NoError(t, err)
				require.Len(t, all.Identities, 12)
				var paged []identitysvc.Identity
				token := ""
				for pages := 0; ; pages++ {
					require.Less(t, pages, 10)
					page, err := env.Service.ListIdentities(ctx, viewer, env.NS, identitysvc.IdentityQuery{
						Filter: filter, OrderBy: order, Descending: desc, PageSize: 5, PageToken: token,
					})
					require.NoError(t, err)
					require.Equal(t, 12, page.Total)
					paged = append(paged, page.Identities...)
					if page.NextPageToken == "" {
						break
					}
					token = page.NextPageToken
				}
				require.Equal(t, f.keys(all.Identities), f.keys(paged))
				if order == identitysvc.OrderCreatedAt {
					keys := f.keys(paged)
					want := []string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11"}
					if desc {
						slices.Reverse(want)
					}
					require.Equal(t, want, keys)
				}
			})
		}
	}

	t.Run("token of another order is rejected", func(t *testing.T) {
		page, err := env.Service.ListIdentities(ctx, viewer, env.NS, identitysvc.IdentityQuery{Filter: filter, PageSize: 2})
		require.NoError(t, err)
		require.NotEmpty(t, page.NextPageToken)
		_, err = env.Service.ListIdentities(ctx, viewer, env.NS, identitysvc.IdentityQuery{
			Filter: filter, PageSize: 2, PageToken: page.NextPageToken, OrderBy: identitysvc.OrderScore,
		})
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})
}

func TestGetIdentity(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=secret-session-1234","user_agent":"UA/1","signature":"token-abcdef","_labels":{"k":"g"}}`)
	id := identityBySession(t, env, "g")
	env.Exec(t, `INSERT INTO state_events (id, tenant_id, namespace_id, site_id, subject_kind, subject_id, endpoint_group_id, from_state, to_state, action)
		VALUES ($1, $2, $3, $4, 'identity', $5, $6, 'active', 'active', 'cooldown')`,
		idgen.New(idgen.StateEvent), env.TenantID, env.NS.ID, env.SiteA.ID, id, env.SiteA.ID+"_web_default")

	t.Run("masked for viewers", func(t *testing.T) {
		detail, err := env.Service.GetIdentity(ctx, env.Role(authz.RoleViewer), id, true)
		require.NoError(t, err)
		require.False(t, detail.Revealed)
		require.Equal(t, map[string]any{"sessionid": identity.MaskPrefix + "1234"}, detail.Payload["cookies"])
		require.Equal(t, identity.MaskPrefix+"cdef", detail.Payload["signature"])
		require.Equal(t, "UA/1", detail.Payload["user_agent"])
		require.Len(t, detail.RecentEvents, 1)
		require.Equal(t, "cooldown", detail.RecentEvents[0].Action)
		require.Equal(t, "shop", detail.RecentEvents[0].SiteName)
		require.Equal(t, "_default", detail.RecentEvents[0].EndpointGroupName)
		require.Equal(t, env.SiteA.ID, detail.Site.ID)
		_, revealed := env.Audit.Last("identity.reveal")
		require.False(t, revealed)
	})

	t.Run("reveal with identity:reveal", func(t *testing.T) {
		detail, err := env.Service.GetIdentity(ctx, env.Role(authz.RoleOperator, authz.PermIdentityReveal), id, true)
		require.NoError(t, err)
		require.True(t, detail.Revealed)
		require.Equal(t, "token-abcdef", detail.Payload["signature"])
		entry, ok := env.Audit.Last("identity.reveal")
		require.True(t, ok)
		require.Equal(t, id, entry.ResourceID)
	})

	t.Run("no reveal requested", func(t *testing.T) {
		detail, err := env.Service.GetIdentity(ctx, env.Owner(env.NS), id, false)
		require.NoError(t, err)
		require.False(t, detail.Revealed)
	})

	t.Run("payload unavailable fails closed", func(t *testing.T) {
		svc := identitysvc.NewService(env.Pool, nil, env.Pepper, env.Catalog, env.Hot, env.Ops, nil, nil, nil)
		detail, err := svc.GetIdentity(ctx, env.Owner(env.NS), id, true)
		require.NoError(t, err)
		require.Nil(t, detail.Payload)
		require.False(t, detail.Revealed)
	})

	t.Run("denials", func(t *testing.T) {
		_, err := env.Service.GetIdentity(ctx, env.Stranger(), id, false)
		requireReason(t, err, apperr.ReasonNotFound)
		_, err = env.Service.GetIdentity(ctx, env.SiteRole(authz.RoleViewer, env.SiteB), id, false)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.GetIdentity(ctx, env.Token(t, "identity:write:market"), id, false)
		requireReason(t, err, apperr.ReasonScopeMissing)
		_, err = env.Service.GetIdentity(ctx, env.Owner(env.NS), "idt_missing", false)
		requireReason(t, err, apperr.ReasonNotFound)
	})

	t.Run("resolve", func(t *testing.T) {
		ref, err := env.Service.ResolveIdentity(ctx, env.Role(authz.RoleViewer), id, authz.PermIdentityRead)
		require.NoError(t, err)
		require.Equal(t, env.SiteA.ID, ref.Site.ID)
		require.Equal(t, env.NS.ID, ref.Namespace.ID)
		_, err = env.Service.ResolveIdentity(ctx, env.Role(authz.RoleViewer), id, authz.PermIdentityWrite)
		requireReason(t, err, apperr.ReasonPermissionDenied)
	})
}

func TestUpdateIdentityPayload(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	importJSONL(t, env, "web_cookie", "{\"cookies\":\"sessionid=u1\",\"_labels\":{\"k\":\"u1\"}}\n{\"cookies\":\"sessionid=u2\",\"_labels\":{\"k\":\"u2\"}}")
	id := identityBySession(t, env, "u1")
	env.Exec(t, `UPDATE identities SET state = 'expired' WHERE id = $1`, id)
	op := env.Role(authz.RoleOperator)

	t.Run("changed payload", func(t *testing.T) {
		env.Hot.Reset()
		got, err := env.Service.UpdateIdentityPayload(ctx, op, id, map[string]any{"cookies": map[string]any{"sessionid": "u1", "new": "1"}})
		require.NoError(t, err)
		require.Equal(t, 2, got.PayloadVersion)
		require.Equal(t, identitysvc.StatePending, got.State)
		require.Equal(t, []string{id}, env.Hot.SyncedIDs(identitysvc.SyncOptions{ResetHealth: true, ResetFailures: true}))
		entry, ok := env.Audit.Last("identity.update_payload")
		require.True(t, ok)
		require.Equal(t, true, entry.Details["changed"])
		require.Equal(t, "expired", entry.Details["from_state"])
		require.Equal(t, 1, env.QueryInt(t, `SELECT count(*) FROM state_events WHERE subject_id = $1`, id))
	})

	t.Run("unchanged payload", func(t *testing.T) {
		env.Hot.Reset()
		got, err := env.Service.UpdateIdentityPayload(ctx, op, id, map[string]any{"cookies": "new=1; sessionid=u1"})
		require.NoError(t, err)
		require.Equal(t, 2, got.PayloadVersion)
		require.Empty(t, env.Hot.Calls())
	})

	t.Run("unique key of another identity", func(t *testing.T) {
		_, err := env.Service.UpdateIdentityPayload(ctx, op, id, map[string]any{"cookies": "sessionid=u2"})
		requireReason(t, err, apperr.ReasonAlreadyExists)
	})

	t.Run("stale unique hash is rewritten", func(t *testing.T) {
		env.Exec(t, `UPDATE identities SET unique_hash = '\x00'::bytea WHERE id = $1`, id)
		got, err := env.Service.UpdateIdentityPayload(ctx, op, id, map[string]any{"cookies": "new=1; sessionid=u1"})
		require.NoError(t, err)
		require.Equal(t, 2, got.PayloadVersion, "same payload, only the unique hash is rewritten")
		res := importJSONL(t, env, "web_cookie", `{"cookies":"new=1; sessionid=u1"}`)
		require.Equal(t, 1, res.Unchanged)
		require.Zero(t, res.Created)
	})

	t.Run("stale catalog type is reloaded", func(t *testing.T) {
		spec := strings.Replace(identitysvctest.WebCookieYAML, "activation: probe", "activation: immediate", 1)
		_, err := env.Service.UpdateIdentityType(ctx, env.Owner(env.NS), web.ID, spec)
		require.NoError(t, err)
		env.Exec(t, `UPDATE identities SET state = 'expired' WHERE id = $1`, id)
		got, err := env.Service.UpdateIdentityPayload(ctx, op, id, map[string]any{"cookies": "new=2; sessionid=u1"})
		require.NoError(t, err)
		require.Equal(t, 3, got.PayloadVersion)
		require.Equal(t, identitysvc.StateActive, got.State, "the stored immediate activation applies, not the stale snapshot")
	})

	errTests := []struct {
		name    string
		p       *authz.Principal
		id      string
		payload map[string]any
		reason  apperr.Reason
	}{
		{"invalid payload", op, id, map[string]any{"user_agent": 5}, apperr.ReasonInvalidArgument},
		{"nil payload", op, id, nil, apperr.ReasonInvalidArgument},
		{"viewer", env.Role(authz.RoleViewer), id, map[string]any{"cookies": "sessionid=u1"}, apperr.ReasonPermissionDenied},
		{"stranger", env.Stranger(), id, map[string]any{"cookies": "sessionid=u1"}, apperr.ReasonNotFound},
		{"missing", op, "idt_missing", map[string]any{"cookies": "sessionid=u1"}, apperr.ReasonNotFound},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.UpdateIdentityPayload(ctx, tc.p, tc.id, tc.payload)
			requireReason(t, err, tc.reason)
		})
	}
}

func TestUpdateIdentity(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	importJSONL(t, env, "web_cookie", `{"cookies":"sessionid=m1","_labels":{"k":"m1"},"_account":"old"}`)
	id := identityBySession(t, env, "m1")
	op := env.Role(authz.RoleOperator)

	env.Hot.Reset()
	got, err := env.Service.UpdateIdentity(ctx, op, id, identitysvc.IdentityUpdate{
		Region: ptr("SG"), SetTags: true, Tags: []string{"a", "a", "b"}, SetLabels: true,
		Labels: map[string]string{"k": "m1", "team": "x"}, AccountRef: ptr("new-account"),
	})
	require.NoError(t, err)
	require.Equal(t, "SG", got.Region)
	require.Equal(t, []string{"a", "b"}, got.Tags)
	require.Equal(t, map[string]string{"k": "m1", "team": "x"}, got.Labels)
	require.Equal(t, "new-account", got.AccountRef)
	calls := env.Hot.Calls()
	require.Len(t, calls, 2)
	require.Equal(t, "accounts", calls[0].Method)
	require.Len(t, calls[0].IDs, 2, "old and new account are synchronized")
	require.Equal(t, []string{id}, calls[1].IDs)

	got, err = env.Service.UpdateIdentity(ctx, op, id, identitysvc.IdentityUpdate{AccountRef: ptr(""), SetTags: true})
	require.NoError(t, err)
	require.Empty(t, got.AccountRef)
	require.Empty(t, got.AccountID)
	require.Empty(t, got.Tags)
	require.Equal(t, "SG", got.Region)

	got, err = env.Service.UpdateIdentity(ctx, op, id, identitysvc.IdentityUpdate{AccountRef: ptr("new-account")})
	require.NoError(t, err)
	require.Equal(t, "new-account", got.AccountRef)
	_, ok := env.Audit.Last("identity.update")
	require.True(t, ok)

	errTests := []struct {
		name   string
		p      *authz.Principal
		in     identitysvc.IdentityUpdate
		reason apperr.Reason
	}{
		{"too many tags", op, identitysvc.IdentityUpdate{SetTags: true, Tags: manyStrings(identitysvc.MaxTags + 1)}, apperr.ReasonInvalidArgument},
		{"bad label value", op, identitysvc.IdentityUpdate{SetLabels: true, Labels: map[string]string{"k": strings.Repeat("v", 300)}}, apperr.ReasonInvalidArgument},
		{"bad region", op, identitysvc.IdentityUpdate{Region: ptr("a\x00b")}, apperr.ReasonInvalidArgument},
		{"bad account", op, identitysvc.IdentityUpdate{AccountRef: ptr(strings.Repeat("r", 300))}, apperr.ReasonInvalidArgument},
		{"viewer", env.Role(authz.RoleViewer), identitysvc.IdentityUpdate{Region: ptr("US")}, apperr.ReasonPermissionDenied},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.UpdateIdentity(ctx, tc.p, id, tc.in)
			requireReason(t, err, tc.reason)
		})
	}
}

func manyStrings(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("t%d", i)
	}
	return out
}
