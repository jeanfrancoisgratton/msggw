// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/bridge.go

// Package corebridge is the transport-agnostic core: it joins any
// transport.Transport to Mattermost, generified from what was originally two
// separate, hand-duplicated packages (internal/bridge for Google Messages,
// internal/discordbridge for Discord — see docs/discord-adapter-design.md
// for why they started out separate, and docs/msggw-rcs-mattermost-discord.md
// for the architecture this package implements).
//
// It is deliberately modeled first on internal/discordbridge rather than
// internal/bridge: Discord's bridge has none of RCS's extra mechanisms
// (asynchronous send-then-echo, delivery-status reactions, backfill,
// mark-as-read), so it is the generic core's natural minimum shape. Those
// extra mechanisms are layered in via the optional capability interfaces in
// internal/transport, gated by a type assertion on the concrete
// transport.Transport a Bridge is given, rather than being forced onto every
// adapter.
package corebridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"msggw/internal/mattermost"
	"msggw/internal/storage"
	"msggw/internal/transport"
)

// Bridge wires one transport to Mattermost. There is one per Google
// Messages tenant (one paired phone each) and exactly one for Discord (one
// shared bot connection for the whole daemon) — see cmd/daemon.go and
// cmd/discord.go.
type Bridge struct {
	tenant string
	cfg    BridgeConfig
	log    *slog.Logger
	db     *storage.DB
	tp     transport.Transport
	mm     *mattermost.Client

	router *Router

	mu sync.Mutex
	// conversationLocks serialises work per conversation. Two messages
	// arriving at once in a conversation with no thread yet would otherwise
	// both create a root post, and one of the two threads would be orphaned.
	conversationLocks map[string]*sync.Mutex
}

// New builds a bridge from its already-constructed parts. tenant scopes
// every storage call — one per Google Messages phone, or the fixed value
// "discord" for the single shared Discord instance.
func New(tenant string, cfg BridgeConfig, log *slog.Logger, db *storage.DB, tp transport.Transport, mm *mattermost.Client) (*Bridge, error) {
	router, err := NewRouter(cfg)
	if err != nil {
		return nil, err
	}

	return &Bridge{
		tenant:            tenant,
		cfg:               cfg,
		log:               log,
		db:                db,
		tp:                tp,
		mm:                mm,
		router:            router,
		conversationLocks: make(map[string]*sync.Mutex),
	}, nil
}

// Run pumps both event streams until ctx is cancelled or the transport's
// event stream closes for good. It returns nil on a clean shutdown.
func (b *Bridge) Run(ctx context.Context) error {
	tpEvents := b.tp.Events()
	mmEvents := b.mm.Listen(ctx)

	for {
		select {
		case <-ctx.Done():
			b.log.Info("shutting the bridge down")
			return nil

		case evt, ok := <-tpEvents:
			if !ok {
				return errors.New("the transport event stream closed")
			}
			if err := b.handleTransportEvent(ctx, evt); err != nil {
				// One bad message must not take the daemon down: the next
				// one may well be fine.
				b.log.Error("handling a transport event", "error", err)
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

func (b *Bridge) handleTransportEvent(ctx context.Context, evt transport.Event) error {
	switch e := evt.(type) {
	case transport.ReadyEvent:
		b.log.Info("the transport is ready", "user", e.Username, "user_id", e.UserID, "session", e.SessionID)
		return nil

	case transport.MessageEvent:
		return b.handleIncomingMessage(ctx, e.Message)

	case transport.ConnectionEvent:
		b.logConnection(e)
		return nil

	default:
		b.log.Debug("ignoring an unhandled transport event", "event", fmt.Sprintf("%T", evt))
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

func (b *Bridge) logConnection(e transport.ConnectionEvent) {
	switch e.State {
	case transport.ConnConnected:
		b.log.Info("connected")
	case transport.ConnDisconnected:
		b.log.Warn("the connection dropped; reconnecting")
	case transport.ConnResumed:
		b.log.Info("the connection resumed")
	case transport.ConnPhoneUnreachable:
		b.log.Warn("the phone is unreachable")
	case transport.ConnPhoneReachable:
		b.log.Info("the phone is reachable again")
	case transport.ConnRecovered:
		b.log.Info("the connection recovered")
	case transport.ConnNoData:
		b.log.Warn("no data received recently")
	case transport.ConnTemporaryError:
		b.log.Warn("a temporary connection error", "error", e.Err)
	case transport.ConnFatalError:
		b.log.Error("a fatal connection error", "error", e.Err)
	default:
		b.log.Debug("connection", "state", e.State, "error", e.Err)
	}
}

// lockConversation serialises work on one conversation. The returned
// function releases it.
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
