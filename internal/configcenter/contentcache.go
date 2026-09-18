package configcenter

import (
	"container/list"
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/configcenter/configdb"
)

// spanOverheadBytes approximates the memory of one cached reference span.
const spanOverheadBytes = 48

// contentKey identifies an immutable published version.
type contentKey struct {
	itemID  string
	version int32
}

// content is an immutable published version prepared for delivery: its
// secret references are scanned once and shared by every reader.
type content struct {
	format      string
	body        string
	publishedAt time.Time
	spans       []secretRefSpan
	refs        []SecretRef
	refsErr     error
}

func newContent(format, body string, publishedAt time.Time) *content {
	c := &content{format: format, body: body, publishedAt: publishedAt}
	spans, err := scanSecretRefs(body)
	if err == nil {
		c.spans = spans
		c.refs, err = distinctRefs(spans)
	}
	c.refsErr = err
	return c
}

func (c *content) memSize() int64 {
	return int64(len(c.body)) + int64(len(c.spans))*spanOverheadBytes
}

type cacheEntry struct {
	key contentKey
	val *content
}

// contentCache is a byte-bounded LRU of published contents plus a
// singleflight group that collapses concurrent loads of the same version
// (for example many watchers woken by one publish).
type contentCache struct {
	maxBytes      int64
	maxEntryBytes int64

	mu    sync.Mutex
	used  int64
	order *list.List
	items map[contentKey]*list.Element

	flight singleflight.Group
}

func newContentCache(maxBytes, maxEntryBytes int64) *contentCache {
	return &contentCache{
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		order:         list.New(),
		items:         make(map[contentKey]*list.Element),
	}
}

func (c *contentCache) get(k contentKey) (*content, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cacheEntry).val, true
}

func (c *contentCache) put(k contentKey, v *content) {
	size := v.memSize()
	if size > c.maxEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.items[k] = c.order.PushFront(&cacheEntry{key: k, val: v})
	c.used += size
	for c.used > c.maxBytes {
		back := c.order.Back()
		if back == nil {
			return
		}
		ent := back.Value.(*cacheEntry)
		c.order.Remove(back)
		delete(c.items, ent.key)
		c.used -= ent.val.memSize()
	}
}

// contentSelection is the result of loadContents.
type contentSelection struct {
	// found holds the selected contents.
	found map[contentKey]*content
	// absent holds keys whose version does not exist (item deleted).
	absent map[contentKey]struct{}
}

func tooLargeError(budget int64) error {
	return apperr.InvalidArgument("", "the requested config items exceed %d bytes in total; request fewer items per call", budget)
}

// loadContents loads published contents in key order within a byte budget.
// The first existing key is always selected. Beyond the budget it fails
// unless partial is set, in which case the remaining keys are neither found
// nor absent (deferred).
func (s *Service) loadContents(ctx context.Context, keys []contentKey, budget int64, partial bool) (contentSelection, error) {
	sel := contentSelection{found: make(map[contentKey]*content, len(keys)), absent: make(map[contentKey]struct{})}
	hits := make(map[contentKey]*content, len(keys))
	var misses []contentKey
	for _, k := range keys {
		if c, ok := s.contents.get(k); ok {
			hits[k] = c
		} else {
			misses = append(misses, k)
		}
	}
	q := s.queries()
	sizes := make(map[contentKey]int64, len(misses))
	if len(misses) > 0 {
		ids, versions := splitContentKeys(misses)
		rows, err := q.ConfigVersionSizes(ctx, configdb.ConfigVersionSizesParams{ItemIds: ids, Versions: versions})
		if err != nil {
			return sel, fmt.Errorf("load config content sizes: %w", err)
		}
		for _, r := range rows {
			sizes[contentKey{itemID: r.ItemID, version: r.Version}] = r.ContentBytes
		}
	}

	var total int64
	var toLoad []contentKey
	selected := 0
	for _, k := range keys {
		var size int64
		hit, isHit := hits[k]
		switch {
		case isHit:
			size = int64(len(hit.body))
		default:
			sz, ok := sizes[k]
			if !ok {
				sel.absent[k] = struct{}{}
				continue
			}
			size = sz
		}
		if selected > 0 && total+size > budget {
			if !partial {
				return sel, tooLargeError(budget)
			}
			break
		}
		total += size
		selected++
		if isHit {
			sel.found[k] = hit
		} else {
			toLoad = append(toLoad, k)
		}
	}
	if err := s.fetchContents(ctx, q, toLoad, sel); err != nil {
		return sel, err
	}
	return sel, nil
}

// fetchContents loads uncached contents into sel (single versions through
// the singleflight group, several in one query) and caches them.
func (s *Service) fetchContents(ctx context.Context, q *configdb.Queries, keys []contentKey, sel contentSelection) error {
	switch len(keys) {
	case 0:
		return nil
	case 1:
		k := keys[0]
		flightKey := k.itemID + ":" + strconv.FormatInt(int64(k.version), 10)
		v, err, _ := s.contents.flight.Do(flightKey, func() (any, error) {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.OperationTimeout)
			defer cancel()
			return s.queryContents(lctx, q, keys)
		})
		if err != nil {
			return err
		}
		mergeContents(v.(map[contentKey]*content), keys, sel)
		return nil
	default:
		loaded, err := s.queryContents(ctx, q, keys)
		if err != nil {
			return err
		}
		mergeContents(loaded, keys, sel)
		return nil
	}
}

func (s *Service) queryContents(ctx context.Context, q *configdb.Queries, keys []contentKey) (map[contentKey]*content, error) {
	ids, versions := splitContentKeys(keys)
	rows, err := q.ConfigVersionContents(ctx, configdb.ConfigVersionContentsParams{ItemIds: ids, Versions: versions})
	if err != nil {
		return nil, fmt.Errorf("load config contents: %w", err)
	}
	out := make(map[contentKey]*content, len(rows))
	for _, r := range rows {
		k := contentKey{itemID: r.ItemID, version: r.Version}
		c := newContent(r.Format, r.Content, r.PublishedAt)
		s.contents.put(k, c)
		out[k] = c
	}
	return out, nil
}

func mergeContents(loaded map[contentKey]*content, keys []contentKey, sel contentSelection) {
	for _, k := range keys {
		if c, ok := loaded[k]; ok {
			sel.found[k] = c
		} else {
			sel.absent[k] = struct{}{}
		}
	}
}

func splitContentKeys(keys []contentKey) ([]string, []int32) {
	ids := make([]string, len(keys))
	versions := make([]int32, len(keys))
	for i, k := range keys {
		ids[i] = k.itemID
		versions[i] = k.version
	}
	return ids, versions
}
