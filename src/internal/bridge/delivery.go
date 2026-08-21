// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:07:42
// Original filename: src/internal/bridge/delivery.go

package bridge

import (
	"context"
	"fmt"

	"rcs_gateway/internal/gmessages"
	"rcs_gateway/internal/storage"
)

// Delivery state is shown as a reaction on the post. A reaction is the least
// intrusive way Mattermost offers: it does not change the message text, it
// does not create a second post, and it disappears cleanly when the state
// moves on.
const (
	emojiPending   = "hourglass_flowing_sand"
	emojiSent      = "envelope"
	emojiDelivered = "white_check_mark"
	emojiRead      = "eyes"
	emojiFailed    = "warning"
)

// handleStatusUpdate applies a new delivery state to a message that is already
// bridged.
func (b *Bridge) handleStatusUpdate(ctx context.Context, known storage.Message, msg gmessages.Message) error {
	newStatus := gmessages.Status(msg.Status)
	oldStatus := gmessages.Status(known.Status)

	changed, err := b.db.UpdateMessageStatus(ctx, msg.ID, int32(newStatus))
	if err != nil {
		return fmt.Errorf("recording the status of message %s: %w", msg.ID, err)
	}
	if !changed {
		// The phone repeats status updates freely; re-decorating the post on
		// every repeat would churn reactions for no reason.
		return nil
	}

	b.log.Debug("a message changed delivery state",
		"message", msg.ID, "post", known.PostID,
		"from", oldStatus.String(), "to", newStatus.String())

	return b.applyDeliveryStatus(ctx, known.PostID, newStatus, oldStatus)
}

// applyDeliveryStatus moves a post's status reaction from the old state to the
// new one. It is a no-op unless routing.post_delivery_status is on.
func (b *Bridge) applyDeliveryStatus(ctx context.Context, postID string, newStatus, oldStatus gmessages.Status) error {
	if !b.cfg.Routing.PostDeliveryStatus || postID == "" {
		return nil
	}

	newEmoji := statusEmoji(newStatus)
	oldEmoji := statusEmoji(oldStatus)
	if newEmoji == oldEmoji {
		return nil
	}

	if oldEmoji != "" {
		if err := b.mm.Unreact(ctx, postID, oldEmoji); err != nil {
			// The old reaction may never have been added — the first status a
			// message gets has no predecessor — so this is not worth failing
			// the update over.
			b.log.Debug("could not remove a stale status reaction",
				"post", postID, "emoji", oldEmoji, "error", err)
		}
	}
	if newEmoji == "" {
		return nil
	}
	if err := b.mm.React(ctx, postID, newEmoji); err != nil {
		return fmt.Errorf("marking post %s as %s: %w", postID, newEmoji, err)
	}
	return nil
}

// statusEmoji maps a delivery state onto the reaction that stands for it, and
// returns "" for states not worth showing.
//
// Only outgoing states are decorated: a reaction on an incoming message would
// say something about our own reading of it, which Mattermost already shows.
func statusEmoji(status gmessages.Status) string {
	switch {
	case status == 0:
		return ""
	case !status.Outgoing():
		return ""
	case status.Failed():
		return emojiFailed
	case status.Read():
		return emojiRead
	case status.Delivered():
		return emojiDelivered
	case status.Pending():
		return emojiPending
	default:
		// Everything else that is outgoing and not pending has left the phone.
		return emojiSent
	}
}
