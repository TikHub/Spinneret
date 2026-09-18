package breaker

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakertest"
)

func TestRuntimeContent(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	now := env.Clock.Now()
	admin := breakertest.User("usr_admin", authz.RoleAdmin)

	content, version, err := svc.RuntimeContent(ctx, breakertest.NamespaceID, KindBreakers)
	require.NoError(t, err)
	require.Zero(t, version)
	require.JSONEq(t, `{"namespace":"prod","version":0,"sites":{
		"alpha":{"paused":false,"groups":{}},"beta":{"paused":false,"groups":{}}}}`, content)

	_, err = svc.Open(ctx, admin, env.Search.ID, "15m", "incident")
	require.NoError(t, err)
	_, err = svc.Open(ctx, admin, env.DefaultB.ID, "", "")
	require.NoError(t, err)
	// A half-open breaker is listed; a stale brko member of a closed breaker
	// and a member of an unknown group are not.
	env.SetBreaker(t, env.AppDefaultA, map[string]string{"st": "half_open", "rsn": "probing"})
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Sadd().Key(env.Keys.OpenBreakers(env.SiteA.Key)).
		Member(strconv.FormatInt(env.DefaultA.Key, 10), "777777", "not-a-number").Build()).Error())

	version, err = svc.RuntimeVersion(ctx, breakertest.NamespaceID, KindBreakers)
	require.NoError(t, err)
	require.Equal(t, int64(2), version)
	content, version, err = svc.RuntimeContent(ctx, breakertest.NamespaceID, KindBreakers)
	require.NoError(t, err)
	require.Equal(t, int64(2), version)
	openUntil := now.Add(15 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	require.JSONEq(t, `{"namespace":"prod","version":2,"sites":{
		"alpha":{"paused":false,"groups":{
			"web/search":{"state":"open","open_until":"`+openUntil+`","manual":true,"reason":"incident"},
			"app/_default":{"state":"half_open","open_until":null,"manual":false,"reason":"probing"}}},
		"beta":{"paused":false,"groups":{
			"web/_default":{"state":"open","open_until":null,"manual":true,"reason":""}}}}}`, content)

	// Cached per version: a change without a version bump is not visible.
	env.SetBreaker(t, env.AppDefaultA, map[string]string{"st": "closed"})
	cached, _, err := svc.RuntimeContent(ctx, breakertest.NamespaceID, KindBreakers)
	require.NoError(t, err)
	require.Equal(t, content, cached)

	// Site switches bump both kinds.
	_, err = svc.SetSitePaused(ctx, admin, env.Namespace, "beta", true, "maintenance")
	require.NoError(t, err)
	content, version, err = svc.RuntimeContent(ctx, breakertest.NamespaceID, KindBreakers)
	require.NoError(t, err)
	require.Equal(t, int64(3), version)
	var doc BreakersContent
	require.NoError(t, json.Unmarshal([]byte(content), &doc))
	require.True(t, doc.Sites["beta"].Paused)
	require.NotContains(t, doc.Sites["alpha"].Groups, "app/_default")

	content, version, err = svc.RuntimeContent(ctx, breakertest.NamespaceID, KindSiteSwitches)
	require.NoError(t, err)
	require.Equal(t, int64(1), version)
	pausedAt := now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	require.JSONEq(t, `{"namespace":"prod","version":1,"sites":{
		"alpha":{"paused":false,"reason":"","paused_at":null},
		"beta":{"paused":true,"reason":"maintenance","paused_at":"`+pausedAt+`"}}}`, content)

	_, _, err = svc.RuntimeContent(ctx, breakertest.NamespaceID, "configs")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.RuntimeVersion(ctx, breakertest.NamespaceID, "")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, _, err = svc.RuntimeContent(ctx, "ns_missing", KindSiteSwitches)
	requireCode(t, err, connect.CodeNotFound)

	// A corrupt version is an internal error.
	require.NoError(t, env.Redis.Do(ctx, env.Redis.B().Hset().Key(env.Keys.RuntimeVersions(breakertest.NamespaceID)).
		FieldValue().FieldValue(KindBreakers, "x").Build()).Error())
	_, _, err = svc.RuntimeContent(ctx, breakertest.NamespaceID, KindBreakers)
	requireCode(t, err, connect.CodeInternal)
}

func TestRuntimeContentEmptyNamespace(t *testing.T) {
	t.Parallel()
	env := breakertest.New(t)
	svc := newService(t, env)
	ctx := breakertest.Context(t)
	_, err := env.Pool.Exec(ctx, `INSERT INTO namespaces (id, tenant_id, name) VALUES ('ns_empty', $1, 'empty')`, breakertest.TenantID)
	require.NoError(t, err)
	content, version, err := svc.RuntimeContent(ctx, "ns_empty", KindBreakers)
	require.NoError(t, err)
	require.Zero(t, version)
	require.JSONEq(t, `{"namespace":"empty","version":0,"sites":{}}`, content)
}

func TestRuntimeCacheBounds(t *testing.T) {
	t.Parallel()
	c := newRuntimeCache()
	k := runtimeKey{namespaceID: "ns", kind: KindBreakers}
	c.put(k, 2, "v2")
	got, ok := c.get(k, 2)
	require.True(t, ok)
	require.Equal(t, "v2", got)
	_, ok = c.get(k, 1)
	require.False(t, ok)
	// The latest put wins, so a reset version counter never serves a document
	// rendered before the reset.
	c.put(k, 1, "v1")
	got, ok = c.get(k, 1)
	require.True(t, ok)
	require.Equal(t, "v1", got)
	_, ok = c.get(k, 2)
	require.False(t, ok)
	for i := range maxRuntimeCacheEntries + 10 {
		c.put(runtimeKey{namespaceID: strconv.Itoa(i), kind: KindBreakers}, 1, "x")
	}
	require.LessOrEqual(t, len(c.entries), maxRuntimeCacheEntries)
}
