// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:09:12
// Original filename: src/cmd/daemon.go

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/bridge"
	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/listener"
)

// When the daemon starts before "msg-gw pair" has ever been run, it retries
// instead of failing outright, so a container that starts the daemon and the
// pairing command separately has a window to pair before the daemon gives up.
const (
	unpairedStartupRetries  = 5
	unpairedStartupInterval = 60 * time.Second
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
		gm, err := newGMessagesClientWithRetry(ctx, log, gmCfg)
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

		if cfg.Listener.Port != 0 {
			if err := startListener(ctx, cfg, log); err != nil {
				return err
			}
		}

		log.Info("bridge running",
			"mattermost_bot", mm.BotUsername(),
			"default_route", cfg.Routing.Default.String(),
			"routing_rules", len(cfg.Routing.Rules),
			"threads", cfg.ThreadPerConversationEnabled())

		return br.Run(ctx)
	},
}

// startListener brings up the HTTP(S) listener (currently just a health
// check — see internal/listener) in the background, for client-mode pairing
// once it exists. It runs for the lifetime of ctx; a failure after startup
// is logged rather than brought down the bridge with it, since the listener
// is not on the message path.
func startListener(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	lst, err := listener.New(listener.Config{
		Port:     cfg.Listener.Port,
		CertFile: cfg.Listener.CertFile,
		KeyFile:  cfg.Listener.KeyFile,
	}, listener.DefaultHandler(), log)
	if err != nil {
		return fmt.Errorf("starting the listener: %w", err)
	}

	go func() {
		if err := lst.Run(ctx); err != nil {
			log.Error("listener stopped", "error", err)
		}
	}()
	return nil
}

// newGMessagesClientWithRetry builds the Google Messages client, retrying
// while no session has been paired yet. This gives an operator who starts the
// daemon and "msg-gw pair" as separate steps (e.g. two container commands) a
// window to pair before the daemon gives up for good.
func newGMessagesClientWithRetry(ctx context.Context, log *slog.Logger, gmCfg gmessages.Config) (*gmessages.Client, error) {
	for attempt := 1; ; attempt++ {
		gm, err := gmessages.New(gmCfg)
		if err == nil {
			return gm, nil
		}
		if !errors.Is(err, gmessages.ErrNotPaired) || attempt >= unpairedStartupRetries {
			return nil, err
		}

		log.Warn("not paired with Google Messages yet, will retry",
			"attempt", attempt, "max_attempts", unpairedStartupRetries, "retry_in", unpairedStartupInterval)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(unpairedStartupInterval):
		}
	}
}
