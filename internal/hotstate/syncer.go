// Package hotstate synchronizes the Redis hot state (spec §5) with the
// PostgreSQL truth: it materializes identities, accounts, proxies and ready
// queues, rebuilds everything after Redis data loss (with cold-start
// protection and health restore from hot_state_snapshots), reads the live
// state of identities for the console and persists changed health state back
// to PostgreSQL with a leader job.
//
// Scripts run by other packages (acquire, release, observe, apply, breaker)
// read and write the same keys concurrently. The synchronization therefore
// never overwrites hot counters owned by those scripts (al xl scd sru gs gts gn
// gnf glf lu rbd rbn on identities, al sc sts sn nf lf cd on proxies), never
// reverts newer Redis-first lifecycle changes (compared by their change time
// "sct") or shortens automatic proxy-global cooldowns, keeps proxy
// bindings ("px") created by acquire.lua while their proxy is materialized,
// and re-verifies candidates inside Lua before removing ready-queue members.
package hotstate

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/hotstate/hotstatedb"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
)

// Tuning constants. They bound memory, Lua blocking time and round trips.
const (
	// identityPageSize is the number of identities loaded per PostgreSQL page.
	identityPageSize = 1000
	// identitiesPerCall bounds the identities handled by one script call.
	identitiesPerCall = 100
	// groupOpsPerCall bounds identities × endpoint groups per script call. One
	// group operation costs about one microsecond of Lua time, so a call
	// blocks Redis for roughly a millisecond (acquire p99 target: 5 ms).
	groupOpsPerCall = 1000
	// callsPerPipeline is the number of script calls sent per round trip.
	// Redis executes a pipeline back to back, so this multiplies the blocking
	// time of a call; keep it at 1 for bulk materialization scripts.
	callsPerPipeline = 1
	// proxyPageSize is the number of proxies loaded per PostgreSQL page.
	proxyPageSize = 1000
	// proxiesPerCall bounds the proxies handled by one script call.
	proxiesPerCall = 200
	// accountMembersPerCall bounds the member hkeys passed to one account call.
	accountMembersPerCall = 1000
	// idLookupChunk bounds the IDs passed to one PostgreSQL ANY() lookup.
	idLookupChunk = 1000
	// commandsPerPipeline bounds plain commands sent per round trip.
	commandsPerPipeline = 500
	// scanCount is the COUNT hint of SCAN/ZSCAN iterations.
	scanCount = 1000
	// pruneCandidatesPerCall bounds the candidates verified by one prune call.
	pruneCandidatesPerCall = 500
	// coldStartJitter is the maximum random delay added to ready scores that
	// would otherwise be <= now after a rebuild (spec §5.6).
	coldStartJitter = 60 * time.Second
	// ensurePollInterval is how often EnsureBuilt polls the epoch while
	// another instance rebuilds.
	ensurePollInterval = 500 * time.Millisecond
	// rebuildLockName names the PostgreSQL advisory lock serializing rebuilds.
	rebuildLockName = "spinneret:hotstate:rebuild"
	// metaGroupsField is the site meta field listing the endpoint group hkeys
	// materialized by the last SyncSite (additive to spec §5; lets later syncs
	// drop the keys of deleted groups without a keyspace scan).
	metaGroupsField = "egs"
)

// Identity and proxy states used by the materialization.
const (
	stateActive  = "active"
	statePending = "pending"
	stateBanned  = "banned"
	stateQuar    = "quarantined"
	stateRetired = "retired"
)

// Script modes (ARGV of the sync scripts).
const (
	modeAuthoritative = "a"
	modeMerge         = "m"
)

var (
	//go:embed lua/sync_identities.lua
	syncIdentitiesLua string
	//go:embed lua/sync_accounts.lua
	syncAccountsLua string
	//go:embed lua/sync_proxies.lua
	syncProxiesLua string
	//go:embed lua/sync_maintenance.lua
	syncMaintenanceLua string
)

// SyncOptions controls optional resets applied while synchronizing identities.
type SyncOptions struct {
	// ResetHealth resets the existing per-endpoint health entries to the
	// endpoint group baseline (score, samples and the failure streak; the
	// scheduling fields cd, ru and lu are kept), deletes the global score
	// (gs/gts/gn) and clears the global failure streak (gnf/glf).
	ResetHealth bool
	// ResetFailures clears the consecutive-failure streak (nfail, lastfail) of
	// existing health entries and the global failure streak (gnf/glf).
	// Implied by ResetHealth.
	ResetFailures bool
}

// Syncer materializes and reads the Redis hot state. It is safe for
// concurrent use.
type Syncer struct {
	pool   *pgxpool.Pool
	q      *hotstatedb.Queries
	rdb    rueidis.Client
	keys   redis.Keys
	cat    catalog.Catalog
	logger *slog.Logger

	syncIdentities  *redis.Script
	syncAccounts    *redis.Script
	syncProxies     *redis.Script
	syncMaintenance *redis.Script

	// snapshotCursor is the index of the site the snapshot job resumes with.
	snapshotCursor atomic.Int64

	// now and jitter are replaceable in tests.
	now    func() time.Time
	jitter func() int64
}

// NewSyncer creates a hot-state synchronizer.
func NewSyncer(pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cat catalog.Catalog, logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		pool:            pool,
		q:               hotstatedb.New(pool),
		rdb:             rdb,
		keys:            keys,
		cat:             cat,
		logger:          logger.With(slog.String("component", "hotstate")),
		syncIdentities:  redis.NewScript("hotstate_sync_identities", syncIdentitiesLua),
		syncAccounts:    redis.NewScript("hotstate_sync_accounts", syncAccountsLua),
		syncProxies:     redis.NewScript("hotstate_sync_proxies", syncProxiesLua),
		syncMaintenance: redis.NewScript("hotstate_sync_maintenance", syncMaintenanceLua),
		now:             time.Now,
		// Cold-start jitter only spreads load; it needs no cryptographic randomness.
		jitter: func() int64 { return rand.Int64N(coldStartJitter.Milliseconds() + 1) }, //nolint:gosec // G404: non-security jitter
	}
}

// siteSnapshot returns the catalog snapshot of a site, reloading its
// namespace when the local snapshot does not know the site yet. It returns an
// apperr NotFound error when the site does not exist in PostgreSQL.
func (s *Syncer) siteSnapshot(ctx context.Context, siteID string) (*catalog.Site, *catalog.Namespace, error) {
	if site, ns, ok := s.cat.Site(siteID); ok {
		return site, ns, nil
	}
	ref, err := s.q.HotstateSiteRef(ctx, siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, apperr.NotFound("site %s not found", siteID)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load site %s: %w", siteID, err)
	}
	if err := s.cat.Reload(ctx, ref.NamespaceID); err != nil {
		return nil, nil, fmt.Errorf("reload catalog namespace %s: %w", ref.NamespaceID, err)
	}
	if site, ns, ok := s.cat.Site(siteID); ok {
		return site, ns, nil
	}
	return nil, nil, fmt.Errorf("site %s is missing from the catalog snapshot of namespace %s", siteID, ref.NamespaceID)
}

// keyPrefix returns the effective Redis key prefix.
func (s *Syncer) keyPrefix() string {
	if s.keys.Prefix == "" {
		return redis.DefaultPrefix
	}
	return s.keys.Prefix
}

// runScripts executes script calls in pipelines of callsPerPipeline and hands
// every reply to handle. Calls in one pipeline must touch disjoint state or be
// order independent (see redis.Script.ExecMulti).
func (s *Syncer) runScripts(ctx context.Context, script *redis.Script, calls []rueidis.LuaExec, handle func(rueidis.RedisResult) error) error {
	for start := 0; start < len(calls); start += callsPerPipeline {
		end := min(start+callsPerPipeline, len(calls))
		results := script.ExecMulti(ctx, s.rdb, calls[start:end]...)
		for _, res := range results {
			if err := res.Error(); err != nil {
				return fmt.Errorf("run script %s: %w", script.Name, err)
			}
			if handle != nil {
				if err := handle(res); err != nil {
					return fmt.Errorf("decode script %s reply: %w", script.Name, err)
				}
			}
		}
	}
	return nil
}

// doMulti sends commands in pipelines of at most commandsPerPipeline and
// returns the results in command order. Bounded pipelines keep the time Redis
// spends on one burst small.
func (s *Syncer) doMulti(ctx context.Context, cmds rueidis.Commands) []rueidis.RedisResult {
	if len(cmds) <= commandsPerPipeline {
		return s.rdb.DoMulti(ctx, cmds...)
	}
	out := make([]rueidis.RedisResult, 0, len(cmds))
	for start := 0; start < len(cmds); start += commandsPerPipeline {
		end := min(start+commandsPerPipeline, len(cmds))
		out = append(out, s.rdb.DoMulti(ctx, cmds[start:end]...)...)
	}
	return out
}

// doCommands sends commands with doMulti and returns the first error other
// than a nil reply.
func (s *Syncer) doCommands(ctx context.Context, cmds rueidis.Commands) error {
	for _, res := range s.doMulti(ctx, cmds) {
		if err := res.Error(); err != nil && !rueidis.IsRedisNil(err) {
			return err
		}
	}
	return nil
}
