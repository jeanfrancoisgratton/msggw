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
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"msggw/internal/bridge"
	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/listener"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// When a user starts before "msg-gw pair NAME" has ever been run for them, it
// retries instead of failing outright, so a container that starts the daemon
// and the pairing command separately has a window to pair before that user's
// bridge gives up.
const (
	unpairedStartupRetries  = 5
	unpairedStartupInterval = 60 * time.Second
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the bridge",
	Long: `Run the bridge: connect every configured user to Google Messages with their
stored session, connect once to Mattermost as the shared bot, and pass
messages both ways until stopped.

Each user in "users" runs independently, in its own goroutine. A user that
cannot connect (not paired yet, a broken session, ...) is logged and skipped —
it does not stop the other users' bridges, or the daemon itself, from running.

SIGINT and SIGTERM shut it down cleanly, persisting every connected user's
Google Messages session so the next start does not need to re-pair.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)
		log.Info("starting msg-gw", "config", cfg.Path(), "state_dir", cfg.StateDir, "users", len(cfg.Users))

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

		if cfg.Listener.Port != 0 {
			if err := startListener(ctx, cfg, log); err != nil {
				return err
			}
		}

		log.Info("mattermost connected", "bot", mm.BotUsername())

		var wg sync.WaitGroup
		for _, user := range cfg.Users {
			wg.Add(1)
			go func(user config.UserConfig) {
				defer wg.Done()
				runUser(ctx, cfg, user, log, db, mm)
			}(user)

		}
		wg.Wait()
		return nil
	},
}

// runUser brings up and runs one tenant's bridge for the lifetime of ctx.
//
// Every failure here — pairing never having happened, a broken session, the
// bridge itself erroring out — is logged with the tenant's name and simply
// ends this goroutine. It never propagates to the other tenants or to the
// daemon as a whole: one person's session being broken must not take down
// everyone else's working bridge.
func runUser(ctx context.Context, cfg *config.Config, user config.UserConfig, log *slog.Logger, db *storage.DB, mm *mattermost.Client) {
	log = log.With("user", user.Name)

	gmCfg, err := newGMessagesConfig(user, cfg, log)
	if err != nil {
		log.Error("misconfigured, not starting", "error", err)
		return
	}
	gm, err := newGMessagesClientWithRetry(ctx, log, gmCfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Error("could not start", "error", err)
		return
	}

	if err := gm.Connect(ctx); err != nil {
		log.Error("could not connect to Google Messages", "error", err)
		return
	}
	// The session's auth token is refreshed while the daemon runs; saving on
	// the way out keeps the very last one, which the periodic save on refresh
	// may not have covered if the refresh was recent.
	defer func() {
		gm.Disconnect()
		if err := gm.SaveSession(); err != nil {
			log.Error("could not persist the Google Messages session on shutdown", "error", err)
		}
	}()

	br, err := bridge.New(user, log, db, gm, mm)
	if err != nil {
		log.Error("could not start the bridge", "error", err)
		return
	}

	log.Info("bridge running",
		"default_route", user.Routing.Default.String(),
		"routing_rules", len(user.Routing.Rules),
		"threads", user.Routing.ThreadPerConversationEnabled())

	if err := br.Run(ctx); err != nil {
		log.Error("the bridge stopped", "error", err)
	}
}

func startListener(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	mux := listener.DefaultHandler()
	pairSrv := newPairServer(cfg, log)
	pairSrv.mount(mux)
	go pairSrv.sweep(ctx)

	lst, err := listener.New(listener.Config{
		Port:     cfg.Listener.Port,
		CertFile: cfg.Listener.CertFile,
		KeyFile:  cfg.Listener.KeyFile,
	}, mux, log)
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
// daemon and "msg-gw pair NAME" as separate steps (e.g. two container
// commands) a window to pair before this user's bridge gives up.
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
