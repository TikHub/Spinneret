package secretapi_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/TikHub/Spinneret/gen/go/spinneret/v1"
	"github.com/TikHub/Spinneret/internal/api/secretapi"
	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/vault"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

func TestKEKHandlers(t *testing.T) {
	e := newEnv(t)
	e.createSecret(t, "app/key", "value")
	root := platformAdmin()

	status, err := e.h.GetKEKStatus(as(root), connect.NewRequest(&spinneretv1.GetKEKStatusRequest{}))
	require.NoError(t, err)
	require.Equal(t, vaulttest.DefaultKEKID, status.Msg.GetCurrentKekId())
	require.Len(t, status.Msg.GetKeks(), 1)
	require.True(t, status.Msg.GetKeks()[0].GetCurrent())
	require.True(t, status.Msg.GetKeks()[0].GetConfigured())
	require.EqualValues(t, 1, status.Msg.GetKeks()[0].GetWrappedRecords())
	require.False(t, status.Msg.GetRewrapRunning())
	require.Nil(t, status.Msg.GetLastRewrapFinishedAt())

	started, err := e.h.StartKEKRewrap(as(root), connect.NewRequest(&spinneretv1.StartKEKRewrapRequest{}))
	require.NoError(t, err)
	require.True(t, started.Msg.GetStarted())
	require.NoError(t, e.rw.Wait(context.Background()))

	status, err = e.h.GetKEKStatus(as(root), connect.NewRequest(&spinneretv1.GetKEKStatusRequest{}))
	require.NoError(t, err)
	require.NotNil(t, status.Msg.GetLastRewrapFinishedAt())
	require.Empty(t, status.Msg.GetLastRewrapError())
	require.Zero(t, status.Msg.GetRewrapTotal())

	entries := e.rec.byAction(secretapi.AuditActionKEKRewrapStart)
	require.Len(t, entries, 1)
	require.Equal(t, audit.ResultOK, entries[0].Result)
	require.Equal(t, true, entries[0].Details["started"])
	require.Equal(t, root.ID, entries[0].ActorID)

	// Manager failures surface as internal errors and are audited.
	failing := secretapi.New(e.store, failingKEK{}, e.cat, e.rec, nil)
	_, err = failing.GetKEKStatus(as(root), connect.NewRequest(&spinneretv1.GetKEKStatusRequest{}))
	requireReason(t, err, apperr.ReasonInternal)
	_, err = failing.StartKEKRewrap(as(root), connect.NewRequest(&spinneretv1.StartKEKRewrapRequest{}))
	requireReason(t, err, apperr.ReasonInternal)
	entries = e.rec.byAction(secretapi.AuditActionKEKRewrapStart)
	require.Len(t, entries, 2)
	require.Equal(t, audit.ResultError, entries[1].Result)

	busy := secretapi.New(e.store, conflictKEK{}, e.cat, nil, nil)
	_, err = busy.StartKEKRewrap(as(root), connect.NewRequest(&spinneretv1.StartKEKRewrapRequest{}))
	requireReason(t, err, apperr.ReasonConflict)
}

type failingKEK struct{}

func (failingKEK) Start(context.Context) (bool, error) { return false, errors.New("boom") }
func (failingKEK) Status(context.Context) (vault.KEKStatus, error) {
	return vault.KEKStatus{}, errors.New("boom")
}

type conflictKEK struct{ failingKEK }

func (conflictKEK) Start(context.Context) (bool, error) { return false, apperr.Conflict("busy") }
