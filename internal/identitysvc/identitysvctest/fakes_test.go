package identitysvctest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/identitysvc"
)

func TestFakes(t *testing.T) {
	ctx := context.Background()
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "default")
	p := authz.System("test")

	hot := &Hot{}
	opts := identitysvc.SyncOptions{ResetHealth: true}
	require.NoError(t, hot.SyncIdentities(ctx, "sit_1", []string{"idt_1"}, opts))
	require.NoError(t, hot.SyncAccounts(ctx, "sit_1", []string{"acc_1"}))
	require.NoError(t, hot.RemoveIdentities(ctx, "sit_1", []string{"idt_2"}))
	require.Len(t, hot.Calls(), 3)
	require.Equal(t, []string{"idt_1"}, hot.SyncedIDs(opts))
	hot.Err = errors.New("down")
	require.Error(t, hot.SyncAccounts(ctx, "sit_1", nil))
	hot.State = identitysvc.HotState{Present: true}
	state, err := hot.IdentityHotState(ctx, nil, "idt_1")
	require.NoError(t, err)
	require.True(t, state.Present)
	hot.Reset()
	require.Empty(t, hot.Calls())

	ops := &Operator{FailIDs: []string{"b"}, RevertIDs: []string{"x"}}
	res, err := ops.OperateIdentities(ctx, p, ns, []string{"a", "b"}, identitysvc.OperationRequest{Operation: "enable"})
	require.NoError(t, err)
	require.Equal(t, 2, res.Matched)
	require.Equal(t, 1, res.Succeeded)
	res, err = ops.OperateAccount(ctx, p, ns, "acc_1", identitysvc.OperationRequest{Operation: "ban"})
	require.NoError(t, err)
	require.Equal(t, 1, res.Succeeded)
	_, ids, err := ops.RevertActions(ctx, p, ns, identitysvc.RevertRequest{})
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, ids)
	require.Len(t, ops.Calls(), 3)
	ops.Err = errors.New("fail")
	_, err = ops.OperateIdentities(ctx, p, ns, nil, identitysvc.OperationRequest{})
	require.Error(t, err)
	_, err = ops.OperateAccount(ctx, p, ns, "", identitysvc.OperationRequest{})
	require.Error(t, err)
	_, _, err = ops.RevertActions(ctx, p, ns, identitysvc.RevertRequest{})
	require.Error(t, err)

	rec := &Audit{}
	rec.Record(ctx, audit.Entry{Action: "a"})
	rec.Record(ctx, audit.Entry{Action: "b", ResourceID: "1"})
	rec.Record(ctx, audit.Entry{Action: "b", ResourceID: "2"})
	require.Equal(t, []string{"a", "b", "b"}, rec.Actions())
	last, ok := rec.Last("b")
	require.True(t, ok)
	require.Equal(t, "2", last.ResourceID)
	_, ok = rec.Last("c")
	require.False(t, ok)

	bus := events.NewMemoryBus()
	evs := &Events{}
	unsubscribe := evs.Subscribe(bus)
	defer unsubscribe()
	require.NoError(t, bus.Publish(ctx, events.NamespaceChannel("ns_1"), events.Event{Type: "x", Data: []byte(`{"a":1}`)}))
	require.Len(t, evs.All(), 1)
	require.Equal(t, map[string]any{"a": 1.0}, Data(evs.All()[0]))
	require.Nil(t, Data(events.Event{Data: []byte(`[`)}))
}

func TestEnv(t *testing.T) {
	env := NewEnv(t)
	require.Equal(t, "shop", env.SiteA.Name)
	require.Equal(t, env.OtherNS, env.namespaceOf(env.OtherSite))
	it := env.CreateType(t, env.SiteA, AppDeviceYAML)
	env.RegisterType(t, env.SiteA, it)
	require.Contains(t, env.SiteA.IdentityTypesByID, it.ID)
	require.Equal(t, 1, env.QueryInt(t, `SELECT count(*) FROM identity_types`))
	require.True(t, env.Token(t, "identity:write").Can(authz.PermIdentityRead, authz.Resource{
		TenantID: env.TenantID, NamespaceID: env.NS.ID, NamespaceName: env.NS.Name, SiteID: env.SiteA.ID, SiteName: env.SiteA.Name,
	}))
	require.NotNil(t, env.Stranger())
	require.Empty(t, env.NoRole().Bindings)
}
