package hotstate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
	"github.com/Evil0ctal/Spinneret/internal/jobs"
)

// snapshotPageSize is the number of hot_state_snapshots rows restored per page.
const snapshotPageSize = 5000

// errRebuildRunning reports that another instance holds the rebuild lock.
var errRebuildRunning = errors.New("hot-state rebuild already running")

// RebuildAll rebuilds the hot state of every site from PostgreSQL under the
// PostgreSQL advisory lock "spinneret:hotstate:rebuild": per site it writes
// the meta hash, restores health state missing in Redis from
// hot_state_snapshots, materializes proxies and identities authoritatively
// with cold-start protection (ready scores that would be <= now are spread
// uniformly over the next 60 s), prunes stale queue members and finally sets
// the epoch key to a new random id. It returns an apperr Unavailable error
// with reason "rebuilding" when another instance is already rebuilding.
func (s *Syncer) RebuildAll(ctx context.Context) error {
	acquired, _, err := s.rebuildLocked(ctx, false)
	if err != nil {
		return err
	}
	if !acquired {
		return apperr.Unavailable(apperr.ReasonRebuilding, ensurePollInterval.Milliseconds(),
			"hot-state rebuild already running").WithCause(errRebuildRunning)
	}
	return nil
}

// EnsureBuilt rebuilds the hot state when the epoch key is missing (for
// example after Redis lost its data) and reports whether this call rebuilt.
// It returns false without doing anything when the epoch exists. When another
// instance is rebuilding it waits until the epoch appears (or the lock is
// released without an epoch, in which case it rebuilds itself), bounded by ctx.
func (s *Syncer) EnsureBuilt(ctx context.Context) (bool, error) {
	for {
		exists, err := s.epochExists(ctx)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
		acquired, rebuilt, err := s.rebuildLocked(ctx, true)
		if err != nil {
			return false, err
		}
		if acquired {
			return rebuilt, nil
		}
		timer := time.NewTimer(ensurePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, fmt.Errorf("wait for hot-state rebuild: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// rebuildLocked takes the rebuild advisory lock without blocking and, when it
// was acquired, rebuilds. With onlyIfMissing the rebuild is skipped when the
// epoch appeared meanwhile. It reports whether the lock was acquired and
// whether a rebuild ran.
func (s *Syncer) rebuildLocked(ctx context.Context, onlyIfMissing bool) (acquired, rebuilt bool, err error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, false, fmt.Errorf("acquire connection for rebuild lock: %w", err)
	}
	defer conn.Release()
	key := jobs.LockKey(rebuildLockName)
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		return false, false, fmt.Errorf("try rebuild lock: %w", err)
	}
	if !ok {
		return false, false, nil
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			// Closing the session releases the lock as well.
			_ = conn.Conn().Close(uctx)
		}
	}()
	if onlyIfMissing {
		exists, err := s.epochExists(ctx)
		if err != nil || exists {
			return true, false, err
		}
	}
	if err := s.rebuild(ctx); err != nil {
		return true, false, err
	}
	return true, true, nil
}

// rebuild performs the rebuild; the caller holds the lock.
func (s *Syncer) rebuild(ctx context.Context) error {
	start := time.Now()
	epoch, err := newEpoch()
	if err != nil {
		return err
	}
	sites, err := s.q.HotstateListSites(ctx)
	if err != nil {
		return fmt.Errorf("list sites: %w", err)
	}
	s.logger.Info("hot-state rebuild started", slog.Int("sites", len(sites)))
	opts := siteSyncOptions{mode: modeAuthoritative, coldStart: true, restore: true, epoch: epoch}
	for _, site := range sites {
		if _, err := s.syncSite(ctx, site.ID, opts); err != nil {
			if apperr.IsNotFound(err) {
				continue // deleted concurrently
			}
			return fmt.Errorf("rebuild site %s: %w", site.ID, err)
		}
	}
	known := make(map[int64]struct{}, len(sites))
	for _, site := range sites {
		known[site.Hkey] = struct{}{}
	}
	if err := s.removeOrphanSites(ctx, known); err != nil {
		return err
	}
	if err := s.rdb.Do(ctx, s.rdb.B().Set().Key(s.keys.Epoch()).Value(epoch).Build()).Error(); err != nil {
		return fmt.Errorf("write hot-state epoch: %w", err)
	}
	s.logger.Info("hot-state rebuild finished", slog.Int("sites", len(sites)), slog.Duration("duration", time.Since(start)))
	return nil
}

// removeOrphanSites removes the hot state of sites whose meta hash exists in
// Redis but whose row no longer exists in PostgreSQL (for example when a
// RemoveSite call was lost). Candidates are re-checked in PostgreSQL so a site
// created during the rebuild is never removed.
func (s *Syncer) removeOrphanSites(ctx context.Context, known map[int64]struct{}) error {
	prefix := s.keyPrefix()
	candidates := make(map[int64]struct{})
	err := s.scanKeys(ctx, escapeGlob(prefix)+":{s*}:meta", func(keys []string) (bool, error) {
		for _, k := range keys {
			siteKey, rest, ok := siteKeyOf(prefix, k)
			if !ok || rest != "meta" {
				continue
			}
			if _, ok := known[siteKey]; !ok {
				candidates[siteKey] = struct{}{}
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("find orphan sites: %w", err)
	}
	if len(candidates) == 0 {
		return nil
	}
	list := make([]int64, 0, len(candidates))
	for k := range candidates {
		list = append(list, k)
	}
	existing, err := s.q.HotstateExistingSiteHkeys(ctx, list)
	if err != nil {
		return fmt.Errorf("check orphan sites: %w", err)
	}
	for _, k := range existing {
		delete(candidates, k)
	}
	for k := range candidates {
		if err := s.RemoveSite(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) epochExists(ctx context.Context) (bool, error) {
	n, err := s.rdb.Do(ctx, s.rdb.B().Exists().Key(s.keys.Epoch()).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("check hot-state epoch: %w", err)
	}
	return n > 0, nil
}

func newEpoch() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate hot-state epoch: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// restoreSnapshots copies persisted health state into Redis for entries that
// are missing there (HSETNX), before identities and proxies are scored:
// "ie" rows → "hs:<eg>" packed values, "ig" rows → identity gs/gts/gn (and
// scd/sru/lu), "ps" rows → proxy sc/sts/sn/nf/lf/cd.
func (s *Syncer) restoreSnapshots(ctx context.Context, site *catalog.Site) (int64, error) {
	var restored int64
	n, err := s.restoreIdentityEndpoint(ctx, site)
	if err != nil {
		return restored, err
	}
	restored += n
	if n, err = s.restoreIdentityGlobal(ctx, site); err != nil {
		return restored, err
	}
	restored += n
	if n, err = s.restoreProxies(ctx, site); err != nil {
		return restored, err
	}
	return restored + n, nil
}

func (s *Syncer) restoreIdentityEndpoint(ctx context.Context, site *catalog.Site) (int64, error) {
	var restored int64
	var afterSubject, afterGroup string
	for {
		rows, err := s.q.HotstateSnapshotIdentityEndpointPage(ctx, hotstatedb.HotstateSnapshotIdentityEndpointPageParams{
			SiteID: site.ID, AfterSubjectID: afterSubject, AfterGroupID: afterGroup, PageSize: snapshotPageSize,
		})
		if err != nil {
			return restored, fmt.Errorf("load identity endpoint snapshots of site %s: %w", site.ID, err)
		}
		if len(rows) == 0 {
			return restored, nil
		}
		cmds := make(rueidis.Commands, 0, len(rows))
		for _, r := range rows {
			packed := packHealth(healthEntry{
				Score: r.Score, ScoreTS: r.UpdatedAt.UnixMilli(), Samples: int64(r.Samples),
				NFail: int64(r.ConsecutiveFailures), LastFail: timeMs(r.LastFailureAt),
				Cooldown: timeMs(r.CooldownUntil), Reuse: timeMs(r.ReuseUntil), LastUsed: timeMs(r.LastUsedAt),
			})
			cmds = append(cmds, s.rdb.B().Hsetnx().Key(s.keys.Health(site.Key, r.GroupHkey)).
				Field(itoa(r.IdentityHkey)).Value(packed).Build())
		}
		if err := s.doCommands(ctx, cmds); err != nil {
			return restored, fmt.Errorf("restore identity endpoint health of site %s: %w", site.ID, err)
		}
		restored += int64(len(rows))
		last := rows[len(rows)-1]
		afterSubject, afterGroup = last.SubjectID, last.EndpointGroupID
		if len(rows) < snapshotPageSize {
			return restored, nil
		}
	}
}

func (s *Syncer) restoreIdentityGlobal(ctx context.Context, site *catalog.Site) (int64, error) {
	var restored int64
	var after string
	for {
		rows, err := s.q.HotstateSnapshotIdentityGlobalPage(ctx, hotstatedb.HotstateSnapshotIdentityGlobalPageParams{
			SiteID: site.ID, AfterSubjectID: after, PageSize: snapshotPageSize,
		})
		if err != nil {
			return restored, fmt.Errorf("load identity global snapshots of site %s: %w", site.ID, err)
		}
		if len(rows) == 0 {
			return restored, nil
		}
		cmds := make(rueidis.Commands, 0, 6*len(rows))
		for _, r := range rows {
			key := s.keys.Identity(site.Key, r.IdentityHkey)
			// A row without samples only carries the site cooldown, reuse or
			// last use: the identity had no global score to restore.
			if r.Samples > 0 {
				cmds = append(cmds,
					s.rdb.B().Hsetnx().Key(key).Field("gs").Value(formatScore(r.Score)).Build(),
					s.rdb.B().Hsetnx().Key(key).Field("gts").Value(itoa(r.UpdatedAt.UnixMilli())).Build(),
					s.rdb.B().Hsetnx().Key(key).Field("gn").Value(itoa(int64(r.Samples))).Build(),
				)
			}
			cmds = appendHsetnxPositive(s.rdb, cmds, key, []intField{
				{"scd", timeMs(r.CooldownUntil)}, {"sru", timeMs(r.ReuseUntil)}, {"lu", timeMs(r.LastUsedAt)},
			})
		}
		if err := s.doCommands(ctx, cmds); err != nil {
			return restored, fmt.Errorf("restore identity global health of site %s: %w", site.ID, err)
		}
		restored += int64(len(rows))
		after = rows[len(rows)-1].SubjectID
		if len(rows) < snapshotPageSize {
			return restored, nil
		}
	}
}

func (s *Syncer) restoreProxies(ctx context.Context, site *catalog.Site) (int64, error) {
	var restored int64
	var after string
	for {
		rows, err := s.q.HotstateSnapshotProxyPage(ctx, hotstatedb.HotstateSnapshotProxyPageParams{
			SiteID: site.ID, AfterSubjectID: after, PageSize: snapshotPageSize,
		})
		if err != nil {
			return restored, fmt.Errorf("load proxy snapshots of site %s: %w", site.ID, err)
		}
		if len(rows) == 0 {
			return restored, nil
		}
		cmds := make(rueidis.Commands, 0, 6*len(rows))
		for _, r := range rows {
			key := s.keys.ProxySite(site.Key, r.ProxyHkey)
			// A row without samples only carries the failure streak or the
			// site cooldown: the proxy had no score to restore.
			if r.Samples > 0 {
				cmds = append(cmds,
					s.rdb.B().Hsetnx().Key(key).Field("sc").Value(formatScore(r.Score)).Build(),
					s.rdb.B().Hsetnx().Key(key).Field("sts").Value(itoa(r.UpdatedAt.UnixMilli())).Build(),
					s.rdb.B().Hsetnx().Key(key).Field("sn").Value(itoa(int64(r.Samples))).Build(),
				)
			}
			cmds = appendHsetnxPositive(s.rdb, cmds, key, []intField{
				{"nf", int64(r.ConsecutiveFailures)}, {"lf", timeMs(r.LastFailureAt)}, {"cd", timeMs(r.CooldownUntil)},
			})
		}
		if err := s.doCommands(ctx, cmds); err != nil {
			return restored, fmt.Errorf("restore proxy health of site %s: %w", site.ID, err)
		}
		restored += int64(len(rows))
		after = rows[len(rows)-1].SubjectID
		if len(rows) < snapshotPageSize {
			return restored, nil
		}
	}
}

// intField is a hash field with an integer value.
type intField struct {
	name  string
	value int64
}

// appendHsetnxPositive appends "HSETNX key field value" for every field whose
// value is positive (zero means unset in the hot-state encoding).
func appendHsetnxPositive(rdb rueidis.Client, cmds rueidis.Commands, key string, fields []intField) rueidis.Commands {
	for _, f := range fields {
		if f.value > 0 {
			cmds = append(cmds, rdb.B().Hsetnx().Key(key).Field(f.name).Value(itoa(f.value)).Build())
		}
	}
	return cmds
}

// formatScore formats a score with two decimals like sp_hs_pack.
func formatScore(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	if s == "-0.00" {
		return "0.00"
	}
	return s
}
