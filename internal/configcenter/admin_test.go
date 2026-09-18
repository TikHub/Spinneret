package configcenter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/events"
)

func TestDraftPublishRollbackDiffLifecycle(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()

	it, err := e.svc.CreateItem(ctx, e.operator, e.ns, CreateRequest{
		Group: "crawler", Key: "search.json", Format: FormatJSON, SchemaJSON: personSchema,
		Description: "search crawler", Content: `{"name":"a"}`,
	})
	require.NoError(t, err)
	require.True(t, idValid(it.ID))
	require.Equal(t, "prod", it.NamespaceName)
	require.Zero(t, it.CurrentVersion)
	require.True(t, it.HasDraft)
	require.Equal(t, `{"name":"a"}`, *it.DraftContent)
	require.Equal(t, e.operator.Actor(), it.DraftUpdatedBy)
	require.NotNil(t, it.DraftUpdatedAt)
	require.JSONEq(t, personSchema, string(it.Schema))
	require.Len(t, e.audit.find(ActionCreate, audit.ResultOK), 1)

	// The operator cannot publish.
	_, _, err = e.svc.Publish(ctx, e.operator, PublishRequest{ID: it.ID})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.Len(t, e.audit.find(ActionPublish, audit.ResultDenied), 1)

	// Publish v1.
	it, v, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID, Comment: "first", ExpectedVersion: 0})
	require.NoError(t, err)
	require.Equal(t, int32(1), v)
	require.Equal(t, int32(1), it.CurrentVersion)
	require.False(t, it.HasDraft)
	require.Nil(t, it.DraftContent)
	require.Equal(t, `{"name":"a"}`, it.PublishedContent)
	require.Equal(t, e.admin.Actor(), it.PublishedBy)
	require.NotNil(t, it.PublishedAt)

	cfgEvents := e.events.on(events.ChannelConfig)
	require.NotEmpty(t, cfgEvents)
	var data ConfigEventData
	require.NoError(t, json.Unmarshal(cfgEvents[len(cfgEvents)-1].Data, &data))
	require.Equal(t, ConfigEventData{NS: e.ns.ID, Group: "crawler", Key: "search.json", Version: 1}, data)
	nsEvents := e.events.on(events.NamespaceChannel(e.ns.ID))
	require.Len(t, nsEvents, 1)
	require.Equal(t, events.TypeConfigPublished, nsEvents[0].Type)
	var pub PublishedEventData
	require.NoError(t, json.Unmarshal(nsEvents[0].Data, &pub))
	require.Equal(t, PublishedEventData{ItemID: it.ID, Group: "crawler", Key: "search.json", Version: 1, Actor: e.admin.Actor()}, pub)

	// Publishing without a draft is a failed precondition.
	_, _, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID})
	requireReason(t, err, apperr.ReasonFailedPrecondition)

	// A draft that does not match the schema is rejected.
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"age":1}`})
	requireReason(t, err, apperr.ReasonInvalidArgument)

	it, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"b"}`})
	require.NoError(t, err)
	require.True(t, it.HasDraft)

	d, err := e.svc.DiffVersions(ctx, e.viewer, it.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, `{"name":"a"}`, d.FromContent)
	require.Equal(t, `{"name":"b"}`, d.ToContent)
	require.Contains(t, d.Unified, "--- v1\n+++ draft\n")
	require.Contains(t, d.Unified, `+{"name":"b"}`)

	// Optimistic concurrency.
	_, _, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID, ExpectedVersion: 5})
	requireReason(t, err, apperr.ReasonConflict)
	it, v, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID, ExpectedVersion: 1})
	require.NoError(t, err)
	require.Equal(t, int32(2), v)

	// Rollback to v1 creates v3 with source_version 1 and keeps drafts.
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"c"}`})
	require.NoError(t, err)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 2})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	_, _, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 9})
	requireReason(t, err, apperr.ReasonNotFound)
	_, _, err = e.svc.Rollback(ctx, e.operator, RollbackRequest{ID: it.ID, Version: 1})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	it, v, err = e.svc.Rollback(ctx, e.admin, RollbackRequest{ID: it.ID, Version: 1, Comment: "revert"})
	require.NoError(t, err)
	require.Equal(t, int32(3), v)
	require.Equal(t, `{"name":"a"}`, it.PublishedContent)
	require.True(t, it.HasDraft)
	nsEvents = e.events.on(events.NamespaceChannel(e.ns.ID))
	require.NoError(t, json.Unmarshal(nsEvents[len(nsEvents)-1].Data, &pub))
	require.Equal(t, int32(3), pub.Version)
	require.Equal(t, int32(1), pub.SourceVersion)
	require.Len(t, e.audit.find(ActionRollback, audit.ResultOK), 1)

	// Versions, newest first, paged.
	page, err := e.svc.ListVersions(ctx, e.viewer, it.ID, 2, "")
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	require.Len(t, page.Versions, 2)
	require.Equal(t, int32(3), page.Versions[0].Version)
	require.Equal(t, int32(1), page.Versions[0].SourceVersion)
	require.Equal(t, "revert", page.Versions[0].Comment)
	require.Equal(t, `{"name":"b"}`, page.Versions[1].Content)
	require.NotEmpty(t, page.NextPageToken)
	page, err = e.svc.ListVersions(ctx, e.viewer, it.ID, 2, page.NextPageToken)
	require.NoError(t, err)
	require.Len(t, page.Versions, 1)
	require.Equal(t, int32(1), page.Versions[0].Version)
	require.Equal(t, "first", page.Versions[0].Comment)
	require.Empty(t, page.NextPageToken)
	_, err = e.svc.ListVersions(ctx, e.viewer, it.ID, 2, "!!")
	requireReason(t, err, apperr.ReasonInvalidArgument)

	d, err = e.svc.DiffVersions(ctx, e.viewer, it.ID, 1, 2)
	require.NoError(t, err)
	require.Contains(t, d.Unified, "--- v1\n+++ v2\n")
	d, err = e.svc.DiffVersions(ctx, e.viewer, it.ID, 1, 3)
	require.NoError(t, err)
	require.Empty(t, d.Unified)
	_, err = e.svc.DiffVersions(ctx, e.viewer, it.ID, 7, 0)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.svc.DiffVersions(ctx, e.viewer, it.ID, 0, 8)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.svc.DiffVersions(ctx, e.viewer, it.ID, -1, 0)
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Delete.
	err = e.svc.DeleteItem(ctx, e.operator, it.ID)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.NoError(t, e.svc.DeleteItem(ctx, e.admin, it.ID))
	_, err = e.svc.GetItem(ctx, e.admin, it.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	require.Error(t, e.svc.DeleteItem(ctx, e.admin, it.ID))
	cfgEvents = e.events.on(events.ChannelConfig)
	last := cfgEvents[len(cfgEvents)-1]
	require.Equal(t, EventTypeConfigDeleted, last.Type)
	require.NoError(t, json.Unmarshal(last.Data, &data))
	require.Zero(t, data.Version)
	require.Len(t, e.audit.find(ActionDelete, audit.ResultOK), 1)
}

func idValid(id string) bool { return strings.HasPrefix(id, "cfg_") && len(id) == 36 }

func TestCreateItemValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.create(t, "crawler", "dup", FormatText, "x", false)

	tests := []struct {
		name   string
		p      *authz.Principal
		req    CreateRequest
		reason apperr.Reason
		msg    string
	}{
		{name: "viewer denied", p: e.viewer, req: CreateRequest{Group: "g", Key: "k", Format: FormatText}, reason: apperr.ReasonPermissionDenied},
		{name: "operator publish denied", p: e.operator, req: CreateRequest{Group: "g", Key: "k", Format: FormatText, Publish: true}, reason: apperr.ReasonPermissionDenied},
		{name: "reserved group", p: e.admin, req: CreateRequest{Group: "_runtime", Key: "k", Format: FormatText}, reason: apperr.ReasonInvalidArgument, msg: "reserved"},
		{name: "bad key", p: e.admin, req: CreateRequest{Group: "g", Key: "../k", Format: FormatText}, reason: apperr.ReasonInvalidArgument},
		{name: "bad format", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: "toml"}, reason: apperr.ReasonInvalidArgument, msg: "format"},
		{name: "schema on text", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatText, SchemaJSON: `{}`}, reason: apperr.ReasonInvalidArgument, msg: "json or yaml"},
		{name: "invalid schema", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatJSON, SchemaJSON: `{"type":1}`}, reason: apperr.ReasonInvalidArgument, msg: "invalid JSON Schema"},
		{name: "invalid json", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatJSON, Content: `{`}, reason: apperr.ReasonInvalidArgument, msg: "JSON"},
		{name: "yaml schema mismatch", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatYAML, SchemaJSON: personSchema, Content: "age: 1\n"}, reason: apperr.ReasonInvalidArgument, msg: "schema"},
		{name: "json empty publish", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatJSON, Publish: true}, reason: apperr.ReasonInvalidArgument},
		{name: "long description", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatText, Description: strings.Repeat("d", MaxDescriptionLen+1)}, reason: apperr.ReasonInvalidArgument},
		{name: "long comment", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatText, Comment: strings.Repeat("c", MaxCommentLen+1)}, reason: apperr.ReasonInvalidArgument},
		{name: "duplicate", p: e.admin, req: CreateRequest{Group: "crawler", Key: "dup", Format: FormatText}, reason: apperr.ReasonAlreadyExists},
		{name: "missing secrets", p: e.admin, req: CreateRequest{Group: "g", Key: "k", Format: FormatText, Content: "${secret:a/b} ${secret:c#2}", Publish: true}, reason: apperr.ReasonFailedPrecondition, msg: "a/b, c#2"},
		{name: "wrong tenant token", p: e.token(t, e.otherNS, "config:publish"), req: CreateRequest{Group: "g", Key: "k", Format: FormatText}, reason: apperr.ReasonScopeMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.CreateItem(ctx, tc.p, e.ns, tc.req)
			requireReason(t, err, tc.reason)
			if tc.msg != "" {
				require.Contains(t, err.Error(), tc.msg)
			}
		})
	}
	_, err := e.svc.CreateItem(ctx, nil, e.ns, CreateRequest{})
	requireReason(t, err, apperr.ReasonSessionInvalid)
	require.NotEmpty(t, e.audit.find(ActionCreate, audit.ResultDenied))

	// Empty content creates an item without a draft; the publish scope of a
	// token covers create + publish.
	it, err := e.svc.CreateItem(ctx, e.admin, e.ns, CreateRequest{Group: "g", Key: "empty", Format: FormatJSON})
	require.NoError(t, err)
	require.False(t, it.HasDraft)
	ci := e.token(t, e.ns, "config:publish:ci*")
	it, err = e.svc.CreateItem(ctx, ci, e.ns, CreateRequest{Group: "ci-tools", Key: "k.yaml", Format: FormatYAML, Content: "a: 1\n", Publish: true})
	require.NoError(t, err)
	require.Equal(t, int32(1), it.CurrentVersion)
	require.Equal(t, ci.Actor(), it.PublishedBy)
	_, err = e.svc.CreateItem(ctx, ci, e.ns, CreateRequest{Group: "other", Key: "k", Format: FormatText})
	requireReason(t, err, apperr.ReasonScopeMissing)
}

func TestSaveDraftSemantics(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	it := e.create(t, "g", "k.json", FormatJSON, `{"name":"x"}`, true)

	// Identical to the published content clears the draft.
	it, err := e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"y"}`})
	require.NoError(t, err)
	require.True(t, it.HasDraft)
	it, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"x"}`})
	require.NoError(t, err)
	require.False(t, it.HasDraft)
	entries := e.audit.find(ActionSaveDraft, audit.ResultOK)
	require.Equal(t, true, entries[len(entries)-1].Details["draft_cleared"])

	// Schema and description changes.
	desc := "new description"
	schema := personSchema
	it, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"z"}`, SchemaJSON: &schema, Description: &desc})
	require.NoError(t, err)
	require.Equal(t, desc, it.Description)
	require.JSONEq(t, personSchema, string(it.Schema))
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"other":1}`})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	bad := `{"type":"nope"}`
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `{"name":"z"}`, SchemaJSON: &bad})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	empty := ""
	it, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `[1]`, SchemaJSON: &empty})
	require.NoError(t, err)
	require.Nil(t, it.Schema)
	longDesc := strings.Repeat("x", MaxDescriptionLen+1)
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: `[1]`, Description: &longDesc})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.SaveDraft(ctx, e.operator, DraftRequest{ID: it.ID, Content: strings.Repeat("x", MaxContentBytes+1)})
	requireReason(t, err, apperr.ReasonInvalidArgument)

	// Permissions and addressing.
	_, err = e.svc.SaveDraft(ctx, e.viewer, DraftRequest{ID: it.ID, Content: `[2]`})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.NotEmpty(t, e.audit.find(ActionSaveDraft, audit.ResultDenied))
	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: "cfg_unknown", Content: `[2]`})
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: "cfg_0192a3f4c1d27b8e9a01f2c3d4e5a6b7", Content: `[2]`})
	requireReason(t, err, apperr.ReasonNotFound)
	otherAdmin := userPrincipal(e.otherNS.TenantID, authz.RoleAdmin)
	_, err = e.svc.SaveDraft(ctx, otherAdmin, DraftRequest{ID: it.ID, Content: `[2]`})
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.svc.GetItem(ctx, e.token(t, e.otherNS, "config:read"), it.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	platform := &authz.Principal{Kind: authz.KindUser, ID: "usr_p", IsPlatformAdmin: true}
	got, err := e.svc.GetItem(ctx, platform, it.ID)
	require.NoError(t, err)
	require.Equal(t, it.ID, got.ID)
	got, err = e.svc.GetItem(ctx, authz.System("test"), it.ID)
	require.NoError(t, err)
	require.Equal(t, it.ID, got.ID)
}

func TestPublishSecretReferences(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	e.addSecret(t, e.ns, "signing/api_key", 2, "key")
	e.addSecret(t, e.otherNS, "other/secret", 1, "nope")

	it := e.create(t, "crawler", "refs.yaml", FormatYAML, "", false)
	content := "key: ${secret:signing/api_key}\nold: ${secret:signing/api_key#1}\nmissing: ${secret:signing/api_key#3}\nx: ${secret:other/secret}\n"
	_, err := e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: it.ID, Content: content})
	require.NoError(t, err, "drafts may reference secrets that do not exist yet")
	_, _, err = e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	require.Contains(t, err.Error(), "signing/api_key#3, other/secret")
	require.NotContains(t, err.Error(), "signing/api_key#1")

	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: it.ID, Content: "key: ${secret:signing/api_key}\nold: ${secret:signing/api_key#1}\n"})
	require.NoError(t, err)
	_, v, err := e.svc.Publish(ctx, e.admin, PublishRequest{ID: it.ID})
	require.NoError(t, err)
	require.Equal(t, int32(1), v)

	_, err = e.svc.SaveDraft(ctx, e.admin, DraftRequest{ID: it.ID, Content: "bad: ${secret:Bad}\n"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
}

func TestListItems(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	for _, k := range []string{"a.json", "b.json", "c.json"} {
		e.create(t, "crawler", k, FormatJSON, `{}`, true)
	}
	e.create(t, "ci", "deploy.yaml", FormatYAML, "a: 1\n", false)
	_, err := e.svc.CreateItem(ctx, e.admin, e.ns, CreateRequest{Group: "misc", Key: "done.txt", Format: FormatText, Description: "100%_done"})
	require.NoError(t, err)
	e.runtime.bump(e.ns.ID, RuntimeBreakers)

	page, err := e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{PageSize: 3})
	require.NoError(t, err)
	require.Equal(t, 7, page.Total)
	require.Len(t, page.Items, 5, "2 runtime items + page size")
	require.Equal(t, RuntimeGroup, page.Items[0].Group)
	require.Equal(t, RuntimeBreakers, page.Items[0].Key)
	require.Equal(t, int32(2), page.Items[0].CurrentVersion)
	require.True(t, page.Items[0].ReadOnly)
	require.Equal(t, int32(1), page.Items[1].CurrentVersion, "site_switches never changed")
	require.Equal(t, "ci", page.Items[2].Group)
	require.True(t, page.Items[2].HasDraft)
	require.Nil(t, page.Items[2].DraftContent)
	require.Equal(t, "a.json", page.Items[3].Key)
	require.NotNil(t, page.Items[3].PublishedAt)
	require.Empty(t, page.Items[3].PublishedContent)
	require.NotEmpty(t, page.NextPageToken)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{PageSize: 3, PageToken: page.NextPageToken})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.Equal(t, "c.json", page.Items[0].Key)
	require.Equal(t, "misc", page.Items[1].Group)
	require.Empty(t, page.NextPageToken)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Group: "crawler"})
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	require.Len(t, page.Items, 3)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Search: "%_"})
	require.NoError(t, err)
	require.Equal(t, 1, page.Total, "LIKE metacharacters are escaped")
	require.Equal(t, "done.txt", page.Items[0].Key)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Search: "BREAKER"})
	require.NoError(t, err)
	require.Equal(t, 1, page.Total)
	require.Equal(t, RuntimeBreakers, page.Items[0].Key)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Group: RuntimeGroup})
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)

	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Group: "_other"})
	require.NoError(t, err)
	require.Zero(t, page.Total)

	// A token restricted to a group glob only sees its groups and no runtime items.
	tok := e.token(t, e.ns, "config:read:cr*")
	page, err = e.svc.ListItems(ctx, tok, e.ns, ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 3, page.Total)
	for _, it := range page.Items {
		require.Equal(t, "crawler", it.Group)
	}
	_, err = e.svc.ListItems(ctx, tok, e.ns, ListOptions{Group: "ci"})
	requireReason(t, err, apperr.ReasonScopeMissing)

	// Unrestricted token scope includes the runtime group.
	page, err = e.svc.ListItems(ctx, e.token(t, e.ns, "config:read"), e.ns, ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 7, page.Total)

	// Runtime provider failures do not break listing.
	e.runtime.setErr(context.DeadlineExceeded)
	page, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Group: RuntimeGroup})
	require.NoError(t, err)
	require.Zero(t, page.Items[0].CurrentVersion)
	e.runtime.setErr(nil)

	// Denials and invalid input.
	_, err = e.svc.ListItems(ctx, e.token(t, e.ns, "lease:acquire"), e.ns, ListOptions{})
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = e.svc.ListItems(ctx, userPrincipal(e.otherNS.TenantID, authz.RoleOwner), e.ns, ListOptions{})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Group: "-bad"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{PageToken: "%%%"})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.ListItems(ctx, e.viewer, e.ns, ListOptions{Search: strings.Repeat("s", 300)})
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.svc.ListItems(ctx, nil, e.ns, ListOptions{})
	requireReason(t, err, apperr.ReasonSessionInvalid)
}

func TestGetItemByLocator(t *testing.T) {
	t.Parallel()
	e := newEnv(t, Config{})
	ctx := context.Background()
	created := e.create(t, "crawler", "a.json", FormatJSON, `{"a":"${secret:x}"}`, false)

	it, err := e.svc.GetItemByLocator(ctx, e.viewer, e.ns, "crawler", "a.json")
	require.NoError(t, err)
	require.Equal(t, created.ID, it.ID)
	require.Equal(t, `{"a":"${secret:x}"}`, *it.DraftContent, "admin reads never resolve references")

	e.runtime.bump(e.ns.ID, RuntimeSiteSwitches)
	it, err = e.svc.GetItemByLocator(ctx, e.viewer, e.ns, RuntimeGroup, RuntimeSiteSwitches)
	require.NoError(t, err)
	require.True(t, it.ReadOnly)
	require.Equal(t, int32(2), it.CurrentVersion)
	require.JSONEq(t, `{"kind":"site_switches","version":1}`, it.PublishedContent)

	for _, tc := range []struct{ group, key string }{
		{"crawler", "missing"}, {RuntimeGroup, "unknown"}, {"_other", "k"}, {"crawler", "../x"},
	} {
		_, err = e.svc.GetItemByLocator(ctx, e.viewer, e.ns, tc.group, tc.key)
		requireReason(t, err, apperr.ReasonNotFound)
	}
	_, err = e.svc.GetItemByLocator(ctx, e.token(t, e.ns, "config:read:x"), e.ns, "crawler", "a.json")
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = e.svc.GetItemByLocator(ctx, nil, e.ns, "crawler", "a.json")
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = e.svc.GetItem(ctx, e.token(t, e.ns, "lease:acquire"), created.ID)
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = e.svc.GetItem(ctx, nil, created.ID)
	requireReason(t, err, apperr.ReasonSessionInvalid)

	e.runtime.setErr(context.Canceled)
	_, err = e.svc.GetItemByLocator(ctx, e.viewer, e.ns, RuntimeGroup, RuntimeBreakers)
	require.Error(t, err)
	e.runtime.setErr(nil)

	noRuntime := New(Config{}, e.pool, e.cat, e.bus, e.secrets, nil, nil, nil, nil)
	_, err = noRuntime.GetItemByLocator(ctx, e.viewer, e.ns, RuntimeGroup, RuntimeBreakers)
	requireReason(t, err, apperr.ReasonNotFound)
}
