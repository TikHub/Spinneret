package server

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/Evil0ctal/Spinneret/internal/authz"
)

// catalogSyncTimeout bounds how long an admin request waits for catalog
// reloads before it proceeds with the current snapshots.
const catalogSyncTimeout = 3 * time.Second

// catalogSyncLogEvery throttles the warning logged when syncing fails.
const catalogSyncLogEvery = time.Minute

// catalogSyncInterceptor gives console and administration requests
// read-your-writes consistency across instances: before an authenticated
// request runs, namespaces changed on any instance (catalog change marks
// ahead of this instance's snapshots) are reloaded. Without it a request
// routed to another replica right after a write could observe the previous
// catalog, e.g. "namespace not found" right after CreateNamespace. Node-facing
// services do not use it: their hot paths tolerate the Pub/Sub propagation
// delay. Failures are logged and the request proceeds with the current
// snapshots.
type catalogSyncInterceptor struct {
	sync    func(context.Context) error
	timeout time.Duration
	logger  *slog.Logger
	lastLog atomic.Int64
}

func newCatalogSyncInterceptor(sync func(context.Context) error, logger *slog.Logger) *catalogSyncInterceptor {
	return &catalogSyncInterceptor{sync: sync, timeout: catalogSyncTimeout, logger: logger}
}

func (i *catalogSyncInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		i.syncFor(ctx, req.Spec().Procedure)
		return next(ctx, req)
	}
}

func (i *catalogSyncInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *catalogSyncInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		i.syncFor(ctx, conn.Spec().Procedure)
		return next(ctx, conn)
	}
}

// syncFor syncs the catalog for an authenticated request (public procedures
// such as Login do not read the catalog).
func (i *catalogSyncInterceptor) syncFor(ctx context.Context, procedure string) {
	if _, ok := authz.FromContext(ctx); !ok {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, i.timeout)
	defer cancel()
	err := i.sync(sctx)
	if err == nil || ctx.Err() != nil {
		return
	}
	now := time.Now().UnixNano()
	last := i.lastLog.Load()
	if now-last >= int64(catalogSyncLogEvery) && i.lastLog.CompareAndSwap(last, now) {
		i.logger.Warn("catalog sync before admin request failed; serving current snapshots",
			slog.String("procedure", procedure), slog.Any("error", err))
	}
}
