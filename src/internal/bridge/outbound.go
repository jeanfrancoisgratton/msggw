// rcs_gateway
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:07:21
// Original filename: src/internal/bridge/outbound.go

package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rcs_gateway/internal/gmessages"
	"rcs_gateway/internal/mattermost"
	"rcs_gateway/internal/storage"
)

// handleOutgoingPost sends a Mattermost post to the phone.
//
// Posts made by the bridge itself never reach this: the Mattermost client
// filters them out on the way in.
func (b *Bridge) handleOutgoingPost(ctx context.Context, post mattermost.Post) error {
	conv, err := b.conversationForPost(ctx, post)
	if errors.Is(err, storage.ErrNotFound) {
		// A post in a channel the bridge uses, but outside any thread it owns.
		// People do talk in these channels; that is not an error.
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

	var result gmessages.SendResult
	if len(post.FileIDs) > 0 {
		attachments, err := b.fetchAttachments(ctx, post)
		if err != nil {
			return err
		}
		result, err = b.gm.SendMedia(ctx, conv.ID, replyToID, text, attachments)
		if err != nil {
			return b.reportSendFailure(ctx, post, conv, err)
		}
	} else {
		result, err = b.gm.SendReply(ctx, conv.ID, replyToID, text)
		if err != nil {
			return b.reportSendFailure(ctx, post, conv, err)
		}
	}

	// The phone will echo this message back with a real ID; the temporary ID
	// is what will let that echo be recognised as ours rather than posted
	// again.
	if err := b.db.AddPendingOutbound(ctx, result.TmpID, post.ID, conv.ID); err != nil {
		return fmt.Errorf("recording the outgoing message from post %s: %w", post.ID, err)
	}

	b.log.Info("sent a Mattermost post to the phone",
		"conversation", conv.ID, "post", post.ID, "tmp_id", result.TmpID,
		"attachments", len(post.FileIDs))

	return nil
}

// conversationForPost finds the Google Messages conversation a post is meant
// for.
func (b *Bridge) conversationForPost(ctx context.Context, post mattermost.Post) (storage.Conversation, error) {
	if b.threadMode {
		conv, err := b.db.GetConversationByRootPost(ctx, post.ThreadRoot())
		if err != nil {
			return storage.Conversation{}, err
		}
		return conv, nil
	}

	// Without threads, the channel itself identifies the conversation — and
	// only when exactly one is bridged into it. GetSoleConversationInChannel
	// refuses to guess otherwise.
	conv, err := b.db.GetSoleConversationInChannel(ctx, post.ChannelID)
	if err != nil {
		return storage.Conversation{}, err
	}
	return conv, nil
}

// replyTarget maps a Mattermost reply onto the Google Messages message it
// answers, so that RCS shows it as a quoted reply.
//
// In thread mode every message in a conversation is a reply to the same root
// post, so the thread structure says nothing about which message is being
// answered; only a post that explicitly replies to another bridged post does.
func (b *Bridge) replyTarget(ctx context.Context, post mattermost.Post, conv storage.Conversation) (string, error) {
	if post.RootID == "" || post.RootID == conv.RootPostID {
		return "", nil
	}
	// Mattermost collapses nested replies onto the thread root, so a reply to
	// a specific message only arrives with RootId set to that message when the
	// thread is not the conversation's own.
	msg, err := b.messageForPost(ctx, post.RootID)
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// messageForPost is the reverse mapping, post to Google Messages message.
func (b *Bridge) messageForPost(ctx context.Context, postID string) (storage.Message, error) {
	// The mapping table is keyed by message ID, so this is a scan of one
	// index rather than a lookup; there is no reverse index because this path
	// is rare and the table is small.
	return b.db.GetMessageByPost(ctx, postID)
}

// fetchAttachments downloads a post's files from Mattermost so they can be
// uploaded to Google Messages.
func (b *Bridge) fetchAttachments(ctx context.Context, post mattermost.Post) ([]gmessages.Attachment, error) {
	attachments := make([]gmessages.Attachment, 0, len(post.FileIDs))

	for _, fileID := range post.FileIDs {
		info, err := b.mm.FileInfo(ctx, fileID)
		if err != nil {
			return nil, err
		}
		data, err := b.mm.Download(ctx, fileID)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, gmessages.Attachment{
			Name:     info.Name,
			MimeType: info.MimeType,
			Data:     data,
		})
	}

	return attachments, nil
}

// reportSendFailure tells the operator, in the thread where they typed it,
// that a message did not go out. A failure that only appears in the daemon's
// log is a message the sender believes was delivered.
func (b *Bridge) reportSendFailure(ctx context.Context, post mattermost.Post, conv storage.Conversation, sendErr error) error {
	b.log.Error("could not send a Mattermost post to the phone",
		"conversation", conv.ID, "post", post.ID, "error", sendErr)

	if b.cfg.Routing.PostDeliveryStatus {
		if err := b.mm.React(ctx, post.ID, emojiFailed); err != nil {
			b.log.Warn("could not mark a failed post", "post", post.ID, "error", err)
		}
	}

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
