// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/transport/events.go

package transport

// Event is what the generic bridge core consumes from Transport.Events. Each
// adapter translates its own protocol's event set into this smaller,
// shared vocabulary — the same role gmessages.Event/discord.Event already
// played for their own bridges.
type Event interface{ isTransportEvent() }

// ReadyEvent is emitted once a transport's connection is fully up.
type ReadyEvent struct {
	// SessionID is populated by gmessages only.
	SessionID string
	// UserID and Username are populated by Discord only.
	UserID   string
	Username string
	// Conversations is populated by gmessages only, which receives its
	// conversation list as part of becoming ready; nil for Discord.
	Conversations []Conversation
}

// MessageEvent carries one message, incoming or (for gmessages) an echo of
// one this daemon sent.
type MessageEvent struct {
	Message Message
}

// ConversationEvent reports a conversation's metadata changing: a new
// conversation, a renamed group, a changed participant list. Emitted by
// gmessages only — Discord has no channel-rename tracking in v1 (see
// docs/discord-adapter-design.md §6), so it never produces this event.
type ConversationEvent struct {
	Conversation Conversation
}

// ConnectionEvent reports the health of a transport's connection, so the
// daemon can log it.
type ConnectionEvent struct {
	// State is one of the Conn* constants.
	State string
	Err   error
}

// Connection states. Discord only ever emits ConnConnected/ConnDisconnected/
// ConnResumed; gmessages only ever emits the phone/temporary/fatal/no-data
// states. Both sets live here rather than split by adapter so the generic
// core's logging switch has one exhaustive list to work from.
const (
	ConnConnected    = "connected"
	ConnDisconnected = "disconnected"
	ConnResumed      = "resumed"

	ConnPhoneUnreachable = "phone_unreachable"
	ConnPhoneReachable   = "phone_reachable"
	ConnTemporaryError   = "temporary_error"
	ConnRecovered        = "recovered"
	ConnFatalError       = "fatal_error"
	ConnNoData           = "no_data_received"
)

func (ReadyEvent) isTransportEvent()        {}
func (MessageEvent) isTransportEvent()      {}
func (ConversationEvent) isTransportEvent() {}
func (ConnectionEvent) isTransportEvent()   {}
