package hotstate

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
)

// pruneReadyQueues removes ready-queue members that are no longer eligible.
// A queue whose cardinality equals the number of eligible identities seen in
// PostgreSQL is skipped: after the materialization every eligible identity is
// a member, so equal cardinality means there is no stale member. Otherwise the
// queue is scanned; members unknown to PostgreSQL are removed from the hot
// state entirely and every other suspicious member is re-verified in Lua
// against its live identity hash, so concurrent synchronizations are never
// undone.
func (s *Syncer) pruneReadyQueues(ctx context.Context, plan *groupPlan, tracker *identityTracker) (int64, error) {
	if len(plan.groups) == 0 {
		return 0, nil
	}
	cmds := make(rueidis.Commands, len(plan.groups))
	for i, g := range plan.groups {
		cmds[i] = s.rdb.B().Zcard().Key(s.keys.Ready(plan.site.Key, g.Key)).Build()
	}
	cards := make([]int64, len(plan.groups))
	for i, res := range s.doMulti(ctx, cmds) {
		n, err := res.AsInt64()
		if err != nil {
			return 0, fmt.Errorf("count ready queue of site %s: %w", plan.site.ID, err)
		}
		cards[i] = n
	}
	var pruned int64
	for i, g := range plan.groups {
		if cards[i] == tracker.expected[g.Key] {
			continue
		}
		n, err := s.pruneReadyQueue(ctx, plan, g, tracker)
		if err != nil {
			return pruned, err
		}
		pruned += n
	}
	return pruned, nil
}

// pruneReadyQueue scans one ready queue and removes stale members.
func (s *Syncer) pruneReadyQueue(ctx context.Context, plan *groupPlan, g *catalog.EndpointGroup, tracker *identityTracker) (int64, error) {
	key := s.keys.Ready(plan.site.Key, g.Key)
	meta := s.keys.SiteMeta(plan.site.Key)
	typeNames := plan.eligibleTypeNames(g)
	var pruned int64
	var verify []string
	var unknown []int64

	flushVerify := func() error {
		if len(verify) == 0 {
			return nil
		}
		args := make([]string, 0, 3+len(verify))
		args = append(args, "prune_ready", itoa(g.Key), typeNames)
		args = append(args, verify...)
		verify = verify[:0]
		res := s.syncMaintenance.Exec(ctx, s.rdb, []string{meta}, args)
		n, err := res.AsInt64()
		if err != nil {
			return fmt.Errorf("prune ready queue %s: %w", key, err)
		}
		pruned += n
		return nil
	}
	flushUnknown := func() error {
		if len(unknown) == 0 {
			return nil
		}
		batch := unknown
		unknown = nil
		live, err := s.q.HotstateLiveIdentityHkeys(ctx, hotstatedb.HotstateLiveIdentityHkeysParams{SiteID: plan.site.ID, Hkeys: batch})
		if err != nil {
			return fmt.Errorf("check identities of site %s: %w", plan.site.ID, err)
		}
		liveSet := make(map[int64]struct{}, len(live))
		for _, k := range live {
			liveSet[k] = struct{}{}
		}
		var recs []identityRecord
		for _, k := range batch {
			if _, ok := liveSet[k]; ok {
				verify = append(verify, itoa(k))
				continue
			}
			recs = append(recs, identityRecord{remove: true, hkey: k, profile: plan.removal})
		}
		res, err := s.applyIdentities(ctx, plan, recs, identityBatch{mode: modeAuthoritative, accountMode: modeMerge})
		if err != nil {
			return err
		}
		pruned += res.removed
		return nil
	}

	var cursor uint64
	for {
		entry, err := s.rdb.Do(ctx, s.rdb.B().Zscan().Key(key).Cursor(cursor).Count(scanCount).Build()).AsScanEntry()
		if err != nil {
			return pruned, fmt.Errorf("scan ready queue %s: %w", key, err)
		}
		// ZSCAN replies alternate member and score.
		for i := 0; i+1 < len(entry.Elements); i += 2 {
			member := entry.Elements[i]
			hkey, err := strconv.ParseInt(member, 10, 64)
			if err != nil {
				verify = append(verify, member)
				continue
			}
			info, known := tracker.find(hkey)
			switch {
			case known && info.ready && plan.eligibleFor(int(info.profile), g.Key):
			case known:
				verify = append(verify, member)
			default:
				unknown = append(unknown, hkey)
			}
		}
		if len(unknown) >= idLookupChunk {
			if err := flushUnknown(); err != nil {
				return pruned, err
			}
		}
		if len(verify) >= pruneCandidatesPerCall {
			if err := flushVerify(); err != nil {
				return pruned, err
			}
		}
		cursor = entry.Cursor
		if cursor == 0 {
			break
		}
	}
	if err := flushUnknown(); err != nil {
		return pruned, err
	}
	if err := flushVerify(); err != nil {
		return pruned, err
	}
	return pruned, nil
}

// proxyInfo is the compact per-proxy record kept while a site is materialized.
type proxyInfo struct {
	hkey   int64
	active bool
}

// proxyTracker records the proxies of a namespace in ascending key order.
type proxyTracker struct {
	items  []proxyInfo
	active int64
}

func (t *proxyTracker) len() int { return len(t.items) }

func (t *proxyTracker) find(hkey int64) (proxyInfo, bool) {
	i := sort.Search(len(t.items), func(i int) bool { return t.items[i].hkey >= hkey })
	if i < len(t.items) && t.items[i].hkey == hkey {
		return t.items[i], true
	}
	return proxyInfo{}, false
}

// syncSiteProxies materializes every proxy of the site's namespace into the site.
func (s *Syncer) syncSiteProxies(ctx context.Context, site *catalog.Site, mode string) (*proxyTracker, error) {
	tracker := &proxyTracker{}
	var after int64
	for {
		page, err := s.q.HotstateProxiesPage(ctx, hotstatedb.HotstateProxiesPageParams{
			NamespaceID: site.NamespaceID, AfterHkey: after, PageSize: proxyPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("load proxies of namespace %s: %w", site.NamespaceID, err)
		}
		if len(page) == 0 {
			return tracker, nil
		}
		rows := make([]proxyRow, len(page))
		for i, r := range page {
			rows[i] = proxyRow(r)
			active := r.State == stateActive
			tracker.items = append(tracker.items, proxyInfo{hkey: r.Hkey, active: active})
			if active {
				tracker.active++
			}
		}
		if err := s.applyProxies(ctx, site.Key, rows, mode); err != nil {
			return nil, err
		}
		after = page[len(page)-1].Hkey
		if len(page) < proxyPageSize {
			return tracker, nil
		}
	}
}

// pruneProxyQueue removes stale members of "pxrdy" (same strategy as
// pruneReadyQueues: cardinality check, scan, PostgreSQL existence check and
// Lua re-verification of the live proxy hash).
func (s *Syncer) pruneProxyQueue(ctx context.Context, site *catalog.Site, tracker *proxyTracker) (int64, error) {
	key := s.keys.ProxyReady(site.Key)
	meta := s.keys.SiteMeta(site.Key)
	card, err := s.rdb.Do(ctx, s.rdb.B().Zcard().Key(key).Build()).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("count proxy queue of site %s: %w", site.ID, err)
	}
	if card == tracker.active {
		return 0, nil
	}
	var verify []string
	var unknown []int64
	var cursor uint64
	for {
		entry, err := s.rdb.Do(ctx, s.rdb.B().Zscan().Key(key).Cursor(cursor).Count(scanCount).Build()).AsScanEntry()
		if err != nil {
			return 0, fmt.Errorf("scan proxy queue of site %s: %w", site.ID, err)
		}
		for i := 0; i+1 < len(entry.Elements); i += 2 {
			member := entry.Elements[i]
			hkey, err := strconv.ParseInt(member, 10, 64)
			if err != nil {
				verify = append(verify, member)
				continue
			}
			info, known := tracker.find(hkey)
			switch {
			case known && info.active:
			case known:
				verify = append(verify, member)
			default:
				unknown = append(unknown, hkey)
			}
		}
		cursor = entry.Cursor
		if cursor == 0 {
			break
		}
	}
	var removeKeys []string
	for _, chunk := range chunkInt64(unknown, idLookupChunk) {
		live, err := s.q.HotstateLiveProxyHkeys(ctx, hotstatedb.HotstateLiveProxyHkeysParams{NamespaceID: site.NamespaceID, Hkeys: chunk})
		if err != nil {
			return 0, fmt.Errorf("check proxies of namespace %s: %w", site.NamespaceID, err)
		}
		liveSet := make(map[int64]struct{}, len(live))
		for _, k := range live {
			liveSet[k] = struct{}{}
		}
		for _, k := range chunk {
			if _, ok := liveSet[k]; ok {
				verify = append(verify, itoa(k))
			} else {
				removeKeys = append(removeKeys, itoa(k))
			}
		}
	}
	var calls []rueidis.LuaExec
	for _, chunk := range chunkStrings(verify, pruneCandidatesPerCall) {
		calls = append(calls, rueidis.LuaExec{Keys: []string{meta}, Args: append([]string{"prune_proxies"}, chunk...)})
	}
	for _, chunk := range chunkStrings(removeKeys, pruneCandidatesPerCall) {
		calls = append(calls, rueidis.LuaExec{Keys: []string{meta}, Args: append([]string{"remove_proxies"}, chunk...)})
	}
	var pruned int64
	if err := s.runScripts(ctx, s.syncMaintenance, calls, func(res rueidis.RedisResult) error {
		n, err := res.AsInt64()
		pruned += n
		return err
	}); err != nil {
		return pruned, fmt.Errorf("prune proxy queue of site %s: %w", site.ID, err)
	}
	return pruned, nil
}
