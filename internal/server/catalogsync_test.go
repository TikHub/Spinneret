package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
	"github.com/Evil0ctal/Spinneret/internal/authz"
)

func TestCatalogSyncInterceptorSyncsAuthenticatedAdminRequests(t *testing.T) {
	var calls atomic.Int64
	var syncErr atomic.Pointer[error]
	var deadline atomic.Bool
	sync := func(ctx context.Context) error {
		calls.Add(1)
		_, ok := ctx.Deadline()
		deadline.Store(ok)
		if p := syncErr.Load(); p != nil {
			return *p
		}
		return nil
	}
	var logs bytes.Buffer
	i := newCatalogSyncInterceptor(sync, slog.New(slog.NewTextHandler(&logs, nil)))
	req := fakeRequest{spec: connect.Spec{Procedure: "/spinneret.v1.SiteAdminService/ListSites"}}
	handled := 0
	next := i.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		handled++
		return connect.NewResponse(&spinneretv1.ListSitesResponse{}), nil
	})

	// Public requests (no principal, e.g. Login) do not sync.
	_, err := next(context.Background(), req)
	require.NoError(t, err)
	require.Zero(t, calls.Load())
	require.Equal(t, 1, handled)

	authed := authz.WithPrincipal(context.Background(), authz.System("test"))
	_, err = next(authed, req)
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "authenticated admin requests sync the catalog first")
	require.True(t, deadline.Load(), "sync is bounded by a timeout")
	require.Equal(t, 2, handled)

	// A failing sync is logged once per interval and the request proceeds.
	boom := errors.New("redis down")
	syncErr.Store(&boom)
	for range 3 {
		_, err = next(authed, req)
		require.NoError(t, err)
	}
	require.Equal(t, 5, handled)
	require.Equal(t, 1, bytes.Count(logs.Bytes(), []byte("catalog sync before admin request failed")))

	// A request whose context ended does not log the failure.
	logs.Reset()
	i.lastLog.Store(0)
	canceled, cancel := context.WithCancel(authed)
	cancel()
	_, err = next(canceled, req)
	require.NoError(t, err)
	require.Zero(t, logs.Len())

	require.Equal(t, catalogSyncTimeout, i.timeout)
	require.Less(t, i.timeout, 10*time.Second)
}
