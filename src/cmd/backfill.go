// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/cmd/backfill.go

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"msggw/internal/config"
)

var (
	backfillDays  int
	backfillCount int
)

var backfillCmd = &cobra.Command{
	Use:   "backfill NAME",
	Short: "Change a user's first-bridge backfill window",
	Long: `Change how much history "msg-gw" posts to Mattermost the first time each of
NAME's conversations is bridged, without hand-editing config.json.

  --days N    fetch the last N days of a conversation's history (default 7;
              0 disables day-based backfill)
  --count N   fetch the last N messages instead, regardless of age (0
              disables it too; only consulted when --days resolves to 0)

Only the flag(s) you actually pass are changed; whichever setting you leave
out keeps its current value. See "msg-gw config sample" and
docs/CONFIGURATION.md#gmessages for how the two settings interact.

Both apply only to conversations bridged after this change — an
already-bridged conversation keeps whatever history it started with.

Example:

  msg-gw backfill jfgratton --days 14

Fetching runs synchronously the first time each conversation is seen, one
message (and its attachments) at a time, and blocks the bridge from handling
any other message while it does — a wide window or a media-heavy history will
visibly stall live traffic on that first run.

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

		daysChanged := cmd.Flags().Changed("days")
		countChanged := cmd.Flags().Changed("count")
		if !daysChanged && !countChanged {
			return fmt.Errorf("nothing to change: pass --days and/or --count")
		}

		newCfg, err := setBackfill(cfg, args[0], daysChanged, backfillDays, countChanged, backfillCount)
		if err != nil {
			return err
		}

		user, err := findUser(newCfg, args[0])
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%s: backfill_days=%d backfill_count=%d\n",
			args[0], user.GMessages.BackfillDaysCount(), user.GMessages.BackfillCount)
		fmt.Fprintln(out, `Run "msg-gw reload" to pick up the change.`)
		return nil
	},
}

// setBackfill applies the requested backfill changes to name's entry via
// config.Mutate, so the write is validated and atomic the same way "msg-gw
// rules" is. Only the fields whose *Changed flag is true are touched.
func setBackfill(cfg *config.Config, name string, daysChanged bool, days int, countChanged bool, count int) (*config.Config, error) {
	if _, err := findUser(cfg, name); err != nil {
		return nil, err
	}

	return config.Mutate(cfg.Path(), func(c *config.Config) error {
		for i := range c.Users {
			if c.Users[i].Name != name {
				continue
			}
			if daysChanged {
				d := days
				c.Users[i].GMessages.BackfillDays = &d
			}
			if countChanged {
				c.Users[i].GMessages.BackfillCount = count
			}
			return nil
		}
		return fmt.Errorf("no user named %q in %s", name, cfg.Path())
	})
}

func init() {
	backfillCmd.Flags().IntVar(&backfillDays, "days", config.DefaultBackfillDays,
		"days of conversation history to backfill on first bridge (0 disables day-based backfill)")
	backfillCmd.Flags().IntVar(&backfillCount, "count", 0,
		"messages to backfill on first bridge instead, if --days resolves to 0 (0 disables)")
}
