// Command spnr is the Spinneret administration CLI: database migrations,
// bootstrap of the first administrator, API tokens, hot-state rebuilds, KEK
// management, load-test seeding and container health checks.
//
// It reads the same SPINNERET_* environment variables as spinneret-server.
// Commands that only need PostgreSQL (migrate, admin init, token create)
// require only SPINNERET_DATABASE_URL.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Evil0ctal/Spinneret/cmd/internal/buildinfo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], &app{lookup: os.LookupEnv, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr})
	stop()
	os.Exit(code)
}

// exitError carries a specific exit code (for example healthcheck failures).
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// execute runs the CLI and returns the process exit code.
func execute(ctx context.Context, args []string, a *app) int {
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil && ee.err.Error() != "" {
				_, _ = fmt.Fprintf(a.stderr, "spnr: %v\n", ee.err)
			}
			return ee.code
		}
		_, _ = fmt.Fprintf(a.stderr, "spnr: %v\n", err)
		return 1
	}
	return 0
}

// newRootCmd builds the command tree.
func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "spnr",
		Short: "Spinneret administration CLI",
		Long: "spnr administers a Spinneret deployment: database migrations, the first administrator,\n" +
			"API tokens, hot-state rebuilds, key-encryption keys and load-test data.\n\n" +
			"It reads the same SPINNERET_* environment variables as spinneret-server.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildinfo.Version(),
	}
	root.SetVersionTemplate("spnr {{.Version}}\n")
	root.PersistentFlags().StringVar(&a.logLevel, "log-level", "warn", "log level of diagnostic output on stderr (debug, info, warn, error)")
	root.AddCommand(
		newMigrateCmd(a),
		newAdminCmd(a),
		newTokenCmd(a),
		newRebuildCmd(a),
		newKEKCmd(a),
		newSeedCmd(a),
		newHealthcheckCmd(a),
		newVersionCmd(a),
		newConfigCmd(a),
	)
	return root
}

// newVersionCmd prints the build version.
func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the spnr version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintf(a.stdout, "spnr %s\n", buildinfo.Version())
			return err
		},
	}
}

// writeLine writes one line to w, returning the write error.
func writeLine(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format+"\n", args...)
	return err
}
