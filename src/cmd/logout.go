// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:52
// Original filename: src/cmd/logout.go

package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/gmessages"
)

var logoutLocalOnly bool

var logoutCmd = &cobra.Command{
	Use:   "logout NAME",
	Short: "Unpair the daemon from Google Messages",
	Long: `Revoke the pairing on the phone and delete the stored session for the user
named NAME (a "name" entry under "users" in the configuration).

With --local-only the phone is left alone and only the stored session is
deleted. That is what to use when the pairing has already been revoked from the
phone, or when the session is too broken to connect with.

The SQLite mappings are not touched: re-pairing the same phone keeps the
existing Mattermost threads.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)
		out := cmd.OutOrStdout()

		user, err := findUser(cfg, args[0])
		if err != nil {
			return err
		}

		gmCfg, err := newGMessagesConfig(user, cfg, log)
		if err != nil {
			return err
		}

		if logoutLocalOnly {
			if err := gmCfg.Session.Clear(); err != nil {
				return err
			}
			fmt.Fprintf(out, "Deleted the session at %s.\n", gmCfg.Session.Describe())
			fmt.Fprintln(out, "The pairing is still active on the phone; remove it there under Settings -> Device pairing.")
			return nil
		}

		client, err := gmessages.New(gmCfg)
		if errors.Is(err, gmessages.ErrNotPaired) {
			fmt.Fprintln(out, "There is no stored session: nothing to do.")
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w (use --local-only to delete the stored session anyway)", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Revoking needs a live session, so connect first. A failure here is
		// not fatal: Unpair clears the local session either way.
		if err := client.Connect(ctx); err != nil {
			log.Warn("could not connect before unpairing", "error", err)
		}

		if err := client.Unpair(ctx); err != nil {
			return err
		}

		fmt.Fprintf(out, "Unpaired. The session at %s has been deleted.\n", gmCfg.Session.Describe())
		return nil
	},
}

func init() {
	logoutCmd.Flags().BoolVar(&logoutLocalOnly, "local-only", false,
		"delete the stored session without revoking the pairing on the phone")
}
