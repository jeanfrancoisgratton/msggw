// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:08:41
// Original filename: src/cmd/config.go

package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"msggw/internal/config"
	"msggw/internal/secrets"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and validate the configuration",
}

var configSampleCmd = &cobra.Command{
	Use:   "sample",
	Short: "Print a sample configuration file",
	Long: `Print a sample configuration file on standard output.

  msg-gw config sample > /etc/msggw/config.json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprint(cmd.OutOrStdout(), config.Sample())
		return err
	},
}

var configCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Validate the configuration and resolve its secrets",
	Long: `Validate the configuration file and check that every secret it refers to can
actually be read. Nothing is sent to Mattermost or to Google.

Secret values are never printed: only where each one comes from, and whether it
could be read.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "configuration : %s\n", cfg.Path())
		fmt.Fprintf(out, "state dir     : %s\n", cfg.StateDir)
		fmt.Fprintf(out, "db driver     : %s\n", cfg.Backend.Driver)

		var problems []error

		if cfg.Backend.Driver == config.DatabaseDriverPostgres {
			fmt.Fprintf(out, "database      : (see backend.postgres.dsn_ref below)\n")
		} else if path, err := cfg.SQLitePath(); err != nil {
			fmt.Fprintf(out, "database      : BAD REFERENCE — %v\n", err)
			problems = append(problems, err)
		} else {
			fmt.Fprintf(out, "database      : %s\n", path)
		}

		if url, err := cfg.MattermostURL(); err != nil {
			fmt.Fprintf(out, "mattermost    : BAD REFERENCE — %v\n", err)
			problems = append(problems, err)
		} else {
			fmt.Fprintf(out, "mattermost    : %s\n", url)
		}

		fmt.Fprintf(out, "default route : %s\n", cfg.Routing.Default.String())
		fmt.Fprintf(out, "routing rules : %d\n", len(cfg.Routing.Rules))
		fmt.Fprintf(out, "threads       : %v\n", cfg.ThreadPerConversationEnabled())

		fmt.Fprintln(out)
		problems = append(problems, checkSecret(cmd, "mattermost token", cfg.Mattermost.TokenRef, cfg, true))
		problems = append(problems, checkSecret(cmd, "gmessages session", cfg.GMessages.SessionRef, cfg, false))
		if cfg.Backend.Driver == config.DatabaseDriverPostgres {
			problems = append(problems, checkSecret(cmd, "database dsn", cfg.Backend.Postgres.DSNRef, cfg, true))
		}

		for _, problem := range problems {
			if problem != nil {
				return fmt.Errorf("the configuration is not usable as it stands")
			}
		}

		fmt.Fprintln(out, "\nthe configuration is valid")
		return nil
	},
}

// checkSecret reports whether a secret reference resolves and can be read.
// A session that does not exist yet is not a problem — that is what pairing is
// for — so mustExist distinguishes the two cases.
func checkSecret(cmd *cobra.Command, label, ref string, cfg *config.Config, mustExist bool) error {
	out := cmd.OutOrStdout()

	store, err := secrets.Open(ref, cfg.Vault)
	if err != nil {
		fmt.Fprintf(out, "%-18s: BAD REFERENCE — %v\n", label, err)
		return err
	}

	value, err := store.Load()
	switch {
	case err == nil && len(value) > 0:
		fmt.Fprintf(out, "%-18s: %s (%d bytes)\n", label, store.Describe(), len(value))
		return nil
	case err == nil, errors.Is(err, secrets.ErrNotFound):
		if mustExist {
			fmt.Fprintf(out, "%-18s: %s — EMPTY\n", label, store.Describe())
			return fmt.Errorf("%s is empty", label)
		}
		fmt.Fprintf(out, "%-18s: %s — not stored yet, run \"msg-gw pair\"\n", label, store.Describe())
		return nil
	default:
		fmt.Fprintf(out, "%-18s: %s — UNREADABLE: %v\n", label, store.Describe(), err)
		return err
	}
}

func init() {
	configCmd.AddCommand(configSampleCmd, configCheckCmd)
}
