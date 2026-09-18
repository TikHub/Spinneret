package hotstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
	"github.com/Evil0ctal/Spinneret/internal/jobs"
)

// Snapshot job parameters (spec §6.8: leader, 60 s).
const (
	snapshotJobName     = "hotstate_snapshot"
	snapshotInterval    = 60 * time.Second
	snapshotTimeout     = 50 * time.Second
	snapshotBudget      = 20 * time.Second
	snapshotPopCount    = 5000
	snapshotCacheLimit  = 200_000
	subjectIdentityEG   = "ie"
	subjectIdentityGlob = "ig"
	subjectProxySite    = "ps"
)

// SnapshotJob returns the leader job that persists changed hot state to
// hot_state_snapshots every 60 s.
func (s *Syncer) SnapshotJob() jobs.Job {
	return jobs.Job{
		Name:     snapshotJobName,
		Interval: snapshotInterval,
		Mode:     jobs.Leader,
		Timeout:  snapshotTimeout,
		Run:      s.snapshotOnce,
	}
}

// snapshotRow is one hot_state_snapshots row to upsert.
type snapshotRow struct {
	subject, subjectID, groupID                 string
	score                                       float64
	samples, failures                           int32
	lastFailure, cooldown, reuse, lastUsed, upd int64
}

// snapshotKey identifies a hot_state_snapshots row to delete.
type snapshotKey struct {
	subject, subjectID, groupID string
}

// snapshotSite identifies the site whose dirty set is drained.
type snapshotSite struct {
	id          string
	key         int64
	namespaceID string
}

// snapshotRun holds the caches mapping hot-state keys of one site to IDs.
// Every lookup is scoped to the site (identities, endpoint groups) or to its
// namespace (proxies), so a run is never shared between sites.
type snapshotRun struct {
	identities map[int64]string // identity hkey → id ("" = unknown)
	proxies    map[int64]string
	groups     map[int64]string
}

func newSnapshotRun() *snapshotRun {
	return &snapshotRun{identities: map[int64]string{}, proxies: map[int64]string{}, groups: map[int64]string{}}
}

// trim bounds the caches.
func (r *snapshotRun) trim() {
	if len(r.identities)+len(r.proxies)+len(r.groups) > snapshotCacheLimit {
		*r = *newSnapshotRun()
	}
}

// snapshotOnce drains the dirty sets of every site into hot_state_snapshots
// until they are empty or the time budget is used.
func (s *Syncer) snapshotOnce(ctx context.Context) error {
	sites, err := s.q.HotstateListSites(ctx)
	if err != nil {
		return fmt.Errorf("list sites: %w", err)
	}
	if len(sites) == 0 {
		return nil
	}
	deadline := time.Now().Add(snapshotBudget)
	var total int
	var errs []error
	// Start where the previous run ran out of budget so a large backlog on
	// one site cannot starve the others.
	offset := int(s.snapshotCursor.Load() % int64(len(sites)))
	for i := range sites {
		idx := (offset + i) % len(sites)
		site := sites[idx]
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		s.snapshotCursor.Store(int64(idx))
		target := snapshotSite{id: site.ID, key: site.Hkey, namespaceID: site.NamespaceID}
		run := newSnapshotRun()
		for time.Now().Before(deadline) {
			popped, err := s.snapshotRound(ctx, target, run)
			if err != nil {
				// Entries were requeued; continue with the other sites.
				errs = append(errs, err)
				break
			}
			total += popped
			if popped < snapshotPopCount {
				break
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
	}
	if total > 0 {
		s.logger.Debug("hot-state snapshot persisted", slog.Int("entries", total))
	}
	return errors.Join(errs...)
}

// snapshotRound pops up to snapshotPopCount dirty entries of a site and
// persists their current values. On failure the entries are put back.
func (s *Syncer) snapshotRound(ctx context.Context, site snapshotSite, run *snapshotRun) (int, error) {
	dirtyKey := s.keys.Dirty(site.key)
	entries, err := s.rdb.Do(ctx, s.rdb.B().Spop().Key(dirtyKey).Count(snapshotPopCount).Build()).AsStrSlice()
	if err != nil && !rueidis.IsRedisNil(err) {
		return 0, fmt.Errorf("pop dirty entries of site %s: %w", site.id, err)
	}
	if len(entries) == 0 {
		return 0, nil
	}
	if err := s.persistDirty(ctx, site, entries, run); err != nil {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rerr := s.rdb.Do(restoreCtx, s.rdb.B().Sadd().Key(dirtyKey).Member(entries...).Build()).Error(); rerr != nil {
			s.logger.Warn("could not requeue dirty entries", slog.String("site_id", site.id), slog.Any("error", rerr))
		}
		return 0, err
	}
	return len(entries), nil
}

// dirtyEntry is a parsed dirty-set member.
type dirtyEntry struct {
	kind  byte // 'e', 'g' or 'p'
	group int64
	key   int64
}

// parseDirty parses "e<eg>:<i>", "g<i>" and "p<p>".
func parseDirty(v string) (dirtyEntry, bool) {
	if len(v) < 2 {
		return dirtyEntry{}, false
	}
	switch v[0] {
	case 'e':
		eg, i, ok := strings.Cut(v[1:], ":")
		if !ok {
			return dirtyEntry{}, false
		}
		g, err1 := strconv.ParseInt(eg, 10, 64)
		k, err2 := strconv.ParseInt(i, 10, 64)
		if err1 != nil || err2 != nil {
			return dirtyEntry{}, false
		}
		return dirtyEntry{kind: 'e', group: g, key: k}, true
	case 'g', 'p':
		k, err := strconv.ParseInt(v[1:], 10, 64)
		if err != nil {
			return dirtyEntry{}, false
		}
		return dirtyEntry{kind: v[0], key: k}, true
	}
	return dirtyEntry{}, false
}

// persistDirty reads the current values of dirty entries and upserts (or
// deletes, when the value is gone) their snapshot rows. Entries that decode to
// the same subject (for example "e5:7" and "e5:07") are persisted once: a
// duplicate row in one upsert would fail the whole batch on every retry.
func (s *Syncer) persistDirty(ctx context.Context, site snapshotSite, raw []string, run *snapshotRun) error {
	siteID, siteKey := site.id, site.key
	entries := make([]dirtyEntry, 0, len(raw))
	cmds := make(rueidis.Commands, 0, len(raw))
	seen := make(map[dirtyEntry]struct{}, len(raw))
	for _, v := range raw {
		e, ok := parseDirty(v)
		if !ok {
			continue
		}
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		switch e.kind {
		case 'e':
			cmds = append(cmds, s.rdb.B().Hget().Key(s.keys.Health(siteKey, e.group)).Field(itoa(e.key)).Build())
		case 'g':
			cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.Identity(siteKey, e.key)).
				Field("gs", "gts", "gn", "scd", "sru", "lu").Build())
		case 'p':
			cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.ProxySite(siteKey, e.key)).
				Field("sc", "sts", "sn", "nf", "lf", "cd").Build())
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := s.resolveSnapshotIDs(ctx, site, entries, run); err != nil {
		return err
	}
	results := s.doMulti(ctx, cmds)
	nowMs := s.now().UnixMilli()
	var upserts []snapshotRow
	var deletes []snapshotKey
	for i, e := range entries {
		row, present, known, err := s.snapshotValue(e, results[i], run, nowMs)
		if err != nil {
			return fmt.Errorf("read dirty entry of site %s: %w", siteID, err)
		}
		if !known {
			continue
		}
		if present {
			upserts = append(upserts, row)
		} else {
			deletes = append(deletes, snapshotKey{subject: row.subject, subjectID: row.subjectID, groupID: row.groupID})
		}
	}
	if err := s.writeSnapshots(ctx, siteID, upserts, deletes); err != nil {
		return err
	}
	run.trim()
	return nil
}

// snapshotValue converts the Redis reply of a dirty entry into a snapshot row.
// present is false when the value no longer exists; known is false when the
// entry refers to an entity unknown to PostgreSQL.
func (s *Syncer) snapshotValue(e dirtyEntry, res rueidis.RedisResult, run *snapshotRun, nowMs int64) (snapshotRow, bool, bool, error) {
	var row snapshotRow
	switch e.kind {
	case 'e':
		row.subject, row.subjectID, row.groupID = subjectIdentityEG, run.identities[e.key], run.groups[e.group]
		if row.subjectID == "" || row.groupID == "" {
			return row, false, false, nil
		}
		packed, err := res.ToString()
		if rueidis.IsRedisNil(err) {
			return row, false, true, nil
		}
		if err != nil {
			return row, false, false, err
		}
		h := parseHealth(packed, 0, nowMs)
		row.score, row.samples, row.failures = h.Score, clampInt32(h.Samples), clampInt32(h.NFail)
		row.lastFailure, row.cooldown, row.reuse, row.lastUsed, row.upd = h.LastFail, h.Cooldown, h.Reuse, h.LastUsed, h.ScoreTS
		return row, true, true, nil
	case 'g':
		row.subject, row.subjectID = subjectIdentityGlob, run.identities[e.key]
		if row.subjectID == "" {
			return row, false, false, nil
		}
		vals, err := hmgetStrings(res, 6)
		if err != nil {
			return row, false, false, err
		}
		row.cooldown, row.reuse, row.lastUsed = parseOptInt(vals[3], 0), parseOptInt(vals[4], 0), parseOptInt(vals[5], 0)
		// The row also carries the site cooldown/reuse and last use, which
		// exist without a global score (for example a manual identity x site
		// cooldown of an identity that was never observed).
		if vals[0] == nil && row.cooldown <= 0 && row.reuse <= 0 && row.lastUsed <= 0 {
			return row, false, true, nil
		}
		if vals[0] != nil {
			// gs and gn are written together (observe.lua), so samples > 0
			// marks a persisted score on restore.
			row.score = parseFloat(*vals[0], 0)
			row.upd = parseOptInt(vals[1], nowMs)
			row.samples = clampInt32(parseOptInt(vals[2], 0))
		}
		return row, true, true, nil
	default:
		row.subject, row.subjectID = subjectProxySite, run.proxies[e.key]
		if row.subjectID == "" {
			return row, false, false, nil
		}
		vals, err := hmgetStrings(res, 6)
		if err != nil {
			return row, false, false, err
		}
		row.failures = clampInt32(parseOptInt(vals[3], 0))
		row.lastFailure, row.cooldown = parseOptInt(vals[4], 0), parseOptInt(vals[5], 0)
		// Failure streaks and site cooldowns exist without a score (for
		// example a manual proxy x site cooldown before the first check).
		if vals[0] == nil && row.failures <= 0 && row.lastFailure <= 0 && row.cooldown <= 0 {
			return row, false, true, nil
		}
		if vals[0] != nil {
			// sc and sn are written together, so samples > 0 marks a
			// persisted score on restore.
			row.score = parseFloat(*vals[0], 0)
			row.upd = parseOptInt(vals[1], nowMs)
			row.samples = clampInt32(parseOptInt(vals[2], 0))
		}
		return row, true, true, nil
	}
}

// resolveSnapshotIDs fills the run caches with the IDs of the hot-state keys
// referenced by entries.
func (s *Syncer) resolveSnapshotIDs(ctx context.Context, site snapshotSite, entries []dirtyEntry, run *snapshotRun) error {
	siteID := site.id
	var idKeys, pxKeys, egKeys []int64
	seen := map[[2]int64]struct{}{}
	want := func(kind int64, k int64, cache map[int64]string, list *[]int64) {
		if _, ok := cache[k]; ok {
			return
		}
		if _, ok := seen[[2]int64{kind, k}]; ok {
			return
		}
		seen[[2]int64{kind, k}] = struct{}{}
		*list = append(*list, k)
	}
	snap, _, siteKnown := s.cat.Site(siteID)
	for _, e := range entries {
		switch e.kind {
		case 'e':
			want(0, e.key, run.identities, &idKeys)
			if _, ok := run.groups[e.group]; !ok && siteKnown {
				if g, ok := snap.GroupsByKey[e.group]; ok {
					run.groups[e.group] = g.ID
				}
			}
			want(1, e.group, run.groups, &egKeys)
		case 'g':
			want(0, e.key, run.identities, &idKeys)
		case 'p':
			want(2, e.key, run.proxies, &pxKeys)
		}
	}
	for _, chunk := range chunkInt64(idKeys, idLookupChunk) {
		rows, err := s.q.HotstateIdentityIDsByHkeys(ctx, hotstatedb.HotstateIdentityIDsByHkeysParams{SiteID: siteID, Hkeys: chunk})
		if err != nil {
			return fmt.Errorf("resolve identity keys: %w", err)
		}
		fill(run.identities, chunk)
		for _, r := range rows {
			run.identities[r.Hkey] = r.ID
		}
	}
	for _, chunk := range chunkInt64(egKeys, idLookupChunk) {
		rows, err := s.q.HotstateGroupIDsByHkeys(ctx, hotstatedb.HotstateGroupIDsByHkeysParams{SiteID: siteID, Hkeys: chunk})
		if err != nil {
			return fmt.Errorf("resolve endpoint group keys: %w", err)
		}
		fill(run.groups, chunk)
		for _, r := range rows {
			run.groups[r.Hkey] = r.ID
		}
	}
	for _, chunk := range chunkInt64(pxKeys, idLookupChunk) {
		rows, err := s.q.HotstateProxyIDsByHkeys(ctx, hotstatedb.HotstateProxyIDsByHkeysParams{
			NamespaceID: site.namespaceID, Hkeys: chunk,
		})
		if err != nil {
			return fmt.Errorf("resolve proxy keys: %w", err)
		}
		fill(run.proxies, chunk)
		for _, r := range rows {
			run.proxies[r.Hkey] = r.ID
		}
	}
	return nil
}

// fill marks keys as looked up (unknown until a row sets the ID).
func fill(cache map[int64]string, keys []int64) {
	for _, k := range keys {
		cache[k] = ""
	}
}

const upsertSnapshotsSQL = `
INSERT INTO hot_state_snapshots (site_id, subject, subject_id, endpoint_group_id, score, samples,
                                 consecutive_failures, last_failure_at, cooldown_until, reuse_until,
                                 last_used_at, updated_at)
SELECT $1, u.subject, u.subject_id, u.endpoint_group_id, u.score, u.samples, u.failures,
       u.last_failure_at, u.cooldown_until, u.reuse_until, u.last_used_at, u.updated_at
FROM unnest($2::text[], $3::text[], $4::text[], $5::float8[], $6::int[], $7::int[],
            $8::timestamptz[], $9::timestamptz[], $10::timestamptz[], $11::timestamptz[], $12::timestamptz[])
         AS u(subject, subject_id, endpoint_group_id, score, samples, failures, last_failure_at,
              cooldown_until, reuse_until, last_used_at, updated_at)
ON CONFLICT (site_id, subject, subject_id, endpoint_group_id) DO UPDATE
SET score                = EXCLUDED.score,
    samples              = EXCLUDED.samples,
    consecutive_failures = EXCLUDED.consecutive_failures,
    last_failure_at      = EXCLUDED.last_failure_at,
    cooldown_until       = EXCLUDED.cooldown_until,
    reuse_until          = EXCLUDED.reuse_until,
    last_used_at         = EXCLUDED.last_used_at,
    updated_at           = EXCLUDED.updated_at`

const deleteSnapshotsSQL = `
DELETE FROM hot_state_snapshots s
USING unnest($2::text[], $3::text[], $4::text[]) AS d(subject, subject_id, endpoint_group_id)
WHERE s.site_id = $1
  AND s.subject = d.subject
  AND s.subject_id = d.subject_id
  AND s.endpoint_group_id = d.endpoint_group_id`

// writeSnapshots upserts and deletes snapshot rows of a site in one transaction.
func (s *Syncer) writeSnapshots(ctx context.Context, siteID string, upserts []snapshotRow, deletes []snapshotKey) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if len(upserts) > 0 {
			n := len(upserts)
			subjects, ids, groups := make([]string, n), make([]string, n), make([]string, n)
			scores := make([]float64, n)
			samples, failures := make([]int32, n), make([]int32, n)
			lastFail, cooldown, reuse, lastUsed := make([]*time.Time, n), make([]*time.Time, n), make([]*time.Time, n), make([]*time.Time, n)
			updated := make([]time.Time, n)
			now := s.now().UTC()
			for i, r := range upserts {
				subjects[i], ids[i], groups[i] = r.subject, r.subjectID, r.groupID
				scores[i], samples[i], failures[i] = r.score, r.samples, r.failures
				lastFail[i], cooldown[i], reuse[i], lastUsed[i] = msToTimePtr(r.lastFailure), msToTimePtr(r.cooldown), msToTimePtr(r.reuse), msToTimePtr(r.lastUsed)
				updated[i] = now
				if t := msToTimePtr(r.upd); t != nil {
					updated[i] = *t
				}
			}
			if _, err := tx.Exec(ctx, upsertSnapshotsSQL, siteID, subjects, ids, groups, scores, samples, failures,
				lastFail, cooldown, reuse, lastUsed, updated); err != nil {
				return fmt.Errorf("upsert hot-state snapshots: %w", err)
			}
		}
		if len(deletes) > 0 {
			n := len(deletes)
			subjects, ids, groups := make([]string, n), make([]string, n), make([]string, n)
			for i, d := range deletes {
				subjects[i], ids[i], groups[i] = d.subject, d.subjectID, d.groupID
			}
			if _, err := tx.Exec(ctx, deleteSnapshotsSQL, siteID, subjects, ids, groups); err != nil {
				return fmt.Errorf("delete hot-state snapshots: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write hot-state snapshots of site %s: %w", siteID, err)
	}
	return nil
}

// hmgetStrings decodes an HMGET reply into n optional strings.
func hmgetStrings(res rueidis.RedisResult, n int) ([]*string, error) {
	arr, err := res.ToArray()
	if err != nil {
		return nil, err
	}
	out := make([]*string, n)
	for i := 0; i < n && i < len(arr); i++ {
		if v, err := arr[i].ToString(); err == nil {
			out[i] = &v
		}
	}
	return out, nil
}

// parseOptInt parses an optional integer field.
func parseOptInt(v *string, def int64) int64 {
	if v == nil {
		return def
	}
	return parseInt(*v, def)
}
