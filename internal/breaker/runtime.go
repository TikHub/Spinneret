package breaker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/breaker/breakerdb"
)

// runtimeTimeLayout formats timestamps in runtime content (RFC 3339, UTC,
// millisecond precision).
const runtimeTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// maxRuntimeCacheEntries bounds the cached runtime documents (one per
// namespace and kind).
const maxRuntimeCacheEntries = 10_000

// runtimeKey identifies a cached runtime document.
type runtimeKey struct {
	namespaceID string
	kind        string
}

// runtimeEntry is a rendered runtime document of one version.
type runtimeEntry struct {
	version int64
	content string
}

// runtimeCache keeps the latest rendered document per namespace and kind.
type runtimeCache struct {
	mu      sync.Mutex
	entries map[runtimeKey]runtimeEntry
}

func newRuntimeCache() *runtimeCache {
	return &runtimeCache{entries: make(map[runtimeKey]runtimeEntry)}
}

func (c *runtimeCache) get(k runtimeKey, version int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || e.version != version {
		return "", false
	}
	return e.content, true
}

// put stores the latest rendered document of k. It always replaces the entry:
// get only serves an exact version match, so an out-of-order put merely costs
// a re-render, while keeping a higher version could serve stale content after
// the version counter was reset (Redis data loss) and climbed back to it.
func (c *runtimeCache) put(k runtimeKey, version int64, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[k]; !ok && len(c.entries) >= maxRuntimeCacheEntries {
		for victim := range c.entries {
			delete(c.entries, victim)
			break
		}
	}
	c.entries[k] = runtimeEntry{version: version, content: content}
}

// BreakersContent is the "_runtime/breakers" document.
type BreakersContent struct {
	Namespace string                  `json:"namespace"`
	Version   int64                   `json:"version"`
	Sites     map[string]BreakersSite `json:"sites"`
}

// BreakersSite lists the non-closed breakers of a site.
type BreakersSite struct {
	Paused bool                     `json:"paused"`
	Groups map[string]BreakersGroup `json:"groups"`
}

// BreakersGroup is one non-closed breaker, keyed "<client>/<group>".
type BreakersGroup struct {
	State     string  `json:"state"`
	OpenUntil *string `json:"open_until"`
	Manual    bool    `json:"manual"`
	Reason    string  `json:"reason"`
}

// SiteSwitchesContent is the "_runtime/site_switches" document.
type SiteSwitchesContent struct {
	Namespace string                       `json:"namespace"`
	Version   int64                        `json:"version"`
	Sites     map[string]SiteSwitchContent `json:"sites"`
}

// SiteSwitchContent is the switch of one site.
type SiteSwitchContent struct {
	Paused   bool    `json:"paused"`
	Reason   string  `json:"reason"`
	PausedAt *string `json:"paused_at"`
}

func validRuntimeKind(kind string) error {
	if kind != KindBreakers && kind != KindSiteSwitches {
		return apperr.InvalidArgument("", "unknown runtime config kind %q", kind)
	}
	return nil
}

// RuntimeVersion returns the runtime config version of kind ("breakers" or
// "site_switches") for the namespace: HGET "rtv:<ns>" kind, 0 when missing.
func (s *Service) RuntimeVersion(ctx context.Context, namespaceID, kind string) (int64, error) {
	if err := validRuntimeKind(kind); err != nil {
		return 0, err
	}
	rctx, cancel := s.ioContext(ctx)
	defer cancel()
	v, err := s.rdb.Do(rctx, s.rdb.B().Hget().Key(s.keys.RuntimeVersions(namespaceID)).Field(kind).Build()).ToString()
	if rueidis.IsRedisNil(err) {
		return 0, nil
	}
	if err != nil {
		return 0, apperr.Internal(fmt.Errorf("read runtime version %s of namespace %s: %w", kind, namespaceID, err))
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, apperr.Internal(fmt.Errorf("parse runtime version %s of namespace %s: %w", kind, namespaceID, err))
	}
	return n, nil
}

// RuntimeContent renders the read-only runtime config document of kind for
// the namespace and returns it with its version. Documents are cached per
// (namespace, kind, version).
//
// Kind "breakers" (BreakersContent) lists every site with its paused flag and
// only its non-closed breakers, keyed "<client>/<group>"; open_until is RFC 3339
// for timed opens and null otherwise:
//
//	{"namespace":"prod","version":7,"sites":{"shop":{"paused":false,"groups":
//	  {"web/search":{"state":"open","open_until":"2026-09-17T10:02:00.000Z","manual":false,"reason":"…"}}}}}
//
// Kind "site_switches" (SiteSwitchesContent):
//
//	{"namespace":"prod","version":3,"sites":{"shop":{"paused":true,"reason":"…","paused_at":"2026-09-17T10:00:00.000Z"}}}
func (s *Service) RuntimeContent(ctx context.Context, namespaceID, kind string) (string, int64, error) {
	version, err := s.RuntimeVersion(ctx, namespaceID, kind)
	if err != nil {
		return "", 0, err
	}
	key := runtimeKey{namespaceID: namespaceID, kind: kind}
	if content, ok := s.runtime.get(key, version); ok {
		return content, version, nil
	}
	var doc any
	switch kind {
	case KindBreakers:
		doc, err = s.breakersContent(ctx, namespaceID, version)
	default:
		doc, err = s.siteSwitchesContent(ctx, namespaceID, version)
	}
	if err != nil {
		return "", 0, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", 0, apperr.Internal(fmt.Errorf("marshal runtime %s: %w", kind, err))
	}
	content := string(raw)
	s.runtime.put(key, version, content)
	return content, version, nil
}

// namespaceSites loads the namespace name and its sites.
func (s *Service) namespaceSites(ctx context.Context, namespaceID string) (string, []breakerdb.BreakerNamespaceSitesRow, error) {
	qctx, cancel := s.ioContext(ctx)
	defer cancel()
	ns, err := s.q.BreakerNamespace(qctx, namespaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, apperr.NotFound("namespace not found")
	}
	if err != nil {
		return "", nil, apperr.Internal(fmt.Errorf("load namespace %s: %w", namespaceID, err))
	}
	sites, err := s.q.BreakerNamespaceSites(qctx, namespaceID)
	if err != nil {
		return "", nil, apperr.Internal(fmt.Errorf("load sites of namespace %s: %w", namespaceID, err))
	}
	return ns.Name, sites, nil
}

func (s *Service) siteSwitchesContent(ctx context.Context, namespaceID string, version int64) (SiteSwitchesContent, error) {
	name, sites, err := s.namespaceSites(ctx, namespaceID)
	if err != nil {
		return SiteSwitchesContent{}, err
	}
	doc := SiteSwitchesContent{Namespace: name, Version: version, Sites: make(map[string]SiteSwitchContent, len(sites))}
	for _, site := range sites {
		sw := SiteSwitchContent{Paused: site.Paused}
		if site.Paused {
			sw.Reason = site.PausedReason
			sw.PausedAt = formatRuntimeTime(site.PausedAt)
		}
		doc.Sites[site.Name] = sw
	}
	return doc, nil
}

func (s *Service) breakersContent(ctx context.Context, namespaceID string, version int64) (BreakersContent, error) {
	name, sites, err := s.namespaceSites(ctx, namespaceID)
	if err != nil {
		return BreakersContent{}, err
	}
	doc := BreakersContent{Namespace: name, Version: version, Sites: make(map[string]BreakersSite, len(sites))}
	if len(sites) == 0 {
		return doc, nil
	}

	// Non-closed breaker members per site.
	type member struct {
		siteName string
		siteKey  int64
		groupKey int64
	}
	var members []member
	for start := 0; start < len(sites); start += readsPerPipeline {
		end := min(start+readsPerPipeline, len(sites))
		cmds := make(rueidis.Commands, 0, end-start)
		for _, site := range sites[start:end] {
			cmds = append(cmds, s.rdb.B().Smembers().Key(s.keys.OpenBreakers(site.Hkey)).Build())
		}
		rctx, cancel := s.ioContext(ctx)
		results := s.rdb.DoMulti(rctx, cmds...)
		cancel()
		for i, res := range results {
			site := sites[start+i]
			doc.Sites[site.Name] = BreakersSite{Paused: site.Paused, Groups: map[string]BreakersGroup{}}
			keys, err := res.AsStrSlice()
			if err != nil {
				return BreakersContent{}, apperr.Internal(fmt.Errorf("read open breakers of site %s: %w", site.ID, err))
			}
			for _, k := range keys {
				gk, err := strconv.ParseInt(k, 10, 64)
				if err == nil {
					members = append(members, member{siteName: site.Name, siteKey: site.Hkey, groupKey: gk})
				}
			}
		}
	}
	if len(members) == 0 {
		return doc, nil
	}

	hkeys := make([]int64, 0, len(members))
	for _, m := range members {
		hkeys = append(hkeys, m.groupKey)
	}
	qctx, cancel := s.ioContext(ctx)
	rows, err := s.q.BreakerGroupsByKeys(qctx, breakerdb.BreakerGroupsByKeysParams{NamespaceID: namespaceID, Hkeys: hkeys})
	cancel()
	if err != nil {
		return BreakersContent{}, apperr.Internal(fmt.Errorf("load endpoint groups of namespace %s: %w", namespaceID, err))
	}
	names := make(map[int64]string, len(rows))
	for _, r := range rows {
		names[r.Hkey] = r.Client + "/" + r.Name
	}

	for start := 0; start < len(members); start += readsPerPipeline {
		end := min(start+readsPerPipeline, len(members))
		cmds := make(rueidis.Commands, 0, end-start)
		for _, m := range members[start:end] {
			cmds = append(cmds, s.rdb.B().Hmget().Key(s.keys.Breaker(m.siteKey, m.groupKey)).Field("st", "ou", "man", "rsn").Build())
		}
		rctx, cancel := s.ioContext(ctx)
		results := s.rdb.DoMulti(rctx, cmds...)
		cancel()
		for i, res := range results {
			m := members[start+i]
			groupName, known := names[m.groupKey]
			vals, err := res.ToArray()
			if err != nil {
				return BreakersContent{}, apperr.Internal(fmt.Errorf("read breaker of group %d: %w", m.groupKey, err))
			}
			if !known || len(vals) != 4 {
				continue
			}
			fields := make(map[string]string, 4)
			for j, f := range []string{"st", "ou", "man", "rsn"} {
				if v, err := vals[j].ToString(); err == nil {
					fields[f] = v
				}
			}
			h := parseHashFields(fields)
			if h.State == StateClosed {
				continue
			}
			g := BreakersGroup{State: h.State, Manual: h.Manual, Reason: h.Reason}
			if h.State == StateOpen && h.OpenUntilMs > 0 {
				t := millisTime(h.OpenUntilMs)
				g.OpenUntil = formatRuntimeTime(&t)
			}
			doc.Sites[m.siteName].Groups[groupName] = g
		}
	}
	return doc, nil
}

func formatRuntimeTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(runtimeTimeLayout)
	return &s
}
