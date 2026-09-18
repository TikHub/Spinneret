package hotstate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// identityRow is the PostgreSQL view of an identity used for materialization.
type identityRow = hotstatedb.HotstateIdentitiesByIDsRow

// identityRecord is one entry of a sync_identities call.
type identityRecord struct {
	remove  bool
	row     identityRow // zero for removals of identities unknown to PostgreSQL
	hkey    int64
	profile int
	jitter  int64
}

// identityBatch controls how a set of identity records is applied.
type identityBatch struct {
	// mode applies to identity lifecycle fields, accountMode to account
	// st/bu (modeAuthoritative or modeMerge).
	mode          string
	accountMode   string
	resetHealth   bool
	resetFailures bool
	coldStart     bool
	// accountSct holds the PostgreSQL lifecycle change time (ms) of accounts
	// by hot-state key; only loaded for authoritative account materialization
	// (0 otherwise).
	accountSct map[int64]int64
}

// identityStats accumulates the replies of sync_identities calls.
type identityStats struct {
	synced  int64
	removed int64
	written int64
}

// SyncIdentities materializes identities of a site from PostgreSQL
// (PostgreSQL-first admin operations): identity hash fields, account hashes
// and membership, ready-queue membership and scores (score = availability,
// spec §5.6), optionally resetting health or failure streaks. Retired
// identities and identities that no longer exist are removed from the hot state.
// The bound proxy ("px") stored in Redis wins over proxy_bindings while that
// proxy is materialized on the site, because bindings are created Redis-first
// by acquire.lua.
func (s *Syncer) SyncIdentities(ctx context.Context, siteID string, identityIDs []string, opts SyncOptions) error {
	ids := dedupeStrings(identityIDs)
	if len(ids) == 0 {
		return nil
	}
	site, _, err := s.siteSnapshot(ctx, siteID)
	if err != nil {
		return err
	}
	plan := newGroupPlan(site)
	// Account lifecycle fields are PostgreSQL-first only through SyncAccounts;
	// an identity synchronization never reverts a Redis-first account ban.
	batch := identityBatch{
		mode: modeAuthoritative, accountMode: modeMerge,
		resetHealth: opts.ResetHealth, resetFailures: opts.ResetFailures,
	}
	var missing []string
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		rows, err := s.q.HotstateIdentitiesByIDs(ctx, hotstatedb.HotstateIdentitiesByIDsParams{SiteID: siteID, Ids: chunk})
		if err != nil {
			return fmt.Errorf("load identities of site %s: %w", siteID, err)
		}
		found := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			found[r.ID] = struct{}{}
		}
		for _, id := range chunk {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		if _, err := s.applyIdentityRows(ctx, plan, rows, batch); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		return s.RemoveIdentities(ctx, siteID, missing)
	}
	return nil
}

// RemoveIdentities removes identities from the hot state of a site: identity
// hash, ready-queue membership and health entries of every endpoint group,
// quota counters, account membership, ban history and cross-attribution
// state, plus their hot_state_snapshots rows. Identities still present in
// PostgreSQL are resolved by ID; for identities already deleted the site's
// identity hashes are scanned for a matching "iid", which costs a keyspace
// scan, so callers should remove identities before deleting their rows.
func (s *Syncer) RemoveIdentities(ctx context.Context, siteID string, identityIDs []string) error {
	ids := dedupeStrings(identityIDs)
	if len(ids) == 0 {
		return nil
	}
	site, _, err := s.siteSnapshot(ctx, siteID)
	if err != nil {
		return err
	}
	plan := newGroupPlan(site)
	removal := identityBatch{mode: modeAuthoritative, accountMode: modeMerge}
	unknown := make(map[string]struct{})
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		rows, err := s.q.HotstateIdentitiesByIDs(ctx, hotstatedb.HotstateIdentitiesByIDsParams{SiteID: siteID, Ids: chunk})
		if err != nil {
			return fmt.Errorf("load identities of site %s: %w", siteID, err)
		}
		found := make(map[string]struct{}, len(rows))
		recs := make([]identityRecord, 0, len(rows))
		for _, r := range rows {
			found[r.ID] = struct{}{}
			recs = append(recs, identityRecord{remove: true, row: r, hkey: r.Hkey, profile: plan.removal})
		}
		for _, id := range chunk {
			if _, ok := found[id]; !ok && idgen.Valid(id, idgen.Identity) {
				unknown[id] = struct{}{}
			}
		}
		if _, err := s.applyIdentities(ctx, plan, recs, removal); err != nil {
			return err
		}
	}
	if len(unknown) > 0 {
		keys, err := s.findIdentityKeys(ctx, site.Key, unknown)
		if err != nil {
			return err
		}
		recs := make([]identityRecord, 0, len(keys))
		for _, k := range keys {
			recs = append(recs, identityRecord{remove: true, hkey: k, profile: plan.removal})
		}
		if _, err := s.applyIdentities(ctx, plan, recs, removal); err != nil {
			return err
		}
	}
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		if err := s.q.HotstateDeleteIdentitySnapshots(ctx, hotstatedb.HotstateDeleteIdentitySnapshotsParams{
			SiteID: siteID, IdentityIds: chunk,
		}); err != nil {
			return fmt.Errorf("delete hot-state snapshots of site %s: %w", siteID, err)
		}
	}
	return nil
}

// applyIdentityRows turns PostgreSQL rows into sync (or, for retired
// identities, removal) records and applies them. The snapshot rows of retired
// identities are deleted as well.
func (s *Syncer) applyIdentityRows(ctx context.Context, plan *groupPlan, rows []identityRow, batch identityBatch) (identityStats, error) {
	recs := make([]identityRecord, 0, len(rows))
	var retired []string
	for _, r := range rows {
		if r.State == stateRetired {
			retired = append(retired, r.ID)
			recs = append(recs, identityRecord{remove: true, row: r, hkey: r.Hkey, profile: plan.removal})
			continue
		}
		rec := identityRecord{row: r, hkey: r.Hkey, profile: plan.profileIndex(r.Client, r.TypeID)}
		if batch.coldStart {
			rec.jitter = s.jitter()
		}
		recs = append(recs, rec)
	}
	if batch.accountMode == modeAuthoritative {
		times, err := s.accountStateTimes(ctx, rows)
		if err != nil {
			return identityStats{}, err
		}
		batch.accountSct = times
	}
	stats, err := s.applyIdentities(ctx, plan, recs, batch)
	if err != nil {
		return stats, err
	}
	if len(retired) > 0 {
		if err := s.q.HotstateDeleteIdentitySnapshots(ctx, hotstatedb.HotstateDeleteIdentitySnapshotsParams{
			SiteID: plan.site.ID, IdentityIds: retired,
		}); err != nil {
			return stats, fmt.Errorf("delete hot-state snapshots of retired identities: %w", err)
		}
	}
	return stats, nil
}

// accountStateTimes loads the lifecycle change times (ms) of the accounts of
// rows, keyed by account hot-state key.
func (s *Syncer) accountStateTimes(ctx context.Context, rows []identityRow) (map[int64]int64, error) {
	seen := make(map[int64]struct{})
	var keys []int64
	for _, r := range rows {
		if r.AccountHkey == nil || r.State == stateRetired {
			continue
		}
		if _, ok := seen[*r.AccountHkey]; !ok {
			seen[*r.AccountHkey] = struct{}{}
			keys = append(keys, *r.AccountHkey)
		}
	}
	out := make(map[int64]int64, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	times, err := s.q.HotstateAccountStateTimes(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("load account state change times: %w", err)
	}
	for _, t := range times {
		out[t.Hkey] = t.StateChangedAt.UnixMilli()
	}
	return out, nil
}

// applyIdentities splits records into bounded script calls and runs them one
// by one, so neither the encoded arguments nor Redis blocking time grow with
// the number of records.
func (s *Syncer) applyIdentities(ctx context.Context, plan *groupPlan, recs []identityRecord, batch identityBatch) (identityStats, error) {
	var stats identityStats
	if len(recs) == 0 {
		return stats, nil
	}
	meta := []string{s.keys.SiteMeta(plan.site.Key)}
	nowMs := s.now().UnixMilli()
	handle := func(res rueidis.RedisResult) error {
		vals, err := res.AsIntSlice()
		if err != nil {
			return err
		}
		if len(vals) >= 3 {
			stats.synced += vals[0]
			stats.removed += vals[1]
			stats.written += vals[2]
		}
		return nil
	}
	start, ops := 0, 0
	for i, rec := range recs {
		ops += max(plan.list[rec.profile].size(), 1)
		if i < len(recs)-1 && i-start+1 < identitiesPerCall && ops < groupOpsPerCall {
			continue
		}
		call := rueidis.LuaExec{Keys: meta, Args: identityCallArgs(plan, recs[start:i+1], batch, nowMs)}
		if err := s.runScripts(ctx, s.syncIdentities, []rueidis.LuaExec{call}, handle); err != nil {
			return stats, fmt.Errorf("sync identities of site %s: %w", plan.site.ID, err)
		}
		start, ops = i+1, 0
	}
	s.logger.Debug("identities synchronized",
		slog.String("site_id", plan.site.ID), slog.Int64("synced", stats.synced),
		slog.Int64("removed", stats.removed), slog.Int64("ready_written", stats.written))
	return stats, nil
}

// identityRecordValues is the number of ARGV values of one identity record of
// sync_identities.lua.
const identityRecordValues = 16

// identityCallArgs encodes the ARGV of one sync_identities call.
func identityCallArgs(plan *groupPlan, recs []identityRecord, batch identityBatch, nowMs int64) []string {
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	// Local profile numbering (1-based in Lua) and account de-duplication.
	localProfiles := make(map[int]int)
	var profileOrder []int
	type account struct{ st, bu, cd, sct string }
	accounts := make(map[int64]account)
	var accountOrder []int64
	for _, rec := range recs {
		if _, ok := localProfiles[rec.profile]; !ok {
			localProfiles[rec.profile] = len(profileOrder) + 1
			profileOrder = append(profileOrder, rec.profile)
		}
		if rec.remove || rec.row.AccountHkey == nil {
			continue
		}
		key := *rec.row.AccountHkey
		if _, ok := accounts[key]; ok {
			continue
		}
		state := stateActive
		if rec.row.AccountState != nil {
			state = *rec.row.AccountState
		}
		accounts[key] = account{
			st:  state,
			bu:  itoa(banUntilMs(state, rec.row.AccountBanUntil)),
			cd:  itoa(timeMs(rec.row.AccountCooldownUntil)),
			sct: itoa(batch.accountSct[key]),
		}
		accountOrder = append(accountOrder, key)
	}

	args := make([]string, 0, 9+2*len(profileOrder)+5*len(accountOrder)+identityRecordValues*len(recs))
	accountMode := batch.accountMode
	if accountMode == "" {
		accountMode = modeMerge
	}
	healthBaselines := ""
	if batch.resetHealth {
		healthBaselines = plan.baselineCSV()
	}
	args = append(args, itoa(nowMs), batch.mode, accountMode, flag(batch.resetHealth), flag(batch.resetFailures),
		flag(batch.coldStart), healthBaselines)
	args = append(args, itoa(int64(len(profileOrder))))
	for _, idx := range profileOrder {
		pr := plan.list[idx]
		args = append(args, csvInt64(pr.eligible), csvInt64(pr.other))
	}
	args = append(args, itoa(int64(len(accountOrder))))
	for _, key := range accountOrder {
		a := accounts[key]
		args = append(args, itoa(key), a.st, a.bu, a.cd, a.sct)
	}
	for _, rec := range recs {
		prof := itoa(int64(localProfiles[rec.profile]))
		if rec.remove {
			args = append(args, "R", itoa(rec.hkey), "", "", "", "", "", "", "", "", "", "", "", prof, "0", "0")
			continue
		}
		r := rec.row
		acc, px := "", ""
		if r.AccountHkey != nil {
			acc = itoa(*r.AccountHkey)
		}
		if r.ProxyHkey != nil {
			px = itoa(*r.ProxyHkey)
		}
		args = append(args, "S", itoa(rec.hkey), r.ID, r.State, r.TypeName,
			itoa(int64(r.TypeVersion)), itoa(int64(r.PayloadVersion)), acc, r.Region,
			itoa(banUntilMs(r.State, r.BanUntil)), itoa(quarantineUntilMs(r.State, r.QuarantineUntil)),
			itoa(timeMs(r.ActivatedAt)), px, prof, itoa(rec.jitter), itoa(r.StateChangedAt.UnixMilli()))
	}
	return args
}

// banUntilMs encodes the "bu" field: -1 for a permanent ban (banned without an
// end), the end in ms for a temporary ban, 0 otherwise.
func banUntilMs(state string, until *time.Time) int64 {
	if state != stateBanned {
		return 0
	}
	if until == nil {
		return -1
	}
	return until.UnixMilli()
}

// quarantineUntilMs encodes the "qu" field: the quarantine end while quarantined, 0 otherwise.
func quarantineUntilMs(state string, until *time.Time) int64 {
	if state != stateQuar || until == nil {
		return 0
	}
	return until.UnixMilli()
}
