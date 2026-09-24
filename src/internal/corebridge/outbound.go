// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/corebridge/outbound.go

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

// handleOutgoingPost sends a Mattermost post to the transport.
//
// Posts made by the bridge itself never reach this: the Mattermost client
// filters them out on the way in.
func (b *Bridge) handleOutgoingPost(ctx context.Context, post mattermost.Post) error {
	conv, err := b.conversationForPost(ctx, post)
	if errors.Is(err, storage.ErrNotFound) {
		// A post in a channel the bridge uses, but outside any thread it
		// owns. People do talk in these channels; that is not an error.
		b.log.Debug("ignoring a Mattermost post that is not in a bridged thread",
			"post", post.ID, "channel", post.ChannelID, "root", post.RootID)
		return nil
	}
	if err != nil {
		return err
	}

	text := strings.TrimSpace(post.Message)
	if text == "" && len(post.FileIDs) == 0 {
		return nil
	}

	replyToID, err := b.replyTarget(ctx, post, conv)
	if err != nil {
		b.log.Warn("could not resolve what a Mattermost post replies to",
			"post", post.ID, "error", err)
	}

	uploads, err := b.fetchAttachments(ctx, post)
	if err != nil {
		return err
	}

	sent, err := b.tp.Send(ctx, conv.ID, replyToID, text, uploads)
	if err != nil {
		return b.reportSendFailure(ctx, post, conv, err)
	}

	if err := b.db.SaveMessage(ctx, b.tenant, storage.Message{
		ID:             sent.ID,
		PostID:         post.ID,
		ConversationID: conv.ID,
		Direction:      storage.DirectionOut,
		CreatedAt:      sent.Timestamp,
	}); err != nil {
		return fmt.Errorf("recording the outgoing message from post %s: %w", post.ID, err)
	}

	b.log.Info("sent a Mattermost post",
		"conversation", conv.ID, "post", post.ID, "message", sent.ID, "attachments", len(uploads))

	return nil
}

// conversationForPost finds the conversation a post is meant for.
func (b *Bridge) conversationForPost(ctx context.Context, post mattermost.Post) (storage.Conversation, error) {
	if b.cfg.ThreadPerConversation {
		return b.db.GetConversationByRootPost(ctx, b.tenant, post.ThreadRoot())
	}

	// Without threads, the channel itself identifies the conversation — and
	// only when exactly one is bridged into it. GetSoleConversationInChannel
	// refuses to guess otherwise.
	return b.db.GetSoleConversationInChannel(ctx, b.tenant, post.ChannelID)
}

// replyTarget maps a Mattermost reply onto the message it answers, so the
// far end shows it as a native reply where that concept exists.
//
// In thread mode every message in a conversation is a reply to the same
// root post, so the thread structure says nothing about which message is
// being answered; only a post that explicitly replies to another bridged
// post does.
func (b *Bridge) replyTarget(ctx context.Context, post mattermost.Post, conv storage.Conversation) (string, error) {
	if post.RootID == "" || post.RootID == conv.RootPostID {
		return "", nil
	}
	msg, err := b.messageForPost(ctx, post.RootID)
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// messageForPost is the reverse mapping, post to message.
func (b *Bridge) messageForPost(ctx context.Context, postID string) (storage.Message, error) {
	return b.db.GetMessageByPost(ctx, b.tenant, postID)
}

// fetchAttachments downloads a post's files from Mattermost so they can be
// uploaded to the transport.
func (b *Bridge) fetchAttachments(ctx context.Context, post mattermost.Post) ([]transport.Upload, error) {
	uploads := make([]transport.Upload, 0, len(post.FileIDs))

	for _, fileID := range post.FileIDs {
		info, err := b.mm.FileInfo(ctx, fileID)
		if err != nil {
			return nil, err
		}
		data, err := b.mm.Download(ctx, fileID)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, transport.Upload{
			Name:     info.Name,
			MimeType: info.MimeType,
			Data:     data,
		})
	}

	return uploads, nil
}

// reportSendFailure tells the operator, in the thread where they typed it,
// that a message did not go out. A failure that only appears in the
// daemon's log is a message the sender believes was delivered.
func (b *Bridge) reportSendFailure(ctx context.Context, post mattermost.Post, conv storage.Conversation, sendErr error) error {
	b.log.Error("could not send a Mattermost post",
		"conversation", conv.ID, "post", post.ID, "error", sendErr)

	if _, err := b.mm.Post(ctx, mattermost.NewPost{
		ChannelID: post.ChannelID,
		RootID:    post.ThreadRoot(),
		Message:   fmt.Sprintf("⚠️ This message was not sent: %s", sendErr),
	}); err != nil {
		b.log.Warn("could not report a send failure in Mattermost", "post", post.ID, "error", err)
	}

	// The failure has been reported where it matters; returning it as well
	// would only log it twice.
	return nil
}
