package vault

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveCacheBounds(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	value := strings.Repeat("v", 1000)
	entry := resolveEntry{secretID: "sec_1", value: value}
	perEntry := resolveEntrySize(resolveKey("ns", "p0"), entry)

	tests := []struct {
		name        string
		size        int
		maxBytes    int
		adds        int
		wantEntries int
	}{
		{"count bound", 3, 1 << 20, 10, 3},
		{"byte bound", 100, 4 * perEntry, 10, 4},
		{"entry larger than budget", 100, perEntry - 1, 5, 0},
		{"non-positive size falls back to one entry", 0, 1 << 20, 5, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newResolveCache(tt.size, tt.maxBytes, time.Minute)
			for i := range tt.adds {
				c.addIfCurrent("ns", "p"+strconv.Itoa(i), entry, now, c.generation())
			}
			entries, bytes := c.usage()
			require.Equal(t, tt.wantEntries, entries)
			require.LessOrEqual(t, bytes, tt.maxBytes)
			require.Equal(t, entries*perEntry, bytes, "byte accounting follows evictions")
			if tt.wantEntries > 0 {
				_, ok := c.get("ns", "p"+strconv.Itoa(tt.adds-1), now)
				require.True(t, ok, "the newest entry is kept")
			}
		})
	}
}

func TestResolveCacheAccounting(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	c := newResolveCache(10, 1<<20, time.Second)
	small := resolveEntry{secretID: "sec_1", value: "small"}
	large := resolveEntry{secretID: "sec_1", value: strings.Repeat("x", 500)}

	// Replacing an entry swaps its size instead of adding to it.
	c.addIfCurrent("ns", "p", small, now, c.generation())
	c.addIfCurrent("ns", "p", large, now, c.generation())
	entries, bytes := c.usage()
	require.Equal(t, 1, entries)
	require.Equal(t, resolveEntrySize(resolveKey("ns", "p"), large), bytes)

	// Expired entries are dropped on access and release their bytes.
	_, ok := c.get("ns", "p", now.Add(time.Second))
	require.False(t, ok)
	entries, bytes = c.usage()
	require.Zero(t, entries)
	require.Zero(t, bytes)

	// An invalidation after the generation was read discards the add.
	gen := c.generation()
	c.remove("ns", "other")
	c.addIfCurrent("ns", "p", small, now, gen)
	_, ok = c.get("ns", "p", now)
	require.False(t, ok)
	c.addIfCurrent("ns", "p", small, now, c.generation())
	got, ok := c.get("ns", "p", now)
	require.True(t, ok)
	require.Equal(t, "small", got.value)
	c.remove("ns", "p")
	entries, bytes = c.usage()
	require.Zero(t, entries)
	require.Zero(t, bytes)
}
