package identitysvc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/identitysvc"
	"github.com/TikHub/Spinneret/internal/identitysvc/identitysvctest"
	"github.com/TikHub/Spinneret/internal/pkg/durationx"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
)

// importN imports n identities of type name on site and returns their IDs.
func importN(t *testing.T, env *identitysvctest.Env, siteName, typeName, prefix string, n int) []string {
	t.Helper()
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "{\"cookies\":\"sessionid=%s%d\",\"_labels\":{\"batch\":\"%s\"}}\n", prefix, i, prefix)
	}
	_, err := env.Service.ImportIdentities(context.Background(), env.Owner(env.NS), env.NS, identitysvc.ImportInput{
		Site: siteName, Type: typeName, Format: "jsonl", Data: b.String(),
	})
	require.NoError(t, err)
	rows, err := env.Pool.Query(context.Background(), `SELECT id FROM identities WHERE labels->>'batch' = $1 ORDER BY id`, prefix)
	require.NoError(t, err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.Len(t, ids, n)
	return ids
}

func TestOperateIdentities(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	env.CreateType(t, env.SiteB, strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "alt_cookie", 1))
	aIDs := importN(t, env, "shop", "web_cookie", "a", 3)
	bIDs := importN(t, env, "market", "alt_cookie", "b", 2)
	op := env.Role(authz.RoleOperator)
	cooldown := identitysvc.OperationRequest{Operation: "cooldown", Duration: durationx.Duration(time.Hour), Reason: "manual"}

	t.Run("success with unknown ids", func(t *testing.T) {
		env.Ops.FailIDs = []string{aIDs[1]}
		defer func() { env.Ops.FailIDs = nil }()
		ids := append(append([]string{}, aIDs...), "idt_unknown", bIDs[0])
		res, err := env.Service.OperateIdentities(ctx, op, ids, cooldown)
		require.NoError(t, err)
		require.Equal(t, 4, res.Matched)
		require.Equal(t, 3, res.Succeeded)
		require.Len(t, res.Failed, 2)
		require.Equal(t, identitysvc.BulkFailure{ID: "idt_unknown", Reason: identitysvc.FailureNotFound, Message: "identity not found"}, res.Failed[0])
		calls := env.Ops.Calls()
		require.Equal(t, env.NS.ID, calls[len(calls)-1].Namespace)
		require.Equal(t, append(append([]string{}, aIDs...), bIDs[0]), calls[len(calls)-1].IDs)
	})

	t.Run("identity_endpoint scope", func(t *testing.T) {
		group := env.SiteA.ID + "_web_default"
		req := identitysvc.OperationRequest{Operation: "reset_stats", Scope: "identity_endpoint", EndpointGroupID: group}
		res, err := env.Service.OperateIdentities(ctx, op, []string{aIDs[0], bIDs[0]}, req)
		require.NoError(t, err)
		require.Equal(t, 1, res.Matched)
		require.Len(t, res.Failed, 1)
		require.Equal(t, identitysvc.FailureEndpointGroupUnknown, res.Failed[0].Reason)

		req.EndpointGroupID = "eg_missing"
		_, err = env.Service.OperateIdentities(ctx, op, []string{aIDs[0]}, req)
		requireReason(t, err, apperr.ReasonEndpointGroupUnknown)
	})

	errTests := []struct {
		name   string
		p      *authz.Principal
		ids    []string
		req    identitysvc.OperationRequest
		reason apperr.Reason
	}{
		{"site restricted operator touching another site", env.SiteRole(authz.RoleOperator, env.SiteB), []string{bIDs[0], aIDs[0]}, cooldown, apperr.ReasonPermissionDenied},
		{"viewer", env.Role(authz.RoleViewer), aIDs, cooldown, apperr.ReasonPermissionDenied},
		{"no ids", op, nil, cooldown, apperr.ReasonInvalidArgument},
		{"duplicate ids", op, []string{aIDs[0], aIDs[0]}, cooldown, apperr.ReasonInvalidArgument},
		{"too many ids", op, manyStrings(identitysvc.MaxOperateIDs + 1), cooldown, apperr.ReasonInvalidArgument},
		{"unknown operation", op, aIDs, identitysvc.OperationRequest{Operation: "explode"}, apperr.ReasonInvalidArgument},
		{"cooldown without duration", op, aIDs, identitysvc.OperationRequest{Operation: "cooldown"}, apperr.ReasonInvalidArgument},
		{"permanent cooldown", op, aIDs, identitysvc.OperationRequest{Operation: "cooldown", Duration: durationx.Permanent}, apperr.ReasonInvalidArgument},
		{"ban without duration", op, aIDs, identitysvc.OperationRequest{Operation: "ban"}, apperr.ReasonInvalidArgument},
		{"permanent quarantine", op, aIDs, identitysvc.OperationRequest{Operation: "quarantine", Duration: durationx.Permanent}, apperr.ReasonInvalidArgument},
		{"endpoint scope without group", op, aIDs, identitysvc.OperationRequest{Operation: "reset_stats", Scope: "identity_endpoint"}, apperr.ReasonInvalidArgument},
		{"bad scope", op, aIDs, identitysvc.OperationRequest{Operation: "reset_stats", Scope: "account"}, apperr.ReasonInvalidArgument},
		{"long reason", op, aIDs, identitysvc.OperationRequest{Operation: "disable", Reason: strings.Repeat("r", 600)}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.OperateIdentities(ctx, tc.p, tc.ids, tc.req)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("stranger ids are not found", func(t *testing.T) {
		res, err := env.Service.OperateIdentities(ctx, env.Stranger(), aIDs[:1], identitysvc.OperationRequest{Operation: "ban", Duration: durationx.Permanent})
		require.NoError(t, err)
		require.Equal(t, 0, res.Matched)
		require.Len(t, res.Failed, 1)
	})

	t.Run("operator errors propagate", func(t *testing.T) {
		env.Ops.Err = apperr.Conflict("busy")
		defer func() { env.Ops.Err = nil }()
		_, err := env.Service.OperateIdentities(ctx, op, aIDs, identitysvc.OperationRequest{Operation: "enable"})
		requireReason(t, err, apperr.ReasonConflict)
	})

	t.Run("operator not configured", func(t *testing.T) {
		svc := identitysvc.NewService(env.Pool, env.Cipher, env.Pepper, env.Catalog, env.Hot, nil, nil, nil, nil)
		_, err := svc.OperateIdentities(ctx, op, aIDs, identitysvc.OperationRequest{Operation: "enable"})
		requireReason(t, err, apperr.ReasonInternal)
		_, _, err = svc.OperateAccount(ctx, op, "acc_x", identitysvc.OperationRequest{Operation: "enable"})
		requireReason(t, err, apperr.ReasonInternal)
		_, _, err = svc.RevertActions(ctx, op, env.NS, identitysvc.RevertInput{})
		requireReason(t, err, apperr.ReasonInternal)
		_, err = svc.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{}, identitysvc.OperationRequest{Operation: "enable"}, 0, false)
		requireReason(t, err, apperr.ReasonInternal)
	})
}

func TestBulkOperateIdentities(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	env.CreateType(t, env.SiteB, strings.Replace(identitysvctest.WebCookieYAML, "web_cookie", "alt_cookie", 1))
	aIDs := importN(t, env, "shop", "web_cookie", "a", 2345)
	importN(t, env, "market", "alt_cookie", "b", 5)
	op := env.Role(authz.RoleOperator)
	disable := identitysvc.OperationRequest{Operation: "disable", Reason: "bulk"}

	t.Run("dry run counts", func(t *testing.T) {
		res, err := env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{}, disable, 0, true)
		require.NoError(t, err)
		require.Equal(t, 2350, res.Matched)
		require.Empty(t, env.Ops.Calls())
	})

	t.Run("chunks and limit", func(t *testing.T) {
		res, err := env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{Site: "shop"}, disable, 2200, false)
		require.NoError(t, err)
		require.Equal(t, 2200, res.Matched)
		require.Equal(t, 2200, res.Succeeded)
		calls := env.Ops.Calls()
		require.Len(t, calls, 3)
		require.Len(t, calls[0].IDs, 1000)
		require.Len(t, calls[2].IDs, 200)
		require.Equal(t, aIDs[0], calls[0].IDs[0])
		entry, ok := env.Audit.Last("identity.bulk_operate")
		require.True(t, ok)
		require.Equal(t, 2200, entry.Details["resolved"])
	})

	t.Run("endpoint group narrows to its site", func(t *testing.T) {
		req := identitysvc.OperationRequest{Operation: "reset_stats", Scope: "identity_endpoint", EndpointGroupID: env.SiteB.ID + "_web_default"}
		res, err := env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{}, req, 0, true)
		require.NoError(t, err)
		require.Equal(t, 5, res.Matched)
		req.EndpointGroupID = "eg_missing"
		_, err = env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{}, req, 0, true)
		requireReason(t, err, apperr.ReasonEndpointGroupUnknown)
	})

	t.Run("site restricted principal", func(t *testing.T) {
		res, err := env.Service.BulkOperateIdentities(ctx, env.SiteRole(authz.RoleOperator, env.SiteB), env.NS, identitysvc.IdentityFilter{}, disable, 0, true)
		require.NoError(t, err)
		require.Equal(t, 5, res.Matched)
	})

	t.Run("errors", func(t *testing.T) {
		_, err := env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{}, disable, identitysvc.MaxBulkIdentities+1, true)
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = env.Service.BulkOperateIdentities(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.IdentityFilter{}, disable, 0, true)
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{States: []string{"x"}}, disable, 0, true)
		requireReason(t, err, apperr.ReasonInvalidArgument)

		env.Ops.Err = errors.New("boom")
		defer func() { env.Ops.Err = nil }()
		_, err = env.Service.BulkOperateIdentities(ctx, op, env.NS, identitysvc.IdentityFilter{Site: "market"}, disable, 0, false)
		require.ErrorContains(t, err, "boom")
		entry, ok := env.Audit.Last("identity.bulk_operate")
		require.True(t, ok)
		require.Equal(t, "error", entry.Result)
	})
}

func TestRevertActions(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	from := time.Now().Add(-time.Hour)
	env.Ops.RevertIDs = []string{"idt_1", "idt_2", "idt_1"}

	t.Run("unrestricted principal makes one call", func(t *testing.T) {
		res, ids, err := env.Service.RevertActions(ctx, env.Role(authz.RoleOperator), env.NS, identitysvc.RevertInput{
			Request: identitysvc.RevertRequest{From: from, Rule: "r1", ResetHealth: true},
		})
		require.NoError(t, err)
		require.Equal(t, 3, res.Matched)
		require.Equal(t, []string{"idt_1", "idt_2"}, ids)
		calls := env.Ops.Calls()
		require.Len(t, calls, 1)
		require.Empty(t, calls[0].Revert.SiteID)
		require.Equal(t, []string{"ban", "quarantine", "expire", "cooldown"}, calls[0].Revert.Actions)
		require.False(t, calls[0].Revert.To.IsZero())
		require.True(t, calls[0].Revert.ResetHealth)
	})

	t.Run("site restricted principal makes one call per site", func(t *testing.T) {
		before := len(env.Ops.Calls())
		_, _, err := env.Service.RevertActions(ctx, env.SiteRole(authz.RoleOperator, env.SiteA, env.SiteB), env.NS, identitysvc.RevertInput{
			Request: identitysvc.RevertRequest{From: from, Actions: []string{"ban"}},
		})
		require.NoError(t, err)
		calls := env.Ops.Calls()[before:]
		require.Len(t, calls, 2)
		require.ElementsMatch(t, []string{env.SiteA.ID, env.SiteB.ID}, []string{calls[0].Revert.SiteID, calls[1].Revert.SiteID})
	})

	t.Run("site filter", func(t *testing.T) {
		before := len(env.Ops.Calls())
		_, _, err := env.Service.RevertActions(ctx, env.Role(authz.RoleOperator), env.NS, identitysvc.RevertInput{
			Site: "market", Request: identitysvc.RevertRequest{From: from},
		})
		require.NoError(t, err)
		calls := env.Ops.Calls()[before:]
		require.Len(t, calls, 1)
		require.Equal(t, env.SiteB.ID, calls[0].Revert.SiteID)
	})

	errTests := []struct {
		name   string
		p      *authz.Principal
		in     identitysvc.RevertInput
		reason apperr.Reason
	}{
		{"missing start", env.Role(authz.RoleOperator), identitysvc.RevertInput{}, apperr.ReasonInvalidArgument},
		{"start after end", env.Role(authz.RoleOperator), identitysvc.RevertInput{Request: identitysvc.RevertRequest{From: from, To: from.Add(-time.Minute)}}, apperr.ReasonInvalidArgument},
		{"bad action", env.Role(authz.RoleOperator), identitysvc.RevertInput{Request: identitysvc.RevertRequest{From: from, Actions: []string{"unban"}}}, apperr.ReasonInvalidArgument},
		{"long policy id", env.Role(authz.RoleOperator), identitysvc.RevertInput{Request: identitysvc.RevertRequest{From: from, PolicyID: strings.Repeat("p", 65)}}, apperr.ReasonInvalidArgument},
		{"viewer", env.Role(authz.RoleViewer), identitysvc.RevertInput{Request: identitysvc.RevertRequest{From: from}}, apperr.ReasonPermissionDenied},
		{"site not accessible", env.SiteRole(authz.RoleOperator, env.SiteB), identitysvc.RevertInput{Site: "shop", Request: identitysvc.RevertRequest{From: from}}, apperr.ReasonPermissionDenied},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := env.Service.RevertActions(ctx, tc.p, env.NS, tc.in)
			requireReason(t, err, tc.reason)
		})
	}

	t.Run("operator error", func(t *testing.T) {
		env.Ops.Err = apperr.Internal(errors.New("x"))
		defer func() { env.Ops.Err = nil }()
		_, _, err := env.Service.RevertActions(ctx, env.Role(authz.RoleOperator), env.NS, identitysvc.RevertInput{Request: identitysvc.RevertRequest{From: from}})
		requireReason(t, err, apperr.ReasonInternal)
	})
}

func TestAccounts(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	op := env.Role(authz.RoleOperator)

	created, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{
		Site: "shop", ExternalRef: "alice", Region: "US", Tags: []string{"vip"}, Notes: "line1\nline2",
	})
	require.NoError(t, err)
	require.Equal(t, "shop", created.SiteName)
	require.Equal(t, identitysvc.AccountActive, created.State)
	entry, ok := env.Audit.Last("account.upsert")
	require.True(t, ok)
	require.Equal(t, true, entry.Details["created"])

	updated, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "alice", Region: "JP"})
	require.NoError(t, err)
	require.Equal(t, created.ID, updated.ID)
	require.Equal(t, "JP", updated.Region)
	require.Empty(t, updated.Tags)

	_, err = env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "market", ExternalRef: "bob"})
	require.NoError(t, err)
	importJSONL(t, env, "web_cookie", "{\"cookies\":\"sessionid=1\",\"_account\":\"alice\"}\n{\"cookies\":\"sessionid=2\",\"_account\":\"carol\"}")
	env.Exec(t, `UPDATE accounts SET state = 'banned' WHERE external_ref = 'carol'`)

	for i := range 5 {
		_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: fmt.Sprintf("extra-%d", i)})
		require.NoError(t, err)
	}

	listTests := []struct {
		name  string
		p     *authz.Principal
		query identitysvc.AccountQuery
		want  []string
	}{
		{"search", env.Role(authz.RoleViewer), identitysvc.AccountQuery{Search: "ALI"}, []string{"alice"}},
		{"state", env.Role(authz.RoleViewer), identitysvc.AccountQuery{State: "banned"}, []string{"carol"}},
		{"site", env.Role(authz.RoleViewer), identitysvc.AccountQuery{Site: "market"}, []string{"bob"}},
		{"restricted", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.AccountQuery{}, []string{"bob"}},
	}
	for _, tc := range listTests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := env.Service.ListAccounts(ctx, tc.p, env.NS, tc.query)
			require.NoError(t, err)
			var refs []string
			for _, a := range page.Accounts {
				refs = append(refs, a.ExternalRef)
			}
			require.ElementsMatch(t, tc.want, refs)
			require.Equal(t, len(tc.want), page.Total)
		})
	}

	t.Run("pagination and identity counts", func(t *testing.T) {
		var refs []string
		counts := map[string]int{}
		token := ""
		for {
			page, err := env.Service.ListAccounts(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.AccountQuery{PageSize: 3, PageToken: token})
			require.NoError(t, err)
			require.Equal(t, 8, page.Total)
			for _, a := range page.Accounts {
				refs = append(refs, a.ExternalRef)
				counts[a.ExternalRef] = a.IdentityCount
			}
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		}
		require.Len(t, refs, 8)
		require.Equal(t, 1, counts["alice"])
		require.Equal(t, 0, counts["bob"])
	})

	t.Run("operate account", func(t *testing.T) {
		acc, res, err := env.Service.OperateAccount(ctx, op, created.ID, identitysvc.OperationRequest{
			Operation: "ban", Duration: durationx.Permanent, Reason: "fraud",
		})
		require.NoError(t, err)
		require.Equal(t, created.ID, acc.ID)
		require.Equal(t, 1, res.Succeeded)
		calls := env.Ops.Calls()
		require.Equal(t, "account", calls[len(calls)-1].Method)
		require.Equal(t, created.ID, calls[len(calls)-1].AccountID)
	})

	errTests := []struct {
		name string
		run  func() error
		want apperr.Reason
	}{
		{"upsert viewer", func() error {
			_, err := env.Service.UpsertAccount(ctx, env.Role(authz.RoleViewer), env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "x"})
			return err
		}, apperr.ReasonPermissionDenied},
		{"upsert empty ref", func() error {
			_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop"})
			return err
		}, apperr.ReasonInvalidArgument},
		{"upsert long notes", func() error {
			_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "x", Notes: strings.Repeat("n", 5000)})
			return err
		}, apperr.ReasonInvalidArgument},
		{"upsert control characters", func() error {
			_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "x", Notes: "a\x01"})
			return err
		}, apperr.ReasonInvalidArgument},
		{"upsert bad region", func() error {
			_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "x", Region: strings.Repeat("r", 65)})
			return err
		}, apperr.ReasonInvalidArgument},
		{"upsert bad tags", func() error {
			_, err := env.Service.UpsertAccount(ctx, op, env.NS, identitysvc.AccountUpsert{Site: "shop", ExternalRef: "x", Tags: []string{"no spaces"}})
			return err
		}, apperr.ReasonInvalidArgument},
		{"list bad state", func() error {
			_, err := env.Service.ListAccounts(ctx, op, env.NS, identitysvc.AccountQuery{State: "zombie"})
			return err
		}, apperr.ReasonInvalidArgument},
		{"list no permission", func() error {
			_, err := env.Service.ListAccounts(ctx, env.NoRole(), env.NS, identitysvc.AccountQuery{})
			return err
		}, apperr.ReasonPermissionDenied},
		{"operate viewer", func() error {
			_, _, err := env.Service.OperateAccount(ctx, env.Role(authz.RoleViewer), created.ID, identitysvc.OperationRequest{Operation: "enable"})
			return err
		}, apperr.ReasonPermissionDenied},
		{"operate stranger", func() error {
			_, _, err := env.Service.OperateAccount(ctx, env.Stranger(), created.ID, identitysvc.OperationRequest{Operation: "enable"})
			return err
		}, apperr.ReasonNotFound},
		{"operate unknown", func() error {
			_, _, err := env.Service.OperateAccount(ctx, op, idgen.New(idgen.Account), identitysvc.OperationRequest{Operation: "enable"})
			return err
		}, apperr.ReasonNotFound},
		{"operate identity-only operation", func() error {
			_, _, err := env.Service.OperateAccount(ctx, op, created.ID, identitysvc.OperationRequest{Operation: "archive"})
			return err
		}, apperr.ReasonInvalidArgument},
		{"operate with scope", func() error {
			_, _, err := env.Service.OperateAccount(ctx, op, created.ID, identitysvc.OperationRequest{Operation: "enable", Scope: "identity"})
			return err
		}, apperr.ReasonInvalidArgument},
		{"operate operator failure", func() error {
			env.Ops.Err = apperr.Conflict("busy")
			defer func() { env.Ops.Err = nil }()
			_, _, err := env.Service.OperateAccount(ctx, op, created.ID, identitysvc.OperationRequest{Operation: "enable"})
			return err
		}, apperr.ReasonConflict},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			requireReason(t, tc.run(), tc.want)
		})
	}
}

func TestListStateEvents(t *testing.T) {
	env := identitysvctest.NewEnv(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	insert := func(i int, siteID, kind, action string, shadow bool) {
		env.Exec(t, `INSERT INTO state_events (id, created_at, tenant_id, namespace_id, site_id, subject_kind, subject_id, action, shadow, until)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			idgen.New(idgen.StateEvent), base.Add(time.Duration(i)*time.Minute), env.TenantID, env.NS.ID, siteID, kind,
			fmt.Sprintf("subj_%d", i), action, shadow, base.Add(time.Hour))
	}
	for i := range 10 {
		insert(i, env.SiteA.ID, "identity", []string{"ban", "cooldown"}[i%2], i == 3)
	}
	insert(10, env.SiteB.ID, "identity", "expire", false)
	insert(11, "", "proxy", "ban", false)
	env.Exec(t, `INSERT INTO state_events (id, tenant_id, namespace_id, site_id, subject_kind, subject_id, action)
		VALUES ($1, $2, $3, $4, 'identity', 'other', 'ban')`, idgen.New(idgen.StateEvent), env.OtherTenantID, env.OtherNS.ID, env.OtherSite.ID)

	viewer := env.Role(authz.RoleViewer)
	yes := true
	tests := []struct {
		name  string
		p     *authz.Principal
		query identitysvc.StateEventQuery
		want  []string
	}{
		{"all newest first", viewer, identitysvc.StateEventQuery{PageSize: 100},
			[]string{"subj_11", "subj_10", "subj_9", "subj_8", "subj_7", "subj_6", "subj_5", "subj_4", "subj_3", "subj_2", "subj_1", "subj_0"}},
		{"site restricted excludes namespace events", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.StateEventQuery{}, []string{"subj_10"}},
		{"site filter", viewer, identitysvc.StateEventQuery{Site: "market"}, []string{"subj_10"}},
		{"kind", viewer, identitysvc.StateEventQuery{SubjectKind: "proxy"}, []string{"subj_11"}},
		{"subject", viewer, identitysvc.StateEventQuery{SubjectID: "subj_4"}, []string{"subj_4"}},
		{"actions", viewer, identitysvc.StateEventQuery{Actions: []string{"expire", "ban"}, Site: "shop"}, []string{"subj_8", "subj_6", "subj_4", "subj_2", "subj_0"}},
		{"shadow", viewer, identitysvc.StateEventQuery{Shadow: &yes}, []string{"subj_3"}},
		{"time range", viewer, identitysvc.StateEventQuery{From: base.Add(2 * time.Minute), To: base.Add(4 * time.Minute)}, []string{"subj_3", "subj_2"}},
		{"identity token without proxy:read omits proxy events", env.Token(t, "identity:write"), identitysvc.StateEventQuery{From: base.Add(9 * time.Minute)},
			[]string{"subj_10", "subj_9"}},
		{"admin token sees proxy events", env.Token(t, "admin"), identitysvc.StateEventQuery{From: base.Add(10 * time.Minute)},
			[]string{"subj_11", "subj_10"}},
		{"site scoped token", env.Token(t, "identity:write:market"), identitysvc.StateEventQuery{}, []string{"subj_10"}},
		{"site filter naming a site without events", viewer, identitysvc.StateEventQuery{Site: "market", SubjectKind: "account"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := env.Service.ListStateEvents(ctx, tc.p, env.NS, tc.query)
			require.NoError(t, err)
			var got []string
			for _, ev := range page.Events {
				got = append(got, ev.SubjectID)
			}
			require.Equal(t, tc.want, got)
		})
	}

	t.Run("pagination", func(t *testing.T) {
		var got []string
		token := ""
		for {
			page, err := env.Service.ListStateEvents(ctx, viewer, env.NS, identitysvc.StateEventQuery{PageSize: 5, PageToken: token})
			require.NoError(t, err)
			for _, ev := range page.Events {
				got = append(got, ev.SubjectID)
				require.Equal(t, "default", ev.NamespaceName)
				require.NotNil(t, ev.Until)
			}
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		}
		require.Len(t, got, 12)
	})

	errTests := []struct {
		name   string
		p      *authz.Principal
		query  identitysvc.StateEventQuery
		reason apperr.Reason
	}{
		{"no permission", env.NoRole(), identitysvc.StateEventQuery{}, apperr.ReasonPermissionDenied},
		{"proxy events without proxy:read", env.Token(t, "identity:write"), identitysvc.StateEventQuery{SubjectKind: "proxy"}, apperr.ReasonScopeMissing},
		{"site the principal cannot read", env.SiteRole(authz.RoleViewer, env.SiteB), identitysvc.StateEventQuery{Site: "shop"}, apperr.ReasonPermissionDenied},
		{"bad kind", viewer, identitysvc.StateEventQuery{SubjectKind: "user"}, apperr.ReasonInvalidArgument},
		{"bad action", viewer, identitysvc.StateEventQuery{Actions: []string{"Ban!"}}, apperr.ReasonInvalidArgument},
		{"too many actions", viewer, identitysvc.StateEventQuery{Actions: manyActions(identitysvc.MaxEventActions + 1)}, apperr.ReasonInvalidArgument},
		{"long subject", viewer, identitysvc.StateEventQuery{SubjectID: strings.Repeat("s", 65)}, apperr.ReasonInvalidArgument},
		{"range order", viewer, identitysvc.StateEventQuery{From: base, To: base}, apperr.ReasonInvalidArgument},
		{"bad token", viewer, identitysvc.StateEventQuery{PageToken: "!!!"}, apperr.ReasonInvalidArgument},
	}
	for _, tc := range errTests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.Service.ListStateEvents(ctx, tc.p, env.NS, tc.query)
			requireReason(t, err, tc.reason)
		})
	}
}

func manyActions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "a" + strings.Repeat("b", i%20) + string(rune('a'+i%26))
	}
	return out
}
