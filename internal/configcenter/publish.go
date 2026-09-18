package configcenter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/configcenter/configdb"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres/db"
)

// Publish validates the draft of an item and publishes it as a new version
// (config:publish). With ExpectedVersion > 0 the publish fails with conflict
// unless the current version equals it. Validation covers syntax, the JSON
// Schema (yaml is converted to JSON), secret reference syntax and the
// existence of every referenced secret (and pinned version) in the
// namespace (failed_precondition listing the missing references). The draft
// is cleared and config/namespace events are published.
func (s *Service) Publish(ctx context.Context, p *authz.Principal, req PublishRequest) (Item, int32, error) {
	h, err := s.loadHeader(ctx, p, req.ID)
	if err != nil {
		return Item{}, 0, err
	}
	if err := s.requireMutation(ctx, p, h.NS, authz.PermConfigPublish, ActionPublish, h.ID, h.Group, h.Key); err != nil {
		return Item{}, 0, err
	}
	if err := validateText("comment", req.Comment, MaxCommentLen); err != nil {
		return Item{}, 0, err
	}
	if req.ExpectedVersion < 0 {
		return Item{}, 0, apperr.InvalidArgument("", "expected_version must not be negative")
	}
	var version int32
	actor := p.Actor()
	tctx, cancel := s.opContext(ctx)
	defer cancel()
	err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := configdb.New(tx)
		item, err := q.ConfigLockItem(tctx, h.ID)
		if err != nil {
			return pgstore.MapError(err, "config item")
		}
		if req.ExpectedVersion > 0 && item.CurrentVersion != req.ExpectedVersion {
			return apperr.Conflict("config item was modified: current version is %d, expected %d", item.CurrentVersion, req.ExpectedVersion)
		}
		if item.DraftContent == nil {
			return apperr.FailedPrecondition("", "config item %q has no draft to publish", itemName(h.Group, h.Key))
		}
		version, err = nextItemVersion(tctx, q, item)
		if err != nil {
			return err
		}
		if err := validatePublishable(tctx, q, item, *item.DraftContent); err != nil {
			return err
		}
		return insertVersion(tctx, q, h.ID, version, *item.DraftContent, req.Comment, 0, actor, true)
	})
	if err != nil {
		return Item{}, 0, wrapErr(err, "publish config item")
	}
	s.record(ctx, p, h.NS, ActionPublish, h.ID, h.Group, h.Key, audit.ResultOK,
		map[string]any{"version": version, "comment": req.Comment})
	s.announcePublished(ctx, p, h, version, 0)
	it, err := s.itemView(ctx, h.NS, h.ID)
	return it, version, err
}

// Rollback publishes the content of an old version as a new version whose
// source_version is the old version (config:publish). The content is
// validated like a publish against the current schema; the draft is kept.
func (s *Service) Rollback(ctx context.Context, p *authz.Principal, req RollbackRequest) (Item, int32, error) {
	h, err := s.loadHeader(ctx, p, req.ID)
	if err != nil {
		return Item{}, 0, err
	}
	if err := s.requireMutation(ctx, p, h.NS, authz.PermConfigPublish, ActionRollback, h.ID, h.Group, h.Key); err != nil {
		return Item{}, 0, err
	}
	if err := validateText("comment", req.Comment, MaxCommentLen); err != nil {
		return Item{}, 0, err
	}
	if req.Version < 1 {
		return Item{}, 0, apperr.InvalidArgument("", "version must be at least 1")
	}
	var version int32
	actor := p.Actor()
	tctx, cancel := s.opContext(ctx)
	defer cancel()
	err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := configdb.New(tx)
		item, err := q.ConfigLockItem(tctx, h.ID)
		if err != nil {
			return pgstore.MapError(err, "config item")
		}
		if req.Version == item.CurrentVersion {
			return apperr.FailedPrecondition("", "version %d is already the current version", req.Version)
		}
		old, err := q.ConfigGetVersion(tctx, configdb.ConfigGetVersionParams{ItemID: h.ID, Version: req.Version})
		if err != nil {
			return pgstore.MapError(err, fmt.Sprintf("config version %d", req.Version))
		}
		version, err = nextItemVersion(tctx, q, item)
		if err != nil {
			return err
		}
		if err := validatePublishable(tctx, q, item, old.Content); err != nil {
			return err
		}
		return insertVersion(tctx, q, h.ID, version, old.Content, req.Comment, req.Version, actor, false)
	})
	if err != nil {
		return Item{}, 0, wrapErr(err, "roll back config item")
	}
	s.record(ctx, p, h.NS, ActionRollback, h.ID, h.Group, h.Key, audit.ResultOK,
		map[string]any{"version": version, "source_version": req.Version, "comment": req.Comment})
	s.announcePublished(ctx, p, h, version, req.Version)
	it, err := s.itemView(ctx, h.NS, h.ID)
	return it, version, err
}

// DeleteItem deletes a config item and all of its versions (config:publish).
// Watchers are not woken by a deletion. The item's last version is kept as
// the version floor of its namespace, group and key, so an item created again
// under the same name never reuses a version number a node may still hold.
func (s *Service) DeleteItem(ctx context.Context, p *authz.Principal, id string) error {
	h, err := s.loadHeader(ctx, p, id)
	if err != nil {
		return err
	}
	if err := s.requireMutation(ctx, p, h.NS, authz.PermConfigPublish, ActionDelete, h.ID, h.Group, h.Key); err != nil {
		return err
	}
	tctx, cancel := s.opContext(ctx)
	defer cancel()
	err = pgstore.InTx(tctx, s.pool, func(tx pgx.Tx, _ *db.Queries) error {
		q := configdb.New(tx)
		item, err := q.ConfigLockItem(tctx, h.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound(itemNotFoundFormat)
		}
		if err != nil {
			return fmt.Errorf("lock config item: %w", err)
		}
		if item.CurrentVersion > 0 {
			if err := q.ConfigRaiseVersionFloor(tctx, configdb.ConfigRaiseVersionFloorParams{
				NamespaceID: item.NamespaceID, GroupName: item.GroupName, Key: item.Key, LastVersion: item.CurrentVersion,
			}); err != nil {
				return fmt.Errorf("record config version floor: %w", err)
			}
		}
		n, err := q.ConfigDeleteItem(tctx, h.ID)
		if err != nil {
			return fmt.Errorf("delete config item: %w", err)
		}
		if n == 0 {
			return apperr.NotFound(itemNotFoundFormat)
		}
		return nil
	})
	if err != nil {
		return wrapErr(err, "delete config item")
	}
	s.record(ctx, p, h.NS, ActionDelete, h.ID, h.Group, h.Key, audit.ResultOK, nil)
	s.announceDeleted(ctx, h)
	return nil
}

// nextItemVersion returns the version the next publish or rollback of the
// locked item gets: one above both its current version and the version floor
// left by deleted items of the same namespace, group and key.
func nextItemVersion(ctx context.Context, q *configdb.Queries, item configdb.ConfigItem) (int32, error) {
	floor, err := versionFloor(ctx, q, item.NamespaceID, item.GroupName, item.Key)
	if err != nil {
		return 0, err
	}
	return nextVersion(max(item.CurrentVersion, floor))
}

// versionFloor returns the highest version published by deleted items of a
// namespace, group and key (0 when none).
func versionFloor(ctx context.Context, q *configdb.Queries, namespaceID, group, key string) (int32, error) {
	floor, err := q.ConfigVersionFloor(ctx, configdb.ConfigVersionFloorParams{
		NamespaceID: namespaceID, GroupName: group, Key: key,
	})
	if err != nil {
		return 0, fmt.Errorf("load config version floor: %w", err)
	}
	return floor, nil
}

func nextVersion(current int32) (int32, error) {
	if current == math.MaxInt32 {
		return 0, apperr.FailedPrecondition("", "config item has reached the maximum version number")
	}
	return current + 1, nil
}

// validatePublishable validates content for publishing on the locked item.
func validatePublishable(ctx context.Context, q *configdb.Queries, item configdb.ConfigItem, content string) error {
	sch, err := schemaFor(item.Format, item.Schema)
	if err != nil {
		return err
	}
	refs, err := validateContent(item.Format, content, sch)
	if err != nil {
		return err
	}
	return checkSecretsExist(ctx, q, item.NamespaceID, refs)
}

// insertVersion writes a version and makes it current.
func insertVersion(ctx context.Context, q *configdb.Queries, itemID string, version int32, content, comment string,
	sourceVersion int32, actor string, clearDraft bool,
) error {
	params := configdb.ConfigInsertVersionParams{
		ItemID: itemID, Version: version, Content: content, Comment: comment, PublishedBy: actor,
	}
	if sourceVersion > 0 {
		params.SourceVersion = &sourceVersion
	}
	if _, err := q.ConfigInsertVersion(ctx, params); err != nil {
		return pgstore.MapError(err, fmt.Sprintf("config version %d", version))
	}
	if err := q.ConfigSetCurrentVersion(ctx, configdb.ConfigSetCurrentVersionParams{
		ID: itemID, Version: version, ClearDraft: clearDraft,
	}); err != nil {
		return fmt.Errorf("set current config version: %w", err)
	}
	return nil
}

// checkSecretsExist verifies that every referenced secret exists in the
// namespace and that pinned versions exist.
func checkSecretsExist(ctx context.Context, q *configdb.Queries, namespaceID string, refs []SecretRef) error {
	if len(refs) == 0 {
		return nil
	}
	paths := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	var pinnedPaths []string
	var pinnedVersions []int32
	for _, r := range refs {
		if _, ok := seen[r.Path]; !ok {
			seen[r.Path] = struct{}{}
			paths = append(paths, r.Path)
		}
		if r.Version > 0 {
			pinnedPaths = append(pinnedPaths, r.Path)
			pinnedVersions = append(pinnedVersions, int32(r.Version))
		}
	}
	existing, err := q.ConfigExistingSecretPaths(ctx, configdb.ConfigExistingSecretPathsParams{NamespaceID: namespaceID, Paths: paths})
	if err != nil {
		return fmt.Errorf("check referenced secrets: %w", err)
	}
	exists := make(map[string]struct{}, len(existing))
	for _, path := range existing {
		exists[path] = struct{}{}
	}
	versions := make(map[SecretRef]struct{})
	if len(pinnedPaths) > 0 {
		rows, err := q.ConfigExistingSecretVersions(ctx, configdb.ConfigExistingSecretVersionsParams{
			Paths: pinnedPaths, Versions: pinnedVersions, NamespaceID: namespaceID,
		})
		if err != nil {
			return fmt.Errorf("check referenced secret versions: %w", err)
		}
		for _, r := range rows {
			versions[SecretRef{Path: r.Path, Version: int(r.Version)}] = struct{}{}
		}
	}
	var missing []string
	for _, r := range refs {
		_, pathOK := exists[r.Path]
		_, versionOK := versions[r]
		if !pathOK || (r.Version > 0 && !versionOK) {
			missing = append(missing, r.String())
		}
	}
	if len(missing) > 0 {
		return apperr.FailedPrecondition("", "referenced secrets do not exist in the namespace: %s", strings.Join(missing, ", "))
	}
	return nil
}
