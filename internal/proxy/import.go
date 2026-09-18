package proxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/audit"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/pkg/idgen"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
	"github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

// importChunk bounds the rows written or looked up by one statement.
const importChunk = 1000

// Defaults are attribute values applied to imported rows that do not set them.
type Defaults struct {
	// Kind; empty means datacenter.
	Kind     string
	Region   string
	City     string
	Provider string
	// Tags are added to the tags of every row.
	Tags []string
	// MaxConcurrency; 0 means 1.
	MaxConcurrency  int
	SessionTemplate string
}

// ImportRequest imports proxies into a namespace.
type ImportRequest struct {
	// Format is lines, jsonl or csv.
	Format string
	// Data is the import document (at most MaxImportBytes).
	Data     string
	Defaults Defaults
	// DryRun validates and counts without storing anything.
	DryRun bool
}

// ImportResult summarizes an import.
type ImportResult struct {
	Created   int
	Updated   int
	Unchanged int
	Failed    []ImportFailure
}

// attrSet records which attributes a row (or a default) explicitly sets.
type attrSet struct {
	kind, region, city, provider, tags, maxConcurrency, sessionTemplate bool
}

// importCandidate is a validated import row.
type importCandidate struct {
	line  int
	url   ParsedURL
	hash  []byte
	attrs Attributes
	set   attrSet
}

// importUpdate is an existing proxy whose attributes change.
type importUpdate struct {
	id    string
	attrs Attributes
}

// ImportProxies imports proxies (proxy:write). Rows are de-duplicated by URL
// hash against the namespace and within the import; existing proxies get the
// attributes a row or a default sets, other attributes keep their values.
func (s *Service) ImportProxies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, req ImportRequest) (ImportResult, error) {
	if err := requireNamespace(p, ns, authz.PermProxyWrite); err != nil {
		return ImportResult{}, err
	}
	defaults, err := normalizeDefaults(req.Defaults)
	if err != nil {
		return ImportResult{}, err
	}
	failures := &failureList{}
	rows, err := parseImport(req.Format, req.Data, failures)
	if err != nil {
		return ImportResult{}, err
	}
	candidates := s.resolveRows(rows, defaults, failures)
	existing, err := s.findExisting(ctx, ns.ID, candidates)
	if err != nil {
		return ImportResult{}, err
	}

	var creates []importCandidate
	var updates []importUpdate
	res := ImportResult{}
	for _, c := range candidates {
		row, ok := existing[hex.EncodeToString(c.hash)]
		if !ok {
			creates = append(creates, c)
			continue
		}
		current := attributesOf(row.Kind, row.Region, row.City, row.Provider, row.MaxConcurrency, row.Tags, row.SessionTemplate)
		merged := mergeAttributes(current, c)
		if merged.equal(current) {
			res.Unchanged++
			continue
		}
		updates = append(updates, importUpdate{id: row.ID, attrs: merged})
	}
	if req.DryRun {
		res.Created, res.Updated, res.Failed = len(creates), len(updates), failures.result()
		return res, nil
	}

	var createdIDs []string
	err = inTx(ctx, s.pool, func(q *proxydb.Queries) error {
		ids, err := s.insertProxies(ctx, q, ns.ID, creates)
		if err != nil {
			return err
		}
		createdIDs = ids
		return updateImported(ctx, q, ns.ID, updates)
	})
	if err != nil {
		return ImportResult{}, postgres.MapError(err, "proxy import")
	}
	res.Created = len(createdIDs)
	res.Unchanged += len(creates) - len(createdIDs) // inserted concurrently by another import
	res.Updated = len(updates)
	res.Failed = failures.result()

	sctx, cancel := detached(ctx)
	defer cancel()
	s.record(sctx, p, ns, "proxy.import", "", "", audit.ResultOK, map[string]any{
		"format": req.Format, "created": res.Created, "updated": res.Updated,
		"unchanged": res.Unchanged, "failed": failures.count(),
	})
	changed := make([]string, 0, len(createdIDs)+len(updates))
	changed = append(changed, createdIDs...)
	for _, u := range updates {
		changed = append(changed, u.id)
	}
	if err := s.syncProxies(sctx, ns.ID, changed); err != nil {
		s.logger.Error("proxy import: hot state sync failed", slog.String("namespace_id", ns.ID), slog.Any("error", err))
		return res, apperr.Internal(err)
	}
	return res, nil
}

// normalizeDefaults validates import defaults.
func normalizeDefaults(d Defaults) (Defaults, error) {
	d.Kind = strings.ToLower(strings.TrimSpace(d.Kind))
	if d.Kind != "" && !ValidKind(d.Kind) {
		return d, apperr.InvalidArgument("", "defaults.kind must be one of datacenter, residential, mobile, tunnel")
	}
	if d.MaxConcurrency < 0 || d.MaxConcurrency > MaxMaxConcurrency {
		return d, apperr.InvalidArgument("", "defaults.max_concurrency must be between 0 and %d", MaxMaxConcurrency)
	}
	probe := Attributes{
		Kind: KindDatacenter, Region: strings.TrimSpace(d.Region), City: strings.TrimSpace(d.City),
		Provider: strings.TrimSpace(d.Provider), MaxConcurrency: 1, Tags: d.Tags, SessionTemplate: d.SessionTemplate,
	}
	if err := probe.validate(); err != nil {
		return d, apperr.InvalidArgument("", "defaults: %v", err)
	}
	d.Region, d.City, d.Provider, d.Tags = probe.Region, probe.City, probe.Provider, probe.Tags
	return d, nil
}

// resolveRows validates rows, applies defaults and removes duplicates.
func (s *Service) resolveRows(rows []importRow, d Defaults, failures *failureList) []importCandidate {
	seen := make(map[string]int, len(rows))
	out := make([]importCandidate, 0, len(rows))
	for _, row := range rows {
		c, err := resolveRow(row, d)
		if err != nil {
			failures.add(row.Line, "%v", err)
			continue
		}
		c.hash = URLHash(s.pepper, c.url)
		key := hex.EncodeToString(c.hash)
		if first, dup := seen[key]; dup {
			failures.add(row.Line, "duplicate of line %d", first)
			continue
		}
		seen[key] = row.Line
		out = append(out, c)
	}
	return out
}

// resolveRow turns one row into a candidate.
func resolveRow(row importRow, d Defaults) (importCandidate, error) {
	u, err := ParseProxyURL(row.URL)
	if err != nil {
		return importCandidate{}, err
	}
	c := importCandidate{line: row.Line, url: u}
	a := Attributes{
		Kind: KindDatacenter, Region: d.Region, City: d.City, Provider: d.Provider,
		MaxConcurrency: 1, Tags: d.Tags, SessionTemplate: d.SessionTemplate,
	}
	c.set = attrSet{
		kind: d.Kind != "", region: d.Region != "", city: d.City != "", provider: d.Provider != "",
		tags: len(d.Tags) > 0, maxConcurrency: d.MaxConcurrency > 0, sessionTemplate: d.SessionTemplate != "",
	}
	if d.Kind != "" {
		a.Kind = d.Kind
	}
	if d.MaxConcurrency > 0 {
		a.MaxConcurrency = d.MaxConcurrency
	}
	if row.Kind != nil {
		a.Kind, c.set.kind = strings.ToLower(strings.TrimSpace(*row.Kind)), true
	}
	if row.Region != nil {
		a.Region, c.set.region = strings.TrimSpace(*row.Region), true
	}
	if row.City != nil {
		a.City, c.set.city = strings.TrimSpace(*row.City), true
	}
	if row.Provider != nil {
		a.Provider, c.set.provider = strings.TrimSpace(*row.Provider), true
	}
	if row.TagsSet {
		a.Tags, c.set.tags = mergeTags(row.Tags, d.Tags), true
	}
	if row.MaxConcurrency != nil {
		a.MaxConcurrency, c.set.maxConcurrency = *row.MaxConcurrency, true
	}
	if row.SessionTemplate != nil {
		a.SessionTemplate, c.set.sessionTemplate = *row.SessionTemplate, true
	}
	if err := a.validate(); err != nil {
		return importCandidate{}, err
	}
	c.attrs = a
	return c, nil
}

// rowAttributes extracts the attributes of a stored proxy.
func rowAttributes(row proxydb.Proxy) Attributes {
	return attributesOf(row.Kind, row.Region, row.City, row.Provider, row.MaxConcurrency, row.Tags, row.SessionTemplate)
}

// attributesOf builds Attributes from stored column values.
func attributesOf(kind, region, city, provider string, maxConcurrency int32, tags []string, sessionTemplate string) Attributes {
	if tags == nil {
		tags = []string{}
	}
	return Attributes{
		Kind: kind, Region: region, City: city, Provider: provider,
		MaxConcurrency: int(maxConcurrency), Tags: tags, SessionTemplate: sessionTemplate,
	}
}

// mergeAttributes applies the attributes a candidate sets onto current.
func mergeAttributes(current Attributes, c importCandidate) Attributes {
	m := current
	if c.set.kind {
		m.Kind = c.attrs.Kind
	}
	if c.set.region {
		m.Region = c.attrs.Region
	}
	if c.set.city {
		m.City = c.attrs.City
	}
	if c.set.provider {
		m.Provider = c.attrs.Provider
	}
	if c.set.tags {
		m.Tags = c.attrs.Tags
	}
	if c.set.maxConcurrency {
		m.MaxConcurrency = c.attrs.MaxConcurrency
	}
	if c.set.sessionTemplate {
		m.SessionTemplate = c.attrs.SessionTemplate
	}
	return m
}

// findExisting loads the proxies of the namespace matching candidate hashes,
// keyed by hex hash.
func (s *Service) findExisting(ctx context.Context, namespaceID string, candidates []importCandidate) (map[string]proxydb.ProxyFindByHashesRow, error) {
	out := make(map[string]proxydb.ProxyFindByHashesRow)
	for start := 0; start < len(candidates); start += importChunk {
		end := min(start+importChunk, len(candidates))
		hashes := make([][]byte, 0, end-start)
		for _, c := range candidates[start:end] {
			hashes = append(hashes, c.hash)
		}
		rows, err := s.q.ProxyFindByHashes(ctx, proxydb.ProxyFindByHashesParams{NamespaceID: namespaceID, Hashes: hashes})
		if err != nil {
			return nil, fmt.Errorf("look up existing proxies: %w", err)
		}
		for _, r := range rows {
			out[hex.EncodeToString(r.UrlHash)] = r
		}
	}
	return out, nil
}

// insertProxies seals and inserts new proxies in chunks, returning the IDs
// actually inserted (rows that conflict with a concurrent insert are skipped).
func (s *Service) insertProxies(ctx context.Context, q *proxydb.Queries, namespaceID string, creates []importCandidate) ([]string, error) {
	var inserted []string
	for start := 0; start < len(creates); start += importChunk {
		chunk := creates[start:min(start+importChunk, len(creates))]
		arg := proxydb.ProxyInsertBatchParams{NamespaceID: namespaceID}
		for _, c := range chunk {
			id := idgen.New(idgen.Proxy)
			sealed, err := SealURL(s.cipher, id, c.url)
			if err != nil {
				return nil, fmt.Errorf("seal proxy url: %w", err)
			}
			arg.Ids = append(arg.Ids, id)
			arg.Schemes = append(arg.Schemes, c.url.Scheme)
			arg.Hosts = append(arg.Hosts, c.url.Host)
			arg.Ports = append(arg.Ports, int32(c.url.Port))
			arg.UsernameHints = append(arg.UsernameHints, c.url.UsernameHint())
			arg.DisplayUrls = append(arg.DisplayUrls, c.url.DisplayURL())
			arg.UrlHashes = append(arg.UrlHashes, c.hash)
			arg.UrlCiphertexts = append(arg.UrlCiphertexts, sealed.Ciphertext)
			arg.UrlWrappedDeks = append(arg.UrlWrappedDeks, sealed.WrappedDEK)
			arg.UrlKekIds = append(arg.UrlKekIds, sealed.KEKID)
			arg.Kinds = append(arg.Kinds, c.attrs.Kind)
			arg.Regions = append(arg.Regions, c.attrs.Region)
			arg.Cities = append(arg.Cities, c.attrs.City)
			arg.Providers = append(arg.Providers, c.attrs.Provider)
			arg.MaxConcurrencies = append(arg.MaxConcurrencies, int32(c.attrs.MaxConcurrency))
			arg.Tags = append(arg.Tags, strings.Join(c.attrs.Tags, ","))
			arg.SessionTemplates = append(arg.SessionTemplates, c.attrs.SessionTemplate)
		}
		ids, err := q.ProxyInsertBatch(ctx, arg)
		if err != nil {
			return nil, fmt.Errorf("insert proxies: %w", err)
		}
		inserted = append(inserted, ids...)
	}
	return inserted, nil
}

// updateImported writes the merged attributes of existing proxies.
func updateImported(ctx context.Context, q *proxydb.Queries, namespaceID string, updates []importUpdate) error {
	for start := 0; start < len(updates); start += importChunk {
		chunk := updates[start:min(start+importChunk, len(updates))]
		arg := proxydb.ProxyUpdateAttributesBatchParams{NamespaceID: namespaceID}
		for _, u := range chunk {
			arg.Ids = append(arg.Ids, u.id)
			arg.Kinds = append(arg.Kinds, u.attrs.Kind)
			arg.Regions = append(arg.Regions, u.attrs.Region)
			arg.Cities = append(arg.Cities, u.attrs.City)
			arg.Providers = append(arg.Providers, u.attrs.Provider)
			arg.MaxConcurrencies = append(arg.MaxConcurrencies, int32(u.attrs.MaxConcurrency))
			arg.Tags = append(arg.Tags, strings.Join(u.attrs.Tags, ","))
			arg.SessionTemplates = append(arg.SessionTemplates, u.attrs.SessionTemplate)
		}
		if err := q.ProxyUpdateAttributesBatch(ctx, arg); err != nil {
			return fmt.Errorf("update imported proxies: %w", err)
		}
	}
	return nil
}
