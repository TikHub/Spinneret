package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/rueidis"
	"github.com/spf13/cobra"

	"github.com/Evil0ctal/Spinneret/internal/appconfig"
	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/configcenter"
	"github.com/Evil0ctal/Spinneret/internal/events"
	"github.com/Evil0ctal/Spinneret/internal/hotstate"
	"github.com/Evil0ctal/Spinneret/internal/identitysvc"
	"github.com/Evil0ctal/Spinneret/internal/policy"
	"github.com/Evil0ctal/Spinneret/internal/policysvc"
	"github.com/Evil0ctal/Spinneret/internal/proxy"
	"github.com/Evil0ctal/Spinneret/internal/site"
	"github.com/Evil0ctal/Spinneret/internal/sitesvc"
	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
	"github.com/Evil0ctal/Spinneret/internal/store/redis"
	"github.com/Evil0ctal/Spinneret/internal/tenancy"
	"github.com/Evil0ctal/Spinneret/internal/vault"
)

// auditDrainTimeout bounds the final audit flush of the seed command.
const auditDrainTimeout = 10 * time.Second

func newSeedCmd(a *app) *cobra.Command {
	o := seedOptions{}
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Create load-test and end-to-end test data (idempotent)",
		Long: "Create or complete a load-test site: endpoint groups g0..g{N-1} matching /api/g<i>/,\n" +
			"the identity type " + seedIdentityType + ", synthetic identities, a published rotation policy\n" +
			"(" + seedRotationPolicy + ") and a relaxed breaker policy (" + seedBreakerPolicy + ") bound to the site,\n" +
			"the config item " + seedConfigGroup + "/" + seedConfigKey + ", optional proxies and a node token.\n" +
			"Existing objects are kept; the node token is re-created (the old one is revoked).\n\n" +
			"Prints {\"token\": ..., \"site\": ..., \"groups\": N, \"identities\": N} on stdout.\n" +
			"Requires the full server configuration (database, Redis and KEK settings).",
		Example: "  spnr seed --site loadtest --identities 100000 --groups 50\n" +
			"  spnr seed --site loadtest --proxies 100 --proxy-url 'http://lt-{i}:secret@mocktarget:9091'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.applyDefaults()
			if err := o.validate(); err != nil {
				return err
			}
			cfg, err := a.fullConfig()
			if err != nil {
				return err
			}
			res, err := a.seed(cmd.Context(), cfg, o)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(a.stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.Tenant, "tenant", "default", "tenant (created when missing)")
	f.StringVar(&o.Namespace, "namespace", "default", "namespace (created when missing)")
	f.StringVar(&o.Site, "site", "loadtest", "site name")
	f.StringVar(&o.Client, "client", "web", "client type")
	f.IntVar(&o.Groups, "groups", 50, "number of endpoint groups")
	f.IntVar(&o.Identities, "identities", 100_000, "number of identities")
	f.IntVar(&o.Proxies, "proxies", 0, "number of proxies (0 = no proxies)")
	f.StringVar(&o.ProxyURL, "proxy-url", "http://loadtest-{i}:loadtest@mocktarget:9091", "proxy URL template; {i} is replaced by the proxy index")
	f.StringVar(&o.TokenName, "token-name", "", "name of the node token (default: the site name)")
	f.IntVar(&o.ChunkSize, "chunk-size", seedDefaultChunk, "identities per import call")
	_ = f.MarkHidden("chunk-size")
	return cmd
}

// seeder holds the services used by the seed command.
type seeder struct {
	o       seedOptions
	logger  *slog.Logger
	out     func(format string, args ...any)
	pool    *pgxpool.Pool
	bus     events.Bus
	cat     *catalog.Store
	hot     *hotstate.Syncer
	p       *authz.Principal
	tenancy *tenancy.Service
	sites   *sitesvc.Service
	ids     *identitysvc.Service
	pols    *policysvc.Service
	configs *configcenter.Service
	proxies *proxy.Service
}

// seed runs the whole seeding procedure.
func (a *app) seed(ctx context.Context, cfg appconfig.Config, o seedOptions) (seedResult, error) {
	logger := a.logger()
	pool, err := pgstore.Open(ctx, cfg.DatabaseURL, cfg.DatabaseMaxConns)
	if err != nil {
		return seedResult{}, fmt.Errorf("connect postgresql: %w", err)
	}
	defer pool.Close()
	if err := requireCurrentSchema(ctx, pool); err != nil {
		return seedResult{}, err
	}
	rdb, err := redis.Open(ctx, cfg.RedisURL, cfg.RedisAddrs)
	if err != nil {
		return seedResult{}, fmt.Errorf("connect redis: %w", err)
	}
	defer rdb.Close()
	keys := redis.NewKeys(cfg.RedisPrefix)
	provider, err := vault.LoadLocalKEKProvider(cfg.KEKFile, cfg.KEKs, cfg.KEKCurrent)
	if err != nil {
		return seedResult{}, fmt.Errorf("load key-encryption keys: %w", err)
	}
	cipher := vault.NewCipher(provider, cfg.DEKCacheSize, cfg.DEKCacheTTL)
	pepper, err := vault.SystemKey(ctx, pool, cipher, "dedupe_pepper", 32)
	if err != nil {
		return seedResult{}, fmt.Errorf("load dedupe pepper: %w", err)
	}

	auditW := audit.NewWriter(pool, logger)
	auditCtx, stopAudit := context.WithCancel(context.WithoutCancel(ctx))
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		_ = auditW.Run(auditCtx)
	}()
	defer func() {
		stopAudit()
		select {
		case <-auditDone:
		case <-time.After(auditDrainTimeout):
			logger.Warn("audit entries of the seed were not fully flushed")
		}
	}()

	s := newSeeder(o, logger, pool, rdb, keys, cipher, pepper, auditW, func(format string, args ...any) {
		_, _ = fmt.Fprintf(a.stderr, format+"\n", args...)
	})
	return s.run(ctx)
}

func newSeeder(o seedOptions, logger *slog.Logger, pool *pgxpool.Pool, rdb rueidis.Client, keys redis.Keys, cipher *vault.Cipher,
	pepper []byte, rec audit.Recorder, out func(string, ...any),
) *seeder {
	// Publishing on the Redis bus lets running instances reload what the seed changed.
	bus := events.NewRedisBus(rdb, keys, "", logger)
	cat := catalog.NewStore(pool, bus, logger)
	hot := hotstate.NewSyncer(pool, rdb, keys, cat, logger)
	pols := policysvc.NewService(pool, cat, hot, rec, logger, policysvc.WithEventBus(bus))
	return &seeder{
		o: o, logger: logger, out: out, pool: pool, bus: bus, cat: cat, hot: hot,
		p:       authz.System("spnr-seed"),
		tenancy: tenancy.NewService(pool, cat, pols, rec, logger),
		sites:   sitesvc.NewService(pool, cat, hot, rec, rdb, keys, logger),
		ids:     identitysvc.NewService(pool, cipher, pepper, cat, identitySyncer{hot: hot}, nil, rec, bus, logger),
		pols:    pols,
		configs: configcenter.New(configcenter.Config{}, pool, cat, bus, nil, nil, rec, nil, logger),
		proxies: proxy.NewService(pool, cipher, pepper, rdb, keys, cat, hot, rec, bus, logger),
	}
}

func (s *seeder) run(ctx context.Context) (seedResult, error) {
	start := time.Now()
	if err := s.cat.ReloadAll(ctx); err != nil {
		return seedResult{}, fmt.Errorf("load catalog: %w", err)
	}
	// Build the hot state first when no instance did yet: identities imported
	// afterwards are materialized directly instead of going through the cold
	// rebuild of the first server start (which delays availability by up to 60 s).
	if rebuilt, err := s.hot.EnsureBuilt(ctx); err != nil {
		return seedResult{}, fmt.Errorf("ensure hot state: %w", err)
	} else if rebuilt {
		s.out("built the hot state")
	}
	ns, err := s.ensureNamespace(ctx)
	if err != nil {
		return seedResult{}, err
	}
	st, err := s.ensureSite(ctx, ns)
	if err != nil {
		return seedResult{}, err
	}
	if err := s.ensureGroups(ctx, ns); err != nil {
		return seedResult{}, err
	}
	typeID, err := s.ensureIdentityType(ctx, ns)
	if err != nil {
		return seedResult{}, err
	}
	// Policies are bound before identities are imported so binding changes
	// never have to re-materialize a large site.
	if err := s.ensurePolicy(ctx, ns, policy.KindRotation, seedRotationPolicy, seedRotationYAML(s.o.Proxies > 0)); err != nil {
		return seedResult{}, err
	}
	if err := s.ensurePolicy(ctx, ns, policy.KindBreaker, seedBreakerPolicy, seedBreakerYAML()); err != nil {
		return seedResult{}, err
	}
	if err := s.ensureConfig(ctx, ns); err != nil {
		return seedResult{}, err
	}
	proxies, err := s.ensureProxies(ctx, ns)
	if err != nil {
		return seedResult{}, err
	}
	identities, err := s.ensureIdentities(ctx, ns, typeID)
	if err != nil {
		return seedResult{}, err
	}
	token, err := s.rotateToken(ctx, ns)
	if err != nil {
		return seedResult{}, err
	}
	if err := s.hot.SyncSite(ctx, st.ID); err != nil {
		return seedResult{}, fmt.Errorf("synchronize hot state of site %s: %w", s.o.Site, err)
	}
	s.out("seed finished in %s", time.Since(start).Round(time.Millisecond))
	return seedResult{Token: token, Site: s.o.Site, Groups: s.o.Groups, Identities: identities, Proxies: proxies}, nil
}

// namespace returns the current catalog snapshot of the seeded namespace.
func (s *seeder) namespace(tenantID string) (*catalog.Namespace, error) {
	ns, ok := s.cat.NamespaceByName(tenantID, s.o.Namespace)
	if !ok {
		return nil, fmt.Errorf("namespace %s/%s is not in the catalog", s.o.Tenant, s.o.Namespace)
	}
	return ns, nil
}

// ensureNamespace creates the tenant and the namespace when they are missing.
func (s *seeder) ensureNamespace(ctx context.Context) (*catalog.Namespace, error) {
	var tenantID string
	err := s.pool.QueryRow(ctx, `SELECT id FROM tenants WHERE name = $1`, s.o.Tenant).Scan(&tenantID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		t, err := s.tenancy.CreateTenant(ctx, s.p, tenancy.CreateTenantInput{Name: s.o.Tenant})
		if err != nil && !isAlreadyExists(err) {
			return nil, fmt.Errorf("create tenant %s: %w", s.o.Tenant, err)
		}
		if err == nil {
			tenantID = t.ID
			s.out("created tenant %s", s.o.Tenant)
		} else if err := s.pool.QueryRow(ctx, `SELECT id FROM tenants WHERE name = $1`, s.o.Tenant).Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("look up tenant %s: %w", s.o.Tenant, err)
		}
	case err != nil:
		return nil, fmt.Errorf("look up tenant %s: %w", s.o.Tenant, err)
	}
	s.p.TenantID = tenantID
	if ns, ok := s.cat.NamespaceByName(tenantID, s.o.Namespace); ok {
		return ns, nil
	}
	if _, err := s.tenancy.CreateNamespace(ctx, s.p, tenancy.CreateNamespaceInput{Name: s.o.Namespace}); err != nil && !isAlreadyExists(err) {
		return nil, fmt.Errorf("create namespace %s: %w", s.o.Namespace, err)
	}
	if err := s.cat.ReloadAll(ctx); err != nil {
		return nil, fmt.Errorf("reload catalog: %w", err)
	}
	s.out("ensured namespace %s/%s", s.o.Tenant, s.o.Namespace)
	return s.namespace(tenantID)
}

// ensureSite creates the site or adds the client to an existing one.
func (s *seeder) ensureSite(ctx context.Context, ns *catalog.Namespace) (*catalog.Site, error) {
	if st, ok := ns.Sites[s.o.Site]; ok {
		if !st.HasClient(s.o.Client) {
			clients := append(slices.Clone(st.Clients), s.o.Client)
			if _, err := s.sites.UpdateSite(ctx, s.p, st.ID, sitesvc.UpdateSiteInput{Clients: clients}); err != nil {
				return nil, fmt.Errorf("add client %s to site %s: %w", s.o.Client, s.o.Site, err)
			}
		}
	} else {
		if _, err := s.sites.CreateSite(ctx, s.p, ns, sitesvc.CreateSiteInput{
			Name: s.o.Site, DisplayName: seedDisplayName(s.o.Site), Description: "Synthetic load-test site (spnr seed)", Clients: []string{s.o.Client},
		}); err != nil {
			return nil, fmt.Errorf("create site %s: %w", s.o.Site, err)
		}
		s.out("created site %s", s.o.Site)
	}
	fresh, err := s.namespace(ns.TenantID)
	if err != nil {
		return nil, err
	}
	st, ok := fresh.Sites[s.o.Site]
	if !ok {
		return nil, fmt.Errorf("site %s is not in the catalog", s.o.Site)
	}
	return st, nil
}

// ensureGroups creates the missing endpoint groups g0..g{N-1}.
func (s *seeder) ensureGroups(ctx context.Context, ns *catalog.Namespace) error {
	created := 0
	for i := range s.o.Groups {
		cur, err := s.namespace(ns.TenantID)
		if err != nil {
			return err
		}
		st := cur.Sites[s.o.Site]
		if _, ok := st.Group(s.o.Client, seedGroupName(i)); ok {
			continue
		}
		if _, err := s.sites.CreateEndpointGroup(ctx, s.p, st.ID, sitesvc.CreateGroupInput{
			Client: s.o.Client, Name: seedGroupName(i),
			Rules: []sitesvc.URIRule{{Kind: site.RulePrefix, Pattern: seedGroupPrefix(i)}},
		}); err != nil && !isAlreadyExists(err) {
			return fmt.Errorf("create endpoint group %s: %w", seedGroupName(i), err)
		}
		created++
	}
	if created > 0 {
		s.out("created %d endpoint groups", created)
	}
	return nil
}

// ensureIdentityType creates the load-test identity type and returns its ID.
func (s *seeder) ensureIdentityType(ctx context.Context, ns *catalog.Namespace) (string, error) {
	cur, err := s.namespace(ns.TenantID)
	if err != nil {
		return "", err
	}
	if t, ok := cur.Sites[s.o.Site].IdentityTypes[seedIdentityType]; ok {
		return t.ID, nil
	}
	t, err := s.ids.CreateIdentityType(ctx, s.p, cur, s.o.Site, seedIdentityTypeYAML(s.o.Site, s.o.Client))
	if err != nil {
		return "", fmt.Errorf("create identity type %s: %w", seedIdentityType, err)
	}
	s.out("created identity type %s", seedIdentityType)
	return t.ID, nil
}

// ensurePolicy creates and publishes a policy when missing and binds it to the site.
func (s *seeder) ensurePolicy(ctx context.Context, ns *catalog.Namespace, kind policy.Kind, name, yamlText string) error {
	cur, err := s.namespace(ns.TenantID)
	if err != nil {
		return err
	}
	page, err := s.pols.ListPolicies(ctx, s.p, cur, policysvc.ListPoliciesInput{Kind: kind, Search: name, PageSize: 100})
	if err != nil {
		return fmt.Errorf("list %s policies: %w", kind, err)
	}
	// Published versions store the normalized YAML; compare in that form.
	spec, err := policy.ParseYAML(kind, []byte(yamlText))
	if err != nil {
		return fmt.Errorf("parse %s policy %s: %w", kind, name, err)
	}
	normalized, err := policy.MarshalYAML(spec)
	if err != nil {
		return fmt.Errorf("normalize %s policy %s: %w", kind, name, err)
	}
	var id string
	for _, listed := range page.Policies {
		if listed.Name != name {
			continue
		}
		id = listed.ID
		// List results carry no YAML; load the policy to compare its published content.
		pol, err := s.pols.GetPolicy(ctx, s.p, id)
		if err != nil {
			return fmt.Errorf("load policy %s: %w", name, err)
		}
		if pol.CurrentVersion > 0 && strings.TrimSpace(pol.PublishedYAML) == strings.TrimSpace(string(normalized)) {
			break
		}
		// Missing or different published content (for example --proxies changed): publish the seed spec.
		if _, err := s.pols.SaveDraft(ctx, s.p, id, yamlText); err != nil {
			return fmt.Errorf("save draft of policy %s: %w", name, err)
		}
		if _, err := s.pols.PublishPolicy(ctx, s.p, policysvc.PublishInput{ID: id, Comment: "spnr seed"}); err != nil {
			return fmt.Errorf("publish policy %s: %w", name, err)
		}
		s.out("published a new version of %s policy %s", kind, name)
		break
	}
	if id == "" {
		pol, err := s.pols.CreatePolicy(ctx, s.p, cur, policysvc.CreateInput{Kind: kind, YAML: yamlText, Publish: true, Comment: "spnr seed"})
		if err != nil {
			return fmt.Errorf("create policy %s: %w", name, err)
		}
		id = pol.ID
		s.out("created %s policy %s", kind, name)
	}
	if _, err := s.pols.SetBinding(ctx, s.p, id, policysvc.Target{Site: s.o.Site}); err != nil {
		return fmt.Errorf("bind policy %s to site %s: %w", name, s.o.Site, err)
	}
	return nil
}

// ensureConfig publishes the load-test config item when missing.
func (s *seeder) ensureConfig(ctx context.Context, ns *catalog.Namespace) error {
	cur, err := s.namespace(ns.TenantID)
	if err != nil {
		return err
	}
	_, err = s.configs.GetItemByLocator(ctx, s.p, cur, seedConfigGroup, seedConfigKey)
	if err == nil {
		return nil
	}
	if !apperr.IsNotFound(err) {
		return fmt.Errorf("look up config %s/%s: %w", seedConfigGroup, seedConfigKey, err)
	}
	if _, err := s.configs.CreateItem(ctx, s.p, cur, configcenter.CreateRequest{
		Group: seedConfigGroup, Key: seedConfigKey, Format: "json", Description: "Load-test configuration (spnr seed)",
		Content: seedConfigContent(s.o), Publish: true, Comment: "spnr seed",
	}); err != nil && !isAlreadyExists(err) {
		return fmt.Errorf("create config %s/%s: %w", seedConfigGroup, seedConfigKey, err)
	}
	s.out("published config %s/%s", seedConfigGroup, seedConfigKey)
	return nil
}

// ensureProxies imports the proxies (existing URLs are left unchanged).
func (s *seeder) ensureProxies(ctx context.Context, ns *catalog.Namespace) (int, error) {
	if s.o.Proxies == 0 {
		return 0, nil
	}
	cur, err := s.namespace(ns.TenantID)
	if err != nil {
		return 0, err
	}
	res, err := s.proxies.ImportProxies(ctx, s.p, cur, proxy.ImportRequest{
		Format: "lines", Data: seedProxyLines(s.o.ProxyURL, s.o.Proxies),
		Defaults: proxy.Defaults{Kind: "datacenter", Provider: "loadtest", Tags: []string{"loadtest"}, MaxConcurrency: 1000},
	})
	if err != nil {
		return 0, fmt.Errorf("import proxies: %w", err)
	}
	if len(res.Failed) > 0 {
		return 0, fmt.Errorf("import proxies: line %d: %s", res.Failed[0].Line, res.Failed[0].Message)
	}
	s.out("proxies: %d created, %d updated, %d unchanged", res.Created, res.Updated, res.Unchanged)
	return res.Created + res.Updated + res.Unchanged, nil
}

// ensureIdentities imports the synthetic identities in chunks and returns the
// number of identities of the load-test type.
func (s *seeder) ensureIdentities(ctx context.Context, ns *catalog.Namespace, typeID string) (int, error) {
	existing, err := s.countIdentities(ctx, typeID)
	if err != nil {
		return 0, err
	}
	if existing >= s.o.Identities {
		return existing, nil
	}
	start := time.Now()
	created := 0
	for _, chunk := range seedChunks(s.o.Identities, s.o.ChunkSize) {
		cur, err := s.namespace(ns.TenantID)
		if err != nil {
			return 0, err
		}
		res, err := s.ids.ImportIdentities(ctx, s.p, cur, identitysvc.ImportInput{
			Site: s.o.Site, Type: seedIdentityType, Format: "jsonl", Mode: "create_only",
			Data: seedIdentityRows(chunk[0], chunk[1]),
		})
		if err != nil {
			return 0, fmt.Errorf("import identities %d..%d: %w", chunk[0], chunk[1]-1, err)
		}
		if len(res.Failed) > 0 {
			return 0, fmt.Errorf("import identities %d..%d: line %d: %s", chunk[0], chunk[1]-1, res.Failed[0].Line, res.Failed[0].Message)
		}
		created += res.Created
		s.out("identities: %d/%d imported (%d created)", chunk[1], s.o.Identities, created)
	}
	s.out("identity import finished in %s", time.Since(start).Round(time.Millisecond))
	return s.countIdentities(ctx, typeID)
}

func (s *seeder) countIdentities(ctx context.Context, typeID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM identities WHERE type_id = $1`, typeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count identities: %w", err)
	}
	return n, nil
}

// revokeTokenByNameSQL revokes the usable tokens with a given name. Names are
// unique among usable tokens only, so the name is free again afterwards.
//
//nolint:gosec // G101: an SQL statement, not a credential.
const revokeTokenByNameSQL = `
UPDATE api_tokens
SET revoked_at = now()
WHERE namespace_id = $1 AND name = $2 AND revoked_at IS NULL
RETURNING id`

// rotateToken revokes an existing node token of the configured name and creates a new one.
func (s *seeder) rotateToken(ctx context.Context, ns *catalog.Namespace) (string, error) {
	rows, err := s.pool.Query(ctx, revokeTokenByNameSQL, ns.ID, s.o.TokenName)
	if err != nil {
		return "", fmt.Errorf("revoke token %s: %w", s.o.TokenName, err)
	}
	revoked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("revoke token %s: %w", s.o.TokenName, err)
	}
	for _, id := range revoked {
		data, _ := json.Marshal(map[string]string{"token_id": id})
		ev := events.Event{Type: auth.EventTokenRevoked, TenantID: ns.TenantID, NamespaceID: ns.ID, At: time.Now().UTC(), Data: data}
		if err := s.bus.Publish(ctx, events.ChannelTokens, ev); err != nil {
			s.logger.Warn("publish token revocation (running instances drop it after the token cache TTL)", "token_id", id, "error", err)
		}
	}
	plaintext, _, err := auth.CreateTokenDirect(ctx, s.pool, ns.TenantID, ns.ID, s.o.TokenName, seedScopes, nil)
	if err != nil {
		return "", fmt.Errorf("create token %s: %w", s.o.TokenName, err)
	}
	if len(revoked) > 0 {
		s.out("revoked %d previous token(s) named %s", len(revoked), s.o.TokenName)
	}
	return plaintext, nil
}

// requireCurrentSchema fails when the database schema is behind the binary.
func requireCurrentSchema(ctx context.Context, pool *pgxpool.Pool) error {
	current, err := pgstore.MigrationVersion(ctx, pool)
	if err != nil {
		return err
	}
	latest, err := pgstore.LatestMigrationVersion()
	if err != nil {
		return err
	}
	if current < latest {
		return fmt.Errorf("database schema is at version %d, binary expects %d: run spnr migrate up", current, latest)
	}
	return nil
}

// identitySyncer adapts the hot-state syncer to identitysvc.HotSyncer.
type identitySyncer struct{ hot *hotstate.Syncer }

func (a identitySyncer) SyncIdentities(ctx context.Context, siteID string, ids []string, opts identitysvc.SyncOptions) error {
	return a.hot.SyncIdentities(ctx, siteID, ids, hotstate.SyncOptions(opts))
}

func (a identitySyncer) RemoveIdentities(ctx context.Context, siteID string, ids []string) error {
	return a.hot.RemoveIdentities(ctx, siteID, ids)
}

func (a identitySyncer) SyncAccounts(ctx context.Context, siteID string, ids []string) error {
	return a.hot.SyncAccounts(ctx, siteID, ids)
}

// isAlreadyExists reports whether err is an apperr already_exists error
// (a concurrent seed created the object first).
func isAlreadyExists(err error) bool {
	return apperr.ReasonOf(err) == apperr.ReasonAlreadyExists
}
