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
	"strconv"
	"strings"
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
Google Messages session so the next start does not need to re-pair.

SIGHUP — or "msg-gw reload" — re-reads the configuration file and, if it is
still valid, restarts every user's bridge, the Mattermost connection and the
listener against it, without leaving this process or losing its PID. This is
what makes a config or rules change take effect in a container started with
"exec message-gateway daemon", where there is no supervisor to restart the
process for you. An invalid configuration is rejected and logged; the daemon
keeps running on the configuration it already had.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		log := newLogger(cfg)
		log.Info("starting msg-gw", "config", cfg.Path(), "state_dir", cfg.StateDir, "users", len(cfg.Users))

		// Signal handling is armed before the pid file is written: once that
		// file exists, "msg-gw reload" (or anyone else) may signal this PID
		// immediately, and a SIGHUP with no handler yet installed would kill
		// the daemon outright instead of reloading it.
		rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)

		if err := acquirePIDFile(cfg); err != nil {
			return err
		}
		defer removePIDFile(cfg, log)

		for {
			genCtx, cancelGen := context.WithCancel(rootCtx)
			genDone := make(chan error, 1)
			go func(cfg *config.Config) {
				genDone <- runGeneration(genCtx, cfg, log)
			}(cfg)

		waitGeneration:
			for {
				select {
				case <-rootCtx.Done():
					cancelGen()
					<-genDone
					return nil

				case err := <-genDone:
					// The generation ended on its own — every user's bridge
					// exited (e.g. none of them are paired yet, or a fatal
					// setup error) — not because of a shutdown or a reload.
					cancelGen()
					return err

				case <-hup:
					log.Info("reload requested (SIGHUP)", "config", cfg.Path())
					newCfg, err := config.Load(cfg.Path())
					if err != nil {
						log.Error("reload: the configuration is invalid, keeping the current one running",
							"config", cfg.Path(), "error", err)
						continue waitGeneration
					}
					log.Info("reload: configuration is valid, restarting against it")
					cancelGen()
					<-genDone
					cfg = newCfg
					break waitGeneration
				}
			}
		}
	},
}

// runGeneration brings up storage, the Mattermost connection, the optional
// listener and every configured user's bridge against cfg, and runs them
// until ctx is cancelled. It is the entire daemon for one "generation" of the
// configuration: daemon's RunE calls it once at startup, and once more per
// successful reload, each time against a freshly loaded *config.Config and a
// context scoped to that generation alone.
func runGeneration(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	db, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	mm, err := newMattermost(ctx, cfg, log)
	if err != nil {
		return err
	}

	var wg sync.WaitGroup

	if cfg.Listener.Port != 0 {
		if err := startListener(ctx, cfg, log, &wg); err != nil {
			return err
		}
	}

	log.Info("mattermost connected", "bot", mm.BotUsername())

	for _, user := range cfg.Users {
		wg.Add(1)
		go func(user config.UserConfig) {
			defer wg.Done()
			runUser(ctx, cfg, user, log, db, mm)
		}(user)
	}
	wg.Wait()
	return nil
}

// acquirePIDFile records this process's PID at cfg.PIDFile(), refusing to
// start if another daemon using the same state_dir is already alive — two
// daemons sharing one SQLite file and one set of Google Messages sessions
// would corrupt both. A stale file left behind by a daemon that did not shut
// down cleanly (e.g. SIGKILL) is silently overwritten, since its PID is
// confirmed dead first.
func acquirePIDFile(cfg *config.Config) error {
	path := cfg.PIDFile()
	if existing, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(existing))); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil && proc.Signal(syscall.Signal(0)) == nil {
				return fmt.Errorf("a daemon is already running (pid %d, from %s); refusing to start a second one against the same state_dir", pid, path)
			}
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

// removePIDFile removes cfg.PIDFile() on a clean shutdown. Its absence is
// how "reload" tells a stopped daemon from a running one, so this only runs
// once the daemon is actually exiting for good — never across a reload,
// which keeps the same process and the same PID throughout.
func removePIDFile(cfg *config.Config, log *slog.Logger) {
	if err := os.Remove(cfg.PIDFile()); err != nil && !os.IsNotExist(err) {
		log.Warn("removing pid file", "path", cfg.PIDFile(), "error", err)
	}
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
		"default_direct", user.Routing.DefaultDirect.String(),
		"default_group", defaultGroupLog(user.Routing),
		"routing_rules", len(user.Routing.Rules),
		"threads", user.Routing.ThreadPerConversationEnabled())

	if err := br.Run(ctx); err != nil {
		log.Error("the bridge stopped", "error", err)
	}
}

// defaultGroupLog renders routing.default_group for a log line, making the
// fallback to default_direct (an unset destination, config.go's Validate
// allows it) visible instead of printing "invalid destination".
func defaultGroupLog(r config.RoutingConfig) string {
	if r.DefaultGroup.Type == "" {
		return "(same as default_direct)"
	}
	return r.DefaultGroup.String()
}

// startListener brings up the remote-pairing HTTP listener and hands its
// goroutine to wg, so that a caller doing wg.Wait() — runGeneration, in
// particular — does not consider the generation over until the listener has
// actually released its port. Without that, a reload racing to rebind the
// same port against the next generation could find it still held by the one
// being torn down.
func startListener(ctx context.Context, cfg *config.Config, log *slog.Logger, wg *sync.WaitGroup) error {
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

	wg.Add(1)
	go func() {
		defer wg.Done()
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
