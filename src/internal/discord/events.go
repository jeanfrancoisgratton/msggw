// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discord/events.go

package discord

import "sync"

// Event is what the bridge consumes. discordgo's own event set is
// translated into this smaller vocabulary the same way gmessages translates
// libgm's, so that a discordgo API change shows up here rather than
// throughout the bridge.
type Event interface{ isDiscordEvent() }

// ReadyEvent is emitted once the gateway session is up and the bot's own
// identity is known.
type ReadyEvent struct {
	UserID   string
	Username string
}

// MessageEvent carries one message. The bot's own messages (including the
// gateway's echo of a message this daemon just sent) never reach here — see
// onMessageCreate in client.go.
type MessageEvent struct {
	Message Message
}

// ConnectionEvent reports the health of the gateway connection, so the
// daemon can log it.
type ConnectionEvent struct {
	// State is one of the Conn* constants.
	State string
	Err   error
}

// Connection states.
const (
	ConnConnected    = "connected"
	ConnDisconnected = "disconnected"
	ConnResumed      = "resumed"
)

func (ReadyEvent) isDiscordEvent()      {}
func (MessageEvent) isDiscordEvent()    {}
func (ConnectionEvent) isDiscordEvent() {}

// ---------------------------------------------------------------------------
// event queue
// ---------------------------------------------------------------------------

// queue is an unbounded FIFO between discordgo's handler goroutines and the
// bridge, copied from gmessages' queue of the same name (each protocol
// package owns its own; there is no shared queue package).
//
// It is unbounded on purpose. A fixed channel would force a choice between
// dropping messages when Mattermost is slow — unacceptable for a message
// bridge — and blocking discordgo's own event-dispatch goroutine, which
// would stall the gateway's heartbeat.
type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Event
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(evt Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, evt)
	q.cond.Signal()
}

// pop blocks until an event is available, and returns ok == false once the
// queue is closed and drained.
func (q *queue) pop() (Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return nil, false
	}
	evt := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return evt, true
}

func (q *queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}
