// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/transport/transport.go

package transport

import "context"

// Transport is the minimum surface a backend adapter must implement for the
// generic bridge core (internal/corebridge) to run it: connect, receive
// events, send a message, fetch an attachment's bytes, and look up a
// conversation. Anything a transport can additionally do — asynchronous
// send-then-echo, delivery status, backfill, mark-as-read — is expressed as
// one of the optional capability interfaces in capabilities.go instead of
// being forced into this interface as a no-op for backends that lack it.
type Transport interface {
	// Connect brings the transport up. It returns once the connection is
	// established; a ReadyEvent follows on Events once the transport has
	// finished whatever handshake it needs (e.g. gmessages' conversation
	// list, Discord's gateway identify).
	Connect(ctx context.Context) error

	// Disconnect tears the connection down and closes the Events channel.
	Disconnect()

	// Events returns the event stream. Ranging over it ends when the
	// transport is disconnected.
	Events() <-chan Event

	// Send posts a message to a conversation, optionally as a reply to
	// replyToID (empty for none). A transport whose backend does not return
	// the real message ID synchronously (gmessages: the phone echoes it back
	// later) returns a Message with an empty ID and implements AsyncSender
	// so the core knows to await the echo instead of treating the return
	// value as final.
	Send(ctx context.Context, conversationID, replyToID, text string, uploads []Upload) (Message, error)

	// Download fetches an attachment's bytes.
	Download(ctx context.Context, att Attachment) ([]byte, error)

	// Conversation fetches a conversation's current metadata, from cache
	// when the adapter keeps one.
	Conversation(ctx context.Context, conversationID string) (Conversation, error)
}
