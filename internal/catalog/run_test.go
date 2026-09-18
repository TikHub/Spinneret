package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/site"
	"github.com/TikHub/Spinneret/internal/testutil"
)

func newClosedPool(t *testing.T, pool *pgxpool.Pool) (*pgxpool.Pool, error) {
	t.Helper()
	closed, err := pgxpool.New(context.Background(), pool.Config().ConnString())
	if err != nil {
		return nil, err
	}
	closed.Close()
	return closed, nil
}

func startRun(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop")
		}
	})
}

func TestStoreRunAppliesPeerInvalidations(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	tenantID := sd.tenant("acme")
	nsID := sd.namespace(tenantID, "prod")
	bus := events.NewMemoryBus()

	local := NewStore(pool, bus, nil)
	peer := NewStore(pool, bus, nil)
	peer.debounce = 10 * time.Millisecond
	peer.fullInterval = time.Hour
	startRun(t, peer)

	// Run performs the initial full load.
	require.Eventually(t, func() bool { _, ok := peer.Namespace(nsID); return ok }, 5*time.Second, 10*time.Millisecond)

	changed := make(chan string, 16)
	peer.OnChange(func(id string) { changed <- id })

	siteID, _ := sd.site(nsID, "shop", "web")
	sd.group(siteID, "web", site.DefaultGroup)
	require.NoError(t, local.Invalidate(context.Background(), nsID))
	_, _, ok := local.Site(siteID)
	require.True(t, ok, "Invalidate reloads locally before returning")

	require.Eventually(t, func() bool { _, _, ok := peer.Site(siteID); return ok }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, nsID, <-changed)

	// Events without origin and with the namespace only in the envelope work too.
	sd.group(siteID, "web", "search")
	require.NoError(t, bus.Publish(context.Background(), events.ChannelCatalog, events.Event{Type: InvalidateEventType, NamespaceID: nsID}))
	require.Eventually(t, func() bool {
		s, _, ok := peer.Site(siteID)
		return ok && len(s.GroupsByID) == 2
	}, 5*time.Second, 10*time.Millisecond)
}

func TestStoreRunPeriodicFullReload(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	tenantID := sd.tenant("acme")
	store := NewStore(pool, nil, nil)
	store.fullInterval = 20 * time.Millisecond
	startRun(t, store)

	nsID := sd.namespace(tenantID, "late")
	require.Eventually(t, func() bool { _, ok := store.NamespaceByName(tenantID, "late"); return ok }, 5*time.Second, 10*time.Millisecond)
	sd.deleteNamespace(nsID)
	require.Eventually(t, func() bool { _, ok := store.Namespace(nsID); return !ok }, 5*time.Second, 10*time.Millisecond)
}

func TestStoreParseInvalidation(t *testing.T) {
	store := NewStore(nil, nil, nil)
	own, err := json.Marshal(invalidation{NS: "ns_1", Origin: store.origin})
	require.NoError(t, err)
	peer, err := json.Marshal(invalidation{NS: "ns_2", Origin: "other"})
	require.NoError(t, err)
	tests := []struct {
		name string
		ev   events.Event
		want string
		ok   bool
	}{
		{"own origin ignored", events.Event{Data: own}, "", false},
		{"peer", events.Event{Data: peer}, "ns_2", true},
		{"namespace from envelope", events.Event{NamespaceID: "ns_3"}, "ns_3", true},
		{"malformed", events.Event{Data: json.RawMessage(`[1]`)}, "", false},
		{"empty", events.Event{}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := store.parseInvalidation(tc.ev)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestStoreListenerPanicIsRecovered(t *testing.T) {
	store := NewStore(nil, nil, nil)
	var calls atomic.Int32
	store.OnChange(func(string) { panic("boom") })
	store.OnChange(func(string) { calls.Add(1) })
	require.NotPanics(t, func() { store.notify("ns_1") })
	require.Equal(t, int32(1), calls.Load())
}

// TestStoreConcurrentInvalidations checks that every Invalidate observes the
// change committed before it was called, even while other goroutines reload
// the same namespace and readers use the snapshots.
func TestStoreConcurrentInvalidations(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	tenantID := sd.tenant("acme")
	nsID := sd.namespace(tenantID, "prod")
	siteID, _ := sd.site(nsID, "shop", "web")
	sd.group(siteID, "web", site.DefaultGroup)

	bus := events.NewMemoryBus()
	store := NewStore(pool, bus, nil)
	store.debounce = time.Millisecond
	startRun(t, store)
	ctx := context.Background()
	require.NoError(t, store.ReloadAll(ctx))

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, ns := range store.Namespaces(tenantID) {
					for _, s := range ns.SitesByID {
						_, _, _ = s.MatchGroup("web", "/a/b")
						_ = len(s.GroupsByID)
					}
				}
				_, _, _ = store.Site(siteID)
			}
		}()
	}

	const writers, perWriter = 6, 5
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				groupID, _ := sd.group(siteID, "web", fmt.Sprintf("g-%d-%d", w, i))
				if err := store.Invalidate(ctx, nsID); err != nil {
					errs <- err
					return
				}
				s, _, ok := store.Site(siteID)
				if !ok {
					errs <- fmt.Errorf("site missing after invalidate")
					return
				}
				if _, ok := s.GroupsByID[groupID]; !ok {
					errs <- fmt.Errorf("group %s missing after invalidate", groupID)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	s, _, ok := store.Site(siteID)
	require.True(t, ok)
	require.Len(t, s.GroupsByID, writers*perWriter+1)
}

// TestStoreRunRetriesFailedInitialLoad checks that Run retries a failed first
// full load with backoff instead of waiting for the periodic reload.
func TestStoreRunRetriesFailedInitialLoad(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	nsID := sd.namespace(sd.tenant("acme"), "prod")
	sd.exec(`ALTER TABLE namespaces RENAME TO namespaces_hidden`)

	store := NewStore(pool, nil, nil)
	store.fullInterval = time.Hour
	store.retryBackoff = 20 * time.Millisecond
	startRun(t, store)

	time.Sleep(100 * time.Millisecond)
	require.False(t, store.Loaded(), "loads fail while the table is missing")
	sd.exec(`ALTER TABLE namespaces_hidden RENAME TO namespaces`)
	require.Eventually(t, store.Loaded, 5*time.Second, 10*time.Millisecond)
	_, ok := store.Namespace(nsID)
	require.True(t, ok)
}

// TestStoreLoadTimeoutExcludesQueueing checks that the load timeout bounds the
// database work only, not the wait for a load slot.
func TestStoreLoadTimeoutExcludesQueueing(t *testing.T) {
	pool := testutil.Postgres(t)
	sd := newSeeder(t, pool)
	nsID := sd.namespace(sd.tenant("acme"), "prod")

	store := NewStore(pool, nil, nil)
	store.loadTimeout = time.Second
	for i := 0; i < cap(store.loadSem); i++ {
		store.loadSem <- struct{}{}
	}
	done := make(chan error, 1)
	go func() { done <- store.Reload(context.Background(), nsID) }()
	time.Sleep(1500 * time.Millisecond)
	for i := 0; i < cap(store.loadSem); i++ {
		<-store.loadSem
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not finish")
	}
	_, ok := store.Namespace(nsID)
	require.True(t, ok)
}

func TestInitialRetryBackoff(t *testing.T) {
	store := NewStore(nil, nil, nil)
	require.Equal(t, DefaultRetryBackoff, store.initialRetryBackoff())
	store.retryBackoff = 0
	require.Equal(t, DefaultRetryBackoff, store.initialRetryBackoff())
	store.retryBackoff = time.Millisecond
	require.Equal(t, time.Millisecond, store.initialRetryBackoff())
}
