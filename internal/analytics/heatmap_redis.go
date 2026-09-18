package analytics

import (
	"context"
	"fmt"

	"github.com/redis/rueidis"

	"github.com/TikHub/Spinneret/internal/analytics/analyticsdb"
	"github.com/TikHub/Spinneret/internal/catalog"
)

// heatmapHot is the Redis state behind a heatmap page. The group-indexed
// slices are indexed [column][row].
type heatmapHot struct {
	health    [][]string
	hasHealth [][]bool
	// readyAt holds the ready queue scores (available-at ms) as returned by
	// Redis; they stay floats so that out-of-range scores (e.g. +inf) compare
	// correctly instead of overflowing an integer conversion.
	readyAt    [][]float64
	inReady    [][]bool
	identities []identityHot
}

// identityFields are the identity hash fields read for heatmap cells.
var identityFields = []string{fieldState, fieldBanUntil, fieldSiteCooldown}

// accountFields are the account hash fields read for heatmap cells.
var accountFields = []string{fieldState, fieldBanUntil, fieldCooldown}

// readHeatmapHot reads, in one pipelined round trip, HMGET hs:<eg> and
// ZMSCORE rdy:<eg> for every group (all identities of the page at once),
// HMGET id:<i> for every identity and HMGET acc:<a> for every distinct account.
func (s *Service) readHeatmapHot(ctx context.Context, site *catalog.Site, groups []*catalog.EndpointGroup,
	identities []analyticsdb.AnalyticsHeatmapIdentitiesRow) (heatmapHot, error) {
	members := make([]string, len(identities))
	for i, id := range identities {
		members[i] = itoa(id.Hkey)
	}
	accountIndex := map[int64]int{}
	var accounts []int64
	for _, id := range identities {
		if id.AccountHkey > 0 {
			if _, seen := accountIndex[id.AccountHkey]; !seen {
				accountIndex[id.AccountHkey] = len(accounts)
				accounts = append(accounts, id.AccountHkey)
			}
		}
	}

	cmds := make(rueidis.Commands, 0, 2*len(groups)+len(identities)+len(accounts))
	for _, g := range groups {
		cmds = append(cmds,
			s.rdb.B().Hmget().Key(s.keys.Health(site.Key, g.Key)).Field(members...).Build(),
			s.rdb.B().Zmscore().Key(s.keys.Ready(site.Key, g.Key)).Member(members...).Build())
	}
	for _, id := range identities {
		cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.Identity(site.Key, id.Hkey)).Field(identityFields...).Build())
	}
	for _, a := range accounts {
		cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.Account(site.Key, a)).Field(accountFields...).Build())
	}
	results := s.rdb.DoMulti(ctx, cmds...)

	out := heatmapHot{
		health:     make([][]string, len(groups)),
		hasHealth:  make([][]bool, len(groups)),
		readyAt:    make([][]float64, len(groups)),
		inReady:    make([][]bool, len(groups)),
		identities: make([]identityHot, len(identities)),
	}
	for col, g := range groups {
		if err := out.parseGroup(col, results[2*col], results[2*col+1], len(identities)); err != nil {
			return heatmapHot{}, fmt.Errorf("read hot state of endpoint group %s: %w", g.ID, err)
		}
	}
	base := 2 * len(groups)
	accountState := make([][]string, len(accounts))
	for i, a := range accounts {
		vals, err := stringsOf(results[base+len(identities)+i])
		if err != nil {
			return heatmapHot{}, fmt.Errorf("read account %d hot state: %w", a, err)
		}
		accountState[i] = vals
	}
	for row, id := range identities {
		vals, err := stringsOf(results[base+row])
		if err != nil {
			return heatmapHot{}, fmt.Errorf("read identity %s hot state: %w", id.ID, err)
		}
		var acc []string
		if idx, ok := accountIndex[id.AccountHkey]; ok {
			acc = accountState[idx]
		}
		out.identities[row] = buildIdentityHot(id, vals, acc)
	}
	return out, nil
}

// parseGroup decodes the HMGET hs and ZMSCORE rdy replies of one group.
func (h *heatmapHot) parseGroup(col int, healthRes, readyRes rueidis.RedisResult, rows int) error {
	h.health[col] = make([]string, rows)
	h.hasHealth[col] = make([]bool, rows)
	h.readyAt[col] = make([]float64, rows)
	h.inReady[col] = make([]bool, rows)
	packed, err := healthRes.ToArray()
	if err != nil {
		return fmt.Errorf("health hash: %w", err)
	}
	scores, err := readyRes.ToArray()
	if err != nil {
		return fmt.Errorf("ready queue: %w", err)
	}
	for row := 0; row < rows && row < len(packed); row++ {
		v, ok, err := optionalString(packed[row])
		if err != nil {
			return fmt.Errorf("health entry: %w", err)
		}
		h.health[col][row], h.hasHealth[col][row] = v, ok
	}
	for row := 0; row < rows && row < len(scores); row++ {
		if scores[row].IsNil() {
			continue
		}
		score, err := scores[row].AsFloat64()
		if err != nil {
			return fmt.Errorf("ready score: %w", err)
		}
		h.readyAt[col][row], h.inReady[col][row] = score, true
	}
	return nil
}

// buildIdentityHot combines the identity hash (st, bu, scd), the account hash
// (st, bu, cd) and the PostgreSQL row. The Redis state wins when present
// because lifecycle changes are applied there first.
func buildIdentityHot(row analyticsdb.AnalyticsHeatmapIdentitiesRow, id, acc []string) identityHot {
	field := func(vals []string, i int) string {
		if i < len(vals) {
			return vals[i]
		}
		return ""
	}
	var out identityHot
	out.cooldownUntil = parseInt(field(id, 2), 0)

	state := field(id, 0)
	if state == "" {
		state = row.State
	}
	if state == stateBanned {
		until := parseInt(field(id, 1), 0)
		if until == 0 && row.BanUntil != nil {
			until = row.BanUntil.UnixMilli()
		}
		out.setBan(until)
	}
	if len(acc) > 0 {
		out.cooldownUntil = max(out.cooldownUntil, parseInt(field(acc, 2), 0))
		if field(acc, 0) == stateBanned {
			out.setBan(parseInt(field(acc, 1), 0))
		}
	}
	return out
}

// setBan merges a ban ending at until ms (permanentBanMillis or 0 = permanent).
func (h *identityHot) setBan(until int64) {
	h.banned = true
	if until == permanentBanMillis || until == 0 {
		h.banPermanent = true
		return
	}
	h.banUntil = max(h.banUntil, until)
}
