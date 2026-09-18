package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/events"
)

// Store tuning defaults.
const (
	// DefaultDebounce delays reloads triggered by peer invalidations so that
	// bursts of changes to one namespace cause a single rebuild.
	DefaultDebounce = 100 * time.Millisecond
	// DefaultFullReloadInterval is the period of the safety-net full reload.
	DefaultFullReloadInterval = 60 * time.Second
	// DefaultLoadTimeout bounds the database work of one namespace reload.
	DefaultLoadTimeout = 30 * time.Second
	// DefaultRetryBackoff is the first delay before Run retries a failed full
	// load while nothing was loaded yet; it doubles up to the full reload
	// interval.
	DefaultRetryBackoff = time.Second
	// maxConcurrentLoads bounds concurrent namespace loads (database
	// connections and CPU used for compiling snapshots).
	maxConcurrentLoads = 4
	// eventQueueSize bounds invalidations received but not yet scheduled;
	// on overflow a full reload is scheduled instead.
	eventQueueSize = 1024
	// maxJoinedErrors bounds the errors reported by ReloadAll.
	maxJoinedErrors = 8
)

// InvalidateEventType is the event type published on events.ChannelCatalog.
const InvalidateEventType = "invalidate"

// invalidation is the data of a catalog channel event. Origin identifies the
// publishing Store so that it can ignore its own events, which the bus
// delivers locally as well.
type invalidation struct {
	NS     string `json:"ns"`
	Origin string `json:"origin,omitempty"`
}

// Store is the PostgreSQL-backed implementation of Catalog. Snapshots are
// immutable and published through an atomically swapped index, so reads are
// lock-free. Reloads of one namespace are serialized and coalesced: a caller
// always waits for a load that started after its request, so it observes every
// change committed before it called Reload or Invalidate.
type Store struct {
	pool   *pgxpool.Pool
	bus    events.Bus
	logger *slog.Logger
	origin string

	debounce     time.Duration
	fullInterval time.Duration
	loadTimeout  time.Duration
	retryBackoff time.Duration
	now          func() time.Time

	idx     atomic.Pointer[index]
	version atomic.Uint64
	loaded  atomic.Bool // a full reload succeeded at least once

	// writeMu serializes index swaps and guards fingerprints, applied and
	// missingMarks.
	writeMu      sync.Mutex
	fingerprints map[string]uint64

	// marks are the shared change marks (nil = disabled, see SetChangeMarks);
	// applied is the mark observed by the last completed load per namespace;
	// missingMarks are marked namespaces absent from the last full reload.
	marks        ChangeMarks
	applied      map[string]int64
	missingMarks map[string]struct{}

	flightMu sync.Mutex
	flights  map[string]*nsFlights
	loadSem  chan struct{}

	listenerMu   sync.RWMutex
	listeners    map[uint64]func(string)
	nextListener uint64
}

var _ Catalog = (*Store)(nil)

// NewStore creates an empty catalog store. Call ReloadAll (or Run, which loads
// everything when nothing was loaded yet) before serving requests. bus may be
// nil, in which case Invalidate only reloads locally and Run relies on the
// periodic full reload. A nil logger discards logs.
func NewStore(pool *pgxpool.Pool, bus events.Bus, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Store{
		pool:         pool,
		bus:          bus,
		logger:       logger.With(slog.String("component", "catalog")),
		origin:       randomOrigin(),
		debounce:     DefaultDebounce,
		fullInterval: DefaultFullReloadInterval,
		loadTimeout:  DefaultLoadTimeout,
		retryBackoff: DefaultRetryBackoff,
		now:          time.Now,
		fingerprints: map[string]uint64{},
		applied:      map[string]int64{},
		missingMarks: map[string]struct{}{},
		flights:      map[string]*nsFlights{},
		loadSem:      make(chan struct{}, maxConcurrentLoads),
		listeners:    map[uint64]func(string){},
	}
	s.idx.Store(emptyIndex())
	return s
}

func randomOrigin() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to time.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) index() *index { return s.idx.Load() }

// Loaded reports whether a full reload has succeeded at least once (for
// readiness checks).
func (s *Store) Loaded() bool { return s.loaded.Load() }

// Namespace implements Catalog.
func (s *Store) Namespace(id string) (*Namespace, bool) {
	ns, ok := s.index().byID[id]
	return ns, ok
}

// NamespaceByName implements Catalog.
func (s *Store) NamespaceByName(tenantID, name string) (*Namespace, bool) {
	ns, ok := s.index().byName[nameKey{tenantID: tenantID, name: name}]
	return ns, ok
}

// Namespaces implements Catalog. The result is ordered by namespace ID and
// owned by the caller.
func (s *Store) Namespaces(tenantID string) []*Namespace {
	sorted := s.index().sorted
	out := make([]*Namespace, 0, len(sorted))
	for _, ns := range sorted {
		if tenantID == "" || ns.TenantID == tenantID {
			out = append(out, ns)
		}
	}
	return out
}

// Site implements Catalog.
func (s *Store) Site(id string) (*Site, *Namespace, bool) {
	e, ok := s.index().sitesByID[id]
	return e.site, e.ns, ok
}

// SiteByKey implements Catalog.
func (s *Store) SiteByKey(key int64) (*Site, *Namespace, bool) {
	e, ok := s.index().sitesByKey[key]
	return e.site, e.ns, ok
}

// Reload implements Catalog. It waits for a load of the namespace that started
// after the call, or until ctx is done (the load itself continues in the
// background, bounded by the load timeout).
func (s *Store) Reload(ctx context.Context, namespaceID string) error {
	if namespaceID == "" {
		return errors.New("catalog: reload: namespace id is empty")
	}
	return s.request(ctx, namespaceID).wait(ctx)
}

// ReloadAll implements Catalog. Namespaces that no longer exist are removed.
// Errors of individual namespaces are joined; namespaces that loaded
// successfully are swapped in regardless.
func (s *Store) ReloadAll(ctx context.Context) error {
	lctx, cancel := context.WithTimeout(ctx, s.loadTimeout)
	// Change marks are read before the listing (see pruneMarks).
	var marks map[string]int64
	if s.marks != nil {
		var merr error
		if marks, merr = s.marks.All(lctx); merr != nil {
			s.logger.Debug("catalog: reading change marks failed", slog.Any("error", merr))
			marks = nil
		}
	}
	ids, err := listNamespaceIDs(lctx, s.pool)
	cancel()
	if err != nil {
		return fmt.Errorf("catalog: reload all: %w", err)
	}
	targets := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		targets[id] = struct{}{}
	}
	present := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		present[id] = struct{}{}
	}
	// Namespaces missing from the listing are reloaded individually rather than
	// dropped, because they may have been created after the listing.
	for id := range s.index().byID {
		targets[id] = struct{}{}
	}
	waiting := make(map[string]*flight, len(targets))
	for id := range targets {
		waiting[id] = s.request(ctx, id)
	}
	var errs []error
	failed := 0
	for id, f := range waiting {
		if err := f.wait(ctx); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("catalog: reload all: %w", ctx.Err())
			}
			failed++
			if len(errs) < maxJoinedErrors {
				errs = append(errs, fmt.Errorf("namespace %s: %w", id, err))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("catalog: reload all: %d of %d namespaces failed: %w", failed, len(targets), errors.Join(errs...))
	}
	s.loaded.Store(true)
	s.pruneMarks(ctx, marks, present)
	return nil
}

// Invalidate implements Catalog: it bumps the namespace's change mark (when
// change marks are enabled), reloads the namespace locally and then publishes
// {"ns": id} on events.ChannelCatalog so that peers reload it too. Bumping and
// publishing are best effort (peers also reload periodically): their failures
// are logged, while a local reload failure is returned.
func (s *Store) Invalidate(ctx context.Context, namespaceID string) error {
	if namespaceID != "" {
		s.bumpMark(ctx, namespaceID)
	}
	err := s.Reload(ctx, namespaceID)
	if s.bus != nil && namespaceID != "" {
		data, merr := json.Marshal(invalidation{NS: namespaceID, Origin: s.origin})
		if merr == nil {
			merr = s.bus.Publish(ctx, events.ChannelCatalog, events.Event{
				Type:        InvalidateEventType,
				NamespaceID: namespaceID,
				Data:        data,
			})
		}
		if merr != nil {
			s.logger.Warn("catalog: publishing invalidation failed; peers catch up on their periodic reload",
				slog.String("namespace_id", namespaceID), slog.Any("error", merr))
		}
	}
	if err != nil {
		return fmt.Errorf("catalog: invalidate namespace %s: %w", namespaceID, err)
	}
	return nil
}

// OnChange implements Catalog. Listeners run synchronously on the goroutine
// that swapped the snapshot and must return quickly.
func (s *Store) OnChange(fn func(namespaceID string)) (unregister func()) {
	if fn == nil {
		return func() {}
	}
	s.listenerMu.Lock()
	s.nextListener++
	id := s.nextListener
	s.listeners[id] = fn
	s.listenerMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.listenerMu.Lock()
			delete(s.listeners, id)
			s.listenerMu.Unlock()
		})
	}
}

func (s *Store) notify(namespaceID string) {
	s.listenerMu.RLock()
	fns := make([]func(string), 0, len(s.listeners))
	for _, fn := range s.listeners {
		fns = append(fns, fn)
	}
	s.listenerMu.RUnlock()
	for _, fn := range fns {
		s.callListener(fn, namespaceID)
	}
}

func (s *Store) callListener(fn func(string), namespaceID string) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("catalog: change listener panicked",
				slog.String("namespace_id", namespaceID), slog.Any("panic", r))
		}
	}()
	fn(namespaceID)
}

// load rebuilds one namespace from PostgreSQL and swaps it into the index. It
// is only called by the flight runner, which serializes loads per namespace.
// The load timeout starts once a load slot is acquired, so loads queued behind
// others (for example during a full reload of many namespaces) do not expire
// while they wait; base itself is detached from request cancellation.
func (s *Store) load(base context.Context, namespaceID string) error {
	select {
	case s.loadSem <- struct{}{}:
	case <-base.Done():
		return base.Err()
	}
	defer func() { <-s.loadSem }()
	ctx, cancel := context.WithTimeout(base, s.loadTimeout)
	defer cancel()

	// The mark is observed before PostgreSQL is read: every change whose mark
	// is at most this value was committed before the read and is included.
	mark, markOK := s.observeMark(ctx, namespaceID)
	raw, err := loadRaw(ctx, s.pool, namespaceID)
	if errors.Is(err, errNamespaceNotFound) {
		s.remove(namespaceID, mark, markOK)
		return nil
	}
	if err != nil {
		return err
	}
	fp, fpErr := raw.fingerprint()
	if fpErr != nil {
		s.logger.Warn("catalog: fingerprinting namespace failed, rebuilding unconditionally",
			slog.String("namespace_id", namespaceID), slog.Any("error", fpErr))
	} else if s.unchanged(namespaceID, fp, mark, markOK) {
		return nil
	}
	ns := (&builder{raw: raw, logger: s.logger}).build(s.version.Add(1), s.now())

	s.writeMu.Lock()
	s.idx.Store(s.index().with(ns))
	if fpErr == nil {
		s.fingerprints[namespaceID] = fp
	} else {
		delete(s.fingerprints, namespaceID)
	}
	s.recordMarkLocked(namespaceID, mark, markOK)
	s.writeMu.Unlock()
	s.notify(namespaceID)
	return nil
}

// unchanged reports whether the loaded snapshot of the namespace was built
// from rows with the same fingerprint. In that case the snapshot is current
// as of this load, so the observed change mark is recorded.
func (s *Store) unchanged(namespaceID string, fp uint64, mark int64, markOK bool) bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, ok := s.index().byID[namespaceID]; !ok {
		return false
	}
	prev, ok := s.fingerprints[namespaceID]
	if ok && prev == fp {
		s.recordMarkLocked(namespaceID, mark, markOK)
		return true
	}
	return false
}

// remove drops a namespace that no longer exists and records the change mark
// observed by the load that found it missing.
func (s *Store) remove(namespaceID string, mark int64, markOK bool) {
	s.writeMu.Lock()
	cur := s.index()
	_, existed := cur.byID[namespaceID]
	if existed {
		s.idx.Store(cur.without(namespaceID))
	}
	delete(s.fingerprints, namespaceID)
	s.recordMarkLocked(namespaceID, mark, markOK)
	s.writeMu.Unlock()
	if existed {
		s.notify(namespaceID)
	}
}
