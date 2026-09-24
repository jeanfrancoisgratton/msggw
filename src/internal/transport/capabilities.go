// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/transport/capabilities.go

package transport

import "context"

// The interfaces below are optional: a Transport implementation satisfies
// whichever of them its backend actually supports, and the generic bridge
// core type-asserts for each one at the call site where it would matter
// (e.g. "if this transport reports delivery status, react to the post").
// A backend that supports none of them (Discord, in full) is not forced to
// carry no-op implementations of concepts — async send, delivery status,
// backfill, read receipts — that its protocol has no equivalent of.
//
// Two related mechanisms are deliberately NOT capability interfaces:
//
//   - The pending-outbound *storage* mechanism (storage.AddPendingOutbound/
//     TakePendingOutbound) is already fully protocol-neutral and lives in
//     the generic core itself; only the decision to use it at all is gated
//     on AsyncSender, via PendingID below.
//   - Phone-based routing is a config.Rule concern, not a Transport one —
//     see internal/corebridge/routing.go's GenericRule.

// AsyncSender is implemented by a transport whose Send does not return the
// final message ID synchronously — gmessages: the phone echoes a sent
// message back later, as an ordinary MessageEvent carrying the temporary ID
// Send chose. Discord's Send returns the real ID immediately and implements
// no such interface.
type AsyncSender interface {
	// PendingID extracts the transport's own correlation ID from the
	// Message Send returned with an empty ID, for the core to hand to
	// storage.AddPendingOutbound and later match against an incoming
	// MessageEvent via storage.TakePendingOutbound.
	PendingID(Message) string
}

// StatusReporter is implemented by a transport that can report a delivery
// status change on a message it already sent (gmessages: SMS/RCS delivery
// states). Discord bots get no such signal and implement no such interface.
type StatusReporter interface {
	// Status extracts a human-readable delivery-status label from a
	// Message (e.g. for a Mattermost reaction), and reports whether it
	// represents a terminal failure.
	Status(Message) (label string, failed bool)
}

// Backfiller is implemented by a transport that can fetch a conversation's
// recent history on first bridge (gmessages: FetchMessages). Discord's v1
// adapter does not implement this — see docs/discord-adapter-design.md §6 —
// though discordgo's message-history endpoint would make it straightforward
// to add later.
type Backfiller interface {
	Backfill(ctx context.Context, conversationID string, count int) ([]Message, error)
}

// ReadMarker is implemented by a transport that can mark a conversation read
// on the far end (gmessages: MarkRead). Discord gives a bot no equivalent
// action.
type ReadMarker interface {
	MarkRead(ctx context.Context, conversationID, messageID string) error
}
