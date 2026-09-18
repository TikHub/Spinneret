package configcenter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
)

func TestItemOperationsDenied(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	it := e.create(t, "crawler", "a.txt", FormatText, "v1", true)
	e.publishContent(t, it.ID, "v2")

	otherGroup := e.token(t, e.ns, "config:publish:other*")
	otherNS := e.token(t, e.otherNS, "config:publish")
	outsider := userPrincipal(e.otherNS.TenantID, authz.RoleOwner)
	desc := "d"

	ops := []struct {
		name   string
		action string // audited denial action, "" when reads are not audited
		call   func(p *authz.Principal) error
	}{
		{name: "get", call: func(p *authz.Principal) error { _, err := e.svc.GetItem(ctx, p, it.ID); return err }},
		{name: "save draft", action: ActionSaveDraft, call: func(p *authz.Principal) error {
			_, err := e.svc.SaveDraft(ctx, p, DraftRequest{ID: it.ID, Content: "x", Description: &desc})
			return err
		}},
		{name: "publish", action: ActionPublish, call: func(p *authz.Principal) error {
			_, _, err := e.svc.Publish(ctx, p, PublishRequest{ID: it.ID})
			return err
		}},
		{name: "rollback", action: ActionRollback, call: func(p *authz.Principal) error {
			_, _, err := e.svc.Rollback(ctx, p, RollbackRequest{ID: it.ID, Version: 1})
			return err
		}},
		{name: "delete", action: ActionDelete, call: func(p *authz.Principal) error { return e.svc.DeleteItem(ctx, p, it.ID) }},
		{name: "versions", call: func(p *authz.Principal) error {
			_, err := e.svc.ListVersions(ctx, p, it.ID, 10, "")
			return err
		}},
		{name: "diff", call: func(p *authz.Principal) error { _, err := e.svc.DiffVersions(ctx, p, it.ID, 1, 2); return err }},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			before := len(e.audit.find(op.action, audit.ResultDenied))
			requireReason(t, op.call(otherGroup), apperr.ReasonScopeMissing)
			if op.action != "" {
				denied := e.audit.find(op.action, audit.ResultDenied)
				require.Len(t, denied, before+1)
				require.Equal(t, it.ID, denied[len(denied)-1].ResourceID)
				require.Equal(t, "crawler/a.txt", denied[len(denied)-1].ResourceName)
			}
			// Items of other tenants or namespaces do not exist for the caller.
			requireReason(t, op.call(otherNS), apperr.ReasonNotFound)
			requireReason(t, op.call(outsider), apperr.ReasonNotFound)
			requireReason(t, op.call(nil), apperr.ReasonSessionInvalid)
		})
	}

	// The item is untouched.
	got, err := e.svc.GetItem(ctx, e.viewer, it.ID)
	require.NoError(t, err)
	require.Equal(t, int32(2), got.CurrentVersion)
	require.False(t, got.HasDraft)
}

func TestPublishAndRollbackInputValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	it := e.create(t, "g", "k.json", FormatJSON, `{"a":1}`, true)
	long := strings.Repeat("c", MaxCommentLen+1)

	_, _, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID, ExpectedVersion: -1})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID, Comment: long})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 0})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 1, Comment: long})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.DiffVersions(ctx, e.admin, it.ID, 0, -1)
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Rolling back re-validates the old content against the current schema.
	schema := `{"type":"object","required":["b"]}`
	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: it.ID, Content: `{"b":2}`, SchemaJSON: &schema})
	require.NoError(t, err)
	_, v, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID})
	require.NoError(t, err)
	require.Equal(t, int32(2), v)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 1})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	require.Contains(t, err.Error(), "schema")

	// A pinned secret version that does not exist blocks the rollback too.
	e.addSecret(t, e.ns, "api/key", 1, "k")
	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: it.ID, Content: `{"b":"${secret:api/key#1}"}`})
	require.NoError(t, err)
	_, v, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID})
	require.NoError(t, err)
	e.publishContent(t, it.ID, `{"b":"plain"}`)
	e.exec(t, `DELETE FROM secret_versions WHERE version = 1`)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: v})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	require.Contains(t, err.Error(), "api/key#1")
}

func TestListVersionsResponseBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{MaxResponseBytes: 1_000})
	ctx := context.Background()
	it := e.create(t, "g", "big.txt", FormatText, strings.Repeat("a", 600), true)
	e.publishContent(t, it.ID, strings.Repeat("b", 600))
	e.publishContent(t, it.ID, strings.Repeat("c", 600))

	var seen []int32
	token := ""
	for range 5 {
		page, err := e.svc.ListVersions(ctx, e.viewer, it.ID, 10, token)
		require.NoError(t, err)
		require.Equal(t, 3, page.Total)
		require.Len(t, page.Versions, 1, "each page stays within the byte budget")
		seen = append(seen, page.Versions[0].Version)
		token = page.NextPageToken
		if token == "" {
			break
		}
	}
	require.Equal(t, []int32{3, 2, 1}, seen)

	_, err := e.svc.ListVersions(ctx, e.viewer, it.ID, 10, encodeCursor(versionCursor{Before: 0}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.ListVersions(ctx, e.viewer, it.ID, 10, encodeCursor(versionCursor{Before: -3}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestWatchSecretPermissionDeniedFailsWholeRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.addSecret(t, e.ns, "api/key", 1, "s3cret")
	e.create(t, "g", "plain.txt", FormatText, "plain", true)
	a := e.create(t, "g", "a.txt", FormatText, "a=${secret:api/key}", true)
	e.create(t, "g", "b.json", FormatJSON, `{"b":"${secret:api/key}"}`, true)
	refs := []WatchRef{{Group: "g", Key: "plain.txt"}, {Group: "g", Key: "a.txt"}, {Group: "g", Key: "b.json"}}

	noSecret := e.token(t, e.ns, "config:read")
	items, err := e.svc.WatchConfig(ctx, noSecret, e.ns, refs, time.Second)
	requireReason(t, err, apperr.ReasonScopeMissing)
	require.Nil(t, items, "no item is delivered when one reference is denied")
	denied := e.audit.find(ActionRead, audit.ResultDenied)
	require.Len(t, denied, 1)
	require.Equal(t, a.ID, denied[0].ResourceID)
	require.Equal(t, noSecret.Actor(), "token:"+denied[0].ActorID)
	for _, entry := range denied {
		for _, v := range entry.Details {
			require.NotContains(t, fmt.Sprint(v), "s3cret")
		}
	}

	// A token with the secret scope receives every item; the shared
	// reference is read once per request.
	node := e.token(t, e.ns, "config:read", "secret:read:prod/api/*")
	before := len(e.secrets.purposeLog())
	items, err = e.svc.WatchConfig(ctx, node, e.ns, refs, time.Second)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.False(t, items[0].HasSecretRefs)
	require.Equal(t, "a=s3cret-v1", items[1].Content)
	require.True(t, items[1].HasSecretRefs)
	require.JSONEq(t, `{"b":"s3cret-v1"}`, items[2].Content)
	require.Len(t, e.secrets.purposeLog(), before+1)
}
