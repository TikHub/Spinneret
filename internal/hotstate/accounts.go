package hotstate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/hotstate/hotstatedb"
)

// Operations of sync_accounts.lua.
const (
	accountOpReplace = "r"
	accountOpAdd     = "a"
	accountOpPush    = "p"
)

// accountCall holds the arguments of the sync_accounts calls of one account.
type accountCall struct {
	members []int64
	baseArg []string
}

// SyncAccounts materializes accounts of a site from PostgreSQL: the account
// hash ("st", "bu" with -1 for permanent bans, "cd" = max(Redis, PostgreSQL)
// so automatic cooldowns written by apply.lua are never shortened) and the
// member set rebuilt from identities.account_id. "st"/"bu" are applied unless
// Redis holds a newer Redis-first change that is still in force: the change
// time of the account in PostgreSQL is its latest lifecycle state event (see
// sync_accounts.lua), so an automatic ban not yet persisted by the StateWriter
// is not reverted by membership or attribute synchronizations. When the cooldown moves
// forward the members' ready-queue scores are pushed (ZADD XX GT). Accounts
// that no longer exist are skipped: their hot-state key is unknown once the
// row is gone and their members are re-pointed by SyncIdentities.
func (s *Syncer) SyncAccounts(ctx context.Context, siteID string, accountIDs []string) error {
	ids := dedupeStrings(accountIDs)
	if len(ids) == 0 {
		return nil
	}
	site, _, err := s.siteSnapshot(ctx, siteID)
	if err != nil {
		return err
	}
	plan := newGroupPlan(site)
	egCSV := csvInt64(plan.all)
	meta := s.keys.SiteMeta(site.Key)
	nowMs := itoa(s.now().UnixMilli())
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		rows, err := s.q.HotstateAccountsByIDs(ctx, hotstatedb.HotstateAccountsByIDsParams{SiteID: siteID, Ids: chunk})
		if err != nil {
			return fmt.Errorf("load accounts of site %s: %w", siteID, err)
		}
		if len(rows) < len(chunk) {
			s.logger.Debug("accounts not found while synchronizing",
				slog.String("site_id", siteID), slog.Int("missing", len(chunk)-len(rows)))
		}
		if len(rows) == 0 {
			continue
		}
		members, err := s.q.HotstateAccountMembers(ctx, hotstatedb.HotstateAccountMembersParams{SiteID: siteID, AccountIds: chunk})
		if err != nil {
			return fmt.Errorf("load account members of site %s: %w", siteID, err)
		}
		byAccount := make(map[string][]int64, len(rows))
		for _, m := range members {
			byAccount[m.AccountID] = append(byAccount[m.AccountID], m.Hkey)
		}
		calls := make([]accountCall, 0, len(rows))
		for _, r := range rows {
			calls = append(calls, accountCall{
				members: byAccount[r.ID],
				baseArg: []string{
					egCSV, itoa(r.Hkey), r.State, itoa(banUntilMs(r.State, r.BanUntil)), itoa(timeMs(r.CooldownUntil)),
					itoa(r.StateChangedAt.UnixMilli()), nowMs,
				},
			})
		}
		if err := s.runAccountCalls(ctx, meta, len(plan.all), calls); err != nil {
			return fmt.Errorf("sync accounts of site %s: %w", siteID, err)
		}
	}
	return nil
}

// runAccountCalls writes every account in two phases. The first call of an
// account writes its hash and replaces the member set (up to
// accountMembersPerCall members, atomically); further member chunks only add
// members. Then, for accounts whose cooldown moved forward, the members'
// ready-queue scores are pushed in push-only calls bounded to groupOpsPerCall
// member × endpoint group evaluations, so no call blocks Redis for long.
// Pushes use ZADD XX GT and are therefore order independent.
func (s *Syncer) runAccountCalls(ctx context.Context, meta string, groups int, calls []accountCall) error {
	build := func(c accountCall, op string, push bool, members []int64) rueidis.LuaExec {
		args := make([]string, 0, 3+len(c.baseArg)+len(members))
		args = append(args, modeAuthoritative, op, boolArg(push))
		args = append(args, c.baseArg...)
		for _, m := range members {
			args = append(args, itoa(m))
		}
		return rueidis.LuaExec{Keys: []string{meta}, Args: args}
	}
	first := make([]rueidis.LuaExec, len(calls))
	var rest []rueidis.LuaExec
	for i, c := range calls {
		chunks := chunkInt64(c.members, accountMembersPerCall)
		var head []int64
		if len(chunks) > 0 {
			head = chunks[0]
			for _, chunk := range chunks[1:] {
				rest = append(rest, build(c, accountOpAdd, false, chunk))
			}
		}
		first[i] = build(c, accountOpReplace, false, head)
	}
	moved := make([]bool, 0, len(calls))
	if err := s.runScripts(ctx, s.syncAccounts, first, func(res rueidis.RedisResult) error {
		vals, err := res.AsIntSlice()
		if err != nil {
			return err
		}
		moved = append(moved, len(vals) > 0 && vals[0] == 1)
		return nil
	}); err != nil {
		return err
	}
	if err := s.runScripts(ctx, s.syncAccounts, rest, nil); err != nil {
		return err
	}
	if groups == 0 {
		return nil
	}
	pushChunk := max(1, groupOpsPerCall/groups)
	var pushes []rueidis.LuaExec
	for i, c := range calls {
		if !moved[i] {
			continue
		}
		for _, chunk := range chunkInt64(c.members, pushChunk) {
			pushes = append(pushes, build(c, accountOpPush, true, chunk))
		}
	}
	return s.runScripts(ctx, s.syncAccounts, pushes, nil)
}

func boolArg(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
