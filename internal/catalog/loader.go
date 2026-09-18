package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TikHub/Spinneret/internal/catalog/catalogdb"
)

// rollbackTimeout bounds the rollback that ends a load transaction.
const rollbackTimeout = 5 * time.Second

// errNamespaceNotFound is returned by loadRaw when the namespace no longer exists.
var errNamespaceNotFound = errors.New("catalog: namespace not found")

// rawNamespace holds every database row needed to build one namespace snapshot.
type rawNamespace struct {
	Namespace     catalogdb.CatalogGetNamespaceRow
	Sites         []catalogdb.CatalogListSitesRow
	Groups        []catalogdb.CatalogListEndpointGroupsRow
	Rules         []catalogdb.CatalogListURIRulesRow
	IdentityTypes []catalogdb.CatalogListIdentityTypesRow
	Policies      []catalogdb.CatalogListPublishedPoliciesRow
	Bindings      []catalogdb.CatalogListPolicyBindingsRow
}

// fingerprint returns a content hash of the rows. Two loads with the same
// fingerprint produce equivalent snapshots, so a rebuild can be skipped.
func (r *rawNamespace) fingerprint() (uint64, error) {
	h := xxhash.New()
	enc := json.NewEncoder(h)
	for _, part := range []any{r.Namespace, r.Sites, r.Groups, r.Rules, r.IdentityTypes, r.Policies, r.Bindings} {
		if err := enc.Encode(part); err != nil {
			return 0, fmt.Errorf("fingerprint namespace %s: %w", r.Namespace.ID, err)
		}
	}
	return h.Sum64(), nil
}

// listNamespaceIDs returns the IDs of every namespace.
func listNamespaceIDs(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ids, err := catalogdb.New(pool).CatalogListNamespaceIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	return ids, nil
}

// loadRaw reads the rows of one namespace inside a read-only REPEATABLE READ
// transaction so that all of them come from one consistent database snapshot.
// It returns errNamespaceNotFound when the namespace does not exist.
func loadRaw(ctx context.Context, pool *pgxpool.Pool, namespaceID string) (*rawNamespace, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("load namespace %s: begin transaction: %w", namespaceID, err)
	}
	// The transaction is read-only: rolling back simply releases the snapshot.
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rctx)
	}()

	q := catalogdb.New(tx)
	raw := &rawNamespace{}
	raw.Namespace, err = q.CatalogGetNamespace(ctx, namespaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNamespaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load namespace %s: %w", namespaceID, err)
	}
	if raw.Sites, err = q.CatalogListSites(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load sites of namespace %s: %w", namespaceID, err)
	}
	if raw.Groups, err = q.CatalogListEndpointGroups(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load endpoint groups of namespace %s: %w", namespaceID, err)
	}
	if raw.Rules, err = q.CatalogListURIRules(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load uri rules of namespace %s: %w", namespaceID, err)
	}
	if raw.IdentityTypes, err = q.CatalogListIdentityTypes(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load identity types of namespace %s: %w", namespaceID, err)
	}
	if raw.Policies, err = q.CatalogListPublishedPolicies(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load policies of namespace %s: %w", namespaceID, err)
	}
	if raw.Bindings, err = q.CatalogListPolicyBindings(ctx, namespaceID); err != nil {
		return nil, fmt.Errorf("load policy bindings of namespace %s: %w", namespaceID, err)
	}
	return raw, nil
}
