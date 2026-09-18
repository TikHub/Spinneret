package sitesvc

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/pkg/idgen"
	"github.com/TikHub/Spinneret/internal/store/redis"
	"github.com/TikHub/Spinneret/internal/testutil"
)

// fakeHot records hot-state calls and optionally fails them.
type fakeHot struct {
	mu      sync.Mutex
	synced  []string
	removed []int64
	err     error
}

func (f *fakeHot) SyncSite(_ context.Context, siteID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synced = append(f.synced, siteID)
	return f.err
}

func (f *fakeHot) RemoveSite(_ context.Context, siteKey int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, siteKey)
	return f.err
}

func (f *fakeHot) syncedSites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.synced...)
}

func (f *fakeHot) removedKeys() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.removed...)
}

// memAudit keeps audit entries in memory.
type memAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (m *memAudit) Record(_ context.Context, e audit.Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

func (m *memAudit) last() audit.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[len(m.entries)-1]
}

// failingCatalog wraps a catalog and fails Invalidate.
type failingCatalog struct {
	catalog.Catalog
}

func (failingCatalog) Invalidate(context.Context, string) error {
	return errors.New("invalidate failed")
}

type env struct {
	t        *testing.T
	pool     *pgxpool.Pool
	rdb      rueidis.Client
	keys     redis.Keys
	cat      *catalog.Store
	hot      *fakeHot
	audit    *memAudit
	svc      *Service
	tenantID string
	ns       *catalog.Namespace
	actor    *authz.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	e := &env{t: t, pool: pool, rdb: rdb, keys: keys, hot: &fakeHot{}, audit: &memAudit{}}
	e.cat = catalog.NewStore(pool, nil, nil)
	e.svc = NewService(pool, e.cat, e.hot, e.audit, rdb, keys, nil)
	e.tenantID = idgen.New(idgen.Tenant)
	e.exec(`INSERT INTO tenants (id, name) VALUES ($1, 'acme')`, e.tenantID)
	nsID := idgen.New(idgen.Namespace)
	e.exec(`INSERT INTO namespaces (id, tenant_id, name) VALUES ($1, $2, 'prod')`, nsID, e.tenantID)
	require.NoError(t, e.cat.ReloadAll(context.Background()))
	ns, ok := e.cat.Namespace(nsID)
	require.True(t, ok)
	e.ns = ns
	e.actor = &authz.Principal{Kind: authz.KindUser, ID: "usr_1", Name: "alice", TenantID: e.tenantID, IsPlatformAdmin: true}
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	_, err := e.pool.Exec(context.Background(), sql, args...)
	require.NoError(e.t, err)
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e *env) createSite(name string, clients ...string) Site {
	e.t.Helper()
	s, err := e.svc.CreateSite(context.Background(), e.actor, e.ns, CreateSiteInput{Name: name, Clients: clients})
	require.NoError(e.t, err)
	return s
}

// addIdentity inserts an identity type (if typeID is empty) and one identity
// of a client, returning the type ID.
func (e *env) addIdentity(siteID, client, typeID string) string {
	e.t.Helper()
	if typeID == "" {
		typeID = idgen.New(idgen.IdentityType)
		e.exec(`INSERT INTO identity_types (id, site_id, client, name) VALUES ($1, $2, $3, $4)`,
			typeID, siteID, client, "type_"+typeID[len(typeID)-8:])
	}
	id := idgen.New(idgen.Identity)
	e.exec(`INSERT INTO identities (id, site_id, client, type_id, unique_hash, payload_hash) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, siteID, client, typeID, []byte(id), []byte("p"))
	return typeID
}

func requireCode(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.As(err)
	require.True(t, ok, "expected an apperr error, got %v", err)
	require.Equal(t, reason, ae.Reason, ae.Error())
}
