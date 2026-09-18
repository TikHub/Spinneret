package configcenter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
)

// maxRuntimeCacheEntries bounds the runtime content cache (two entries per
// namespace); the cache is reset when it grows beyond the bound.
const maxRuntimeCacheEntries = 4096

// runtimeKinds lists the "_runtime" items with their descriptions.
var runtimeKinds = []struct {
	kind        string
	description string
}{
	{RuntimeBreakers, "Endpoint group circuit breaker states (system-maintained, read-only)."},
	{RuntimeSiteSwitches, "Site pause switches (system-maintained, read-only)."},
}

func validRuntimeKind(kind string) bool {
	return kind == RuntimeBreakers || kind == RuntimeSiteSwitches
}

func runtimeDescription(kind string) string {
	for _, rk := range runtimeKinds {
		if rk.kind == kind {
			return rk.description
		}
	}
	return ""
}

// apiRuntimeVersion maps a provider version to the API version. Provider
// counters start at 0 (no transition yet) while API versions start at 1
// ("0 = holds nothing" in WatchConfig), so the API version is provider + 1.
func apiRuntimeVersion(v int64) int32 {
	if v < 0 {
		v = 0
	}
	if v >= math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v + 1)
}

type runtimeContent struct {
	body    string
	version int32
}

// runtimeCache keeps the last content of each runtime item and collapses
// concurrent provider reads.
type runtimeCache struct {
	mu     sync.Mutex
	items  map[itemKey]runtimeContent
	flight singleflight.Group
}

func newRuntimeCache() *runtimeCache {
	return &runtimeCache{items: make(map[itemKey]runtimeContent)}
}

func (c *runtimeCache) get(k itemKey) (runtimeContent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[k]
	return v, ok
}

func (c *runtimeCache) put(k itemKey, v runtimeContent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; !ok && len(c.items) >= maxRuntimeCacheEntries {
		clear(c.items)
	}
	c.items[k] = v
}

// runtimeContentFor returns the content of a runtime item. When want > 0 and
// the cached content has that version it is reused; otherwise the provider
// is read (concurrent reads of the same item are collapsed).
func (s *Service) runtimeContentFor(ctx context.Context, nsID, kind string, want int32) (runtimeContent, bool, error) {
	if s.runtime == nil || !validRuntimeKind(kind) {
		return runtimeContent{}, false, nil
	}
	k := itemKey{ns: nsID, group: RuntimeGroup, key: kind}
	if want > 0 {
		if rc, ok := s.runtimes.get(k); ok && rc.version == want {
			return rc, true, nil
		}
	}
	v, err, _ := s.runtimes.flight.Do(nsID+"\x00"+kind, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.OperationTimeout)
		defer cancel()
		body, version, err := s.runtime.RuntimeContent(lctx, nsID, kind)
		if err != nil {
			return nil, fmt.Errorf("read runtime config %s: %w", kind, err)
		}
		rc := runtimeContent{body: body, version: apiRuntimeVersion(version)}
		s.runtimes.put(k, rc)
		return rc, nil
	})
	if err != nil {
		return runtimeContent{}, false, err
	}
	return v.(runtimeContent), true, nil
}

// runtimeItemInfo builds the admin view of a runtime item including content.
func (s *Service) runtimeItemInfo(ctx context.Context, ns *catalog.Namespace, kind string) (Item, error) {
	rc, ok, err := s.runtimeContentFor(ctx, ns.ID, kind, 0)
	if err != nil {
		return Item{}, err
	}
	if !ok {
		return Item{}, apperr.NotFound(itemNotFoundFormat)
	}
	it := runtimeItemStub(ns, kind)
	it.CurrentVersion = rc.version
	it.PublishedContent = rc.body
	return it, nil
}

func runtimeItemStub(ns *catalog.Namespace, kind string) Item {
	return Item{
		NamespaceID:   ns.ID,
		NamespaceName: ns.Name,
		Group:         RuntimeGroup,
		Key:           kind,
		Format:        FormatJSON,
		Description:   runtimeDescription(kind),
		ReadOnly:      true,
	}
}

// virtualRuntimeItems returns the "_runtime" items matching the list filters
// that the principal may read, with their current versions (no content).
func (s *Service) virtualRuntimeItems(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, group, search string) []Item {
	if s.runtime == nil || (group != "" && group != RuntimeGroup) {
		return nil
	}
	if !p.Can(authz.PermConfigRead, configResource(ns, RuntimeGroup)) {
		return nil
	}
	needle := strings.ToLower(search)
	var out []Item
	for _, rk := range runtimeKinds {
		if needle != "" && !strings.Contains(rk.kind, needle) && !strings.Contains(strings.ToLower(rk.description), needle) {
			continue
		}
		it := runtimeItemStub(ns, rk.kind)
		v, err := s.runtime.RuntimeVersion(ctx, ns.ID, rk.kind)
		if err != nil {
			s.logger.Warn("read runtime config version failed",
				slog.String("namespace_id", ns.ID), slog.String("kind", rk.kind), slog.Any("error", err))
		} else {
			it.CurrentVersion = apiRuntimeVersion(v)
		}
		out = append(out, it)
	}
	return out
}
