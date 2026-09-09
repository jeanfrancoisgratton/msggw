// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/backfill.go

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"msggw/internal/config"
)

var backfillCount int

var backfillCmd = &cobra.Command{
	Use:   "backfill NAME",
	Short: "Change a user's first-bridge backfill window",
	Long: `Change how much history "msg-gw" posts to Mattermost the first time each of
NAME's conversations is bridged, without hand-editing config.json.

  --backlog N, -b N   fetch the last N messages of a conversation's history
                      (0 disables backfill)

Applies only to conversations bridged after this change — an already-bridged
conversation keeps whatever history it started with.

Example:

  msg-gw backfill jfgratton --backlog 50

Fetching runs synchronously the first time each conversation is seen, one
message (and its attachments) at a time, and blocks the bridge from handling
any other message while it does — a long backlog or a media-heavy history
will visibly stall live traffic on that first run.

The change is validated the same way "msg-gw config check" validates
config.json, and only written if the result still loads cleanly — but the
running daemon does not pick it up until it is reloaded (see "msg-gw
reload").`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		if !cmd.Flags().Changed("backlog") {
			return fmt.Errorf("nothing to change: pass --backlog")
		}

		newCfg, err := setBackfill(cfg, args[0], backfillCount)
		if err != nil {
			return err
		}

		user, err := findUser(newCfg, args[0])
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%s: backfill_count=%d\n", args[0], user.GMessages.BackfillCount)
		fmt.Fprintln(out, `Run "msg-gw reload" to pick up the change.`)
		return nil
	},
}

// setBackfill applies the requested backfill count to name's entry via
// config.Mutate, so the write is validated and atomic the same way "msg-gw
// rules" is.
func setBackfill(cfg *config.Config, name string, count int) (*config.Config, error) {
	if _, err := findUser(cfg, name); err != nil {
		return nil, err
	}

	return config.Mutate(cfg.Path(), func(c *config.Config) error {
		for i := range c.Users {
			if c.Users[i].Name != name {
				continue
			}
			c.Users[i].GMessages.BackfillCount = count
			return nil
		}
		return fmt.Errorf("no user named %q in %s", name, cfg.Path())
	})
}

func init() {
	backfillCmd.Flags().IntVarP(&backfillCount, "backlog", "b", 0,
		"messages to backfill on first bridge (0 disables)")
}
