// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:06:54
// Original filename: src/internal/bridge/inbound.go

package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"msggw/internal/gmessages"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// handleIncomingMessage moves one Google Messages message into Mattermost.
//
// The same event type also carries the phone's echo of a message the bridge
// sent, and later status updates for messages already posted, so the first
// thing this does is work out which of the three it is looking at.
func (b *Bridge) handleIncomingMessage(ctx context.Context, msg gmessages.Message) error {
	// The echo of one of our own sends. Its temporary ID is the only thing
	// tying the phone's message ID back to the Mattermost post it came from.
	if postID, err := b.db.TakePendingOutbound(ctx, msg.TmpID); err == nil {
		if err := b.db.SaveMessage(ctx, storage.Message{
			ID:             msg.ID,
			PostID:         postID,
			ConversationID: msg.ConversationID,
			Direction:      storage.DirectionOut,
			Status:         int32(msg.Status),
			CreatedAt:      msg.Timestamp,
		}); err != nil {
			return fmt.Errorf("recording the sent message %s: %w", msg.ID, err)
		}
		b.log.Debug("the phone acknowledged an outgoing message",
			"message", msg.ID, "post", postID, "status", msg.Status)
		return b.applyDeliveryStatus(ctx, postID, msg.Status, 0)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("looking up the pending outgoing message %s: %w", msg.TmpID, err)
	}

	// A message already bridged. Anything new about it is a status change.
	if known, err := b.db.GetMessage(ctx, msg.ID); err == nil {
		return b.handleStatusUpdate(ctx, known, msg)
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("looking up message %s: %w", msg.ID, err)
	}

	if msg.IsOld {
		// A replay of something a previous session already acknowledged. The
		// bridge has no record of it, which means it was never bridged — most
		// likely it predates the bridge entirely. Posting the phone's whole
		// backlog on a first run is not what anyone wants; the backfill_count
		// setting exists for when it is.
		b.log.Debug("ignoring a replayed message the bridge never had",
			"message", msg.ID, "conversation", msg.ConversationID)
		return nil
	}

	if !msg.HasContent() {
		// A status-only event for a message the bridge does not know. Nothing
		// to post and nothing to update.
		b.log.Debug("ignoring an empty message event",
			"message", msg.ID, "status", msg.Status)
		return nil
	}

	conv, err := b.ensureConversation(ctx, msg.ConversationID)
	if err != nil {
		return err
	}

	if err := b.postMessage(ctx, conv, msg); err != nil {
		return err
	}

	if err := b.db.TouchConversation(ctx, conv.ID, msg.Timestamp); err != nil {
		b.log.Warn("could not record conversation activity", "conversation", conv.ID, "error", err)
	}

	if b.cfg.GMessages.MarkReadOnBridge && !msg.IsFromMe {
		if err := b.gm.MarkRead(ctx, msg.ConversationID, msg.ID); err != nil {
			b.log.Warn("could not mark a conversation read on the phone",
				"conversation", msg.ConversationID, "error", err)
		}
	}

	return nil
}

// postMessage creates the Mattermost post for a message and records the
// mapping.
func (b *Bridge) postMessage(ctx context.Context, conv storage.Conversation, msg gmessages.Message) error {
	fileIDs, notes := b.transferAttachments(ctx, conv, msg)

	post := mattermost.NewPost{
		ChannelID: conv.ChannelID,
		RootID:    conv.RootPostID,
		Message:   formatMessage(conv, msg, notes),
		FileIDs:   fileIDs,
	}

	postID, err := b.mm.Post(ctx, post)
	if err != nil {
		return fmt.Errorf("posting message %s to Mattermost: %w", msg.ID, err)
	}

	direction := storage.DirectionIn
	if msg.IsFromMe {
		// Sent from the phone itself, or from another paired device. It still
		// belongs in the thread, but it is not something Mattermost sent.
		direction = storage.DirectionOut
	}

	if err := b.db.SaveMessage(ctx, storage.Message{
		ID:             msg.ID,
		PostID:         postID,
		ConversationID: conv.ID,
		Direction:      direction,
		Status:         int32(msg.Status),
		CreatedAt:      msg.Timestamp,
	}); err != nil {
		// The post is already up. Failing to record it means the next replay
		// of this message would post it twice, so it is worth an error rather
		// than a warning.
		return fmt.Errorf("recording message %s as post %s: %w", msg.ID, postID, err)
	}

	b.log.Info("bridged a message to Mattermost",
		"conversation", conv.ID, "message", msg.ID, "post", postID,
		"kind", msg.Kind, "from_me", msg.IsFromMe, "attachments", len(fileIDs))

	return nil
}

// transferAttachments downloads a message's media from Google Messages and
// re-uploads it to Mattermost, returning the file IDs and any notes to add to
// the post text.
//
// An attachment that cannot be transferred does not stop the message: the text
// is worth posting on its own, with a line saying what is missing.
func (b *Bridge) transferAttachments(ctx context.Context, conv storage.Conversation, msg gmessages.Message) (fileIDs []string, notes []string) {
	for _, media := range msg.Media {
		if media.Pending() {
			notes = append(notes, fmt.Sprintf("_the phone has not downloaded %s yet_", attachmentLabel(media)))
			continue
		}

		data, isThumbnail, err := b.gm.Download(ctx, media)
		if err != nil {
			b.log.Warn("could not download an attachment",
				"message", msg.ID, "attachment", media.Name, "error", err)
			notes = append(notes, fmt.Sprintf("_could not fetch %s from the phone_", attachmentLabel(media)))
			continue
		}

		fileID, err := b.mm.Upload(ctx, conv.ChannelID, media.FileName(), data)
		if err != nil {
			b.log.Warn("could not upload an attachment to Mattermost",
				"message", msg.ID, "attachment", media.Name, "error", err)
			notes = append(notes, fmt.Sprintf("_could not upload %s to Mattermost_", attachmentLabel(media)))
			continue
		}

		if isThumbnail {
			notes = append(notes, fmt.Sprintf("_%s is a thumbnail: the full-size file is still on the phone_",
				attachmentLabel(media)))
		}
		fileIDs = append(fileIDs, fileID)
	}
	return fileIDs, notes
}

func attachmentLabel(media gmessages.Media) string {
	if name := strings.TrimSpace(media.Name); name != "" {
		return "`" + name + "`"
	}
	if media.MimeType != "" {
		return "the " + media.MimeType + " attachment"
	}
	return "an attachment"
}

// formatMessage renders a message as Mattermost markdown.
//
// The sender is named on every post because a Mattermost thread shows only the
// bot as author; without it, a group conversation would be unreadable. The
// carrier is named because whether a message travelled over RCS or fell back
// to SMS is the thing this project exists to make visible.
func formatMessage(conv storage.Conversation, msg gmessages.Message, notes []string) string {
	var out strings.Builder

	sender := msg.SenderName
	if sender == "" {
		sender = msg.SenderPhone
	}
	if sender == "" {
		if msg.IsFromMe {
			sender = "me"
		} else {
			sender = conv.DisplayName
		}
	}
	if msg.IsFromMe {
		sender += " (me)"
	}

	fmt.Fprintf(&out, "**%s** _%s_", sender, msg.Kind)
	if msg.Status.Failed() {
		fmt.Fprintf(&out, " ⚠️ _%s_", humanStatus(msg.Status))
	}
	out.WriteString("\n")

	if msg.Subject != "" {
		fmt.Fprintf(&out, "**%s**\n", msg.Subject)
	}
	if msg.Text != "" {
		out.WriteString(msg.Text)
		out.WriteString("\n")
	}
	for _, note := range notes {
		out.WriteString(note)
		out.WriteString("\n")
	}

	return strings.TrimRight(out.String(), "\n")
}

// humanStatus turns a protocol status name into something readable, so a post
// says "recipient lost RCS" rather than
// "OUTGOING_FAILED_RECIPIENT_LOST_RCS".
func humanStatus(status gmessages.Status) string {
	name := status.String()
	name = strings.TrimPrefix(name, "OUTGOING_")
	name = strings.TrimPrefix(name, "INCOMING_")
	name = strings.TrimPrefix(name, "FAILED_")
	return strings.ToLower(strings.ReplaceAll(name, "_", " "))
}
