// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/inbound.go

package corebridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"msggw/internal/mattermost"
	"msggw/internal/storage"
	"msggw/internal/transport"
)

// handleIncomingMessage moves one message into Mattermost.
//
// This is currently Discord's shape: the only two things a MessageEvent can
// be are "new" or "already bridged" (a reconnect replaying something from
// before it dropped). A transport that can echo its own sent messages back
// with a temporary ID (gmessages), or report a delivery-status change on an
// already-known message, is not wired in yet — see internal/transport's
// AsyncSender/StatusReporter capability interfaces, added to this function
// once a transport that implements them is plugged into this package.
func (b *Bridge) handleIncomingMessage(ctx context.Context, msg transport.Message) error {
	if _, err := b.db.GetMessage(ctx, b.tenant, msg.ID); err == nil {
		b.log.Debug("ignoring a replayed message the bridge already has", "message", msg.ID)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("looking up message %s: %w", msg.ID, err)
	}

	if !msg.HasContent() {
		b.log.Debug("ignoring a message with no text or attachments",
			"message", msg.ID, "conversation", msg.ConversationID)
		return nil
	}

	conv, err := b.ensureConversation(ctx, msg.ConversationID)
	if err != nil {
		return err
	}

	if err := b.postMessage(ctx, conv, msg); err != nil {
		return err
	}

	if err := b.db.TouchConversation(ctx, b.tenant, conv.ID, msg.Timestamp); err != nil {
		b.log.Warn("could not record conversation activity", "conversation", conv.ID, "error", err)
	}

	return nil
}

// postMessage creates the Mattermost post for a message and records the
// mapping.
func (b *Bridge) postMessage(ctx context.Context, conv storage.Conversation, msg transport.Message) error {
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
		direction = storage.DirectionOut
	}
	if err := b.db.SaveMessage(ctx, b.tenant, storage.Message{
		ID:             msg.ID,
		PostID:         postID,
		ConversationID: conv.ID,
		Direction:      direction,
		CreatedAt:      msg.Timestamp,
	}); err != nil {
		// The post is already up. Failing to record it means a replayed
		// event would post it twice, so it is worth an error rather than a
		// warning.
		return fmt.Errorf("recording message %s as post %s: %w", msg.ID, postID, err)
	}

	b.log.Info("bridged a message to Mattermost",
		"conversation", conv.ID, "message", msg.ID, "post", postID, "attachments", len(fileIDs))

	return nil
}

// transferAttachments downloads a message's attachments from the transport
// and re-uploads them to Mattermost, returning the file IDs and any notes to
// add to the post text.
//
// An attachment that cannot be transferred does not stop the message: the
// text is worth posting on its own, with a line saying what is missing.
func (b *Bridge) transferAttachments(ctx context.Context, conv storage.Conversation, msg transport.Message) (fileIDs []string, notes []string) {
	for _, att := range msg.Attachments {
		data, err := b.tp.Download(ctx, att)
		if err != nil {
			b.log.Warn("could not download an attachment",
				"message", msg.ID, "attachment", att.Name, "error", err)
			notes = append(notes, fmt.Sprintf("_could not fetch %s_", attachmentLabel(att)))
			continue
		}

		fileID, err := b.mm.Upload(ctx, conv.ChannelID, att.Name, data)
		if err != nil {
			b.log.Warn("could not upload an attachment to Mattermost",
				"message", msg.ID, "attachment", att.Name, "error", err)
			notes = append(notes, fmt.Sprintf("_could not upload %s to Mattermost_", attachmentLabel(att)))
			continue
		}
		fileIDs = append(fileIDs, fileID)
	}
	return fileIDs, notes
}

func attachmentLabel(att transport.Attachment) string {
	if name := strings.TrimSpace(att.Name); name != "" {
		return "`" + name + "`"
	}
	if att.MimeType != "" {
		return "the " + att.MimeType + " attachment"
	}
	return "an attachment"
}

// formatMessage renders a message as Mattermost markdown.
//
// The sender is named on every post because a Mattermost thread shows only
// the bot as author; without it, a conversation with several senders would
// be unreadable.
func formatMessage(conv storage.Conversation, msg transport.Message, notes []string) string {
	var out strings.Builder

	sender := msg.SenderName
	if sender == "" {
		sender = conv.DisplayName
	}

	fmt.Fprintf(&out, "**%s**\n", sender)
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
