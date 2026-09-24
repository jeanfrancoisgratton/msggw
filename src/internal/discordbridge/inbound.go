// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discordbridge/inbound.go

package discordbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"msggw/internal/discord"
	"msggw/internal/mattermost"
	"msggw/internal/storage"
)

// handleIncomingMessage moves one Discord message into Mattermost.
//
// Unlike gmessages, discord.Client never emits the bot's own sends back as
// a separate event (see discord/client.go's onMessageCreate), so there is
// no echo/temporary-ID case to check here, and no delivery-status update
// case either: Discord gives a bot no read-receipt/delivery signal to
// reflect. The only two things a MessageEvent can be are "new" or "already
// bridged" (a gateway resume replaying something from before a reconnect).
func (b *Bridge) handleIncomingMessage(ctx context.Context, msg discord.Message) error {
	if _, err := b.db.GetMessage(ctx, tenant, msg.ID); err == nil {
		b.log.Debug("ignoring a replayed Discord message the bridge already has", "message", msg.ID)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("looking up message %s: %w", msg.ID, err)
	}

	if !msg.HasContent() {
		// A message with only an embed or a sticker — nothing this v1
		// adapter knows how to render. See docs/discord-adapter-design.md.
		b.log.Debug("ignoring a Discord message with no text or attachments",
			"message", msg.ID, "channel", msg.ConversationID)
		return nil
	}

	conv, err := b.ensureConversation(ctx, msg.ConversationID)
	if err != nil {
		return err
	}

	if err := b.postMessage(ctx, conv, msg); err != nil {
		return err
	}

	if err := b.db.TouchConversation(ctx, tenant, conv.ID, msg.Timestamp); err != nil {
		b.log.Warn("could not record conversation activity", "conversation", conv.ID, "error", err)
	}

	return nil
}

// postMessage creates the Mattermost post for a message and records the
// mapping.
func (b *Bridge) postMessage(ctx context.Context, conv storage.Conversation, msg discord.Message) error {
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

	if err := b.db.SaveMessage(ctx, tenant, storage.Message{
		ID:             msg.ID,
		PostID:         postID,
		ConversationID: conv.ID,
		Direction:      storage.DirectionIn,
		CreatedAt:      msg.Timestamp,
	}); err != nil {
		// The post is already up. Failing to record it means a replayed
		// event would post it twice, so it is worth an error rather than a
		// warning.
		return fmt.Errorf("recording message %s as post %s: %w", msg.ID, postID, err)
	}

	b.log.Info("bridged a message to Mattermost",
		"channel", conv.ID, "message", msg.ID, "post", postID, "attachments", len(fileIDs))

	return nil
}

// transferAttachments downloads a message's attachments from Discord and
// re-uploads them to Mattermost, returning the file IDs and any notes to
// add to the post text.
//
// An attachment that cannot be transferred does not stop the message: the
// text is worth posting on its own, with a line saying what is missing.
func (b *Bridge) transferAttachments(ctx context.Context, conv storage.Conversation, msg discord.Message) (fileIDs []string, notes []string) {
	for _, att := range msg.Attachments {
		data, err := b.dc.Download(ctx, att)
		if err != nil {
			b.log.Warn("could not download a Discord attachment",
				"message", msg.ID, "attachment", att.Name, "error", err)
			notes = append(notes, fmt.Sprintf("_could not fetch %s from Discord_", attachmentLabel(att)))
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

func attachmentLabel(att discord.Attachment) string {
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
// the bot as author; without it, a channel with several Discord users would
// be unreadable. There is no carrier/status line the way gmessages'
// formatMessage has: Discord has no SMS-vs-RCS distinction and no delivery
// status to show.
func formatMessage(conv storage.Conversation, msg discord.Message, notes []string) string {
	var out strings.Builder

	sender := msg.AuthorName
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
