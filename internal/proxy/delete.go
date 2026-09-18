package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
)

// reasonInternal marks bulk items that failed because of an internal error.
const reasonInternal = string(apperr.ReasonInternal)

// DeleteProxies deletes proxies (proxy:write).
//
// The proxies of each namespace are deleted in one transaction: the rows are
// locked, removed from the hot state through HotSyncer.RemoveProxies while
// they still exist (so the syncer resolves identity bindings from PostgreSQL
// instead of scanning Redis, and concurrent writers that lock the rows cannot
// re-materialize them), and then deleted. Identity bindings are removed by the
// foreign key cascade. When the hot-state removal or the deletion fails,
// nothing of that namespace is deleted and its proxies are re-synchronized
// best effort, so a retry is safe: the whole request fails with internal when
// no namespace succeeded, otherwise the proxies of failed namespaces are
// reported as failed items with reason internal.
func (s *Service) DeleteProxies(ctx context.Context, p *authz.Principal, ids []string) (BulkResult, error) {
	ids, err := validateIDs(ids)
	if err != nil {
		return BulkResult{}, err
	}
	groups, res, err := s.authorizeBulk(ctx, p, ids, authz.PermProxyWrite)
	if err != nil {
		return BulkResult{}, err
	}
	var groupErrs []error
	var failedItems []BulkFailure
	for _, g := range groups {
		deleted, err := s.deleteGroup(ctx, g)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return BulkResult{}, ctxErr
			}
			s.logger.Error("proxy delete failed", slog.String("namespace_id", g.ns.ID), slog.Any("error", err))
			groupErrs = append(groupErrs, err)
			for _, id := range g.ids {
				failedItems = append(failedItems, BulkFailure{ID: id, Reason: reasonInternal, Message: "delete failed; retry the request"})
			}
			continue
		}
		removed := make(map[string]bool, len(deleted))
		for _, d := range deleted {
			removed[d.ID] = true
		}
		for _, id := range g.ids {
			if !removed[id] {
				res.Failed = append(res.Failed, BulkFailure{ID: id, Reason: reasonNotFound, Message: "proxy not found"})
			}
		}
		res.Succeeded += len(deleted)
		if len(deleted) > 0 {
			s.afterDelete(ctx, p, g.ns, deleted)
		}
	}
	if len(groupErrs) > 0 {
		if res.Succeeded == 0 {
			return BulkResult{}, apperr.Internal(errors.Join(groupErrs...))
		}
		res.Failed = append(res.Failed, failedItems...)
	}
	return res, nil
}

// deleteGroup removes the proxies of one namespace from the hot state and
// deletes them in a single transaction, returning the deleted rows.
func (s *Service) deleteGroup(ctx context.Context, g nsGroup) ([]proxydb.ProxyDeleteManyRow, error) {
	var (
		deleted []proxydb.ProxyDeleteManyRow
		locked  []string
	)
	err := inTx(ctx, s.pool, func(q *proxydb.Queries) error {
		deleted, locked = nil, nil
		rows, err := q.ProxyGetManyForUpdate(ctx, g.ids)
		if err != nil {
			return fmt.Errorf("lock proxies: %w", err)
		}
		for _, r := range rows {
			if r.NamespaceID == g.ns.ID {
				locked = append(locked, r.ID)
			}
		}
		if len(locked) == 0 {
			return nil
		}
		if err := s.removeFromHot(ctx, g.ns.ID, locked); err != nil {
			return err
		}
		if deleted, err = q.ProxyDeleteMany(ctx, locked); err != nil {
			return fmt.Errorf("delete proxies: %w", err)
		}
		return nil
	})
	if err == nil {
		return deleted, nil
	}
	if len(locked) > 0 {
		// The hot-state removal may have succeeded (partially) although the
		// rows still exist: restore them. Rows that were deleted after all are
		// removed by the syncer.
		sctx, cancel := detached(ctx)
		defer cancel()
		if serr := s.syncProxies(sctx, g.ns.ID, locked); serr != nil {
			s.logger.Error("proxy delete: hot state restore failed", slog.String("namespace_id", g.ns.ID), slog.Any("error", serr))
		}
	}
	return nil, err
}

// removeFromHot removes proxies from the hot state in bounded chunks.
func (s *Service) removeFromHot(ctx context.Context, namespaceID string, ids []string) error {
	if s.hot == nil {
		return nil
	}
	for start := 0; start < len(ids); start += hotSyncChunk {
		end := min(start+hotSyncChunk, len(ids))
		if err := s.hot.RemoveProxies(ctx, namespaceID, ids[start:end]); err != nil {
			return fmt.Errorf("remove proxies from hot state: %w", err)
		}
	}
	return nil
}

// afterDelete emits proxy.state events and audit entries of deleted proxies.
func (s *Service) afterDelete(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, rows []proxydb.ProxyDeleteManyRow) {
	sctx, cancel := detached(ctx)
	defer cancel()
	evs := make([]StateEventData, len(rows))
	for i, r := range rows {
		evs[i] = StateEventData{SubjectID: r.ID, From: r.State, Action: "delete"}
		s.record(sctx, p, ns, "proxy.delete", r.ID, r.DisplayUrl, audit.ResultOK, nil)
	}
	if err := publishState(sctx, s.bus, ns, evs); err != nil {
		s.logger.Warn("proxy delete: publish events failed", slog.String("namespace_id", ns.ID), slog.Any("error", err))
	}
}
