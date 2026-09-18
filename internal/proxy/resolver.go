package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// Resolver cache parameters.
const (
	ResolverCacheSize = 100_000
	ResolverCacheTTL  = 30 * time.Second
	resolveTimeout    = 5 * time.Second
)

// Assignment is the proxy handed to a node with a lease.
type Assignment struct {
	ID string
	// URL is the full proxy URL including credentials (session template applied).
	URL    string
	Kind   string
	Region string
}

// resolvedProxy is a cached, decrypted proxy.
type resolvedProxy struct {
	namespaceID string
	url         ParsedURL
	kind        string
	region      string
	template    []templateSegment
	// plain is the rendered URL when there is no session template.
	plain string
}

// Resolver turns lease proxy IDs into proxy URLs. Decrypted URLs are cached
// per proxy ID in an expirable LRU (ResolverCacheSize entries, ResolverCacheTTL);
// concurrent misses of one proxy share a single database load. Cache entries
// are dropped on proxy.state events when Subscribe is used; without a
// subscription URL or template changes become visible within ResolverCacheTTL.
type Resolver struct {
	q      *proxydb.Queries
	cipher *vault.Cipher
	logger *slog.Logger
	cache  *expirable.LRU[string, *resolvedProxy]
	group  singleflight.Group
	// mu guards gen together with cache insertions and removals. gen is
	// incremented by every invalidation so that a load which started before an
	// invalidation does not repopulate the cache with stale data.
	mu  sync.Mutex
	gen uint64
}

// NewResolver creates a resolver.
func NewResolver(pool *pgxpool.Pool, cipher *vault.Cipher, logger *slog.Logger) *Resolver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		q:      proxydb.New(pool),
		cipher: cipher,
		logger: logger.With(slog.String("component", "proxy_resolver")),
		cache:  expirable.NewLRU[string, *resolvedProxy](ResolverCacheSize, nil, ResolverCacheTTL),
	}
}

// Resolve returns the assignment of proxyID for a lease: the decrypted URL
// with the session template (if any) rendered into the user name. It returns
// apperr NotFound when the proxy does not exist in namespaceID.
func (r *Resolver) Resolve(ctx context.Context, namespaceID, proxyID, identityID, leaseID string) (*Assignment, error) {
	if proxyID == "" {
		return nil, apperr.InvalidArgument("", "proxy id is required")
	}
	rp, err := r.lookup(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	if rp.namespaceID != namespaceID {
		return nil, apperr.NotFound("proxy not found")
	}
	a := &Assignment{ID: proxyID, Kind: rp.kind, Region: rp.region, URL: rp.plain}
	if rp.template != nil {
		user, err := renderTemplate(rp.template, templateVars{
			username: rp.url.Username, password: rp.url.Password, identityID: identityID, leaseID: leaseID,
		})
		if err != nil {
			return nil, apperr.Internal(err)
		}
		u := rp.url.URL()
		switch {
		case user == "":
			// An empty user name cannot carry credentials: keep the stored URL.
		case rp.url.Password != "":
			u.User = url.UserPassword(user, rp.url.Password)
		default:
			u.User = url.User(user)
		}
		a.URL = u.String()
	}
	return a, nil
}

// lookup returns the cached proxy or loads it once for concurrent callers.
func (r *Resolver) lookup(ctx context.Context, proxyID string) (*resolvedProxy, error) {
	if rp, ok := r.cache.Get(proxyID); ok {
		return rp, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := r.group.DoChan(proxyID, func() (any, error) {
		r.mu.Lock()
		gen := r.gen
		r.mu.Unlock()
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolveTimeout)
		defer cancel()
		rp, err := r.load(lctx, proxyID)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		if r.gen == gen {
			r.cache.Add(proxyID, rp)
		}
		r.mu.Unlock()
		return rp, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*resolvedProxy), nil
	}
}

// load reads and decrypts one proxy.
func (r *Resolver) load(ctx context.Context, proxyID string) (*resolvedProxy, error) {
	row, err := r.q.ProxyResolveLoad(ctx, proxyID)
	if err != nil {
		if isNoRows(err) {
			return nil, apperr.NotFound("proxy not found")
		}
		return nil, fmt.Errorf("load proxy %s: %w", proxyID, err)
	}
	u, err := OpenURL(r.cipher, proxyID, vault.Sealed{Ciphertext: row.UrlCiphertext, WrappedDEK: row.UrlWrappedDek, KEKID: row.UrlKekID})
	if err != nil {
		r.logger.Error("decrypt proxy url failed", slog.String("proxy_id", proxyID), slog.Any("error", err))
		return nil, apperr.Internal(fmt.Errorf("decrypt proxy %s url: %w", proxyID, err))
	}
	rp := &resolvedProxy{namespaceID: row.NamespaceID, url: u, kind: row.Kind, region: row.Region}
	if row.SessionTemplate == "" {
		rp.plain = u.String()
		return rp, nil
	}
	segs, err := parseTemplate(row.SessionTemplate)
	if err != nil {
		return nil, apperr.Internal(fmt.Errorf("proxy %s session template: %w", proxyID, err))
	}
	rp.template = segs
	return rp, nil
}

// Invalidate drops the cached URL of a proxy.
func (r *Resolver) Invalidate(proxyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	r.cache.Remove(proxyID)
}

// Subscribe invalidates cached proxies on proxy.state events of every
// namespace (the bus delivers peer events as well). It returns the
// unsubscribe function.
func (r *Resolver) Subscribe(bus events.Bus) (unsubscribe func()) {
	if bus == nil {
		return func() {}
	}
	return bus.Subscribe(events.ChannelAll, func(_ context.Context, _ string, ev events.Event) {
		if ev.Type != events.TypeProxyState || len(ev.Data) == 0 {
			return
		}
		var data StateEventData
		if err := json.Unmarshal(ev.Data, &data); err != nil || data.SubjectID == "" {
			return
		}
		r.Invalidate(data.SubjectID)
	})
}
