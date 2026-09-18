package server

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/observability"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/scheduler"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

// fakeExecer records upsert batches and fails the first failures calls.
type fakeExecer struct {
	mu       sync.Mutex
	batches  [][]any
	failures int
}

func (f *fakeExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sql != upsertProxyBindingsSQL {
		return pgconn.CommandTag{}, errors.New("unexpected statement")
	}
	if f.failures > 0 {
		f.failures--
		return pgconn.CommandTag{}, errors.New("connection reset")
	}
	f.batches = append(f.batches, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeExecer) snapshot() [][]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]any(nil), f.batches...)
}

func TestBindingArgsCoalesceSortAndNormalize(t *testing.T) {
	at := time.Date(2026, 9, 17, 23, 30, 0, 0, time.FixedZone("x", 3600))
	pending := map[string]scheduler.ProxyBinding{}
	coalesceBinding(pending, scheduler.ProxyBinding{IdentityID: "idt_b", ProxyID: "pxy_old", At: at, RebindDay: "20260917", RebindsToday: 1})
	coalesceBinding(pending, scheduler.ProxyBinding{IdentityID: "idt_b", ProxyID: "pxy_new", At: at.Add(time.Second), RebindDay: "20260917", RebindsToday: 2})
	coalesceBinding(pending, scheduler.ProxyBinding{IdentityID: "idt_b", ProxyID: "pxy_stale", At: at.Add(-time.Hour)})
	coalesceBinding(pending, scheduler.ProxyBinding{IdentityID: "idt_a", ProxyID: "pxy_1", At: at, RebindDay: "bogus", RebindsToday: -4})
	require.Len(t, pending, 2)

	args := bindingArgs(pending)
	require.Equal(t, []string{"idt_a", "idt_b"}, args[0])
	require.Equal(t, []string{"pxy_1", "pxy_new"}, args[1])
	require.Equal(t, []time.Time{at.UTC(), at.Add(time.Second).UTC()}, args[2])
	require.Equal(t, []string{"2026-09-17", "2026-09-17"}, args[3], "malformed days fall back to the UTC day of the binding")
	require.Equal(t, []int32{0, 2}, args[4], "negative counters are clamped")
}

func TestProxyBindingWriterFlushesRetriesAndDrains(t *testing.T) {
	db := &fakeExecer{failures: 1}
	m := observability.NewMetrics()
	w := newProxyBindingWriter(db, m, discardLogger())
	w.flushEvery = 10 * time.Millisecond
	w.retryDelay = time.Millisecond

	w.Record(scheduler.ProxyBinding{IdentityID: "", ProxyID: "pxy_1"}) // ignored
	w.Record(scheduler.ProxyBinding{IdentityID: "idt_1", ProxyID: "pxy_1", At: time.Now()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	require.Eventually(t, func() bool { return len(db.snapshot()) == 1 }, 5*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 1, w.written.Load())
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, 1.0, counterValue(t, m.DBWriteBatches, "retry"))

	// Bindings still queued at shutdown are written by the final drain.
	drainDB := &fakeExecer{}
	d := newProxyBindingWriter(drainDB, nil, discardLogger())
	d.flushEvery = time.Hour
	for _, id := range []string{"idt_1", "idt_2", "idt_1"} {
		d.Record(scheduler.ProxyBinding{IdentityID: id, ProxyID: "pxy_1", At: time.Now()})
	}
	stopped, stop := context.WithCancel(context.Background())
	stop()
	require.NoError(t, d.Run(stopped))
	batches := drainDB.snapshot()
	require.Len(t, batches, 1)
	require.Equal(t, []string{"idt_1", "idt_2"}, batches[0][0])
	require.EqualValues(t, 2, d.written.Load())
}

func TestProxyBindingWriterDropsWhenFull(t *testing.T) {
	db := &fakeExecer{failures: 10}
	w := newProxyBindingWriter(db, nil, discardLogger())
	w.queue = make(chan scheduler.ProxyBinding, 1)
	w.retryDelay = time.Millisecond
	w.Record(scheduler.ProxyBinding{IdentityID: "idt_1", ProxyID: "pxy_1"})
	w.Record(scheduler.ProxyBinding{IdentityID: "idt_2", ProxyID: "pxy_1"})
	require.EqualValues(t, 1, w.dropped.Load())

	// A batch that keeps failing is dropped after the retries.
	pending := map[string]scheduler.ProxyBinding{"idt_1": {IdentityID: "idt_1", ProxyID: "pxy_1"}}
	w.flush(context.Background(), pending)
	require.Empty(t, pending)
	require.EqualValues(t, 2, w.dropped.Load())
	require.Empty(t, db.snapshot())
}

func TestProxyBindingUpsertAgainstPostgres(t *testing.T) {
	pool := testutil.Postgres(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		require.NoError(t, err)
	}
	tenant, ns, site, typ := idgen.New(idgen.Tenant), idgen.New(idgen.Namespace), idgen.New(idgen.Site), idgen.New(idgen.IdentityType)
	exec(`INSERT INTO tenants (id, name) VALUES ($1, 'bind-test')`, tenant)
	exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod')`, ns, tenant)
	exec(`INSERT INTO sites (id, namespace_id, name) VALUES ($1, $2, 'shop')`, site, ns)
	exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, 'web', 'cookie')`, typ, site)
	identity := idgen.New(idgen.Identity)
	exec(`INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ($1, $2, 'web', $3, $4, $5)`,
		identity, site, typ, randomBytes(t), randomBytes(t))
	proxies := []string{idgen.New(idgen.Proxy), idgen.New(idgen.Proxy)}
	for i, id := range proxies {
		exec(`INSERT INTO proxies (id, namespace_id, scheme, host, port, display_url, url_hash, url_ciphertext, url_wrapped_dek, url_kek_id)
		      VALUES ($1, $2, 'http', 'proxy.local', $3, 'http://proxy.local', $4, $5, $6, 'k1')`,
			id, ns, 8080+i, randomBytes(t), randomBytes(t), randomBytes(t))
	}

	w := newProxyBindingWriter(pool, nil, discardLogger())
	at := time.Now().UTC().Truncate(time.Millisecond)
	w.flush(ctx, map[string]scheduler.ProxyBinding{
		identity:      {IdentityID: identity, ProxyID: proxies[0], At: at, RebindDay: at.Format("20060102"), RebindsToday: 1},
		"idt_missing": {IdentityID: "idt_missing", ProxyID: proxies[0], At: at},
	})
	require.Zero(t, w.dropped.Load(), "rows of deleted identities are skipped, not failed")

	type row struct {
		proxy   string
		boundAt time.Time
		rebinds int32
	}
	read := func() row {
		var r row
		require.NoError(t, pool.QueryRow(ctx, `SELECT proxy_id, bound_at, rebinds_today FROM proxy_bindings WHERE identity_id = $1`, identity).
			Scan(&r.proxy, &r.boundAt, &r.rebinds))
		return r
	}
	require.Equal(t, row{proxies[0], at, 1}, row{read().proxy, read().boundAt.UTC(), read().rebinds})

	// A newer rebind replaces the binding; an older one does not.
	w.flush(ctx, map[string]scheduler.ProxyBinding{identity: {IdentityID: identity, ProxyID: proxies[1], At: at.Add(time.Minute), RebindsToday: 2}})
	require.Equal(t, proxies[1], read().proxy)
	w.flush(ctx, map[string]scheduler.ProxyBinding{identity: {IdentityID: identity, ProxyID: proxies[0], At: at.Add(-time.Minute), RebindsToday: 3}})
	got := read()
	require.Equal(t, proxies[1], got.proxy)
	require.EqualValues(t, 2, got.rebinds)
	require.Zero(t, w.dropped.Load())
}

func randomBytes(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}
