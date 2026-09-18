package hotstate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/redis/rueidis"
)

// scanKeys iterates every key matching pattern on every node with SCAN and
// hands the keys to fn page by page. fn returning stop=true ends the scan.
func (s *Syncer) scanKeys(ctx context.Context, pattern string, fn func(keys []string) (stop bool, err error)) error {
	for addr, node := range s.rdb.Nodes() {
		var cursor uint64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entry, err := node.Do(ctx, node.B().Scan().Cursor(cursor).Match(pattern).Count(scanCount).Build()).AsScanEntry()
			if err != nil {
				return fmt.Errorf("scan %s on %s: %w", pattern, addr, err)
			}
			if len(entry.Elements) > 0 {
				stop, err := fn(entry.Elements)
				if err != nil || stop {
					return err
				}
			}
			cursor = entry.Cursor
			if cursor == 0 {
				break
			}
		}
	}
	return nil
}

// findIdentityKeys scans the identity hashes of a site for the given identity
// IDs ("iid" field) and returns the hot-state keys found.
func (s *Syncer) findIdentityKeys(ctx context.Context, siteKey int64, ids map[string]struct{}) ([]int64, error) {
	base := s.keys.SiteBase(siteKey)
	prefix := base + "id:"
	remaining := len(ids)
	var found []int64
	err := s.scanKeys(ctx, escapeGlob(prefix)+"*", func(keys []string) (bool, error) {
		cmds := make(rueidis.Commands, 0, len(keys))
		hkeys := make([]int64, 0, len(keys))
		for _, k := range keys {
			n, err := strconv.ParseInt(strings.TrimPrefix(k, prefix), 10, 64)
			if err != nil {
				continue
			}
			hkeys = append(hkeys, n)
			cmds = append(cmds, s.rdb.B().Hget().Key(k).Field("iid").Build())
		}
		for i, res := range s.doMulti(ctx, cmds) {
			iid, err := res.ToString()
			if rueidis.IsRedisNil(err) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("read identity id: %w", err)
			}
			if _, ok := ids[iid]; ok {
				found = append(found, hkeys[i])
				remaining--
			}
		}
		return remaining <= 0, nil
	})
	if err != nil {
		return nil, fmt.Errorf("find identities of site %d: %w", siteKey, err)
	}
	return found, nil
}

// escapeGlob backslash-escapes the Redis glob metacharacters * ? [ ] \ in s.
func escapeGlob(s string) string {
	if !strings.ContainsAny(s, `*?[]\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// siteKeyOf parses the site hot-state key from a key "P:{s<key>}:...".
func siteKeyOf(prefix, key string) (int64, string, bool) {
	rest, ok := strings.CutPrefix(key, prefix+":{s")
	if !ok {
		return 0, "", false
	}
	end := strings.Index(rest, "}:")
	if end <= 0 {
		return 0, "", false
	}
	n, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, rest[end+2:], true
}
