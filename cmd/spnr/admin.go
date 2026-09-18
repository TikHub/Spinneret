package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/auth"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/policysvc"
)

// alreadyInitialized is printed when a platform administrator exists.
const alreadyInitialized = "already initialized"

func newAdminCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Administer console users",
	}
	var (
		username, passwordEnv, tenant, namespace string
		passwordStdin                            bool
	)
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create the first platform administrator",
		Long: "Create the first platform administrator together with a tenant and a namespace\n" +
			"(with the default policies). The command is idempotent: when a platform administrator\n" +
			"already exists it prints \"" + alreadyInitialized + "\" and exits with status 0.\n\n" +
			"Only SPINNERET_DATABASE_URL is required; when SPINNERET_REDIS_URL is set running\n" +
			"instances are notified to reload the new namespace immediately.",
		Example: "  SPINNERET_ADMIN_PASSWORD=... spnr admin init --username admin --password-env SPINNERET_ADMIN_PASSWORD\n" +
			"  printf '%s\\n' \"$PASSWORD\" | spnr admin init --username admin --password-stdin",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := a.readPassword(passwordEnv, passwordStdin)
			if err != nil {
				return err
			}
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				return a.adminInit(ctx, pool, username, password, tenant, namespace)
			})
		},
	}
	initCmd.Flags().StringVar(&username, "username", "", "administrator user name (required)")
	initCmd.Flags().StringVar(&passwordEnv, "password-env", "", "name of the environment variable holding the password")
	initCmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from the first line of stdin")
	initCmd.Flags().StringVar(&tenant, "tenant", "default", "tenant to create or reuse")
	initCmd.Flags().StringVar(&namespace, "namespace", "default", "namespace to create or reuse")
	_ = initCmd.MarkFlagRequired("username")
	initCmd.MarkFlagsMutuallyExclusive("password-env", "password-stdin")
	initCmd.MarkFlagsOneRequired("password-env", "password-stdin")
	cmd.AddCommand(initCmd)
	return cmd
}

// readPassword returns the password from the named environment variable or stdin.
func (a *app) readPassword(envName string, fromStdin bool) (string, error) {
	switch {
	case fromStdin && envName != "":
		return "", errors.New("use either --password-env or --password-stdin")
	case fromStdin:
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		password := strings.TrimRight(line, "\r\n")
		if password == "" {
			return "", errors.New("the password read from stdin is empty")
		}
		return password, nil
	case envName != "":
		password, ok := a.lookup(envName)
		if !ok || password == "" {
			return "", fmt.Errorf("environment variable %s is not set or empty", envName)
		}
		return password, nil
	default:
		return "", errors.New("one of --password-env or --password-stdin is required")
	}
}

// adminInit bootstraps the platform administrator unless one exists.
func (a *app) adminInit(ctx context.Context, pool *pgxpool.Pool, username, password, tenant, namespace string) error {
	exists, err := platformAdminExists(ctx, pool)
	if err != nil {
		return err
	}
	if exists {
		return writeLine(a.stdout, alreadyInitialized)
	}
	// The installer only uses the transaction it is given.
	installer := policysvc.NewService(pool, nil, nil, nil, a.logger())
	userID, err := auth.BootstrapPlatformAdmin(ctx, pool, username, password, tenant, namespace, installer)
	if err != nil {
		if e, ok := apperr.As(err); ok && e.Code == connect.CodeFailedPrecondition {
			// A concurrent run may have won the race.
			if again, cerr := platformAdminExists(ctx, pool); cerr == nil && again {
				return writeLine(a.stdout, alreadyInitialized)
			}
		}
		return fmt.Errorf("bootstrap platform administrator: %w", err)
	}
	a.notifyNamespace(ctx, pool, tenant, namespace)
	return writeLine(a.stdout, "created platform administrator %s (%s) in tenant %s, namespace %s", auth.NormalizeUsername(username), userID, tenant, namespace)
}

func platformAdminExists(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE is_platform_admin)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check platform administrators: %w", err)
	}
	return exists, nil
}

// notifyNamespace asks running instances (best effort, when Redis is
// configured) to reload the catalog snapshot of a namespace created by the CLI.
func (a *app) notifyNamespace(ctx context.Context, pool *pgxpool.Pool, tenant, namespace string) {
	logger := a.logger()
	rdb, keys, err := a.optionalRedis(ctx)
	if err != nil {
		logger.Warn("running instances were not notified; they pick up the namespace within a minute", "error", err)
		return
	}
	if rdb == nil {
		return
	}
	defer rdb.Close()
	_, nsID, err := lookupNamespace(ctx, pool, tenant, namespace)
	if err != nil {
		logger.Warn("running instances were not notified", "error", err)
		return
	}
	if err := publishCatalogInvalidation(ctx, events.NewRedisBus(rdb, keys, "", logger), nsID); err != nil {
		logger.Warn("running instances were not notified; they pick up the namespace within a minute", "error", err)
	}
}

// publishCatalogInvalidation publishes the catalog invalidation event of a namespace.
func publishCatalogInvalidation(ctx context.Context, bus events.Bus, namespaceID string) error {
	data, err := json.Marshal(map[string]string{"ns": namespaceID})
	if err != nil {
		return fmt.Errorf("encode catalog event: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return bus.Publish(pctx, events.ChannelCatalog, events.Event{
		Type: catalog.InvalidateEventType, NamespaceID: namespaceID, At: time.Now().UTC(), Data: data,
	})
}
