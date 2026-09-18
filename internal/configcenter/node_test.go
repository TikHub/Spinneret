package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

func TestNodeReadsResolveSecrets(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.addSecret(t, e.ns, "signing/api_key", 2, `k"ey`)
	e.addSecret(t, e.ns, "shared/token", 1, "tok")

	jsonItem := e.create(t, "crawler", "search.json", FormatJSON,
		`{"key":"${secret:signing/api_key}","old":"${secret:signing/api_key#1}","plain":1}`, true)
	e.create(t, "crawler", "plain.yaml", FormatYAML, "a: 1\n", true)
	e.create(t, "crawler", "draft-only.txt", FormatText, "x", false)
	e.create(t, "shared", "t.txt", FormatText, "token=${secret:shared/token}", true)

	node := e.token(t, e.ns, "config:read", "secret:read:prod/signing/*", "secret:read:prod/shared/*")
	it, err := e.svc.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "search.json"})
	require.NoError(t, err)
	require.True(t, it.HasSecretRefs)
	require.Equal(t, "prod", it.Namespace)
	require.Equal(t, int32(1), it.Version)
	require.Equal(t, FormatJSON, it.Format)
	require.NotNil(t, it.UpdatedAt)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(it.Content), &decoded))
	require.Equal(t, `k"ey-v2`, decoded["key"])
	require.Equal(t, `k"ey-v1`, decoded["old"])
	require.Contains(t, e.secrets.purposeLog(), "config:crawler/search.json")

	items, missing, err := e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{
		{Group: "shared", Key: "t.txt"},
		{Group: "crawler", Key: "plain.yaml"},
		{Group: "crawler", Key: "draft-only.txt"},
		{Group: "crawler", Key: "nope"},
		{Group: "crawler", Key: "plain.yaml"},
		{Group: "_unknown", Key: "x"},
		{Group: "crawler", Key: "bad key"},
	})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "token=tok-v1", items[0].Content)
	require.True(t, items[0].HasSecretRefs)
	require.Equal(t, "a: 1\n", items[1].Content)
	require.False(t, items[1].HasSecretRefs)
	require.Equal(t, []Ref{
		{Group: "crawler", Key: "draft-only.txt"}, {Group: "crawler", Key: "nope"},
		{Group: "_unknown", Key: "x"}, {Group: "crawler", Key: "bad key"},
	}, missing)

	_, err = e.svc.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "draft-only.txt"})
	requireReason(t, err, apperr.ReasonNotFound)

	// Missing secret permission fails the whole request and is audited.
	limited := e.token(t, e.ns, "config:read", "secret:read:prod/shared/*")
	_, _, err = e.svc.BatchGetConfig(ctx, limited, e.ns, []Ref{{Group: "shared", Key: "t.txt"}, {Group: "crawler", Key: "search.json"}})
	requireReason(t, err, apperr.ReasonScopeMissing)
	require.Contains(t, err.Error(), "signing/api_key")
	denied := e.audit.find(ActionRead, audit.ResultDenied)
	require.Len(t, denied, 1)
	require.Equal(t, jsonItem.ID, denied[0].ResourceID)
	require.Equal(t, "signing/api_key", denied[0].Details["secret_path"])

	// Users need secret:reveal to resolve references.
	_, err = e.svc.GetConfig(ctx, e.viewer, e.ns, Ref{Group: "shared", Key: "t.txt"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	it, err = e.svc.GetConfig(ctx, e.admin, e.ns, Ref{Group: "shared", Key: "t.txt"})
	require.NoError(t, err)
	require.Equal(t, "token=tok-v1", it.Content)

	// config:read is checked per group before existence.
	groupOnly := e.token(t, e.ns, "config:read:shared", "secret:read:prod/*")
	_, err = e.svc.GetConfig(ctx, groupOnly, e.ns, Ref{Group: "crawler", Key: "nope"})
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, _, err = e.svc.BatchGetConfig(ctx, groupOnly, e.ns, []Ref{{Group: "shared", Key: "t.txt"}, {Group: "crawler", Key: "plain.yaml"}})
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = e.svc.GetConfig(ctx, e.token(t, e.otherNS, "config:read"), e.ns, Ref{Group: "shared", Key: "t.txt"})
	requireReason(t, err, apperr.ReasonScopeMissing)

	// Secrets deleted after publishing.
	e.secrets.mu.Lock()
	delete(e.secrets.values, "shared/token")
	e.secrets.mu.Unlock()
	_, err = e.svc.GetConfig(ctx, node, e.ns, Ref{Group: "shared", Key: "t.txt"})
	requireReason(t, err, apperr.ReasonFailedPrecondition)

	e.secrets.mu.Lock()
	e.secrets.failWith = errors.New("vault unavailable")
	e.secrets.mu.Unlock()
	_, err = e.svc.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "search.json"})
	require.ErrorContains(t, err, "vault unavailable")
	e.secrets.mu.Lock()
	e.secrets.failWith = apperr.Unavailable(apperr.ReasonRebuilding, 10, "busy")
	e.secrets.mu.Unlock()
	_, err = e.svc.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "search.json"})
	requireReason(t, err, apperr.ReasonRebuilding)

	// Without a secret reader, items with references cannot be served.
	noSecrets := New(Config{}, e.pool, e.cat, e.bus, nil, e.runtime, nil, nil, nil)
	_, err = noSecrets.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "search.json"})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	it, err = noSecrets.GetConfig(ctx, node, e.ns, Ref{Group: "crawler", Key: "plain.yaml"})
	require.NoError(t, err)
	require.Equal(t, "a: 1\n", it.Content)

	// Input validation.
	_, _, err = e.svc.BatchGetConfig(ctx, node, e.ns, nil)
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, err = e.svc.BatchGetConfig(ctx, node, e.ns, make([]Ref, MaxNodeItems+1))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.GetConfig(ctx, nil, e.ns, Ref{Group: "g", Key: "k"})
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestNodeReadsRuntimeItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.runtime.bump(e.ns.ID, RuntimeBreakers)
	e.runtime.bump(e.ns.ID, RuntimeBreakers)

	node := e.token(t, e.ns, "config:read")
	it, err := e.svc.GetConfig(ctx, node, e.ns, Ref{Group: RuntimeGroup, Key: RuntimeBreakers})
	require.NoError(t, err)
	require.Equal(t, int32(3), it.Version)
	require.Equal(t, FormatJSON, it.Format)
	require.JSONEq(t, `{"kind":"breakers","version":2}`, it.Content)
	require.Nil(t, it.UpdatedAt)

	items, missing, err := e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{
		{Group: RuntimeGroup, Key: RuntimeSiteSwitches}, {Group: RuntimeGroup, Key: "other"},
	})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int32(1), items[0].Version)
	require.Equal(t, []Ref{{Group: RuntimeGroup, Key: "other"}}, missing)

	// Tokens restricted to other groups cannot read the runtime group.
	_, err = e.svc.GetConfig(ctx, e.token(t, e.ns, "config:read:crawler"), e.ns, Ref{Group: RuntimeGroup, Key: RuntimeBreakers})
	requireReason(t, err, apperr.ReasonScopeMissing)
	it, err = e.svc.GetConfig(ctx, e.token(t, e.ns, "config:read:_runtime"), e.ns, Ref{Group: RuntimeGroup, Key: RuntimeBreakers})
	require.NoError(t, err)
	require.Equal(t, int32(3), it.Version)

	e.runtime.setErr(errors.New("redis down"))
	_, err = e.svc.GetConfig(ctx, node, e.ns, Ref{Group: RuntimeGroup, Key: RuntimeBreakers})
	require.ErrorContains(t, err, "redis down")
}

func TestNodeReadsResponseBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{MaxResponseBytes: 1_000})
	ctx := context.Background()
	big := strings.Repeat("x", 600)
	e.create(t, "g", "a", FormatText, big, true)
	e.create(t, "g", "b", FormatText, big, true)
	e.create(t, "g", "small", FormatText, "s", true)
	node := e.token(t, e.ns, "config:read")

	_, _, err := e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{{Group: "g", Key: "a"}, {Group: "g", Key: "b"}})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	require.Contains(t, err.Error(), "exceed 1000 bytes")

	// A single item may exceed the budget, and cached contents count too.
	items, _, err := e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{{Group: "g", Key: "a"}, {Group: "g", Key: "small"}})
	require.NoError(t, err)
	require.Len(t, items, 2)
	_, _, err = e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{{Group: "g", Key: "a"}, {Group: "g", Key: "b"}})
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Watches return a subset and the rest on the next poll.
	watched := []WatchRef{{Group: "g", Key: "a"}, {Group: "g", Key: "b"}, {Group: "g", Key: "small"}}
	first, err := e.svc.WatchConfig(ctx, node, e.ns, watched, 0)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, "a", first[0].Key)
	watched[0].Version = first[0].Version
	second, err := e.svc.WatchConfig(ctx, node, e.ns, watched, 0)
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.Equal(t, "b", second[0].Key)
	require.Equal(t, "small", second[1].Key)
}

func TestNodeReadsWithSecretsGrowBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{MaxResponseBytes: 100})
	ctx := context.Background()
	e.addSecret(t, e.ns, "big", 1, strings.Repeat("v", 200))
	e.create(t, "g", "a", FormatText, "${secret:big}", true)
	e.create(t, "g", "b", FormatText, "b", true)
	node := e.token(t, e.ns, "config:read", "secret:read:prod/*")
	_, _, err := e.svc.BatchGetConfig(ctx, node, e.ns, []Ref{{Group: "g", Key: "a"}, {Group: "g", Key: "b"}})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	items, err := e.svc.WatchConfig(ctx, node, e.ns, []WatchRef{{Group: "g", Key: "a"}, {Group: "g", Key: "b"}}, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func TestNodeUserPrincipalWithoutConfigRead(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	outsider := userPrincipal(e.otherNS.TenantID, authz.RoleOwner)
	_, err := e.svc.GetConfig(context.Background(), outsider, e.ns, Ref{Group: "g", Key: "k"})
	requireReason(t, err, apperr.ReasonPermissionDenied)
}
