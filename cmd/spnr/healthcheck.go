package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

func newHealthcheckCmd(a *app) *cobra.Command {
	var (
		target  string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe an HTTP health endpoint (exit 0 on 2xx)",
		Long: "Send GET to --url and exit with status 0 when the response status is 2xx, 1 otherwise.\n" +
			"Intended for container HEALTHCHECK instructions in images without a shell.",
		Example: "  spnr healthcheck --url http://127.0.0.1:8080/readyz",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := probe(cmd.Context(), target, timeout); err != nil {
				return &exitError{code: 1, err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "url", "http://127.0.0.1:8080/readyz", "URL to probe")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "request timeout")
	return cmd
}

// probe performs one health check request.
func probe(ctx context.Context, target string, timeout time.Duration) error {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid --url %q: an absolute http(s) URL is required", target)
	}
	if timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{
		// Health endpoints never redirect; do not follow redirects to other hosts.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("probe %s: status %d", u.Redacted(), resp.StatusCode)
	}
	return nil
}

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the server configuration",
	}
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate the SPINNERET_* environment and print it with secrets redacted",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.fullConfig()
			if err != nil {
				return err
			}
			enc := json.NewEncoder(a.stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg.Redacted())
		},
	}
	cmd.AddCommand(check)
	return cmd
}
