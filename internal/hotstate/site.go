package hotstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
)

// siteSyncOptions controls a full site materialization.
type siteSyncOptions struct {
	// mode is modeMerge for SyncSite (lifecycle fields already in Redis win)
	// and modeAuthoritative for rebuilds.
	mode string
	// coldStart spreads ready scores <= now over [now, now+60s].
	coldStart bool
	// restore restores health state from hot_state_snapshots before scoring.
	restore bool
	// epoch, when set, is written to the site meta "built" field.
	epoch string
}

// siteSyncStats summarizes a site materialization.
type siteSyncStats struct {
	identities   int64
	removed      int64
	proxies      int64
	prunedReady  int64
	prunedProxy  int64
	droppedGroup int64
	restored     int64
}

// SyncSite materializes a whole site: meta hash ("ns", "site", and "paused"
// read from PostgreSQL), every identity (in pages of 1000, without resets),
// ready-queue membership of every endpoint group (members that are no longer
// eligible are removed), every proxy of the namespace, and the removal of keys
// of deleted endpoint groups. Lifecycle fields that Redis-first writers (apply.lua, acquire.lua)
// already stored are kept, so a concurrent automatic ban is never reverted by
// a stale PostgreSQL read; use SyncIdentities for PostgreSQL-first changes.
func (s *Syncer) SyncSite(ctx context.Context, siteID string) error {
	_, err := s.syncSite(ctx, siteID, siteSyncOptions{mode: modeMerge})
	return err
}

// syncSite implements SyncSite and the per-site part of RebuildAll.
func (s *Syncer) syncSite(ctx context.Context, siteID string, o siteSyncOptions) (siteSyncStats, error) {
	var stats siteSyncStats
	start := time.Now()
	site, ns, err := s.siteSnapshot(ctx, siteID)
	if err != nil {
		return stats, err
	}
	// The pause switch is read from PostgreSQL: a catalog snapshot that missed
	// a switch change must not flip the flag back, and a site deleted after
	// the snapshot was taken must not be materialized again.
	ref, err := s.q.HotstateSiteRef(ctx, siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return stats, apperr.NotFound("site %s not found", siteID)
	}
	if err != nil {
		return stats, fmt.Errorf("load site %s: %w", siteID, err)
	}
	plan := newGroupPlan(site)
	groupKeys, err := s.q.HotstateSiteGroupHkeys(ctx, siteID)
	if err != nil {
		return stats, fmt.Errorf("load endpoint groups of site %s: %w", siteID, err)
	}
	if stats.droppedGroup, err = s.writeMeta(ctx, plan, ns, groupKeys, ref.Paused); err != nil {
		return stats, err
	}
	if o.restore {
		if stats.restored, err = s.restoreSnapshots(ctx, site); err != nil {
			return stats, err
		}
	}
	proxies, err := s.syncSiteProxies(ctx, site, o.mode)
	if err != nil {
		return stats, err
	}
	stats.proxies = int64(proxies.len())
	tracker, err := s.syncSiteIdentities(ctx, plan, o, &stats)
	if err != nil {
		return stats, err
	}
	if stats.prunedReady, err = s.pruneReadyQueues(ctx, plan, tracker); err != nil {
		return stats, err
	}
	if stats.prunedProxy, err = s.pruneProxyQueue(ctx, site, proxies); err != nil {
		return stats, err
	}
	if o.epoch != "" {
		// "built" is written last: the site is fully materialized for this epoch.
		cmd := s.rdb.B().Hset().Key(s.keys.SiteMeta(site.Key)).FieldValue().FieldValue("built", o.epoch).Build()
		if err := s.rdb.Do(ctx, cmd).Error(); err != nil {
			return stats, fmt.Errorf("write build epoch of site %s: %w", site.ID, err)
		}
	}
	s.logger.Info("site hot state synchronized",
		slog.String("site_id", site.ID), slog.Int64("site_key", site.Key),
		slog.Int64("identities", stats.identities), slog.Int64("removed_identities", stats.removed),
		slog.Int64("proxies", stats.proxies), slog.Int64("pruned_ready", stats.prunedReady),
		slog.Int64("pruned_proxies", stats.prunedProxy), slog.Int64("dropped_groups", stats.droppedGroup),
		slog.Int64("restored", stats.restored), slog.Duration("duration", time.Since(start)))
	return stats, nil
}

// writeMeta writes the site meta hash and drops the keys of endpoint groups
// that were materialized before but no longer exist in PostgreSQL. It returns
// the number of dropped groups.
func (s *Syncer) writeMeta(ctx context.Context, plan *groupPlan, ns *catalog.Namespace, groupKeys []int64, sitePaused bool) (int64, error) {
	site := plan.site
	meta := s.keys.SiteMeta(site.Key)
	prevRaw, err := s.rdb.Do(ctx, s.rdb.B().Hget().Key(meta).Field(metaGroupsField).Build()).ToString()
	if err != nil && !rueidis.IsRedisNil(err) {
		return 0, fmt.Errorf("read meta of site %s: %w", site.ID, err)
	}
	current := make(map[int64]struct{}, len(groupKeys)+len(plan.all))
	for _, k := range groupKeys {
		current[k] = struct{}{}
	}
	for _, k := range plan.all {
		current[k] = struct{}{}
	}
	var dropped []string
	for _, k := range parseCSVInt64(prevRaw) {
		if _, ok := current[k]; !ok {
			dropped = append(dropped, itoa(k))
		}
	}
	if len(dropped) > 0 {
		call := rueidis.LuaExec{Keys: []string{meta}, Args: append([]string{"drop_groups"}, dropped...)}
		if err := s.runScripts(ctx, s.syncMaintenance, []rueidis.LuaExec{call}, nil); err != nil {
			return 0, fmt.Errorf("drop deleted endpoint groups of site %s: %w", site.ID, err)
		}
	}
	all := make([]int64, 0, len(current))
	for k := range current {
		all = append(all, k)
	}
	paused := "0"
	if sitePaused {
		paused = "1"
	}
	cmd := s.rdb.B().Hset().Key(meta).FieldValue().
		FieldValue("ns", ns.ID).FieldValue("site", site.ID).FieldValue("paused", paused).
		FieldValue(metaGroupsField, csvInt64(sortedInt64(all))).Build()
	if err := s.rdb.Do(ctx, cmd).Error(); err != nil {
		return 0, fmt.Errorf("write meta of site %s: %w", site.ID, err)
	}
	return int64(len(dropped)), nil
}

// identityInfo is the compact per-identity eligibility record kept while a
// site is materialized (bounded: 16 bytes per identity).
type identityInfo struct {
	hkey    int64
	profile int32
	ready   bool
}

// identityTracker records the identities seen during a site materialization,
// in ascending hot-state key order (pages are loaded by hkey).
type identityTracker struct {
	items    []identityInfo
	expected map[int64]int64 // group key → eligible identities
}

func (t *identityTracker) add(info identityInfo, plan *groupPlan) {
	t.items = append(t.items, info)
	if !info.ready {
		return
	}
	for _, k := range plan.list[info.profile].eligible {
		t.expected[k]++
	}
}

// find returns the record of an identity key.
func (t *identityTracker) find(hkey int64) (identityInfo, bool) {
	i := sort.Search(len(t.items), func(i int) bool { return t.items[i].hkey >= hkey })
	if i < len(t.items) && t.items[i].hkey == hkey {
		return t.items[i], true
	}
	return identityInfo{}, false
}

// syncSiteIdentities materializes every identity of a site page by page.
func (s *Syncer) syncSiteIdentities(ctx context.Context, plan *groupPlan, o siteSyncOptions, stats *siteSyncStats) (*identityTracker, error) {
	tracker := &identityTracker{expected: make(map[int64]int64, len(plan.all))}
	batch := identityBatch{mode: o.mode, accountMode: o.mode, coldStart: o.coldStart}
	var after int64
	for {
		page, err := s.q.HotstateIdentitiesPage(ctx, hotstatedb.HotstateIdentitiesPageParams{
			SiteID: plan.site.ID, AfterHkey: after, PageSize: identityPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("load identities of site %s: %w", plan.site.ID, err)
		}
		if len(page) == 0 {
			return tracker, nil
		}
		rows := make([]identityRow, len(page))
		for i, r := range page {
			rows[i] = identityRow(r)
		}
		res, err := s.applyIdentityRows(ctx, plan, rows, batch)
		if err != nil {
			return nil, err
		}
		stats.identities += res.synced
		stats.removed += res.removed
		for _, r := range rows {
			if r.State == stateRetired {
				continue
			}
			idx := plan.profileIndex(r.Client, r.TypeID)
			tracker.add(identityInfo{
				hkey: r.Hkey, profile: int32(idx), ready: r.State == stateActive || r.State == statePending,
			}, plan)
		}
		after = page[len(page)-1].Hkey
		if len(page) < identityPageSize {
			return tracker, nil
		}
	}
}

// RemoveSite deletes every key of a site ("P:{s<key>}:*") with SCAN and
// UNLINK in batches. It is used after a site was deleted.
func (s *Syncer) RemoveSite(ctx context.Context, siteKey int64) error {
	pattern := escapeGlob(s.keys.SiteBase(siteKey)) + "*"
	removed := 0
	err := s.scanKeys(ctx, pattern, func(keys []string) (bool, error) {
		// All keys of a site share one hash tag, so a multi-key UNLINK is valid in cluster mode.
		n, err := s.rdb.Do(ctx, s.rdb.B().Unlink().Key(keys...).Build()).AsInt64()
		if err != nil {
			return false, fmt.Errorf("unlink keys: %w", err)
		}
		removed += int(n)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("remove hot state of site %d: %w", siteKey, err)
	}
	s.logger.Info("site hot state removed", slog.Int64("site_key", siteKey), slog.Int("keys", removed))
	return nil
}
