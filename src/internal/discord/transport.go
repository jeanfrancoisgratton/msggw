// msggw
// Written by J.F. Gratton <jean-francois@famillegratton.net>
// Original filename: src/internal/discord/transport.go

package discord

import (
	"context"

	"msggw/internal/transport"
)

// transportAdapter satisfies transport.Transport by delegating to Client and
// converting its own types to/from the normalized ones. It exists as a
// separate, small file rather than a change to client.go/events.go/types.go
// so that package discord's own reviewed, working shape is untouched by the
// Transport interface's existence — Discord genuinely needs nothing from it
// beyond a translation layer, since it implements none of the optional
// capability interfaces in internal/transport (see docs/discord-adapter-design.md
// for why: no async send, no delivery status, no backfill, no read receipts).
type transportAdapter struct {
	*Client
}

// AsTransport exposes c as a transport.Transport.
func (c *Client) AsTransport() transport.Transport { return transportAdapter{c} }

func (a transportAdapter) Events() <-chan transport.Event {
	out := make(chan transport.Event)
	go func() {
		defer close(out)
		for evt := range a.Client.Events() {
			out <- convertEvent(evt)
		}
	}()
	return out
}

func (a transportAdapter) Send(ctx context.Context, conversationID, replyToID, text string, uploads []transport.Upload) (transport.Message, error) {
	discordUploads := make([]Upload, len(uploads))
	for i, u := range uploads {
		discordUploads[i] = Upload{Name: u.Name, MimeType: u.MimeType, Data: u.Data}
	}
	msg, err := a.Client.Send(ctx, conversationID, replyToID, text, discordUploads)
	if err != nil {
		return transport.Message{}, err
	}
	return toTransportMessage(msg), nil
}

func (a transportAdapter) Download(ctx context.Context, att transport.Attachment) ([]byte, error) {
	return a.Client.Download(ctx, Attachment{ID: att.ID, Name: att.Name, MimeType: att.MimeType, URL: att.URL, Size: att.Size})
}

func (a transportAdapter) Conversation(ctx context.Context, conversationID string) (transport.Conversation, error) {
	conv, err := a.Client.Conversation(ctx, conversationID)
	if err != nil {
		return transport.Conversation{}, err
	}
	return toTransportConversation(conv), nil
}

func convertEvent(evt Event) transport.Event {
	switch e := evt.(type) {
	case ReadyEvent:
		return transport.ReadyEvent{UserID: e.UserID, Username: e.Username}
	case MessageEvent:
		return transport.MessageEvent{Message: toTransportMessage(e.Message)}
	case ConnectionEvent:
		return transport.ConnectionEvent{State: e.State, Err: e.Err}
	default:
		// Exhaustive over discord.Event's only implementations (see
		// events.go); unreachable in practice.
		return transport.ConnectionEvent{State: transport.ConnConnected}
	}
}

func toTransportParticipant(p Participant) transport.Participant {
	return transport.Participant{ID: p.ID, DisplayName: p.DisplayName, IsMe: p.IsMe}
}

func toTransportConversation(c Conversation) transport.Conversation {
	participants := make([]transport.Participant, len(c.Participants))
	for i, p := range c.Participants {
		participants[i] = toTransportParticipant(p)
	}
	return transport.Conversation{
		ID:           c.ID,
		Name:         c.Name,
		IsGroup:      c.IsGroup,
		GuildID:      c.GuildID,
		Participants: participants,
	}
}

func toTransportAttachment(a Attachment) transport.Attachment {
	return transport.Attachment{ID: a.ID, Name: a.Name, MimeType: a.MimeType, URL: a.URL, Size: a.Size}
}

func toTransportMessage(m Message) transport.Message {
	attachments := make([]transport.Attachment, len(m.Attachments))
	for i, a := range m.Attachments {
		attachments[i] = toTransportAttachment(a)
	}
	return transport.Message{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		GuildID:        m.GuildID,
		SenderID:       m.AuthorID,
		SenderName:     m.AuthorName,
		IsFromMe:       m.IsFromMe,
		Timestamp:      m.Timestamp,
		Text:           m.Text,
		Attachments:    attachments,
		ReplyToID:      m.ReplyToID,
	}
}
