// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:12
// Original filename: src/cmd/daemon.go

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"msggw/internal/bridge"
	"msggw/internal/gmessages"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the bridge",
	Long: `Run the bridge: connect to Google Messages with the stored session, connect to
Mattermost as the configured bot, and pass messages both ways until stopped.

SIGINT and SIGTERM shut it down cleanly, persisting the Google Messages session
so the next start does not need to re-pair.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)
		log.Info("starting msg-gw", "config", cfg.Path(), "state_dir", cfg.StateDir)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		db, err := openStorage(ctx, cfg)
		if err != nil {
			return err
		}
		defer db.Close()

		mm, err := newMattermost(ctx, cfg, log)
		if err != nil {
			return err
		}

		gmCfg, err := newGMessagesConfig(cfg, log)
		if err != nil {
			return err
		}
		gm, err := gmessages.New(gmCfg)
		if err != nil {
			return err
		}

		if err := gm.Connect(ctx); err != nil {
			return err
		}
		// The session's auth token is refreshed while the daemon runs; saving
		// on the way out keeps the very last one, which the periodic save on
		// refresh may not have covered if the refresh was recent.
		defer func() {
			gm.Disconnect()
			if err := gm.SaveSession(); err != nil {
				log.Error("could not persist the Google Messages session on shutdown", "error", err)
			}
		}()

		br, err := bridge.New(cfg, log, db, gm, mm)
		if err != nil {
			return err
		}

		log.Info("bridge running",
			"mattermost_bot", mm.BotUsername(),
			"default_route", cfg.Routing.Default.String(),
			"routing_rules", len(cfg.Routing.Rules),
			"threads", cfg.ThreadPerConversationEnabled())

		return br.Run(ctx)
	},
}
