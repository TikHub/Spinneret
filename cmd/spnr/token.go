package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/Evil0ctal/Spinneret/internal/auth"
	"github.com/Evil0ctal/Spinneret/internal/authz"
	"github.com/Evil0ctal/Spinneret/internal/pkg/durationx"
)

// maxTokenDescription bounds --description (the console enforces the same limit).
const maxTokenDescription = 512

func newTokenCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens",
	}
	var in tokenCreateInput
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an API token and print its secret",
		Long: "Create an API token bound to one tenant and namespace. Only the plaintext token is\n" +
			"printed on stdout (it cannot be retrieved later). Only SPINNERET_DATABASE_URL is required.\n\n" +
			"Scopes: lease:acquire[:<site>], report:write[:<site>], config:read[:<group glob>],\n" +
			"config:publish[:<group glob>], secret:read:<namespace>/<path glob>, identity:write[:<site>],\n" +
			"proxy:write, admin.",
		Example: "  spnr token create --tenant default --namespace default --name crawler-hk \\\n" +
			"    --scope lease:acquire --scope report:write --scope config:read --expires 720h",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			expiresAt, err := in.validate(time.Now())
			if err != nil {
				return err
			}
			return a.withDatabase(cmd.Context(), func(ctx context.Context, pool *pgxpool.Pool) error {
				plaintext, err := createToken(ctx, pool, in, expiresAt)
				if err != nil {
					return err
				}
				return writeLine(a.stdout, "%s", plaintext)
			})
		},
	}
	f := create.Flags()
	f.StringVar(&in.tenant, "tenant", "default", "tenant name")
	f.StringVar(&in.namespace, "namespace", "default", "namespace name")
	f.StringVar(&in.name, "name", "", "token name, unique in the namespace (required)")
	f.StringArrayVar(&in.scopes, "scope", nil, "scope granted to the token (repeatable, at least one)")
	f.StringVar(&in.expires, "expires", "720h", `lifetime such as "720h" or "30d"; "0" or "never" for no expiry`)
	f.StringVar(&in.description, "description", "", "free-form description")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("scope")
	cmd.AddCommand(create)
	return cmd
}

// tokenCreateInput holds the flags of token create.
type tokenCreateInput struct {
	tenant, namespace, name, expires, description string
	scopes                                        []string
}

// validate checks the flags and returns the expiry (nil = never).
func (in *tokenCreateInput) validate(now time.Time) (*time.Time, error) {
	if strings.TrimSpace(in.name) == "" {
		return nil, errors.New("--name is required")
	}
	if len(in.scopes) == 0 {
		return nil, errors.New("at least one --scope is required")
	}
	if _, err := authz.ParseScopes(in.scopes); err != nil {
		return nil, fmt.Errorf("invalid --scope: %w", err)
	}
	if len(in.description) > maxTokenDescription {
		return nil, fmt.Errorf("--description must be at most %d bytes", maxTokenDescription)
	}
	return parseExpiry(in.expires, now)
}

// parseExpiry converts an --expires value into an absolute time (nil = never).
func parseExpiry(v string, now time.Time) (*time.Time, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "never", "permanent":
		return nil, nil
	}
	d, err := durationx.Parse(v)
	if err != nil || d.IsPermanent() || d.Std() <= 0 {
		return nil, fmt.Errorf("invalid --expires %q: use a positive duration such as 720h or 30d", v)
	}
	at := now.Add(d.Std()).UTC()
	return &at, nil
}

// createToken creates the token and sets its description.
func createToken(ctx context.Context, pool *pgxpool.Pool, in tokenCreateInput, expiresAt *time.Time) (string, error) {
	tenantID, namespaceID, err := lookupNamespace(ctx, pool, in.tenant, in.namespace)
	if err != nil {
		return "", err
	}
	plaintext, id, err := auth.CreateTokenDirect(ctx, pool, tenantID, namespaceID, in.name, in.scopes, expiresAt)
	if err != nil {
		return "", fmt.Errorf("create token: %w", err)
	}
	if in.description != "" {
		if _, err := pool.Exec(ctx, `UPDATE api_tokens SET description = $2 WHERE id = $1`, id, in.description); err != nil {
			return "", fmt.Errorf("set description of token %s: %w", id, err)
		}
	}
	return plaintext, nil
}
