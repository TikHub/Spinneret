package identityapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/api/identityapi"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc/identitysvctest"
)

func newHandler(t *testing.T) (*identityapi.Handler, *identitysvctest.Env) {
	t.Helper()
	env := identitysvctest.NewEnv(t)
	return identityapi.New(env.Catalog, env.Service, env.Hot, nil), env
}

func as(p *authz.Principal) context.Context {
	return authz.WithPrincipal(context.Background(), p)
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func TestIdentityTypeRPCs(t *testing.T) {
	h, env := newHandler(t)
	owner := as(env.Owner(env.NS))
	viewer := as(env.Role(authz.RoleViewer))

	created, err := h.CreateIdentityType(owner, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{
		Namespace: "default", Site: "shop", SpecYaml: identitysvctest.WebCookieYAML,
	}))
	require.NoError(t, err)
	it := created.Msg.GetIdentityType()
	require.Equal(t, "web_cookie", it.GetName())
	require.Equal(t, "default", it.GetNamespace())
	require.Equal(t, "shop", it.GetSite())
	require.Equal(t, "probe", it.GetActivation())
	require.Equal(t, []string{"cookies.sessionid"}, it.GetUniqueBy())
	require.Len(t, it.GetFields(), 4)
	require.Equal(t, "api_key", it.GetFields()[0].GetName())
	require.True(t, it.GetFields()[1].GetSensitive())
	require.NotEmpty(t, it.GetJsonSchema())
	require.NotNil(t, it.GetCreatedAt())

	got, err := h.GetIdentityType(viewer, connect.NewRequest(&spinneretv1.GetIdentityTypeRequest{Id: it.GetId()}))
	require.NoError(t, err)
	require.Equal(t, it.GetId(), got.Msg.GetIdentityType().GetId())

	list, err := h.ListIdentityTypes(viewer, connect.NewRequest(&spinneretv1.ListIdentityTypesRequest{Namespace: "default"}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetIdentityTypes(), 1)
	require.EqualValues(t, 1, list.Msg.GetTotal())

	updated, err := h.UpdateIdentityType(owner, connect.NewRequest(&spinneretv1.UpdateIdentityTypeRequest{
		Id: it.GetId(), SpecYaml: strings.Replace(identitysvctest.WebCookieYAML, "activation: probe", "activation: immediate", 1),
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Msg.GetIdentityType().GetVersion())

	t.Run("preview", func(t *testing.T) {
		res, err := h.PreviewDelivery(viewer, connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{
			Namespace:   "default",
			Source:      &spinneretv1.PreviewDeliveryRequest_TypeId{TypeId: it.GetId()},
			PayloadJson: `{"cookies":{"sessionid":"x"},"api_key":"k/v"}`,
		}))
		require.NoError(t, err)
		require.Empty(t, res.Msg.GetErrors())
		require.Equal(t, "sessionid=x", res.Msg.GetCredential().GetCookieHeader())
		require.Equal(t, "<secret:k/v>", res.Msg.GetCredential().GetHeaders()["X-Api-Key"])
		require.Equal(t, "x", res.Msg.GetNormalizedPayload().GetFields()["cookies"].GetStructValue().GetFields()["sessionid"].GetStringValue())

		res, err = h.PreviewDelivery(viewer, connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{
			Namespace: "default", Site: "shop",
			Source:      &spinneretv1.PreviewDeliveryRequest_SpecYaml{SpecYaml: identitysvctest.AppDeviceYAML},
			PayloadJson: `{"device_id":"d","install_id":"i","extra":{"a":[1,true]}}`,
		}))
		require.NoError(t, err)
		require.Equal(t, "d", res.Msg.GetCredential().GetQuery()["device_id"])
		require.True(t, res.Msg.GetCredential().GetJson().GetStructValue().GetFields()["a"].GetListValue().GetValues()[1].GetBoolValue())

		res, err = h.PreviewDelivery(viewer, connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{
			Namespace: "default", Source: &spinneretv1.PreviewDeliveryRequest_TypeId{TypeId: it.GetId()}, PayloadJson: `{}`,
		}))
		require.NoError(t, err)
		require.Len(t, res.Msg.GetErrors(), 1)
		require.Nil(t, res.Msg.GetCredential())

		_, err = h.PreviewDelivery(viewer, connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{Namespace: "default", PayloadJson: `{}`}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	denials := []struct {
		name   string
		call   func() error
		reason apperr.Reason
	}{
		{"unauthenticated", func() error {
			_, err := h.ListIdentityTypes(context.Background(), connect.NewRequest(&spinneretv1.ListIdentityTypesRequest{Namespace: "default"}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"unknown namespace", func() error {
			_, err := h.ListIdentityTypes(viewer, connect.NewRequest(&spinneretv1.ListIdentityTypesRequest{Namespace: "nope"}))
			return err
		}, apperr.ReasonNotFound},
		{"token of another namespace name", func() error {
			_, err := h.ListIdentityTypes(as(env.Token(t, "identity:write")), connect.NewRequest(&spinneretv1.ListIdentityTypesRequest{Namespace: "other"}))
			return err
		}, apperr.ReasonScopeMissing},
		{"viewer create", func() error {
			_, err := h.CreateIdentityType(viewer, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{Namespace: "default", Site: "shop", SpecYaml: identitysvctest.AppDeviceYAML}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"viewer update", func() error {
			_, err := h.UpdateIdentityType(viewer, connect.NewRequest(&spinneretv1.UpdateIdentityTypeRequest{Id: it.GetId(), SpecYaml: identitysvctest.WebCookieYAML}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"viewer delete", func() error {
			_, err := h.DeleteIdentityType(viewer, connect.NewRequest(&spinneretv1.DeleteIdentityTypeRequest{Id: it.GetId()}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"stranger get", func() error {
			_, err := h.GetIdentityType(as(env.Stranger()), connect.NewRequest(&spinneretv1.GetIdentityTypeRequest{Id: it.GetId()}))
			return err
		}, apperr.ReasonNotFound},
		{"unauthenticated get", func() error {
			_, err := h.GetIdentityType(context.Background(), connect.NewRequest(&spinneretv1.GetIdentityTypeRequest{Id: it.GetId()}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"unauthenticated update", func() error {
			_, err := h.UpdateIdentityType(context.Background(), connect.NewRequest(&spinneretv1.UpdateIdentityTypeRequest{Id: it.GetId()}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"unauthenticated delete", func() error {
			_, err := h.DeleteIdentityType(context.Background(), connect.NewRequest(&spinneretv1.DeleteIdentityTypeRequest{Id: it.GetId()}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"unauthenticated create", func() error {
			_, err := h.CreateIdentityType(context.Background(), connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{Namespace: "default"}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"unauthenticated preview", func() error {
			_, err := h.PreviewDelivery(context.Background(), connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{Namespace: "default"}))
			return err
		}, apperr.ReasonSessionInvalid},
		{"preview denied", func() error {
			_, err := h.PreviewDelivery(as(env.SiteRole(authz.RoleViewer, env.SiteB)), connect.NewRequest(&spinneretv1.PreviewDeliveryRequest{
				Namespace: "default", Source: &spinneretv1.PreviewDeliveryRequest_TypeId{TypeId: it.GetId()}, PayloadJson: `{}`,
			}))
			return err
		}, apperr.ReasonPermissionDenied},
	}
	for _, tc := range denials {
		t.Run(tc.name, func(t *testing.T) {
			requireReason(t, tc.call(), tc.reason)
		})
	}

	_, err = h.DeleteIdentityType(owner, connect.NewRequest(&spinneretv1.DeleteIdentityTypeRequest{Id: it.GetId()}))
	require.NoError(t, err)
}

func TestIdentityRPCs(t *testing.T) {
	h, env := newHandler(t)
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	op := as(env.Role(authz.RoleOperator))
	viewer := as(env.Role(authz.RoleViewer))

	imported, err := h.ImportIdentities(op, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: "default", Site: "shop", Type: "web_cookie", Format: "jsonl",
		Data: "{\"cookies\":\"sessionid=one\",\"signature\":\"secret-token\",\"_tags\":[\"t\"],\"_labels\":{\"k\":\"one\"},\"_account\":\"alice\"}\n{}",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, imported.Msg.GetCreated())
	require.Len(t, imported.Msg.GetFailed(), 1)
	require.EqualValues(t, 2, imported.Msg.GetFailed()[0].GetLine())

	list, err := h.ListIdentities(viewer, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{
		Namespace: "default", Filter: &spinneretv1.IdentityFilter{Tags: []string{"t"}}, OrderBy: "score", Descending: true,
	}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetIdentities(), 1)
	ident := list.Msg.GetIdentities()[0]
	require.Equal(t, "pending", ident.GetState())
	require.Equal(t, "alice", ident.GetAccountRef())
	require.Equal(t, map[string]string{"k": "one"}, ident.GetLabels())
	require.EqualValues(t, 70, ident.GetGlobalScore())
	id := ident.GetId()

	t.Run("get with hot state", func(t *testing.T) {
		env.Hot.State = identitysvc.HotState{
			Present: true, State: "pending", ActiveLeases: 2, GlobalScore: 55.5, GlobalSamples: 9, BoundProxyID: "pxy_1",
			SiteCooldownUntil: time.Now().Add(time.Minute),
			Groups:            []identitysvc.EndpointHotState{{EndpointGroup: "_default", EndpointGroupID: "eg_1", Client: "web", Score: 40, Samples: 3, InReadyQueue: true}},
		}
		res, err := h.GetIdentity(viewer, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id, Reveal: true}))
		require.NoError(t, err)
		require.False(t, res.Msg.GetRevealed())
		require.True(t, strings.HasPrefix(res.Msg.GetPayload().GetFields()["signature"].GetStringValue(), "••••"))
		require.EqualValues(t, 2, res.Msg.GetIdentity().GetActiveLeases())
		require.Equal(t, 55.5, res.Msg.GetIdentity().GetGlobalScore())
		require.Equal(t, "pxy_1", res.Msg.GetIdentity().GetBoundProxyId())
		require.True(t, res.Msg.GetHotState().GetPresent())
		require.NotNil(t, res.Msg.GetHotState().GetSiteCooldownUntil())
		require.Nil(t, res.Msg.GetHotState().GetExclusiveUntil())
		require.Len(t, res.Msg.GetHotState().GetGroups(), 1)
		require.True(t, res.Msg.GetHotState().GetGroups()[0].GetInReadyQueue())

		revealed, err := h.GetIdentity(as(env.Owner(env.NS)), connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id, Reveal: true}))
		require.NoError(t, err)
		require.True(t, revealed.Msg.GetRevealed())
		require.Equal(t, "secret-token", revealed.Msg.GetPayload().GetFields()["signature"].GetStringValue())

		env.Hot.StateErr = errors.New("redis down")
		defer func() { env.Hot.StateErr = nil }()
		res, err = h.GetIdentity(viewer, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
		require.NoError(t, err)
		require.Nil(t, res.Msg.GetHotState())
		require.EqualValues(t, 70, res.Msg.GetIdentity().GetGlobalScore())
	})

	t.Run("hot state rpc", func(t *testing.T) {
		env.Hot.State = identitysvc.HotState{Present: true, State: "active"}
		res, err := h.GetIdentityHotState(viewer, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
		require.NoError(t, err)
		require.Equal(t, "active", res.Msg.GetHotState().GetState())

		env.Hot.StateErr = errors.New("redis down")
		_, err = h.GetIdentityHotState(viewer, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
		requireReason(t, err, apperr.ReasonInternal)
		env.Hot.StateErr = apperr.Unavailable(apperr.ReasonRebuilding, 100, "rebuilding")
		_, err = h.GetIdentityHotState(viewer, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
		requireReason(t, err, apperr.ReasonRebuilding)
		env.Hot.StateErr = nil

		noHot := identityapi.New(env.Catalog, env.Service, nil, nil)
		_, err = noHot.GetIdentityHotState(viewer, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
		requireReason(t, err, apperr.ReasonRebuilding)
		detail, err := noHot.GetIdentity(viewer, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
		require.NoError(t, err)
		require.Nil(t, detail.Msg.GetHotState())

		_, err = h.GetIdentityHotState(as(env.Stranger()), connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
		requireReason(t, err, apperr.ReasonNotFound)
	})

	t.Run("update payload and attributes", func(t *testing.T) {
		payload, err := structpb.NewStruct(map[string]any{"cookies": map[string]any{"sessionid": "one", "v": "2"}})
		require.NoError(t, err)
		res, err := h.UpdateIdentityPayload(op, connect.NewRequest(&spinneretv1.UpdateIdentityPayloadRequest{Id: id, Payload: payload}))
		require.NoError(t, err)
		require.EqualValues(t, 2, res.Msg.GetIdentity().GetPayloadVersion())
		_, err = h.UpdateIdentityPayload(op, connect.NewRequest(&spinneretv1.UpdateIdentityPayloadRequest{Id: id}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.UpdateIdentityPayload(viewer, connect.NewRequest(&spinneretv1.UpdateIdentityPayloadRequest{Id: id, Payload: payload}))
		requireReason(t, err, apperr.ReasonPermissionDenied)

		region := "JP"
		upd, err := h.UpdateIdentity(op, connect.NewRequest(&spinneretv1.UpdateIdentityRequest{
			Id: id, Region: &region, SetLabels: true, Labels: map[string]string{"x": "y"},
		}))
		require.NoError(t, err)
		require.Equal(t, "JP", upd.Msg.GetIdentity().GetRegion())
		require.Equal(t, []string{"t"}, upd.Msg.GetIdentity().GetTags())
		require.Equal(t, map[string]string{"x": "y"}, upd.Msg.GetIdentity().GetLabels())
		_, err = h.UpdateIdentity(viewer, connect.NewRequest(&spinneretv1.UpdateIdentityRequest{Id: id, Region: &region}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
	})

	t.Run("state events", func(t *testing.T) {
		env.Exec(t, `UPDATE identities SET state = 'expired' WHERE id = $1`, id)
		payload, err := structpb.NewStruct(map[string]any{"cookies": "sessionid=one; v=3"})
		require.NoError(t, err)
		_, err = h.UpdateIdentityPayload(op, connect.NewRequest(&spinneretv1.UpdateIdentityPayloadRequest{Id: id, Payload: payload}))
		require.NoError(t, err)
		res, err := h.ListStateEvents(viewer, connect.NewRequest(&spinneretv1.ListStateEventsRequest{
			Namespace: "default", SubjectKind: "identity",
			TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(time.Now().Add(-time.Hour)), End: timestamppb.New(time.Now().Add(time.Hour))},
		}))
		require.NoError(t, err)
		require.Len(t, res.Msg.GetEvents(), 1)
		ev := res.Msg.GetEvents()[0]
		require.Equal(t, "expired", ev.GetFromState())
		require.Equal(t, "pending", ev.GetToState())
		require.Equal(t, "payload_update", ev.GetAction())
		require.Equal(t, "shop", ev.GetSite())
		require.Equal(t, "default", ev.GetNamespace())

		detail, err := h.GetIdentity(viewer, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
		require.NoError(t, err)
		require.Len(t, detail.Msg.GetRecentEvents(), 1)

		_, err = h.ListStateEvents(viewer, connect.NewRequest(&spinneretv1.ListStateEventsRequest{
			Namespace: "default", TimeRange: &spinneretv1.TimeRange{Start: &timestamppb.Timestamp{Seconds: -1 << 62}},
		}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.ListStateEvents(viewer, connect.NewRequest(&spinneretv1.ListStateEventsRequest{
			Namespace: "default", TimeRange: &spinneretv1.TimeRange{End: &timestamppb.Timestamp{Nanos: -1}},
		}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
	})

	denials := []struct {
		name   string
		call   func(ctx context.Context) error
		reason apperr.Reason
	}{
		{"list", func(ctx context.Context) error {
			_, err := h.ListIdentities(ctx, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{Namespace: "default"}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"import", func(ctx context.Context) error {
			_, err := h.ImportIdentities(ctx, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{Namespace: "default", Site: "shop", Type: "web_cookie", Format: "jsonl", Data: "{}"}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"events", func(ctx context.Context) error {
			_, err := h.ListStateEvents(ctx, connect.NewRequest(&spinneretv1.ListStateEventsRequest{Namespace: "default"}))
			return err
		}, apperr.ReasonPermissionDenied},
		{"get", func(ctx context.Context) error {
			_, err := h.GetIdentity(ctx, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
			return err
		}, apperr.ReasonPermissionDenied},
	}
	restricted := as(env.SiteRole(authz.RoleViewer, env.SiteB))
	for _, tc := range denials {
		t.Run("denied "+tc.name, func(t *testing.T) {
			ctx := restricted
			if tc.name == "list" || tc.name == "events" || tc.name == "import" {
				ctx = as(env.NoRole())
			}
			requireReason(t, tc.call(ctx), tc.reason)
			requireReason(t, tc.call(context.Background()), apperr.ReasonSessionInvalid)
		})
	}
	for _, call := range []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := h.GetIdentityHotState(ctx, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
			return err
		},
		func(ctx context.Context) error {
			_, err := h.UpdateIdentityPayload(ctx, connect.NewRequest(&spinneretv1.UpdateIdentityPayloadRequest{Id: id}))
			return err
		},
		func(ctx context.Context) error {
			_, err := h.UpdateIdentity(ctx, connect.NewRequest(&spinneretv1.UpdateIdentityRequest{Id: id}))
			return err
		},
	} {
		requireReason(t, call(context.Background()), apperr.ReasonSessionInvalid)
	}
}

func TestOperationAndAccountRPCs(t *testing.T) {
	h, env := newHandler(t)
	env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	op := as(env.Role(authz.RoleOperator))
	viewer := as(env.Role(authz.RoleViewer))
	_, err := h.ImportIdentities(op, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Namespace: "default", Site: "shop", Type: "web_cookie", Format: "csv",
		Data: "cookies,_account\nsessionid=a,alice\nsessionid=b,alice\n",
	}))
	require.NoError(t, err)
	var ids []string
	rows, err := env.Pool.Query(context.Background(), `SELECT id FROM identities ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	require.Len(t, ids, 2)

	t.Run("operate identities", func(t *testing.T) {
		env.Ops.FailIDs = []string{ids[1]}
		defer func() { env.Ops.FailIDs = nil }()
		res, err := h.OperateIdentities(op, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{
			Ids: ids, Operation: "cooldown", Duration: "1d12h", Reason: "manual",
		}))
		require.NoError(t, err)
		require.EqualValues(t, 2, res.Msg.GetResult().GetMatched())
		require.EqualValues(t, 1, res.Msg.GetResult().GetSucceeded())
		require.Len(t, res.Msg.GetResult().GetFailed(), 1)
		calls := env.Ops.Calls()
		require.Equal(t, 36*time.Hour, calls[len(calls)-1].Request.Duration.Std())

		_, err = h.OperateIdentities(op, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{Ids: ids, Operation: "cooldown", Duration: "soon"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.OperateIdentities(viewer, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{Ids: ids, Operation: "enable"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.OperateIdentities(context.Background(), connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{Ids: ids, Operation: "enable"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("bulk operate", func(t *testing.T) {
		res, err := h.BulkOperateIdentities(op, connect.NewRequest(&spinneretv1.BulkOperateIdentitiesRequest{
			Namespace: "default", Filter: &spinneretv1.IdentityFilter{Site: "shop"}, Operation: "ban", Duration: "permanent", DryRun: true,
		}))
		require.NoError(t, err)
		require.EqualValues(t, 2, res.Msg.GetResult().GetMatched())

		_, err = h.BulkOperateIdentities(op, connect.NewRequest(&spinneretv1.BulkOperateIdentitiesRequest{Namespace: "default", Operation: "enable"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.BulkOperateIdentities(op, connect.NewRequest(&spinneretv1.BulkOperateIdentitiesRequest{
			Namespace: "default", Filter: &spinneretv1.IdentityFilter{}, Operation: "cooldown", Duration: "-1s",
		}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.BulkOperateIdentities(viewer, connect.NewRequest(&spinneretv1.BulkOperateIdentitiesRequest{
			Namespace: "default", Filter: &spinneretv1.IdentityFilter{}, Operation: "enable",
		}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.BulkOperateIdentities(context.Background(), connect.NewRequest(&spinneretv1.BulkOperateIdentitiesRequest{Namespace: "default"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("revert", func(t *testing.T) {
		env.Ops.RevertIDs = []string{ids[0]}
		res, err := h.RevertActions(op, connect.NewRequest(&spinneretv1.RevertActionsRequest{
			Namespace: "default", Rule: "r", TimeRange: &spinneretv1.TimeRange{Start: timestamppb.New(time.Now().Add(-time.Hour))},
		}))
		require.NoError(t, err)
		require.Equal(t, []string{ids[0]}, res.Msg.GetIdentityIds())
		require.EqualValues(t, 1, res.Msg.GetResult().GetSucceeded())

		_, err = h.RevertActions(op, connect.NewRequest(&spinneretv1.RevertActionsRequest{Namespace: "default"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.RevertActions(op, connect.NewRequest(&spinneretv1.RevertActionsRequest{
			Namespace: "default", TimeRange: &spinneretv1.TimeRange{Start: &timestamppb.Timestamp{Nanos: -5}},
		}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.RevertActions(viewer, connect.NewRequest(&spinneretv1.RevertActionsRequest{
			Namespace: "default", TimeRange: &spinneretv1.TimeRange{Start: timestamppb.Now()},
		}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.RevertActions(context.Background(), connect.NewRequest(&spinneretv1.RevertActionsRequest{Namespace: "default"}))
		requireReason(t, err, apperr.ReasonSessionInvalid)
	})

	t.Run("accounts", func(t *testing.T) {
		up, err := h.UpsertAccount(op, connect.NewRequest(&spinneretv1.UpsertAccountRequest{
			Namespace: "default", Site: "shop", ExternalRef: "alice", Region: "US", Tags: []string{"vip"}, Notes: "n",
		}))
		require.NoError(t, err)
		acc := up.Msg.GetAccount()
		require.Equal(t, "shop", acc.GetSite())
		require.EqualValues(t, 2, acc.GetIdentityCount())
		require.Equal(t, []string{"vip"}, acc.GetTags())

		list, err := h.ListAccounts(viewer, connect.NewRequest(&spinneretv1.ListAccountsRequest{Namespace: "default", Search: "ali"}))
		require.NoError(t, err)
		require.Len(t, list.Msg.GetAccounts(), 1)
		require.EqualValues(t, 1, list.Msg.GetTotal())

		opRes, err := h.OperateAccount(op, connect.NewRequest(&spinneretv1.OperateAccountRequest{
			Id: acc.GetId(), Operation: "cooldown", Duration: "30m", Reason: "r",
		}))
		require.NoError(t, err)
		require.Equal(t, acc.GetId(), opRes.Msg.GetAccount().GetId())
		require.EqualValues(t, 1, opRes.Msg.GetIdentities().GetSucceeded())

		_, err = h.OperateAccount(op, connect.NewRequest(&spinneretv1.OperateAccountRequest{Id: acc.GetId(), Operation: "ban", Duration: "forever"}))
		requireReason(t, err, apperr.ReasonInvalidArgument)
		_, err = h.OperateAccount(viewer, connect.NewRequest(&spinneretv1.OperateAccountRequest{Id: acc.GetId(), Operation: "enable"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.UpsertAccount(viewer, connect.NewRequest(&spinneretv1.UpsertAccountRequest{Namespace: "default", Site: "shop", ExternalRef: "x"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		_, err = h.ListAccounts(as(env.NoRole()), connect.NewRequest(&spinneretv1.ListAccountsRequest{Namespace: "default"}))
		requireReason(t, err, apperr.ReasonPermissionDenied)
		for _, call := range []func() error{
			func() error {
				_, err := h.OperateAccount(context.Background(), connect.NewRequest(&spinneretv1.OperateAccountRequest{Id: acc.GetId()}))
				return err
			},
			func() error {
				_, err := h.UpsertAccount(context.Background(), connect.NewRequest(&spinneretv1.UpsertAccountRequest{Namespace: "default"}))
				return err
			},
			func() error {
				_, err := h.ListAccounts(context.Background(), connect.NewRequest(&spinneretv1.ListAccountsRequest{Namespace: "default"}))
				return err
			},
		} {
			requireReason(t, call(), apperr.ReasonSessionInvalid)
		}
	})
}

func TestTokenPrincipals(t *testing.T) {
	h, env := newHandler(t)
	web := env.CreateType(t, env.SiteA, identitysvctest.WebCookieYAML)
	shopToken := as(env.Token(t, "identity:write:shop"))
	marketToken := as(env.Token(t, "identity:write:market"))

	imported, err := h.ImportIdentities(shopToken, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
		Site: "shop", Type: "web_cookie", Format: "jsonl", Data: `{"cookies":"sessionid=tok","signature":"secret-value"}`,
	}))
	require.NoError(t, err, "tokens resolve their own namespace without a namespace name")
	require.EqualValues(t, 1, imported.Msg.GetCreated())

	list, err := h.ListIdentities(shopToken, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.GetIdentities(), 1)
	id := list.Msg.GetIdentities()[0].GetId()

	detail, err := h.GetIdentity(shopToken, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id, Reveal: true}))
	require.NoError(t, err)
	require.False(t, detail.Msg.GetRevealed(), "identity:write tokens cannot reveal payloads")
	require.True(t, strings.HasPrefix(detail.Msg.GetPayload().GetFields()["signature"].GetStringValue(), "••••"))

	empty, err := h.ListIdentities(marketToken, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{}))
	require.NoError(t, err)
	require.Empty(t, empty.Msg.GetIdentities(), "site-scoped tokens only list their sites")

	otherNamespace := env.Token(t, "admin")
	otherNamespace.NamespaceID, otherNamespace.NamespaceName, otherNamespace.TenantID = env.OtherNS.ID, env.OtherNS.Name, env.OtherTenantID

	denials := []struct {
		name   string
		call   func() error
		reason apperr.Reason
	}{
		{"get on another site", func() error {
			_, err := h.GetIdentity(marketToken, connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
			return err
		}, apperr.ReasonScopeMissing},
		{"get from another tenant", func() error {
			_, err := h.GetIdentity(as(otherNamespace), connect.NewRequest(&spinneretv1.GetIdentityRequest{Id: id}))
			return err
		}, apperr.ReasonNotFound},
		{"list naming a foreign site", func() error {
			_, err := h.ListIdentities(marketToken, connect.NewRequest(&spinneretv1.ListIdentitiesRequest{Filter: &spinneretv1.IdentityFilter{Site: "shop"}}))
			return err
		}, apperr.ReasonScopeMissing},
		{"import into another site", func() error {
			_, err := h.ImportIdentities(marketToken, connect.NewRequest(&spinneretv1.ImportIdentitiesRequest{
				Site: "shop", Type: "web_cookie", Format: "jsonl", Data: `{"cookies":"sessionid=x"}`,
			}))
			return err
		}, apperr.ReasonScopeMissing},
		{"create identity type", func() error {
			_, err := h.CreateIdentityType(shopToken, connect.NewRequest(&spinneretv1.CreateIdentityTypeRequest{
				Site: "shop", SpecYaml: identitysvctest.AppDeviceYAML,
			}))
			return err
		}, apperr.ReasonScopeMissing},
		{"update identity type", func() error {
			_, err := h.UpdateIdentityType(shopToken, connect.NewRequest(&spinneretv1.UpdateIdentityTypeRequest{Id: web.ID, SpecYaml: identitysvctest.WebCookieYAML}))
			return err
		}, apperr.ReasonScopeMissing},
		{"operate on another site", func() error {
			_, err := h.OperateIdentities(marketToken, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{Ids: []string{id}, Operation: "disable"}))
			return err
		}, apperr.ReasonScopeMissing},
		{"hot state on another site", func() error {
			_, err := h.GetIdentityHotState(marketToken, connect.NewRequest(&spinneretv1.GetIdentityHotStateRequest{Id: id}))
			return err
		}, apperr.ReasonScopeMissing},
		{"proxy state events", func() error {
			_, err := h.ListStateEvents(shopToken, connect.NewRequest(&spinneretv1.ListStateEventsRequest{SubjectKind: "proxy"}))
			return err
		}, apperr.ReasonScopeMissing},
		{"namespace name of another namespace", func() error {
			_, err := h.ListAccounts(shopToken, connect.NewRequest(&spinneretv1.ListAccountsRequest{Namespace: "other"}))
			return err
		}, apperr.ReasonScopeMissing},
	}
	for _, tc := range denials {
		t.Run(tc.name, func(t *testing.T) {
			requireReason(t, tc.call(), tc.reason)
		})
	}

	res, err := h.OperateIdentities(shopToken, connect.NewRequest(&spinneretv1.OperateIdentitiesRequest{
		Ids: []string{id, "idt_unknown"}, Operation: "disable",
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Msg.GetResult().GetSucceeded())
	require.Len(t, res.Msg.GetResult().GetFailed(), 1)
	require.Equal(t, "not_found", res.Msg.GetResult().GetFailed()[0].GetReason())
}
