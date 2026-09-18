package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/hotstate"
)

func newRebuildCmd(a *app) *cobra.Command {
	var tenant, namespace, site string
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the Redis hot state from PostgreSQL",
		Long: "Rebuild the Redis hot state. Without --site the hot-state epoch is deleted and every\n" +
			"site is rebuilt (API instances report not-ready until the rebuild finished). With --site\n" +
			"(and --tenant/--namespace) only that site is re-materialized.\n\n" +
			"Requires SPINNERET_DATABASE_URL and SPINNERET_REDIS_URL (or SPINNERET_REDIS_ADDRS) with\n" +
			"the SPINNERET_REDIS_PREFIX of the deployment.",
		Example: "  spnr rebuild\n  spnr rebuild --tenant default --namespace default --site shop",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if site == "" && (cmd.Flags().Changed("tenant") || cmd.Flags().Changed("namespace")) {
				return errors.New("--tenant and --namespace select the namespace of --site; add --site for a site rebuild")
			}
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				return a.rebuild(ctx, pool, tenant, namespace, site)
			})
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "default", "tenant of the site")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "namespace of the site")
	cmd.Flags().StringVar(&site, "site", "", "rebuild only this site")
	return cmd
}

func (a *app) rebuild(ctx context.Context, pool *pgxpool.Pool, tenant, namespace, site string) error {
	rdb, keys, err := a.optionalRedis(ctx)
	if err != nil {
		return err
	}
	if rdb == nil {
		return errors.New(envRedisURL + " or " + envRedisAddrs + " is required")
	}
	defer rdb.Close()
	logger := a.logger()
	cat := catalog.NewStore(pool, nil, logger)
	if err := cat.ReloadAll(ctx); err != nil {
		return fmt.Errorf("load catalog: %w", err)
	}
	syncer := hotstate.NewSyncer(pool, rdb, keys, cat, logger)
	start := time.Now()

	if site != "" {
		tenantID, _, err := lookupNamespace(ctx, pool, tenant, namespace)
		if err != nil {
			return err
		}
		ns, ok := cat.NamespaceByName(tenantID, namespace)
		if !ok {
			return fmt.Errorf("namespace %q of tenant %q not found", namespace, tenant)
		}
		st, ok := ns.Sites[site]
		if !ok {
			return fmt.Errorf("site %q not found in namespace %s/%s", site, tenant, namespace)
		}
		if err := syncer.SyncSite(ctx, st.ID); err != nil {
			return fmt.Errorf("rebuild site %s: %w", site, err)
		}
		return writeLine(a.stdout, "rebuilt hot state of site %s/%s/%s in %s", tenant, namespace, site, time.Since(start).Round(time.Millisecond))
	}

	if err := rdb.Do(ctx, rdb.B().Del().Key(keys.Epoch()).Build()).Error(); err != nil {
		return fmt.Errorf("delete hot-state epoch: %w", err)
	}
	if err := syncer.RebuildAll(ctx); err != nil {
		return fmt.Errorf("rebuild hot state: %w", err)
	}
	return writeLine(a.stdout, "rebuilt hot state of every site in %s", time.Since(start).Round(time.Millisecond))
}
