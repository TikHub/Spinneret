package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/auth/authdb"
	"github.com/Evil0ctal/Spinneret/internal/auth/authtest"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/testutil"
)

const testPassword = "correct-horse-battery"

// env wires the auth services against real PostgreSQL and Redis.
type env struct {
	t        *testing.T
	pool     *pgxpool.Pool
	rdb      rueidis.Client
	keys     redis.Keys
	bus      events.Bus
	rec      *authtest.Recorder
	cfg      Config
	auth     *Authenticator
	users    *Users
	tokens   *Tokens
	auditLog *AuditLogs
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testutil.Postgres(t)
	rdb, keys := testutil.Redis(t)
	e := &env{
		t:    t,
		pool: pool,
		rdb:  rdb,
		keys: keys,
		bus:  events.NewMemoryBus(),
		rec:  &authtest.Recorder{},
		cfg: Config{
			TokenCacheTTL:  time.Minute,
			SessionTTL:     time.Hour,
			TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
			Argon2:         fastArgon2,
		},
	}
	logger := slog.New(slog.DiscardHandler)
	e.auth = NewAuthenticator(e.cfg, pool, rdb, keys, e.bus, logger)
	e.users = NewUsers(pool, rdb, keys, e.rec, e.cfg, logger, WithEventBus(e.bus))
	e.tokens = NewTokens(pool, e.bus, e.rec, logger)
	e.auditLog = NewAuditLogs(pool)
	return e
}

// runAuthenticator starts Run until the test ends.
func (e *env) runAuthenticator() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(e.t, e.auth.Run(ctx))
	}()
	e.t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-e.auth.started:
	case <-time.After(5 * time.Second):
		e.t.Fatal("authenticator did not start")
	}
}

func (e *env) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	e.t.Cleanup(cancel)
	return ctx
}

func (e *env) hash(password string) string {
	h, err := HashPasswordWithParams(password, fastArgon2)
	require.NoError(e.t, err)
	return h
}

// world is a tenant with one namespace, two sites and a set of users.
type world struct {
	tenant, ns, nsName, siteA, siteB string
	owner, viewer, platformAdmin     string
	ownerBinding, viewerBinding      authz.Binding
}

func (e *env) world(tenantName string) world {
	t := e.t
	w := world{nsName: "prod"}
	w.tenant = authtest.Tenant(t, e.pool, tenantName)
	w.ns = authtest.Namespace(t, e.pool, w.tenant, w.nsName)
	w.siteA = authtest.Site(t, e.pool, w.ns, "shop")
	w.siteB = authtest.Site(t, e.pool, w.ns, "market")
	w.owner = authtest.User(t, e.pool, tenantName+"-owner", e.hash(testPassword), false)
	w.ownerBinding = authtest.Binding(t, e.pool, w.owner, w.tenant, authz.RoleOwner, "", nil)
	w.viewer = authtest.User(t, e.pool, tenantName+"-viewer", e.hash(testPassword), false)
	w.viewerBinding = authtest.Binding(t, e.pool, w.viewer, w.tenant, authz.RoleViewer, "", nil)
	w.platformAdmin = authtest.User(t, e.pool, tenantName+"-root", e.hash(testPassword), true)
	return w
}

func (w world) ownerP() *authz.Principal {
	return authtest.UserPrincipal(w.owner, w.tenant, w.ownerBinding)
}

func (w world) viewerP() *authz.Principal {
	return authtest.UserPrincipal(w.viewer, w.tenant, w.viewerBinding)
}

func (w world) nsRef() NamespaceRef {
	return NamespaceRef{ID: w.ns, TenantID: w.tenant, Name: w.nsName}
}

// request builds an HTTP request with optional bearer token and cookie.
func request(method, bearer, cookie string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(method, "/spinneret.v1.X/Y", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func requireReason(t *testing.T, err error, reason apperr.Reason) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, apperr.ReasonOf(err), "error: %v", err)
}

func (e *env) newToken(w world, name string, in CreateTokenInput) (string, string) {
	e.t.Helper()
	in.Name = name
	if in.Scopes == nil {
		in.Scopes = []string{"lease:acquire", "report:write"}
	}
	prep, err := prepareToken(in, time.Now())
	require.NoError(e.t, err)
	view, plaintext, err := insertToken(e.ctx(), authdb.New(e.pool), w.nsRef(), in, prep, "test")
	require.NoError(e.t, err)
	return plaintext, view.ID
}

func redisKeysForUnit() redis.Keys { return redis.NewKeys("unit") }
