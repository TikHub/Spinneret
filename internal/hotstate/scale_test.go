package hotstate

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scaleEnv overrides the scale test size as "<identities>:<groups>".
const scaleEnv = "SPINNERET_HOTSTATE_SCALE"

// TestSyncSiteScale materializes 20 000 identities × 5 endpoint groups (or the
// size in SPINNERET_HOTSTATE_SCALE) and reports the duration of a cold
// rebuild, a warm SyncSite and a SyncSite that has to prune stale members.
func TestSyncSiteScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test skipped in -short mode")
	}
	identities, groupCount := 20000, 5
	if v := os.Getenv(scaleEnv); v != "" {
		a, b, _ := strings.Cut(v, ":")
		n, err1 := strconv.Atoi(a)
		g, err2 := strconv.Atoi(b)
		require.NoError(t, err1)
		require.NoError(t, err2)
		identities, groupCount = n, g
	}
	f := newFixture(t)
	site := f.addSite(f.ns, "scale", "web")
	typ := f.addType(site, "web", "cookie")
	groups := []int64{site.Groups[groupKey("web", "_default")].Key}
	for i := 1; i < groupCount; i++ {
		groups = append(groups, f.addGroup(site, "web", fmt.Sprintf("group-%d", i)).Key)
	}
	accID, _ := f.addAccount(site, "shared", "active", nil)
	f.exec(`
		INSERT INTO identities (id, site_id, client, type_id, account_id, state, unique_hash, payload_hash)
		SELECT 'idt_' || lpad(to_hex(n), 32, '0'), $1, 'web', $2,
		       CASE WHEN n % 100 = 0 THEN $3 END,
		       CASE WHEN n % 10 = 0 THEN 'banned' WHEN n % 7 = 0 THEN 'pending' ELSE 'active' END,
		       decode(lpad(to_hex(n), 32, '0'), 'hex'), decode(lpad(to_hex(n), 32, '0'), 'hex')
		FROM generate_series(1, $4::int) AS n`, site.ID, typ.ID, accID, identities)
	for i := 0; i < 200; i++ {
		f.addProxy(f.ns, proxySpec{})
	}
	var ready int64
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM identities WHERE site_id = $1 AND state IN ('active', 'pending')`, site.ID).Scan(&ready))

	start := time.Now()
	require.NoError(t, f.syncer.RebuildAll(f.ctx))
	rebuild := time.Since(start)

	counts := func() {
		t.Helper()
		for _, g := range groups {
			n, err := f.rdb.Do(f.ctx, f.rdb.B().Zcard().Key(f.keys.Ready(site.Key, g)).Build()).AsInt64()
			require.NoError(t, err)
			require.Equal(t, ready, n)
		}
	}
	counts()

	start = time.Now()
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	warm := time.Since(start)
	counts()

	for i := int64(0); i < 50; i++ {
		f.do(f.rdb.B().Zadd().Key(f.keys.Ready(site.Key, groups[i%int64(len(groups))])).ScoreMember().ScoreMember(0, key(10_000_000+i)).Build())
	}
	start = time.Now()
	require.NoError(t, f.syncer.SyncSite(f.ctx, site.ID))
	pruning := time.Since(start)
	counts()

	t.Logf("identities=%d groups=%d proxies=200: rebuild=%s sync_site=%s sync_site_with_prune=%s",
		identities, len(groups), rebuild, warm, pruning)
}
