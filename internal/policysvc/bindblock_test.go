package policysvc_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvctest"
)

// bindingOwners maps "<site>/<client>/<group>" targets of one kind to policy names.
func bindingOwners(t *testing.T, f *policysvctest.Fixture, kind policy.Kind) map[string]string {
	t.Helper()
	bs, err := f.Service.ListBindings(context.Background(), f.Admin, f.Namespace, kind, "")
	require.NoError(t, err)
	out := map[string]string{}
	for _, b := range bs {
		out[b.SiteName+"/"+b.Client+"/"+b.EndpointGroupName] = b.PolicyName
	}
	return out
}

func TestPublishAppliesBindBlockOnlyWhenNewOrChanged(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	const siteBind = "name: p-signals\nbind: {site: shop}\n"
	p := publishNew(t, f, policy.KindSignal, siteBind)
	require.Equal(t, "p-signals", bindingOwners(t, f, policy.KindSignal)["shop//"])
	_ = f.Events.Take()

	// Another policy takes the shop target explicitly.
	q := publishNew(t, f, policy.KindSignal, "name: q-signals\n")
	_, err := svc.SetBinding(ctx, f.Admin, q.ID, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	require.Equal(t, "q-signals", bindingOwners(t, f, policy.KindSignal)["shop//"])

	// Republishing p with the same bind block keeps the explicit binding.
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, siteBind+"trust_outcome_hint: true\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	require.Equal(t, "q-signals", bindingOwners(t, f, policy.KindSignal)["shop//"])

	// A changed bind block is applied.
	_ = f.Events.Take()
	_ = f.Audit.Take()
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, "name: p-signals\nbind: {site: shop, client: web}\n")
	require.NoError(t, err)
	v3, err := svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	owners := bindingOwners(t, f, policy.KindSignal)
	require.Equal(t, "p-signals", owners["shop/web/"])
	require.Equal(t, "q-signals", owners["shop//"])
	evs := f.Events.Take()
	require.Len(t, evs, 1)
	var data policysvc.PublishedEvent
	require.NoError(t, json.Unmarshal(evs[0].Data, &data))
	require.NotEmpty(t, data.BindingID)
	require.Equal(t, f.Shop.ID, data.SiteID)
	require.Equal(t, f.Shop.ID, evs[0].SiteID)
	var publishAudit audit.Entry
	for _, e := range f.Audit.Take() {
		if e.Action == policysvc.AuditPolicyPublish {
			publishAudit = e
		}
	}
	require.Equal(t, data.BindingID, publishAudit.Details["binding_id"])

	// Rolling back to v1 publishes a bind block that differs from v3's: applied.
	rb, err := svc.RollbackPolicy(ctx, f.Admin, p.ID, 1, "back to site level")
	require.NoError(t, err)
	require.Equal(t, v3.CurrentVersion+1, rb.CurrentVersion)
	require.Equal(t, "p-signals", bindingOwners(t, f, policy.KindSignal)["shop//"])

	// Deleting that binding sticks across a republish of the same bind block.
	var siteBinding string
	for _, b := range rb.Bindings {
		if b.Level == policy.LevelSite {
			siteBinding = b.ID
		}
	}
	require.NotEmpty(t, siteBinding)
	require.NoError(t, svc.DeleteBinding(ctx, f.Admin, siteBinding))
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, siteBind+"trust_outcome_hint: false\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	_, bound := bindingOwners(t, f, policy.KindSignal)["shop//"]
	require.False(t, bound)

	// A stored current version that no longer parses counts as changed.
	_, err = f.Pool.Exec(ctx, `UPDATE policy_versions SET spec = '{"name":"p-signals","rules":[{"outcome":"nope"}]}'
		WHERE policy_id = $1 AND version = (SELECT current_version FROM policies WHERE id = $1)`, p.ID)
	require.NoError(t, err)
	_, err = svc.SaveDraft(ctx, f.Operator, p.ID, siteBind)
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: p.ID})
	require.NoError(t, err)
	require.Equal(t, "p-signals", bindingOwners(t, f, policy.KindSignal)["shop//"])
}

func TestCommentsAndMarkersCountCharacters(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// 1024 CJK characters are 3072 bytes but within the 1024-character limit.
	okComment := strings.Repeat("策", policysvc.MaxCommentLength)
	p, err := svc.CreatePolicy(ctx, f.Admin, f.Namespace, policysvc.CreateInput{
		Kind: policy.KindBreaker, YAML: "name: cjk-breaker\n", Publish: true, Comment: okComment,
	})
	require.NoError(t, err)
	versions, err := svc.ListPolicyVersions(ctx, f.Viewer, p.ID, 0, 0)
	require.NoError(t, err)
	require.Equal(t, okComment, versions.Versions[0].Comment)
	_, err = svc.RollbackPolicy(ctx, f.Admin, p.ID, 1, okComment+"策")
	requireCode(t, err, connect.CodeInvalidArgument)

	page, err := svc.ListPolicies(ctx, f.Viewer, f.Namespace, policysvc.ListPoliciesInput{Search: strings.Repeat("策", policysvc.MaxSearchLength)})
	require.NoError(t, err)
	require.Zero(t, page.Total)

	marker := strings.Repeat("验", 64)
	res, err := svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) {
		in.Report.Markers = []string{marker}
		in.Report.BusinessCode = strings.Repeat("码", 64)
		in.Report.HTTPStatus = 200
	}))
	require.NoError(t, err)
	require.Equal(t, policy.OutcomeSuccess, res.Outcome)
	_, err = svc.DebugReport(ctx, f.Viewer, f.Namespace, debugIn(func(in *policysvc.DebugInput) {
		in.Report.Markers = []string{marker + "验"}
	}))
	requireCode(t, err, connect.CodeInvalidArgument)
}
