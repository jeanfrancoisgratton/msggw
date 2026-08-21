// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original timestamp: 2026.08.16 20:00:59
// Original filename: src/internal/gmessages/messages.go

package gmessages

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
)

// defaultConversationCount is how many conversations to ask for. The phone
// returns its most recent ones; a person's active thread count is well under
// this.
const defaultConversationCount = 100

// ListConversations returns the inbox, most recent first, and refreshes the
// conversation cache from it.
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	resp, err := c.gm.ListConversations(ctx, defaultConversationCount,
		gmproto.ListConversationsRequest_INBOX)
	if err != nil {
		return nil, fmt.Errorf("listing conversations: %w", err)
	}

	out := make([]Conversation, 0, len(resp.GetConversations()))
	for _, raw := range resp.GetConversations() {
		c.cacheConversation(raw)
		out = append(out, convertConversation(raw))
	}
	return out, nil
}

// GetConversation returns one conversation, from cache when possible.
func (c *Client) GetConversation(ctx context.Context, id string) (Conversation, error) {
	raw, err := c.rawConversation(ctx, id)
	if err != nil {
		return Conversation{}, err
	}
	return convertConversation(raw), nil
}

// FetchMessages returns the most recent count messages of a conversation,
// newest first. It backs the optional backfill of a newly bridged thread.
func (c *Client) FetchMessages(ctx context.Context, conversationID string, count int) ([]Message, error) {
	resp, err := c.gm.FetchMessages(ctx, conversationID, int64(count), nil)
	if err != nil {
		return nil, fmt.Errorf("fetching messages of %s: %w", conversationID, err)
	}

	conv, _ := c.conversation(conversationID)
	out := make([]Message, 0, len(resp.GetMessages()))
	for _, raw := range resp.GetMessages() {
		out = append(out, convertMessage(raw, true, conv))
	}
	return out, nil
}

// SendResult reports what happened to an outgoing message.
type SendResult struct {
	// TmpID is the temporary ID this message was sent under. Google Messages
	// does not return the final message ID: it arrives later as an ordinary
	// message event carrying this TmpID, which is how the two are tied
	// together.
	TmpID string
}

// SendText sends a text message.
func (c *Client) SendText(ctx context.Context, conversationID, text string) (SendResult, error) {
	return c.send(ctx, conversationID, "", []*gmproto.MessageInfo{{
		Data: &gmproto.MessageInfo_MessageContent{
			MessageContent: &gmproto.MessageContent{Content: text},
		},
	}})
}

// SendReply sends a text message as an RCS reply to replyToID.
func (c *Client) SendReply(ctx context.Context, conversationID, replyToID, text string) (SendResult, error) {
	return c.send(ctx, conversationID, replyToID, []*gmproto.MessageInfo{{
		Data: &gmproto.MessageInfo_MessageContent{
			MessageContent: &gmproto.MessageContent{Content: text},
		},
	}})
}

// Attachment is a file on its way to the phone.
type Attachment struct {
	Name     string
	MimeType string
	Data     []byte
}

// SendMedia uploads attachments and sends them, with text as an accompanying
// caption when it is not empty.
func (c *Client) SendMedia(ctx context.Context, conversationID, replyToID, text string, attachments []Attachment) (SendResult, error) {
	if len(attachments) == 0 {
		return SendResult{}, fmt.Errorf("no attachments to send")
	}

	info := make([]*gmproto.MessageInfo, 0, len(attachments)+1)
	for _, att := range attachments {
		uploaded, err := c.gm.UploadMedia(att.Data, att.Name, att.MimeType)
		if err != nil {
			return SendResult{}, fmt.Errorf("uploading %s to Google Messages: %w", att.Name, err)
		}
		info = append(info, &gmproto.MessageInfo{
			Data: &gmproto.MessageInfo_MediaContent{MediaContent: uploaded},
		})
	}
	if text != "" {
		info = append(info, &gmproto.MessageInfo{
			Data: &gmproto.MessageInfo_MessageContent{
				MessageContent: &gmproto.MessageContent{Content: text},
			},
		})
	}

	return c.send(ctx, conversationID, replyToID, info)
}

func (c *Client) send(ctx context.Context, conversationID, replyToID string, info []*gmproto.MessageInfo) (SendResult, error) {
	conv, err := c.rawConversation(ctx, conversationID)
	if err != nil {
		return SendResult{}, err
	}

	tmpID := uuid.NewString()
	req := &gmproto.SendMessageRequest{
		ConversationID: conversationID,
		MessagePayload: &gmproto.MessagePayload{
			TmpID:          tmpID,
			TmpID2:         tmpID,
			ConversationID: conversationID,
			ParticipantID:  conv.GetDefaultOutgoingID(),
			MessageInfo:    info,
		},
		SIMPayload: c.simPayload(conv),
		TmpID:      tmpID,
		// Forcing RCS only makes sense on a conversation that is already RCS
		// and has not been latched to SMS; asking for it elsewhere would make
		// the phone reject the send rather than fall back.
		ForceRCS: c.cfg.ForceRCS &&
			conv.GetType() == gmproto.ConversationType_RCS &&
			conv.GetSendMode() == gmproto.ConversationSendMode_SEND_MODE_AUTO,
	}
	if replyToID != "" {
		req.Reply = &gmproto.ReplyPayload{MessageID: replyToID}
	}

	resp, err := c.gm.SendMessage(ctx, req)
	if err != nil {
		return SendResult{}, fmt.Errorf("sending to %s: %w", conversationID, err)
	}
	if status := resp.GetStatus(); status != gmproto.SendMessageResponse_SUCCESS {
		// FAILURE_4 is the phone saying Google Messages is not its default SMS
		// app, which is a configuration problem worth naming outright.
		if status == gmproto.SendMessageResponse_FAILURE_4 {
			return SendResult{}, fmt.Errorf("the phone refused the message: Google Messages is probably not its default SMS app")
		}
		return SendResult{}, fmt.Errorf("the phone refused the message: %s", status)
	}

	return SendResult{TmpID: tmpID}, nil
}

// StartConversation finds or creates the conversation with a set of phone
// numbers, so that a Mattermost user can start a thread with someone the
// bridge has never seen.
func (c *Client) StartConversation(ctx context.Context, numbers []string, groupName string) (Conversation, error) {
	if len(numbers) == 0 {
		return Conversation{}, fmt.Errorf("no phone number given")
	}

	contacts := make([]*gmproto.ContactNumber, 0, len(numbers))
	for _, number := range numbers {
		contacts = append(contacts, &gmproto.ContactNumber{
			// 2 is what the web client sends for a number picked from
			// contacts; 7 is for one typed by hand. 2 works for both.
			MysteriousInt: 2,
			Number:        number,
			Number2:       number,
		})
	}

	req := &gmproto.GetOrCreateConversationRequest{Numbers: contacts}
	if groupName != "" {
		createGroup := true
		req.RCSGroupName = &groupName
		req.CreateRCSGroup = &createGroup
	}

	resp, err := c.gm.GetOrCreateConversation(ctx, req)
	if err != nil {
		return Conversation{}, fmt.Errorf("starting a conversation: %w", err)
	}
	if resp.GetStatus() == gmproto.GetOrCreateConversationResponse_CREATE_RCS {
		return Conversation{}, fmt.Errorf("the phone wants this to be created as an RCS group: pass a group name")
	}
	if resp.GetConversation() == nil {
		return Conversation{}, fmt.Errorf("the phone returned no conversation for %v", numbers)
	}

	c.cacheConversation(resp.GetConversation())
	return convertConversation(resp.GetConversation()), nil
}

// MarkRead marks a conversation read on the phone up to messageID.
func (c *Client) MarkRead(ctx context.Context, conversationID, messageID string) error {
	if err := c.gm.MarkRead(ctx, conversationID, messageID); err != nil {
		return fmt.Errorf("marking %s read: %w", conversationID, err)
	}
	return nil
}

// SetTyping tells the phone to show a typing indicator in a conversation.
func (c *Client) SetTyping(ctx context.Context, conversationID string) error {
	conv, err := c.rawConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if err := c.gm.SetTyping(ctx, conversationID, c.simPayload(conv)); err != nil {
		return fmt.Errorf("setting typing state on %s: %w", conversationID, err)
	}
	return nil
}
