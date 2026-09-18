package policysvc_test

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/policysvc/policysvctest"
)

// publishNew creates and publishes a policy as the admin.
func publishNew(t *testing.T, f *policysvctest.Fixture, kind policy.Kind, yaml string) policysvc.Policy {
	t.Helper()
	p, err := f.Service.CreatePolicy(context.Background(), f.Admin, f.Namespace, policysvc.CreateInput{Kind: kind, YAML: yaml, Publish: true})
	require.NoError(t, err)
	return p
}

// draftNew creates a draft policy as the operator.
func draftNew(t *testing.T, f *policysvctest.Fixture, kind policy.Kind, yaml string) policysvc.Policy {
	t.Helper()
	p, err := f.Service.CreatePolicy(context.Background(), f.Operator, f.Namespace, policysvc.CreateInput{Kind: kind, YAML: yaml})
	require.NoError(t, err)
	return p
}

func TestExtendsValidation(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	// A child of an unpublished parent cannot be published.
	parent := draftNew(t, f, policy.KindSignal, "name: parent\nrules:\n  - {name: p1, when: {markers: [a]}, outcome: captcha}\n")
	child := draftNew(t, f, policy.KindSignal, "name: child\nextends: parent\nrules:\n  - {name: c1, when: {markers: [b]}, outcome: banned}\n")
	_, err := svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: child.ID})
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Contains(t, err.Error(), "unknown signal policy \"parent\"")

	// Kind matters: an action policy named parent does not satisfy a signal extends.
	publishNew(t, f, policy.KindAction, "name: parent\n")
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: child.ID})
	requireCode(t, err, connect.CodeInvalidArgument)

	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: parent.ID})
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: child.ID})
	require.NoError(t, err)

	// Cycle: parent may not extend its own descendant.
	_, err = svc.SaveDraft(ctx, f.Operator, parent.ID, "name: parent\nextends: child\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: parent.ID})
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Contains(t, err.Error(), "cycle")

	// Self extension is rejected when saving the draft.
	_, err = svc.SaveDraft(ctx, f.Operator, parent.ID, "name: parent\nextends: parent\n")
	requireCode(t, err, connect.CodeInvalidArgument)

	// Deleting a policy that published policies extend is refused.
	err = svc.DeletePolicy(ctx, f.Admin, parent.ID)
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	require.Contains(t, err.Error(), "child")

	// Chains are limited to policy.MaxExtendsDepth policies.
	prev := ""
	var ids []string
	for i := 1; i <= policy.MaxExtendsDepth; i++ {
		yaml := fmt.Sprintf("name: level%d\nmode: enforce\n", i)
		if prev != "" {
			yaml += "extends: " + prev + "\n"
		}
		ids = append(ids, publishNew(t, f, policy.KindAction, yaml).ID)
		prev = fmt.Sprintf("level%d", i)
	}
	tooDeep := draftNew(t, f, policy.KindAction, "name: level6\nextends: level5\n")
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: tooDeep.ID})
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Contains(t, err.Error(), "maximum depth")

	// Descendants count as well: making the root extend another policy would
	// push level5's chain beyond the limit.
	publishNew(t, f, policy.KindAction, "name: base\n")
	_, err = svc.SaveDraft(ctx, f.Operator, ids[0], "name: level1\nextends: base\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: ids[0]})
	requireCode(t, err, connect.CodeInvalidArgument)
	require.Contains(t, err.Error(), `"level5"`)

	// Removing the extends of level3 shortens the chains again, so level1 may
	// then extend base (base → level1 → level2 is 3 policies; level3..5 stand alone).
	_, err = svc.SaveDraft(ctx, f.Operator, ids[2], "name: level3\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: ids[2]})
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: ids[0]})
	require.NoError(t, err)

	// A missing parent is an invalid argument.
	orphan := draftNew(t, f, policy.KindAction, "name: orphan\nextends: missing\n")
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: orphan.ID})
	requireCode(t, err, connect.CodeInvalidArgument)

	// A stored parent version that no longer validates is reported as a failed precondition.
	bad := publishNew(t, f, policy.KindSignal, "name: bad-parent\n")
	_, err = f.Pool.Exec(ctx, `UPDATE policy_versions SET spec = '{"name":"bad-parent","rules":[{"outcome":"nope"}]}' WHERE policy_id = $1`, bad.ID)
	require.NoError(t, err)
	badChild := draftNew(t, f, policy.KindSignal, "name: bad-child\nextends: bad-parent\n")
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: badChild.ID})
	requireReason(t, err, apperr.ReasonFailedPrecondition)
	require.Contains(t, err.Error(), "no longer valid")

	// Rolling back to a stored version that no longer validates fails the same way.
	_, err = svc.SaveDraft(ctx, f.Operator, bad.ID, "name: bad-parent\ntrust_outcome_hint: true\n")
	require.NoError(t, err)
	_, err = svc.PublishPolicy(ctx, f.Admin, policysvc.PublishInput{ID: bad.ID})
	require.NoError(t, err)
	_, err = svc.RollbackPolicy(ctx, f.Admin, bad.ID, 1, "")
	requireReason(t, err, apperr.ReasonFailedPrecondition)
}

func TestResolvePoliciesFlattensExtends(t *testing.T) {
	t.Parallel()
	f := policysvctest.New(t)
	ctx := context.Background()
	svc := f.Service

	child := publishNew(t, f, policy.KindSignal,
		"name: shop-signals\nextends: default-signal\nbind: {site: shop}\nrules:\n  - {name: account-banned, when: {markers: [account_banned]}, outcome: banned}\n")
	require.Len(t, child.Bindings, 1)
	act := publishNew(t, f, policy.KindAction,
		"name: shop-actions\nextends: default-action\nmode: shadow\nbind: {site: shop}\nrules:\n  - {name: forbidden-cooldown, when: {outcome: forbidden}, action: cooldown, scope: identity_site, base: 10m}\n")

	resolved, err := svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "shop"})
	require.NoError(t, err)
	require.Len(t, resolved, 4)
	require.Equal(t, policy.KindSignal, resolved[1].Kind)
	require.Equal(t, child.ID, resolved[1].PolicyID)
	require.Equal(t, policy.LevelSite, resolved[1].Level)
	require.Equal(t, 1, resolved[1].Version)
	require.Contains(t, resolved[1].YAML, "default-signal -> shop-signals")
	require.Contains(t, resolved[1].YAML, "name: account-banned")
	require.Contains(t, resolved[1].YAML, "name: proxy-error")
	require.NotContains(t, resolved[1].YAML, "extends:")

	// The flattened YAML is a valid policy with the concatenated rules.
	flat, err := policy.ParseYAML(policy.KindSignal, []byte(resolved[1].YAML))
	require.NoError(t, err)
	require.Len(t, flat.(*policy.SignalSpec).Rules, 12)

	require.Equal(t, act.ID, resolved[2].PolicyID)
	flatAct, err := policy.ParseYAML(policy.KindAction, []byte(resolved[2].YAML))
	require.NoError(t, err)
	require.Equal(t, policy.ModeShadow, flatAct.(*policy.ActionSpec).Mode)
	require.Len(t, flatAct.(*policy.ActionSpec).Rules, 8)
	require.Len(t, flatAct.(*policy.ActionSpec).Escalation, 2, "escalation inherited from the parent")

	// Rotation and breaker fall back to the namespace defaults.
	require.Equal(t, policy.LevelNamespace, resolved[0].Level)
	require.Equal(t, "default-rotation", resolved[0].Name)
	require.Equal(t, policy.DefaultYAML(policy.KindRotation), resolved[0].YAML)

	// forum only sees namespace defaults.
	resolved, err = svc.ResolvePolicies(ctx, f.Viewer, f.Namespace, policysvc.Target{Site: "forum", Client: "web"})
	require.NoError(t, err)
	for _, r := range resolved {
		require.Equal(t, policy.LevelNamespace, r.Level)
		require.Equal(t, policy.DefaultPolicyName(r.Kind), r.Name)
	}
}
