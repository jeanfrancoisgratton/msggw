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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// reloadRequest asks the generation loop to reload config.json. source
// labels who asked, for logs. respond is always non-nil and is called
// exactly once with the outcome — nil on success, an error otherwise —
// which is how a listener handler (see rulesserver.go) turns "someone
// pushed new rules" into a synchronous pass/fail HTTP response. SIGHUP's own
// trigger (see forwardReloadSignal) passes a no-op respond, since "msg-gw
// reload" has never waited on anything but the signal being delivered.
type reloadRequest struct {
	source  string
	respond func(error)
}

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
still valid, restarts every user's bridge and the Mattermost connection
against it, without leaving this process or losing its PID. A user's own
"msg-gw rules push --remote" does the same thing internally, over the
listener, once its change is saved. This is what makes a config or rules
change take effect in a container started with "exec message-gateway
daemon", where there is no supervisor to restart the process for you. An
invalid configuration is rejected and logged; the daemon keeps running on
the configuration it already had. If the new configuration is valid but
fails to actually come up (e.g. Mattermost is briefly unreachable), the
daemon reverts to the configuration it had before, rather than staying
down.

The listener (remote pairing and remote rules management) runs for the
whole lifetime of the process, not per configuration generation, so a
reload never drops a client's connection to it.`,
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

		sharedCfg := new(atomic.Pointer[config.Config])
		sharedCfg.Store(cfg)

		reloadRequests := make(chan reloadRequest)
		go forwardReloadSignal(rootCtx, hup, reloadRequests)

		lst, err := startPersistentListener(rootCtx, cfg.Listener, sharedCfg, reloadRequests, log)
		if err != nil {
			return err
		}
		defer lst.stop(log)

		genCtx, cancelGen := context.WithCancel(rootCtx)
		genDone := make(chan error, 1)
		go func(cfg *config.Config) {
			genDone <- runGeneration(genCtx, cfg, log, make(chan error, 1))
		}(cfg)

		// Every generation from here on — the initial one above, and every
		// one a successful reload swaps in below — is tracked by exactly
		// one (genCtx, cancelGen, genDone) triple in these variables. A
		// reload case fully starts, confirms and swaps in its replacement
		// itself before this loop sees it again, so there is nothing left
		// for the loop to do afterward except go back to select — unlike an
		// earlier version of this loop, nothing here should ever try to
		// start a generation of its own at the top.
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

			case req := <-reloadRequests:
				log.Info("reload requested", "source", req.source, "config", cfg.Path())
				newCfg, err := config.Load(cfg.Path())
				if err != nil {
					log.Error("reload: the configuration is invalid, keeping the current one running",
						"config", cfg.Path(), "error", err)
					req.respond(err)
					continue
				}

				cancelGen()
				<-genDone

				newCtx, newCancel := context.WithCancel(rootCtx)
				newGenDone := make(chan error, 1)
				newReady := make(chan error, 1)
				go func(cfg *config.Config) {
					newGenDone <- runGeneration(newCtx, cfg, log, newReady)
				}(newCfg)

				startErr, running := waitReady(newReady, newGenDone)
				if startErr != nil {
					newCancel() // safe even if !running: the goroutine already exited on its own.
					if running {
						<-newGenDone
					}
					// A shutdown racing an in-flight reload can itself be
					// why the new generation failed to start (its context
					// is a child of rootCtx). That is not a reload failure
					// worth alarming logs or the caller over — it is just
					// "we were about to shut down anyway" — so it takes
					// priority over the ordinary failure-and-rollback
					// handling below.
					if rootCtx.Err() != nil {
						log.Info("reload: shutting down before the new configuration finished starting")
						req.respond(rootCtx.Err())
						return nil
					}

					log.Error("reload: the new configuration failed to start, reverting to the previous one",
						"error", startErr)

					genCtx, cancelGen = context.WithCancel(rootCtx)
					genDone = make(chan error, 1)
					rollbackReady := make(chan error, 1)
					go func(cfg *config.Config) {
						genDone <- runGeneration(genCtx, cfg, log, rollbackReady)
					}(cfg) // cfg is still the previous, known-good configuration.

					rbErr, rbRunning := waitReady(rollbackReady, genDone)
					if rbErr != nil {
						cancelGen() // safe even if !rbRunning: the goroutine already exited on its own.
						if rbRunning {
							<-genDone
						}
						if rootCtx.Err() != nil {
							log.Info("reload: shutting down before the previous configuration could be restarted")
							req.respond(rootCtx.Err())
							return nil
						}
						req.respond(fmt.Errorf("the new configuration failed to start (%w), and reverting to the previous one also failed", startErr))
						return fmt.Errorf("reload failed and the previous configuration could not be restarted either: %w", rbErr)
					}

					log.Info("reload: reverted to the previous configuration, which is running again")
					req.respond(fmt.Errorf("the new configuration failed to start (%w); the previous configuration is still running", startErr))
					continue
				}

				log.Info("reload: configuration is valid and running")
				sharedCfg.Store(newCfg)
				req.respond(nil)

				if !reflect.DeepEqual(lst.listenerConfig(), newCfg.Listener) {
					lst.stop(log)
					lst, err = startPersistentListener(rootCtx, newCfg.Listener, sharedCfg, reloadRequests, log)
					if err != nil {
						// The listener failing to rebind does not roll back
						// the generation swap above: the bridges themselves
						// are running fine on the new configuration, only
						// remote pairing/rules are affected. Surfaced
						// loudly since it is silent otherwise.
						log.Error("reload: the listener could not be rebound with the new configuration; remote pairing and remote rules management are now unavailable",
							"error", err)
					}
				}

				cfg = newCfg
				genCtx, cancelGen, genDone = newCtx, newCancel, newGenDone
			}
		}
	},
}

// waitReady blocks until a generation started with runGeneration either
// signals it is set up (storage, Mattermost) via ready, or exits on its own
// via genDone before ever reaching that point. A nil err means it is up.
//
// running reports whether the generation is still going and its genDone
// therefore still needs to be cancelled-and-drained by the caller: true
// when ready fired (the generation is live), false when genDone fired
// instead (it already exited on its own, and genDone has already been
// received from here — receiving from it again would block forever).
func waitReady(ready, genDone <-chan error) (err error, running bool) {
	select {
	case err := <-ready:
		return err, true
	case err := <-genDone:
		if err == nil {
			err = errors.New("the generation exited immediately")
		}
		return err, false
	}
}

// forwardReloadSignal turns each SIGHUP into a fire-and-forget reloadRequest,
// so the generation loop only ever has one trigger to select on regardless
// of whether a reload was asked for by the operator (SIGHUP, "msg-gw
// reload") or by a user's own rules push over the listener.
func forwardReloadSignal(ctx context.Context, hup <-chan os.Signal, reloadRequests chan<- reloadRequest) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			select {
			case reloadRequests <- reloadRequest{source: "sighup", respond: func(error) {}}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// runGeneration brings up storage, the Mattermost connection and every
// configured user's bridge against cfg, and runs them until ctx is
// cancelled. It is one "generation" of the configuration: daemon's RunE
// calls it once at startup, and once more per successful reload, each time
// against a freshly loaded *config.Config and a context scoped to that
// generation alone.
//
// ready is sent a single value — nil — once setup (storage, Mattermost) has
// succeeded and every user's bridge is being started, so a caller can tell
// "this generation is up" apart from "this generation is still starting"
// without waiting for it to run to completion. It is never sent to if setup
// itself fails; the returned error covers that case instead. ready must be
// buffered (capacity at least 1) so a caller that never reads it (a
// generation's normal startup) does not block this goroutine.
func runGeneration(ctx context.Context, cfg *config.Config, log *slog.Logger, ready chan<- error) error {
	db, err := openStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	mm, err := newMattermost(ctx, cfg, log)
	if err != nil {
		return err
	}

	ready <- nil
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

// daemonListener owns the process-lifetime HTTP(S) listener that serves
// remote pairing and remote rules management. Unlike the rest of a
// "generation" (storage, Mattermost, every user's bridge), it is not torn
// down and rebuilt on every reload — see daemon.go's package doc and
// startPersistentListener — only when listener.port/cert_file/key_file
// itself changes.
type daemonListener struct {
	cfg    config.ListenerConfig
	cancel context.CancelFunc
	done   chan error
}

// listenerConfig reports the ListenerConfig l is currently bound with, or
// the zero value if l is nil (the listener is disabled) — so callers can
// compare against a freshly loaded configuration without a nil check first.
func (l *daemonListener) listenerConfig() config.ListenerConfig {
	if l == nil {
		return config.ListenerConfig{}
	}
	return l.cfg
}

// stop tears l down, if it is running at all, and waits for it to fully
// release its port. Safe to call on a nil l (nothing to do).
func (l *daemonListener) stop(log *slog.Logger) {
	if l == nil {
		return
	}
	l.cancel()
	if err := <-l.done; err != nil {
		log.Error("listener stopped", "error", err)
	}
}

// startPersistentListener brings up the daemon's listener bound to lcfg,
// scoped to ctx (the whole process's lifetime, not one generation's) —
// mounting both remote pairing (pairServer) and remote rules management
// (rulesServer). Both read the live configuration through sharedCfg rather
// than a snapshot, since they outlive any single generation. A lcfg.Port of
// 0 disables the listener: startPersistentListener returns a nil
// *daemonListener and no error, the same way ListenerConfig.Port already
// means "off" elsewhere.
func startPersistentListener(ctx context.Context, lcfg config.ListenerConfig, sharedCfg *atomic.Pointer[config.Config], reloadRequests chan<- reloadRequest, log *slog.Logger) (*daemonListener, error) {
	if lcfg.Port == 0 {
		return nil, nil
	}

	listenerCtx, cancel := context.WithCancel(ctx)

	mux := listener.DefaultHandler()
	pairSrv := newPairServer(sharedCfg, log)
	pairSrv.mount(mux)
	go pairSrv.sweep(listenerCtx)
	rulesSrv := newRulesServer(sharedCfg, log, reloadRequests)
	rulesSrv.mount(mux)

	lst, err := listener.New(listener.Config{
		Port:     lcfg.Port,
		CertFile: lcfg.CertFile,
		KeyFile:  lcfg.KeyFile,
	}, mux, log)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("starting the listener: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- lst.Run(listenerCtx)
	}()

	return &daemonListener{cfg: lcfg, cancel: cancel, done: done}, nil
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
