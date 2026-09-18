package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/TikHub/Spinneret/internal/vault"
)

// rewrapPollInterval is how often kek rewrap prints progress.
const rewrapPollInterval = time.Second

func newKEKCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kek",
		Short: "Manage key-encryption keys",
		Long: "Generate key-encryption keys (KEKs), inspect which KEKs wrap stored data keys and\n" +
			"re-wrap every data key with the current KEK after a rotation.\n\n" +
			"Rotation: add the new key to SPINNERET_KEK_FILE / SPINNERET_KEKS, make it current\n" +
			"(SPINNERET_KEK_CURRENT or list it last), restart the instances, run `spnr kek rewrap`,\n" +
			"then remove the old key once `spnr kek status` shows no records wrapped with it.",
	}

	var id string
	generate := &cobra.Command{
		Use:   "generate",
		Short: "Print a new random KEK line (id:base64)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if !vault.ValidKEKID(id) {
				return fmt.Errorf("invalid --id %q", id)
			}
			key, err := vault.GenerateKEK()
			if err != nil {
				return fmt.Errorf("generate kek: %w", err)
			}
			return writeLine(a.stdout, "%s:%s", id, key)
		},
	}
	generate.Flags().StringVar(&id, "id", "k1", "key id")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show configured KEKs, wrapped record counts and rewrap progress",
		Long:  "Requires SPINNERET_DATABASE_URL and the KEK settings (SPINNERET_KEK_FILE / SPINNERET_KEKS).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withRewrapper(cmd.Context(), func(ctx context.Context, r *vault.Rewrapper) error {
				st, err := r.Status(ctx)
				if err != nil {
					return err
				}
				return printKEKStatus(a.stdout, st)
			})
		},
	}

	rewrap := &cobra.Command{
		Use:   "rewrap",
		Short: "Re-wrap every data key with the current KEK and wait until done",
		Long: "Start the rewrap job (or follow the one already running on another instance) and print\n" +
			"progress until it finished. Exits non-zero when the job reported errors.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.withRewrapper(cmd.Context(), func(ctx context.Context, r *vault.Rewrapper) error {
				return runRewrap(ctx, r, a.stdout, rewrapPollInterval)
			})
		},
	}
	cmd.AddCommand(generate, status, rewrap)
	return cmd
}

// withRewrapper opens the database and the KEK provider for fn.
func (a *app) withRewrapper(ctx context.Context, fn func(ctx context.Context, r *vault.Rewrapper) error) error {
	file, keys, current := a.env("SPINNERET_KEK_FILE"), a.env("SPINNERET_KEKS"), a.env("SPINNERET_KEK_CURRENT")
	if file == "" && keys == "" {
		return errors.New("SPINNERET_KEK_FILE or SPINNERET_KEKS is required")
	}
	provider, err := vault.LoadLocalKEKProvider(file, keys, current)
	if err != nil {
		return fmt.Errorf("load key-encryption keys: %w", err)
	}
	pool, err := a.openDatabase(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	r := vault.NewRewrapper(pool, vault.NewCipher(provider, 10_000, 10*time.Minute), a.logger())
	defer r.Close()
	return fn(ctx, r)
}

// kekRewrapper is the subset of *vault.Rewrapper used by runRewrap.
type kekRewrapper interface {
	Start(ctx context.Context) (bool, error)
	Status(ctx context.Context) (vault.KEKStatus, error)
}

// runRewrap starts (or follows) the rewrap job and prints progress until it is done.
func runRewrap(ctx context.Context, r kekRewrapper, out io.Writer, poll time.Duration) error {
	started, err := r.Start(ctx)
	if err != nil {
		return fmt.Errorf("start kek rewrap: %w", err)
	}
	if started {
		if err := writeLine(out, "kek rewrap started"); err != nil {
			return err
		}
	} else if err := writeLine(out, "kek rewrap already running on another instance; following its progress"); err != nil {
		return err
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		st, err := r.Status(ctx)
		if err != nil {
			return fmt.Errorf("read kek rewrap status: %w", err)
		}
		if err := writeLine(out, "progress: %d/%d records re-wrapped to %s", st.Done, st.Total, st.CurrentKEKID); err != nil {
			return err
		}
		if !st.Running {
			if st.LastError != "" {
				return fmt.Errorf("kek rewrap finished with errors: %s", st.LastError)
			}
			return writeLine(out, "kek rewrap finished")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("kek rewrap interrupted (the job stops with this process): %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// printKEKStatus renders a KEK status report.
func printKEKStatus(out io.Writer, st vault.KEKStatus) error {
	lines := []string{fmt.Sprintf("current kek: %s", st.CurrentKEKID)}
	for _, k := range st.KEKs {
		flags := ""
		if k.Current {
			flags += " current"
		}
		if !k.Configured {
			flags += " NOT-CONFIGURED"
		}
		lines = append(lines, fmt.Sprintf("  %-16s wrapped_records=%d%s", k.ID, k.WrappedRecords, flags))
	}
	job := "idle"
	if st.Running {
		job = "running"
	}
	lines = append(lines, fmt.Sprintf("rewrap: %s (%d/%d)", job, st.Done, st.Total))
	if st.LastFinishedAt != nil {
		lines = append(lines, fmt.Sprintf("last finished: %s", st.LastFinishedAt.UTC().Format(time.RFC3339)))
	}
	if st.LastError != "" {
		lines = append(lines, fmt.Sprintf("last error: %s", st.LastError))
	}
	for _, l := range lines {
		if err := writeLine(out, "%s", l); err != nil {
			return err
		}
	}
	return nil
}
