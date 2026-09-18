package policysvc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvctest"
)

const breakerV1 = `name: strict-breaker
description: first
min_requests: 20
`

const breakerV2 = `name: strict-breaker
description: second
min_requests: 30
trip:
  risk_ratio_gte: 0.5
`

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	e, ok := apperr.As(err)
	require.Truef(t, ok, "expected *apperr.Error, got %T: %v", err, err)
	require.Equalf(t, code, e.Code, "unexpected error: %v", err)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equalf(t, reason, apperr.ReasonOf(err), "unexpected error: %v", err)
}

func auditActions(entries []audit.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Action+":"+e.Result)
	}
	return out
}

func TestPolicyLifecycle(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service
	_ = f.Audit.Take()

	// The operator creates a draft.
	created, err := svc.CreatePolicy(ctx, f.Operator, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: breakerV1})
	require.NoError(t, err)
	require.Equal(t, "strict-breaker", created.Name)
	require.Equal(t, "first", created.Description)
	require.Equal(t, 0, created.CurrentVersion)
	require.True(t, created.HasDraft)
	require.Equal(t, breakerV1, created.DraftYAML)
	require.Empty(t, created.PublishedYAML)
	require.Equal(t, "user:usr_operator", created.CreatedBy)
	require.Equal(t, "user:usr_operator", created.DraftUpdatedBy)
	require.NotNil(t, created.DraftUpdatedAt)
	require.Equal(t, policysvctest.NamespaceName, created.NamespaceName)
	require.Equal(t, []string{"policy.create:ok"}, auditActions(f.Audit.Take()))

	// The operator cannot publish.
	_, err = svc.PublishPolicy(ctx, f.Operator, policysvc.PublishInput{ID: created.ID})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.Equal(t, []string{"policy.publish:denied"}, auditActions(f.Audit.Take()))

	// Diff of an unpublished policy: published side is empty, draft side is the draft.
	diff, err := svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, 0, 0)
	require.NoError(t, err)
	require.Empty(t, diff.FromYAML)
	require.Equal(t, breakerV1, diff.ToYAML)
	require.Contains(t, diff.UnifiedDiff, "+++ draft")

	// The admin publishes version 1.
	pub, err := svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: created.ID, Comment: "initial", ExpectedVersion: 0})
	require.NoError(t, err)
	require.Equal(t, 1, pub.CurrentVersion)
	require.False(t, pub.HasDraft)
	require.Empty(t, pub.DraftYAML)
	require.Contains(t, pub.PublishedYAML, "min_requests: 20")
	require.Contains(t, pub.PublishedYAML, "revert_recent_cooldowns: endpoint", "published YAML is normalized with defaults")
	require.Equal(t, []string{"policy.publish:ok"}, auditActions(f.Audit.Take()))
	require.Contains(t, f.Catalog.Calls(), "invalidate:"+f.Namespace.ID)
	require.Empty(t, f.Hot.Take(), "breaker policies do not resync hot state")
	evs := f.Events.Take()
	require.Len(t, evs, 1)
	require.Equal(t, events.TypePolicyPublished, evs[0].Type)
	var data policysvc.PublishedEvent
	require.NoError(t, json.Unmarshal(evs[0].Data, &data))
	require.Equal(t, policysvc.PublishedEvent{
		PolicyID: created.ID, Name: "strict-breaker", Kind: "breaker", Version: 1, Action: "publish", Actor: "user:usr_admin",
	}, data)

	// Publishing without a draft fails.
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: created.ID})
	requireReason(t, err, apperr.ReasonFailedPrecondition)

	// Draft v2, then diff published → draft.
	draft, err := svc.SaveDraft(ctx, f.Operator, created.ID, breakerV2)
	require.NoError(t, err)
	require.True(t, draft.HasDraft)
	require.Equal(t, "first", draft.Description, "description follows the published version once published")
	diff, err = svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, pub.PublishedYAML, diff.FromYAML)
	require.Equal(t, breakerV2, diff.ToYAML)
	require.Contains(t, diff.UnifiedDiff, "--- v1")
	require.Contains(t, diff.UnifiedDiff, "+min_requests: 30")

	// Optimistic concurrency.
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: created.ID, ExpectedVersion: 5})
	requireReason(t, err, apperr.ReasonConflict)

	v2, err := svc.PublishPolicy(ctx, f.OperatorPubExt, policysvc.PublishInput{ID: created.ID, ExpectedVersion: 1, Comment: "tighten"})
	require.NoError(t, err)
	require.Equal(t, 2, v2.CurrentVersion)
	require.Equal(t, "second", v2.Description)

	// Versions, newest first, paginated.
	page, err := svc.ListPolicyVersions(ctx, f.Viewer, created.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.True(t, page.More)
	require.Len(t, page.Versions, 1)
	require.Equal(t, 2, page.Versions[0].Version)
	require.Equal(t, "tighten", page.Versions[0].Comment)
	require.Equal(t, "user:usr_oppub", page.Versions[0].CreatedBy)
	page, err = svc.ListPolicyVersions(ctx, f.Viewer, created.ID, 1, 2)
	require.NoError(t, err)
	require.False(t, page.More)
	require.Equal(t, 1, page.Versions[0].Version)
	_, err = svc.ListPolicyVersions(ctx, f.Viewer, created.ID, 10, -1)
	requireCode(t, err, connect.CodeInvalidArgument)

	// Rollback to v1 publishes v3 with the v1 spec; the draft is kept.
	_, err = svc.SaveDraft(ctx, f.Operator, created.ID, breakerV2)
	require.NoError(t, err)
	_ = f.Events.Take()
	v3, err := svc.RollbackPolicy(ctx, f.Admin, created.ID, 1, "revert")
	require.NoError(t, err)
	require.Equal(t, 3, v3.CurrentVersion)
	require.True(t, v3.HasDraft)
	require.Equal(t, "first", v3.Description)
	require.Equal(t, pub.PublishedYAML, v3.PublishedYAML)
	evs = f.Events.Take()
	require.Len(t, evs, 1)
	require.Contains(t, string(evs[0].Data), `"action":"rollback"`)

	diff, err = svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, 1, 3)
	require.NoError(t, err)
	require.Empty(t, diff.UnifiedDiff)
	diff, err = svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, 2, 3)
	require.NoError(t, err)
	require.Contains(t, diff.UnifiedDiff, "-min_requests: 30")
	_, err = svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, 9, 0)
	requireCode(t, err, connect.CodeNotFound)
	_, err = svc.DiffPolicyVersions(ctx, f.Viewer, created.ID, -1, 0)
	requireCode(t, err, connect.CodeInvalidArgument)

	_, err = svc.RollbackPolicy(ctx, f.Admin, created.ID, 3, "")
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	_, err = svc.RollbackPolicy(ctx, f.Admin, created.ID, 42, "")
	requireCode(t, err, connect.CodeNotFound)
	_, err = svc.RollbackPolicy(ctx, f.Admin, created.ID, 0, "")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.RollbackPolicy(ctx, f.Operator, created.ID, 2, "")
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.RollbackPolicy(ctx, f.Admin, created.ID, 2, strings.Repeat("x", policysvc.MaxCommentLength+1))
	requireCode(t, err, connect.CodeInvalidArgument)

	// Get.
	got, err := svc.GetPolicy(ctx, f.AdminToken, created.ID)
	require.NoError(t, err)
	require.Equal(t, v3.PublishedYAML, got.PublishedYAML)
	require.Equal(t, breakerV2, got.DraftYAML)

	// Delete requires write and publish.
	_ = f.Audit.Take()
	err = svc.DeletePolicy(ctx, f.Operator, created.ID)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	require.NoError(t, svc.DeletePolicy(ctx, f.Admin, created.ID))
	require.Equal(t, []string{"policy.delete:denied", "policy.delete:ok"}, auditActions(f.Audit.Take()))
	_, err = svc.GetPolicy(ctx, f.Admin, created.ID)
	requireCode(t, err, connect.CodeNotFound)
	require.Zero(t, f.Count(t, `SELECT count(*) FROM policy_versions WHERE policy_id = $1`, created.ID))
	err = svc.DeletePolicy(ctx, f.Admin, created.ID)
	requireCode(t, err, connect.CodeNotFound)
}

func TestCreatePolicyWithPublishAndValidation(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	tests := []struct {
		name   string
		in     policysvc.CreateInput
		code   connect.Code
		reason apperr.Reason
	}{
		{name: "invalid yaml", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: [x"}, code: connect.CodeInvalidArgument},
		{name: "unknown field", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: a\nbogus: 1\n"}, code: connect.CodeInvalidArgument},
		{name: "invalid name", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: Bad_Name\n"}, code: connect.CodeInvalidArgument},
		{name: "invalid kind", in: policysvc.CreateInput{Kind: "nope", YAML: "name: a\n"}, code: connect.CodeInvalidArgument},
		{name: "empty yaml", in: policysvc.CreateInput{Kind: policy.KindBreaker}, code: connect.CodeInvalidArgument},
		{name: "oversized yaml", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: strings.Repeat("#", policysvc.MaxYAMLBytes+1)}, code: connect.CodeInvalidArgument},
		{name: "oversized comment", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: a\n", Comment: strings.Repeat("c", 2000)}, code: connect.CodeInvalidArgument},
		{name: "duplicate default", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: default-breaker\n"}, code: connect.CodeAlreadyExists},
		{name: "unknown bind site", in: policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: b\nbind: {site: nope}\n", Publish: true}, code: connect.CodeInvalidArgument, reason: apperr.ReasonSiteUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.CreatePolicy(ctx, f.Admin, f.Namespace, tt.in)
			requireCode(t, err, tt.code)
			if tt.reason != "" {
				requireReason(t, err, tt.reason)
			}
		})
	}

	// Operators cannot create published policies; viewers cannot create at all.
	_, err := svc.CreatePolicy(ctx, f.Operator, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: breakerV1, Publish: true})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.CreatePolicy(ctx, f.Viewer, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: breakerV1})
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.CreatePolicy(ctx, nil, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: breakerV1})
	requireReason(t, err, apperr.ReasonSessionInvalid)
	_, err = svc.CreatePolicy(ctx, f.OtherTenant, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: breakerV1})
	requireReason(t, err, apperr.ReasonPermissionDenied)

	// Create + publish with a bind block creates the binding and resyncs the site.
	rot := "name: shop-web\nbind: {site: shop, client: web}\nidentity_types: [cookie]\n"
	p, err := svc.CreatePolicy(ctx, f.AdminToken, f.Namespace, policysvc.CreateInput{Kind: policy.KindRotation, YAML: rot, Publish: true, Comment: "go"})
	require.NoError(t, err)
	require.Equal(t, 1, p.CurrentVersion)
	require.False(t, p.HasDraft)
	require.Len(t, p.Bindings, 1)
	require.Equal(t, policy.LevelClient, p.Bindings[0].Level)
	require.Equal(t, "shop", p.Bindings[0].SiteName)
	require.Equal(t, "web", p.Bindings[0].Client)
	require.Equal(t, []string{f.Shop.ID}, f.Hot.Take())

	// Same name + kind is a conflict; same name with another kind is allowed.
	_, err = svc.CreatePolicy(ctx, f.Admin, f.Namespace, policysvc.CreateInput{Kind: policy.KindRotation, YAML: "name: shop-web\n"})
	requireCode(t, err, connect.CodeAlreadyExists)
	_, err = svc.CreatePolicy(ctx, f.Admin, f.Namespace, policysvc.CreateInput{Kind: policy.KindBreaker, YAML: "name: shop-web\n"})
	require.NoError(t, err)

	// Draft validation: must parse, name immutable.
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, "name: renamed\n")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, "name: shop-web\nrotation: {strategy: nope}\n")
	requireCode(t, err, connect.CodeInvalidArgument)
	_, err = svc.SaveDraft(ctx, f.Viewer, p.ID, rot)
	requireReason(t, err, apperr.ReasonPermissionDenied)
	_, err = svc.SaveDraft(ctx, f.Operator, "pol_missing", rot)
	requireCode(t, err, connect.CodeNotFound)
	_, err = svc.SaveDraft(ctx, f.Operator, "", rot)
	requireCode(t, err, connect.CodeInvalidArgument)

	// Republishing with identical identity types does not resync; changing them does.
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, rot+"rotation: {strategy: best_health}\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	require.Empty(t, f.Hot.Take())
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, "name: shop-web\nbind: {site: shop, client: web}\nidentity_types: [cookie, device]\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	require.Equal(t, []string{f.Shop.ID}, f.Hot.Take())

	// Publishing a draft that became invalid for the target (bind on an unknown client) fails atomically.
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, "name: shop-web\nbind: {site: shop, client: tv}\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	requireReason(t, err, apperr.ReasonClientUnknown)
	got, err := svc.GetPolicy(ctx, f.Admin, p.ID)
	require.NoError(t, err)
	require.Equal(t, 3, got.CurrentVersion)
}
