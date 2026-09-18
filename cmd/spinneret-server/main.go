// Command spinneret-server runs a Spinneret control-plane instance.
//
// Configuration comes from SPINNERET_* environment variables (see
// documents/en/03-configuration.md); flags override a few of them.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/TikHub/Spinneret/cmd/internal/buildinfo"
	"github.com/TikHub/Spinneret/internal/appconfig"
	"github.com/TikHub/Spinneret/internal/server"
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

// options are the parsed command-line flags.
type options struct {
	role        string
	migrate     bool
	showVersion bool
}

// parseFlags parses the command line. errHelp is returned for -h/--help.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("spinneret-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.role, "role", "", "instance role: all, api or worker (overrides SPINNERET_ROLE)")
	fs.BoolVar(&o.migrate, "migrate", false, "apply pending database migrations at startup")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(fs.Output(), "Usage: spinneret-server [--role all|api|worker] [--migrate] [--version]\n\n"+
			"Runs a Spinneret instance configured through SPINNERET_* environment variables.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	return o, nil
}

// withRole returns an environment lookup in which a non-empty role overrides SPINNERET_ROLE.
func withRole(lookup func(string) (string, bool), role string) func(string) (string, bool) {
	if role == "" {
		return lookup
	}
	return func(key string) (string, bool) {
		if key == "SPINNERET_ROLE" {
			return role, true
		}
		return lookup(key)
	}
}

// run executes the command and returns the process exit code.
func run(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "spinneret-server: %v\n", err)
		return 2
	}
	buildVersion := buildinfo.Version()
	if o.showVersion {
		_, _ = fmt.Fprintf(stdout, "spinneret-server %s\n", buildVersion)
		return 0
	}
	cfg, err := appconfig.LoadFrom(withRole(lookup, o.role))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "spinneret-server: invalid configuration:\n%v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		// Restore the default signal behavior after the first signal so that a
		// second one terminates a shutdown that takes too long.
		<-ctx.Done()
		stop()
	}()

	srv, err := server.New(ctx, cfg, server.Options{Version: buildVersion, Migrate: o.migrate})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "spinneret-server: startup failed: %v\n", err)
		return 1
	}
	if err := srv.Run(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "spinneret-server: %v\n", err)
		return 1
	}
	return 0
}
