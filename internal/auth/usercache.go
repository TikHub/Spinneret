package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/TikHub/Spinneret/internal/auth/authdb"
	"github.com/TikHub/Spinneret/internal/authz"
)

const (
	userCacheSize   = 10_000
	tenantCacheSize = 10_000
)

// userRecord is the authentication view of a user. It is immutable.
type userRecord struct {
	id            string
	username      string
	disabled      bool
	platformAdmin bool
	// credential is the password generation (credentialOf) sessions must match.
	credential string
	bindings   []authz.Binding
}

// errUserUnknown reports a user ID that does not exist.
var errUserUnknown = errors.New("auth: unknown user")

// loadUserRecord reads a user and its role bindings from PostgreSQL.
func loadUserRecord(ctx context.Context, q *authdb.Queries, userID string) (*userRecord, error) {
	u, err := q.AuthUserByID(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errUserUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	rows, err := q.AuthBindingsOfUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load role bindings: %w", err)
	}
	rec := &userRecord{
		id:            u.ID,
		username:      u.Username,
		disabled:      u.Disabled,
		platformAdmin: u.IsPlatformAdmin,
		credential:    credentialOf(u.PasswordChangedAt),
		bindings:      make([]authz.Binding, 0, len(rows)),
	}
	for _, row := range rows {
		rec.bindings = append(rec.bindings, bindingFromRow(row))
	}
	return rec, nil
}

// bindingFromRow converts a stored role binding into its authz form.
func bindingFromRow(row authdb.RoleBinding) authz.Binding {
	b := authz.Binding{
		ID:       row.ID,
		TenantID: row.TenantID,
		Role:     authz.Role(row.Role),
		SiteIDs:  row.SiteIds,
		Extra:    make([]authz.Permission, 0, len(row.ExtraPermissions)),
	}
	if row.NamespaceID != nil {
		b.NamespaceID = *row.NamespaceID
	}
	for _, p := range row.ExtraPermissions {
		b.Extra = append(b.Extra, authz.Permission(p))
	}
	return b
}

// principal builds a user principal for the active tenant.
func (u *userRecord) principal(tenantID string, meta RequestMeta) *authz.Principal {
	return &authz.Principal{
		Kind:            authz.KindUser,
		ID:              u.id,
		Name:            u.username,
		TenantID:        tenantID,
		IsPlatformAdmin: u.platformAdmin,
		Bindings:        u.bindings,
		ClientIP:        meta.ClientIP,
		UserAgent:       meta.UserAgent,
		Node:            meta.Node,
	}
}

// userCache caches users with their bindings and tenant existence for a short
// time (userCacheTTL), so session authentication does not hit PostgreSQL on
// every request.
type userCache struct {
	q          *authdb.Queries
	users      *expirable.LRU[string, *userRecord]
	tenants    *expirable.LRU[string, bool]
	group      singleflight.Group
	generation atomic.Uint64
	// mu makes "compare generation, then add" atomic with respect to
	// invalidations.
	mu sync.Mutex
}

func newUserCache(q *authdb.Queries) *userCache {
	return &userCache{
		q:       q,
		users:   expirable.NewLRU[string, *userRecord](userCacheSize, nil, userCacheTTL),
		tenants: expirable.NewLRU[string, bool](tenantCacheSize, nil, userCacheTTL),
	}
}

// user returns the cached or freshly loaded user record.
func (c *userCache) user(ctx context.Context, userID string) (*userRecord, error) {
	if rec, ok := c.users.Get(userID); ok {
		return rec, nil
	}
	v, err, _ := c.group.Do("u:"+userID, func() (any, error) {
		gen := c.generation.Load()
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
		defer cancel()
		rec, err := loadUserRecord(qctx, c.q, userID)
		if err != nil {
			return nil, err
		}
		c.addIfCurrent(gen, func() { c.users.Add(userID, rec) })
		return rec, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*userRecord), nil
}

// tenantExists reports whether a tenant exists.
func (c *userCache) tenantExists(ctx context.Context, tenantID string) (bool, error) {
	if ok, hit := c.tenants.Get(tenantID); hit {
		return ok, nil
	}
	v, err, _ := c.group.Do("t:"+tenantID, func() (any, error) {
		gen := c.generation.Load()
		qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dbTimeout)
		defer cancel()
		ok, err := c.q.AuthTenantExists(qctx, tenantID)
		if err != nil {
			return false, fmt.Errorf("check tenant: %w", err)
		}
		c.addIfCurrent(gen, func() { c.tenants.Add(tenantID, ok) })
		return ok, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

// addIfCurrent runs add unless an invalidation happened since gen was read.
func (c *userCache) addIfCurrent(gen uint64, add func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation.Load() == gen {
		add()
	}
}

// dropUser removes a cached user.
func (c *userCache) dropUser(userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation.Add(1)
	c.users.Remove(userID)
}

// purge empties the caches.
func (c *userCache) purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation.Add(1)
	c.users.Purge()
	c.tenants.Purge()
}
