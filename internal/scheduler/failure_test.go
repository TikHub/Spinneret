package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// setString replaces key with a plain string so that scripts touching it
// fail with WRONGTYPE.
func (f *fixture) setString(key string) {
	f.t.Helper()
	require.NoError(f.t, f.do(f.rdb.B().Del().Key(key).Build()).Error())
	require.NoError(f.t, f.do(f.rdb.B().Set().Key(key).Value("corrupt").Build()).Error())
}

func TestScriptFailuresAreInternal(t *testing.T) {
	t.Run("acquire", func(t *testing.T) {
		f := newFixture(t)
		f.identity(1, 0, []int64{testWebGroup})
		f.setString(f.keys.Breaker(testSiteKey, testWebGroup))
		_, err := f.acquire(f.tokenCtx(), AcquireRequest{Wait: 200})
		e := requireAppErr(t, err, apperr.ReasonInternal)
		require.Equal(t, connect.CodeInternal, e.Code)
		require.NotContains(t, e.Message, "WRONGTYPE", "script errors are not exposed")
		require.Equal(t, []string{ResultError}, f.rec.acquireResults())
		require.Equal(t, "0", f.idField(1, "al"))
	})

	t.Run("renew and release", func(t *testing.T) {
		f := newFixture(t)
		id := idgen.LeasePrefix(testSiteKey) + "01"
		f.setString(f.keys.Lease(testSiteKey, id))
		_, err := f.svc.Renew(f.tokenCtx(), id, 0)
		requireReason(t, err, apperr.ReasonInternal)
		_, err = f.svc.Release(f.tokenCtx(), id)
		requireReason(t, err, apperr.ReasonInternal)
		_, err = f.svc.ReleaseLease(f.ctx, testSiteKey, id, f.clock())
		requireReason(t, err, apperr.ReasonInternal)
		require.Empty(t, f.rec.endKinds())
	})

	t.Run("reaper", func(t *testing.T) {
		f := newFixture(t)
		f.setString(f.keys.LeaseExpiry(testSiteKey))
		err := f.svc.reapOnce(f.ctx)
		require.Error(t, err)
		require.Contains(t, err.Error(), f.site.ID)
	})

	t.Run("reaper stops on canceled context", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(f.ctx)
		cancel()
		require.NoError(t, f.svc.reapOnce(ctx), "cancellation is not reported as a reap failure")
	})
}

func TestRenderFailureWithFailingReleaseIsLogged(t *testing.T) {
	f := newFixture(t)
	logs := &syncBuffer{}
	f.svc.logger = slog.New(slog.NewTextHandler(logs, nil))
	f.identity(1, 0, []int64{testWebGroup})
	f.creds.fail["idt_1"] = true
	// Corrupt the identity hash while the credential renders so that the
	// compensating release fails too.
	f.creds.onCall = func(string) { f.setString(f.keys.Identity(testSiteKey, 1)) }

	_, err := f.acquire(f.tokenCtx(), AcquireRequest{})
	requireReason(t, err, apperr.ReasonInternal)
	out := logs.String()
	require.Contains(t, out, "lease rendering failed")
	require.Contains(t, out, "release after failed acquire")
	require.NotContains(t, out, "sid=", "credentials are never logged")
}

func TestUnknownTokenNamespace(t *testing.T) {
	f := newFixture(t)
	f.identity(1, 0, []int64{testWebGroup})
	g := f.mustAcquire(AcquireRequest{})
	scopes, err := authz.ParseScopes([]string{"lease:acquire"})
	require.NoError(t, err)
	ctx := authz.WithPrincipal(f.ctx, &authz.Principal{
		Kind: authz.KindToken, ID: "tok_2", TenantID: f.ns.TenantID, NamespaceID: "ns_gone", NamespaceName: "gone", Scopes: scopes,
	})
	_, err = f.acquire(ctx, AcquireRequest{})
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.svc.Renew(ctx, g.Lease.ID, 0)
	requireReason(t, err, apperr.ReasonNotFound)
	_, err = f.svc.Release(ctx, g.Lease.ID)
	requireReason(t, err, apperr.ReasonNotFound)
	require.Equal(t, "active", f.lease(g.Lease.ID)["st"])

	// A token of another tenant bound to a namespace ID of this tenant is
	// rejected by the scope check.
	ctx = authz.WithPrincipal(f.ctx, &authz.Principal{
		Kind: authz.KindToken, ID: "tok_3", TenantID: "ten_other", NamespaceID: f.ns.ID, NamespaceName: f.ns.Name, Scopes: scopes,
	})
	_, err = f.acquire(ctx, AcquireRequest{})
	requireReason(t, err, apperr.ReasonScopeMissing)
	_, err = f.svc.Release(ctx, g.Lease.ID)
	requireReason(t, err, apperr.ReasonLeaseUnknown)
}
