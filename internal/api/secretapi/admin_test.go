package secretapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/vault"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaulttest"
)

func TestAdminSecretFlow(t *testing.T) {
	e := newEnv(t)
	admin := as(user(e.tenant, authz.RoleAdmin))
	viewer := as(user(e.tenant, authz.RoleViewer))
	expires := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)

	created, err := e.h.CreateSecret(admin, connect.NewRequest(&spinneretv1.CreateSecretRequest{
		Namespace: "prod", Path: "signing/api_key", Value: "signer-value-abcd", Description: "signer",
		Tags: []string{"signing"}, ExpiresAt: timestamppb.New(expires),
	}))
	require.NoError(t, err)
	info := created.Msg.GetSecret()
	require.Equal(t, "prod", info.GetNamespace())
	require.Equal(t, "signing/api_key", info.GetPath())
	require.Equal(t, vault.SecretMask+"abcd", info.GetMaskedValue())
	require.EqualValues(t, 1, info.GetCurrentVersion())
	require.True(t, expires.Equal(info.GetExpiresAt().AsTime()))
	require.Nil(t, info.GetLastAccessedAt())
	for _, p := range []string{"signing/other", "vendor/a", "vendor/b"} {
		e.createSecret(t, p, "value-"+p)
	}

	got, err := e.h.GetSecret(viewer, connect.NewRequest(&spinneretv1.SecretAdminServiceGetSecretRequest{Id: info.GetId()}))
	require.NoError(t, err)
	require.Equal(t, info.GetMaskedValue(), got.Msg.GetSecret().GetMaskedValue())
	require.Equal(t, []string{"signing"}, got.Msg.GetSecret().GetTags())

	// Paged listing.
	var paths []string
	pageToken := ""
	for {
		resp, err := e.h.ListSecrets(viewer, connect.NewRequest(&spinneretv1.ListSecretsRequest{
			Namespace: "prod", PageSize: 3, PageToken: pageToken,
		}))
		require.NoError(t, err)
		require.EqualValues(t, 4, resp.Msg.GetTotal())
		for _, s := range resp.Msg.GetSecrets() {
			paths = append(paths, s.GetPath())
		}
		if resp.Msg.GetNextPageToken() == "" {
			break
		}
		pageToken = resp.Msg.GetNextPageToken()
	}
	require.Equal(t, []string{"signing/api_key", "signing/other", "vendor/a", "vendor/b"}, paths)

	filtered, err := e.h.ListSecrets(viewer, connect.NewRequest(&spinneretv1.ListSecretsRequest{
		Namespace: "prod", Search: "SIGN", Tags: []string{"signing"},
	}))
	require.NoError(t, err)
	require.Len(t, filtered.Msg.GetSecrets(), 1)
	require.Empty(t, filtered.Msg.GetNextPageToken())

	updated, err := e.h.UpdateSecret(admin, connect.NewRequest(&spinneretv1.UpdateSecretRequest{
		Id: info.GetId(), Value: ptr("rotated-value-wxyz"), Description: ptr("rotated"), SetTags: true,
		ClearExpiresAt: true,
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Msg.GetSecret().GetCurrentVersion())
	require.Equal(t, vault.SecretMask+"wxyz", updated.Msg.GetSecret().GetMaskedValue())
	require.Equal(t, "rotated", updated.Msg.GetSecret().GetDescription())
	require.Empty(t, updated.Msg.GetSecret().GetTags())
	require.Nil(t, updated.Msg.GetSecret().GetExpiresAt())

	newExpiry := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	updated, err = e.h.UpdateSecret(admin, connect.NewRequest(&spinneretv1.UpdateSecretRequest{
		Id: info.GetId(), ExpiresAt: timestamppb.New(newExpiry),
	}))
	require.NoError(t, err)
	require.True(t, newExpiry.Equal(updated.Msg.GetSecret().GetExpiresAt().AsTime()))

	revealed, err := e.h.RevealSecret(admin, connect.NewRequest(&spinneretv1.RevealSecretRequest{Id: info.GetId(), Version: 1}))
	require.NoError(t, err)
	require.Equal(t, "signer-value-abcd", revealed.Msg.GetValue())
	require.EqualValues(t, 1, revealed.Msg.GetVersion())
	revealed, err = e.h.RevealSecret(admin, connect.NewRequest(&spinneretv1.RevealSecretRequest{Id: info.GetId()}))
	require.NoError(t, err)
	require.Equal(t, "rotated-value-wxyz", revealed.Msg.GetValue())

	versions, err := e.h.ListSecretVersions(viewer, connect.NewRequest(&spinneretv1.ListSecretVersionsRequest{Id: info.GetId(), PageSize: 1}))
	require.NoError(t, err)
	require.EqualValues(t, 2, versions.Msg.GetTotal())
	require.EqualValues(t, 2, versions.Msg.GetVersions()[0].GetVersion())
	require.Equal(t, vaulttest.DefaultKEKID, versions.Msg.GetVersions()[0].GetKekId())
	require.NotEmpty(t, versions.Msg.GetNextPageToken())
	versions, err = e.h.ListSecretVersions(viewer, connect.NewRequest(&spinneretv1.ListSecretVersionsRequest{
		Id: info.GetId(), PageSize: 1, PageToken: versions.Msg.GetNextPageToken(),
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, versions.Msg.GetVersions()[0].GetVersion())
	require.Empty(t, versions.Msg.GetNextPageToken())

	var logs []*spinneretv1.SecretAccessLog
	pageToken = ""
	for {
		resp, err := e.h.ListSecretAccessLogs(viewer, connect.NewRequest(&spinneretv1.ListSecretAccessLogsRequest{
			Id: info.GetId(), PageSize: 2, PageToken: pageToken,
		}))
		require.NoError(t, err)
		logs = append(logs, resp.Msg.GetLogs()...)
		if resp.Msg.GetNextPageToken() == "" {
			break
		}
		pageToken = resp.Msg.GetNextPageToken()
	}
	actions := make([]string, len(logs))
	for i, l := range logs {
		actions[i] = l.GetAction()
	}
	require.Equal(t, []string{"secret.reveal", "secret.reveal", "secret.update", "secret.update", "secret.create"}, actions)
	require.EqualValues(t, 2, logs[0].GetVersion())
	require.EqualValues(t, 1, logs[1].GetVersion())
	require.Equal(t, "10.1.1.1", logs[0].GetIp())
	require.Equal(t, "user", logs[0].GetActorKind())

	_, err = e.h.DeleteSecret(admin, connect.NewRequest(&spinneretv1.DeleteSecretRequest{Id: info.GetId()}))
	require.NoError(t, err)
	_, err = e.h.GetSecret(viewer, connect.NewRequest(&spinneretv1.SecretAdminServiceGetSecretRequest{Id: info.GetId()}))
	requireReason(t, err, apperr.ReasonNotFound)
}

func TestAdminPermissionDenials(t *testing.T) {
	e := newEnv(t)
	s := e.createSecret(t, "app/key", "value-123456789")
	id := s.GetId()

	rpcs := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"ListSecrets", func(ctx context.Context) error {
			_, err := e.h.ListSecrets(ctx, connect.NewRequest(&spinneretv1.ListSecretsRequest{Namespace: "prod"}))
			return err
		}},
		{"GetSecret", func(ctx context.Context) error {
			_, err := e.h.GetSecret(ctx, connect.NewRequest(&spinneretv1.SecretAdminServiceGetSecretRequest{Id: id}))
			return err
		}},
		{"CreateSecret", func(ctx context.Context) error {
			_, err := e.h.CreateSecret(ctx, connect.NewRequest(&spinneretv1.CreateSecretRequest{Namespace: "prod", Path: "new/key", Value: "v"}))
			return err
		}},
		{"UpdateSecret", func(ctx context.Context) error {
			_, err := e.h.UpdateSecret(ctx, connect.NewRequest(&spinneretv1.UpdateSecretRequest{Id: id, Description: ptr("x")}))
			return err
		}},
		{"DeleteSecret", func(ctx context.Context) error {
			_, err := e.h.DeleteSecret(ctx, connect.NewRequest(&spinneretv1.DeleteSecretRequest{Id: id}))
			return err
		}},
		{"RevealSecret", func(ctx context.Context) error {
			_, err := e.h.RevealSecret(ctx, connect.NewRequest(&spinneretv1.RevealSecretRequest{Id: id}))
			return err
		}},
		{"ListSecretVersions", func(ctx context.Context) error {
			_, err := e.h.ListSecretVersions(ctx, connect.NewRequest(&spinneretv1.ListSecretVersionsRequest{Id: id}))
			return err
		}},
		{"ListSecretAccessLogs", func(ctx context.Context) error {
			_, err := e.h.ListSecretAccessLogs(ctx, connect.NewRequest(&spinneretv1.ListSecretAccessLogsRequest{Id: id}))
			return err
		}},
		{"GetKEKStatus", func(ctx context.Context) error {
			_, err := e.h.GetKEKStatus(ctx, connect.NewRequest(&spinneretv1.GetKEKStatusRequest{}))
			return err
		}},
		{"StartKEKRewrap", func(ctx context.Context) error {
			_, err := e.h.StartKEKRewrap(ctx, connect.NewRequest(&spinneretv1.StartKEKRewrapRequest{}))
			return err
		}},
	}

	denied, notFound, scope := apperr.ReasonPermissionDenied, apperr.ReasonNotFound, apperr.ReasonScopeMissing
	otherTenant := idgen.New(idgen.Tenant)
	cases := []struct {
		name   string
		ctx    context.Context
		expect map[string]apperr.Reason // rpc → reason; missing = allowed
	}{
		{"unauthenticated", context.Background(), map[string]apperr.Reason{
			"ListSecrets": apperr.ReasonSessionInvalid, "GetSecret": apperr.ReasonSessionInvalid,
			"CreateSecret": apperr.ReasonSessionInvalid, "UpdateSecret": apperr.ReasonSessionInvalid,
			"DeleteSecret": apperr.ReasonSessionInvalid, "RevealSecret": apperr.ReasonSessionInvalid,
			"ListSecretVersions": apperr.ReasonSessionInvalid, "ListSecretAccessLogs": apperr.ReasonSessionInvalid,
			"GetKEKStatus": apperr.ReasonSessionInvalid, "StartKEKRewrap": apperr.ReasonSessionInvalid,
		}},
		{"viewer", as(user(e.tenant, authz.RoleViewer)), map[string]apperr.Reason{
			"CreateSecret": denied, "UpdateSecret": denied, "DeleteSecret": denied, "RevealSecret": denied,
			"GetKEKStatus": denied, "StartKEKRewrap": denied,
		}},
		{"operator", as(user(e.tenant, authz.RoleOperator)), map[string]apperr.Reason{
			"CreateSecret": denied, "UpdateSecret": denied, "DeleteSecret": denied, "RevealSecret": denied,
			"GetKEKStatus": denied, "StartKEKRewrap": denied,
		}},
		{"owner is not platform admin", as(user(e.tenant, authz.RoleOwner)), map[string]apperr.Reason{
			"GetKEKStatus": denied, "StartKEKRewrap": denied, "DeleteSecret": "",
		}},
		{"user of another tenant", as(user(otherTenant, authz.RoleOwner)), map[string]apperr.Reason{
			"ListSecrets": notFound, "GetSecret": notFound, "CreateSecret": notFound, "UpdateSecret": notFound,
			"DeleteSecret": notFound, "RevealSecret": notFound, "ListSecretVersions": notFound,
			"ListSecretAccessLogs": notFound, "GetKEKStatus": denied, "StartKEKRewrap": denied,
		}},
		{"node token", as(token(t, e.ns, "secret:read:prod/*", "lease:acquire")), map[string]apperr.Reason{
			"ListSecrets": scope, "GetSecret": notFound, "CreateSecret": scope, "UpdateSecret": notFound,
			"DeleteSecret": notFound, "RevealSecret": notFound, "ListSecretVersions": notFound,
			"ListSecretAccessLogs": notFound, "GetKEKStatus": scope, "StartKEKRewrap": scope,
		}},
		{"admin token of other namespace", as(token(t, e.staging, "admin")), map[string]apperr.Reason{
			"ListSecrets": scope, "GetSecret": notFound, "CreateSecret": scope, "UpdateSecret": notFound,
			"DeleteSecret": notFound, "RevealSecret": notFound, "ListSecretVersions": notFound,
			"ListSecretAccessLogs": notFound, "GetKEKStatus": scope, "StartKEKRewrap": scope,
		}},
	}
	for _, c := range cases {
		for _, rpc := range rpcs {
			if rpc.name == "DeleteSecret" {
				continue // checked separately below, as a success removes the secret
			}
			t.Run(c.name+"/"+rpc.name, func(t *testing.T) {
				err := rpc.call(c.ctx)
				want, ok := c.expect[rpc.name]
				if !ok || want == "" {
					if rpc.name == "CreateSecret" && err != nil {
						// A repeated successful create reports already_exists.
						requireReason(t, err, apperr.ReasonAlreadyExists)
						return
					}
					require.NoError(t, err)
					return
				}
				requireReason(t, err, want)
			})
		}
	}
	for _, c := range cases {
		if want := c.expect["DeleteSecret"]; want != "" {
			requireReason(t, rpcs[4].call(c.ctx), want)
		}
	}
	require.NoError(t, rpcs[4].call(as(user(e.tenant, authz.RoleOwner))))
}

func TestAdminInvalidRequests(t *testing.T) {
	e := newEnv(t)
	admin := as(user(e.tenant, authz.RoleAdmin))
	s := e.createSecret(t, "app/key", "value")

	_, err := e.h.ListSecrets(admin, connect.NewRequest(&spinneretv1.ListSecretsRequest{Namespace: "prod", PageToken: "!!"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.ListSecrets(admin, connect.NewRequest(&spinneretv1.ListSecretsRequest{Namespace: "missing"}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.h.ListSecrets(admin, connect.NewRequest(&spinneretv1.ListSecretsRequest{}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(apperr.ToConnect(err)))

	_, err = e.h.ListSecretVersions(admin, connect.NewRequest(&spinneretv1.ListSecretVersionsRequest{Id: s.GetId(), PageToken: "%%"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(apperr.ToConnect(err)))
	negative, err := encode(map[string]int{"b": -3})
	require.NoError(t, err)
	_, err = e.h.ListSecretVersions(admin, connect.NewRequest(&spinneretv1.ListSecretVersionsRequest{Id: s.GetId(), PageToken: negative}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	_, err = e.h.ListSecretAccessLogs(admin, connect.NewRequest(&spinneretv1.ListSecretAccessLogsRequest{Id: s.GetId(), PageToken: "%%"}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(apperr.ToConnect(err)))
	empty, err := encode(map[string]string{"i": ""})
	require.NoError(t, err)
	_, err = e.h.ListSecretAccessLogs(admin, connect.NewRequest(&spinneretv1.ListSecretAccessLogsRequest{Id: s.GetId(), PageToken: empty}))
	requireReason(t, err, apperr.ReasonInvalidArgument)

	badTS := &timestamppb.Timestamp{Seconds: 1, Nanos: -1}
	_, err = e.h.CreateSecret(admin, connect.NewRequest(&spinneretv1.CreateSecretRequest{Namespace: "prod", Path: "x", Value: "v", ExpiresAt: badTS}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.UpdateSecret(admin, connect.NewRequest(&spinneretv1.UpdateSecretRequest{Id: s.GetId(), ExpiresAt: badTS}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.UpdateSecret(admin, connect.NewRequest(&spinneretv1.UpdateSecretRequest{
		Id: s.GetId(), ExpiresAt: timestamppb.Now(), ClearExpiresAt: true,
	}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.CreateSecret(admin, connect.NewRequest(&spinneretv1.CreateSecretRequest{Namespace: "prod", Path: "../x", Value: "v"}))
	requireReason(t, err, apperr.ReasonInvalidArgument)
	_, err = e.h.RevealSecret(admin, connect.NewRequest(&spinneretv1.RevealSecretRequest{Id: s.GetId(), Version: 9}))
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = e.h.DeleteSecret(admin, connect.NewRequest(&spinneretv1.DeleteSecretRequest{Id: "sec_missing"}))
	requireReason(t, err, apperr.ReasonNotFound)

	// Token principals resolve their own namespace; another name is refused.
	_, err = e.h.ListSecrets(as(token(t, e.ns, "admin")), connect.NewRequest(&spinneretv1.ListSecretsRequest{Namespace: "staging"}))
	requireReason(t, err, apperr.ReasonScopeMissing)
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
