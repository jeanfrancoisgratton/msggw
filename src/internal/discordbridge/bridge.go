// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discordbridge/bridge.go

// Package discordbridge joins the two halves: Discord messages become
// Mattermost posts, and Mattermost replies become Discord messages.
//
// It mirrors internal/bridge's shape (which does the same for Google
// Messages) but is a separate, standalone package rather than a retrofit of
// it — see docs/discord-adapter-design.md for why. The discord and
// mattermost packages know nothing of each other.
package discordbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"msggw/internal/config"
	"msggw/internal/discord"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// tenant is the fixed storage tenant key for Discord. storage.DB's methods
// are already scoped by an opaque tenant string, one per Google Messages
// phone pairing today; Discord has no per-phone concept — it is one shared
// bot for the whole daemon (see config.DiscordConfig's doc comment) — so
// this bridge always uses one fixed key instead of a per-tenant one. No
// storage schema change was needed for this.
const tenant = "discord"

// Bridge wires the two sides together. Unlike bridge.Bridge, there is only
// ever one of these per daemon (Discord is one shared connection, not
// per-tenant).
type Bridge struct {
	cfg config.DiscordConfig
	log *slog.Logger
	db  *storage.DB
	dc  *discord.Client
	mm  *mattermost.Client

	router *Router

	// threadMode mirrors cfg.Routing.thread_per_conversation, read often
	// enough to be worth not walking the config for.
	threadMode bool

	mu sync.Mutex
	// conversationLocks serialises work per channel. Two messages arriving
	// at once in a channel with no thread yet would otherwise both create a
	// root post, and one of the two threads would be orphaned. Mirrors
	// bridge.Bridge's conversationLocks.
	conversationLocks map[string]*sync.Mutex
}

// New builds a bridge from its already-constructed parts.
func New(cfg config.DiscordConfig, log *slog.Logger, db *storage.DB, dc *discord.Client, mm *mattermost.Client) (*Bridge, error) {
	router, err := NewRouter(cfg.Routing)
	if err != nil {
		return nil, err
	}

	return &Bridge{
		cfg:               cfg,
		log:               log,
		db:                db,
		dc:                dc,
		mm:                mm,
		router:            router,
		threadMode:        cfg.Routing.ThreadPerConversationEnabled(),
		conversationLocks: make(map[string]*sync.Mutex),
	}, nil
}

// Run pumps both event streams until ctx is cancelled or the Discord
// gateway closes for good. It returns nil on a clean shutdown.
func (b *Bridge) Run(ctx context.Context) error {
	dcEvents := b.dc.Events()
	mmEvents := b.mm.Listen(ctx)

	for {
		select {
		case <-ctx.Done():
			b.log.Info("shutting the Discord bridge down")
			return nil

		case evt, ok := <-dcEvents:
			if !ok {
				return errors.New("the Discord event stream closed")
			}
			if err := b.handleDiscordEvent(ctx, evt); err != nil {
				// One bad message must not take the daemon down: the next
				// one may well be fine.
				b.log.Error("handling a Discord event", "error", err)
			}

		case evt, ok := <-mmEvents:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("the Mattermost event stream closed")
			}
			if err := b.handleMMEvent(ctx, evt); err != nil {
				b.log.Error("handling a Mattermost event", "error", err)
			}
		}
	}
}

func (b *Bridge) handleDiscordEvent(ctx context.Context, evt discord.Event) error {
	switch e := evt.(type) {
	case discord.ReadyEvent:
		b.log.Info("the Discord session is ready", "user", e.Username, "user_id", e.UserID)
		return nil

	case discord.MessageEvent:
		return b.handleIncomingMessage(ctx, e.Message)

	case discord.ConnectionEvent:
		b.logConnection(e)
		return nil

	default:
		b.log.Debug("ignoring an unhandled Discord event", "event", fmt.Sprintf("%T", evt))
		return nil
	}
}

func (b *Bridge) handleMMEvent(ctx context.Context, evt mattermost.Event) error {
	switch e := evt.(type) {
	case mattermost.PostEvent:
		return b.handleOutgoingPost(ctx, e.Post)

	case mattermost.ConnectionEvent:
		if e.Err != nil {
			b.log.Warn("Mattermost connection", "state", e.State, "error", e.Err)
		} else {
			b.log.Debug("Mattermost connection", "state", e.State)
		}
		return nil

	default:
		return nil
	}
}

func (b *Bridge) logConnection(e discord.ConnectionEvent) {
	switch e.State {
	case discord.ConnConnected:
		b.log.Info("connected to Discord")
	case discord.ConnDisconnected:
		// discordgo reconnects the gateway on its own; this is informational,
		// not an error the daemon needs to act on.
		b.log.Warn("the Discord gateway connection dropped; reconnecting")
	case discord.ConnResumed:
		b.log.Info("the Discord gateway session resumed")
	default:
		b.log.Debug("Discord connection", "state", e.State, "error", e.Err)
	}
}

// lockConversation serialises work on one channel. The returned function
// releases it.
func (b *Bridge) lockConversation(id string) func() {
	b.mu.Lock()
	lock, ok := b.conversationLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		b.conversationLocks[id] = lock
	}
	b.mu.Unlock()

	lock.Lock()
	return lock.Unlock
}
