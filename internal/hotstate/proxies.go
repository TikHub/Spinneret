package hotstate

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
)

// proxyRow is the PostgreSQL view of a proxy used for materialization.
type proxyRow = hotstatedb.HotstateProxiesByIDsRow

// SyncProxies materializes proxies of a namespace into every site of the
// namespace (spec §5.4): static fields of "px:<p>" (preserving the lease and
// health fields owned by other scripts), the lifecycle ends "bu"/"qu" compared
// by apply.lua (from proxies.ban_until) and "pxrdy" membership with score
// max(cd, gcd). The PostgreSQL lifecycle fields are applied unless Redis holds
// a newer Redis-first change that is still in force (compared by
// state_changed_at, see sync_proxies.lua), and the global cooldown "gcd" is
// max(Redis, PostgreSQL), so automatic bans, quarantines and proxy-global
// cooldowns that exist only in Redis survive. Proxies that no longer exist are
// removed (RemoveProxies).
func (s *Syncer) SyncProxies(ctx context.Context, namespaceID string, proxyIDs []string) error {
	ids := dedupeStrings(proxyIDs)
	if len(ids) == 0 {
		return nil
	}
	sites, err := s.namespaceSiteKeys(ctx, namespaceID)
	if err != nil {
		return err
	}
	var missing []string
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		rows, err := s.q.HotstateProxiesByIDs(ctx, hotstatedb.HotstateProxiesByIDsParams{NamespaceID: namespaceID, Ids: chunk})
		if err != nil {
			return fmt.Errorf("load proxies of namespace %s: %w", namespaceID, err)
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
		for _, siteKey := range sites {
			if err := s.applyProxies(ctx, siteKey, rows, modeAuthoritative); err != nil {
				return err
			}
		}
	}
	if len(missing) > 0 {
		return s.RemoveProxies(ctx, namespaceID, missing)
	}
	return nil
}

// RemoveProxies removes proxies from every site of a namespace: "px:<p>",
// cross-attribution state and "pxrdy" membership, clears identity bindings
// ("px") that point to them and deletes their hot_state_snapshots rows.
// Proxies still present in PostgreSQL are resolved by ID and their bindings
// through proxy_bindings; proxies already deleted are found by scanning the
// proxy hashes ("pid"), and their former bindings by scanning identity hashes,
// so callers should remove proxies before deleting their rows.
func (s *Syncer) RemoveProxies(ctx context.Context, namespaceID string, proxyIDs []string) error {
	ids := dedupeStrings(proxyIDs)
	if len(ids) == 0 {
		return nil
	}
	siteRows, err := s.q.HotstateNamespaceSites(ctx, namespaceID)
	if err != nil {
		return fmt.Errorf("load sites of namespace %s: %w", namespaceID, err)
	}
	sites := make([]int64, len(siteRows))
	siteSet := make(map[int64]struct{}, len(siteRows))
	siteKeyByID := make(map[string]int64, len(siteRows))
	for i, r := range siteRows {
		sites[i] = r.Hkey
		siteSet[r.Hkey] = struct{}{}
		siteKeyByID[r.ID] = r.Hkey
	}

	proxyKeys := make(map[int64]struct{})
	bindings := make(map[int64][]string) // site key → identity/proxy hkey pairs
	unknown := make(map[string]struct{})
	var removedIDs []string
	for _, chunk := range chunkStrings(ids, idLookupChunk) {
		rows, err := s.q.HotstateProxiesByIDs(ctx, hotstatedb.HotstateProxiesByIDsParams{NamespaceID: namespaceID, Ids: chunk})
		if err != nil {
			return fmt.Errorf("load proxies of namespace %s: %w", namespaceID, err)
		}
		found := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			found[r.ID] = struct{}{}
			proxyKeys[r.Hkey] = struct{}{}
			removedIDs = append(removedIDs, r.ID)
		}
		for _, id := range chunk {
			if _, ok := found[id]; !ok && idgen.Valid(id, idgen.Proxy) {
				unknown[id] = struct{}{}
			}
		}
		bound, err := s.q.HotstateProxyBindingsByProxyIDs(ctx, chunk)
		if err != nil {
			return fmt.Errorf("load proxy bindings: %w", err)
		}
		for _, b := range bound {
			if key, ok := siteKeyByID[b.SiteID]; ok {
				bindings[key] = append(bindings[key], itoa(b.IdentityHkey), itoa(b.ProxyHkey))
			}
		}
	}
	if len(unknown) > 0 {
		deleted, err := s.findDeletedProxyKeys(ctx, siteSet, unknown)
		if err != nil {
			return err
		}
		if len(deleted) > 0 {
			for k, pid := range deleted {
				proxyKeys[k] = struct{}{}
				removedIDs = append(removedIDs, pid)
			}
			if err := s.findBindingsByScan(ctx, siteSet, deleted, bindings); err != nil {
				return err
			}
		}
	}

	members := make([]string, 0, len(proxyKeys))
	for k := range proxyKeys {
		members = append(members, itoa(k))
	}
	for _, siteKey := range sites {
		if err := s.removeSiteProxies(ctx, siteKey, members, bindings[siteKey]); err != nil {
			return err
		}
	}
	for _, chunk := range chunkStrings(dedupeStrings(removedIDs), idLookupChunk) {
		if err := s.q.HotstateDeleteProxySnapshots(ctx, chunk); err != nil {
			return fmt.Errorf("delete proxy hot-state snapshots: %w", err)
		}
	}
	return nil
}

// removeSiteProxies runs the remove_proxies and clear_bindings maintenance
// operations on one site.
func (s *Syncer) removeSiteProxies(ctx context.Context, siteKey int64, members, bindingPairs []string) error {
	meta := s.keys.SiteMeta(siteKey)
	var calls []rueidis.LuaExec
	for _, chunk := range chunkStrings(members, proxiesPerCall) {
		calls = append(calls, rueidis.LuaExec{Keys: []string{meta}, Args: append([]string{"remove_proxies"}, chunk...)})
	}
	for start := 0; start < len(bindingPairs); start += 2 * proxiesPerCall {
		end := min(start+2*proxiesPerCall, len(bindingPairs))
		calls = append(calls, rueidis.LuaExec{Keys: []string{meta}, Args: append([]string{"clear_bindings"}, bindingPairs[start:end]...)})
	}
	if err := s.runScripts(ctx, s.syncMaintenance, calls, nil); err != nil {
		return fmt.Errorf("remove proxies from site %d: %w", siteKey, err)
	}
	return nil
}

// proxyRecordValues is the number of ARGV values of one proxy record of
// sync_proxies.lua.
const proxyRecordValues = 13

// applyProxies writes proxy rows into one site.
func (s *Syncer) applyProxies(ctx context.Context, siteKey int64, rows []proxyRow, mode string) error {
	if len(rows) == 0 {
		return nil
	}
	meta := s.keys.SiteMeta(siteKey)
	nowMs := itoa(s.now().UnixMilli())
	calls := make([]rueidis.LuaExec, 0, (len(rows)+proxiesPerCall-1)/proxiesPerCall)
	for start := 0; start < len(rows); start += proxiesPerCall {
		end := min(start+proxiesPerCall, len(rows))
		args := make([]string, 0, 2+proxyRecordValues*(end-start))
		args = append(args, mode, nowMs)
		for _, r := range rows[start:end] {
			args = append(args, itoa(r.Hkey), r.ID, r.State, r.Kind, r.Region, r.Provider, tagList(r.Tags),
				itoa(int64(r.MaxConcurrency)), itoa(int64(r.UrlVersion)), itoa(timeMs(r.CooldownUntil)),
				itoa(banUntilMs(r.State, r.BanUntil)), itoa(proxyQuarantineUntilMs(r.State, r.BanUntil)),
				itoa(r.StateChangedAt.UnixMilli()))
		}
		calls = append(calls, rueidis.LuaExec{Keys: []string{meta}, Args: args})
	}
	if err := s.runScripts(ctx, s.syncProxies, calls, nil); err != nil {
		return fmt.Errorf("sync proxies of site %d: %w", siteKey, err)
	}
	return nil
}

// proxyQuarantineUntilMs encodes the proxy "qu" field: proxies keep the end
// of a quarantine in proxies.ban_until, so it is the quarantine end while
// quarantined and 0 otherwise.
func proxyQuarantineUntilMs(state string, banUntil *time.Time) int64 {
	if state != stateQuar || banUntil == nil {
		return 0
	}
	return banUntil.UnixMilli()
}

// namespaceSiteKeys returns the hot-state keys of the sites of a namespace.
func (s *Syncer) namespaceSiteKeys(ctx context.Context, namespaceID string) ([]int64, error) {
	rows, err := s.q.HotstateNamespaceSites(ctx, namespaceID)
	if err != nil {
		return nil, fmt.Errorf("load sites of namespace %s: %w", namespaceID, err)
	}
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Hkey
	}
	return out, nil
}

// findDeletedProxyKeys scans the proxy hashes of the given sites for proxies
// whose "pid" is in ids and returns their hot-state keys mapped to their IDs.
func (s *Syncer) findDeletedProxyKeys(ctx context.Context, sites map[int64]struct{}, ids map[string]struct{}) (map[int64]string, error) {
	prefix := s.keyPrefix()
	found := make(map[int64]string)
	err := s.scanKeys(ctx, escapeGlob(prefix)+":{s*}:px:*", func(keys []string) (bool, error) {
		cmds := make(rueidis.Commands, 0, len(keys))
		pkeys := make([]int64, 0, len(keys))
		for _, k := range keys {
			siteKey, rest, ok := siteKeyOf(prefix, k)
			if !ok {
				continue
			}
			if _, ok := sites[siteKey]; !ok {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimPrefix(rest, "px:"), 10, 64)
			if err != nil || !strings.HasPrefix(rest, "px:") {
				continue
			}
			pkeys = append(pkeys, n)
			cmds = append(cmds, s.rdb.B().Hget().Key(k).Field("pid").Build())
		}
		for i, res := range s.doMulti(ctx, cmds) {
			pid, err := res.ToString()
			if rueidis.IsRedisNil(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("read proxy id: %w", err)
			}
			if _, ok := ids[pid]; ok {
				found[pkeys[i]] = pid
			}
		}
		return false, nil
	})
	if err != nil {
		return nil, fmt.Errorf("find deleted proxies: %w", err)
	}
	return found, nil
}

// findBindingsByScan scans identity hashes of the given sites and appends
// identity/proxy pairs whose "px" is one of the proxy keys to bindings.
func (s *Syncer) findBindingsByScan(ctx context.Context, sites map[int64]struct{}, proxyKeys map[int64]string, bindings map[int64][]string) error {
	prefix := s.keyPrefix()
	wanted := make(map[string]struct{}, len(proxyKeys))
	for k := range proxyKeys {
		wanted[itoa(k)] = struct{}{}
	}
	err := s.scanKeys(ctx, escapeGlob(prefix)+":{s*}:id:*", func(keys []string) (bool, error) {
		cmds := make(rueidis.Commands, 0, len(keys))
		type ref struct {
			site int64
			id   string
		}
		refs := make([]ref, 0, len(keys))
		for _, k := range keys {
			siteKey, rest, ok := siteKeyOf(prefix, k)
			if !ok || !strings.HasPrefix(rest, "id:") {
				continue
			}
			if _, ok := sites[siteKey]; !ok {
				continue
			}
			refs = append(refs, ref{site: siteKey, id: strings.TrimPrefix(rest, "id:")})
			cmds = append(cmds, s.rdb.B().Hget().Key(k).Field("px").Build())
		}
		for i, res := range s.doMulti(ctx, cmds) {
			px, err := res.ToString()
			if rueidis.IsRedisNil(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("read identity binding: %w", err)
			}
			if _, ok := wanted[px]; ok {
				bindings[refs[i].site] = append(bindings[refs[i].site], refs[i].id, px)
			}
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("find bindings of deleted proxies: %w", err)
	}
	return nil
}
