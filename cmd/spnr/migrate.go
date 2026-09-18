package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	pgstore "github.com/Evil0ctal/Spinneret/internal/store/postgres"
)

func newMigrateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage the PostgreSQL schema",
		Long: "Apply, roll back or inspect the database migrations embedded in this binary.\n" +
			"Only SPINNERET_DATABASE_URL is required. Concurrent runs are serialized with an advisory lock.",
	}

	up := &cobra.Command{
		Use:   "up",
		Short: "Apply every pending migration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				if err := pgstore.Migrate(ctx, pool); err != nil {
					return err
				}
				v, err := pgstore.MigrationVersion(ctx, pool)
				if err != nil {
					return err
				}
				return writeLine(a.stdout, "database schema is at version %d", v)
			})
		},
	}

	var to int64
	down := &cobra.Command{
		Use:   "down",
		Short: "Roll back migrations (one step by default)",
		Long: "Roll back migrations. Without --to the latest applied migration is rolled back;\n" +
			"--to N rolls back until the schema version equals N (0 removes every migration).\n" +
			"Rolling back drops tables and data.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			toSet := cmd.Flags().Changed("to")
			if toSet && to < 0 {
				return errors.New("--to must be >= 0")
			}
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				current, err := pgstore.MigrationVersion(ctx, pool)
				if err != nil {
					return err
				}
				target, err := downTarget(current, to, toSet)
				if err != nil {
					return err
				}
				if target == current {
					return writeLine(a.stdout, "database schema is already at version %d", current)
				}
				if err := pgstore.MigrateDownTo(ctx, pool, target); err != nil {
					return err
				}
				v, err := pgstore.MigrationVersion(ctx, pool)
				if err != nil {
					return err
				}
				return writeLine(a.stdout, "database schema is at version %d", v)
			})
		},
	}
	down.Flags().Int64Var(&to, "to", 0, "target schema version")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the applied and the embedded schema version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				current, err := pgstore.MigrationVersion(ctx, pool)
				if err != nil {
					return err
				}
				latest, err := pgstore.LatestMigrationVersion()
				if err != nil {
					return err
				}
				return writeLine(a.stdout, "%s", migrationStatus(current, latest))
			})
		},
	}

	cmd.AddCommand(up, down, status)
	return cmd
}

// downTarget computes the version to roll back to.
func downTarget(current, to int64, toSet bool) (int64, error) {
	if !toSet {
		return max(current-1, 0), nil
	}
	if to > current {
		return 0, fmt.Errorf("--to %d is above the current schema version %d (use migrate up)", to, current)
	}
	return to, nil
}

// migrationStatus describes the schema state for humans.
func migrationStatus(current, latest int64) string {
	switch {
	case current == latest:
		return fmt.Sprintf("database schema version %d (up to date)", current)
	case current < latest:
		return fmt.Sprintf("database schema version %d, binary version %d (%d pending: run spnr migrate up)", current, latest, latest-current)
	default:
		return fmt.Sprintf("database schema version %d is newer than this binary (%d)", current, latest)
	}
}

// withDatabase opens a database-only connection pool for fn.
func (a *app) withDatabase(ctx context.Context, fn func(ctx context.Context, pool *pgxpool.Pool) error) error {
	pool, err := a.openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(ctx, pool)
}
