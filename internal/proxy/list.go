package proxy

import (
	"context"
	"fmt"
	"strings"

	"github.com/Evil0ctal/Spinneret/internal/apperr"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/catalog"
	"github.com/Evil0ctal/Spinneret/internal/proxy/proxydb"
)

// List limits.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
	maxFilterValues = 64
	maxSearchLength = 256
)

// ListFilter selects proxies of a namespace. Repeated filters match any of
// their values, except Tags which must all be present. Empty States match
// every state except retired.
type ListFilter struct {
	States    []string
	Kinds     []string
	Providers []string
	Regions   []string
	Tags      []string
	// Search matches a substring of the display URL, host or exit IP, or an ID prefix.
	Search    string
	PageSize  int
	PageToken string
}

// ListResult is one page of proxies.
type ListResult struct {
	Proxies       []*Proxy
	NextPageToken string
	Total         int
}

// listCursor is the keyset pagination cursor (proxies are ordered by ID, newest first).
type listCursor struct {
	ID string `json:"id"`
}

// ListProxies lists proxies of a namespace (proxy:read) including bound
// identity counts and the hot state of the sites the principal can read.
func (s *Service) ListProxies(ctx context.Context, p *authz.Principal, ns *catalog.Namespace, f ListFilter) (ListResult, error) {
	if err := requireNamespace(p, ns, authz.PermProxyRead); err != nil {
		return ListResult{}, err
	}
	if err := validateFilter(f); err != nil {
		return ListResult{}, err
	}
	size := f.PageSize
	switch {
	case size <= 0:
		size = DefaultPageSize
	case size > MaxPageSize:
		size = MaxPageSize
	}
	var cur listCursor
	if f.PageToken != "" {
		if err := decodeCursor(f.PageToken, &cur); err != nil {
			return ListResult{}, err
		}
	}
	search, idPrefix := searchPatterns(f.Search)
	rows, err := s.q.ProxyList(ctx, proxydb.ProxyListParams{
		NamespaceID: ns.ID, States: nonNil(f.States), Kinds: nonNil(f.Kinds), Providers: nonNil(f.Providers),
		Regions: nonNil(f.Regions), Tags: nonNil(f.Tags), SearchPattern: search, IDPrefixPattern: idPrefix,
		AfterID: cur.ID, PageLimit: int32(size + 1),
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("list proxies: %w", err)
	}
	total, err := s.q.ProxyCount(ctx, proxydb.ProxyCountParams{
		NamespaceID: ns.ID, States: nonNil(f.States), Kinds: nonNil(f.Kinds), Providers: nonNil(f.Providers),
		Regions: nonNil(f.Regions), Tags: nonNil(f.Tags), SearchPattern: search, IDPrefixPattern: idPrefix,
	})
	if err != nil {
		return ListResult{}, fmt.Errorf("count proxies: %w", err)
	}
	res := ListResult{Total: int(total)}
	if len(rows) > size {
		rows = rows[:size]
		if res.NextPageToken, err = encodeCursor(listCursor{ID: rows[len(rows)-1].ID}); err != nil {
			return ListResult{}, err
		}
	}
	res.Proxies = make([]*Proxy, len(rows))
	for i, row := range rows {
		res.Proxies[i] = newProxyView(row, ns)
	}
	if err := s.decorate(ctx, p, ns, res.Proxies); err != nil {
		return ListResult{}, err
	}
	return res, nil
}

// validateFilter checks the filter values (the API layer validates them too).
func validateFilter(f ListFilter) error {
	for _, st := range f.States {
		if !ValidState(st) {
			return apperr.InvalidArgument("", "unknown state %q", truncate(st, 32))
		}
	}
	for _, k := range f.Kinds {
		if !ValidKind(k) {
			return apperr.InvalidArgument("", "unknown kind %q", truncate(k, 32))
		}
	}
	for _, list := range [][]string{f.States, f.Kinds, f.Providers, f.Regions, f.Tags} {
		if len(list) > maxFilterValues {
			return apperr.InvalidArgument("", "at most %d values per filter are allowed", maxFilterValues)
		}
	}
	if len(f.Search) > maxSearchLength {
		return apperr.InvalidArgument("", "search must be at most %d bytes", maxSearchLength)
	}
	return nil
}

// searchPatterns returns the ILIKE substring pattern and the LIKE ID prefix
// pattern for a search string ("" when empty).
func searchPatterns(search string) (substring, idPrefix string) {
	search = strings.TrimSpace(search)
	if search == "" {
		return "", ""
	}
	escaped := escapeLike(search)
	return "%" + escaped + "%", escaped + "%"
}

// escapeLike escapes the LIKE metacharacters % _ and the escape character \.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// nonNil returns s or an empty slice: a nil slice would be sent as SQL NULL.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
