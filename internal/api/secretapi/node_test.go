package secretapi_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/vault"
)

func TestNodeGetSecret(t *testing.T) {
	e := newEnv(t)
	s := e.createSecret(t, "signing/api_key", "node-value")
	_, err := e.h.UpdateSecret(as(user(e.tenant, authz.RoleAdmin)), connect.NewRequest(&spinneretv1.UpdateSecretRequest{
		Id: s.GetId(), Value: ptr("node-value-2"), ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)),
	}))
	require.NoError(t, err)

	node := token(t, e.ns, "secret:read:prod/signing/*")
	resp, err := e.node.GetSecret(as(node), connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "signing/api_key"}))
	require.NoError(t, err)
	require.Equal(t, "signing/api_key", resp.Msg.GetPath())
	require.EqualValues(t, 2, resp.Msg.GetVersion())
	require.Equal(t, "node-value-2", resp.Msg.GetValue())
	require.NotNil(t, resp.Msg.GetExpiresAt())

	resp, err = e.node.GetSecret(as(node), connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "signing/api_key", Version: 1}))
	require.NoError(t, err)
	require.Equal(t, "node-value", resp.Msg.GetValue())

	reads := e.rec.byAction(vault.AuditActionSecretRead)
	require.Len(t, reads, 2)
	require.Equal(t, "api", reads[0].Details["purpose"])
	require.Equal(t, audit.ResultOK, reads[0].Result)
	require.Equal(t, node.ID, reads[0].ActorID)

	tests := []struct {
		name   string
		ctx    context.Context
		path   string
		reason apperr.Reason
	}{
		{"scope glob mismatch", as(token(t, e.ns, "secret:read:prod/vendor/*")), "signing/api_key", apperr.ReasonScopeMissing},
		{"no secret scope", as(token(t, e.ns, "lease:acquire", "report:write")), "signing/api_key", apperr.ReasonScopeMissing},
		{"admin token", as(token(t, e.ns, "admin")), "signing/api_key", apperr.ReasonScopeMissing},
		{"token of other namespace", as(token(t, e.staging, "secret:read:*")), "signing/api_key", apperr.ReasonNotFound},
		{"missing secret", as(node), "signing/missing", apperr.ReasonNotFound},
		{"user", as(user(e.tenant, authz.RoleOwner)), "signing/api_key", apperr.ReasonPermissionDenied},
		{"unauthenticated", context.Background(), "signing/api_key", apperr.ReasonSessionInvalid},
		{"invalid path", as(node), "signing//api_key", apperr.ReasonInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.node.GetSecret(tt.ctx, connect.NewRequest(&spinneretv1.GetSecretRequest{Path: tt.path}))
			requireReason(t, err, tt.reason)
		})
	}
	denied := 0
	for _, r := range e.rec.byAction(vault.AuditActionSecretRead) {
		if r.Result == audit.ResultDenied {
			denied++
		}
	}
	require.Equal(t, 3, denied)

	// A token whose namespace is missing from the catalog.
	e.cat.Remove(e.ns.ID)
	_, err = e.node.GetSecret(as(node), connect.NewRequest(&spinneretv1.GetSecretRequest{Path: "signing/api_key"}))
	requireReason(t, err, apperr.ReasonNotFound)
}
