// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:05:43
// Original filename: src/internal/bridge/bridge.go

// Package bridge joins the two halves: Google Messages events become
// Mattermost posts, and Mattermost replies become Google Messages sends.
//
// It is the only package that knows about both sides. The gmessages and
// mattermost packages know nothing of each other.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"msggw/internal/config"
	"msggw/internal/gmessages"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// pendingOutboundTTL is how long a sent-but-unacknowledged message keeps its
// temporary ID. Google Messages normally echoes a message back within seconds;
// anything still pending after an hour is never coming back, and its entry
// would otherwise sit in the table for good.
const pendingOutboundTTL = time.Hour

// pruneInterval is how often the pending table is swept.
const pruneInterval = 15 * time.Minute

// Bridge wires the two sides together.
type Bridge struct {
	cfg    *config.Config
	log    *slog.Logger
	db     *storage.DB
	gm     *gmessages.Client
	mm     *mattermost.Client
	router *Router

	// threadMode mirrors routing.thread_per_conversation, read often enough to
	// be worth not walking the config for.
	threadMode bool

	mu sync.Mutex
	// conversationLocks serialises work per conversation. Two messages
	// arriving at once in a conversation with no thread yet would otherwise
	// both create a root post, and one of the two threads would be orphaned.
	conversationLocks map[string]*sync.Mutex
}

// New builds a bridge from its already-constructed parts.
func New(cfg *config.Config, log *slog.Logger, db *storage.DB, gm *gmessages.Client, mm *mattermost.Client) (*Bridge, error) {
	router, err := NewRouter(cfg.Routing)
	if err != nil {
		return nil, err
	}

	return &Bridge{
		cfg:               cfg,
		log:               log,
		db:                db,
		gm:                gm,
		mm:                mm,
		router:            router,
		threadMode:        cfg.ThreadPerConversationEnabled(),
		conversationLocks: make(map[string]*sync.Mutex),
	}, nil
}

// Run pumps both event streams until ctx is cancelled or the Google Messages
// session ends. It returns nil on a clean shutdown.
func (b *Bridge) Run(ctx context.Context) error {
	gmEvents := b.gm.Events()
	mmEvents := b.mm.Listen(ctx)

	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			b.log.Info("shutting the bridge down")
			return nil

		case <-prune.C:
			b.prunePending(ctx)

		case evt, ok := <-gmEvents:
			if !ok {
				return errors.New("the Google Messages event stream closed")
			}
			if err := b.handleGMEvent(ctx, evt); err != nil {
				// One bad message must not take the daemon down: the next one
				// may well be fine, and a bridge that exits on a single
				// protocol oddity is a bridge that is always down.
				b.log.Error("handling a Google Messages event", "error", err)
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

func (b *Bridge) handleGMEvent(ctx context.Context, evt gmessages.Event) error {
	switch e := evt.(type) {
	case gmessages.ReadyEvent:
		b.log.Info("the Google Messages session is ready",
			"session", e.SessionID, "conversations", len(e.Conversations))
		return nil

	case gmessages.MessageEvent:
		return b.handleIncomingMessage(ctx, e.Message)

	case gmessages.ConversationEvent:
		return b.handleConversationUpdate(ctx, e.Conversation)

	case gmessages.PairedEvent:
		b.log.Info("paired with a phone", "phone", e.PhoneID)
		return nil

	case gmessages.LoggedOutEvent:
		// Nothing the daemon does can recover this; it needs a human and a new
		// pairing. Saying so plainly beats reconnect attempts that cannot work.
		return fmt.Errorf("the Google Messages session ended: %s — run \"msg-gw pair\" again", e.Reason)

	case gmessages.ConnectionEvent:
		b.logConnection(e)
		return nil

	case gmessages.AlertEvent:
		b.log.Debug("the phone sent a status alert", "alert", e.Type.String())
		return nil

	default:
		b.log.Debug("ignoring an unhandled Google Messages event", "event", fmt.Sprintf("%T", evt))
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

func (b *Bridge) logConnection(e gmessages.ConnectionEvent) {
	switch e.State {
	case gmessages.ConnPhoneUnreachable:
		b.log.Warn("the phone is not responding: messages will queue until it is back")
	case gmessages.ConnPhoneReachable:
		b.log.Info("the phone is responding again")
	case gmessages.ConnRecovered:
		b.log.Info("the Google Messages connection recovered")
	case gmessages.ConnNoData:
		b.log.Warn("no data received from the phone for a long time: the session may be stale")
	case gmessages.ConnTemporaryError:
		b.log.Warn("temporary Google Messages error", "error", e.Err)
	case gmessages.ConnFatalError:
		b.log.Error("fatal Google Messages error", "error", e.Err)
	default:
		b.log.Debug("Google Messages connection", "state", e.State, "error", e.Err)
	}
}

func (b *Bridge) prunePending(ctx context.Context) {
	removed, err := b.db.PrunePendingOutbound(ctx, pendingOutboundTTL)
	if err != nil {
		b.log.Warn("pruning unacknowledged outgoing messages", "error", err)
		return
	}
	if removed > 0 {
		b.log.Warn("gave up on outgoing messages the phone never acknowledged", "count", removed)
	}
}

// lockConversation serialises work on one conversation. The returned function
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
